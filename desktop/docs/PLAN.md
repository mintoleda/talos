# Paseo Rust rebuild plan

## Product boundary

Paseo is the interface and control plane. Talos owns the default coding-agent loop, tools, transcript, context management, and safety classification. Paseo owns projects/workspaces, provider selection, process/session supervision, the durable UI timeline, permission presentation, persistence, and desktop/CLI experiences.

Talos is launched as `talos acp`. Claude Code is the only secondary provider, launched through the official Claude Agent ACP adapter. Paseo never silently changes the runtime that owns an existing session.

## Architecture

```text
talos-gpui ────────────┐
                      ├─ local versioned IPC ─ talosd
talosdctl ────────────┘                         │
                                               ├─ talos-core (use cases/provider registry)
                                               ├─ talos-store (SQLite + migrations)
                                               └─ providers
                                                   ├─ Talos ACP (default)
                                                    └─ Claude Code ACP
```

Shared domain types are provider-neutral. ACP owns newline-delimited bidirectional JSON-RPC, process lifecycle, capability negotiation, streaming updates, permissions, cancellation, load/resume, and model/mode/config changes.

## Workstreams and ownership

1. **Domain + storage** — IDs, lifecycle state machines, events, permissions, provider descriptors, SQLite migrations/repositories.
2. **ACP/Talos** — robust stdio transport, typed RPC, Talos session adapter, fake ACP fixture tests.
3. **Core + providers** — Talos/Claude Code registry and default selection, agent orchestration, event fan-out, cancellation, and persistence integration.
4. **Daemon + IPC** — local authenticated endpoint, snapshots/commands/events, reconnect/replay, lifecycle and diagnostics.
5. **GPUI desktop** — navigation, history, timeline/tool/permission surfaces, responsive shell, reference-inspired prompt bar and reduced-motion-aware animation.
6. **CLI + migration + packaging** — diagnostics, provider/session commands, read-only legacy inventory/import, desktop launch and release artifacts.

## Delivery sequence — backend first, UI last

[STATUS.md](STATUS.md) is the authoritative execution checklist: current evidence,
verification failures, dependencies and acceptance gates. Source presence is not
completion; fixture tests do not establish live-provider or restart acceptance.

1. **P0 — baseline and review:** inventory/review existing backend changes and
   establish passing non-desktop tests/lints without modifying unfinished UI.
2. **P1 — CLI vertical slice (M1 backend):** Talos session creation, streaming,
   follow-up, permissions, cancellation, persistence/resume, concurrent isolation,
   reconnect/replay and explicit Claude Code selection. Prove the workflow through
   daemon/IPC/CLI with fixtures and a live Talos run before desktop integration.
3. **P2 — local daily-driver backend (M2 backend):** projects/workspaces and safe
   worktrees, model/mode/thinking capabilities, durable images, PTY lifecycle,
   history/search, usage/diagnostics and configuration, all exposed without UI.
4. **P3 — migration and distribution:** read-only inventory, backup and recoverable
   import, source-preservation tests and fresh-home Linux install/upgrade checks.
5. **P4 — desktop integration and QA (M1/M2 UI):** only after P0–P3 pass, finish
   the GPUI shell/composer, timeline/tools/diffs, permission/attachment/terminal
   surfaces, accessibility, responsive layout and release acceptance. Preserve
   existing UI work in the meantime; defer its cleanup and expansion to this phase.

This sequencing does not waive the full-workspace release checks or the final
CLI **and** desktop workflow. Non-desktop checks are interim gates only.

### M3 — broader Paseo parity (separate post-local-release backlog)

Remote hosts/pairing, schedules/heartbeats, browser/MCP platform tools, voice,
OS notifications, usage dashboards, plugin compatibility and cross-platform
installers/update/rollback remain explicit parity debt. They do not block P4
unless a local MVP acceptance requirement specifically depends on them. The UI
is the final phase of the current local-MVP implementation, not an indefinite
wait for all future platform features.

## Visual direction

Refined industrial minimalism: near-black canvas, translucent charcoal surfaces, hairline separators, restrained soft-blue primary accent, and a warm-orange provider mark. Motion is short and purposeful. The composer is the visual anchor: a low, wide rounded shell with the placeholder `Ask anything. @ files, $ skills, / commands`, model/provider selector, activity indicator, attachment control, and circular blue send/stop control.

## Definition of done

The rebuild is considered usable when a fresh install can create or import a workspace, run and resume Talos sessions, stream text/tools/usage, resolve permissions, cancel safely, persist history, explicitly select Claude Code when desired, and complete the same workflow through both the GPUI app and CLI without modifying the legacy `~/.paseo` tree. Full parity remains tracked explicitly in `docs/architecture/feature-parity.md`.
