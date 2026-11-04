package mcp

// ServerConfig describes one MCP server.
//
// Transport selection: Command set -> stdio. URL set and Transport == "sse" ->
// legacy SSE transport. URL set otherwise ("" or "http") -> streamable HTTP.
type ServerConfig struct {
	Name      string            `toml:"name"`
	Command   string            `toml:"command"`
	Args      []string          `toml:"args"`
	Env       map[string]string `toml:"env"`
	URL       string            `toml:"url"`
	Transport string            `toml:"transport"` // "" | "stdio" | "sse" | "http"
	Headers   map[string]string `toml:"headers"`   // extra HTTP headers (http transport only)
}
