package loop

import (
	"testing"

	"github.com/mintoleda/talos/internal/protocol"
	"github.com/mintoleda/talos/internal/session"
)

func TestPromptByteStability(t *testing.T) {
	system := "You are a helpful assistant."
	tools := []protocol.ToolSchema{
		{Name: "read", Description: "Read a file", Parameters: []byte(`{"type":"object"}`)},
		{Name: "write", Description: "Write a file", Parameters: []byte(`{"type":"object"}`)},
	}

	pb1 := NewPromptBuilder(system, tools, "test-model")
	pb2 := NewPromptBuilder(system, tools, "test-model")

	tx1, _ := session.Create(t.TempDir() + "/t1.jsonl")
	tx2, _ := session.Create(t.TempDir() + "/t2.jsonl")

	req1 := pb1.Build(tx1)
	req2 := pb2.Build(tx2)

	h1 := pb1.PrefixHash(req1)
	h2 := pb2.PrefixHash(req2)

	if h1 != h2 {
		t.Fatalf("prefix hashes diverge for identical inputs: %q vs %q", h1, h2)
	}

	s1 := pb1.Shape(req1, tx1)
	s2 := pb2.Shape(req2, tx2)

	if s1.SystemHash != s2.SystemHash {
		t.Fatalf("system hashes diverge: %q vs %q", s1.SystemHash, s2.SystemHash)
	}
	if s1.ToolsHash != s2.ToolsHash {
		t.Fatalf("tools hashes diverge: %q vs %q", s1.ToolsHash, s2.ToolsHash)
	}
	if s1.MsgHash != s2.MsgHash {
		t.Fatalf("message hashes diverge: %q vs %q", s1.MsgHash, s2.MsgHash)
	}
}

func TestPromptStabilityWithOneMessage(t *testing.T) {
	system := "You are a helpful assistant."
	tools := []protocol.ToolSchema{
		{Name: "read", Description: "Read a file", Parameters: []byte(`{"type":"object"}`)},
	}

	pb1 := NewPromptBuilder(system, tools, "test-model")
	pb2 := NewPromptBuilder(system, tools, "test-model")

	tx1, _ := session.Create(t.TempDir() + "/t1.jsonl")
	tx2, _ := session.Create(t.TempDir() + "/t2.jsonl")

	msg := protocol.TextMessage(protocol.RoleUser, "hello")
	tx1.Append(msg)
	tx2.Append(msg)

	req1 := pb1.Build(tx1)
	req2 := pb2.Build(tx2)

	if pb1.PrefixHash(req1) != pb2.PrefixHash(req2) {
		t.Fatal("prefix hashes diverge with identical single message")
	}
}

func TestCacheMissCause(t *testing.T) {
	pb := NewPromptBuilder("", nil, "")

	t.Run("identical", func(t *testing.T) {
		shape := PrefixShape{SystemHash: "abc", ToolsHash: "def", MsgHash: "ghi", RewriteVer: 1}
		cause := pb.CacheMissCause(shape, shape)
		if cause != "" {
			t.Fatalf("expected empty cause for identical, got %q", cause)
		}
	})

	t.Run("system change", func(t *testing.T) {
		prev := PrefixShape{SystemHash: "abc", ToolsHash: "def", MsgHash: "ghi"}
		cur := PrefixShape{SystemHash: "xyz", ToolsHash: "def", MsgHash: "ghi"}
		if got := pb.CacheMissCause(prev, cur); got != "system" {
			t.Fatalf("expected 'system', got %q", got)
		}
	})

	t.Run("tools change", func(t *testing.T) {
		prev := PrefixShape{SystemHash: "abc", ToolsHash: "def", MsgHash: "ghi"}
		cur := PrefixShape{SystemHash: "abc", ToolsHash: "xyz", MsgHash: "ghi"}
		if got := pb.CacheMissCause(prev, cur); got != "tools" {
			t.Fatalf("expected 'tools', got %q", got)
		}
	})

	t.Run("rewrite version bump", func(t *testing.T) {
		prev := PrefixShape{SystemHash: "abc", ToolsHash: "def", MsgHash: "ghi", RewriteVer: 1}
		cur := PrefixShape{SystemHash: "abc", ToolsHash: "def", MsgHash: "ghi", RewriteVer: 2}
		if got := pb.CacheMissCause(prev, cur); got != "history_rewrite" {
			t.Fatalf("expected 'history_rewrite', got %q", got)
		}
	})

	t.Run("msg hash change", func(t *testing.T) {
		prev := PrefixShape{SystemHash: "abc", ToolsHash: "def", MsgHash: "ghi"}
		cur := PrefixShape{SystemHash: "abc", ToolsHash: "def", MsgHash: "jkl"}
		if got := pb.CacheMissCause(prev, cur); got != "history_rewrite" {
			t.Fatalf("expected 'history_rewrite', got %q", got)
		}
	})
}
