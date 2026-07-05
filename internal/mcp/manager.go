package mcp

import (
	"context"
	"fmt"
	"sync"

	"github.com/mintoleda/talos/internal/tools"
)

type Manager struct {
	servers []*ServerConn
}

func NewManager(ctx context.Context, cfgs []ServerConfig) (*Manager, []error) {
	if len(cfgs) == 0 {
		return &Manager{}, nil
	}

	type result struct {
		conn *ServerConn
		err  error
	}
	ch := make(chan result, len(cfgs))
	var wg sync.WaitGroup
	for _, cfg := range cfgs {
		wg.Add(1)
		go func(cfg ServerConfig) {
			defer wg.Done()
			conn, err := Connect(ctx, cfg)
			ch <- result{conn: conn, err: err}
		}(cfg)
	}
	go func() {
		wg.Wait()
		close(ch)
	}()

	var manager Manager
	var errs []error
	for r := range ch {
		if r.err != nil {
			errs = append(errs, r.err)
		} else {
			manager.servers = append(manager.servers, r.conn)
		}
	}
	return &manager, errs
}

func (m *Manager) Tools() []tools.Tool {
	var all []tools.Tool
	for _, s := range m.servers {
		all = append(all, bridgeTools(s)...)
	}
	return all
}

func (m *Manager) Servers() []*ServerConn {
	return m.servers
}

func (m *Manager) ConnectedCount() int {
	return len(m.servers)
}

func (m *Manager) Close() {
	for _, s := range m.servers {
		s.Close()
	}
}

func (m *Manager) Status() string {
	if len(m.servers) == 0 {
		return "no mcp servers connected"
	}
	var total int
	for _, s := range m.servers {
		total += len(s.Tools())
	}
	s := fmt.Sprintf("mcp servers (%d connected, %d tools total)", len(m.servers), total)
	for _, srv := range m.servers {
		s += fmt.Sprintf("\n  %s — %d tools", srv.Name(), len(srv.Tools()))
	}
	return s
}
