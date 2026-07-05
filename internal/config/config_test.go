package config

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/mintoleda/talos/internal/safety"
)

func TestLoadFileSectionedMemoryRoleProviders(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.toml")
	data := []byte(`
[provider]
provider = "opencode-go"
model = "deepseek-v4-flash"
thinking_level = "high"

[compaction]
compact_threshold = 0.8
summary_model = "cheap-summary"

[memory]
historian = true
historian_provider = "openrouter"
historian_model = "google/gemini-2.5-flash"
dreamer_provider = "anthropic"
dreamer_model = "claude-3-5-haiku-latest"
`)
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	cfg := &Config{
		Provider:                  "openai",
		Model:                     "deepseek-chat",
		PermissionMode:            safety.ModeAuto,
		BashTimeout:               120 * time.Second,
		BashMaxTimeout:            600 * time.Second,
		CompactThreshold:          0.85,
		CompactEmergencyThreshold: 0.95,
	}
	if err := loadFile(path, cfg); err != nil {
		t.Fatal(err)
	}
	if cfg.Provider != "opencode-go" || cfg.Model != "deepseek-v4-flash" || cfg.ThinkingLevel != "high" {
		t.Fatalf("provider section not loaded: provider=%q model=%q thinking=%q", cfg.Provider, cfg.Model, cfg.ThinkingLevel)
	}
	if !cfg.Historian {
		t.Fatal("historian was not enabled")
	}
	if cfg.HistorianProvider != "openrouter" || cfg.HistorianModel != "google/gemini-2.5-flash" {
		t.Fatalf("historian role not loaded: provider=%q model=%q", cfg.HistorianProvider, cfg.HistorianModel)
	}
	if cfg.DreamerProvider != "anthropic" || cfg.DreamerModel != "claude-3-5-haiku-latest" {
		t.Fatalf("dreamer role not loaded: provider=%q model=%q", cfg.DreamerProvider, cfg.DreamerModel)
	}
	if cfg.SummaryModel != "cheap-summary" || cfg.CompactThreshold != 0.8 {
		t.Fatalf("compaction section not loaded: summary=%q threshold=%v", cfg.SummaryModel, cfg.CompactThreshold)
	}
}
