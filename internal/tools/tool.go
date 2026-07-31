package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

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

// StreamingTool is an optional capability for tools that produce incremental
// output (e.g. bash). The executor calls ExecuteStreaming instead of Execute,
// and the tool emits ToolOutputDelta events as output arrives. The final
// return value is the complete ToolResult captured by ToolFinished.
// A nil emit is possible (headless callers), so implementations must tolerate it.
type StreamingTool interface {
	Tool
	ExecuteStreaming(ctx context.Context, args map[string]any, emit protocol.EmitFunc) (protocol.ToolResult, error)
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

// MaxResultBytes is the ceiling applied to every tool result before it enters
// the transcript. Snip/prune only runs once context is already over 60%, so an
// oversized single result would otherwise be billed in full at least once — and
// one larger than the context window would wedge the session outright.
const MaxResultBytes = 100 << 10

// CapResultContent truncates content to max bytes, keeping the head and tail
// on line boundaries and prefixing a marker that says what was dropped.
func CapResultContent(content string, max int) string {
	if max <= 0 {
		max = MaxResultBytes
	}
	if len(content) <= max {
		return content
	}
	headLen := max * 3 / 4
	tailLen := max - headLen

	head := content[:headLen]
	if i := strings.LastIndexByte(head, '\n'); i > headLen/2 {
		head = head[:i]
	}
	tail := content[len(content)-tailLen:]
	if i := strings.IndexByte(tail, '\n'); i >= 0 && i < tailLen/2 {
		tail = tail[i+1:]
	}

	var b strings.Builder
	fmt.Fprintf(&b, "[truncated tool result — %d bytes total, showing first %d and last %d; narrow the query or path and re-run if you need the rest]\n",
		len(content), len(head), len(tail))
	b.WriteString(strings.ToValidUTF8(head, ""))
	b.WriteString("\n\n…\n\n")
	b.WriteString(strings.ToValidUTF8(tail, ""))
	return b.String()
}

func okResult(content string) protocol.ToolResult {
	return protocol.ToolResult{Content: content, IsError: false}
}

func errResult(err error) protocol.ToolResult {
	return protocol.ToolResult{Content: err.Error(), IsError: true}
}
