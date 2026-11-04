// Package acp implements the agent side of the Agent Client Protocol
// (https://agentclientprotocol.com) over newline-delimited JSON-RPC 2.0 on
// stdio, so a client such as Paseo can drive the Talos runtime headlessly.
package acp

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sync"
)

// JSON-RPC error codes used by ACP.
const (
	codeParseError     = -32700
	codeInvalidRequest = -32600
	codeMethodNotFound = -32601
	codeInvalidParams  = -32602
	codeInternalError  = -32603
)

// RPCError is a JSON-RPC error object.
type RPCError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
	Data    any    `json:"data,omitempty"`
}

func (e *RPCError) Error() string { return fmt.Sprintf("rpc error %d: %s", e.Code, e.Message) }

func invalidParams(err error) *RPCError {
	return &RPCError{Code: codeInvalidParams, Message: err.Error()}
}

type wireMsg struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method,omitempty"`
	Params  json.RawMessage `json:"params,omitempty"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *RPCError       `json:"error,omitempty"`
}

// afterResponse wraps a handler result with a callback run once the response
// has been written, for notifications that must follow it (e.g. session
// updates that reference a session id the client only learns from the
// response).
type afterResponse struct {
	result any
	after  func()
}

// Handler answers one incoming request. Notifications (no id) are dispatched
// to the same handler; their return value is discarded.
type Handler func(ctx context.Context, method string, params json.RawMessage) (any, error)

// Conn is a bidirectional JSON-RPC connection. Incoming requests are handled
// concurrently so a long session/prompt never blocks session/cancel or the
// responses to our own outgoing requests (session/request_permission).
type Conn struct {
	r       io.Reader
	w       io.Writer
	handler Handler

	wmu sync.Mutex

	mu      sync.Mutex
	nextID  int64
	pending map[string]chan wireMsg
	closed  bool

	wg sync.WaitGroup
}

func NewConn(r io.Reader, w io.Writer, h Handler) *Conn {
	return &Conn{r: r, w: w, handler: h, pending: make(map[string]chan wireMsg)}
}

// Serve reads messages until EOF or ctx is cancelled. In-flight handlers see
// ctx cancelled when Serve returns and are waited for.
func (c *Conn) Serve(ctx context.Context) error {
	ctx, cancel := context.WithCancel(ctx)
	defer func() {
		cancel()
		c.failPending()
		c.wg.Wait()
	}()

	sc := bufio.NewScanner(c.r)
	sc.Buffer(make([]byte, 0, 1<<20), 64<<20) // prompts can embed large resources
	for sc.Scan() {
		line := sc.Bytes()
		if len(line) == 0 {
			continue
		}
		var m wireMsg
		if err := json.Unmarshal(line, &m); err != nil {
			c.write(wireMsg{JSONRPC: "2.0", ID: json.RawMessage("null"), Error: &RPCError{Code: codeParseError, Message: err.Error()}})
			continue
		}
		switch {
		case m.Method != "":
			c.wg.Add(1)
			go func(m wireMsg) {
				defer c.wg.Done()
				c.dispatch(ctx, m)
			}(m)
		case len(m.ID) > 0:
			c.mu.Lock()
			ch := c.pending[string(m.ID)]
			delete(c.pending, string(m.ID))
			c.mu.Unlock()
			if ch != nil {
				ch <- m
			}
		}
	}
	if err := sc.Err(); err != nil && !errors.Is(err, io.EOF) {
		return err
	}
	return nil
}

func (c *Conn) dispatch(ctx context.Context, m wireMsg) {
	result, err := c.handler(ctx, m.Method, m.Params)
	var after func()
	if ar, ok := result.(afterResponse); ok {
		result, after = ar.result, ar.after
	}
	if after != nil {
		defer after()
	}
	if len(m.ID) == 0 {
		return // notification
	}
	resp := wireMsg{JSONRPC: "2.0", ID: m.ID}
	if err != nil {
		var re *RPCError
		if !errors.As(err, &re) {
			re = &RPCError{Code: codeInternalError, Message: err.Error()}
		}
		resp.Error = re
	} else {
		if result == nil {
			result = struct{}{}
		}
		raw, merr := json.Marshal(result)
		if merr != nil {
			resp.Error = &RPCError{Code: codeInternalError, Message: merr.Error()}
		} else {
			resp.Result = raw
		}
	}
	c.write(resp)
}

// Notify sends a notification to the client.
func (c *Conn) Notify(method string, params any) error {
	raw, err := json.Marshal(params)
	if err != nil {
		return err
	}
	return c.write(wireMsg{JSONRPC: "2.0", Method: method, Params: raw})
}

// Call sends a request to the client and waits for its response.
func (c *Conn) Call(ctx context.Context, method string, params, out any) error {
	raw, err := json.Marshal(params)
	if err != nil {
		return err
	}
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return errors.New("acp: connection closed")
	}
	c.nextID++
	id := json.RawMessage(fmt.Sprintf("%d", c.nextID))
	ch := make(chan wireMsg, 1)
	c.pending[string(id)] = ch
	c.mu.Unlock()

	if err := c.write(wireMsg{JSONRPC: "2.0", ID: id, Method: method, Params: raw}); err != nil {
		c.drop(id)
		return err
	}
	select {
	case resp := <-ch:
		if resp.Error != nil {
			return resp.Error
		}
		if out != nil && len(resp.Result) > 0 {
			return json.Unmarshal(resp.Result, out)
		}
		return nil
	case <-ctx.Done():
		c.drop(id)
		return ctx.Err()
	}
}

func (c *Conn) drop(id json.RawMessage) {
	c.mu.Lock()
	delete(c.pending, string(id))
	c.mu.Unlock()
}

func (c *Conn) failPending() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.closed = true
	for id, ch := range c.pending {
		ch <- wireMsg{Error: &RPCError{Code: codeInternalError, Message: "connection closed"}}
		delete(c.pending, id)
	}
}

func (c *Conn) write(m wireMsg) error {
	raw, err := json.Marshal(m)
	if err != nil {
		return err
	}
	raw = append(raw, '\n')
	c.wmu.Lock()
	defer c.wmu.Unlock()
	_, err = c.w.Write(raw)
	return err
}
