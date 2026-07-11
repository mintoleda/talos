package server

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"golang.org/x/net/websocket"

	"github.com/mintoleda/talos/internal/client/rpc"
	"github.com/mintoleda/talos/internal/config"
	"github.com/mintoleda/talos/internal/engine"
	"github.com/mintoleda/talos/internal/gitutil"
	"github.com/mintoleda/talos/internal/protocol"
	"github.com/mintoleda/talos/internal/transport"
	"github.com/mintoleda/talos/internal/version"
)

// Daemon is the multi-session talos server: one unix socket + localhost ws,
// SessionManager-backed, with per-connection subscribe routing.
type Daemon struct {
	cfg         *config.Config
	manager     *SessionManager
	baseDir     string
	sockPath    string
	pidPath     string
	idleTimeout time.Duration // 0 = never idle-exit
	token       string
	wsListen    string // host:port; empty => 127.0.0.1:0
	webDir      string
	startedAt   time.Time

	mu            sync.Mutex
	activeClients int
	lastActivity  time.Time
	conns         map[int]*daemonConn
	connNext      int
}

type daemonConn struct {
	defaultSession string
	subs           map[string]func() // sessionID -> unsubscribe
	events         chan stampedEvent
}

type stampedEvent struct {
	session string
	event   protocol.Event
}

// NewDaemon constructs a daemon.
// idleTimeout 0 = never idle-exit; positive = exit after that duration of
// no clients, no busy engines, and no engine activity.
func NewDaemon(cfg *config.Config, idleTimeout time.Duration) *Daemon {
	baseDir := cfg.BaseDir
	d := &Daemon{
		cfg:          cfg,
		manager:      NewSessionManager(cfg),
		baseDir:      baseDir,
		sockPath:     filepath.Join(baseDir, "daemon.sock"),
		pidPath:      filepath.Join(baseDir, "daemon.pid"),
		idleTimeout:  idleTimeout,
		token:        cfg.ServerToken,
		wsListen:     "127.0.0.1:0",
		lastActivity: time.Now(),
		conns:        make(map[int]*daemonConn),
	}
	if cfg.ServerListen != "" {
		addr := cfg.ServerListen
		addr = strings.TrimPrefix(addr, "ws:")
		addr = strings.TrimPrefix(addr, "tcp:")
		if addr != "" {
			d.wsListen = addr
		}
	}
	d.manager.SetStatusFn(func(st protocol.SessionStatus) {
		d.broadcastStatus(st)
	})
	return d
}

func (d *Daemon) Manager() *SessionManager { return d.manager }

func (d *Daemon) SetWebDir(dir string) { d.webDir = dir }

func (d *Daemon) SetToken(token string) { d.token = token }

