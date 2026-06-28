package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/mintoleda/talos/internal/protocol"
)

type grepTool struct{}

func NewGrep() Tool { return &grepTool{} }

func (t *grepTool) Name() string { return "grep" }

func (t *grepTool) Description() string {
	return "Search file contents for a regular expression. Optional path narrows to a directory or file (defaults to the working directory). Results are sorted by path then line number and reported as path:line:text."
}

func (t *grepTool) Schema() json.RawMessage {
	return json.RawMessage(`{
		"type": "object",
		"properties": {
			"pattern": {"type": "string", "description": "regex pattern"},
			"path": {"type": "string", "description": "directory or file"}
		},
		"required": ["pattern"]
	}`)
}

func (t *grepTool) Execute(ctx context.Context, args map[string]any) (protocol.ToolResult, error) {
	pattern, err := str(args, "pattern")
	if err != nil {
		return errResult(err), nil
	}
	path := "."
	if p, ok := args["path"].(string); ok && p != "" {
		path = p
	}

	if _, err := exec.LookPath("rg"); err == nil {
		cmd := exec.CommandContext(ctx, "rg", "--line-number", "--no-heading", "--color=never", pattern, path)
		out, err := cmd.CombinedOutput()
		if err != nil && len(out) == 0 {
			return okResult("(no matches)"), nil
		}
		return okResult(string(out)), nil
	}

	re, err := regexp.Compile(pattern)
	if err != nil {
		return errResult(fmt.Errorf("invalid pattern: %w", err)), nil
	}
	type hit struct {
		path string
		line int
		text string
	}
	var hits []hit
	_ = filepath.WalkDir(path, func(p string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		data, err := os.ReadFile(p)
		if err != nil {
			return nil
		}
		lines := strings.Split(string(data), "\n")
		for i, line := range lines {
			if re.MatchString(line) {
				hits = append(hits, hit{path: p, line: i + 1, text: line})
			}
		}
		return nil
	})
	sort.Slice(hits, func(i, j int) bool {
		if hits[i].path != hits[j].path {
			return hits[i].path < hits[j].path
		}
		return hits[i].line < hits[j].line
	})
	var out strings.Builder
	for _, h := range hits {
		fmt.Fprintf(&out, "%s:%d:%s\n", h.path, h.line, h.text)
	}
	if len(hits) == 0 {
		return okResult("(no matches)"), nil
	}
	return okResult(out.String()), nil
}
