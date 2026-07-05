package mcp

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/BurntSushi/toml"
	"github.com/mintoleda/talos/internal/tools"
)

// buildTestMCPServer builds the test MCP server binary once for all tests.
func buildTestMCPServer(t *testing.T) string {
	t.Helper()
	bin := filepath.Join(t.TempDir(), "test-mcp-server")
	src := filepath.Join("testdata", "mcp-server", "main.go")
	cmd := exec.Command("go", "build", "-o", bin, src)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("build test MCP server: %v\n%s", err, out)
	}
	return bin
}

func TestIntegration_StdioConnectAndListTools(t *testing.T) {
	bin := buildTestMCPServer(t)
	ctx := context.Background()

	cfg := ServerConfig{
		Name:    "test-server",
		Command: bin,
		Args:    []string{},
	}

	conn, err := Connect(ctx, cfg)
	if err != nil {
		t.Fatalf("Connect failed: %v", err)
	}
	defer conn.Close()

	if conn.Name() != "test-server" {
		t.Errorf("Name() = %q, want %q", conn.Name(), "test-server")
	}

	tools := conn.Tools()
	if len(tools) != 2 {
		t.Fatalf("expected 2 tools, got %d", len(tools))
	}

	// Verify echo tool
	echoTool := tools[0]
	if echoTool.Name != "echo" {
		t.Errorf("tool[0].Name = %q, want %q", echoTool.Name, "echo")
	}
	if echoTool.Description != "Echoes the input message" {
		t.Errorf("tool[0].Description = %q, want %q", echoTool.Description, "Echoes the input message")
	}
	if len(echoTool.InputSchema) == 0 {
		t.Error("tool[0].InputSchema is empty")
	}

	// Verify add tool
	addTool := tools[1]
	if addTool.Name != "add" {
		t.Errorf("tool[1].Name = %q, want %q", addTool.Name, "add")
	}
	if addTool.Description != "Adds two numbers" {
		t.Errorf("tool[1].Description = %q, want %q", addTool.Description, "Adds two numbers")
	}
}

func TestIntegration_StdioCallToolEcho(t *testing.T) {
	bin := buildTestMCPServer(t)
	ctx := context.Background()

	conn, err := Connect(ctx, ServerConfig{
		Name:    "test-server",
		Command: bin,
	})
	if err != nil {
		t.Fatalf("Connect failed: %v", err)
	}
	defer conn.Close()

	result, err := conn.CallTool(ctx, "echo", map[string]any{"message": "hello world"})
	if err != nil {
		t.Fatalf("CallTool(echo) failed: %v", err)
	}

	if result.IsError {
		t.Fatal("IsError = true, want false")
	}
	if len(result.Content) != 1 {
		t.Fatalf("expected 1 content block, got %d", len(result.Content))
	}
	if result.Content[0].Type != "text" {
		t.Errorf("Content[0].Type = %q, want %q", result.Content[0].Type, "text")
	}
	if result.Content[0].Text != "echo: hello world" {
		t.Errorf("Content[0].Text = %q, want %q", result.Content[0].Text, "echo: hello world")
	}
}

func TestIntegration_StdioCallToolAdd(t *testing.T) {
	bin := buildTestMCPServer(t)
	ctx := context.Background()

	conn, err := Connect(ctx, ServerConfig{
		Name:    "test-server",
		Command: bin,
	})
	if err != nil {
		t.Fatalf("Connect failed: %v", err)
	}
	defer conn.Close()

	result, err := conn.CallTool(ctx, "add", map[string]any{"a": 42.0, "b": 8.0})
	if err != nil {
		t.Fatalf("CallTool(add) failed: %v", err)
	}

	if result.IsError {
		t.Fatal("IsError = true, want false")
	}
	if len(result.Content) != 1 {
		t.Fatalf("expected 1 content block, got %d", len(result.Content))
	}
	if result.Content[0].Text != "50" {
		t.Errorf("Content[0].Text = %q, want %q", result.Content[0].Text, "50")
	}
}

func TestIntegration_StdioCallToolNotFound(t *testing.T) {
	bin := buildTestMCPServer(t)
	ctx := context.Background()

	conn, err := Connect(ctx, ServerConfig{
		Name:    "test-server",
		Command: bin,
	})
	if err != nil {
		t.Fatalf("Connect failed: %v", err)
	}
	defer conn.Close()

	result, err := conn.CallTool(ctx, "nonexistent", map[string]any{})
	if err != nil {
		t.Fatalf("CallTool(nonexistent) failed: %v", err)
	}

	if !result.IsError {
		t.Fatal("IsError = false, want true for unknown tool")
	}
	if len(result.Content) != 1 {
		t.Fatalf("expected 1 content block, got %d", len(result.Content))
	}
	if result.Content[0].Text != "unknown tool: nonexistent" {
		t.Errorf("Content[0].Text = %q, want %q", result.Content[0].Text, "unknown tool: nonexistent")
	}
}

