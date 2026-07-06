package testutil

import (
	"context"

	"github.com/mintoleda/talos/internal/executor"
	"github.com/mintoleda/talos/internal/protocol"
	"github.com/mintoleda/talos/internal/safety"
)

// Ensure FakeExecutor satisfies executor.Executor.
var _ executor.Executor = (*FakeExecutor)(nil)

// FakeExecutor implements executor.Executor for testing. It records every tool
// call and returns a fixed success result.
type FakeExecutor struct {
	Calls []protocol.ToolUse
}

func (e *FakeExecutor) Run(ctx context.Context, tu protocol.ToolUse, _ protocol.EmitFunc) protocol.ToolResult {
	e.Calls = append(e.Calls, tu)
	return protocol.ToolResult{ToolUseID: tu.ID, Content: "ok"}
}

func (e *FakeExecutor) Policy() *safety.Policy { return nil }

func (e *FakeExecutor) KillBg() {}

func (e *FakeExecutor) Close() {}
