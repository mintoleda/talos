package acp

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/mintoleda/talos/internal/config"
	"github.com/mintoleda/talos/internal/session"
)

func TestSessionDeletionExtension(t *testing.T) {
	_, base := isolate(t)
	a := newAgent(&config.Config{BaseDir: base})
	raw, err := a.initialize(json.RawMessage(`{"protocolVersion":1}`))
	if err != nil {
		t.Fatal(err)
	}
	caps := raw.(InitializeResponse).AgentCapabilities.Extensions["talos"].(map[string]any)
	if caps["sessionDeletions"] != "talos/list_deleted_sessions" {
		t.Fatalf("missing deletion capability: %+v", caps)
	}
	project := t.TempDir()
	sess := session.SessionAt(project, "deleted")
	if err := os.MkdirAll(filepath.Dir(sess.Path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(sess.Path, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := session.DeleteSession(project, sess.ID); err != nil {
		t.Fatal(err)
	}
	value, err := a.handle(context.Background(), "talos/list_deleted_sessions", json.RawMessage("{}"))
	if err != nil {
		t.Fatal(err)
	}
	got := value.(session.SessionDeletions)
	if len(got.DeletedSessionIDs) != 1 || got.DeletedSessionIDs[0] != sess.ID || len(got.MissingSessionIDs) != 0 {
		t.Fatalf("unexpected inventory: %+v", got)
	}
}
