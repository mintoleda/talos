package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"syscall"
	"time"

	"github.com/mintoleda/talos/internal/protocol"
)

type bashTool struct {
	cwd            string
	defaultTimeout time.Duration
	maxTimeout     time.Duration
	maxOutput      int
}

func NewBash(cwd string, defaultTimeout, maxTimeout time.Duration, maxOutput int) Tool {
	if defaultTimeout == 0 {
		defaultTimeout = 120 * time.Second
	}
	if maxTimeout == 0 {
		maxTimeout = 600 * time.Second
	}
	if maxOutput == 0 {
		maxOutput = 30 * 1024
	}
	return &bashTool{cwd: cwd, defaultTimeout: defaultTimeout, maxTimeout: maxTimeout, maxOutput: maxOutput}
}

func (t *bashTool) Name() string { return "bash" }

func (t *bashTool) Description() string {
	return "Run a shell command in the working directory via /bin/sh -c. No interactive stdin; commands that prompt will fail. Output is captured (stdout+stderr) and truncated if large. Optional timeout_seconds. The whole process group is killed on timeout or cancellation."
}

func (t *bashTool) Schema() json.RawMessage {
	return json.RawMessage(`{
		"type": "object",
		"properties": {
			"command": {"type": "string", "description": "shell command"},
			"timeout_seconds": {"type": "integer", "description": "timeout in seconds"}
		},
		"required": ["command"]
	}`)
}

func (t *bashTool) Execute(ctx context.Context, args map[string]any) (protocol.ToolResult, error) {
	command, err := str(args, "command")
	if err != nil {
		return errResult(err), nil
	}
	timeout := t.defaultTimeout
	if v, ok := args["timeout_seconds"].(float64); ok {
		timeout = time.Duration(clamp(int(v), 1, int(t.maxTimeout.Seconds()))) * time.Second
	}

	cctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	cmd := exec.CommandContext(cctx, "/bin/sh", "-c", command)
	cmd.Dir = t.cwd
	cmd.Env = nonInteractiveEnv()
	cmd.Stdin = nil
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error {
		if cmd.Process == nil {
			return nil
		}
		return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	}
	cmd.WaitDelay = 2 * time.Second

	cw := &cappedWriter{max: t.maxOutput}
	cmd.Stdout, cmd.Stderr = cw, cw

	runErr := cmd.Run()
	out := cw.String()

	switch {
	case cctx.Err() == context.DeadlineExceeded:
		return okResult(out + fmt.Sprintf("\n[timed out after %s — process group killed]", timeout)), nil
	case runErr != nil:
		return protocol.ToolResult{Content: out + "\n[exit: " + exitCode(runErr) + "]", IsError: true}, nil
	default:
		return okResult(out), nil
	}
}

func nonInteractiveEnv() []string {
	base := os.Environ()
	set := map[string]string{
		"GIT_TERMINAL_PROMPT": "0",
		"DEBIAN_FRONTEND":     "noninteractive",
		"PAGER":               "cat",
	}
	out := make([]string, 0, len(base))
	for _, e := range base {
		if i := strings.Index(e, "="); i > 0 {
			if _, ok := set[e[:i]]; ok {
				continue
			}
		}
		out = append(out, e)
	}
	for k, v := range set {
		out = append(out, k+"="+v)
	}
	return out
}

func exitCode(err error) string {
	if exitErr, ok := err.(*exec.ExitError); ok {
		return fmt.Sprintf("%d", exitErr.ExitCode())
	}
	return "?"
}

func clamp(v, min, max int) int {
	if v < min {
		return min
	}
	if v > max {
		return max
	}
	return v
}

// cappedWriter keeps the first half and last half of the output (head+tail) up
// to max bytes, eliding the middle with a marker. This preserves both the start
// of a command's output and its tail (where errors/exit summaries usually land).
type cappedWriter struct {
	max   int
	head  []byte
	tail  []byte
	total int
}

func (c *cappedWriter) Write(p []byte) (int, error) {
	n0 := len(p)
	c.total += n0
	half := c.max / 2
	if half < 1 {
		half = c.max
	}
	if len(c.head) < half {
		take := half - len(c.head)
		if take > len(p) {
			take = len(p)
		}
		c.head = append(c.head, p[:take]...)
		p = p[take:]
	}
	if len(p) > 0 {
		c.tail = append(c.tail, p...)
		if len(c.tail) > half {
			c.tail = c.tail[len(c.tail)-half:]
		}
	}
	return n0, nil
}

func (c *cappedWriter) String() string {
	if c.total <= len(c.head)+len(c.tail) {
		return string(c.head) + string(c.tail)
	}
	elided := c.total - len(c.head) - len(c.tail)
	return fmt.Sprintf("%s\n[... %d bytes elided ...]\n%s", c.head, elided, c.tail)
}
