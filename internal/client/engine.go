// Package client defines the client-facing Engine interface that abstracts
// over local (in-process) and remote (server-attach) loop interaction.
//
// Step 2 of the engine seam refactor: introduce the interface and a LocalEngine
// implementation. The TUI still uses Config directly; step 3 switches it over.
package client

import (
	"github.com/mintoleda/talos/internal/models"
	"github.com/mintoleda/talos/internal/protocol"
	"github.com/mintoleda/talos/internal/tui/dialogs"
)

// Engine captures every capability the TUI needs to drive a conversation loop.
// LocalEngine and (future) RemoteEngine both satisfy it.
type Engine interface {
	// Submit sends user content blocks to the engine for processing.
	Submit(blocks []protocol.ContentBlock)
	// Interrupt cancels the currently running turn.
	Interrupt()
	// Approve sends a permission approval (or denial) back to the loop.
	// plan is reserved for future diff/plan display.
	Approve(ok bool, plan []byte)
	// Steer injects a steer message (text typed while the agent was busy)
	// that gets processed before the next LLM call.
	Steer(blocks []protocol.ContentBlock)
	// WithdrawSteer removes the most recently queued steer message, if any.
	WithdrawSteer() []protocol.ContentBlock
	// PendingSteers reports how many steer messages are queued.
	PendingSteers() int

	// NewSession starts a fresh conversation and returns its ID.
	NewSession() (id string, err error)
	// Resume loads an existing session by ID, returning the new session ID
	// and the frozen message history for replay.
	Resume(id string) (newID string, history []protocol.FrozenMessage, err error)
	// ListSessions returns all sessions for the current project.
	ListSessions() ([]dialogs.SessionEntry, error)
	// DeleteSession removes a session by ID.
	DeleteSession(id string) error
	// ListModels fetches available models across all logged-in providers.
	ListModels() ([]models.Entry, error)
	// SwitchModel creates a new provider client and swaps it into the loop.
	SwitchModel(provider, model string) error
	// CycleThinking advances to the next abstract thinking level and returns it.
	CycleThinking() (level string, err error)
	// CurrentThinkingLevel returns the current thinking level without cycling.
	CurrentThinkingLevel() string
	// CyclePermissionMode advances to the next permission mode (auto→ask→panic→auto)
	// and returns the new mode name.
	CyclePermissionMode() (mode string, err error)
	// PermissionMode returns the current permission mode name without cycling.
	PermissionMode() string
	// TogglePanic toggles panic mode on/off. When toggled on, the current mode is
	// saved and panic is engaged. When toggled off, the saved mode is restored.
	TogglePanic() (mode string, err error)
	// Compact triggers manual compaction, optionally guided by a focus string.
	Compact(focus string) error
	// Stats returns cumulative token and cost counters.
	Stats() (input, output, cacheMiss int, cost float64, err error)
	// LoginProviders returns the list of known providers with login status.
	LoginProviders() ([]dialogs.LoginProvider, error)
	// Login persists an API key for the given provider.
	Login(provider, key string) error
	// MCPStatus returns a human-readable summary of MCP server connections.
	MCPStatus() (string, error)
	// MCPCount returns the number of connected MCP servers.
	MCPCount() int
	// CancelSubagent cancels a running subagent by ID.
	CancelSubagent(id string)
	// History returns the current transcript backlog for newly attached clients.
	History() ([]protocol.FrozenMessage, error)
	// ListFiles returns file-picker candidates for @ completion.
	ListFiles(prefix string) ([]string, error)
	// ResolveInput turns user text into content blocks on the engine machine.
	ResolveInput(text string) ([]protocol.ContentBlock, string, error)
	// PushInstruction builds the /push agent instruction on the engine machine.
	PushInstruction() (msg string, notice string, err error)

	// Events returns a read-only channel of protocol events emitted by the
	// engine (text deltas, tool calls, notices, turn-end, etc.).
	Events() <-chan protocol.Event

	// Close shuts down the engine goroutines and releases resources.
	Close()
}
