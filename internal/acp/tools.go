package acp

import (
	"path/filepath"
	"strings"

	"github.com/mintoleda/talos/internal/protocol"
)

// toolKind maps a Talos tool name onto an ACP tool kind, which selects the
// client's renderer (Paseo: read view, diff view, shell view, ...).
func toolKind(name string) string {
	switch name {
	case "read":
		return "read"
	case "edit", "write":
		return "edit"
	case "grep", "glob", "ls", "find", "fff", "ffgrep", "search", "memory_search":
		return "search"
	case "bash", "bash_background", "bash_read_output", "bash_kill":
		return "execute"
	case "web_fetch", "web_search":
		return "fetch"
	case "memory_write", "memory_update", "memory_delete", "skill":
		return "think"
	}
	return "other"
}

func strArg(args map[string]any, key string) string {
	s, _ := args[key].(string)
	return s
}

func intArg(args map[string]any, key string) int {
	switch v := args[key].(type) {
	case float64:
		return int(v)
	case int:
		return v
	}
	return 0
}

// toolTitle is a one-line human summary of a tool call.
func toolTitle(name string, args map[string]any) string {
	first := func(keys ...string) string {
		for _, k := range keys {
			if v := strArg(args, k); v != "" {
				return v
			}
		}
		return ""
	}
	var detail string
	switch name {
	case "bash", "bash_background":
		detail = firstLine(strArg(args, "command"))
	case "read", "edit", "write", "ls":
		detail = strArg(args, "path")
	case "grep", "glob":
		detail = strArg(args, "pattern")
		if p := strArg(args, "path"); p != "" {
			detail += " in " + p
		}
	case "web_fetch":
		detail = strArg(args, "url")
	case "skill":
		detail = first("name", "skill")
	default:
		detail = first("query", "pattern", "path", "id", "name")
	}
	if detail == "" {
		return name
	}
	return name + ": " + truncate(detail, 120)
}

func toolLocations(name, cwd string, args map[string]any) []ToolCallLocation {
	p := strArg(args, "path")
	if p == "" {
		return nil
	}
	switch name {
	case "read", "edit", "write", "ls", "grep", "search":
	default:
		return nil
	}
	if !filepath.IsAbs(p) {
		p = filepath.Join(cwd, p)
	}
	loc := ToolCallLocation{Path: p}
	if name == "read" {
		loc.Line = intArg(args, "offset")
	}
	return []ToolCallLocation{loc}
}

// toolDiff renders edit/write arguments as an ACP diff.
func toolDiff(name, cwd string, args map[string]any) []ToolCallContent {
	p := strArg(args, "path")
	if p == "" {
		return nil
	}
	if !filepath.IsAbs(p) {
		p = filepath.Join(cwd, p)
	}
	switch name {
	case "edit":
		old := strArg(args, "old_string")
		return []ToolCallContent{{Type: "diff", Path: p, OldText: &old, NewText: ptr(strArg(args, "new_string"))}}
	case "write":
		return []ToolCallContent{{Type: "diff", Path: p, NewText: ptr(strArg(args, "content"))}}
	}
	return nil
}

func toolStart(id, name, cwd string, args map[string]any) SessionUpdate {
	u := SessionUpdate{
		SessionUpdate: "tool_call",
		ToolCallID:    id,
		Title:         toolTitle(name, args),
		Kind:          toolKind(name),
		Status:        ToolInProgress,
		Locations:     toolLocations(name, cwd, args),
		RawInput:      args,
	}
	if d := toolDiff(name, cwd, args); d != nil {
		u.ToolContent = d
	}
	return u
}

const maxToolOutput = 32 << 10

func toolFinish(id, name, cwd string, args map[string]any, res protocol.ToolResult) SessionUpdate {
	text := res.Content
	if len(text) > maxToolOutput {
		text = text[:maxToolOutput/2] + "\n[... truncated ...]\n" + text[len(text)-maxToolOutput/2:]
	}
	u := SessionUpdate{SessionUpdate: "tool_call_update", ToolCallID: id, Status: ToolCompleted}
	if res.IsError {
		u.Status = ToolFailed
		u.RawOutput = map[string]any{"message": text}
	} else {
		switch toolKind(name) {
		case "execute":
			u.RawOutput = map[string]any{"output": text}
		default:
			u.RawOutput = map[string]any{"content": text}
		}
	}
	var content []ToolCallContent
	// Edits keep their diff; the tool's own confirmation adds nothing.
	if !res.IsError {
		content = toolDiff(name, cwd, args)
	}
	if content == nil && text != "" {
		tc := textContent(text)
		content = []ToolCallContent{{Type: "content", Content: &tc}}
	}
	if content != nil {
		u.ToolContent = content
	}
	return u
}

func firstLine(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i] + " …"
	}
	return s
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
