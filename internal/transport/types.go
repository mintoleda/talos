package transport

import "encoding/json"

type ClientMsg struct {
	Type     string          `json:"type"` // "auth" | "input" | "steer" | "interrupt" | "approve" | "request" | "subscribe" | "unsubscribe"
	Text     string          `json:"text,omitempty"`
	Approved bool            `json:"approved,omitempty"`
	Plan     json.RawMessage `json:"plan,omitempty"`
	ID       uint64          `json:"id,omitempty"`
	Method   string          `json:"method,omitempty"`
	Params   json.RawMessage `json:"params,omitempty"`
	Token    string          `json:"token,omitempty"`
	Session  string          `json:"session,omitempty"` // target session; "" = connection default
}

type ServerMsg struct {
	Type    string          `json:"type"` // "hello" | "event" | "error" | "response"
	Version string          `json:"version,omitempty"`
	Session string          `json:"session,omitempty"` // originating session for events/errors; "" on multi-session hello
	EType   string          `json:"etype,omitempty"`
	Event   json.RawMessage `json:"event,omitempty"`
	ID      uint64          `json:"id,omitempty"`
	Result  json.RawMessage `json:"result,omitempty"`
	Err     string          `json:"err,omitempty"`
}
