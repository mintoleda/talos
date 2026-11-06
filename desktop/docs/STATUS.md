# Implementation status and execution checklist

This is the execution tracker for [PLAN.md](PLAN.md). The feature requirements
remain in [feature-parity.md](architecture/feature-parity.md); file ownership is
in [workstreams.md](architecture/workstreams.md).

## Baseline and status rules

Evidence snapshot: final certification of the current shared dirty checkout at
`c7cfcc1` (`main`), with source modifications listed by `git status` preserved
and uncommitted. Commands run from `desktop/` unless otherwise stated. This records
verified gates and limitations; it is not a claim of release readiness.

- **Implemented**: a source path exists; this does not establish acceptance.
- **Fixture-verified**: relevant automated checks passed without a live provider.
- **Partial**: some requirements exist, but the full acceptance gate is open.
- **Open**: no completion evidence recorded; investigate before implementing.
- **Deferred**: intentionally outside the current phase, not removed from scope.
- Check a task only after recording its command/scenario, outcome and scope here.
  An unavailable live provider is a blocker, not a passing result.

**Execution order: P0 → P1 → P2 → P3 → P4. Desktop UI is P4, last.** Preserve
existing UI changes; no visual work, widget expansion or UI cleanup is scheduled
before the non-UI gates. Broader parity remains a separate backlog, not a hidden
prerequisite that makes the desktop wait indefinitely.

## Backend implementation pass

Backend implementation through distribution and the application shell currently
builds, formats, lints, and passes full-workspace tests; interactive UI and full
provider-authenticated acceptance remain limited as recorded below. Project/workspace
migration covers a subset of P3, not complete migration parity. Do not treat fixture
results as live provider or interactive visual acceptance.

Initial implementation pass, integrated and regression-tested:

- Cancellation-safe partial IPC frame reads on server and client; authentication
  and protocol checks on every command; bounded queues with explicit replay
  requirements instead of silent event loss.
- Additive `replay_sessions` with independent per-session cursor maps, pagination
  that never acknowledges truncated records, and ordered-session-head merging
  that remains safe across backwards wall-clock adjustments. See
  [the replay contract](architecture/ipc-replay.md).
- CLI `events replay --cursors`, `session replay --session`, and paginated
  follow/replay deduplication with gap/lag recovery. Transport disconnection still
  ends follow; automatic reconnect is not claimed.
- ACP idle-reader busy-spin fix: retain the watch receiver's seen version instead
  of cloning it repeatedly. A regression test bounds idle-reader polling.
- Bounded/tracked daemon connections, handshake/write deadlines and bounded
  shutdown draining. Startup validates legacy-home aliases before any writes;
  token creation is private and rejects symlinks; failed startup cleans its socket.
- Startup reconciliation marks orphaned runs `Interrupted`, cancels unresolved
  permissions and resets busy agents without automatically restarting providers.
  Run completion persists cleanup before terminal publication; shutdown joins run
  writers and drains accepted lifecycle operations before store close, aborting any
  work left only after the bounded drain deadline; create/load are serialized.
- Scoped permission decisions persist outcomes before replying; cancelled/dropped
  callbacks are reconciled before the terminal run event. Provider mutations
  reject values absent from advertised capabilities/options.
- A real-process fixture harness and independent CI `backend-acceptance` job.
  The complete initial suite and opt-in Rust wrapper passed, including cancellation
  while a permission was pending and shutdown/restart under a pending permission.
  Harness results are not proof of live model/provider parity.

`request_key` provides durable, session-scoped acceptance deduplication: identical
key/payload retries return the original run, while conflicting payloads fail. This
is not exactly-once external execution; do not automatically retry unkeyed prompts
after a lost response. See [the prompt-acceptance contract](architecture/prompt-acceptance.md).
Daemon startup now recovers only a regular lock marker whose PID returns `ESRCH`
and a Unix socket whose connection is refused; unknown/importer markers and live
or unverifiable paths remain fail-closed. The persistent `talosd.guard` inode
serializes concurrent starts without removing the importer's create-new marker.

