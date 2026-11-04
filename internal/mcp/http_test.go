package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeHTTPServer is a minimal streamable-HTTP MCP server.
type fakeHTTPServer struct {
	t   *testing.T
	srv *httptest.Server

	mu       sync.Mutex
	methods  []string // JSON-RPC methods received, in order
	deleted  bool
	sessions []string // Mcp-Session-Id seen on each non-initialize POST
	versions []string // MCP-Protocol-Version seen on each non-initialize POST
}

const fakeSessionID = "sess-123"

func newFakeHTTPServer(t *testing.T) *fakeHTTPServer {
	f := &fakeHTTPServer{t: t}
	f.srv = httptest.NewServer(http.HandlerFunc(f.handle))
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakeHTTPServer) handle(w http.ResponseWriter, r *http.Request) {
	if r.Header.Get("X-Test") != "yes" {
		http.Error(w, "missing custom header", http.StatusBadRequest)
		return
	}
	if r.Method == http.MethodDelete {
		f.mu.Lock()
		f.deleted = r.Header.Get(headerSessionID) == fakeSessionID
		f.mu.Unlock()
		w.WriteHeader(http.StatusOK)
		return
	}
	if r.Method != http.MethodPost {
		http.Error(w, "bad method", http.StatusMethodNotAllowed)
		return
	}
	if r.Header.Get("Content-Type") != "application/json" {
		http.Error(w, "bad content-type", http.StatusBadRequest)
		return
	}
	accept := r.Header.Get("Accept")
	if !strings.Contains(accept, "application/json") || !strings.Contains(accept, "text/event-stream") {
		http.Error(w, "bad accept", http.StatusNotAcceptable)
		return
	}
	body, _ := io.ReadAll(r.Body)
	var msg struct {
		ID     *int            `json:"id"`
		Method string          `json:"method"`
		Params json.RawMessage `json:"params"`
	}
	if err := json.Unmarshal(body, &msg); err != nil {
		http.Error(w, "bad json", http.StatusBadRequest)
		return
	}

	f.mu.Lock()
	f.methods = append(f.methods, msg.Method)
	if msg.Method != "initialize" {
		f.sessions = append(f.sessions, r.Header.Get(headerSessionID))
		f.versions = append(f.versions, r.Header.Get(headerProtocolVersion))
	}
	f.mu.Unlock()

	if msg.ID == nil { // notification
		w.WriteHeader(http.StatusAccepted)
		return
	}
	id := *msg.ID

	switch msg.Method {
	case "initialize":
		w.Header().Set(headerSessionID, fakeSessionID)
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"jsonrpc":"2.0","id":%d,"result":{"protocolVersion":"2025-03-26","capabilities":{},"serverInfo":{"name":"fake","version":"1"}}}`, id)
	case "tools/list":
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"jsonrpc":"2.0","id":%d,"result":{"tools":[{"name":"echo","description":"Echo","inputSchema":{"type":"object"}}]}}`, id)
	case "tools/call":
		w.Header().Set("Content-Type", "text/event-stream")
		fl := w.(http.Flusher)
		// A server notification and a server request first; neither is our response.
		fmt.Fprint(w, ": keep-alive\n\n")
		fmt.Fprint(w, "event: message\ndata: {\"jsonrpc\":\"2.0\",\"method\":\"notifications/progress\",\"params\":{\"progress\":1}}\n\n")
		fmt.Fprint(w, "data: {\"jsonrpc\":\"2.0\",\"id\":\"srv-1\",\"method\":\"ping\"}\n\n")
		fl.Flush()
		// Response split across multiple data lines (joined with \n), CRLF line endings.
		fmt.Fprintf(w, "event: message\r\ndata: {\"jsonrpc\":\"2.0\",\r\ndata: \"id\":%d,\r\ndata: \"result\":{\"content\":[{\"type\":\"text\",\"text\":\"hi\"}]}}\r\n\r\n", id)
		fl.Flush()
	case "boom":
		http.Error(w, "kaput", http.StatusInternalServerError)
	case "hang":
		<-r.Context().Done()
	case "empty-stream":
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: {\"jsonrpc\":\"2.0\",\"method\":\"notifications/x\"}\n\n")
	default:
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"jsonrpc":"2.0","id":%d,"error":{"code":-32601,"message":"method not found"}}`, id)
	}
}

func (f *fakeHTTPServer) config() ServerConfig {
	return ServerConfig{Name: "web", URL: f.srv.URL + "/mcp", Headers: map[string]string{"X-Test": "yes"}}
}

func TestHTTPTransportSessionAndResponseKinds(t *testing.T) {
	f := newFakeHTTPServer(t)
	tr := newHTTPTransport(f.srv.URL, f.config().Headers)
	ctx := context.Background()

	r, err := tr.Send(ctx, jsonRPCRequest{JSONRPC: jsonRPCVersion, ID: 1, Method: "initialize"})
	if err != nil || r.ID != 1 || r.Error != nil {
		t.Fatalf("initialize: %+v, %v", r, err)
	}
	if id, _ := tr.session(); id != fakeSessionID {
		t.Fatalf("session id not captured: %q", id)
	}

	// application/json
	r, err = tr.Send(ctx, jsonRPCRequest{JSONRPC: jsonRPCVersion, ID: 2, Method: "tools/list"})
	if err != nil || !strings.Contains(string(r.Result), `"echo"`) {
		t.Fatalf("tools/list: %+v, %v", r, err)
	}

	// text/event-stream with preceding notification/request events
	r, err = tr.Send(ctx, jsonRPCRequest{JSONRPC: jsonRPCVersion, ID: 3, Method: "tools/call"})
	if err != nil || r.ID != 3 || !strings.Contains(string(r.Result), `"hi"`) {
		t.Fatalf("tools/call: %+v, %v", r, err)
	}

	// JSON-RPC error responses are returned, not turned into transport errors.
	r, err = tr.Send(ctx, jsonRPCRequest{JSONRPC: jsonRPCVersion, ID: 4, Method: "nope"})
	if err != nil || r.Error == nil || r.Error.Code != -32601 {
		t.Fatalf("error response: %+v, %v", r, err)
	}

	// Notification -> 202.
	if err := tr.Notify(ctx, "notifications/initialized", nil); err != nil {
		t.Fatalf("notify: %v", err)
	}

	f.mu.Lock()
	for i, s := range f.sessions {
		if s != fakeSessionID {
			t.Errorf("request %d: session header = %q, want %q", i, s, fakeSessionID)
		}
	}
	if len(f.sessions) != 4 {
		t.Errorf("expected 4 post-initialize requests, got %d", len(f.sessions))
	}
	f.mu.Unlock()

	if err := tr.Close(); err != nil {
		t.Fatal(err)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if !f.deleted {
		t.Error("Close did not DELETE the session")
	}
}

func TestHTTPTransportErrors(t *testing.T) {
	f := newFakeHTTPServer(t)
	tr := newHTTPTransport(f.srv.URL, f.config().Headers)
	defer tr.Close()
	ctx := context.Background()

	_, err := tr.Send(ctx, jsonRPCRequest{JSONRPC: jsonRPCVersion, ID: 1, Method: "boom"})
	if err == nil || !strings.Contains(err.Error(), "500") || !strings.Contains(err.Error(), "kaput") {
		t.Errorf("non-2xx error = %v", err)
	}

	_, err = tr.Send(ctx, jsonRPCRequest{JSONRPC: jsonRPCVersion, ID: 2, Method: "empty-stream"})
	if err == nil || !strings.Contains(err.Error(), "without response") {
		t.Errorf("empty stream error = %v", err)
	}

	// Missing custom header -> 400 surfaced.
	bare := newHTTPTransport(f.srv.URL, nil)
	defer bare.Close()
	_, err = bare.Send(ctx, jsonRPCRequest{JSONRPC: jsonRPCVersion, ID: 1, Method: "initialize"})
	if err == nil || !strings.Contains(err.Error(), "400") {
		t.Errorf("expected 400, got %v", err)
	}
}

func TestHTTPTransportContextCancel(t *testing.T) {
	f := newFakeHTTPServer(t)
	tr := newHTTPTransport(f.srv.URL, f.config().Headers)
	defer tr.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	start := time.Now()
	_, err := tr.Send(ctx, jsonRPCRequest{JSONRPC: jsonRPCVersion, ID: 1, Method: "hang"})
	if err == nil {
		t.Fatal("expected error on cancelled ctx")
	}
	if time.Since(start) > 3*time.Second {
		t.Errorf("Send did not return promptly after ctx cancel")
	}
}

func TestHTTPTransportCloseAbortsInFlight(t *testing.T) {
	f := newFakeHTTPServer(t)
	tr := newHTTPTransport(f.srv.URL, f.config().Headers)

	errCh := make(chan error, 1)
	go func() {
		_, err := tr.Send(context.Background(), jsonRPCRequest{JSONRPC: jsonRPCVersion, ID: 1, Method: "hang"})
		errCh <- err
	}()
	time.Sleep(100 * time.Millisecond)
	tr.Close()
	select {
	case err := <-errCh:
		if err == nil {
			t.Error("expected error after Close")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("in-flight Send not aborted by Close")
	}
}

func TestHTTPConnectAndCallTool(t *testing.T) {
	f := newFakeHTTPServer(t)
	cfg := f.config()

	mgr, errs := NewManager(context.Background(), []ServerConfig{cfg})
	if len(errs) != 0 {
		t.Fatalf("NewManager errors: %v", errs)
	}
	if mgr.ConnectedCount() != 1 {
		t.Fatalf("connected = %d", mgr.ConnectedCount())
	}
	conn := mgr.Servers()[0]
	if len(conn.Tools()) != 1 || conn.Tools()[0].Name != "echo" {
		t.Fatalf("tools = %+v", conn.Tools())
	}
	res, err := conn.CallTool(context.Background(), "echo", map[string]any{"message": "x"})
	if err != nil {
		t.Fatalf("CallTool: %v", err)
	}
	if len(res.Content) != 1 || res.Content[0].Text != "hi" {
		t.Errorf("result = %+v", res)
	}
	if len(mgr.Tools()) != 1 {
		t.Errorf("bridged tools = %d", len(mgr.Tools()))
	}
	mgr.Close()

	f.mu.Lock()
	defer f.mu.Unlock()
	want := []string{"initialize", "notifications/initialized", "tools/list", "tools/call"}
	if strings.Join(f.methods, ",") != strings.Join(want, ",") {
		t.Errorf("methods = %v, want %v", f.methods, want)
	}
	for i, v := range f.versions {
		if v != "2025-03-26" {
			t.Errorf("request %d: protocol version header = %q", i, v)
		}
	}
	if !f.deleted {
		t.Error("session not deleted on Close")
	}
}

func TestNewTransportSelection(t *testing.T) {
	f := newFakeHTTPServer(t)
	url := f.srv.URL

	for _, tc := range []struct {
		transport string
		wantSSE   bool
	}{
		{"", false},
		{"http", false},
		{"sse", true},
	} {
		tr, err := newTransport(ServerConfig{Name: "x", URL: url, Transport: tc.transport})
		if err != nil {
			t.Fatal(err)
		}
		switch tr.(type) {
		case *sseTransport:
			if !tc.wantSSE {
				t.Errorf("transport %q: got sse, want http", tc.transport)
			}
		case *httpTransport:
			if tc.wantSSE {
				t.Errorf("transport %q: got http, want sse", tc.transport)
			}
		default:
			t.Errorf("transport %q: unexpected type %T", tc.transport, tr)
		}
		tr.Close()
	}
}

// A server may ping mid-call and withhold the tool result until answered.
func TestHTTPTransportAnswersServerPing(t *testing.T) {
	pong := make(chan json.RawMessage, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var msg struct {
			ID     json.RawMessage `json:"id"`
			Method string          `json:"method"`
			Result json.RawMessage `json:"result"`
		}
		_ = json.NewDecoder(r.Body).Decode(&msg)
		if msg.Method == "" { // our reply to the ping
			pong <- msg.ID
			w.WriteHeader(http.StatusAccepted)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		fl := w.(http.Flusher)
		fmt.Fprint(w, "event: message\ndata: {\"jsonrpc\":\"2.0\",\"id\":\"p1\",\"method\":\"ping\"}\n\n")
		fl.Flush()
		select {
		case <-pong:
		case <-time.After(2 * time.Second):
			return // no pong: end the stream without a result
		}
		fmt.Fprintf(w, "event: message\ndata: {\"jsonrpc\":\"2.0\",\"id\":%s,\"result\":{\"ok\":true}}\n\n", msg.ID)
	}))
	defer srv.Close()

	tr := newHTTPTransport(srv.URL, nil)
	defer tr.Close()
	resp, err := tr.Send(context.Background(), jsonRPCRequest{JSONRPC: jsonRPCVersion, ID: 7, Method: "tools/call"})
	if err != nil {
		t.Fatalf("Send: %v", err)
	}
	if string(resp.Result) != `{"ok":true}` {
		t.Fatalf("result = %s", resp.Result)
	}
}
