package openai

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"strings"

	"github.com/mintoleda/talos/internal/protocol"
)

func parseSSE(body io.ReadCloser, out chan<- protocol.ProviderEvent) {
	defer close(out)
	defer body.Close()

	r := bufio.NewReader(body)

	type partial struct {
		id   string
		name string
		args strings.Builder
	}
	partials := map[int]*partial{}
	var order []int
	stopReason := "stop"

	flushToolCalls := func() {
		for _, idx := range order {
			p := partials[idx]
			var args map[string]any
			if s := p.args.String(); s != "" {
				if err := json.Unmarshal([]byte(s), &args); err != nil {
					out <- protocol.PEError{Err: fmt.Errorf("tool args parse (%s): %w", p.name, err)}
					continue
				}
			} else {
				args = map[string]any{}
			}
			out <- protocol.PEToolCall{ToolUse: protocol.ToolUse{ID: p.id, Name: p.name, Args: args}}
		}
	}

	for {
		line, err := r.ReadString('\n')
		if len(line) > 0 {
			line = strings.TrimRight(line, "\r\n")
			if line == "" || strings.HasPrefix(line, ":") {
				// ignore
			} else if data, ok := strings.CutPrefix(line, "data: "); ok {
				if data == "[DONE]" {
					flushToolCalls()
					out <- protocol.PEDone{StopReason: stopReason}
					return
				}
				var chunk streamChunk
				if e := json.Unmarshal([]byte(data), &chunk); e != nil {
					out <- protocol.PEError{Err: fmt.Errorf("chunk parse: %w", e)}
					return
				}
				if chunk.Usage != nil {
					out <- protocol.PEUsage{Usage: protocol.Usage{
						PromptTokens:       chunk.Usage.PromptTokens,
						CompletionTokens:   chunk.Usage.CompletionTokens,
						CachedPromptTokens: chunk.Usage.PromptCacheHitTokens,
					}}
				}
				for _, ch := range chunk.Choices {
					if ch.FinishReason != nil && *ch.FinishReason != "" {
						stopReason = *ch.FinishReason
					}
					if ch.Delta.Content != "" {
						out <- protocol.PEText{Text: ch.Delta.Content}
					}
					if ch.Delta.ReasoningContent != "" {
						out <- protocol.PEThinking{Text: ch.Delta.ReasoningContent}
					}
					for _, tc := range ch.Delta.ToolCalls {
						p := partials[tc.Index]
						if p == nil {
							p = &partial{}
							partials[tc.Index] = p
							order = append(order, tc.Index)
						}
						if tc.ID != "" {
							p.id = tc.ID
						}
						if tc.Function.Name != "" {
							p.name = tc.Function.Name
						}
						p.args.WriteString(tc.Function.Arguments)
					}
				}
			}
		}
		if err == io.EOF {
			flushToolCalls()
			out <- protocol.PEDone{StopReason: stopReason}
			return
		}
		if err != nil {
			out <- protocol.PEError{Err: fmt.Errorf("read stream: %w", err)}
			return
		}
	}
}
