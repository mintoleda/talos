package mcp

import "context"

type Transport interface {
	Send(ctx context.Context, req jsonRPCRequest) (jsonRPCResponse, error)
	Close() error
}
