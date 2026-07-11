/**
 * ChatView — renders the message history with streaming text/thinking/tools.
 */

import React, { useRef, useEffect } from 'react';
import type { MessageEntry, ToolCallState } from './state';
import { Markdown } from './Markdown';

interface ChatViewProps {
  messages: MessageEntry[];
  streamedText: string;
  streamedThinking: string;
  activeTools: ToolCallState[];
  busy: boolean;
  thinkExpanded: boolean;
}

export function ChatView({ messages, streamedText, streamedThinking, activeTools, busy, thinkExpanded }: ChatViewProps) {
  const bottomRef = useRef<HTMLDivElement>(null);

  useEffect(() => {
    bottomRef.current?.scrollIntoView({ behavior: 'smooth' });
  }, [messages, streamedText, streamedThinking, activeTools]);

  return (
    <div className="chat-view">
      {messages.map((msg, i) => (
        <MessageRow key={i} msg={msg} thinkExpanded={thinkExpanded} />
      ))}

      {/* In-progress assistant response */}
      {busy && (
        <>
          {streamedThinking && (
            <div className="message assistant">
              <div className="msg-label thinking-label">
                {thinkExpanded ? 'thinking' : `thinking·${Math.round(streamedThinking.length / 4)}tok`}
              </div>
              {thinkExpanded && (
                <div className="msg-text thinking-text">{streamedThinking}</div>
              )}
            </div>
          )}
          {streamedText && (
            <div className="message assistant">
              <div className="msg-label">assistant</div>
              <div className="msg-text"><Markdown text={streamedText} /></div>
            </div>
          )}
          {activeTools.length > 0 && (
            <div className="message tool">
              <div className="msg-label">tools</div>
              {activeTools.map((t) => (
                <ToolCallCard key={t.id} tool={t} running />
              ))}
            </div>
          )}
        </>
      )}

      <div ref={bottomRef} />
    </div>
  );
}

function MessageRow({ msg, thinkExpanded }: { msg: MessageEntry; thinkExpanded: boolean }) {
  if (msg.role === 'tool') {
    return (
      <div className="message tool">
        <div className="msg-label">tools</div>
        {msg.toolCalls.map((t) => (
          <ToolCallCard key={t.id} tool={t} running={false} />
        ))}
      </div>
    );
  }

  if (msg.type === 'thinking') {
    return (
      <div className="message assistant">
        <div className="msg-label thinking-label">
          {thinkExpanded ? 'thinking' : `thinking·${Math.round(msg.text.length / 4)}tok`}
        </div>
        {thinkExpanded && <div className="msg-text thinking-text">{msg.text}</div>}
      </div>
    );
  }

  const cls = msg.role === 'user' ? 'user' : 'assistant';
  return (
    <div className={`message ${cls}`}>
      <div className="msg-label">{msg.role}</div>
      <div className="msg-text">
        {msg.role === 'assistant' ? <Markdown text={msg.text} /> : msg.text}
      </div>
      {msg.usage && (
        <div className="msg-usage">
          {msg.usage.prompt_tokens}↑ {msg.usage.completion_tokens}↓
          {msg.usage.cached_prompt_tokens > 0 && ` (${msg.usage.cached_prompt_tokens} cached)`}
        </div>
      )}
    </div>
  );
}

function ToolCallCard({ tool, running }: { tool: ToolCallState; running: boolean }) {
  return (
    <div className="tool-card">
      <div className="tool-header">
        <span className="tool-name">{tool.name}</span>
        {running ? <span className="tool-badge running">running</span> : <span className="tool-badge done">done</span>}
      </div>
      {tool.output && (
        <details className="tool-output">
          <summary>output</summary>
          <pre>{tool.output}</pre>
        </details>
      )}
      {tool.result && (
        <details className="tool-result">
          <summary>result</summary>
          <pre>{tool.result.slice(0, 500)}{tool.result.length > 500 ? '…' : ''}</pre>
        </details>
      )}
    </div>
  );
}
