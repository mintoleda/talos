package acp

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/mintoleda/talos/internal/config"
	"github.com/mintoleda/talos/internal/models"
	"github.com/mintoleda/talos/internal/provider"
	"github.com/mintoleda/talos/internal/safety"
)

const testWait = 15 * time.Second

// ---------------------------------------------------------------------------
// Environment isolation
// ---------------------------------------------------------------------------

// isolate points every piece of on-disk state at temp dirs and clears every
// provider key so no real network model listing or pricing refresh can occur.
// It returns the fake HOME and the Talos base dir.
func isolate(t *testing.T) (home, baseDir string) {
	t.Helper()
	home = t.TempDir()
	baseDir = filepath.Join(home, ".talos")
	if err := os.MkdirAll(baseDir, 0o755); err != nil {
		t.Fatal(err)
	}
	// A fresh pricing cache stops pricing.Load from refreshing it over the network.
	if err := os.WriteFile(filepath.Join(baseDir, "pricing-cache.json"), []byte("{}"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", home)
	t.Setenv("TALOS_HOME", baseDir)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("XDG_DATA_HOME", filepath.Join(home, ".local", "share"))
	t.Setenv("XDG_STATE_HOME", filepath.Join(home, ".local", "state"))
	for _, k := range provider.All {
		if k.EnvVar != "" { // OAuth providers have no env var
			t.Setenv(k.EnvVar, "")
		}
	}
	return home, baseDir
}

func transcripts(t *testing.T, home string) []string {
	t.Helper()
	var out []string
	_ = filepath.Walk(filepath.Join(home, ".talos", "sessions"), func(p string, info os.FileInfo, err error) error {
		if err == nil && !info.IsDir() && strings.HasSuffix(p, ".jsonl") {
			out = append(out, p)
		}
		return nil
	})
	return out
}

// ---------------------------------------------------------------------------
// Fake OpenAI-compatible streaming API
// ---------------------------------------------------------------------------

type fakeLLM struct {
	srv    *httptest.Server
	mu     sync.Mutex
	reqs   []map[string]any
	handle func(n int, w http.ResponseWriter, r *http.Request)
}

func newFakeLLM(t *testing.T, handle func(n int, w http.ResponseWriter, r *http.Request)) *fakeLLM {
	t.Helper()
	f := &fakeLLM{handle: handle}
	f.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/chat/completions" || r.Method != http.MethodPost {
			http.NotFound(w, r)
			return
		}
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		f.mu.Lock()
		f.reqs = append(f.reqs, body)
		n := len(f.reqs) - 1
		f.mu.Unlock()
		f.handle(n, w, r)
	}))
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakeLLM) requests() []map[string]any {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]map[string]any(nil), f.reqs...)
}

func sseStart(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "text/event-stream")
	w.WriteHeader(http.StatusOK)
	if fl, ok := w.(http.Flusher); ok {
		fl.Flush()
	}
}

func sseData(w http.ResponseWriter, v any) {
	b, _ := json.Marshal(v)
	fmt.Fprintf(w, "data: %s\n\n", b)
	if fl, ok := w.(http.Flusher); ok {
		fl.Flush()
	}
}

func sseDone(w http.ResponseWriter, finish string) {
	sseData(w, map[string]any{"choices": []any{map[string]any{"delta": map[string]any{}, "finish_reason": finish}}})
	sseData(w, map[string]any{"choices": []any{}, "usage": map[string]any{"prompt_tokens": 12, "completion_tokens": 3}})
	fmt.Fprint(w, "data: [DONE]\n\n")
}

func sseTextChunk(w http.ResponseWriter, text string) {
	sseData(w, map[string]any{"choices": []any{map[string]any{"delta": map[string]any{"content": text}}}})
}

// replyText streams text as one SSE chunk per part, then finishes the turn.
func replyText(w http.ResponseWriter, parts ...string) {
	sseStart(w)
	for _, p := range parts {
		sseTextChunk(w, p)
	}
	sseDone(w, "stop")
}

