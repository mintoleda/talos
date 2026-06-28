package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/mintoleda/talos/internal/fff"
	"github.com/mintoleda/talos/internal/protocol"
)

// FFFIndex is the shared fuzzy-finding index used by fff and ffgrep.
// It is set up once by DefaultRegistry.
var FFFIndex *fff.Index

func ensureFFFIndex(cwd string) *fff.Index {
	home, err := os.UserHomeDir()
	if err != nil {
		home = "."
	}
	idxDir := filepath.Join(home, ".talos", "fff")
	idx := fff.NewIndex(cwd, idxDir)
	_ = idx.Load() // ignore missing index
	if idx.Stats() == 0 {
		_ = idx.Build()
	}
	return idx
}

type fffTool struct {
	cwd string
}

func NewFFF(cwd string) Tool { return &fffTool{cwd: cwd} }

func (t *fffTool) Name() string { return "fff" }

func (t *fffTool) Description() string {
	return "Fuzzy-find files by path. Ranks results by fuzzy match quality and frecency. Use this when you don't know the exact file name or want the most relevant matches."
}

func (t *fffTool) Schema() json.RawMessage {
	return json.RawMessage(`{
		"type": "object",
		"properties": {
			"query": {"type": "string", "description": "fuzzy query to match against file paths"},
			"limit": {"type": "integer", "description": "maximum number of results (default 10)"}
		},
		"required": ["query"]
	}`)
}

func (t *fffTool) Execute(ctx context.Context, args map[string]any) (protocol.ToolResult, error) {
	query, err := str(args, "query")
	if err != nil {
		return errResult(err), nil
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

type ffgrepTool struct {
	cwd string
}

func NewFFGrep(cwd string) Tool { return &ffgrepTool{cwd: cwd} }

func (t *ffgrepTool) Name() string { return "ffgrep" }

func (t *ffgrepTool) Description() string {
	return "Fuzzy-search file contents. Returns matching lines ranked by fuzzy relevance. Use this when grep's exact regex is too rigid or you want ranked natural-language matches."
}

func (t *ffgrepTool) Schema() json.RawMessage {
	return json.RawMessage(`{
		"type": "object",
		"properties": {
			"query": {"type": "string", "description": "fuzzy query to match against file contents"},
			"limit": {"type": "integer", "description": "maximum number of results (default 10)"}
		},
		"required": ["query"]
	}`)
}

func (t *ffgrepTool) Execute(ctx context.Context, args map[string]any) (protocol.ToolResult, error) {
	query, err := str(args, "query")
	if err != nil {
		return errResult(err), nil
	}
	limit, _, _ := intArg(args, "limit")
	if limit <= 0 {
		limit = 10
	}

	idx := ensureFFFIndex(t.cwd)
	results, err := idx.SearchFiles(query, limit)
	if err != nil {
		return errResult(fmt.Errorf("ffgrep: %w", err)), nil
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
