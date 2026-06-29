package config

import (
	"bytes"
	_ "embed"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/BurntSushi/toml"
	"github.com/mintoleda/talos/internal/safety"
)

//go:embed CORE.md
var corePrompt string

type Config struct {
	BaseURL        string
	APIKey         string
	Model          string
	Provider       string
	PermissionMode safety.Mode
	BashTimeout    time.Duration
	BashMaxTimeout time.Duration
	BashMaxOutput  int
	SystemPrompt   string
	BaseDir        string
	ThinkingLevel  string
	SearchURL                 string  // custom search endpoint (empty = DuckDuckGo HTML)
	MaxAgentDepth             int     // max subagent nesting depth (0 = default 3)
	CompactThreshold          float64 // normal compaction fires at this fraction (0 = default 0.85)
	CompactEmergencyThreshold float64 // emergency: compact regardless of chunk size (0 = default 0.95)
	CompactChunkSize          int     // messages per compaction chunk (0 = default 20)
	SummaryModel              string  // model for compaction summaries (empty = deterministic placeholder)
}

func Load() (*Config, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		home = "."
	}
	cfg := &Config{
		BaseURL:        "https://api.deepseek.com",
		Model:          "deepseek-chat",
		Provider:       "openai",
		PermissionMode: safety.ModeAuto,
		BashTimeout:    120 * time.Second,
		BashMaxTimeout: 600 * time.Second,
		BashMaxOutput:  30 * 1024,
		SystemPrompt:   strings.TrimSpace(corePrompt),
		BaseDir:        filepath.Join(home, ".talos"),
		ThinkingLevel:             "off",
		CompactThreshold:          0.85,
		CompactEmergencyThreshold: 0.95,
		CompactChunkSize:          20,
	}

	// Resolution order (lowest to highest precedence):
	//   CORE.md (shipped) → SYSTEM_PROMPT.md (global) → AGENTS.md (project)
	// Command-line flags are applied last by the caller.
	cfgFile := filepath.Join(cfg.BaseDir, "config.toml")
	if _, err := os.Stat(cfgFile); err == nil {
		if err := loadFile(cfgFile, cfg); err != nil {
			return nil, fmt.Errorf("config %s: %w", cfgFile, err)
		}
	}

	// ~/.talos/SYSTEM_PROMPT.md takes precedence over config.toml
	// but is overridden by project-level AGENTS.md (loaded in main).
	if sp, err := LoadUserSystemPrompt(cfg.BaseDir); err != nil {
		return nil, fmt.Errorf("SYSTEM_PROMPT.md: %w", err)
	} else if sp != "" {
		cfg.SystemPrompt = sp
	}

	cfg.ResolveAPIKey()
	return cfg, nil
}

func (c *Config) Override(baseURL, model, key string) {
	if baseURL != "" {
		c.BaseURL = baseURL
	}
	if model != "" {
		c.Model = model
	}
	if key != "" {
		c.APIKey = key
	}
}

func (c *Config) OverrideProvider(provider string) {
	if provider != "" {
		c.Provider = provider
		c.ResolveAPIKey()
	}
}

func (c *Config) ResolveAPIKey() {
	// Normalize provider aliases to canonical names for auth.json lookups.
	authName := c.Provider
	switch authName {
	case "go":
		authName = "opencode-go"
	case "zen":
		authName = "opencode-zen"
	case "opencode":
		authName = "opencode-go"
	}
	c.APIKey = ReadAuthKey(c.BaseDir, authName)
}

// fileConfig mirrors the small set of keys allowed in ~/.talos/config.toml.
// Pointers/zero-checks let us distinguish "absent" from "explicit zero" so the
// file only overrides a default when the key is actually present.
type fileConfig struct {
	BaseURL               string `toml:"base_url"`
	APIKey                string `toml:"api_key"`
	Model                 string `toml:"model"`
	Provider              string `toml:"provider"`
	PermissionMode        string `toml:"permission_mode"`
	ThinkingLevel         string `toml:"thinking_level"`
	BashTimeoutSeconds    int    `toml:"bash_timeout_seconds"`
	BashMaxTimeoutSeconds int    `toml:"bash_max_timeout_seconds"`
	BashMaxOutput         int    `toml:"bash_max_output"`
	SearchURL             string `toml:"search_url"`
	MaxAgentDepth         int    `toml:"max_agent_depth"`
	// Deprecated: use thinking_level. Kept for backward compat.
	ThinkingBudget int `toml:"thinking_budget"`
	CompactThreshold          float64 `toml:"compact_threshold"`
	CompactEmergencyThreshold float64 `toml:"compact_emergency"`
	CompactChunkSize          int     `toml:"compact_chunk_size"`
	SummaryModel              string  `toml:"summary_model"` // empty = deterministic placeholder
}

