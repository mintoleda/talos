# Talos ACP client and session manager

**Status:** design proposal for the Rust Paseo rebuild. This is not an implementation specification for Talos; Talos' current ACP wire behavior below is the compatibility target. Talos and Claude Code are the only supported providers.

## 1. Evidence and boundary

The `paseo-rs` checkout was empty apart from Git metadata when inspected, so there was no local Rust API or project convention to extend. This report therefore specifies a clean-slate Paseo-side design. No files in Talos or the user's other repositories were changed.

The Talos observations come from `/home/adetola/repos/talos`, especially `docs/paseo-integration.md`, `internal/acp/{agent,conn,session,tools,types,agent_test}.go`, `internal/stack`, `internal/protocol`, `internal/loop`, `internal/executor`, `internal/safety`, `internal/session`, `internal/provider`, and the MCP transport code. Important qualification: the Talos checkout was on branch `talos` at `aaccca2` with 18 modified tracked files and 11 untracked files. The ACP server, its tests, the `talos acp` command, streamable-HTTP MCP transport, and the Paseo integration document were among those uncommitted files. Treat this as observed working-tree behavior, not a guarantee about a published Talos build. The Talos integration document reports end-to-end fake-LLM tests and a live Paseo run; its checked-in status could not be verified from that dirty checkout.

The runtime architecture document says Paseo owns UI, workspace/worktree, session/agent management, permission presentation, remote hosts, and high-level timelines; Talos owns the agent loop, tools, safety, context, and canonical model transcript. The Rust client must preserve that ownership boundary.

## 2. Architecture decision

Implement one ACP client/runtime shared by Talos and Claude Code. Make `talos` the preferred/default provider by configuration and provider selection, not by hard-coding Talos into the loop or silently substituting it for Claude Code. A launch or protocol failure must not silently switch providers: that would misrepresent which model/runtime owns a session. Both providers use the same transport, session, event, and permission machinery, with capability checks and tolerant handling of optional/unknown extensions.

Suggested crate seams (consistent with the wider workspace proposal):

```text
paseo-application/
  provider_port.rs   provider/session use cases and provider-neutral events
  agent_manager.rs   provider instances, sessions, routing, persistence, recovery
paseo-acp/
  process.rs         ACP child transport, stdio framing, stderr capture
  rpc.rs             bidirectional JSON-RPC, pending calls, server requests
  client.rs          initialize and ACP method wrappers / capability checks
  session.rs         native session state, per-session serialization
  translate.rs       ACP session updates -> Paseo domain events
paseo-process/       shared child supervision and process-tree shutdown
```

Define an async provider-neutral `Provider`/`AgentSession` port (for example `capabilities`, `list_models`, `start_session`, `prompt`, mode/config update, cancel, and event stream) for the application layer. The ACP implementation owns protocol details; the UI should not branch on raw JSON. A provider configuration entry selects `{provider_id, kind, command, args, env, host, enabled, default}`. Talos uses `talos acp`; Claude Code uses the official `@agentclientprotocol/claude-agent-acp` adapter. Keep raw provider/model identifiers separate: `provider_id = "talos"` is not the same thing as Talos' ACP model ID such as `openai/deepseek-chat`. Treat `provider_id = "claude"` similarly.

Core records should include:

- `ProviderInstanceKey`: provider config ID/revision plus host/runtime identity. It owns one long-lived ACP process/connection and may multiplex sessions.
- `AcpSessionRecord`: Paseo session ID, workspace/host ID, provider instance key, native ACP `sessionId`, cwd/worktree identity, selected model/mode/config options, negotiated capabilities, and lifecycle state.
- `TurnRecord`: Paseo turn ID, active ACP prompt request ID, cancellation state, and a monotonically increasing event sequence for ordered UI delivery.

Persist Paseo IDs and native ACP IDs separately. Never expose the native ID as Paseo's primary key. One ACP process per provider instance is the initial default; route every notification by its `sessionId`, and serialize prompts/control operations per ACP session. Keep the transport abstraction replaceable so a remote-host process transport can be added without changing ACP/session logic.

## 3. Framing and bidirectional JSON-RPC