// Start listens on unix + websocket until ctx cancel or idle exit.
func (d *Daemon) Start(ctx context.Context) error {
	d.startedAt = time.Now()
	if d.token == "" {
		d.token = generateToken()
	}

	if IsAlive(d.sockPath) {
		// The IsAlive dial wakes the live daemon's accept loop, which
		// self-heals a missing discovery file — give it a moment so callers
		// polling for daemon.json can still find the running daemon.
		discoveryPath := DiscoveryPath(d.baseDir)
		deadline := time.Now().Add(3 * time.Second)
		for {
			if _, err := os.Stat(discoveryPath); err == nil {
				return fmt.Errorf("daemon already accepting connections at %s", d.sockPath)
			}
			if !time.Now().Before(deadline) {
				break
			}
			time.Sleep(200 * time.Millisecond)
		}
		return fmt.Errorf("daemon already accepting connections at %s but %s is missing and was not restored", d.sockPath, discoveryPath)
	}
	if err := os.Remove(d.sockPath); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("remove stale unix socket %s: %w", d.sockPath, err)
	}
	if err := os.MkdirAll(filepath.Dir(d.sockPath), 0o755); err != nil {
		return err
	}
	unixLn, err := net.Listen("unix", d.sockPath)
	if err != nil {
		return fmt.Errorf("listen unix %s: %w", d.sockPath, err)
	}
	defer func() {
		_ = unixLn.Close()
		_ = os.Remove(d.sockPath)
	}()

	if err := writePID(d.pidPath); err != nil {
		return err
	}
	defer os.Remove(d.pidPath)

	wsLn, err := net.Listen("tcp", d.wsListen)
	if err != nil {
		return fmt.Errorf("listen ws %s: %w", d.wsListen, err)
	}
	wsAddr := wsLn.Addr().String()

	discoveryPath := DiscoveryPath(d.baseDir)
	discovery := Discovery{
		PID:       os.Getpid(),
		Socket:    d.sockPath,
		WS:        "ws://" + displayAddr(wsAddr) + "/ws",
		Token:     d.token,
		Version:   version.VERSION,
		StartedAt: d.startedAt,
	}
	if err := WriteDiscovery(discoveryPath, discovery); err != nil {
		_ = wsLn.Close()
		return err
	}
	defer RemoveDiscovery(discoveryPath)

	fmt.Fprintf(os.Stderr, "[daemon] unix %s\n", d.sockPath)
	url := "http://" + displayAddr(wsAddr)
	if d.token != "" {
		url += "/?token=" + d.token
	}
	fmt.Fprintf(os.Stderr, "[daemon] web ui: %s\n", url)

	wsCtx, wsCancel := context.WithCancel(ctx)
	defer wsCancel()
	go func() {
		if err := d.serveWeb(wsCtx, wsLn); err != nil && wsCtx.Err() == nil {
			fmt.Fprintf(os.Stderr, "[daemon] web: %v\n", err)
		}
	}()

	var idle *time.Ticker
	if d.idleTimeout > 0 {
		idle = time.NewTicker(time.Second)
		defer idle.Stop()
	}

	for {
		var idleC <-chan time.Time
		if idle != nil {
			idleC = idle.C
		}
		select {
		case <-ctx.Done():
			d.manager.CloseAll()
			return nil
		case <-idleC:
			if d.shouldIdleExit() {
				d.manager.CloseAll()
				return nil
			}
		default:
		}

		// Self-heal: if daemon.json was deleted while we're alive, clients
		// can't discover us and redundant spawns fail. Restore it.
		if _, err := os.Stat(discoveryPath); os.IsNotExist(err) {
			if err := WriteDiscovery(discoveryPath, discovery); err != nil {
				fmt.Fprintf(os.Stderr, "[daemon] restore discovery: %v\n", err)
			} else {
				fmt.Fprintf(os.Stderr, "[daemon] restored missing %s\n", discoveryPath)
			}
		}

		if deadlineLn, ok := unixLn.(interface{ SetDeadline(time.Time) error }); ok {
			_ = deadlineLn.SetDeadline(time.Now().Add(2 * time.Second))
		}
		conn, err := unixLn.Accept()
		if err != nil {
			if opErr, ok := err.(*net.OpError); ok && opErr.Timeout() {
				continue
			}
			if ctx.Err() != nil {
				d.manager.CloseAll()
				return nil
			}
			return fmt.Errorf("accept: %w", err)
		}
		go d.handleConn(ctx, conn)
	}
}

func (d *Daemon) shouldIdleExit() bool {
	if d.idleTimeout <= 0 {
		return false
	}
	d.mu.Lock()
	clients := d.activeClients
	last := d.lastActivity
	d.mu.Unlock()
	if clients > 0 {
		return false
	}
	if d.manager.AnyBusy() {
		return false
	}
	engLast := d.manager.LastEngineActivity()
	if engLast.After(last) {
		last = engLast
	}
	// No engines and no recent client activity.
	if d.manager.LiveCount() > 0 && time.Since(engLast) < d.idleTimeout {
		return false
	}
	return time.Since(last) >= d.idleTimeout
}

func (d *Daemon) serveWeb(ctx context.Context, ln net.Listener) error {
	mux := http.NewServeMux()
	mux.Handle("/ws", wsHandler(func() string { return ln.Addr().String() }, func(conn *websocket.Conn) {
		d.handleConn(ctx, conn)
	}))
	if d.webDir != "" {
		mux.Handle("/", http.FileServer(http.Dir(d.webDir)))
	}
	srv := &http.Server{Handler: mux}
	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = srv.Shutdown(shutdownCtx)
	}()
	err := srv.Serve(ln)
	if err == http.ErrServerClosed {
		return nil
	}
	return err
}

