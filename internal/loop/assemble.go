package loop

import (
	"context"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/mintoleda/talos/internal/protocol"
)

// Compiled regexes for markdown normalization.
var (
	// fenceRe matches triple-or-more backtick or tilde sequences that are
	// NOT on their own line (preceded by a non-newline character).
	// The model sometimes writes "text.```go" instead of "text.\n\n```go",
	// which glamour refuses to render as a code block.
	fenceRe = regexp.MustCompile(`([^\n])((?:` + "`" + `{3,}|~{3,}))`)

	// sentenceRe matches missing space after sentence-ending punctuation
	// when the next word starts with an uppercase letter. The model often
	// concatenates sentences: "components.Let" → "components. Let".
	sentenceRe = regexp.MustCompile(`([a-z0-9)])([.!?])([A-Z])`)
)

// normalizeMarkdown fixes common formatting mistakes in model-written markdown
// so glamour (the TUI renderer) can render them correctly. It is applied to
// every text block both during streaming and at final assembly.
func normalizeMarkdown(s string) string {
	s = fenceRe.ReplaceAllString(s, "$1\n\n$2")
	s = sentenceRe.ReplaceAllString(s, "$1$2 $3")
	return s
}

// streamWithRetry wraps streamAndAssemble with exponential-backoff retries for
// transient provider errors. It only retries when no text has been emitted to
// the UI yet — once streaming has started, a partial response can't be cleanly
// replayed, so the error is returned as-is.
func (l *Loop) streamWithRetry(ctx context.Context, req protocol.Request, emit protocol.EmitFunc) (protocol.Message, protocol.Usage, error) {
	const maxAttempts = 3
	delay := time.Second
	var lastErr error
	for attempt := 0; attempt < maxAttempts; attempt++ {
		if attempt > 0 {
			select {
			case <-ctx.Done():
				return protocol.Message{}, protocol.Usage{}, ctx.Err()
			case <-time.After(delay):
			}
			delay *= 2
			emit(protocol.Notice{Level: "warn", Text: fmt.Sprintf("provider error — retrying (attempt %d/%d): %v", attempt+1, maxAttempts, lastErr)})
		}
		var textEmitted bool
		wrapped := func(ev protocol.Event) {
			if _, ok := ev.(protocol.TextDelta); ok {
				textEmitted = true
			}
			emit(ev)
		}
		msg, usage, err := l.streamAndAssemble(ctx, req, wrapped)
		if err == nil {
			return msg, usage, nil
		}
		lastErr = err
		if ctx.Err() != nil || textEmitted {
			return protocol.Message{}, usage, err
		}
	}
	return protocol.Message{}, protocol.Usage{}, fmt.Errorf("provider failed after %d attempts: %w", maxAttempts, lastErr)
}

func (l *Loop) streamAndAssemble(ctx context.Context, req protocol.Request, emit protocol.EmitFunc) (protocol.Message, protocol.Usage, error) {
	stream, err := l.provider.StreamTurn(ctx, req)
	if err != nil {
		return protocol.Message{}, protocol.Usage{}, fmt.Errorf("start stream: %w", err)
	}

	var (
		textSB   strings.Builder
		toolUses []protocol.ContentBlock
		usage    protocol.Usage
		stop     string
	)

	for ev := range stream {
		switch e := ev.(type) {
		case protocol.PEText:
			normalized := normalizeMarkdown(e.Text)
			textSB.WriteString(normalized)
			emit(protocol.TextDelta{Text: normalized})
		case protocol.PEThinking:
			// Thinking blocks are the model's internal reasoning process.
			// We intentionally drop them here rather than emitting them as
			// notices, because the raw thinking text is noisy and users found
			// it confusing when it appeared inline in the chat transcript.
			// The thinking level is controlled via /thinking in the TUI.
		case protocol.PEToolCall:
			tu := e.ToolUse
			toolUses = append(toolUses, protocol.ContentBlock{
				Type: protocol.BlockToolUse, ToolUse: &tu,
			})
		case protocol.PEUsage:
			usage = e.Usage
		case protocol.PEDone:
			stop = e.StopReason
		case protocol.PEError:
			return protocol.Message{}, usage, fmt.Errorf("stream: %w", e.Err)
		}
	}
	_ = stop

	var content []protocol.ContentBlock
	if textSB.Len() > 0 {
		// Normalize the full text too — catches any edge cases that fell
		// across chunk boundaries during streaming.
		text := normalizeMarkdown(textSB.String())
		content = append(content, protocol.ContentBlock{Type: protocol.BlockText, Text: text})
	}
	content = append(content, toolUses...)
	return protocol.Message{Role: protocol.RoleAssistant, Content: content}, usage, nil
}
