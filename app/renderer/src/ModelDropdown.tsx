/**
 * ModelDropdown — listModels grouped by provider, search, switchModel.
 */

import React, { useEffect, useMemo, useRef, useState } from 'react'
import type { Engine } from './engine'
import type { ModelEntry, ModelsResult } from './protocol'
import { EngineRPC } from './protocol'

let modelsCache: ModelEntry[] | null = null

type Props = {
  engine: Engine | null
  sessionId: string
  provider: string
  model: string
  open: boolean
  onOpenChange: (open: boolean) => void
}

export function ModelDropdown({
  engine,
  sessionId,
  provider,
  model,
  open,
  onOpenChange,
}: Props) {
  const [models, setModels] = useState<ModelEntry[]>(modelsCache ?? [])
  const [query, setQuery] = useState('')
  const [loading, setLoading] = useState(false)
  const [err, setErr] = useState('')
  const searchRef = useRef<HTMLInputElement>(null)

  useEffect(() => {
    if (!open || !engine) return
    setQuery('')
    setErr('')
    if (modelsCache) {
      setModels(modelsCache)
      return
    }
    setLoading(true)
    void engine
      .request(EngineRPC.ListModels, undefined, sessionId)
      .then((raw) => {
        const list = ((raw as ModelsResult)?.models ?? []) as ModelEntry[]
        modelsCache = list
        setModels(list)
      })
      .catch((e) => setErr(e instanceof Error ? e.message : String(e)))
      .finally(() => setLoading(false))
  }, [open, engine, sessionId])

  useEffect(() => {
    if (open) searchRef.current?.focus()
  }, [open])

  const filtered = useMemo(() => {
    const q = query.trim().toLowerCase()
    if (!q) return models
    return models.filter((m) => `${m.Provider}/${m.ID}`.toLowerCase().includes(q))
  }, [models, query])

  const grouped = useMemo(() => {
    const map = new Map<string, ModelEntry[]>()
    for (const m of filtered) {
      const list = map.get(m.Provider) ?? []
      list.push(m)
      map.set(m.Provider, list)
    }
    return [...map.entries()]
  }, [filtered])

  const label = provider && model ? `${provider}/${model}` : model || 'model'

  const pick = (m: ModelEntry) => {
    if (!engine) return
    onOpenChange(false)
    void engine
      .request(EngineRPC.SwitchModel, { provider: m.Provider, model: m.ID }, sessionId)
      .catch((e) => setErr(e instanceof Error ? e.message : String(e)))
  }

  return (
    <div className="composer-dd">
      <button
        type="button"
        className={`composer-chip ${open ? 'open' : ''}`}
        onClick={() => onOpenChange(!open)}
        title="Switch model"
      >
        <span className="chip-label">{label}</span>
        <span className="chip-caret" aria-hidden>
          ▾
        </span>
      </button>
      {open && (
        <div className="composer-menu model-menu" role="listbox">
          <input
            ref={searchRef}
            className="composer-menu-search"
            placeholder="Search models…"
            value={query}
            onChange={(e) => setQuery(e.target.value)}
            onKeyDown={(e) => {
              if (e.key === 'Escape') {
                e.stopPropagation()
                onOpenChange(false)
              }
            }}
          />
          <div className="composer-menu-scroll">
            {loading && <div className="composer-menu-empty">loading…</div>}
            {err && <div className="composer-menu-empty err">{err}</div>}
            {!loading && !err && grouped.length === 0 && (
              <div className="composer-menu-empty">no models</div>
            )}
            {grouped.map(([prov, entries]) => (
              <div key={prov} className="model-group">
                <div className="model-group-label">{prov}</div>
                {entries.map((m) => {
                  const active = m.Provider === provider && m.ID === model
                  return (
                    <button
                      key={`${m.Provider}/${m.ID}`}
                      type="button"
                      className={`composer-menu-item ${active ? 'active' : ''}`}
                      onClick={() => pick(m)}
                    >
                      {m.ID}
                    </button>
                  )
                })}
              </div>
            ))}
          </div>
          <button
            type="button"
            className="composer-menu-footer"
            onClick={() => {
              modelsCache = null
              setLoading(true)
              void engine
                ?.request(EngineRPC.ListModels, undefined, sessionId)
                .then((raw) => {
                  const list = ((raw as ModelsResult)?.models ?? []) as ModelEntry[]
                  modelsCache = list
                  setModels(list)
                })
                .finally(() => setLoading(false))
            }}
          >
            refresh
          </button>
        </div>
      )}
    </div>
  )
}
