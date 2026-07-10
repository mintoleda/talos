package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"sync"
	"syscall"
	"time"

	"github.com/mintoleda/talos/internal/protocol"
)

const (
	modelBufSize   = 32 * 1024
	uiBufSize      = 256 * 1024
	coalesceBytes  = 4 * 1024
	coalescePeriod = 100 * time.Millisecond
	snapshotTail   = 2 * 1024
)

// BackgroundRegistry owns long-running child processes spawned by the
// bash_background tool. It is safe for concurrent use.
type BackgroundRegistry struct {
	mu     sync.Mutex
	procs  map[string]*backgroundProc
	cwd    string
	nextID int
	emit   protocol.EmitFunc // nil = silent (tests OK)
}

type backgroundProc struct {
	id        string
	command   string
	dir       string
	cmd       *exec.Cmd
	modelBuf  *ringBuffer // 32KB, Drain() for bash_read_output
	uiBuf     *ringBuffer // 256KB, Peek()/String() non-draining for UI
	startedAt time.Time
	exited    bool
	exitCode  int
	coal      *outputCoalescer
}

// BgProcSnapshot is a point-in-time view of a background process.
type BgProcSnapshot struct {
	ID           string
	Command      string
	Dir          string
	Running      bool
	ExitCode     int
	StartedAt    time.Time
	RecentOutput string
}

func NewBackgroundRegistry(cwd string) *BackgroundRegistry {
	return &BackgroundRegistry{procs: make(map[string]*backgroundProc), cwd: cwd}
}

// SetEmit wires lifecycle/output events onto the session event stream.
// Passing nil disables emission (silent; used by tools tests).
func (r *BackgroundRegistry) SetEmit(fn protocol.EmitFunc) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.emit = fn
}

func (r *BackgroundRegistry) emitLocked(ev protocol.Event) {
	if r.emit != nil {
		r.emit(ev)
	}
}

func (r *BackgroundRegistry) Start(command string) (string, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	r.nextID++
	id := fmt.Sprintf("bg-%d", r.nextID)
	cmd := exec.Command("/bin/sh", "-c", command)
	cmd.Dir = r.cwd
	cmd.Env = nonInteractiveEnv()
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}

	modelBuf := newRingBuffer(modelBufSize)
	uiBuf := newRingBuffer(uiBufSize)
	coal := newOutputCoalescer(id, func() protocol.EmitFunc {
		r.mu.Lock()
		defer r.mu.Unlock()
		return r.emit
	})

	tee := &teeWriter{model: modelBuf, ui: uiBuf, coal: coal}
	cmd.Stdout = tee
	cmd.Stderr = tee

	startedAt := time.Now()
	if err := cmd.Start(); err != nil {
		coal.stop()
		return "", err
	}

	p := &backgroundProc{
		id:        id,
		command:   command,
		dir:       r.cwd,
		cmd:       cmd,
		modelBuf:  modelBuf,
		uiBuf:     uiBuf,
		startedAt: startedAt,
		coal:      coal,
	}
	r.procs[id] = p
	r.emitLocked(protocol.BgStarted{ID: id, Command: command, Dir: r.cwd})

	go r.waitProc(p)
	return id, nil
}

func (r *BackgroundRegistry) waitProc(p *backgroundProc) {
	err := p.cmd.Wait()
	code := 0
	if err != nil {
		if ee, ok := err.(*exec.ExitError); ok {
			code = ee.ExitCode()
		} else {
			code = -1
		}
	}

	p.coal.flush()
	p.coal.stop()

	r.mu.Lock()
	defer r.mu.Unlock()
	if cur, ok := r.procs[p.id]; !ok || cur != p {
		return
	}
	p.exited = true
	p.exitCode = code
	r.emitLocked(protocol.BgExited{ID: p.id, Code: code})
}

// Read returns the accumulated model-facing output for a handle and clears it.
func (r *BackgroundRegistry) Read(id string) (string, error) {
	r.mu.Lock()
	p, ok := r.procs[id]
	r.mu.Unlock()
	if !ok {
		return "", fmt.Errorf("unknown background handle: %s", id)
	}
	return p.modelBuf.Drain(), nil
}

// Kill signals a background process (and its process group) with SIGKILL.
// The process stays in the registry until Dismiss so the UI can show exit state.
func (r *BackgroundRegistry) Kill(id string) error {
	r.mu.Lock()
	p, ok := r.procs[id]
	r.mu.Unlock()
	if !ok {
		return fmt.Errorf("unknown background handle: %s", id)
	}
	if p.cmd.Process == nil {
		return nil
	}
	r.mu.Lock()
	exited := p.exited
	r.mu.Unlock()
	if exited {
		return nil
	}
	return syscall.Kill(-p.cmd.Process.Pid, syscall.SIGKILL)
}

