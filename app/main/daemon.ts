/**
 * daemon.ts — discover / spawn / health-check the talos multi-session daemon.
 *
 * Pure helpers (parseDiscovery, versionsMismatch) are unit-tested under vitest.
 * Spawn / probe paths are exercised manually via the Electron app.
 */

import { spawn, execFile } from 'node:child_process'
import { promises as fs, readFileSync } from 'node:fs'
import { homedir } from 'node:os'
import { join } from 'node:path'
import { promisify } from 'node:util'
import WebSocket from 'ws'

const execFileAsync = promisify(execFile)

export type DaemonInfo = {
  wsURL: string
  token: string
  version: string
  pid?: number
}

export type Discovery = {
  pid: number
  socket: string
  ws: string
  token: string
  version: string
  started_at?: string
}

export type AppSettings = {
  talosPath?: string
}

const POLL_MS = 250
const SPAWN_TIMEOUT_MS = 10_000
const PROBE_TIMEOUT_MS = 2_000

export function talosHome(homeDir?: string): string {
  return join(homeDir ?? homedir(), '.talos')
}

export function discoveryPath(homeDir?: string): string {
  return join(talosHome(homeDir), 'daemon.json')
}

export function appSettingsPath(homeDir?: string): string {
  return join(talosHome(homeDir), 'app.json')
}

/** Parse discovery JSON. Returns null if missing required fields. */
export function parseDiscovery(raw: string): Discovery | null {
  let data: unknown
  try {
    data = JSON.parse(raw)
  } catch {
    return null
  }
  if (!data || typeof data !== 'object') return null
  const o = data as Record<string, unknown>
  const ws = typeof o.ws === 'string' ? o.ws : ''
  const token = typeof o.token === 'string' ? o.token : ''
  const version = typeof o.version === 'string' ? o.version : ''
  const socket = typeof o.socket === 'string' ? o.socket : ''
  const pid = typeof o.pid === 'number' ? o.pid : 0
  if (!ws || !token) return null
  return {
    pid,
    socket,
    ws,
    token,
    version,
    started_at: typeof o.started_at === 'string' ? o.started_at : undefined,
  }
}

/** True when hello version differs from discovery/binary version (both non-empty). */
export function versionsMismatch(a: string, b: string): boolean {
  if (!a || !b) return false
  return a !== b
}

export function readDiscovery(homeDir?: string): Discovery | null {
  try {
    const raw = readFileSync(discoveryPath(homeDir), 'utf8')
    return parseDiscovery(raw)
  } catch {
    return null
  }
}

export async function readDiscoveryAsync(homeDir?: string): Promise<Discovery | null> {
  try {
    const raw = await fs.readFile(discoveryPath(homeDir), 'utf8')
    return parseDiscovery(raw)
  } catch {
    return null
  }
}

export async function removeDiscovery(homeDir?: string): Promise<void> {
  try {
    await fs.unlink(discoveryPath(homeDir))
  } catch {
    // missing is fine
  }
}

export async function readAppSettings(homeDir?: string): Promise<AppSettings> {
  try {
    const raw = await fs.readFile(appSettingsPath(homeDir), 'utf8')
    const data = JSON.parse(raw) as AppSettings
    return data && typeof data === 'object' ? data : {}
  } catch {
    return {}
  }
}

export async function writeAppSettings(settings: AppSettings, homeDir?: string): Promise<void> {
  const dir = talosHome(homeDir)
  await fs.mkdir(dir, { recursive: true })
  await fs.writeFile(appSettingsPath(homeDir), JSON.stringify(settings, null, 2) + '\n', {
    mode: 0o600,
  })
}

/**
 * Probe a daemon WebSocket: open, expect a hello within timeoutMs.
 * Returns the hello version on success, null on failure.
 */
export async function probeDaemon(
  wsURL: string,
  token: string,
  timeoutMs = PROBE_TIMEOUT_MS,
): Promise<{ version: string } | null> {
  return new Promise((resolve) => {
    let settled = false
    const finish = (result: { version: string } | null) => {
      if (settled) return
      settled = true
      clearTimeout(timer)
      try {
        ws.close()
      } catch {
        /* ignore */
      }
      resolve(result)
    }

    const timer = setTimeout(() => finish(null), timeoutMs)
    let ws: WebSocket
    try {
      ws = new WebSocket(wsURL)
    } catch {
      finish(null)
      return
    }

    ws.on('open', () => {
      if (token) {
        try {
          ws.send(JSON.stringify({ type: 'auth', token }))
        } catch {
          finish(null)
        }
      }
    })

    ws.on('message', (data) => {
      try {
        const msg = JSON.parse(String(data)) as { type?: string; version?: string }
        if (msg.type === 'hello') {
          finish({ version: msg.version ?? '' })
        }
      } catch {
        // ignore non-json
      }
    })

    ws.on('error', () => finish(null))
    ws.on('close', () => finish(null))
  })
}

