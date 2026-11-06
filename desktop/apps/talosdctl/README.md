# talosdctl

`talosdctl` is an authenticated client for the Rust-owned `talosd` Unix socket. It never reads or modifies legacy `~/.paseo` state. Start `talosd` separately.

```sh
~/.cargo/bin/cargo run -p talosdctl -- health
~/.cargo/bin/cargo run -p talosdctl -- status --json
~/.cargo/bin/cargo run -p talosdctl -- provider list
~/.cargo/bin/cargo run -p talosdctl -- provider diagnostic talos
~/.cargo/bin/cargo run -p talosdctl -- workspace list
~/.cargo/bin/cargo run -p talosdctl -- workspace add my-project /absolute/existing/directory
~/.cargo/bin/cargo run -p talosdctl -- workspace show WORKSPACE_ID
~/.cargo/bin/cargo run -p talosdctl -- workspace set-project WORKSPACE_ID PROJECT_ID
~/.cargo/bin/cargo run -p talosdctl -- workspace set-project WORKSPACE_ID none

~/.cargo/bin/cargo run -p talosdctl -- project create my-project /absolute/repository/root
~/.cargo/bin/cargo run -p talosdctl -- project list
~/.cargo/bin/cargo run -p talosdctl -- project show PROJECT_ID
~/.cargo/bin/cargo run -p talosdctl -- project rename PROJECT_ID 'New name'
~/.cargo/bin/cargo run -p talosdctl -- project delete PROJECT_ID
~/.cargo/bin/cargo run -p talosdctl -- worktree create PROJECT_ID 'Feature work' feature/my-change HEAD
~/.cargo/bin/cargo run -p talosdctl -- worktree delete WORKSPACE_ID

~/.cargo/bin/cargo run -p talosdctl -- terminal create WORKSPACE_ID --program sh --arg=-i
~/.cargo/bin/cargo run -p talosdctl -- terminal list WORKSPACE_ID
~/.cargo/bin/cargo run -p talosdctl -- terminal input WORKSPACE_ID TERMINAL_ID $'echo hello\\n'
~/.cargo/bin/cargo run -p talosdctl -- terminal resize WORKSPACE_ID TERMINAL_ID 40 120
~/.cargo/bin/cargo run -p talosdctl -- terminal capture WORKSPACE_ID TERMINAL_ID --limit 65536
~/.cargo/bin/cargo run -p talosdctl -- terminal kill WORKSPACE_ID TERMINAL_ID

~/.cargo/bin/cargo run -p talosdctl -- session new WORKSPACE_ID --provider talos --title 'Refactor auth'
~/.cargo/bin/cargo run -p talosdctl -- session list --workspace WORKSPACE_ID
~/.cargo/bin/cargo run -p talosdctl -- session show SESSION_ID
~/.cargo/bin/cargo run -p talosdctl -- session load SESSION_ID
~/.cargo/bin/cargo run -p talosdctl -- session rename SESSION_ID 'New title'
~/.cargo/bin/cargo run -p talosdctl -- session archive SESSION_ID
~/.cargo/bin/cargo run -p talosdctl -- session prompt SESSION_ID 'Summarize this project'
~/.cargo/bin/cargo run -p talosdctl -- session cancel SESSION_ID
~/.cargo/bin/cargo run -p talosdctl -- session set-model SESSION_ID MODEL_ID
~/.cargo/bin/cargo run -p talosdctl -- session set-mode SESSION_ID MODE_ID
~/.cargo/bin/cargo run -p talosdctl -- session set-thinking SESSION_ID high
~/.cargo/bin/cargo run -p talosdctl -- session replay --session SESSION_ID --after 0 --limit 100
~/.cargo/bin/cargo run -p talosdctl -- history search 'connection timeout' --limit 50
~/.cargo/bin/cargo run -p talosdctl -- history search 'connection timeout' --session SESSION_ID
~/.cargo/bin/cargo run -p talosdctl -- usage
~/.cargo/bin/cargo run -p talosdctl -- usage --session SESSION_ID

~/.cargo/bin/cargo run -p talosdctl -- agent list
~/.cargo/bin/cargo run -p talosdctl -- agent list --workspace WORKSPACE_ID
~/.cargo/bin/cargo run -p talosdctl -- agent show AGENT_ID

~/.cargo/bin/cargo run -p talosdctl -- model list --provider talos

~/.cargo/bin/cargo run -p talosdctl -- permission list
~/.cargo/bin/cargo run -p talosdctl -- permission allow PERMISSION_ID
~/.cargo/bin/cargo run -p talosdctl -- permission deny PERMISSION_ID

~/.cargo/bin/cargo run -p talosdctl -- events replay --cursors '{}' --limit 100 --json
~/.cargo/bin/cargo run -p talosdctl -- events tail --follow --after 0
~/.cargo/bin/cargo run -p talosdctl -- shutdown
```

