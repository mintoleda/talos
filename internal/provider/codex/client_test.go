package codex

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/mintoleda/talos/internal/config"
	"github.com/mintoleda/talos/internal/protocol"
)

const okSSE = `data: {"type":"response.output_item.added","output_index":0,"item":{"type":"message"}}

data: {"type":"response.output_text.delta","output_index":0,"delta":"ok"}

data: {"type":"response.completed","response":{"status":"completed","usage":{"input_tokens":3,"output_tokens":1}}}

`

// codexBackend fakes both the auth server (/oauth/token) and the Codex
// backend (/codex/responses). Responses accept only the token in valid.
type codexBackend struct {
	t         *testing.T
	srv       *httptest.Server
	mu        sync.Mutex
	valid     string // access token the backend accepts
	nextTok   string // access token handed out on refresh
	refresh   int
	rejectAll bool
	requests  []*http.Request
	bodies    [][]byte
}

func newBackend(t *testing.T) *codexBackend {
	b := &codexBackend{t: t}
	b.srv = httptest.NewServer(http.HandlerFunc(b.serve))
	t.Cleanup(b.srv.Close)
	return b
}

func (b *codexBackend) serve(w http.ResponseWriter, r *http.Request) {
	b.mu.Lock()
	defer b.mu.Unlock()
	switch r.URL.Path {
	case "/oauth/token":
		_ = r.ParseForm()
		if r.PostForm.Get("grant_type") != "refresh_token" {
			b.t.Errorf("grant_type = %q", r.PostForm.Get("grant_type"))
		}
		b.refresh++
		b.valid = b.nextTok
		_ = json.NewEncoder(w).Encode(map[string]any{
			"access_token": b.nextTok, "refresh_token": "rotated", "expires_in": 3600,
		})
	case "/backend-api/codex/responses":
		body, _ := io.ReadAll(r.Body)
		b.requests = append(b.requests, r)
		b.bodies = append(b.bodies, body)
		if b.rejectAll || r.Header.Get("Authorization") != "Bearer "+b.valid {
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte(`{"error":{"message":"invalid token"}}`))
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte(okSSE))
	default:
		http.NotFound(w, r)
	}
}

type memStore struct {
	mu    sync.Mutex
	cred  config.OAuthCredential
	saves int
}

func (s *memStore) Load() (config.OAuthCredential, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.cred, nil
}

func (s *memStore) Save(c config.OAuthCredential) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.cred = c
	s.saves++
	return nil
}

func (b *codexBackend) client(store CredentialStore) *Client {
	o := NewOAuth()
	o.AuthBaseURL = b.srv.URL
	return NewWithStore(b.srv.URL+"/backend-api", store, o)
}

func drain(t *testing.T, ch <-chan protocol.ProviderEvent) string {
	t.Helper()
	var text string
	for ev := range ch {
		switch e := ev.(type) {
		case protocol.PEText:
			text += e.Text
		case protocol.PEError:
			t.Fatalf("stream error: %v", e.Err)
		}
	}
	return text
}

func sampleRequest() protocol.Request {
	return protocol.Request{
		System: "be brief",
		Model:  "gpt-6-sol",
		Tools: []protocol.ToolSchema{{
			Name: "read", Description: "read a file", Parameters: json.RawMessage(`{"type":"object","properties":{"path":{"type":"string"}}}`),
		}},
		Messages: []protocol.FrozenMessage{
			{Msg: protocol.TextMessage(protocol.RoleUser, "hi")},
			{Msg: protocol.Message{Role: protocol.RoleAssistant, Content: []protocol.ContentBlock{
				{Type: protocol.BlockText, Text: "reading"},
				{Type: protocol.BlockToolUse, ToolUse: &protocol.ToolUse{ID: "toolu_01:x", Name: "read", Args: map[string]any{"path": "a"}}},
			}}},
			{Msg: protocol.Message{Role: protocol.RoleTool, Content: []protocol.ContentBlock{
				{Type: protocol.BlockToolResult, ToolResult: &protocol.ToolResult{ToolUseID: "toolu_01:x", Content: "contents"}},
			}}},
		},
		Volatile:      []protocol.ContentBlock{{Type: protocol.BlockText, Text: "<env/>"}},
		ThinkingLevel: "minimal",
	}
}

