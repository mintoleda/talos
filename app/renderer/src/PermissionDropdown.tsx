/**
 * PermissionDropdown — auto/ask + panic toggle via setPermissionMode / togglePanic.
 */

import React from 'react'
import type { Engine } from './engine'
import { EngineRPC } from './protocol'

const MODES = [
  { id: 'auto', label: 'auto' },
  { id: 'ask', label: 'ask' },
] as const

type Props = {
  engine: Engine | null
  sessionId: string
  permissionMode: string
  open: boolean
  onOpenChange: (open: boolean) => void
}

export function PermissionDropdown({
  engine,
  sessionId,
  permissionMode,
  open,
  onOpenChange,
}: Props) {
  const isPanic = permissionMode === 'panic'
  const label = isPanic ? 'panic' : permissionMode || 'auto'

  const setMode = (mode: string) => {
    if (!engine) return
    onOpenChange(false)
    void engine.request(EngineRPC.SetPermissionMode, { mode }, sessionId)
  }

  const togglePanic = () => {
    if (!engine) return
    void engine.request(EngineRPC.TogglePanic, undefined, sessionId)
  }

  return (
    <div className="composer-dd">
      <button
        type="button"
        className={`composer-chip perm-chip ${isPanic ? 'panic' : ''} ${open ? 'open' : ''}`}
        onClick={() => onOpenChange(!open)}
        title="Permission mode"
      >
        <span className="chip-label">{label}</span>
        <span className="chip-caret" aria-hidden>
          ▾
        </span>
      </button>
      {open && (
        <div className="composer-menu perm-menu" role="listbox">
          {MODES.map((m) => (
            <button
              key={m.id}
              type="button"
              className={`composer-menu-item ${!isPanic && permissionMode === m.id ? 'active' : ''}`}
              onClick={() => setMode(m.id)}
            >
              {m.label}
            </button>
          ))}
          <div className="composer-menu-sep" />
          <button
            type="button"
            className={`composer-menu-item panic-item ${isPanic ? 'active' : ''}`}
            onClick={() => {
              togglePanic()
              onOpenChange(false)
            }}
          >
            {isPanic ? 'exit panic' : 'panic'}
          </button>
        </div>
      )}
    </div>
  )
}
