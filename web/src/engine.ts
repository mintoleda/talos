/**
 * engine.ts — WebSocket engine mirroring the Go RemoteEngine.
 *
 * Wraps one WebSocket connection, demuxes ServerMsg into:
 *   (a) an event stream (push-based protocol.Event)
 *   (b) request/response by id (Promise per in-flight request)
 */

import type { ServerMsg, ClientMsg, Event } from './protocol';

export class Engine {
  private ws: WebSocket;
  private closed = false;

  /** Callback for inbound events (TextDelta, ToolStarted, …) */
  onEvent: ((ev: Event) => void) | null = null;
  /** Callback when the connection drops */
  onClose: ((reason: string) => void) | null = null;
  /** Callback when the hello handshake completes */
  onReady: ((session: string) => void) | null = null;

  private pending = new Map<number, { resolve: (val: unknown) => void; reject: (err: Error) => void }>();
  private nextID = 1;

  constructor(url: string, token?: string) {
    this.ws = new WebSocket(url);

    this.ws.onopen = () => {
      if (token) {
        this.sendRaw({ type: 'auth', token });
      }
    };

    this.ws.onmessage = (msg) => {
      let sm: ServerMsg;
      try {
        sm = JSON.parse(msg.data);
      } catch {
        return;
      }

      if (sm.type === 'hello') {
        this.onReady?.(sm.session ?? '');
        return;
      }

      if (sm.type === 'error') {
        console.error('server error:', sm.err);
        return;
      }

      if (sm.type === 'response') {
        const p = this.pending.get(sm.id ?? 0);
        if (p) {
          this.pending.delete(sm.id ?? 0);
          if (sm.err) {
            p.reject(new Error(sm.err));
          } else {
            p.resolve(sm.result);
          }
        }
        return;
      }

      if (sm.type === 'event' && sm.etype && sm.event) {
        const raw = sm.event as Record<string, unknown>;
        raw.etype = sm.etype;
        this.onEvent?.(raw as unknown as Event);
      }
    };

    this.ws.onclose = () => {
      this.closed = true;
      this.onClose?.('connection closed');
    };

    this.ws.onerror = () => {
      this.closed = true;
      this.onClose?.('connection error');
    };
  }

  /** Submit user text as a new turn. */
  submit(text: string) {
    this.sendRaw({ type: 'input', text });
  }

  /** Interrupt the current turn. */
  interrupt() {
    this.sendRaw({ type: 'interrupt' });
  }

  /** Approve or deny a permission request. */
  approve(ok: boolean) {
    this.sendRaw({ type: 'approve', approved: ok });
  }

  /** Send a steer message (text typed while busy). */
  steer(text: string) {
    this.sendRaw({ type: 'steer', text });
  }

  /** Call an RPC method and await the result. */
  async request(method: string, params?: unknown): Promise<unknown> {
    const id = this.nextID++;
    return new Promise((resolve, reject) => {
      this.pending.set(id, { resolve, reject });
      this.sendRaw({ type: 'request', id, method, params });
      setTimeout(() => {
        if (this.pending.has(id)) {
          this.pending.delete(id);
          reject(new Error('request timeout'));
        }
      }, 30_000);
    });
  }

  close() {
    this.closed = true;
    this.ws.close();
  }

  private sendRaw(msg: ClientMsg) {
    if (this.closed || this.ws.readyState !== WebSocket.OPEN) return;
    this.ws.send(JSON.stringify(msg));
  }
}
