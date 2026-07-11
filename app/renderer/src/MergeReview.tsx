/**
 * MergeReview — summary → file diff → execute flow for worktree sessions.
 */

import React, { useCallback, useEffect, useState } from 'react'
import type { Engine } from './engine'
import type {
  MergeExecuteResult,
  MergeFileStat,
  MergePreviewResult,
  SessionInfo,
} from './protocol'
import { MergeRPC } from './protocol'
import { DiffView } from './DiffView'

export type MergeReviewProps = {
  session: SessionInfo
  engine: Engine
  onClose: () => void
  onMerged: () => void
  onCopyPrompt: (text: string) => void
}

type Strategy = 'squash' | 'merge' | 'ff'

function shortSha(sha: string): string {
  return sha.slice(0, 7)
}

function conflictPrompt(branch: string, base: string, files: string[]): string {
  const list = files.map((f) => `- ${f}`).join('\n')
  return [
    `The merge of \`${branch}\` into \`${base}\` has conflicts in:`,
    list,
    '',
    'Please resolve these conflicts (rebase or merge as appropriate), commit the resolution, and leave the branch ready to merge.',
  ].join('\n')
}

export function MergeReview({
  session,
  engine,
  onClose,
  onMerged,
  onCopyPrompt,
}: MergeReviewProps) {
  const [preview, setPreview] = useState<MergePreviewResult | null>(null)
  const [error, setError] = useState<string | null>(null)
  const [loading, setLoading] = useState(true)
  const [selectedPath, setSelectedPath] = useState<string | null>(null)
  const [diff, setDiff] = useState<string>('')
  const [diffLoading, setDiffLoading] = useState(false)
  const [strategy, setStrategy] = useState<Strategy>('squash')
  const [base, setBase] = useState('')
  const [message, setMessage] = useState('')
  const [cleanup, setCleanup] = useState(true)
  const [committing, setCommitting] = useState(false)
  const [executing, setExecuting] = useState(false)
  const [conflict, setConflict] = useState<MergeExecuteResult | null>(null)
  const [commitMsg, setCommitMsg] = useState('Save worktree changes')

  const loadPreview = useCallback(
    async (baseOverride?: string) => {
      setLoading(true)
      setError(null)
      setConflict(null)
      try {
        const params: { id: string; base?: string } = { id: session.id }
        if (baseOverride) params.base = baseOverride
        const raw = (await engine.request(MergeRPC.Preview, params)) as MergePreviewResult
        setPreview(raw)
        setBase(raw.base)
        setMessage((prev) => {
          if (prev) return prev
          const pref =
            (session.preview || '').trim().split('\n')[0] ||
            raw.commits[0]?.subject ||
            `Merge ${raw.branch}`
          return pref.slice(0, 72)
        })
        setStrategy((s) => (raw.can_ff === false && s === 'ff' ? 'squash' : s))
      } catch (e) {
        setError(e instanceof Error ? e.message : String(e))
      } finally {
        setLoading(false)
      }
    },
    [engine, session.id, session.preview],
  )

  useEffect(() => {
    void loadPreview()
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [session.id])

  useEffect(() => {
    const onKey = (e: KeyboardEvent) => {
      if (e.key === 'Escape') {
        if (selectedPath) setSelectedPath(null)
        else onClose()
      }
    }
    window.addEventListener('keydown', onKey)
    return () => window.removeEventListener('keydown', onKey)
  }, [onClose, selectedPath])

  const openFile = async (path: string) => {
    setSelectedPath(path)
    setDiffLoading(true)
    setDiff('')
    try {
      const raw = (await engine.request(MergeRPC.FileDiff, {
        id: session.id,
        path,
        base: base || undefined,
      })) as { unified?: string }
      setDiff(raw?.unified ?? '')
    } catch (e) {
      setDiff(`error: ${e instanceof Error ? e.message : String(e)}`)
    } finally {
      setDiffLoading(false)
    }
  }

  const commitWorktree = async () => {
    setCommitting(true)
    setError(null)
    try {
      await engine.request(MergeRPC.CommitWorktree, {
        id: session.id,
        message: commitMsg || 'Save worktree changes',
      })
      await loadPreview(base || undefined)
    } catch (e) {
      setError(e instanceof Error ? e.message : String(e))
    } finally {
      setCommitting(false)
    }
  }

  const execute = async () => {
    if (!preview) return
    setExecuting(true)
    setError(null)
    setConflict(null)
    try {
      const raw = (await engine.request(MergeRPC.Execute, {
        id: session.id,
        strategy,
        message: message || undefined,
        cleanup,
        base: base || undefined,
      })) as MergeExecuteResult
      if (raw.conflict) {
        setConflict(raw)
        return
      }
      if (raw.merged) {
        onMerged()
        onClose()
      }
    } catch (e) {
      setError(e instanceof Error ? e.message : String(e))
    } finally {
      setExecuting(false)
    }
  }

  const idle = preview?.session_state === 'idle' || preview?.session_state === 'unloaded'
  const blocked =
    !preview ||
    preview.dirty_worktree ||
    preview.dirty_main ||
    !idle ||
    preview.ahead <= 0 ||
    executing

  const fileRow = (f: MergeFileStat) => (
    <button
      key={f.path}
      type="button"
      className={`merge-file${selectedPath === f.path ? ' selected' : ''}`}
      onClick={() => void openFile(f.path)}
    >
      <span className="merge-file-status">{f.status || 'M'}</span>
      <span className="merge-file-path">{f.path}</span>
      <span className="merge-file-stats">
        <span className="add">+{f.additions}</span>
        <span className="del">−{f.deletions}</span>
      </span>
    </button>
  )

  return (
    <div className="modal-backdrop" role="presentation" onClick={onClose}>
      <div
        className="merge-review"
        role="dialog"
        aria-modal="true"
        aria-labelledby="merge-title"
        onClick={(e) => e.stopPropagation()}
      >
        <header className="merge-header">
          <div>
            <h2 id="merge-title" className="modal-title">
              Review & merge
            </h2>
            <p className="merge-sub">
              {preview ? (
                <>
                  <code>{preview.branch}</code>
                  {' → '}
                  <input
                    className="merge-base-input"
                    value={base}
                    onChange={(e) => setBase(e.target.value)}
                    onBlur={() => {
                      if (base && base !== preview.base) void loadPreview(base)
                    }}
                    onKeyDown={(e) => {
                      if (e.key === 'Enter') {
                        e.preventDefault()
                        void loadPreview(base)
                      }
                    }}
                    title="Default branch (editable)"
                    aria-label="Base branch"
                  />
                  {preview.ahead > 0 && (
                    <span className="merge-chip">⎇ {preview.ahead}↑ ready</span>
                  )}
                  {preview.behind > 0 && (
                    <span className="merge-chip warn">{preview.behind}↓ behind</span>
                  )}
                </>
              ) : (
                session.branch || session.id.slice(0, 8)
              )}
            </p>
          </div>
          <button type="button" className="btn btn-ghost" onClick={onClose}>
            Close
          </button>
        </header>

        {loading && <div className="merge-loading">Loading preview…</div>}
        {error && <div className="merge-error">{error}</div>}

        {preview && !loading && (
          <div className="merge-body">
            <section className="merge-panel">
              <h3 className="merge-section-title">Commits ({preview.commits.length})</h3>
              {preview.commits.length === 0 ? (
                <p className="merge-muted">No commits ahead of base</p>
              ) : (
                <ul className="merge-commits">
                  {preview.commits.map((c) => (
                    <li key={c.sha}>
                      <code className="merge-sha">{shortSha(c.sha)}</code>
                      <span className="merge-subject">{c.subject}</span>
                      <span className="merge-author">{c.author}</span>
                    </li>
                  ))}
                </ul>
              )}

              {preview.dirty_worktree && (
                <div className="merge-warn">
                  <p>Worktree has uncommitted changes. Commit them before merging.</p>
                  <div className="merge-warn-row">
                    <input
                      className="create-input"
                      value={commitMsg}
                      onChange={(e) => setCommitMsg(e.target.value)}
                      placeholder="Commit message"
                    />
                    <button
                      type="button"
                      className="btn btn-submit"
                      disabled={committing}
                      onClick={() => void commitWorktree()}
                    >
                      {committing ? 'Committing…' : 'Commit all'}
                    </button>
                  </div>
                </div>
              )}

              {preview.dirty_main && (
                <div className="merge-warn">
                  {(preview.dirty_main_hit?.length ?? 0) > 0 ? (
                    <p>
                      Origin checkout is dirty in files this merge touches:{' '}
                      {preview.dirty_main_hit!.join(', ')}. Merge is blocked until those are
                      cleaned up.
                    </p>
                  ) : (
                    <p>Origin checkout has uncommitted changes. Commit, stash, or discard them before merging.</p>
                  )}
                </div>
              )}

              {!idle && (
                <div className="merge-warn">
                  Session is {preview.session_state}. Wait until idle to merge.
                </div>
              )}

              <h3 className="merge-section-title">Files ({preview.files.length})</h3>
              <div className="merge-files">{preview.files.map(fileRow)}</div>
            </section>

            <section className="merge-panel merge-diff-panel">
              <h3 className="merge-section-title">
                {selectedPath ? selectedPath : 'Select a file'}
              </h3>
              {diffLoading ? (
                <div className="merge-loading">Loading diff…</div>
              ) : selectedPath ? (
                <DiffView unified={diff} />
              ) : (
                <p className="merge-muted">Click a file to view its unified diff.</p>
              )}
            </section>
          </div>
        )}

        {conflict && (
          <div className="merge-conflict">
            <h3 className="merge-section-title">Conflicts detected</h3>
            <p className="merge-muted">
              Pre-flight found conflicts. The origin checkout was not modified.
            </p>
            <ul className="merge-conflict-list">
              {(conflict.conflict_files ?? []).map((f) => (
                <li key={f}>
                  <code>{f}</code>
                </li>
              ))}
            </ul>
            <button
              type="button"
              className="btn btn-submit"
              onClick={() => {
                if (!preview) return
                onCopyPrompt(
                  conflictPrompt(preview.branch, preview.base, conflict.conflict_files ?? []),
                )
                onClose()
              }}
            >
              Copy resolution prompt
            </button>
          </div>
        )}

        {preview && !loading && (
          <footer className="merge-bar">
            <label className="merge-field">
              Strategy
              <select
                className="create-select"
                value={strategy}
                onChange={(e) => setStrategy(e.target.value as Strategy)}
              >
                <option value="squash">Squash</option>
                <option value="merge">Merge commit</option>
                <option value="ff" disabled={!preview.can_ff}>
                  Fast-forward{!preview.can_ff ? ' (unavailable)' : ''}
                </option>
              </select>
            </label>
            <label className="merge-field merge-msg">
              Message
              <input
                className="create-input"
                value={message}
                onChange={(e) => setMessage(e.target.value)}
              />
            </label>
            <label className="merge-cleanup">
              <input
                type="checkbox"
                checked={cleanup}
                onChange={(e) => setCleanup(e.target.checked)}
              />
              Clean up worktree &amp; branch
            </label>
            <button
              type="button"
              className="btn btn-submit"
              disabled={!!blocked}
              onClick={() => void execute()}
            >
              {executing ? 'Merging…' : 'Merge'}
            </button>
          </footer>
        )}
      </div>
    </div>
  )
}
