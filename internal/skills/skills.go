// Package skills implements a simple "skills as markdown files" system.
//
// Skills are plain .md files in a skills/ directory (global
// ~/.talos/skills/ and/or project-local .talos/skills/). Each file's
// first meaningful line is treated as a one-sentence description.  On
// startup the skills package scans registered dirs, builds a compact
// listing, and returns a snippet that callers can inject into the system
// prompt so the LLM knows what's available.  The LLM uses the ordinary
// read tool to load a skill's full contents.
package skills

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// A Skill is a single reusable capability documented in a markdown file.
type Skill struct {
	Name        string // file stem (e.g. "deploy-docker")
	Path        string // full path to the .md file
	Description string // one-line summary
}

// Dir describes a directory to scan for skills.
type Dir struct {
	Path  string
	Label string // used only for diagnostic / debugging
}

// Scan reads all SKILL.md files from the given directories and their
// immediate subdirectories.  Later dirs in the slice override earlier ones
// with the same skill name.  Hidden files/dirs (starting with "." or "_")
// are ignored.
func Scan(dirs []Dir) ([]Skill, error) {
	byName := map[string]Skill{}
	order := []string{} // insertion order for deterministic output
	for _, d := range dirs {
		entries, err := os.ReadDir(d.Path)
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return nil, fmt.Errorf("scan skills %s: %w", d.Path, err)
		}
		for _, e := range entries {
			if !e.IsDir() {
				continue
			}
			name := e.Name()
			if strings.HasPrefix(name, ".") || strings.HasPrefix(name, "_") {
				continue
			}

			skillPath := filepath.Join(d.Path, name, "SKILL.md")
			if _, err := os.Stat(skillPath); err != nil {
				continue // subdirectory without SKILL.md — skip
			}

			desc, err := extractDescription(skillPath)
			if err != nil {
				desc = "(no description)"
			}

			sk := Skill{
				Name:        name,
				Path:        skillPath,
				Description: desc,
			}

			if _, exists := byName[name]; !exists {
				order = append(order, name)
			}
			byName[name] = sk
		}
	}

	skills := make([]Skill, 0, len(byName))
	for _, name := range order {
		skills = append(skills, byName[name])
	}
	sort.Slice(skills, func(i, j int) bool {
		return skills[i].Name < skills[j].Name
	})
	return skills, nil
}

// RenderListing returns a compact markdown snippet listing all skills with
// their invocation syntax. It is meant to be appended to the system prompt.
//
// Example output:
//
//	## Available Skills
//
//	- `deploy-docker` — Deploy the current project to Docker
//	  skill: deploy-docker
//	- `run-tests` — Run tests with coverage and formatting
//	  skill: run-tests
func RenderListing(skills []Skill) string {
	if len(skills) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("\n\n## Available Skills\n\n")
	for _, s := range skills {
		b.WriteString(fmt.Sprintf("- `%s` — %s\n", s.Name, s.Description))
		b.WriteString(fmt.Sprintf("  skill: %s\n", s.Name))
	}
	return b.String()
}

// extractDescription reads the first meaningful line from a markdown file.
// Rules:
//   - Leading blank lines are skipped.
//   - YAML frontmatter (delimited by ---) is skipped.
//   - A heading with an embedded separator (e.g. "# Title — Description"
//     or "# Title - Description") yields the part after the separator.
//   - A bare heading (no separator) is skipped and the search continues.
//   - The first non-empty, non-heading, non-separator line is used.
func extractDescription(path string) (string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	text := string(data)
	inFrontmatter := false
	for _, line := range strings.Split(text, "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			continue
		}

		// Track YAML frontmatter.
		if trimmed == "---" {
			inFrontmatter = !inFrontmatter
			continue
		}
		if inFrontmatter {
			continue
		}

		// Thematic break.
		if isThematicBreak(trimmed) {
			continue
		}

		// Heading? Check for embedded description first.
		if isHeading(trimmed) {
			if desc := extractHeadingDesc(trimmed); desc != "" {
				return desc, nil
			}
			continue // bare heading, skip
		}

		// Regular line — this is the description.
		return trimmed, nil
	}
	return "", fmt.Errorf("no description found in %s", path)
}

// isHeading returns true if s looks like an ATX heading (starts with #).
func isHeading(s string) bool {
	return len(s) > 0 && s[0] == '#'
}

// extractHeadingDesc checks whether a heading contains a description
// separator (" — " or " - ").  If so it returns the part after the
// separator; otherwise it returns "".
func extractHeadingDesc(s string) string {
	for _, sep := range []string{" — ", " - "} {
		if idx := strings.Index(s, sep); idx > 0 {
			after := strings.TrimSpace(s[idx+len(sep):])
			if after != "" {
				return after
			}
		}
	}
	return ""
}

func isThematicBreak(s string) bool {
	// HR: three or more of ---, ***, ___
	if len(s) < 3 {
		return false
	}
	for _, r := range s {
		if r != '-' && r != '*' && r != '_' && r != ' ' {
			return false
		}
	}
	return len(strings.TrimSpace(s)) >= 3
}
