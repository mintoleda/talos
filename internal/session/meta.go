package session

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// SessionMeta is the sidecar persisted next to a transcript so any frontend
// (daemon, TUI, app) can list and resume the session with its directory,
// isolation, and provider info intact.
type SessionMeta struct {
	ID            string    `json:"id"`
	Dir           string    `json:"dir"`
	ProjectDir    string    `json:"project_dir"`
	Isolation     string    `json:"isolation"` // "worktree" | "none"
	Branch        string    `json:"branch,omitempty"`
	DefaultBranch string    `json:"default_branch,omitempty"` // cached origin default
	Merged        bool      `json:"merged,omitempty"`         // true after merge.execute cleanup
	Provider      string    `json:"provider"`
	Model         string    `json:"model"`
	CreatedAt     time.Time `json:"created_at"`
	LastActive    time.Time `json:"last_active"`
}

// MetaProjectKey returns the project key a meta's transcript is stored under.
func MetaProjectKey(meta SessionMeta) string {
	if meta.ProjectDir != "" {
		return meta.ProjectDir
	}
	return meta.Dir
}

// MetaPath returns the sidecar path for a transcript path.
func MetaPath(transcriptPath string) string {
	return strings.TrimSuffix(transcriptPath, ".jsonl") + ".meta.json"
}

// WriteSessionMeta persists meta next to the transcript.
func WriteSessionMeta(meta SessionMeta) error {
	dir := filepath.Join(SessionsDir(), ProjectHash(MetaProjectKey(meta)))
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	path := filepath.Join(dir, meta.ID+".meta.json")
	data, err := json.MarshalIndent(meta, "", "  ")
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// ReadSessionMeta loads meta from an explicit path.
func ReadSessionMeta(path string) (SessionMeta, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return SessionMeta{}, err
	}
	var meta SessionMeta
	if err := json.Unmarshal(data, &meta); err != nil {
		return SessionMeta{}, err
	}
	return meta, nil
}

// FindSessionMeta searches all project dirs under SessionsDir for id.meta.json.
func FindSessionMeta(id string) (SessionMeta, error) {
	root := SessionsDir()
	entries, err := os.ReadDir(root)
	if err != nil {
		if os.IsNotExist(err) {
			return SessionMeta{}, fmt.Errorf("session not found: %s", id)
		}
		return SessionMeta{}, err
	}
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		path := filepath.Join(root, e.Name(), id+".meta.json")
		meta, err := ReadSessionMeta(path)
		if err == nil {
			return meta, nil
		}
		// Fallback: transcript exists without meta — cannot recover dir.
		tx := filepath.Join(root, e.Name(), id+".jsonl")
		if _, err := os.Stat(tx); err == nil {
			return SessionMeta{}, fmt.Errorf("session %s has no meta sidecar (dir unknown)", id)
		}
	}
	return SessionMeta{}, fmt.Errorf("session not found: %s", id)
}

// ListAllSessionMetas walks every project under SessionsDir.
func ListAllSessionMetas() ([]SessionMeta, error) {
	root := SessionsDir()
	entries, err := os.ReadDir(root)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var out []SessionMeta
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		proj := filepath.Join(root, e.Name())
		files, err := os.ReadDir(proj)
		if err != nil {
			continue
		}
		for _, f := range files {
			name := f.Name()
			if strings.HasSuffix(name, ".meta.json") {
				meta, err := ReadSessionMeta(filepath.Join(proj, name))
				if err != nil {
					continue
				}
				out = append(out, meta)
			}
		}
		// Transcripts without meta are skipped (cannot recover dir).
	}
	return out, nil
}

// TouchSessionMeta updates LastActive on an existing meta file.
func TouchSessionMeta(id string) error {
	meta, err := FindSessionMeta(id)
	if err != nil {
		return err
	}
	meta.LastActive = time.Now()
	return WriteSessionMeta(meta)
}

// EnsureSessionMeta creates the meta sidecar if missing (touching LastActive
// when present) so sessions started outside the daemon still get listed.
func EnsureSessionMeta(sess Session, dir, projectDir, providerName, model string) {
	if _, err := os.Stat(MetaPath(sess.Path)); err == nil {
		_ = TouchSessionMeta(sess.ID)
		return
	}
	now := time.Now()
	_ = WriteSessionMeta(SessionMeta{
		ID:         sess.ID,
		Dir:        dir,
		ProjectDir: projectDir,
		Isolation:  "none",
		Provider:   providerName,
		Model:      model,
		CreatedAt:  now,
		LastActive: now,
	})
}
