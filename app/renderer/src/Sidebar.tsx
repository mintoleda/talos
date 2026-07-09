/**
 * Sidebar — left rail: New Agent, session list, footer status.
 */

import React, { forwardRef } from 'react'
import type { SessionInfo } from './protocol'
import { SessionList } from './SessionList'

export type SidebarProps = {
  sessions: SessionInfo[]
  focusedID: string | null
  connected: boolean
  daemonLabel?: string
  onNewAgent: () => void
  onFocus: (id: string) => void
  onStop: (id: string) => void
  onDelete: (id: string) => void
  onReveal: (dir: string) => void
}

export const Sidebar = forwardRef<HTMLElement, SidebarProps>(function Sidebar(
  {
    sessions,
    focusedID,
    connected,
    daemonLabel,
    onNewAgent,
    onFocus,
    onStop,
    onDelete,
    onReveal,
  },
  ref,
) {
  return (
    <aside className="sidebar" ref={ref} tabIndex={-1}>
      <div className="sidebar-header">
        <span className="sidebar-brand">talos</span>
        <button type="button" className="new-agent-btn" onClick={onNewAgent}>
          <span className="new-agent-plus" aria-hidden>
            ＋
          </span>
          New Agent
        </button>
      </div>
      <SessionList
        sessions={sessions}
        focusedID={focusedID}
        onFocus={onFocus}
        onStop={onStop}
        onDelete={onDelete}
        onReveal={onReveal}
      />
      <footer className="sidebar-footer">
        <span className={`conn-dot ${connected ? 'on' : 'off'}`} aria-hidden />
        <span className="sidebar-footer-text">
          {connected ? daemonLabel || 'connected' : 'offline'}
        </span>
      </footer>
    </aside>
  )
})
