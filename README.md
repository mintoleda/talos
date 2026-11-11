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

New sessions reuse the last provider/model used in that working directory.
`--model` and `--provider` override this; directories without model history use
the configured defaults. Model selections do not rewrite the global config.

see [docs/configuration.md](docs/configuration.md) for the full config reference.

## run

```bash
./talos                          # full-screen tui (default)
./talos -p "explain this repo"   # one-shot
./talos -c                       # continue latest session
./talos -r                       # pick a session to resume
./talos serve                    # multi-session daemon (foreground)
./talos serve -d                 # multi-session daemon (background)
./talos server list              # list running daemon
./talos server kill              # kill the daemon
./talos server help              # show server management commands
```

### native desktop (Rust, GPUI)

`desktop/` is a Rust workspace containing the native `talos-gpui` app, a local
daemon/CLI, and storage. It drives Talos headlessly through `talos acp` (Agent
Client Protocol over stdio), with Claude Code as the only secondary provider.
It is the desktop UI; the legacy Electron app has been removed. Node.js and npm
are not required for development.

```bash
make desktop          # build ./talos, then run the desktop UI
make desktop-test     # cargo test --workspace
make desktop-accept   # daemon/CLI acceptance against a fake ACP agent
```

See [desktop/README.md](desktop/README.md) and
[docs/paseo-integration.md](docs/paseo-integration.md) for the Go/Rust boundary.

### headless ACP

```bash
./talos acp           # ACP v1 on stdio, for the desktop or any ACP client
```

## sessions and daemon

The TUI runs its engine in-process. Use `./talos -c` to continue the latest saved
session or `./talos -r` to select one; attaching a TUI to a running daemon is no
longer supported.

`talos serve` retains the Go daemon's Unix-socket RPC API for programmatic
clients. It has no WebSocket listener or web UI. The native desktop uses its own
Rust daemon (`talosd`) and `talos acp`, not `talos serve`.

## slash commands

| command | description |
|---|---|
| `/new` | start a fresh session |
| `/compact [focus]` | compact conversation history |
| `/model [query]` | switch provider/model |
| command | description |
|---|---|---|
| `/help` | show this help |
| `/new` | start a fresh session |
| `/compact [focus]` | compact conversation history |
| `/model [query]` | switch provider/model |
| `/thinking` | cycle thinking level (off → minimal → low → medium → high → xhigh) |
| `/login` | add an api key |
| `/stats` | token usage and cache hit rate |
| `/resume [id]` | resume a session |
| `/subagents` | toggle subagent delegation on/off |
| `/mcp` | list connected MCP servers and their tools |
| `/exit` | quit |

## skills

User-defined skills live in `~/.talos/skills/<name>/SKILL.md` files. They are
scanned at startup, injected into the system prompt, and appear in slash
autocompletion. Type `/<name>` in the prompt box to tab-complete — selecting a
skill populates the input with `skill:<name>`, which the model interprets as a
skill invocation (the model reads the SKILL.md via the `skill` tool).

The built-in `push` skill is now defined this way:

```bash
mkdir -p ~/.talos/skills/push
# write SKILL.md describing commit-and-push steps
```

## further reading

- [docs/architecture.md](docs/architecture.md) — package map and component seams
- [docs/paseo-integration.md](docs/paseo-integration.md) — `talos acp` and the Rust desktop boundary
- [desktop/README.md](desktop/README.md) — native Rust desktop workspace
- [docs/configuration.md](docs/configuration.md) — full config reference
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
- ~~memory system would be cool~~
- ~~tabs/session windows~~
- ~~mcp support~~
- multi-modal
  - [x] images
  - videos
- themes
