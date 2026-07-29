# architecture

```
┌──────────────────────────────────────────────────────────────┐
│                     cmd/talos                                  │
│  main.go ── CLI flags, daemon mode, attach, one-shot          │
│  app.go  ── session mgmt, Build(), engine wiring, MCP startup│
└──────────┬───────────────────────────────────────────────────┘
           │
           │  client.Engine interface (local or RemoteEngine)
           │
┌──────────▼───────────────────────────────────────────────────┐
│                      internal/tui                              │
│  model.go ── Bubble Tea model (vim-mode, slash commands,      │
│              panes: chat, tools, subagents, skill completions) │
│  dialogs/ ── login, confirm, model-picker, session-picker     │
│  panes/   ── chat (markdown renderer), tools, subagents        │
└──┬──────────────┬──────────────┬──────────────────────────────┘
   │              │              │
   │    Engine    │   Executor   │  Transcript / Compactor
   ▼              ▼              ▼
┌──────────┐ ┌──────────┐ ┌──────────────┐
│ engine   │ │ executor │ │   session    │
│ (client. │ │ tool     │ │ transcript   │
│  Engine) │ │ registry │ │ compactor    │
│ loop.Loop│ │ mcp.Mgr  │ │ historian    │
└──────────┘ └──────────┘ └──────────────┘
   │              │              │
   ▼              ▼              ▼
┌──────────┐ ┌──────────┐ ┌──────────────┐
│ protocol │ │  tools   │ │    safety    │
│ value-   │ │ read,    │ │ checkpointer │
│ typed    │ │ write,   │ │ policy,      │
│ events   │ │ edit,    │ │ classifier   │
│          │ │ bash, bg,│ │              │
│          │ │ search,  │ │              │
│          │ │ web,     │ │              │
│          │ │ memory,  │ │              │
│          │ │ skill,   │ │              │
│          │ │ fff, mcp │ │              │
└──────────┘ └──────────┘ └──────────────┘

                    ┌──────────────┐
                    │   server     │
                    │ (internal/   │
                    │  server)     │
                    │ unix-socket  │
                    │ daemon       │
                    │ attach client│
                    └──────────────┘
```

## seams

1. **`cmd/talos/`** wires everything together: parses CLI flags, loads config and auth, creates the provider, executor, MCP manager, engine, and session, then hands control to the TUI, one-shot renderer, or server.

2. **`internal/tui/`** — Bubble Tea TUI with a chat pane (markdown rendering), tools pane, subagents pane, skill-completion support, and dialogs for login, confirm, plan review, model picking, and session picking. Communicates with the engine through the `client.Engine` interface. Worktree merge-back review lives in the Electron app (`merge.*` RPCs), not the TUI.

3. **`internal/loop/`** — drives the LLM conversation: builds requests from the system prompt + tool schemas + transcript via `PromptBuilder`, streams responses from the provider, assembles tool results back into messages, and compacts when context is near capacity.

4. **`internal/provider/`** — abstracts the LLM API (OpenAI-compatible and Anthropic) with streaming token-by-token events.

5. **`internal/tools/`** — tool registry: read, write, edit, bash, grep, glob, ls, find, search, fuzzy file finder (fff), web search/fetch, memory read/write/delete/update, skill tool, and background process management.

6. **`internal/safety/`** — git-based checkpoints (hidden `refs/checkpoints/*`), dangerous-command classifier, and permission policy with four modes (plan, ask, auto, headless).

7. **`internal/session/`** — persists conversations as append-only JSONL transcripts with automatic compaction when the context window is nearly full.

8. **`internal/server/`** — runs the loop as a Unix-socket daemon, allowing remote TUI clients to attach.

9. **`internal/engine/`** — the `Engine` struct that wraps `loop.Loop`, `Provider`, `Policy`, `MCPManager`, and `ClientManager` to expose the `client.Engine` interface. The central coordinator for both local TUI and daemon sessions.

10. **`internal/client/`** — client-facing `Engine` interface and `RemoteEngine` (socket-backed) implementation, plus file resolution helpers (`ResolveInput`, `ListFiles`).

11. **`internal/config/`** — file-based configuration (CORE.md, config.toml, auth.json, SYSTEM_PROMPT.md). no environment variables.

12. **`internal/agents/`** — loads markdown-defined subagent definitions (scout, researcher, worker) and provides a builder to spawn them as child loops.

13. **`internal/mcp/`** — Model Context Protocol support: manager, server connection, stdio/SSE transports, and tool bridging.

14. **`internal/protocol/`** — value-typed seam types: every component boundary speaks owned, serializable messages (Event, Message, Request).

15. **`internal/transport/`** — client/server wire message types for daemon mode.

16. **`internal/skills/`** — SKILL.md loading, listing, and description extraction for user-defined skills.

17. **`internal/memory/`** — persistent project memory (append-only JSONL, compaction, scoring, contradiction detection).

## packages

| package | purpose |
|---|---|
| `internal/protocol` | value-typed seam types (Event, Message, Request) |
| `internal/provider` | Provider interface + OpenAI + Anthropic + routing |
| `internal/loop` | turn loop, prompt builder, stream assembler |
| `internal/tools` | tool registry: read, write, edit, bash, grep, glob, ls, find, search, fff, web, memory, skill, background, mcp bridge |
| `internal/safety` | checkpointer, dangerous-command classifier, policy |
| `internal/session` | append-only JSONL transcript, compaction, session paths |
| `internal/executor` | tool execution + permission gating |
| `internal/tui` | Bubble Tea TUI (model, panes, dialogs, styles, markdown) |
| `internal/engine` | Engine struct — wraps loop, provider, policy, MCP; satisfies client.Engine |
| `internal/client` | client.Engine interface + RemoteEngine (socket-backed), file resolution |
| `internal/agents` | subagent definitions, builder, spawn (scout, researcher, worker) |
| `internal/server` | unix-socket daemon, session manager, WebSocket |
| `internal/mcp` | MCP protocol types, stdio/SSE transports, manager, tool bridge |
| `internal/transport` | client/server wire message types |
| `internal/config` | file-based config (CORE.md, config.toml, auth.json, SYSTEM_PROMPT.md) |
| `internal/gitutil` | git operations: worktree, merge, status, diff |
| `internal/fff` | fuzzy file finder index (frecent path ranking) |
| `internal/skills` | SKILL.md loading, listing, description extraction |
| `internal/memory` | persistent project memory (append/load/compact/search) |
| `internal/models` | model listing and filtering |
| `internal/pricing` | token pricing table with cost calculation |
| `internal/version` | version string + compatibility check |
| `internal/notify` | desktop notification wrappers |
| `internal/jsonutil` | deterministic JSON marshalling helpers |
