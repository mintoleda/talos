package protocol

type Event interface{ isEvent() }

type BatchStarted struct{ Num int }
type BatchFinished struct{ Num int }

type TextDelta struct{ Text string }
type ToolStarted struct {
	ID   string
	Name string
	Args map[string]any
}
type ToolFinished struct {
	ID     string
	Result ToolResult
}
type Notice struct {
	Level string
	Text  string
}
type TurnEnded struct {
	StopReason string
	Usage      Usage
}

// PermissionRequested is emitted by the executor when a tool needs human
// confirmation. The handler must send exactly one value to ReplyCh: true to
// allow, false to deny. The channel is owned by whoever creates the event.
type PermissionRequested struct {
	ToolName string
	Command  string
	Reason   string
	ReplyCh  chan<- bool
}

// SubagentStarted is emitted when the primary agent (or a subagent) spawns a
// subagent via a spawn tool. ID uniquely identifies this run; Agent is the
// agent definition's name (e.g. "scout"); Task is the delegated instruction.
type SubagentStarted struct {
	ID    string
	Agent string
	Task  string
}

// SubagentEvent wraps an event emitted by a running subagent's own loop. The
// Inner event (TextDelta, ToolStarted, ToolFinished, Notice, …) is tagged with
// the subagent's ID and name so the frontend can route it to that subagent's
// view instead of the primary chat — preserving the primary agent's context
// isolation. Permission requests are NOT wrapped: they pass through unwrapped
// so the existing approval dialog handles them.
type SubagentEvent struct {
	ID    string
	Agent string
	Inner Event
}

// SubagentUsage carries the per-subagent accounting shown when its row is
// expanded in the TUI. Cache stats are intentionally omitted.
type SubagentUsage struct {
	InputTokens   int
	OutputTokens  int
	ContextTokens int     // current context size of the subagent
	ContextLimit  int     // subagent model's context window (0 = unknown)
	Cost          float64 // USD, 0 when the model's price is unknown
}

// PromptEstimate is emitted before each streaming iteration to communicate
// the estimated prompt token count and context limit so the TUI can show
// live context usage even before the API returns actual usage data.
// Prompt tokens grow across iterations as tool results are appended.
type PromptEstimate struct {
	PromptTokens int
	ContextLimit int
}

// UserInput is emitted when a client submits input to a server. It is
// broadcast to all attached clients so they can render the user's message
// in the chat pane, not just the submitting client.
type UserInput struct {
	Text string
}

// SubagentFinished is emitted when a subagent run completes. Result is the
// subagent's final message — the only thing the calling agent sees.
type SubagentFinished struct {
	ID      string
	Agent   string
	Result  string
	IsError bool
	Usage   SubagentUsage
}

// ModelChanged is emitted by the server when the model or thinking level
// changes (via /model or /thinking). Clients should update their status bar.
type ModelChanged struct {
	Provider      string
	Model         string
	ThinkingLevel string
}

func (UserInput) isEvent()           {}
func (ModelChanged) isEvent()        {}
func (BatchStarted) isEvent()        {}
func (BatchFinished) isEvent()       {}
func (TextDelta) isEvent()           {}
func (ToolStarted) isEvent()         {}
func (ToolFinished) isEvent()        {}
func (Notice) isEvent()              {}
func (TurnEnded) isEvent()           {}
func (PermissionRequested) isEvent() {}
func (SubagentStarted) isEvent()     {}
func (SubagentEvent) isEvent()       {}
func (PromptEstimate) isEvent()    {}
func (SubagentFinished) isEvent()    {}

type Usage struct {
	PromptTokens       int
	CompletionTokens   int
	CachedPromptTokens int
}

type EmitFunc func(Event)