// Dismiss removes a process from the registry (typically after exit).
// Running processes are killed first.
func (r *BackgroundRegistry) Dismiss(id string) error {
	r.mu.Lock()
	p, ok := r.procs[id]
	if !ok {
		r.mu.Unlock()
		return fmt.Errorf("unknown background handle: %s", id)
	}
	exited := p.exited
	r.mu.Unlock()

	if !exited {
		_ = r.Kill(id)
		// Wait briefly for Wait goroutine to mark exited; still remove either way.
		deadline := time.Now().Add(2 * time.Second)
		for time.Now().Before(deadline) {
			r.mu.Lock()
			exited = p.exited
			r.mu.Unlock()
			if exited {
				break
			}
			time.Sleep(10 * time.Millisecond)
		}
	}

	r.mu.Lock()
	delete(r.procs, id)
	r.mu.Unlock()
	return nil
}

func (r *BackgroundRegistry) KillAll() {
	r.mu.Lock()
	ids := make([]string, 0, len(r.procs))
	for id, p := range r.procs {
		ids = append(ids, id)
		if !p.exited && p.cmd.Process != nil {
			_ = syscall.Kill(-p.cmd.Process.Pid, syscall.SIGKILL)
		}
	}
	r.mu.Unlock()

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		r.mu.Lock()
		allDone := true
		for _, p := range r.procs {
			if !p.exited {
				allDone = false
				break
			}
		}
		r.mu.Unlock()
		if allDone {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}

	r.mu.Lock()
	r.procs = make(map[string]*backgroundProc)
	r.mu.Unlock()
	_ = ids
}

// List returns snapshots of all background processes.
func (r *BackgroundRegistry) List() []BgProcSnapshot {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]BgProcSnapshot, 0, len(r.procs))
	for _, p := range r.procs {
		out = append(out, p.snapshot(snapshotTail))
	}
	return out
}

// UILog returns up to maxBytes of the non-draining UI buffer tail.
func (r *BackgroundRegistry) UILog(id string, maxBytes int) (string, error) {
	r.mu.Lock()
	p, ok := r.procs[id]
	r.mu.Unlock()
	if !ok {
		return "", fmt.Errorf("unknown background handle: %s", id)
	}
	if maxBytes <= 0 {
		maxBytes = uiBufSize
	}
	return p.uiBuf.Peek(maxBytes), nil
}

func (p *backgroundProc) snapshot(tail int) BgProcSnapshot {
	return BgProcSnapshot{
		ID:           p.id,
		Command:      p.command,
		Dir:          p.dir,
		Running:      !p.exited,
		ExitCode:     p.exitCode,
		StartedAt:    p.startedAt,
		RecentOutput: p.uiBuf.Peek(tail),
	}
}

// teeWriter fans out process output to the model buffer, UI buffer, and coalescer.
type teeWriter struct {
	model *ringBuffer
	ui    *ringBuffer
	coal  *outputCoalescer
}

func (t *teeWriter) Write(p []byte) (int, error) {
	_, _ = t.model.Write(p)
	_, _ = t.ui.Write(p)
	t.coal.write(p)
	return len(p), nil
}

// outputCoalescer batches BgOutput emissions (≥4KB or ≥100ms).
type outputCoalescer struct {
	mu      sync.Mutex
	id      string
	getEmit func() protocol.EmitFunc
	buf     []byte
	timer   *time.Timer
	closed  bool
}

func newOutputCoalescer(id string, getEmit func() protocol.EmitFunc) *outputCoalescer {
	return &outputCoalescer{id: id, getEmit: getEmit}
}

func (c *outputCoalescer) write(p []byte) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed || c.getEmit == nil || c.getEmit() == nil {
		return
	}
	c.buf = append(c.buf, p...)
	if len(c.buf) >= coalesceBytes {
		c.flushLocked()
		return
	}
	if c.timer == nil {
		c.timer = time.AfterFunc(coalescePeriod, c.flush)
	}
}

func (c *outputCoalescer) flush() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.flushLocked()
}

func (c *outputCoalescer) flushLocked() {
	if c.timer != nil {
		c.timer.Stop()
		c.timer = nil
	}
	emit := protocol.EmitFunc(nil)
	if c.getEmit != nil {
		emit = c.getEmit()
	}
	if len(c.buf) == 0 || emit == nil {
		return
	}
	text := string(c.buf)
	c.buf = c.buf[:0]
	emit(protocol.BgOutput{ID: c.id, Text: text})
}

func (c *outputCoalescer) stop() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.closed = true
	if c.timer != nil {
		c.timer.Stop()
		c.timer = nil
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

// Peek returns up to n trailing bytes without draining. n<=0 returns all.
func (r *ringBuffer) Peek(n int) string {
	r.mu.Lock()
	defer r.mu.Unlock()
	if n <= 0 || n >= len(r.data) {
		return string(r.data)
	}
	return string(r.data[len(r.data)-n:])
}

func (r *ringBuffer) String() string {
	return r.Peek(0)
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