### Isolated live-provider probe

Installed Talos `0.2.0` and pinned Claude ACP `0.100.0` session creation, provider
restart, and native resume of the same session passed in isolated temporary homes;
no replacement session was created. Authenticated streaming, permission, cancel,
and usage workflows could not be exercised because isolated provider credentials
were unavailable. Provider output and secrets were suppressed; no real `~/.talos`
or `~/.paseo` home was accessed. These results do not certify authenticated live
workflow acceptance.

## Final certification snapshot (2026-11-08)

Commands run against the current dirty source at `c7cfcc1`; no source was staged,
committed, reverted, or cleaned. `git diff --check` passes. Workspace gates run
from `desktop/`:

| Command | Outcome |
|---|---|
| `cargo test --workspace --locked` | Pass: 239 passed, 0 failed, 1 ignored opt-in process wrapper. Desktop: 27 tests passed, including permission scoping and headless responsive-shell rendering. |
| `cargo clippy --workspace --all-targets --locked -- -D warnings` | Pass |
| `cargo fmt --all -- --check` | Pass |
| `go build ./... && go test ./...` (repo root) | Pass |
| `cargo build --release --locked -p talosd -p talosdctl -p talos-desktop --bins` | Pass |
| `python3 scripts/talosd-smoke.py --timeout 240` (all scenarios) | Pass: daemon + CLI process-level scenarios, including dirty-worktree refusal and managed-worktree quarantine/archive/restore preserving tracked content; isolated temporary homes and fixture provider. |
| `python3 scripts/talosd-smoke.py --scenario lifecycle --scenario keyed --scenario isolation --scenario redaction --scenario interrupt --scenario cancel-permission --scenario terminal --timeout 300` | Pass; fixture provider and isolated temporary homes |
| `python3 scripts/talosd-smoke.py --scenario projects --timeout 300` | Fail at stale destructive-delete assertion described above; no code changed to mask it |
| `python3 scripts/packaging-acceptance.py --compiled-artifact --verbose` | Pass: release-binary install/project/worktree preservation/CLI workflow, stale daemon restart, replacement, uninstall and retention; all in temporary homes. |
| `systemctl --user is-system-running` | Unavailable: `systemctl` executable not found (exit 127); no service actions attempted. |
| `git diff --check` | Pass |

Interactive visual review was not performed. GUI display variables are present but
were not used to claim visual acceptance. Tests provide headless rendering evidence
only. Live-provider evidence above is limited to isolated Talos `0.2.0` and pinned
Claude ACP `0.100.0` creation/restart/native-resume; authenticated streaming,
permission, cancel, and usage remain blocked by absent isolated credentials.
Provider output/secrets were suppressed; real `~/.talos` and `~/.paseo` were not
accessed. No usable systemd user manager or real legacy data was available.

Historical evidence follows and is superseded where current outcomes above say so:

| Check | Result | Meaning |
|---|---|---|
| `cargo check --workspace --locked` | Historical baseline | Superseded by final verification below |
| `cargo fmt --all -- --check` | Historical fail | Superseded: final check passes |
| `cargo clippy --workspace --all-targets -- -D warnings` | Historical fail | Superseded: final check passes |
| `cargo test --workspace` | Historical fail | Superseded: final check passes; current desktop count is recorded below |
| `cargo clippy --workspace --all-targets --exclude talos-desktop -- -D warnings` | Historical pass | Superseded by final full-workspace Clippy pass |
| `cargo test --workspace --exclude talos-desktop` | Historical pass | Superseded by final full-workspace test pass |

This section is historical; final workspace gate results and live-provider limits
are recorded below.

### Monorepo import check (2026-11-03)

Run from `desktop/` after importing into the Talos monorepo; source unchanged.

