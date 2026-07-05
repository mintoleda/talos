package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/mintoleda/talos/internal/protocol"
	"github.com/mintoleda/talos/internal/tools"
)

// mcpConn is the subset of *ServerConn required by the tool bridge.
// Extracted as an interface so tests can use a fake without a real MCP connection.
type mcpConn interface {
	Name() string
	CallTool(ctx context.Context, name string, args map[string]any) (callToolResult, error)
}

type mcpToolBridge struct {
	conn   mcpConn
	mcpDef MCPTool
}

func (b *mcpToolBridge) Name() string {
	return "mcp__" + b.conn.Name() + "__" + b.mcpDef.Name
}

func (b *mcpToolBridge) Description() string {
	return b.mcpDef.Description
}

func (b *mcpToolBridge) Schema() json.RawMessage {
	if len(b.mcpDef.InputSchema) == 0 {
		return tools.EmptySchema()
	}
	return b.mcpDef.InputSchema
}

func (b *mcpToolBridge) Execute(ctx context.Context, args map[string]any) (protocol.ToolResult, error) {
	res, err := b.conn.CallTool(ctx, b.mcpDef.Name, args)
	if err != nil {
		return protocol.ToolResult{IsError: true, Content: fmt.Sprintf("mcp call failed: %v", err)}, nil
	}

	var parts []string
	for _, c := range res.Content {
		if c.Type == "text" {
			parts = append(parts, c.Text)
		}
	}
	content := strings.Join(parts, "\n")
	return protocol.ToolResult{Content: content, IsError: res.IsError}, nil
}

func bridgeTools(conn *ServerConn) []tools.Tool {
	out := make([]tools.Tool, len(conn.Tools()))
	for i, t := range conn.Tools() {
		out[i] = &mcpToolBridge{conn: conn, mcpDef: t}
	}
	return out
}