func TestStreamTurnRequestShape(t *testing.T) {
	b := newBackend(t)
	tok := fakeJWT("acct_s")
	b.valid = tok
	store := &memStore{cred: config.OAuthCredential{Access: tok, Refresh: "r", Expires: time.Now().Add(time.Hour).UnixMilli(), AccountID: "acct_s"}}
	c := b.client(store)

	ch, err := c.StreamTurn(context.Background(), sampleRequest())
	if err != nil {
		t.Fatal(err)
	}
	if got := drain(t, ch); got != "ok" {
		t.Fatalf("text = %q", got)
	}
	if b.refresh != 0 {
		t.Fatal("unexpected refresh for a fresh token")
	}

	h := b.requests[0].Header
	for k, want := range map[string]string{
		"Authorization":      "Bearer " + tok,
		"Chatgpt-Account-Id": "acct_s",
		"Originator":         Originator,
		"Openai-Beta":        "responses=experimental",
		"Accept":             "text/event-stream",
		"Content-Type":       "application/json",
	} {
		if h.Get(k) != want {
			t.Errorf("header %s = %q, want %q", k, h.Get(k), want)
		}
	}
	if h.Get("Session-Id") == "" || h.Get("Session-Id") != h.Get("X-Client-Request-Id") {
		t.Errorf("session headers = %q / %q", h.Get("Session-Id"), h.Get("X-Client-Request-Id"))
	}

	var body map[string]any
	if err := json.Unmarshal(b.bodies[0], &body); err != nil {
		t.Fatal(err)
	}
	if body["model"] != "gpt-6-sol" || body["store"] != false || body["stream"] != true ||
		body["instructions"] != "be brief" || body["tool_choice"] != "auto" || body["parallel_tool_calls"] != true {
		t.Fatalf("body = %s", b.bodies[0])
	}
	if body["prompt_cache_key"] != h.Get("Session-Id") {
		t.Errorf("prompt_cache_key = %v", body["prompt_cache_key"])
	}
	if inc, _ := json.Marshal(body["include"]); string(inc) != `["reasoning.encrypted_content"]` {
		t.Errorf("include = %s", inc)
	}
	if r, _ := json.Marshal(body["reasoning"]); string(r) != `{"effort":"low","summary":"auto"}` {
		t.Errorf("reasoning = %s", r)
	}
	// Check raw bytes so field order matches what is actually sent.
	input := b.bodies[0]
	if !strings.Contains(string(input), `"tools":[{"type":"function","name":"read","description":"read a file","parameters":{`) {
		t.Errorf("tools missing in %s", input)
	}
	for _, want := range []string{
		`{"role":"user","content":[{"type":"input_text","text":"hi"}]}`,
		`{"type":"message","role":"assistant","content":[{"type":"output_text","text":"reading","annotations":[]}],"status":"completed","id":"msg_talos_1"}`,
		`{"type":"function_call","call_id":"toolu_01_x","name":"read","arguments":"{\"path\":\"a\"}"}`,
		`{"type":"function_call_output","call_id":"toolu_01_x","output":"contents"}`,
		`{"role":"user","content":[{"type":"input_text","text":"` + "\\u003cenv/\\u003e" + `"}]}`,
	} {
		if !strings.Contains(string(input), want) {
			t.Errorf("input missing %s\n got %s", want, input)
		}
	}
}

func TestReasoningFor(t *testing.T) {
	cases := []struct {
		model, level string
		want         *reasoning
	}{
		{"gpt-6-sol", "off", &reasoning{Effort: "none"}},
		{"gpt-6-sol", "", &reasoning{Effort: "none"}},
		{"gpt-6-astra", "off", nil},
		{"gpt-6-sol", "minimal", &reasoning{Effort: "low", Summary: "auto"}},
		{"gpt-6-sol", "medium", &reasoning{Effort: "medium", Summary: "auto"}},
		{"gpt-6-sol", "xhigh", &reasoning{Effort: "xhigh", Summary: "auto"}},
	}
	for _, c := range cases {
		got := reasoningFor(c.model, c.level)
		if (got == nil) != (c.want == nil) || (got != nil && *got != *c.want) {
			t.Errorf("reasoningFor(%s,%s) = %+v, want %+v", c.model, c.level, got, c.want)
		}
	}
}

func TestStreamTurnRefreshesNearExpiry(t *testing.T) {
	b := newBackend(t)
	b.nextTok = fakeJWT("acct_r")
	store := &memStore{cred: config.OAuthCredential{
		Access: fakeJWT("acct_r") + "old", Refresh: "r", Expires: time.Now().Add(time.Minute).UnixMilli(), AccountID: "acct_r",
	}}
	ch, err := b.client(store).StreamTurn(context.Background(), sampleRequest())
	if err != nil {
		t.Fatal(err)
	}
	drain(t, ch)
	if b.refresh != 1 || store.saves != 1 {
		t.Fatalf("refresh=%d saves=%d", b.refresh, store.saves)
	}
	if store.cred.Access != b.nextTok || store.cred.Refresh != "rotated" {
		t.Fatalf("stored cred not updated: refresh=%q", store.cred.Refresh)
	}
	if len(b.requests) != 1 {
		t.Fatalf("requests = %d", len(b.requests))
	}
}