func (d *Daemon) handleConn(ctx context.Context, conn net.Conn) {
	defer conn.Close()

	dc := &daemonConn{
		subs:   make(map[string]func()),
		events: make(chan stampedEvent, 64),
	}

	d.mu.Lock()
	d.activeClients++
	d.lastActivity = time.Now()
	idx := d.connNext
	d.connNext++
	d.conns[idx] = dc
	d.mu.Unlock()
	defer func() {
		for _, unsub := range dc.subs {
			unsub()
		}
		d.mu.Lock()
		delete(d.conns, idx)
		d.activeClients--
		d.lastActivity = time.Now()
		d.mu.Unlock()
		close(dc.events)
	}()

	enc := json.NewEncoder(conn)
	dec := json.NewDecoder(conn)
	var encMu sync.Mutex

	if err := encodeServerMsg(enc, &encMu, transport.ServerMsg{Type: "hello", Version: version.VERSION, Session: ""}); err != nil {
		return
	}

	if d.token != "" {
		var auth transport.ClientMsg
		if err := dec.Decode(&auth); err != nil {
			return
		}
		if auth.Type != "auth" || auth.Token != d.token {
			_ = encodeServerMsg(enc, &encMu, transport.ServerMsg{Type: "error", Err: "unauthorized"})
			return
		}
	}

	go func() {
		for se := range dc.events {
			if err := d.encodeEvent(enc, &encMu, se.session, se.event); err != nil {
				return
			}
		}
	}()

	for {
		var cm transport.ClientMsg
		if err := dec.Decode(&cm); err != nil {
			return
		}
		d.mu.Lock()
		d.lastActivity = time.Now()
		d.mu.Unlock()

		switch cm.Type {
		case "subscribe":
			if err := d.handleSubscribe(ctx, dc, cm.Session); err != nil {
				_ = encodeServerMsg(enc, &encMu, transport.ServerMsg{Type: "error", Err: err.Error(), Session: cm.Session})
			}
		case "unsubscribe":
			d.handleUnsubscribe(dc, cm.Session)
		case "input", "steer", "interrupt", "approve":
			eng, sid, err := d.resolveEngine(ctx, dc, cm.Session)
			if err != nil {
				_ = encodeServerMsg(enc, &encMu, transport.ServerMsg{Type: "error", Err: err.Error(), Session: cm.Session})
				continue
			}
			eng.Touch()
			switch cm.Type {
			case "input":
				eng.SubmitText(cm.Text)
			case "steer":
				eng.SteerText(cm.Text)
			case "interrupt":
				eng.Interrupt()
			case "approve":
				eng.Approve(cm.Approved, cm.Plan)
			}
			_ = sid
		case "request":
			result, sid, err := d.handleRequest(ctx, dc, cm)
			resp := transport.ServerMsg{Type: "response", ID: cm.ID, Result: result, Session: sid}
			if err != nil {
				resp.Err = err.Error()
			}
			if err := encodeServerMsg(enc, &encMu, resp); err != nil {
				return
			}
		}
	}
}

func (d *Daemon) handleSubscribe(ctx context.Context, dc *daemonConn, sessionID string) error {
	if sessionID == "" {
		return fmt.Errorf("session is required for subscribe")
	}
	eng, err := d.manager.Resume(ctx, sessionID)
	if err != nil {
		return err
	}
	if dc.defaultSession == "" {
		dc.defaultSession = sessionID
	}
	if _, ok := dc.subs[sessionID]; ok {
		// Already subscribed; still refresh snapshot.
		snap := eng.Snapshot()
		d.pushEvent(dc, sessionID, snap)
		return nil
	}
	unsub := eng.Subscribe(func(ev protocol.Event) {
		// SessionStatus is broadcast to all conns separately.
		if _, ok := ev.(protocol.SessionStatus); ok {
			return
		}
		d.pushEvent(dc, sessionID, ev)
	})
	dc.subs[sessionID] = unsub
	snap := eng.Snapshot()
	if snap.Busy || snap.StreamedText != "" || len(snap.ActiveTools) > 0 || snap.PendingPermission != nil {
		d.pushEvent(dc, sessionID, snap)
	} else {
		// Always send snapshot so clients can clear stale UI.
		d.pushEvent(dc, sessionID, snap)
	}
	return nil
}

