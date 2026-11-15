package session

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// SessionDeletions separates explicit deletions from legacy orphaned sidecars.
// A missing transcript alone is not proof of deletion: session/new is lazy.
type SessionDeletions struct {
	DeletedSessionIDs []string `json:"deletedSessionIds"`
	MissingSessionIDs []string `json:"missingSessionIds"`
}

// ListSessionDeletions is read-only. Errors never become an empty inventory
// that clients might mistake for authoritative deletion.
func ListSessionDeletions() (SessionDeletions, error) {
	result := SessionDeletions{DeletedSessionIDs: []string{}, MissingSessionIDs: []string{}}
	projects, err := os.ReadDir(SessionsDir())
	if os.IsNotExist(err) {
		return result, nil
	}
	if err != nil {
		return result, err
	}
	deleted, missing := map[string]bool{}, map[string]bool{}
	for _, project := range projects {
		if !project.IsDir() {
			continue
		}
		dir := filepath.Join(SessionsDir(), project.Name())
		files, err := os.ReadDir(dir)
		if err != nil {
			return result, err
		}
		for _, file := range files {
			name := file.Name()
			explicit := strings.HasSuffix(name, ".deleted")
			if file.IsDir() || (!explicit && !strings.HasSuffix(name, ".meta.json")) {
				continue
			}
			id := strings.TrimSuffix(name, ".meta.json")
			if explicit {
				id = strings.TrimSuffix(name, ".deleted")
			}
			if _, err := os.Stat(filepath.Join(dir, id+".jsonl")); err == nil {
				continue
			} else if !os.IsNotExist(err) {
				return result, err
			}
			if explicit {
				deleted[id] = true
			} else {
				missing[id] = true
			}
		}
	}
	for id := range deleted {
		result.DeletedSessionIDs = append(result.DeletedSessionIDs, id)
	}
	for id := range missing {
		if !deleted[id] {
			result.MissingSessionIDs = append(result.MissingSessionIDs, id)
		}
	}
	sort.Strings(result.DeletedSessionIDs)
	sort.Strings(result.MissingSessionIDs)
	return result, nil
}
