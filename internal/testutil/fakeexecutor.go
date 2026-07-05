package testutil

import (
	"context"

	"github.com/mintoleda/talos/internal/protocol"
)

// FakeExecutor implements executor.Executor for testing. It records every tool
// call and returns a fixed success result.
type FakeExecutor struct {
	Calls []protocol.ToolUse
}

func (e *FakeExecutor) Run(ctx context.Context, tu protocol.ToolUse, _ protocol.EmitFunc) protocol.ToolResult {
	e.Calls = append(e.Calls, tu)
	return protocol.ToolResult{ToolUseID: tu.ID, Content: "ok"}
}

func (e *FakeExecutor) KillBg() {}

func (e *FakeExecutor) Close() {}
