package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"

	"github.com/mintoleda/talos/internal/protocol"
)

// SkillTool lets the LLM load a skill by name instead of by full path.
type SkillTool struct {
	byName map[string]string // skill name -> full path to SKILL.md
	dirs   []string          // for error messages
}

// NewSkillTool creates a skill tool that searches the given directories.
// Each directory is scanned for subdirectories containing SKILL.md.
func NewSkillTool(dirs []string) *SkillTool {
	byName := make(map[string]string)
	for _, dir := range dirs {
		entries, err := os.ReadDir(dir)
		if err != nil {
			continue
		}
		for _, e := range entries {
			if !e.IsDir() {
				continue
			}
			name := e.Name()
			if len(name) > 0 && (name[0] == '.' || name[0] == '_') {
				continue
			}
			path := filepath.Join(dir, name, "SKILL.md")
			if _, err := os.Stat(path); err != nil {
				continue
			}
			byName[name] = path
		}
	}
	return &SkillTool{byName: byName, dirs: dirs}
}

func (t *SkillTool) Name() string        { return "skill" }
func (t *SkillTool) Description() string { return "Load a skill by name. Returns the full SKILL.md content. Use when the task matches one of the Available Skills listed in the system prompt." }

func (t *SkillTool) Schema() json.RawMessage {
	// Build a sorted list of available names for the enum.
	names := make([]string, 0, len(t.byName))
	for n := range t.byName {
		names = append(names, n)
	}
	sort.Strings(names)

	schema := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"name": map[string]any{
				"type":        "string",
				"description": "Name of the skill to load (e.g. frontend-design, search-models)",
			},
		},
		"required": []string{"name"},
	}
	if len(names) > 0 {
		props := schema["properties"].(map[string]any)
		nameProps := props["name"].(map[string]any)
		nameProps["enum"] = names
	}
	b, _ := json.Marshal(schema)
	return json.RawMessage(b)
}

func (t *SkillTool) Execute(ctx context.Context, args map[string]any) (protocol.ToolResult, error) {
	name, err := str(args, "name")
	if err != nil {
		return errResult(err), nil
	}
	path, ok := t.byName[name]
	if !ok {
		var available []string
		for n := range t.byName {
			available = append(available, n)
		}
		sort.Strings(available)
		return errResult(fmt.Errorf("skill %q not found; available: %v", name, available)), nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return errResult(fmt.Errorf("skill %s: %w", name, err)), nil
	}
	return protocol.ToolResult{Content: string(data)}, nil
}

func (t *SkillTool) ValidNames() []string {
	names := make([]string, 0, len(t.byName))
	for n := range t.byName {
		names = append(names, n)
	}
	sort.Strings(names)
	return names
}
