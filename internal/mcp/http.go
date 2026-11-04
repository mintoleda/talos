package mcp

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	headerSessionID       = "Mcp-Session-Id"
	headerProtocolVersion = "MCP-Protocol-Version"
)

// notifier is implemented by transports that can send JSON-RPC notifications
// (messages with no id and no response).
type notifier interface {
	Notify(ctx context.Context, method string, params any) error
}

// httpTransport implements the MCP "Streamable HTTP" client transport
// (spec 2025-03-26 / 2025-06-18): every JSON-RPC message is POSTed to a single
// endpoint and the reply is either a JSON body or an SSE stream.
type httpTransport struct {
	url     string
	headers map[string]string
	client  *http.Client

	mu           sync.Mutex
	sessionID    string
	protoVersion string

	ctx    context.Context
	cancel context.CancelFunc
}

func newHTTPTransport(url string, headers map[string]string) *httpTransport {
	ctx, cancel := context.WithCancel(context.Background())
	return &httpTransport{
		url:     url,
		headers: headers,
		client:  &http.Client{}, // no overall timeout: SSE responses may stream; ctx governs
		ctx:     ctx,
		cancel:  cancel,
	}
}

func (t *httpTransport) setProtocolVersion(v string) {
	t.mu.Lock()
	t.protoVersion = v
	t.mu.Unlock()
}

func (t *httpTransport) session() (id, version string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.sessionID, t.protoVersion
}

// post sends body and returns the open response (2xx only). The returned
// cancel func must be called once the body has been consumed.
func (t *httpTransport) post(ctx context.Context, body []byte) (*http.Response, context.CancelFunc, error) {
	reqCtx, cancel := context.WithCancel(ctx)
	// Close() aborts in-flight requests.
	stop := context.AfterFunc(t.ctx, cancel)
	done := func() {
		stop()
		cancel()
	}

	httpReq, err := http.NewRequestWithContext(reqCtx, http.MethodPost, t.url, bytes.NewReader(body))
	if err != nil {
		done()
		return nil, nil, fmt.Errorf("create request: %w", err)
	}
	for k, v := range t.headers {
		httpReq.Header.Set(k, v)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Accept", "application/json, text/event-stream")
	sid, ver := t.session()
	if sid != "" {
		httpReq.Header.Set(headerSessionID, sid)
	}
	if ver != "" {
		httpReq.Header.Set(headerProtocolVersion, ver)
	}

	resp, err := t.client.Do(httpReq)
	if err != nil {
		done()
		if t.ctx.Err() != nil && ctx.Err() == nil {
			return nil, nil, errors.New("transport closed")
		}
		return nil, nil, fmt.Errorf("post message: %w", err)
	}
	if id := resp.Header.Get(headerSessionID); id != "" {
		t.mu.Lock()
		t.sessionID = id
		t.mu.Unlock()
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		msg, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		resp.Body.Close()
		done()
		return nil, nil, fmt.Errorf("server returned %d: %s", resp.StatusCode, string(msg))
	}
	return resp, done, nil
}

func (t *httpTransport) Send(ctx context.Context, req jsonRPCRequest) (jsonRPCResponse, error) {
	body, err := json.Marshal(req)
	if err != nil {
		return jsonRPCResponse{}, fmt.Errorf("marshal request: %w", err)
	}
	resp, done, err := t.post(ctx, body)
	if err != nil {
		return jsonRPCResponse{}, err
	}
	defer done()
	defer resp.Body.Close()
	// Dropping the connection does not cancel the request on the server
	// (streamable HTTP spec); tell it explicitly so remote work stops.
	defer func() {
		if ctx.Err() != nil && t.ctx.Err() == nil {
			go t.notifyCancelled(req.ID, ctx.Err().Error())
		}
	}()

	mt, _, _ := mime.ParseMediaType(resp.Header.Get("Content-Type"))
	switch mt {
	case "application/json":
		data, err := io.ReadAll(resp.Body)
		if err != nil {
			return jsonRPCResponse{}, fmt.Errorf("read response: %w", err)
		}
		r, ok, err := findResponse(data, req.ID)
		if err != nil {
			return jsonRPCResponse{}, err
		}
		if !ok {
			return jsonRPCResponse{}, fmt.Errorf("no response for request id %d", req.ID)
		}
		return r, nil
	case "text/event-stream":
		return readSSEResponse(resp.Body, req.ID, t.answer)
	default:
		return jsonRPCResponse{}, fmt.Errorf("unexpected response (status %d, content-type %q)", resp.StatusCode, resp.Header.Get("Content-Type"))
	}
}

// Notify sends a JSON-RPC notification. The server should answer 202 Accepted.
func (t *httpTransport) Notify(ctx context.Context, method string, params any) error {
	n := jsonRPCNotification{JSONRPC: jsonRPCVersion, Method: method}
	if params != nil {
		b, err := json.Marshal(params)
		if err != nil {
			return fmt.Errorf("marshal params: %w", err)
		}
		n.Params = b
	}
	body, err := json.Marshal(n)
	if err != nil {
		return fmt.Errorf("marshal notification: %w", err)
	}
	resp, done, err := t.post(ctx, body)
	if err != nil {
		return err
	}
	defer done()
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<20))
	return nil
}

// notifyCancelled sends notifications/cancelled for an abandoned request.
func (t *httpTransport) notifyCancelled(id int, reason string) {
	ctx, cancel := context.WithTimeout(t.ctx, 5*time.Second)
	defer cancel()
	_ = t.Notify(ctx, "notifications/cancelled", map[string]any{"requestId": id, "reason": reason})
}

