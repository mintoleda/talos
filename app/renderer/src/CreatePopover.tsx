/**
 * CreatePopover — new agent: project, isolation, optional model.
 */

import React, { useEffect, useState } from 'react'
import type { Engine } from './engine'
import { DaemonRPC, type ProbeDirResult } from './protocol'
import { readRecentProjects, writeRecentProject } from './appState'

export type CreatePopoverProps = {
  engine: Engine | null
  onCreated: (sessionID: string) => void
  onClose: () => void
}

export function CreatePopover({ engine, onCreated, onClose }: CreatePopoverProps) {
  const recent = readRecentProjects()
  const [dir, setDir] = useState(recent[0] ?? '')
  const [isolation, setIsolation] = useState<'worktree' | 'none'>('worktree')
  const [model, setModel] = useState('')
  const [isRepo, setIsRepo] = useState<boolean | null>(null)
  const [probing, setProbing] = useState(false)
  const [submitting, setSubmitting] = useState(false)
  const [error, setError] = useState('')

  useEffect(() => {
    const onKey = (e: KeyboardEvent) => {
      if (e.key === 'Escape') onClose()
    }
    window.addEventListener('keydown', onKey)
    return () => window.removeEventListener('keydown', onKey)
  }, [onClose])

  useEffect(() => {
    if (!dir || !engine) {
      setIsRepo(null)
      return
    }
    let cancelled = false
    setProbing(true)
    engine
      .request(DaemonRPC.ProbeDir, { dir })
      .then((raw) => {
        if (cancelled) return
        const r = raw as ProbeDirResult
        setIsRepo(!!r.is_repo)
        if (!r.is_repo) setIsolation('none')
        // Prefer repo root without retriggering probe loops on equal paths.
        if (r.is_repo && r.project_dir && r.project_dir !== dir) {
          setDir(r.project_dir)
        }
      })
      .catch(() => {
        if (!cancelled) setIsRepo(null)
      })
      .finally(() => {
        if (!cancelled) setProbing(false)
      })
    return () => {
      cancelled = true
    }
  }, [dir, engine])

  const browse = async () => {
    if (!window.talos) return
    const picked = await window.talos.pickDirectory()
    if (picked) setDir(picked)
  }

  const submit = async () => {
    if (!engine || !dir.trim() || submitting) return
    setSubmitting(true)
    setError('')
    try {
      writeRecentProject(dir.trim())
      const params: Record<string, string> = {
        dir: dir.trim(),
        isolation: isRepo === false ? 'none' : isolation,
      }
      if (model.trim()) params.model = model.trim()
      const created = (await engine.request(DaemonRPC.CreateSession, params)) as {
        session?: { id?: string }
      }
      const id = created?.session?.id
      if (!id) throw new Error('failed to create session')
      onCreated(id)
      onClose()
    } catch (e) {
      setError(e instanceof Error ? e.message : String(e))
    } finally {
      setSubmitting(false)
    }
  }

  const worktreeDisabled = isRepo === false || probing

  return (
    <div className="popover-backdrop" role="presentation" onClick={onClose}>
      <div
        className="create-popover"
        role="dialog"
        aria-labelledby="create-title"
        onClick={(e) => e.stopPropagation()}
      >
        <h2 id="create-title" className="create-title">
          New agent
        </h2>

        <label className="field-label" htmlFor="create-dir">
          Project
        </label>
        <div className="create-dir-row">
          <select
            id="create-dir"
            className="create-select"
            value={recent.includes(dir) ? dir : ''}
            onChange={(e) => {
              if (e.target.value) setDir(e.target.value)
            }}
          >
            <option value="" disabled>
              {dir ? dir : 'Select recent…'}
            </option>
            {recent.map((p) => (
              <option key={p} value={p}>
                {p}
              </option>
            ))}
          </select>
          {window.talos && (
            <button type="button" className="btn btn-ghost" onClick={() => void browse()}>
              Browse
            </button>
          )}
        </div>
        {dir && (
          <input
            className="create-input"
            value={dir}
            onChange={(e) => setDir(e.target.value)}
            onKeyDown={(e) => {
              if (e.key === 'Enter') void submit()
            }}
            spellCheck={false}
          />
        )}

        <fieldset className="create-isolation">
          <legend className="field-label">Isolation</legend>
          <label className="radio-row">
            <input
              type="radio"
              name="isolation"
              checked={isolation === 'worktree'}
              disabled={worktreeDisabled}
              onChange={() => setIsolation('worktree')}
            />
            Worktree
            {isRepo === false && <span className="field-hint"> — not a git repo</span>}
          </label>
          <label className="radio-row">
            <input
              type="radio"
              name="isolation"
              checked={isolation === 'none'}
              onChange={() => setIsolation('none')}
            />
            None (project dir)
          </label>
        </fieldset>

        <label className="field-label" htmlFor="create-model">
          Model <span className="field-optional">(optional)</span>
        </label>
        <input
          id="create-model"
          className="create-input"
          placeholder="provider default"
          value={model}
          onChange={(e) => setModel(e.target.value)}
          onKeyDown={(e) => {
            if (e.key === 'Enter') void submit()
          }}
        />

        {error && <p className="create-error">{error}</p>}

        <div className="create-actions">
          <button type="button" className="btn btn-ghost" onClick={onClose}>
            Cancel
          </button>
          <button
            type="button"
            className="btn btn-submit"
            disabled={!dir.trim() || submitting || !engine}
            onClick={() => void submit()}
          >
            {submitting ? 'Creating…' : 'Create'}
          </button>
        </div>
      </div>
    </div>
  )
}
