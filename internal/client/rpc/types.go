package rpc

import (
	"github.com/mintoleda/talos/internal/models"
	"github.com/mintoleda/talos/internal/protocol"
	"github.com/mintoleda/talos/internal/tui/dialogs"
)

const (
	NewSession           = "engine.newSession"
	Resume               = "engine.resume"
	ListSessions         = "engine.listSessions"
	DeleteSession        = "engine.deleteSession"
	ListModels           = "engine.listModels"
	SwitchModel          = "engine.switchModel"
	CycleThinking        = "engine.cycleThinking"
	CurrentThinking      = "engine.currentThinking"
	CyclePermissionMode  = "engine.cyclePermissionMode"
	PermissionMode       = "engine.permissionMode"
	TogglePanic          = "engine.togglePanic"
	WithdrawSteer        = "engine.withdrawSteer"
	Compact              = "engine.compact"
	Stats                = "engine.stats"
	LoginProviders       = "engine.loginProviders"
	Login                = "engine.login"
	MCPStatus            = "engine.mcpStatus"
	MCPCount             = "engine.mcpCount"
	CancelSubagent       = "engine.cancelSubagent"
	History              = "engine.history"
	ListFiles            = "engine.listFiles"
	ResolveInput         = "engine.resolveInput"
	PushInstruction      = "engine.pushInstruction"
)

type ResumeParams struct {
	ID string `json:"id"`
}

type ResumeResult struct {
	ID      string                   `json:"id"`
	History []protocol.FrozenMessage `json:"history"`
}

type DeleteSessionParams struct {
	ID string `json:"id"`
}

type SwitchModelParams struct {
	Provider string `json:"provider"`
	Model    string `json:"model"`
}

type LevelResult struct {
	Level string `json:"level"`
}

type BlocksResult struct {
	Blocks []protocol.ContentBlock `json:"blocks"`
}

type CompactParams struct {
	Focus string `json:"focus"`
}

type StatsResult struct {
	Input     int     `json:"input"`
	Output    int     `json:"output"`
	CacheMiss int     `json:"cache_miss"`
	Cost      float64 `json:"cost"`
}

type LoginParams struct {
	Provider string `json:"provider"`
	Key      string `json:"key"`
}

type StatusResult struct {
	Status string `json:"status"`
}

type CountResult struct {
	Count int `json:"count"`
}

type CancelSubagentParams struct {
	ID string `json:"id"`
}

type SessionsResult struct {
	Sessions []dialogs.SessionEntry `json:"sessions"`
}

type ModelsResult struct {
	Models []models.Entry `json:"models"`
}

type LoginProvidersResult struct {
	Providers []dialogs.LoginProvider `json:"providers"`
}

type HistoryResult struct {
	History []protocol.FrozenMessage `json:"history"`
}

type ListFilesParams struct {
	Prefix string `json:"prefix"`
}

type ListFilesResult struct {
	Files []string `json:"files"`
}

type ResolveInputParams struct {
	Text string `json:"text"`
}

type ResolveInputResult struct {
	Blocks  []protocol.ContentBlock `json:"blocks"`
	Display string                  `json:"display"`
}

type PushInstructionResult struct {
	Message string `json:"message"`
	Notice  string `json:"notice"`
}
