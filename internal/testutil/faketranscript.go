package testutil

import (
	"path/filepath"
	"testing"

	"github.com/mintoleda/talos/internal/session"
)

// NewTestTranscript creates a fresh in-memory-backed transcript in a temp
// directory. The caller should defer tx.Close() to clean up.
func NewTestTranscript(t *testing.T) *session.Transcript {
	t.Helper()
	tx, err := session.Create(filepath.Join(t.TempDir(), "test.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	return tx
}
