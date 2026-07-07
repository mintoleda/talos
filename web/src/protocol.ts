// Protocol types for the Talos WebSocket client.
// Mirrors internal/protocol/event.go, message.go, request.go, and transport/types.go.
// All event structs use snake_case JSON keys matching the Go side.

// ── Transport layer ──────────────────────────────────────────────────────

export interface ClientMsg {
  type: "auth" | "input" | "steer" | "interrupt" | "approve" | "request";
  text?: string;
  approved?: boolean;
  plan?: unknown[];
  id?: number;
  method?: string;
  params?: unknown;
  token?: string;
}

export interface ServerMsg {
  type: "hello" | "event" | "error" | "response";
  version?: string;
  session?: string;
  etype?: string;      // snake_case event type name
  event?: unknown;      // raw JSON object
  id?: number;
  result?: unknown;
  err?: string;
}

// ── Content blocks (message.go) ──────────────────────────────────────────

export type Role = "system" | "user" | "assistant" | "tool";
export type BlockType = "text" | "tool_use" | "tool_result" | "image";

export interface ImageBlock {
  media_type: string;
  data: string;
}

export interface ToolUse {
  id: string;
  name: string;
  args: Record<string, unknown>;
}

export interface ToolResult {
  tool_use_id: string;
  content: string;
  is_error?: boolean;
}

export interface ContentBlock {
  type: BlockType;
  text?: string;
  tool_use?: ToolUse;
  tool_result?: ToolResult;
  image?: ImageBlock;
}

export interface Message {
  role: Role;
  content: ContentBlock[];
}

export interface Usage {
  prompt_tokens: number;
  completion_tokens: number;
  cached_prompt_tokens: number;
}

export interface SubagentUsage {
  input_tokens: number;
  output_tokens: number;
  context_tokens: number;
  context_limit: number;
  cost: number;
}

// ── Request types (request.go) ──────────────────────────────────────────

export interface FrozenMessage {
  msg: Message;
  raw: number[];
}

export interface ToolSchema {
  name: string;
  description: string;
  parameters: unknown;
}

export interface Request {
  system: string;
  tools: ToolSchema[];
  messages: FrozenMessage[];
  volatile?: ContentBlock[];
  model: string;
  thinking_level?: string;
}

// ── Event types (event.go) ───────────────────────────────────────────────
// Every event type has a snake_case etype on the wire.  Use the
// Event union type and the discriminator field `etype` to narrow.

export interface BatchStartedEvent {
  etype: "batch_started";
  num: number;
}
export interface BatchFinishedEvent {
  etype: "batch_finished";
  num: number;
}
export interface TextDeltaEvent {
  etype: "text_delta";
  text: string;
}
export interface ToolStartedEvent {
  etype: "tool_started";
  id: string;
  name: string;
  args: Record<string, unknown>;
}
export interface ToolFinishedEvent {
  etype: "tool_finished";
  id: string;
  result: ToolResult;
}
export interface ToolOutputDeltaEvent {
  etype: "tool_output_delta";
  id: string;
  text: string;
}
export interface NoticeEvent {
  etype: "notice";
  level: string;
  text: string;
}
export interface TurnEndedEvent {
  etype: "turn_ended";
  stop_reason: string;
  usage: Usage;
}
export interface PermissionRequestedEvent {
  etype: "permission_requested";
  tool_name: string;
  command: string;
  reason: string;
  // note: reply_ch is stripped from the wire (chan<- bool, never serialised)
}
export interface SubagentStartedEvent {
  etype: "subagent_started";
  id: string;
  agent: string;
  task: string;
}
export interface PromptEstimateEvent {
  etype: "prompt_estimate";
  prompt_tokens: number;
  context_limit: number;
}
export interface SubagentFinishedEvent {
  etype: "subagent_finished";
  id: string;
  agent: string;
  result: string;
  is_error: boolean;
  usage: SubagentUsage;
}
export interface ModelChangedEvent {
  etype: "model_changed";
  provider: string;
  model: string;
  thinking_level: string;
}
export interface PermissionModeChangedEvent {
  etype: "permission_mode_changed";
  mode: string;
}
export interface UserInputEvent {
  etype: "user_input";
  text: string;
}
export interface ThinkingBlockEvent {
  etype: "thinking_block";
  text: string;
}
export interface ThinkingDeltaEvent {
  etype: "thinking_delta";
  text: string;
}
export interface EngineSnapshotEvent {
  etype: "engine_snapshot";
  busy: boolean;
  streamed_text: string;
  active_tools: ToolSnapshot[];
}
export interface ToolSnapshot {
  id: string;
  name: string;
  args: Record<string, unknown>;
}

// SubagentEvent wraps a nested event tagged with {"etype": …, "event": …}.
export interface SubagentEventEvent {
  etype: "subagent_event";
  id: string;
  agent: string;
  inner: TaggedEvent;
}

// TaggedEvent is the recursive wrapper for SubagentEvent.inner.
export interface TaggedEvent {
  etype: string;
  event: unknown;
}

// ── Event union ─────────────────────────────────────────────────────────
// Discriminated on etype.  Use a type guard or switch on etype to narrow.

export type Event =
  | BatchStartedEvent
  | BatchFinishedEvent
  | TextDeltaEvent
  | ToolStartedEvent
  | ToolFinishedEvent
  | ToolOutputDeltaEvent
  | NoticeEvent
  | TurnEndedEvent
  | PermissionRequestedEvent
  | SubagentStartedEvent
  | PromptEstimateEvent
  | SubagentFinishedEvent
  | ModelChangedEvent
  | PermissionModeChangedEvent
  | UserInputEvent
  | ThinkingBlockEvent
  | ThinkingDeltaEvent
  | EngineSnapshotEvent
  | SubagentEventEvent;

// ── Helpers ──────────────────────────────────────────────────────────────

/** Parse a ServerMsg whose type is "event" into a typed Event union member. */
export function decodeEvent(sm: ServerMsg): Event {
  const raw = sm.event as Record<string, unknown>;
  if (!raw) throw new Error("event body is empty");
  raw.etype = sm.etype;
  return raw as unknown as Event;
}

/** Convert an Event union value back into wire form for display / logging. */
export function eventLabel(e: Event): string {
  return e.etype;
}
