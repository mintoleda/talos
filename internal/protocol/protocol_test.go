package protocol

import (
	"encoding/json"
	"testing"
)

func TestMessageRoundTrip(t *testing.T) {
	m := Message{
		Role: RoleAssistant,
		Content: []ContentBlock{
			{Type: BlockText, Text: "hello"},
			{Type: BlockToolUse, ToolUse: &ToolUse{ID: "1", Name: "read", Args: map[string]any{"path": "x.go"}}},
		},
	}
	b, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	var m2 Message
	if err := json.Unmarshal(b, &m2); err != nil {
		t.Fatal(err)
	}
	if len(m2.Content) != 2 {
		t.Fatalf("expected 2 blocks, got %d", len(m2.Content))
	}
	if m2.Content[1].ToolUse.Args["path"] != "x.go" {
		t.Fatalf("args lost: %v", m2.Content[1].ToolUse.Args)
	}
}

func TestToolUses(t *testing.T) {
	m := Message{Role: RoleAssistant, Content: []ContentBlock{
		{Type: BlockToolUse, ToolUse: &ToolUse{ID: "a", Name: "read", Args: nil}},
		{Type: BlockText, Text: "x"},
	}}
	uses := m.ToolUses()
	if len(uses) != 1 || uses[0].ID != "a" {
		t.Fatalf("expected one tool use, got %v", uses)
	}
}

// TestEventRoundTrip exhaustively tests that every event type can be
// marshalled and unmarshalled without error, including the special
// SubagentEvent recursive encoding.
func TestEventRoundTrip(t *testing.T) {
	// Build one instance of every event type.  PermissionRequested is tested
	// separately because ReplyCh cannot be marshalled.
	tests := []struct {
		name string
		ev   Event
	}{
		{"BatchStarted", BatchStarted{Num: 3}},
		{"BatchFinished", BatchFinished{Num: 3}},
		{"TextDelta", TextDelta{Text: "hello"}},
		{"ToolStarted", ToolStarted{ID: "t1", Name: "read", Args: map[string]any{"path": "x.go"}}},
		{"ToolFinished", ToolFinished{ID: "t1", Result: ToolResult{ToolUseID: "t1", Content: "ok"}}},
		{"ToolOutputDelta", ToolOutputDelta{ID: "t1", Text: "chunk"}},
		{"Notice", Notice{Level: "info", Text: "test"}},
		{"TurnEnded", TurnEnded{StopReason: "stop", Usage: Usage{PromptTokens: 10, CompletionTokens: 5}}},
		{"SubagentStarted", SubagentStarted{ID: "s1", Agent: "scout", Task: "do thing"}},
		{"PromptEstimate", PromptEstimate{PromptTokens: 100, ContextLimit: 8192}},
		{"SubagentFinished", SubagentFinished{ID: "s1", Agent: "scout", Result: "done", IsError: false, Usage: SubagentUsage{InputTokens: 10, OutputTokens: 5, Cost: 0.001}}},
		{"ModelChanged", ModelChanged{Provider: "openai", Model: "gpt-4", ThinkingLevel: "off"}},
		{"PermissionModeChanged", PermissionModeChanged{Mode: "ask"}},
		{"UserInput", UserInput{Text: "hello"}},
		{"ThinkingBlock", ThinkingBlock{Text: "...thinking..."}},
		{"ThinkingDelta", ThinkingDelta{Text: "..."}},
		{"EngineSnapshot", EngineSnapshot{Busy: true, StreamedText: "hello", ActiveTools: []ToolSnapshot{{ID: "t1", Name: "read", Args: map[string]any{"path": "x.go"}}}}},
		{"SessionStatus", SessionStatus{ID: "s1", State: "busy", Preview: "hello", Dir: "/tmp/proj"}},
		{"SubagentEvent with TextDelta", SubagentEvent{ID: "s1", Agent: "scout", Inner: TextDelta{Text: "hello"}}},
		{"SubagentEvent with ToolStarted", SubagentEvent{ID: "s1", Agent: "scout", Inner: ToolStarted{ID: "t1", Name: "read"}}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			etype, raw, err := MarshalEvent(tt.ev)
			if err != nil {
				t.Fatalf("MarshalEvent: %v", err)
			}
			if etype == "" {
				t.Fatal("empty etype")
			}
			if len(raw) == 0 {
				t.Fatal("empty raw bytes")
			}

			ev2, err := UnmarshalEvent(etype, raw)
			if err != nil {
				t.Fatalf("UnmarshalEvent: %v", err)
			}
			if ev2 == nil {
				t.Fatal("UnmarshalEvent returned nil")
			}

			// Re-marshal the decoded event and compare etype.
			etype2, raw2, err := MarshalEvent(ev2)
			if err != nil {
				t.Fatalf("re-MarshalEvent: %v", err)
			}
			if etype2 != etype {
				t.Fatalf("etype changed: %q → %q", etype, etype2)
			}
			if string(raw2) != string(raw) {
				t.Fatalf("raw changed:\n  %s\n  %s", string(raw), string(raw2))
			}
		})
	}
}

// TestPermissionRequestedWire tests that PermissionRequested marshals without
// the ReplyCh field (it has json:"-").
func TestPermissionRequestedWire(t *testing.T) {
	ch := make(chan<- bool, 1)
	ev := PermissionRequested{ToolName: "bash", Command: "ls", Reason: "test", ReplyCh: ch}
	etype, raw, err := MarshalEvent(ev)
	if err != nil {
		t.Fatalf("MarshalEvent: %v", err)
	}
	if etype != "permission_requested" {
		t.Fatalf("expected permission_requested, got %s", etype)
	}

	// Decode into a map to verify fields.
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatalf("json.Unmarshal: %v", err)
	}
	if m["tool_name"] != "bash" {
		t.Fatalf("expected tool_name=bash, got %v", m["tool_name"])
	}
	if _, exists := m["reply_ch"]; exists {
		t.Fatal("reply_ch should not be in wire JSON")
	}
}

// TestSubagentEventRoundTripDeep tests deep nesting of SubagentEvent.
func TestSubagentEventRoundTripDeep(t *testing.T) {
	inner := SubagentEvent{
		ID:    "s2",
		Agent: "nested",
		Inner: TextDelta{Text: "nested text"},
	}
	outer := SubagentEvent{
		ID:    "s1",
		Agent: "outer",
		Inner: inner,
	}
	etype, raw, err := MarshalEvent(outer)
	if err != nil {
		t.Fatalf("MarshalEvent: %v", err)
	}
	ev, err := UnmarshalEvent(etype, raw)
	if err != nil {
		t.Fatalf("UnmarshalEvent: %v", err)
	}
	se, ok := ev.(SubagentEvent)
	if !ok {
		t.Fatal("expected SubagentEvent")
	}
	if se.ID != "s1" || se.Agent != "outer" {
		t.Fatalf("outer fields: %+v", se)
	}
	se2, ok := se.Inner.(SubagentEvent)
	if !ok {
		t.Fatal("expected nested SubagentEvent")
	}
	if se2.ID != "s2" || se2.Agent != "nested" {
		t.Fatalf("inner fields: %+v", se2)
	}
	if _, ok := se2.Inner.(TextDelta); !ok {
		t.Fatal("expected innermost TextDelta")
	}
}
