package codex

import "encoding/json"

// responsesRequest is the body POSTed to /codex/responses. It mirrors pi's
// buildRequestBody: stateless (store=false) with encrypted reasoning
// included, and the system prompt carried in instructions.
type responsesRequest struct {
	Model             string         `json:"model"`
	Store             bool           `json:"store"`
	Stream            bool           `json:"stream"`
	Instructions      string         `json:"instructions"`
	Input             []inputItem    `json:"input"`
	Text              textOptions    `json:"text"`
	Include           []string       `json:"include"`
	PromptCacheKey    string         `json:"prompt_cache_key,omitempty"`
	ToolChoice        string         `json:"tool_choice"`
	ParallelToolCalls bool           `json:"parallel_tool_calls"`
	Tools             []functionTool `json:"tools,omitempty"`
	Reasoning         *reasoning     `json:"reasoning,omitempty"`
}

type textOptions struct {
	Verbosity string `json:"verbosity"`
}

type reasoning struct {
	Effort  string `json:"effort"`
	Summary string `json:"summary,omitempty"`
}

// inputItem is the union of the Responses input item shapes talos sends:
// user/developer messages (Role+Content), assistant "message" items,
// "function_call" and "function_call_output".
type inputItem struct {
	Type      string        `json:"type,omitempty"`
	Role      string        `json:"role,omitempty"`
	Content   []contentPart `json:"content,omitempty"`
	Status    string        `json:"status,omitempty"`
	ID        string        `json:"id,omitempty"`
	CallID    string        `json:"call_id,omitempty"`
	Name      string        `json:"name,omitempty"`
	Arguments string        `json:"arguments,omitempty"`
	Output    *string       `json:"output,omitempty"`
}

type contentPart struct {
	Type        string          `json:"type"`
	Text        string          `json:"text,omitempty"`
	ImageURL    string          `json:"image_url,omitempty"`
	Detail      string          `json:"detail,omitempty"`
	Annotations json.RawMessage `json:"annotations,omitempty"`
}

type functionTool struct {
	Type        string          `json:"type"`
	Name        string          `json:"name"`
	Description string          `json:"description"`
	Parameters  json.RawMessage `json:"parameters"`
}

// streamEvent covers every SSE event field the parser reads.
type streamEvent struct {
	Type        string       `json:"type"`
	OutputIndex int          `json:"output_index"`
	Delta       string       `json:"delta"`
	Arguments   string       `json:"arguments"`
	Item        *outputItem  `json:"item"`
	Response    *responseObj `json:"response"`
	Code        any          `json:"code"`
	Message     any          `json:"message"`
	Error       *apiError    `json:"error"`
}

type outputItem struct {
	Type      string     `json:"type"`
	ID        string     `json:"id"`
	CallID    string     `json:"call_id"`
	Name      string     `json:"name"`
	Arguments string     `json:"arguments"`
	Summary   []textPart `json:"summary"`
	Content   []struct {
		Type    string `json:"type"`
		Text    string `json:"text"`
		Refusal string `json:"refusal"`
	} `json:"content"`
}

type textPart struct {
	Text string `json:"text"`
}

type responseObj struct {
	ID                string    `json:"id"`
	Status            string    `json:"status"`
	Usage             *usageObj `json:"usage"`
	Error             *apiError `json:"error"`
	IncompleteDetails *struct {
		Reason string `json:"reason"`
	} `json:"incomplete_details"`
}

type usageObj struct {
	InputTokens        int `json:"input_tokens"`
	OutputTokens       int `json:"output_tokens"`
	TotalTokens        int `json:"total_tokens"`
	InputTokensDetails *struct {
		CachedTokens     int `json:"cached_tokens"`
		CacheWriteTokens int `json:"cache_write_tokens"`
	} `json:"input_tokens_details"`
}

type apiError struct {
	Code    any    `json:"code"`
	Type    string `json:"type"`
	Message string `json:"message"`
}
