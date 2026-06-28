package provider

import (
	"context"
	"testing"

	"github.com/mintoleda/talos/internal/protocol"
)

type fakeProvider struct {
	name string
}

func (f *fakeProvider) StreamTurn(ctx context.Context, req protocol.Request) (<-chan protocol.ProviderEvent, error) {
	ch := make(chan protocol.ProviderEvent, 1)
	ch <- protocol.PEText{Text: f.name}
	close(ch)
	return ch, nil
}

func (f *fakeProvider) ListModels(ctx context.Context) ([]string, error) {
	return nil, nil
}

func TestRoundRobin(t *testing.T) {
	a := &fakeProvider{name: "a"}
	b := &fakeProvider{name: "b"}
	rr := &RoundRobin{}
	p1 := rr.Pick(protocol.Request{}, []Provider{a, b})
	p2 := rr.Pick(protocol.Request{}, []Provider{a, b})
	if p1 == p2 {
		t.Fatal("expected round-robin to alternate")
	}
}

func TestRoutingProvider(t *testing.T) {
	a := &fakeProvider{name: "a"}
	rp, err := NewRoutingProvider(PrimaryWithFallback{}, a)
	if err != nil {
		t.Fatal(err)
	}
	ch, err := rp.StreamTurn(context.Background(), protocol.Request{})
	if err != nil {
		t.Fatal(err)
	}
	var text string
	for ev := range ch {
		if e, ok := ev.(protocol.PEText); ok {
			text = e.Text
		}
	}
	if text != "a" {
		t.Fatalf("expected primary provider a, got %q", text)
	}
}
