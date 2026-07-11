package session

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// absPathRe matches absolute unix paths inside transcript text.
var absPathRe = regexp.MustCompile(`(?:/[A-Za-z0-9._@+~][A-Za-z0-9._@+~-]*)+`)

// BackfillReport summarizes one BackfillMetas run.
type BackfillReport struct {
	Created    []SessionMeta // metas written this run
	Unresolved []string      // project-hash buckets whose dir could not be recovered
}

// BackfillMetas writes meta sidecars for transcripts that predate the sidecar
// format. The project directory is recovered by hashing candidate dirs against
// the bucket name, falling back to mining absolute paths out of the transcript
// and hash-verifying each path's ancestors. A resolved dir is exact — hashes
// can't collide by accident here — so metas are only written, never guessed.
func BackfillMetas(candidates []string) (BackfillReport, error) {
	var report BackfillReport
	root := SessionsDir()
	entries, err := os.ReadDir(root)
	if err != nil {
		if os.IsNotExist(err) {
			return report, nil
		}
		return report, err
	}

	byHash := make(map[string]string)
	for _, c := range candidates {
		abs, err := filepath.Abs(c)
		if err != nil {
			continue
		}
		byHash[ProjectHash(abs)] = abs
	}

	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		bucket := e.Name()
		proj := filepath.Join(root, bucket)
		files, err := os.ReadDir(proj)
		if err != nil {
			continue
		}
		hasMeta := make(map[string]bool)
		var bare []os.DirEntry
		for _, f := range files {
			name := f.Name()
			if strings.HasSuffix(name, ".meta.json") {
				hasMeta[strings.TrimSuffix(name, ".meta.json")] = true
			}
		}
		for _, f := range files {
			name := f.Name()
			if strings.HasSuffix(name, ".jsonl") && !hasMeta[strings.TrimSuffix(name, ".jsonl")] {
				bare = append(bare, f)
			}
		}
		if len(bare) == 0 {
			continue
		}

		dir, ok := byHash[bucket]
		if !ok {
			dir, ok = mineProjectDir(proj, bare, bucket)
		}
		if !ok {
			report.Unresolved = append(report.Unresolved, bucket)
			continue
		}

		for _, f := range bare {
			id := strings.TrimSuffix(f.Name(), ".jsonl")
			info, err := f.Info()
			if err != nil {
				continue
			}
			meta := SessionMeta{
				ID:         id,
				Dir:        dir,
				ProjectDir: dir,
				Isolation:  "none",
				CreatedAt:  info.ModTime(),
				LastActive: info.ModTime(),
			}
			if err := WriteSessionMeta(meta); err != nil {
				continue
			}
			report.Created = append(report.Created, meta)
		}
	}
	return report, nil
}

// mineProjectDir scans transcripts for absolute paths and hash-verifies each
// path and its ancestors against the bucket name.
func mineProjectDir(proj string, files []os.DirEntry, bucket string) (string, bool) {
	tried := make(map[string]bool)
	for _, f := range files {
		data, err := os.ReadFile(filepath.Join(proj, f.Name()))
		if err != nil {
			continue
		}
		for _, m := range absPathRe.FindAllString(string(data), -1) {
			for p := m; p != "/" && p != "."; p = filepath.Dir(p) {
				if tried[p] {
					break
				}
				tried[p] = true
				if ProjectHash(p) == bucket {
					return p, true
				}
			}
		}
	}
	return "", false
}

// DefaultBackfillCandidates returns likely project dirs: cwd, existing metas'
// dirs, and non-hidden directories up to two levels under home (~/x, ~/x/y).
func DefaultBackfillCandidates() []string {
	var out []string
	if cwd, err := os.Getwd(); err == nil {
		out = append(out, cwd)
	}
	if metas, err := ListAllSessionMetas(); err == nil {
		for _, m := range metas {
			out = append(out, MetaProjectKey(m))
		}
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return out
	}
	out = append(out, home)
	level1, _ := os.ReadDir(home)
	for _, e := range level1 {
		if !e.IsDir() || strings.HasPrefix(e.Name(), ".") {
			continue
		}
		d1 := filepath.Join(home, e.Name())
		out = append(out, d1)
		level2, _ := os.ReadDir(d1)
		for _, e2 := range level2 {
			if e2.IsDir() && !strings.HasPrefix(e2.Name(), ".") {
				out = append(out, filepath.Join(d1, e2.Name()))
			}
		}
	}
	return out
}
