# The Engine Seam — unifying local, attach, and (future) web/remote clients

This doc captures a design direction, not yet-built code. It explains why the
local TUI and the `attach`-to-server TUI have diverging capabilities today, and
the refactor that collapses them into one path — which also makes a web UI or a
Tailscale-remote client a straightforward third consumer rather than a rewrite.

## Where we are today

talos already has a client/server split:

- `internal/server` — an **`Engine`** interface (`SessionID`, `Subscribe`,
  `Submit`, `Interrupt`, `Approve`, `Snapshot`) with a `LoopEngine`
  implementation that owns the `loop.Loop`. `Server` exposes it over a Unix
  socket; `RunClient` dials it.
- `internal/transport` — the wire format: `ServerMsg` / `ClientMsg` JSON.
- `internal/protocol` — the `Event` stream (`TextDelta`, `ToolStarted`,
  `TurnEnded`, …) broadcast server→client.

The TUI is already just a client. `runAttach` in `cmd/talos/main.go` proves a
frontend need not share the engine's process.

## The problem: the protocol is push-only

Server→client is a rich event stream. Client→server is **three verbs**:
`input`, `interrupt`, `approve`. There is no request/response channel.

Everything that needs a query or a command with a return value — list models,
list sessions, switch model, cycle thinking, compact, get stats, login, list
files for the `@` picker, fetch transcript history — is done in **local mode**
by calling Go **function pointers** stored in `tui.Config` (`FetchModels`,
`FetchSessions`, `NewSession`, `StatsSnapshot`, `MCPStatus`, …). See
`cmd/talos/app.go:makeNewTabFn` and `internal/tui/run.go:Run`, which populate
~20 such callbacks.

`runAttach` cannot populate those callbacks — it is on the other end of a socket
that only speaks `input`/`interrupt`/`approve`. So it wires **three**
(`SubmitFn`, `SubmitSlash`, `InterruptFn`) and leaves the rest nil. The TUI
degrades gracefully on nil, so each missing callback silently becomes a
second-class or dead feature.

### The capability gap (attach mode vs local)

Walking `Model.handleSlash` with the attach `Config`:

| Feature | Local | Attached | Cause |
|---|---|---|---|
| `/new` | new session | silently nothing | `NewSession` nil |
| `/resume` | picker / `<id>` | "/resume is unavailable" | `ResumeSession` nil |
| `/model` | interactive picker | text list forwarded | `FetchModels` nil |
| `/stats` | real numbers | canned string | no `StatsSnapshot` |
| `/compact` | compacts | silently nothing | `CompactCh` nil; server has no handler |
| `/login` | dialog | "not available in this mode" | `LoginProviders` nil |
| `/mcp` | status | "mcp status unavailable" | `MCPStatus` nil |
| steer (Enter while busy) | queues | silently nothing | `SteerQueue` nil |
| status bar cost/context | live | starts at 0, never reseeds | no `SeedStats`/`StatsSnapshot` |

The server's `SetSlashHandler` (`cmd/talos/main.go:runServer`) only implements
`/thinking`, `/model`, `/mcp`, so even the "forward it" path is dead for `/new`,
`/resume`, `/compact`, `/stats`, `/login`.

### Latent wrong-machine bugs

Some frontend operations touch the **client's** machine but conceptually belong
to the **engine's** machine. On the same box (today's attach) this is
coincidentally fine; over Tailscale/web it is a real bug:

- `/push` (`Model.pushInstruction`) shells out to `git`/`gh` in the client cwd.
- `@path` resolution (`resolveInput`) and the `@` file picker (`filepicker.go`)
  read the client filesystem.
- clipboard paste (`pasteClipboardImage`) runs the client's `wl-paste`/`xclip`.

## The direction: one `Engine` client interface, two implementations

Invert the dependency. Instead of the TUI holding 20 closures into in-process
state, it depends on a single **client-facing `Engine` interface**. Provide:

- `LocalEngine` — direct in-process calls (exactly what `app.go` does today).
- `RemoteEngine` — marshals each method to a protocol request/response over the
  socket.

The TUI `Config`'s ~20 callbacks collapse to one `Engine`. `Run` and
`runAttach` become the same function differing only in which `Engine` they
construct. There is no longer a place for local and attach to drift, because
there is only one wiring. "Local" = attached to an in-process engine; "attach" =
attached to a remote engine; "web" = a third consumer of the same interface.

Sketch:

```go
type Engine interface {
    // fire-and-forget (already exist as verbs)
    Submit(blocks []protocol.ContentBlock)
    Interrupt()
    Approve(ok bool, plan []byte)
    Steer(blocks []protocol.ContentBlock)

    // request/response (today's Config callbacks)
    NewSession() (id string, err error)
    Resume(id string) ([]protocol.FrozenMessage, error)
    ListSessions() ([]SessionEntry, error)
    DeleteSession(id string) error
    ListModels() ([]models.Entry, error)
    SwitchModel(provider, model string) error
    CycleThinking() (level string, err error)
    Compact(focus string) error
    Stats() (Stats, error)
    LoginProviders() ([]LoginProvider, error)
    Login(provider, key string) error
    MCPStatus() (string, error)
    ListFiles(prefix string) ([]string, error) // @-picker over the wire
    History() ([]protocol.FrozenMessage, error)

    Events() <-chan protocol.Event
}
```

## Sequenced plans

Implement in this order — each is a landable step:

1. `plans/engine-1-request-response.md` — add request/response framing to
   `transport`.
2. `plans/engine-2-local-engine.md` — extract the client-facing `Engine`
   interface + `LocalEngine` (no behavior change). **De-risks everything.**
3. `plans/engine-3-tui-on-engine.md` — point the TUI at `Engine`; collapse the
   20 `Config` callbacks.
4. `plans/engine-4-remote-engine.md` — `RemoteEngine` + server-side request
   dispatch. Attach reaches parity as a side effect.
5. `plans/engine-5-server-authoritative-state.md` — move stats/history into the
   engine; route `/push`, `@path`, file listing to the engine's machine.
6. `plans/engine-6-remote-transport.md` — TCP/WebSocket listener + auth for
   Tailscale and the browser.

After step 2 the local/attach divergence is already structurally gone; steps
3–4 make it real in the UI; step 5 fixes the wrong-machine class; step 6 opens
it to the network.