| Check | Result |
|---|---|
| `cargo fmt --all -- --check` | Pass |
| `cargo clippy --workspace --all-targets --exclude talos-desktop --locked -- -D warnings` | Pass |
| `cargo test --workspace --locked --no-fail-fast` | All backend crates pass; desktop `permissions_are_scoped_and_resolved_by_run` still fails (pre-existing) |
| `go build ./... && go vet ./... && go test ./...` (repo root) | Pass |
| `python3 scripts/talosd-smoke.py --timeout 300` | **Flaky**: 1 of 4 runs passed (3 in `desktop/`, 1 in the original `paseo-rs` checkout). Failures: "tool lifecycle events were not attributed to the tool prompt run" (×2) and a cancel run stuck in `CancelRequested` (×1). Same behavior before the move; open race, see P1 |

### Initial integrated implementation results

Before the follow-up DeepSeek work:

| Check | Result |
|---|---|
| `cargo fmt --all -- --check` | Pass |
| `cargo test --workspace --exclude talos-desktop --locked` | 69 passed; 0 failed; 1 opt-in smoke wrapper ignored |
| `cargo clippy --workspace --all-targets --exclude talos-desktop --locked -- -D warnings` | Pass |
| `cargo build --locked -p talosd -p talosdctl -p talos-acp --bins` | Pass |
| `python3 scripts/talosd-smoke.py --verbose --timeout 90` | Entire initial process suite passed; tail delivered 88 unique events across two sessions at one-event page size |
| `cargo test -p talosdctl --test daemon_smoke -- --ignored --nocapture` | Opt-in wrapper passed the entire process suite |
| Forced Python `--timeout 1 --keep` using frozen binaries | Expected failure; all five traced fixture children gone after cleanup; retained debug home explicitly removed |
| `cargo test --workspace --locked` | Existing desktop permission-state test still fails (21 desktop tests pass) |
| `cargo clippy --workspace --all-targets --locked -- -D warnings` | Existing desktop lint debt still blocks full workspace |

Historical initial integrated checkpoint; current acceptance is recorded in the final snapshot above.

### P1 keyed-acceptance and lifecycle follow-up (2026-11-05)

The following combined smoke/idempotency changes were applied and verified in an
isolated integration worktree based on `3487808`; they are not yet committed to
`main`. Commands run from `desktop/` in that worktree.

| Check | Result | Scope |
|---|---|---|
| `cargo fmt --all -- --check` | Pass | Combined backend patch |
| `cargo test --workspace --exclude talos-desktop --locked --no-fail-fast` | Pass: 115 passed, 0 failed, 1 ignored wrapper | Non-desktop fixtures/unit tests |
| `cargo clippy --workspace --all-targets --exclude talos-desktop --locked -- -D warnings` | Pass | Non-desktop strict lint gate |
| `cargo build --locked -p talosd -p talosdctl -p talos-acp --bins` | Pass | Process-harness binaries |
| `python3 scripts/talosd-smoke.py --timeout 90` | 10 consecutive full-suite passes | Temporary homes and fake ACP provider; no live model/provider |
| `cargo test -p talosdctl --test daemon_smoke -- --ignored --nocapture` | Pass: 1 test | Opt-in process wrapper |
| `git diff --check` | Pass | Combined patch |

Independent targeted review found no blocker in the latest lifecycle and keyed
acceptance changes. Shutdown is bounded: if the lifecycle-drain deadline expires,
remaining durability tasks are aborted before store close; durable completion is
verified for successful draining, not after that forced-abort deadline. Live Talos
resume, Claude Code, desktop acceptance, P3 migration/distribution and full-workspace
release gates remain unverified.

### Explicit archival session-import follow-up (2026-11-05)

