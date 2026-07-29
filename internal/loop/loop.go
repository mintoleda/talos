package loop

import (
	"context"
	"errors"
	"fmt"
	"sync"

	"github.com/mintoleda/talos/internal/executor"
	"github.com/mintoleda/talos/internal/protocol"
	"github.com/mintoleda/talos/internal/provider"
	"github.com/mintoleda/talos/internal/session"
)

var exclusiveTools = map[string]bool{
	"edit":  true,
	"write": true,
}

type Stats struct {
	Calls               int
	InputTokens         int
	CachedTokens        int
	OutputTokens        int
	CacheCreationTokens int
}

func (s Stats) CacheHitRate() float64 {
	if s.InputTokens == 0 {
		return 0
	}
	return float64(s.CachedTokens) / float64(s.InputTokens)
}

type Loop struct {
	provider   provider.Provider
	executor   executor.Executor
	tx         *session.Transcript
	promptB    *PromptBuilder
	compactor  *session.Compactor
	DebugCache bool

	batchNum int

	// MaxIterations caps how many tool-call round-trips a single turn may
	// make. 0 (the default) means unlimited — the turn runs until the model
	// stops requesting tools or the context is cancelled.
	MaxIterations int

	KillBgOnInterrupt bool

	// SteerFunc is called after tool execution to drain pending steer messages
	// queued by the TUI while the agent was busy. Each element is a single
	// user message ([]ContentBlock for text + optional images). Returning nil
	// means nothing is pending. Called from the loop goroutine, so it must be
	// thread-safe. Messages are injected before the next LLM call — the same
	// pattern as pi's "steer" mechanism.
	SteerFunc func() [][]protocol.ContentBlock

	stats             Stats
	lastPromptTokens  int
}

func New(p provider.Provider, e executor.Executor, tx *session.Transcript, pb *PromptBuilder) *Loop {
	l := &Loop{provider: p, executor: e, tx: tx, promptB: pb}
	// Restore aggregate token usage from the transcript's last stats snapshot.
	// This is a no-op for fresh transcripts (e.g. subagents) and is what makes
	// `/stats` carry across `talos -c` restarts — the previous session's usage
	// would otherwise be invisible after a reload. The accumulator is updated
	// in-place on every turn; restoring here seeds it from disk.
	if tx != nil {
		sr := tx.RestoreStats()
		l.stats = Stats{
			Calls:               sr.Calls,
			InputTokens:         sr.InputTokens,
			CachedTokens:        sr.CachedTokens,
			OutputTokens:        sr.OutputTokens,
			CacheCreationTokens: sr.CacheCreationTokens,
		}
	}
	return l
}

func (l *Loop) Stats() Stats { return l.stats }

func (l *Loop) ResetStats() { l.stats = Stats{} }

func (l *Loop) History() []protocol.FrozenMessage {
	if l.tx == nil {
		return nil
	}
	return l.tx.Frozen()
}

func (l *Loop) Close() {
	if l.tx != nil {
		_ = l.tx.WriteStats(session.StatsRecord{
			Calls:               l.stats.Calls,
			InputTokens:         l.stats.InputTokens,
			CachedTokens:        l.stats.CachedTokens,
			OutputTokens:        l.stats.OutputTokens,
			CacheCreationTokens: l.stats.CacheCreationTokens,
		})
		_ = l.tx.Close()
	}
	if l.executor != nil {
		l.executor.Close()
	}
}

// CompactNow forces a compaction of the oldest conversation chunk, optionally
// guided by a focus message that tells the summarizer what to preserve. It is
// a no-op (returns empty string) when there is nothing to compact. The summary
// text is returned for display.
func (l *Loop) CompactNow(ctx context.Context, focus string) (string, error) {
	if l.compactor == nil {
		return "", fmt.Errorf("no compactor configured")
	}
	return l.compactor.CompactNow(ctx, l.tx, focus)
}

func (l *Loop) SetCompactor(c *session.Compactor) {
	l.compactor = c
}

// SetProvider swaps the LLM provider at runtime. Used by /provider and /model
// commands in the TUI/CLI to switch providers mid-session without losing the
// conversation transcript.
func (l *Loop) SetProvider(p provider.Provider) {
	l.provider = p
}

