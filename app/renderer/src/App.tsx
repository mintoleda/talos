/**
 * App — root component wiring Engine + State + UI.
 */

import React, { useState, useCallback, useEffect, useRef } from 'react'
import { Engine } from './engine'
import type { Event, SessionInfo } from './protocol'
import { type ChatState, initialState, reduceState, ingestHistory } from './state'
import { ChatView } from './ChatView'
import { Composer } from './Composer'
import { PermissionPrompt } from './PermissionPrompt'
import { StatusBar } from './StatusBar'

const RECENT_PROJECT_KEY = 'talos.recentProject'

function getWsUrl(): string {
  const params = new URLSearchParams(window.location.search)
  const urlParam = params.get('ws')
  if (urlParam) return urlParam

  // In browser/dev, Vite (or the daemon) proxies /ws.
  const proto = window.location.protocol === 'https:' ? 'wss:' : 'ws:'
  return `${proto}//${window.location.host}/ws`
}

function readRecentProject(): string | null {
  try {
    return localStorage.getItem(RECENT_PROJECT_KEY)
  } catch {
    return null
  }
}

function writeRecentProject(dir: string) {
  try {
    localStorage.setItem(RECENT_PROJECT_KEY, dir)
  } catch {
    /* ignore */
  }
}

function pickMostRecentLive(sessions: SessionInfo[]): SessionInfo | null {
  const live = sessions.filter((s) => s.live)
  if (live.length === 0) return null
  live.sort((a, b) => {
    const ta = Date.parse(a.last_active || a.created_at || '') || 0
    const tb = Date.parse(b.last_active || b.created_at || '') || 0
    return tb - ta
  })
  return live[0]
}

