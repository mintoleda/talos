package testutil

import (
	"context"

	"github.com/mintoleda/talos/internal/protocol"
)

// FakeProvider implements provider.Provider for testing. It delivers
// pre-configured event batches on StreamTurn and records how many calls were
// made. A zero-value FakeProvider returns no events (immediately closed
// channel), which behaves like an empty response.
type FakeProvider struct {
	Batches [][]protocol.ProviderEvent // each StreamTurn call pops the next batch
	Calls   int
}

func (f *FakeProvider) StreamTurn(ctx context.Context, req protocol.Request) (<-chan protocol.ProviderEvent, error) {
	ch := make(chan protocol.ProviderEvent)
	var batch []protocol.ProviderEvent
	if f.Calls < len(f.Batches) {
		batch = f.Batches[f.Calls]
	}
	f.Calls++
	go func() {
		defer close(ch)
		for _, e := range batch {
			ch <- e
		}
	}()
	return ch, nil
}

func (f *FakeProvider) ListModels(ctx context.Context) ([]string, error) {
	return nil, nil
}
