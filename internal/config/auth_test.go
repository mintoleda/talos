package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestWriteAuthKeyPreservesAccountID(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "auth.json")
	data := []byte(`{"cloudflare":{"type":"api_key","key":"old","account_id":"acct_123"}}`)
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	if err := WriteAuthKey(dir, "cloudflare", "new"); err != nil {
		t.Fatal(err)
	}
	if got := ReadAuthKey(dir, "cloudflare"); got != "new" {
		t.Fatalf("key=%q want new", got)
	}
	if got := ReadAuthAccountID(dir, "cloudflare"); got != "acct_123" {
		t.Fatalf("account_id=%q want acct_123", got)
	}
}

func TestWriteOAuthPiShapeAndPreservesOthers(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "auth.json")
	data := []byte(`{"openai":{"type":"api_key","key":"sk-1","extra":"kept"}}`)
	if err := os.WriteFile(path, data, 0644); err != nil {
		t.Fatal(err)
	}
	cred := OAuthCredential{Access: "acc", Refresh: "ref", Expires: 1700000000000, AccountID: "acct"}
	if err := WriteOAuth(dir, "openai-codex", cred); err != nil {
		t.Fatal(err)
	}

	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var file map[string]map[string]any
	if err := json.Unmarshal(raw, &file); err != nil {
		t.Fatal(err)
	}
	got := file["openai-codex"]
	want := map[string]any{"type": "oauth", "access": "acc", "refresh": "ref", "expires": float64(1700000000000), "accountId": "acct"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("oauth entry = %v, want %v", got, want)
	}
	if file["openai"]["key"] != "sk-1" || file["openai"]["extra"] != "kept" {
		t.Fatalf("other entry not preserved: %v", file["openai"])
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if perm := info.Mode().Perm(); perm != 0600 {
		t.Fatalf("mode = %o, want 600", perm)
	}

	back, ok := ReadOAuth(dir, "openai-codex")
	if !ok || back != cred {
		t.Fatalf("ReadOAuth = %+v %v, want %+v", back, ok, cred)
	}
	if ReadAuthKey(dir, "openai-codex") != "" {
		t.Fatal("oauth entry must not surface as an API key")
	}
	if ReadAuthKey(dir, "openai") != "sk-1" {
		t.Fatal("api key lost")
	}
}

func TestReadOAuthAcceptsPiFile(t *testing.T) {
	dir := t.TempDir()
	data := []byte(`{"openai-codex":{"type":"oauth","access":"a","refresh":"r","expires":123,"accountId":"x"}}`)
	if err := os.WriteFile(filepath.Join(dir, "auth.json"), data, 0600); err != nil {
		t.Fatal(err)
	}
	cred, ok := ReadOAuth(dir, "openai-codex")
	if !ok || cred.Access != "a" || cred.Refresh != "r" || cred.Expires != 123 || cred.AccountID != "x" {
		t.Fatalf("ReadOAuth = %+v %v", cred, ok)
	}
	if _, ok := ReadOAuth(dir, "missing"); ok {
		t.Fatal("missing entry reported present")
	}
}

func TestWriteRefusesMalformedAuthFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "auth.json")
	if err := os.WriteFile(path, []byte("{not json"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := WriteOAuth(dir, "openai-codex", OAuthCredential{Access: "a", Refresh: "r"}); err == nil {
		t.Fatal("expected error for malformed auth.json")
	}
	raw, _ := os.ReadFile(path)
	if string(raw) != "{not json" {
		t.Fatal("malformed auth.json was overwritten")
	}
}
