# Current Paseo architecture: observed evidence and Rust reconstruction notes

**Status:** static reconstruction report; observations are not runtime-validated. This describes the locally inspected Paseo 0.13.0 desktop artifact and installed `@getpaseo` packages, not an assertion that every route or provider was enabled in a running installation.

## Evidence boundary

### Observed

- The shipped desktop artifact inspected was `/tmp/.mount_paseo-zWkiWr/resources/app.asar` (Paseo `0.13.0`). Its application-owned code includes `dist/main.js`, `dist/preload.js`, and desktop settings modules; the ASAR also contains the bundled `@getpaseo` packages. Static inspection and REA analysis were used; the app was not launched and no interactive requests were made.
- Package-level evidence came from `/home/adetola/.local/lib/node_modules/@getpaseo/cli/node_modules/@getpaseo/{client,protocol,server,plugin}`. The most useful sources are `protocol/dist/messages.js`, `protocol/dist/binary-frames/*.d.ts`, `server/dist/server/server/{bootstrap.js,websocket-server.js,session.js,agent/agent-manager.js,agent/agent-storage.js,agent/provider-registry.js,authorization/*}`, and `server/dist/scripts/supervisor-entrypoint.js` / `supervisor.js`.
- The observed implementation is JavaScript/TypeScript compiled to JavaScript, not a Rust daemon. Package code describes intended behavior but cannot establish what this particular machine’s live daemon, settings, or credentials were doing. The contents of `/home/adetola/.paseo` and `/home/adetola/repos/talos` were not used as evidence or modified.

### Unknown / not established

- Live launch order, enabled features, actual daemon address, authentication settings, provider availability, request success/failure, data retention, and crash/restart outcomes were not observed.
- Static source does not establish all on-disk schemas, migration compatibility, UI behavior, or whether every listed protocol variant is used by the desktop build. Treat package declarations as the strongest available contract evidence, not proof of runtime support in all clients.

## Observed component and process map

```text
Electron renderer
  ├─ preload bridge ── Electron main process
  │                    ├─ local daemon manager / lifecycle
  │                    └─ desktop-only browser, window, dialog, opener APIs
  └─ Paseo client ───── WebSocket /ws ── server Session ── feature services
                                              ├─ agent manager ── provider clients / child processes
                                              ├─ workspace, Git, terminal and file services
                                              ├─ schedules, plugins, MCP and other integrations
                                              └─ JSON-backed registries and provider-native session history

CLI / other clients ────────────────────────┘
```

- **Desktop boundary.** `app.asar!/dist/preload.js` exposes a constrained bridge. `app.asar!/dist/main.js` handles the generic `paseo:invoke` command path plus specific desktop channels such as `paseo:browser:*`, `paseo:window:*`, `paseo:dialog:*`, and `paseo:opener:openUrl`. The renderer-facing browser/window functions are separate from daemon session RPC. `app.asar!/dist/settings/desktop-settings.js` and `window-state.js` define `desktop-settings.json` and `window-state.json` under Electron’s `userDataPath`; these are desktop preferences/window state, not the daemon’s project registry.
- **Daemon supervision.** `server/dist/scripts/supervisor-entrypoint.js` acquires/heartbeats a PID lock under the Paseo home, starts a daemon worker, and configures crash restart. The supervisor updates the lock with the worker’s listen/server identity and clears it on worker exit. `server/dist/server/server/daemon-worker.js` is the worker entry; `server/dist/server/server/bootstrap.js` composes the HTTP server, WebSocket server, registries, provider/agent management, and service modules. This establishes a parent/worker design in source; restart timing and recovery quality were not measured.
- **Request/session ownership.** `server/dist/server/server/websocket-server.js` accepts WebSockets at `/ws`, validates the handshake/admission, and creates a `Session` from `server/dist/server/server/session.js`. `Session` is deliberately transport-agnostic: it routes requests to domain services, owns subscriptions and per-client authorization, and emits session messages. Agent lifecycle logic is concentrated in `agent/agent-manager.js`; provider implementations are under `agent/providers/` and are assembled by `agent/provider-registry.js`.

## Observed protocol and security surface

### Desktop IPC and HTTP

- Desktop IPC uses Electron IPC. The generic main-process handler is `paseo:invoke`; the preload additionally exposes browser registration/automation, window, notification, dialog, menu, and external-URL operations. The desktop bridge is not the daemon’s public RPC protocol.
- `server/server/bootstrap.js` mounts HTTP routes including `GET /api/health`, `GET /api/status`, `POST /api/terminal-activity`, `GET /api/files/download`, and the agent MCP route `/mcp/agents`. It also mounts the built-in Web UI and service-proxy middleware. The download route uses a download token; the MCP handler is a separate HTTP entry point. Route ordering and the individual handlers matter, so do not infer that every route shares one authentication policy.
- The daemon also has `/ws`; the WebSocket protocol is the main client control/event surface. `server/server/websocket-server.js` and `server/server/auth.js` contain handshake/authentication logic. The package supports bearer credentials and hello-level password/local-credential authentication. `protocol/dist/messages.js` defines `hello.rejected` reasons including password required, incorrect password, and incompatible protocol.