func TestStreamTurnRetriesOnceOn401(t *testing.T) {
	b := newBackend(t)
	b.valid = "something-else"
	b.nextTok = fakeJWT("acct_u")
	store := &memStore{cred: config.OAuthCredential{
		Access: fakeJWT("acct_u") + "revoked", Refresh: "r", Expires: time.Now().Add(time.Hour).UnixMilli(),
	}}
	ch, err := b.client(store).StreamTurn(context.Background(), sampleRequest())
	if err != nil {
		t.Fatal(err)
	}
	if got := drain(t, ch); got != "ok" {
		t.Fatalf("text = %q", got)
	}
	if b.refresh != 1 || len(b.requests) != 2 {
		t.Fatalf("refresh=%d requests=%d", b.refresh, len(b.requests))
	}
}

func TestStreamTurnPersistentAuthFailure(t *testing.T) {
	b := newBackend(t)
	b.nextTok = fakeJWT("acct_f")
	store := &memStore{cred: config.OAuthCredential{
		Access: fakeJWT("acct_f") + "x", Refresh: "r", Expires: time.Now().Add(time.Hour).UnixMilli(),
	}}
	// Backend rejects even the refreshed token.
	b.rejectAll = true
	c := b.client(store)
	_, err := c.StreamTurn(context.Background(), sampleRequest())
	if err == nil || !strings.Contains(err.Error(), "401") || !strings.Contains(err.Error(), "invalid token") {
		t.Fatalf("err = %v", err)
	}
	if b.refresh != 1 {
		t.Fatalf("refresh = %d, want exactly one", b.refresh)
	}
}

func TestFileStoreRefreshPersists(t *testing.T) {
	dir := t.TempDir()
	b := newBackend(t)
	b.nextTok = fakeJWT("acct_file")
	if err := config.WriteOAuth(dir, ProviderName, config.OAuthCredential{
		Access: "expired", Refresh: "r", Expires: time.Now().Add(-time.Hour).UnixMilli(), AccountID: "acct_file",
	}); err != nil {
		t.Fatal(err)
	}
	ch, err := b.client(FileStore(dir)).StreamTurn(context.Background(), sampleRequest())
	if err != nil {
		t.Fatal(err)
	}
	drain(t, ch)
	cred, ok := config.ReadOAuth(dir, ProviderName)
	if !ok || cred.Access != b.nextTok || cred.Refresh != "rotated" || cred.AccountID != "acct_file" {
		t.Fatalf("persisted cred mismatch (ok=%v refresh=%q account=%q)", ok, cred.Refresh, cred.AccountID)
	}
}

func TestStreamTurnNotLoggedIn(t *testing.T) {
	_, err := New("", t.TempDir()).StreamTurn(context.Background(), sampleRequest())
	if err == nil || !strings.Contains(err.Error(), "not logged in") {
		t.Fatalf("err = %v", err)
	}
}

func TestErrorMessageUsageLimit(t *testing.T) {
	msg := errorMessage(429, []byte(`{"error":{"code":"usage_limit_reached","plan_type":"PLUS"}}`))
	if msg != "You have hit your ChatGPT usage limit (plus plan)." {
		t.Fatalf("msg = %q", msg)
	}
	if got := errorMessage(400, []byte(`{"error":{"message":"bad model"}}`)); got != "bad model" {
		t.Fatalf("msg = %q", got)
	}
}

func TestListModelsStatic(t *testing.T) {
	ids, err := New("", t.TempDir()).ListModels(context.Background())
	if err != nil || len(ids) != len(Models) || ids[0] != "gpt-5.6-luna" {
		t.Fatalf("ids = %v err = %v", ids, err)
	}
}

func TestEndpoint(t *testing.T) {
	for base, want := range map[string]string{
		"":                                      "https://chatgpt.com/backend-api/codex/responses",
		"https://x/backend-api/":                "https://x/backend-api/codex/responses",
		"https://x/backend-api/codex":           "https://x/backend-api/codex/responses",
		"https://x/backend-api/codex/responses": "https://x/backend-api/codex/responses",
	} {
		if got := NewWithStore(base, &memStore{}, NewOAuth()).endpoint(); got != want {
			t.Errorf("endpoint(%q) = %q, want %q", base, got, want)
		}
	}
}
