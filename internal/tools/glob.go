package tools

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/mintoleda/talos/internal/protocol"
)

type globTool struct{}

func NewGlob() Tool { return &globTool{} }

func (t *globTool) Name() string { return "glob" }

func (t *globTool) Description() string {
	return "Find files matching a shell glob pattern (supports ** for recursive matches). Returns matching paths, sorted."
}

func (t *globTool) Schema() json.RawMessage {
	return json.RawMessage(`{
		"type": "object",
		"properties": {
			"pattern": {"type": "string"}
		},
		"required": ["pattern"]
	}`)
}

func (t *globTool) Execute(ctx context.Context, args map[string]any) (protocol.ToolResult, error) {
	pattern, err := str(args, "pattern")
	if err != nil {
		return errResult(err), nil
	}
	matches, err := filepath.Glob(pattern)
	if err != nil {
		return errResult(err), nil
	}
	if strings.Contains(pattern, "**") {
		matches = doublestarGlob(pattern)
	}
	sort.Strings(matches)
	return okResult(strings.Join(matches, "\n")), nil
}

func doublestarGlob(pattern string) []string {
	root, suffix, found := strings.Cut(pattern, "/**")
	if !found {
		return nil
	}
	suffix = strings.TrimPrefix(suffix, "/")
	var out []string
	_ = filepath.WalkDir(root, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if matched, _ := filepath.Match(suffix, filepath.Base(p)); matched {
			out = append(out, p)
		}
		return nil
	})
	return out
}
