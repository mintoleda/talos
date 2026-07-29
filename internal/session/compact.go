package session

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/mintoleda/talos/internal/protocol"
)

type Summarizer interface {
	Summarize(ctx context.Context, msgs []protocol.Message) (string, error)
	WithFocus(focus string) Summarizer
}

type SummarizerFunc func(context.Context, []protocol.Message) (string, error)

func (f SummarizerFunc) Summarize(ctx context.Context, msgs []protocol.Message) (string, error) {
	return f(ctx, msgs)
}

func (f SummarizerFunc) WithFocus(focus string) Summarizer {
	if focus == "" {
		return f
	}
	return SummarizerFunc(func(ctx context.Context, msgs []protocol.Message) (string, error) {
		focused := make([]protocol.Message, 0, len(msgs)+1)
		focused = append(focused, protocol.TextMessage(protocol.RoleUser,
			"When summarising, please prioritise information related to: "+focus))
		focused = append(focused, msgs...)
		return f(ctx, focused)
	})
}

type DropSummarizer struct{}

func (DropSummarizer) Summarize(_ context.Context, _ []protocol.Message) (string, error) {
	return "[Earlier conversation has been summarized to stay within context limits.]", nil
}

func (d DropSummarizer) WithFocus(_ string) Summarizer { return d }

type ExtractSummarizer struct {
	MaxUserBytes  int
	MaxTotalBytes int
}

func (e ExtractSummarizer) WithFocus(_ string) Summarizer { return e }

func (e ExtractSummarizer) Summarize(_ context.Context, msgs []protocol.Message) (string, error) {
	maxUser := e.MaxUserBytes
	if maxUser <= 0 {
		maxUser = 2048
	}
	maxTotal := e.MaxTotalBytes
	if maxTotal <= 0 {
		maxTotal = 8192
	}

	var b strings.Builder
	b.WriteString("[Earlier conversation compacted. Extract of dropped messages:]\n")
	var paths []string
	seen := map[string]bool{}
	for _, m := range msgs {
		for _, block := range m.Content {
			switch {
			case block.Type == protocol.BlockText && m.Role == protocol.RoleUser:
				text := truncate(block.Text, maxUser)
				if strings.TrimSpace(text) != "" {
					b.WriteString("user: " + text + "\n")
				}
			case block.Type == protocol.BlockText && m.Role == protocol.RoleAssistant:
				line := firstLine(block.Text)
				if line != "" {
					b.WriteString("assistant: " + truncate(line, 200) + "\n")
				}
			case block.Type == protocol.BlockToolUse && block.ToolUse != nil:
				if p, ok := block.ToolUse.Args["path"].(string); ok && p != "" && !seen[p] {
					seen[p] = true
					paths = append(paths, p)
				}
			}
		}
		if b.Len() >= maxTotal {
			break
		}
	}
	if len(paths) > 0 && b.Len() < maxTotal {
		b.WriteString("files touched: " + strings.Join(paths, ", ") + "\n")
	}
	return truncate(b.String(), maxTotal), nil
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "\u2026"
}

func firstLine(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	return s
}

type LLMSummarizer struct {
	Provider provider
	Model    string
	System   string
}

type provider interface {
	StreamTurn(ctx context.Context, req protocol.Request) (<-chan protocol.ProviderEvent, error)
}

func NewLLMSummarizer(p provider, model, system string) *LLMSummarizer {
	if system == "" {
		system = "You are a summarizer. Condense the following conversation into a single concise paragraph that preserves the user's intent, key facts, and decisions. Do not add commentary."
	}
	return &LLMSummarizer{Provider: p, Model: model, System: system}
}

func (s *LLMSummarizer) WithFocus(focus string) Summarizer {
	if focus == "" {
		return s
	}
	ns := *s
	ns.System = s.System + "\n\nThe user has asked to prioritise the following in the summary: " + focus
	return &ns
}

func (s *LLMSummarizer) Summarize(ctx context.Context, msgs []protocol.Message) (string, error) {
	frozen := make([]protocol.FrozenMessage, len(msgs))
	for i, m := range msgs {
		raw, err := json.Marshal(m)
		if err != nil {
			return "", err
		}
		frozen[i] = protocol.FrozenMessage{Msg: m, Raw: raw}
	}
	req := protocol.Request{
		System:   s.System,
		Messages: frozen,
		Model:    s.Model,
	}
	stream, err := s.Provider.StreamTurn(ctx, req)
	if err != nil {
		return "", err
	}
	var summary string
	for ev := range stream {
		switch e := ev.(type) {
		case protocol.PEText:
			summary += e.Text
		case protocol.PEError:
			return "", e.Err
		}
	}
	if summary == "" {
		return "(no summary)", nil
	}
	return summary, nil
}

