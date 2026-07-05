package tui

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/charmbracelet/lipgloss"
	ignore "github.com/sabhiram/go-gitignore"
	"github.com/sahilm/fuzzy"

	"github.com/mintoleda/talos/internal/tui/styles"
)

type filePickerEntry struct {
	Path  string
	IsDir bool
	Score int
}

type filePickerState struct {
	active   bool
	query    string
	results  []filePickerEntry
	selected int
	atIndex  int // index of the @ character in the input

	// Cached directory entries to avoid re-walking on every keystroke
	// within a single @ session. Invalidated on deactivate.
	cachedRoot    string
	cachedEntries []filePickerEntry
}

// maxWalkDepth limits how deep collectDirEntries descends. It's set to 6
// to cover virtually all project structures without risking hangs in $HOME
// (the shouldSkipDir block keeps home-dir-scale garbage out of the walk).
const maxWalkDepth = 6

func compileGitignore(root string) *ignore.GitIgnore {
	giPath := filepath.Join(root, ".gitignore")
	data, err := os.ReadFile(giPath)
	if err != nil {
		return ignore.CompileIgnoreLines()
	}
	return ignore.CompileIgnoreLines(strings.Split(string(data), "\n")...)
}

// shouldSkipDir returns true for directories that are always skipped during
// file-picker walks. The list is aggressive because the file picker must
// never hang — even in $HOME with its enormous hidden/commercial trees.
func shouldSkipDir(name string) bool {
	switch name {
	// Version control
	case ".git", ".svn", ".hg":
		return true
	// Dependencies
	case "node_modules", "vendor", ".bundle", "deps", "packages":
		return true
	// Build output
	case "dist", "build", "out", "target", "bin", "obj", "Debug", "Release":
		return true
	// IDE / editor
	case ".vscode", ".idea", ".vs", "__pycache__", ".mypy_cache", ".pytest_cache":
		return true
	// Home-dir-scale garbage (these are massive on every system)
	case ".cache", ".npm", ".nvm", ".cargo", ".rustup", ".local", ".config",
		".mozilla", ".java", ".gradle", ".lein", ".stack",
		"Library", "Applications", "AppData", ".dotnet",
		".gem", ".rbenv", ".pyenv", ".conda", ".venv":
		return true
	// Talos own data
	case ".talos":
		return true
	}
	return strings.HasPrefix(name, ".")
}

// collectDirEntries walks root up to maxDepth and returns file/dir paths.
// Hidden dirs and gitignored files are skipped. The walk is designed to be
// fast (no FFF index building) so it's safe to call on any directory.
func collectDirEntries(root string, maxDepth int) []filePickerEntry {
	gi := compileGitignore(root)
	var entries []filePickerEntry
	seen := make(map[string]bool)

	filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		rel, _ := filepath.Rel(root, path)
		if rel == "." {
			return nil
		}

		if gi.MatchesPath(rel) {
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}

		depth := strings.Count(rel, string(filepath.Separator))
		if depth > maxDepth {
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}

		if d.IsDir() {
			base := filepath.Base(path)
			if shouldSkipDir(base) {
				return filepath.SkipDir
			}
			if _, ok := seen[path]; !ok {
				seen[path] = true
				entries = append(entries, filePickerEntry{Path: rel, IsDir: true})
			}
			return nil
		}

		if _, ok := seen[path]; !ok {
			seen[path] = true
			entries = append(entries, filePickerEntry{Path: rel, IsDir: false})
		}
		return nil
	})

	return entries
}

// fuzzyMatchEntries filters entries by fuzzy-matching their paths against query.
// Returns the top `limit` results ranked by match quality.
func fuzzyMatchEntries(entries []filePickerEntry, query string, limit int) []filePickerEntry {
	if query == "" {
		// No query: return all entries sorted by depth (shallow first), then name.
		sort.Slice(entries, func(i, j int) bool {
			di := strings.Count(entries[i].Path, string(filepath.Separator))
			dj := strings.Count(entries[j].Path, string(filepath.Separator))
			if di != dj {
				return di < dj
			}
			return entries[i].Path < entries[j].Path
		})
		if limit > 0 && len(entries) > limit {
			entries = entries[:limit]
		}
		return entries
	}

	paths := make([]string, len(entries))
	for i, e := range entries {
		paths[i] = e.Path
	}

	matches := fuzzy.FindFrom(strings.ToLower(query), filePickerSource(paths))
	scored := make([]filePickerEntry, 0, len(matches))
	for _, m := range matches {
		scored = append(scored, filePickerEntry{
			Path:  entries[m.Index].Path,
			IsDir: entries[m.Index].IsDir,
			Score: m.Score,
		})
	}

	sort.Slice(scored, func(i, j int) bool {
		return scored[i].Score > scored[j].Score
	})

	if limit > 0 && len(scored) > limit {
		scored = scored[:limit]
	}
	return scored
}

// filePickerSource adapts a string slice to fuzzy.Source, normalizing
// path separators to spaces so "src main" matches "src/main.go".
type filePickerSource []string

