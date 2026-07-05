package loop

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strings"

	"github.com/mintoleda/talos/internal/provider"
	"github.com/mintoleda/talos/internal/protocol"
	"github.com/mintoleda/talos/internal/session"
)

type PromptBuilder struct {
	system             string
	tools              []protocol.ToolSchema
	model              string
	ctxLimit           int
	thinkingLevel      string
	contextFn          func() string
	permissionModeText string

	// subagentListing is the "## Subagents you can delegate to" section,
	// conditionally included in the system prompt at Build time.
	subagentListing   string
	subagentToolNames map[string]bool
	subagentEnabled   bool // default true when SetSubagentData has been called
}

func NewPromptBuilder(system string, tools []protocol.ToolSchema, model string) *PromptBuilder {
	return &PromptBuilder{system: system, tools: tools, model: model}
}

func (b *PromptBuilder) SetThinkingLevel(level string) {
	b.thinkingLevel = provider.ClampThinkingLevel(b.model, level)
}

// SetSubagentData stores the subagent listing and the set of subagent tool
// names so the builder can conditionally exclude them from the tool schema and
// system prompt when subagents are disabled at runtime.
func (b *PromptBuilder) SetSubagentData(listing string, toolNames map[string]bool) {
	b.subagentListing = listing
	b.subagentToolNames = toolNames
	b.subagentEnabled = true
}

// SetSubagentEnabled enables or disables subagent inclusion in the built request.
func (b *PromptBuilder) SetSubagentEnabled(enabled bool) {
	b.subagentEnabled = enabled
}

// SubagentEnabled reports whether subagents are currently enabled.
func (b *PromptBuilder) SubagentEnabled() bool { return b.subagentEnabled }

func (b *PromptBuilder) ThinkingLevel() string { return b.thinkingLevel }

// SetContextFn installs a per-turn reminder that is surfaced via
// Request.Volatile at request-build time. Used to surface dynamic state
// (e.g. "files read this session") without invalidating the cacheable
// prefix — Volatile is rendered outside any cache breakpoint, so changes
// here never bust the cache and the transcript itself is never mutated.
// The function should be cheap and must not mutate the transcript.
func (b *PromptBuilder) SetContextFn(fn func() string) {
	b.contextFn = fn
}

// SetPermissionModeText sets a brief description of the current permission
// mode that is surfaced via Request.Volatile so the model knows how its tool
// calls will be handled. Since Volatile is rendered outside any cache
// breakpoint, it never busts the cacheable prefix.
func (b *PromptBuilder) SetPermissionModeText(text string) {
	b.permissionModeText = text
}

func (b *PromptBuilder) Build(tx *session.Transcript) protocol.Request {
	// Build tools list, conditionally filtering out subagent spawn tools.
	var schemas []protocol.ToolSchema
	for _, t := range b.tools {
		if !b.subagentEnabled && b.subagentToolNames[t.Name] {
			continue
		}
		schemas = append(schemas, t)
	}

	// Build system prompt, conditionally stripping the subagent listing.
	system := b.system
	if !b.subagentEnabled && b.subagentListing != "" {
		system = strings.Replace(system, b.subagentListing, "", 1)
	}

	msgs := tx.Frozen()
	if summaries := tx.Summaries(); len(summaries) > 0 {
		combined := make([]protocol.FrozenMessage, 0, len(summaries)+len(msgs))
		combined = append(combined, summaries...)
		combined = append(combined, msgs...)
		msgs = combined
	}
	var volatile []protocol.ContentBlock
	if len(msgs) > 0 {
		var reminders []string
		if b.permissionModeText != "" {
			reminders = append(reminders, b.permissionModeText)
		}
		if b.contextFn != nil {
			if r := b.contextFn(); r != "" {
				reminders = append(reminders, r)
			}
		}
		if len(reminders) > 0 {
			volatile = append(volatile, protocol.ContentBlock{
				Type: protocol.BlockText,
				Text: strings.Join(reminders, "\n\n"),
			})
		}
	}
	return protocol.Request{
		System:        system,
		Tools:         schemas,
		Messages:      msgs,
		Volatile:      volatile,
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
