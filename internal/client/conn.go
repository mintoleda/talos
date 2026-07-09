package client

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"sync"
	"sync/atomic"

	"golang.org/x/net/websocket"

	"github.com/mintoleda/talos/internal/protocol"
	"github.com/mintoleda/talos/internal/transport"
	"github.com/mintoleda/talos/internal/version"
)

// ClientConn is a JSON-line connection to a talos server/daemon.
type ClientConn struct {
	enc     *json.Encoder
	dec     *json.Decoder
	writeMu sync.Mutex
	nextID  atomic.Uint64
	Session string // stamped on every outbound ClientMsg when non-empty

	pendingMu sync.Mutex
	pending   map[uint64]chan response
}

type response struct {
	result json.RawMessage
	err    string
}

func (c *ClientConn) Send(text string) error {
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	return c.enc.Encode(transport.ClientMsg{Type: "input", Text: text, Session: c.Session})
}

func (c *ClientConn) Steer(text string) error {
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	return c.enc.Encode(transport.ClientMsg{Type: "steer", Text: text, Session: c.Session})
}

func (c *ClientConn) Interrupt() error {
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	return c.enc.Encode(transport.ClientMsg{Type: "interrupt", Session: c.Session})
}

func (c *ClientConn) Approve(ok bool, plan []byte) error {
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	return c.enc.Encode(transport.ClientMsg{Type: "approve", Approved: ok, Plan: plan, Session: c.Session})
}

func (c *ClientConn) Subscribe(session string) error {
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	return c.enc.Encode(transport.ClientMsg{Type: "subscribe", Session: session})
}

func (c *ClientConn) Unsubscribe(session string) error {
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	return c.enc.Encode(transport.ClientMsg{Type: "unsubscribe", Session: session})
}

func (c *ClientConn) Request(ctx context.Context, method string, params any) (json.RawMessage, error) {
	raw, err := json.Marshal(params)
	if err != nil {
		return nil, err
	}

	id := c.nextID.Add(1)
	ch := make(chan response, 1)

	c.pendingMu.Lock()
	c.pending[id] = ch
	c.pendingMu.Unlock()

	c.writeMu.Lock()
	err = c.enc.Encode(transport.ClientMsg{Type: "request", ID: id, Method: method, Params: raw, Session: c.Session})
	c.writeMu.Unlock()
	if err != nil {
		c.removePending(id)
		return nil, err
	}

	select {
	case resp := <-ch:
		if resp.err != "" {
			return nil, errors.New(resp.err)
		}
		return resp.result, nil
	case <-ctx.Done():
		c.removePending(id)
		return nil, ctx.Err()
	}
}

func (c *ClientConn) removePending(id uint64) {
	c.pendingMu.Lock()
	delete(c.pending, id)
	c.pendingMu.Unlock()
}

func (c *ClientConn) handleResponse(sm transport.ServerMsg) {
	c.pendingMu.Lock()
	ch := c.pending[sm.ID]
	delete(c.pending, sm.ID)
	c.pendingMu.Unlock()
	if ch == nil {
		return
	}
	ch <- response{result: sm.Result, err: sm.Err}
}

// RunClient connects to a talos server over a Unix socket.
func RunClient(ctx context.Context, sockPath string) (*ClientConn, <-chan protocol.Event, error) {
	return RunClientNetwork(ctx, "unix", sockPath, "")
}

// RunClientNetwork dials network/address (unix or tcp).
func RunClientNetwork(ctx context.Context, network, address, token string) (*ClientConn, <-chan protocol.Event, error) {
	conn, err := net.Dial(network, address)
	if err != nil {
		return nil, nil, fmt.Errorf("no daemon at %s — start with 'talos serve': %w", address, err)
	}
	return runClientConn(ctx, conn, token)
}

// RunClientWebSocket dials a websocket URL.
func RunClientWebSocket(ctx context.Context, url, token string) (*ClientConn, <-chan protocol.Event, error) {
	conn, err := websocket.Dial(url, "", "http://localhost/")
	if err != nil {
		return nil, nil, fmt.Errorf("no websocket server at %s: %w", url, err)
	}
	return runClientConn(ctx, conn, token)
}

func runClientConn(ctx context.Context, conn net.Conn, token string) (*ClientConn, <-chan protocol.Event, error) {
	enc := json.NewEncoder(conn)
	dec := json.NewDecoder(conn)

	var hello transport.ServerMsg
	if err := dec.Decode(&hello); err != nil || hello.Type != "hello" {
		_ = conn.Close()
		return nil, nil, fmt.Errorf("bad handshake")
	}
	if !version.Compatible(hello.Version) {
		_ = conn.Close()
		return nil, nil, fmt.Errorf("server version %s incompatible with client %s", hello.Version, version.VERSION)
	}
	if token != "" {
		if err := enc.Encode(transport.ClientMsg{Type: "auth", Token: token}); err != nil {
			_ = conn.Close()
			return nil, nil, err
		}
	}

	cc := &ClientConn{enc: enc, dec: dec, pending: make(map[uint64]chan response)}
	events := make(chan protocol.Event, 64)

	go func() {
		defer close(events)
		defer conn.Close()
		for {
			var sm transport.ServerMsg
			if err := dec.Decode(&sm); err != nil {
				events <- protocol.Notice{Level: "error", Text: "server disconnected"}
				return
			}
			if sm.Type == "response" {
				cc.handleResponse(sm)
				continue
			}
			if sm.Type != "event" {
				continue
			}
			ev, err := protocol.UnmarshalEvent(sm.EType, sm.Event)
			if err != nil {
				continue
			}
			select {
			case events <- ev:
			case <-ctx.Done():
				return
			}
		}
	}()

	return cc, events, nil
}
