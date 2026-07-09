package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/mintoleda/talos/internal/engine"
)

func TestOpenAICompatibleBaseURLCloudflareFromAuthJSON(t *testing.T) {
	dir := t.TempDir()
	data := []byte(`{"cloudflare":{"type":"api_key","key":"secret","account_id":"acct_auth"}}`)
	if err := os.WriteFile(filepath.Join(dir, "auth.json"), data, 0600); err != nil {
		t.Fatal(err)
	}
	base, err := engine.OpenAICompatibleBaseURL(dir, "cloudflare", "")
	if err != nil {
		t.Fatal(err)
	}
	want := "https://api.cloudflare.com/client/v4/accounts/acct_auth/ai"
	if base != want {
		t.Fatalf("base=%q want %q", base, want)
	}
}

func TestOpenAICompatibleBaseURLCloudflareFromEnvAccountID(t *testing.T) {
	t.Setenv("CLOUDFLARE_ACCOUNT_ID", "acct_env")
	base, err := engine.OpenAICompatibleBaseURL(t.TempDir(), "cloudflare", "")
	if err != nil {
		t.Fatal(err)
	}
	want := "https://api.cloudflare.com/client/v4/accounts/acct_env/ai"
	if base != want {
		t.Fatalf("base=%q want %q", base, want)
	}
}

func TestOpenAICompatibleBaseURLCloudflareStripsV1Override(t *testing.T) {
	base, err := engine.OpenAICompatibleBaseURL(t.TempDir(), "cloudflare", "https://api.cloudflare.com/client/v4/accounts/acct_123/ai/v1")
	if err != nil {
		t.Fatal(err)
	}
	want := "https://api.cloudflare.com/client/v4/accounts/acct_123/ai"
	if base != want {
		t.Fatalf("base=%q want %q", base, want)
	}
}

func TestOpenAICompatibleBaseURLCloudflareRequiresAccountID(t *testing.T) {
	t.Setenv("CLOUDFLARE_ACCOUNT_ID", "")
	if _, err := engine.OpenAICompatibleBaseURL(t.TempDir(), "cloudflare", ""); err == nil {
		t.Fatal("expected missing account id error")
	}
}
