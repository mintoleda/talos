/**
 * Pure helpers for the composer (insert-at-cursor, mention/slash detection).
 */

export function insertAtCursor(
  value: string,
  start: number,
  end: number,
  insertion: string,
): { value: string; cursor: number } {
  const s = Math.max(0, Math.min(start, value.length))
  const e = Math.max(s, Math.min(end, value.length))
  const next = value.slice(0, s) + insertion + value.slice(e)
  return { value: next, cursor: s + insertion.length }
}

/** Find @mention query at cursor: returns start index of '@' and query after it. */
export function mentionAtCursor(
  value: string,
  cursor: number,
): { start: number; query: string } | null {
  const before = value.slice(0, cursor)
  const at = before.lastIndexOf('@')
  if (at < 0) return null
  if (at > 0) {
    const prev = before[at - 1]
    if (prev && !/\s/.test(prev)) return null
  }
  const query = before.slice(at + 1)
  if (/\s/.test(query)) return null
  return { start: at, query }
}

/** Slash palette when input (trimmed left) starts with '/' and has no newline. */
export function slashQuery(value: string): string | null {
  if (value.includes('\n')) return null
  const trimmedStart = value.match(/^\s*/)?.[0].length ?? 0
  if (value[trimmedStart] !== '/') return null
  return value.slice(trimmedStart)
}

export function debounce<T extends (...args: never[]) => void>(fn: T, ms: number) {
  let t: ReturnType<typeof setTimeout> | null = null
  const wrapped = (...args: Parameters<T>) => {
    if (t) clearTimeout(t)
    t = setTimeout(() => {
      t = null
      fn(...args)
    }, ms)
  }
  wrapped.cancel = () => {
    if (t) clearTimeout(t)
    t = null
  }
  return wrapped
}
