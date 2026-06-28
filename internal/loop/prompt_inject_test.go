package loop

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/mintoleda/talos/internal/protocol"
	"github.com/mintoleda/talos/internal/session"
)

func TestPromptBuilderInjectsContextFn(t *testing.T) {
	tx, err := session.Create(filepath.Join(t.TempDir(), "t.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	pb := NewPromptBuilder("sys", nil, "m")
	pb.SetContextFn(func() string {
		return "<system-reminder>\nFiles read: a.go, b.go\n</system-reminder>"
	})
	if err := tx.Append(protocol.TextMessage(protocol.RoleUser, "do the thing")); err != nil {
		t.Fatal(err)
	}
	req := pb.Build(tx)
	last := req.Messages[len(req.Messages)-1]
	if last.Msg.Role != protocol.RoleUser {
		t.Fatalf("expected last message user, got %s", last.Msg.Role)
	}
	if len(last.Msg.Content) == 0 || last.Msg.Content[0].Type != protocol.BlockText {
		t.Fatal("expected text block")
	}
	got := last.Msg.Content[0].Text
	if !strings.HasPrefix(got, "<system-reminder>") {
		t.Fatalf("expected reminder prefix, got: %q", got)
	}
	if !strings.Contains(got, "do the thing") {
		t.Fatalf("expected user content preserved, got: %q", got)
	}

	// The transcript itself must be unchanged.
	frozen := tx.Frozen()
	lastFrozen := frozen[len(frozen)-1]
	if lastFrozen.Msg.Content[0].Text != "do the thing" {
		t.Fatalf("transcript was mutated: %q", lastFrozen.Msg.Content[0].Text)
	}
}

func TestPromptBuilderContextFnSkipsNonUserLast(t *testing.T) {
	tx, err := session.Create(filepath.Join(t.TempDir(), "t.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	pb := NewPromptBuilder("sys", nil, "m")
	pb.SetContextFn(func() string { return "<system-reminder>never seen</system-reminder>" })
	if err := tx.Append(protocol.TextMessage(protocol.RoleAssistant, "model said hi")); err != nil {
		t.Fatal(err)
	}
	req := pb.Build(tx)
	if strings.Contains(req.Messages[len(req.Messages)-1].Msg.Content[0].Text, "system-reminder") {
		t.Fatal("should not inject when last message is assistant")
	}
}

func TestPromptBuilderContextFnEmptyDoesNotMutate(t *testing.T) {
	tx, err := session.Create(filepath.Join(t.TempDir(), "t.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	pb := NewPromptBuilder("sys", nil, "m")
	pb.SetContextFn(func() string { return "" })
	if err := tx.Append(protocol.TextMessage(protocol.RoleUser, "hi")); err != nil {
		t.Fatal(err)
	}
	req := pb.Build(tx)
	if req.Messages[len(req.Messages)-1].Msg.Content[0].Text != "hi" {
		t.Fatal("empty reminder should leave text alone")
	}
}
