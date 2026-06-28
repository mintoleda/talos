package tools

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestSkillTool(t *testing.T) {
	dir := t.TempDir()

	// Create a skill directory with SKILL.md
	sub := filepath.Join(dir, "test-skill")
	os.MkdirAll(sub, 0755)
	os.WriteFile(filepath.Join(sub, "SKILL.md"), []byte("# Test Skill\n\nDo the thing."), 0644)

	// Create a non-skill directory (no SKILL.md)
	os.MkdirAll(filepath.Join(dir, "empty"), 0755)

	// Create a hidden directory
	os.MkdirAll(filepath.Join(dir, ".hidden"), 0755)
	os.WriteFile(filepath.Join(dir, ".hidden", "SKILL.md"), []byte("hidden"), 0644)

	tool := NewSkillTool([]string{dir})

	if tool.Name() != "skill" {
		t.Errorf("expected name 'skill', got %s", tool.Name())
	}

	// Valid invocation
	res, err := tool.Execute(context.Background(), map[string]any{"name": "test-skill"})
	if err != nil {
		t.Fatal(err)
	}
	if res.IsError {
		t.Fatalf("unexpected error: %s", res.Content)
	}
	if res.Content != "# Test Skill\n\nDo the thing." {
		t.Errorf("unexpected content: %q", res.Content)
	}

	// Unknown skill
	res, _ = tool.Execute(context.Background(), map[string]any{"name": "missing"})
	if !res.IsError {
		t.Error("expected error for missing skill")
	}
}

func TestSkillToolSchemaEnum(t *testing.T) {
	dir := t.TempDir()
	os.MkdirAll(filepath.Join(dir, "alpha"), 0755)
	os.WriteFile(filepath.Join(dir, "alpha", "SKILL.md"), []byte("alpha"), 0644)
	os.MkdirAll(filepath.Join(dir, "beta"), 0755)
	os.WriteFile(filepath.Join(dir, "beta", "SKILL.md"), []byte("beta"), 0644)

	tool := NewSkillTool([]string{dir})
	names := tool.ValidNames()
	if len(names) != 2 {
		t.Fatalf("expected 2 skills, got %d", len(names))
	}
	if names[0] != "alpha" || names[1] != "beta" {
		t.Errorf("unexpected names: %v", names)
	}
}