func loadFile(path string, cfg *Config) error {
	var fc fileConfig
	if _, err := toml.DecodeFile(path, &fc); err != nil {
		return err
	}
	if fc.BaseURL != "" {
		cfg.BaseURL = fc.BaseURL
	}
	if fc.APIKey != "" {
		cfg.APIKey = fc.APIKey
	}
	if fc.Model != "" {
		cfg.Model = fc.Model
	}
	if fc.Provider != "" {
		cfg.Provider = fc.Provider
	}
	if fc.PermissionMode != "" {
		cfg.PermissionMode = safety.ParseMode(fc.PermissionMode)
	}
	if fc.ThinkingLevel != "" {
		cfg.ThinkingLevel = fc.ThinkingLevel
	} else if fc.ThinkingBudget > 0 {
		// Backward compat: map old thinking_budget to an equivalent level.
		switch {
		case fc.ThinkingBudget >= 16384:
			cfg.ThinkingLevel = "xhigh"
		case fc.ThinkingBudget >= 8192:
			cfg.ThinkingLevel = "high"
		case fc.ThinkingBudget >= 4096:
			cfg.ThinkingLevel = "medium"
		case fc.ThinkingBudget >= 2048:
			cfg.ThinkingLevel = "low"
		default:
			cfg.ThinkingLevel = "minimal"
		}
	}
	if fc.BashTimeoutSeconds > 0 {
		cfg.BashTimeout = time.Duration(fc.BashTimeoutSeconds) * time.Second
	}
	if fc.BashMaxTimeoutSeconds > 0 {
		cfg.BashMaxTimeout = time.Duration(fc.BashMaxTimeoutSeconds) * time.Second
	}
	if fc.BashMaxOutput > 0 {
		cfg.BashMaxOutput = fc.BashMaxOutput
	}
	if fc.SearchURL != "" {
		cfg.SearchURL = fc.SearchURL
	}
	if fc.MaxAgentDepth > 0 {
		cfg.MaxAgentDepth = fc.MaxAgentDepth
	}
	if fc.CompactThreshold > 0 {
		cfg.CompactThreshold = fc.CompactThreshold
	}
	if fc.CompactEmergencyThreshold > 0 {
		cfg.CompactEmergencyThreshold = fc.CompactEmergencyThreshold
	}
	if fc.CompactChunkSize > 0 {
		cfg.CompactChunkSize = fc.CompactChunkSize
	}
	if fc.SummaryModel != "" {
		cfg.SummaryModel = fc.SummaryModel
	}
	return nil
}

func LoadUserSystemPrompt(baseDir string) (string, error) {
	path := filepath.Join(baseDir, "SYSTEM_PROMPT.md")
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return "", nil
		}
		return "", err
	}
	return strings.TrimSpace(string(data)), nil
}

func LoadProjectSystemPrompt(projectRoot string) (string, error) {
	dir := projectRoot
	if dir == "" || dir == "." {
		var err error
		dir, err = os.Getwd()
		if err != nil {
			return "", err
		}
	}
	path := filepath.Join(dir, "AGENTS.md")
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return "", nil
		}
		return "", err
	}
	return strings.TrimSpace(string(data)), nil
}

func (c *Config) Validate() error {
	if c.APIKey == "" {
		return fmt.Errorf("API key is required")
	}
	return nil
}

func SaveProviderModel(baseDir, provider, model string) error {
	path := filepath.Join(baseDir, "config.toml")

	var fc fileConfig
	if data, err := os.ReadFile(path); err == nil {
		if err := toml.Unmarshal(data, &fc); err != nil {
			return fmt.Errorf("parse config: %w", err)
		}
	} else if !os.IsNotExist(err) {
		return err
	}

	fc.Provider = provider
	fc.Model = model

	var buf bytes.Buffer
	if err := toml.NewEncoder(&buf).Encode(fc); err != nil {
		return fmt.Errorf("encode config: %w", err)
	}
	return os.WriteFile(path, buf.Bytes(), 0644)
}

func SaveThinkingLevel(baseDir, level string) error {
	path := filepath.Join(baseDir, "config.toml")

	var fc fileConfig
	if data, err := os.ReadFile(path); err == nil {
		if err := toml.Unmarshal(data, &fc); err != nil {
			return fmt.Errorf("parse config: %w", err)
		}
	} else if !os.IsNotExist(err) {
		return err
	}

	fc.ThinkingLevel = level

	var buf bytes.Buffer
	if err := toml.NewEncoder(&buf).Encode(fc); err != nil {
		return fmt.Errorf("encode config: %w", err)
	}
	return os.WriteFile(path, buf.Bytes(), 0644)
}
