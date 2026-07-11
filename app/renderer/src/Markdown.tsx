/**
 * Markdown — render assistant text as sanitized GitHub-flavored markdown.
 */

import React, { useMemo } from 'react';
import { marked } from 'marked';
import DOMPurify from 'dompurify';

marked.setOptions({ gfm: true, breaks: true });

// Force links out of the Electron window into the default browser.
DOMPurify.addHook('afterSanitizeAttributes', (node) => {
  if (node.tagName === 'A') {
    node.setAttribute('target', '_blank');
    node.setAttribute('rel', 'noopener noreferrer');
  }
});

export function Markdown({ text }: { text: string }) {
  const html = useMemo(() => {
    const raw = marked.parse(text, { async: false });
    return DOMPurify.sanitize(raw);
  }, [text]);

  return <div className="markdown" dangerouslySetInnerHTML={{ __html: html }} />;
}
