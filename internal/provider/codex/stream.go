package codex

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/mintoleda/talos/internal/protocol"
)

// slot tracks one in-flight output item, keyed by output_index.
type slot struct {
	kind    string // "reasoning" | "message" | "function_call"
	callID  string
	name    string
	args    strings.Builder
	emitted bool // a delta was streamed, so the item.done text is redundant
}

// streamParser turns Codex Responses SSE events into ProviderEvents,
// following pi's processResponsesStream semantics.
type streamParser struct {
	ctx      context.Context
	out      chan<- protocol.ProviderEvent
	slots    map[int]*slot
	sawTools bool
}

// send delivers ev unless the consumer has gone away.
func (p *streamParser) send(ev protocol.ProviderEvent) bool {
	select {
	case p.out <- ev:
		return true
	case <-p.ctx.Done():
		return false
	}
}

func parseStream(ctx context.Context, body io.ReadCloser, out chan<- protocol.ProviderEvent) {
	defer close(out)
	defer body.Close()

	p := &streamParser{ctx: ctx, out: out, slots: map[int]*slot{}}
	r := bufio.NewReader(body)
	var dataLines []string

	// handleFrame returns true when the stream is finished (terminal event
	// or error already sent).
	handleFrame := func() bool {
		if len(dataLines) == 0 {
			return false
		}
		data := strings.TrimSpace(strings.Join(dataLines, "\n"))
		dataLines = dataLines[:0]
		if data == "" || data == "[DONE]" {
			return false
		}
		var ev streamEvent
		if err := json.Unmarshal([]byte(data), &ev); err != nil {
			p.send(protocol.PEError{Err: fmt.Errorf("invalid Codex SSE JSON: %w", err)})
			return true
		}
		done, err := p.handle(ev)
		if err != nil {
			p.send(protocol.PEError{Err: err})
			return true
		}
		return done
	}

	for {
		line, err := r.ReadString('\n')
		line = strings.TrimRight(line, "\r\n")
		if line == "" {
			if handleFrame() {
				return
			}
		} else if data, ok := strings.CutPrefix(line, "data:"); ok {
			dataLines = append(dataLines, strings.TrimSpace(data))
		}
		if err == io.EOF {
			// EOF terminates any residual frame.
			if handleFrame() {
				return
			}
			p.send(protocol.PEError{Err: errors.New("codex stream ended before a terminal response event")})
			return
		}
		if err != nil {
			if ctx.Err() == nil {
				p.send(protocol.PEError{Err: fmt.Errorf("read stream: %w", err)})
			}
			return
		}
	}
}

// handle processes one event. done reports a terminal event was handled.
func (p *streamParser) handle(ev streamEvent) (done bool, err error) {
	switch ev.Type {
	case "response.output_item.added":
		if ev.Item != nil {
			p.createSlot(ev.OutputIndex, ev.Item)
		}

	case "response.reasoning_summary_text.delta", "response.reasoning_text.delta":
		if s := p.slot(ev.OutputIndex, "reasoning"); s != nil && ev.Delta != "" {
			s.emitted = true
			p.send(protocol.PEThinking{Text: ev.Delta})
		}

	case "response.reasoning_summary_part.done":
		if s := p.slot(ev.OutputIndex, "reasoning"); s != nil {
			p.send(protocol.PEThinking{Text: "\n\n"})
		}

	case "response.output_text.delta", "response.refusal.delta":
		if s := p.slot(ev.OutputIndex, "message"); s != nil && ev.Delta != "" {
			s.emitted = true
			p.send(protocol.PEText{Text: ev.Delta})
		}

	case "response.function_call_arguments.delta":
		if s := p.slot(ev.OutputIndex, "function_call"); s != nil {
			s.args.WriteString(ev.Delta)
		}

	case "response.function_call_arguments.done":
		if s := p.slot(ev.OutputIndex, "function_call"); s != nil {
			s.args.Reset()
			s.args.WriteString(ev.Arguments)
		}

	case "response.output_item.done":
		if ev.Item == nil {
			return false, nil
		}
		return false, p.finishItem(ev.OutputIndex, ev.Item)

	case "response.completed", "response.done", "response.incomplete":
		return true, p.finishResponse(ev.Response)

	case "response.failed":
		msg := "Codex response failed"
		if ev.Response != nil && ev.Response.Error != nil {
			if m := ev.Response.Error.Message; m != "" {
				msg = m
			} else if c := stringOf(ev.Response.Error.Code); c != "" {
				msg = c
			}
		}
		return true, errors.New(msg)

	case "error":
		code, msg := stringOf(ev.Code), stringOf(ev.Message)
		if ev.Error != nil {
			if code == "" {
				code = stringOf(ev.Error.Code)
			}
			if msg == "" {
				msg = ev.Error.Message
			}
		}
		if msg == "" {
			msg = code
		}
		if msg == "" {
			msg = "unknown error"
		}
		return true, fmt.Errorf("codex error: %s", msg)
	}
	return false, nil
}

