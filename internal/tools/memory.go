package tools

import (
	"context"
	"encoding/json"

	"github.com/mintoleda/talos/internal/memory"
	"github.com/mintoleda/talos/internal/protocol"
)

type memoryWriteTool struct {
	baseDir string
}

func NewMemoryWrite(baseDir string) Tool { return &memoryWriteTool{baseDir: baseDir} }

func (t *memoryWriteTool) Name() string { return "memory_write" }

func (t *memoryWriteTool) Description() string {
	return "Persist a memory entry that will be available in all future sessions. Use this when you observe a user preference, an architectural decision, or important project context worth remembering across sessions. Keep entries concise and specific."
}

func (t *memoryWriteTool) Schema() json.RawMessage {
	return json.RawMessage(`{
		"type": "object",
		"properties": {
			"entry": {
				"type": "string",
				"description": "The memory to persist. Be specific and concise — one clear fact or preference per entry."
			}
		},
		"required": ["entry"]
	}`)
}

func (t *memoryWriteTool) Execute(_ context.Context, args map[string]any) (protocol.ToolResult, error) {
	entry, err := str(args, "entry")
	if err != nil {
		return errResult(err), nil
	}
	if err := memory.Append(t.baseDir, entry); err != nil {
		return errResult(err), nil
	}
	return okResult("memory saved"), nil
}
