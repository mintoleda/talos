package memory

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

func Path(baseDir string) string {
	return filepath.Join(baseDir, "memory.md")
}

func Load(baseDir string) (string, error) {
	data, err := os.ReadFile(Path(baseDir))
	if err != nil {
		if os.IsNotExist(err) {
			return "", nil
		}
		return "", err
	}
	return strings.TrimSpace(string(data)), nil
}

func Append(baseDir, entry string) error {
	path := Path(baseDir)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0o644)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = fmt.Fprintf(f, "[%s] %s\n", time.Now().UTC().Format(time.RFC3339), strings.TrimSpace(entry))
	return err
}

func Render(entries []Entry) string {
	if len(entries) == 0 {
		return ""
	}
	byCat := make(map[string][]Entry)
	for _, e := range entries {
		byCat[e.Category] = append(byCat[e.Category], e)
	}
	cats := make([]string, 0, len(byCat))
	for cat := range byCat {
		cats = append(cats, cat)
	}
	sort.Strings(cats)
	var b strings.Builder
	b.WriteString("\n\n## Project memory\n\n")
	for _, cat := range cats {
		b.WriteString("### ")
		b.WriteString(cat)
		b.WriteByte('\n')
		for _, e := range byCat[cat] {
			fmt.Fprintf(&b, "- [%s] %s\n", e.ID, e.Text)
		}
	}
	return b.String()
}