export function App() {
  const [state, setState] = useState<ChatState>(initialState)
  const engineRef = useRef<Engine | null>(null)
  const [connected, setConnected] = useState(false)
  const [session, setSession] = useState('')
  const [error, setError] = useState('')
  const [needsProject, setNeedsProject] = useState(false)
  const [versionBanner, setVersionBanner] = useState<string | null>(null)
  const bootRef = useRef(0)
  const sessionRef = useRef('')
  const readyOnceRef = useRef(false)

  useEffect(() => {
    sessionRef.current = session
  }, [session])

  const attachSession = useCallback(async (eng: Engine, sessionId: string) => {
    eng.subscribe(sessionId)
    setSession(sessionId)
    setConnected(true)
    setNeedsProject(false)
    setError('')

    eng
      .request('engine.history', undefined, sessionId)
      .then((raw) => {
        const hist = (raw as { history?: unknown[] })?.history ?? []
        setState((s) => ingestHistory(s, hist))
      })
      .catch(() => {})

    eng
      .request('engine.permissionMode', undefined, sessionId)
      .then((raw) => {
        const r = raw as { level?: string } | null
        if (r?.level) {
          setState((s) => ({ ...s, permissionMode: r.level! }))
        }
      })
      .catch(() => {})
  }, [])

  const resolveOrCreateSession = useCallback(
    async (eng: Engine, preferredDir?: string | null): Promise<string | null> => {
      const listed = (await eng.request('daemon.listSessions')) as {
        sessions?: SessionInfo[]
      }
      const sessions = listed?.sessions ?? []
      const recent = pickMostRecentLive(sessions)
      if (recent?.id) {
        return recent.id
      }

      let dir =
        preferredDir ||
        new URLSearchParams(window.location.search).get('dir') ||
        readRecentProject()

      // In Electron, missing dir → show Open project UI (don't auto-dialog).
      if (!dir) {
        return null
      }

      writeRecentProject(dir)
      const created = (await eng.request('daemon.createSession', {
        dir,
        // default isolation: omit → daemon default (worktree); never force "none"
      })) as { session?: { id?: string } }
      return created?.session?.id ?? null
    },
    [],
  )

  const checkVersionMismatch = useCallback(
    async (helloVersion: string, discoveryVersion: string) => {
      if (!helloVersion || !discoveryVersion) {
        setVersionBanner(null)
        return
      }
      if (helloVersion !== discoveryVersion) {
        setVersionBanner(
          `Daemon version ${helloVersion} differs from binary ${discoveryVersion}. Restart the daemon to upgrade.`,
        )
      } else {
        setVersionBanner(null)
      }
    },
    [],
  )

  const startEngine = useCallback(
    async (url: string, token: string | undefined, discoveryVersion: string) => {
      const boot = ++bootRef.current
      engineRef.current?.close()
      setState(initialState)
      setConnected(false)
      setSession('')
      setNeedsProject(false)

      readyOnceRef.current = false
      const eng = new Engine(url, token, {
        autoReconnect: true,
        resolveConnection: window.talos
          ? async () => {
              const d = await window.talos!.getDaemon()
              return { url: d.wsURL, token: d.token }
            }
          : undefined,
        onReconnect: (sid) => {
          if (boot !== bootRef.current) return
          const current = sessionRef.current || sid
          if (current) {
            eng.subscribe(current)
            setConnected(true)
            setError('')
          }
        },
      })
      engineRef.current = eng

      eng.onReady = async (sid, helloVersion) => {
        if (boot !== bootRef.current) return
        setError('')
        void checkVersionMismatch(helloVersion ?? eng.serverVersion, discoveryVersion)

        // Full bootstrap only on first hello; reconnects use onReconnect.
        if (readyOnceRef.current) return
        readyOnceRef.current = true

        try {
          let sessionId = sid
          if (!sessionId) {
            sessionId = (await resolveOrCreateSession(eng)) ?? ''
          }
          if (!sessionId) {
            if (window.talos) {
              setNeedsProject(true)
              setConnected(false)
              setError('')
              return
            }
            setError('no session — pass ?dir=/absolute/project/path')
            setConnected(false)
            return
          }
          await attachSession(eng, sessionId)
        } catch (e) {
          setError(e instanceof Error ? e.message : String(e))
          setConnected(false)
        }
      }

      eng.onEvent = (ev: Event) => {
        setState((s) => reduceState(s, ev))
      }

      eng.onClose = (reason) => {
        setConnected(false)
        setError(reason)
      }
    },
    [attachSession, checkVersionMismatch, resolveOrCreateSession],
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
          setError(e instanceof Error ? e.message : String(e))
        }
      }
    })()

    const unsub = window.talos?.onNewSession(() => {
      void (async () => {
        const eng = engineRef.current
        if (!eng || !window.talos) return
        try {
          const dir = await window.talos.pickDirectory()
          if (!dir) return
          writeRecentProject(dir)
          const created = (await eng.request('daemon.createSession', { dir })) as {
            session?: { id?: string }
          }
          const id = created?.session?.id
          if (id) await attachSession(eng, id)
        } catch (e) {
          setError(e instanceof Error ? e.message : String(e))
        }
      })()
    })

    return () => {
      cancelled = true
      unsub?.()
      engineRef.current?.close()
      engineRef.current = null
    }
  }, [attachSession, startEngine])

  const handleOpenProject = useCallback(async () => {
    if (!window.talos) return
    const eng = engineRef.current
    try {
      const dir = await window.talos.pickDirectory()
      if (!dir) return
      writeRecentProject(dir)
      if (!eng) {
        const d = await window.talos.getDaemon()
        await startEngine(d.wsURL, d.token, d.version)
        return
      }
      const created = (await eng.request('daemon.createSession', { dir })) as {
        session?: { id?: string }
      }
      const id = created?.session?.id
      if (!id) {
        setError('failed to create session')
        return
      }
      await attachSession(eng, id)
    } catch (e) {
      setError(e instanceof Error ? e.message : String(e))
    }
  }, [attachSession, startEngine])

  const handleRestartDaemon = useCallback(async () => {
    if (!window.talos) return
    setError('restarting daemon…')
    setVersionBanner(null)
    try {
      const d = await window.talos.restartDaemon()
      await startEngine(d.wsURL, d.token, d.version)
    } catch (e) {
      setError(e instanceof Error ? e.message : String(e))
    }
  }, [startEngine])

  const handleSubmit = useCallback(
    (text: string) => {
      engineRef.current?.submit(text, session || undefined)
    },
    [session],
  )

  const handleInterrupt = useCallback(() => {
    engineRef.current?.interrupt(session || undefined)
  }, [session])

  const handleApprove = useCallback(() => {
    engineRef.current?.approve(true, session || undefined)
    setState((s) => ({ ...s, permissionRequest: null }))
  }, [session])

  const handleDeny = useCallback(() => {
    engineRef.current?.approve(false, session || undefined)
    setState((s) => ({ ...s, permissionRequest: null }))
  }, [session])

  if (!connected) {
    return (
      <div className="app">
        {versionBanner && (
          <div className="version-banner">
            <span>{versionBanner}</span>
            {window.talos && (
              <button type="button" onClick={() => void handleRestartDaemon()}>
                Restart daemon
              </button>
            )}
          </div>
        )}
        <div className="connecting">
          <h1>talos</h1>
          {error ? <p className="error">{error}</p> : needsProject ? null : <p>connecting…</p>}
          {needsProject && (
            <>
              <p>Open a project to start a session.</p>
              <button type="button" className="primary-btn" onClick={() => void handleOpenProject()}>
                Open project
              </button>
            </>
          )}
          {!window.talos && !needsProject && (
            <p className="hint">
              Start a daemon with <code>talos serve</code>
            </p>
          )}
        </div>
      </div>
    )
  }

  return (
    <div className="app">
      {versionBanner && (
        <div className="version-banner">
          <span>{versionBanner}</span>
          {window.talos && (
            <button type="button" onClick={() => void handleRestartDaemon()}>
              Restart daemon
            </button>
          )}
        </div>
      )}
      <StatusBar
        provider={state.provider}
        model={state.model}
        thinkingLevel={state.thinkingLevel}
        permissionMode={state.permissionMode}
        promptTokens={state.promptTokens}
        contextLimit={state.contextLimit}
        busy={state.busy}
      />
      <ChatView
        messages={state.messages}
        streamedText={state.streamedText}
        streamedThinking={state.streamedThinking}
        activeTools={state.activeTools}
        busy={state.busy}
      />
      {state.permissionRequest && (
        <PermissionPrompt
          request={state.permissionRequest}
          onApprove={handleApprove}
          onDeny={handleDeny}
        />
      )}
      <Composer busy={state.busy} onSubmit={handleSubmit} onInterrupt={handleInterrupt} />
    </div>
  )
}
