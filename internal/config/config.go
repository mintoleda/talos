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
	"github.com/mintoleda/talos/internal/mcp"
	"github.com/mintoleda/talos/internal/notify"
	"github.com/mintoleda/talos/internal/safety"
)

//go:embed CORE.md
var corePrompt string

//go:embed permissions/auto.md
var permissionAuto string

//go:embed permissions/ask.md
var permissionAsk string

//go:embed permissions/panic.md
var permissionPanic string

type Config struct {
	BaseURL                   string
	APIKey                    string
	Model                     string
	Provider                  string
	PermissionMode            safety.Mode
	BashTimeout               time.Duration
	BashMaxTimeout            time.Duration
	BashMaxOutput             int
	SystemPrompt              string
	BaseDir                   string
	ThinkingLevel             string
	SearchURL                 string // custom search endpoint (empty = DuckDuckGo HTML)
	MaxAgentDepth             int    // max subagent nesting depth (0 = default 3)
	EnableSubagents           bool
	CompactThreshold          float64 // normal compaction fires at this fraction (0 = default 0.85)
	CompactEmergencyThreshold float64 // emergency: compact regardless of chunk size (0 = default 0.95)
	CompactChunkSize          int     // messages per compaction chunk (0 = default 20)
	SummaryModel              string  // model for compaction summaries (empty = deterministic placeholder)
	Historian                 bool
	HistorianProvider         string
	HistorianModel            string
	HistorianBaseURL          string
	HistorianAPIKey           string
	DreamerProvider           string
	DreamerModel              string
	DreamerBaseURL            string
	DreamerAPIKey             string
	KillBgOnInterrupt         bool
	ServerListen              string        // empty/unix default, or tcp host:port
	ServerToken               string        // auth token for network listeners
	ServerIdleTimeout         time.Duration // multi-session daemon idle exit; 0 = no timeout

	Notifications notify.Config // desktop notification settings

	MCPServers []mcp.ServerConfig // MCP servers from config.toml
}

func Load() (*Config, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		home = "."
	}
	cfg := &Config{
		BaseURL:                   "https://api.deepseek.com",
		Model:                     "deepseek-chat",
		Provider:                  "openai",
		PermissionMode:            safety.ModeAuto,
		BashTimeout:               120 * time.Second,
		BashMaxTimeout:            600 * time.Second,
		BashMaxOutput:             30 * 1024,
		SystemPrompt:              strings.TrimSpace(corePrompt),
		BaseDir:                   filepath.Join(home, ".talos"),
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
	c.APIKey = ResolveKeyFor(c.BaseDir, authName, "")
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
	EnableSubagents       *bool  `toml:"enable_subagents"`
	// Deprecated: use thinking_level. Kept for backward compat.
	ThinkingBudget            int     `toml:"thinking_budget"`
	KillBgOnInterrupt         *bool   `toml:"kill_bg_on_interrupt"`
	CompactThreshold          float64 `toml:"compact_threshold"`
	CompactEmergencyThreshold float64 `toml:"compact_emergency"`
	CompactChunkSize          int     `toml:"compact_chunk_size"`
	SummaryModel              string  `toml:"summary_model"` // empty = deterministic placeholder
	Historian                 *bool   `toml:"historian"`
	HistorianProvider         string  `toml:"historian_provider"`
	HistorianModel            string  `toml:"historian_model"`
	HistorianBaseURL          string  `toml:"historian_base_url"`
	HistorianAPIKey           string  `toml:"historian_api_key"`
	DreamerProvider           string  `toml:"dreamer_provider"`
	DreamerModel              string  `toml:"dreamer_model"`
	DreamerBaseURL            string  `toml:"dreamer_base_url"`
	DreamerAPIKey             string  `toml:"dreamer_api_key"`
	ServerListen      string `toml:"server_listen"`
	ServerToken       string `toml:"server_token"`
	ServerIdleTimeout string `toml:"server_idle_timeout"` // duration string, e.g. "30m"

	Notifications *notifyFileConfig `toml:"notifications"`

	MCPServers []mcp.ServerConfig `toml:"mcp_servers"`
}

type sectionedConfig struct {
	Provider      providerFileConfig   `toml:"provider"`
	Timeout       timeoutFileConfig    `toml:"timeout"`
	Search        searchFileConfig     `toml:"search"`
	Agent         agentFileConfig      `toml:"agent"`
	Compaction    compactionFileConfig `toml:"compaction"`
	Memory        memoryFileConfig     `toml:"memory"`
	Server        serverFileConfig     `toml:"server"`
	Notifications *notifyFileConfig    `toml:"notifications"`
	MCPServers    []mcp.ServerConfig   `toml:"mcp_servers"`
}

type providerFileConfig struct {
	BaseURL        string `toml:"base_url"`
	APIKey         string `toml:"api_key"`
	Model          string `toml:"model"`
	Provider       string `toml:"provider"`
	PermissionMode string `toml:"permission_mode"`
	ThinkingLevel  string `toml:"thinking_level"`
}

