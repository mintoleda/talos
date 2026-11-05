# Implementation status and execution checklist

This is the execution tracker for [PLAN.md](PLAN.md). The feature requirements
remain in [feature-parity.md](architecture/feature-parity.md); file ownership is
in [workstreams.md](architecture/workstreams.md).

## Baseline and status rules

Evidence snapshot: the standalone `paseo-rs` working tree over `f56b32a`,
imported unchanged into the Talos monorepo under `desktop/` (`main`).
Commands below run from `desktop/`. This is not a release certification.

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

Implementation remains within P0/P1; P2–P4 are deferred, with desktop UI last.
GPT-6 Luna (`xhigh`) completed the initial lifecycle/recovery pass and targeted
independent reviews; DeepSeek Flash (`high`) completed keyed-acceptance concurrency
and process/tool-lifecycle regression work. The orchestrator integrated their work
and updated evidence below. Migration preflight remains safety hardening only; it
does not advance or certify P3 feature parity. Fixture results do not close the P1
live Talos or Claude Code gates.

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
Automatic stale lock/socket removal remains intentionally unsupported.

### Installed Talos probe (no model calls)

Installed `talos 0.2.0` successfully negotiated ACP v1 and created a session using
isolated temporary `HOME`/`TALOS_HOME`. After terminating and restarting the
provider, loading that **empty, never-prompted** native session failed with
`session not found`. No model requests were sent, no real provider credentials
were used, and temporary homes were removed. This is not prompted-session resume
acceptance; that live gate remains open and must not silently create a replacement
session or change providers.

## Verification snapshot

Historical pre-implementation baseline (superseded by implementation results
when recorded below):

| Check | Result | Meaning |
|---|---|---|
| `cargo check --workspace --locked` | Pass, desktop warnings | Compiles; not runtime acceptance |
| `cargo fmt --all -- --check` | Fail | Existing desktop formatting debt; no broad formatting applied |
| `cargo clippy --workspace --all-targets -- -D warnings` | Fail | Desktop warnings/lints block the workspace gate |
| `cargo test --workspace` | Fail | Desktop `state::tests::permissions_are_scoped_and_resolved_by_run` fails; 21 other desktop tests pass; run stops before establishing other packages |
| `cargo clippy --workspace --all-targets --exclude talos-desktop -- -D warnings` | Pass | Non-desktop lint baseline only |
| `cargo test --workspace --exclude talos-desktop` | Pass | Non-desktop automated baseline only |

The excluded-desktop commands are interim gates, never substitutes for the final
workspace gates. Live Talos, real daemon restart recovery, fresh installation,
and full migration acceptance were not exercised in this inventory.

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

These are an initial integrated checkpoint, not completion of P1 or the release.

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

## Current implementation inventory

| Area | Status / evidence | Remaining acceptance |
|---|---|---|
| Domain and SQLite | Fixture-verified foundation: `crates/talos-domain`, `crates/talos-store`; store tests cover reopen, migrations, rollback and `concurrent_prompt_acceptance_is_atomic_across_connections` | Crash/restart recovery across the complete daemon/provider stack |
| Talos / Claude Code ACP | Fixture-verified transport: `crates/talos-acp/tests/process.rs` covers streaming, ordered event barriers, permission cancellation, load replay, malformed/large frames, process exit and redaction | Authenticated live Talos and Claude Code workflows plus provider-native resume after daemon restart |
| Core orchestration | Fixture-verified recovery, cancellation-safe lifecycle writes and durable keyed acceptance; core tests cover concurrent conflicts, restart/interrupted retries, dropped callers and shutdown draining | Live authenticated Talos and Claude Code acceptance; forced-abort shutdown after the drain deadline is not covered by the successful-drain guarantee |
| Daemon / IPC | Fixture-verified commands and delivery: `apps/talosd/src/main.rs`, `crates/talos-ipc/tests/{protocol,reliability}.rs` cover scoped replay, truncation, clock rollback, fragmented input, lag and auth; combined process smoke passed 10/10 | Live-provider restart/resume and post-deadline forced-shutdown behavior |
| CLI / provider config | Fixture-verified process workflow and keyed retries; CLI tests cover typed session cursors, multi-page follow and stalled-page rejection | Live-provider acceptance, automatic reconnect and config reload boundary remain open |
| Projects / workspaces | Partial: workspace create/list/get/rename/archive/restore are fixture-verified through domain, SQLite, core, IPC, daemon and CLI; archiving rejects workspaces with active persisted runs and preserves IDs/history | Project CRUD/association, safe managed-worktree creation/deletion and persisted branch/ownership links |
| Permissions / trust | Partial: durable timeline plus pending request/decision handlers and scoped fixture tests | Restart/timeout behavior must fail closed; stale decisions must not approve another run |
| Usage / diagnostics | Partial: summaries and provider diagnostics have daemon fixture tests | Unknown usage/prices versus zero, aggregation/replay deduplication and redaction acceptance |
| Attachments / output | Open beyond JSON CLI output | Image validation, durable storage/reference replay, provider capability rejection and migration integrity |
| Terminals / scripts | Open PTY backend; script management deferred | Owned PTY lifecycle/input/resize/capture/cleanup with two-workspace isolation |
| Legacy migration | Partial: `crates/paseo-migrate/src/lib.rs`; 22 tests cover dry-run/idempotent workspace-subset import, overlap/symlink/hardlink guards, WAL-aware snapshots, journaled retry and rollback | Real-data/source-hash workflow and kill/restart crash windows; sessions/history/attachments remain explicitly unsupported |
| Packaging / settings | Partial: `.github/workflows/ci.yml`, `packaging/`, `docs/release/README.md` | Fresh-home install/upgrade/uninstall and config validation without modifying user data; no signed/cross-platform release claim |
| Desktop | Deferred, existing code retained in `apps/talos-desktop` | Fix failing test/lints/formatting, then connect verified backend contracts and perform visual/accessibility QA |
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
- [x] Make `scripts/talosd-smoke.py` reliable: after fixing delayed provider-event
  attribution, cancellation sequence races and shutdown-drain ownership, the full
  suite passed 10/10 consecutive runs at `--timeout 90`; the ignored Rust wrapper
  also passed. Earlier 2026-11-03 failures are retained above as historical evidence.
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
- [ ] Repeat prompt/follow-up/cancel/restart-resume against installed Talos with
  credentials in an isolated provider home. Record provider version and redacted
  outcomes; never write to the real `~/.talos` home or legacy `~/.paseo`.
