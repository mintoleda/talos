package config

import (
	"os"
	"path/filepath"
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