// answer replies to a server-initiated request seen on a response stream.
// Only ping is supported; anything else gets method-not-found so the server
// does not wait forever.
func (t *httpTransport) answer(env rpcEnvelope) {
	type reply struct {
		JSONRPC string          `json:"jsonrpc"`
		ID      json.RawMessage `json:"id"`
		Result  any             `json:"result,omitempty"`
		Error   *jsonRPCError   `json:"error,omitempty"`
	}
	r := reply{JSONRPC: jsonRPCVersion, ID: env.ID}
	if env.Method == "ping" {
		r.Result = struct{}{}
	} else {
		r.Error = &jsonRPCError{Code: -32601, Message: "method not found: " + env.Method}
	}
	body, err := json.Marshal(r)
	if err != nil {
		return
	}
	go func() {
		ctx, cancel := context.WithTimeout(t.ctx, 5*time.Second)
		defer cancel()
		resp, done, err := t.post(ctx, body)
		if err != nil {
			return
		}
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<20))
		resp.Body.Close()
		done()
	}()
}

// Close aborts in-flight requests and, if a session exists, asks the server to
// terminate it (best effort).
func (t *httpTransport) Close() error {
	t.cancel()
	sid, ver := t.session()
	if sid == "" {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodDelete, t.url, nil)
	if err != nil {
		return nil
	}
	for k, v := range t.headers {
		req.Header.Set(k, v)
	}
	req.Header.Set(headerSessionID, sid)
	if ver != "" {
		req.Header.Set(headerProtocolVersion, ver)
	}
	if resp, err := t.client.Do(req); err == nil {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
		resp.Body.Close()
	}
	return nil
}

// rpcEnvelope decodes any JSON-RPC message; ID is raw because server-initiated
// requests may use string ids.
type rpcEnvelope struct {
	ID     json.RawMessage `json:"id"`
	Method string          `json:"method"`
	Result json.RawMessage `json:"result"`
	Error  *jsonRPCError   `json:"error"`
}

// matchResponse reports whether data is the JSON-RPC response for id.
// Notifications and server-initiated requests (Method set) never match.
func matchResponse(data []byte, id int) (jsonRPCResponse, bool) {
	var env rpcEnvelope
	if err := json.Unmarshal(data, &env); err != nil || env.Method != "" || len(env.ID) == 0 {
		return jsonRPCResponse{}, false
	}
	got, err := strconv.Atoi(strings.TrimSpace(string(env.ID)))
	if err != nil || got != id {
		return jsonRPCResponse{}, false
	}
	return jsonRPCResponse{JSONRPC: jsonRPCVersion, ID: got, Result: env.Result, Error: env.Error}, true
}

// findResponse extracts the response for id from a JSON body (single object
// or batch array).
func findResponse(data []byte, id int) (jsonRPCResponse, bool, error) {
	trimmed := bytes.TrimSpace(data)
	if len(trimmed) == 0 {
		return jsonRPCResponse{}, false, errors.New("empty response body")
	}
	if trimmed[0] == '[' {
		var batch []json.RawMessage
		if err := json.Unmarshal(trimmed, &batch); err != nil {
			return jsonRPCResponse{}, false, fmt.Errorf("decode response: %w", err)
		}
		for _, m := range batch {
			if r, ok := matchResponse(m, id); ok {
				return r, true, nil
			}
		}
		return jsonRPCResponse{}, false, nil
	}
	if !json.Valid(trimmed) {
		return jsonRPCResponse{}, false, errors.New("decode response: invalid JSON")
	}
	r, ok := matchResponse(trimmed, id)
	return r, ok, nil
}

// readSSEResponse reads SSE events until the JSON-RPC response for id arrives.
// Server-initiated requests are passed to onRequest (if non-nil);
// notifications are ignored.
func readSSEResponse(body io.Reader, id int, onRequest func(rpcEnvelope)) (jsonRPCResponse, error) {
	br := bufio.NewReader(body)
	var event string
	var data []string

	dispatch := func() (jsonRPCResponse, bool) {
		defer func() { event, data = "", nil }()
		if len(data) == 0 || (event != "" && event != "message") {
			return jsonRPCResponse{}, false
		}
		payload := []byte(strings.Join(data, "\n"))
		if onRequest != nil {
			var env rpcEnvelope
			if json.Unmarshal(payload, &env) == nil && env.Method != "" && len(env.ID) > 0 && string(env.ID) != "null" {
				onRequest(env)
				return jsonRPCResponse{}, false
			}
		}
		r, ok, err := findResponse(payload, id)
		if err != nil || !ok {
			return jsonRPCResponse{}, false
		}
		return r, true
	}

	for {
		line, err := br.ReadString('\n')
		if line != "" || err == nil {
			line = strings.TrimRight(line, "\r\n")
			switch {
			case line == "":
				if r, ok := dispatch(); ok {
					return r, nil
				}
			case strings.HasPrefix(line, ":"):
				// comment / keep-alive
			case strings.HasPrefix(line, "event:"):
				event = strings.TrimSpace(line[len("event:"):])
			case strings.HasPrefix(line, "data:"):
				data = append(data, strings.TrimPrefix(line[len("data:"):], " "))
			}
		}
		if err != nil {
			if r, ok := dispatch(); ok {
				return r, nil
			}
			if err == io.EOF {
				return jsonRPCResponse{}, fmt.Errorf("event stream ended without response for request id %d", id)
			}
			return jsonRPCResponse{}, fmt.Errorf("read event stream: %w", err)
		}
	}
}
