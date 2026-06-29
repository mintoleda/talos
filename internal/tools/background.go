package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"sync"
	"syscall"

	"github.com/mintoleda/talos/internal/protocol"
)

// BackgroundRegistry owns long-running child processes spawned by the
// bash_background tool. It is safe for concurrent use.
type BackgroundRegistry struct {
	mu    sync.Mutex
	procs map[string]*backgroundProc
	cwd   string
}

type backgroundProc struct {
	id     string
	cmd    *exec.Cmd
	buffer *ringBuffer
}

func NewBackgroundRegistry(cwd string) *BackgroundRegistry {
	return &BackgroundRegistry{procs: make(map[string]*backgroundProc), cwd: cwd}
}

func (r *BackgroundRegistry) Start(command string) (string, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	id := fmt.Sprintf("bg-%d", len(r.procs)+1)
	cmd := exec.Command("/bin/sh", "-c", command)
	cmd.Dir = r.cwd
	cmd.Env = nonInteractiveEnv()
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}

	buf := newRingBuffer(32 * 1024)
	cmd.Stdout = buf
	cmd.Stderr = buf
	if err := cmd.Start(); err != nil {
		return "", err
	}
	r.procs[id] = &backgroundProc{id: id, cmd: cmd, buffer: buf}
	return id, nil
}

// Read returns the accumulated output for a handle and clears it.
func (r *BackgroundRegistry) Read(id string) (string, error) {
	r.mu.Lock()
	p, ok := r.procs[id]
	r.mu.Unlock()
	if !ok {
		return "", fmt.Errorf("unknown background handle: %s", id)
	}
	return p.buffer.Drain(), nil
}

// Kill terminates a background process and its process group.
func (r *BackgroundRegistry) Kill(id string) error {
	r.mu.Lock()
	p, ok := r.procs[id]
	delete(r.procs, id)
	r.mu.Unlock()
	if !ok {
		return fmt.Errorf("unknown background handle: %s", id)
	}
	if p.cmd.Process == nil {
		return nil
	}
	return syscall.Kill(-p.cmd.Process.Pid, syscall.SIGKILL)
}

func (r *BackgroundRegistry) KillAll() {
	r.mu.Lock()
	ids := make([]string, 0, len(r.procs))
	for id := range r.procs {
		ids = append(ids, id)
	}
	r.mu.Unlock()
	for _, id := range ids {
		_ = r.Kill(id)
	}
}

// ringBuffer is a thread-safe capped buffer that keeps the most recent bytes.
type ringBuffer struct {
	mu   sync.Mutex
	data []byte
	max  int
}

func newRingBuffer(max int) *ringBuffer {
	return &ringBuffer{max: max}
}

func (r *ringBuffer) Write(p []byte) (int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.data = append(r.data, p...)
	if len(r.data) > r.max {
		r.data = r.data[len(r.data)-r.max:]
	}
	return len(p), nil
}

func (r *ringBuffer) Drain() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	s := string(r.data)
	r.data = r.data[:0]
	return s
}

type bashBackgroundTool struct {
	cwd string
	reg *BackgroundRegistry
}

func NewBashBackground(cwd string, reg *BackgroundRegistry) Tool {
	return &bashBackgroundTool{cwd: cwd, reg: reg}
}

func (t *bashBackgroundTool) Name() string { return "bash_background" }
func (t *bashBackgroundTool) Description() string {
	return "Run a shell command in the background and return a handle. The process group is killed when the session ends."
}
func (t *bashBackgroundTool) Schema() json.RawMessage {
	return json.RawMessage(`{"type":"object","properties":{"command":{"type":"string","description":"shell command to run in the background"}},"required":["command"]}`)
}
func (t *bashBackgroundTool) Execute(ctx context.Context, args map[string]any) (protocol.ToolResult, error) {
	command, err := str(args, "command")
	if err != nil {
		return errResult(err), nil
	}
	id, err := t.reg.Start(command)
	if err != nil {
		return errResult(err), nil
	}
	return okResult(fmt.Sprintf("started background process: %s", id)), nil
}

type bashReadOutputTool struct {
	reg *BackgroundRegistry
}

func NewBashReadOutput(reg *BackgroundRegistry) Tool {
	return &bashReadOutputTool{reg: reg}
}

func (t *bashReadOutputTool) Name() string { return "bash_read_output" }
func (t *bashReadOutputTool) Description() string {
	return "Read accumulated output from a background process handle."
}
func (t *bashReadOutputTool) Schema() json.RawMessage {
	return json.RawMessage(`{"type":"object","properties":{"handle":{"type":"string"}},"required":["handle"]}`)
}
func (t *bashReadOutputTool) Execute(ctx context.Context, args map[string]any) (protocol.ToolResult, error) {
	id, err := str(args, "handle")
	if err != nil {
		return errResult(err), nil
	}
	out, err := t.reg.Read(id)
	if err != nil {
		return errResult(err), nil
	}
	return okResult(out), nil
}

type bashKillTool struct {
	reg *BackgroundRegistry
}

func NewBashKill(reg *BackgroundRegistry) Tool {
	return &bashKillTool{reg: reg}
}

func (t *bashKillTool) Name() string        { return "bash_kill" }
func (t *bashKillTool) Description() string { return "Kill a background process by handle." }
func (t *bashKillTool) Schema() json.RawMessage {
	return json.RawMessage(`{"type":"object","properties":{"handle":{"type":"string"}},"required":["handle"]}`)
}
func (t *bashKillTool) Execute(ctx context.Context, args map[string]any) (protocol.ToolResult, error) {
	id, err := str(args, "handle")
	if err != nil {
		return errResult(err), nil
	}
	if err := t.reg.Kill(id); err != nil {
		return errResult(err), nil
	}
	return okResult(fmt.Sprintf("killed %s", id)), nil
}