### WebSocket messages

`protocol/dist/messages.js` defines a Zod-validated envelope:

- WebSocket-level `ping`, `pong`, `hello`, `recording_state`, and `hello.rejected` messages.
- `hello` carries a `clientId`, `clientType` (`mobile`, `browser`, `cli`, `mcp`, or `hub`), integer `protocolVersion`, optional app version/capability set, optional requested session permissions, and optional password or local-credential auth.
- Application traffic is wrapped as `{ type: "session", message: ... }`. Requests generally carry `requestId`; errors can be emitted as `rpc_error`. This is a typed custom RPC/event protocol, not JSON-RPC 2.0.
- Representative requests in `server/server/session.js` include `fetch_agents_request`, `create_agent_request`, `send_agent_message_request`, `cancel_agent_request`, `agent_permission_response`, `fetch_agent_timeline_request`, `fetch_workspaces_request`, `create_paseo_worktree_request`, `create_terminal_request`, `list_provider_models_request`, and `list_provider_modes_request`. The same dispatcher covers Git/checkout, project/workspace configuration, files, schedules, plugins, browser automation, voice, and subscriptions. These names are examples, not an exhaustive API inventory.
- Binary frames are part of the protocol, not arbitrary WebSocket bytes. `protocol/dist/binary-frames/terminal.d.ts` defines terminal opcodes `Output=1`, `Input=2`, `Resize=3`, `Snapshot=4`, `Restore=5`, with a slot and payload. `file-transfer.d.ts` defines `FileBegin=16`, `FileChunk=17`, `FileEnd=18`, keyed by request ID. `host-route.d.ts` and the binary-frame demultiplexer cover connected-host routing.

### Authorization

- `server/server/authorization/operation-permissions.js` maps inbound and outbound message types to permissions; `authorization/index.js` defines the `SessionAuthorization` check and `OWNER_PERMISSIONS`.
- The declared permission vocabulary is `daemon.read`, `daemon.manage`, `tunnel.manage`, `access.manage`, `workspace.read`, `workspace.write`, `workspace.manage`, `automation.manage`, and `hub.execute` (`protocol/dist/messages.d.ts`). Some operations accept one of multiple permissions; the authorization helper treats an array as an alternative. Outbound events are also filtered, so authorization is not only a request-time gate.
- The reconstruction implication is to preserve message-level authorization and capability negotiation, not to rely on hiding controls in the UI. Exact policy for unlisted/unknown operations and behavior under every client version remain runtime-unverified.

## Observed state, persistence, and agent lifecycle

- `server/server/paseo-home.js` resolves `PASEO_HOME`, defaulting to `~/.paseo`. `persisted-config.js` loads the daemon’s versioned configuration. `bootstrap.js` constructs `AgentStorage` using the resolved `agentStoragePath`, and file-backed project/workspace registries at `projects/projects.json` and `projects/workspaces.json`.
- `workspace-registry-model.js` distinguishes workspace placements including `local_checkout`, `worktree`, and `directory`. This is a meaningful domain distinction: a workspace is not necessarily just a path string.
- `agent/agent-storage.js` and `agent-manager.js` persist agent records/snapshots and provider persistence handles. The lifecycle supports creating, resuming and importing provider sessions; closing, archiving, reloading, cancelling, permission responses, and timeline hydration. Agent runs have explicit lifecycle/turn boundaries, and cancellation interrupts the provider session with bounded settlement logic. These details are visible in `agent-manager.js`; exact restart recovery was not exercised.
- Timeline data is not simply equivalent to a single Paseo-owned transcript database. The manager has a timeline-store abstraction and defaults to an in-memory timeline store; provider-native persistence handles and provider-history hydration are also present. The inspected `bootstrap.js` path does not establish a durable normalized timeline backend. Do not assume all transcripts are durably stored in one Paseo format.
- `server/server/atomic-file.js` and JSON-backed registries establish atomic-file persistence patterns in some stores. They do not prove that every state category uses the same mechanism or durability guarantees.

## Observed provider and extension model

- `agent/provider-registry.js` builds providers dynamically from built-ins, configured overrides, plugin providers, and derived providers. Concrete clients imported there include Claude, Codex App Server, Copilot ACP, Cursor ACP, generic ACP, Kimi ACP, Kiro ACP, OpenCode, OMP, Pi RPC, and Trae ACP. Provider definitions/capabilities are declared separately in `protocol/dist/provider-manifest.js`. This is a registry of different integration styles, not a single uniform backend protocol.
- Provider clients expose differing capabilities and persistence behavior. ACP-based providers use child-process/protocol adapters; others have provider-specific clients/runtimes. The code also supports custom providers and providers derived from built-ins. Exact availability on this host was not tested in this investigation.
- Plugins are isolated behind a child-process runtime and a validated plugin-process protocol (`server/server/plugins/{runtime.js,plugin-process.js,plugin-process-protocol.js}`). Treat this process/protocol boundary as a security and lifecycle requirement; the package code alone does not establish that every plugin is sandboxed at the operating-system level.
- MCP is both an agent integration and an HTTP surface. The installed `@getpaseo/protocol` declares the `/mcp/agents`-related message/config types; `bootstrap.js` mounts the route. The details of externally reachable network exposure were not tested.