// replyToolCall streams a single tool call whose arguments are JSON-encoded
// from args, split across two chunks like a real provider.
func replyToolCall(w http.ResponseWriter, id, name string, args map[string]any) {
	raw, _ := json.Marshal(args)
	half := len(raw) / 2
	sseStart(w)
	call := func(idx int, id, name, frag string) {
		sseData(w, map[string]any{"choices": []any{map[string]any{"delta": map[string]any{"tool_calls": []any{
			map[string]any{"index": idx, "id": id, "type": "function", "function": map[string]any{"name": name, "arguments": frag}},
		}}}}})
	}
	call(0, id, name, string(raw[:half]))
	call(0, "", "", string(raw[half:]))
	sseDone(w, "tool_calls")
}

// ---------------------------------------------------------------------------
// Fake ACP client
// ---------------------------------------------------------------------------

type rpcResp struct {
	Result json.RawMessage
	Error  *RPCError
	// updatesBefore is how many session/update notifications had been
	// received when this response arrived (the agent writes in order).
	updatesBefore int
}

type update map[string]any

func (u update) kind() string { s, _ := u["sessionUpdate"].(string); return s }

func (u update) str(k string) string { s, _ := u[k].(string); return s }

type fakeClient struct {
	t  *testing.T
	cw *io.PipeWriter // client -> agent
	ar *io.PipeReader // agent -> client
	aw *io.PipeWriter

	wmu    sync.Mutex
	mu     sync.Mutex
	nextID int
	wait   map[string]chan rpcResp
	ups    []update
	perms  []RequestPermissionRequest

	// onRequest answers agent->client requests (session/request_permission).
	onRequest func(method string, params json.RawMessage) any

	served chan error
	stopFn func()
}

// startAgent runs Serve against a fake client. The agent is shut down (EOF on
// its input) by t.Cleanup, or earlier via stop().
func startAgent(t *testing.T, cfg *config.Config) *fakeClient {
	t.Helper()
	cr, cw := io.Pipe()
	ar, aw := io.Pipe()
	c := &fakeClient{t: t, cw: cw, ar: ar, aw: aw, wait: map[string]chan rpcResp{}, served: make(chan error, 1)}
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		c.served <- Serve(ctx, cfg, cr, aw)
		aw.Close()
	}()
	go c.readLoop()
	var once sync.Once
	t.Cleanup(func() { once.Do(func() { c.stopWith(cancel) }) })
	c.stopFn = func() { once.Do(func() { c.stopWith(cancel) }) }
	return c
}

func (c *fakeClient) stop() { c.stopFn() }

func (c *fakeClient) stopWith(cancel context.CancelFunc) {
	c.cw.Close()
	select {
	case <-c.served:
	case <-time.After(testWait):
		c.t.Error("Serve did not return after EOF")
	}
	cancel()
	c.ar.Close()
}

func (c *fakeClient) writeLine(v any) {
	b, err := json.Marshal(v)
	if err != nil {
		c.t.Fatal(err)
	}
	c.wmu.Lock()
	defer c.wmu.Unlock()
	if _, err := c.cw.Write(append(b, '\n')); err != nil {
		c.t.Errorf("client write: %v", err)
	}
}

func (c *fakeClient) readLoop() {
	sc := bufio.NewScanner(c.ar)
	sc.Buffer(make([]byte, 0, 1<<20), 64<<20)
	for sc.Scan() {
		var m struct {
			ID     json.RawMessage `json:"id"`
			Method string          `json:"method"`
			Params json.RawMessage `json:"params"`
			Result json.RawMessage `json:"result"`
			Error  *RPCError       `json:"error"`
		}
		if err := json.Unmarshal(sc.Bytes(), &m); err != nil {
			c.t.Errorf("bad line from agent %q: %v", sc.Text(), err)
			continue
		}
		switch {
		case m.Method == "session/update":
			var n struct {
				SessionID string `json:"sessionId"`
				Update    update `json:"update"`
			}
			if err := json.Unmarshal(m.Params, &n); err != nil {
				c.t.Errorf("bad session/update: %v", err)
				continue
			}
			c.mu.Lock()
			c.ups = append(c.ups, n.Update)
			c.mu.Unlock()
		case m.Method != "" && len(m.ID) > 0:
			go c.answer(m.ID, m.Method, m.Params)
		case len(m.ID) > 0:
			c.mu.Lock()
			ch := c.wait[string(m.ID)]
			delete(c.wait, string(m.ID))
			n := len(c.ups)
			c.mu.Unlock()
			if ch != nil {
				ch <- rpcResp{Result: m.Result, Error: m.Error, updatesBefore: n}
			}
		}
	}
}

