package mcp

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"sync"
	"sync/atomic"
)

type stdioTransport struct {
	cmd    *exec.Cmd
	stdin  io.WriteCloser
	mu     sync.Mutex
	nextID atomic.Int32

	pending   map[int]chan jsonRPCResponse
	pendingMu sync.Mutex
	done      chan struct{}
}

func newStdioTransport(name, command string, args []string, env map[string]string) (*stdioTransport, error) {
	cmd := exec.Command(command, args...)
	cmd.Env = os.Environ()
	for k, v := range env {
		cmd.Env = append(cmd.Env, k+"="+v)
	}
	cmd.Stderr = &prefixedWriter{w: os.Stderr, prefix: "[" + name + "] "}

	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, fmt.Errorf("stdin pipe: %w", err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, fmt.Errorf("stdout pipe: %w", err)
	}

	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("start: %w", err)
	}

	t := &stdioTransport{
		cmd:     cmd,
		stdin:   stdin,
		pending: make(map[int]chan jsonRPCResponse),
		done:    make(chan struct{}),
	}

	go t.drain(stdout)
	return t, nil
}

func (t *stdioTransport) Send(ctx context.Context, req jsonRPCRequest) (jsonRPCResponse, error) {
	ch := make(chan jsonRPCResponse, 1)
	t.pendingMu.Lock()
	t.pending[req.ID] = ch
	t.pendingMu.Unlock()

	t.mu.Lock()
	err := json.NewEncoder(t.stdin).Encode(req)
	t.mu.Unlock()
	if err != nil {
		t.pendingMu.Lock()
		delete(t.pending, req.ID)
		t.pendingMu.Unlock()
		return jsonRPCResponse{}, fmt.Errorf("write request: %w", err)
	}

	select {
	case resp := <-ch:
		return resp, nil
	case <-ctx.Done():
		t.pendingMu.Lock()
		delete(t.pending, req.ID)
		t.pendingMu.Unlock()
		return jsonRPCResponse{}, ctx.Err()
	}
}

func (t *stdioTransport) drain(stdout io.Reader) {
	defer close(t.done)
	scanner := bufio.NewScanner(stdout)
	scanner.Buffer(make([]byte, 0, 64*1024), 10*1024*1024)
	for scanner.Scan() {
		line := scanner.Bytes()
		var resp jsonRPCResponse
		if err := json.Unmarshal(line, &resp); err != nil {
			fmt.Fprintf(os.Stderr, "[mcp] bad response: %v\n", err)
			continue
		}
		t.pendingMu.Lock()
		ch, ok := t.pending[resp.ID]
		if ok {
			delete(t.pending, resp.ID)
		}
		t.pendingMu.Unlock()
		if ok {
			ch <- resp
		}
	}
}

func (t *stdioTransport) Close() error {
	t.mu.Lock()
	t.stdin.Close()
	t.mu.Unlock()

	// Kill the process if it doesn't exit on stdin close.
	if t.cmd.Process != nil {
		t.cmd.Process.Kill()
	}
	<-t.done

	t.pendingMu.Lock()
	for id, ch := range t.pending {
		close(ch)
		delete(t.pending, id)
	}
	t.pendingMu.Unlock()

	return nil
}

type prefixedWriter struct {
	w      io.Writer
	prefix string
}

func (p *prefixedWriter) Write(b []byte) (int, error) {
	_, err := fmt.Fprintf(p.w, "%s%s", p.prefix, string(b))
	return len(b), err
}