// Compactor decides when and how to fold the oldest conversation into a
// stable summary prefix.
type Compactor struct {
	Summarizer         Summarizer
	Historian          *Historian
	Threshold          float64
	EmergencyThreshold float64 // above this, compact regardless of fold size
	SnipRatio          float64 // triggers snip pass (~0.60); 0 = disable
	KeepTail           int     // most recent messages that are never compacted
	TailBudgetTokens   int     // token budget for kept tail (0 = 16384)
	FoldMinTokens      int     // minimum tokens in fold region to trigger (0 = 400)
	OnCompaction       func()  // called after each successful compaction

	// Anti-loop latch: if compaction fires on consecutive checks while
	// prompt stays above threshold, pause auto-compaction.
	latchCount          int
	latchedPromptTokens int
}

func NewCompactor(s Summarizer) *Compactor {
	return &Compactor{
		Summarizer:         s,
		Threshold:          0.85,
		EmergencyThreshold: 0.95,
		SnipRatio:          0.60,
		KeepTail:           2,
		TailBudgetTokens:   16384,
		FoldMinTokens:      400,
	}
}

func (c *Compactor) Clamp() {
	if c.Threshold == 0 {
		c.Threshold = 0.85
	}
	if c.Threshold < 0.1 {
		c.Threshold = 0.1
	}
	if c.Threshold > 1.0 {
		c.Threshold = 1.0
	}
	if c.EmergencyThreshold == 0 {
		c.EmergencyThreshold = 0.95
	}
	if c.EmergencyThreshold < c.Threshold {
		c.EmergencyThreshold = c.Threshold
	}
	if c.EmergencyThreshold > 1.0 {
		c.EmergencyThreshold = 1.0
	}
	if c.KeepTail == 0 {
		c.KeepTail = 2
	}
	if c.KeepTail < 1 {
		c.KeepTail = 1
	}
	if c.KeepTail > 50 {
		c.KeepTail = 50
	}
	if c.TailBudgetTokens <= 0 {
		c.TailBudgetTokens = 16384
	}
	if c.FoldMinTokens <= 0 {
		c.FoldMinTokens = 400
	}
	if c.SnipRatio <= 0 {
		c.SnipRatio = 0.60
	}
	if c.SnipRatio < 0.1 {
		c.SnipRatio = 0.1
	}
	if c.SnipRatio > c.Threshold {
		c.SnipRatio = c.Threshold
	}
}

func estimateTokens(fms []protocol.FrozenMessage) int {
	n := 0
	for _, fm := range fms {
		n += len(fm.Raw) / 4
	}
	return n
}

// foldTail finds the boundary index where everything before it is foldable
// (older than the token-budgeted tail). It walks backward, accumulating
// estimated token sizes from len(Raw)/4, stops when the tail budget is met,
// then aligns the boundary so tool-call/result pairs are not split.
// Returns 0 when nothing should be folded (e.g. everything fits in tail).
func (c *Compactor) foldTail(frozen []protocol.FrozenMessage, tailBudget int) int {
	if len(frozen) == 0 {
		return 0
	}
	keep := c.KeepTail
	if keep < 1 {
		keep = 1
	}
	if len(frozen) <= keep {
		return 0
	}
	maxEnd := len(frozen) - keep
	acc := 0
	boundary := len(frozen)
	for i := len(frozen) - 1; i >= keep && acc < tailBudget; i-- {
		acc += len(frozen[i].Raw) / 4
		boundary = i
	}
	if boundary > maxEnd {
		boundary = maxEnd
	}
	for boundary < maxEnd && splitsToolPair(frozen, boundary) {
		boundary++
	}
	for boundary > 0 && splitsToolPair(frozen, boundary) {
		boundary--
	}
	return boundary
}

// splitsToolPair reports whether cutting frozen at index end would separate an
// assistant tool_use inside the chunk from its tool_result outside it.
func splitsToolPair(frozen []protocol.FrozenMessage, end int) bool {
	for i := 0; i < end; i++ {
		if frozen[i].Msg.Role != protocol.RoleAssistant {
			continue
		}
		for _, block := range frozen[i].Msg.Content {
			if block.Type != protocol.BlockToolUse || block.ToolUse == nil {
				continue
			}
			found := false
			for j := i + 1; j < end && !found; j++ {
				if frozen[j].Msg.Role != protocol.RoleTool {
					continue
				}
				for _, b := range frozen[j].Msg.Content {
					if b.Type == protocol.BlockToolResult && b.ToolResult != nil &&
						b.ToolResult.ToolUseID == block.ToolUse.ID {
						found = true
						break
					}
				}
			}
			if found {
				continue
			}
			for j := end; j < len(frozen); j++ {
				if frozen[j].Msg.Role != protocol.RoleTool {
					continue
				}
				for _, b := range frozen[j].Msg.Content {
					if b.Type == protocol.BlockToolResult && b.ToolResult != nil &&
						b.ToolResult.ToolUseID == block.ToolUse.ID {
						return true
					}
				}
			}
		}
	}
	return false
}

