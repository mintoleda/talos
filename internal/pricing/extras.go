package pricing

// extras fills gaps in the pi.dev snapshot for models talos serves itself.
// Entries already present in data.json win, so a regenerated snapshot takes
// over once it covers them.
//
// OpenAI Codex (ChatGPT subscription) models, from pi's openai-codex
// catalogue (base tier; USD per million tokens). Usage is billed against the
// subscription, so these are notional API-equivalent costs.
var extras = map[string]Price{
	"openai-codex/gpt-5.6-luna":  {Input: 0.2, Output: 1.2, CacheRead: 0.02, CacheWrite: 0.25, Context: 272000},
	"openai-codex/gpt-5.6-sol":   {Input: 4, Output: 20, CacheRead: 0.4, CacheWrite: 5, Context: 272000},
	"openai-codex/gpt-5.6-terra": {Input: 2, Output: 12, CacheRead: 0.2, CacheWrite: 2.5, Context: 272000},
	"openai-codex/gpt-6-astra":   {Input: 10, Output: 50, CacheRead: 1, CacheWrite: 12.5, Context: 272000},
	"openai-codex/gpt-6-luna":    {Input: 0.1, Output: 0.5, CacheRead: 0.01, CacheWrite: 0.125, Context: 272000},
	"openai-codex/gpt-6-sol":     {Input: 2, Output: 10, CacheRead: 0.2, CacheWrite: 2.5, Context: 272000},
	"openai-codex/gpt-6.1-sol":   {Input: 2, Output: 10, CacheRead: 0.1, CacheWrite: 2.5, Context: 500000},
}

func init() {
	for id, p := range extras {
		if _, ok := Default.m[id]; !ok {
			Default.m[id] = p
		}
	}
}
