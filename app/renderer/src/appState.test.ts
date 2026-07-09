import { describe, expect, it } from 'vitest'
import {
  applySessionStatus,
  groupSessionsByProject,
  initialAppState,
  pickBootFocus,
  pruneTranscripts,
  sessionsFromList,
  setTranscript,
} from './appState'
import { initialState } from './state'
import type { SessionInfo } from './protocol'

function sess(partial: Partial<SessionInfo> & { id: string }): SessionInfo {
  return {
    dir: '/p',
    project_dir: '/p',
    isolation: 'none',
    state: 'idle',
    live: true,
    provider: 't',
    model: 'm',
    preview: '',
    created_at: '2026-01-01T00:00:00Z',
    last_active: '2026-01-01T00:00:00Z',
    ...partial,
  }
}

describe('appState', () => {
  it('groups by project basename and sorts by activity', () => {
    const groups = groupSessionsByProject([
      sess({ id: 'a', project_dir: '/repos/talos', last_active: '2026-01-02T00:00:00Z', preview: 'new' }),
      sess({ id: 'b', project_dir: '/repos/talos', last_active: '2026-01-01T00:00:00Z', preview: 'old' }),
      sess({ id: 'c', project_dir: '/repos/dotfiles', last_active: '2026-01-03T00:00:00Z' }),
    ])
    expect(groups[0].label).toBe('dotfiles')
    expect(groups[1].label).toBe('talos')
    expect(groups[1].sessions.map((s) => s.id)).toEqual(['a', 'b'])
  })

  it('pickBootFocus prefers stored then most recent live', () => {
    const map = sessionsFromList([
      sess({ id: 'x', live: true, last_active: '2026-01-01T00:00:00Z' }),
      sess({ id: 'y', live: true, last_active: '2026-01-02T00:00:00Z' }),
      sess({ id: 'z', live: false, state: 'unloaded', last_active: '2026-01-03T00:00:00Z' }),
    ])
    expect(pickBootFocus(map, 'x')).toBe('x')
    expect(pickBootFocus(map, 'missing')).toBe('y')
    expect(pickBootFocus(map, null)).toBe('y')
  })

  it('deleted status removes session and clears focus', () => {
    let app = initialAppState()
    app = {
      ...app,
      sessions: sessionsFromList([sess({ id: 's1' })]),
      focusedID: 's1',
    }
    app = setTranscript(app, 's1', initialState())
    app = applySessionStatus(app, { etype: 'session_status', id: 's1', state: 'deleted' })
    expect(app.sessions.has('s1')).toBe(false)
    expect(app.focusedID).toBeNull()
    expect(app.transcripts.has('s1')).toBe(false)
  })

  it('prunes LRU transcripts keeping focused', () => {
    const transcripts = new Map<string, ReturnType<typeof initialState>>()
    const order: string[] = []
    for (let i = 0; i < 12; i++) {
      const id = `s${i}`
      transcripts.set(id, initialState())
      order.push(id)
    }
    const { transcripts: next, order: nextOrder } = pruneTranscripts(transcripts, order, 's11', 10)
    expect(next.size).toBe(10)
    expect(next.has('s11')).toBe(true)
    expect(nextOrder).toContain('s11')
  })
})