func (c *fakeClient) answer(id json.RawMessage, method string, params json.RawMessage) {
	if method == "session/request_permission" {
		var req RequestPermissionRequest
		if err := json.Unmarshal(params, &req); err != nil {
			c.t.Errorf("bad permission request: %v", err)
		}
		c.mu.Lock()
		c.perms = append(c.perms, req)
		c.mu.Unlock()
	}
	var result any = map[string]any{}
	if c.onRequest != nil {
		result = c.onRequest(method, params)
	}
	c.writeLine(map[string]any{"jsonrpc": "2.0", "id": id, "result": result})
}

// send issues a request and returns a channel for its response.
func (c *fakeClient) send(method string, params any) <-chan rpcResp {
	c.mu.Lock()
	c.nextID++
	id := c.nextID
	ch := make(chan rpcResp, 1)
	c.wait[fmt.Sprint(id)] = ch
	c.mu.Unlock()
	c.writeLine(map[string]any{"jsonrpc": "2.0", "id": id, "method": method, "params": params})
	return ch
}

func (c *fakeClient) recv(ch <-chan rpcResp) rpcResp {
	c.t.Helper()
	select {
	case r := <-ch:
		return r
	case <-time.After(testWait):
		c.t.Fatal("timed out waiting for response")
		return rpcResp{}
	}
}

// call sends a request and requires success.
func (c *fakeClient) call(method string, params any) rpcResp {
	c.t.Helper()
	r := c.recv(c.send(method, params))
	if r.Error != nil {
		c.t.Fatalf("%s: %v", method, r.Error)
	}
	return r
}

func (c *fakeClient) notify(method string, params any) {
	c.writeLine(map[string]any{"jsonrpc": "2.0", "method": method, "params": params})
}

func (c *fakeClient) updates() []update {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]update(nil), c.ups...)
}

func (c *fakeClient) permissions() []RequestPermissionRequest {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]RequestPermissionRequest(nil), c.perms...)
}

// waitUpdate blocks until an update matching pred has been received.
func (c *fakeClient) waitUpdate(desc string, pred func(update) bool) update {
	c.t.Helper()
	deadline := time.Now().Add(testWait)
	for time.Now().Before(deadline) {
		for _, u := range c.updates() {
			if pred(u) {
				return u
			}
		}
		time.Sleep(5 * time.Millisecond)
	}
	c.t.Fatalf("timed out waiting for update: %s; got %s", desc, dumpUpdates(c.updates()))
	return nil
}

func dumpUpdates(us []update) string {
	var parts []string
	for _, u := range us {
		b, _ := json.Marshal(u)
		parts = append(parts, string(b))
	}
	return strings.Join(parts, "\n")
}

func kindIs(k string) func(update) bool { return func(u update) bool { return u.kind() == k } }

func filterKind(us []update, k string) []update {
	var out []update
	for _, u := range us {
		if u.kind() == k {
			out = append(out, u)
		}
	}
	return out
}

// messageText concatenates the text of message-chunk updates of one kind.
func messageText(us []update, k string) string {
	var sb strings.Builder
	for _, u := range filterKind(us, k) {
		content, _ := u["content"].(map[string]any)
		s, _ := content["text"].(string)
		sb.WriteString(s)
	}
	return sb.String()
}

func decode[T any](t *testing.T, raw json.RawMessage) T {
	t.Helper()
	var v T
	if err := json.Unmarshal(raw, &v); err != nil {
		t.Fatalf("decode %s: %v", raw, err)
	}
	return v
}

// ---------------------------------------------------------------------------
// Test scaffolding
// ---------------------------------------------------------------------------

type env struct {
	home string
	cwd  string
	cfg  *config.Config
	llm  *fakeLLM
	c    *fakeClient
}

func testConfig(baseDir, url string) *config.Config {
	return &config.Config{
		BaseURL:                   url,
		APIKey:                    "dummy",
		Model:                     "test-model",
		Provider:                  "openai",
		PermissionMode:            safety.ModeAuto,
		BashTimeout:               20 * time.Second,
		BashMaxTimeout:            30 * time.Second,
		BashMaxOutput:             30 * 1024,
		SystemPrompt:              "You are a test agent.",
		BaseDir:                   baseDir,
		ThinkingLevel:             "off",
		CompactThreshold:          0.85,
		CompactEmergencyThreshold: 0.95,
		CompactChunkSize:          20,
	}
}

