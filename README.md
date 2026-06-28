# talos

a terminal-native coding-agent harness built in go.

inspired by `claude code`, `codex`, and [Mario Zechner's](https://x.com/badlogicgames) [pi](pi.dev).

## setup

prerequisites: go 1.24+, an api key for your llm provider.

```bash
git clone https://github.com/mintoleda/talos
cd talos
go build ./cmd/talos
```

install globally (optional):
```bash
ln -sf "$PWD/talos" ~/.local/bin/talos
```

## configure

add an api key via the tui:
```bash
./talos
# then type /login
```

or edit `~/.talos/auth.json` directly:
```json
{
  "anthropic": {"type": "api_key", "key": "sk-ant-..."},
  "deepseek":  {"type": "api_key", "key": "sk-..."},
  "openai":    {"type": "api_key", "key": "sk-..."}
}
```

you can create `~/.talos/config.toml` to set defaults:
```toml
# anthropic
provider = "anthropic"
model    = "claude-sonnet-4-6"

# openai
provider = "openai"
model    = "gpt-5.5"

permission_mode = "auto"
```

see [docs/configuration.md](docs/configuration.md) for the full config reference.

## run

```bash
./talos                          # full-screen tui (default)
./talos -p "explain this repo"   # one-shot
./talos -c                       # continue latest session
./talos -r                       # pick a session to resume
./talos server                   # run as a daemon server (background)
./talos attach                   # attach to a running server (auto-discovers)
./talos attach <session-id>      # attach to a specific server
./talos server list              # list running servers
./talos server kill <session-id> # kill a specific server
./talos server kill-all          # kill all running servers
./talos server help              # show server management commands
```

## server / attach workflow

Run a long-lived daemon, then detach/reattach from any terminal:

```bash
# Terminal 1: start the server (keeps your session + cache warm)
./talos server &
disown

# Terminal 2, 3, ...: attach with the full TUI
./talos attach

# List or kill servers from anywhere
./talos server list
./talos server kill-all
```

The server auto-shuts down after 30 minutes of inactivity with no clients attached.
Multi-client is fully supported — all attached clients see the same conversation
and receive each others' inputs.

### slash commands in attach mode

Slash commands like `/model`, `/thinking`, and `/model <provider/model>`
are forwarded to the server and executed there. All attached clients see
the status bar update automatically.

```
/model                           # list available models
/model deepseek/deepseek-v4-flash # switch directly
/thinking                        # cycle thinking level
```

## slash commands

| command | description |
|---|---|
| `/new` | start a fresh session |
| `/compact [focus]` | compact conversation history |
| `/model [query]` | switch provider/model |
| `/thinking` | cycle thinking level (off → minimal → low → medium → high → xhigh) |
| `/login` | add an api key |
| `/stats` | token usage and cache hit rate |
| `/resume [id]` | resume a session |
| `/push` | commit and push changes |
| `/exit` | quit |

## further reading

- [docs/architecture.md](docs/architecture.md) — package map and component seams
- [docs/configuration.md](docs/configuration.md) — full config reference
- `AGENTS.md` — per-project system prompt
- `~/.talos/SYSTEM_PROMPT.md` — global system prompt overrides
- `~/.talos/subagents/` — define custom subagents as markdown files

## notes

**startup time to first interactive prompt** (10-run average, measured via tmux):

| tool   |  avg   |  min   |   max  | spread |
|--------|--------|--------|--------|--------|
| talos  | 418ms  | 411ms  | 474ms  | 63ms   |
| codex  | 852ms  | 364ms  | 1788ms | 1424ms |
| claude | 1299ms | 1131ms | 1616ms | 485ms  |
| pi     | 1510ms | 1456ms | 1585ms | 129ms  |

i used pi a lot and configured it quite heavily, so the time is largely misleading.

- working on vim binds because i like neovim
- still need /config and probably other / commands
- memory system would be cool
- tabs/session windows
- mcp support
- multi-modal
  - [x] images
  - videos
- themes
