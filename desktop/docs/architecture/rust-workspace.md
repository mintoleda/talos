# Paseo Rust workspace: architecture proposal

**Status:** design proposal, not implementation. This report is based on the locally observable Paseo runtime surface and repository state; it does not claim source-code parity.

## Evidence boundary

### Observed

- `/home/adetola/repos/paseo-rs` was an empty Git checkout at inspection time (no tracked source files or commits), and no applicable `AGENTS.md`/`CLAUDE.md` was found. No implementation is being modified here.
- The runtime's exposed control surface includes provider/model discovery, agent creation/status/activity/cancellation, workspaces, terminals, scheduled runs, permissions, and browser automation. These are evidence of supported operations in this installation—not proof of every shipped feature or its internal implementation.
- The provider registry reports Claude, Codex, Pi, Antigravity and Talos available; Copilot, OpenCode, OMP, Muse and Cursor are present but unavailable in this environment. Capability/mode sets differ by provider. In particular, the registry describes Copilot as using Agent Client Protocol (ACP).
- The local `~/.paseo` directory contains runtime/configuration state such as `config.json`, agent/provider state, projects/workspaces, schedules, plugins, attachments, and logs. Only configuration key names/types and the empty package-lock metadata were inspected; secret values, logs, and the Talos checkout were not read.
- The workspace listing included local checkouts and directory workspaces, and a separate Talos checkout exists. This report does not modify either `~/.paseo` or `/home/adetola/repos/talos`.

### Not established

The underlying Paseo source, exact database/schema, wire protocols, recovery guarantees, UI framework, and legacy on-disk formats were not available in this checkout. Recommendations below are proposed design choices. Before implementing compatibility importers, inventory and fixture the real legacy formats using redacted/test copies.

## Design goals

1. One reliable local daemon owns mutable application state and process lifecycles; UI and CLI are clients.
2. Keep domain rules independent of Tokio, SQLite, UI, subprocesses, and vendor SDKs.
3. Stream agent output/events without allowing slow clients to block execution or persistence.
4. Treat external agents, plugins, and workspace processes as untrusted boundaries; limit credentials and permissions.
5. Preserve valuable user state during migration, but do not perpetuate incidental legacy internals in the new design.
6. Make failure, cancellation, restart, and compatibility behavior explicit and testable.

## Workspace and dependency direction

Start as a Cargo workspace with these crates (some can begin as modules if the workspace is initially small; split when boundaries stabilize):

| Crate | Responsibility | Must not depend on |
|---|---|---|
| `paseo-domain` | IDs, value types, state machines, domain events, validation, provider-neutral concepts | Tokio, database, UI, concrete providers |
| `paseo-application` | Use cases/commands, authorization decisions, orchestration ports, transaction boundaries | UI toolkit, concrete database/transport/provider |
| `paseo-store` | SQLite repositories, migrations, transaction/outbox implementation, legacy import readers | UI, provider SDKs |
| `paseo-providers` | Provider registry and native/API adapters behind application ports | UI, direct database access |
| `paseo-acp` | ACP JSON-RPC transport/session implementation over child-process stdio | Application policy, UI |
| `paseo-process` | Child-process supervision, terminal/workspace process lifecycle, OS-specific process handling | Domain policy, UI |
| `paseo-ipc` | Versioned daemon-client protocol, framing, local socket setup/authentication | Provider implementations, UI toolkit |
| `paseo-daemon` | Composition root, Tokio runtime, IPC server, workers, shutdown | UI toolkit |
| `paseo-ui` | Desktop shell, view state, IPC client, rendering/input | Store and provider implementations |
| `paseo-cli` | Headless client commands, diagnostics, import/export | Store internals (all mutations through daemon) |
| `paseo-test-support` | Fake ports, fixtures, deterministic clocks/process harnesses | Production dependency graph |

Dependency direction: `domain <- application <- daemon`; `store`, `providers`, `acp`, `process`, and `ipc` implement/use ports at the edge. `ui` and `cli` speak only the public IPC protocol. Avoid a giant `common` crate and avoid passing database rows or provider SDK types into domain/UI code. Keep feature flags at edge crates, not as pervasive conditional compilation in the domain.

## Domain model