func (d *Daemon) handleUnsubscribe(dc *daemonConn, sessionID string) {
	if unsub, ok := dc.subs[sessionID]; ok {
		unsub()
		delete(dc.subs, sessionID)
	}
}

func (d *Daemon) resolveEngine(ctx context.Context, dc *daemonConn, sessionHint string) (*engine.Engine, string, error) {
	sid := sessionHint
	if sid == "" {
		sid = dc.defaultSession
	}
	if sid == "" {
		return nil, "", fmt.Errorf("no session selected")
	}
	eng, ok := d.manager.Get(sid)
	if !ok {
		var err error
		eng, err = d.manager.Resume(ctx, sid)
		if err != nil {
			return nil, sid, err
		}
	}
	return eng, sid, nil
}

func (d *Daemon) handleRequest(ctx context.Context, dc *daemonConn, cm transport.ClientMsg) (json.RawMessage, string, error) {
	method := cm.Method
	if strings.HasPrefix(method, "daemon.") || strings.HasPrefix(method, "merge.") {
		result, err := d.handleDaemonRPC(ctx, method, cm.Params)
		return result, "", err
	}
	eng, sid, err := d.resolveEngine(ctx, dc, cm.Session)
	if err != nil {
		return nil, sid, err
	}
	eng.Touch()
	result, err := eng.HandleRequest(ctx, method, cm.Params)
	return result, sid, err
}

func (d *Daemon) handleDaemonRPC(ctx context.Context, method string, params json.RawMessage) (json.RawMessage, error) {
	switch method {
	case rpc.DaemonCreateSession:
		p, err := decodeParams[rpc.CreateSessionParams](params)
		if err != nil {
			return nil, err
		}
		info, err := d.manager.Create(ctx, p)
		if err != nil {
			return nil, err
		}
		return json.Marshal(rpc.CreateSessionResult{Session: info})
	case rpc.DaemonListSessions:
		return json.Marshal(rpc.ListSessionsResult{Sessions: d.manager.List()})
	case rpc.DaemonStopSession:
		p, err := decodeParams[rpc.StopSessionParams](params)
		if err != nil {
			return nil, err
		}
		return nil, d.manager.Stop(p.ID)
	case rpc.DaemonDeleteSession:
		p, err := decodeParams[rpc.DeleteSessionDaemonParams](params)
		if err != nil {
			return nil, err
		}
		return nil, d.manager.Delete(p.ID)
	case rpc.DaemonStatus:
		return json.Marshal(rpc.DaemonStatusResult{
			Version:         version.VERSION,
			Uptime:          int64(time.Since(d.startedAt).Seconds()),
			Sessions:        d.manager.LiveCount(),
			OrphanWorktrees: d.manager.OrphanWorktrees(),
		})
	case rpc.DaemonGCWorktrees:
		removed, err := d.manager.GCWorktrees()
		if err != nil {
			return nil, err
		}
		return json.Marshal(rpc.GCWorktreesResult{Removed: removed})
	case rpc.DaemonProbeDir:
		p, err := decodeParams[rpc.ProbeDirParams](params)
		if err != nil {
			return nil, err
		}
		dir := p.Dir
		if dir == "" {
			return nil, fmt.Errorf("dir is required")
		}
		abs, err := filepath.Abs(dir)
		if err != nil {
			return nil, fmt.Errorf("resolve dir: %w", err)
		}
		result := rpc.ProbeDirResult{ProjectDir: abs}
		if gitutil.IsRepo(abs) {
			result.IsRepo = true
			if root, err := gitutil.RepoRoot(abs); err == nil {
				result.ProjectDir = root
			}
		}
		return json.Marshal(result)
	case rpc.MergePreview:
		p, err := decodeParams[rpc.MergePreviewParams](params)
		if err != nil {
			return nil, err
		}
		result, err := d.manager.MergePreview(p)
		if err != nil {
			return nil, err
		}
		return json.Marshal(result)
	case rpc.MergeFileDiff:
		p, err := decodeParams[rpc.MergeFileDiffParams](params)
		if err != nil {
			return nil, err
		}
		result, err := d.manager.MergeFileDiff(p)
		if err != nil {
			return nil, err
		}
		return json.Marshal(result)
	case rpc.MergeExecute:
		p, err := decodeParams[rpc.MergeExecuteParams](params)
		if err != nil {
			return nil, err
		}
		result, err := d.manager.MergeExecute(p)
		if err != nil {
			return nil, err
		}
		return json.Marshal(result)
	case rpc.MergeCommitWorktree:
		p, err := decodeParams[rpc.MergeCommitWorktreeParams](params)
		if err != nil {
			return nil, err
		}
		result, err := d.manager.MergeCommitWorktree(p)
		if err != nil {
			return nil, err
		}
		return json.Marshal(result)
	default:
		return nil, fmt.Errorf("unknown method: %s", method)
	}
}

