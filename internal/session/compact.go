package session

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/mintoleda/talos/internal/protocol"
)

type Summarizer interface {
	Summarize(ctx context.Context, msgs []protocol.Message) (string, error)
	// WithFocus returns a Summarizer that is guided by the given focus message.
	// The focus tells the summarizer what to prioritise preserving. If focus is
	// empty a nil-safe no-op summarizer is returned (the receiver itself).
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

// DropSummarizer replaces the oldest conversation chunk with a constant
// placeholder text. The placeholder is byte-identical across all compactions,
// so the post-compaction prefix is cacheable — same remaining messages after
// the same boundary produce the same prefix hash. Zero cost, zero API calls.
type DropSummarizer struct{}

func (DropSummarizer) Summarize(_ context.Context, _ []protocol.Message) (string, error) {
	return "[Earlier conversation has been summarized to stay within context limits.]", nil
}

func (d DropSummarizer) WithFocus(_ string) Summarizer { return d }

// ExtractSummarizer builds a summary mechanically from the dropped chunk: user
// messages verbatim (they carry the intent and are usually small), assistant
// text trimmed to its first line, tool results dropped, plus a list of file
// paths touched by tool calls. No LLM call, and the output is a pure function
// of the chunk — so like DropSummarizer it is deterministic and reproducible
// on replay, and costs the same single cache miss any compaction incurs.
type ExtractSummarizer struct {
	// MaxUserBytes caps each user message in the extract (0 = 2048).
	MaxUserBytes int
	// MaxTotalBytes caps the whole summary (0 = 8192).
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
	return s[:n] + "…"
}

func firstLine(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	return s
}

// LLMSummarizer uses a provider to summarize a chunk of conversation history.
// It emits no events and blocks until the summary is returned.
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

// Compactor decides when and how to compact the oldest chunk of Zone B into a
// stable summary prefix.
type Compactor struct {
	Summarizer         Summarizer
	Historian          *Historian
	ChunkSize          int
	Threshold          float64
	EmergencyThreshold float64 // above this, compact regardless of chunk size
	KeepTail           int     // most recent messages that are never compacted
	OnCompaction       func() // called after each successful compaction
}

func NewCompactor(s Summarizer) *Compactor {
	return &Compactor{
		Summarizer:         s,
		ChunkSize:          20,
		Threshold:          0.85,
		EmergencyThreshold: 0.95,
		KeepTail:           2,
	}
}

// Clamp snaps compaction thresholds and chunk size to valid ranges.
// Callers that set custom values after NewCompactor should call this afterward.
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
	if c.ChunkSize == 0 {
		c.ChunkSize = 20
	}
	if c.ChunkSize < 5 {
		c.ChunkSize = 5
	}
	if c.ChunkSize > 500 {
		c.ChunkSize = 500
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
}

// alignedChunk returns the oldest chunk of messages whose boundary does not
// split tool-call/result pairs. It extends the target ChunkSize forward to
// include all tool-result messages for any assistant tool_calls in the window.
// This prevents sending incomplete exchanges to providers (e.g. DeepSeek) that
// reject messages with dangling tool_calls.
//
// The chunk never reaches into the last KeepTail messages, so compaction can
// never consume the entire transcript (e.g. the in-flight turn's user message).
// If forward extension would cross that boundary, the boundary shrinks
// backward to the nearest clean break instead — possibly to zero (no chunk).
func (c *Compactor) alignedChunk(frozen []protocol.FrozenMessage) []protocol.FrozenMessage {
	keep := c.KeepTail
	if keep < 1 {
		keep = 1
	}
	maxEnd := len(frozen) - keep
	if maxEnd <= 0 {
		return nil
	}
	target := c.ChunkSize
	if target == 0 {
		target = 20
	}
	end := target
	if end > maxEnd {
		end = maxEnd
	}
	for end < maxEnd && splitsToolPair(frozen, end) {
		end++
	}
	for end > 0 && splitsToolPair(frozen, end) {
		end--
	}
	return frozen[:end]
}

// splitsToolPair reports whether cutting frozen at index end would separate an
// assistant tool_use inside the chunk from its tool_result outside it. A
// tool_use with no result anywhere in the transcript does not count as split.
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
			// The result may exist beyond 'end' — that's a split.
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

// compactChunk summarises the given chunk of frozen messages, persists a
// CompactionRecord, and drops the chunk from the in-memory frozen list. If
// focus is non-empty the summarizer is guided to preserve information about it.
func (c *Compactor) compactChunk(ctx context.Context, tx *Transcript, chunk []protocol.FrozenMessage, focus string) (string, error) {
	msgs := make([]protocol.Message, len(chunk))
	for i, fm := range chunk {
		msgs[i] = fm.Msg
	}
	if c.Historian != nil {
		c.Historian.ExtractAsync(msgs)
	}

	summarizer := c.Summarizer
	if focus != "" {
		summarizer = c.Summarizer.WithFocus(focus)
	}

	summary, err := summarizer.Summarize(ctx, msgs)
	if err != nil {
		return "", fmt.Errorf("compaction summarize: %w", err)
	}

	summaryMsg := protocol.TextMessage(protocol.RoleAssistant, summary)
	summaryRaw, err := json.Marshal(summaryMsg)
	if err != nil {
		return "", err
	}

	chunkIDs := make([]int, len(chunk))
	for i := range chunk {
		chunkIDs[i] = i
	}
	rec := CompactionRecord{
		Type:       "compaction",
		ChunkIDs:   chunkIDs,
		Summary:    summary,
		SummaryAt:  time.Now().UTC(),
		MessageRaw: summaryRaw,
	}
	if err := tx.AppendCompaction(rec); err != nil {
		return "", err
	}
	tx.DropOldest(len(chunk))

	if c.OnCompaction != nil {
		c.OnCompaction()
	}

	return summary, nil
}

// MaybeCompact compacts the oldest chunk if prompt usage exceeds the threshold.
// It mutates the transcript in place: the chunk is removed from the in-memory
// frozen list and a summary message is prepended to the prefix. A compaction
// record is appended to the JSONL.
//
// When usage exceeds EmergencyThreshold, the ChunkSize guard is bypassed so
// even short-but-dense conversations can be compacted to free headroom.
func (c *Compactor) MaybeCompact(ctx context.Context, tx *Transcript, promptTokens, ctxLimit int) (bool, error) {
	if c.Threshold == 0 {
		c.Threshold = 0.85
	}
	if c.ChunkSize == 0 {
		c.ChunkSize = 20
	}
	if ctxLimit == 0 || float64(promptTokens)/float64(ctxLimit) < c.Threshold {
		return false, nil
	}
	frozen := tx.Frozen()

	// Emergency: above EmergencyThreshold, compact whatever we can even if
	// the session is shorter than ChunkSize (e.g. few messages but enormous
	// tool outputs consumed the context).
	emergency := c.EmergencyThreshold > 0 && float64(promptTokens)/float64(ctxLimit) >= c.EmergencyThreshold

	if !emergency && len(frozen) <= c.ChunkSize {
		return false, nil
	}
	chunk := c.alignedChunk(frozen)
	if len(chunk) == 0 {
		return false, nil
	}
	if _, err := c.compactChunk(ctx, tx, chunk, ""); err != nil {
		return false, err
	}
	return true, nil
}

// CompactNow forces compaction of the oldest chunk immediately, regardless of
// context usage. If focus is non-empty it guides the summarizer to preserve
// information related to the focus topic. Returns the summary text, or an error.
func (c *Compactor) CompactNow(ctx context.Context, tx *Transcript, focus string) (string, error) {
	if c.ChunkSize == 0 {
		c.ChunkSize = 20
	}
	frozen := tx.Frozen()
	if len(frozen) == 0 {
		return "", nil
	}
	chunk := c.alignedChunk(frozen)
	if len(chunk) == 0 {
		return "", nil
	}
	return c.compactChunk(ctx, tx, chunk, focus)
}
