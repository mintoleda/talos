package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"github.com/mintoleda/talos/internal/fff"
	"github.com/mintoleda/talos/internal/protocol"
)

type readTool struct {
	reads *ReadSet
	index *fff.Index
}

func NewRead(reads *ReadSet, index *fff.Index) Tool { return &readTool{reads: reads, index: index} }

func (t *readTool) Name() string { return "read" }

func (t *readTool) Description() string {
	return "Read a file from disk. Returns the contents with 1-based line numbers. Optional offset (1-based start line) and limit (max lines). Reading a file is required before editing it."
}

func (t *readTool) Schema() json.RawMessage {
	return json.RawMessage(`{
		"type": "object",
		"properties": {
			"path": {"type": "string", "description": "file path"},
			"offset": {"type": "integer", "description": "1-based start line"},
			"limit": {"type": "integer", "description": "max lines to read"}
		},
		"required": ["path"]
	}`)
}

func (t *readTool) Execute(ctx context.Context, args map[string]any) (protocol.ToolResult, error) {
	path, err := str(args, "path")
	if err != nil {
		return errResult(err), nil
	}
	offset, _, _ := intArg(args, "offset")
	if offset < 0 {
		offset = 0
	}
	limit, hasLimit, err := intArg(args, "limit")
	if err != nil {
		return errResult(err), nil
	}
	if !hasLimit {
		limit = 2000
	}

	data, err := os.ReadFile(path)
	if err != nil {
		return errResult(fmt.Errorf("read %s: %w", path, err)), nil
	}

	lines := strings.Split(string(data), "\n")
	start := offset
	if start < 1 {
		start = 1
	}
	end := start + limit
	if end > len(lines)+1 {
		end = len(lines) + 1
	}
	var out strings.Builder
	for i := start - 1; i < end-1 && i < len(lines); i++ {
		fmt.Fprintf(&out, "%6d\t%s\n", i+1, lines[i])
	}
	if len(lines) > limit && (end-start) < len(lines) {
		fmt.Fprintf(&out, "\n[truncated: %d total lines]\n", len(lines))
	}
	_ = t.reads.Record(path)
	if t.index != nil {
		t.index.RecordRead(path)
	}
	return okResult(out.String()), nil
}