func isSmallUserMessage(fm protocol.FrozenMessage, maxTokens int) bool {
	if fm.Msg.Role != protocol.RoleUser {
		return false
	}
	return len(fm.Raw)/4 <= maxTokens
}

// pinMessages extracts messages from the fold region that should be kept
// verbatim: the first small user turn (<1500 tok) and any other small user
// turns. Returns (pinned, remaining). Both slices share order with input.
func (c *Compactor) pinMessages(frozen []protocol.FrozenMessage) (pinned, remaining []protocol.FrozenMessage) {
	if len(frozen) == 0 {
		return nil, nil
	}

	pinnedSet := make(map[int]bool)

	firstUserIdx := -1
	for i, fm := range frozen {
		if fm.Msg.Role == protocol.RoleUser {
			firstUserIdx = i
			break
		}
	}
	if firstUserIdx >= 0 && isSmallUserMessage(frozen[firstUserIdx], 1500) {
		pinnedSet[firstUserIdx] = true
	}

	for i, fm := range frozen {
		if i == firstUserIdx {
			continue
		}
		if isSmallUserMessage(fm, 1500) {
			pinnedSet[i] = true
		}
	}

	pinned = make([]protocol.FrozenMessage, 0, len(pinnedSet))
	remaining = make([]protocol.FrozenMessage, 0, len(frozen)-len(pinnedSet))
	for i, fm := range frozen {
		if pinnedSet[i] {
			pinned = append(pinned, fm)
		} else {
			remaining = append(remaining, fm)
		}
	}
	return
}

