/**
 * appState.ts — Multi-session app state on top of per-session ChatState.
 */

import type { Event, SessionInfo, SessionStatusEvent } from './protocol'
import { type ChatState, initialState, reduceState, ingestHistory } from './state'

export const TRANSCRIPT_LRU = 10
export const FOCUSED_SESSION_KEY = 'talos.focusedSession'
export const RECENT_PROJECTS_KEY = 'talos.recentProjects'

export type AppState = {
  sessions: Map<string, SessionInfo>
  focusedID: string | null
  transcripts: Map<string, ChatState>
  /** Access order for LRU (oldest first). */
  transcriptOrder: string[]
  connected: boolean
  error: string
  versionBanner: string | null
  needsProject: boolean
}

export function initialAppState(): AppState {
  return {
    sessions: new Map(),
    focusedID: null,
    transcripts: new Map(),
    transcriptOrder: [],
    connected: false,
    error: '',
    versionBanner: null,
    needsProject: false,
  }
}

/** Per-session chat reducer (alias of reduceState). */
export const reduceChat = reduceState

export function focusedChat(app: AppState): ChatState {
  if (!app.focusedID) return initialState()
  return app.transcripts.get(app.focusedID) ?? initialState()
}

export function sessionsFromList(list: SessionInfo[]): Map<string, SessionInfo> {
  const m = new Map<string, SessionInfo>()
  for (const s of list) m.set(s.id, s)
  return m
}

export function pickBootFocus(
  sessions: Map<string, SessionInfo>,
  storedID: string | null,
): string | null {
  if (storedID && sessions.has(storedID)) return storedID
  const live = [...sessions.values()].filter((s) => s.live)
  if (live.length === 0) return null
  live.sort((a, b) => sessionActiveMs(b) - sessionActiveMs(a))
  return live[0]?.id ?? null
}

export function sessionActiveMs(s: SessionInfo): number {
  return Date.parse(s.last_active || s.created_at || '') || 0
}

export function flattenSessions(sessions: Map<string, SessionInfo>): SessionInfo[] {
  const groups = groupSessionsByProject([...sessions.values()])
  const out: SessionInfo[] = []
  for (const g of groups) out.push(...g.sessions)
  return out
}

export type ProjectGroup = {
  projectDir: string
  label: string
  sessions: SessionInfo[]
}

export function groupSessionsByProject(list: SessionInfo[]): ProjectGroup[] {
  const byDir = new Map<string, SessionInfo[]>()
  for (const s of list) {
    const key = s.project_dir || s.dir || '(unknown)'
    const arr = byDir.get(key) ?? []
    arr.push(s)
    byDir.set(key, arr)
  }
  const groups: ProjectGroup[] = []
  for (const [projectDir, sessions] of byDir) {
    sessions.sort((a, b) => sessionActiveMs(b) - sessionActiveMs(a))
    groups.push({
      projectDir,
      label: basename(projectDir),
      sessions,
    })
  }
  groups.sort((a, b) => {
    const ta = a.sessions[0] ? sessionActiveMs(a.sessions[0]) : 0
    const tb = b.sessions[0] ? sessionActiveMs(b.sessions[0]) : 0
    return tb - ta
  })
  return groups
}

export function basename(path: string): string {
  const cleaned = path.replace(/\/+$/, '')
  const i = cleaned.lastIndexOf('/')
  return i >= 0 ? cleaned.slice(i + 1) : cleaned || path
}

export function touchTranscript(order: string[], id: string): string[] {
  const next = order.filter((x) => x !== id)
  next.push(id)
  return next
}

export function pruneTranscripts(
  transcripts: Map<string, ChatState>,
  order: string[],
  focusedID: string | null,
  cap = TRANSCRIPT_LRU,
): { transcripts: Map<string, ChatState>; order: string[] } {
  if (transcripts.size <= cap) return { transcripts, order }
  const next = new Map(transcripts)
  let nextOrder = [...order]
  while (next.size > cap && nextOrder.length > 0) {
    const oldest = nextOrder[0]
    nextOrder = nextOrder.slice(1)
    if (oldest === focusedID) {
      nextOrder.push(oldest)
      // Avoid infinite loop if only focused remains over cap (shouldn't).
      if (nextOrder.every((id) => id === focusedID || !next.has(id))) break
      continue
    }
    next.delete(oldest)
  }
  nextOrder = nextOrder.filter((id) => next.has(id))
  return { transcripts: next, order: nextOrder }
}

