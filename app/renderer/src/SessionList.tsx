/**
 * SessionList — sessions grouped by project_dir basename.
 */

import React, { useState } from 'react'
import type { SessionInfo } from './protocol'
import { groupSessionsByProject } from './appState'
import { SessionRow } from './SessionRow'

export type SessionListProps = {
  sessions: SessionInfo[]
  focusedID: string | null
  onFocus: (id: string) => void
  onStop: (id: string) => void
  onDelete: (id: string) => void
  onReveal: (dir: string) => void
  onMerge?: (id: string) => void
}

export function SessionList({
  sessions,
  focusedID,
  onFocus,
  onStop,
  onDelete,
  onReveal,
  onMerge,
}: SessionListProps) {
  const groups = groupSessionsByProject(sessions)
  const [expanded, setExpanded] = useState<Set<string>>(new Set())

  if (groups.length === 0) {
    return <div className="session-list-empty">No sessions yet</div>
  }

  return (
    <div className="session-list">
      {groups.map((g) => {
        const isExpanded = expanded.has(g.projectDir)
        const showAll = isExpanded || g.sessions.length <= 5
        const visible = showAll ? g.sessions : g.sessions.slice(0, 5)
        const remaining = g.sessions.length - visible.length
        return (
          <section key={g.projectDir} className="session-group">
            <button
              type="button"
              className="session-group-header"
              title={g.projectDir}
              onClick={() => {
                setExpanded((prev) => {
                  const next = new Set(prev)
                  if (next.has(g.projectDir)) {
                    next.delete(g.projectDir)
                  } else {
                    next.add(g.projectDir)
                  }
                  return next
                })
              }}
            >
              <span
                className={`session-group-chevron${isExpanded ? ' expanded' : ''}`}
                aria-hidden
              />
              {g.label}
            </button>
            <ul className="session-group-list">
              {visible.map((s) => (
                <li key={s.id}>
                  <SessionRow
                    session={s}
                    selected={s.id === focusedID}
                    onSelect={() => onFocus(s.id)}
                    onStop={() => onStop(s.id)}
                    onDelete={() => onDelete(s.id)}
                    onReveal={() => onReveal(s.dir || s.project_dir)}
                    onMerge={onMerge ? () => onMerge(s.id) : undefined}
                  />
                </li>
              ))}
              {remaining > 0 && (
                <li>
                  <button
                    type="button"
                    className="session-group-more"
                    onClick={() => {
                      setExpanded((prev) => {
                        const next = new Set(prev)
                        next.add(g.projectDir)
                        return next
                      })
                    }}
                  >
                    … +{remaining} more
                  </button>
                </li>
              )}
            </ul>
          </section>
        )
      })}
    </div>
  )
}
