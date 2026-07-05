package server

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"syscall"
	"time"

	"golang.org/x/net/websocket"

	"github.com/mintoleda/talos/internal/protocol"
	"github.com/mintoleda/talos/internal/transport"
	"github.com/mintoleda/talos/internal/version"
)

func SocketPath(baseDir, sessionID string) string {
	return filepath.Join(baseDir, "server", sessionID+".sock")
}

func PidFile(baseDir, sessionID string) string {
	return filepath.Join(baseDir, "server", sessionID+".pid")
}

type Engine interface {
	SessionID() string
	Subscribe(fn func(protocol.Event))
	Submit(text string)
	Steer(text string)
	Interrupt()
	Approve(approved bool, plan []byte)
	Snapshot() protocol.EngineSnapshot
}

type RequestHandler func(ctx context.Context, method string, params json.RawMessage) (json.RawMessage, error)

type Server struct {
	engine      Engine
	sockPath    string
	pidPath     string
	idleTimeout time.Duration
	requests    RequestHandler
	network     string
	address     string
	token       string

	mu            sync.Mutex
	subscribers   map[int]func(protocol.Event)
	subNext       int
	activeClients int
	lastActivity  time.Time
}

// New creates a server. idleTimeout defaults to 30 minutes if zero.
func New(engine Engine, sockPath, pidPath string, idleTimeout time.Duration) *Server {
	if idleTimeout == 0 {
		idleTimeout = 30 * time.Minute
	}
	s := &Server{
		engine:       engine,
		sockPath:     sockPath,
		pidPath:      pidPath,
		idleTimeout:  idleTimeout,
		network:      "unix",
		address:      sockPath,
		subscribers:  make(map[int]func(protocol.Event)),
		lastActivity: time.Now(),
	}
	engine.Subscribe(func(e protocol.Event) {
		s.broadcast(e)
	})
	return s
}

func (s *Server) SetListen(network, address string) {
	s.network = network
	s.address = address
}

func (s *Server) SetToken(token string) {
	s.token = token
}

func (s *Server) SetRequestHandler(h RequestHandler) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.requests = h
}

func (s *Server) broadcast(e protocol.Event) {
	s.mu.Lock()
	subs := make([]func(protocol.Event), 0, len(s.subscribers))
	for _, fn := range s.subscribers {
		subs = append(subs, fn)
	}
	s.mu.Unlock()
	for _, fn := range subs {
		fn(e)
	}
}

// Start listens on the Unix socket and serves clients until ctx is done or
// the idle timeout fires with no clients connected.
func (s *Server) Start(ctx context.Context) error {
	if s.network == "" {
		s.network = "unix"
	}
	if s.address == "" {
		s.address = s.sockPath
	}
	if s.network == "ws" {
		return s.startWebSocket(ctx)
	}
	if s.network == "unix" {
		_ = os.Remove(s.address)
		if err := os.MkdirAll(filepath.Dir(s.address), 0o755); err != nil {
			return err
		}
	}
	ln, err := net.Listen(s.network, s.address)
	if err != nil {
		return fmt.Errorf("listen %s %s: %w", s.network, s.address, err)
	}
	if s.network == "unix" {
		defer os.Remove(s.address)
	}

	if err := writePID(s.pidPath); err != nil {
		return err
	}
	defer os.Remove(s.pidPath)

	idle := time.NewTimer(s.idleTimeout)
	defer idle.Stop()

	for {
		select {
		case <-ctx.Done():
			return nil
		case <-idle.C:
			s.mu.Lock()
			if s.activeClients == 0 && time.Since(s.lastActivity) >= s.idleTimeout {
				s.mu.Unlock()
				return nil
			}
			s.mu.Unlock()
			idle.Reset(s.idleTimeout)
		default:
		}

		// Accept with a short timeout so we can check ctx/idle above.
		if deadlineLn, ok := ln.(interface{ SetDeadline(time.Time) error }); ok {
			_ = deadlineLn.SetDeadline(time.Now().Add(2 * time.Second))
		}
		conn, err := ln.Accept()
		if err != nil {
			if opErr, ok := err.(*net.OpError); ok && opErr.Timeout() {
				continue
			}
			if ctx.Err() != nil {
				return nil
			}
			return fmt.Errorf("accept: %w", err)
		}
		go s.handleConn(ctx, conn)
	}
}

