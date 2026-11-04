# Local IPC replay and delivery contract

Protocol version remains **1**. `replay_sessions` is an additive command. Older
servers may reject it; clients must report incompatibility rather than silently
substitute an unsafe global cursor. Wire types live in
`crates/paseo-ipc/src/lib.rs`; handlers live in `apps/paseod/src/main.rs`.

## Identity and ordering

- Durable identity: timeline `id` plus `session_id`.
- `sequence` is monotonic **within one session**, not globally.
- Live notification `cursor` is that event's session sequence; determine the
  session from `event.payload.session_id`. It is not a global high watermark.
- Persisted replay is the authority. Notifications may be duplicated by replay
  and may be missed during disconnection or queue lag.

## Lossless multi-session pagination

```json
{"method":"replay_sessions","params":{"after":{"SESSION_ID":7},"limit":100}}
```

The response contains `events`, `session_cursors`,
`cursor_scope: "per_session_sequence"`, and `has_more`.

- `after` maps session ids to the last delivered sequence. Omitted existing or
  newly discovered sessions start at zero. Unknown session ids are rejected.
- Limits default to 256 and clamp to 1–4096. Ordered session heads are merged by
  timestamp, session id and sequence; the response is limited across all sessions.
  Sequence order within each session is preserved even if the wall clock moves
  backwards. Timestamps do not define a strict global ordering.
- Each returned cursor advances only over **delivered** records. A session whose
  records were truncated retains its prior cursor (or zero).
- Pass the returned `session_cursors` into the next request until `has_more` is
  false. Do not turn the maximum cursor into a scalar watermark.
- The response is a page, not an atomic multi-session snapshot. New records may
  appear while paging; live notifications and subsequent replay reconcile them.

`replay_session {session_id, after, limit}` remains the efficient single-session
API. The original aggregate `replay {after, limit}` is retained for compatibility;
its scalar `after` is applied independently to every session. Its `next_cursor`
**cannot safely resume a mixed-session stream**. New consumers must use
`replay_sessions` or track separate `replay_session` positions.

## Lag and cancellation safety

A `ServerFrame::Event` with kind `replay_required` is a control notification, not
a durable timeline record. It has cursor zero, no durable event id, and a payload
with `reason` and `skipped`. Reasons currently include `daemon_event_lag`,
`server_event_lag`, and `client_event_lag`. Never advance a replay cursor from it.
Replay from the last delivered per-session cursors, then deduplicate buffered
notifications. The skipped value is diagnostic, not a recovery position.

Server broadcast delivery and client RPC-time event buffering are bounded.
Client overflow retains a replay marker while continuing to read the RPC response,
rather than silently evicting durable notifications. A partial incoming frame
survives cancellation when live delivery wins the server's select; client
`next_frame` similarly retains partial input across cancellation. A cancelled
`call` is **not** safe to retry on the same connection because a response may still
be in flight; reconnect before issuing another request.

Daemon client tasks are tracked and capped at 64. Handshake and individual frame
writes have five-second deadlines; shutdown drains clients for at most 500 ms
before aborting remaining tasks and shutting down the core. Frame size remains
bounded at 1 MiB. After a crash, stale lock/socket removal remains an explicit
operator action after checking that no daemon is running.

## CLI

```sh
paseoctl events replay --cursors '{}' --limit 100 --json
paseoctl events replay --cursors '{"SESSION_ID":7}' --limit 100 --json
paseoctl session replay --session SESSION_ID --after 7 --json
paseoctl events tail --follow --limit 100 --json
```

Follow subscribes on connection, drains all replay pages, deduplicates buffered
live notifications and replays on explicit lag or a sequence gap. Its initial
`--after` seeds the cursor for each session present at startup; newly created
sessions start at zero. Transport disconnection still ends the command: callers
must reconnect with their saved per-session positions; automatic reconnect is
not claimed.

## Evidence

- IPC `tests/reliability.rs`: fragmented request with an intervening live event;
  overflowing client queue preserves an explicit replay requirement.
- IPC unit tests: cancelled partial read, lag signal and closed event channel
  without closing command input.
- Daemon unit tests: one-record pages across two sessions deliver all records
  exactly once and never acknowledge truncated records; unknown session cursor
  rejection; legacy replay cursor truncation regression.
- CLI parser tests: typed JSON cursor maps, session-specific replay and invalid
  cursor rejection. Process-level scenarios are tracked in `docs/STATUS.md`.