Use strongly typed newtypes (`WorkspaceId`, `AgentId`, `RunId`, `ProviderId`, `ScheduleId`, `PermissionRequestId`, `EventId`) and validated value objects (`ModelRef`, `WorkspacePath`, `AgentConfig`, `ProviderMode`, `SecretRef`). Use UUID/ULID-like opaque IDs; do not encode filesystem paths, provider names, or sequential database keys as identity. Timestamps should be UTC instants plus explicit timezone identifiers for human schedule definitions.

Core aggregates/entities:

- **Workspace**: local path or managed worktree, isolation kind, lifecycle, metadata.
- **Agent**: chosen provider/model/mode, workspace, configuration snapshot, status, active run reference.
- **Run**: immutable input/config snapshot, start/end, terminal outcome, cancellation reason and process/session references.
- **Conversation/event stream**: ordered user/assistant/tool/provider events with stable IDs and opaque, versioned payloads where provider-specific fidelity is required.
- **Permission request**: action and target, risk category, request/run identity, expiry, and explicit pending/allowed/denied/expired resolution.
- **Provider descriptor**: capabilities, modes, model discovery and availability. Capabilities are negotiated, not inferred from provider name.
- **Schedule** and **schedule run**: recurrence definition, timezone, bounds (expiry/max runs), next fire time, result history.
- **Workspace process/terminal**: supervised process identity, state, exit status, and bounded output cursor.

Represent state transitions with enums and checked methods (e.g. `Queued -> Starting -> Running -> Succeeded | Failed | Cancelled`), not arbitrary strings. Domain event names should be stable and versioned at serialization boundaries. Keep rich provider-specific data in adapter-owned payloads, while normalizing common text, tool calls, usage, permission requests, and completion into cross-provider types.

## Async execution and event delivery

Use **Tokio** in daemon/edge crates. A single multithread runtime is the straightforward default for concurrent provider I/O, IPC, timers, and subprocess supervision. Do not create a runtime per agent. Keep CPU-heavy parsing/indexing off async workers with bounded `spawn_blocking` or dedicated workers; never hold a synchronous SQLite lock or domain mutex across `.await`.

Separate **commands** (request/response, validated and authorized) from **events** (ordered notifications). Use bounded Tokio `mpsc` for internal work queues, `watch` for latest state such as shutdown/config generation, and broadcast/fan-out at IPC edges only where loss is acceptable. A slow client must be disconnected or told to resume from a cursor; never let its backpressure stall a provider process or database writer. Persist user-visible events before publishing them; assign monotonically increasing per-run sequence/cursor values. On reconnect, clients fetch a snapshot and replay from a cursor, with duplicate-safe event IDs.

Use a transactional outbox (or atomically committed event rows) so state changes and their durable events cannot diverge. Avoid a general-purpose distributed event-bus dependency: this is a single-host daemon and a small explicit event router with bounded queues is easier to reason about.

## Persistence and migrations

Use **SQLite** as the initial local store: it is embedded, portable, transactional, and fits a single-user daemon without another service. Use WAL mode, foreign keys, a busy timeout, and explicit transaction boundaries. Choose one async strategy and keep it behind `paseo-store`: `sqlx` with compile-time/runtime checked queries is suitable if its SQLite features are validated on supported targets; alternatively use `rusqlite` on a dedicated bounded blocking writer/connection pool if operational simplicity is preferred. Do not mix ad hoc database access across crates. Benchmark realistic event volume and retain bounded/rotatable logs and output blobs; large attachments should live as files addressed by metadata rows rather than huge SQLite blobs by default.

Keep ordered SQL migration files embedded in the binary, each with a schema version/checksum. Migrations must be transactional when SQLite permits; for non-transactional steps, use a staged/restart-safe procedure. At startup: acquire a single-instance lock, make a verified backup before destructive migrations, migrate, then verify schema/integrity. Test upgrades from every released schema fixture, not just a fresh database. Separate durable core state from rebuildable caches and indexes.

A legacy importer should be read-only against the old state by default: explicit user invocation, dry-run summary, redact secrets from reports, copy/backup before import, idempotent mapping, preserve opaque IDs where useful, and record source/version/import outcome. Never start by silently rewriting or deleting `~/.paseo`; support export and recovery. Translate established settings and histories where semantics are known, and surface unsupported data for review instead of silently dropping it.

## Configuration and secrets

Use layered, typed configuration: built-in defaults < user config (TOML, versioned) < environment overrides for deployment/testing < explicit CLI overrides. Parse into validated structs at startup and apply reloadable fields atomically as a new configuration generation; restart-only fields should be clearly marked. Keep durable user preferences/config separate from operational state in SQLite.

