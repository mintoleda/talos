package safety

import (
	"path/filepath"
	"testing"

	"github.com/mintoleda/talos/internal/protocol"
)

func writeTU(path string) protocol.ToolUse {
	return protocol.ToolUse{Name: "write", Args: map[string]any{"path": path}}
}

func bashTU(cmd string) protocol.ToolUse {
	return protocol.ToolUse{Name: "bash", Args: map[string]any{"command": cmd}}
}

func TestPathBoundary(t *testing.T) {
	root := t.TempDir()
	p := NewPolicy(ModeAuto, root, NewClassifier(), true)

	in := filepath.Join(root, "sub", "f.go")
	if d, _ := p.Check(writeTU(in)); d != Allow {
		t.Fatalf("in-tree write should be allowed, got %v", d)
	}
	for _, out := range []string{
		filepath.Join(root, "..", "escape.go"),
		"/etc/passwd",
		filepath.Join(root, "sub", "..", "..", "escape.go"),
	} {
		if d, _ := p.Check(writeTU(out)); d != Block {
			t.Fatalf("out-of-tree write %q should be blocked, got %v", out, d)
		}
	}
}

func TestAutoAllowAndHeadlessBlock(t *testing.T) {
	root := t.TempDir()
	dangerous := "sudo rm something"

	pi := NewPolicy(ModeAuto, root, NewClassifier(), true)
	if d, _ := pi.Check(bashTU(dangerous)); d != Allow {
		t.Fatalf("auto+interactive dangerous bash should auto-allow, got %v", d)
	}

	ph := NewPolicy(ModeAuto, root, NewClassifier(), false)
	if d, _ := ph.Check(bashTU(dangerous)); d != Block {
		t.Fatalf("auto+headless dangerous bash should block, got %v", d)
	}

	// catastrophic always blocks regardless of mode/interactivity.
	for _, p := range []*Policy{pi, ph} {
		if d, _ := p.Check(bashTU("mkfs.ext4 /dev/sda")); d != Block {
			t.Fatalf("catastrophic bash should always block, got %v", d)
		}
	}
}

func TestAskModePrompts(t *testing.T) {
	root := t.TempDir()
	pi := NewPolicy(ModeAsk, root, NewClassifier(), true)
	if d, _ := pi.Check(bashTU("git reset --hard")); d != Prompt {
		t.Fatalf("ask+interactive dangerous bash should prompt, got %v", d)
	}
	ph := NewPolicy(ModeAsk, root, NewClassifier(), false)
	if d, _ := ph.Check(bashTU("git reset --hard")); d != Block {
		t.Fatalf("ask+headless dangerous bash should block, got %v", d)
	}
}
