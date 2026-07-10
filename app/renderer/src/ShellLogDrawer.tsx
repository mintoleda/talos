/**
 * ShellLogDrawer — full log view for one background process.
 */

import React, { useEffect, useRef, useState } from 'react'
import type { BgProcState } from './state'

export type ShellLogDrawerProps = {
  proc: BgProcState
  text: string
  onClose: () => void
  onKill: () => void
  onDismiss: () => void
}

export function ShellLogDrawer({
  proc,
  text,
  onClose,
  onKill,
  onDismiss,
}: ShellLogDrawerProps) {
  const preRef = useRef<HTMLPreElement>(null)
  const [stick, setStick] = useState(true)

  useEffect(() => {
    if (!stick || !preRef.current) return
    preRef.current.scrollTop = preRef.current.scrollHeight
  }, [text, stick])

  const onScroll = () => {
    const el = preRef.current
    if (!el) return
    const atBottom = el.scrollHeight - el.scrollTop - el.clientHeight < 24
    setStick(atBottom)
  }

  return (
    <div className="shell-log-drawer">
      <div className="shell-log-header">
        <div className="shell-log-title">
          <span className="shell-log-id">{proc.id}</span>
          <span className="shell-log-cmd mono" title={proc.command}>
            {proc.command}
          </span>
          {!proc.running && (
            <span className="shell-log-status">exit {proc.exitCode}</span>
          )}
        </div>
        <div className="shell-log-actions">
          <label className="shell-log-stick">
            <input
              type="checkbox"
              checked={stick}
              onChange={(e) => setStick(e.target.checked)}
            />
            stick
          </label>
          {proc.running ? (
            <button type="button" className="shell-session-btn" onClick={onKill}>
              Kill
            </button>
          ) : (
            <button type="button" className="shell-session-btn" onClick={onDismiss}>
              Dismiss
            </button>
          )}
          <button type="button" className="shell-session-btn" onClick={onClose}>
            Close
          </button>
        </div>
      </div>
      <pre
        ref={preRef}
        className="shell-log-body mono"
        onScroll={onScroll}
      >
        {text || '…'}
      </pre>
    </div>
  )
}
