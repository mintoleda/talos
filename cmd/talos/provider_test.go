package main

import "testing"

func TestOpenAICompatibleBaseURLCloudflareFromAccountID(t *testing.T) {
	t.Setenv("CLOUDFLARE_ACCOUNT_ID", "acct_123")
	base, err := openAICompatibleBaseURL("cloudflare", "")
	if err != nil {
		t.Fatal(err)
	}
	want := "https://api.cloudflare.com/client/v4/accounts/acct_123/ai"
	if base != want {
		t.Fatalf("base=%q want %q", base, want)
	}
}

func TestOpenAICompatibleBaseURLCloudflareStripsV1Override(t *testing.T) {
	base, err := openAICompatibleBaseURL("cloudflare", "https://api.cloudflare.com/client/v4/accounts/acct_123/ai/v1")
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
	if _, err := openAICompatibleBaseURL("cloudflare", ""); err == nil {
		t.Fatal("expected missing account id error")
	}
}
