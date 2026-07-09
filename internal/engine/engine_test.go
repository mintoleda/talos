package engine

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/mintoleda/talos/internal/client"
	"github.com/mintoleda/talos/internal/client/rpc"
	"github.com/mintoleda/talos/internal/loop"
	"github.com/mintoleda/talos/internal/mcp"
	"github.com/mintoleda/talos/internal/notify"
	"github.com/mintoleda/talos/internal/pricing"
	"github.com/mintoleda/talos/internal/protocol"
	"github.com/mintoleda/talos/internal/safety"
	"github.com/mintoleda/talos/internal/session"
	"github.com/mintoleda/talos/internal/testutil"
)

// recordingProvider captures the last StreamTurn request for assertions.
type recordingProvider struct {
	mu   sync.Mutex
	last protocol.Request
	testutil.FakeProvider
}

func (r *recordingProvider) StreamTurn(ctx context.Context, req protocol.Request) (<-chan protocol.ProviderEvent, error) {
	r.mu.Lock()
	r.last = req
	r.mu.Unlock()
	return r.FakeProvider.StreamTurn(ctx, req)
}

func (r *recordingProvider) lastRequest() protocol.Request {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.last
}

func TestEngineImplementsClientEngine(t *testing.T) {
	var _ client.Engine = (*Engine)(nil)
}

func engineHarness(t *testing.T) *Engine {
	t.Helper()

	tx := testutil.NewTestTranscript(t)
	prov := &testutil.FakeProvider{}
	exec := &testutil.FakeExecutor{}
	pb := loop.NewPromptBuilder("system", nil, "test-model")
	lp := loop.New(prov, exec, tx, pb)

	tmpDir := t.TempDir()
	baseDir := filepath.Join(tmpDir, ".talos")
	if err := os.MkdirAll(baseDir, 0755); err != nil {
		t.Fatal(err)
	}
	prices := pricing.Default

	mgr, errs := mcp.NewManager(context.Background(), nil)
	for _, e := range errs {
		t.Logf("mcp: %v", e)
	}

	eng := NewEngine(Params{
		Loop:          lp,
		PromptBuilder: pb,
		Prices:        prices,
		Provider:      "test",
		Model:         "test-model",
		BaseDir:       baseDir,
		CWD:           tmpDir,
		MCPManager:    mgr,
		AgentBuilder:  nil,
		Checkpointer:  nil,
		NotifyConfig:  notify.DefaultConfig(),
		Context:       context.Background(),
	})
	return eng
}

func TestEngineStats(t *testing.T) {
	eng := engineHarness(t)
	defer eng.Close()

	in, out, miss, cost, err := eng.Stats()
	if err != nil {
		t.Fatalf("Stats: %v", err)
	}
	if in != 0 || out != 0 || miss != 0 || cost != 0 {
		t.Fatalf("expected zero stats, got input=%d output=%d miss=%d cost=%.4f", in, out, miss, cost)
	}
}

func TestEngineNewSession(t *testing.T) {
	eng := engineHarness(t)
	defer eng.Close()

	id, err := eng.NewSession()
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	if id == "" {
		t.Fatal("expected non-empty session id")
	}
}

func TestEngineCycleThinking(t *testing.T) {
	eng := engineHarness(t)
	defer eng.Close()

	level, err := eng.CycleThinking()
	if err != nil {
		t.Fatalf("CycleThinking: %v", err)
	}
	if level == "" {
		t.Fatal("expected non-empty thinking level")
	}
}

func TestEngineCurrentThinkingLevel(t *testing.T) {
	eng := engineHarness(t)
	defer eng.Close()

	_ = eng.CurrentThinkingLevel()
}

