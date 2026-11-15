package executor

import (
	"context"
	"encoding/json"
	"os/exec"
	"testing"

	"github.com/mintoleda/talos/internal/protocol"
	"github.com/mintoleda/talos/internal/safety"
	"github.com/mintoleda/talos/internal/tools"
)

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

type fakeBash struct{ ran bool }

func (f *fakeBash) Name() string            { return "bash" }
func (f *fakeBash) Description() string     { return "test" }
func (f *fakeBash) Schema() json.RawMessage { return tools.EmptySchema() }
func (f *fakeBash) Execute(ctx context.Context, args map[string]any) (protocol.ToolResult, error) {
	f.ran = true
	return protocol.ToolResult{Content: "ran"}, nil
}

func TestCodeModeNestedCallsKeepExecutorPolicy(t *testing.T) {
	if _, err := exec.LookPath("deno"); err != nil {
		t.Skip("Deno is not installed")
	}

	reg := tools.EmptyRegistry()
	fb := &fakeBash{}
	reg.Add(fb)
	reg.Add(tools.NewCodeMode(reg))
	e := New(reg, safety.NewPolicy(safety.ModeAsk, ".", safety.NewClassifier(), true))

	var events []protocol.Event
	res := e.Run(context.Background(), protocol.ToolUse{
		ID:   "outer",
		Name: "codemode",
		Args: map[string]any{"code": `const results = await Promise.allSettled([tools.bash({command: "sudo true"})]); return results[0].reason.message;`},
	}, func(ev protocol.Event) {
		events = append(events, ev)
		if request, ok := ev.(protocol.PermissionRequested); ok {
			request.ReplyCh <- false
		}
	})
	if res.IsError {
		t.Fatalf("codemode returned error: %s", res.Content)
	}
	if res.Content != "denied by user" {
		t.Fatalf("nested tool result = %q", res.Content)
	}
	if fb.ran {
		t.Fatal("nested bash call bypassed the executor policy")
	}
	nestedID := ""
	finished := false
	for _, ev := range events {
		switch event := ev.(type) {
		case protocol.ToolStarted:
			if event.Name == "bash" {
				nestedID = event.ID
			}
		case protocol.ToolFinished:
			if nestedID != "" && event.ID == nestedID && event.Result.ToolUseID == nestedID {
				finished = true
			}
		}
	}
	if nestedID == "" || !finished {
		t.Fatalf("nested tool events missing: started=%v finished=%v", nestedID != "", finished)
	}
}

// Frontends (TUI dialog, daemon clients, ACP) answer permission prompts
// asynchronously; the executor must wait for the reply rather than deny.
func TestExecutorWaitsForAsyncApproval(t *testing.T) {
	for _, approve := range []bool{true, false} {
		reg := tools.EmptyRegistry()
		fb := &fakeBash{}
		reg.Add(fb)
		pol := safety.NewPolicy(safety.ModeAsk, ".", safety.NewClassifier(), true)
		e := New(reg, pol)

		var req protocol.PermissionRequested
		emit := func(ev protocol.Event) {
			if pr, ok := ev.(protocol.PermissionRequested); ok {
				req = pr
				go func() { pr.ReplyCh <- approve }()
			}
		}
		tu := protocol.ToolUse{ID: "t1", Name: "bash", Args: map[string]any{"command": "sudo true"}}
		res := e.Run(context.Background(), tu, emit)
		if req.ToolUseID != "t1" || req.Command != "sudo true" {
			t.Fatalf("request = %+v", req)
		}
		if fb.ran != approve || res.IsError == approve {
			t.Errorf("approve=%v: ran=%v res=%+v", approve, fb.ran, res)
		}
	}
}

func TestExecutorPromptCancelled(t *testing.T) {
	reg := tools.EmptyRegistry()
	reg.Add(&fakeBash{})
	e := New(reg, safety.NewPolicy(safety.ModeAsk, ".", safety.NewClassifier(), true))
	ctx, cancel := context.WithCancel(context.Background())
	emit := func(ev protocol.Event) {
		if _, ok := ev.(protocol.PermissionRequested); ok {
			cancel()
		}
	}
	res := e.Run(ctx, protocol.ToolUse{ID: "t1", Name: "bash", Args: map[string]any{"command": "sudo true"}}, emit)
	if !res.IsError {
		t.Fatalf("want cancelled error, got %+v", res)
	}
}

// Relative paths resolve against the session's workdir, not the process cwd
// (they differ under ACP and the multi-session daemon).
func TestResolvePathsAgainstWorkDir(t *testing.T) {
	args := map[string]any{"path": "sub/file.go", "content": "x"}
	got := resolvePaths(protocol.ToolUse{Name: "write", Args: args}, "/work")
	if got.Args["path"] != "/work/sub/file.go" {
		t.Errorf("path = %v", got.Args["path"])
	}
	if args["path"] != "sub/file.go" {
		t.Error("caller's args were mutated")
	}
	for _, tu := range []protocol.ToolUse{
		{Name: "write", Args: map[string]any{"path": "/abs/f"}},
		{Name: "bash", Args: map[string]any{"path": "rel"}},
	} {
		if got := resolvePaths(tu, "/work"); got.Args["path"] != tu.Args["path"] {
			t.Errorf("%s: path rewritten to %v", tu.Name, got.Args["path"])
		}
	}
}
