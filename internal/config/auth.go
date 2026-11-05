package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
)

// authEntry is one provider's record in auth.json. API-key entries use
// Type "api_key" with Key (and optionally AccountID for Cloudflare). OAuth
// entries use pi's shape so the two files stay interchangeable:
// {"type":"oauth","access":…,"refresh":…,"expires":<unix ms>,"accountId":…}.
type authEntry struct {
	Type      string `json:"type"`
	Key       string `json:"key,omitempty"`
	AccountID string `json:"account_id,omitempty"`

	Access         string `json:"access,omitempty"`
	Refresh        string `json:"refresh,omitempty"`
	Expires        int64  `json:"expires,omitempty"`
	OAuthAccountID string `json:"accountId,omitempty"`
}

// OAuthCredential is a stored OAuth login (currently only "openai-codex").
// Expires is a Unix timestamp in milliseconds, matching pi.
type OAuthCredential struct {
	Access    string `json:"access"`
	Refresh   string `json:"refresh"`
	Expires   int64  `json:"expires"`
	AccountID string `json:"accountId,omitempty"`
}

// authMu serializes read-modify-write cycles on auth.json within a process
// (e.g. concurrent token refreshes from subagents sharing a login).
var authMu sync.Mutex

func authPath(baseDir string) string { return filepath.Join(baseDir, "auth.json") }

// readAuthEntries returns the raw per-provider records. A missing file is an
// empty map; a malformed file is an error so writers never clobber it.
func readAuthEntries(baseDir string) (map[string]json.RawMessage, error) {
	entries := map[string]json.RawMessage{}
	data, err := os.ReadFile(authPath(baseDir))
	if errors.Is(err, os.ErrNotExist) {
		return entries, nil
	}
	if err != nil {
		return nil, err
	}
	if len(data) == 0 {
		return entries, nil
	}
	if err := json.Unmarshal(data, &entries); err != nil {
		return nil, fmt.Errorf("parse auth.json: %w", err)
	}
	if entries == nil {
		entries = map[string]json.RawMessage{}
	}
	return entries, nil
}

func readAuthEntry(baseDir, providerName string) (authEntry, bool) {
	entries, err := readAuthEntries(baseDir)
	if err != nil {
		return authEntry{}, false
	}
	raw, ok := entries[providerName]
	if !ok {
		return authEntry{}, false
	}
	var e authEntry
	if err := json.Unmarshal(raw, &e); err != nil {
		return authEntry{}, false
	}
	return e, true
}

// updateAuthEntry rewrites one provider's record, keeping every other entry
// (including fields talos does not know about) intact, and replaces the file
// atomically with mode 0600.
func updateAuthEntry(baseDir, providerName string, update func(*authEntry)) error {
	authMu.Lock()
	defer authMu.Unlock()

	entries, err := readAuthEntries(baseDir)
	if err != nil {
		return err
	}
	var e authEntry
	if raw, ok := entries[providerName]; ok {
		_ = json.Unmarshal(raw, &e)
	}
	update(&e)
	raw, err := json.Marshal(e)
	if err != nil {
		return err
	}
	entries[providerName] = raw
	data, err := json.MarshalIndent(entries, "", "  ")
	if err != nil {
		return err
	}
	return writeFileAtomic(authPath(baseDir), data)
}

func writeFileAtomic(path string, data []byte) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	// CreateTemp opens the file 0600, so tokens are never world-readable.
	f, err := os.CreateTemp(dir, ".auth-*.json")
	if err != nil {
		return err
	}
	tmp := f.Name()
	if _, err := f.Write(data); err != nil {
		f.Close()
		os.Remove(tmp)
		return err
	}
	if err := f.Close(); err != nil {
		os.Remove(tmp)
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		os.Remove(tmp)
		return err
	}
	return nil
}

func ReadAuthKey(baseDir, providerName string) string {
	e, _ := readAuthEntry(baseDir, providerName)
	return e.Key
}

func ReadAuthAccountID(baseDir, providerName string) string {
	e, _ := readAuthEntry(baseDir, providerName)
	return e.AccountID
}

func WriteAuthKey(baseDir, providerName, key string) error {
	return updateAuthEntry(baseDir, providerName, func(e *authEntry) {
		e.Type = "api_key"
		e.Key = key
	})
}

// ReadOAuth returns the stored OAuth credential for providerName, if any.
func ReadOAuth(baseDir, providerName string) (OAuthCredential, bool) {
	e, ok := readAuthEntry(baseDir, providerName)
	if !ok || e.Type != "oauth" || e.Access == "" {
		return OAuthCredential{}, false
	}
	return OAuthCredential{
		Access:    e.Access,
		Refresh:   e.Refresh,
		Expires:   e.Expires,
		AccountID: e.OAuthAccountID,
	}, true
}

// WriteOAuth persists an OAuth credential, replacing any previous entry
// (API key or OAuth) for providerName.
func WriteOAuth(baseDir, providerName string, cred OAuthCredential) error {
	if cred.Access == "" || cred.Refresh == "" {
		return fmt.Errorf("oauth credential for %s is incomplete", providerName)
	}
	return updateAuthEntry(baseDir, providerName, func(e *authEntry) {
		*e = authEntry{
			Type:           "oauth",
			Access:         cred.Access,
			Refresh:        cred.Refresh,
			Expires:        cred.Expires,
			OAuthAccountID: cred.AccountID,
		}
	})
}

func ResolveKeyFor(baseDir, providerName, envVar string) string {
	if key := ReadAuthKey(baseDir, providerName); key != "" {
		return key
	}
	if envVar == "" {
		envVar = defaultKeyEnv(providerName)
	}
	if envVar == "" {
		return ""
	}
	return os.Getenv(envVar)
}

func ResolveCloudflareAccountID(baseDir string) string {
	if accountID := ReadAuthAccountID(baseDir, "cloudflare"); accountID != "" {
		return accountID
	}
	return os.Getenv("CLOUDFLARE_ACCOUNT_ID")
}

func defaultKeyEnv(providerName string) string {
	switch providerName {
	case "anthropic":
		return "ANTHROPIC_API_KEY"
	case "cloudflare":
		return "CLOUDFLARE_API_KEY"
	case "deepseek":
		return "DEEPSEEK_API_KEY"
	case "openai":
		return "OPENAI_API_KEY"
	case "openrouter":
		return "OPENROUTER_API_KEY"
	case "opencode-go", "opencode-zen":
		return "OPENCODE_API_KEY"
	default:
		return ""
	}
}