func (p *streamParser) createSlot(idx int, item *outputItem) *slot {
	switch item.Type {
	case "reasoning", "message", "function_call":
	default:
		return nil
	}
	s := &slot{kind: item.Type, callID: item.CallID, name: item.Name}
	s.args.WriteString(item.Arguments)
	p.slots[idx] = s
	return s
}

func (p *streamParser) slot(idx int, kind string) *slot {
	if s := p.slots[idx]; s != nil && s.kind == kind {
		return s
	}
	return nil
}

func (p *streamParser) finishItem(idx int, item *outputItem) error {
	s := p.slot(idx, item.Type)
	if s == nil {
		s = p.createSlot(idx, item)
	}
	if s == nil {
		return nil
	}
	delete(p.slots, idx)

	switch item.Type {
	case "reasoning":
		if !s.emitted {
			var parts []string
			for _, sp := range item.Summary {
				if sp.Text != "" {
					parts = append(parts, sp.Text)
				}
			}
			if len(parts) == 0 {
				for _, c := range item.Content {
					if c.Text != "" {
						parts = append(parts, c.Text)
					}
				}
			}
			text := strings.Join(parts, "\n\n")
			if text != "" {
				p.send(protocol.PEThinking{Text: text})
			}
		}
	case "message":
		if !s.emitted {
			var sb strings.Builder
			for _, c := range item.Content {
				if c.Type == "output_text" {
					sb.WriteString(c.Text)
				} else {
					sb.WriteString(c.Refusal)
				}
			}
			if sb.Len() > 0 {
				p.send(protocol.PEText{Text: sb.String()})
			}
		}
	case "function_call":
		raw := item.Arguments
		if raw == "" {
			raw = s.args.String()
		}
		callID := item.CallID
		if callID == "" {
			callID = s.callID
		}
		name := item.Name
		if name == "" {
			name = s.name
		}
		args := map[string]any{}
		if strings.TrimSpace(raw) != "" {
			if err := json.Unmarshal([]byte(raw), &args); err != nil {
				return fmt.Errorf("tool args parse (%s): %w", name, err)
			}
		}
		p.sawTools = true
		p.send(protocol.PEToolCall{ToolUse: protocol.ToolUse{ID: callID, Name: name, Args: args}})
	}
	return nil
}

func (p *streamParser) finishResponse(resp *responseObj) error {
	for _, s := range p.slots {
		if s.kind == "function_call" {
			return fmt.Errorf("codex stream completed with an unfinished tool call: %s (%s)", s.name, s.callID)
		}
	}
	if resp == nil {
		p.send(protocol.PEDone{StopReason: p.stopReason("stop")})
		return nil
	}
	if u := resp.Usage; u != nil {
		usage := protocol.Usage{
			// input_tokens already includes cached and cache-write tokens,
			// matching talos's "total prompt size" convention.
			PromptTokens:     u.InputTokens,
			CompletionTokens: u.OutputTokens,
		}
		if d := u.InputTokensDetails; d != nil {
			usage.CachedPromptTokens = d.CachedTokens
			usage.CacheCreationTokens = d.CacheWriteTokens
		}
		p.send(protocol.PEUsage{Usage: usage})
	}
	reason := ""
	if resp.IncompleteDetails != nil {
		reason = resp.IncompleteDetails.Reason
	}
	switch resp.Status {
	case "incomplete":
		if reason == "max_output_tokens" {
			p.send(protocol.PEDone{StopReason: "length"})
			return nil
		}
		if reason == "" {
			return errors.New("codex response incomplete without a provider reason")
		}
		return fmt.Errorf("codex response incomplete: %s", reason)
	case "failed", "cancelled":
		if resp.Error != nil && resp.Error.Message != "" {
			return fmt.Errorf("codex response %s: %s", resp.Status, resp.Error.Message)
		}
		return fmt.Errorf("codex response %s", resp.Status)
	}
	p.send(protocol.PEDone{StopReason: p.stopReason("stop")})
	return nil
}

func (p *streamParser) stopReason(def string) string {
	if p.sawTools {
		return "tool_calls"
	}
	return def
}

func stringOf(v any) string {
	switch x := v.(type) {
	case nil:
		return ""
	case string:
		return x
	default:
		return fmt.Sprint(x)
	}
}