// setup isolates state, starts the fake LLM and an agent, and initializes it.
func setup(t *testing.T, handle func(n int, w http.ResponseWriter, r *http.Request)) *env {
	t.Helper()
	home, baseDir := isolate(t)
	e := &env{home: home, cwd: t.TempDir()}
	e.llm = newFakeLLM(t, handle)
	e.cfg = testConfig(baseDir, e.llm.srv.URL)
	e.c = startAgent(t, e.cfg)
	e.c.call("initialize", InitializeRequest{ProtocolVersion: 1})
	return e
}

func (e *env) newSession(t *testing.T) NewSessionResponse {
	t.Helper()
	r := e.c.call("session/new", NewSessionRequest{CWD: e.cwd, MCPServers: []MCPServer{}})
	return decode[NewSessionResponse](t, r.Result)
}

func (e *env) prompt(t *testing.T, sid, text string) PromptResponse {
	t.Helper()
	r := e.c.call("session/prompt", PromptRequest{SessionID: sid, Prompt: []ContentBlock{textContent(text)}})
	return decode[PromptResponse](t, r.Result)
}

// ---------------------------------------------------------------------------
// Tests
// ---------------------------------------------------------------------------

func TestInitializeCapabilities(t *testing.T) {
	e := setup(t, func(int, http.ResponseWriter, *http.Request) {})
	// setup already initialized; do it again to inspect the response.
	r := e.c.call("initialize", InitializeRequest{ProtocolVersion: 1})
	resp := decode[InitializeResponse](t, r.Result)
	if resp.ProtocolVersion != ProtocolVersion {
		t.Errorf("protocolVersion = %d", resp.ProtocolVersion)
	}
	if !resp.AgentCapabilities.LoadSession {
		t.Error("loadSession = false")
	}
	if !resp.AgentCapabilities.MCPCapabilities.HTTP {
		t.Error("mcpCapabilities.http = false")
	}
	if resp.AgentCapabilities.Extensions["talos"] == nil {
		t.Error("Talos model-catalog extension was not advertised")
	}
	if resp.AgentInfo == nil || resp.AgentInfo.Name != "talos" {
		t.Errorf("agentInfo = %+v", resp.AgentInfo)
	}

	// Raw JSON shape: authMethods must be an array, not null.
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(r.Result, &raw); err != nil {
		t.Fatal(err)
	}
	if string(raw["authMethods"]) != "[]" {
		t.Errorf("authMethods = %s, want []", raw["authMethods"])
	}
}

func TestListModelsDoesNotCreateSessionOrTranscript(t *testing.T) {
	agent := &Agent{
		sessions: make(map[string]*acpSession),
		listModels: func(context.Context) []models.Entry {
			return nil
		},
	}
	if _, err := agent.handle(context.Background(), "talos/list_models", nil); err != nil {
		t.Fatal(err)
	}
	if len(agent.sessions) != 0 {
		t.Fatalf("model catalogue allocated %d ACP sessions", len(agent.sessions))
	}

	e := setup(t, func(int, http.ResponseWriter, *http.Request) {
		t.Error("model discovery must not call the LLM")
	})
	result := e.c.call("talos/list_models", map[string]any{})
	response := decode[ModelCatalogResponse](t, result.Result)
	if response.Models == nil {
		t.Fatal("model catalogue must be an empty or populated array, not null")
	}
	if files := transcripts(t, e.home); len(files) != 0 {
		t.Fatalf("model discovery created transcripts: %v", files)
	}
}

func TestUnknownMethodAndSession(t *testing.T) {
	e := setup(t, func(int, http.ResponseWriter, *http.Request) {})
	r := e.c.recv(e.c.send("bogus/method", map[string]any{}))
	if r.Error == nil || r.Error.Code != codeMethodNotFound {
		t.Errorf("bogus method error = %+v", r.Error)
	}
	r = e.c.recv(e.c.send("session/prompt", PromptRequest{SessionID: "nope", Prompt: []ContentBlock{textContent("x")}}))
	if r.Error == nil || r.Error.Code != codeInvalidParams {
		t.Errorf("unknown session error = %+v", r.Error)
	}
	r = e.c.recv(e.c.send("session/new", NewSessionRequest{CWD: filepath.Join(e.cwd, "missing")}))
	if r.Error == nil || r.Error.Code != codeInvalidParams {
		t.Errorf("bad cwd error = %+v", r.Error)
	}
}

