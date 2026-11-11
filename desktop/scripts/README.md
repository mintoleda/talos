# Process-level smoke harness

## Linux packaging acceptance

Run the isolated fresh-home packaging check with:

```sh
python3 scripts/packaging-acceptance.py --verbose
```

It creates two temporary fixture release archives, verifies each with
`sha256sum -c`, confirms a modified archive fails checksum verification, then
installs version 1, upgrades to version 2, and uninstalls in a temporary
`HOME`. It checks the installed files and modes, preservation of `.talosd`,
`.paseo`, and unrelated user files, plus installer/uninstaller refusal of
symlink destinations. The fixture binaries are stubs: this verifies the
filesystem and checksum contract, not a compiled release artifact or CLI/UI
workflow. The test refuses to run as root, matching the user installer.

CI runs this separately as the **Desktop Linux packaging acceptance** job. The
current workflow does not assemble or upload a release archive. For the additional
local compiled-artifact acceptance, build a Linux release bundle then run:

```sh
RUST_FONTCONFIG_DLOPEN=1 cargo build --release --locked -p talosd -p talosdctl -p talos-gpui --bins
python3 scripts/packaging-acceptance.py --compiled-artifact --verbose
```

This creates a temporary checksum-verified archive from the compiled release
binaries, installs it into an isolated home, runs the real daemon/CLI project,
worktree, and PTY hard-crash workflows, restarts through the stale lock/socket,
then replaces prior binaries with the compiled bundle and uninstalls while
checking retained daemon data. It verifies the packaged `KillSignal`,
`KillMode=control-group`, and restart directives. It does not launch the desktop
UI or exercise a live systemd-user manager.

The current environment has no `systemctl` executable: `systemctl --user
is-system-running` returns `/bin/bash: systemctl: command not found` (exit 127).
No user service is started or modified during this acceptance.

`talosd-smoke.py` launches the real `talosd`, `talosdctl` and `acp-fixture`
binaries against temporary Rust-owned homes. It is deterministic, needs no live
provider, and never reads or writes `~/.talosd`, `~/.paseo`, or a real Talos
provider home.

## Build and run

The script never invokes cargo. Build the binaries first:

```sh
cargo build -p talosd -p talosdctl -p talos-acp --bins
python3 scripts/talosd-smoke.py --verbose --timeout 300
```

Run the same harness through the ignored integration test:

```sh
cargo test -p talosdctl --test daemon_smoke -- --ignored --nocapture
```

Do not run `cargo build` while a smoke run is in progress: replacing the
binaries under `target/debug` invalidates running processes and their child
spawns. Build first, then run the harness to completion.

`--keep` preserves the temporary root for debugging, including daemon logs,
the fixture trace (`traces/*.jsonl`), and each home. `--timeout SECONDS` installs
a script-wide alarm that raises through the normal `finally` cleanup path; use
`--timeout 300` in CI or any unattended run. `--scenario NAME` runs one scenario
(repeatable: `lifecycle`, `keyed`, `isolation`, `redaction`, `interrupt`,
`cancel-permission`, `terminal`, `projects`). A forced-kill debug root (`--keep`)
can retain stale markers until the next daemon start; the daemon then removes a
confirmed-dead PID marker and refused Unix socket automatically. Active or
unverifiable paths remain fail-closed.

## Failure diagnostics

On assertion failure the harness prints the last 80 lines of each live daemon
log. Run-terminal timeouts and tool/assistant attribution failures additionally
include the offending event sequence and `run_id` pairs plus recent daemon
`WARN`/`ERROR` lines. The default child `RUST_LOG` is
`talosd=debug,talos_core=debug,talos_acp=debug` so core lifecycle warnings are
not hidden behind the daemon target; override `RUST_LOG` in the environment to
change verbosity.

A single invocation is not evidence of determinism. Repeat the affected
scenario when investigating a race, for example:

```sh
for run in $(seq 1 10); do
  python3 scripts/talosd-smoke.py --scenario lifecycle --timeout 90 || break
done
python3 scripts/talosd-smoke.py --scenario cancel-permission --timeout 90
```

## Binary overrides

| Variable | Default |
|---|---|
| `TALOSD_SMOKE_DAEMON_BIN` | `target/debug/talosd` |
| `TALOSD_SMOKE_CTL_BIN` | `target/debug/talosdctl` |
| `TALOSD_ACP_FIXTURE_BIN` | `target/debug/acp-fixture` |
| `TALOSD_SMOKE_PYTHON` | `python3` (test wrapper only) |
| `TALOSD_SMOKE_TIMEOUT_SECS` | `300` (test wrapper only; the script alarm is 10s earlier) |

