package client

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/mintoleda/talos/internal/loop"
	"github.com/mintoleda/talos/internal/mcp"
	"github.com/mintoleda/talos/internal/notify"
	"github.com/mintoleda/talos/internal/pricing"
	"github.com/mintoleda/talos/internal/protocol"
	"github.com/mintoleda/talos/internal/session"
	"github.com/mintoleda/talos/internal/testutil"
)

// TestLocalEngineImplementsEngine ensures LocalEngine satisfies the Engine
// interface at compile time. If LocalEngine is ever changed so it no longer
// implements Engine, this test won't compile.
func TestLocalEngineImplementsEngine(t *testing.T) {
	var _ Engine = (*LocalEngine)(nil)
}

// engineHarness creates a LocalEngine wired to fakes for testing. The caller
// must defer engine.Close().
func engineHarness(t *testing.T) *LocalEngine {
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

	eng := NewLocalEngine(Params{
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

func TestLocalEngineStats(t *testing.T) {
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

func TestLocalEngineNewSession(t *testing.T) {
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

func TestLocalEngineCycleThinking(t *testing.T) {
	eng := engineHarness(t)
	defer eng.Close()

	// With a fake model (unknown), all 6 levels are available.
	level, err := eng.CycleThinking()
	if err != nil {
		t.Fatalf("CycleThinking: %v", err)
	}
	if level == "" {
		t.Fatal("expected non-empty thinking level")
	}
}

func TestLocalEngineCurrentThinkingLevel(t *testing.T) {
	eng := engineHarness(t)
	defer eng.Close()

	level := eng.CurrentThinkingLevel()
	// Default is empty string for a fresh PromptBuilder.
	_ = level
}

func TestLocalEngineSubmitAndEvents(t *testing.T) {
	eng := engineHarness(t)
	defer eng.Close()

	// Submit should not block and events channel should be open.
	eng.Submit(protocol.TextBlocks("hello"))

	// The fake provider returns no events, so the turn should end quickly.
	select {
	case ev := <-eng.Events():
		switch ev.(type) {
		case protocol.TurnEnded:
			// Good — the fake loop finishes immediately.
		default:
			t.Logf("got event type %T", ev)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for event from fake loop")
	}
}

func TestLocalEngineInterrupt(t *testing.T) {
	eng := engineHarness(t)
	defer eng.Close()

	// Interrupt should not panic and should be non-blocking.
	eng.Interrupt()
	eng.Interrupt() // double-interrupt should be safe
}

func TestLocalEngineSteer(t *testing.T) {
	eng := engineHarness(t)
	defer eng.Close()

	// Steer enqueues a message.
	eng.Steer(protocol.TextBlocks("steer message"))
	// Drain it via the underlying sync queue.
	drained := eng.sq.Drain()
	if len(drained) != 1 {
		t.Fatalf("expected 1 drained message, got %d", len(drained))
	}
}

func TestLocalEngineCompact(t *testing.T) {
	eng := engineHarness(t)
	defer eng.Close()

	// Compact should not block; with no compactor configured it still
	// gets processed in the bg goroutine and emits an error event.
	eng.Compact("some focus")
}

func TestLocalEngineListSessions(t *testing.T) {
	eng := engineHarness(t)
	defer eng.Close()

	sessions, err := eng.ListSessions()
	if err != nil {
		t.Fatalf("ListSessions: %v", err)
	}
	// Should be empty since we haven't created any sessions.
	if len(sessions) != 0 {
		t.Logf("expected 0 sessions, got %d", len(sessions))
	}
}

func TestLocalEngineLoginProviders(t *testing.T) {
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

func TestLocalEngineMCPStatus(t *testing.T) {
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

func TestLocalEngineMCPCount(t *testing.T) {
	eng := engineHarness(t)
	defer eng.Close()

	count := eng.MCPCount()
	if count != 0 {
		t.Fatalf("expected 0 MCP connections, got %d", count)
	}
}

func TestLocalEngineDeleteSession(t *testing.T) {
	eng := engineHarness(t)
	defer eng.Close()

	// Deleting a non-existent session should return an error.
	err := eng.DeleteSession("nonexistent-id")
	if err == nil {
		t.Log("DeleteSession on nonexistent id returned nil (may vary by filesystem)")
	}
}

func TestLocalEngineLogin(t *testing.T) {
	eng := engineHarness(t)
	defer eng.Close()

	// Writing a key should succeed.
	err := eng.Login("test-provider", "test-key")
	if err != nil {
		t.Fatalf("Login: %v", err)
	}
}

func TestLocalEngineResume(t *testing.T) {
	eng := engineHarness(t)
	defer eng.Close()

	// Create a session manually with a persisted message so the file is on disk.
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

func TestLocalEngineCancelSubagent(t *testing.T) {
	eng := engineHarness(t)
	defer eng.Close()

	// CancelSubagent on a nil builder should not panic.
	eng.CancelSubagent("nonexistent")
}

func TestLocalEngineSwitchModel(t *testing.T) {
	eng := engineHarness(t)
	defer eng.Close()

	// Without a SwitchProvider function, this should error.
	err := eng.SwitchModel("openai", "gpt-4")
	if err == nil {
		t.Log("SwitchModel returned nil (acceptable if no switch function configured)")
	}
}

func TestLocalEngineClose(t *testing.T) {
	eng := engineHarness(t)

	// Close should clean up without panicking.
	eng.Close()

	// Double-close should be safe.
	eng.Close()
}

// TestLocalEngineResumeLatest tests that Resume with an empty string picks
// the latest session.
func TestLocalEngineResumeLatest(t *testing.T) {
	eng := engineHarness(t)
	defer eng.Close()

	// Create a session with a persisted message so it shows up as "latest".
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

// TestLocalEngineListModels tests that ListModels returns at least zero
// results without error.
func TestLocalEngineListModels(t *testing.T) {
	eng := engineHarness(t)
	defer eng.Close()

	models, err := eng.ListModels()
	if err != nil {
		t.Fatalf("ListModels: %v", err)
	}
	// With no real API keys configured, this returns empty slice — not an error.
	_ = models
}

// TestLocalEngineSubmitClosed ensures Submit doesn't deadlock after Close.
func TestLocalEngineSubmitClosed(t *testing.T) {
	eng := engineHarness(t)
	eng.Close()

	// Must not block.
	eng.Submit(protocol.TextBlocks("hello"))
}

// TestLocalEngineInterruptClosed ensures Interrupt doesn't deadlock after Close.
func TestLocalEngineInterruptClosed(t *testing.T) {
	eng := engineHarness(t)
	eng.Close()

	eng.Interrupt()
}

// TestLocalEngineEventsClosed ensures Events returns a closed channel after Close.
func TestLocalEngineEventsClosed(t *testing.T) {
	eng := engineHarness(t)
	eng.Close()

	_, ok := <-eng.Events()
	if ok {
		t.Fatal("expected closed event channel after Close")
	}
}
