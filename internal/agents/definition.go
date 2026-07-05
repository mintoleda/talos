// Package agents implements markdown-defined subagents: small, single-purpose
// agents the primary agent can delegate to as ordinary tools. Each agent is a
// .md file whose YAML frontmatter declares its name, description, allowed
// tools, the subagents it may itself spawn, and its model/thinking level; the
// body is the agent's system prompt.
//
// Definitions are loaded from three layers, later overriding earlier by name:
//
//	embedded builtin/*.md      (scout, researcher, worker — model inherits from caller)
//	~/.talos/subagents/*.md    (global, user-defined)
//	<repo>/.talos/subagents/*.md (project-local)
package agents

import (
	"embed"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"
)

//go:embed builtin/*.md
var builtinFS embed.FS

// Definition is a fully-parsed agent loadout.
type Definition struct {
	Name        string   // unique agent name; also the spawn tool's name
	Description string   // one-line summary shown to the calling agent
	Tools       []string // allowed tool names ("*" or "all" = every tool)
	Subagents   []string // names of agents this agent may itself spawn
	Provider    string   // provider override (empty = inherit caller's provider)
	Model       string   // model override (empty = inherit caller's model); also accepts "provider/model"
	Thinking    string   // thinking level override (empty = inherit)
	Prompt      string   // system prompt (markdown body after frontmatter)
	Path        string   // source path ("<builtin>" for embedded)
}

// frontmatter mirrors the YAML header of an agent markdown file.
type frontmatter struct {
	Name        string   `yaml:"name"`
	Description string   `yaml:"description"`
	Tools       []string `yaml:"tools"`
	Subagents   []string `yaml:"subagents"`
	Provider    string   `yaml:"provider"`
	Model       string   `yaml:"model"`
	Thinking    string   `yaml:"thinking"`
}

// Dir is a directory to scan for agent markdown files.
type Dir struct {
	Path  string
	Label string // diagnostic only
}

// Load builds the agent set: embedded builtins first, then each dir in order,
// later definitions overriding earlier ones with the same name.
func Load(dirs []Dir) (map[string]Definition, error) {
	out := map[string]Definition{}

	entries, err := builtinFS.ReadDir("builtin")
	if err != nil {
		return nil, fmt.Errorf("read builtin agents: %w", err)
	}
	for _, e := range entries {
		data, err := builtinFS.ReadFile("builtin/" + e.Name())
		if err != nil {
			continue
		}
		if d, err := parse(e.Name(), data, "<builtin>"); err == nil {
			out[d.Name] = d
		}
	}

	for _, dir := range dirs {
		des, err := os.ReadDir(dir.Path)
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return nil, fmt.Errorf("scan agents %s: %w", dir.Path, err)
		}
		for _, e := range des {
			name := e.Name()
			if e.IsDir() || !strings.HasSuffix(name, ".md") ||
				strings.HasPrefix(name, ".") || strings.HasPrefix(name, "_") {
				continue
			}
			p := filepath.Join(dir.Path, name)
			data, err := os.ReadFile(p)
			if err != nil {
				continue
			}
			d, err := parse(name, data, p)
			if err != nil {
				return nil, err
			}
			out[d.Name] = d
		}
	}
	return out, nil
}

// parse splits a markdown file into its YAML frontmatter and prompt body.
func parse(filename string, data []byte, path string) (Definition, error) {
	yamlPart, body := splitFrontmatter(string(data))
	var fm frontmatter
	if yamlPart != "" {
		if err := yaml.Unmarshal([]byte(yamlPart), &fm); err != nil {
			return Definition{}, fmt.Errorf("agent %s: parse frontmatter: %w", filename, err)
		}
	}
	name := strings.TrimSpace(fm.Name)
	if name == "" {
		name = strings.TrimSuffix(filename, ".md")
	}
	model := strings.TrimSpace(fm.Model)
	providerOverride := strings.TrimSpace(fm.Provider)
	// Support "provider/model" shorthand in the model field.
	if providerOverride == "" && strings.Contains(model, "/") {
		parts := strings.SplitN(model, "/", 2)
		providerOverride = parts[0]
		model = parts[1]
	}
	return Definition{
		Name:        name,
		Description: strings.TrimSpace(fm.Description),
		Tools:       fm.Tools,
		Subagents:   fm.Subagents,
		Provider:    providerOverride,
		Model:       model,
		Thinking:    strings.TrimSpace(fm.Thinking),
		Prompt:      strings.TrimSpace(body),
		Path:        path,
	}, nil
}

// splitFrontmatter separates a leading `--- ... ---` YAML block from the body.
// If no frontmatter is present, the whole text is treated as the body.
func splitFrontmatter(text string) (yamlPart, body string) {
	text = strings.TrimPrefix(text, "\uFEFF")
	if !strings.HasPrefix(text, "---") {
		return "", text
	}
	rest := strings.TrimPrefix(text[3:], "\r")
	rest = strings.TrimPrefix(rest, "\n")
	idx := strings.Index(rest, "\n---")
	if idx < 0 {
		return "", text
	}
	yamlPart = rest[:idx]
	body = rest[idx+len("\n---"):]
	if nl := strings.IndexByte(body, '\n'); nl >= 0 {
		body = body[nl+1:]
	} else {
		body = ""
	}
	return yamlPart, body
}

// RenderListing returns a compact markdown snippet naming the subagents the
// primary agent may spawn, for appending to its system prompt. It mirrors the
// skills listing so the model is nudged to delegate.
func RenderListing(defs map[string]Definition, allowed []string) string {
	var b strings.Builder
	b.WriteString("\n\n## Subagents you can delegate to\n\n")
	n := 0
	for _, name := range allowed {
		d, ok := defs[name]
		if !ok {
			continue
		}
		fmt.Fprintf(&b, "- `%s` — %s\n", d.Name, d.Description)
		n++
	}
	if n == 0 {
		return ""
	}
	b.WriteString("\nCall a subagent like any other tool, passing a self-contained `task`. " +
		"You see only its final report, not its intermediate work, so delegate whole " +
		"sub-problems to keep your own context focused.\n")
	return b.String()
}