// SetTranscript swaps in a fresh transcript (e.g. for /new), closing the old one.
// Zone A (system+tools) is unchanged, so the provider's prefix cache for the
// stable portion stays warm even after starting a new conversation.
// Stats are flushed to the old transcript and restored from the new one.
func (l *Loop) SetTranscript(tx *session.Transcript) {
	if l.tx != nil {
		_ = l.tx.WriteStats(session.StatsRecord{
			Calls:               l.stats.Calls,
			InputTokens:         l.stats.InputTokens,
			CachedTokens:        l.stats.CachedTokens,
			OutputTokens:        l.stats.OutputTokens,
			CacheCreationTokens: l.stats.CacheCreationTokens,
		})
		_ = l.tx.Close()
	}
	sr := tx.RestoreStats()
	l.stats = Stats{
		Calls:               sr.Calls,
		InputTokens:         sr.InputTokens,
		CachedTokens:        sr.CachedTokens,
		OutputTokens:        sr.OutputTokens,
		CacheCreationTokens: sr.CacheCreationTokens,
	}
	l.tx = tx
}

// indexedResult pairs a tool result with its original position in the
// assistant's tool_use list so the final tool message can be assembled in
// deterministic order even when tools run concurrently.
type indexedResult struct {
	idx int
	protocol.ContentBlock
}

// runToolsParallel executes all tool calls concurrently, preserving the order
// required by the LLM. If the batch contains more than one exclusive tool
// (edit/write) they are serialised to prevent file-state races.
func (l *Loop) runToolsParallel(ctx context.Context, toolUses []protocol.ToolUse, emit protocol.EmitFunc) ([]protocol.ContentBlock, error) {
	results := make([]indexedResult, len(toolUses))

	if needsSerial(toolUses) {
		for i, tu := range toolUses {
			emit(protocol.ToolStarted{ID: tu.ID, Name: tu.Name, Args: tu.Args})
			res := l.executor.Run(ctx, tu, emit)
			emit(protocol.ToolFinished{ID: tu.ID, Result: res})
			results[i] = indexedResult{
				idx: i,
				ContentBlock: protocol.ContentBlock{
					Type:       protocol.BlockToolResult,
					ToolResult: &res,
				},
			}
			if ctx.Err() != nil {
				break
			}
		}
	} else {
		var wg sync.WaitGroup
		for i, tu := range toolUses {
			emit(protocol.ToolStarted{ID: tu.ID, Name: tu.Name, Args: tu.Args})
			wg.Add(1)
			go func(idx int, tu protocol.ToolUse) {
				defer wg.Done()
				res := l.executor.Run(ctx, tu, emit)
				emit(protocol.ToolFinished{ID: tu.ID, Result: res})
				results[idx] = indexedResult{
					idx: idx,
					ContentBlock: protocol.ContentBlock{
						Type:       protocol.BlockToolResult,
						ToolResult: &res,
					},
				}
			}(i, tu)
		}

		done := make(chan struct{})
		go func() {
			wg.Wait()
			close(done)
		}()

		select {
		case <-done:
		case <-ctx.Done():
			if l.KillBgOnInterrupt {
				l.executor.KillBg()
			}
			<-done
		}
	}

	// Fill in any gaps left by context cancellation that prevented a goroutine
	// from assigning its result.
	blocks := make([]protocol.ContentBlock, len(toolUses))
	for i, tu := range toolUses {
		if results[i].ContentBlock.Type == "" {
			blocks[i] = protocol.ContentBlock{
				Type: protocol.BlockToolResult,
				ToolResult: &protocol.ToolResult{
					ToolUseID: tu.ID,
					Content:   "interrupted before execution",
					IsError:   true,
				},
			}
		} else {
			blocks[i] = results[i].ContentBlock
		}
	}

	if ctx.Err() != nil {
		return blocks, ctx.Err()
	}
	return blocks, nil
}

func needsSerial(toolUses []protocol.ToolUse) bool {
	if len(toolUses) < 2 {
		return false
	}
	seen := make(map[string]int)
	for _, tu := range toolUses {
		if exclusiveTools[tu.Name] {
			path, _ := tu.Args["path"].(string)
			seen[path]++
			if seen[path] > 1 {
				return true
			}
		}
	}
	return false
}