func (d *Daemon) pushEvent(dc *daemonConn, sessionID string, ev protocol.Event) {
	select {
	case dc.events <- stampedEvent{session: sessionID, event: ev}:
	case <-time.After(5 * time.Second):
	}
}

func (d *Daemon) broadcastStatus(st protocol.SessionStatus) {
	d.mu.Lock()
	conns := make([]*daemonConn, 0, len(d.conns))
	for _, c := range d.conns {
		conns = append(conns, c)
	}
	d.mu.Unlock()
	for _, c := range conns {
		d.pushEvent(c, st.ID, st)
	}
}

func (d *Daemon) encodeEvent(enc *json.Encoder, mu *sync.Mutex, sessionID string, e protocol.Event) error {
	etype, raw, err := protocol.MarshalEvent(e)
	if err != nil {
		return err
	}
	return encodeServerMsg(enc, mu, transport.ServerMsg{Type: "event", Session: sessionID, EType: etype, Event: raw})
}

func decodeParams[T any](raw json.RawMessage) (T, error) {
	var v T
	if len(raw) == 0 || string(raw) == "null" {
		return v, nil
	}
	if err := json.Unmarshal(raw, &v); err != nil {
		return v, err
	}
	return v, nil
}

func generateToken() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// DaemonSockPath returns the well-known unix socket path.
func DaemonSockPath(baseDir string) string {
	return filepath.Join(baseDir, "daemon.sock")
}

// DaemonPidPath returns the daemon pidfile path.
func DaemonPidPath(baseDir string) string {
	return filepath.Join(baseDir, "daemon.pid")
}

// KillDaemon sends SIGTERM using discovery/pidfile and cleans up.
func KillDaemon(baseDir string) error {
	path := DiscoveryPath(baseDir)
	disc, err := ReadDiscovery(path)
	pid := -1
	if err == nil {
		pid = disc.PID
	} else {
		pid = ReadPID(DaemonPidPath(baseDir))
	}
	if pid <= 0 {
		_ = RemoveDiscovery(path)
		_ = os.Remove(DaemonSockPath(baseDir))
		_ = os.Remove(DaemonPidPath(baseDir))
		return fmt.Errorf("no daemon pid found (stale files cleaned up)")
	}
	proc, err := os.FindProcess(pid)
	if err != nil {
		_ = RemoveDiscovery(path)
		_ = os.Remove(DaemonSockPath(baseDir))
		_ = os.Remove(DaemonPidPath(baseDir))
		return fmt.Errorf("find process %d: %w", pid, err)
	}
	if err := proc.Signal(syscall.SIGTERM); err != nil {
		return fmt.Errorf("kill daemon (pid %d): %w", pid, err)
	}
	_ = RemoveDiscovery(path)
	_ = os.Remove(DaemonSockPath(baseDir))
	_ = os.Remove(DaemonPidPath(baseDir))
	return nil
}

// ResolveIdleTimeout returns the daemon idle timeout: unset → 30m; explicit 0 → never.
func ResolveIdleTimeout(cfg *config.Config) time.Duration {
	if cfg == nil || !cfg.ServerIdleTimeoutSet {
		return 30 * time.Minute
	}
	return cfg.ServerIdleTimeout
}