func TestIntegration_StdioMultipleCalls(t *testing.T) {
	bin := buildTestMCPServer(t)
	ctx := context.Background()

	conn, err := Connect(ctx, ServerConfig{
		Name:    "test-server",
		Command: bin,
	})
	if err != nil {
		t.Fatalf("Connect failed: %v", err)
	}
	defer conn.Close()

	// Call echo twice
	r1, err := conn.CallTool(ctx, "echo", map[string]any{"message": "first"})
	if err != nil {
		t.Fatalf("first call failed: %v", err)
	}
	if r1.Content[0].Text != "echo: first" {
		t.Errorf("first call = %q, want %q", r1.Content[0].Text, "echo: first")
	}

	r2, err := conn.CallTool(ctx, "echo", map[string]any{"message": "second"})
	if err != nil {
		t.Fatalf("second call failed: %v", err)
	}
	if r2.Content[0].Text != "echo: second" {
		t.Errorf("second call = %q, want %q", r2.Content[0].Text, "echo: second")
	}

	// Call add after echo
	r3, err := conn.CallTool(ctx, "add", map[string]any{"a": 10.0, "b": 20.0})
	if err != nil {
		t.Fatalf("third call failed: %v", err)
	}
	if r3.Content[0].Text != "30" {
		t.Errorf("third call = %q, want %q", r3.Content[0].Text, "30")
	}
}

func TestIntegration_BridgeFullPipeline(t *testing.T) {
	bin := buildTestMCPServer(t)
	ctx := context.Background()

	conn, err := Connect(ctx, ServerConfig{
		Name:    "test-server",
		Command: bin,
	})
	if err != nil {
		t.Fatalf("Connect failed: %v", err)
	}
	defer conn.Close()

	// Bridge the tools
	bridged := bridgeTools(conn)
	if len(bridged) != 2 {
		t.Fatalf("expected 2 bridged tools, got %d", len(bridged))
	}

	// Check name mangling
	if bridged[0].Name() != "mcp__test-server__echo" {
		t.Errorf("bridged[0].Name() = %q, want %q", bridged[0].Name(), "mcp__test-server__echo")
	}
	if bridged[1].Name() != "mcp__test-server__add" {
		t.Errorf("bridged[1].Name() = %q, want %q", bridged[1].Name(), "mcp__test-server__add")
	}

	// Check tool interface conformance
	reg := tools.EmptyRegistry()
	reg.Add(bridged[0], bridged[1])

	schemas := reg.Schemas()
	if len(schemas) != 2 {
		t.Fatalf("expected 2 schemas, got %d", len(schemas))
	}

	// Execute echo via bridge
	result, err := bridged[0].Execute(ctx, map[string]any{"message": "bridge test"})
	if err != nil {
		t.Fatalf("Execute(echo) failed: %v", err)
	}
	if result.Content != "echo: bridge test" {
		t.Errorf("Execute content = %q, want %q", result.Content, "echo: bridge test")
	}
	if result.IsError {
		t.Error("IsError = true, want false")
	}

	// Execute add via bridge
	result, err = bridged[1].Execute(ctx, map[string]any{"a": 7.0, "b": 3.0})
	if err != nil {
		t.Fatalf("Execute(add) failed: %v", err)
	}
	if result.Content != "10" {
		t.Errorf("Execute content = %q, want %q", result.Content, "10")
	}
}

func TestIntegration_ManagerWithRealServer(t *testing.T) {
	bin := buildTestMCPServer(t)
	ctx := context.Background()

	cfgs := []ServerConfig{
		{
			Name:    "server-1",
			Command: bin,
		},
		{
			Name:    "server-2",
			Command: bin,
		},
	}

	mgr, errs := NewManager(ctx, cfgs)
	if len(errs) != 0 {
		t.Fatalf("NewManager errors: %v", errs)
	}
	defer mgr.Close()

	if mgr.ConnectedCount() != 2 {
		t.Errorf("ConnectedCount = %d, want 2", mgr.ConnectedCount())
	}

	status := mgr.Status()
	if len(status) == 0 {
		t.Error("Status() returned empty string")
	}

	allTools := mgr.Tools()
	if len(allTools) != 4 {
		t.Fatalf("expected 4 total tools (2 servers × 2 tools each), got %d", len(allTools))
	}

	// Verify name mangling
	names := make(map[string]bool)
	for _, tool := range allTools {
		names[tool.Name()] = true
	}
	if !names["mcp__server-1__echo"] {
		t.Error("missing mcp__server-1__echo")
	}
	if !names["mcp__server-1__add"] {
		t.Error("missing mcp__server-1__add")
	}
	if !names["mcp__server-2__echo"] {
		t.Error("missing mcp__server-2__echo")
	}
	if !names["mcp__server-2__add"] {
		t.Error("missing mcp__server-2__add")
	}
}

