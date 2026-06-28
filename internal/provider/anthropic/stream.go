package anthropic

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"strings"

	"github.com/mintoleda/talos/internal/protocol"
)

type blockState struct {
	blockType string
	id        string
	name      string
	textSB    strings.Builder
	jsonSB    strings.Builder
	thinkSB   strings.Builder
}

func parseSSE(body io.ReadCloser, out chan<- protocol.ProviderEvent) {
	defer close(out)
	defer body.Close()

	r := bufio.NewReader(body)
	blocks := map[int]*blockState{}

	var eventType string
	var dataLines []string

	flushBlock := func(idx int) {
		bs := blocks[idx]
		if bs == nil {
			return
		}
		switch bs.blockType {
		case "tool_use":
			var args map[string]any
			if s := bs.jsonSB.String(); s != "" {
				if err := json.Unmarshal([]byte(s), &args); err != nil {
					out <- protocol.PEError{Err: fmt.Errorf("tool args parse (%s): %w", bs.name, err)}
					return
				}
			} else {
				args = map[string]any{}
			}
			out <- protocol.PEToolCall{ToolUse: protocol.ToolUse{ID: bs.id, Name: bs.name, Args: args}}
		case "thinking":
			out <- protocol.PEThinking{Text: bs.thinkSB.String()}
		}
		delete(blocks, idx)
	}

	for {
		line, err := r.ReadString('\n')
		line = strings.TrimRight(line, "\r\n")

		if line == "" {
			if eventType != "" && len(dataLines) > 0 {
				data := strings.Join(dataLines, "\n")
				var ev sseEvent
				if je := json.Unmarshal([]byte(data), &ev); je != nil {
					out <- protocol.PEError{Err: fmt.Errorf("parse sse event: %w", je)}
					return
				}
				processEvent(eventType, ev, blocks, out, flushBlock)
			}
			eventType = ""
			dataLines = dataLines[:0]
		} else if s, ok := strings.CutPrefix(line, "event: "); ok {
			eventType = strings.TrimSpace(s)
		} else if s, ok := strings.CutPrefix(line, "data: "); ok {
			dataLines = append(dataLines, s)
		}

		if err == io.EOF {
			return
		}
		if err != nil {
			out <- protocol.PEError{Err: fmt.Errorf("read stream: %w", err)}
			return
		}
	}
}

func processEvent(
	typ string,
	ev sseEvent,
	blocks map[int]*blockState,
	out chan<- protocol.ProviderEvent,
	flush func(int),
) {
	switch typ {
	case "content_block_start":
		if ev.ContentBlock == nil {
			return
		}
		bs := &blockState{blockType: ev.ContentBlock.Type}
		if ev.ContentBlock.Type == "tool_use" {
			bs.id = ev.ContentBlock.ID
			bs.name = ev.ContentBlock.Name
		}
		blocks[ev.Index] = bs
	case "content_block_delta":
		bs := blocks[ev.Index]
		if bs == nil || ev.Delta == nil {
			return
		}
		switch ev.Delta.Type {
		case "text_delta":
			bs.textSB.WriteString(ev.Delta.Text)
			out <- protocol.PEText{Text: ev.Delta.Text}
		case "input_json_delta":
			bs.jsonSB.WriteString(ev.Delta.PartialJSON)
		case "thinking_delta":
			bs.thinkSB.WriteString(ev.Delta.Thinking)
		}
	case "content_block_stop":
		flush(ev.Index)
	case "message_delta":
		if ev.Usage != nil {
			out <- protocol.PEUsage{Usage: protocol.Usage{
				PromptTokens:       ev.Usage.InputTokens,
				CompletionTokens:   ev.Usage.OutputTokens,
				CachedPromptTokens: ev.Usage.CacheReadInputTokens,
			}}
		}
		if ev.Delta != nil {
			out <- protocol.PEDone{StopReason: ev.Delta.Type}
		}
	case "message_stop":
		// final; channel will be closed by the defer
	case "error":
		out <- protocol.PEError{Err: fmt.Errorf("anthropic stream error: %s", ev.Type)}
	}
}