func TestSessionNewStateAndLazyBuild(t *testing.T) {
	e := setup(t, func(int, http.ResponseWriter, *http.Request) {
		t.Error("no LLM request expected for session/new")
	})
	resp := e.newSession(t)
	if resp.SessionID == "" {
		t.Fatal("empty sessionId")
	}

	if resp.Modes != nil {
		t.Errorf("permission modes should not be advertised: %+v", resp.Modes)
	}

	if resp.Models == nil || resp.Models.CurrentModelID != "openai/test-model" {
		t.Fatalf("models = %+v", resp.Models)
	}
	found := false
	for _, m := range resp.Models.AvailableModels {
		found = found || m.ModelID == "openai/test-model"
	}
	if !found {
		t.Errorf("current model missing from availableModels: %+v", resp.Models.AvailableModels)
	}

	var thought *ConfigOption
	for i := range resp.ConfigOptions {
		if resp.ConfigOptions[i].ID == "thought_level" {
			thought = &resp.ConfigOptions[i]
		}
	}
	if thought == nil {
		t.Fatalf("no thought_level option in %+v", resp.ConfigOptions)
	}
	if thought.Category != "thought_level" || thought.Type != "select" {
		t.Errorf("thought_level option = %+v", *thought)
	}

	// available_commands_update follows the response and names the builtins.
	u := e.c.waitUpdate("available_commands_update", kindIs("available_commands_update"))
	cmds, _ := u["availableCommands"].([]any)
	names := map[string]bool{}
	for _, c := range cmds {
		names[c.(map[string]any)["name"].(string)] = true
	}
	for _, want := range []string{"compact", "model", "mcp", "subagents"} {
		if !names[want] {
			t.Errorf("missing command %q in %v", want, names)
		}
	}

	// Lazy build: no transcript until the session is actually used.
	if files := transcripts(t, e.home); len(files) != 0 {
		t.Errorf("session/new created transcripts: %v", files)
	}
	if n := len(e.llm.requests()); n != 0 {
		t.Errorf("session/new made %d LLM requests", n)
	}
}

func TestPromptStreamsText(t *testing.T) {
	e := setup(t, func(n int, w http.ResponseWriter, r *http.Request) {
		replyText(w, "Hel", "lo ", "world")
	})
	sid := e.newSession(t).SessionID
	resp := e.prompt(t, sid, "say hello")
	if resp.StopReason != StopEndTurn {
		t.Errorf("stopReason = %q", resp.StopReason)
	}
	if resp.Usage == nil || resp.Usage.InputTokens != 12 || resp.Usage.OutputTokens != 3 {
		t.Errorf("usage = %+v", resp.Usage)
	}

	ups := e.c.updates()
	chunks := filterKind(ups, "agent_message_chunk")
	if len(chunks) == 0 {
		t.Fatalf("no agent_message_chunk in %s", dumpUpdates(ups))
	}
	for _, u := range chunks {
		content, ok := u["content"].(map[string]any)
		if !ok || content["type"] != "text" {
			t.Errorf("message chunk content must be a text object, got %#v", u["content"])
		}
	}
	if got := messageText(ups, "agent_message_chunk"); got != "Hello world" {
		t.Errorf("agent text = %q", got)
	}

	reqs := e.llm.requests()
	if len(reqs) != 1 {
		t.Fatalf("LLM requests = %d", len(reqs))
	}
	if reqs[0]["model"] != "test-model" || reqs[0]["stream"] != true {
		t.Errorf("request model/stream = %v/%v", reqs[0]["model"], reqs[0]["stream"])
	}
	if b, _ := json.Marshal(reqs[0]["messages"]); !strings.Contains(string(b), "say hello") {
		t.Errorf("prompt text not forwarded: %s", b)
	}

	// The turn is persisted now.
	if files := transcripts(t, e.home); len(files) != 1 {
		t.Errorf("transcripts = %v", files)
	}
}

