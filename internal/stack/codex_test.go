package stack

import (
	"context"
	"testing"

	"github.com/mintoleda/talos/internal/config"
	"github.com/mintoleda/talos/internal/provider"
	"github.com/mintoleda/talos/internal/provider/codex"
)

func TestListModelsIncludesCodexWhenLoggedIn(t *testing.T) {
	for _, k := range provider.All {
		if k.EnvVar != "" {
			t.Setenv(k.EnvVar, "")
		}
	}
	dir := t.TempDir()
	if got := ListModels(context.Background(), dir); len(got) != 0 {
		t.Fatalf("expected no models without credentials, got %v", got)
	}
	if err := config.WriteOAuth(dir, codex.ProviderName, config.OAuthCredential{Access: "a", Refresh: "r"}); err != nil {
		t.Fatal(err)
	}
	got := ListModels(context.Background(), dir)
	if len(got) != len(codex.Models) {
		t.Fatalf("models = %v", got)
	}
	for _, e := range got {
		if e.Provider != codex.ProviderName {
			t.Fatalf("unexpected entry %+v", e)
		}
	}
}

func TestNewProviderCodex(t *testing.T) {
	cfg := &config.Config{Provider: codex.ProviderName, BaseDir: t.TempDir(), BaseURL: "https://api.deepseek.com"}
	prov, _, err := NewProvider(cfg, true)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := prov.(*codex.Client); !ok {
		t.Fatalf("provider = %T, want *codex.Client", prov)
	}
	if _, err := OpenAICompatibleBaseURL(cfg.BaseDir, codex.ProviderName, ""); err == nil {
		t.Fatal("openai-codex must not resolve as an OpenAI-compatible endpoint")
	}
}
