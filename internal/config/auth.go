package config

import (
	"encoding/json"
	"os"
	"path/filepath"
)

type authEntry struct {
	Type      string `json:"type"`
	Key       string `json:"key"`
	AccountID string `json:"account_id,omitempty"`
}

func ReadAuthKey(baseDir, providerName string) string {
	data, err := os.ReadFile(filepath.Join(baseDir, "auth.json"))
	if err != nil {
		return ""
	}
	var entries map[string]authEntry
	if err := json.Unmarshal(data, &entries); err != nil {
		return ""
	}
	return entries[providerName].Key
}

func ReadAuthAccountID(baseDir, providerName string) string {
	data, err := os.ReadFile(filepath.Join(baseDir, "auth.json"))
	if err != nil {
		return ""
	}
	var entries map[string]authEntry
	if err := json.Unmarshal(data, &entries); err != nil {
		return ""
	}
	return entries[providerName].AccountID
}

func WriteAuthKey(baseDir, providerName, key string) error {
	path := filepath.Join(baseDir, "auth.json")
	entries := map[string]authEntry{}
	if data, err := os.ReadFile(path); err == nil {
		_ = json.Unmarshal(data, &entries)
	}
	entry := entries[providerName]
	entry.Type = "api_key"
	entry.Key = key
	entries[providerName] = entry
	data, err := json.MarshalIndent(entries, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0600)
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
