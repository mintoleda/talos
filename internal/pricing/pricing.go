// Package pricing maps model IDs to their token prices and context window so
// the harness can show the dollar cost and context usage of a model run
// (notably per-subagent stats in the TUI).
//
// The built-in table is generated from https://pi.dev/models (the same source
// as the search-models skill), flattened to `provider/model-id -> {input,
// output, context}` where input/output are US dollars per million tokens. It
// is embedded so cost works offline, and can be extended or corrected via
// ~/.talos/pricing.toml.
//
// To refresh the snapshot:
//
//go:generate go run ./gen
package pricing

import (
	"context"
	_ "embed"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/BurntSushi/toml"
)

//go:embed data.json
var embedded []byte

// Price holds the per-million-token rates and context window for one model.
type Price struct {
	Input   float64 // USD per 1,000,000 input tokens
	Output  float64 // USD per 1,000,000 output tokens
	Context int     // context window in tokens (0 = unknown)
}

// rawPrice mirrors the compact embedded JSON shape: {"i":, "o":, "c":}.
type rawPrice struct {
	I float64 `json:"i"`
	O float64 `json:"o"`
	C int     `json:"c"`
}

// Table is a lookup of model ID to Price.
type Table struct {
	m map[string]Price
}

// Default is the built-in table parsed from the embedded models.dev snapshot.
var Default = parseEmbedded()

func parseEmbedded() *Table {
	var raw map[string]rawPrice
	_ = json.Unmarshal(embedded, &raw)
	m := make(map[string]Price, len(raw))
	for id, r := range raw {
		m[id] = Price{Input: r.I, Output: r.O, Context: r.C}
	}
	return &Table{m: m}
}

const (
	cacheFile  = "pricing-cache.json"
	cacheMaxAge = 24 * time.Hour
	fetchTimeout = 5 * time.Second
)

// Load returns a pricing table built from (lowest to highest precedence):
//  1. The embedded data.json snapshot (always available offline).
//  2. ~/.talos/pricing-cache.json — the result of the last successful live
//     fetch. If the cache is older than 24 h, Load attempts a refresh in the
//     background; the current call uses whatever is already cached.
//  3. ~/.talos/pricing.toml — user overrides (highest precedence).
//
// Any failure at steps 2–3 is silently ignored; the table always returns at
// least the embedded data.
//
// The override file format is:
//
//	[models."deepseek-chat"]
//	input = 0.14
//	output = 0.28
//	context = 1000000
func Load(baseDir string) *Table {
	t := &Table{m: make(map[string]Price, len(Default.m))}
	for k, v := range Default.m {
		t.m[k] = v
	}

	cachePath := filepath.Join(baseDir, cacheFile)
	if raw, err := os.ReadFile(cachePath); err == nil {
		var cached map[string]rawPrice
		if json.Unmarshal(raw, &cached) == nil {
			for k, r := range cached {
				t.m[k] = Price{Input: r.I, Output: r.O, Context: r.C}
			}
		}
	}

	// Refresh the cache in the background if it's stale.
	info, err := os.Stat(cachePath)
	if err != nil || time.Since(info.ModTime()) > cacheMaxAge {
		go func() {
			ctx, cancel := context.WithTimeout(context.Background(), fetchTimeout)
			defer cancel()
			if data, err := FetchLive(ctx); err == nil {
				if b, err := MarshalRaw(data); err == nil {
					_ = os.WriteFile(cachePath, b, 0644)
				}
			}
		}()
	}

	applyTOMLOverrides(baseDir, t)
	return t
}

func applyTOMLOverrides(baseDir string, t *Table) {
	data, err := os.ReadFile(filepath.Join(baseDir, "pricing.toml"))
	if err != nil {
		return
	}
	var override struct {
		Models map[string]struct {
			Input   float64 `toml:"input"`
			Output  float64 `toml:"output"`
			Context int     `toml:"context"`
		} `toml:"models"`
	}
	if toml.Unmarshal(data, &override) != nil {
		return
	}
	for id, p := range override.Models {
		t.m[id] = Price{Input: p.Input, Output: p.Output, Context: p.Context}
	}
}

// Lookup resolves a model ID to its Price.
//
// Resolution order:
//  1. Exact match ("deepseek/deepseek-chat" → key "deepseek/deepseek-chat")
//  2. Model-ID suffix match: extract the last path segment of the lookup key
//     and scan for any table key ending with "/segment". This handles bare IDs
//     ("deepseek-chat" → matches "deepseek/deepseek-chat") and cross-provider
//     prefixes ("opencode-go/deepseek-chat" → id "deepseek-chat" → matches
//     "deepseek/deepseek-chat").
func (t *Table) Lookup(model string) (Price, bool) {
	if p, ok := t.m[model]; ok {
		return p, true
	}
	// Extract the model-id segment (the part after the last "/", or the whole
	// string if there's no "/").
	modelID := model
	if i := strings.LastIndex(model, "/"); i >= 0 {
		modelID = model[i+1:]
	}
	suffix := "/" + modelID
	var best string
	for k := range t.m {
		if strings.HasSuffix(k, suffix) && len(k) > len(best) {
			best = k
		}
	}
	if best != "" {
		return t.m[best], true
	}
	return Price{}, false
}

// Cost returns the dollar cost of a run with the given token counts. It returns
// 0 when the model's price is unknown.
func (t *Table) Cost(model string, inputTokens, outputTokens int) float64 {
	p, ok := t.Lookup(model)
	if !ok {
		return 0
	}
	return float64(inputTokens)/1e6*p.Input + float64(outputTokens)/1e6*p.Output
}

// ContextWindow returns the model's context window in tokens, or 0 if unknown.
func (t *Table) ContextWindow(model string) int {
	if p, ok := t.Lookup(model); ok {
		return p.Context
	}
	return 0
}
