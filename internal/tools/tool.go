package tools

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/mintoleda/talos/internal/protocol"
)

type Tool interface {
	Name() string
	Description() string
	Schema() json.RawMessage
	Execute(ctx context.Context, args map[string]any) (protocol.ToolResult, error)
}

// EmittingTool is an optional capability for tools that need to emit live events
// while running — e.g. a subagent spawn tool surfacing its child's activity. The
// executor passes the current turn's emit function to such tools instead of
// calling the plain Execute. A nil emit is still possible (headless callers), so
// implementations must tolerate it.
type EmittingTool interface {
	Tool
	ExecuteWithEmit(ctx context.Context, args map[string]any, emit protocol.EmitFunc) (protocol.ToolResult, error)
}

func str(args map[string]any, key string) (string, error) {
	v, ok := args[key]
	if !ok {
		return "", fmt.Errorf("missing arg %q", key)
	}
	s, ok := v.(string)
	if !ok {
		return "", fmt.Errorf("arg %q must be a string", key)
	}
	return s, nil
}

func intArg(args map[string]any, key string) (int, bool, error) {
	v, ok := args[key]
	if !ok {
		return 0, false, nil
	}
	switch n := v.(type) {
	case float64:
		return int(n), true, nil
	case int:
		return n, true, nil
	case int64:
		return int(n), true, nil
	default:
		return 0, false, fmt.Errorf("arg %q must be an integer", key)
	}
}

func okResult(content string) protocol.ToolResult {
	return protocol.ToolResult{Content: content, IsError: false}
}

func errResult(err error) protocol.ToolResult {
	return protocol.ToolResult{Content: err.Error(), IsError: true}
}
