package client

import (
	"context"
	"encoding/json"
	"net"
	"path/filepath"
	"testing"
	"time"

	"github.com/mintoleda/talos/internal/transport"
	"github.com/mintoleda/talos/internal/version"
)

func TestRunClientBadHandshake(t *testing.T) {
	dir := t.TempDir()
	sockPath := filepath.Join(dir, "bad.sock")

	ln, err := net.Listen("unix", sockPath)
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer ln.Close()

	go func() {
		conn, _ := ln.Accept()
		if conn != nil {
			enc := json.NewEncoder(conn)
			enc.Encode(transport.ServerMsg{Type: "oops"})
			conn.Close()
		}
	}()

	time.Sleep(50 * time.Millisecond)

	_, _, err = RunClient(context.Background(), sockPath)
	if err == nil {
		t.Fatal("expected error for bad handshake")
	}
}

func TestRunClientVersionMismatch(t *testing.T) {
	dir := t.TempDir()
	sockPath := filepath.Join(dir, "version.sock")

	ln, err := net.Listen("unix", sockPath)
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer ln.Close()

	go func() {
		conn, _ := ln.Accept()
		if conn != nil {
			enc := json.NewEncoder(conn)
			enc.Encode(transport.ServerMsg{Type: "hello", Version: "1.0.0"})
			json.NewDecoder(conn).Decode(&struct{}{})
			conn.Close()
		}
	}()

	time.Sleep(50 * time.Millisecond)

	_, _, err = RunClient(context.Background(), sockPath)
	if err == nil {
		t.Fatal("expected version mismatch error")
	}
}

func TestRunClientNoServer(t *testing.T) {
	_, _, err := RunClient(context.Background(), "/nonexistent/test.sock")
	if err == nil {
		t.Fatal("expected error when no server")
	}
}

func TestVersionCompatible(t *testing.T) {
	if !version.Compatible("0.2.0") {
		t.Fatal("expected same version to be compatible")
	}
}