Run from `desktop/`. The optional `--sessions-source` has no inferred default;
the importer snapshots selected source files as bytes and never repairs or writes
them. Supported message text and uniquely paired tool calls are imported into
archived records; unknown/malformed records remain raw extensions, and validated
inline PNG/JPEG/GIF/WebP payloads are copied by SHA-256 into private content-
addressed storage. Folded archive messages are merged in archive order, then
unmatched current-transcript order. Canonical JSON identity ignores whitespace
and object-key order when removing pinned copies; per-source repeat counts remain
intact, while identical turns across folds are recorded as ambiguous. Every
physical archive/current record is retained byte-for-byte as base64 with raw
SHA-256, line and byte offset provenance. Unknown and malformed archive records
remain raw-only. Oversized or over-budget sessions are left wholly unimported.
Imported sessions have no native provider handle or run IDs, and archived agents
are rejected by both cached and provider-loading session paths.

| Check | Result | Scope |
|---|---|---|
| `cargo test -p paseo-migrate -p paseo-migrate-cli --locked` | Pass: 57 tests | Migration fixtures, malformed/oversize bounds, canonical deduplication, provenance, source preservation and CLI kill/recovery/idempotency |
| `cargo test -p talos-core -p talos-store --locked` | Pass: 67 tests | Core archived-agent refusal and store historical-import invariants, plus regressions |
| `cargo test --workspace --locked` | Historical failure | Superseded by final workspace pass recorded above |
| `cargo test --workspace --exclude talos-desktop --locked` | Pass: 204 passed, 1 ignored | Full non-UI Rust workspace; Unix-socket daemon fixtures ran outside the sandbox |
| `python3 scripts/talosd-smoke.py --timeout 120` | Pass | Fake-provider daemon/CLI process scenarios; temporary fixture state only |
| Temporary `paseo-migrate` CLI apply twice | Pass: first imported 1 session; rerun imported 0 | Explicit temporary source/destination fixture; no real home |
| `cargo clippy --workspace --all-targets --exclude talos-desktop --locked -- -D warnings` | Historical pass | Superseded by final full-workspace Clippy pass |
| `rustfmt --edition 2024 --check` on migration sources and the CLI fixture | Pass | Formatting for this follow-up |
| `cargo fmt --all -- --check` | Blocked by missing desktop module and formatting differences in other dirty files | Workspace-wide formatting check |
| `git diff --check` | Pass | Current combined patch |

No real legacy sessions source was available at the standard location, so no real
data dry-run was performed. No provider, installed runtime or UI was accessed.
The fixture acceptance does not verify every transcript variant, precise
per-message timestamps, provider runtime state, non-inline attachments, legacy
worktree reconstruction/state, plugins, or kill points beyond the covered journal
and database recovery windows.

## Current implementation inventory

