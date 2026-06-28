package safety

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func initRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	run := func(args ...string) {
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	run("init")
	run("config", "user.email", "test@example.com")
	run("config", "user.name", "Test")
	os.WriteFile(filepath.Join(dir, "tracked.txt"), []byte("v1\n"), 0o644)
	run("add", ".")
	run("commit", "-m", "initial")
	return dir
}

func TestCheckpointSnapshotRestore(t *testing.T) {
	repo := initRepo(t)
	cp := NewCheckpointer(repo)

	// Modify a tracked file and add an untracked one, then snapshot.
	os.WriteFile(filepath.Join(repo, "tracked.txt"), []byte("v2\n"), 0o644)
	os.WriteFile(filepath.Join(repo, "untracked.txt"), []byte("new\n"), 0o644)

	ref, err := cp.Snapshot("test")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(ref, "refs/checkpoints/") {
		t.Fatalf("unexpected ref: %s", ref)
	}

	// Mutate further, then restore from the checkpoint.
	os.WriteFile(filepath.Join(repo, "tracked.txt"), []byte("v3\n"), 0o644)
	os.Remove(filepath.Join(repo, "untracked.txt"))
	if err := cp.Restore(ref); err != nil {
		t.Fatal(err)
	}

	if b, _ := os.ReadFile(filepath.Join(repo, "tracked.txt")); string(b) != "v2\n" {
		t.Fatalf("tracked file not restored: %q", b)
	}
	if b, _ := os.ReadFile(filepath.Join(repo, "untracked.txt")); string(b) != "new\n" {
		t.Fatalf("untracked file not restored: %q", b)
	}

	// git log (no --all) must stay clean: still just the initial commit.
	out, err := exec.Command("git", "-C", repo, "log", "--oneline").CombinedOutput()
	if err != nil {
		t.Fatal(err)
	}
	if n := len(strings.Fields(strings.TrimSpace(string(out)))); n == 0 {
		t.Fatal("expected at least the initial commit")
	}
	if strings.Count(string(out), "\n") > 1 {
		t.Fatalf("git log should not show checkpoint commits:\n%s", out)
	}

	refs, err := cp.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(refs) != 1 || refs[0] != ref {
		t.Fatalf("expected exactly the one checkpoint ref, got %v", refs)
	}
}