Paths are resolved from the repository root (the script's parent directory).

## Covered scenarios

- workspace add/list/show and session create with the fixture ACP provider;
- prompt, streamed assistant text, durable replay, and session/global usage CLI
  aggregation with unknown cost preserved as `null`;
- deterministic tool lifecycle (`tool_call` running -> `tool_call_update`
  completed), durable replay, run-id attribution for every tool/assistant/usage
  update, a rapid follow-up that must not inherit late tool updates, and no
  duplicated tool events after session load, restart, or follow-up (including
  provider-replayed tool updates);
- durable keyed prompt identity: identical retry after completion and while a
  run is active, same-key/different-text conflict, cross-session key
  independence, restart lookup before `session load`, and no re-executed
  provider prompt or duplicated UserText/Tool events;
- permission allow and deny, including durable request options and resolution;
- two agents with independent pending permissions, duplicate/stale decisions,
  and post-cancel decision rejection;
- cancellation, rename and archive, plus rejection of a prompt after archive;
- lossless multi-session replay through `events replay --cursors`, per-session
  replay continuity, a disconnected live-tail client, and overlapping
  session-local sequence numbers;
- graceful shutdown, socket/lock removal, child cleanup, restart with explicit
  `session load`, native fixture resume without a duplicate `session/new`, and
  no duplicated replay output;
- shutdown underneath a pending permission with restart recovery;
- cancellation during a pending permission;
- wrong and missing bearer tokens, incompatible protocol negotiation, a missing
  provider executable, and two isolated homes;
- provider stderr/config secret redaction in the durable timeline, diagnostics
  and daemon logs;
- terminal creation/list/input/resize/capture/kill through the compiled CLI,
  workspace access isolation, and PTY descendant cleanup after both graceful and
  hard daemon termination (also repeated with the compiled release artifact);
- project create/list/rename/delete, workspace association path checks, and refusal
  to delete a project while workspaces remain associated;
- managed Git worktree creation from a temporary local repository, dirty/untracked
  checkout refusal, successful non-forced removal, and workspace ownership checks.

The `keyed` scenario treats `request_key` as the durable daemon contract; the
unkeyed prompt path is exercised separately and does not claim idempotency.

## Safety and cleanup

Every daemon and provider child runs with `HOME` and `TALOSD_HOME` pointing
inside a `tempfile.mkdtemp` root. Daemons are started in their own process group.
On assertion failure, `finally` stops every daemon with a bounded wait, kills
leftover fixture PIDs recorded by `TALOSD_ACP_FIXTURE_TRACE`, and removes the
temporary root unless `--keep` was supplied.

Both timeout paths unwind instead of orphaning children:

- the ignored Rust wrapper passes `--timeout` to the script, so its alarm raises
  `SmokeFailure` and runs `finally` before the wrapper's own deadline;
- if the wrapper still reaches its deadline, it sends `SIGINT` to the Python
  process and waits up to 20 seconds for that cleanup before a hard `SIGKILL`.
- daemons are started with `start_new_session=True`; cleanup kills their process
  group only after a graceful stop attempt.

## Fixture modes

`acp-fixture` keeps its existing `stream`, `permission`, `cancel`, `eof`,
`malformed`, `oversized`, `stderr` and `minimal` modes. The smoke harness uses
`TALOSD_ACP_FIXTURE_MODE=scripted`, which selects a deterministic scenario from
the prompt text:

| Prompt contains | Behaviour |
|---|---|
| `permission` | request `allow_once`/`reject_once`, then report the selection |
| `tool` | emit `tool_call` running then `tool_call_update` completed, with durable input/output |
| `resume-probe` | require a prior `session/load` in this fixture process |
| `cancel` | stream `partial`, wait for `session/cancel`, return cancelled |
| `secret-exit` | write the configured secret to stderr, then exit |
| anything else | stream `hello ` + `world` and usage |

`TALOSD_ACP_FIXTURE_TRACE=<path>` appends JSON-lines process events
(`start`, `session/new`, `session/load`, permission decisions, cancellation)
for cleanup and restart assertions. `TALOSD_ACP_FIXTURE_ECHO_SECRET=1` writes
`FIXTURE_SECRET` to stderr so the redaction scenario can verify it never reaches
durable output.
