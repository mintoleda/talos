package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"github.com/mintoleda/talos/internal/protocol"
)

type editTool struct {
	reads *ReadSet
}

func NewEdit(reads *ReadSet) Tool { return &editTool{reads: reads} }

func (t *editTool) Name() string { return "edit" }

func (t *editTool) Description() string {
	return "Replace an exact string in a file. old_string must match exactly (including whitespace) and be unique unless replace_all is true. You must read the file first. Fails loudly on no match or ambiguous match — re-read and retry rather than guessing."
}

func (t *editTool) Schema() json.RawMessage {
	return json.RawMessage(`{
		"type": "object",
		"properties": {
			"path": {"type": "string"},
			"old_string": {"type": "string"},
			"new_string": {"type": "string"},
			"replace_all": {"type": "boolean"}
		},
		"required": ["path", "old_string", "new_string"]
	}`)
}

func (t *editTool) Execute(ctx context.Context, args map[string]any) (protocol.ToolResult, error) {
	path, err := str(args, "path")
	if err != nil {
		return errResult(err), nil
	}
	oldS, err := str(args, "old_string")
	if err != nil {
		return errResult(err), nil
	}
	newS, err := str(args, "new_string")
	if err != nil {
		return errResult(err), nil
	}
	replaceAll, _ := args["replace_all"].(bool)

	if !t.reads.SeenAndFresh(path) {
		return errResult(fmt.Errorf("must read %s before editing it (or it changed since last read); call read first", path)), nil
	}

	data, err := os.ReadFile(path)
	if err != nil {
		return errResult(fmt.Errorf("read %s: %w", path, err)), nil
	}
	content := string(data)

	n := strings.Count(content, oldS)
	actualOld := oldS
	fuzzy := false

	switch {
	case n == 0:
		// Try whitespace-tolerant match: compare line-by-line ignoring leading whitespace.
		matches := fuzzyFind(content, oldS)
		if len(matches) == 0 {
			return errResult(fmt.Errorf("no exact match for old_string in %s; re-read the file and copy the exact text (incl. whitespace)", path)), nil
		}
		if len(matches) > 1 && !replaceAll {
			return errResult(fmt.Errorf("old_string matches %d places in %s (after whitespace normalization); add surrounding context to make it unique, or pass replace_all=true", len(matches), path)), nil
		}
		actualOld = matches[0]
		// Preserve the file's indentation structure by re-indenting new_string to match the file block.
		newS = reindent(actualOld, oldS, newS)
		fuzzy = true
	case n > 1 && !replaceAll:
		return errResult(fmt.Errorf("old_string matches %d places in %s; add surrounding context to make it unique, or pass replace_all=true", n, path)), nil
	}

	var updated string
	if replaceAll {
		if fuzzy {
			// replace_all with fuzzy match: replace every occurrence of the actual block
			updated = strings.ReplaceAll(content, actualOld, newS)
		} else {
			updated = strings.ReplaceAll(content, oldS, newS)
		}
	} else {
		updated = strings.Replace(content, actualOld, newS, 1)
	}
	if err := os.WriteFile(path, []byte(updated), 0o644); err != nil {
		return errResult(fmt.Errorf("write %s: %w", path, err)), nil
	}
	_ = t.reads.Update(path)
	msg := fmt.Sprintf("edited %s (%d replacement(s))", path, max(n, 1))
	if fuzzy {
		msg = fmt.Sprintf("edited %s (fuzzy match, %d replacement(s))", path, max(strings.Count(content, actualOld), 1))
	}
	return okResult(msg), nil
}

// fuzzyFind searches content for blocks whose lines match oldS when leading
// whitespace is stripped. Returns the actual blocks from content that matched.
func fuzzyFind(content, oldS string) []string {
	oldLines := strings.Split(oldS, "\n")
	normOld := make([]string, len(oldLines))
	for i, l := range oldLines {
		normOld[i] = strings.TrimLeft(l, " \t")
	}

	contentLines := strings.Split(content, "\n")
	var matches []string
	for start := 0; start <= len(contentLines)-len(oldLines); start++ {
		ok := true
		for i := 0; i < len(oldLines); i++ {
			if strings.TrimLeft(contentLines[start+i], " \t") != normOld[i] {
				ok = false
				break
			}
		}
		if ok {
			block := strings.Join(contentLines[start:start+len(oldLines)], "\n")
			matches = append(matches, block)
		}
	}
	return matches
}

// reindent adjusts newS so its indentation matches the file block's indentation
// structure, based on the difference between actualOld and oldS.
func reindent(actualOld, oldS, newS string) string {
	actualLines := strings.Split(actualOld, "\n")
	oldLines := strings.Split(oldS, "\n")
	newLines := strings.Split(newS, "\n")

	oldBase := minLeading(oldLines)
	actualBase := minLeading(actualLines)

	var out strings.Builder
	for i, l := range newLines {
		if i > 0 {
			out.WriteByte('\n')
		}
		if strings.TrimSpace(l) == "" {
			continue
		}
		ws, content := splitLeading(l)
		indent := actualBase
		if len(ws) > len(oldBase) {
			indent += ws[len(oldBase):]
		}
		out.WriteString(indent)
		out.WriteString(content)
	}
	return out.String()
}

func minLeading(lines []string) string {
	min := ""
	found := false
	for _, l := range lines {
		if strings.TrimSpace(l) == "" {
			continue
		}
		ws, _ := splitLeading(l)
		if !found || len(ws) < len(min) {
			min = ws
			found = true
		}
	}
	return min
}

func splitLeading(s string) (ws, rest string) {
	i := 0
	for i < len(s) && (s[i] == ' ' || s[i] == '\t') {
		i++
	}
	return s[:i], s[i:]
}

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}