func (l *Loop) RunTurn(ctx context.Context, userInput []protocol.ContentBlock, emit protocol.EmitFunc) error {
	msg := protocol.Message{Role: protocol.RoleUser, Content: userInput}
	if err := l.tx.Append(msg); err != nil {
		return fmt.Errorf("append user message: %w", err)
	}

	// Turn-boundary compaction check: use real prompt tokens from the
	// previous turn's last API call, not an estimate. The active turn's
	// messages are never inside the folded region — this runs before the
	// first LLM call of the turn, after the user message is appended.
	if l.compactor != nil && l.lastPromptTokens > 0 {
		if compacted, err := l.compactor.MaybeCompact(ctx, l.tx, l.lastPromptTokens, l.promptB.ContextLimit()); err != nil {
			emit(protocol.Notice{Level: "error", Text: fmt.Sprintf("compaction failed: %v", err)})
		} else if compacted {
			emit(protocol.Notice{Level: "info", Text: "compaction: folded earlier conversation"})
		}
	}

	var turnUsage protocol.Usage
	for iter := 0; l.MaxIterations == 0 || iter < l.MaxIterations; iter++ {
		if l.SteerFunc != nil && iter > 0 {
			if steerMessages := l.SteerFunc(); len(steerMessages) > 0 {
				for _, blocks := range steerMessages {
					if err := l.tx.Append(protocol.Message{Role: protocol.RoleUser, Content: blocks}); err != nil {
						return fmt.Errorf("append steer message: %w", err)
					}
				}
				emit(protocol.Notice{Level: "info", Text: fmt.Sprintf("✎ steer: %d message(s) injected", len(steerMessages))})
			}
		}

		req := l.promptB.Build(l.tx)
		if l.DebugCache {
			emit(protocol.Notice{Level: "debug", Text: fmt.Sprintf("cache prefix hash: %s", l.promptB.PrefixHash(req))})
		}
		if pct := l.promptB.ContextUsage(req); pct > 0.85 {
			emit(protocol.Notice{Level: "warn", Text: fmt.Sprintf("context %.0f%% full — consider /new", pct*100)})
		}

		estimated := l.promptB.EstimatedTokens(req)
		emit(protocol.PromptEstimate{
			PromptTokens: estimated,
			ContextLimit: l.promptB.ContextLimit(),
		})

		prevShape := l.promptB.Shape(req, l.tx)

		assistant, usage, err := l.streamWithRetry(ctx, req, emit)
		if err != nil {
			return err
		}
		l.promptB.Calibrate(estimated, usage.PromptTokens)

		// Miss attribution: if cache hit rate is unexpectedly low,
		// compare prefix shapes from before/after this turn to guess why.
		if l.DebugCache || (usage.PromptTokens > 0 && float64(usage.CachedPromptTokens)/float64(usage.PromptTokens) < 0.1) {
			curShape := l.promptB.Shape(req, l.tx)
			if cause := l.promptB.CacheMissCause(prevShape, curShape); cause != "" {
				emit(protocol.Notice{Level: "debug", Text: fmt.Sprintf("cache miss cause: %s", cause)})
			}
		}
		l.stats.Calls++
		l.stats.InputTokens += usage.PromptTokens
		l.stats.CachedTokens += usage.CachedPromptTokens
		l.stats.OutputTokens += usage.CompletionTokens
		l.stats.CacheCreationTokens += usage.CacheCreationTokens
		turnUsage.PromptTokens += usage.PromptTokens
		turnUsage.CompletionTokens += usage.CompletionTokens
		turnUsage.CachedPromptTokens += usage.CachedPromptTokens
		turnUsage.CacheCreationTokens += usage.CacheCreationTokens

		if err := l.tx.Append(assistant); err != nil {
			return fmt.Errorf("append assistant message: %w", err)
		}

		toolUses := assistant.ToolUses()
		if len(toolUses) == 0 {
			l.lastPromptTokens = usage.PromptTokens
			emit(protocol.TurnEnded{StopReason: "stop", Usage: turnUsage, ContextTokens: usage.PromptTokens})
			return nil
		}

		if len(toolUses) > 1 {
			l.batchNum++
			emit(protocol.BatchStarted{Num: l.batchNum})
		}

		results, err := l.runToolsParallel(ctx, toolUses, emit)
		if err != nil {
			return err
		}

		if len(toolUses) > 1 {
			emit(protocol.BatchFinished{Num: l.batchNum})
		}
		if err := l.tx.Append(protocol.Message{Role: protocol.RoleTool, Content: results}); err != nil {
			return fmt.Errorf("append tool results: %w", err)
		}
		nextReq := l.promptB.Build(l.tx)
		emit(protocol.PromptEstimate{
			PromptTokens: l.promptB.EstimatedTokens(nextReq),
			ContextLimit: l.promptB.ContextLimit(),
		})
	}
	return errors.New("turn iteration limit exceeded")
}
