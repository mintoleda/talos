package server

import (
	"context"
	"encoding/json"
	"net"
	"path/filepath"
	"testing"
	"time"

	"github.com/mintoleda/talos/internal/client/rpc"
	"github.com/mintoleda/talos/internal/config"
	"github.com/mintoleda/talos/internal/transport"
)

func TestDaemonUnixTransport(t *testing.T) {
	baseDir := t.TempDir()
	d := NewDaemon(&config.Config{BaseDir: baseDir, ServerToken: "test-token"}, 0)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- d.Start(ctx) }()
	t.Cleanup(func() {
		cancel()
		select {
		case err := <-done:
			if err != nil {
				t.Errorf("daemon: %v", err)
			}
		case <-time.After(4 * time.Second):
			t.Error("daemon did not stop")
		}
	})

	var discovery Discovery
	deadline := time.Now().Add(4 * time.Second)
	for {
		var err error
		discovery, err = ReadDiscovery(DiscoveryPath(baseDir))
		if err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("discovery: %v", err)
		}
		time.Sleep(10 * time.Millisecond)
	}
	if discovery.Socket != filepath.Join(baseDir, "daemon.sock") {
		t.Fatalf("unexpected socket: %s", discovery.Socket)
	}

	conn, err := net.Dial("unix", discovery.Socket)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if err := conn.SetDeadline(time.Now().Add(4 * time.Second)); err != nil {
		t.Fatal(err)
	}
	dec, enc := json.NewDecoder(conn), json.NewEncoder(conn)
	var hello transport.ServerMsg
	if err := dec.Decode(&hello); err != nil || hello.Type != "hello" {
		t.Fatalf("hello: %+v, error: %v", hello, err)
	}
	if err := enc.Encode(transport.ClientMsg{Type: "auth", Token: discovery.Token}); err != nil {
		t.Fatal(err)
	}
	if err := enc.Encode(transport.ClientMsg{Type: "request", ID: 1, Method: rpc.DaemonListSessions}); err != nil {
		t.Fatal(err)
	}
	var response transport.ServerMsg
	if err := dec.Decode(&response); err != nil {
		t.Fatal(err)
	}
	if response.Type != "response" || response.ID != 1 || response.Err != "" {
		t.Fatalf("response: %+v", response)
	}
}