| Area | Status / evidence | Remaining acceptance |
|---|---|---|
| Domain and SQLite | Fixture-verified foundation: `crates/talos-domain`, `crates/talos-store`; store tests cover reopen, migrations, rollback and `concurrent_prompt_acceptance_is_atomic_across_connections` | Crash/restart recovery across the complete daemon/provider stack |
| Talos / Claude Code ACP | Fixture-verified transport: `crates/talos-acp/tests/process.rs` covers streaming, ordered event barriers, permission cancellation, load replay, malformed/large frames, process exit and redaction. Isolated Talos 0.2.0 and Claude ACP 0.100.0 create/restart/native-resume probes passed without replacement. | Authenticated streaming, permission, cancel and usage remain blocked by absent isolated credentials |
| Core orchestration | Fixture-verified recovery, cancellation-safe lifecycle writes and durable keyed acceptance; core tests cover concurrent conflicts, restart/interrupted retries, dropped callers and shutdown draining | Live authenticated Talos and Claude Code acceptance; forced-abort shutdown after the drain deadline is not covered by the successful-drain guarantee |
| Daemon / IPC | Fixture-verified commands and delivery: `apps/talosd/src/main.rs`, `crates/talos-ipc/tests/{protocol,reliability}.rs` cover scoped replay, truncation, clock rollback, fragmented input, lag and auth; core process smoke scenarios passed this run | Projects smoke still has an obsolete destructive-delete assertion; post-deadline forced-shutdown behavior remains open |
| CLI / provider config | Fixture-verified process workflow and keyed retries; CLI tests cover typed session cursors, multi-page follow and stalled-page rejection | Live-provider acceptance, automatic reconnect and config reload boundary remain open |
| Projects / workspaces | Partial: CRUD/association, managed worktree quarantine-preservation, restore, dirty/untracked refusal, and ownership validation have unit and compiled-artifact coverage | Crash/interruption recovery around Git/store boundaries and actual systemd-user runtime acceptance remain open |
| Permissions / trust | Partial: durable timeline plus pending request/decision handlers and scoped fixture tests | Restart/timeout behavior must fail closed; stale decisions must not approve another run |
| Usage / diagnostics | Partial: provider usage remains nullable, CLI/IPC summary preserves unknown versus known zero, and cumulative snapshots are merged per run; summary is lifetime provider-reported subtotal, not an invoice or locally priced estimate | Coverage metadata for sessions without usage, provider-specific price tables, cross-provider billing semantics, and redaction acceptance |
| Attachments / output | Fixture-verified image prompt slice: MIME/path/size validation, content-addressed private storage, timeline references across restart, keyed retries and ACP capability gating through CLI/IPC; validated inline historical images are fixture-tested | Attachment retrieval/display and non-inline legacy payload migration remain open; no UI work is included |
| Terminals / scripts | Partial: owned PTY backend, workspace-scoped IPC/CLI, graceful cleanup, child cleanup after hard daemon death, and compiled-artifact stale lock/socket restart are fixture-verified | Runtime systemd-user cgroup acceptance and script management (deferred) |
| Legacy migration | Partial: `crates/paseo-migrate` imports projects/workspaces plus explicitly selected `--sessions-source` history as archived agents/sessions; fixtures cover text, ordered tool pairing, inline-image hashes, canonical folded-history deduplication with raw provenance, bounded malformed/oversize handling, idempotency, source mutation/symlinks and process-kill recovery | No real home or provider was accessed; unsupported transcript/runtime state, non-inline attachments, legacy worktrees, plugins and desktop state remain unimported; other crash windows are open |
| Packaging / settings | Fixture and local compiled-artifact acceptance cover fresh install, upgrade replacement, uninstall retention, stale daemon restart, symlink refusal, and packaged systemd directives in temporary homes | Live systemd-user manager acceptance and release archive publication; no signed/cross-platform release claim |
| Desktop | Application shell compiles; full-workspace tests/lints/fmt pass and headless responsive rendering test passes | Interactive lifecycle, visual/accessibility QA, and full surface acceptance remain open |
| Remote / schedules / browser-MCP / voice / OS notifications / plugins / usage dashboards | Deferred broader parity | Separate acceptance work after the local release; see parity inventory |

## P0 — establish a reproducible non-UI baseline

Owner: core/daemon/CLI maintainers; scope excludes `apps/talos-desktop`.

- [x] Inventory the dirty tree without discarding or committing unrelated changes.
- [x] Run non-desktop tests and strict Clippy; record the workspace blockers above.
- [x] Review initial uncommitted backend changes against IPC v1 compatibility,
  capability checks, bounded queues and cancellation/shutdown. Fixed partial-read
  corruption, replay cursor loss, ACP busy-spin and core terminal/permission races.
- [x] Add regression tests for those initial review findings and rerun interim
  gates: see initial integrated results above. Follow-up changes require new runs.
- [ ] Make a reviewed backend checkpoint separately from unfinished UI work.

Exit: reviewed non-UI diff, reproducible passing interim gates, no undocumented
IPC incompatibility. Do not mark the whole workspace green.

## P1 — prove the backend / CLI vertical slice

Depends on P0. Owners: ACP, core, storage, daemon/IPC, CLI.

- [x] Add a deterministic process-level daemon + CLI smoke harness using a fake
  provider and temporary Rust-owned home: `scripts/talosd-smoke.py`, with an opt-in
  Rust wrapper in `apps/talosdctl/tests/daemon_smoke.rs` and a dedicated CI job.
  Individual workflow acceptance below still requires a passing execution.
