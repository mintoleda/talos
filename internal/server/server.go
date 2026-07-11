package server

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/url"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
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
	Subscribe(fn func(protocol.Event)) (cancel func())
	SubmitText(text string)
	SteerText(text string)
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
	webDir      string // path to static web assets (empty = no static serving)

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

func (s *Server) SetWebDir(dir string) {
	s.webDir = dir
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

// Start listens on the Unix socket (so attach and `server list` work) and
// serves clients until ctx is done or the idle timeout fires with no clients
// connected. If a ws listener was configured via SetListen, the web server
// runs alongside the Unix socket.
func (s *Server) Start(ctx context.Context) error {
	if s.network == "" {
		s.network = "unix"
	}
	if s.address == "" {
		s.address = s.sockPath
	}

	network, address := s.network, s.address
	if network == "ws" {
		wsCtx, wsCancel := context.WithCancel(ctx)
		defer wsCancel()
		go func() {
			if err := s.startWebSocket(wsCtx); err != nil {
				fmt.Fprintf(os.Stderr, "[web] error: %v\n", err)
			}
		}()
		// The web listener runs in the background; the main loop below
		// still serves the Unix socket for attach/list/kill.
		network, address = "unix", s.sockPath
	}

	if network == "unix" {
		_ = os.Remove(address)
		if err := os.MkdirAll(filepath.Dir(address), 0o755); err != nil {
			return err
		}
	}
	ln, err := net.Listen(network, address)
	if err != nil {
		return fmt.Errorf("listen %s %s: %w", network, address, err)
	}
	if network == "unix" {
		defer os.Remove(address)
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
	mux.Handle("/ws", wsHandler(func() string { return s.address }, func(conn *websocket.Conn) {
		s.handleConn(ctx, conn)
	}))
	if s.webDir != "" {
		mux.Handle("/", http.FileServer(http.Dir(s.webDir)))
	}

	ln, err := listenWithFallback(s.address)
	if err != nil {
		return err
	}
	actualAddr := ln.Addr().String()
	s.address = actualAddr

	url := "http://" + displayAddr(actualAddr)
	if s.token != "" {
		url += "/?token=" + s.token
	}
	fmt.Fprintf(os.Stderr, "\n  web ui:  %s\n\n", url)

	srv := &http.Server{Handler: mux}
	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = srv.Shutdown(shutdownCtx)
	}()
	err = srv.Serve(ln)
	if err == http.ErrServerClosed {
		return nil
	}
	return err
}

// listenWithFallback listens on addr; if the port is busy it scans upward
// (up to 20 ports) so a second server can start alongside a first.
func listenWithFallback(addr string) (net.Listener, error) {
	host, portStr, err := net.SplitHostPort(addr)
	if err != nil {
		return nil, fmt.Errorf("bad listen address %q: %w", addr, err)
	}
	port, err := strconv.Atoi(portStr)
	if err != nil {
		return nil, fmt.Errorf("bad port in %q: %w", addr, err)
	}
	if port == 0 {
		port = 8080
	}
	for i := 0; i < 20; i++ {
		ln, err := net.Listen("tcp", net.JoinHostPort(host, strconv.Itoa(port+i)))
		if err == nil {
			return ln, nil
		}
	}
	return nil, fmt.Errorf("no free port in %d-%d on %s", port, port+19, host)
}

// displayAddr rewrites wildcard/loopback IPs to something clickable.
func displayAddr(addr string) string {
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return addr
	}
	if host == "" || host == "::" || host == "0.0.0.0" || host == "127.0.0.1" || host == "::1" {
		host = "localhost"
	}
	return net.JoinHostPort(host, port)
}

// isLocalhost returns true if addr is a loopback address or empty.
func isLocalhost(addr string) bool {
	if addr == "" {
		return true
	}
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return false
	}
	return host == "localhost" || host == "127.0.0.1" || host == "::1"
}

// wsHandler wraps handle in a websocket.Server whose handshake applies our
// origin policy. The default websocket.Handler handshake 403s any request
// without a parseable Origin header, which rejects every non-browser client
// (Go, Node ws); token auth is the real barrier, so missing origins are fine.
func wsHandler(addr func() string, handle func(*websocket.Conn)) http.Handler {
	return websocket.Server{
		Handshake: func(_ *websocket.Config, req *http.Request) error {
			a := addr()
			if isLocalhost(a) {
				return nil
			}
			origin := req.Header.Get("Origin")
			if origin == "" {
				origin = req.Header.Get("Sec-WebSocket-Origin")
			}
			if !originAllowed(origin, a) {
				return fmt.Errorf("origin not allowed")
			}
			return nil
		},
		Handler: handle,
	}
}

// originAllowed checks if a browser origin is permitted to connect. Token
// auth is the real barrier; this is defense-in-depth against cross-site
// WebSocket connections. Non-browser clients send no Origin header and are
// allowed; browser origins must target this server's host or loopback.
func originAllowed(origin, addr string) bool {
	if origin == "" {
		return true
	}
	u, err := url.Parse(origin)
	if err != nil {
		return false
	}
	oh := u.Hostname()
	if oh == "localhost" || oh == "127.0.0.1" || oh == "::1" {
		return true
	}
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return false
	}
	return oh == host
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
	// client sees any in-progress turn (busy indicator, streamed text, tools,
	// or a pending permission prompt).
	if snap := s.engine.Snapshot(); snap.Busy || snap.StreamedText != "" || len(snap.ActiveTools) > 0 || snap.PendingPermission != nil {
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
			s.engine.SubmitText(cm.Text)
		case "steer":
			s.engine.SteerText(cm.Text)
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
	etype, raw, err := protocol.MarshalEvent(e)
	if err != nil {
		return err
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
