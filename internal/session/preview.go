package session

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/mintoleda/talos/internal/protocol"
)

type SessionPreview struct {
	ID      string
	ModTime time.Time
	Preview string
}

// ListSessionPreviews returns all sessions for projectRoot sorted newest first,
// each annotated with a truncated preview of the last user message.
func ListSessionPreviews(projectRoot string) ([]SessionPreview, error) {
	pid := ProjectHash(projectRoot)
	dir := filepath.Join(SessionsDir(), pid)
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}

	var out []SessionPreview
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".jsonl") {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		id := strings.TrimSuffix(e.Name(), ".jsonl")
		path := filepath.Join(dir, e.Name())
		preview := lastUserMessage(path)
		if len(preview) > 80 {
			preview = preview[:77] + "..."
		}
		out = append(out, SessionPreview{ID: id, ModTime: info.ModTime(), Preview: preview})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ModTime.After(out[j].ModTime) })
	return out, nil
}

// lastUserMessage scans path (a JSONL transcript) and returns the text of the
// last user message, or "" if none is found.
func lastUserMessage(path string) string {
	f, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer f.Close()

	var last string
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 1024*1024), 1024*1024)
	for sc.Scan() {
		line := sc.Bytes()
		if len(line) == 0 {
			continue
		}
		var m protocol.Message
		if err := json.Unmarshal(line, &m); err != nil {
			continue
		}
		if m.Role == protocol.RoleUser {
			for _, b := range m.Content {
				if b.Type == protocol.BlockText && b.Text != "" {
					last = b.Text
					break
				}
			}
		}
	}
	return last
}