type timeoutFileConfig struct {
	BashTimeoutSeconds    int `toml:"bash_timeout_seconds"`
	BashMaxTimeoutSeconds int `toml:"bash_max_timeout_seconds"`
	BashMaxOutput         int `toml:"bash_max_output"`
}

type searchFileConfig struct {
	SearchURL string `toml:"search_url"`
}

type agentFileConfig struct {
	MaxAgentDepth   int   `toml:"max_agent_depth"`
	EnableSubagents *bool `toml:"enable_subagents"`
	// Deprecated: use provider.thinking_level. Kept for backward compat.
	ThinkingBudget int `toml:"thinking_budget"`
}

type compactionFileConfig struct {
	CompactThreshold          float64 `toml:"compact_threshold"`
	CompactEmergencyThreshold float64 `toml:"compact_emergency"`
	CompactChunkSize          int     `toml:"compact_chunk_size"`
	SummaryModel              string  `toml:"summary_model"`
	Historian                 *bool   `toml:"historian"`
	EnableSubagents           *bool   `toml:"enable_subagents"`
}

type memoryFileConfig struct {
	Historian         *bool  `toml:"historian"`
	HistorianProvider string `toml:"historian_provider"`
	HistorianModel    string `toml:"historian_model"`
	HistorianBaseURL  string `toml:"historian_base_url"`
	HistorianAPIKey   string `toml:"historian_api_key"`
	DreamerProvider   string `toml:"dreamer_provider"`
	DreamerModel      string `toml:"dreamer_model"`
	DreamerBaseURL    string `toml:"dreamer_base_url"`
	DreamerAPIKey     string `toml:"dreamer_api_key"`
}

type serverFileConfig struct {
	Listen      string `toml:"listen"`
	Token       string `toml:"token"`
	IdleTimeout string `toml:"idle_timeout"` // duration string, e.g. "30m"
}

// notifyFileConfig mirrors the [notifications] section of config.toml.
type notifyFileConfig struct {
	Enabled            bool `toml:"enabled"`
	NotifyOnPermission bool `toml:"notify_on_permission"`
	NotifyOnTurnEnded  bool `toml:"notify_on_turn_ended"`
	NotifyOnError      bool `toml:"notify_on_error"`
}

func mergeSectionedConfig(fc *fileConfig, sc sectionedConfig) {
	if fc.BaseURL == "" {
		fc.BaseURL = sc.Provider.BaseURL
	}
	if fc.APIKey == "" {
		fc.APIKey = sc.Provider.APIKey
	}
	if fc.Model == "" {
		fc.Model = sc.Provider.Model
	}
	if fc.Provider == "" {
		fc.Provider = sc.Provider.Provider
	}
	if fc.PermissionMode == "" {
		fc.PermissionMode = sc.Provider.PermissionMode
	}
	if fc.ThinkingLevel == "" {
		fc.ThinkingLevel = sc.Provider.ThinkingLevel
	}
	if fc.BashTimeoutSeconds == 0 {
		fc.BashTimeoutSeconds = sc.Timeout.BashTimeoutSeconds
	}
	if fc.BashMaxTimeoutSeconds == 0 {
		fc.BashMaxTimeoutSeconds = sc.Timeout.BashMaxTimeoutSeconds
	}
	if fc.BashMaxOutput == 0 {
		fc.BashMaxOutput = sc.Timeout.BashMaxOutput
	}
	if fc.SearchURL == "" {
		fc.SearchURL = sc.Search.SearchURL
	}
	if fc.MaxAgentDepth == 0 {
		fc.MaxAgentDepth = sc.Agent.MaxAgentDepth
	}
	if fc.EnableSubagents == nil {
		if sc.Agent.EnableSubagents != nil {
			fc.EnableSubagents = sc.Agent.EnableSubagents
		} else {
			fc.EnableSubagents = sc.Compaction.EnableSubagents
		}
	}
	if fc.ThinkingBudget == 0 {
		fc.ThinkingBudget = sc.Agent.ThinkingBudget
	}
	if fc.CompactThreshold == 0 {
		fc.CompactThreshold = sc.Compaction.CompactThreshold
	}
	if fc.CompactEmergencyThreshold == 0 {
		fc.CompactEmergencyThreshold = sc.Compaction.CompactEmergencyThreshold
	}
	if fc.CompactChunkSize == 0 {
		fc.CompactChunkSize = sc.Compaction.CompactChunkSize
	}
	if fc.SummaryModel == "" {
		fc.SummaryModel = sc.Compaction.SummaryModel
	}
	if fc.Historian == nil {
		if sc.Memory.Historian != nil {
			fc.Historian = sc.Memory.Historian
		} else {
			fc.Historian = sc.Compaction.Historian
		}
	}
	if fc.HistorianProvider == "" {
		fc.HistorianProvider = sc.Memory.HistorianProvider
	}
	if fc.HistorianModel == "" {
		fc.HistorianModel = sc.Memory.HistorianModel
	}
	if fc.HistorianBaseURL == "" {
		fc.HistorianBaseURL = sc.Memory.HistorianBaseURL
	}
	if fc.HistorianAPIKey == "" {
		fc.HistorianAPIKey = sc.Memory.HistorianAPIKey
	}
	if fc.DreamerProvider == "" {
		fc.DreamerProvider = sc.Memory.DreamerProvider
	}
	if fc.DreamerModel == "" {
		fc.DreamerModel = sc.Memory.DreamerModel
	}
	if fc.DreamerBaseURL == "" {
		fc.DreamerBaseURL = sc.Memory.DreamerBaseURL
	}
	if fc.DreamerAPIKey == "" {
		fc.DreamerAPIKey = sc.Memory.DreamerAPIKey
	}
	if fc.ServerListen == "" {
		fc.ServerListen = sc.Server.Listen
	}
	if fc.ServerToken == "" {
		fc.ServerToken = sc.Server.Token
	}
	if fc.ServerIdleTimeout == "" {
		fc.ServerIdleTimeout = sc.Server.IdleTimeout
	}
	if fc.Notifications == nil {
		fc.Notifications = sc.Notifications
	}
	if len(fc.MCPServers) == 0 {
		fc.MCPServers = sc.MCPServers
	}
}

