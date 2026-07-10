package tools

import (
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/mintoleda/talos/internal/protocol"
)

func TestBackgroundIDMonotonicAfterKill(t *testing.T) {
	reg := NewBackgroundRegistry(t.TempDir())
	id1, err := reg.Start("sleep 30")
	if err != nil {
		t.Fatal(err)
	}
	if id1 != "bg-1" {
		t.Fatalf("want bg-1, got %s", id1)
	}
	id2, err := reg.Start("sleep 30")
	if err != nil {
		t.Fatal(err)
	}
	if id2 != "bg-2" {
		t.Fatalf("want bg-2, got %s", id2)
	}
	if err := reg.Kill(id1); err != nil {
		t.Fatal(err)
	}
	waitExited(t, reg, id1)
	id3, err := reg.Start("sleep 30")
	if err != nil {
		t.Fatal(err)
	}
	if id3 != "bg-3" {
		t.Fatalf("collision: want bg-3 after kill, got %s", id3)
	}
	// bg-2 must still be present and running.
	list := reg.List()
	var found2 bool
	for _, s := range list {
		if s.ID == id2 {
			found2 = true
			if !s.Running {
				t.Fatal("bg-2 should still be running")
			}
		}
	}
	if !found2 {
		t.Fatal("bg-2 missing after reusing old len-based ID path")
	}
	reg.KillAll()
}

func TestBackgroundWaitSetsExitCode(t *testing.T) {
	reg := NewBackgroundRegistry(t.TempDir())
	id, err := reg.Start("exit 7")
	if err != nil {
		t.Fatal(err)
	}
	waitExited(t, reg, id)
	list := reg.List()
	if len(list) != 1 {
		t.Fatalf("want 1 proc, got %d", len(list))
	}
	if list[0].Running {
		t.Fatal("expected exited")
	}
	if list[0].ExitCode != 7 {
		t.Fatalf("want exit 7, got %d", list[0].ExitCode)
	}
}

func TestBackgroundUIBufferNotDrainedByRead(t *testing.T) {
	reg := NewBackgroundRegistry(t.TempDir())
	id, err := reg.Start(`printf 'hello-ui-buffer\n'`)
	if err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(2 * time.Second)
	var modelOut, uiOut string
	for time.Now().Before(deadline) {
		modelOut, _ = reg.Read(id)
		uiOut, _ = reg.UILog(id, 0)
		if strings.Contains(modelOut, "hello-ui-buffer") || strings.Contains(uiOut, "hello-ui-buffer") {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	// Drain model buffer (may have already drained in loop).
	_, _ = reg.Read(id)
	uiOut, err = reg.UILog(id, 0)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(uiOut, "hello-ui-buffer") {
		t.Fatalf("UI buffer drained by Read; got %q", uiOut)
	}
	waitExited(t, reg, id)
}

func TestBackgroundCoalescedEmit(t *testing.T) {
	reg := NewBackgroundRegistry(t.TempDir())
	var mu sync.Mutex
	var outputs []string
	reg.SetEmit(func(ev protocol.Event) {
		if o, ok := ev.(protocol.BgOutput); ok {
			mu.Lock()
			outputs = append(outputs, o.Text)
			mu.Unlock()
		}
	})
	id, err := reg.Start(`printf 'coalesce-line\n'`)
	if err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		mu.Lock()
		n := len(outputs)
		mu.Unlock()
		if n > 0 {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	mu.Lock()
	defer mu.Unlock()
	joined := strings.Join(outputs, "")
	if !strings.Contains(joined, "coalesce-line") {
		t.Fatalf("expected BgOutput with coalesce-line, got %#v", outputs)
	}
	waitExited(t, reg, id)
}

func TestBackgroundDismissRemoves(t *testing.T) {
	reg := NewBackgroundRegistry(t.TempDir())
	id, err := reg.Start("exit 0")
	if err != nil {
		t.Fatal(err)
	}
	waitExited(t, reg, id)
	if err := reg.Dismiss(id); err != nil {
		t.Fatal(err)
	}
	if len(reg.List()) != 0 {
		t.Fatal("expected empty after dismiss")
	}
}

func TestRingBufferPeek(t *testing.T) {
	r := newRingBuffer(64)
	_, _ = r.Write([]byte("abcdefghij"))
	if got := r.Peek(4); got != "ghij" {
		t.Fatalf("Peek(4)=%q", got)
	}
	if got := r.Drain(); got != "abcdefghij" {
		t.Fatalf("Drain=%q", got)
	}
	if got := r.Peek(0); got != "" {
		t.Fatalf("Peek after drain=%q", got)
	}
}

func waitExited(t *testing.T, reg *BackgroundRegistry, id string) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		for _, s := range reg.List() {
			if s.ID == id && !s.Running {
				return
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s to exit", id)
}
