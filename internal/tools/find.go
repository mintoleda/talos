package tools

import (
	"context"
	"encoding/json"
	"path/filepath"
	"sort"
	"strings"

	"github.com/mintoleda/talos/internal/protocol"
)

// findTool unifies glob-style exact matching and fff-style fuzzy path search
// into a single tool. The agent just provides a query; talos decides whether it
// looks like a glob pattern or a fuzzy search.
type findTool struct {
	cwd string
}

func NewFind(cwd string) Tool { return &findTool{cwd: cwd} }

func (t *findTool) Name() string { return "find" }

func (t *findTool) Description() string {
	return "Find files. If the query contains glob metacharacters (*, ?, [) it is treated as an exact shell glob; otherwise it is treated as a fuzzy, frecency-ranked path query. Returns matching paths, one per line."
}

func (t *findTool) Schema() json.RawMessage {
	return json.RawMessage(`{
		"type": "object",
		"properties": {
			"query": {"type": "string", "description": "glob pattern (e.g. '*.go', 'cmd/**/*.go') or fuzzy query (e.g. 'prompt builder')"},
			"limit": {"type": "integer", "description": "maximum fuzzy results (default 10, ignored for glob)"}
		},
		"required": ["query"]
	}`)
}

func (t *findTool) Execute(ctx context.Context, args map[string]any) (protocol.ToolResult, error) {
	query, err := str(args, "query")
	if err != nil {
		return errResult(err), nil
	}

	if isGlob(query) {
		return t.glob(query)
	}

	limit, _, _ := intArg(args, "limit")
	if limit <= 0 {
		limit = 10
	}

	idx := ensureFFFIndex(t.cwd)
	results := idx.Find(query, limit)
	if len(results) == 0 {
		return okResult("(no matches)"), nil
	}
	var sb strings.Builder
	for _, r := range results {
		sb.WriteString(r.Path)
		sb.WriteByte('\n')
	}
	return okResult(sb.String()), nil
}

func (t *findTool) glob(pattern string) (protocol.ToolResult, error) {
	matches, err := filepath.Glob(pattern)
	if err != nil {
		return errResult(err), nil
	}
	if strings.Contains(pattern, "**") {
		matches = doublestarGlob(pattern)
	}
	sort.Strings(matches)
	if len(matches) == 0 {
		return okResult("(no matches)"), nil
	}
	return okResult(strings.Join(matches, "\n")), nil
}

// isGlob reports whether s looks like a shell glob pattern rather than a fuzzy
// natural-language query.
func isGlob(s string) bool {
	return strings.ContainsAny(s, "*?[")
}
