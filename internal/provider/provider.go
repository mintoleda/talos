package provider

import (
	"context"

	"github.com/mintoleda/talos/internal/protocol"
)

type Provider interface {
	StreamTurn(ctx context.Context, req protocol.Request) (<-chan protocol.ProviderEvent, error)
	ListModels(ctx context.Context) ([]string, error)
}
