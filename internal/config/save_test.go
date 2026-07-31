package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const sectioned = `[provider]
base_url = ""
model = "deepseek-v4-flash"
provider = "deepseek"
thinking_level = "high"

[timeout]
bash_timeout_seconds = 120

[memory]
historian = true
`

func TestSaveThinkingLevelSectioned(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.toml")
	if err := os.WriteFile(path, []byte(sectioned), 0644); err != nil {
		t.Fatal(err)
	}
	if err := SaveThinkingLevel(dir, "xhigh"); err != nil {
		t.Fatalf("save: %v", err)
	}
	got := readFile(t, path)
	if !strings.Contains(got, `thinking_level = "xhigh"`) {
		t.Errorf("level not updated:\n%s", got)
	}
	for _, want := range []string{"[timeout]", "bash_timeout_seconds = 120", "[memory]", "historian = true"} {
		if !strings.Contains(got, want) {
			t.Errorf("lost %q from config:\n%s", want, got)
		}
	}
	cfg := &Config{}
	if err := loadFile(path, cfg); err != nil {
		t.Fatalf("reload: %v", err)
	}
	if cfg.ThinkingLevel != "xhigh" || cfg.Model != "deepseek-v4-flash" {
		t.Errorf("reloaded %+v", cfg)
	}
}

func TestSaveProviderModelFlat(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.toml")
	src := "# my config\nmodel = \"old\"\nprovider = \"old\"\napi_key = \"secret\"\n\n[[mcp_servers]]\nname = \"fs\"\n"
	if err := os.WriteFile(path, []byte(src), 0644); err != nil {
		t.Fatal(err)
	}
	if err := SaveProviderModel(dir, "anthropic", "claude-opus-5"); err != nil {
		t.Fatalf("save: %v", err)
	}
	got := readFile(t, path)
	for _, want := range []string{"# my config", `model = "claude-opus-5"`, `provider = "anthropic"`, `api_key = "secret"`, "[[mcp_servers]]", `name = "fs"`} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q:\n%s", want, got)
		}
	}
}

func TestSaveInsertsMissingKey(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.toml")
	if err := os.WriteFile(path, []byte("[provider]\nmodel = \"m\"\n\n[server]\nlisten = \"x\"\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := SaveThinkingLevel(dir, "low"); err != nil {
		t.Fatal(err)
	}
	got := readFile(t, path)
	if !strings.Contains(got, "model = \"m\"\nthinking_level = \"low\"\n\n[server]") {
		t.Errorf("insert placed wrong:\n%s", got)
	}
}

func TestSaveCreatesFile(t *testing.T) {
	dir := t.TempDir()
	if err := SaveThinkingLevel(dir, "high"); err != nil {
		t.Fatal(err)
	}
	if got := readFile(t, filepath.Join(dir, "config.toml")); got != "thinking_level = \"high\"\n" {
		t.Errorf("got %q", got)
	}
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}
