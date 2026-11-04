# Workstreams — Paseo Rust rebuild

Status: backend-first execution; desktop work deferred until the final local-MVP
phase. This is an ownership map, not a claim that delegated agents are currently
running or that deliverables are complete. Current evidence and task status live
in [../STATUS.md](../STATUS.md). Every workstream must keep its own packages green
(`cargo test`, `cargo clippy -- -D warnings`) and must not reformat files owned by
another workstream.

## Dependency order and phase gates

1. **P0:** review/checkpoint backend changes; establish non-UI test/lint baseline.
2. **P1:** A plus ACP/storage owners prove daemon/CLI lifecycle, live Talos,
   permissions, restart, concurrency and replay before desktop integration.
3. **P2:** A plus domain/storage/provider owners close local backend parity:
   projects/worktrees, capabilities, images, PTY, search/usage and config.
4. **P3:** migration/storage plus E prove safe import and Linux distribution.
5. **P4:** B/C/D implement and verify the desktop last; E completes desktop QA
   and runs the full-workspace release gates without exclusions.

Existing UI code remains in place. Do not start shell polish, widgets, screenshots
or UI-only lint cleanup during P0–P3. Backend acceptance must not depend on the
unfinished desktop. Broader M3 work remains separately deferred.

Owner roles below are assignments for execution, not completion statuses. For
P0–P3 regression checks use `cargo test --workspace --exclude paseo` and
`cargo clippy --workspace --all-targets --exclude paseo -- -D warnings`; the
full workspace checks remain mandatory before release.

## Architecture contract

- `paseo-domain` — provider-neutral IDs, lifecycle, timeline events, permissions.
- `paseo-acp` — ACP stdio client for Talos and Claude Code.
- `paseo-store` — SQLite persistence and migrations.
- `paseo-core` — provider registry, agent/session orchestration, configuration.
- `paseo-ipc` — versioned newline-JSON IPC between daemon and clients.
- `apps/paseod` — daemon.
- `apps/paseo-cli` (`paseoctl`) — operator CLI.
- `apps/paseo` — egui desktop client.
- `apps/paseo-migrate`, `crates/paseo-migrate` — read-only legacy inventory.

Talos (`talos acp`) is the default provider. Claude Code is the only secondary
provider and never silently replaces the provider that owns an existing session.

## Workstream A — daemon, IPC, CLI (P0–P2)

Owner files: `crates/paseo-ipc/**`, `crates/paseo-core/**`, `apps/paseod/**`,
`apps/paseo-cli/**`, `docs/architecture/*.md` (IPC sections only).

Deliverables:

1. Additive IPC commands (protocol version stays 1; unknown commands already fail
   closed):
   - `list_pending_permissions` → `[{id, session_id, run_id, action, target, risk, details}]`
   - `list_models {provider_id}` → `[{id, label, description, thinking_options, default_thinking_option_id, available}]`
   - `archive_session {session_id}` / `rename_session {session_id, title}`
   - `set_thinking {session_id, thinking_option_id}`
   - `usage_summary {session_id?}` → per-session and aggregate token/cost records
   - `shutdown`
2. `snapshot` includes `pending_permissions`, `usage`, `providers` with models and
   modes, and `version`.
3. CLI: `session new <workspace> --provider --title`, `session archive|rename`,
   `agent list|show`, `permission list|allow|deny`, `events tail --follow`,
   `model list --provider`, `shutdown`. Remove "not available in IPC v1" stubs.
4. Provider configuration: `~/.paseo-rs/config.toml` supports `default_provider`
   and per-provider `command`, `args`, `env`; document a Talos setup that passes
   model/auth via env into `talos acp` (e.g. `TALOS_HOME`, `OPENAI_API_KEY`) and
   never reads or writes legacy `~/.paseo` or `~/.talos`.
5. Tests: IPC round-trip for every new command, daemon handler tests for
   permission scoping and usage aggregation, CLI parsing tests. Keep public docs
   current.

## Workstream B — egui shell and composer (deferred to P4)

Owner files: `apps/paseo/src/main.rs`, `app.rs`, `backend.rs`, `state.rs`,
`theme.rs`, `ui/mod.rs`, `ui/composer.rs`, `ui/sidebar.rs`, `ui/overlays.rs`.

Deliverables: 60 px rail, resizable 286 px history, centered timeline, inspector,
settings, command palette, reference-fidelity composer, responsive 720/1040/1440
breakpoints, keyboard map, reduced motion and high contrast.

## Workstream C — icon and widget kit (deferred to P4)

Owner files: `apps/paseo/src/ui/icons.rs`, `apps/paseo/src/ui/widgets.rs`.

Deliverables: hand-painted 24-grid vector icons for every `Icon` variant, square
icon buttons with hover wash, tooltips, presence dots, chips, section labels,
keyboard hints, dividers. No emoji glyphs in the shell.

## Workstream D — timeline and markdown (deferred to P4)

Owner files: `apps/paseo/src/ui/timeline.rs`, `apps/paseo/src/ui/markdown.rs`.

Deliverables: markdown rendering with code blocks and copy affordances; user
turns; streamed assistant blocks with a caret; collapsible reasoning; tool cards
with states and expandable output; high-salience permission cards with
allow-once/deny and in-flight state; error/notice blocks; scroll assist and a
"new messages" pill; empty and offline states.

## Workstream E — QA, docs, packaging (non-UI P0–P3; desktop QA P4)

Owner files: `docs/**` (except architecture IPC), `packaging/**`, `README.md`,
`apps/*/README.md`, integration tests under `tests/`.

P0–P3 deliverables: maintain evidence-backed status, process-level CLI lifecycle
and restart/reconnect tests, safe migration fixtures and source hashes, temporary
fresh-home packaging/install checks, release notes and accurate install docs.

P4 deliverables: screenshot review at 1600×900, 1280×800, 900×650 and 720×520;
keyboard walkthrough; desktop offline/reconnect and stale-event tests; perf check
with a long timeline; full-workspace formatting, strict Clippy and tests.

## Supporting backend ownership — P1–P3

These packages were missing from the original desktop-heavy owner map. Assign a
named owner before concurrent implementation; do not let multiple streams edit
them implicitly.

- **Domain/storage:** `crates/paseo-domain/**`, `crates/paseo-store/**` — durable
  lifecycle/recovery, project/workspace metadata, attachment references and
  transaction/replay integrity. Coordinate public type changes with A.
- **Providers:** `crates/paseo-acp/**` — Talos/Claude Code transport and process
  supervision, capabilities, live-provider smoke tests and native resume.
  Provider integration in `crates/paseo-core/**` remains owned by A.
- **Migration:** `crates/paseo-migrate/**`, `apps/paseo-migrate/**` — path safety,
  dry-run/backup, recoverable import and source-preservation fixtures; coordinate
  storage API changes with the domain/storage owner.
- **PTY:** select module/package ownership before implementation in P2; expose
  typed lifecycle operations through A's IPC/CLI without introducing UI ownership.

Acceptance tasks and dependencies are tracked once in `../STATUS.md`; this file
must not become a second, contradictory completion checklist.
