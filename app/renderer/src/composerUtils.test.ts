import { describe, expect, it, vi } from 'vitest'
import { debounce, insertAtCursor, mentionAtCursor, slashQuery } from './composerUtils'

describe('insertAtCursor', () => {
  it('inserts at cursor', () => {
    expect(insertAtCursor('hello', 5, 5, ' world')).toEqual({
      value: 'hello world',
      cursor: 11,
    })
  })

  it('replaces selection', () => {
    expect(insertAtCursor('abXXXcd', 2, 5, 'Y')).toEqual({
      value: 'abYcd',
      cursor: 3,
    })
  })
})

describe('mentionAtCursor', () => {
  it('detects @query', () => {
    expect(mentionAtCursor('see @foo', 8)).toEqual({ start: 4, query: 'foo' })
  })

  it('ignores mid-word @', () => {
    expect(mentionAtCursor('email@x', 7)).toBeNull()
  })

  it('stops at whitespace', () => {
    expect(mentionAtCursor('@a b', 4)).toBeNull()
  })
})

describe('slashQuery', () => {
  it('returns slash line', () => {
    expect(slashQuery('/mod')).toBe('/mod')
    expect(slashQuery('  /new')).toBe('/new')
  })

  it('rejects multiline or non-slash', () => {
    expect(slashQuery('hi')).toBeNull()
    expect(slashQuery('/a\nb')).toBeNull()
  })
})

describe('debounce', () => {
  it('delays calls', () => {
    vi.useFakeTimers()
    const fn = vi.fn()
    const d = debounce(fn, 80)
    d()
    d()
    expect(fn).not.toHaveBeenCalled()
    vi.advanceTimersByTime(80)
    expect(fn).toHaveBeenCalledTimes(1)
    vi.useRealTimers()
  })
})
