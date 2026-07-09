package engine

import (
	"fmt"
	"strings"

	"github.com/mintoleda/talos/internal/config"
	"github.com/mintoleda/talos/internal/memory"
	"github.com/mintoleda/talos/internal/provider"
	"github.com/mintoleda/talos/internal/provider/anthropic"
	"github.com/mintoleda/talos/internal/provider/openai"
	"github.com/mintoleda/talos/internal/session"
)

// NewProvider creates an LLM provider and compactor from the given config.
func NewProvider(cfg *config.Config, noTools bool) (provider.Provider, *session.Compactor, error) {
	var prov provider.Provider
	switch cfg.Provider {
	case "anthropic":
		base := CleanBaseURL(cfg.BaseURL)
		if base == "" || base == "https://api.deepseek.com" {
			base = "https://api.anthropic.com"
		}
		prov = anthropic.New(base, cfg.APIKey, anthropic.Config{
			MaxTokens:     8192,
			ThinkingLevel: cfg.ThinkingLevel,
		})
	default:
		// For all OpenAI-compatible providers, look up the canonical base URL
		// from the known-provider registry so switchProvider works correctly
		// after a model picker selection. Always check known providers first,
		// and only fall back to cfg.BaseURL for custom/unknown providers —
		// the default "https://api.deepseek.com" should not override a known
		// provider's endpoint.
		aliases := map[string]string{"go": "opencode-go", "zen": "opencode-zen", "opencode": "opencode-zen"}
		name := cfg.Provider
		if a, ok := aliases[name]; ok {
			name = a
		}
		base, err := OpenAICompatibleBaseURL(cfg.BaseDir, name, cfg.BaseURL)
		if err != nil {
			return nil, nil, err
		}
		prov = openai.New(base, cfg.APIKey)
	}

	// Build the compactor. By default, use a deterministic, zero-cost
	// extractive summarizer (user messages verbatim, tool results dropped) —
	// like the old placeholder it makes no API calls and is reproducible on
	// replay. When the user sets summary_model in config, an LLM-based
	// summarizer (using the specified model) replaces it for richer summaries.
	var sum session.Summarizer = session.ExtractSummarizer{}
	if cfg.SummaryModel != "" {
		sum = session.NewLLMSummarizer(prov, cfg.SummaryModel, "")
	}
	compactor := session.NewCompactor(sum)
	if cfg.CompactThreshold > 0 {
		compactor.Threshold = cfg.CompactThreshold
	}
	if cfg.CompactEmergencyThreshold > 0 {
		compactor.EmergencyThreshold = cfg.CompactEmergencyThreshold
	}
	if cfg.CompactChunkSize > 0 {
		compactor.ChunkSize = cfg.CompactChunkSize
	}
	compactor.Clamp()
	return prov, compactor, nil
}

// OpenAICompatibleBaseURL resolves the base URL for an OpenAI-compatible provider.
func OpenAICompatibleBaseURL(baseDir, name, configured string) (string, error) {
	base := CleanBaseURL(configured)
	if name == "cloudflare" {
		if base == "" || base == "https://api.deepseek.com" {
			accountID := config.ResolveCloudflareAccountID(baseDir)
			if accountID == "" {
				return "", fmt.Errorf("cloudflare provider requires auth.json account_id, CLOUDFLARE_ACCOUNT_ID, or a base_url ending in /accounts/{id}/ai/v1")
			}
			base = fmt.Sprintf("https://api.cloudflare.com/client/v4/accounts/%s/ai", accountID)
		}
		return base, nil
	}
	if kp, ok := provider.ByName(name); ok {
		if base == "" || (base == "https://api.deepseek.com" && name != "deepseek") {
			base = kp.BaseURL
		}
	}
	return base, nil
}

// RoleLLMConfig returns a shallow copy of cfg with role-specific provider overrides.
func RoleLLMConfig(cfg *config.Config, providerName, modelName, baseURL, apiKey string) *config.Config {
	cc := *cfg
	if providerName != "" {
		cc.Provider = providerName
		if baseURL == "" {
			cc.BaseURL = ""
		}
	}
	if modelName != "" {
		cc.Model = modelName
	}
	if baseURL != "" {
		cc.BaseURL = baseURL
	}
	if apiKey != "" {
		cc.APIKey = apiKey
	} else {
		cc.ResolveAPIKey()
	}
	return &cc
}

// HistorianProvider builds the provider used for post-compaction memory extraction.
func HistorianProvider(cfg *config.Config) (provider.Provider, string, error) {
	model := cfg.HistorianModel
	if model == "" {
		model = cfg.SummaryModel
	}
	if model == "" {
		model = cfg.Model
	}
	roleCfg := RoleLLMConfig(cfg, cfg.HistorianProvider, model, cfg.HistorianBaseURL, cfg.HistorianAPIKey)
	prov, _, err := NewProvider(roleCfg, true)
	return prov, model, err
}

// DreamerProvider builds the provider used by `talos dream` curation.
func DreamerProvider(cfg *config.Config, overrideModel string) (provider.Provider, string, error) {
	model := overrideModel
	if model == "" {
		model = cfg.DreamerModel
	}
	if model == "" {
		model = cfg.SummaryModel
	}
	if model == "" {
		model = cfg.Model
	}
	roleCfg := RoleLLMConfig(cfg, cfg.DreamerProvider, model, cfg.DreamerBaseURL, cfg.DreamerAPIKey)
	prov, _, err := NewProvider(roleCfg, true)
	return prov, model, err
}

// NewHistorian constructs a session historian when enabled and a store is available.
func NewHistorian(cfg *config.Config, store *memory.Store) (*session.Historian, error) {
	if !cfg.Historian || store == nil {
		return nil, nil
	}
	prov, model, err := HistorianProvider(cfg)
	if err != nil {
		return nil, err
	}
	return &session.Historian{Provider: prov, Model: model, Store: store}, nil
}

// CleanBaseURL strips trailing /v1 (and /v1/) suffixes from a provider base URL.
// The anthropic and openai clients already append their own /v1/<endpoint> paths,
// so a user-provided URL like https://api.openai.com/v1 would produce double /v1
// segments (e.g. /v1/v1/chat/completions).
func CleanBaseURL(raw string) string {
	s := strings.TrimRight(raw, "/")
	if strings.HasSuffix(s, "/v1") {
		s = s[:len(s)-3]
	}
	return s
}
