package anthropic

import "encoding/json"

// --- request ---

type msgRequest struct {
	Model     string     `json:"model"`
	MaxTokens int        `json:"max_tokens"`
	System    []sysBlock `json:"system"`
	Messages  []apiMsg   `json:"messages"`
	Tools     []apiTool  `json:"tools,omitempty"`
	Stream    bool       `json:"stream"`
	Thinking  *thinkCfg  `json:"thinking,omitempty"`
}

type sysBlock struct {
	Type         string        `json:"type"`
	Text         string        `json:"text"`
	CacheControl *cacheControl `json:"cache_control,omitempty"`
}

type cacheControl struct {
	Type string `json:"type"`
} // {"type":"ephemeral"}

type apiMsg struct {
	Role    string     `json:"role"`
	Content []apiBlock `json:"content"`
}

type imageSource struct {
	Type      string `json:"type"`
	MediaType string `json:"media_type"`
	Data      string `json:"data"`
}

type apiBlock struct {
	Type         string        `json:"type"`
	Text         string        `json:"text,omitempty"`
	ID           string        `json:"id,omitempty"`
	Name         string        `json:"name,omitempty"`
	Input        any           `json:"input,omitempty"`
	ToolUseID    string        `json:"tool_use_id,omitempty"`
	Content      string        `json:"content,omitempty"`
	IsError      bool          `json:"is_error,omitempty"`
	Thinking     string        `json:"thinking,omitempty"`
	Source       *imageSource  `json:"source,omitempty"`
	CacheControl *cacheControl `json:"cache_control,omitempty"`
}

type apiTool struct {
	Name         string          `json:"name"`
	Description  string          `json:"description"`
	InputSchema  json.RawMessage `json:"input_schema"`
	CacheControl *cacheControl   `json:"cache_control,omitempty"`
}

type thinkCfg struct {
	Type         string `json:"type"`
	BudgetTokens int    `json:"budget_tokens"`
}

// --- SSE events ---

type sseEvent struct {
	Type         string    `json:"type"`
	Index        int       `json:"index"`
	Delta        *sseDelta `json:"delta,omitempty"`
	Usage        *sseUsage `json:"usage,omitempty"`
	ContentBlock *apiBlock `json:"content_block,omitempty"`
}

type sseDelta struct {
	Type        string `json:"type"`
	Text        string `json:"text,omitempty"`
	PartialJSON string `json:"partial_json,omitempty"`
	Thinking    string `json:"thinking,omitempty"`
}

type sseUsage struct {
	InputTokens              int `json:"input_tokens"`
	OutputTokens             int `json:"output_tokens"`
	CacheCreationInputTokens int `json:"cache_creation_input_tokens"`
	CacheReadInputTokens     int `json:"cache_read_input_tokens"`
}

// --- non-streaming response ---

type msgResponse struct {
	Content []apiBlock `json:"content"`
	Usage   *sseUsage  `json:"usage"`
}
