# architecture

```
┌──────────────────────────────────────────────────────────────┐
│                     cmd/talos                                  │
│  main.go ── CLI flags, provider wiring, session lifecycle     │
│  app.go  ── session mgmt, provider/model switching, model     │
│             fetching, login                                    │
└──────────┬───────────────────────────────────────────────────┘
           │
           │  UserInput / ApprovePermission / Interrupt
           │  (via channels to Bubble Tea)
           │
┌──────────▼───────────────────────────────────────────────────┐
│                      internal/tui                              │
│  model.go ── Bubble Tea model (vim-mode, slash commands,      │
│              panes: chat, tools, subagents)                    │
│  dialogs/ ── login, confirm, model-picker, session-picker     │
│              dialogs                                           │
│  panes/   ── chat (markdown renderer), tools, subagents        │
└──┬──────────────┬──────────────┬──────────────────────────────┘
   │              │              │
   │    Provider  │   Executor   │  Transcript / Compactor
   ▼              ▼              ▼
┌──────────┐ ┌──────────┐ ┌──────────────┐
│ provider │ │ executor │ │   session    │
│ openai/  │ │ tool     │ │ transcript   │
│ anthropic│ │ registry │ │ compactor    │
└──────────┘ └──────────┘ └──────────────┘
   │              │              │
   ▼              ▼              ▼
┌──────────┐ ┌──────────┐ ┌──────────────┐
│ protocol │ │  tools   │ │    safety    │
│ value-   │ │ read,    │ │ checkpointer │
│ typed    │ │ write,   │ │ policy,      │
│ events   │ │ edit,    │ │ classifier   │
│          │ │ bash,    │ │              │
│          │ │ grep,    │ │              │
│          │ │ glob,    │ │              │
│          │ │ ls, fff, │ │              │
│          │ │ search,  │ │              │
│          │ │ web,     │ │              │
│          │ │ memory,  │ │              │
│          │ │ skill,   │ │              │
│          │ │ bg       │ │              │
└──────────┘ └──────────┘ └──────────────┘

                    ┌──────────────┐
                    │    server    │
                    │ unix-socket  │
                    │ daemon       │
                    │ attach client│
                    └──────────────┘
```

## seams

1. **`cmd/talos/`** wires everything together: parses CLI flags, loads config and auth, creates the provider, executor, loop, and session, then hands control to the TUI, one-shot renderer, or server.

2. **`internal/tui/`** — Bubble Tea TUI with a chat pane (markdown rendering), tools pane, subagents pane, and dialogs for login, confirm, plan review, merge review, model picking, and session picking.

3. **`internal/loop/`** — drives the LLM conversation: builds requests from the system prompt + tool schemas + transcript, streams responses from the provider, assembles tool results back into messages, and compacts when context is near capacity.

4. **`internal/provider/`** — abstracts the LLM API (OpenAI-compatible and Anthropic) with streaming token-by-token events.

5. **`internal/tools/`** — tool registry: read, write, edit, bash, grep, glob, ls, find, search, fuzzy file finder (fff), web search/fetch, memory write, skill tool, and background processes.

6. **`internal/safety/`** — git-based checkpoints (hidden `refs/checkpoints/*`), dangerous-command classifier, and permission policy with four modes (plan, ask, auto, headless).

7. **`internal/session/`** — persists conversations as append-only JSONL transcripts with automatic compaction when the context window is nearly full.

8. **`internal/server/`** — runs the loop as a Unix-socket daemon, allowing remote TUI clients to attach.

10. **`internal/config/`** — file-based configuration (CORE.md, config.toml, auth.json, SYSTEM_PROMPT.md). no environment variables.

11. **`internal/agents/`** — loads markdown-defined subagent definitions (scout, researcher, worker) and provides a builder to spawn them as child loops.

12. **`internal/protocol/`** — value-typed seam types: every component boundary speaks owned, serializable messages (Event, Message, Request).

13. **`internal/transport/`** — client/server wire message types for daemon mode.

## packages

| package | purpose |
|---|---|
| `internal/protocol` | value-typed seam types (Event, Message, Request) |
| `internal/provider` | Provider interface + OpenAI + Anthropic + routing |
| `internal/loop` | turn loop, prompt builder, stream assembler |
| `internal/tools` | tool registry: read, write, edit, bash, grep, glob, ls, find, search, fff, web, memory, skill, background |
| `internal/safety` | checkpointer, dangerous-command classifier, policy |
| `internal/session` | append-only JSONL transcript, compaction, session paths |
| `internal/executor` | tool execution + permission gating |
| `internal/tui` | Bubble Tea TUI (model, panes, dialogs, styles, markdown) |
| `internal/agents` | subagent definitions, builder, spawn (scout, researcher, worker) |
| `internal/server` | unix-socket daemon, client lifecycle |
| `internal/transport` | client/server wire message types |
| `internal/config` | file-based config (CORE.md, config.toml, auth.json, SYSTEM_PROMPT.md) |
| `internal/fff` | fuzzy file finder index (frecent path ranking) |
| `internal/skills` | SKILL.md loading and listing |
| `internal/memory` | persistent memory append/load |
| `internal/models` | model listing and filtering |
| `internal/pricing` | token pricing table with cost calculation |
| `internal/version` | version string + compatibility check |
