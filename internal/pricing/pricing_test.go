package pricing

import "testing"

func TestEmbeddedParses(t *testing.T) {
	if len(Default.m) == 0 {
		t.Fatal("embedded pricing table is empty")
	}
}

func TestLookupAndCost(t *testing.T) {
	p, ok := Default.Lookup("deepseek-chat")
	if !ok {
		t.Fatal("deepseek-chat not found")
	}
	if p.Input <= 0 || p.Output <= 0 || p.Context <= 0 {
		t.Errorf("unexpected price: %+v", p)
	}

	// Cost of 1M in + 1M out should equal input+output per-million rates.
	want := p.Input + p.Output
	if got := Default.Cost("deepseek-chat", 1_000_000, 1_000_000); got != want {
		t.Errorf("cost = %v, want %v", got, want)
	}

	if Default.ContextWindow("deepseek-chat") != p.Context {
		t.Errorf("context window mismatch")
	}
}

func TestLookupProviderPrefix(t *testing.T) {
	// Provider-namespaced IDs should resolve to the bare model entry.
	if _, ok := Default.Lookup("opencode-go/deepseek-chat"); !ok {
		t.Error("provider-prefixed lookup failed")
	}
}

func TestUnknownModel(t *testing.T) {
	if got := Default.Cost("totally-made-up-model-xyz", 1000, 1000); got != 0 {
		t.Errorf("unknown model cost = %v, want 0", got)
	}
}
