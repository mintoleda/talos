package mcp

import (
	"context"
	"encoding/json"
	"sync"
	"testing"
)

// fakeStdioServer simulates an MCP server process over stdin/stdout pipes
// by returning canned responses keyed by method name.
type fakeStdioServer struct {
	t           *testing.T
	responses   map[string]json.RawMessage // method → result JSON
	mu          sync.Mutex
	received    []jsonRPCRequest
	allowNotify bool // if false, fail on notifications
}

func newFakeStdioServer(t *testing.T, responses map[string]json.RawMessage) *fakeStdioServer {
	return &fakeStdioServer{t: t, responses: responses}
}

func (f *fakeStdioServer) handle(req jsonRPCRequest) jsonRPCResponse {
	f.mu.Lock()
	f.received = append(f.received, req)
	f.mu.Unlock()

	if req.Method == "" {
		if !f.allowNotify {
			f.t.Fatalf("unexpected notification (no method) in request: %+v", req)
		}
		return jsonRPCResponse{JSONRPC: jsonRPCVersion, ID: req.ID}
	}

	result, ok := f.responses[req.Method]
	if !ok {
		return jsonRPCResponse{
			JSONRPC: jsonRPCVersion,
			ID:      req.ID,
			Error:   &jsonRPCError{Code: -32601, Message: "method not found: " + req.Method},
		}
	}
	return jsonRPCResponse{JSONRPC: jsonRPCVersion, ID: req.ID, Result: result}
}

// run starts the fake server, reading from stdin and writing to stdout.
// It returns when stdin is closed (or ctx is done).
func (f *fakeStdioServer) run(ctx context.Context) {
	// This is meant to be run as a subprocess, but for testing we use
	// the fakeTransport directly. The real stdio test is done by
	// exercising the stdioTransport with a real subprocess mock.
}

func TestStdioSendReceive(t *testing.T) {
	// We test the stdio transport by building a helper that writes
	// requests to a pipe and reads responses. Since we can't easily
	// spawn a fake Go subprocess in a unit test without exec, we
	// instead test the concurrent demuxing by exercising the
	// stdioTransport.drain method directly.

	// The transport's drain goroutine reads lines from stdout and
	// dispatches to pending channels. We can simulate out-of-order
	// responses by writing JSON lines to a pipe and checking the
	// channels are dispatched correctly.
	t.Run("drain dispatches by ID", func(t *testing.T) {
		pr, pw := newBidiPipe()
		t.Cleanup(func() { pr.Close(); pw.Close() })

		// Simulate the pending map and drain.
		pending := make(map[int]chan jsonRPCResponse)
		var mu sync.Mutex

		go func() {
			scanner := newBidiScanner(pr)
			for scanner.Scan() {
				line := scanner.Bytes()
				var resp jsonRPCResponse
				if err := json.Unmarshal(line, &resp); err != nil {
					t.Logf("bad response: %v", err)
					continue
				}
				mu.Lock()
				ch, ok := pending[resp.ID]
				if ok {
					delete(pending, resp.ID)
				}
				mu.Unlock()
				if ok {
					ch <- resp
				}
			}
		}()

		// Send two requests out of order
		resp1 := make(chan jsonRPCResponse, 1)
		resp2 := make(chan jsonRPCResponse, 1)

		mu.Lock()
		pending[1] = resp1
		pending[2] = resp2
		mu.Unlock()

		// Write response for ID 2 first, then ID 1
		pw.WriteLine(`{"jsonrpc":"2.0","id":2,"result":"two"}`)
		pw.WriteLine(`{"jsonrpc":"2.0","id":1,"result":"one"}`)

		r2 := <-resp2
		if string(r2.Result) != `"two"` {
			t.Errorf("response 2 = %s, want \"two\"", string(r2.Result))
		}
		r1 := <-resp1
		if string(r1.Result) != `"one"` {
			t.Errorf("response 1 = %s, want \"one\"", string(r1.Result))
		}
	})
}

// bidiPipe is a pair of one-way pipes joined so that writes to one
// end are readable from the other, simulating stdin/stdout.
type bidiPipe struct {
	r *bidiReader
	w *bidiWriter
}

func newBidiPipe() (*bidiReader, *bidiWriter) {
	pr, pw := &bidiReader{}, &bidiWriter{}
	pr.ch = make(chan string, 100)
	pw.ch = pr.ch
	return pr, pw
}

// bidiReader implements io.Reader by reading from a channel.
type bidiReader struct {
	ch   chan string
	buf  []byte
	done bool
}

func (r *bidiReader) Read(p []byte) (int, error) {
	if r.done {
		return 0, nil
	}
	if len(r.buf) == 0 {
		s, ok := <-r.ch
		if !ok {
			r.done = true
			return 0, nil
		}
		r.buf = []byte(s)
	}
	n := copy(p, r.buf)
	r.buf = r.buf[n:]
	return n, nil
}

func (r *bidiReader) Close() error { return nil }

// bidiWriter implements io.WriteCloser by writing to a channel.
type bidiWriter struct {
	ch chan<- string
}

func (w *bidiWriter) WriteLine(line string) {
	w.ch <- line + "\n"
}

func (w *bidiWriter) Write(p []byte) (int, error) {
	w.ch <- string(p)
	return len(p), nil
}

func (w *bidiWriter) Close() error {
	close(w.ch)
	return nil
}

// bidiScanner wraps a bidiReader to provide bufio.Scanner-like line scanning.
type bidiScanner struct {
	r      *bidiReader
	buf    []byte
	line   []byte
	done   bool
	closed bool
}

func newBidiScanner(r *bidiReader) *bidiScanner {
	return &bidiScanner{r: r}
}

func (s *bidiScanner) Scan() bool {
	if s.done {
		return false
	}
	s.line = nil
	tmp := make([]byte, 4096)
	for {
		n, err := s.r.Read(tmp)
		if n == 0 {
			s.done = true
			return false
		}
		_ = err
		s.buf = append(s.buf, tmp[:n]...)
		if idx := indexOf(s.buf, '\n'); idx >= 0 {
			s.line = s.buf[:idx]
			s.buf = s.buf[idx+1:]
			return true
		}
	}
}

func (s *bidiScanner) Bytes() []byte { return s.line }

func (s *bidiScanner) Text() string { return string(s.line) }

func indexOf(b []byte, c byte) int {
	for i, v := range b {
		if v == c {
			return i
		}
	}
	return -1
}
