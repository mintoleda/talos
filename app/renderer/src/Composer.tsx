/**
 * Composer — multiline input with @-mentions, slash palette, steer queue,
 * model / permission / thinking chips.
 */

import React, { useCallback, useEffect, useMemo, useRef, useState } from 'react'
import type { Engine } from './engine'
import type { CommandDesc, ListCommandsResult, ListFilesResult, SessionInfo } from './protocol'
import { EngineRPC } from './protocol'
import { debounce, insertAtCursor, mentionAtCursor, slashQuery } from './composerUtils'
import { ModelDropdown } from './ModelDropdown'
import { PermissionDropdown } from './PermissionDropdown'

const LOCAL_COMMANDS: CommandDesc[] = [
  { name: '/new', summary: 'Create a new agent session', local: true },
  { name: '/sessions', summary: 'Focus the session sidebar', local: true },
]

export type ComposerProps = {
  busy: boolean
  session: SessionInfo | null
  provider: string
  model: string
  thinkingLevel: string
  permissionMode: string
  engine: Engine | null
  sessionId: string
  onSubmit: (text: string) => void
  onSteer: (text: string) => void
  onWithdrawSteer: () => void
  onInterrupt: () => void
  onLocalCommand: (name: string) => void
  /** Increment / change to clear local steer chips (e.g. on TurnEnded). */
  steerClearSignal?: number
}

type PopoverKind = 'none' | 'mention' | 'slash' | 'model' | 'permission'

