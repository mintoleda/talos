package loop

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"

	"github.com/mintoleda/talos/internal/provider"
	"github.com/mintoleda/talos/internal/protocol"
	"github.com/mintoleda/talos/internal/session"
)

type PromptBuilder struct {
	system        string
	tools         []protocol.ToolSchema
	model         string
	ctxLimit      int
	thinkingLevel string
	contextFn     func() string // optional per-turn reminder injected into the last user message
}

func NewPromptBuilder(system string, tools []protocol.ToolSchema, model string) *PromptBuilder {
	return &PromptBuilder{system: system, tools: tools, model: model}
}

func (b *PromptBuilder) SetThinkingLevel(level string) {
	b.thinkingLevel = provider.ClampThinkingLevel(b.model, level)
}

func (b *PromptBuilder) ThinkingLevel() string { return b.thinkingLevel }

// SetContextFn installs a per-turn reminder that is prepended to the last
// user message at request-build time. Used to surface dynamic state (e.g.
// "files read this session") without invalidating the cacheable prefix —
// the last user message is excluded from PrefixHash on purpose, so changes
// here never bust the cache. The function should be cheap and must not
// mutate the transcript.
func (b *PromptBuilder) SetContextFn(fn func() string) {
	b.contextFn = fn
}

func (b *PromptBuilder) Build(tx *session.Transcript) protocol.Request {
	msgs := tx.Frozen()
	if summaries := tx.Summaries(); len(summaries) > 0 {
		combined := make([]protocol.FrozenMessage, 0, len(summaries)+len(msgs))
		combined = append(combined, summaries...)
		combined = append(combined, msgs...)
		msgs = combined
	}
	if b.contextFn != nil {
		// Copy the messages slice so a contextFn injection never mutates the
		// transcript's underlying storage — tx.Frozen() returns the live slice.
		msgs = append([]protocol.FrozenMessage(nil), msgs...)
		if reminder := b.contextFn(); reminder != "" && len(msgs) > 0 {
			last := msgs[len(msgs)-1]
			if last.Msg.Role == protocol.RoleUser {
				// Copy on write: don't mutate the frozen transcript.
				augmented := last
				augmented.Msg = last.Msg
				augmented.Msg.Content = append([]protocol.ContentBlock(nil), last.Msg.Content...)
				prepended := false
				for i, blk := range augmented.Msg.Content {
					if blk.Type == protocol.BlockText {
						augmented.Msg.Content[i].Text = reminder + "\n\n" + blk.Text
						prepended = true
						break
					}
				}
				if !prepended {
					augmented.Msg.Content = append([]protocol.ContentBlock{{
						Type: protocol.BlockText,
						Text: reminder,
					}}, augmented.Msg.Content...)
				}
				// Drop the cached Raw so callers that re-serialise see the
				// new text. PrefixHash still uses the original Raw for older
				// messages, which is what we want.
				augmented.Raw = nil
				msgs[len(msgs)-1] = augmented
			}
		}
	}
	return protocol.Request{
		System:        b.system,
		Tools:         b.tools,
		Messages:      msgs,
		Model:         b.model,
		ThinkingLevel: b.thinkingLevel,
	}
}

func (b *PromptBuilder) ContextUsage(req protocol.Request) float64 {
	if b.ctxLimit == 0 {
		return 0
	}
	return float64(b.EstimatedTokens(req)) / float64(b.ctxLimit)
}

// EstimatedTokens returns a rough token count (1 token ≈ 4 bytes) for the
// prompt. This is intentionally approximate; providers bill by their own
// tokenizers.
func (b *PromptBuilder) EstimatedTokens(req protocol.Request) int {
	approx := len(req.System)
	for _, t := range req.Tools {
		approx += len(t.Description) + len(t.Parameters)
	}
	for _, m := range req.Messages {
		approx += len(m.Raw)
	}
	return approx / 4
}

func (b *PromptBuilder) ContextLimit() int { return b.ctxLimit }

func (b *PromptBuilder) PrefixHash(req protocol.Request) string {
	h := sha256.New()
	if req.System != "" {
		h.Write([]byte(req.System))
	}
	for _, t := range req.Tools {
		h.Write([]byte(t.Name))
		h.Write(t.Parameters)
	}
	// Exclude the message appended this turn so the hash reflects only the stable
	// cacheable prefix; on a healthy session it must not change between turns.
	prefix := req.Messages
	if len(prefix) > 0 {
		prefix = prefix[:len(prefix)-1]
	}
	for _, m := range prefix {
		if len(m.Raw) > 0 {
			h.Write(m.Raw)
		} else {
			data, _ := json.Marshal(m.Msg)
			h.Write(data)
		}
	}
	return hex.EncodeToString(h.Sum(nil))[:16]
}

func (b *PromptBuilder) Model() string { return b.model }

// SetContextLimit drives compaction thresholds and context-usage warnings.
// Should be set from the pricing table after construction.
func (b *PromptBuilder) SetContextLimit(n int) {
	if n > 0 {
		b.ctxLimit = n
	}
}

func (b *PromptBuilder) SetModel(model string) {
	if model != "" {
		b.model = model
	}
}