Talos speaks **newline-delimited JSON-RPC 2.0 on stdin/stdout**. Do not use LSP `Content-Length` framing. Each non-empty line is one JSON-RPC envelope. The Talos scanner accepts lines up to 64 MiB because prompts can contain embedded resources; use a bounded async line reader with a compatible configurable ceiling (at least 64 MiB for this integration), not a small default `LinesCodec` limit. Reject/record over-limit frames without unbounded allocation.

The Rust connection should have:

1. A single writer task/lock that serializes complete JSON objects and appends exactly one `\n`.
2. A reader task that routes responses to a pending-call map and routes inbound methods to handlers without blocking the reader.
3. A typed `RpcId` that preserves JSON integer and string IDs. Do not assume all agents use the same ID type. Talos currently uses numeric IDs in both directions.
4. A pending-call map keyed by `RpcId`, with one-shot response senders, deadlines/cancellation, and guaranteed failure of every pending call on EOF/process exit.
5. A server-request dispatcher. Talos calls the client with `session/request_permission` while `session/prompt` is still outstanding. The reader must continue processing this request and return a JSON-RPC result; waiting for the prompt response before reading would deadlock.
6. Concurrent session-update dispatch keyed by native `sessionId`. Preserve read order when enqueueing events for one session; tool operations from Talos may interleave because tools can execute in parallel.

Stdout is protocol-only. Drain stderr concurrently into a bounded diagnostic ring buffer and log with provider/session context; redact configured secrets and never parse stderr as ACP. Talos' `cmd/talos/acp.go` redirects accidental stdout logging to stderr. The client must still treat any malformed stdout line as a protocol error, not silently display it as assistant text.

JSON-RPC behavior to implement: requests have `jsonrpc`, `id`, `method`, and optional `params`; notifications omit `id` and receive no response; responses contain the original `id` and exactly `result` or `error`. Preserve unknown result/update fields where practical. Convert JSON-RPC errors to typed Paseo errors while retaining code, message, and data. Talos uses standard codes `-32700` parse, `-32600` invalid request, `-32601` method not found, `-32602` invalid params, and `-32603` internal error.

## 4. Handshake and capability negotiation

Start the configured executable with its configured arguments (for Talos, an absolute executable path plus `acp`), provider environment, and a controlled cwd. The ACP `cwd` is still sent on every session operation; Talos uses that session cwd and does not create a worktree. Do not depend on the daemon's `PATH` or process cwd. Talos credentials are resolved by Talos from its own config (`~/.talos`/`TALOS_HOME`) or inherited environment; Paseo may inject explicitly configured env, but must not copy credentials into logs or ACP session metadata.

Send `initialize` once per process. Talos' accepted request shape is:

```json
{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":1,"clientCapabilities":{}}}
```

Only advertise client FS/terminal capabilities that Paseo actually implements. Talos does not require filesystem callbacks. Validate the returned protocol version and retain the full capabilities and `agentInfo`. Current Talos response advertises `loadSession: true`, prompt `image: true`, `audio: false`, `embeddedContext: true`, MCP `http: true` and `sse: true`, plus session `list` and `close`; `authMethods` is `[]`, and `authenticate` is a no-op. Its handler tolerates `$/...` extension notifications. Send the ACP initialized notification if required by the negotiated ACP version; an unknown notification is harmless to this Talos handler, but do not treat that as a general ACP guarantee.

Gate optional calls by capabilities: do not call load/list/close if the agent did not advertise them; do not offer image/audio/config/mode controls the agent cannot accept. For unknown `session/update` variants, preserve and forward an `AgentEvent::Extension`/diagnostic rather than crashing the reader or dropping the process. An empty `authMethods` means Paseo should not invent a Talos ACP login flow; authentication belongs to Talos config/environment.

## 5. Session creation, persistence, resume, and close

### Create

Call `session/new` with the workspace cwd and an explicit `mcpServers` array (empty when none). The Paseo platform MCP endpoint should be included here when enabled; details are in §9. Talos validates the cwd as an existing directory, finds the repository root, allocates its own random native session ID, and returns that ID plus modes, models, and config options. Save the Paseo/native mapping as soon as the response arrives and before sending the first prompt. If persistence fails, best-effort `session/close` the returned native session and surface the error.

