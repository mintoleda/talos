package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
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
	reads          *ReadSet
	maxWalkFiles   int
}

func NewBash(cwd string, defaultTimeout, maxTimeout time.Duration, maxOutput int, reads *ReadSet) Tool {
	if defaultTimeout == 0 {
		defaultTimeout = 120 * time.Second
	}
	if maxTimeout == 0 {
		maxTimeout = 600 * time.Second
	}
	if maxOutput == 0 {
		maxOutput = 30 * 1024
	}
	return &bashTool{
		cwd:            cwd,
		defaultTimeout: defaultTimeout,
		maxTimeout:     maxTimeout,
		maxOutput:      maxOutput,
		reads:          reads,
		maxWalkFiles:   50000,
	}
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
	cw := &cappedWriter{max: t.maxOutput}
	return t.runBash(ctx, args, cw)
}

// ExecuteStreaming is the StreamingTool implementation. It runs the command
// and emits ToolOutputDelta events for every chunk of output so the TUI can
// show live output when Ctrl+O is toggled.
func (t *bashTool) ExecuteStreaming(ctx context.Context, args map[string]any, emit protocol.EmitFunc) (protocol.ToolResult, error) {
	cw := &streamingWriter{cappedWriter: cappedWriter{max: t.maxOutput}, emit: emit}
	return t.runBash(ctx, args, cw)
}

func (t *bashTool) runBash(ctx context.Context, args map[string]any, cw io.Writer) (protocol.ToolResult, error) {
	command, err := str(args, "command")
	if err != nil {
		return errResult(err), nil
	}
	timeout := t.defaultTimeout
	if v, ok := args["timeout_seconds"].(float64); ok {
		timeout = time.Duration(clamp(int(v), 1, int(t.maxTimeout.Seconds()))) * time.Second
	}

	var before map[string]time.Time
	if t.reads != nil {
		before, _ = walkModTimes(t.cwd, t.maxWalkFiles)
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

	cmd.Stdout, cmd.Stderr = cw, cw

	runErr := cmd.Run()

	var out string
	if scw, ok := cw.(interface{ String() string }); ok {
		out = scw.String()
	}

	if t.reads != nil && before != nil {
		after, _ := walkModTimes(t.cwd, t.maxWalkFiles)
		changed := diffModTimes(before, after)
		var unread []string
		for _, p := range changed {
			if !t.reads.WasSeen(p) {
				unread = append(unread, p)
			}
		}
		if len(changed) > 0 {
			t.reads.MarkStaleBatch(changed)
		}
		if len(unread) > 0 {
			const maxWarn = 5
			shown := unread
			extra := ""
			if len(unread) > maxWarn {
				shown = unread[:maxWarn]
				extra = fmt.Sprintf(" (+%d more)", len(unread)-maxWarn)
			}
			out += fmt.Sprintf(
				"\n[⚠ unread files modified by this command:%s %s. "+
					"The read-before-write rule was bypassed. "+
					"Use `read` + `edit`/`write` instead. "+
					"Revert these changes if they were not intentional.]",
				extra, strings.Join(shown, ", "))
		}
	}

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

// streamingWriter wraps cappedWriter and emits ToolOutputDelta events for every
// Write call so the TUI can show live output. The ID field on each delta is left
// empty — the executor injects the correct tool-use ID before forwarding.
type streamingWriter struct {
	cappedWriter
	emit protocol.EmitFunc
}

func (s *streamingWriter) Write(p []byte) (int, error) {
	if s.emit != nil {
		s.emit(protocol.ToolOutputDelta{Text: string(p)})
	}
	return s.cappedWriter.Write(p)
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