Store API keys and refresh tokens through the OS credential store (e.g. `keyring`-backed platform facilities); config/database hold only opaque `SecretRef`s. Provide an encrypted fallback only if a secure platform backend is unavailable, with an explicit passphrase/unlock lifecycle—never plaintext fallback. Child agents receive only credentials required for their provider and only via the provider's supported secure channel; avoid inheriting the daemon's entire environment. Redact secrets, auth headers, and sensitive command arguments in errors/traces. Protect Unix sockets and state directories with user-only permissions; Windows uses user-scoped named-pipe ACLs.

## Provider and ACP boundaries

Define an async object-safe application port, for example `Provider::capabilities`, `list_models`, `start_session`, and session methods for prompt/input, mode/config changes, cancel, and event streaming. Return provider-neutral event types plus a versioned opaque extension payload. Treat streaming as a `Stream` with explicit end/error semantics. Use capability flags for streaming, resume, modes, tools, permissions, image input, usage, and session persistence; support partial capability sets.

Provide two adapter families:

1. **Native/API adapters** for providers that benefit from direct APIs or require Paseo-native lifecycle/permission semantics.
2. **ACP adapters** for compatible external agent executables. Keep ACP protocol framing, JSON-RPC request correlation, initialization/capability negotiation, session setup, prompt streaming, cancellation, and stderr handling inside `paseo-acp`. Spawn with explicit executable/args/environment/cwd, pipe stdin/stdout separately from stderr, enforce startup/idle/shutdown timeouts, bound message sizes, validate JSON-RPC IDs, and treat malformed/unknown messages as protocol errors with diagnostics. Never parse human-readable stdout as a control protocol. Stop the child process tree on cancellation/shutdown, with graceful ACP cancellation followed by a bounded grace period and forced termination. Keep compatibility shims for protocol/version quirks in adapters, not application use cases.

Provider availability is runtime discovery, not a compile-time guarantee. A missing binary, invalid credentials, or unsupported capability should be represented as a structured unavailable/degraded status. Provider implementation selection and aliases belong in a registry/config migration layer, not in domain logic.

## Daemon/UI communication

Use a local, versioned **JSON-RPC 2.0** protocol initially, with newline-delimited JSON or length-prefixed frames over a Unix domain socket (Windows named pipe). JSON is debuggable and makes provider/plugin boundaries straightforward; benchmark before considering a more compact protocol. Define explicit protocol version negotiation, request IDs, typed method schemas, error codes, event sequence cursors, and a maximum frame size. Do not expose raw database models as wire contracts. Generate or share schema types only through a small protocol crate with compatibility tests.

The daemon binds only to a local endpoint, authenticates the local user via OS permissions and a per-install capability token where the platform/transport requires it, and rejects remote connections by default. UI reconnect uses exponential backoff, a fresh snapshot, then event replay. Mutations are idempotent where feasible (caller-supplied request ID/idempotency key), and clients must tolerate duplicate events. CLI is another protocol client; diagnostic/read-only commands should work when possible without opening UI. Version mismatch should fail clearly or negotiate a supported subset, not deserialize optimistically.

## Cancellation, shutdown, and supervision

Use `tokio_util::sync::CancellationToken` with a hierarchy: daemon shutdown -> agent/run -> provider session / child process / stream subscription. Cancellation is a request, not a fabricated completion: persist a cancellation-requested state, notify provider, wait a bounded grace period, terminate child process tree if necessary, reap it, then persist the terminal outcome. Make cancellation idempotent and race-safe against normal completion. Distinguish user cancel, timeout, daemon shutdown, provider failure, and forced kill.

On daemon shutdown: stop accepting new work, mark/drain in-flight work according to policy, cancel child scopes, flush persisted events, stop scheduler/workers, close database, and release the single-instance lock. On restart, reconcile runs left active against process/session records and mark them interrupted/recoverable; never claim they succeeded merely because the daemon restarted. Test cancellation at each lifecycle boundary and ensure no zombie processes or unbounded tasks.

## Security and permissions

Treat provider output, ACP peers, workspace files, plugins, attachments, and IPC payloads as untrusted. Validate bounds and paths; prevent workspace traversal/symlink escapes in sensitive operations. The permission flow is a first-class domain protocol with explicit allow/deny, timeout, audit event, and race-safe one-time resolution. Persist enough information to explain a decision without retaining secrets. Keep security policy in application/domain services and enforce it independently of UI affordances.

