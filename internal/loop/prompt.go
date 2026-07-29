package loop

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"sort"
	"strings"

	"github.com/mintoleda/talos/internal/provider"
	"github.com/mintoleda/talos/internal/protocol"
	"github.com/mintoleda/talos/internal/session"
)

// PrefixShape decomposes the cacheable prefix so the loop can attribute
// a cache miss to system, tools, or history-rewrite.
type PrefixShape struct {
	SystemHash string `json:"system"`
	ToolsHash  string `json:"tools"`
	MsgHash    string `json:"messages"`
	RewriteVer int    `json:"rewrite_ver"`
}

type PromptBuilder struct {
	system             string
	tools              []protocol.ToolSchema
	model              string
	ctxLimit           int
	thinkingLevel      string
	contextFn          func() string
	permissionModeText string
	calibration        float64

	subagentListing   string
	subagentToolNames map[string]bool
	subagentEnabled   bool

	lastShape PrefixShape
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

// EstimatedTokens returns a rough token count for the prompt, starting from
// the 1 token ≈ 4 bytes heuristic and scaled by the ratio observed between
// past estimates and the provider's actual prompt_tokens (see Calibrate).
func (b *PromptBuilder) EstimatedTokens(req protocol.Request) int {
	approx := len(req.System)
	for _, t := range req.Tools {
		approx += len(t.Description) + len(t.Parameters)
	}
	for _, m := range req.Messages {
		approx += len(m.Raw)
	}
	ratio := b.calibration
	if ratio == 0 {
		ratio = 1
	}
	return int(float64(approx) / 4 * ratio)
}

// Calibrate adjusts the estimator with the actual prompt token count the
// provider reported for a request whose estimate was estimated. The correction
// is smoothed and clamped so one odd data point can't skew the gauge.
func (b *PromptBuilder) Calibrate(estimated, actual int) {
	if estimated < 500 || actual < 500 {
		return
	}
	cur := b.calibration
	if cur == 0 {
		cur = 1
	}
	next := cur * (0.5 + 0.5*float64(actual)/float64(estimated))
	next = min(max(next, 0.25), 4)
	b.calibration = next
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

// Shape returns a decomposed prefix shape suitable for cross-turn
// comparison. SystemHash covers the system prompt; ToolsHash covers the
// sorted tool schemas; MsgHash covers the stable message prefix (all
// messages except the last); RewriteVer is the transcript's rewrite
// version counter (bumped by compactions/snips/prunes).
func (b *PromptBuilder) Shape(req protocol.Request, tx *session.Transcript) PrefixShape {
	sh := sha256.New()
	if req.System != "" {
		sh.Write([]byte(req.System))
	}
	systemHash := hex.EncodeToString(sh.Sum(nil))[:12]

	th := sha256.New()
	sorted := make([]protocol.ToolSchema, len(req.Tools))
	copy(sorted, req.Tools)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].Name < sorted[j].Name })
	for _, t := range sorted {
		th.Write([]byte(t.Name))
		th.Write(t.Parameters)
	}
	toolsHash := hex.EncodeToString(th.Sum(nil))[:12]

	mh := sha256.New()
	prefix := req.Messages
	if len(prefix) > 0 {
		prefix = prefix[:len(prefix)-1]
	}
	for _, m := range prefix {
		if len(m.Raw) > 0 {
			mh.Write(m.Raw)
		} else {
			data, _ := json.Marshal(m.Msg)
			mh.Write(data)
		}
	}
	msgHash := hex.EncodeToString(mh.Sum(nil))[:12]

	rw := 0
	if tx != nil {
		rw = tx.RewriteVersion()
	}
	return PrefixShape{
		SystemHash: systemHash,
		ToolsHash:  toolsHash,
		MsgHash:    msgHash,
		RewriteVer: rw,
	}
}

// CacheMissCause compares the previous prefix shape with the current one
// and reports the most likely cause of a cache miss. Returns "" when
// shapes are identical (the miss is unexplained, or cache simply expired).
func (b *PromptBuilder) CacheMissCause(prev, cur PrefixShape) string {
	if prev.SystemHash != "" && cur.SystemHash != "" && prev.SystemHash != cur.SystemHash {
		return "system"
	}
	if prev.ToolsHash != "" && cur.ToolsHash != "" && prev.ToolsHash != cur.ToolsHash {
		return "tools"
	}
	if prev.RewriteVer != cur.RewriteVer {
		return "history_rewrite"
	}
	if prev.MsgHash != "" && cur.MsgHash != "" && prev.MsgHash != cur.MsgHash {
		return "history_rewrite"
	}
	if prev.SystemHash == cur.SystemHash && prev.ToolsHash == cur.ToolsHash && prev.MsgHash == cur.MsgHash {
		return ""
	}
	return "unknown"
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