export function Composer({
  busy,
  session,
  provider,
  model,
  thinkingLevel,
  permissionMode,
  engine,
  sessionId,
  onSubmit,
  onSteer,
  onWithdrawSteer,
  onInterrupt,
  onLocalCommand,
  steerClearSignal = 0,
}: ComposerProps) {
  const [value, setValue] = useState('')
  const [steers, setSteers] = useState<string[]>([])
  const [popover, setPopover] = useState<PopoverKind>('none')
  const [files, setFiles] = useState<string[]>([])
  const [commands, setCommands] = useState<CommandDesc[] | null>(null)
  const [highlight, setHighlight] = useState(0)
  const inputRef = useRef<HTMLTextAreaElement>(null)
  const rootRef = useRef<HTMLDivElement>(null)
  const cursorRef = useRef(0)

  useEffect(() => {
    if (!busy) inputRef.current?.focus()
  }, [busy])

  useEffect(() => {
    setSteers([])
  }, [steerClearSignal, sessionId])

  const loadCommands = useCallback(async () => {
    if (commands || !engine) return commands ?? LOCAL_COMMANDS
    try {
      const raw = (await engine.request(EngineRPC.ListCommands, undefined, sessionId)) as ListCommandsResult
      const server = (raw?.commands ?? []).map((c) => ({ ...c, local: false }))
      const merged = [...LOCAL_COMMANDS, ...server]
      setCommands(merged)
      return merged
    } catch {
      setCommands(LOCAL_COMMANDS)
      return LOCAL_COMMANDS
    }
  }, [commands, engine, sessionId])

  const fetchFiles = useMemo(
    () =>
      debounce((prefix: string) => {
        if (!engine) return
        void engine
          .request(EngineRPC.ListFiles, { prefix }, sessionId)
          .then((raw) => {
            setFiles((raw as ListFilesResult)?.files ?? [])
            setHighlight(0)
          })
          .catch(() => setFiles([]))
      }, 80),
    [engine, sessionId],
  )

  useEffect(() => () => fetchFiles.cancel(), [fetchFiles])

  const syncPopovers = useCallback(
    (text: string, cursor: number) => {
      const mention = mentionAtCursor(text, cursor)
      if (mention) {
        setPopover('mention')
        fetchFiles(mention.query)
        return
      }
      const sq = slashQuery(text)
      if (sq !== null) {
        setPopover('slash')
        void loadCommands()
        setHighlight(0)
        return
      }
      setPopover((p) => (p === 'mention' || p === 'slash' ? 'none' : p))
    },
    [fetchFiles, loadCommands],
  )

  const filteredCommands = useMemo(() => {
    const list = commands ?? LOCAL_COMMANDS
    const sq = slashQuery(value)
    if (sq === null) return list
    const q = sq.toLowerCase()
    return list.filter((c) => c.name.toLowerCase().startsWith(q) || c.name.toLowerCase().includes(q.slice(1)))
  }, [commands, value])

  const mentionItems = files
  const activeItems =
    popover === 'mention' ? mentionItems : popover === 'slash' ? filteredCommands.map((c) => c.name) : []

  useEffect(() => {
    if (highlight >= activeItems.length) setHighlight(0)
  }, [activeItems.length, highlight])

  const setCursor = (pos: number) => {
    cursorRef.current = pos
    requestAnimationFrame(() => {
      const el = inputRef.current
      if (!el) return
      el.selectionStart = pos
      el.selectionEnd = pos
    })
  }

  const insertMention = (path: string) => {
    const el = inputRef.current
    const cursor = el ? el.selectionStart : cursorRef.current
    const mention = mentionAtCursor(value, cursor)
    if (!mention) return
    const { value: next, cursor: c } = insertAtCursor(value, mention.start, cursor, `@${path} `)
    setValue(next)
    setCursor(c)
    setPopover('none')
    setFiles([])
    inputRef.current?.focus()
  }

  const applySlash = (cmd: CommandDesc) => {
    if (cmd.local) {
      setValue('')
      setPopover('none')
      onLocalCommand(cmd.name)
      return
    }
    const fill = cmd.args ? `${cmd.name} ` : cmd.name
    setValue(fill)
    setCursor(fill.length)
    setPopover('none')
    inputRef.current?.focus()
  }

  const sendOrSteer = () => {
    const text = value.trim()
    if (!text) return
    const sq = slashQuery(value)
    if (sq !== null && popover === 'slash' && filteredCommands.length > 0) {
      const cmd = filteredCommands[highlight] ?? filteredCommands[0]
      if (cmd && sq !== cmd.name && !sq.startsWith(cmd.name + ' ')) {
        applySlash(cmd)
        return
      }
      if (cmd?.local && (sq === cmd.name || sq.startsWith(cmd.name + ' '))) {
        setValue('')
        setPopover('none')
        onLocalCommand(cmd.name)
        return
      }
    }
    if (busy) {
      onSteer(text)
      setSteers((s) => [...s, text])
      setValue('')
      setPopover('none')
      return
    }
    onSubmit(text)
    setValue('')
    setPopover('none')
  }

  const withdrawLast = () => {
    if (steers.length === 0) return
    setSteers((s) => s.slice(0, -1))
    onWithdrawSteer()
  }

  const closePopover = () => setPopover('none')

  const handleKeyDown = (e: React.KeyboardEvent<HTMLTextAreaElement>) => {
    if (popover === 'mention' || popover === 'slash') {
      if (e.key === 'ArrowDown') {
        e.preventDefault()
        setHighlight((h) => (activeItems.length ? (h + 1) % activeItems.length : 0))
        return
      }
      if (e.key === 'ArrowUp') {
        e.preventDefault()
        setHighlight((h) =>
          activeItems.length ? (h - 1 + activeItems.length) % activeItems.length : 0,
        )
        return
      }
      if (e.key === 'Tab' || (e.key === 'Enter' && !e.shiftKey && activeItems.length > 0)) {
        if (popover === 'mention' && mentionItems[highlight]) {
          e.preventDefault()
          insertMention(mentionItems[highlight])
          return
        }
        if (popover === 'slash' && filteredCommands[highlight]) {
          e.preventDefault()
          const cmd = filteredCommands[highlight]
          if (e.key === 'Enter' && slashQuery(value) === cmd.name) {
            // complete command line — fall through to submit
          } else {
            applySlash(cmd)
            return
          }
        }
      }
      if (e.key === 'Escape') {
        e.preventDefault()
        closePopover()
        return
      }
    }

    if (e.key === 'Escape') {
      e.preventDefault()
      if (popover !== 'none') {
        closePopover()
        return
      }
      if (busy) onInterrupt()
      return
    }

    if (e.key === 'Backspace' && value === '' && steers.length > 0) {
      e.preventDefault()
      withdrawLast()
      return
    }

    if (e.key === 'Enter' && !e.shiftKey) {
      e.preventDefault()
      sendOrSteer()
    }
  }

  const onChange = (e: React.ChangeEvent<HTMLTextAreaElement>) => {
    const text = e.target.value
    const cursor = e.target.selectionStart
    cursorRef.current = cursor
    setValue(text)
    if (popover === 'model' || popover === 'permission') {
      // keep chip menus until click-away
    } else {
      syncPopovers(text, cursor)
    }
  }

  useEffect(() => {
    const onDoc = (ev: MouseEvent) => {
      if (!rootRef.current?.contains(ev.target as Node)) {
        setPopover('none')
      }
    }
    document.addEventListener('mousedown', onDoc)
    return () => document.removeEventListener('mousedown', onDoc)
  }, [])

  const branchLabel = (() => {
    if (!session?.branch) return null
    let s = session.branch
    if ((session.ahead ?? 0) > 0) s += ` ↑${session.ahead}`
    if (session.dirty) s += ' •'
    return s
  })()

  const cycleThinking = () => {
    if (!engine) return
    void engine.request(EngineRPC.CycleThinking, undefined, sessionId)
  }

  const thinkingLabel = thinkingLevel || 'think'

  return (
    <div className="composer" ref={rootRef}>
      <div className="composer-main">
        <div className="composer-input-wrap">
          <textarea
            ref={inputRef}
            className="composer-input"
            value={value}
            onChange={onChange}
            onKeyDown={handleKeyDown}
            onClick={(e) => {
              const t = e.currentTarget
              cursorRef.current = t.selectionStart
              if (popover !== 'model' && popover !== 'permission') {
                syncPopovers(t.value, t.selectionStart)
              }
            }}
            onSelect={(e) => {
              cursorRef.current = e.currentTarget.selectionStart
            }}
            placeholder="Use / for commands, @ for context…"
            rows={2}
          />
          {popover === 'mention' && mentionItems.length > 0 && (
            <div className="composer-palette" role="listbox">
              {mentionItems.map((f, i) => (
                <button
                  key={f}
                  type="button"
                  className={`composer-menu-item ${i === highlight ? 'active' : ''}`}
                  onMouseDown={(ev) => {
                    ev.preventDefault()
                    insertMention(f)
                  }}
                >
                  {f}
                </button>
              ))}
            </div>
          )}
          {popover === 'slash' && filteredCommands.length > 0 && (
            <div className="composer-palette" role="listbox">
              {filteredCommands.map((c, i) => (
                <button
                  key={c.name}
                  type="button"
                  className={`composer-menu-item ${i === highlight ? 'active' : ''}`}
                  onMouseDown={(ev) => {
                    ev.preventDefault()
                    applySlash(c)
                  }}
                >
                  <span className="slash-name">
                    {c.name}
                    {c.local ? <span className="slash-local"> local</span> : null}
                  </span>
                  <span className="slash-summary">{c.summary}</span>
                </button>
              ))}
            </div>
          )}
        </div>

        {steers.length > 0 && (
          <div className="steer-chips">
            {steers.map((s, i) => (
              <span key={`${i}-${s.slice(0, 12)}`} className="steer-chip">
                <span className="steer-tag">steer</span>
                <span className="steer-text">{s}</span>
                {i === steers.length - 1 && (
                  <button
                    type="button"
                    className="steer-x"
                    aria-label="Withdraw steer"
                    onClick={withdrawLast}
                  >
                    ×
                  </button>
                )}
              </span>
            ))}
          </div>
        )}

        <div className="composer-toolbar">
          <div className="composer-chips-left">
            {branchLabel && (
              <span className="composer-chip branch-chip" title={session?.dir}>
                ⎇ {branchLabel}
              </span>
            )}
          </div>
          <div className="composer-chips-right">
            <ModelDropdown
              engine={engine}
              sessionId={sessionId}
              provider={provider}
              model={model}
              open={popover === 'model'}
              onOpenChange={(o) => setPopover(o ? 'model' : 'none')}
            />
            <PermissionDropdown
              engine={engine}
              sessionId={sessionId}
              permissionMode={permissionMode}
              open={popover === 'permission'}
              onOpenChange={(o) => setPopover(o ? 'permission' : 'none')}
            />
            <button
              type="button"
              className="composer-chip think-chip"
              onClick={cycleThinking}
              title="Cycle thinking level"
            >
              {thinkingLabel}
            </button>
            {busy ? (
              <button type="button" className="btn btn-interrupt composer-send" onClick={onInterrupt}>
                ■
              </button>
            ) : (
              <button
                type="button"
                className="btn btn-submit composer-send"
                onClick={sendOrSteer}
                disabled={!value.trim()}
              >
                ⏵
              </button>
            )}
          </div>
        </div>
      </div>
    </div>
  )
}
