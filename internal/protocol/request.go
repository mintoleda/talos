package protocol

import "encoding/json"

type Request struct {
	System        string
	Tools         []ToolSchema
	Messages      []FrozenMessage
	Volatile      []ContentBlock
	Model         string
	ThinkingLevel string
}

type FrozenMessage struct {
	Msg Message
	Raw []byte
}

type ToolSchema struct {
	Name        string
	Description string
	Parameters  json.RawMessage
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

func (PEText) isProviderEvent()     {}
func (PEToolCall) isProviderEvent() {}
func (PEUsage) isProviderEvent()    {}
func (PEError) isProviderEvent()    {}
func (PEDone) isProviderEvent()     {}
func (PEThinking) isProviderEvent() {}
