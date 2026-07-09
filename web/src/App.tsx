/**
 * App — root component wiring Engine + State + UI.
 */

import React, { useState, useCallback, useEffect, useRef } from 'react';
import { Engine } from './engine';
import type { Event } from './protocol';
import { type ChatState, initialState, reduceState, ingestHistory } from './state';
import { ChatView } from './ChatView';
import { Composer } from './Composer';
import { PermissionPrompt } from './PermissionPrompt';
import { StatusBar } from './StatusBar';

function getWsUrl(): string {
  const params = new URLSearchParams(window.location.search);
  const urlParam = params.get('ws');
  if (urlParam) return urlParam;

  // In dev, Vite proxies /ws to the server.
  const proto = window.location.protocol === 'https:' ? 'wss:' : 'ws:';
  return `${proto}//${window.location.host}/ws`;
}

export function App() {
  const [state, setState] = useState<ChatState>(initialState);
  const engineRef = useRef<Engine | null>(null);
  const [connected, setConnected] = useState(false);
  const [session, setSession] = useState('');
  const [error, setError] = useState('');

  useEffect(() => {
    const url = getWsUrl();
    const params = new URLSearchParams(window.location.search);
    const token = params.get('token') ?? undefined;

    const eng = new Engine(url, token);
    engineRef.current = eng;

    eng.onReady = async (sid) => {
      setConnected(true);
      setError('');

      let sessionId = sid;
      if (!sessionId) {
        try {
          const params = new URLSearchParams(window.location.search);
          const dir = params.get('dir');
          if (dir) {
            const created = await eng.request('daemon.createSession', {
              dir,
              isolation: 'none',
            }) as { session?: { id?: string } };
            sessionId = created?.session?.id ?? '';
          }
          if (!sessionId) {
            const listed = await eng.request('daemon.listSessions') as { sessions?: { id: string }[] };
            sessionId = listed?.sessions?.[0]?.id ?? '';
          }
          if (!sessionId) {
            setError('no session — pass ?dir=/absolute/project/path');
            setConnected(false);
            return;
          }
          eng.subscribe(sessionId);
        } catch (e) {
          setError(e instanceof Error ? e.message : String(e));
          setConnected(false);
          return;
        }
      } else {
        eng.subscribe(sessionId);
      }
      setSession(sessionId);

      eng.request('engine.history', undefined, sessionId).then((raw) => {
        const hist = (raw as { history?: unknown[] })?.history ?? [];
        setState((s) => ingestHistory(s, hist));
      }).catch(() => {});

      eng.request('engine.permissionMode', undefined, sessionId).then((raw) => {
        const r = raw as { level?: string } | null;
        if (r?.level) {
          setState((s) => ({ ...s, permissionMode: r.level! }));
        }
      }).catch(() => {});
    };

    eng.onEvent = (ev: Event) => {
      setState((s) => reduceState(s, ev));
    };

    eng.onClose = (reason) => {
      setConnected(false);
      setError(reason);
    };

    return () => {
      eng.close();
      engineRef.current = null;
    };
  }, []);

  const handleSubmit = useCallback((text: string) => {
    engineRef.current?.submit(text, session || undefined);
  }, [session]);

  const handleInterrupt = useCallback(() => {
    engineRef.current?.interrupt(session || undefined);
  }, [session]);

  const handleApprove = useCallback(() => {
    engineRef.current?.approve(true, session || undefined);
    setState((s) => ({ ...s, permissionRequest: null }));
  }, [session]);

  const handleDeny = useCallback(() => {
    engineRef.current?.approve(false, session || undefined);
    setState((s) => ({ ...s, permissionRequest: null }));
  }, [session]);

  if (!connected) {
    return (
      <div className="app">
        <div className="connecting">
          <h1>talos</h1>
          {error ? (
            <p className="error">{error}</p>
          ) : (
            <p>connecting…</p>
          )}
          <p className="hint">
            Start a daemon with <code>talos serve</code>
          </p>
        </div>
      </div>
    );
  }

  return (
    <div className="app">
      <StatusBar
        provider={state.provider}
        model={state.model}
        thinkingLevel={state.thinkingLevel}
        permissionMode={state.permissionMode}
        promptTokens={state.promptTokens}
        contextLimit={state.contextLimit}
        busy={state.busy}
      />
      <ChatView
        messages={state.messages}
        streamedText={state.streamedText}
        streamedThinking={state.streamedThinking}
        activeTools={state.activeTools}
        busy={state.busy}
      />
      {state.permissionRequest && (
        <PermissionPrompt
          request={state.permissionRequest}
          onApprove={handleApprove}
          onDeny={handleDeny}
        />
      )}
      <Composer
        busy={state.busy}
        onSubmit={handleSubmit}
        onInterrupt={handleInterrupt}
      />
    </div>
  );
}
