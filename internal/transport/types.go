package transport

import "encoding/json"

type ClientMsg struct {
	Type     string          `json:"type"` // "input" | "interrupt" | "approve"
	Text     string          `json:"text,omitempty"`
	Approved bool            `json:"approved,omitempty"`
	Plan     json.RawMessage `json:"plan,omitempty"`
}

type ServerMsg struct {
	Type    string          `json:"type"` // "hello" | "event" | "error"
	Version string          `json:"version,omitempty"`
	Session string          `json:"session,omitempty"`
	EType   string          `json:"etype,omitempty"`
	Event   json.RawMessage `json:"event,omitempty"`
	Err     string          `json:"err,omitempty"`
}
