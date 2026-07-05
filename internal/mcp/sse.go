package mcp

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
)

type sseTransport struct {
	url    string
	client *http.Client

	pending   map[int]chan jsonRPCResponse
	pendingMu sync.Mutex
	nextID    atomic.Int32

	ctx    context.Context
	cancel context.CancelFunc
}

func newSSETransport(url string) *sseTransport {
	ctx, cancel := context.WithCancel(context.Background())
	t := &sseTransport{
		url:     strings.TrimRight(url, "/"),
		client:  &http.Client{},
		pending: make(map[int]chan jsonRPCResponse),
		ctx:     ctx,
		cancel:  cancel,
	}
	go t.listenSSE()
	return t
}

func (t *sseTransport) Send(ctx context.Context, req jsonRPCRequest) (jsonRPCResponse, error) {
	ch := make(chan jsonRPCResponse, 1)
	t.pendingMu.Lock()
	t.pending[req.ID] = ch
	t.pendingMu.Unlock()

	body, err := json.Marshal(req)
	if err != nil {
		t.pendingMu.Lock()
		delete(t.pending, req.ID)
		t.pendingMu.Unlock()
		return jsonRPCResponse{}, fmt.Errorf("marshal request: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, "POST", t.url+"/message", bytes.NewReader(body))
	if err != nil {
		t.pendingMu.Lock()
		delete(t.pending, req.ID)
		t.pendingMu.Unlock()
		return jsonRPCResponse{}, fmt.Errorf("create request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")

	resp, err := t.client.Do(httpReq)
	if err != nil {
		t.pendingMu.Lock()
		delete(t.pending, req.ID)
		t.pendingMu.Unlock()
		return jsonRPCResponse{}, fmt.Errorf("post message: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusAccepted {
		msg, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		t.pendingMu.Lock()
		delete(t.pending, req.ID)
		t.pendingMu.Unlock()
		return jsonRPCResponse{}, fmt.Errorf("server returned %d: %s", resp.StatusCode, string(msg))
	}

	select {
	case r := <-ch:
		return r, nil
	case <-ctx.Done():
		t.pendingMu.Lock()
		delete(t.pending, req.ID)
		t.pendingMu.Unlock()
		return jsonRPCResponse{}, ctx.Err()
	}
}

func (t *sseTransport) listenSSE() {
	req, err := http.NewRequestWithContext(t.ctx, "GET", t.url+"/sse", nil)
	if err != nil {
		return
	}
	req.Header.Set("Accept", "text/event-stream")

	for {
		resp, err := t.client.Do(req)
		if err != nil {
			select {
			case <-t.ctx.Done():
				return
			default:
				continue
			}
		}

		t.readSSEStream(resp.Body)
		resp.Body.Close()

		select {
		case <-t.ctx.Done():
			return
		default:
		}
	}
}

func (t *sseTransport) readSSEStream(r io.Reader) {
	scanner := bufio.NewScanner(r)
	var event, data string
	for scanner.Scan() {
		line := scanner.Text()
		if line == "" {
			if data != "" {
				if event == "message" || event == "" {
					var resp jsonRPCResponse
					if err := json.Unmarshal([]byte(data), &resp); err == nil {
						t.pendingMu.Lock()
						ch, ok := t.pending[resp.ID]
						if ok {
							delete(t.pending, resp.ID)
						}
						t.pendingMu.Unlock()
						if ok {
							ch <- resp
						}
					}
				}
				event = ""
				data = ""
			}
			continue
		}
		if strings.HasPrefix(line, "event:") {
			event = strings.TrimSpace(line[6:])
		} else if strings.HasPrefix(line, "data:") {
			data += strings.TrimSpace(line[5:])
		}
	}
}

func (t *sseTransport) Close() error {
	t.cancel()
	return nil
}
