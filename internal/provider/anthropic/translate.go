package anthropic

import (
	"encoding/json"

	"github.com/mintoleda/talos/internal/jsonutil"
	"github.com/mintoleda/talos/internal/provider"
	"github.com/mintoleda/talos/internal/protocol"
)

// Config holds Anthropic-specific tunables.
type Config struct {
	MaxTokens      int
	ThinkingBudget int // Deprecated: use ThinkingLevel; kept for backward compat in tests.
	ThinkingLevel  string
}

func buildBody(req protocol.Request, cfg Config) ([]byte, error) {
	ar := msgRequest{
		Model:     req.Model,
		MaxTokens: cfg.MaxTokens,
		Stream:    true,
	}
	if ar.MaxTokens == 0 {
		ar.MaxTokens = 8192
	}

	// Zone A: system block with breakpoint at the end.
	if req.System != "" {
		ar.System = []sysBlock{{
			Type:         "text",
			Text:         req.System,
			CacheControl: &cacheControl{Type: "ephemeral"},
		}}
	}

	// Zone A: tools with breakpoint on the last tool.
	ar.Tools = make([]apiTool, len(req.Tools))
	for i, t := range req.Tools {
		ar.Tools[i] = apiTool{
			Name:        t.Name,
			Description: t.Description,
			InputSchema: t.Parameters,
		}
	}
	if len(ar.Tools) > 0 {
		ar.Tools[len(ar.Tools)-1].CacheControl = &cacheControl{Type: "ephemeral"}
	}

	// Zone B: conversation history. Breakpoint on the last user message.
	ar.Messages = make([]apiMsg, 0, len(req.Messages))
	for i, fm := range req.Messages {
		am, err := protoToAPI(fm.Msg)
		if err != nil {
			return nil, err
		}
		if i == len(req.Messages)-1 && fm.Msg.Role == protocol.RoleUser && len(am.Content) > 0 {
			am.Content[len(am.Content)-1].CacheControl = &cacheControl{Type: "ephemeral"}
		}
		ar.Messages = append(ar.Messages, am)
	}

	// Zone C: volatile tail.
	if len(req.Volatile) > 0 {
		tail, err := volatileToAPI(req.Volatile)
		if err != nil {
			return nil, err
		}
		ar.Messages = append(ar.Messages, tail)
	}

	if budget := thinkingBudget(cfg); budget > 0 {
		ar.Thinking = &thinkCfg{Type: "enabled", BudgetTokens: budget}
	}

	return json.Marshal(ar)
}

func protoToAPI(m protocol.Message) (apiMsg, error) {
	am := apiMsg{Role: string(m.Role)}
	// Anthropic uses "user" for tool results, not "tool".
	if m.Role == protocol.RoleTool {
		am.Role = "user"
	}
	for _, b := range m.Content {
		switch b.Type {
		case protocol.BlockText:
			am.Content = append(am.Content, apiBlock{Type: "text", Text: b.Text})
		case protocol.BlockToolUse:
			if b.ToolUse != nil {
				am.Content = append(am.Content, apiBlock{
					Type:  "tool_use",
					ID:    b.ToolUse.ID,
					Name:  b.ToolUse.Name,
					Input: json.RawMessage(jsonutil.MustMarshalDeterministic(b.ToolUse.Args)),
				})
			}
		case protocol.BlockToolResult:
			if b.ToolResult != nil {
				am.Content = append(am.Content, apiBlock{
					Type:      "tool_result",
					ToolUseID: b.ToolResult.ToolUseID,
					Content:   b.ToolResult.Content,
					IsError:   b.ToolResult.IsError,
				})
			}
		case protocol.BlockImage:
			if b.Image != nil {
				am.Content = append(am.Content, apiBlock{
					Type: "image",
					Source: &imageSource{
						Type:      "base64",
						MediaType: b.Image.MediaType,
						Data:      b.Image.Data,
					},
				})
			}
		}
	}
	return am, nil
}

func volatileToAPI(blocks []protocol.ContentBlock) (apiMsg, error) {
	am := apiMsg{Role: "user"}
	for _, b := range blocks {
		switch b.Type {
		case protocol.BlockText:
			am.Content = append(am.Content, apiBlock{Type: "text", Text: b.Text})
		case protocol.BlockImage:
			if b.Image != nil {
				am.Content = append(am.Content, apiBlock{
					Type: "image",
					Source: &imageSource{
						Type:      "base64",
						MediaType: b.Image.MediaType,
						Data:      b.Image.Data,
					},
				})
			}
		}
	}
	return am, nil
}

func apiToProto(am apiMsg) protocol.Message {
	m := protocol.Message{Role: protocol.Role(am.Role)}
	if am.Role == "user" {
		// Could be a normal user message or a tool-result carrier. Inspect
		// content to decide.
		m.Role = protocol.RoleUser
	}
	for _, b := range am.Content {
		switch b.Type {
		case "text":
			m.Content = append(m.Content, protocol.ContentBlock{Type: protocol.BlockText, Text: b.Text})
		case "tool_use":
			args, _ := b.Input.(map[string]any)
			if args == nil {
				args = map[string]any{}
			}
			m.Content = append(m.Content, protocol.ContentBlock{
				Type: protocol.BlockToolUse,
				ToolUse: &protocol.ToolUse{
					ID:   b.ID,
					Name: b.Name,
					Args: args,
				},
			})
		case "tool_result":
			m.Content = append(m.Content, protocol.ContentBlock{
				Type: protocol.BlockToolResult,
				ToolResult: &protocol.ToolResult{
					ToolUseID: b.ToolUseID,
					Content:   b.Content,
					IsError:   b.IsError,
				},
			})
		}
	}
	return m
}

// thinkingBudget returns the anthropic thinking budget tokens to use.
// The abstract thinking level takes precedence; the numeric budget is the
// deprecated fallback.
func thinkingBudget(cfg Config) int {
	if cfg.ThinkingLevel != "" && cfg.ThinkingLevel != "off" {
		return provider.MapThinkingToAnthropicBudget(cfg.ThinkingLevel)
	}
	return cfg.ThinkingBudget
}