Talos deliberately builds the runtime lazily: `session/new` creates no transcript, MCP connection, inference request, or checkpoint until first prompt. It does obtain the model catalogue while returning session state, which can make bounded provider model-list API calls. Do not equate a successful ACP session allocation with a transcript already existing. Talos sends `available_commands_update` **after** the `session/new` response, so register the returned native ID before routing that notification.

### Resume/load

On Paseo restart or explicit resume, start/initialize the selected ACP provider and call `session/load` with the stored native ID, the current workspace cwd, and the same applicable MCP servers. Talos opens its transcript/meta sidecar (using the metadata project key when needed), replays history as session updates, then returns modes/models/config options. Replay notifications can arrive before the load response. Buffer them while load is pending and atomically replace/reconcile the UI projection only after a successful response; otherwise the UI can show duplicate or half-replayed history. `available_commands_update` follows the response. Serialize load against any active prompt for the same Paseo session.

Talos owns the canonical model transcript in `~/.talos/sessions/<project-hash>/<native-id>.jsonl` plus a metadata sidecar. Its `session/load` replay covers stored user/assistant text, images, tool calls, and results; thinking deltas are display-only and are not replayed. Interrupted tool calls replay as failed. The Paseo timeline is a UI projection, not an alternative prompt-history source. On load, use replay as canonical history while retaining Paseo-only metadata (workspace, title overrides, activity, event IDs) and avoid appending the same replay twice.

Talos `session/list` reads metadata sidecars, sorts newest first, filters by cwd/project root, skips meta entries without a transcript, and currently ignores the input cursor (it returns no next cursor). Treat it as best-effort discovery, not an authoritative deletion or pagination API. Keep Paseo's own session list authoritative for UI ownership; use ACP list for Talos-side discovery/recovery and offer an explicit import/resume path. If the stored native ID is missing, preserve the Paseo record/timeline and offer a new/forked Talos session rather than silently discarding it.

`session/close` cancels a turn, releases the in-process runtime/MCP/transcript handles, and leaves the Talos transcript and metadata on disk. It is **not** a delete API. Separate `detach`, provider shutdown, session close, and Paseo archive/delete. On ordinary UI navigation, do not destroy a resumable session. On process shutdown, cancel turns, close active native sessions best-effort, close stdin, wait for a bounded graceful exit, then terminate the child if needed. Talos closes all remaining sessions when stdio reaches EOF. Do not claim a deleted Paseo record also erases Talos history; ACP has no such operation.

### List and close API shapes

`session/list` takes optional `{cwd, cursor}` and returns `{sessions:[{sessionId,cwd,title?,updatedAt?}],nextCursor?}`. `session/close` takes `{sessionId}` and returns `{}`. Feature-gate both from the handshake. Native IDs are opaque strings; never derive them from project paths or display titles.

## 6. Prompt lifecycle, updates, and UI event mapping

Call `session/prompt` with `{sessionId, prompt:[ContentBlock...]}` and retain the JSON-RPC request until its response. Talos accepts text, base64 image (`mimeType`, `data`), `resource_link`, embedded text/binary `resource`; it does not advertise audio. Paseo should pass images only when the negotiated capability allows it; map text resources to embedded context only when supported; otherwise resolve to safe text/reference blocks or explain unsupported input. Do not send local paths as if they were file contents.

Normalize updates to a provider-neutral event carrying Paseo session ID, turn ID, per-session sequence, and original native IDs where needed:

| ACP `sessionUpdate` | Paseo event / behavior |
|---|---|
| `agent_message_chunk` | append assistant text delta |
| `agent_thought_chunk` | thought/reasoning channel; respect the UI's visibility policy |
| `user_message_chunk` | replay-only user content; do not duplicate the live user input already rendered by Paseo |
| `tool_call` | create tool activity with `toolCallId`, title, kind, status, raw input, locations, and optional diff content |
| `tool_call_update` | update that activity with status/output/content/diff; preserve `rawOutput` |
| `usage_update` | update context occupancy (`used`, `size`), not billed token totals |
| `current_mode_update` | update the selected provider mode |
| `config_option_update` | replace/update that option's state |
| `available_commands_update` | refresh session slash-command completion |
| unknown update | retain as extension/diagnostic; do not crash |

