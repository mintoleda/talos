package rpc

import (
	"time"

	"github.com/mintoleda/talos/internal/models"
	"github.com/mintoleda/talos/internal/protocol"
	"github.com/mintoleda/talos/internal/tui/dialogs"
)

const (
	NewSession          = "engine.newSession"
	Resume              = "engine.resume"
	ListSessions        = "engine.listSessions"
	DeleteSession       = "engine.deleteSession"
	ListModels          = "engine.listModels"
	SwitchModel         = "engine.switchModel"
	CycleThinking       = "engine.cycleThinking"
	CurrentThinking     = "engine.currentThinking"
	CyclePermissionMode = "engine.cyclePermissionMode"
	PermissionMode      = "engine.permissionMode"
	TogglePanic         = "engine.togglePanic"
	WithdrawSteer       = "engine.withdrawSteer"
	Compact             = "engine.compact"
	Stats               = "engine.stats"
	LoginProviders      = "engine.loginProviders"
	Login               = "engine.login"
	MCPStatus           = "engine.mcpStatus"
	MCPCount            = "engine.mcpCount"
	CancelSubagent      = "engine.cancelSubagent"
	History             = "engine.history"
	ListFiles           = "engine.listFiles"
	ResolveInput        = "engine.resolveInput"
	PushInstruction     = "engine.pushInstruction"
	ListCommands        = "engine.listCommands"
	SetPermissionMode   = "engine.setPermissionMode"
	ListBg              = "engine.listBg"
	KillBg              = "engine.killBg"
	BgLog               = "engine.bgLog"
	DismissBg           = "engine.dismissBg"

	DaemonCreateSession = "daemon.createSession"
	DaemonListSessions  = "daemon.listSessions"
	DaemonStopSession   = "daemon.stopSession"
	DaemonDeleteSession = "daemon.deleteSession"
	DaemonStatus        = "daemon.status"
	DaemonGCWorktrees   = "daemon.gcWorktrees"
	DaemonProbeDir      = "daemon.probeDir"
)

// SessionInfo describes a live or persisted-resumable session for the
// multi-session daemon sidebar and session picker.
type SessionInfo struct {
	ID         string    `json:"id"`
	Dir        string    `json:"dir"`         // effective cwd (worktree path if isolated)
	ProjectDir string    `json:"project_dir"` // origin repo root
	Isolation  string    `json:"isolation"`
	Branch     string    `json:"branch,omitempty"`
	Ahead      int       `json:"ahead,omitempty"`
	Dirty      bool      `json:"dirty,omitempty"`
	State      string    `json:"state"` // "idle"|"busy"|"awaiting_approval"|"unloaded"
	Live       bool      `json:"live"`  // engine loaded in daemon
	Provider   string    `json:"provider"`
	Model      string    `json:"model"`
	Preview    string    `json:"preview"` // first-user-message snippet
	CreatedAt  time.Time `json:"created_at"`
	LastActive time.Time `json:"last_active"`
}

type CreateSessionParams struct {
	Dir       string `json:"dir"`                 // project dir (absolute)
	Isolation string `json:"isolation,omitempty"` // "worktree" (default) | "none"
	Resume    string `json:"resume,omitempty"`    // session ID to load instead of fresh
	Provider  string `json:"provider,omitempty"`
	Model     string `json:"model,omitempty"`
}

type CreateSessionResult struct {
	Session SessionInfo `json:"session"`
}

type ListSessionsResult struct {
	Sessions []SessionInfo `json:"sessions"` // live + persisted-resumable
}

type StopSessionParams struct {
	ID string `json:"id"`
}

type DeleteSessionDaemonParams struct {
	ID string `json:"id"`
}

type DaemonStatusResult struct {
	Version         string   `json:"version"`
	Uptime          int64    `json:"uptime_seconds"`
	Sessions        int      `json:"sessions"`
	OrphanWorktrees []string `json:"orphan_worktrees,omitempty"`
}

type GCWorktreesResult struct {
	Removed []string `json:"removed"`
}

type ProbeDirParams struct {
	Dir string `json:"dir"`
}

type ProbeDirResult struct {
	IsRepo     bool   `json:"is_repo"`
	ProjectDir string `json:"project_dir"`
}

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

// CommandDesc describes a slash command for the composer palette.
type CommandDesc struct {
	Name    string `json:"name"`
	Summary string `json:"summary"`
	Args    string `json:"args,omitempty"`
}

type ListCommandsResult struct {
	Commands []CommandDesc `json:"commands"`
}

type SetPermissionModeParams struct {
	Mode string `json:"mode"`
}

type BgProcInfo struct {
	ID        string    `json:"id"`
	Command   string    `json:"command"`
	Dir       string    `json:"dir"`
	Running   bool      `json:"running"`
	ExitCode  int       `json:"exit_code"`
	StartedAt time.Time `json:"started_at"`
}

type ListBgResult struct {
	Procs []BgProcInfo `json:"procs"`
}

type KillBgParams struct {
	ID string `json:"id"`
}

type DismissBgParams struct {
	ID string `json:"id"`
}

type BgLogParams struct {
	ID        string `json:"id"`
	TailBytes int    `json:"tail_bytes"`
}

type BgLogResult struct {
	Text string `json:"text"`
}