async function pathExists(p: string): Promise<boolean> {
  try {
    await fs.access(p)
    return true
  } catch {
    return false
  }
}

async function whichOnPath(bin: string): Promise<string | null> {
  try {
    const { stdout } = await execFileAsync('which', [bin], { env: process.env })
    const p = stdout.trim().split('\n')[0]
    return p || null
  } catch {
    return null
  }
}

async function whichViaLoginShell(bin: string): Promise<string | null> {
  const shell = process.env.SHELL || '/bin/bash'
  try {
    const { stdout } = await execFileAsync(shell, ['-lc', `command -v ${bin}`], {
      env: process.env,
      timeout: 10_000,
    })
    const p = stdout.trim().split('\n')[0]
    return p || null
  } catch {
    return null
  }
}

/**
 * Resolve the talos binary path:
 * 1. opts.talosPath / cached ~/.talos/app.json
 * 2. `talos` on PATH
 * 3. login-shell `command -v talos`, then cache to app.json
 */
export async function resolveTalosPath(cached?: string, homeDir?: string): Promise<string> {
  if (cached && (await pathExists(cached))) {
    return cached
  }

  const settings = await readAppSettings(homeDir)
  if (settings.talosPath && (await pathExists(settings.talosPath))) {
    return settings.talosPath
  }

  const onPath = await whichOnPath('talos')
  if (onPath) {
    return onPath
  }

  const viaShell = await whichViaLoginShell('talos')
  if (viaShell) {
    await writeAppSettings({ ...settings, talosPath: viaShell }, homeDir)
    return viaShell
  }

  throw new Error(
    'talos binary not found — set ~/.talos/app.json { "talosPath": "..." } or install talos on PATH',
  )
}

async function spawnDaemon(talosPath: string): Promise<void> {
  const child = spawn(talosPath, ['serve', '-d'], {
    detached: true,
    stdio: 'ignore',
    env: process.env,
  })
  child.unref()
}

async function waitForDiscovery(homeDir?: string, timeoutMs = SPAWN_TIMEOUT_MS): Promise<Discovery> {
  const deadline = Date.now() + timeoutMs
  while (Date.now() < deadline) {
    const d = await readDiscoveryAsync(homeDir)
    if (d) return d
    await new Promise((r) => setTimeout(r, POLL_MS))
  }
  throw new Error('timed out waiting for ~/.talos/daemon.json after spawning talos serve -d')
}

function discoveryToInfo(d: Discovery): DaemonInfo {
  return {
    wsURL: d.ws,
    token: d.token,
    // Prefer discovery version (written at spawn) so the renderer can
    // compare against hello.version for mismatch banners.
    version: d.version,
    pid: d.pid || undefined,
  }
}

export async function ensureDaemon(opts?: {
  talosPath?: string
  homeDir?: string
}): Promise<DaemonInfo> {
  const homeDir = opts?.homeDir

  const existing = await readDiscoveryAsync(homeDir)
  if (existing) {
    const hello = await probeDaemon(existing.ws, existing.token)
    if (hello) {
      return discoveryToInfo(existing)
    }
    await removeDiscovery(homeDir)
  }

  const talosPath = await resolveTalosPath(opts?.talosPath, homeDir)
  await spawnDaemon(talosPath)
  const d = await waitForDiscovery(homeDir)
  const hello = await probeDaemon(d.ws, d.token)
  if (!hello) {
    throw new Error(`spawned daemon at ${d.ws} but probe failed`)
  }
  return discoveryToInfo(d)
}

function killPid(pid: number): void {
  if (!pid || pid <= 0) return
  try {
    process.kill(pid, 'SIGTERM')
  } catch {
    // already dead
  }
}

async function waitPidGone(pid: number, timeoutMs = 5_000): Promise<void> {
  if (!pid) return
  const deadline = Date.now() + timeoutMs
  while (Date.now() < deadline) {
    try {
      process.kill(pid, 0)
      await new Promise((r) => setTimeout(r, 100))
    } catch {
      return
    }
  }
}

/** User-initiated restart: SIGTERM the discovery pid, then ensureDaemon again. */
export async function restartDaemon(opts?: {
  talosPath?: string
  homeDir?: string
}): Promise<DaemonInfo> {
  const homeDir = opts?.homeDir
  const existing = await readDiscoveryAsync(homeDir)
  if (existing?.pid) {
    killPid(existing.pid)
    await waitPidGone(existing.pid)
  }
  await removeDiscovery(homeDir)
  return ensureDaemon(opts)
}