Talos maps tools to ACP kinds: read → `read`; edit/write → `edit` with diff; grep/glob/ls/find/fff/search/memory search → `search`; bash/background process tools → `execute`; web search/fetch → `fetch`; memory write/update/delete and skill → `think`; unknown → `other`. Tool completion status is `completed` or `failed`; while running it is `in_progress`. `tool_call_update.content` is an array of ACP tool content (`content` or `diff`), while message chunk `content` is one content object. Keep those different JSON shapes. Tool output deltas are coalesced (about 250 ms) and bounded/truncated to 32 KiB by Talos; do not assume every byte is sent as a separate update.

A live prompt does not emit a `user_message_chunk` for the input; Paseo renders its own submitted user event. A successful prompt response carries `{stopReason, usage?}`. Map `end_turn`, `max_tokens`, `max_turn_requests`, `refusal`, and `cancelled` without collapsing them to generic success. Usage totals are accumulated across model calls/tool-loop iterations; `totalTokens = inputTokens + outputTokens`, with cached-read and cached-write counts where available. The separate usage update represents current context occupancy.

Talos emits text/thinking deltas, tool lifecycle events, permission requests, notices (non-debug/non-context notices appear as thought chunks), prompt-estimate/final usage updates, and turn completion. Tool calls may be parallel, so correlate every update by `toolCallId`; do not rely on a single global current tool. Preserve event order on each session while allowing updates for other sessions to proceed.

Slash commands currently announced by Talos include `/compact [focus]`, `/model [provider/model]`, `/mcp`, `/subagents`, and discovered skills. Skill command input is rewritten to `skill:<name>`. Prefer the provider's slash-command surface when advertised; do not intercept these in Paseo as local commands and thereby bypass Talos' own transcript/loop. Generic ACP agents may announce a different command set.

## 7. Permissions and safety ownership

Talos owns classification and execution policy; Paseo owns the human interaction. The ACP client must handle the reverse-direction JSON-RPC request `session/request_permission` concurrently with the active prompt. Route it by `sessionId`, display the tool title/kind/locations/raw input and reason, and return the selected option exactly:

```json
{"outcome":{"outcome":"selected","optionId":"allow_once"}}
```

Talos currently offers `allow_once`, `allow_always`, and `reject_once`. If the prompt is canceled, the permission UI is dismissed, the session is closed, or the request cannot be routed, reply with an outcome of `cancelled` (or an explicit reject option when required by the negotiated client contract); never default to allow. Resolve each pending request exactly once. The Talos permission handler waits asynchronously for this response and denies if the turn context is canceled or the client request fails.

Talos exposes `ask`, `auto`, and `panic` modes. `ask` prompts for classified risky shell commands; `auto` runs those prompts without asking but still blocks catastrophic commands; `panic` blocks every tool call, including otherwise safe ones. Its write/edit path checks paths against the session working directory. These semantics are not interchangeable with generic Paseo `plan`, bypass, or auto-review modes. Present the provider-advertised modes and don't map an unsupported global mode silently. In particular, `plan` is not an ACP mode and `panic` is not a read-only plan mode.

`allow_always` is remembered by the current Talos ACP session in memory, keyed by tool name plus exact command. The inspected implementation initializes this map empty on each `session/load`; do not promise that remembered approvals survive process restart or reload. Paseo may provide its own explicitly configured auto-accept feature, but should apply it only to ACP permission requests, make it visible, and still respect Talos' classifier/blocking decisions. Do not synthesize permission requests for operations Talos already allowed or blocked.

## 8. Cancel, mode/model/config updates

### Cancel

`session/cancel` is a **notification**, `{sessionId}`, with no JSON-RPC response. Send it without canceling the client reader or discarding the prompt's pending response. Talos cancels the turn context; pending permission handling resolves denied; the prompt normally finishes with `stopReason: "cancelled"`. Keep streaming partial output until that response or transport loss. For a local UI cancellation, send `session/cancel`, mark the turn cancel-requested, and await the final response. For timeout/process shutdown, send cancel first and then apply the bounded shutdown policy. Do not retry a canceled prompt automatically: the user message may already be in the canonical transcript and replaying it can execute tools twice.

