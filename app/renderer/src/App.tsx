/**
 * App — multi-session shell: sidebar + focused chat.
 */

import React, { useCallback, useEffect, useRef, useState } from 'react'
import { Engine } from './engine'
import type { Event, SessionInfo } from './protocol'
import { DaemonRPC } from './protocol'
import { initialState } from './state'
import {
  type AppState,
  initialAppState,
  applyChatEvent,
  applyHistory,
  applySessionStatus,
  approvalCount,
  focusedChat,
  flattenSessions,
  pickBootFocus,
  readFocusedSession,
  sessionsFromList,
  setTranscript,
  writeFocusedSession,
  writeRecentProject,
} from './appState'
import { ChatView } from './ChatView'
import { Composer } from './Composer'
import { PermissionPrompt } from './PermissionPrompt'
import { StatusBar } from './StatusBar'
import { Sidebar } from './Sidebar'
import { CreatePopover } from './CreatePopover'
import { ConfirmDialog } from './ConfirmDialog'

function getWsUrl(): string {
  const params = new URLSearchParams(window.location.search)
  const urlParam = params.get('ws')
  if (urlParam) return urlParam
  const proto = window.location.protocol === 'https:' ? 'wss:' : 'ws:'
  return `${proto}//${window.location.host}/ws`
}