`workspace create` aliases `workspace add`. Projects persist a name and canonical root path; workspaces can be associated only when their canonical path is inside that root (or carry matching managed-worktree root metadata). A project cannot be deleted while workspaces reference it; clear those associations first. `worktree create` makes a Talos-owned branch checkout under the daemon home after validating the Git root, branch and commit ref. `worktree delete` verifies ownership and the Git worktree registration, refuses dirty or untracked content, archives the workspace only when no active run remains, and uses non-forced Git removal. Branches are retained. Do not manually move or alter managed worktree paths. Project/workspace association is stored in SQLite and survives restart. Use `session replay --session ID` for a single timeline or `events replay --cursors '{"SESSION_ID":7}'` for lossless multi-session pagination; pass each response's `session_cursors` into the next page while `has_more` is true. The legacy flat `events --after` and `session replay` without `--session` apply one scalar independently to every session; their maximum `next_cursor` is not a safe global watermark. `events tail --follow` drains all initial replay pages, deduplicates buffered live events by session sequence, and replays after lag/gaps. Transport disconnection ends the command; automatic reconnect is not yet implemented. See [the IPC replay contract](../../docs/architecture/ipc-replay.md). `session new` creates an agent and its first session through `CreateAgent`, so it requires a workspace id, not a workspace path (create the workspace first with `workspace add`).

`terminal create` starts an owned PTY in the selected workspace (default program `sh`); use repeatable `--arg` flags and optionally `--cwd` for a directory inside the workspace. Input, resize, capture, and kill require both the workspace and terminal IDs, preventing cross-workspace control. Captured output is a bounded, lossy UTF-8 view of recent PTY bytes and reports whether the requested tail was truncated. PTYs are ephemeral and are killed on daemon shutdown or workspace archive; the packaged systemd unit uses `KillMode=control-group` so descendants stay in the service cgroup and are cleaned up after daemon failure. The `terminal` smoke scenario checks descendant cleanup after graceful and hard daemon termination; compiled-artifact acceptance also checks restart through stale lock/socket markers. PTYs are not a filesystem sandbox. Actual cgroup behavior under a live systemd-user manager remains untested because the acceptance environment has no `systemctl` executable.

`usage` aggregates provider-reported metrics from the durable timeline; `--session ID` narrows it to one session. Missing token counts or cost are `null` (unknown), while a reported zero stays `0`. If any usage-bearing run omits a metric, that session/global metric stays `null` rather than reporting a partial subtotal. Same-run usage snapshots are treated as updates, not separately billed events. No provider price is inferred when the provider supplies no cost.

`history search QUERY` finds durable user and assistant text events, newest first. Use `--session SESSION_ID` to scope results and `--limit N` to raise the default 50 results (maximum 1000); `has_more` indicates additional matches. Search excludes tool payloads, thoughts, permission details, and diagnostics. Queries must be 1–512 UTF-8 bytes after trimming.

`--request-key` provides durable, session-scoped acceptance deduplication: retrying the same key and prompt returns the original `run_id`, while reusing a key with different prompt text fails. This does not guarantee exactly-once model/tool execution. Do not automatically retry an unkeyed prompt after a lost response; see the [durable prompt acceptance contract](../../docs/architecture/prompt-acceptance.md).

`--json` emits the complete daemon result. Human output summarizes flat records but preserves nested objects and event payloads. `--home` / `TALOSD_HOME` defaults to `~/.talosd`; `--socket` / `TALOSD_SOCKET` overrides `<home>/talosd.sock`. The CLI reads `<home>/auth.token` when present, or accepts `TALOSD_AUTH_TOKEN` / `--auth-token`; prefer the environment variable to avoid exposing secrets in shell history or process arguments. No files are created by the CLI.

## Configuring Talos and Claude Code via `config.toml`

Paseo keeps its configuration in `<TALOSD_HOME>/config.toml` (default `~/.talosd/config.toml`). Provider environment is injected directly into the spawned `talos acp` process and is redacted from daemon diagnostics, so you can supply model credentials without editing `~/.talos` or `~/.paseo`.

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
TALOS_HOME = "/tmp/talosd-talos-home"
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

`env` values are inherited/overridden by the ACP child process, not by the daemon. `TALOS_HOME` keeps Talos' transcript/session state out of `~/.talos`; `OPENAI_API_KEY` (or the key for the provider your Talos build is configured to use) supplies model auth. `TALOSD_HOME` only relocates talosd's own daemon/database/config state and never points at Talos' home.

The default config contains only `talos` (`command = "talos"`, `args = ["acp"]`) and `claude` (`command = "npx"`, using the pinned official Claude Agent ACP package). Replace commands with absolute paths when the daemon's `PATH` cannot resolve them. `npx` requires Node.js 22 or newer for the adapter. A provider with `enabled = false` is skipped at startup. Authentication failures surface in the timeline; talosd never switches an existing session to the other provider.
