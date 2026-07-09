/**
 * SessionList — sessions grouped by project_dir basename.
 */

import React from 'react'
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
}

export function SessionList({
  sessions,
  focusedID,
  onFocus,
  onStop,
  onDelete,
  onReveal,
}: SessionListProps) {
  const groups = groupSessionsByProject(sessions)

  if (groups.length === 0) {
    return <div className="session-list-empty">No sessions yet</div>
  }

  return (
    <div className="session-list">
      {groups.map((g) => (
        <section key={g.projectDir} className="session-group">
          <h3 className="session-group-label" title={g.projectDir}>
            {g.label}
          </h3>
          <ul className="session-group-list">
            {g.sessions.map((s) => (
              <li key={s.id}>
                <SessionRow
                  session={s}
                  selected={s.id === focusedID}
                  onSelect={() => onFocus(s.id)}
                  onStop={() => onStop(s.id)}
                  onDelete={() => onDelete(s.id)}
                  onReveal={() => onReveal(s.dir || s.project_dir)}
                />
              </li>
            ))}
          </ul>
        </section>
      ))}
    </div>
  )
}
