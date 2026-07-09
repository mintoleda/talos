package server

import (
	"context"
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/mintoleda/talos/internal/client/rpc"
	"github.com/mintoleda/talos/internal/config"
	"github.com/mintoleda/talos/internal/engine"
	"github.com/mintoleda/talos/internal/loop"
	"github.com/mintoleda/talos/internal/session"
	"github.com/mintoleda/talos/internal/testutil"
	"github.com/mintoleda/talos/internal/transport"
)

func TestDaemonHandleConnSubscribeAndRoute(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	baseDir := filepath.Join(home, ".talos")
	cfg := &config.Config{BaseDir: baseDir, Provider: "test", Model: "m"}
	d := NewDaemon(cfg, 0)
	d.token = "" // no auth for pipe test

	dir := t.TempDir()
	var builtID string
	d.manager.buildFn = func(_ context.Context, o engine.BuildOpts) (*engine.Built, error) {
		id := o.SessionID
		if id == "" {
			id = "pipe-sess"
		}
		builtID = id
		pid := session.ProjectHash(o.ProjectDir)
		if o.ProjectDir == "" {
			pid = session.ProjectHash(o.Dir)
		}
		txPath := filepath.Join(session.SessionsDir(), pid, id+".jsonl")
		if err := os.MkdirAll(filepath.Dir(txPath), 0o755); err != nil {
			return nil, err
		}
		if err := os.WriteFile(txPath, nil, 0o600); err != nil {
			return nil, err
		}
		return &engine.Built{
			Cfg:     &config.Config{BaseDir: baseDir, Provider: "test", Model: "m"},
			Dir:     o.Dir,
			Session: session.Session{ID: id, ProjectID: pid, Path: txPath},
		}, nil
	}
	d.manager.newEng = func(b *engine.Built, ctx context.Context) *engine.Engine {
		tx := testutil.NewTestTranscript(t)
		pb := loop.NewPromptBuilder("sys", nil, "m")
		lp := loop.New(&testutil.FakeProvider{}, &testutil.FakeExecutor{}, tx, pb)
		return engine.NewEngine(engine.Params{
			Loop: lp, PromptBuilder: pb, Cfg: b.Cfg, Provider: "test", Model: "m",
			BaseDir: baseDir, CWD: b.Dir, SessionID: b.Session.ID, Context: ctx,
		})
	}

	c1, c2 := net.Pipe()
	defer c1.Close()
	defer c2.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go d.handleConn(ctx, c2)

	dec := json.NewDecoder(c1)
	enc := json.NewEncoder(c1)

	var hello transport.ServerMsg
	if err := dec.Decode(&hello); err != nil {
		t.Fatalf("hello: %v", err)
	}
	if hello.Type != "hello" || hello.Session != "" {
		t.Fatalf("hello = %+v", hello)
	}

	raw, _ := json.Marshal(rpc.CreateSessionParams{Dir: dir, Isolation: "none"})
	if err := enc.Encode(transport.ClientMsg{Type: "request", ID: 1, Method: rpc.DaemonCreateSession, Params: raw}); err != nil {
		t.Fatal(err)
	}
	var resp transport.ServerMsg
	if err := dec.Decode(&resp); err != nil {
		t.Fatalf("create resp: %v", err)
	}
	if resp.Err != "" {
		t.Fatalf("create err: %s", resp.Err)
	}
	var created rpc.CreateSessionResult
	if err := json.Unmarshal(resp.Result, &created); err != nil {
		t.Fatal(err)
	}
	if created.Session.ID != builtID {
		t.Fatalf("id = %s want %s", created.Session.ID, builtID)
	}

	if err := enc.Encode(transport.ClientMsg{Type: "input", Text: "nope"}); err != nil {
		t.Fatal(err)
	}
	var errMsg transport.ServerMsg
	if err := dec.Decode(&errMsg); err != nil {
		t.Fatalf("err msg: %v", err)
	}
	if errMsg.Type != "error" || errMsg.Err != "no session selected" {
		t.Fatalf("expected no session selected, got %+v", errMsg)
	}

	if err := enc.Encode(transport.ClientMsg{Type: "subscribe", Session: created.Session.ID}); err != nil {
		t.Fatal(err)
	}
	var snapMsg transport.ServerMsg
	if err := dec.Decode(&snapMsg); err != nil {
		t.Fatalf("snap: %v", err)
	}
	if snapMsg.Type != "event" || snapMsg.Session != created.Session.ID {
		t.Fatalf("snap = %+v", snapMsg)
	}

	if err := enc.Encode(transport.ClientMsg{Type: "input", Text: "hello"}); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(2 * time.Second)
	sawUser := false
	for time.Now().Before(deadline) {
		_ = c1.SetReadDeadline(time.Now().Add(200 * time.Millisecond))
		var sm transport.ServerMsg
		if err := dec.Decode(&sm); err != nil {
			continue
		}
		if sm.Type == "event" && sm.EType == "user_input" {
			sawUser = true
			break
		}
	}
	if !sawUser {
		t.Fatal("expected user_input event")
	}

	if err := enc.Encode(transport.ClientMsg{Type: "request", ID: 2, Method: rpc.DaemonListSessions}); err != nil {
		t.Fatal(err)
	}
	var listResp transport.ServerMsg
	for {
		_ = c1.SetReadDeadline(time.Now().Add(time.Second))
		if err := dec.Decode(&listResp); err != nil {
			t.Fatalf("list: %v", err)
		}
		if listResp.Type == "response" && listResp.ID == 2 {
			break
		}
	}
	var listed rpc.ListSessionsResult
	if err := json.Unmarshal(listResp.Result, &listed); err != nil {
		t.Fatal(err)
	}
	if len(listed.Sessions) < 1 {
		t.Fatalf("list empty: %+v", listed)
	}
}

func TestResolveIdleTimeout(t *testing.T) {
	if got := ResolveIdleTimeout(nil); got != 30*time.Minute {
		t.Fatalf("nil cfg: %v", got)
	}
	cfg := &config.Config{}
	if got := ResolveIdleTimeout(cfg); got != 30*time.Minute {
		t.Fatalf("unset: %v", got)
	}
	cfg.ServerIdleTimeoutSet = true
	cfg.ServerIdleTimeout = 0
	if got := ResolveIdleTimeout(cfg); got != 0 {
		t.Fatalf("explicit 0: %v", got)
	}
	cfg.ServerIdleTimeout = 5 * time.Minute
	if got := ResolveIdleTimeout(cfg); got != 5*time.Minute {
		t.Fatalf("explicit 5m: %v", got)
	}
}

func TestDaemonSockPaths(t *testing.T) {
	if DaemonSockPath("/tmp/x") != "/tmp/x/daemon.sock" {
		t.Fatal(DaemonSockPath("/tmp/x"))
	}
	if DaemonPidPath("/tmp/x") != "/tmp/x/daemon.pid" {
		t.Fatal(DaemonPidPath("/tmp/x"))
	}
}
