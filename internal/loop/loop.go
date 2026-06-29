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

type Stats struct {
	Calls        int
	InputTokens  int
	CachedTokens int
	OutputTokens int
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

	// SteerFunc is called after tool execution to drain pending steer messages
	// queued by the TUI while the agent was busy. Each element is a single
	// user message ([]ContentBlock for text + optional images). Returning nil
	// means nothing is pending. Called from the loop goroutine, so it must be
	// thread-safe. Messages are injected before the next LLM call — the same
	// pattern as pi's "steer" mechanism.
	SteerFunc func() [][]protocol.ContentBlock

	stats Stats
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
			Calls:        sr.Calls,
			InputTokens:  sr.InputTokens,
			CachedTokens: sr.CachedTokens,
			OutputTokens: sr.OutputTokens,
		}
	}
	return l
}

func (l *Loop) Stats() Stats { return l.stats }

func (l *Loop) ResetStats() { l.stats = Stats{} }

// Close flushes aggregate stats to the transcript before closing.
func (l *Loop) Close() {
	if l.tx != nil {
		_ = l.tx.WriteStats(session.StatsRecord{
			Calls:        l.stats.Calls,
			InputTokens:  l.stats.InputTokens,
			CachedTokens: l.stats.CachedTokens,
			OutputTokens: l.stats.OutputTokens,
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
			Calls:        l.stats.Calls,
			InputTokens:  l.stats.InputTokens,
			CachedTokens: l.stats.CachedTokens,
			OutputTokens: l.stats.OutputTokens,
		})
		_ = l.tx.Close()
	}
	sr := tx.RestoreStats()
	l.stats = Stats{
		Calls:        sr.Calls,
		InputTokens:  sr.InputTokens,
		CachedTokens: sr.CachedTokens,
		OutputTokens: sr.OutputTokens,
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
// required by the LLM. If the context is cancelled before a tool goroutine
// starts, that tool returns a placeholder error.
func (l *Loop) runToolsParallel(ctx context.Context, toolUses []protocol.ToolUse, emit protocol.EmitFunc) ([]protocol.ContentBlock, error) {
	results := make([]indexedResult, len(toolUses))
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
		<-done // wait for partial results
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

func (l *Loop) RunTurn(ctx context.Context, userInput []protocol.ContentBlock, emit protocol.EmitFunc) error {
	msg := protocol.Message{Role: protocol.RoleUser, Content: userInput}
	if err := l.tx.Append(msg); err != nil {
		return fmt.Errorf("append user message: %w", err)
	}

	for iter := 0; l.MaxIterations == 0 || iter < l.MaxIterations; iter++ {
		// Check for steering messages submitted while the agent was busy
		// (e.g. during tool execution or streaming). They are injected
		// into the transcript before the next LLM call, just like pi's
		// "steer" mechanism — messages queued during a turn become
		// additional user context on the next reasoning step.
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
		if l.compactor != nil {
			req := l.promptB.Build(l.tx)
			tokens := l.promptB.EstimatedTokens(req)
			if compacted, err := l.compactor.MaybeCompact(ctx, l.tx, tokens, l.promptB.ContextLimit()); err != nil {
				emit(protocol.Notice{Level: "error", Text: fmt.Sprintf("compaction failed: %v", err)})
			} else if compacted {
				emit(protocol.Notice{Level: "info", Text: "compaction: summarized oldest chunk"})
			}
		}
		req := l.promptB.Build(l.tx)
		if l.DebugCache {
			emit(protocol.Notice{Level: "debug", Text: fmt.Sprintf("cache prefix hash: %s", l.promptB.PrefixHash(req))})
		}
		if pct := l.promptB.ContextUsage(req); pct > 0.85 {
			emit(protocol.Notice{Level: "warn", Text: fmt.Sprintf("context %.0f%% full — consider /new", pct*100)})
		}

		emit(protocol.PromptEstimate{
			PromptTokens: l.promptB.EstimatedTokens(req),
			ContextLimit: l.promptB.ContextLimit(),
		})

		assistant, usage, err := l.streamWithRetry(ctx, req, emit)
		if err != nil {
			// Includes ctx.Canceled on user interrupt; the caller distinguishes it.
			return err
		}
		l.stats.Calls++
		l.stats.InputTokens += usage.PromptTokens
		l.stats.CachedTokens += usage.CachedPromptTokens
		l.stats.OutputTokens += usage.CompletionTokens

		if err := l.tx.Append(assistant); err != nil {
			return fmt.Errorf("append assistant message: %w", err)
		}

		toolUses := assistant.ToolUses()
		if len(toolUses) == 0 {
			emit(protocol.TurnEnded{StopReason: "stop", Usage: usage})
			return nil
		}

		l.batchNum++
		emit(protocol.BatchStarted{Num: l.batchNum})

		results, err := l.runToolsParallel(ctx, toolUses, emit)
		if err != nil {
			return err
		}

		emit(protocol.BatchFinished{Num: l.batchNum})
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
