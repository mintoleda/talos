package server

import (
	"context"
	"encoding/json"
	"fmt"
	"net"

	"github.com/mintoleda/talos/internal/protocol"
	"github.com/mintoleda/talos/internal/transport"
	"github.com/mintoleda/talos/internal/version"
)

type ClientConn struct {
	enc *json.Encoder
	dec *json.Decoder
}

func (c *ClientConn) Send(text string) error {
	return c.enc.Encode(transport.ClientMsg{Type: "input", Text: text})
}

func (c *ClientConn) Interrupt() error {
	return c.enc.Encode(transport.ClientMsg{Type: "interrupt"})
}

// RunClient connects to a talos server over a Unix socket and returns a
// ClientConn plus a channel of server events. The caller should:
//   1. Start a Bubble Tea program.
//   2. Forward events from the returned channel to p.Send(...).
//   3. Wire the ClientConn's Send/Interrupt methods into the TUI model.
//
// The event channel is closed when the server disconnects.
func RunClient(ctx context.Context, sockPath string) (*ClientConn, <-chan protocol.Event, error) {
	conn, err := net.Dial("unix", sockPath)
	if err != nil {
		return nil, nil, fmt.Errorf("no server at %s — start with 'talos --server': %w", sockPath, err)
	}

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
			if sm.Type != "event" {
				continue
			}
			ev, err := decodeEvent(sm)
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

	return &ClientConn{enc: enc, dec: dec}, events, nil
}

func decodeEvent(sm transport.ServerMsg) (protocol.Event, error) {
	switch sm.EType {
	case "UserInput":
		var e protocol.UserInput
		return e, json.Unmarshal(sm.Event, &e)
	case "ModelChanged":
		var e protocol.ModelChanged
		return e, json.Unmarshal(sm.Event, &e)
	case "TextDelta":
		var e protocol.TextDelta
		return e, json.Unmarshal(sm.Event, &e)
	case "ToolStarted":
		var e protocol.ToolStarted
		return e, json.Unmarshal(sm.Event, &e)
	case "ToolFinished":
		var e protocol.ToolFinished
		return e, json.Unmarshal(sm.Event, &e)
	case "Notice":
		var e protocol.Notice
		return e, json.Unmarshal(sm.Event, &e)
	case "TurnEnded":
		var e protocol.TurnEnded
		return e, json.Unmarshal(sm.Event, &e)
	case "PermissionRequested":
		var e protocol.PermissionRequested
		return e, json.Unmarshal(sm.Event, &e)
	}
	return nil, fmt.Errorf("unknown event type %q", sm.EType)
}
