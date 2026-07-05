package mcp

import (
	"context"
	"encoding/json"
	"sync/atomic"
	"testing"
)

// fakeTransport responds to requests with canned responses.
type fakeTransport struct {
	t            *testing.T
	responses    map[string]json.RawMessage // method → result
	nextID       atomic.Int32
	closed       bool
}

func newFakeTransport(t *testing.T, responses map[string]json.RawMessage) *fakeTransport {
	return &fakeTransport{t: t, responses: responses}
}

func (f *fakeTransport) Send(ctx context.Context, req jsonRPCRequest) (jsonRPCResponse, error) {
	result, ok := f.responses[req.Method]
	if !ok {
		return jsonRPCResponse{
			JSONRPC: jsonRPCVersion,
			ID:      req.ID,
			Error:   &jsonRPCError{Code: -32601, Message: "method not found: " + req.Method},
		}, nil
	}
	return jsonRPCResponse{JSONRPC: jsonRPCVersion, ID: req.ID, Result: result}, nil
}

func (f *fakeTransport) Close() error {
	f.closed = true
	return nil
}

func TestConnectValidatesConfig(t *testing.T) {
	ctx := context.Background()

	// Both Command and URL set
	_, err := Connect(ctx, ServerConfig{
		Name:    "bad",
		Command: "echo",
		URL:     "http://localhost:9999",
	})
	if err == nil || err.Error() != `mcp server "bad": set either command or url, not both` {
		t.Errorf("expected both-set error, got: %v", err)
	}

	// Neither set
	_, err = Connect(ctx, ServerConfig{Name: "bad2"})
	if err == nil || err.Error() != `mcp server "bad2": must set command or url` {
		t.Errorf("expected neither-set error, got: %v", err)
	}
}

func TestConnectWithFakeTransport(t *testing.T) {
	initResult, _ := json.Marshal(initializeResult{
		ProtocolVersion: protocolVersion,
		Capabilities:    json.RawMessage(`{}`),
		ServerInfo:      serverInfo{Name: "fake-server", Version: "1.0"},
	})
	listResult, _ := json.Marshal(listToolsResult{
		Tools: []MCPTool{
			{Name: "echo", Description: "Echo input", InputSchema: json.RawMessage(`{"type":"object"}`)},
		},
	})

	fake := newFakeTransport(t, map[string]json.RawMessage{
		"initialize": initResult,
		"tools/list": listResult,
	})

	// Build ServerConn directly with the fake transport.
	s := &ServerConn{
		name:      "fake",
		transport: fake,
	}

	// Manually initialize
	ctx := context.Background()
	_, err := s.sendReq(ctx, "initialize", initializeParams{
		ProtocolVersion: protocolVersion,
		Capabilities:    json.RawMessage(`{}`),
		ClientInfo: clientInfo{
			Name:    "talos",
			Version: "dev",
		},
	})
	if err != nil {
		t.Fatalf("initialize failed: %v", err)
	}

	var ltr listToolsResult
	if err := s.call(ctx, "tools/list", nil, &ltr); err != nil {
		t.Fatalf("tools/list failed: %v", err)
	}

	if len(ltr.Tools) != 1 {
		t.Fatalf("expected 1 tool, got %d", len(ltr.Tools))
	}
	if ltr.Tools[0].Name != "echo" {
		t.Errorf("tool name = %q, want %q", ltr.Tools[0].Name, "echo")
	}

	s.tools = ltr.Tools
	if !fake.closed {
		s.Close()
	}
}

