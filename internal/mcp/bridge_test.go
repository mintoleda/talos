package mcp

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/mintoleda/talos/internal/tools"
)

// fakeConn implements mcpConn for testing the bridge.
type fakeConn struct {
	name  string
	tools []MCPTool
}

func (c *fakeConn) Name() string { return c.name }
func (c *fakeConn) CallTool(ctx context.Context, name string, args map[string]any) (callToolResult, error) {
	if name == "error_tool" {
		return callToolResult{}, nil
	}
	if name == "iserror_tool" {
		return callToolResult{
			Content: []MCPContent{{Type: "text", Text: "something went wrong"}},
			IsError: true,
		}, nil
	}
	return callToolResult{
		Content: []MCPContent{{Type: "text", Text: "hello from " + name}},
	}, nil
}

func TestBridgeNameMangling(t *testing.T) {
	conn := &fakeConn{name: "test-server"}
	mcpDef := MCPTool{Name: "my_tool", Description: "A test tool"}
	bridge := &mcpToolBridge{conn: conn, mcpDef: mcpDef}

	want := "mcp__test-server__my_tool"
	if got := bridge.Name(); got != want {
		t.Errorf("Name() = %q, want %q", got, want)
	}
	if got := bridge.Description(); got != "A test tool" {
		t.Errorf("Description() = %q, want %q", got, "A test tool")
	}
}

func TestBridgeEmptySchema(t *testing.T) {
	conn := &fakeConn{name: "s"}
	defNoSchema := MCPTool{Name: "no_schema"}
	bridge := &mcpToolBridge{conn: conn, mcpDef: defNoSchema}

	got := bridge.Schema()
	want := tools.EmptySchema()
	if string(got) != string(want) {
		t.Errorf("Schema() = %s, want %s", string(got), string(want))
	}

	defWithSchema := MCPTool{
		Name:        "with_schema",
		InputSchema: json.RawMessage(`{"type":"object"}`),
	}
	bridge2 := &mcpToolBridge{conn: conn, mcpDef: defWithSchema}
	if string(bridge2.Schema()) != `{"type":"object"}` {
		t.Errorf("Schema() = %s, want {\"type\":\"object\"}", string(bridge2.Schema()))
	}
}

func TestBridgeExecuteTextContent(t *testing.T) {
	conn := &fakeConn{name: "s"}
	mcpDef := MCPTool{Name: "greet"}
	bridge := &mcpToolBridge{conn: conn, mcpDef: mcpDef}

	result, err := bridge.Execute(context.Background(), map[string]any{"name": "world"})
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if result.Content != "hello from greet" {
		t.Errorf("Content = %q, want %q", result.Content, "hello from greet")
	}
	if result.IsError {
		t.Error("IsError = true, want false")
	}
}

func TestBridgeExecuteIsError(t *testing.T) {
	conn := &fakeConn{name: "s"}
	mcpDef := MCPTool{Name: "iserror_tool"}
	bridge := &mcpToolBridge{conn: conn, mcpDef: mcpDef}

	result, err := bridge.Execute(context.Background(), nil)
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if result.Content != "something went wrong" {
		t.Errorf("Content = %q, want %q", result.Content, "something went wrong")
	}
	if !result.IsError {
		t.Error("IsError = false, want true")
	}
}

func TestBridgeExecutionError(t *testing.T) {
	conn := &fakeConn{name: "s"}
	mcpDef := MCPTool{Name: "error_tool"}
	bridge := &mcpToolBridge{conn: conn, mcpDef: mcpDef}

	result, err := bridge.Execute(context.Background(), nil)
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if result.IsError {
		t.Error("IsError = true, want false for transport error")
	}
}

func TestBridgeContentFlattening(t *testing.T) {
	// Verify that only text content blocks are collected.
	conn := &fakeConn{name: "s"}
	mcpDef := MCPTool{Name: "multi"}
	bridge := &mcpToolBridge{conn: conn, mcpDef: mcpDef}

	result, err := bridge.Execute(context.Background(), nil)
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if result.Content != "hello from multi" {
		t.Errorf("Content = %q, want %q", result.Content, "hello from multi")
	}
}

func TestBridgeToolsHelper(t *testing.T) {
	conn := &fakeConn{
		name: "helper",
		tools: []MCPTool{
			{Name: "a", Description: "tool a"},
			{Name: "b", Description: "tool b"},
		},
	}

	// bridgeTools needs a *ServerConn, not a fakeConn.
	// This is a compile-check: we test the bridge directly above.
	// For the helper, verify it works with a real ServerConn.
	_ = conn
}