func TestToolCallRoundTrip(t *testing.T) {
	tests := []struct {
		name     string
		tool     string
		args     func(cwd string) map[string]any
		wantKind string
		wantOut  string // substring expected in the tool result sent back to the model
	}{
		{
			name:     "read",
			tool:     "read",
			args:     func(cwd string) map[string]any { return map[string]any{"path": filepath.Join(cwd, "note.txt")} },
			wantKind: "read",
			wantOut:  "file-body-xyz",
		},
		{
			name:     "bash",
			tool:     "bash",
			args:     func(string) map[string]any { return map[string]any{"command": "echo hi-from-bash"} },
			wantKind: "execute",
			wantOut:  "hi-from-bash",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var cwd string
			e := setup(t, func(n int, w http.ResponseWriter, r *http.Request) {
				if n == 0 {
					replyToolCall(w, "call_1", tc.tool, tc.args(cwd))
					return
				}
				replyText(w, "all done")
			})
			cwd = e.cwd
			if err := os.WriteFile(filepath.Join(e.cwd, "note.txt"), []byte("file-body-xyz\n"), 0o644); err != nil {
				t.Fatal(err)
			}
			sid := e.newSession(t).SessionID
			resp := e.prompt(t, sid, "do the thing")
			if resp.StopReason != StopEndTurn {
				t.Errorf("stopReason = %q", resp.StopReason)
			}

			ups := e.c.updates()
			calls := filterKind(ups, "tool_call")
			if len(calls) != 1 {
				t.Fatalf("tool_call updates = %d in %s", len(calls), dumpUpdates(ups))
			}
			start := calls[0]
			if start.str("toolCallId") != "call_1" || start.str("kind") != tc.wantKind || start.str("status") != ToolInProgress {
				t.Errorf("tool_call = %v", start)
			}
			if !strings.HasPrefix(start.str("title"), tc.tool+": ") {
				t.Errorf("title = %q", start.str("title"))
			}
			if _, ok := start["rawInput"].(map[string]any); !ok {
				t.Errorf("rawInput = %#v", start["rawInput"])
			}

			var done update
			for _, u := range filterKind(ups, "tool_call_update") {
				if u.str("status") == ToolCompleted {
					done = u
				}
			}
			if done == nil {
				t.Fatalf("no completed tool_call_update in %s", dumpUpdates(ups))
			}
			if done.str("toolCallId") != "call_1" {
				t.Errorf("update toolCallId = %q", done.str("toolCallId"))
			}
			arr, ok := done["content"].([]any)
			if !ok || len(arr) == 0 {
				t.Fatalf("tool_call_update content must be a non-empty array, got %#v", done["content"])
			}
			first := arr[0].(map[string]any)
			inner, _ := first["content"].(map[string]any)
			if first["type"] != "content" || !strings.Contains(fmt.Sprint(inner["text"]), tc.wantOut) {
				t.Errorf("content[0] = %#v, want text containing %q", first, tc.wantOut)
			}

			// Ordering: start before completion, final text after.
			var iStart, iDone, iText = -1, -1, -1
			for i, u := range ups {
				switch {
				case u.kind() == "tool_call" && iStart < 0:
					iStart = i
				case u.kind() == "tool_call_update" && u.str("status") == ToolCompleted && iDone < 0:
					iDone = i
				case u.kind() == "agent_message_chunk":
					iText = i
				}
			}
			if !(iStart < iDone && iDone < iText) {
				t.Errorf("bad ordering start=%d done=%d text=%d", iStart, iDone, iText)
			}
			if got := messageText(ups, "agent_message_chunk"); got != "all done" {
				t.Errorf("final text = %q", got)
			}

			// The second LLM request carries the tool result.
			reqs := e.llm.requests()
			if len(reqs) != 2 {
				t.Fatalf("LLM requests = %d", len(reqs))
			}
			b, _ := json.Marshal(reqs[1]["messages"])
			if !strings.Contains(string(b), tc.wantOut) || !strings.Contains(string(b), "call_1") {
				t.Errorf("tool result missing from second request: %s", b)
			}
		})
	}
}

// promptCommand is classified Prompt (chmod -R) but is harmless: it operates
// on a directory created by the test, and leaves a marker file when it runs.
func promptCommand(dir string) string {
	return fmt.Sprintf("chmod -R u+rw %q && touch %q", dir, filepath.Join(dir, "ran.marker"))
}

