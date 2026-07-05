package notify

import (
	"fmt"

	"github.com/mintoleda/talos/internal/protocol"
)

// Config controls which events trigger desktop notifications.
type Config struct {
	// Enabled is the master switch. When false, no notifications are sent.
	Enabled bool

	// NotifyOnPermission sends a notification when a tool requires user
	// approval (PermissionRequested event).
	NotifyOnPermission bool

	// NotifyOnTurnEnded sends a notification when the agent finishes a
	// turn (TurnEnded event — the agent has completed its response).
	NotifyOnTurnEnded bool

	// NotifyOnError sends a notification when the loop emits a Notice
	// with Level "error".
	NotifyOnError bool
}

// DefaultConfig returns a Config with sensible defaults — notifications are
// off by default (opt-in via config.toml) but when enabled all important
// event types trigger a desktop notification.
func DefaultConfig() Config {
	return Config{
		Enabled:             false,
		NotifyOnPermission:  true,
		NotifyOnTurnEnded:   true,
		NotifyOnError:       true,
	}
}

// Wrap returns an EmitFunc that wraps emit with desktop notification
// dispatch according to cfg. The original event is always forwarded to
// emit unchanged; notifications are fired asynchronously so they never
// block the event pipeline.
func Wrap(emit protocol.EmitFunc, cfg Config) protocol.EmitFunc {
	if !cfg.Enabled {
		return emit
	}
	return func(e protocol.Event) {
		emit(e)
		switch ev := e.(type) {
		case protocol.PermissionRequested:
			if cfg.NotifyOnPermission {
				go Send("Talos: Action Required",
					fmt.Sprintf("Tool %q needs approval: %s", ev.ToolName, ev.Reason))
			}
		case protocol.TurnEnded:
			if cfg.NotifyOnTurnEnded {
				body := "Response complete"
				if ev.StopReason != "" && ev.StopReason != "stop" {
					body = fmt.Sprintf("Response complete (%s)", ev.StopReason)
				}
				go Send("Talos", body)
			}
		case protocol.Notice:
			if cfg.NotifyOnError && ev.Level == "error" {
				go Send("Talos: Error", ev.Text)
			}
		}
	}
}
