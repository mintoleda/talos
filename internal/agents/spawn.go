package agents

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/mintoleda/talos/internal/protocol"
)

// spawnTool is the per-agent delegation tool. Its name is the agent's name, so
// the calling model sees `scout`, `researcher`, `worker`, … as distinct tools.
// Which spawn tools land in an agent's registry *is* the "who may call whom"
// rule — an agent literally cannot name a tool it was not given.
type spawnTool struct {
	builder *Builder
	def     Definition
	depth   int // depth of the agent this tool spawns
}

func (s *spawnTool) Name() string        { return s.def.Name }
func (s *spawnTool) Description() string { return s.def.Description }

func (s *spawnTool) Schema() json.RawMessage {
	return json.RawMessage(`{
  "type": "object",
  "properties": {
    "task": {
      "type": "string",
      "description": "A self-contained instruction for the subagent. It does not see your conversation, so include everything it needs."
    },
    "context": {
      "type": "string",
      "description": "Optional extra background (file paths, constraints, prior findings) to prepend to the task."
    }
  },
  "required": ["task"]
}`)
}

func (s *spawnTool) Execute(ctx context.Context, args map[string]any) (protocol.ToolResult, error) {
	return s.ExecuteWithEmit(ctx, args, nil)
}

// ExecuteWithEmit runs the subagent to completion in an isolated loop, forwarding
// its activity as Subagent* events, and returns only its final message — the one
// thing the calling agent's context absorbs.
func (s *spawnTool) ExecuteWithEmit(ctx context.Context, args map[string]any, emit protocol.EmitFunc) (protocol.ToolResult, error) {
	task, _ := args["task"].(string)
	task = strings.TrimSpace(task)
	if task == "" {
		return protocol.ToolResult{IsError: true, Content: "missing required arg \"task\""}, nil
	}
	if s.depth >= s.builder.maxDepth {
		return protocol.ToolResult{
			IsError: true,
			Content: fmt.Sprintf("subagent nesting limit (%d) reached; cannot spawn %q", s.builder.maxDepth, s.def.Name),
		}, nil
	}

	id := newID()
	agent := s.def.Name

	// Each subagent gets its own cancellable context so it can be killed
	// individually without cancelling the primary turn.
	subCtx, subCancel := context.WithCancel(ctx)
	s.builder.registerCancel(id, subCancel)
	defer func() {
		subCancel()
		s.builder.removeCancel(id)
	}()

	if emit != nil {
		emit(protocol.SubagentStarted{ID: id, Agent: agent, Task: task})
	}

	// Wrap the subagent's events so they route to its own view. Approval gates
	// pass through unwrapped so the existing dialog answers them.
	nested := func(e protocol.Event) {
		if emit == nil {
			return
		}
		switch e.(type) {
		case protocol.PermissionRequested:
			emit(e)
		default:
			emit(protocol.SubagentEvent{ID: id, Agent: agent, Inner: e})
		}
	}

	bl := s.builder.build(s.def, s.depth)
	defer bl.cleanup()

	input := task
	if extra, _ := args["context"].(string); strings.TrimSpace(extra) != "" {
		input = "Context:\n" + strings.TrimSpace(extra) + "\n\nTask:\n" + task
	}

	runErr := bl.lp.RunTurn(subCtx, protocol.TextBlocks(input), nested)
	result, isErr := finalMessage(bl.tx.Frozen(), runErr)
	usage := s.builder.usage(bl)

	if emit != nil {
		emit(protocol.SubagentFinished{ID: id, Agent: agent, Result: result, IsError: isErr, Usage: usage})
	}
	return protocol.ToolResult{Content: result, IsError: isErr}, nil
}

// finalMessage extracts the subagent's last assistant text — its report to the
// caller. A run error still returns any text produced, flagged as an error.
func finalMessage(frozen []protocol.FrozenMessage, runErr error) (string, bool) {
	for i := len(frozen) - 1; i >= 0; i-- {
		m := frozen[i].Msg
		if m.Role != protocol.RoleAssistant {
			continue
		}
		var parts []string
		for _, blk := range m.Content {
			if blk.Type == protocol.BlockText && strings.TrimSpace(blk.Text) != "" {
				parts = append(parts, blk.Text)
			}
		}
		if len(parts) > 0 {
			text := strings.Join(parts, "\n")
			return text, runErr != nil && !errors.Is(runErr, context.Canceled)
		}
	}
	if runErr != nil {
		return "subagent failed: " + runErr.Error(), true
	}
	return "(subagent produced no final message)", false
}
