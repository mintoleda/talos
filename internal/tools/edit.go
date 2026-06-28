package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"github.com/mintoleda/talos/internal/protocol"
)

type editTool struct {
	reads *ReadSet
}

func NewEdit(reads *ReadSet) Tool { return &editTool{reads: reads} }

func (t *editTool) Name() string { return "edit" }

func (t *editTool) Description() string {
	return "Replace an exact string in a file. old_string must match exactly (including whitespace) and be unique unless replace_all is true. You must read the file first. Fails loudly on no match or ambiguous match — re-read and retry rather than guessing."
}

func (t *editTool) Schema() json.RawMessage {
	return json.RawMessage(`{
		"type": "object",
		"properties": {
			"path": {"type": "string"},
			"old_string": {"type": "string"},
			"new_string": {"type": "string"},
			"replace_all": {"type": "boolean"}
		},
		"required": ["path", "old_string", "new_string"]
	}`)
}

func (t *editTool) Execute(ctx context.Context, args map[string]any) (protocol.ToolResult, error) {
	path, err := str(args, "path")
	if err != nil {
		return errResult(err), nil
	}
	oldS, err := str(args, "old_string")
	if err != nil {
		return errResult(err), nil
	}
	newS, err := str(args, "new_string")
	if err != nil {
		return errResult(err), nil
	}
	replaceAll, _ := args["replace_all"].(bool)

	if !t.reads.SeenAndFresh(path) {
		return errResult(fmt.Errorf("must read %s before editing it (or it changed since last read); call read first", path)), nil
	}

	data, err := os.ReadFile(path)
	if err != nil {
		return errResult(fmt.Errorf("read %s: %w", path, err)), nil
	}
	content := string(data)

	n := strings.Count(content, oldS)
	switch {
	case n == 0:
		return errResult(fmt.Errorf("no exact match for old_string in %s; re-read the file and copy the exact text (incl. whitespace)", path)), nil
	case n > 1 && !replaceAll:
		return errResult(fmt.Errorf("old_string matches %d places in %s; add surrounding context to make it unique, or pass replace_all=true", n, path)), nil
	}

	var updated string
	if replaceAll {
		updated = strings.ReplaceAll(content, oldS, newS)
	} else {
		updated = strings.Replace(content, oldS, newS, 1)
	}
	if err := os.WriteFile(path, []byte(updated), 0o644); err != nil {
		return errResult(fmt.Errorf("write %s: %w", path, err)), nil
	}
	_ = t.reads.Update(path)
	return okResult(fmt.Sprintf("edited %s (%d replacement(s))", path, max(n, 1))), nil
}

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}
