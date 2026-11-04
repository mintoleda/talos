# Process-level smoke harness

`paseo-smoke.py` launches the real `paseod`, `paseoctl` and `acp-fixture`
binaries against temporary Rust-owned homes. It is deterministic, needs no live
provider, and never reads or writes `~/.paseo-rs`, `~/.paseo`, or a real Talos
provider home.

## Build and run

The script never invokes cargo. Build the binaries first:

```sh
cargo build -p paseod -p paseo-cli -p paseo-acp --bins
python3 scripts/paseo-smoke.py --verbose --timeout 300
```

Run the same harness through the ignored integration test:

```sh
cargo test -p paseo-cli --test daemon_smoke -- --ignored --nocapture
```

Do not run `cargo build` while a smoke run is in progress: replacing the
binaries under `target/debug` invalidates running processes and their child
spawns. Build first, then run the harness to completion.

`--keep` preserves the temporary root for debugging, including daemon logs,
the fixture trace (`traces/*.jsonl`), and each home. `--timeout SECONDS` installs
a script-wide alarm that raises through the normal `finally` cleanup path; use
`--timeout 300` in CI or any unattended run. `--scenario NAME` runs one scenario
(repeatable: `lifecycle`, `keyed`, `isolation`, `redaction`, `interrupt`,
`cancel-permission`). A forced-kill debug root (`--keep`) can retain a stale
socket, which is intentional: the harness makes no automatic stale-socket
removal claim.

## Binary overrides

| Variable | Default |
|---|---|
| `PASEO_PASEOD_BIN` | `target/debug/paseod` |
| `PASEO_PASEOCTL_BIN` | `target/debug/paseoctl` |
| `PASEO_ACP_FIXTURE_BIN` | `target/debug/acp-fixture` |
| `PASEO_SMOKE_PYTHON` | `python3` (test wrapper only) |
| `PASEO_SMOKE_TIMEOUT_SECS` | `300` (test wrapper only; the script alarm is 10s earlier) |

Paths are resolved from the repository root (the script's parent directory).

## Covered scenarios

- workspace add/list/show and session create with the fixture ACP provider;
- prompt, streamed assistant text, usage, follow-up and durable replay;
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
  and daemon logs.

The `keyed` scenario treats `request_key` as the durable daemon contract; the
unkeyed prompt path is exercised separately and does not claim idempotency.

## Safety and cleanup

Every daemon and provider child runs with `HOME` and `PASEO_RS_HOME` pointing
inside a `tempfile.mkdtemp` root. Daemons are started in their own process group.
On assertion failure, `finally` stops every daemon with a bounded wait, kills
leftover fixture PIDs recorded by `PASEO_ACP_FIXTURE_TRACE`, and removes the
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
`PASEO_ACP_FIXTURE_MODE=scripted`, which selects a deterministic scenario from
the prompt text:

| Prompt contains | Behaviour |
|---|---|
| `permission` | request `allow_once`/`reject_once`, then report the selection |
| `tool` | emit `tool_call` running then `tool_call_update` completed, with durable input/output |
| `resume-probe` | require a prior `session/load` in this fixture process |
| `cancel` | stream `partial`, wait for `session/cancel`, return cancelled |
| `secret-exit` | write the configured secret to stderr, then exit |
| anything else | stream `hello ` + `world` and usage |

`PASEO_ACP_FIXTURE_TRACE=<path>` appends JSON-lines process events
(`start`, `session/new`, `session/load`, permission decisions, cancellation)
for cleanup and restart assertions. `PASEO_ACP_FIXTURE_ECHO_SECRET=1` writes
`FIXTURE_SECRET` to stderr so the redaction scenario can verify it never reaches
durable output.
