package server

import (
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestWriteReadDiscovery(t *testing.T) {
	dir := t.TempDir()
	sockPath := filepath.Join(dir, "daemon.sock")

	ln, err := net.Listen("unix", sockPath)
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer ln.Close()

	path := DiscoveryPath(dir)
	want := Discovery{
		PID:       os.Getpid(),
		Socket:    sockPath,
		WS:        "127.0.0.1:7461",
		Token:     "deadbeef",
		Version:   "0.2.0",
		StartedAt: time.Date(2026, 7, 9, 12, 0, 0, 0, time.UTC),
	}
	if err := WriteDiscovery(path, want); err != nil {
		t.Fatalf("WriteDiscovery: %v", err)
	}

	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("expected mode 0600, got %o", info.Mode().Perm())
	}

	got, err := ReadDiscovery(path)
	if err != nil {
		t.Fatalf("ReadDiscovery: %v", err)
	}
	if got.PID != want.PID || got.Socket != want.Socket || got.WS != want.WS ||
		got.Token != want.Token || got.Version != want.Version {
		t.Fatalf("mismatch:\n  got  %+v\n  want %+v", got, want)
	}
	if !got.StartedAt.Equal(want.StartedAt) {
		t.Fatalf("StartedAt: got %v, want %v", got.StartedAt, want.StartedAt)
	}
}

func TestReadDiscoveryStale(t *testing.T) {
	dir := t.TempDir()
	path := DiscoveryPath(dir)
	d := Discovery{
		PID:     1,
		Socket:  filepath.Join(dir, "missing.sock"),
		Token:   "x",
		Version: "0.2.0",
	}
	if err := WriteDiscovery(path, d); err != nil {
		t.Fatalf("WriteDiscovery: %v", err)
	}
	_, err := ReadDiscovery(path)
	if err == nil {
		t.Fatal("expected stale error")
	}
}

func TestReadDiscoveryMissing(t *testing.T) {
	_, err := ReadDiscovery(filepath.Join(t.TempDir(), "nope.json"))
	if err == nil {
		t.Fatal("expected error for missing file")
	}
}

func TestRemoveDiscovery(t *testing.T) {
	dir := t.TempDir()
	path := DiscoveryPath(dir)
	if err := WriteDiscovery(path, Discovery{Socket: "/tmp/x", Token: "t", Version: "0.2.0"}); err != nil {
		t.Fatalf("WriteDiscovery: %v", err)
	}
	if err := RemoveDiscovery(path); err != nil {
		t.Fatalf("RemoveDiscovery: %v", err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatal("expected file removed")
	}
	if err := RemoveDiscovery(path); err != nil {
		t.Fatalf("RemoveDiscovery missing: %v", err)
	}
}

func TestDiscoveryJSONShape(t *testing.T) {
	// Golden shape eyeballed against the plan / TS consumers.
	d := Discovery{
		PID:       1234,
		Socket:    "/home/u/.talos/daemon.sock",
		WS:        "127.0.0.1:7461",
		Token:     "abc",
		Version:   "0.2.0",
		StartedAt: time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC),
	}
	raw, err := json.Marshal(d)
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"pid", "socket", "ws", "token", "version", "started_at"} {
		if _, ok := m[key]; !ok {
			t.Fatalf("missing key %q in %s", key, raw)
		}
	}
}
