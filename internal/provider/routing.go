package provider

import (
	"context"
	"errors"

	"github.com/mintoleda/talos/internal/protocol"
)

type RoutingPolicy interface {
	Pick(req protocol.Request, providers []Provider) Provider
}

type RoutingProvider struct {
	providers []Provider
	policy    RoutingPolicy
}

func NewRoutingProvider(policy RoutingPolicy, providers ...Provider) (*RoutingProvider, error) {
	if len(providers) == 0 {
		return nil, errors.New("routing provider requires at least one provider")
	}
	if policy == nil {
		policy = PrimaryWithFallback{}
	}
	return &RoutingProvider{providers: providers, policy: policy}, nil
}

func (r *RoutingProvider) StreamTurn(ctx context.Context, req protocol.Request) (<-chan protocol.ProviderEvent, error) {
	p := r.policy.Pick(req, r.providers)
	if p == nil {
		return nil, errors.New("routing policy returned no provider")
	}
	return p.StreamTurn(ctx, req)
}

func (r *RoutingProvider) ListModels(ctx context.Context) ([]string, error) {
	if len(r.providers) == 0 {
		return nil, errors.New("no providers configured")
	}
	return r.providers[0].ListModels(ctx)
}

type RoundRobin struct {
	next int
}

func (rr *RoundRobin) Pick(req protocol.Request, providers []Provider) Provider {
	if len(providers) == 0 {
		return nil
	}
	p := providers[rr.next%len(providers)]
	rr.next++
	return p
}

// PrimaryWithFallback always tries the first provider; it never actually
// falls back in StreamTurn because the Provider interface does not expose
// errors up-front. This is a placeholder for a future implementation that
// probes health or retries.
type PrimaryWithFallback struct{}

func (PrimaryWithFallback) Pick(req protocol.Request, providers []Provider) Provider {
	if len(providers) == 0 {
		return nil
	}
	return providers[0]
}
