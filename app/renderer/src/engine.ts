import type { ServerMsg, ClientMsg, Event } from './protocol'

export type EngineOptions = {
  autoReconnect?: boolean
  onReconnect?: (session: string) => void
  /**
   * Resolve fresh connection credentials before retrying.
   * In Electron this typically calls window.talos.getDaemon().
   */
  resolveConnection?: () => Promise<{ url: string; token?: string } | null>
}

export class Engine {
  private ws: WebSocket | null = null
  private closed = false
  private intentionalClose = false
  private url: string
  private token?: string
  private reconnectAttempt = 0
  private reconnectTimer: ReturnType<typeof setTimeout> | null = null
  private readonly autoReconnect: boolean
  private readonly resolveConnection?: EngineOptions['resolveConnection']

  onEvent: ((ev: Event, session?: string) => void) | null = null
  onClose: ((reason: string) => void) | null = null
  onReady: ((session: string, version?: string) => void) | null = null
  onReconnect: ((session: string) => void) | null = null

  private pending = new Map<number, { resolve: (val: unknown) => void; reject: (err: Error) => void }>()
  private nextID = 1
  private helloVersion = ''
  private hasConnectedOnce = false

  constructor(url: string, token?: string, opts?: EngineOptions) {
    this.url = url
    this.token = token
    this.autoReconnect = opts?.autoReconnect !== false
    this.resolveConnection = opts?.resolveConnection
    this.onReconnect = opts?.onReconnect ?? null
    this.connect()
  }

  get serverVersion(): string {
    return this.helloVersion
  }

  private connect() {
    this.intentionalClose = false
    this.ws = new WebSocket(this.url)

    this.ws.onopen = () => {
      if (this.token) {
        this.sendRaw({ type: 'auth', token: this.token })
      }
    }

    this.ws.onmessage = (msg) => {
      let sm: ServerMsg
      try {
        sm = JSON.parse(msg.data)
      } catch {
        return
      }

      if (sm.type === 'hello') {
        this.helloVersion = sm.version ?? ''
        this.reconnectAttempt = 0
        const session = sm.session ?? ''
        const reconnected = this.hasConnectedOnce
        this.hasConnectedOnce = true
        this.onReady?.(session, this.helloVersion)
        if (reconnected) {
          this.onReconnect?.(session)
        }
        return
      }

      if (sm.type === 'error') {
        console.error('server error:', sm.err, sm.session ? `(session ${sm.session})` : '')
        return
      }

      if (sm.type === 'response') {
        const p = this.pending.get(sm.id ?? 0)
        if (p) {
          this.pending.delete(sm.id ?? 0)
          if (sm.err) {
            p.reject(new Error(sm.err))
          } else {
            p.resolve(sm.result)
          }
        }
        return
      }

      if (sm.type === 'event' && sm.etype && sm.event) {
        const raw = sm.event as Record<string, unknown>
        raw.etype = sm.etype
        this.onEvent?.(raw as unknown as Event, sm.session)
      }
    }

    this.ws.onclose = () => {
      this.rejectAllPending('connection closed')
      if (this.intentionalClose || this.closed) {
        this.onClose?.('connection closed')
        return
      }
      if (this.autoReconnect) {
        this.onClose?.('connection closed — reconnecting…')
        void this.scheduleReconnect()
      } else {
        this.closed = true
        this.onClose?.('connection closed')
      }
    }

    this.ws.onerror = () => {}
  }

  private rejectAllPending(reason: string) {
    for (const [, p] of this.pending) {
      p.reject(new Error(reason))
    }
    this.pending.clear()
  }

  private async scheduleReconnect() {
    if (this.intentionalClose || this.closed) return
    if (this.reconnectTimer) return

    const attempt = this.reconnectAttempt++
    const delay = Math.min(30_000, 500 * Math.pow(2, attempt))

    this.reconnectTimer = setTimeout(async () => {
      this.reconnectTimer = null
      if (this.intentionalClose || this.closed) return

      if (this.resolveConnection) {
        try {
          const next = await this.resolveConnection()
          if (next) {
            this.url = next.url
            this.token = next.token
          }
        } catch {
          // keep previous url/token
        }
      } else if (typeof window !== 'undefined' && window.talos) {
        try {
          const d = await window.talos.getDaemon()
          this.url = d.wsURL
          this.token = d.token
        } catch {
          // keep previous
        }
      }

      this.connect()
    }, delay)
  }

  subscribe(session: string) {
    this.sendRaw({ type: 'subscribe', session })
  }

  unsubscribe(session: string) {
    this.sendRaw({ type: 'unsubscribe', session })
  }

  submit(text: string, session?: string) {
    this.sendRaw({ type: 'input', text, session })
  }

  interrupt(session?: string) {
    this.sendRaw({ type: 'interrupt', session })
  }

  approve(ok: boolean, session?: string) {
    this.sendRaw({ type: 'approve', approved: ok, session })
  }

  steer(text: string, session?: string) {
    this.sendRaw({ type: 'steer', text, session })
  }

  async request(method: string, params?: unknown, session?: string): Promise<unknown> {
    const id = this.nextID++
    return new Promise((resolve, reject) => {
      this.pending.set(id, { resolve, reject })
      this.sendRaw({ type: 'request', id, method, params, session })
      setTimeout(() => {
        if (this.pending.has(id)) {
          this.pending.delete(id)
          reject(new Error('request timeout'))
        }
      }, 30_000)
    })
  }

  close() {
    this.intentionalClose = true
    this.closed = true
    if (this.reconnectTimer) {
      clearTimeout(this.reconnectTimer)
      this.reconnectTimer = null
    }
    this.ws?.close()
  }

  private sendRaw(msg: ClientMsg) {
    if (this.intentionalClose || !this.ws || this.ws.readyState !== WebSocket.OPEN) return
    this.ws.send(JSON.stringify(msg))
  }
}
