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
	bt := NewBash(t.TempDir(), 0, 0, 0)

	// Write a unique marker so we can find the child via ps.
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
	cancel() // simulate user interrupt
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("bash Execute did not return after cancel")
	}

	// Give the OS a moment to reap, then assert no orphaned sleep with our marker.
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
	bt := NewBash(t.TempDir(), time.Second, 5*time.Second, 4096)
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
	bt := NewBash(t.TempDir(), 0, 0, 0)
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
