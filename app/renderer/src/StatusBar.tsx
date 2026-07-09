/**
 * StatusBar — model, thinking level, context meter, permission mode.
 */

import React from 'react';

interface StatusBarProps {
  provider: string;
  model: string;
  thinkingLevel: string;
  permissionMode: string;
  promptTokens: number;
  contextLimit: number;
  busy: boolean;
}

export function StatusBar({
  provider,
  model,
  thinkingLevel,
  permissionMode,
  promptTokens,
  contextLimit,
  busy,
}: StatusBarProps) {
  const ctxPct = contextLimit > 0 ? Math.round((promptTokens / contextLimit) * 100) : 0;
  const ctxColor = ctxPct > 80 ? '#f44' : ctxPct > 60 ? '#fa0' : '#4a4';

  return (
    <div className="status-bar">
      <span className="status-item">
        {provider}/{model}
      </span>
      {thinkingLevel && (
        <span className="status-item thinking-level">{thinkingLevel}</span>
      )}
      <span className={`status-item perm-mode ${permissionMode}`}>
        {permissionMode}
      </span>
      {contextLimit > 0 && (
        <span className="status-item context-meter" style={{ color: ctxColor }}>
          {promptTokens.toLocaleString()} / {contextLimit.toLocaleString()} ({ctxPct}%)
        </span>
      )}
      {busy && <span className="status-item busy-indicator">busy</span>}
    </div>
  );
}
