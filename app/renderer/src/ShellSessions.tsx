/**
 * ShellSessions — collapsed/expanded strip of background shell processes.
 */

import React from 'react'
import type { BgProcState } from './state'

export type ShellSessionsProps = {
  procs: BgProcState[]
  expanded: boolean
  onToggleExpand: () => void
  onOpenLog: (id: string) => void
  onKill: (id: string) => void
  onDismiss: (id: string) => void
}

function commandLabel(command: string): string {
  const trimmed = command.trim()
  if (!trimmed) return 'shell'
  const first = trimmed.split(/\s+/)[0] ?? trimmed
  const base = first.split('/').pop() ?? first
  return base || 'shell'
}

function lastLines(text: string, n: number): string {
  const lines = text.replace(/\s+$/, '').split('\n')
  return lines.slice(-n).join('\n')
}

function lastLine(text: string): string {
  const lines = text.replace(/\s+$/, '').split('\n')
  return lines[lines.length - 1] ?? ''
}

export function ShellSessions({
  procs,
  expanded,
  onToggleExpand,
  onOpenLog,
  onKill,
  onDismiss,
}: ShellSessionsProps) {
  if (procs.length === 0) return null

  const newest = procs[procs.length - 1]
  const preview = newest ? lastLine(newest.recentLines) : ''
  const label =
    procs.length === 1 ? '1 shell session' : `${procs.length} shell sessions`

  return (
    <div className={`shell-sessions${expanded ? ' expanded' : ''}`}>
      <button
        type="button"
        className="shell-sessions-toggle"
        onClick={onToggleExpand}
        aria-expanded={expanded}
      >
        <span className="shell-sessions-chevron" aria-hidden>
          {expanded ? '▾' : '▸'}
        </span>
        <span className="shell-sessions-count">{label}</span>
        {!expanded && preview && (
          <span className="shell-sessions-preview mono">{preview}</span>
        )}
      </button>
      {expanded && (
        <ul className="shell-sessions-list">
          {procs.map((p) => (
            <li
              key={p.id}
              className={`shell-session-row${p.running ? '' : ' exited'}`}
            >
              <button
                type="button"
                className="shell-session-main"
                onClick={() => onOpenLog(p.id)}
                title={p.command}
              >
                <span className="shell-session-name">{commandLabel(p.command)}</span>
                <span className="shell-session-tail mono">
                  {lastLines(p.recentLines, 3) || (p.running ? '…' : `exit ${p.exitCode}`)}
                </span>
                {!p.running && (
                  <span className="shell-session-exit">exit {p.exitCode}</span>
                )}
              </button>
              <div className="shell-session-actions">
                {p.running ? (
                  <button
                    type="button"
                    className="shell-session-btn"
                    title="Kill"
                    onClick={() => onKill(p.id)}
                  >
                    ×
                  </button>
                ) : (
                  <button
                    type="button"
                    className="shell-session-btn"
                    title="Dismiss"
                    onClick={() => onDismiss(p.id)}
                  >
                    ×
                  </button>
                )}
              </div>
            </li>
          ))}
        </ul>
      )}
    </div>
  )
}
