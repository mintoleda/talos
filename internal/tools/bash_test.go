package tools

import (
	"context"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"
)

func TestBashProcessGroupKill(t *testing.T) {
	if _, err := exec.LookPath("ps"); err != nil {
		t.Skip("ps not available")
	}
	bt := NewBash(t.TempDir(), 0, 0, 0, nil)

	marker := "talos_pgtest_marker_42"
	ctx, cancel := context.WithCancel(context.Background())

	done := make(chan struct{})
	go func() {
		// A child sleep that outlives the parent shell if not group-killed.
		_, _ = bt.Execute(ctx, map[string]any{
			"command": "sleep 30 # " + marker,
		})
		close(done)
	}()

	time.Sleep(300 * time.Millisecond)
	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("bash Execute did not return after cancel")
	}

	time.Sleep(300 * time.Millisecond)
	out, _ := exec.Command("ps", "-ax", "-o", "command").CombinedOutput()
	if strings.Contains(string(out), marker) {
		t.Fatalf("orphaned process survived cancel:\n%s", grepMarker(string(out), marker))
	}
}

func grepMarker(s, marker string) string {
	var keep []string
	for _, line := range strings.Split(s, "\n") {
		if strings.Contains(line, marker) {
			keep = append(keep, line)
		}
	}
	return strings.Join(keep, "\n")
}

func TestBashTimeout(t *testing.T) {
	bt := NewBash(t.TempDir(), time.Second, 5*time.Second, 4096, nil)
	res, err := bt.Execute(context.Background(), map[string]any{
		"command":         "sleep 10",
		"timeout_seconds": float64(1),
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(res.Content, "timed out") {
		t.Fatalf("expected timeout note, got: %q", res.Content)
	}
}

func TestBashNonZeroExit(t *testing.T) {
	bt := NewBash(t.TempDir(), 0, 0, 0, nil)
	res, err := bt.Execute(context.Background(), map[string]any{"command": "exit 3"})
	if err != nil {
		t.Fatal(err)
	}
	if !res.IsError || !strings.Contains(res.Content, "exit: 3") {
		t.Fatalf("expected exit:3 error result, got: %+v", res)
	}
}

func TestCappedWriterHeadAndTail(t *testing.T) {
	cw := &cappedWriter{max: 20}
	_, _ = cw.Write([]byte(strings.Repeat("A", 30)))
	_, _ = cw.Write([]byte(strings.Repeat("B", 30)))
	out := cw.String()
	if !strings.HasPrefix(out, "AAAA") {
		t.Fatalf("expected head preserved, got: %q", out)
	}
	if !strings.HasSuffix(out, "BBBB") {
		t.Fatalf("expected tail preserved, got: %q", out)
	}
	if !strings.Contains(out, "elided") {
		t.Fatalf("expected elision marker, got: %q", out)
	}
}

func TestBashUnreadFileMutationWarning(t *testing.T) {
	dir := t.TempDir()
	rs := NewReadSet()
	bt := NewBash(dir, 0, 0, 0, rs)

	res, err := bt.Execute(context.Background(), map[string]any{
		"command": "echo hello > newfile.txt",
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(res.Content, "unread files") {
		t.Fatalf("expected unread-files warning, got: %q", res.Content)
	}
	if !strings.Contains(res.Content, "newfile.txt") {
		t.Fatalf("expected newfile.txt in warning, got: %q", res.Content)
	}
	if rs.WasSeen("newfile.txt") {
		t.Fatal("newfile.txt should not be in ReadSet (never read)")
	}
}

func TestBashReadFileMutationNoWarning(t *testing.T) {
	dir := t.TempDir()
	rs := NewReadSet()
	path := dir + "/test.go"
	if err := os.WriteFile(path, []byte("package p"), 0644); err != nil {
		t.Fatal(err)
	}
	rs.Record(path)

	bt := NewBash(dir, 0, 0, 0, rs)
	res, err := bt.Execute(context.Background(), map[string]any{
		"command": "echo 'package p\n\nfunc F() {}' > test.go",
	})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(res.Content, "unread files") {
		t.Fatalf("should not warn for files already in ReadSet, got: %q", res.Content)
	}
	if rs.WasSeen(path) {
		t.Fatal("test.go should be marked stale after bash modification")
	}
}

func TestBashReadOnlyCommandNoWarning(t *testing.T) {
	dir := t.TempDir()
	rs := NewReadSet()
	bt := NewBash(dir, 0, 0, 0, rs)

	res, err := bt.Execute(context.Background(), map[string]any{
		"command": "echo hello world",
	})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(res.Content, "unread files") {
		t.Fatalf("expected no warning for read-only command, got: %q", res.Content)
	}
}

func TestMarkStaleBatch(t *testing.T) {
	dir := t.TempDir()
	rs := NewReadSet()
	paths := []string{dir + "/a.go", dir + "/b.go", dir + "/c.go"}
	for _, p := range paths {
		os.WriteFile(p, []byte("x"), 0644)
		rs.Record(p)
	}
	for _, p := range paths {
		if !rs.WasSeen(p) {
			t.Fatalf("%s should be seen after Record", p)
		}
	}

	rs.MarkStaleBatch(paths[:2])

	if rs.WasSeen(paths[0]) || rs.WasSeen(paths[1]) {
		t.Fatal("a.go and b.go should be stale after MarkStaleBatch")
	}
	if !rs.WasSeen(paths[2]) {
		t.Fatal("c.go should remain seen")
	}
}

func TestWalkModTimes(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(dir+"/a.txt", []byte("a"), 0644)
	os.WriteFile(dir+"/b.txt", []byte("b"), 0644)
	os.MkdirAll(dir+"/.git/objects", 0755)
	os.WriteFile(dir+"/.git/config", []byte("x"), 0644)
	os.MkdirAll(dir+"/node_modules/pkg", 0755)
	os.WriteFile(dir+"/node_modules/pkg/index.js", []byte("x"), 0644)

	before, err := walkModTimes(dir, 1000)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := before[dir+"/a.txt"]; !ok {
		t.Fatal("a.txt missing from walk")
	}
	if _, ok := before[dir+"/.git/config"]; ok {
		t.Fatal(".git should be skipped")
	}
	if _, ok := before[dir+"/node_modules/pkg/index.js"]; ok {
		t.Fatal("node_modules should be skipped")
	}
}

func TestNonInteractiveEnv(t *testing.T) {
	env := nonInteractiveEnv()
	want := map[string]bool{"GIT_TERMINAL_PROMPT=0": false, "PAGER=cat": false}
	for _, e := range env {
		if _, ok := want[e]; ok {
			want[e] = true
		}
	}
	for k, found := range want {
		if !found {
			t.Fatalf("expected env to contain %q", k)
		}
	}
	_ = os.Environ
}
