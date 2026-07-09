package engine

import (
	"fmt"
	"strings"

	"github.com/mintoleda/talos/internal/client/rpc"
	"github.com/mintoleda/talos/internal/config"
	"github.com/mintoleda/talos/internal/protocol"
	"github.com/mintoleda/talos/internal/provider"
	"github.com/mintoleda/talos/internal/safety"
)

// commands is the single source of truth for slash dispatch and listCommands.
var commands = []rpc.CommandDesc{
	{Name: "/model", Summary: "Switch provider/model or list available models", Args: "[provider/model]"},
	{Name: "/thinking", Summary: "Cycle thinking level"},
	{Name: "/permission", Summary: "Cycle permission mode (auto/ask)"},
	{Name: "/panic", Summary: "Toggle panic mode (blocks all tools)"},
	{Name: "/mcp", Summary: "List connected MCP servers and their tools"},
	{Name: "/subagents", Summary: "Toggle subagent delegation on/off"},
	{Name: "/compact", Summary: "Compact conversation history", Args: "[focus]"},
}

// Commands returns the daemon-owned slash command inventory.
func Commands() []rpc.CommandDesc {
	out := make([]rpc.CommandDesc, len(commands))
	copy(out, commands)
	return out
}

// ListCommands is the engine RPC entry for the composer palette.
func (e *Engine) ListCommands() []rpc.CommandDesc {
	return Commands()
}

// handleSlash processes slash commands for SubmitText (daemon path).
// Returns a notice string; may also Emit ModelChanged / PermissionModeChanged.
func (e *Engine) handleSlash(cmd string) string {
	parts := strings.Fields(cmd)
	if len(parts) == 0 {
		return "empty command"
	}
	switch parts[0] {
	case "/thinking":
		old := e.pb.ThinkingLevel()
		caps := provider.SupportedLevels(e.cfg.Model)
		cur := old
		if cur == "" {
			cur = caps[0]
		}
		for i, l := range caps {
			if l == cur {
				next := caps[(i+1)%len(caps)]
				e.pb.SetThinkingLevel(next)
				_ = config.SaveThinkingLevel(e.baseDir, next)
				e.Emit(protocol.ModelChanged{
					Provider:      e.cfg.Provider,
					Model:         e.cfg.Model,
					ThinkingLevel: next,
				})
				return fmt.Sprintf("thinking level: %s → %s", cur, next)
			}
		}
		return fmt.Sprintf("thinking level: %s", cur)
	case "/model":
		if len(parts) >= 2 {
			arg := parts[1]
			split := strings.SplitN(arg, "/", 2)
			var pName, pModel string
			if len(split) == 2 {
				pName = split[0]
				pModel = split[1]
			} else {
				pModel = arg
				pName = e.cfg.Provider
			}
			if err := e.SwitchModel(pName, pModel); err != nil {
				return fmt.Sprintf("switch model: %v", err)
			}
			e.Emit(protocol.ModelChanged{
				Provider:      e.cfg.Provider,
				Model:         e.cfg.Model,
				ThinkingLevel: e.pb.ThinkingLevel(),
			})
			return fmt.Sprintf("switched to %s/%s", pName, pModel)
		}
		entries, err := e.ListModels()
		if err != nil {
			return fmt.Sprintf("fetch models: %v", err)
		}
		if len(entries) == 0 {
			return "no models available"
		}
		var b strings.Builder
		b.WriteString("Available models:")
		for _, entry := range entries {
			fmt.Fprintf(&b, "\n  %s/%s", entry.Provider, entry.ID)
		}
		b.WriteString("\n\nUse /model <provider/model> to switch.")
		return b.String()
	case "/mcp":
		status, _ := e.MCPStatus()
		return status
	case "/subagents":
		return e.ToggleSubagents()
	case "/permission":
		if e.pol == nil {
			return "permission policy not configured"
		}
		cur := e.pol.Mode()
		next := safety.NextMode(cur)
		e.pol.SetMode(next)
		modeStr := next.String()
		e.Emit(protocol.PermissionModeChanged{Mode: modeStr})
		return fmt.Sprintf("permission mode: %s → %s", cur, modeStr)
	case "/panic":
		if e.pol == nil {
			return "permission policy not configured"
		}
		mode := e.pol.TogglePanic()
		modeStr := mode.String()
		e.Emit(protocol.PermissionModeChanged{Mode: modeStr})
		if modeStr == "panic" {
			return "🔴 panic mode ON"
		}
		return "panic mode OFF — restored " + modeStr
	case "/compact":
		focus := ""
		if len(parts) >= 2 {
			focus = strings.Join(parts[1:], " ")
		}
		if err := e.Compact(focus); err != nil {
			return fmt.Sprintf("compact: %v", err)
		}
		return "compacting…"
	default:
		return fmt.Sprintf("unknown command: %s", parts[0])
	}
}
