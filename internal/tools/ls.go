package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strings"

	"github.com/mintoleda/talos/internal/protocol"
)

type lsTool struct{}

func NewLs() Tool { return &lsTool{} }

func (t *lsTool) Name() string { return "ls" }

func (t *lsTool) Description() string {
	return "List the entries of a directory, sorted. Directories are marked with a trailing slash."
}

func (t *lsTool) Schema() json.RawMessage {
	return json.RawMessage(`{
		"type": "object",
		"properties": {
			"path": {"type": "string"}
		},
		"required": ["path"]
	}`)
}

func (t *lsTool) Execute(ctx context.Context, args map[string]any) (protocol.ToolResult, error) {
	path, err := str(args, "path")
	if err != nil {
		return errResult(err), nil
	}
	entries, err := os.ReadDir(path)
	if err != nil {
		return errResult(fmt.Errorf("ls %s: %w", path, err)), nil
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Name() < entries[j].Name() })
	var out strings.Builder
	for _, e := range entries {
		mark := " "
		if e.IsDir() {
			mark = "/"
		}
		fmt.Fprintf(&out, "%s%s\n", e.Name(), mark)
	}
	return okResult(out.String()), nil
}
