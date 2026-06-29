package openai

import (
	"encoding/json"
	"strings"

	"github.com/mintoleda/talos/internal/jsonutil"
	"github.com/mintoleda/talos/internal/provider"
	"github.com/mintoleda/talos/internal/protocol"
)

func buildBody(req protocol.Request) ([]byte, error) {
	var msgs []chatMessage
	if req.System != "" {
		msgs = append(msgs, chatMessage{Role: string(protocol.RoleSystem), Content: textContent(req.System)})
	}

	for _, fm := range req.Messages {
		m := fm.Msg
		switch m.Role {
		case protocol.RoleUser:
			msgs = append(msgs, chatMessage{Role: string(m.Role), Content: userContent(m.Content)})
		case protocol.RoleAssistant:
			cm := chatMessage{Role: string(m.Role)}
			var textParts []string
			for _, b := range m.Content {
				switch b.Type {
				case protocol.BlockText:
					textParts = append(textParts, b.Text)
				case protocol.BlockToolUse:
					if b.ToolUse != nil {
						cm.ToolCalls = append(cm.ToolCalls, wireToolCall{
							ID:   b.ToolUse.ID,
							Type: "function",
							Function: struct {
								Name      string `json:"name"`
								Arguments string `json:"arguments"`
							}{
								Name:      b.ToolUse.Name,
								Arguments: argsToString(b.ToolUse.Args),
							},
						})
					}
				}
			}
			if len(textParts) > 0 {
				cm.Content = textContent(joinText(textParts))
			}
			// An assistant message with neither content nor tool_calls is invalid
			// for OpenAI-compatible APIs. Skip it entirely — it contributes nothing.
			if cm.Content == nil && len(cm.ToolCalls) == 0 {
				continue
			}
			msgs = append(msgs, cm)
		case protocol.RoleTool:
			for _, b := range m.Content {
				if b.Type == protocol.BlockToolResult && b.ToolResult != nil {
					msgs = append(msgs, chatMessage{
						Role:       string(protocol.RoleTool),
						Content:    textContent(b.ToolResult.Content),
						ToolCallID: b.ToolResult.ToolUseID,
					})
				}
			}
		}
	}

	for _, b := range req.Volatile {
		if b.Type == protocol.BlockText {
			msgs = append(msgs, chatMessage{Role: string(protocol.RoleUser), Content: textContent(b.Text)})
		}
	}

	var tools []chatTool
	for _, t := range req.Tools {
		tools = append(tools, chatTool{
			Type: "function",
			Function: struct {
				Name        string          `json:"name"`
				Description string          `json:"description"`
				Parameters  json.RawMessage `json:"parameters"`
			}{
				Name:        t.Name,
				Description: t.Description,
				Parameters:  t.Parameters,
			},
		})
	}

	effort := provider.MapThinkingToOpenAIEffort(req.ThinkingLevel)
	// Set a generous max_tokens; many providers require it for streaming.
	maxTok := 32768
	cr := chatRequest{
		Model:           req.Model,
		Stream:          true,
		StreamOptions:   &streamOpts{IncludeUsage: true},
		Messages:        msgs,
		Tools:           tools,
		MaxTokens:       maxTok,
		ReasoningEffort: strPtrOrNil(effort),
	}
	return json.Marshal(cr)
}

// userContent builds the JSON content field for a user message. If the blocks
// contain only text, it marshals as a plain string (for wider API compatibility).
// If images are present it marshals as an array of content parts.
func userContent(blocks []protocol.ContentBlock) json.RawMessage {
	var parts []contentPart
	hasImage := false
	for _, b := range blocks {
		switch b.Type {
		case protocol.BlockText:
			parts = append(parts, contentPart{Type: "text", Text: b.Text})
		case protocol.BlockImage:
			if b.Image != nil {
				hasImage = true
				parts = append(parts, contentPart{
					Type: "image_url",
					ImageURL: &imageURLPart{
						URL: "data:" + b.Image.MediaType + ";base64," + b.Image.Data,
					},
				})
			}
		}
	}
	if !hasImage && len(parts) == 1 {
		return textContent(parts[0].Text)
	}
	raw, _ := json.Marshal(parts)
	return raw
}

func textContent(s string) json.RawMessage {
	raw, _ := json.Marshal(s)
	return raw
}

func joinText(parts []string) string {
	return strings.Join(parts, "\n")
}

func strPtrOrNil(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

func argsToString(args map[string]any) string {
	b, err := jsonutil.MarshalDeterministic(args)
	if err != nil {
		return "{}"
	}
	return string(b)
}
