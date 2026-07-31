package config

import (
	"os"
	"path/filepath"
	"strings"
)

type configKV struct {
	Key   string
	Value string
}

// saveProviderKeys rewrites individual keys in config.toml in place, leaving
// every other key, section, comment and blank line untouched. Keys are looked
// up at the document root and in the [provider] section, so both the flat and
// the sectioned config layouts round-trip correctly.
func saveProviderKeys(baseDir string, kvs []configKV) error {
	path := filepath.Join(baseDir, "config.toml")

	data, err := os.ReadFile(path)
	if err != nil && !os.IsNotExist(err) {
		return err
	}

	out := applyProviderKeys(string(data), kvs)
	return os.WriteFile(path, []byte(out), 0644)
}

func applyProviderKeys(src string, kvs []configKV) string {
	trailingNewline := src == "" || strings.HasSuffix(src, "\n")
	var lines []string
	if src != "" {
		lines = strings.Split(strings.TrimSuffix(src, "\n"), "\n")
	}

	applied := make(map[string]bool, len(kvs))
	section := ""
	rootEnd := len(lines)
	providerStart, providerEnd := -1, -1

	for i, line := range lines {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "[") {
			if section == "" && rootEnd == len(lines) {
				rootEnd = i
			}
			if section == "provider" {
				providerEnd = i
			}
			section = strings.Trim(trimmed, "[]")
			if section == "provider" {
				providerStart = i
			}
			continue
		}
		if section != "" && section != "provider" {
			continue
		}
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}
		eq := strings.Index(trimmed, "=")
		if eq < 0 {
			continue
		}
		key := strings.TrimSpace(trimmed[:eq])
		for _, kv := range kvs {
			if key != kv.Key || applied[kv.Key] {
				continue
			}
			indent := line[:len(line)-len(strings.TrimLeft(line, " \t"))]
			lines[i] = indent + kv.Key + " = " + quoteTOML(kv.Value)
			applied[kv.Key] = true
		}
	}
	if section == "provider" {
		providerEnd = len(lines)
	}

	var missing []string
	for _, kv := range kvs {
		if !applied[kv.Key] {
			missing = append(missing, kv.Key+" = "+quoteTOML(kv.Value))
		}
	}
	if len(missing) > 0 {
		at := trimBlankTail(lines, rootEnd)
		if providerStart >= 0 {
			at = trimBlankTail(lines, providerEnd)
		}
		lines = append(lines[:at], append(missing, lines[at:]...)...)
	}

	out := strings.Join(lines, "\n")
	if trailingNewline && out != "" {
		out += "\n"
	}
	return out
}

func trimBlankTail(lines []string, end int) int {
	for end > 0 && strings.TrimSpace(lines[end-1]) == "" {
		end--
	}
	return end
}

func quoteTOML(v string) string {
	r := strings.NewReplacer(`\`, `\\`, `"`, `\"`, "\n", `\n`, "\t", `\t`, "\r", `\r`)
	return `"` + r.Replace(v) + `"`
}
