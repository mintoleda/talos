package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"sync/atomic"
)

type ServerConn struct {
	name      string
	transport Transport
	tools     []MCPTool
	nextID    atomic.Int32
}

func Connect(ctx context.Context, cfg ServerConfig) (*ServerConn, error) {
	if cfg.Command != "" && cfg.URL != "" {
		return nil, fmt.Errorf("mcp server %q: set either command or url, not both", cfg.Name)
	}
	if cfg.Command == "" && cfg.URL == "" {
		return nil, fmt.Errorf("mcp server %q: must set command or url", cfg.Name)
	}

	var transport Transport
	var err error
	if cfg.URL != "" {
		transport = newSSETransport(cfg.URL)
	} else {
		transport, err = newStdioTransport(cfg.Name, cfg.Command, cfg.Args, cfg.Env)
		if err != nil {
			return nil, fmt.Errorf("mcp server %q: stdio: %w", cfg.Name, err)
		}
	}

	s := &ServerConn{
		name:      cfg.Name,
		transport: transport,
	}

	initResult, err := s.sendReq(ctx, "initialize", initializeParams{
		ProtocolVersion: protocolVersion,
		Capabilities:    json.RawMessage(`{}`),
		ClientInfo: clientInfo{
			Name:    "talos",
			Version: "dev",
		},
	})
	if err != nil {
		transport.Close()
		return nil, fmt.Errorf("mcp server %q: initialize: %w", cfg.Name, err)
	}
	_ = initResult

	var ltr listToolsResult
	if err := s.call(ctx, "tools/list", nil, &ltr); err != nil {
		transport.Close()
		return nil, fmt.Errorf("mcp server %q: tools/list: %w", cfg.Name, err)
	}
	s.tools = ltr.Tools

	return s, nil
}

func (s *ServerConn) Name() string    { return s.name }
func (s *ServerConn) Tools() []MCPTool { return s.tools }

func (s *ServerConn) CallTool(ctx context.Context, name string, args map[string]any) (callToolResult, error) {
	var result callToolResult
	if err := s.call(ctx, "tools/call", callToolParams{Name: name, Arguments: args}, &result); err != nil {
		return callToolResult{}, err
	}
	return result, nil
}

func (s *ServerConn) Close() error {
	return s.transport.Close()
}

func (s *ServerConn) sendReq(ctx context.Context, method string, params any) (json.RawMessage, error) {
	var rawParams json.RawMessage
	if params != nil {
		b, err := json.Marshal(params)
		if err != nil {
			return nil, fmt.Errorf("marshal params: %w", err)
		}
		rawParams = b
	}
	req := jsonRPCRequest{
		JSONRPC: jsonRPCVersion,
		ID:      int(s.nextID.Add(1)),
		Method:  method,
		Params:  rawParams,
	}
	resp, err := s.transport.Send(ctx, req)
	if err != nil {
		return nil, err
	}
	if resp.Error != nil {
		return nil, resp.Error
	}
	return resp.Result, nil
}

func (s *ServerConn) call(ctx context.Context, method string, params any, dst any) error {
	result, err := s.sendReq(ctx, method, params)
	if err != nil {
		return err
	}
	return json.Unmarshal(result, dst)
}