- [x] Fixture vertical slice: register workspace → create session → stream
  text/tools/usage → follow-up → permission allow/deny → cancel → rename/archive/
  history. The full process suite includes these scenarios and passed 10 consecutive
  times in the combined integration worktree; this is fake-provider fixture evidence,
  not live Talos acceptance.
- [x] Historical smoke reliability work fixed delayed provider-event attribution,
  cancellation sequence races and shutdown-drain ownership; the prior full suite
  passed 10/10 at `--timeout 90`. This does not describe current full-suite status:
  the projects scenario now fails because it expects destructive deletion, contrary
  to quarantine-preservation semantics. Current outcomes are in the final snapshot.
- [x] Persist optional prompt request keys transactionally with run acceptance.
  Store tests cover cross-connection identical/conflicting races and reopen; core
  tests cover restart/interrupted retries; the keyed process scenario verifies no
  duplicate provider invocation/events. This is durable acceptance deduplication,
  not exactly-once external execution.
- [x] Fixture acceptance: stop/restart the real daemon, replay ordered history and
  resume the same native fixture session without duplicate creation/replayed
  output (`scripts/talosd-smoke.py`, initial suite passed). Live Talos is separate.
- [x] Fixture acceptance: two simultaneous agents with distinct pending requests;
  duplicate, stale and post-cancel decisions fail closed without cross-talk
  (core `concurrent_permission_decisions_are_run_scoped_and_stale_ids_fail_closed`
  and process harness).
- [x] Fixture acceptance: cancel prompts/permission waits; durably settle requests
  before terminal state; bounded shutdown joins writers and drains accepted
  durability operations before store close. Core tests cover dropped cancel,
  permission-resolution and emission callers plus shutdown draining; process
  smoke passed 10/10. If the drain deadline expires, remaining operations are
  aborted; hard-kill stress and durable completion after forced abort are not claimed.
- [x] Fixture acceptance: disconnected-tail durable replay and one-event pages
  deliver 88 unique records across two sessions; IPC reliability tests force lag
  and signal replay explicitly. Session-local ordering is preserved across clock
  rollback. Automatic transport reconnect remains a separate open enhancement.
- [x] Fixture acceptance: missing/wrong auth, bad protocol, unadvertised mutation
  values and unavailable providers are rejected; two homes remain isolated;
  scripted provider secrets are redacted from timeline, diagnostics and logs
  (IPC/daemon unit tests plus complete process harness).
- [x] Isolated Talos `0.2.0` and Claude ACP `0.100.0` session creation, provider
  restart, and native resume of the same session passed without replacement; see
  the isolated live-provider probe above. Do not infer authenticated prompt workflows.
- [ ] Authenticated streaming, permission, cancel and usage workflows against both
  providers remain blocked by missing isolated credentials. Never use real
  `~/.talos` or legacy `~/.paseo`.

Exit: repeatable fixture harness plus recorded live Talos acceptance; backend
and CLI can complete the vertical slice without the desktop.

## P2 — close local daily-driver backend parity

Depends on P1. Owners: domain/store/core plus daemon/IPC/CLI adapters.

- [ ] Complete project/workspace parity. Projects have SQLite CRUD and durable
  workspace association; deletion refuses linked projects and association paths are
  checked. Managed worktree deletion validates ownership and cleanliness, then uses
  a Git worktree move into a hidden quarantine path rather than deleting contents.
  Archived workspaces can restore from that preserved path. Unit and compiled-artifact
  acceptance pass; the process projects scenario still asserts the old destructive
  semantics and fails. Crash interruption between Git and SQLite persistence and
  restart reconciliation remain open.
- [ ] Finish capability-driven model/mode/thinking controls and provider metadata
  round-trip, including explicit unsupported/unavailable errors.
- [x] Implement image MIME/size/path checks, content-addressed durable storage,
  timeline-reference replay, keyed acceptance and provider capability rejection
  through CLI/IPC/ACP. Retrieval/display of stored files remains for UI integration.
