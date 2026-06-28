package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/mintoleda/talos/internal/fff"
	"github.com/mintoleda/talos/internal/protocol"
)

// searchTool unifies regex content search (grep) and fuzzy content search (ffgrep)
// into a single tool. If the query contains regex metacharacters it runs as
// regex-grep; otherwise it runs as fuzzy-ranked content search.
type searchTool struct {
	cwd string
}

func NewSearch(cwd string) Tool { return &searchTool{cwd: cwd} }

func (t *searchTool) Name() string { return "search" }

func (t *searchTool) Description() string {
	return "Search file contents. If the query looks like a regex (contains characters like ^ $ ( ) [ ] | * + \\ {) it runs as exact regex search. Otherwise it runs as fuzzy, ranked content search. Results are path:line:text, one per line."
}

func (t *searchTool) Schema() json.RawMessage {
	return json.RawMessage(`{
		"type": "object",
		"properties": {
			"query": {"type": "string", "description": "regex pattern or fuzzy natural-language query"},
			"path": {"type": "string", "description": "directory or file to search within (default: current dir)"},
			"limit": {"type": "integer", "description": "maximum fuzzy results (default 10, ignored for regex mode)"}
		},
		"required": ["query"]
	}`)
}

func (t *searchTool) Execute(ctx context.Context, args map[string]any) (protocol.ToolResult, error) {
	query, err := str(args, "query")
	if err != nil {
		return errResult(err), nil
	}
	root := "."
	if p, ok := args["path"].(string); ok && p != "" {
		root = p
	}

	if isRegex(query) {
		return t.grep(ctx, query, root)
	}
	return t.fzgrep(query, root, args)
}

// isRegex reports whether s looks like a regex rather than natural language.
func isRegex(s string) bool {
	metas := `\^$()[|+*{`
	for _, r := range s {
		if strings.ContainsRune(metas, r) {
			return true
		}
	}
	return false
}

// ---- regex mode ----

func (t *searchTool) grep(ctx context.Context, pattern, root string) (protocol.ToolResult, error) {
	if _, err := exec.LookPath("rg"); err == nil {
		cmd := exec.CommandContext(ctx, "rg", "--line-number", "--no-heading", "--color=never", pattern, root)
		out, err := cmd.CombinedOutput()
		if err != nil && len(out) == 0 {
			return okResult("(no matches)"), nil
		}
		return okResult(string(out)), nil
	}

	re, err := regexp.Compile(pattern)
	if err != nil {
		return errResult(fmt.Errorf("invalid pattern: %w", err)), nil
	}
	type hit struct {
		path string
		line int
		text string
	}
	var hits []hit
	_ = filepath.WalkDir(root, func(p string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		data, err := os.ReadFile(p)
		if err != nil {
			return nil
		}
		lines := strings.Split(string(data), "\n")
		for i, line := range lines {
			if re.MatchString(line) {
				hits = append(hits, hit{path: p, line: i + 1, text: line})
			}
		}
		return nil
	})
	sort.Slice(hits, func(i, j int) bool {
		if hits[i].path != hits[j].path {
			return hits[i].path < hits[j].path
		}
		return hits[i].line < hits[j].line
	})
	var out strings.Builder
	for _, h := range hits {
		fmt.Fprintf(&out, "%s:%d:%s\n", h.path, h.line, h.text)
	}
	if len(hits) == 0 {
		return okResult("(no matches)"), nil
	}
	return okResult(out.String()), nil
}

// ---- fuzzy mode ----

func (t *searchTool) fzgrep(query, root string, args map[string]any) (protocol.ToolResult, error) {
	limit, _, _ := intArg(args, "limit")
	if limit <= 0 {
		limit = 10
	}

	idx := ensureFFFIndex(t.cwd)
	results, err := idx.SearchFiles(query, limit)
	if err != nil {
		return errResult(fmt.Errorf("fuzzy search: %w", err)), nil
	}

	// Filter by root if a path was specified.
	if root != "." {
		filtered := make([]fff.SearchResult, 0, len(results))
		for _, r := range results {
			if strings.HasPrefix(r.Path, root) {
				filtered = append(filtered, r)
			}
		}
		results = filtered
	}

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
