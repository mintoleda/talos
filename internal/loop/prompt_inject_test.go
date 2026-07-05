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

	// The transcript's last message must be left untouched — reminders go
	// into Volatile, not into the message content.
	last := req.Messages[len(req.Messages)-1]
	if last.Msg.Role != protocol.RoleUser {
		t.Fatalf("expected last message user, got %s", last.Msg.Role)
	}
	if len(last.Msg.Content) == 0 || last.Msg.Content[0].Type != protocol.BlockText {
		t.Fatal("expected text block")
	}
	if last.Msg.Content[0].Text != "do the thing" {
		t.Fatalf("expected last message content untouched, got: %q", last.Msg.Content[0].Text)
	}

	if len(req.Volatile) != 1 || req.Volatile[0].Type != protocol.BlockText {
		t.Fatalf("expected one volatile text block, got: %+v", req.Volatile)
	}
	if !strings.HasPrefix(req.Volatile[0].Text, "<system-reminder>") {
		t.Fatalf("expected reminder prefix, got: %q", req.Volatile[0].Text)
	}

	frozen := tx.Frozen()
	lastFrozen := frozen[len(frozen)-1]
	if lastFrozen.Msg.Content[0].Text != "do the thing" {
		t.Fatalf("transcript was mutated: %q", lastFrozen.Msg.Content[0].Text)
	}
}

func TestPromptBuilderContextFnAppliesRegardlessOfLastRole(t *testing.T) {
	tx, err := session.Create(filepath.Join(t.TempDir(), "t.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	pb := NewPromptBuilder("sys", nil, "m")
	pb.SetContextFn(func() string { return "<system-reminder>seen</system-reminder>" })
	if err := tx.Append(protocol.TextMessage(protocol.RoleAssistant, "model said hi")); err != nil {
		t.Fatal(err)
	}
	req := pb.Build(tx)

	// Unlike the old last-user-message injection, Volatile is populated
	// regardless of which role produced the last transcript message — the
	// provider translation layer decides how to merge it.
	if len(req.Volatile) != 1 {
		t.Fatalf("expected reminder in Volatile even when last message is assistant, got: %+v", req.Volatile)
	}
	if !strings.Contains(req.Volatile[0].Text, "system-reminder") {
		t.Fatalf("expected reminder text, got: %q", req.Volatile[0].Text)
	}
	// The assistant message itself must be untouched.
	last := req.Messages[len(req.Messages)-1]
	if strings.Contains(last.Msg.Content[0].Text, "system-reminder") {
		t.Fatal("assistant message content must not be mutated")
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
	if len(req.Volatile) != 0 {
		t.Fatalf("expected no volatile blocks, got: %+v", req.Volatile)
	}
}

func TestPromptBuilderNoRemindersWhenNoMessages(t *testing.T) {
	tx, err := session.Create(filepath.Join(t.TempDir(), "t.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	pb := NewPromptBuilder("sys", nil, "m")
	pb.SetContextFn(func() string { return "<system-reminder>seen</system-reminder>" })
	pb.SetPermissionModeText("mode: plan")

	req := pb.Build(tx)
	if len(req.Volatile) != 0 {
		t.Fatalf("expected no volatile blocks with an empty transcript, got: %+v", req.Volatile)
	}
}

func TestPromptBuilderPermissionModeBeforeContext(t *testing.T) {
	tx, err := session.Create(filepath.Join(t.TempDir(), "t.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	pb := NewPromptBuilder("sys", nil, "m")
	pb.SetPermissionModeText("mode: plan")
	pb.SetContextFn(func() string { return "context info" })
	if err := tx.Append(protocol.TextMessage(protocol.RoleUser, "hi")); err != nil {
		t.Fatal(err)
	}
	req := pb.Build(tx)
	if len(req.Volatile) != 1 {
		t.Fatalf("expected one merged volatile block, got: %+v", req.Volatile)
	}
	text := req.Volatile[0].Text
	modeIdx := strings.Index(text, "mode: plan")
	ctxIdx := strings.Index(text, "context info")
	if modeIdx == -1 || ctxIdx == -1 || modeIdx > ctxIdx {
		t.Fatalf("expected permission mode text before context text, got: %q", text)
	}
}
