package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
)

func main() {
	scanner := bufio.NewScanner(os.Stdin)
	scanner.Buffer(make([]byte, 0, 64*1024), 10*1024*1024)

	// Hard-coded tool catalog.
	tools := []map[string]any{
		{
			"name":        "echo",
			"description": "Echoes the input message",
			"inputSchema": map[string]any{
				"type": "object",
				"properties": map[string]any{
					"message": map[string]any{"type": "string"},
				},
				"required": []string{"message"},
			},
		},
		{
			"name":        "add",
			"description": "Adds two numbers",
			"inputSchema": map[string]any{
				"type": "object",
				"properties": map[string]any{
					"a": map[string]any{"type": "number"},
					"b": map[string]any{"type": "number"},
				},
				"required": []string{"a", "b"},
			},
		},
	}

	for scanner.Scan() {
		line := scanner.Bytes()
		var req struct {
			ID     int              `json:"id"`
			Method string           `json:"method"`
			Params json.RawMessage  `json:"params,omitempty"`
		}
		if err := json.Unmarshal(line, &req); err != nil {
			continue
		}

		var result json.RawMessage

		switch req.Method {
		case "initialize":
			result, _ = json.Marshal(map[string]any{
				"protocolVersion": "2024-11-05",
				"capabilities":    map[string]any{},
				"serverInfo": map[string]any{
					"name":    "test-mcp-server",
					"version": "1.0.0",
				},
			})

		case "tools/list":
			result, _ = json.Marshal(map[string]any{
				"tools": tools,
			})

		case "tools/call":
			var params struct {
				Name      string         `json:"name"`
				Arguments map[string]any `json:"arguments"`
			}
			if err := json.Unmarshal(req.Params, &params); err != nil {
				result, _ = json.Marshal(map[string]any{
					"content": []map[string]any{
						{"type": "text", "text": fmt.Sprintf("bad params: %v", err)},
					},
					"isError": true,
				})
				break
			}

			var content string
			var isError bool
			switch params.Name {
			case "echo":
				msg, _ := params.Arguments["message"].(string)
				content = "echo: " + msg
			case "add":
				a, _ := params.Arguments["a"].(float64)
				b, _ := params.Arguments["b"].(float64)
				content = fmt.Sprintf("%.0f", a+b)
			default:
				content = "unknown tool: " + params.Name
				isError = true
			}

			result, _ = json.Marshal(map[string]any{
				"content": []map[string]any{
					{"type": "text", "text": content},
				},
				"isError": isError,
			})

		default:
			// Method not found
			resp := map[string]any{
				"jsonrpc": "2.0",
				"id":      req.ID,
				"error": map[string]any{
					"code":    -32601,
					"message": "method not found: " + req.Method,
				},
			}
			out, _ := json.Marshal(resp)
			fmt.Println(string(out))
			continue
		}

		resp := map[string]any{
			"jsonrpc": "2.0",
			"id":      req.ID,
			"result":  json.RawMessage(result),
		}
		out, _ := json.Marshal(resp)
		fmt.Println(string(out))
	}
}
