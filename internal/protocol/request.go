package protocol

import "encoding/json"

type Request struct {
	System        string          `json:"system"`
	Tools         []ToolSchema    `json:"tools"`
	Messages      []FrozenMessage `json:"messages"`
	Volatile      []ContentBlock  `json:"volatile,omitempty"`
	Model         string          `json:"model"`
	ThinkingLevel string          `json:"thinking_level,omitempty"`
}

type FrozenMessage struct {
	Msg Message `json:"msg"`
	Raw []byte  `json:"raw"`
}

type ToolSchema struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	Parameters  json.RawMessage `json:"parameters"`
}

type ProviderEvent interface{ isProviderEvent() }

type PEText struct{ Text string }
type PEToolCall struct{ ToolUse ToolUse }
type PEUsage struct{ Usage Usage }
type PEError struct{ Err error }
type PEDone struct{ StopReason string }

// PEThinking is emitted by providers that support extended thinking (e.g.
// Anthropic). It is display-only and must not be stored in the transcript.
type PEThinking struct{ Text string }

// PEThinkingDone marks a provider-observed reasoning boundary, independently
// of tool-argument assembly or the end of the whole response stream.
type PEThinkingDone struct{}

func (PEText) isProviderEvent()         {}
func (PEToolCall) isProviderEvent()     {}
func (PEUsage) isProviderEvent()        {}
func (PEError) isProviderEvent()        {}
func (PEDone) isProviderEvent()         {}
func (PEThinking) isProviderEvent()     {}
func (PEThinkingDone) isProviderEvent() {}
