package tools

import (
	"io/fs"
	"path/filepath"
	"time"
)

func walkModTimes(root string, maxFiles int) (map[string]time.Time, error) {
	out := make(map[string]time.Time, 1024)
	n := 0
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		base := d.Name()
		if d.IsDir() {
			if base == "" {
				return nil
			}
			switch base {
			case ".git", "node_modules", "vendor", "__pycache__",
				".venv", "venv", ".tox", ".eggs",
				"dist", "build", "target",
				".cache", ".next", ".nuxt", ".turbo",
				"bazel-bin", "bazel-out", "bazel-testlogs":
				return filepath.SkipDir
			}
			if p != root && base[0] == '.' {
				return filepath.SkipDir
			}
			return nil
		}
		if n >= maxFiles {
			return filepath.SkipAll
		}
		info, err := d.Info()
		if err != nil {
			return nil
		}
		if !info.Mode().IsRegular() {
			return nil
		}
		out[p] = info.ModTime()
		n++
		return nil
	})
	return out, err
}

func diffModTimes(before, after map[string]time.Time) []string {
	var changed []string
	for p, afterT := range after {
		beforeT, existed := before[p]
		if !existed || !afterT.Equal(beforeT) {
			changed = append(changed, p)
		}
	}
	return changed
}
