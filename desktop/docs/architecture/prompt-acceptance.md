# Durable prompt acceptance

The IPC v1 `prompt` command accepts an optional `request_key`. Keyed requests use
SQLite schema v2; existing v1 databases are upgraded transactionally. Databases
with a newer, unsupported schema version are rejected rather than silently used.

```json
{"method":"prompt","params":{"session_id":"SESSION_ID","text":"Summarize this project","request_key":"summary-001"}}
```

The keyed response includes `run_id`, `request_key`, the run's `status`, and
`restored` (true when returning an already-accepted request).

## Contract

- A key is scoped to one session, must not be whitespace-only, and is at most
  **256 UTF-8 bytes**. Keys are opaque; use the same exact key on retry.
- The payload fingerprint is SHA-256 over `paseo.prompt.v1\0` followed by the
  exact UTF-8 prompt bytes. Whitespace changes in the prompt are a different
  payload. The key table stores the fingerprint, not a second prompt copy.
- The key, original run id, user timeline event and initial `Starting` event are
  committed in one SQLite transaction. Sequence allocation is serialized with
  that transaction. No key can acknowledge an uncommitted acceptance event.
- An identical retry returns the original run, including while the session is
  busy, when it is unloaded, after archive, and after a daemon restart. It does
  not append another user event or invoke the provider again.
- Reusing a key with different prompt bytes fails with RPC code **-32012**.
  Empty/oversized keys fail with **-32602**. Conflicts do not expose prompt text.
- A new key still requires a loaded, writable session and obeys normal busy,
  archive, capability and shutdown checks. A rejected, unaccepted attempt is not
  recorded as accepted.
- Startup reconciles an accepted run lacking a terminal state to `Interrupted`.
  Retrying its key returns that interrupted run; it never silently re-executes.
  To deliberately run again, use a **new key** after inspecting the old outcome.

This is durable **acceptance deduplication**, not exactly-once external tool/model
execution. A crash can occur after acceptance but before a provider receives the
prompt, or after the provider acts but before a terminal record is committed.
Paseo does not guess which external effects occurred or automatically retry them.

## Cancellation and queue pressure

Prompt submission runs in an owned task; disconnecting or dropping the caller
cannot abandon a reserved active slot halfway through acceptance/startup. The
same serialized start gate is used by shutdown, so accepted work is either
started/settled or recovered explicitly after restart. This also protects the
unkeyed submission path from caller-drop slot leaks.

The store actor uses a bounded async channel with awaited send and a dedicated
blocking receiver. Queue pressure applies backpressure rather than dropping
terminal lifecycle writes. Provider cancellation fallback and ACP session close
are bounded; lifecycle persistence failures are logged instead of silently
reported as successful completion.

## CLI

```sh
paseoctl session prompt SESSION_ID 'Summarize this project' --request-key summary-001 --json
# Retrying this exact request returns the same run_id, with restored: true.
paseoctl session prompt SESSION_ID 'Summarize this project' --request-key summary-001 --json
```

Without `--request-key`, each submission remains a distinct prompt; do not
blindly retry an unkeyed submission after a lost response.

## Evidence and limits

Store tests cover transaction rollback, conflicting payloads, session scoping,
v1→v2 upgrades, reopen integrity and future-version rejection. Core/daemon tests
cover busy/concurrent retries, unloaded/restarted/archived sessions, interrupted
acceptance, dropped callers, error mapping and store saturation. The process
harness independently checks provider invocation/timeline counts and restart
behavior. Current integrated results and any blockers remain in
[../STATUS.md](../STATUS.md); this contract is not a full release certification.
