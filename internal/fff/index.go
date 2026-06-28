// Package fff provides frecency-ranked fuzzy file finding for talos.
// It is a Go-native approximation of pi's fff extension, not a binding to the
// upstream Rust library.
package fff

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	ignore "github.com/sabhiram/go-gitignore"
	"github.com/sahilm/fuzzy"
)

// Entry is one indexed file path plus its frecency score.
type Entry struct {
	Path     string  `json:"path"`
	Score    float64 `json:"score"`
	LastRead int64   `json:"last_read"` // unix seconds
}

// Index holds the searchable file list for a project.
type Index struct {
	mu       sync.RWMutex
	root     string
	entries  []Entry
	indexDir string
	updated  time.Time
}

// NewIndex creates an index for root. The index is persisted under indexDir.
func NewIndex(root, indexDir string) *Index {
	return &Index{
		root:     root,
		indexDir: indexDir,
	}
}

// Load reads a persisted index from disk, if one exists.
func (idx *Index) Load() error {
	path := idx.file()
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	idx.mu.Lock()
	defer idx.mu.Unlock()
	return json.Unmarshal(data, &idx.entries)
}

// Save persists the current index to disk.
func (idx *Index) Save() error {
	if err := os.MkdirAll(idx.indexDir, 0o755); err != nil {
		return err
	}
	idx.mu.RLock()
	defer idx.mu.RUnlock()
	data, err := json.MarshalIndent(idx.entries, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(idx.file(), data, 0o644)
}

// file returns the on-disk path for this project's index.
func (idx *Index) file() string {
	name := projectHash(idx.root) + ".json"
	return filepath.Join(idx.indexDir, name)
}

// projectHash is a stable identifier for a project root.
func projectHash(root string) string {
	abs, _ := filepath.Abs(root)
	// simple stable hash
	h := 0
	for i, c := range abs {
		h += int(c) * (i + 1)
	}
	return fmt.Sprintf("%x", h)
}

// Build walks root and rebuilds the index from disk.
func (idx *Index) Build() error {
	var entries []Entry
	seen := make(map[string]bool)

	err := filepath.WalkDir(idx.root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		rel, _ := filepath.Rel(idx.root, path)
		if rel == "." {
			return nil
		}
		// Skip hidden directories and common junk.
		if d.IsDir() {
			base := filepath.Base(path)
			if strings.HasPrefix(base, ".") && base != "." {
				return filepath.SkipDir
			}
			if base == "node_modules" || base == "vendor" || base == "dist" || base == "build" {
				return filepath.SkipDir
			}
			return nil
		}
		if isIgnored(idx.root, rel) {
			return nil
		}
		if seen[path] {
			return nil
		}
		seen[path] = true
		// Base score: prefer shorter paths and files closer to root.
		depth := strings.Count(rel, string(filepath.Separator))
		baseScore := 1.0 / float64(depth+1)
		entries = append(entries, Entry{
			Path:     path,
			Score:    baseScore,
			LastRead: 0,
		})
		return nil
	})
	if err != nil {
		return err
	}

	idx.mu.Lock()
	idx.entries = entries
	idx.updated = time.Now()
	idx.mu.Unlock()

	return idx.Save()
}

// isIgnored checks .gitignore files along rel's path.
func isIgnored(root, rel string) bool {
	dir := filepath.Dir(rel)
	base := filepath.Base(rel)

	// Walk from root out to the file's directory looking for .gitignore files.
	parts := strings.Split(dir, string(filepath.Separator))
	current := root
	for _, part := range parts {
		if part == "." || part == "" {
			continue
		}
		current = filepath.Join(current, part)
		giPath := filepath.Join(current, ".gitignore")
		if ignore, ok := loadGitignore(giPath); ok {
			if ignore.MatchesPath(base) || ignore.MatchesPath(rel) {
				return true
			}
		}
	}
	return false
}

var ignoreCache sync.Map // map[string]*ignore.GitIgnore

func loadGitignore(path string) (*ignore.GitIgnore, bool) {
	if cached, ok := ignoreCache.Load(path); ok {
		return cached.(*ignore.GitIgnore), true
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, false
	}
	gi := ignore.CompileIgnoreLines(strings.Split(string(data), "\n")...)
	ignoreCache.Store(path, gi)
	return gi, true
}

