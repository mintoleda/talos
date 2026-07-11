/**
 * DiffView — hand-rolled unified diff renderer.
 */

import React from 'react'

export type DiffViewProps = {
  unified: string
}

type DiffLine = {
  kind: 'meta' | 'hunk' | 'add' | 'del' | 'ctx' | 'other'
  text: string
}

function classify(line: string): DiffLine {
  if (line.startsWith('+++') || line.startsWith('---') || line.startsWith('diff ') || line.startsWith('index ')) {
    return { kind: 'meta', text: line }
  }
  if (line.startsWith('@@')) return { kind: 'hunk', text: line }
  if (line.startsWith('+')) return { kind: 'add', text: line }
  if (line.startsWith('-')) return { kind: 'del', text: line }
  if (line.startsWith(' ') || line === '') return { kind: 'ctx', text: line }
  return { kind: 'other', text: line }
}

export function DiffView({ unified }: DiffViewProps) {
  if (!unified.trim()) {
    return <div className="diff-empty">No diff</div>
  }
  const lines = unified.replace(/\n$/, '').split('\n').map(classify)
  return (
    <pre className="diff-view" tabIndex={0}>
      {lines.map((l, i) => (
        <div key={i} className={`diff-line diff-${l.kind}`}>
          {l.text || ' '}
        </div>
      ))}
    </pre>
  )
}