- [x] Owned PTY backend and acceptance. `apps/talosd/src/terminal.rs` bounds
  capture/input/dimensions, canonicalizes workspace-contained CWDs, and exposes
  list/input/resize/capture/kill over workspace-scoped IPC/CLI. Unit and daemon
  tests cover IO, resize, truncation, isolation, process-group kill and graceful
  shutdown. Debug CLI smoke and the compiled release artifact acceptance both
  verified descendant cleanup; the latter SIGKILLs the installed release daemon
  and confirms its PTY child exits. The systemd service also explicitly uses
  `KillMode=control-group`; actual systemd-user manager behavior is not exercised.
- [ ] Complete usage parity. History/search and `talosdctl usage [--session ID]` are
  exposed through CLI/IPC; absent provider metrics and prices remain JSON `null`,
  never fabricated zeroes. Usage snapshots are consolidated per run before
  aggregation, and repeated durable queries do not double count. Provider price
  tables and cross-provider billed-cost semantics remain open.
- [ ] Verify invalid config rejection, atomic persistence, redaction and a
  documented reload-versus-restart boundary.

History search is fixture-verified through SQLite, core, IPC, daemon, and
`talosdctl history search`. It performs bounded, case-insensitive substring
search over user/assistant text, supports an optional session and result limit,
and reports `has_more`; it intentionally excludes tool payloads and has no
continuation cursor. Usage summary is available through CLI/IPC. Unknown token
counts/costs serialize as `null`, known zero as `0`; a missing metric in any
usage-bearing run invalidates that aggregate rather than exposing a partial
subtotal. Cumulative same-run snapshots are merged before totals, and repeated
summary queries replay durable events without double counting. Costs are not
estimated from an unverified pricing source; provider price tables remain open.

Workspace lifecycle is fixture-verified through domain, SQLite, core, IPC,
daemon, and CLI: rename/archive/restore preserve IDs and history, and archive
rejects persisted active runs. Project CRUD and project/workspace association now
have SQLite/core/IPC/CLI fixtures plus the compiled CLI scenario `python3
scripts/talosd-smoke.py --scenario projects --verbose --timeout 90`; association
outside a project root, deletion while linked, dirty/untracked worktree removal,
and unmanaged checkout deletion are refused. Worktree create/delete and persistence
boundaries still need interruption/restart recovery acceptance.

Historical non-UI checkpoint above is superseded by the full-workspace gates in the
final certification snapshot. Project-scenario smoke remains blocked by its stale
destructive-delete expectation, although managed-worktree quarantine and restore
pass unit and compiled-artifact acceptance. Config validation and durable atomic
writes have focused tests; reload-versus-restart behavior remains open.

Exit: each local MVP requirement in the parity inventory has a non-UI test or
recorded CLI acceptance result. Script management, forge automation and remote
features remain separately deferred.

## P3 — safe migration and distribution

Depends on P2. Owners: migration/storage and packaging/QA.

- [x] Audit source/destination overlap, symlink traversal, hard-linked database
  files and SQLite sidecars with temporary fixtures; the daemon home guard also
  rejects the legacy `~/.paseo` destination. No real `~/.paseo` data was read or
  modified.
- [x] Add verified online SQLite destination snapshots and journaled retry/rollback
  for project/workspace inventory import. Tests cover WAL snapshots, orphan backup
  refresh, idempotent rerun, old-journal compatibility, pending-import resume,
  conflict rollback and source preservation. Kill/restart crash-window acceptance
  remains open.
- [x] Preserve projects/workspaces, stable IDs, workspace associations, archive
  state and allowlisted worktree metadata. Legacy ownership markers remain provenance
  only and never authorize deletion. Fixture tests verify source files remain
  unchanged.
- [x] Import supported session history only through explicit `--sessions-source`:
  text, unique tool pairs, raw extension fallbacks and validated inline images are
  fixture-tested as archived records with no native handles or runs. A pending
  session journal resumes after database commit, and reruns remain idempotent.
  Legacy agent configuration, provider/runtime state, unsupported transcript
  semantics and non-inline attachments remain unimported.
