package executor

import (
	"context"
	"fmt"
	"sync"

	"github.com/mintoleda/talos/internal/protocol"
	"github.com/mintoleda/talos/internal/safety"
	"github.com/mintoleda/talos/internal/tools"
)

// Executor runs tools against a registry, respecting the safety policy.
type Executor interface {
	Run(ctx context.Context, tu protocol.ToolUse, emit protocol.EmitFunc) protocol.ToolResult
	Close()
}

type InProcExecutor struct {
	registry *tools.Registry
	policy   *safety.Policy
	permMu   sync.Mutex // serializes interactive permission prompts
}

func New(registry *tools.Registry, policy *safety.Policy) *InProcExecutor {
	return &InProcExecutor{registry: registry, policy: policy}
}

func (e *InProcExecutor) Close() {
	if e.registry != nil {
		e.registry.Close()
	}
}

func (e *InProcExecutor) Run(ctx context.Context, tu protocol.ToolUse, emit protocol.EmitFunc) protocol.ToolResult {
	switch d, reason := e.policy.Check(tu); d {
	case safety.Block:
		return protocol.ToolResult{ToolUseID: tu.ID, IsError: true, Content: "blocked: " + reason}
	case safety.Prompt:
		// Serialize permission prompts so parallel tools don't race on the
		// single frontend dialog / headless stdin path.
		e.permMu.Lock()
		defer e.permMu.Unlock()

		reply := make(chan bool, 1)
		ev := protocol.PermissionRequested{
			ToolName: tu.Name,
			Reason:   reason,
			ReplyCh:  reply,
		}
		if tu.Name == "bash" {
			if cmd, ok := tu.Args["command"].(string); ok {
				ev.Command = cmd
			}
		}
		if emit != nil {
			emit(ev)
		}
		select {
		case allowed := <-reply:
			if !allowed {
				return protocol.ToolResult{ToolUseID: tu.ID, IsError: true, Content: "denied by user"}
			}
		case <-ctx.Done():
			return protocol.ToolResult{ToolUseID: tu.ID, IsError: true, Content: "cancelled: " + ctx.Err().Error()}
		default:
			// No one sent a reply and context not done yet. In headless mode
			// without a renderer this would deadlock, so fail closed.
			return protocol.ToolResult{ToolUseID: tu.ID, IsError: true, Content: "denied by user"}
		}
	}

	tool, ok := e.registry.Get(tu.Name)
	if !ok {
		return protocol.ToolResult{ToolUseID: tu.ID, IsError: true, Content: "unknown tool: " + tu.Name}
	}
	// Tools that surface live activity (e.g. subagent spawn tools) receive the
	// turn's emit function so their child events reach the frontend.
	var (
		res protocol.ToolResult
		err error
	)
	if et, ok := tool.(tools.EmittingTool); ok {
		res, err = et.ExecuteWithEmit(ctx, tu.Args, emit)
	} else {
		res, err = tool.Execute(ctx, tu.Args)
	}
	if err != nil {
		return protocol.ToolResult{ToolUseID: tu.ID, IsError: true, Content: err.Error()}
	}
	res.ToolUseID = tu.ID
	return res
}

// InlineConfirm returns a PermissionRequested handler for tests and headless
// callers that cannot render events. It prompts on stdin directly and sends
// the answer to the event's ReplyCh.
func InlineConfirm(ev protocol.PermissionRequested) {
	fmt.Printf("\n⚠ %s requires approval: %s\nAllow? [y/N] ", ev.ToolName, ev.Reason)
	var ans string
	fmt.Scanln(&ans)
	if ev.ReplyCh != nil {
		ev.ReplyCh <- ans == "y" || ans == "Y"
	}
}