func TestPromptCommandIsClassifiedPrompt(t *testing.T) {
	d, reason := safety.NewClassifier().Classify(promptCommand("/tmp/x"))
	if d != safety.Prompt {
		t.Fatalf("classification = %v (%s), want Prompt; pick another test command", d, reason)
	}
}

func TestPermissionRequest(t *testing.T) {
	tests := []struct {
		name       string
		optionID   string
		wantRan    bool
		wantStatus string
	}{
		{"reject_once", "reject_once", false, ToolFailed},
		{"allow_once", "allow_once", true, ToolCompleted},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var dir string
			e := setup(t, func(n int, w http.ResponseWriter, r *http.Request) {
				if n == 0 {
					replyToolCall(w, "call_perm", "bash", map[string]any{"command": promptCommand(dir)})
					return
				}
				replyText(w, "finished")
			})
			dir = filepath.Join(e.cwd, "target")
			if err := os.Mkdir(dir, 0o755); err != nil {
				t.Fatal(err)
			}
			e.c.onRequest = func(method string, params json.RawMessage) any {
				if method != "session/request_permission" {
					return map[string]any{}
				}
				return map[string]any{"outcome": map[string]any{"outcome": "selected", "optionId": tc.optionID}}
			}
			e.cfg.PermissionMode = safety.ModeAsk
			sid := e.newSession(t).SessionID

			resp := e.prompt(t, sid, "run it")
			if resp.StopReason != StopEndTurn {
				t.Errorf("stopReason = %q", resp.StopReason)
			}

			perms := e.c.permissions()
			if len(perms) != 1 {
				t.Fatalf("permission requests = %d", len(perms))
			}
			p := perms[0]
			if p.SessionID != sid || p.ToolCall.ToolCallID != "call_perm" || p.ToolCall.Kind != "execute" {
				t.Errorf("permission request = %+v", p)
			}
			kinds := map[string]bool{}
			for _, o := range p.Options {
				kinds[o.Kind] = true
			}
			for _, k := range []string{"allow_once", "allow_always", "reject_once"} {
				if !kinds[k] {
					t.Errorf("missing option kind %q in %+v", k, p.Options)
				}
			}

			_, statErr := os.Stat(filepath.Join(dir, "ran.marker"))
			if ran := statErr == nil; ran != tc.wantRan {
				t.Errorf("command ran = %v, want %v", ran, tc.wantRan)
			}

			var final update
			for _, u := range filterKind(e.c.updates(), "tool_call_update") {
				if s := u.str("status"); s == ToolFailed || s == ToolCompleted {
					final = u
				}
			}
			if final == nil || final.str("status") != tc.wantStatus || final.str("toolCallId") != "call_perm" {
				t.Errorf("final tool_call_update = %v, want status %s", final, tc.wantStatus)
			}
		})
	}
}

func TestPromptCancel(t *testing.T) {
	started := make(chan struct{})
	var once sync.Once
	e := setup(t, func(n int, w http.ResponseWriter, r *http.Request) {
		sseStart(w)
		sseTextChunk(w, "partial")
		once.Do(func() { close(started) })
		select {
		case <-r.Context().Done():
		case <-time.After(testWait):
		}
	})
	sid := e.newSession(t).SessionID
	ch := e.c.send("session/prompt", PromptRequest{SessionID: sid, Prompt: []ContentBlock{textContent("long task")}})

	select {
	case <-started:
	case <-time.After(testWait):
		t.Fatal("LLM request never started")
	}
	e.c.waitUpdate("partial chunk", kindIs("agent_message_chunk"))
	e.c.notify("session/cancel", CancelNotification{SessionID: sid})

	r := e.c.recv(ch)
	if r.Error != nil {
		t.Fatalf("prompt error: %v", r.Error)
	}
	if got := decode[PromptResponse](t, r.Result).StopReason; got != StopCancelled {
		t.Errorf("stopReason = %q, want %q", got, StopCancelled)
	}
}