func loadFile(path string, cfg *Config) error {
	var fc fileConfig
	_, flatErr := toml.DecodeFile(path, &fc)
	var sc sectionedConfig
	_, sectionedErr := toml.DecodeFile(path, &sc)
	if flatErr != nil && sectionedErr != nil {
		return flatErr
	}
	if sectionedErr == nil {
		mergeSectionedConfig(&fc, sc)
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
	if fc.KillBgOnInterrupt != nil {
		cfg.KillBgOnInterrupt = *fc.KillBgOnInterrupt
	}
	if fc.SearchURL != "" {
		cfg.SearchURL = fc.SearchURL
	}
	if fc.MaxAgentDepth > 0 {
		cfg.MaxAgentDepth = fc.MaxAgentDepth
	}
	if fc.EnableSubagents != nil {
		cfg.EnableSubagents = *fc.EnableSubagents
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
	if fc.Historian != nil {
		cfg.Historian = *fc.Historian
	}
	if fc.HistorianProvider != "" {
		cfg.HistorianProvider = fc.HistorianProvider
	}
	if fc.HistorianModel != "" {
		cfg.HistorianModel = fc.HistorianModel
	}
	if fc.HistorianBaseURL != "" {
		cfg.HistorianBaseURL = fc.HistorianBaseURL
	}
	if fc.HistorianAPIKey != "" {
		cfg.HistorianAPIKey = fc.HistorianAPIKey
	}
	if fc.DreamerProvider != "" {
		cfg.DreamerProvider = fc.DreamerProvider
	}
	if fc.DreamerModel != "" {
		cfg.DreamerModel = fc.DreamerModel
	}
	if fc.DreamerBaseURL != "" {
		cfg.DreamerBaseURL = fc.DreamerBaseURL
	}
	if fc.DreamerAPIKey != "" {
		cfg.DreamerAPIKey = fc.DreamerAPIKey
	}
	if fc.ServerListen != "" {
		cfg.ServerListen = fc.ServerListen
	}
	if fc.ServerToken != "" {
		cfg.ServerToken = fc.ServerToken
	}
	if fc.ServerIdleTimeout != "" {
		d, err := time.ParseDuration(fc.ServerIdleTimeout)
		if err != nil {
			return fmt.Errorf("server idle_timeout: %w", err)
		}
		cfg.ServerIdleTimeout = d
	}
	if fc.Notifications != nil {
		nc := notify.DefaultConfig()
		nc.Enabled = fc.Notifications.Enabled
		nc.NotifyOnPermission = fc.Notifications.NotifyOnPermission
		nc.NotifyOnTurnEnded = fc.Notifications.NotifyOnTurnEnded
		nc.NotifyOnError = fc.Notifications.NotifyOnError
		cfg.Notifications = nc
	}
	if len(fc.MCPServers) > 0 {
		cfg.MCPServers = fc.MCPServers
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

// PermissionDescription returns the prompt text describing the current
// permission mode. The embedded default is used unless the user has created
// ~/.talos/<mode>.md, which takes precedence.
func PermissionDescription(baseDir string, mode safety.Mode) string {
	def := func() string {
		switch mode {
		case safety.ModeAsk:
			return strings.TrimSpace(permissionAsk)
		case safety.ModePanic:
			return strings.TrimSpace(permissionPanic)
		default:
			return strings.TrimSpace(permissionAuto)
		}
	}
	name := func() string {
		switch mode {
		case safety.ModeAsk:
			return "ask"
		case safety.ModePanic:
			return "panic"
		default:
			return "auto"
		}
	}
	path := filepath.Join(baseDir, name()+".md")
	data, err := os.ReadFile(path)
	if err == nil {
		if t := strings.TrimSpace(string(data)); t != "" {
			return t
		}
	}
	return def()
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
