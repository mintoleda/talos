package loop

import (
	"context"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/mintoleda/talos/internal/executor"
	"github.com/mintoleda/talos/internal/protocol"
	"github.com/mintoleda/talos/internal/session"
)

type fakeProvider struct {
	batches [][]protocol.ProviderEvent
	calls   int
}

func (f *fakeProvider) StreamTurn(ctx context.Context, req protocol.Request) (<-chan protocol.ProviderEvent, error) {
	ch := make(chan protocol.ProviderEvent)
	batch := f.batches[f.calls]
	f.calls++
	go func() {
		defer close(ch)
		for _, e := range batch {
			ch <- e
		}
	}()
	return ch, nil
}

func (f *fakeProvider) ListModels(ctx context.Context) ([]string, error) {
	return nil, nil
}

type fakeExecutor struct{ calls []protocol.ToolUse }

func (e *fakeExecutor) Run(ctx context.Context, tu protocol.ToolUse, _ protocol.EmitFunc) protocol.ToolResult {
	e.calls = append(e.calls, tu)
	return protocol.ToolResult{ToolUseID: tu.ID, Content: "ok"}
}

func (e *fakeExecutor) Close() {}

func newTestLoop(t *testing.T, prov *fakeProvider, exec executor.Executor) *Loop {
	t.Helper()
	tx, err := session.Create(filepath.Join(t.TempDir(), "t.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	pb := NewPromptBuilder("sys", nil, "m")
	return New(prov, exec, tx, pb)
}

// concurrentExecutor blocks until a latch opens so we can detect overlap.
type concurrentExecutor struct {
	calls  []protocol.ToolUse
	mu     sync.Mutex
	latch  chan struct{}
	inside int32 // atomic counter of concurrently-running tools
	max    int32
}

func (e *concurrentExecutor) Run(ctx context.Context, tu protocol.ToolUse, _ protocol.EmitFunc) protocol.ToolResult {
	n := atomic.AddInt32(&e.inside, 1)
	for {
		old := atomic.LoadInt32(&e.max)
		if n <= old || atomic.CompareAndSwapInt32(&e.max, old, n) {
			break
		}
	}

	e.mu.Lock()
	e.calls = append(e.calls, tu)
	e.mu.Unlock()

	<-e.latch
	atomic.AddInt32(&e.inside, -1)
	return protocol.ToolResult{ToolUseID: tu.ID, Content: "ok"}
}

func (e *concurrentExecutor) Close() {}

func TestParallelToolExecution(t *testing.T) {
	prov := &fakeProvider{batches: [][]protocol.ProviderEvent{
		{
			protocol.PEToolCall{ToolUse: protocol.ToolUse{ID: "1", Name: "read", Args: map[string]any{"path": "a"}}},
			protocol.PEToolCall{ToolUse: protocol.ToolUse{ID: "2", Name: "read", Args: map[string]any{"path": "b"}}},
			protocol.PEToolCall{ToolUse: protocol.ToolUse{ID: "3", Name: "read", Args: map[string]any{"path": "c"}}},
			protocol.PEDone{StopReason: "tool_calls"},
		},
		{
			protocol.PEText{Text: "done"},
			protocol.PEUsage{Usage: protocol.Usage{PromptTokens: 10}},
			protocol.PEDone{StopReason: "stop"},
		},
	}}
	exec := &concurrentExecutor{latch: make(chan struct{})}
	lp := newTestLoop(t, prov, exec)

	done := make(chan error, 1)
	go func() {
		done <- lp.RunTurn(context.Background(), protocol.TextBlocks("hi"), func(protocol.Event) {})
	}()

	for {
		if atomic.LoadInt32(&exec.max) == 3 {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	close(exec.latch)

	if err := <-done; err != nil {
		t.Fatalf("RunTurn error: %v", err)
	}

	if atomic.LoadInt32(&exec.max) != 3 {
		t.Fatalf("expected 3 concurrent tools, got %d", exec.max)
	}

	msgs := lp.tx.Frozen()
	if len(msgs) != 4 {
		t.Fatalf("expected 4 messages, got %d", len(msgs))
	}
	toolMsg := msgs[2].Msg
	if toolMsg.Role != protocol.RoleTool || len(toolMsg.Content) != 3 {
		t.Fatalf("expected tool message with 3 results, got %+v", toolMsg)
	}
	for i, id := range []string{"1", "2", "3"} {
		if toolMsg.Content[i].ToolResult == nil || toolMsg.Content[i].ToolResult.ToolUseID != id {
			t.Fatalf("result %d expected tool_use_id %s, got %+v", i, id, toolMsg.Content[i].ToolResult)
		}
	}
}

func TestToolRoundTrip(t *testing.T) {
	prov := &fakeProvider{batches: [][]protocol.ProviderEvent{
		// turn 1: model asks for two tools in one assistant message
		{
			protocol.PEText{Text: "working"},
			protocol.PEToolCall{ToolUse: protocol.ToolUse{ID: "1", Name: "read", Args: map[string]any{"path": "a"}}},
			protocol.PEToolCall{ToolUse: protocol.ToolUse{ID: "2", Name: "read", Args: map[string]any{"path": "b"}}},
			protocol.PEDone{StopReason: "tool_calls"},
		},
		// turn 2: model finishes with no tools
		{
			protocol.PEText{Text: "done"},
			protocol.PEUsage{Usage: protocol.Usage{PromptTokens: 10, CachedPromptTokens: 4}},
			protocol.PEDone{StopReason: "stop"},
		},
	}}
	exec := &fakeExecutor{}
	lp := newTestLoop(t, prov, exec)

	var ended *protocol.TurnEnded
	err := lp.RunTurn(context.Background(), protocol.TextBlocks("hi"), func(ev protocol.Event) {
		if te, ok := ev.(protocol.TurnEnded); ok {
			ended = &te
		}
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(exec.calls) != 2 {
		t.Fatalf("expected 2 tool calls, got %d", len(exec.calls))
	}
	if ended == nil || ended.Usage.CachedPromptTokens != 4 {
		t.Fatalf("expected TurnEnded with cached=4, got %+v", ended)
	}

	// The transcript should hold: user, assistant(tools), tool(2 results), assistant(final).
	msgs := lp.tx.Frozen()
	if len(msgs) != 4 {
		t.Fatalf("expected 4 transcript messages, got %d", len(msgs))
	}
	toolMsg := msgs[2].Msg
	if toolMsg.Role != protocol.RoleTool || len(toolMsg.Content) != 2 {
		t.Fatalf("expected one tool message with 2 results, got %+v", toolMsg)
	}
}

func TestIterationCap(t *testing.T) {
	var batches [][]protocol.ProviderEvent
	for i := 0; i < 60; i++ {
		batches = append(batches, []protocol.ProviderEvent{
			protocol.PEToolCall{ToolUse: protocol.ToolUse{ID: "x", Name: "read", Args: map[string]any{"path": "a"}}},
			protocol.PEDone{StopReason: "tool_calls"},
		})
	}
	prov := &fakeProvider{batches: batches}
	exec := &fakeExecutor{}
	lp := newTestLoop(t, prov, exec)
	lp.MaxIterations = 50

	err := lp.RunTurn(context.Background(), protocol.TextBlocks("hi"), func(ev protocol.Event) {})
	if err == nil {
		t.Fatal("expected iteration limit error, got nil")
	}
	if !strings.Contains(err.Error(), "iteration limit") {
		t.Fatalf("expected iteration limit error, got %v", err)
	}
}

// TestNewRestoresStatsFromTranscript guards the `talos -c` behavior that
// `/stats` shows the previous session's usage after a reload. The transcript
// is created and pre-loaded with a stats record, then `loop.New` should
// initialize its accumulator from that record.
func TestNewRestoresStatsFromTranscript(t *testing.T) {
	path := filepath.Join(t.TempDir(), "t.jsonl")
	tx, err := session.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	// Write a real message first; Transcript.Close() deletes the on-disk file
	// when frozen+summaries are both empty, so a stats-only file wouldn't
	// survive to be reloaded by the next process.
	if err := tx.Append(protocol.TextMessage(protocol.RoleUser, "previous turn")); err != nil {
		t.Fatal(err)
	}
	if err := tx.WriteStats(session.StatsRecord{
		Calls:        3,
		InputTokens:  1000,
		OutputTokens: 500,
		CachedTokens: 200,
	}); err != nil {
		t.Fatal(err)
	}
	// Close so the bytes are flushed, then reopen via Load so the in-memory
	// state matches what `talos -c` would see on startup.
	if err := tx.Close(); err != nil {
		t.Fatal(err)
	}
	tx, err = session.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Close()

	pb := NewPromptBuilder("sys", nil, "m")
	prov := &fakeProvider{}
	lp := New(prov, &fakeExecutor{}, tx, pb)

	got := lp.Stats()
	want := Stats{Calls: 3, InputTokens: 1000, OutputTokens: 500, CachedTokens: 200}
	if got != want {
		t.Fatalf("stats not restored: got %+v, want %+v", got, want)
	}
}

// TestNewFreshTranscriptLeavesStatsZero ensures we don't accidentally seed
// stats from a brand-new (unloaded) transcript. Subagents rely on this.
func TestNewFreshTranscriptLeavesStatsZero(t *testing.T) {
	tx, err := session.Create(filepath.Join(t.TempDir(), "t.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Close()

	pb := NewPromptBuilder("sys", nil, "m")
	prov := &fakeProvider{}
	lp := New(prov, &fakeExecutor{}, tx, pb)

	if got := lp.Stats(); got != (Stats{}) {
		t.Fatalf("expected zero stats for fresh transcript, got %+v", got)
	}
}
