# paseoctl

`paseoctl` is an authenticated client for the Rust-owned `paseod` Unix socket. It never reads or modifies legacy `~/.paseo` state. Start `paseod` separately.

```sh
~/.cargo/bin/cargo run -p paseo-cli -- health
~/.cargo/bin/cargo run -p paseo-cli -- status --json
~/.cargo/bin/cargo run -p paseo-cli -- provider list
~/.cargo/bin/cargo run -p paseo-cli -- provider diagnostic talos
~/.cargo/bin/cargo run -p paseo-cli -- workspace list
~/.cargo/bin/cargo run -p paseo-cli -- workspace add my-project /absolute/existing/directory
~/.cargo/bin/cargo run -p paseo-cli -- workspace show WORKSPACE_ID

~/.cargo/bin/cargo run -p paseo-cli -- session new WORKSPACE_ID --provider talos --title 'Refactor auth'
~/.cargo/bin/cargo run -p paseo-cli -- session list --workspace WORKSPACE_ID
~/.cargo/bin/cargo run -p paseo-cli -- session show SESSION_ID
~/.cargo/bin/cargo run -p paseo-cli -- session load SESSION_ID
~/.cargo/bin/cargo run -p paseo-cli -- session rename SESSION_ID 'New title'
~/.cargo/bin/cargo run -p paseo-cli -- session archive SESSION_ID
~/.cargo/bin/cargo run -p paseo-cli -- session prompt SESSION_ID 'Summarize this project'
~/.cargo/bin/cargo run -p paseo-cli -- session cancel SESSION_ID
~/.cargo/bin/cargo run -p paseo-cli -- session set-model SESSION_ID MODEL_ID
~/.cargo/bin/cargo run -p paseo-cli -- session set-mode SESSION_ID MODE_ID
~/.cargo/bin/cargo run -p paseo-cli -- session set-thinking SESSION_ID high
~/.cargo/bin/cargo run -p paseo-cli -- session replay --session SESSION_ID --after 0 --limit 100

~/.cargo/bin/cargo run -p paseo-cli -- agent list
~/.cargo/bin/cargo run -p paseo-cli -- agent list --workspace WORKSPACE_ID
~/.cargo/bin/cargo run -p paseo-cli -- agent show AGENT_ID

~/.cargo/bin/cargo run -p paseo-cli -- model list --provider talos

~/.cargo/bin/cargo run -p paseo-cli -- permission list
~/.cargo/bin/cargo run -p paseo-cli -- permission allow PERMISSION_ID
~/.cargo/bin/cargo run -p paseo-cli -- permission deny PERMISSION_ID

~/.cargo/bin/cargo run -p paseo-cli -- events replay --cursors '{}' --limit 100 --json
~/.cargo/bin/cargo run -p paseo-cli -- events tail --follow --after 0
~/.cargo/bin/cargo run -p paseo-cli -- shutdown
```

`workspace create` aliases `workspace add`. Use `session replay --session ID` for a single timeline or `events replay --cursors '{"SESSION_ID":7}'` for lossless multi-session pagination; pass each response's `session_cursors` into the next page while `has_more` is true. The legacy flat `events --after` and `session replay` without `--session` apply one scalar independently to every session; their maximum `next_cursor` is not a safe global watermark. `events tail --follow` drains all initial replay pages, deduplicates buffered live events by session sequence, and replays after lag/gaps. Transport disconnection ends the command; automatic reconnect is not yet implemented. See [the IPC replay contract](../../docs/architecture/ipc-replay.md). `session new` creates an agent and its first session through `CreateAgent`, so it requires a workspace id, not a workspace path (create the workspace first with `workspace add`).

`--request-key` is currently correlation metadata only: it is echoed, not durable idempotency. Do not automatically retry prompts after a lost response. Durable request deduplication remains an open backend task.

`--json` emits the complete daemon result. Human output summarizes flat records but preserves nested objects and event payloads. `--home` / `PASEO_RS_HOME` defaults to `~/.paseo-rs`; `--socket` / `PASEO_RS_SOCKET` overrides `<home>/paseod.sock`. The CLI reads `<home>/auth.token` when present, or accepts `PASEO_RS_AUTH_TOKEN` / `--auth-token`; prefer the environment variable to avoid exposing secrets in shell history or process arguments. No files are created by the CLI.

## Configuring Talos and Claude Code via `config.toml`

Paseo keeps its configuration in `<PASEO_RS_HOME>/config.toml` (default `~/.paseo-rs/config.toml`). Provider environment is injected directly into the spawned `talos acp` process and is redacted from daemon diagnostics, so you can supply model credentials without editing `~/.talos` or `~/.paseo`.

```toml
version = 1
default_provider = "talos"
event_capacity = 1024
shutdown_grace_ms = 1500

[[providers]]
id = "talos"
display_name = "Talos"
kind = "acp"
enabled = true
command = "talos"
args = ["acp"]

[providers.env]
TALOS_HOME = "/tmp/paseo-talos-home"
OPENAI_API_KEY = "sk-..."
# Optional: point Talos at a compatible endpoint or model provider.
# OPENAI_BASE_URL = "https://api.openai.com/v1"

[[providers]]
id = "claude"
display_name = "Claude Code"
kind = "acp"
enabled = true
command = "npx"
args = ["--yes", "@agentclientprotocol/claude-agent-acp@0.100.0"]

[providers.env]
# Use an Anthropic API key when the adapter cannot reuse an existing Claude login.
ANTHROPIC_API_KEY = "sk-ant-..."
```

`env` values are inherited/overridden by the ACP child process, not by the daemon. `TALOS_HOME` keeps Talos' transcript/session state out of `~/.talos`; `OPENAI_API_KEY` (or the key for the provider your Talos build is configured to use) supplies model auth. `PASEO_RS_HOME` only relocates Paseo's own daemon/database/config state and never points at Talos' home.

The default config contains only `talos` (`command = "talos"`, `args = ["acp"]`) and `claude` (`command = "npx"`, using the pinned official Claude Agent ACP package). Replace commands with absolute paths when the daemon's `PATH` cannot resolve them. `npx` requires Node.js 22 or newer for the adapter. A provider with `enabled = false` is skipped at startup. Authentication failures surface in the timeline; Paseo never switches an existing session to the other provider.