// archiveFolded writes the dropped raw messages to a JSONL sidecar next to
// the transcript before they are removed, so history stays traceable.
func archiveFolded(tx *Transcript, dropped []protocol.FrozenMessage) error {
	archivePath := tx.Path() + ".archive.jsonl"
	if err := os.MkdirAll(filepath.Dir(archivePath), 0o755); err != nil {
		return err
	}
	f, err := os.OpenFile(archivePath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	defer f.Close()
	rec := struct {
		Type      string    `json:"type"`
		Timestamp time.Time `json:"timestamp"`
		Messages  []json.RawMessage `json:"messages"`
	}{
		Type:      "fold_archive",
		Timestamp: time.Now().UTC(),
		Messages:  make([]json.RawMessage, len(dropped)),
	}
	for i, fm := range dropped {
		rec.Messages[i] = fm.Raw
	}
	data, err := json.Marshal(rec)
	if err != nil {
		return err
	}
	data = append(data, '\n')
	_, err = f.Write(data)
	return err
}

// performFold summarises the non-pinned messages, archives the originals,
// and rewrites the transcript: pinned messages are prepended to summaries,
// the summary replaces the non-pinned region, and the tail stays in frozen.
// If summarization fails, falls back to a mechanical drop placeholder.
func (c *Compactor) performFold(ctx context.Context, tx *Transcript, foldRegion, toSummarize, pinned []protocol.FrozenMessage, tail []protocol.FrozenMessage) (string, error) {
	if len(foldRegion) == 0 {
		return "", nil
	}

	// Archive all dropped messages before rewriting anything.
	if err := archiveFolded(tx, foldRegion); err != nil {
		return "", fmt.Errorf("archive fold: %w", err)
	}

	// Run historian on the foldable region.
	if c.Historian != nil && len(foldRegion) > 0 {
		msgs := make([]protocol.Message, len(foldRegion))
		for i, fm := range foldRegion {
			msgs[i] = fm.Msg
		}
		c.Historian.ExtractAsync(msgs)
	}

	var summary string
	var err error
	if len(toSummarize) > 0 {
		summarizeMsgs := make([]protocol.Message, len(toSummarize))
		for i, fm := range toSummarize {
			summarizeMsgs[i] = fm.Msg
		}
		summary, err = c.Summarizer.Summarize(ctx, summarizeMsgs)
		if err != nil {
			// Fallback: mechanical drop placeholder.
			summary = fmt.Sprintf("[Earlier conversation has been folded to stay within context limits. %d messages archived.]", len(foldRegion))
			err = nil
		}
	} else {
		summary = fmt.Sprintf("[%d earlier messages folded; archived.]", len(foldRegion))
	}

	// Build the canonical summary message.
	summaryMsg := protocol.TextMessage(protocol.RoleAssistant, summary)
	summaryRaw, err := json.Marshal(summaryMsg)
	if err != nil {
		return "", err
	}

	// Pin messages: write each as its own frozen message to summaries.
	for _, fm := range pinned {
		if err := tx.PrependFrozenMessage(fm); err != nil {
			return "", err
		}
	}

	rec := CompactionRecord{
		Type:       "compaction",
		Summary:    summary,
		SummaryAt:  time.Now().UTC(),
		MessageRaw: summaryRaw,
	}
	if err := tx.AppendCompaction(rec); err != nil {
		return "", err
	}

	// Drop the entire fold region from the frozen list.
	tx.DropOldest(len(foldRegion))

	if c.OnCompaction != nil {
		c.OnCompaction()
	}

	return summary, nil
}

// MaybeCompact attempts to reclaim context by first snipping/pruning stale
// tool results, then folding old history into a summary prefix if still needed.
// Must be called at turn boundaries (not in per-iteration tool loops) using
// real prompt tokens from the last API call.
func (c *Compactor) MaybeCompact(ctx context.Context, tx *Transcript, promptTokens, ctxLimit int) (bool, error) {
	if c.Threshold == 0 {
		c.Threshold = 0.85
	}

	// Phase 2 — Snip/prune tier: free context reclamation before paying
	// for a summarization. Runs at SnipRatio (~60%) and above.
	if snipped, _ := c.SnipPrune(tx, promptTokens, ctxLimit); snipped {
		// Snip/prune itself is a cache-reset pass. Re-read usage after
		// it. If pruning alone dropped us below threshold, skip the fold.
		newFrozen := tx.Frozen()
		newEstimate := estimateTokens(newFrozen)
		if ctxLimit > 0 && float64(newEstimate)/float64(ctxLimit) < c.Threshold {
			c.latchCount = 0
			c.latchedPromptTokens = 0
			return true, nil
		}
	}

	if ctxLimit == 0 || float64(promptTokens)/float64(ctxLimit) < c.Threshold {
		c.latchCount = 0
		c.latchedPromptTokens = 0
		return false, nil
	}

	// Anti-loop latch: if prompt stayed above threshold after two
	// consecutive folds, pause auto-compaction until it drops below.
	if c.latchCount >= 2 && promptTokens >= c.latchedPromptTokens {
		return false, nil
	}

	tailBudget := c.TailBudgetTokens
	if tailBudget <= 0 {
		tailBudget = 16384
	}
	if ctxLimit > 0 {
		halfLimit := ctxLimit / 2
		if tailBudget > halfLimit {
			tailBudget = halfLimit
		}
	}

	frozen := tx.Frozen()
	boundary := c.foldTail(frozen, tailBudget)
	if boundary == 0 {
		// Even without a tail, we have nothing meaningful to fold.
		// If emergency, try harder: fold everything except KeepTail.
		emergency := c.EmergencyThreshold > 0 && float64(promptTokens)/float64(ctxLimit) >= c.EmergencyThreshold
		if emergency {
			rawBoundary := len(frozen) - c.KeepTail
			if rawBoundary <= 0 {
				return false, nil
			}
			boundary = rawBoundary
		} else {
			return false, nil
		}
	}

	foldRegion := frozen[:boundary]
	pinned, toSummarize := c.pinMessages(foldRegion)

	emergency := c.EmergencyThreshold > 0 && float64(promptTokens)/float64(ctxLimit) >= c.EmergencyThreshold

	foldMin := c.FoldMinTokens
	if foldMin <= 0 {
		foldMin = 400
	}
	foldableTokens := estimateTokens(toSummarize)
	if !emergency && foldableTokens < foldMin {
		// Not worth the summarization call for such a small region.
		return false, nil
	}

	_, err := c.performFold(ctx, tx, foldRegion, toSummarize, pinned, frozen[boundary:])
	if err != nil {
		return false, err
	}

	// Update latch: if still above threshold, increment.
	if float64(promptTokens)/float64(ctxLimit) >= c.Threshold {
		c.latchCount++
		c.latchedPromptTokens = promptTokens
	} else {
		c.latchCount = 0
		c.latchedPromptTokens = 0
	}

	return true, nil
}

// CompactNow forces a fold immediately, regardless of context usage.
// If focus is non-empty it guides the summarizer.
func (c *Compactor) CompactNow(ctx context.Context, tx *Transcript, focus string) (string, error) {
	frozen := tx.Frozen()
	if len(frozen) == 0 {
		return "", nil
	}

	// Manual /compact folds the entire transcript as a single fold.
	boundary := len(frozen) - c.KeepTail
	if boundary <= 0 {
		return "", nil
	}
	foldRegion := frozen[:boundary]
	pinned, toSummarize := c.pinMessages(foldRegion)

	if focus != "" {
		origSummarizer := c.Summarizer
		c.Summarizer = c.Summarizer.WithFocus(focus)
		defer func() { c.Summarizer = origSummarizer }()
	}

	return c.performFold(ctx, tx, foldRegion, toSummarize, pinned, frozen[boundary:])
}
