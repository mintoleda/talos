/**
 * Composer — input area with Enter to submit, Esc to interrupt.
 */

import React, { useState, useRef, useEffect } from 'react';

interface ComposerProps {
  busy: boolean;
  onSubmit: (text: string) => void;
  onInterrupt: () => void;
}

export function Composer({ busy, onSubmit, onInterrupt }: ComposerProps) {
  const [value, setValue] = useState('');
  const inputRef = useRef<HTMLTextAreaElement>(null);

  useEffect(() => {
    if (!busy) inputRef.current?.focus();
  }, [busy]);

  const handleKeyDown = (e: React.KeyboardEvent) => {
    if (e.key === 'Enter' && !e.shiftKey) {
      e.preventDefault();
      if (busy) {
        onInterrupt();
      } else if (value.trim()) {
        onSubmit(value.trim());
        setValue('');
      }
    }
    if (e.key === 'Escape' && busy) {
      onInterrupt();
    }
  };

  return (
    <div className="composer">
      <textarea
        ref={inputRef}
        className="composer-input"
        value={value}
        onChange={(e) => setValue(e.target.value)}
        onKeyDown={handleKeyDown}
        placeholder={busy ? 'Press Enter to interrupt…' : 'Type a message…'}
        rows={2}
        disabled={false}
      />
      <div className="composer-actions">
        {busy ? (
          <button className="btn btn-interrupt" onClick={onInterrupt}>
            interrupt
          </button>
        ) : (
          <button
            className="btn btn-submit"
            onClick={() => { if (value.trim()) { onSubmit(value.trim()); setValue(''); } }}
            disabled={!value.trim()}
          >
            send
          </button>
        )}
      </div>
    </div>
  );
}
