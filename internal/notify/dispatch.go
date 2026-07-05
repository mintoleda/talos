// Package notify dispatches desktop notifications using platform-specific
// mechanisms. It tries notify-send (FreeDesktop) on any platform where it's
// available, osascript on macOS, and falls back to a terminal bell character.
package notify

import (
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"strings"
)

// Send dispatches a desktop notification with the given title and body.
// It is safe to call from any goroutine. Errors are silently dropped so the
// calling path is never disrupted.
func Send(title, body string) {
	_ = send(title, body)
}

// send attempts each platform-specific method. Returns true if a desktop
// notification was successfully dispatched (not just a terminal bell).
func send(title, body string) bool {
	if tryNotifySend(title, body) {
		return true
	}

	if runtime.GOOS == "darwin" {
		if tryOSAScript(title, body) {
			return true
		}
	}

	fmt.Fprint(os.Stderr, "\a")
	return false
}

func tryNotifySend(title, body string) bool {
	cmd := exec.Command("notify-send", "--app-name", "talos", title, body)
	return cmd.Run() == nil
}

func tryOSAScript(title, body string) bool {
	title = strings.ReplaceAll(title, `"`, `\"`)
	body = strings.ReplaceAll(body, `"`, `\"`)
	script := fmt.Sprintf(`display notification "%s" with title "%s"`, body, title)
	cmd := exec.Command("osascript", "-e", script)
	return cmd.Run() == nil
}
