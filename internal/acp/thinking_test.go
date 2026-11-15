package acp

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"github.com/mintoleda/talos/internal/protocol"
)

func TestThinkingCompletionCarriesBoundaryWithoutDuplicatingText(t *testing.T) {
	var output bytes.Buffer
	s := &acpSession{id: "test-session", agent: &Agent{conn: NewConn(nil, &output, nil)}}
	turn := &turn{s: s}
	turn.emit(protocol.ThinkingDelta{Text: "plan"})
	turn.emit(protocol.ThinkingBlock{Text: "plan"})
	if !strings.Contains(output.String(), `"text":""`) {
		t.Fatal("empty text content must retain its required text field")
	}
	lines := strings.Split(strings.TrimSpace(output.String()), "\n")
	if len(lines) != 2 {
		t.Fatalf("updates = %d, want 2", len(lines))
	}
	var first, last struct {
		Params SessionNotification `json:"params"`
	}
	if err := json.Unmarshal([]byte(lines[0]), &first); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal([]byte(lines[1]), &last); err != nil {
		t.Fatal(err)
	}
	if first.Params.Update.Content.Text != "plan" {
		t.Fatalf("delta = %+v", first)
	}
	update := last.Params.Update
	if update.SessionUpdate != "agent_thought_chunk" || update.Content == nil || update.Content.Text != "" {
		t.Fatalf("completion must not repeat thought text: %+v", update)
	}
	meta, _ := update.Meta["talos"].(map[string]any)
	if meta["thoughtComplete"] != true {
		t.Fatalf("missing completion marker: %+v", update.Meta)
	}
}
