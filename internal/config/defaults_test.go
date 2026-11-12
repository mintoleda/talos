package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadHasNoImplicitModelProviderOrEndpoint(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("TALOS_HOME", t.TempDir())
	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Provider != "" || cfg.Model != "" || cfg.BaseURL != "" {
		t.Fatalf("unexpected implicit routing: provider=%q model=%q base_url=%q", cfg.Provider, cfg.Model, cfg.BaseURL)
	}
}

func TestLoadPreservesExplicitModelRouting(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	dir := t.TempDir()
	t.Setenv("TALOS_HOME", dir)
	content := "provider = \"deepseek\"\nmodel = \"deepseek-chat\"\nbase_url = \"https://api.deepseek.com\"\n"
	if err := os.WriteFile(filepath.Join(dir, "config.toml"), []byte(content), 0600); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Provider != "deepseek" || cfg.Model != "deepseek-chat" || cfg.BaseURL != "https://api.deepseek.com" {
		t.Fatalf("explicit configuration was changed: provider=%q model=%q base_url=%q", cfg.Provider, cfg.Model, cfg.BaseURL)
	}
}
