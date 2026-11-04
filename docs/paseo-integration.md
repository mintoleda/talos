# Paseo + Talos direction

> **Monorepo update.** The Paseo interface layer is now the Rust workspace in
> [`desktop/`](../desktop/README.md) inside this repository, not a separate
> product. In this document "Paseo" means that desktop (egui app `paseo`, daemon
> `paseod`, CLI `paseoctl`); the installed JavaScript Paseo remains a supported
> ACP client but is no longer the target. See [Monorepo layout](#monorepo-layout).

## The actual goal

The goal is not to replace Paseo's multi-harness support or turn Talos into another full desktop coding product.

The goal is to simplify the everyday stack to:

```text
Paseo
  └── Talos
        ├── Talos-native model providers
        └── selected subscription-backed harness bridges
```

- **Paseo** is the interface and control plane.
- **Talos** is the primary coding-agent runtime.
- Other complete harnesses remain available only where they provide something Talos cannot obtain directly, especially subscription-backed usage.

This preserves what is valuable about Paseo—session management, multiple agents, remote control, browser/voice/platform features, and the ability to connect other harnesses—without requiring Pi, Claude Code, Codex, and Talos to all be equal parts of the normal workflow.

## Monorepo layout

```text
talos/
├── cmd/talos/        Go CLI: TUI, `serve`, `attach`, `acp`
├── internal/         Go runtime: loop, providers, tools, safety, sessions, MCP
│   ├── acp/          ACP v1 server used by the desktop
│   └── stack/        runtime constructor shared by TUI, daemon and ACP
├── app/              Electron app (legacy; to be replaced by desktop/)
├── desktop/          Rust workspace: egui app, paseod, paseoctl, store, ACP client
├── docs/
└── Makefile          `talos`, `app*`, `desktop*` targets
```

The only interface between the two languages is the `talos acp` process
boundary: the Rust daemon spawns the Go binary and speaks ACP over stdio. There
is no FFI, shared database or shared session store. Consequences:

- Talos owns the model transcript under its own home (`~/.talos` or
  `TALOS_HOME`); the desktop owns the UI timeline in its SQLite store under
  `~/.paseo-rs`.
- Runtime behavior is fixed once, in Go. The Rust side must not reimplement
  loop, tool, compaction or safety logic.
- Changes to the ACP surface land in `internal/acp` and
  `desktop/crates/paseo-acp` together, with tests on both sides
  (`go test ./internal/acp/...`, the `acp-fixture` process tests and
  `desktop/scripts/paseo-smoke.py`).
- CI (`.github/workflows/ci.yml`) runs the Go build/vet/test job and the desktop
  backend acceptance job on every push; full-workspace desktop UI gates are
  non-blocking until `desktop/docs/STATUS.md` closes them.

`talos serve` and the Electron app keep working unchanged while the desktop
reaches parity; they are not used by the desktop.

## Why

Pi has been an excellent primary harness and strongly influenced Talos. Paseo is an excellent layer above harnesses. Talos already contains the agent behavior and implementation that can become the preferred default beneath Paseo.

The desired simplification is therefore:

- Paseo replaces separate harness user interfaces.
- Talos replaces Pi as the default general-purpose loop beneath Paseo.
- Claude Code or another harness remains available when its subscription, authentication, or unique runtime is specifically useful.

This is not an attempt to erase the value of Pi or eliminate Paseo's provider ecosystem. It is a way to reduce the number of overlapping default layers.

## Responsibilities

### Paseo owns

- The desktop/web user experience
- Projects, workspaces, and worktrees
- Session and agent management
- Remote hosts and fleet orchestration
- Scheduling
- Voice and browser integration
- Notifications
- Permission presentation
- High-level timelines and usage display
- Selection between Talos and exceptional external harnesses

### Talos owns

- The primary system prompt
- The agent loop
- Model API translation
- Native coding tools
- Tool execution and safety classification
- Context management and compaction
- Canonical model transcripts and resume behavior
- Skills, memory, and local subagents
- MCP consumption, including Paseo platform tools

## What to keep from Talos

The Paseo-oriented Talos runtime is more than `internal/loop`. A reliable runtime still needs:

- `internal/loop`
- `internal/provider`
- `internal/protocol`
- `internal/tools`
- `internal/executor`
- `internal/safety`
- Transcript, pruning, compaction, and resume functionality from `internal/session`
- Headless portions of `internal/engine`
- Configuration and authentication
- MCP support
- Skills
- Relevant memory and subagent functionality
- Usage, model metadata, and prompt-cache accounting
- A transport adapter for Paseo, preferably ACP

## What Talos no longer needs as a product layer

Once Paseo integration is working, the slim Talos branch does not need its own:

- Bubble Tea TUI
- Electron application (superseded by `desktop/`)
- Project rail
- Attach client
- User-facing multi-session daemon
- Filesystem browser UI RPCs
- Worktree orchestration
- Merge-review UI
- Desktop notifications
- Other presentation-specific code already supplied by Paseo

Low-level Git support can remain where it is part of checkpoints or tool safety.

## Integration

Talos should expose a headless command:

```bash
talos acp
```

Paseo should register it as a provider through its existing Agent Client Protocol integration.

In ACP mode:

- Paseo supplies the selected working directory or worktree.
- Talos does not create another worktree.
- Talos owns the model transcript.
- Paseo stores the Talos session/native handle and owns the UI timeline.
- Talos emits permission requests; Paseo asks the user and returns the decision.
- Talos uses Paseo's MCP server for browser, scheduling, remote-host, and cross-agent capabilities.

## Models versus harnesses

Two different concepts must remain separate.

### Talos-native model providers

These feed model inference into the Talos loop. Talos owns the prompt, tools, transcript, compaction, and behavior.

Examples include:

- Anthropic API models
- OpenAI API models
- OpenAI-compatible endpoints
- Other future raw model APIs

This is the preferred path because every model receives the same Talos behavior and tool environment.

### Subscription-backed harness bridges

Claude Code and Codex are complete agent harnesses, not raw model APIs. Their subscription authentication may provide usage that is not available to Talos through a normal API key.

They will remain separate Paseo providers alongside Talos. Talos will not wrap Claude Code, Codex, or another complete agent harness as a backend.

This keeps ownership honest: a Talos session uses the Talos loop, while a Claude Code session uses Claude Code's loop. Paseo provides the common interface and session-management layer for both.

## Claude subscription usage

Using a Claude model through Talos's ordinary Anthropic API adapter normally requires API billing; it is not automatically covered by a Claude Pro or Max subscription.

Claude Code itself can use the subscription. Anthropic also documents subscription-backed use of Claude Code, `claude -p`, and the Claude Agent SDK. Therefore Claude subscription usage can be preserved, but it should be treated as a Claude Code/Agent SDK execution path rather than assumed to be raw Messages API access for Talos's native loop.

The practical initial design is:

```text
Paseo
  ├── Talos       <- default; Talos loop and tools; API-backed models
  └── Claude Code <- optional; preserves Claude subscription usage
```

There is no current plan to put the Claude Agent SDK inside Talos. Paseo already has a mature Claude Code adapter that preserves subscription usage, so duplicating that integration would add complexity without improving the product boundary.

The same principle applies to any Codex subscription or other harness-specific allowance: keep the external harness as a separate Paseo provider when it provides valuable authentication, billing, or runtime behavior that a raw model adapter cannot reproduce.

## Branch plan

Preserve the current standalone application and create a slim integration branch:

- `talos-full`: current standalone Talos, including TUI, Electron app, daemon, and worktree/merge UX.
- `talos`: headless primary runtime intended to sit beneath Paseo.

The Rust desktop is developed on the `talos` branch under `desktop/`.

Do not delete UI code before the headless path works. First add `talos acp`, connect it to Paseo, and verify sessions, tools, permissions, cancellation, compaction, and resume. Then remove redundant product layers from the `talos` branch.

Avoid maintaining two independent copies of the agent loop. Runtime fixes should have one clear source of truth.

## Initial target architecture

```text
                         Paseo
             UI / sessions / workspaces / fleet
                  /                       \
                 /                         \
        Talos provider                Claude Code provider
       default daily path             subscription exception
              |
       Talos agent runtime
      /                  \
 native coding tools    Paseo MCP tools
```

Paseo can retain its broader provider ecosystem. The simplification comes from making Talos the default rather than removing every escape hatch.

## Migration sequence

1. Preserve the current commit as `talos-full`.
2. Create the slim `talos` branch.
3. Add a headless ACP entrypoint while retaining existing code temporarily.
4. Separate the reusable runtime constructor from Talos UI/daemon wiring.
5. Register Talos in Paseo.
6. Verify prompt streaming, tools, permissions, cancellation, session resume, and context compaction.
7. Inject Paseo platform capabilities through MCP.
8. Keep Claude Code enabled in Paseo for subscription-backed usage.
9. Compare Talos with the existing Pi path on real work before changing the default.
10. Remove redundant Talos UI and orchestration layers only after the Paseo path is dependable.

## Definition of success

The project succeeds when normal work feels like using one product—Paseo—with Talos supplying the preferred agent behavior, while subscription-backed Claude Code and other genuinely useful external harnesses remain available without dominating the architecture or everyday mental model.

## Implementation status

Steps 1–5 and 7 are implemented on the `talos` branch. Step 6 is covered by `internal/acp` end-to-end tests (streaming, tools, permissions, cancellation, session load) and a live Paseo run; it still needs real-model use. Steps 8–10 are operational decisions.

### `talos acp`

`talos acp` serves ACP v1 (newline-delimited JSON-RPC on stdio) from `internal/acp`. Stdout carries only protocol frames; diagnostics go to stderr.

The runtime constructor now lives in `internal/stack` (`stack.Build`, `stack.SwitchModel`, `stack.ListModels`), separate from UI/daemon wiring: `internal/engine` wraps a built stack for the TUI and daemon via `engine.FromBuilt`, while `internal/acp` drives `loop.RunTurn` directly and depends on no TUI, client or server package. There is still one agent loop.

| ACP surface | Talos behavior |
| --- | --- |
| `initialize` | `loadSession`, `sessionCapabilities.list/close`, image + embedded-context prompts, MCP over stdio/HTTP/SSE |
| `session/new` | Allocates a session id and returns modes, models and a `thought_level` option. The runtime is built lazily on first use, so Paseo's probe sessions create no transcript, MCP connection or checkpoint. Talos uses the supplied `cwd` as-is and never creates a worktree. |
| `session/load` | Replays the Talos transcript as `user_message_chunk` / `agent_message_chunk` / `tool_call` / `tool_call_update`, then resumes on the same transcript |
| `session/list`, `session/close` | Backed by session meta sidecars; close cancels any turn and releases the runtime |
| `session/prompt` | One turn of the Talos loop. Text and thinking stream as message/thought chunks; tools stream as `tool_call` → `tool_call_update` with ACP kinds (`read`, `edit` with diffs, `search`, `execute`, `fetch`, `think`); context occupancy streams as `usage_update`; the response carries `stopReason` and token usage |
| `session/cancel` | Cancels the turn context; pending permission prompts resolve as denied; the prompt returns `stopReason: "cancelled"` |
| `session/request_permission` | Emitted when the safety policy prompts. Options: allow once, always allow (remembered per session, per command), reject |
| `session/set_mode` | `ask` / `auto` / `panic` map onto Talos permission modes |
| `session/set_model`, `session/set_config_option` | `provider/model` switching and thinking level, per session |
| Slash commands | `/compact [focus]`, `/model [provider/model]`, `/mcp`, `/subagents`, plus each skill (rewritten to `skill:<name>`) via `available_commands_update` |

Paseo's platform tools arrive through `mcpServers` on `session/new`/`session/load` as a streamable-HTTP server with a bearer header; `internal/mcp/http.go` supports that transport. Permission prompts are now answered asynchronously by the frontend (`internal/executor`) instead of failing closed when no reply is immediately available; the engine queues concurrent prompts from parallel subagents. Relative `path` arguments to `read`/`write`/`edit`/`ls` resolve against the session cwd rather than the process cwd, which matters because Paseo spawns Talos from the daemon's directory.

### Desktop registration

The Rust desktop registers Talos by default as `talos acp` from `PATH`. To use a
local build, set the provider command in `~/.paseo-rs/config.toml`:

```toml
[[providers]]
id = "talos"
display_name = "Talos"
kind = "acp"
enabled = true
command = "/home/<you>/repos/talos/talos"
args = ["acp"]

[providers.env]
# Optional; defaults to ~/.talos
# TALOS_HOME = "/home/<you>/.talos"
```

Provider environment is injected into the spawned process and redacted from
daemon diagnostics. See `desktop/apps/paseo-cli/README.md` for the full schema.

### Legacy Paseo registration

For the installed JavaScript Paseo, add to `~/.paseo/config.json` under `agents.providers`, then `paseo reload`:

```json
"talos": {
  "extends": "acp",
  "label": "Talos",
  "description": "Talos coding agent (native loop, API-backed models)",
  "command": ["/home/<you>/.local/bin/talos", "acp"],
  "enabled": true
}
```

Use an absolute path; the daemon's `PATH` may differ from your shell's. Verify with `paseo provider diagnostic talos`. Talos reads provider credentials from its own config (`~/.talos`, or `TALOS_HOME`) or environment; add `"env": {...}` to the provider entry if the daemon's environment lacks them.