export function setTranscript(
  app: AppState,
  id: string,
  chat: ChatState,
): AppState {
  const transcripts = new Map(app.transcripts)
  transcripts.set(id, chat)
  const order = touchTranscript(app.transcriptOrder, id)
  const pruned = pruneTranscripts(transcripts, order, app.focusedID)
  return { ...app, transcripts: pruned.transcripts, transcriptOrder: pruned.order }
}

export function applyChatEvent(
  app: AppState,
  ev: Event,
  sessionHint?: string,
): AppState {
  if (ev.etype === 'session_status') {
    return applySessionStatus(app, ev)
  }

  const sid = sessionHint || app.focusedID
  if (!sid) return app

  const prev = app.transcripts.get(sid) ?? initialState()
  const next = reduceChat(prev, ev)
  return setTranscript(app, sid, next)
}

export function applySessionStatus(app: AppState, ev: SessionStatusEvent): AppState {
  if (ev.state === 'deleted') {
    const sessions = new Map(app.sessions)
    sessions.delete(ev.id)
    const transcripts = new Map(app.transcripts)
    transcripts.delete(ev.id)
    const order = app.transcriptOrder.filter((id) => id !== ev.id)
    let focusedID = app.focusedID
    if (focusedID === ev.id) focusedID = null
    return { ...app, sessions, transcripts, transcriptOrder: order, focusedID }
  }

  const existing = app.sessions.get(ev.id)
  if (!existing) {
    // Unknown ID — caller should refetch listSessions.
    return app
  }

  const sessions = new Map(app.sessions)
  const patched: SessionInfo = {
    ...existing,
    state: ev.state,
    live: ev.state !== 'unloaded' && ev.state !== 'merged',
  }
  if (ev.preview !== undefined) patched.preview = ev.preview
  if (ev.dir !== undefined) patched.dir = ev.dir
  if (ev.state === 'unloaded' || ev.state === 'merged') patched.live = false
  if (ev.state === 'merged') patched.merged = true
  if (ev.state === 'idle' || ev.state === 'busy' || ev.state === 'awaiting_approval') {
    patched.live = true
  }
  sessions.set(ev.id, patched)
  return { ...app, sessions }
}

export function applyHistory(app: AppState, sessionID: string, history: unknown[]): AppState {
  const prev = app.transcripts.get(sessionID) ?? initialState()
  // Avoid double-ingest if we already have messages from a prior focus.
  if (prev.messages.length > 0) return app
  return setTranscript(app, sessionID, ingestHistory(prev, history))
}

export function readFocusedSession(): string | null {
  try {
    return localStorage.getItem(FOCUSED_SESSION_KEY)
  } catch {
    return null
  }
}

export function writeFocusedSession(id: string | null) {
  try {
    if (id) localStorage.setItem(FOCUSED_SESSION_KEY, id)
    else localStorage.removeItem(FOCUSED_SESSION_KEY)
  } catch {
    /* ignore */
  }
}

export function readRecentProjects(): string[] {
  try {
    const raw = localStorage.getItem(RECENT_PROJECTS_KEY)
    if (!raw) {
      // Migrate single-key from app-4 if present.
      const one = localStorage.getItem('talos.recentProject')
      return one ? [one] : []
    }
    const parsed = JSON.parse(raw) as unknown
    if (!Array.isArray(parsed)) return []
    return parsed.filter((x): x is string => typeof x === 'string')
  } catch {
    return []
  }
}

export function writeRecentProject(dir: string) {
  try {
    const prev = readRecentProjects().filter((d) => d !== dir)
    const next = [dir, ...prev].slice(0, 12)
    localStorage.setItem(RECENT_PROJECTS_KEY, JSON.stringify(next))
    localStorage.setItem('talos.recentProject', dir)
  } catch {
    /* ignore */
  }
}

export function relativeTime(iso: string, now = Date.now()): string {
  const t = Date.parse(iso)
  if (!Number.isFinite(t)) return ''
  const sec = Math.round((now - t) / 1000)
  if (sec < 45) return 'just now'
  if (sec < 3600) return `${Math.floor(sec / 60)}m`
  if (sec < 86400) return `${Math.floor(sec / 3600)}h`
  if (sec < 86400 * 14) return `${Math.floor(sec / 86400)}d`
  return new Date(t).toLocaleDateString(undefined, { month: 'short', day: 'numeric' })
}

export function approvalCount(sessions: Map<string, SessionInfo>): number {
  let n = 0
  for (const s of sessions.values()) {
    if (s.state === 'awaiting_approval') n++
  }
  return n
}