### Mode

Use `session/set_mode` `{sessionId, modeId}` and accept only the modes advertised by that session. Talos responds with `{}` and emits `current_mode_update` after a successful change. Update UI state from the notification and keep an optimistic/pending state until then. A failed request leaves the last confirmed value intact.

### Model

Use `session/set_model` `{sessionId, modelId}` with the exact ACP model ID from `SessionModels` (Talos accepts `provider/model` or a bare ID on its current provider). Talos returns `{}` and does not currently emit a model-changed notification. Track the requested ID as desired only after success; surface it as pending if a turn is active because Talos defers the switch to the next turn boundary. On idle switch failure, report the RPC error and retain the prior confirmed model. Do not parse slash-delimited model IDs into Paseo provider IDs. Talos' catalog is built from configured provider API keys, cached per ACP process, bounded by a four-second listing timeout, and includes the current model even when it is not in the live catalog.

### Config options

Talos exposes a select option `thought_level` with model-specific values (typically `off`, `minimal`, `low`, `medium`, `high`, `xhigh`). Use `session/set_config_option` `{sessionId, configId, value}`; consume the returned `configOptions` as authoritative. Talos applies changes at the next turn boundary if a prompt is running. Update local state from the response and display pending if necessary. Do not assume every ACP agent has Talos' option or that all models accept the same values.

## 9. Paseo MCP tools and working directory

Talos accepts MCP server descriptors on `session/new` and `session/load`. Its conversion supports stdio (`command`, `args`, `env`), HTTP (`type: "http"`, `url`, `headers`), and SSE (`type: "sse"`). Its stack appends these per-session servers to Talos-configured MCP servers. Streamable HTTP and SSE support are advertised; current Paseo integration documentation uses a Paseo platform tool endpoint over streamable HTTP with a bearer header, conceptually:

```json
{"type":"http","name":"paseo","url":"https://<host>/<mcp-path>","headers":[{"name":"Authorization","value":"Bearer <session-token>"}]}
```

Use the actual endpoint/path from Paseo configuration, not this schematic URL. Gate transport choice against the agent's MCP capabilities. Inject only the tools intended for this session/workspace; scope and expire the bearer token, redact it from process diagnostics, and refresh it when loading a session. Talos connects MCP lazily when building the runtime and logs connection failures to stderr; expose connection/tool availability diagnostics without leaking headers.

Paseo must pass the selected cwd/worktree on both create and load. Talos uses the supplied cwd and explicitly does not create a worktree. Its tool executor anchors relative `read`/`write`/`edit`/`ls` paths at the session cwd (an integration-specific change, because Talos may be spawned from the daemon directory). Never assume the ACP child process cwd is the workspace cwd. Preserve Paseo's workspace/worktree authority; Talos' project-root discovery is only for its transcript/memory keying.

## 10. Process lifecycle and recovery policy

Model process state as `Starting -> Ready -> Draining -> Exited`, with explicit `Unavailable`/`ProtocolError` states surfaced to Paseo. Start lazily on first session operation, initialize once, and keep a bounded stderr tail. On shutdown: reject new work, cancel in-flight prompts, settle pending permission calls as canceled, best-effort close native sessions, close stdin, wait briefly, then terminate/kill and fail all pending RPC calls. Ensure stderr is drained and child reaped in all code paths. A child exit must transition every attached session out of `Ready` and emit one provider/session error, not leave a hung turn.

Recovery rules:

- **Initialize/start failure:** report provider unavailable with command/exit diagnostics; never auto-switch provider. Allow an explicit retry/restart.
- **EOF or child crash:** fail pending requests and preserve Paseo timeline. Re-launch only on an explicit/managed retry; if a native ID is known, initialize then `session/load` it. Do not create a replacement session silently.
- **Prompt transport loss/timeout:** mark the turn interrupted/unknown, send cancel if the connection still works, and do not resend the input. Resume with `session/load` to reconcile Talos' persisted transcript.
- **JSON-RPC application error:** keep the process alive unless the stream is invalid; show method/code/message. Do not retry mutating calls such as `session/new`, `session/prompt`, or `session/set_model` blindly.
- **`session/load` says not found:** retain the Paseo record and offer fork/new. A native transcript is not guaranteed merely because `session/new` returned an ID (Talos creates it lazily).
- **Unknown method/capability:** disable that optional control for this agent instance; keep other sessions/features usable.
- **Close failure:** still release local handles/process resources. ACP close is best-effort cleanup, not transcript deletion.