func (s *Server) startWebSocket(ctx context.Context) error {
	mux := http.NewServeMux()
	mux.Handle("/ws", websocket.Handler(func(conn *websocket.Conn) {
		s.handleConn(ctx, conn)
	}))
	srv := &http.Server{Addr: s.address, Handler: mux}
	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = srv.Shutdown(shutdownCtx)
	}()
	err := srv.ListenAndServe()
	if err == http.ErrServerClosed {
		return nil
	}
	return err
}

func (s *Server) handleConn(ctx context.Context, conn net.Conn) {
	defer conn.Close()

	s.mu.Lock()
	s.activeClients++
	s.lastActivity = time.Now()
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		s.activeClients--
		s.lastActivity = time.Now()
		s.mu.Unlock()
	}()

	enc := json.NewEncoder(conn)
	dec := json.NewDecoder(conn)
	var encMu sync.Mutex

	if err := encodeServerMsg(enc, &encMu, transport.ServerMsg{Type: "hello", Version: version.VERSION, Session: s.engine.SessionID()}); err != nil {
		return
	}

	if s.token != "" {
		var auth transport.ClientMsg
		if err := dec.Decode(&auth); err != nil {
			return
		}
		if auth.Type != "auth" || auth.Token != s.token {
			_ = encodeServerMsg(enc, &encMu, transport.ServerMsg{Type: "error", Err: "unauthorized"})
			return
		}
	}

	events := make(chan protocol.Event, 64)
	defer close(events)

	s.mu.Lock()
	idx := s.subNext
	s.subNext++
	s.subscribers[idx] = func(e protocol.Event) {
		select {
		case events <- e:
		case <-time.After(5 * time.Second):
		}
	}
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		delete(s.subscribers, idx)
		s.mu.Unlock()
	}()

	// Send a snapshot of the engine's current state so the newly-attached
	// client sees any in-progress turn (busy indicator, streamed text, tools).
	if snap := s.engine.Snapshot(); snap.Busy || snap.StreamedText != "" || len(snap.ActiveTools) > 0 {
		s.encodeEvent(enc, &encMu, snap)
	}

	go func() {
		for e := range events {
			if err := s.encodeEvent(enc, &encMu, e); err != nil {
				return
			}
		}
	}()

	for {
		var cm transport.ClientMsg
		if err := dec.Decode(&cm); err != nil {
			return
		}
		s.mu.Lock()
		s.lastActivity = time.Now()
		s.mu.Unlock()
		switch cm.Type {
		case "input":
			s.engine.Submit(cm.Text)
		case "steer":
			s.engine.Steer(cm.Text)
		case "interrupt":
			s.engine.Interrupt()
		case "approve":
			s.engine.Approve(cm.Approved, cm.Plan)
		case "request":
			result, err := s.handleRequest(ctx, cm.Method, cm.Params)
			resp := transport.ServerMsg{Type: "response", ID: cm.ID, Result: result}
			if err != nil {
				resp.Err = err.Error()
			}
			if err := encodeServerMsg(enc, &encMu, resp); err != nil {
				return
			}
		}
	}
}

func (s *Server) handleRequest(ctx context.Context, method string, params json.RawMessage) (json.RawMessage, error) {
	s.mu.Lock()
	h := s.requests
	s.mu.Unlock()
	if h == nil {
		return nil, fmt.Errorf("unknown method")
	}
	return h(ctx, method, params)
}

