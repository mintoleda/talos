import { describe, expect, it } from 'vitest'
import { parseDiscovery, versionsMismatch } from './daemon'

describe('parseDiscovery', () => {
  it('parses a valid daemon.json', () => {
    const raw = JSON.stringify({
      pid: 1234,
      socket: '/tmp/talos.sock',
      ws: 'ws://127.0.0.1:9876/ws',
      token: 'abc',
      version: '0.2.0',
      started_at: '2026-01-01T00:00:00Z',
    })
    expect(parseDiscovery(raw)).toEqual({
      pid: 1234,
      socket: '/tmp/talos.sock',
      ws: 'ws://127.0.0.1:9876/ws',
      token: 'abc',
      version: '0.2.0',
      started_at: '2026-01-01T00:00:00Z',
    })
  })

  it('returns null for malformed JSON', () => {
    expect(parseDiscovery('{')).toBeNull()
  })

  it('returns null when ws or token missing', () => {
    expect(parseDiscovery(JSON.stringify({ pid: 1, token: 't' }))).toBeNull()
    expect(parseDiscovery(JSON.stringify({ ws: 'ws://x', pid: 1 }))).toBeNull()
  })
})

describe('versionsMismatch', () => {
  it('detects differing non-empty versions', () => {
    expect(versionsMismatch('0.2.0', '0.3.0')).toBe(true)
    expect(versionsMismatch('0.2.0', '0.2.0')).toBe(false)
  })

  it('ignores empty sides', () => {
    expect(versionsMismatch('', '0.2.0')).toBe(false)
    expect(versionsMismatch('0.2.0', '')).toBe(false)
  })
})