func TestSessionLoadReplaysHistory(t *testing.T) {
	e := setup(t, func(n int, w http.ResponseWriter, r *http.Request) {
		replyText(w, "Hi ", "there")
	})
	sid := e.newSession(t).SessionID
	if resp := e.prompt(t, sid, "hello agent"); resp.StopReason != StopEndTurn {
		t.Fatalf("stopReason = %q", resp.StopReason)
	}
	e.c.stop() // flush and close the first instance

	// Fresh Serve instance over the same on-disk state.
	c2 := startAgent(t, e.cfg)
	c2.call("initialize", InitializeRequest{ProtocolVersion: 1})
	r := c2.call("session/load", LoadSessionRequest{SessionID: sid, CWD: e.cwd, MCPServers: []MCPServer{}})

	// Everything received before the load response is the replay.
	replay := c2.updates()[:r.updatesBefore]
	if got := messageText(replay, "user_message_chunk"); !strings.Contains(got, "hello agent") {
		t.Errorf("replayed user text = %q in %s", got, dumpUpdates(replay))
	}
	if got := messageText(replay, "agent_message_chunk"); got != "Hi there" {
		t.Errorf("replayed agent text = %q in %s", got, dumpUpdates(replay))
	}
	var iUser, iAgent = -1, -1
	for i, u := range replay {
		if u.kind() == "user_message_chunk" && iUser < 0 {
			iUser = i
		}
		if u.kind() == "agent_message_chunk" {
			iAgent = i
		}
	}
	if iUser < 0 || iAgent < 0 || iUser > iAgent {
		t.Errorf("bad replay order user=%d agent=%d", iUser, iAgent)
	}

	// Load response carries session state, and commands follow it.
	resp := decode[LoadSessionResponse](t, r.Result)
	if resp.Modes != nil || resp.Models == nil || len(resp.ConfigOptions) == 0 {
		t.Errorf("unexpected load state: %s", r.Result)
	}
	c2.waitUpdate("available_commands_update", kindIs("available_commands_update"))

	// Loading an unknown session is an invalid-params error.
	bad := c2.recv(c2.send("session/load", LoadSessionRequest{SessionID: "does-not-exist", CWD: e.cwd}))
	if bad.Error == nil || bad.Error.Code != codeInvalidParams {
		t.Errorf("unknown session load error = %+v", bad.Error)
	}
}

func TestSessionUpdateMarshalJSON(t *testing.T) {
	text := textContent("hello")

	tool := SessionUpdate{
		SessionUpdate: "tool_call_update",
		ToolCallID:    "t1",
		Status:        ToolCompleted,
		ToolContent:   []ToolCallContent{{Type: "content", Content: &text}},
	}
	raw, err := json.Marshal(tool)
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]any
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatal(err)
	}
	arr, ok := got["content"].([]any)
	if !ok || len(arr) != 1 {
		t.Fatalf("tool content must be an array under \"content\": %s", raw)
	}
	item := arr[0].(map[string]any)
	inner := item["content"].(map[string]any)
	if item["type"] != "content" || inner["type"] != "text" || inner["text"] != "hello" {
		t.Errorf("tool content item = %s", raw)
	}
	if got["toolCallId"] != "t1" || got["status"] != "completed" || got["sessionUpdate"] != "tool_call_update" {
		t.Errorf("sibling fields lost: %s", raw)
	}
	if strings.Count(string(raw), `"content":[`) != 1 {
		t.Errorf("duplicate content keys: %s", raw)
	}

	chunk := SessionUpdate{SessionUpdate: "agent_message_chunk", Content: &text}
	raw, err = json.Marshal(chunk)
	if err != nil {
		t.Fatal(err)
	}
	got = nil
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatal(err)
	}
	obj, ok := got["content"].(map[string]any)
	if !ok || obj["type"] != "text" || obj["text"] != "hello" {
		t.Errorf("message chunk content must be an object: %s", raw)
	}

	// Neither set: no content key at all.
	raw, _ = json.Marshal(SessionUpdate{SessionUpdate: "current_mode_update", CurrentModeID: "ask"})
	if strings.Contains(string(raw), `"content"`) {
		t.Errorf("unexpected content key: %s", raw)
	}

	// Value receiver: works through a pointer and inside a notification.
	raw, _ = json.Marshal(SessionNotification{SessionID: "s", Update: tool})
	if !strings.Contains(string(raw), `"content":[`) {
		t.Errorf("notification lost tool content array: %s", raw)
	}
}
