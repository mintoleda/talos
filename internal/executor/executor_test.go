package executor

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/mintoleda/talos/internal/protocol"
	"github.com/mintoleda/talos/internal/safety"
	"github.com/mintoleda/talos/internal/tools"
)

// fakeEmitting is a tool that implements tools.EmittingTool. It records whether
// it received an emit function and uses it.
type fakeEmitting struct{ gotEmit bool }

func (f *fakeEmitting) Name() string            { return "fake" }
func (f *fakeEmitting) Description() string     { return "test" }
func (f *fakeEmitting) Schema() json.RawMessage { return tools.EmptySchema() }

func (f *fakeEmitting) Execute(ctx context.Context, args map[string]any) (protocol.ToolResult, error) {
	return protocol.ToolResult{Content: "plain"}, nil
}

func (f *fakeEmitting) ExecuteWithEmit(ctx context.Context, args map[string]any, emit protocol.EmitFunc) (protocol.ToolResult, error) {
	if emit != nil {
		f.gotEmit = true
		emit(protocol.Notice{Level: "info", Text: "from-subtool"})
	}
	return protocol.ToolResult{Content: "emitting"}, nil
}

func TestExecutorRoutesToEmittingTool(t *testing.T) {
	reg := tools.EmptyRegistry()
	ft := &fakeEmitting{}
	reg.Add(ft)
	pol := safety.NewPolicy(safety.ModeAuto, ".", safety.NewClassifier(), true)
	e := New(reg, pol)

	var got []protocol.Event
	emit := func(ev protocol.Event) { got = append(got, ev) }

	res := e.Run(context.Background(), protocol.ToolUse{ID: "1", Name: "fake"}, emit)
	if res.Content != "emitting" {
		t.Errorf("content = %q, want emitting (ExecuteWithEmit path)", res.Content)
	}
	if !ft.gotEmit {
		t.Error("emit was not passed through to the emitting tool")
	}
	if len(got) != 1 {
		t.Fatalf("want 1 forwarded event, got %d", len(got))
	}
	if n, ok := got[0].(protocol.Notice); !ok || n.Text != "from-subtool" {
		t.Errorf("forwarded event = %+v", got[0])
	}
}
