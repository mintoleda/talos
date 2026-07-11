// Protocol types for the Talos WebSocket client.
// Mirrors internal/protocol/event.go, message.go, request.go, and transport/types.go.
// All event structs use snake_case JSON keys matching the Go side.

// ── Transport layer ──────────────────────────────────────────────────────

export interface ClientMsg {
  type: "auth" | "input" | "steer" | "interrupt" | "approve" | "request" | "subscribe" | "unsubscribe";
  text?: string;
  approved?: boolean;
  plan?: unknown[];
  id?: number;
  method?: string;
  params?: unknown;
  token?: string;
  session?: string; // target session; omit / "" = connection default
}

export interface ServerMsg {
  type: "hello" | "event" | "error" | "response";
  version?: string;
  session?: string; // originating session for events/errors; "" on multi-session hello
  etype?: string;      // snake_case event type name
  event?: unknown;      // raw JSON object
  id?: number;
  result?: unknown;
  err?: string;
}

// ── Daemon RPC types (mirrors internal/client/rpc) ────────────────────────

export interface SessionInfo {
  id: string;
  dir: string;
  project_dir: string;
  isolation: string;
  branch?: string;
  ahead?: number;
  dirty?: boolean;
  merged?: boolean;
  state: "idle" | "busy" | "awaiting_approval" | "unloaded" | "merged" | string;
  live: boolean;
  provider: string;
  model: string;
  preview: string;
  created_at: string;
  last_active: string;
}

export interface CreateSessionParams {
  dir: string;
  isolation?: "worktree" | "none" | string;
  resume?: string;
  provider?: string;
  model?: string;
}

export interface CreateSessionResult {
  session: SessionInfo;
}

export interface ListSessionsResult {
  sessions: SessionInfo[];
}

export interface DaemonStatusResult {
  version: string;
  uptime_seconds: number;
  sessions: number;
  orphan_worktrees?: string[];
}

export interface GCWorktreesResult {
  removed: string[];
}

export interface ProbeDirParams {
  dir: string;
}

export interface ProbeDirResult {
  is_repo: boolean;
  project_dir: string;
}

export const DaemonRPC = {
  CreateSession: "daemon.createSession",
  ListSessions: "daemon.listSessions",
  StopSession: "daemon.stopSession",
  DeleteSession: "daemon.deleteSession",
  Status: "daemon.status",
  GCWorktrees: "daemon.gcWorktrees",
  ProbeDir: "daemon.probeDir",
} as const;

export const MergeRPC = {
  Preview: "merge.preview",
  FileDiff: "merge.fileDiff",
  Execute: "merge.execute",
  CommitWorktree: "merge.commitWorktree",
} as const;

export interface MergeCommitInfo {
  sha: string;
  subject: string;
  author: string;
  time: string;
}

export interface MergeFileStat {
  path: string;
  status: string;
  additions: number;
  deletions: number;
}

export interface MergePreviewParams {
  id: string;
  base?: string;
}

export interface MergePreviewResult {
  base: string;
  branch: string;
  ahead: number;
  behind: number;
  dirty_worktree: boolean;
  dirty_main: boolean;
  dirty_main_hit?: string[];
  commits: MergeCommitInfo[];
  files: MergeFileStat[];
  can_ff: boolean;
  session_state: string;
}

export interface MergeFileDiffParams {
  id: string;
  path: string;
  context?: number;
  base?: string;
}

export interface MergeFileDiffResult {
  unified: string;
}

export interface MergeExecuteParams {
  id: string;
  strategy: "squash" | "merge" | "ff" | string;
  message?: string;
  cleanup?: boolean;
  base?: string;
}

export interface MergeExecuteResult {
  merged: boolean;
  conflict: boolean;
  conflict_files?: string[];
  sha?: string;
}

export interface MergeCommitWorktreeParams {
  id: string;
  message: string;
}

export interface MergeCommitWorktreeResult {
  sha: string;
}

export const EngineRPC = {
  ListModels: "engine.listModels",
  SwitchModel: "engine.switchModel",
  CycleThinking: "engine.cycleThinking",
  CurrentThinking: "engine.currentThinking",
  CyclePermissionMode: "engine.cyclePermissionMode",
  SetPermissionMode: "engine.setPermissionMode",
  PermissionMode: "engine.permissionMode",
  TogglePanic: "engine.togglePanic",
  WithdrawSteer: "engine.withdrawSteer",
  Compact: "engine.compact",
  Stats: "engine.stats",
  ListFiles: "engine.listFiles",
  ResolveInput: "engine.resolveInput",
  ListCommands: "engine.listCommands",
  History: "engine.history",
  MCPCount: "engine.mcpCount",
  ListBg: "engine.listBg",
  KillBg: "engine.killBg",
  BgLog: "engine.bgLog",
  DismissBg: "engine.dismissBg",
} as const;

export interface BgProcInfo {
  id: string;
  command: string;
  dir: string;
  running: boolean;
  exit_code: number;
  started_at: string;
}

export interface ListBgResult {
  procs: BgProcInfo[];
}

export interface BgLogResult {
  text: string;
}

export interface CommandDesc {
  name: string;
  summary: string;
  args?: string;
  local?: boolean;
}

export interface ListCommandsResult {
  commands: CommandDesc[];
}

export interface ModelEntry {
  Provider: string;
  ID: string;
}

export interface ModelsResult {
  models: ModelEntry[];
}

export interface ListFilesResult {
  files: string[];
}

export interface StatsResult {
  input: number;
  output: number;
  cache_miss: number;
  cost: number;
}

export interface LevelResult {
  level: string;
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
export interface PendingPermission {
  tool_name: string;
  command: string;
  reason: string;
}
export interface BgSnapshot {
  id: string;
  command: string;
  dir: string;
  running: boolean;
  exit_code?: number;
  recent_output?: string;
  started_at?: string;
}
export interface EngineSnapshotEvent {
  etype: "engine_snapshot";
  busy: boolean;
  streamed_text: string;
  active_tools: ToolSnapshot[];
  pending_permission?: PendingPermission;
  bg_procs?: BgSnapshot[];
}
export interface BgStartedEvent {
  etype: "bg_started";
  id: string;
  command: string;
  dir: string;
}
export interface BgOutputEvent {
  etype: "bg_output";
  id: string;
  text: string;
}
export interface BgExitedEvent {
  etype: "bg_exited";
  id: string;
  code: number;
}
export interface ApprovalResolvedEvent {
  etype: "approval_resolved";
  approved: boolean;
}
export interface SessionStatusEvent {
  etype: "session_status";
  id: string;
  state: "idle" | "busy" | "awaiting_approval" | "unloaded" | "deleted" | string;
  preview?: string;
  dir?: string;
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
  | BgStartedEvent
  | BgOutputEvent
  | BgExitedEvent
  | ApprovalResolvedEvent
  | SessionStatusEvent
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