- [ ] Verify explicit Claude Code selection through the official ACP adapter or report it unavailable; preserve session ownership and never silently substitute a provider.

Exit: repeatable fixture harness plus recorded live Talos acceptance; backend
and CLI can complete the vertical slice without the desktop.

## P2 — close local daily-driver backend parity

Depends on P1. Owners: domain/store/core plus daemon/IPC/CLI adapters.

- [ ] Implement/verify project and workspace CRUD/archive/restore and managed
  worktree ownership checks; never delete dirty or unmanaged checkouts.
- [ ] Finish capability-driven model/mode/thinking controls and provider metadata
  round-trip, including explicit unsupported/unavailable errors.
- [ ] Implement/verify image MIME/size/path checks, durable attachment references,
  restart replay and missing-file errors through CLI and provider adapters.
- [ ] Add owned PTY creation/list/input/resize/capture/kill; prove two-workspace
  isolation and child cleanup without relying on a desktop terminal widget.
- [ ] Expose history/search and basic usage through CLI/IPC; test unknown usage,
  unknown prices and reconnect aggregation without double counting.
- [ ] Verify invalid config rejection, atomic persistence, redaction and a
  documented reload-versus-restart boundary.

History search is fixture-verified through SQLite, core, IPC, daemon, and
`talosdctl history search`. It performs bounded, case-insensitive substring
search over user/assistant text, supports an optional session and result limit,
and reports `has_more`; it intentionally excludes tool payloads and has no
continuation cursor. Usage semantics (unknown versus zero, pricing, and
aggregation deduplication) remain open.

Workspace lifecycle is fixture-verified through domain, SQLite, core, IPC,
daemon, and CLI: rename/archive/restore preserve IDs and history, and archive
rejects persisted active runs. Project association and safe managed-worktree
creation/deletion remain open.

Current backend verification from `desktop/`: `cargo test --workspace
--exclude talos-desktop --locked --no-fail-fast` passed (one opt-in smoke test
ignored); strict non-desktop Clippy and `cargo fmt --all -- --check` passed.
These exclude the desktop UI only. The workspace UI/release gates have not been
run as part of this non-UI workstream. Config validation and durable atomic
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
  for the existing workspace-subset importer. Tests cover WAL snapshots, orphan
  backup refresh, idempotent rerun, pending-import resume and rollback after a
  conflicting import. Kill/restart crash-window acceptance remains open.
- [ ] Preserve projects/workspaces, stable IDs, archives and ownership metadata;
  import sessions/history and hashed attachments or label unsupported records
  explicitly historical/read-only. Never claim a workspace-only import is parity.
- [ ] Run corrupt/missing-file, archived/worktree and partial-import fixtures;
  verify source hashes unchanged and no credentials/runtime/plugin execution.
- [x] Add and run `python3 scripts/packaging-acceptance.py --verbose`: pass for
  checksum-verified fixture archives, fresh install, upgrade, uninstall,
  `.talosd`/`.paseo` and unrelated-file retention, and symlink refusal. This
  CI packaging job uses stub executables; it does not test a compiled artifact
  or real CLI workflow.
- [ ] Install a compiled Linux release artifact and verify its real CLI workflow;
  the current CI workflow does not assemble or upload a release archive.
- [x] Update `docs/release/README.md` and `scripts/README.md` with the tested
  install/uninstall contract and its limits. Live desktop acceptance remains
  pending.

Exit: tested non-UI install and migration contract, passing interim gates and
recorded source-preservation evidence. Ready for desktop integration.

## P4 — desktop integration and release QA (UI last)

Depends on P3; workstreams B/C/D start here. Existing code is a starting point,
not evidence that the desktop acceptance gate is complete.

- [ ] Resolve desktop permission-state test failure, formatting and strict lint debt.
- [ ] Integrate stable IPC contracts; repeat the P1 lifecycle through the desktop,
  including offline/reconnect, permissions and restart/history.
- [ ] Complete shell/composer, icons, markdown/tools/diffs, image/terminal surfaces,
  history/search, keyboard navigation and capability-driven controls.
- [ ] Review 1600×900, 1280×800, 900×650 and 720×520; check reduced motion,
  high contrast, focus, permission prominence and long-timeline performance.
- [ ] Run `cargo fmt --all -- --check`,
  `cargo clippy --workspace --all-targets -- -D warnings`, and
  `cargo test --workspace` with no desktop exclusion; all must pass.
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
