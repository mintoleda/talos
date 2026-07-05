package notify

import (
	"testing"

	"github.com/mintoleda/talos/internal/protocol"
)

func TestSend_DoesNotPanic(t *testing.T) {
	// Send should never panic regardless of platform conditions.
	Send("test title", "test body")
}

func TestWrap_DisabledPreservesEmit(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Enabled = false

	var got bool
	emit := protocol.EmitFunc(func(e protocol.Event) {
		if _, ok := e.(protocol.TurnEnded); ok {
			got = true
		}
	})

	wrapped := Wrap(emit, cfg)
	if wrapped == nil {
		t.Fatal("Wrap returned nil")
	}

	wrapped(protocol.TurnEnded{StopReason: "test"})
	if !got {
		t.Error("disabled Wrap: event was not forwarded to original emit")
	}
}

func TestWrap_EnabledForwardsEvents(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Enabled = true

	var calls int
	emit := protocol.EmitFunc(func(protocol.Event) { calls++ })

	wrapped := Wrap(emit, cfg)
	if wrapped == nil {
		t.Fatal("Wrap returned nil")
	}

	wrapped(protocol.TurnEnded{StopReason: "stop"})
	if calls != 1 {
		t.Errorf("expected 1 call to original emit, got %d", calls)
	}
}

func TestWrap_EnabledPermission(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Enabled = true
	cfg.NotifyOnPermission = true

	ch := make(chan bool, 1)
	emit := protocol.EmitFunc(func(protocol.Event) {})

	wrapped := Wrap(emit, cfg)
	if wrapped == nil {
		t.Fatal("Wrap returned nil")
	}

	// This should not deadlock; the notification is async.
	wrapped(protocol.PermissionRequested{
		ToolName: "bash",
		Command:  "rm -rf /",
		Reason:   "dangerous command",
		ReplyCh:  ch,
	})
}

func TestDefaultConfig(t *testing.T) {
	cfg := DefaultConfig()
	if cfg.Enabled {
		t.Error("DefaultConfig should have Enabled=false (opt-in)")
	}
	if !cfg.NotifyOnPermission {
		t.Error("DefaultConfig should notify on permission")
	}
	if !cfg.NotifyOnTurnEnded {
		t.Error("DefaultConfig should notify on turn ended")
	}
	if !cfg.NotifyOnError {
		t.Error("DefaultConfig should notify on errors")
	}
}