## Testing and quality gates

- **Domain unit tests:** state-machine transition tables, validation, capability logic, serialization compatibility.
- **Application tests:** fake repository/provider/clock/event sink; verify idempotency, authorization, transactional ordering, retries, and cancellation races.
- **Store tests:** temporary SQLite database, migration from every schema fixture, rollback, concurrent read/write, restart recovery, integrity checks.
- **ACP tests:** golden protocol transcripts, fragmented/coalesced frames, large/malformed messages, request correlation, stderr isolation, timeout/cancel/child exit, fake executable. Do not depend on commercial agents in CI.
- **IPC tests:** protocol compatibility/version negotiation, reconnect/replay, auth denial, slow consumer, oversized frame, duplicate command.
- **Process tests:** shell-free argument handling, environment allowlist, cwd/path safety, process-tree teardown per supported OS.
- **End-to-end tests:** daemon plus fake providers and real temporary socket/database; cover create workspace/agent, event stream, permission, cancel, restart and schedule run.
- **Compatibility tests:** sanitized legacy fixtures and importer idempotence; never use the real home directory in tests.
- CI: `cargo fmt --check`, `cargo clippy --workspace --all-targets --all-features -- -D warnings`, tests on Linux/macOS/Windows as applicable, dependency audit/license review, and reproducible release build checks. Keep system integration tests separated from fast unit tests.

## Tracing and diagnostics

Use `tracing` spans across IPC request -> application command -> provider session -> run event; attach IDs (`workspace_id`, `agent_id`, `run_id`, provider) as fields, not high-cardinality metric labels. Emit structured logs to stderr/journald on server deployments and rotating files for desktop installs, with configurable retention and redaction. Persist durable user-facing run events separately from diagnostic logs. Add counters/histograms for queue depth, provider startup/first-token/total latency, event backlog, cancellation duration, and failures; expose a local health/diagnostics command. Never log prompts/tool payloads by default; any opt-in verbose capture needs clear consent, retention controls, and redaction.

## Packaging and operations

Build daemon, desktop UI, and CLI as separate binaries from shared crates. Ship platform-native installers (initially user-local, no service/root requirement) that create state/config directories with restrictive permissions and register daemon autostart only with explicit consent. Support a foreground daemon mode for debugging and a managed background mode with graceful OS shutdown. Bundle migrations and protocol schemas in the binary; discover optional ACP executables rather than bundling every provider. Provide signed release artifacts, checksums, SBOM/dependency notices, crash-safe upgrade/rollback guidance, and uninstall behavior that preserves user data unless explicitly requested. Keep platform-specific process tree management, keyring, socket, autostart, and packaging behind narrow interfaces.

## Incremental delivery plan

1. **Contract and inventory:** freeze a feature/capability matrix from current runtime behavior; record compatibility fixtures using redacted copies; define domain terms, event semantics, wire protocol and threat model.
2. **Foundation:** workspace/lints, domain IDs/state machines, config parsing, tracing setup, SQLite schema/migration harness, daemon single-instance/shutdown skeleton, CLI health command.
3. **One vertical slice:** one workspace, one agent run through a fake provider, durable events, IPC snapshot/replay, minimal UI displaying status/output, cancellation and restart recovery. Establish the boundaries before adding providers.
4. **First real provider + ACP:** implement one native adapter and ACP transport against a fake executable and then one real compatible agent; add capability negotiation, permissions and robust child lifecycle.
5. **Parity core:** add provider registry/model discovery, workspace/process/terminal operations, history, permission UI, config/secrets, and remaining high-value providers behind capability-driven UI.
6. **Schedules/plugins/integrations:** add scheduler persistence/recovery and plugin/browser/attachment functionality only after core lifecycle and security boundaries are stable; use separate process/protocol isolation for extensions.
7. **Migration and hardening:** importer dry-run/backup, data compatibility tests, cross-platform packaging, load/fault testing, security review, staged opt-in migration and rollback path.

Each milestone should ship a working vertical slice with explicit known gaps. Prioritize compatibility at external boundaries (user data, provider protocols, CLI/UI behavior), while using clean new internal domain types and schema rather than reproducing accidental legacy coupling.