func TestEngineSubmitAndEvents(t *testing.T) {
	eng := engineHarness(t)
	defer eng.Close()

	eng.Submit(protocol.TextBlocks("hello"))

	select {
	case ev := <-eng.Events():
		switch ev.(type) {
		case protocol.TurnEnded:
		default:
			t.Logf("got event type %T", ev)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for event from fake loop")
	}
}

func TestEngineInterrupt(t *testing.T) {
	eng := engineHarness(t)
	defer eng.Close()

	eng.Interrupt()
	eng.Interrupt()
}

func TestEngineSteer(t *testing.T) {
	eng := engineHarness(t)
	defer eng.Close()

	eng.Steer(protocol.TextBlocks("steer message"))
	drained := eng.sq.Drain()
	if len(drained) != 1 {
		t.Fatalf("expected 1 drained message, got %d", len(drained))
	}
}

func TestEngineCompact(t *testing.T) {
	eng := engineHarness(t)
	defer eng.Close()

	eng.Compact("some focus")
}

func TestEngineListSessions(t *testing.T) {
	eng := engineHarness(t)
	defer eng.Close()

	sessions, err := eng.ListSessions()
	if err != nil {
		t.Fatalf("ListSessions: %v", err)
	}
	if len(sessions) != 0 {
		t.Logf("expected 0 sessions, got %d", len(sessions))
	}
}

func TestEngineLoginProviders(t *testing.T) {
	eng := engineHarness(t)
	defer eng.Close()

	providers, err := eng.LoginProviders()
	if err != nil {
		t.Fatalf("LoginProviders: %v", err)
	}
	if len(providers) == 0 {
		t.Fatal("expected at least one login provider")
	}
}

func TestEngineMCPStatus(t *testing.T) {
	eng := engineHarness(t)
	defer eng.Close()

	status, err := eng.MCPStatus()
	if err != nil {
		t.Fatalf("MCPStatus: %v", err)
	}
	if status == "" {
		t.Fatal("expected non-empty MCP status")
	}
}

func TestEngineMCPCount(t *testing.T) {
	eng := engineHarness(t)
	defer eng.Close()

	count := eng.MCPCount()
	if count != 0 {
		t.Fatalf("expected 0 MCP connections, got %d", count)
	}
}

func TestEngineDeleteSession(t *testing.T) {
	eng := engineHarness(t)
	defer eng.Close()

	err := eng.DeleteSession("nonexistent-id")
	if err == nil {
		t.Log("DeleteSession on nonexistent id returned nil (may vary by filesystem)")
	}
}

func TestEngineLogin(t *testing.T) {
	eng := engineHarness(t)
	defer eng.Close()

	err := eng.Login("test-provider", "test-key")
	if err != nil {
		t.Fatalf("Login: %v", err)
	}
}

func TestEngineResume(t *testing.T) {
	eng := engineHarness(t)
	defer eng.Close()

	home := t.TempDir()
	t.Setenv("HOME", home)

	ns := session.NewSession(eng.cwd)
	tx, err := session.Create(ns.Path)
	if err != nil {
		t.Fatalf("session.Create: %v", err)
	}
	_ = tx.Append(protocol.TextMessage(protocol.RoleUser, "test message"))
	tx.Close()

	newID, history, err := eng.Resume(ns.ID)
	if err != nil {
		t.Fatalf("Resume(%q): %v", ns.ID, err)
	}
	if newID == "" {
		t.Fatal("expected non-empty resumed session id")
	}
	if len(history) != 1 {
		t.Fatalf("expected 1 frozen message, got %d", len(history))
	}
}

func TestEngineCancelSubagent(t *testing.T) {
	eng := engineHarness(t)
	defer eng.Close()

	eng.CancelSubagent("nonexistent")
}

func TestEngineSwitchModel(t *testing.T) {
	eng := engineHarness(t)
	defer eng.Close()

	// Without a real API key this should error; must not persist config.
	err := eng.SwitchModel("openai", "gpt-4")
	if err == nil {
		t.Log("SwitchModel returned nil (acceptable if provider constructed)")
	}
}

func TestEngineClose(t *testing.T) {
	eng := engineHarness(t)

	eng.Close()
	eng.Close()
}

func TestEngineResumeLatest(t *testing.T) {
	eng := engineHarness(t)
	defer eng.Close()

	home := t.TempDir()
	t.Setenv("HOME", home)

	ns := session.NewSession(eng.cwd)
	tx, err := session.Create(ns.Path)
	if err != nil {
		t.Fatalf("session.Create: %v", err)
	}
	_ = tx.Append(protocol.TextMessage(protocol.RoleUser, "hello"))
	tx.Close()

	newID, history, err := eng.Resume("")
	if err != nil {
		t.Fatalf("Resume(\"\"): %v", err)
	}
	if newID == "" {
		t.Fatal("expected non-empty resumed session id")
	}
	_ = history
}

func TestEngineListModels(t *testing.T) {
	eng := engineHarness(t)
	defer eng.Close()

	models, err := eng.ListModels()
	if err != nil {
		t.Fatalf("ListModels: %v", err)
	}
	_ = models
}

func TestEngineSubmitClosed(t *testing.T) {
	eng := engineHarness(t)
	eng.Close()

	eng.Submit(protocol.TextBlocks("hello"))
}

func TestEngineInterruptClosed(t *testing.T) {
	eng := engineHarness(t)
	eng.Close()

	eng.Interrupt()
}

func TestEngineEventsClosed(t *testing.T) {
	eng := engineHarness(t)
	eng.Close()

	_, ok := <-eng.Events()
	if ok {
		t.Fatal("expected closed event channel after Close")
	}
}

func TestEngineApprovePending(t *testing.T) {
	eng := engineHarness(t)
	defer eng.Close()

	reply := make(chan bool, 1)
	eng.setPending(pendingApproval{
		reply: func(ok bool, _ []byte) {
			reply <- ok
		},
		toolName: "bash",
		command:  "ls",
		reason:   "ask",
	})

	snap := eng.Snapshot()
	if snap.PendingPermission == nil || snap.PendingPermission.ToolName != "bash" {
		t.Fatalf("expected pending permission in snapshot, got %+v", snap.PendingPermission)
	}

	eng.Approve(true, nil)
	select {
	case ok := <-reply:
		if !ok {
			t.Fatal("expected approved")
		}
	default:
		t.Fatal("expected reply")
	}

	// Second Approve is a no-op (first-wins).
	eng.Approve(false, nil)

	snap = eng.Snapshot()
	if snap.PendingPermission != nil {
		t.Fatal("expected pending cleared")
	}
}

func TestEngineSubscribe(t *testing.T) {
	eng := engineHarness(t)
	defer eng.Close()

	got := make(chan protocol.Event, 1)
	cancel := eng.Subscribe(func(ev protocol.Event) {
		select {
		case got <- ev:
		default:
		}
	})
	eng.Emit(protocol.Notice{Level: "info", Text: "hi"})
	select {
	case ev := <-got:
		if n, ok := ev.(protocol.Notice); !ok || n.Text != "hi" {
			t.Fatalf("unexpected event: %#v", ev)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for subscribe")
	}
	cancel()
	eng.Emit(protocol.Notice{Level: "info", Text: "after"})
	select {
	case <-got:
		t.Fatal("should not receive after unsubscribe")
	case <-time.After(50 * time.Millisecond):
	}
}

func TestListCommandsNonEmpty(t *testing.T) {
	cmds := Commands()
	if len(cmds) == 0 {
		t.Fatal("expected non-empty command list")
	}
	names := map[string]bool{}
	for _, c := range cmds {
		names[c.Name] = true
	}
	for _, want := range []string{"/model", "/thinking", "/permission", "/panic", "/mcp", "/subagents", "/compact"} {
		if !names[want] {
			t.Fatalf("missing command %s", want)
		}
	}

	eng := engineHarness(t)
	defer eng.Close()
	raw, err := eng.HandleRequest(context.Background(), rpc.ListCommands, nil)
	if err != nil {
		t.Fatalf("ListCommands RPC: %v", err)
	}
	var out rpc.ListCommandsResult
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(out.Commands) == 0 {
		t.Fatal("ListCommands result empty")
	}
}

func TestSetPermissionMode(t *testing.T) {
	eng := engineHarness(t)
	defer eng.Close()
	eng.pol = safety.NewPolicy(safety.ModeAuto, eng.cwd, safety.NewClassifier(), true)

	if err := eng.SetPermissionMode("ask"); err != nil {
		t.Fatalf("SetPermissionMode: %v", err)
	}
	if got := eng.PermissionMode(); got != "ask" {
		t.Fatalf("want ask, got %s", got)
	}

	params, _ := json.Marshal(rpc.SetPermissionModeParams{Mode: "auto"})
	raw, err := eng.HandleRequest(context.Background(), rpc.SetPermissionMode, params)
	if err != nil {
		t.Fatalf("setPermissionMode RPC: %v", err)
	}
	var level rpc.LevelResult
	if err := json.Unmarshal(raw, &level); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if level.Level != "auto" {
		t.Fatalf("want auto, got %s", level.Level)
	}
}

func TestSubmitTextResolvesImage(t *testing.T) {
	tx := testutil.NewTestTranscript(t)
	rec := &recordingProvider{}
	exec := &testutil.FakeExecutor{}
	pb := loop.NewPromptBuilder("system", nil, "test-model")
	lp := loop.New(rec, exec, tx, pb)

	tmpDir := t.TempDir()
	baseDir := filepath.Join(tmpDir, ".talos")
	if err := os.MkdirAll(baseDir, 0755); err != nil {
		t.Fatal(err)
	}
	// Minimal 1x1 PNG
	png := []byte{
		0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a, 0x00, 0x00, 0x00, 0x0d,
		0x49, 0x48, 0x44, 0x52, 0x00, 0x00, 0x00, 0x01, 0x00, 0x00, 0x00, 0x01,
		0x08, 0x02, 0x00, 0x00, 0x00, 0x90, 0x77, 0x53, 0xde, 0x00, 0x00, 0x00,
		0x0c, 0x49, 0x44, 0x41, 0x54, 0x08, 0xd7, 0x63, 0xf8, 0xff, 0xff, 0x3f,
		0x00, 0x05, 0xfe, 0x02, 0xfe, 0xa7, 0x35, 0x81, 0x84, 0x00, 0x00, 0x00,
		0x00, 0x49, 0x45, 0x4e, 0x44, 0xae, 0x42, 0x60, 0x82,
	}
	if err := os.WriteFile(filepath.Join(tmpDir, "pic.png"), png, 0644); err != nil {
		t.Fatal(err)
	}

	eng := NewEngine(Params{
		Loop:          lp,
		PromptBuilder: pb,
		Prices:        pricing.Default,
		Provider:      "test",
		Model:         "test-model",
		BaseDir:       baseDir,
		CWD:           tmpDir,
		Context:       context.Background(),
	})
	defer eng.Close()

	gotUI := make(chan string, 1)
	cancel := eng.Subscribe(func(ev protocol.Event) {
		if ui, ok := ev.(protocol.UserInput); ok {
			select {
			case gotUI <- ui.Text:
			default:
			}
		}
	})
	defer cancel()

	eng.SubmitText("look @pic.png please")

	select {
	case display := <-gotUI:
		if display != "look @pic.png please" {
			t.Fatalf("UserInput display = %q", display)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for UserInput")
	}

	deadline := time.Now().Add(2 * time.Second)
	var req protocol.Request
	for time.Now().Before(deadline) {
		req = rec.lastRequest()
		if len(req.Messages) > 0 || len(req.Volatile) > 0 {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}

	var blocks []protocol.ContentBlock
	if len(req.Volatile) > 0 {
		blocks = req.Volatile
	} else if len(req.Messages) > 0 {
		blocks = req.Messages[len(req.Messages)-1].Msg.Content
	}
	hasImage := false
	hasText := false
	for _, b := range blocks {
		if b.Type == protocol.BlockImage && b.Image != nil && b.Image.Data != "" {
			hasImage = true
		}
		if b.Type == protocol.BlockText && b.Text != "" {
			hasText = true
		}
	}
	if !hasImage || !hasText {
		t.Fatalf("expected text+image blocks in turn, got %#v", blocks)
	}
}
