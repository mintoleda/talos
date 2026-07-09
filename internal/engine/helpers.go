package engine

import (
	"os"
	"path/filepath"
	"strings"

	"github.com/mintoleda/talos/internal/memory"
)

// FindRepoRoot walks up from dir looking for a .git directory.
// Returns dir itself when none is found.
func FindRepoRoot(dir string) string {
	for d := dir; d != "/" && d != "."; d = filepath.Dir(d) {
		if _, err := os.Stat(filepath.Join(d, ".git")); err == nil {
			return d
		}
	}
	return dir
}

// MemoryReferencesMissingPath reports whether an entry's text/tags mention a
// path under root that no longer exists on disk.
func MemoryReferencesMissingPath(root string, e memory.Entry) bool {
	fields := append(strings.Fields(e.Text), e.Tags...)
	for _, f := range fields {
		f = strings.Trim(f, "`'\".,:;()[]{}")
		if !strings.Contains(f, "/") && !strings.Contains(f, ".") {
			continue
		}
		if strings.HasPrefix(f, "http://") || strings.HasPrefix(f, "https://") {
			continue
		}
		if _, err := os.Stat(filepath.Join(root, f)); err != nil {
			return true
		}
	}
	return false
}
