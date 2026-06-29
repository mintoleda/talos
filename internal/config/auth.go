package config

import (
	"encoding/json"
	"os"
	"path/filepath"
)

type authEntry struct {
	Type string `json:"type"`
	Key  string `json:"key"`
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

func WriteAuthKey(baseDir, providerName, key string) error {
	path := filepath.Join(baseDir, "auth.json")
	entries := map[string]authEntry{}
	if data, err := os.ReadFile(path); err == nil {
		_ = json.Unmarshal(data, &entries)
	}
	entries[providerName] = authEntry{Type: "api_key", Key: key}
	data, err := json.MarshalIndent(entries, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0600)
}

func ResolveKeyFor(baseDir, providerName, _ string) string {
	return ReadAuthKey(baseDir, providerName)
}
