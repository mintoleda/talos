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

func TestCostDetailed(t *testing.T) {
	tbl := &Table{m: map[string]Price{
		"x/m": {Input: 1, Output: 4, CacheRead: 0.2, CacheWrite: 2},
	}}
	got := tbl.CostDetailed("x/m", 1_000_000, 500_000, 100_000, 1_000_000)
	want := 0.4 + 0.1 + 0.2 + 4.0
	if diff := got - want; diff > 1e-9 || diff < -1e-9 {
		t.Errorf("cost = %v, want %v", got, want)
	}

	tbl.m["x/d"] = Price{Input: 1, Output: 4}
	got = tbl.CostDetailed("x/d", 1_000_000, 500_000, 0, 0)
	want = 0.5 + 0.05
	if diff := got - want; diff > 1e-9 || diff < -1e-9 {
		t.Errorf("default cache rate cost = %v, want %v", got, want)
	}

	if tbl.CostDetailed("x/m", 100, 300, 0, 0) < 0 {
		t.Error("cost went negative when cached exceeds prompt")
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

func TestCodexExtras(t *testing.T) {
	if got := Default.ContextWindow("gpt-6.1-sol"); got != 500000 {
		t.Fatalf("gpt-6.1-sol context = %d", got)
	}
	if got := Default.ContextWindow("openai-codex/gpt-6-luna"); got != 272000 {
		t.Fatalf("gpt-6-luna context = %d", got)
	}
}
