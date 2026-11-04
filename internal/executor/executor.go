package executor

import (
	"context"
	"fmt"
	"path/filepath"
	"sync"

	"github.com/mintoleda/talos/internal/protocol"
	"github.com/mintoleda/talos/internal/safety"
	"github.com/mintoleda/talos/internal/tools"
)

type Executor interface {
	Run(ctx context.Context, tu protocol.ToolUse, emit protocol.EmitFunc) protocol.ToolResult
	Close()
	KillBg()
	Policy() *safety.Policy
}

type InProcExecutor struct {
	registry *tools.Registry
	policy   *safety.Policy
	permMu   sync.Mutex // serializes interactive permission prompts
}

func New(registry *tools.Registry, policy *safety.Policy) *InProcExecutor {
	return &InProcExecutor{registry: registry, policy: policy}
}

func (e *InProcExecutor) Policy() *safety.Policy {
	return e.policy
}

func (e *InProcExecutor) KillBg() {
	if e.registry != nil {
		e.registry.KillBg()
	}
}

func (e *InProcExecutor) Close() {
	if e.registry != nil {
		e.registry.Close()
	}
}

func (e *InProcExecutor) Run(ctx context.Context, tu protocol.ToolUse, emit protocol.EmitFunc) protocol.ToolResult {
	tu = resolvePaths(tu, e.policy.WorkDir())
	switch d, reason := e.policy.Check(tu); d {
	case safety.Block:
		return protocol.ToolResult{ToolUseID: tu.ID, IsError: true, Content: "blocked: " + reason}
	case safety.Prompt:
		if denied, ok := e.approve(ctx, tu, reason, emit); !ok {
			return denied
		}
	}

	tool, ok := e.registry.Get(tu.Name)
	if !ok {
		return protocol.ToolResult{ToolUseID: tu.ID, IsError: true, Content: "unknown tool: " + tu.Name}
	}
	// Wrap emit to inject the tool-use ID into any ToolOutputDelta events
	// the tool emits. This keeps tools agnostic of their own execution ID.
	var toolEmit protocol.EmitFunc
	if emit != nil {
		toolEmit = func(ev protocol.Event) {
			if delta, ok := ev.(protocol.ToolOutputDelta); ok {
				delta.ID = tu.ID
			}
			emit(ev)
		}
	}
	// Tools that surface live activity (e.g. subagent spawn tools) receive the
	// turn's emit function so their child events reach the frontend.
	var (
		res protocol.ToolResult
		err error
	)
	if st, ok := tool.(tools.StreamingTool); ok {
		res, err = st.ExecuteStreaming(ctx, tu.Args, toolEmit)
	} else if et, ok := tool.(tools.EmittingTool); ok {
		res, err = et.ExecuteWithEmit(ctx, tu.Args, toolEmit)
	} else {
		res, err = tool.Execute(ctx, tu.Args)
	}
	if err != nil {
		return protocol.ToolResult{ToolUseID: tu.ID, IsError: true, Content: err.Error()}
	}
	res.ToolUseID = tu.ID
	res.Content = tools.CapResultContent(res.Content, tools.MaxResultBytes)
	return res
}

func InlineConfirm(ev protocol.PermissionRequested) {
	fmt.Printf("\n⚠ %s requires approval: %s\nAllow? [y/N] ", ev.ToolName, ev.Reason)
	var ans string
	fmt.Scanln(&ans)
	if ev.ReplyCh != nil {
		ev.ReplyCh <- ans == "y" || ans == "Y"
	}
}

// pathTools take a filesystem "path" argument but no working directory of
// their own.
var pathTools = map[string]bool{"read": true, "write": true, "edit": true, "ls": true}

// resolvePaths anchors a relative "path" argument at the session's working
// directory. Without this those tools resolve against the process cwd, which
// differs from the session cwd under ACP and the multi-session daemon. Args
// are copied: the caller's map is shared with the transcript.
func resolvePaths(tu protocol.ToolUse, dir string) protocol.ToolUse {
	if dir == "" || !pathTools[tu.Name] {
		return tu
	}
	p, ok := tu.Args["path"].(string)
	if !ok || p == "" || filepath.IsAbs(p) {
		return tu
	}
	args := make(map[string]any, len(tu.Args))
	for k, v := range tu.Args {
		args[k] = v
	}
	args["path"] = filepath.Join(dir, p)
	tu.Args = args
	return tu
}

// approve asks the frontend to confirm tu. ok is false when the call must not
// run, with the error result to return.
func (e *InProcExecutor) approve(ctx context.Context, tu protocol.ToolUse, reason string, emit protocol.EmitFunc) (denied protocol.ToolResult, ok bool) {
	// Serialize permission prompts so parallel tools don't race on the single
	// frontend dialog / headless stdin path. Held only while asking, not while
	// the approved tool runs.
	e.permMu.Lock()
	defer e.permMu.Unlock()

	if emit == nil {
		// No frontend can answer the prompt; fail closed instead of waiting
		// forever.
		return protocol.ToolResult{ToolUseID: tu.ID, IsError: true, Content: "denied: no frontend to approve " + tu.Name}, false
	}
	reply := make(chan bool, 1)
	ev := protocol.PermissionRequested{
		ToolUseID: tu.ID,
		ToolName:  tu.Name,
		Args:      tu.Args,
		Reason:    reason,
		ReplyCh:   reply,
	}
	if tu.Name == "bash" || tu.Name == "bash_background" {
		if cmd, ok := tu.Args["command"].(string); ok {
			ev.Command = cmd
		}
	}
	emit(ev)
	// Frontends answer asynchronously (TUI dialog, daemon client, ACP
	// session/request_permission), so wait for the reply or cancellation.
	select {
	case allowed := <-reply:
		if !allowed {
			return protocol.ToolResult{ToolUseID: tu.ID, IsError: true, Content: "denied by user"}, false
		}
		return protocol.ToolResult{}, true
	case <-ctx.Done():
		return protocol.ToolResult{ToolUseID: tu.ID, IsError: true, Content: "cancelled: " + ctx.Err().Error()}, false
	}
}