// SearchResult is one fuzzy match.
type SearchResult struct {
	Path  string
	Score int
}

// Find returns up to limit paths matching query, ranked by fuzzy score and frecency.
func (idx *Index) Find(query string, limit int) []SearchResult {
	idx.mu.RLock()
	defer idx.mu.RUnlock()

	if len(idx.entries) == 0 {
		return nil
	}
	paths := make([]string, len(idx.entries))
	for i, e := range idx.entries {
		paths[i] = e.Path
	}

	matches := fuzzy.FindFrom(strings.ToLower(query), pathSource(paths))
	results := make([]SearchResult, 0, len(matches))
	for _, m := range matches {
		results = append(results, SearchResult{
			Path:  paths[m.Index],
			Score: m.Score,
		})
	}

	// Boost by frecency.
	for i := range results {
		entry := idx.entryByPath(results[i].Path)
		results[i].Score = int(float64(results[i].Score) * entry.Score)
	}

	sort.Slice(results, func(i, j int) bool {
		return results[i].Score > results[j].Score
	})

	if limit > 0 && len(results) > limit {
		results = results[:limit]
	}
	return results
}

func (idx *Index) entryByPath(path string) Entry {
	for _, e := range idx.entries {
		if e.Path == path {
			return e
		}
	}
	return Entry{Path: path, Score: 1.0}
}

// RecordRead bumps the frecency score for a file.
func (idx *Index) RecordRead(path string) {
	idx.mu.Lock()
	defer idx.mu.Unlock()
	for i := range idx.entries {
		if idx.entries[i].Path == path {
			idx.entries[i].Score += 1.0
			idx.entries[i].LastRead = time.Now().Unix()
			return
		}
	}
}

// Stats returns the number of indexed files.
func (idx *Index) Stats() int {
	idx.mu.RLock()
	defer idx.mu.RUnlock()
	return len(idx.entries)
}

// pathSource adapts a string slice to sahilm/fuzzy's Source interface.
// It normalizes common path separators to spaces so queries like
// "prompt build" match "prompt_builder.go".
type pathSource []string

func (s pathSource) String(i int) string {
	return normalizePathForFuzzy(s[i])
}
func (s pathSource) Len() int { return len(s) }

func normalizePathForFuzzy(path string) string {
	var sb strings.Builder
	for _, r := range path {
		switch r {
		case '_', '-', '.', filepath.Separator:
			sb.WriteByte(' ')
		default:
			sb.WriteRune(r)
		}
	}
	return sb.String()
}

// SearchFiles reads each indexed file's contents and returns lines that fuzzy
// match query. This is the naive implementation; a faster version would keep an
// inverted index.
func (idx *Index) SearchFiles(query string, limit int) ([]SearchResult, error) {
	idx.mu.RLock()
	defer idx.mu.RUnlock()

	tokens := strings.Fields(strings.ToLower(query))
	var results []SearchResult

	for _, e := range idx.entries {
		f, err := os.Open(e.Path)
		if err != nil {
			continue
		}
		sc := bufio.NewScanner(f)
		sc.Buffer(make([]byte, 1024*1024), 1024*1024)
		lineNo := 0
		for sc.Scan() {
			lineNo++
			line := sc.Text()
			lower := strings.ToLower(line)
			match := true
			for _, tok := range tokens {
				if !strings.Contains(lower, tok) {
					match = false
					break
				}
			}
			if !match {
				continue
			}
			// Fuzzy-rank each token against the line and sum scores.
			score := 0
			for _, tok := range tokens {
				fm := fuzzy.Find(tok, []string{lower})
				if len(fm) > 0 {
					score += fm[0].Score
				}
			}
			if score == 0 {
				continue
			}
			results = append(results, SearchResult{
				Path:  fmt.Sprintf("%s:%d:%s", e.Path, lineNo, line),
				Score: score,
			})
		}
		f.Close()
		if limit > 0 && len(results) >= limit*4 {
			break
		}
	}

	sort.Slice(results, func(i, j int) bool {
		return results[i].Score > results[j].Score
	})
	if limit > 0 && len(results) > limit {
		results = results[:limit]
	}
	return results, nil
}