func (s filePickerSource) String(i int) string {
	return normalizePickerPath(s[i])
}
func (s filePickerSource) Len() int { return len(s) }

func normalizePickerPath(path string) string {
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

func (fp *filePickerState) getDirEntries(root string) []filePickerEntry {
	if root == "" {
		return nil
	}
	if fp.cachedRoot == root && fp.cachedEntries != nil {
		return fp.cachedEntries
	}
	fp.cachedRoot = root
	fp.cachedEntries = collectDirEntries(root, maxWalkDepth)
	return fp.cachedEntries
}

func (fp *filePickerState) activate(cwd string, query string, atIndex int) {
	fp.active = true
	fp.query = query
	fp.atIndex = atIndex

	// Get all entries via lightweight directory walk.
	// Depth is capped at maxWalkDepth (6) so we don't hang in $HOME.
	// This is the sole source — the stale FFF index is not consulted.
	dirEntries := fp.getDirEntries(cwd)

	// If query is empty, cap at depth 3 to keep the list manageable.
	// The user can type a few characters to drill deeper.
	if query == "" {
		var filtered []filePickerEntry
		for _, e := range dirEntries {
			depth := strings.Count(e.Path, string(filepath.Separator))
			if depth <= 3 {
				filtered = append(filtered, e)
			}
		}
		dirEntries = filtered
	}

	// Filter and rank by fuzzy match.
	fp.results = fuzzyMatchEntries(dirEntries, query, 20)
	if fp.selected >= len(fp.results) {
		fp.selected = 0
	}
}

func (fp *filePickerState) deactivate() {
	fp.active = false
	fp.results = nil
	fp.selected = 0
	fp.query = ""
	// Invalidate walk cache so newly created files are picked up on next @.
	fp.cachedRoot = ""
	fp.cachedEntries = nil
}

func (fp *filePickerState) selectedPath() string {
	if fp.selected < 0 || fp.selected >= len(fp.results) {
		return ""
	}
	return fp.results[fp.selected].Path
}

const maxVisible = 8

// filePickerHeight returns the number of lines to reserve for the picker.
// When all items fit in the viewport we shrink to fit; when there are more
// we reserve the full viewport so scrolling doesn't jump the prompt box.
func (fp *filePickerState) height() int {
	if !fp.active || len(fp.results) == 0 {
		return 0
	}
	n := len(fp.results)
	if n > maxVisible {
		return maxVisible + 3 // header + maxVisible items + footer
	}
	return n + 2 // header + n items, no footer
}

func (fp *filePickerState) render(width int) string {
	if !fp.active || len(fp.results) == 0 {
		return ""
	}

	n := len(fp.results)

	// Compute viewport window centered on the selected item.
	startIdx := fp.selected - maxVisible/2
	if startIdx < 0 {
		startIdx = 0
	}
	if startIdx+maxVisible > n {
		startIdx = n - maxVisible
		if startIdx < 0 {
			startIdx = 0
		}
	}
	endIdx := startIdx + maxVisible
	if endIdx > n {
		endIdx = n
	}

	var b strings.Builder

	// Header bar
	b.WriteString(styles.FilePickerHint.Render("  @ file picker — ↑↓  enter  esc"))
	b.WriteString("\n")

	for i := startIdx; i < endIdx; i++ {
		e := fp.results[i]
		rel := e.Path
		if e.IsDir {
			rel += "/"
		}

		// Truncate if too long
		maxPathW := width - 4
		if maxPathW < 20 {
			maxPathW = 20
		}
		if lipgloss.Width(rel) > maxPathW {
			rel = "…" + rel[len(rel)-maxPathW+1:]
		}

		var rendered string
		if i == fp.selected {
			rendered = styles.FilePickerSelected.Render("  " + rel)
		} else if e.IsDir {
			rendered = styles.FilePickerDir.Render("  " + rel)
		} else {
			rendered = styles.FilePickerFile.Render("  " + rel)
		}

		b.WriteString(rendered)
		b.WriteString("\n")
	}

	// Footer showing scroll position when there are more results than fit.
	if n > maxVisible {
		above := startIdx
		below := n - endIdx
		var parts []string
		if above > 0 {
			parts = append(parts, fmt.Sprintf("%d above", above))
		}
		if below > 0 {
			parts = append(parts, fmt.Sprintf("%d more", below))
		}
		pos := fmt.Sprintf("%d/%d", fp.selected+1, n)
		footer := strings.Join(parts, " · ") + "  (" + pos + ")"
		b.WriteString(styles.FilePickerHint.Render("  … " + footer))
		b.WriteString("\n")
	}

	return b.String()
}

func extractLastAt(val string) (atIndex int, afterQuery string) {
	for i := len(val) - 1; i >= 0; i-- {
		if val[i] == '@' && (i == 0 || val[i-1] == ' ') {
			return i, val[i+1:]
		}
	}
	return -1, ""
}
