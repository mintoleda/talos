package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/mintoleda/talos/internal/protocol"
)

type writeTool struct {
	reads *ReadSet
}

func NewWrite(reads *ReadSet) Tool { return &writeTool{reads: reads} }

func (t *writeTool) Name() string { return "write" }

func (t *writeTool) Description() string {
	return "Create a new file or overwrite an existing one with the given content. Parent directories are created as needed. If the file already exists, you must have read it in this session; the read-before-write rule is enforced — re-read and retry rather than overwriting blind. Use edit for surgical changes to files that already exist."
}

func (t *writeTool) Schema() json.RawMessage {
	return json.RawMessage(`{
		"type": "object",
		"properties": {
			"path": {"type": "string"},
			"content": {"type": "string"}
		},
		"required": ["path", "content"]
	}`)
}

func (t *writeTool) Execute(ctx context.Context, args map[string]any) (protocol.ToolResult, error) {
	path, err := str(args, "path")
	if err != nil {
		return errResult(err), nil
	}
	content, err := str(args, "content")
	if err != nil {
		return errResult(err), nil
	}
	// If the file already exists, require a fresh read in this session. This
	// matches the edit tool's gate so a model cannot silently overwrite a file
	// it has never opened. A missing file is always allowed — that's the
	// normal "create new file" case.
	if _, statErr := os.Stat(path); statErr == nil {
		if t.reads == nil || !t.reads.SeenAndFresh(path) {
			return errResult(fmt.Errorf("must read %s before overwriting it (or it changed since last read); call read first, then retry", path)), nil
		}
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return errResult(fmt.Errorf("mkdir %s: %w", path, err)), nil
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		return errResult(fmt.Errorf("write %s: %w", path, err)), nil
	}
	if t.reads != nil {
		_ = t.reads.Update(path)
	}
	return okResult(fmt.Sprintf("wrote %s", path)), nil
}
