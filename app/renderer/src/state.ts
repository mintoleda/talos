/**
 * state.ts — Pure reducer that accumulates protocol events into chat state.
 *
 * Same accumulation the TUI model does; kept as a pure reducer so it's
 * testable against Go fixtures.
 */

import type {
  Event,
  TextDeltaEvent,
  ThinkingDeltaEvent,
  ThinkingBlockEvent,
  ToolStartedEvent,
  ToolFinishedEvent,
  ToolOutputDeltaEvent,
  NoticeEvent,
  TurnEndedEvent,
  UserInputEvent,
  PermissionRequestedEvent,
  ModelChangedEvent,
  PermissionModeChangedEvent,
  PromptEstimateEvent,
  EngineSnapshotEvent,
  BatchStartedEvent,
  BatchFinishedEvent,
  SubagentStartedEvent,
  SubagentFinishedEvent,
  BgStartedEvent,
  BgOutputEvent,
  BgExitedEvent,
  BgSnapshot,
} from './protocol';

// ── Types ────────────────────────────────────────────────────────────────

export interface ToolCallState {
  id: string;
  name: string;
  args: Record<string, unknown>;
  output: string;        // accumulated ToolOutputDelta
  finished: boolean;
  result?: string;
  isError?: boolean;
}

export interface MessageEntry {
  role: 'user' | 'assistant' | 'tool';
  text: string;
  toolCalls: ToolCallState[];
  usage?: { prompt_tokens: number; completion_tokens: number; cached_prompt_tokens: number };
}

export interface BgProcState {
  id: string;
  command: string;
  dir: string;
  running: boolean;
  exitCode: number;
  recentLines: string;
  startedAt: string;
}

export interface ChatState {
  messages: MessageEntry[];
  /** Streaming text for the assistant's current in-progress turn */
  streamedText: string;
  /** Streaming thinking text (shown separately) */
  streamedThinking: string;
  /** Active (running) tool calls */
  activeTools: ToolCallState[];
  /** Whether the engine is currently processing a turn */
  busy: boolean;
  /** Pending permission request (null = no prompt) */
  permissionRequest: PermissionRequestedEvent | null;
  /** Model info */
  provider: string;
  model: string;
  thinkingLevel: string;
  /** Permission mode */
  permissionMode: string;
  /** Context estimate */
  promptTokens: number;
  contextLimit: number;
  /** Current batch nesting (0 = no batch) */
  batchDepth: number;
  /** Background shell sessions for this engine */
  bgProcs: BgProcState[];
  bgExpanded: boolean;
  bgLogID: string | null;
  bgLogText: string;
}

export function initialState(): ChatState {
  return {
    messages: [],
    streamedText: '',
    streamedThinking: '',
    activeTools: [],
    busy: false,
    permissionRequest: null,
    provider: '',
    model: '',
    thinkingLevel: '',
    permissionMode: 'auto',
    promptTokens: 0,
    contextLimit: 0,
    batchDepth: 0,
    bgProcs: [],
    bgExpanded: false,
    bgLogID: null,
    bgLogText: '',
  };
}

// ── Reducer ──────────────────────────────────────────────────────────────

export function reduceState(s: ChatState, ev: Event): ChatState {
  switch (ev.etype) {
    case 'engine_snapshot':
      return handleSnapshot(s, ev);
    case 'user_input':
      return handleUserInput(s, ev);
    case 'thinking_delta':
      return { ...s, streamedThinking: s.streamedThinking + ev.text };
    case 'thinking_block':
      return { ...s, streamedThinking: s.streamedThinking + ev.text };
    case 'text_delta':
      return { ...s, streamedText: s.streamedText + ev.text };
    case 'tool_started':
      return handleToolStarted(s, ev);
    case 'tool_output_delta':
      return handleToolOutputDelta(s, ev);
    case 'tool_finished':
      return handleToolFinished(s, ev);
    case 'notice':
      return handleNotice(s, ev);
    case 'turn_ended':
      return handleTurnEnded(s, ev);
    case 'permission_requested':
      return { ...s, permissionRequest: ev };
    case 'approval_resolved':
      return { ...s, permissionRequest: null };
    case 'model_changed':
      return { ...s, provider: ev.provider, model: ev.model, thinkingLevel: ev.thinking_level };
    case 'permission_mode_changed':
      return { ...s, permissionMode: ev.mode };
    case 'prompt_estimate':
      return { ...s, promptTokens: ev.prompt_tokens, contextLimit: ev.context_limit };
    case 'batch_started':
      return { ...s, batchDepth: s.batchDepth + 1 };
    case 'batch_finished':
      return { ...s, batchDepth: Math.max(0, s.batchDepth - 1) };
    case 'subagent_started':
      // For the minimal client, emit a notice-like message.
      return {
        ...s,
        messages: [
          ...s.messages,
          { role: 'assistant' as const, text: `[subagent ${ev.agent} started: ${ev.task}]`, toolCalls: [] },
        ],
      };
    case 'subagent_finished':
      return {
        ...s,
        messages: [
          ...s.messages,
          {
            role: 'assistant' as const,
            text: ev.is_error ? `[subagent ${ev.agent} error: ${ev.result}]` : `[subagent ${ev.agent} finished: ${ev.result}]`,
            toolCalls: [],
          },
        ],
      };
    case 'bg_started':
      return handleBgStarted(s, ev);
    case 'bg_output':
      return handleBgOutput(s, ev);
    case 'bg_exited':
      return handleBgExited(s, ev);
    default:
      return s;
  }
}

function bgFromSnapshot(p: BgSnapshot): BgProcState {
  return {
    id: p.id,
    command: p.command,
    dir: p.dir,
    running: p.running,
    exitCode: p.exit_code ?? 0,
    recentLines: p.recent_output ?? '',
    startedAt: p.started_at ?? '',
  };
}

