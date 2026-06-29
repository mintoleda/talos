package protocol

import "encoding/json"

type Role string

const (
	RoleSystem    Role = "system"
	RoleUser      Role = "user"
	RoleAssistant Role = "assistant"
	RoleTool      Role = "tool"
)

type Message struct {
	Role    Role           `json:"role"`
	Content []ContentBlock `json:"content"`
}

type BlockType string

const (
	BlockText       BlockType = "text"
	BlockToolUse    BlockType = "tool_use"
	BlockToolResult BlockType = "tool_result"
	BlockImage      BlockType = "image"
)

// ImageBlock carries a base64-encoded image for multi-modal turns.
type ImageBlock struct {
	MediaType string `json:"media_type"` // e.g. "image/png"
	Data      string `json:"data"`       // base64-encoded bytes
}

type ContentBlock struct {
	Type       BlockType   `json:"type"`
	Text       string      `json:"text,omitempty"`
	ToolUse    *ToolUse    `json:"tool_use,omitempty"`
	ToolResult *ToolResult `json:"tool_result,omitempty"`
	Image      *ImageBlock `json:"image,omitempty"`
}

type ToolUse struct {
	ID   string         `json:"id"`
	Name string         `json:"name"`
	Args map[string]any `json:"args"`
}

type ToolResult struct {
	ToolUseID string `json:"tool_use_id"`
	Content   string `json:"content"`
	IsError   bool   `json:"is_error,omitempty"`
}

func TextMessage(role Role, text string) Message {
	return Message{Role: role, Content: []ContentBlock{{Type: BlockText, Text: text}}}
}

func TextBlocks(text string) []ContentBlock {
	return []ContentBlock{{Type: BlockText, Text: text}}
}

func (m Message) ToolUses() []ToolUse {
	var out []ToolUse
	for _, b := range m.Content {
		if b.Type == BlockToolUse && b.ToolUse != nil {
			out = append(out, *b.ToolUse)
		}
	}
	return out
}

func (m Message) MarshalCanonical() ([]byte, error) {
	return json.Marshal(m)
}
