/**
 * SessionRow — one session in the sidebar list.
 */

import React, { useEffect, useRef, useState } from 'react'
import type { SessionInfo } from './protocol'
import { relativeTime } from './appState'

export type SessionRowProps = {
  session: SessionInfo
  selected: boolean
  onSelect: () => void
  onStop: () => void
  onDelete: () => void
  onReveal: () => void
}

function statusGlyph(state: string, live: boolean): { glyph: string; className: string } {
  if (state === 'busy') return { glyph: '●', className: 'glyph-busy' }
  if (state === 'awaiting_approval') return { glyph: '◐', className: 'glyph-approval' }
  if (live && (state === 'idle' || !state)) return { glyph: '○', className: 'glyph-idle' }
  return { glyph: '◌', className: 'glyph-unloaded' }
}

function branchBadge(s: SessionInfo): string | null {
  if (s.isolation !== 'worktree') return null
  const ahead = s.ahead ?? 0
  const dirty = !!s.dirty
  if (ahead <= 0 && !dirty) return null
  const parts = ['⎇']
  if (ahead > 0) parts.push(`${ahead}↑`)
  if (dirty) parts.push('±')
  return parts.join(' ')
}

function previewTitle(s: SessionInfo): string {
  const p = (s.preview || '').trim()
  if (p) return p
  return s.branch || s.id.slice(0, 8)
}

export function SessionRow({
  session,
  selected,
  onSelect,
  onStop,
  onDelete,
  onReveal,
}: SessionRowProps) {
  const { glyph, className } = statusGlyph(session.state, session.live)
  const branch = branchBadge(session)
  const [menu, setMenu] = useState<{ x: number; y: number } | null>(null)
  const rowRef = useRef<HTMLButtonElement>(null)

  useEffect(() => {
    if (!menu) return
    const close = () => setMenu(null)
    window.addEventListener('click', close)
    window.addEventListener('contextmenu', close)
    return () => {
      window.removeEventListener('click', close)
      window.removeEventListener('contextmenu', close)
    }
  }, [menu])

  return (
    <div className={`session-row-wrap${selected ? ' selected' : ''}`}>
      <button
        ref={rowRef}
        type="button"
        className="session-row"
        onClick={onSelect}
        onContextMenu={(e) => {
          e.preventDefault()
          setMenu({ x: e.clientX, y: e.clientY })
        }}
      >
        <span className={`session-glyph ${className}`} aria-hidden>
          {glyph}
        </span>
        <span className="session-row-body">
          <span className="session-row-top">
            <span className="session-preview">{previewTitle(session)}</span>
            {branch && <span className="session-branch">{branch}</span>}
          </span>
          <span className="session-row-meta">
            {relativeTime(session.last_active || session.created_at)}
          </span>
        </span>
      </button>
      <div className="session-row-actions">
        {session.live && (
          <button type="button" className="row-action" title="Stop" onClick={onStop}>
            Stop
          </button>
        )}
        <button type="button" className="row-action danger" title="Delete" onClick={onDelete}>
          Del
        </button>
      </div>
      {menu && (
        <div
          className="context-menu"
          style={{ left: menu.x, top: menu.y }}
          role="menu"
          onClick={(e) => e.stopPropagation()}
        >
          {session.live && (
            <button
              type="button"
              role="menuitem"
              onClick={() => {
                setMenu(null)
                onStop()
              }}
            >
              Stop
            </button>
          )}
          <button
            type="button"
            role="menuitem"
            onClick={() => {
              setMenu(null)
              onDelete()
            }}
          >
            Delete…
          </button>
          <button
            type="button"
            role="menuitem"
            onClick={() => {
              setMenu(null)
              onReveal()
            }}
          >
            Reveal in folder
          </button>
        </div>
      )}
    </div>
  )
}