export function App() {
  const [app, setApp] = useState<AppState>(initialAppState)
  const engineRef = useRef<Engine | null>(null)
  const bootRef = useRef(0)
  const readyOnceRef = useRef(false)
  const focusedRef = useRef<string | null>(null)
  const sessionsRef = useRef<Map<string, SessionInfo>>(new Map())
  const refetchingRef = useRef(false)
  const [showCreate, setShowCreate] = useState(false)
  const [pendingDelete, setPendingDelete] = useState<SessionInfo | null>(null)
  const [daemonVersion, setDaemonVersion] = useState('')
  const [booting, setBooting] = useState(true)
  const [steerClearSignal, setSteerClearSignal] = useState(0)
  const [stats, setStats] = useState<{ input: number; output: number; cost: number } | null>(null)
  const sidebarRef = useRef<HTMLElement | null>(null)
  const wasBusyRef = useRef(false)

  useEffect(() => {
    focusedRef.current = app.focusedID
  }, [app.focusedID])

  useEffect(() => {
    sessionsRef.current = app.sessions
  }, [app.sessions])

  useEffect(() => {
    writeFocusedSession(app.focusedID)
  }, [app.focusedID])

  useEffect(() => {
    const n = approvalCount(app.sessions)
    void window.talos?.setBadgeCount?.(n)
  }, [app.sessions])

  const refetchSessions = useCallback(async (eng: Engine) => {
    const listed = (await eng.request(DaemonRPC.ListSessions)) as { sessions?: SessionInfo[] }
    const list = listed?.sessions ?? []
    setApp((a) => ({ ...a, sessions: sessionsFromList(list) }))
    return list
  }, [])

  const focusSession = useCallback(async (eng: Engine, sessionId: string) => {
    const prev = focusedRef.current
    if (prev === sessionId) {
      eng.subscribe(sessionId)
      return
    }
    if (prev) {
      eng.unsubscribe(prev)
    }

    const existing = sessionsRef.current.get(sessionId)
    // Resume unloaded sessions via createSession{resume}.
    if (existing && !existing.live) {
      try {
        await eng.request(DaemonRPC.CreateSession, {
          dir: existing.project_dir || existing.dir,
          resume: sessionId,
          isolation: existing.isolation || undefined,
        })
      } catch (e) {
        setApp((a) => ({
          ...a,
          error: e instanceof Error ? e.message : String(e),
        }))
        return
      }
    }

    eng.subscribe(sessionId)
    focusedRef.current = sessionId

    setApp((a) => {
      let next = { ...a, focusedID: sessionId, connected: true, needsProject: false, error: '' }
      if (!next.transcripts.has(sessionId)) {
        next = setTranscript(next, sessionId, initialState())
      }
      return next
    })

    void eng
      .request('engine.history', undefined, sessionId)
      .then((raw) => {
        const hist = (raw as { history?: unknown[] })?.history ?? []
        setApp((a) => {
          const chat = a.transcripts.get(sessionId)
          if (chat && chat.messages.length > 0) return a
          return applyHistory(a, sessionId, hist)
        })
      })
      .catch(() => {})

    void eng
      .request('engine.permissionMode', undefined, sessionId)
      .then((raw) => {
        const r = raw as { level?: string } | null
        if (!r?.level) return
        setApp((a) => {
          const prevChat = a.transcripts.get(sessionId) ?? initialState()
          return setTranscript(a, sessionId, { ...prevChat, permissionMode: r.level! })
        })
      })
      .catch(() => {})
  }, [])

  const checkVersionMismatch = useCallback(async (helloVersion: string, discoveryVersion: string) => {
    if (!helloVersion || !discoveryVersion) {
      setApp((a) => ({ ...a, versionBanner: null }))
      return
    }
    if (helloVersion !== discoveryVersion) {
      setApp((a) => ({
        ...a,
        versionBanner: `Daemon version ${helloVersion} differs from binary ${discoveryVersion}. Restart the daemon to upgrade.`,
      }))
    } else {
      setApp((a) => ({ ...a, versionBanner: null }))
    }
  }, [])

  const startEngine = useCallback(
    async (url: string, token: string | undefined, discoveryVersion: string) => {
      const boot = ++bootRef.current
      engineRef.current?.close()
      setApp(initialAppState())
      focusedRef.current = null
      readyOnceRef.current = false
      setBooting(true)
      setDaemonVersion(discoveryVersion)

      const eng = new Engine(url, token, {
        autoReconnect: true,
        resolveConnection: window.talos
          ? async () => {
              const d = await window.talos!.getDaemon()
              return { url: d.wsURL, token: d.token }
            }
          : undefined,
        onReconnect: () => {
          if (boot !== bootRef.current) return
          void (async () => {
            try {
              await refetchSessions(eng)
              const fid = focusedRef.current
              if (fid) eng.subscribe(fid)
              setApp((a) => ({ ...a, connected: true, error: '' }))
            } catch (e) {
              setApp((a) => ({
                ...a,
                connected: false,
                error: e instanceof Error ? e.message : String(e),
              }))
            }
          })()
        },
      })
      engineRef.current = eng

      eng.onReady = async (_sid, helloVersion) => {
        if (boot !== bootRef.current) return
        setApp((a) => ({ ...a, error: '' }))
        void checkVersionMismatch(helloVersion ?? eng.serverVersion, discoveryVersion)
        setDaemonVersion(helloVersion || discoveryVersion)

        if (readyOnceRef.current) return
        readyOnceRef.current = true

        try {
          // Optional auto-create from ?dir= for browser/dev.
          const dirParam = new URLSearchParams(window.location.search).get('dir')
          const listed = (await eng.request(DaemonRPC.ListSessions)) as { sessions?: SessionInfo[] }
          let list = listed?.sessions ?? []
          if (list.length === 0 && dirParam) {
            writeRecentProject(dirParam)
            await eng.request(DaemonRPC.CreateSession, { dir: dirParam })
            list = ((await eng.request(DaemonRPC.ListSessions)) as { sessions?: SessionInfo[] })
              ?.sessions ?? []
          }
          const map = sessionsFromList(list)
          const focus = pickBootFocus(map, readFocusedSession())
          setApp((a) => ({
            ...a,
            sessions: map,
            connected: true,
            needsProject: !focus,
            error: '',
          }))
          if (focus) {
            await focusSession(eng, focus)
          }
        } catch (e) {
          setApp((a) => ({
            ...a,
            error: e instanceof Error ? e.message : String(e),
            connected: false,
          }))
        } finally {
          if (boot === bootRef.current) setBooting(false)
        }
      }

      eng.onEvent = (ev: Event, session?: string) => {
        if (ev.etype === 'session_status') {
          const unknown = !sessionsRef.current.has(ev.id) && ev.state !== 'deleted'
          setApp((a) => applySessionStatus(a, ev))
          if (unknown && !refetchingRef.current) {
            refetchingRef.current = true
            void refetchSessions(eng).finally(() => {
              refetchingRef.current = false
            })
          }
          return
        }
        setApp((a) => applyChatEvent(a, ev, session))
      }

      eng.onClose = (reason) => {
        setApp((a) => ({ ...a, connected: false, error: reason }))
      }
    },
    [checkVersionMismatch, focusSession, refetchSessions],
  )

  useEffect(() => {
    let cancelled = false

    ;(async () => {
      try {
        if (window.talos) {
          const d = await window.talos.getDaemon()
          if (cancelled) return
          await startEngine(d.wsURL, d.token, d.version)
        } else {
          const url = getWsUrl()
          const params = new URLSearchParams(window.location.search)
          const token = params.get('token') ?? undefined
          await startEngine(url, token, '')
        }
      } catch (e) {
        if (!cancelled) {
          setBooting(false)
          setApp((a) => ({
            ...a,
            error: e instanceof Error ? e.message : String(e),
          }))
        }
      }
    })()

    const unsub = window.talos?.onNewSession(() => {
      setShowCreate(true)
    })

    return () => {
      cancelled = true
      unsub?.()
      engineRef.current?.close()
      engineRef.current = null
    }
  }, [startEngine])

  // Ctrl/Cmd+1..9 focus nth session in flattened list.
  useEffect(() => {
    const onKey = (e: KeyboardEvent) => {
      if (!(e.ctrlKey || e.metaKey)) return
      if (e.key < '1' || e.key > '9') return
      e.preventDefault()
      const idx = Number(e.key) - 1
      const flat = flattenSessions(sessionsRef.current)
      const target = flat[idx]
      if (!target || !engineRef.current) return
      void focusSession(engineRef.current, target.id)
    }
    window.addEventListener('keydown', onKey)
    return () => window.removeEventListener('keydown', onKey)
  }, [focusSession])

  const handleRestartDaemon = useCallback(async () => {
    if (!window.talos) return
    setApp((a) => ({ ...a, error: 'restarting daemon…', versionBanner: null }))
    try {
      const d = await window.talos.restartDaemon()
      await startEngine(d.wsURL, d.token, d.version)
    } catch (e) {
      setApp((a) => ({
        ...a,
        error: e instanceof Error ? e.message : String(e),
      }))
    }
  }, [startEngine])

  const chat = focusedChat(app)
  const sessionList = [...app.sessions.values()]
  const fid = app.focusedID
  const focusedSession = fid ? app.sessions.get(fid) ?? null : null

  const handleSubmit = useCallback(
    (text: string) => {
      if (!fid) return
      engineRef.current?.submit(text, fid)
    },
    [fid],
  )

  const handleSteer = useCallback(
    (text: string) => {
      if (!fid) return
      engineRef.current?.steer(text, fid)
    },
    [fid],
  )

  const handleWithdrawSteer = useCallback(() => {
    if (!fid) return
    void engineRef.current?.request('engine.withdrawSteer', undefined, fid)
  }, [fid])

  const handleInterrupt = useCallback(() => {
    if (!fid) return
    engineRef.current?.interrupt(fid)
  }, [fid])

  const handleLocalCommand = useCallback((name: string) => {
    if (name === '/new') {
      setShowCreate(true)
      return
    }
    if (name === '/sessions') {
      sidebarRef.current?.focus()
      const first = sidebarRef.current?.querySelector<HTMLElement>('.session-row, .new-agent-btn')
      first?.focus()
    }
  }, [])

  // Clear steer chips when turn ends; refresh stats.
  useEffect(() => {
    const busy = chat.busy
    if (wasBusyRef.current && !busy) {
      setSteerClearSignal((n) => n + 1)
      const eng = engineRef.current
      const sid = focusedRef.current
      if (eng && sid) {
        void eng
          .request('engine.stats', undefined, sid)
          .then((raw) => {
            const r = raw as { input?: number; output?: number; cost?: number }
            setStats({
              input: r.input ?? 0,
              output: r.output ?? 0,
              cost: r.cost ?? 0,
            })
          })
          .catch(() => {})
      }
    }
    wasBusyRef.current = busy
  }, [chat.busy])

  const handleApprove = useCallback(() => {
    if (!fid) return
    engineRef.current?.approve(true, fid)
    setApp((a) => {
      const prev = a.transcripts.get(fid) ?? initialState()
      return setTranscript(a, fid, { ...prev, permissionRequest: null })
    })
  }, [fid])

  const handleDeny = useCallback(() => {
    if (!fid) return
    engineRef.current?.approve(false, fid)
    setApp((a) => {
      const prev = a.transcripts.get(fid) ?? initialState()
      return setTranscript(a, fid, { ...prev, permissionRequest: null })
    })
  }, [fid])

  const handleStop = useCallback(async (id: string) => {
    const eng = engineRef.current
    if (!eng) return
    try {
      await eng.request(DaemonRPC.StopSession, { id })
    } catch (e) {
      setApp((a) => ({
        ...a,
        error: e instanceof Error ? e.message : String(e),
      }))
    }
  }, [])

  const handleDeleteConfirm = useCallback(async () => {
    const target = pendingDelete
    setPendingDelete(null)
    if (!target) return
    const eng = engineRef.current
    if (!eng) return
    try {
      await eng.request(DaemonRPC.DeleteSession, { id: target.id })
      if (focusedRef.current === target.id) {
        eng.unsubscribe(target.id)
        focusedRef.current = null
        setApp((a) => ({ ...a, focusedID: null }))
      }
    } catch (e) {
      setApp((a) => ({
        ...a,
        error: e instanceof Error ? e.message : String(e),
      }))
    }
  }, [pendingDelete])

  const handleReveal = useCallback((dir: string) => {
    if (!dir) return
    void window.talos?.showItemInFolder(dir)
  }, [])

  const handleCreated = useCallback(
    (sessionID: string) => {
      const eng = engineRef.current
      if (!eng) return
      void (async () => {
        await refetchSessions(eng)
        await focusSession(eng, sessionID)
      })()
    },
    [focusSession, refetchSessions],
  )

  const deleteWarning = (() => {
    if (!pendingDelete) return ''
    const s = pendingDelete
    const risky =
      s.isolation === 'worktree' && ((s.ahead ?? 0) > 0 || !!s.dirty)
    if (risky) {
      return 'This worktree session has unmerged commits or dirty files. The branch may be kept if it is not fully merged.'
    }
    return `Delete session ${s.preview || s.id.slice(0, 8)}? This cannot be undone.`
  })()

  if (booting && !app.connected && !app.error) {
    return (
      <div className="app shell">
        <div className="connecting">
          <h1>talos</h1>
          <p>connecting…</p>
        </div>
      </div>
    )
  }

  return (
    <div className="app shell">
      {app.versionBanner && (
        <div className="version-banner">
          <span>{app.versionBanner}</span>
          {window.talos && (
            <button type="button" onClick={() => void handleRestartDaemon()}>
              Restart daemon
            </button>
          )}
        </div>
      )}
      <div className="shell-body">
        <Sidebar
          ref={sidebarRef}
          sessions={sessionList}
          focusedID={app.focusedID}
          connected={app.connected}
          daemonLabel={daemonVersion ? `v${daemonVersion}` : undefined}
          onNewAgent={() => setShowCreate(true)}
          onFocus={(id) => {
            const eng = engineRef.current
            if (eng) void focusSession(eng, id)
          }}
          onStop={(id) => void handleStop(id)}
          onDelete={(id) => {
            const s = app.sessions.get(id)
            if (s) setPendingDelete(s)
          }}
          onReveal={handleReveal}
        />
        <main className="main-column">
          {app.error && !fid && (
            <div className="main-error">{app.error}</div>
          )}
          {!fid ? (
            <div className="empty-main">
              <h1>talos</h1>
              <p>Select a session or start a new agent.</p>
              <button type="button" className="primary-btn" onClick={() => setShowCreate(true)}>
                New Agent
              </button>
              {app.error && <p className="error">{app.error}</p>}
            </div>
          ) : (
            <>
              <StatusBar
                provider={chat.provider}
                model={chat.model}
                thinkingLevel={chat.thinkingLevel}
                permissionMode={chat.permissionMode}
                promptTokens={chat.promptTokens}
                contextLimit={chat.contextLimit}
                busy={chat.busy}
                stats={stats}
              />
              <ChatView
                messages={chat.messages}
                streamedText={chat.streamedText}
                streamedThinking={chat.streamedThinking}
                activeTools={chat.activeTools}
                busy={chat.busy}
              />
              {chat.permissionRequest && (
                <PermissionPrompt
                  request={chat.permissionRequest}
                  onApprove={handleApprove}
                  onDeny={handleDeny}
                />
              )}
              <Composer
                busy={chat.busy}
                session={focusedSession}
                provider={chat.provider || focusedSession?.provider || ''}
                model={chat.model || focusedSession?.model || ''}
                thinkingLevel={chat.thinkingLevel}
                permissionMode={chat.permissionMode}
                engine={engineRef.current}
                sessionId={fid}
                onSubmit={handleSubmit}
                onSteer={handleSteer}
                onWithdrawSteer={handleWithdrawSteer}
                onInterrupt={handleInterrupt}
                onLocalCommand={handleLocalCommand}
                steerClearSignal={steerClearSignal}
              />
            </>
          )}
        </main>
      </div>

      {showCreate && (
        <CreatePopover
          engine={engineRef.current}
          onCreated={handleCreated}
          onClose={() => setShowCreate(false)}
        />
      )}
      {pendingDelete && (
        <ConfirmDialog
          title="Delete session"
          body={deleteWarning}
          confirmLabel="Delete"
          danger
          onConfirm={() => void handleDeleteConfirm()}
          onCancel={() => setPendingDelete(null)}
        />
      )}
    </div>
  )
}