function handleBgStarted(s: ChatState, ev: BgStartedEvent): ChatState {
  const rest = s.bgProcs.filter((p) => p.id !== ev.id);
  return {
    ...s,
    bgProcs: [
      ...rest,
      {
        id: ev.id,
        command: ev.command,
        dir: ev.dir,
        running: true,
        exitCode: 0,
        recentLines: '',
        startedAt: new Date().toISOString(),
      },
    ],
  };
}

function handleBgOutput(s: ChatState, ev: BgOutputEvent): ChatState {
  return {
    ...s,
    bgProcs: s.bgProcs.map((p) =>
      p.id === ev.id ? { ...p, recentLines: (p.recentLines + ev.text).slice(-8192) } : p,
    ),
    bgLogText: s.bgLogID === ev.id ? s.bgLogText + ev.text : s.bgLogText,
  };
}

function handleBgExited(s: ChatState, ev: BgExitedEvent): ChatState {
  return {
    ...s,
    bgProcs: s.bgProcs.map((p) =>
      p.id === ev.id ? { ...p, running: false, exitCode: ev.code } : p,
    ),
  };
}

function handleSnapshot(s: ChatState, ev: EngineSnapshotEvent): ChatState {
  const pending = ev.pending_permission
  const bgProcs = (ev.bg_procs ?? []).map(bgFromSnapshot)
  const logStillOpen = s.bgLogID && bgProcs.some((p) => p.id === s.bgLogID)
  return {
    ...s,
    busy: ev.busy,
    streamedText: ev.streamed_text ?? '',
    activeTools: (ev.active_tools ?? []).map((t) => ({
      id: t.id,
      name: t.name,
      args: t.args as Record<string, unknown>,
      output: '',
      finished: false,
    })),
    permissionRequest: pending
      ? {
          etype: 'permission_requested',
          tool_name: pending.tool_name,
          command: pending.command,
          reason: pending.reason,
        }
      : null,
    bgProcs,
    bgLogID: logStillOpen ? s.bgLogID : null,
    bgLogText: logStillOpen ? s.bgLogText : '',
  };
}

function handleUserInput(s: ChatState, ev: UserInputEvent): ChatState {
  return {
    ...s,
    busy: true,
    streamedText: '',
    streamedThinking: '',
    activeTools: [],
    messages: [
      ...s.messages,
      { role: 'user', text: ev.text, toolCalls: [] },
    ],
  };
}

function handleToolStarted(s: ChatState, ev: ToolStartedEvent): ChatState {
  return {
    ...s,
    activeTools: [
      ...s.activeTools,
      { id: ev.id, name: ev.name, args: ev.args as Record<string, unknown>, output: '', finished: false },
    ],
  };
}

function handleToolOutputDelta(s: ChatState, ev: ToolOutputDeltaEvent): ChatState {
  return {
    ...s,
    activeTools: s.activeTools.map((t) =>
      t.id === ev.id ? { ...t, output: t.output + ev.text } : t,
    ),
  };
}

function handleToolFinished(s: ChatState, ev: ToolFinishedEvent): ChatState {
  const tool = s.activeTools.find((t) => t.id === ev.id);
  const others = s.activeTools.filter((t) => t.id !== ev.id);

  if (!tool) return { ...s, activeTools: others };

  const finishedTool: ToolCallState = {
    ...tool,
    finished: true,
    result: ev.result?.content ?? '',
    isError: ev.result?.is_error,
  };

  // Append a tool message entry.
  return {
    ...s,
    activeTools: others,
    messages: [
      ...s.messages,
      {
        role: 'tool',
        text: '',
        toolCalls: [finishedTool],
      },
    ],
  };
}

function handleNotice(s: ChatState, ev: NoticeEvent): ChatState {
  return {
    ...s,
    messages: [
      ...s.messages,
      { role: 'assistant', text: `[${ev.level}] ${ev.text}`, toolCalls: [] },
    ],
  };
}

function handleTurnEnded(s: ChatState, ev: TurnEndedEvent): ChatState {
  const currentText = s.streamedText;
  const currentThinking = s.streamedThinking;

  const assyMsgs: MessageEntry[] = [];

  // Flush thinking as a standalone message if non-empty.
  if (currentThinking) {
    assyMsgs.push({ role: 'assistant', text: currentThinking, toolCalls: [] });
  }
  if (currentText || s.activeTools.length > 0) {
    assyMsgs.push({
      role: 'assistant',
      text: currentText,
      toolCalls: s.activeTools.filter((t) => t.finished),
      usage: ev.usage ? {
        prompt_tokens: ev.usage.prompt_tokens,
        completion_tokens: ev.usage.completion_tokens,
        cached_prompt_tokens: ev.usage.cached_prompt_tokens,
      } : undefined,
    });
  }

  return {
    ...s,
    busy: false,
    streamedText: '',
    streamedThinking: '',
    activeTools: [],
    messages: [...s.messages, ...assyMsgs],
  };
}

/** Merge a history of frozen messages into state (used on attach). */
export function ingestHistory(s: ChatState, history: unknown[]): ChatState {
  // Simple approach: convert frozen messages to text lines.
  for (const h of history) {
    const rec = h as { msg?: { role?: string; content?: Array<{ type?: string; text?: string }> } };
    const role = rec.msg?.role ?? 'user';
    const texts = (rec.msg?.content ?? [])
      .filter((b: { type?: string }) => b.type === 'text')
      .map((b: { text?: string }) => b.text ?? '')
      .join(' ');
    if (texts) {
      s = {
        ...s,
        messages: [...s.messages, { role: role as 'user' | 'assistant', text: texts, toolCalls: [] }],
      };
    }
  }
  return s;
}
