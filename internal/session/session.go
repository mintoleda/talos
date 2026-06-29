package session

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

type Session struct {
	ID        string
	ProjectID string
	Path      string
}

func ProjectHash(root string) string {
	abs, _ := filepath.Abs(root)
	sum := sha256.Sum256([]byte(abs))
	return hex.EncodeToString(sum[:])[:16]
}

func SessionsDir() string {
	home, err := os.UserHomeDir()
	if err != nil {
		home = "."
	}
	return filepath.Join(home, ".talos", "sessions")
}

func NewSession(projectRoot string) Session {
	pid := ProjectHash(projectRoot)
	id := generateID()
	return Session{
		ID:        id,
		ProjectID: pid,
		Path:      filepath.Join(SessionsDir(), pid, id+".jsonl"),
	}
}

func generateID() string {
	b := make([]byte, 12)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

func OpenSession(projectRoot, id string) (Session, error) {
	pid := ProjectHash(projectRoot)
	path := filepath.Join(SessionsDir(), pid, id+".jsonl")
	if _, err := os.Stat(path); err != nil {
		return Session{}, fmt.Errorf("session not found: %s", id)
	}
	return Session{ID: id, ProjectID: pid, Path: path}, nil
}

func LatestSession(projectRoot string) (Session, error) {
	pid := ProjectHash(projectRoot)
	dir := filepath.Join(SessionsDir(), pid)
	entries, err := os.ReadDir(dir)
	if err != nil {
		return Session{}, err
	}
	type item struct {
		name string
		path string
		mod  time.Time
	}
	var items []item
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".jsonl") {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		items = append(items, item{name: e.Name(), path: filepath.Join(dir, e.Name()), mod: info.ModTime()})
	}
	if len(items) == 0 {
		return Session{}, fmt.Errorf("no sessions for project")
	}
	sort.Slice(items, func(i, j int) bool { return items[i].mod.After(items[j].mod) })
	id := strings.TrimSuffix(items[0].name, ".jsonl")
	return Session{ID: id, ProjectID: pid, Path: items[0].path}, nil
}

func DeleteSession(projectRoot, id string) error {
	pid := ProjectHash(projectRoot)
	path := filepath.Join(SessionsDir(), pid, id+".jsonl")
	return os.Remove(path)
}

func ListSessions(projectRoot string) ([]Session, error) {
	pid := ProjectHash(projectRoot)
	dir := filepath.Join(SessionsDir(), pid)
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	var out []Session
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".jsonl") {
			continue
		}
		id := strings.TrimSuffix(e.Name(), ".jsonl")
		out = append(out, Session{ID: id, ProjectID: pid, Path: filepath.Join(dir, e.Name())})
	}
	return out, nil
}