Use per-call deadlines for setup/control, but do not impose a short fixed timeout on a user prompt. Cancellation is protocol-driven. Never hold the global process writer/manager lock while awaiting a response, permission, or UI action.

## 11. Test fixtures and acceptance criteria

Build a stateful fake ACP subprocess fixture in Rust (or a tiny fixture binary launched by process tests), so tests exercise actual pipes, framing, stderr, child exit, and bidirectional calls without a real model/API key. Add fixture transcripts under `tests/fixtures/acp/` for stable message shapes, but prefer a scripted fake for dynamic request IDs and permission response ordering.

Required cases:

1. **Handshake/capabilities:** exact Talos v1 request/response, unsupported version, missing optional capabilities, `authMethods: []`, and one-time initialized notification.
2. **Framing/RPC:** multiple newline frames, blank lines, escaped/newline-containing JSON strings, large embedded prompt under the 64 MiB ceiling, malformed/oversized line, integer and string IDs, out-of-order responses, unknown response ID, notification with no response, JSON-RPC error, EOF with pending calls, and stderr flood without deadlock.
3. **Session create:** `cwd` and empty/non-empty MCP list, save native ID before following commands notification, response state parsing, no transcript assumptions, and error cleanup if Paseo persistence fails.
4. **Text/image prompt and mapping:** append text chunks; image capability gate; tool start/output/final including content-array and diff-array shapes, path locations, failed status, parallel tool updates, usage update vs billed usage, stop-reason preservation, and unknown update forwarding.
5. **Permission:** reverse-direction `session/request_permission` during a pending prompt; allow once/always/reject/cancel; no UI route; duplicate/late response; cancellation settles pending permission once; ensure no prompt reader deadlock.
6. **Cancel:** notification has no pending RPC ID; partial output remains; prompt response resolves as canceled; repeated cancel is harmless; canceled input is never auto-retried.
7. **Resume/list/close:** load updates buffered until response; replay atomically replaces rather than duplicates timeline; interrupted tools; no thinking replay assumption; list filtering/cursor behavior; close preserves transcript and is not delete; missing session leaves Paseo metadata intact.
8. **State changes:** ask/auto/panic advertised and validated; unsupported mode rejected; model IDs preserved verbatim; missing `current_model_update` handled; model/config updates during active turn become pending; failed model/config update does not poison confirmed UI state.
9. **MCP/cwd:** HTTP bearer descriptor reaches session methods but is absent from logs; capability-gated SSE/HTTP; cwd/worktree forwarded on create/load; relative tool paths are displayed relative to the workspace.
10. **Provider retention:** Talos is selected by default; Claude Code launches through its ACP adapter; either provider works with its negotiated capability set; failure never silently routes an existing session to the other provider.

The Talos working tree already contains fake OpenAI SSE support and ACP tests covering initialize, lazy `session/new`, streamed text/usage, read/bash tool round trips, permission decisions, cancellation, load replay, and JSON serialization of tool-call content. Add Paseo-side tests for transport/session management rather than duplicating Talos loop tests. The inspected Talos ACP tests do not visibly cover all list/close/model/config update cases, so keep those as explicit cross-repo contract checks.

## 12. First implementation sequence

1. Add and test the ACP stdio process/RPC layer independently of UI.
2. Add capability-aware client and strongly typed Talos-compatible session/model/mode/config methods.
3. Add `AgentProvider` and provider-instance/session registry; wire Talos as the preferred ACP configuration and Claude Code as the only secondary provider.
4. Normalize session updates and reverse permission requests into Paseo events; verify streaming and cancellation.
5. Persist the Paseo/native ID mapping and implement load/list/close semantics without conflating close and delete.
6. Inject scoped Paseo MCP config and workspace cwd; test secrets and worktree paths.
7. Run the fake fixture suite, then real Talos and Claude Code adapter smoke tests. Only after that switch default selection; never introduce silent cross-provider fallback.