- [x] Exercise cross-process kill/restart recovery during session import with the
  CLI fixture: kill after the pending session journal is durable while a SQLite
  writer blocks the batch, then rerun and verify one archived session, one image,
  preserved source bytes and an idempotent second run. Other kill points remain
  open. No credentials/runtime/provider/plugin state is copied or executed.
- [x] Run corrupt/missing-file, archived/worktree and partial-import fixtures;
  verify fixture source hashes unchanged and no credentials/runtime/plugin execution.
- [x] `python3 scripts/packaging-acceptance.py --verbose` passes checksum-verified
  fixture install/upgrade/uninstall, data retention and symlink refusal. The
  additional compiled release path described below validates real binaries and
  CLI workflow locally; CI still runs fixture stubs.
- [x] Build the current `talosd` and `talosdctl` release binaries and pass
  `python3 scripts/packaging-acceptance.py --compiled-artifact --verbose`:
  compiled install, project/worktree/CLI workflow, PTY descendant death, stale
  lock/socket restart without manual removal, binary upgrade replacement,
  uninstall, and daemon-data retention all passed in temporary directories. The
  desktop binary was packaged but not interactively launched.
- [x] Rebuilt current release binaries with
  `cargo build --release --locked -p talosd -p talosdctl -p talos-desktop --bins`.
  Build passed; packaging acceptance exercised compiled daemon/CLI and desktop
  package layout (not interactive desktop launch).
- [ ] Exercise the unit under a live systemd-user manager. This environment
  returns `/bin/bash: systemctl: command not found` (exit 127) for
  `systemctl --user is-system-running`; no persistent user service was started
  or changed. The package acceptance checks the installed unit directives only.
- Historical focused-daemon checkpoint: `cargo test -p talosd --locked` (34 tests),
  strict non-UI Clippy, and `cargo test --workspace --exclude talos-desktop --locked`
  (204 passed, 1 ignored). Superseded by the full-workspace gates in the final
  certification snapshot; current projects process smoke has the documented stale
  destructive-delete assertion.
- [x] The current CI workflow still does not assemble or upload a release archive.
- [x] Update `docs/release/README.md` and `scripts/README.md` with the tested
  install/uninstall contract and its limits. Live desktop acceptance remains
  pending.

Exit: tested non-UI install and migration contract, passing interim gates and
recorded source-preservation evidence. Ready for desktop integration.

## P4 — desktop integration and release QA (UI last)

Depends on P3; workstreams B/C/D start here. Existing code is a starting point,
not evidence that the desktop acceptance gate is complete.

- [x] Desktop permission-state test, formatting, and strict lint gates pass in the final workspace run above.
- [ ] Integrate stable IPC contracts; repeat the P1 lifecycle through the desktop,
  including offline/reconnect, permissions and restart/history.
- [ ] Complete shell/composer, icons, markdown/tools/diffs, image/terminal surfaces,
  history/search, keyboard navigation and capability-driven controls.
- [ ] Review 1600×900, 1280×800, 900×650 and 720×520; check reduced motion,
  high contrast, focus, permission prominence and long-timeline performance.
- [x] Run `cargo fmt --all -- --check`,
  `cargo clippy --workspace --all-targets --locked -- -D warnings`, and
  `cargo test --workspace --locked` with no desktop exclusion; all passed in the
  final certification snapshot above.
- [ ] Record fresh-install and migration workflows through CLI and desktop;
  reconcile README/release/parity claims and publish a scoped release checklist.

Exit: the local MVP release gate in `architecture/feature-parity.md` is satisfied.
M3/broader parity remains explicitly unclaimed.

## Updating this tracker

For each completed task, add the source/test reference, command or manual
scenario, outcome and limitations in the relevant phase. Reopen tasks when the
contract changes. Keep fixture verification separate from live-provider,
restart, migration and desktop acceptance. Never infer release readiness from
compilation or a feature's presence in source alone.