func TestConnectFullRoundTrip(t *testing.T) {
	initResult, _ := json.Marshal(initializeResult{
		ProtocolVersion: protocolVersion,
		Capabilities:    json.RawMessage(`{}`),
		ServerInfo:      serverInfo{Name: "test-server", Version: "0.1"},
	})
	listResult, _ := json.Marshal(listToolsResult{
		Tools: []MCPTool{
			{Name: "add", Description: "Add two numbers", InputSchema: json.RawMessage(`{"type":"object","properties":{"a":{"type":"number"},"b":{"type":"number"}}}`)},
		},
	})
	callResult, _ := json.Marshal(callToolResult{
		Content: []MCPContent{{Type: "text", Text: "3"}},
	})

	fake := newFakeTransport(t, map[string]json.RawMessage{
		"initialize": initResult,
		"tools/list": listResult,
		"tools/call": callResult,
	})

	s := &ServerConn{
		name:      "test-server",
		transport: fake,
	}

	ctx := context.Background()

	// Initialize
	_, err := s.sendReq(ctx, "initialize", initializeParams{
		ProtocolVersion: protocolVersion,
		Capabilities:    json.RawMessage(`{}`),
		ClientInfo:      clientInfo{Name: "talos", Version: "dev"},
	})
	if err != nil {
		t.Fatalf("initialize: %v", err)
	}

	// List tools
	var ltr listToolsResult
	if err := s.call(ctx, "tools/list", nil, &ltr); err != nil {
		t.Fatalf("tools/list: %v", err)
	}
	s.tools = ltr.Tools

	// Call tool
	result, err := s.CallTool(ctx, "add", map[string]any{"a": 1.0, "b": 2.0})
	if err != nil {
		t.Fatalf("CallTool: %v", err)
	}
	if len(result.Content) != 1 {
		t.Fatalf("expected 1 content block, got %d", len(result.Content))
	}
	if result.Content[0].Text != "3" {
		t.Errorf("result = %q, want %q", result.Content[0].Text, "3")
	}
	if result.IsError {
		t.Error("IsError = true, want false")
	}

	s.Close()
	if !fake.closed {
		t.Error("transport was not closed")
	}
}

func TestConnectInitializeError(t *testing.T) {
	fake := newFakeTransport(t, map[string]json.RawMessage{})
	s := &ServerConn{
		name:      "bad-server",
		transport: fake,
	}

	ctx := context.Background()
	_, err := s.sendReq(ctx, "initialize", initializeParams{
		ProtocolVersion: protocolVersion,
		Capabilities:    json.RawMessage(`{}`),
		ClientInfo:      clientInfo{Name: "talos", Version: "dev"},
	})
	if err == nil {
		t.Fatal("expected error from missing initialize handler")
	}
	s.Close()
}

func TestManagerEmptyConfig(t *testing.T) {
	ctx := context.Background()
	m, errs := NewManager(ctx, nil)
	if errs != nil {
		t.Fatalf("expected no errors for nil config, got: %v", errs)
	}
	if m == nil {
		t.Fatal("manager is nil")
	}
	if m.ConnectedCount() != 0 {
		t.Errorf("ConnectedCount = %d, want 0", m.ConnectedCount())
	}
	if len(m.Tools()) != 0 {
		t.Errorf("Tools() returned %d tools", len(m.Tools()))
	}
	if status := m.Status(); status != "no mcp servers connected" {
		t.Errorf("Status() = %q", status)
	}
	m.Close()
}

func TestManagerNewWithConfig(t *testing.T) {
	// This tests the Manager with invalid server configs (no command/url).
	// NewManager should return errors for each invalid config.
	ctx := context.Background()
	cfgs := []ServerConfig{
		{Name: "valid-but-no-command"},
		{Name: "valid-but-no-url"},
	}
	m, errs := NewManager(ctx, cfgs)
	if len(errs) != len(cfgs) {
		t.Fatalf("expected %d errors from %d invalid configs, got %d", len(cfgs), len(cfgs), len(errs))
	}
	if m.ConnectedCount() != 0 {
		t.Errorf("ConnectedCount = %d, want 0 (all should have failed)", m.ConnectedCount())
	}
	if status := m.Status(); status != "no mcp servers connected" {
		t.Errorf("Status() = %q, want 'no mcp servers connected'", status)
	}
}
