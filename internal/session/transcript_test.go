package session

import (
	"path/filepath"
	"sync"
	"testing"

	"github.com/mintoleda/talos/internal/protocol"
)

func TestTranscriptConcurrentAppend(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "tx.jsonl")
	tx, err := Create(path)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Close()

	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			m := protocol.TextMessage(protocol.RoleUser, string(rune('a'+n%26)))
			if err := tx.Append(m); err != nil {
				t.Errorf("append: %v", err)
			}
		}(i)
	}
	wg.Wait()

	frozen := tx.Frozen()
	if len(frozen) != 50 {
		t.Fatalf("expected 50 messages, got %d", len(frozen))
	}
}

func TestTranscriptAppendLoad(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "tx.jsonl")
	tx, err := Create(path)
	if err != nil {
		t.Fatal(err)
	}
	m := protocol.TextMessage(protocol.RoleUser, "hello")
	if err := tx.Append(m); err != nil {
		t.Fatal(err)
	}
	if err := tx.Close(); err != nil {
		t.Fatal(err)
	}

	tx2, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	frozen := tx2.Frozen()
	if len(frozen) != 1 {
		t.Fatalf("expected 1 message, got %d", len(frozen))
	}
	if frozen[0].Msg.Role != protocol.RoleUser {
		t.Fatalf("wrong role: %v", frozen[0].Msg.Role)
	}
}
