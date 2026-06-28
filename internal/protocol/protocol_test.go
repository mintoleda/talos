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
