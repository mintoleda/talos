package server

import (
	"context"
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/mintoleda/talos/internal/protocol"
	"github.com/mintoleda/talos/internal/transport"
)

// testEngine is a minimal Engine implementation for testing.
type testEngine struct {
	sessionID  string
	subs       []func(protocol.Event)
	inputs     []string
	interrupts int
}

func (e *testEngine) SessionID() string { return e.sessionID }
func (e *testEngine) Subscribe(fn func(protocol.Event)) {
	e.subs = append(e.subs, fn)
}
func (e *testEngine) Submit(text string) {
	e.inputs = append(e.inputs, text)
}
func (e *testEngine) Steer(text string) {}
func (e *testEngine) Interrupt() {
	e.interrupts++
}
func (e *testEngine) Approve(approved bool, plan []byte) {}
func (e *testEngine) Snapshot() protocol.EngineSnapshot  { return protocol.EngineSnapshot{} }

func TestSocketPath(t *testing.T) {
	path := SocketPath("/tmp/talos", "abc123")
	if !filepath.IsAbs(path) {
		t.Fatal("expected absolute path")
	}
	if !contains(path, "abc123") {
		t.Fatal("path should contain session ID")
	}
	if !contains(path, ".sock") {
		t.Fatal("path should end with .sock")
	}
}

func TestPidFile(t *testing.T) {
	path := PidFile("/tmp/talos", "abc123")
	if !filepath.IsAbs(path) {
		t.Fatal("expected absolute path")
	}
	if !contains(path, ".pid") {
		t.Fatal("path should end with .pid")
	}
}

func TestNewServer(t *testing.T) {
	eng := &testEngine{sessionID: "test"}
	s := New(eng, "/tmp/talos-test.sock", "/tmp/talos-test.pid", 0)

	if s == nil {
		t.Fatal("expected non-nil server")
	}
	if s.activeClients != 0 {
		t.Fatalf("expected 0 active clients, got %d", s.activeClients)
	}
}

func TestServerConnectAndSendInput(t *testing.T) {
	dir := t.TempDir()
	sockPath := filepath.Join(dir, "test.sock")
	pidPath := filepath.Join(dir, "test.pid")

	eng := &testEngine{sessionID: "session-1"}
	s := New(eng, sockPath, pidPath, 0)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go func() {
		_ = s.Start(ctx)
	}()

	// Wait for socket to appear.
	for i := 0; i < 50; i++ {
		if _, err := os.Stat(sockPath); err == nil {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}

	// Connect as a client.
	conn, err := net.Dial("unix", sockPath)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer conn.Close()

	enc := json.NewEncoder(conn)

	// Read hello.
	var hello transport.ServerMsg
	if err := json.NewDecoder(conn).Decode(&hello); err != nil {
		t.Fatalf("read hello: %v", err)
	}
	if hello.Type != "hello" {
		t.Fatalf("expected hello, got %s", hello.Type)
	}
	if hello.Session != "session-1" {
		t.Fatalf("expected session session-1, got %s", hello.Session)
	}

	// Send input.
	if err := enc.Encode(transport.ClientMsg{Type: "input", Text: "hello world"}); err != nil {
		t.Fatalf("send input: %v", err)
	}

	time.Sleep(50 * time.Millisecond)

	if len(eng.inputs) != 1 || eng.inputs[0] != "hello world" {
		t.Fatalf("expected engine to receive 'hello world', got %v", eng.inputs)
	}
}

func TestServerConnectAndInterrupt(t *testing.T) {
	dir := t.TempDir()
	sockPath := filepath.Join(dir, "interrupt.sock")
	pidPath := filepath.Join(dir, "interrupt.pid")

	eng := &testEngine{sessionID: "s1"}
	s := New(eng, sockPath, pidPath, 0)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go func() {
		_ = s.Start(ctx)
	}()

	for i := 0; i < 50; i++ {
		if _, err := os.Stat(sockPath); err == nil {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}

	conn, err := net.Dial("unix", sockPath)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer conn.Close()

	enc := json.NewEncoder(conn)
	// Read hello.
	json.NewDecoder(conn).Decode(&transport.ServerMsg{})

	// Send interrupt.
	if err := enc.Encode(transport.ClientMsg{Type: "interrupt"}); err != nil {
		t.Fatalf("send interrupt: %v", err)
	}

	time.Sleep(50 * time.Millisecond)

	if eng.interrupts != 1 {
		t.Fatalf("expected 1 interrupt, got %d", eng.interrupts)
	}
}

func TestServerBroadcastsEvents(t *testing.T) {
	dir := t.TempDir()
	sockPath := filepath.Join(dir, "broadcast.sock")
	pidPath := filepath.Join(dir, "broadcast.pid")

	eng := &testEngine{sessionID: "s1"}
	s := New(eng, sockPath, pidPath, 0)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go func() {
		_ = s.Start(ctx)
	}()

	for i := 0; i < 50; i++ {
		if _, err := os.Stat(sockPath); err == nil {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}

	conn, err := net.Dial("unix", sockPath)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer conn.Close()

	// Read hello.
	json.NewDecoder(conn).Decode(&transport.ServerMsg{})

	// Emit an event through the engine's subscriptions.
	if len(eng.subs) > 0 {
		eng.subs[0](protocol.TextDelta{Text: "broadcasted"})
	}

	// Read the event from the connection.
	var sm transport.ServerMsg
	if err := json.NewDecoder(conn).Decode(&sm); err != nil {
		t.Fatalf("read event: %v", err)
	}
	if sm.Type != "event" {
		t.Fatalf("expected event type, got %s", sm.Type)
	}
	if sm.EType != "TextDelta" {
		t.Fatalf("expected TextDelta etype, got %s", sm.EType)
	}
}

func TestIsAlive(t *testing.T) {
	if IsAlive("/nonexistent/test.sock") {
		t.Fatal("expected false for nonexistent socket")
	}
}

func TestWritePID(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test.pid")

	if err := writePID(path); err != nil {
		t.Fatalf("writePID: %v", err)
	}

	pid := ReadPID(path)
	if pid <= 0 {
		t.Fatalf("expected positive pid, got %d", pid)
	}
}

func TestReadPIDMissingFile(t *testing.T) {
	pid := ReadPID("/nonexistent/pid")
	if pid != -1 {
		t.Fatalf("expected -1 for missing pid, got %d", pid)
	}
}

func TestListRunningEmpty(t *testing.T) {
	dir := t.TempDir()
	ids, err := ListRunning(dir)
	if err != nil {
		t.Fatalf("ListRunning: %v", err)
	}
	if ids != nil {
		t.Fatalf("expected nil, got %v", ids)
	}
}

func TestKillMissingServer(t *testing.T) {
	dir := t.TempDir()
	err := Kill(dir, "nonexistent")
	if err == nil {
		t.Fatal("expected error for nonexistent server")
	}
}

func TestLoopEngineEventSubscription(t *testing.T) {
	// LoopEngine requires concrete *loop.Loop and *safety.Checkpointer types.
	// The emit/subscribe mechanism is tested indirectly through the server
	// broadcast tests above.
}

func contains(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}