func encodeServerMsg(enc *json.Encoder, mu *sync.Mutex, sm transport.ServerMsg) error {
	mu.Lock()
	defer mu.Unlock()
	return enc.Encode(sm)
}

func (s *Server) encodeEvent(enc *json.Encoder, mu *sync.Mutex, e protocol.Event) error {
	raw, err := json.Marshal(e)
	if err != nil {
		return err
	}
	var etype string
	switch e.(type) {
	case protocol.UserInput:
		etype = "UserInput"
	case protocol.ModelChanged:
		etype = "ModelChanged"
	case protocol.TextDelta:
		etype = "TextDelta"
	case protocol.ThinkingDelta:
		etype = "ThinkingDelta"
	case protocol.ThinkingBlock:
		etype = "ThinkingBlock"
	case protocol.ToolStarted:
		etype = "ToolStarted"
	case protocol.ToolFinished:
		etype = "ToolFinished"
	case protocol.Notice:
		etype = "Notice"
	case protocol.TurnEnded:
		etype = "TurnEnded"
	case protocol.PermissionRequested:
		etype = "PermissionRequested"
	case protocol.EngineSnapshot:
		etype = "EngineSnapshot"
	}
	return encodeServerMsg(enc, mu, transport.ServerMsg{Type: "event", EType: etype, Event: raw})
}

func writePID(path string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = fmt.Fprintf(f, "%d", os.Getpid())
	return err
}

// ReadPID reads a pid file. Returns -1 if it cannot be read.
func ReadPID(path string) int {
	f, err := os.Open(path)
	if err != nil {
		return -1
	}
	defer f.Close()
	var pid int
	if _, err := fmt.Fscanf(bufio.NewReader(f), "%d", &pid); err != nil {
		return -1
	}
	return pid
}

// IsAlive tries a quick dial to the Unix socket to verify the server is
// actually accepting connections (not just a stale socket file).
func IsAlive(sockPath string) bool {
	conn, err := net.DialTimeout("unix", sockPath, 500*time.Millisecond)
	if err != nil {
		return false
	}
	conn.Close()
	return true
}

// ListRunning returns the session IDs of all currently-alive talos servers
// under baseDir, ordered by socket mtime (newest first).
func ListRunning(baseDir string) ([]string, error) {
	dir := filepath.Join(baseDir, "server")
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	type item struct {
		id  string
		mod time.Time
	}
	var items []item
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".sock") {
			continue
		}
		sock := filepath.Join(dir, e.Name())
		if !IsAlive(sock) {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		id := strings.TrimSuffix(e.Name(), ".sock")
		items = append(items, item{id: id, mod: info.ModTime()})
	}
	sort.Slice(items, func(i, j int) bool {
		return items[i].mod.After(items[j].mod)
	})
	out := make([]string, len(items))
	for i, it := range items {
		out[i] = it.id
	}
	return out, nil
}

// Kill sends SIGTERM to a running server and cleans up its socket and pid
// files. Returns nil on success or an error describing what went wrong.
func Kill(baseDir, sessionID string) error {
	pidPath := filepath.Join(baseDir, "server", sessionID+".pid")
	sockPath := filepath.Join(baseDir, "server", sessionID+".sock")

	pid := ReadPID(pidPath)
	if pid <= 0 {
		_ = os.Remove(sockPath)
		_ = os.Remove(pidPath)
		return fmt.Errorf("no pid for server %s (stale files cleaned up)", sessionID)
	}
	proc, err := os.FindProcess(pid)
	if err != nil {
		_ = os.Remove(sockPath)
		_ = os.Remove(pidPath)
		return fmt.Errorf("find process %d: %w (stale files cleaned up)", pid, err)
	}
	if err := proc.Signal(syscall.SIGTERM); err != nil {
		return fmt.Errorf("kill server %s (pid %d): %w", sessionID, pid, err)
	}
	_ = os.Remove(sockPath)
	_ = os.Remove(pidPath)
	return nil
}
