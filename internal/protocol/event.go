package protocol

type Event interface{ isEvent() }

type BatchStarted struct {
	Num int `json:"num"`
}
type BatchFinished struct {
	Num int `json:"num"`
}

type TextDelta struct {
	Text string `json:"text"`
}
type ToolStarted struct {
	ID   string         `json:"id"`
	Name string         `json:"name"`
	Args map[string]any `json:"args"`
}
type ToolFinished struct {
	ID     string     `json:"id"`
	Result ToolResult `json:"result"`
}

// ToolOutputDelta carries a chunk of live output from a running tool.
// The TUI accumulates these into a provisional tool segment so the user
// sees output appear in real-time when Ctrl+O is toggled.
type ToolOutputDelta struct {
	ID   string `json:"id"`   // matches ToolStarted/ToolFinished ID
	Text string `json:"text"` // a single chunk of output
}
type Notice struct {
	Level string `json:"level"`
	Text  string `json:"text"`
}
type TurnEnded struct {
	StopReason string `json:"stop_reason"`
	Usage      Usage  `json:"usage"`
}

// PermissionRequested is emitted by the executor when a tool needs human
// confirmation. The handler must send exactly one value to ReplyCh: true to
// allow, false to deny. The channel is owned by whoever creates the event.
type PermissionRequested struct {
	ToolName string      `json:"tool_name"`
	Command  string      `json:"command"`
	Reason   string      `json:"reason"`
	ReplyCh  chan<- bool `json:"-"`
}

// SubagentStarted is emitted when the primary agent (or a subagent) spawns a
// subagent via a spawn tool. ID uniquely identifies this run; Agent is the
// agent definition's name (e.g. "scout"); Task is the delegated instruction.
type SubagentStarted struct {
	ID    string `json:"id"`
	Agent string `json:"agent"`
	Task  string `json:"task"`
}

// SubagentEvent wraps an event emitted by a running subagent's own loop. The
// Inner event (TextDelta, ToolStarted, ToolFinished, Notice, …) is tagged with
// the subagent's ID and name so the frontend can route it to that subagent's
// view instead of the primary chat — preserving the primary agent's context
// isolation. Permission requests are NOT wrapped: they pass through unwrapped
// so the existing approval dialog handles them.
type SubagentEvent struct {
	ID    string `json:"id"`
	Agent string `json:"agent"`
	Inner Event  `json:"inner"`
}

// SubagentUsage carries the per-subagent accounting shown when its row is
// expanded in the TUI. Cache stats are intentionally omitted.
type SubagentUsage struct {
	InputTokens   int     `json:"input_tokens"`
	OutputTokens  int     `json:"output_tokens"`
	ContextTokens int     `json:"context_tokens"` // current context size of the subagent
	ContextLimit  int     `json:"context_limit"`  // subagent model's context window (0 = unknown)
	Cost          float64 `json:"cost"`            // USD, 0 when the model's price is unknown
}

// PromptEstimate is emitted before each streaming iteration to communicate
// the estimated prompt token count and context limit so the TUI can show
// live context usage even before the API returns actual usage data.
// Prompt tokens grow across iterations as tool results are appended.
type PromptEstimate struct {
	PromptTokens int `json:"prompt_tokens"`
	ContextLimit int `json:"context_limit"`
}

// UserInput is emitted when a client submits input to a server. It is
// broadcast to all attached clients so they can render the user's message
// in the chat pane, not just the submitting client.
type UserInput struct {
	Text string `json:"text"`
}

// SubagentFinished is emitted when a subagent run completes. Result is the
// subagent's final message — the only thing the calling agent sees.
type SubagentFinished struct {
	ID      string         `json:"id"`
	Agent   string         `json:"agent"`
	Result  string         `json:"result"`
	IsError bool           `json:"is_error"`
	Usage   SubagentUsage `json:"usage"`
}

// ModelChanged is emitted by the server when the model or thinking level
// changes (via /model or /thinking). Clients should update their status bar.
type ModelChanged struct {
	Provider      string `json:"provider"`
	Model         string `json:"model"`
	ThinkingLevel string `json:"thinking_level"`
}

// PermissionModeChanged is emitted when the permission mode cycles.
// Clients should update their status bar.
type PermissionModeChanged struct {
	Mode string `json:"mode"`
}

// ThinkingBlock carries the complete extended-thinking text for one thinking
// block. Emitted after the block finishes streaming, before text streaming begins.
type ThinkingBlock struct {
	Text string `json:"text"`
}

// ThinkingDelta carries a partial chunk of extended-thinking text as it
// streams in. The TUI accumulates these into a live thinking segment.
type ThinkingDelta struct {
	Text string `json:"text"`
}

// SessionStatus is broadcast to every connection (no subscription needed) on
// any session state transition. It drives the multi-session sidebar cheaply
// and never carries deltas.
type SessionStatus struct {
	ID      string `json:"id"`
	State   string `json:"state"` // idle|busy|awaiting_approval|unloaded|deleted
	Preview string `json:"preview,omitempty"`
	Dir     string `json:"dir,omitempty"`
}

// EngineSnapshot is sent to newly-attached clients so they can sync with the
// server's current turn state (busy flag, streamed text, active tools).
// Without this, a client that attaches mid-turn sees a blank idle screen.
type EngineSnapshot struct {
	Busy              bool               `json:"busy"`
	StreamedText      string             `json:"streamed_text"`
	ActiveTools       []ToolSnapshot     `json:"active_tools"`
	PendingPermission *PendingPermission `json:"pending_permission,omitempty"`
}

// PendingPermission describes an outstanding permission request so a newly
// attached client can show the approval dialog.
type PendingPermission struct {
	ToolName string `json:"tool_name"`
	Command  string `json:"command"`
	Reason   string `json:"reason"`
}

// ApprovalResolved is emitted after Approve consumes a pending permission
// request so every subscribed client can dismiss its confirmation dialog.
type ApprovalResolved struct {
	Approved bool `json:"approved"`
}

type ToolSnapshot struct {
	ID   string         `json:"id"`
	Name string         `json:"name"`
	Args map[string]any `json:"args"`
}

func (ThinkingBlock) isEvent()        {}
func (ThinkingDelta) isEvent()       {}
func (UserInput) isEvent()           {}
func (ModelChanged) isEvent()        {}
func (BatchStarted) isEvent()        {}
func (BatchFinished) isEvent()       {}
func (TextDelta) isEvent()           {}
func (ToolStarted) isEvent()         {}
func (ToolFinished) isEvent()        {}
func (ToolOutputDelta) isEvent()     {}
func (Notice) isEvent()              {}
func (TurnEnded) isEvent()           {}
func (PermissionRequested) isEvent() {}
func (SubagentStarted) isEvent()     {}
func (SubagentEvent) isEvent()       {}
func (PromptEstimate) isEvent()      {}
func (SubagentFinished) isEvent()    {}
func (SessionStatus) isEvent()           {}
func (EngineSnapshot) isEvent()          {}
func (ApprovalResolved) isEvent()        {}
func (PermissionModeChanged) isEvent()   {}

type Usage struct {
	PromptTokens       int `json:"prompt_tokens"`
	CompletionTokens   int `json:"completion_tokens"`
	CachedPromptTokens int `json:"cached_prompt_tokens"`
}

type EmitFunc func(Event)
