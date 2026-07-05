package transport

import (
	"encoding/json"
	"testing"
)

func TestClientMsgMarshalInput(t *testing.T) {
	msg := ClientMsg{Type: "input", Text: "hello world"}
	data, err := json.Marshal(msg)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	var decoded ClientMsg
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if decoded.Type != "input" {
		t.Fatalf("expected type=input, got %q", decoded.Type)
	}
	if decoded.Text != "hello world" {
		t.Fatalf("expected text=hello world, got %q", decoded.Text)
	}
}

func TestClientMsgMarshalInterrupt(t *testing.T) {
	msg := ClientMsg{Type: "interrupt"}
	data, err := json.Marshal(msg)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	// Text should be empty/omitted.
	if contains(string(data), "text") {
		t.Fatal("interrupt should not contain text field")
	}
}

func TestClientMsgMarshalApprove(t *testing.T) {
	msg := ClientMsg{Type: "approve", Approved: true}
	data, err := json.Marshal(msg)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	var decoded ClientMsg
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if decoded.Type != "approve" {
		t.Fatalf("expected type=approve, got %q", decoded.Type)
	}
	if !decoded.Approved {
		t.Fatal("expected Approved=true")
	}
}

func TestServerMsgMarshalHello(t *testing.T) {
	msg := ServerMsg{Type: "hello", Version: "0.2.0", Session: "abc"}
	data, err := json.Marshal(msg)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	var decoded ServerMsg
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if decoded.Type != "hello" {
		t.Fatalf("expected type=hello, got %q", decoded.Type)
	}
	if decoded.Version != "0.2.0" {
		t.Fatalf("expected version=0.2.0, got %q", decoded.Version)
	}
	if decoded.Session != "abc" {
		t.Fatalf("expected session=abc, got %q", decoded.Session)
	}
}

func TestServerMsgMarshalEvent(t *testing.T) {
	eventData := json.RawMessage(`{"text":"delta"}`)
	msg := ServerMsg{Type: "event", EType: "TextDelta", Event: eventData}
	data, err := json.Marshal(msg)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	var decoded ServerMsg
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if decoded.Type != "event" {
		t.Fatalf("expected type=event, got %q", decoded.Type)
	}
	if decoded.EType != "TextDelta" {
		t.Fatalf("expected etype=TextDelta, got %q", decoded.EType)
	}
}

func TestServerMsgMarshalError(t *testing.T) {
	msg := ServerMsg{Type: "error", Err: "something went wrong"}
	data, err := json.Marshal(msg)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	var decoded ServerMsg
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if decoded.Type != "error" {
		t.Fatalf("expected type=error, got %q", decoded.Type)
	}
	if decoded.Err != "something went wrong" {
		t.Fatalf("expected err msg, got %q", decoded.Err)
	}
}

func TestClientMsgRoundTrip(t *testing.T) {
	original := ClientMsg{Type: "input", Text: "test message", Approved: false}
	data, err := json.Marshal(original)
	if err != nil {
		t.Fatal(err)
	}

	var decoded ClientMsg
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatal(err)
	}

	if decoded.Type != original.Type {
		t.Fatalf("type: got %q, want %q", decoded.Type, original.Type)
	}
	if decoded.Text != original.Text {
		t.Fatalf("text: got %q, want %q", decoded.Text, original.Text)
	}
}

func TestServerMsgRoundTrip(t *testing.T) {
	original := ServerMsg{
		Type:    "event",
		Version: "1.0",
		Session: "sess-1",
		EType:   "Notice",
		Event:   json.RawMessage(`{"level":"info","text":"hi"}`),
	}
	data, err := json.Marshal(original)
	if err != nil {
		t.Fatal(err)
	}

	var decoded ServerMsg
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatal(err)
	}

	if decoded.Type != original.Type {
		t.Fatalf("type: got %q, want %q", decoded.Type, original.Type)
	}
	if decoded.EType != original.EType {
		t.Fatalf("etype: got %q, want %q", decoded.EType, original.EType)
	}
}

func contains(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}
