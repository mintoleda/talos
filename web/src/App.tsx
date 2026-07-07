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

    eng.onReady = (sid) => {
      setSession(sid);
      setConnected(true);
      setError('');

      // Load history via RPC.
      eng.request('engine.history').then((raw) => {
        const hist = (raw as { history?: unknown[] })?.history ?? [];
        setState((s) => ingestHistory(s, hist));
      }).catch(() => {
        // Non-fatal — history is a best-effort sync.
      });

      // Fetch initial permission mode.
      eng.request('engine.permissionMode').then((raw) => {
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
    engineRef.current?.submit(text);
  }, []);

  const handleInterrupt = useCallback(() => {
    engineRef.current?.interrupt();
  }, []);

  const handleApprove = useCallback(() => {
    engineRef.current?.approve(true);
    setState((s) => ({ ...s, permissionRequest: null }));
  }, []);

  const handleDeny = useCallback(() => {
    engineRef.current?.approve(false);
    setState((s) => ({ ...s, permissionRequest: null }));
  }, []);

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
            Start a server with <code>talos --server ws:localhost:8080</code>
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