## Proposed Rust reconstruction (design, not observed Paseo behavior)

1. **Preserve external contracts before internal structure.** Build fixtures from the message schemas and binary-frame codecs; version and validate the hello/session envelope; model request correlation, subscriptions, event replay needs, and protocol errors. Do not clone the TypeScript implementation’s module layout literally.
2. **Separate daemon transport from application orchestration.** Keep typed, provider-neutral domain/application services behind transport adapters. Implement local IPC/desktop concerns separately from the daemon WebSocket/client contract; keep authorization policy in the daemon, with inbound checks and outbound filtering.
3. **Model provider capabilities and session persistence explicitly.** Keep provider-native handles opaque and distinguish provider history from Paseo’s normalized timeline. Make create/resume/import/cancel/close outcomes and missing capabilities typed, rather than assuming every provider supports the same lifecycle.
4. **Treat durable state as separate repositories with fixtures.** Specify configuration, project, workspace and agent-record formats independently. Do not silently rewrite the existing Paseo home during reconstruction; compatibility/import behavior needs its own validated, read-only evidence and fixtures.
5. **Adopt the repository’s implementation constraint, not Paseo’s provider breadth.** The Rust project supports Talos/ACP as the primary backend and Claude Code as the only secondary provider, with an `egui`/`eframe` desktop frontend. That is a project decision, not a claim about the observed Paseo provider priority. The broader Paseo registry remains historical parity evidence only.

## Reconstruction checklist

- [ ] Capture protocol fixtures for `hello`, session request/response, `rpc_error`, server/status events, and version/capability negotiation.
- [ ] Implement and round-trip test terminal, file-transfer, and host-route binary frame codecs; bound frame sizes and reject malformed/unknown opcodes safely.
- [ ] Map each supported request/event to an explicit permission and test both inbound denial and outbound filtering, including alternative permission requirements.
- [ ] Define agent, run, permission, timeline, workspace, and provider-session state machines; preserve create/resume/import/cancel/close/archive distinctions.
- [ ] Implement Talos and Claude Code through the shared capability-driven ACP adapter; keep provider handles and provider-specific event payloads opaque and versioned.
- [ ] Inventory only redacted/synthetic legacy fixtures before designing any importer for config, project/workspace registries, agent records, or provider history. Keep any importer read-only by default.
- [ ] Specify daemon startup, single-instance ownership, shutdown, child-process cleanup, and recovery from interrupted work; test crash paths rather than assuming supervisor restart means session recovery.
- [ ] Keep the desktop bridge narrow: daemon operations through the typed client protocol, desktop-only window/browser/system APIs through the UI shell.
- [ ] Mark unimplemented Paseo surfaces (schedules, plugins, voice, browser, MCP, Git/checkout, terminal/file streams) as explicit parity gaps until their contracts and threat boundaries are independently captured.

## Source index

| Concern | Primary evidence |
|---|---|
| Shipped desktop bridge and settings | `app.asar!/dist/{main.js,preload.js,settings/desktop-settings.js,settings/window-state.js}` |
| Daemon bootstrap and routes | `@getpaseo/server/dist/server/server/bootstrap.js` |
| Daemon worker/supervisor | `@getpaseo/server/dist/server/server/daemon-worker.js`; `dist/scripts/{supervisor-entrypoint.js,supervisor.js}` |
| WebSocket handshake/transport | `@getpaseo/server/dist/server/server/websocket-server.js`; `@getpaseo/client/dist/daemon-client-websocket-transport.js` |
| Session RPC schemas | `@getpaseo/protocol/dist/messages.js`; server dispatch in `@getpaseo/server/dist/server/server/session.js` |
| Binary streams | `@getpaseo/protocol/dist/binary-frames/{terminal.d.ts,file-transfer.d.ts,host-route.d.ts,demux.d.ts}` |
| Authorization | `@getpaseo/server/dist/server/server/authorization/{index.js,operation-permissions.js}` |
| Agent lifecycle/storage/providers | `@getpaseo/server/dist/server/server/agent/{agent-manager.js,agent-storage.js,provider-registry.js}` |
| Configuration/home/workspaces | `@getpaseo/server/dist/server/server/{persisted-config.js,paseo-home.js,workspace-registry.js,workspace-registry-model.js,atomic-file.js}` |
| Plugin process boundary | `@getpaseo/server/dist/server/server/plugins/{runtime.js,plugin-process.js,plugin-process-protocol.js}` |