func TestIntegration_StdioServerBinaryNotFound(t *testing.T) {
	ctx := context.Background()

	_, err := Connect(ctx, ServerConfig{
		Name:    "ghost",
		Command: "/nonexistent/mcp-server-binary",
	})
	if err == nil {
		t.Fatal("expected error for nonexistent binary, got nil")
	}
	t.Logf("Got expected error: %v", err)
}

func TestIntegration_StdioServerValidation(t *testing.T) {
	ctx := context.Background()

	// Both command and url
	_, err := Connect(ctx, ServerConfig{
		Name:    "bad",
		Command: "echo",
		URL:     "http://localhost:9999",
	})
	if err == nil {
		t.Fatal("expected error for both command and url")
	}

	// Neither
	_, err = Connect(ctx, ServerConfig{
		Name: "bad2",
	})
	if err == nil {
		t.Fatal("expected error for neither command nor url")
	}
}

// Test that the config.toml parsing properly loads MCP server config
func TestIntegration_ConfigTOML(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.toml")

	// Write a config with MCP server definitions
	cfgContent := `
provider = "openai"
model = "gpt-4"

[[mcp_servers]]
name = "my-tools"
command = "npx"
args = ["-y", "@modelcontextprotocol/server-filesystem", "/tmp"]

[[mcp_servers]]
name = "remote-api"
url = "http://localhost:8080"
env = { AUTH_KEY = "secret123" }
`
	if err := os.WriteFile(cfgPath, []byte(cfgContent), 0644); err != nil {
		t.Fatalf("write config: %v", err)
	}

	// Parse the file manually via loadFile (internal func)
	type fileConfig struct {
		MCPServers []ServerConfig `toml:"mcp_servers"`
	}
	var fc fileConfig
	_, err := toml.DecodeFile(cfgPath, &fc)
	if err != nil {
		t.Fatalf("toml decode: %v", err)
	}

	if len(fc.MCPServers) != 2 {
		t.Fatalf("expected 2 MCP servers, got %d", len(fc.MCPServers))
	}

	srv1 := fc.MCPServers[0]
	if srv1.Name != "my-tools" {
		t.Errorf("server[0].Name = %q, want %q", srv1.Name, "my-tools")
	}
	if srv1.Command != "npx" {
		t.Errorf("server[0].Command = %q, want %q", srv1.Command, "npx")
	}
	if len(srv1.Args) != 3 || srv1.Args[0] != "-y" {
		t.Errorf("server[0].Args = %v", srv1.Args)
	}
	if srv1.URL != "" {
		t.Errorf("server[0].URL = %q, want empty", srv1.URL)
	}

	srv2 := fc.MCPServers[1]
	if srv2.Name != "remote-api" {
		t.Errorf("server[1].Name = %q, want %q", srv2.Name, "remote-api")
	}
	if srv2.URL != "http://localhost:8080" {
		t.Errorf("server[1].URL = %q, want %q", srv2.URL, "http://localhost:8080")
	}
	if srv2.Command != "" {
		t.Errorf("server[1].Command = %q, want empty", srv2.Command)
	}
	if srv2.Env == nil || srv2.Env["AUTH_KEY"] != "secret123" {
		t.Errorf("server[1].Env = %v", srv2.Env)
	}
}

// Verify the test MCP server binary works standalone (smoke test)
func TestIntegration_ServerBinarySmoke(t *testing.T) {
	bin := buildTestMCPServer(t)

	// Send one initialize request via stdin and check response
	cmd := exec.Command(bin)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatalf("stdin pipe: %v", err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatalf("stdout pipe: %v", err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatalf("start: %v", err)
	}

	// Write initialize request
	req := `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2024-11-05","capabilities":{},"clientInfo":{"name":"test","version":"1.0"}}}` + "\n"
	stdin.Write([]byte(req))

	// Read response
	buf := make([]byte, 4096)
	n, err := stdout.Read(buf)
	if err != nil {
		t.Fatalf("read: %v", err)
	}

	var resp struct {
		JSONRPC string          `json:"jsonrpc"`
		ID      int             `json:"id"`
		Result  json.RawMessage `json:"result"`
	}
	if err := json.Unmarshal(buf[:n], &resp); err != nil {
		t.Fatalf("unmarshal: %v\n%s", err, string(buf[:n]))
	}
	if resp.JSONRPC != "2.0" {
		t.Errorf("jsonrpc = %q, want %q", resp.JSONRPC, "2.0")
	}
	if resp.ID != 1 {
		t.Errorf("id = %d, want 1", resp.ID)
	}
	if len(resp.Result) == 0 {
		t.Error("result is empty")
	}

	stdin.Close()
	cmd.Wait()
}
