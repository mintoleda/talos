package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"sync/atomic"
	"testing"
	"time"

	"github.com/mintoleda/talos/internal/protocol"
)

type codeModeTestTool struct{}

func (codeModeTestTool) Name() string        { return "echo" }
func (codeModeTestTool) Description() string { return "Return the supplied value." }
func (codeModeTestTool) Schema() json.RawMessage {
	return json.RawMessage(`{"type":"object","properties":{"value":{"type":"string"}}}`)
}
func (codeModeTestTool) Execute(_ context.Context, args map[string]any) (protocol.ToolResult, error) {
	return okResult(fmt.Sprint(args["value"])), nil
}

func TestCodeModeRunsConcurrentToolCalls(t *testing.T) {
	if _, err := exec.LookPath("deno"); err != nil {
		t.Skip("Deno is not installed")
	}

	reg := EmptyRegistry()
	reg.Add(codeModeTestTool{})
	cm := NewCodeMode(reg)
	reg.Add(cm)

	var active, maxActive atomic.Int64
	setter := cm.(interface{ SetToolCaller(ToolCaller) })
	setter.SetToolCaller(func(ctx context.Context, name string, args map[string]any, _ protocol.EmitFunc) protocol.ToolResult {
		if name != "echo" {
			return protocol.ToolResult{IsError: true, Content: "unexpected tool " + name}
		}
		now := active.Add(1)
		defer active.Add(-1)
		for {
			previous := maxActive.Load()
			if now <= previous || maxActive.CompareAndSwap(previous, now) {
				break
			}
		}
		select {
		case <-ctx.Done():
			return protocol.ToolResult{IsError: true, Content: ctx.Err().Error()}
		case <-time.After(100 * time.Millisecond):
		}
		return okResult(fmt.Sprint(args["value"]))
	})

	result, err := cm.Execute(context.Background(), map[string]any{
		"code": `const results = await Promise.all([
			tools.echo({value: "first"}),
			tools.echo({value: "second"}),
		]);
		return results.map((result) => result.content);`,
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.IsError {
		t.Fatalf("codemode returned error: %s", result.Content)
	}
	if result.Content != "[\n  \"first\",\n  \"second\"\n]" {
		t.Fatalf("output = %q", result.Content)
	}
	if maxActive.Load() < 2 {
		t.Fatalf("tool calls did not overlap; max active = %d", maxActive.Load())
	}
}

func TestCodeModeDeniesDirectRuntimeAccess(t *testing.T) {
	if _, err := exec.LookPath("deno"); err != nil {
		t.Skip("Deno is not installed")
	}

	reg := EmptyRegistry()
	cm := NewCodeMode(reg)
	cm.(interface{ SetToolCaller(ToolCaller) }).SetToolCaller(func(context.Context, string, map[string]any, protocol.EmitFunc) protocol.ToolResult {
		return protocol.ToolResult{IsError: true, Content: "unexpected tool call"}
	})

	result, err := cm.Execute(context.Background(), map[string]any{
		"code": `try {
			await Deno.readTextFile("/etc/passwd");
			return "unexpectedly readable";
		} catch (error) {
			return error.name;
		}`,
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.IsError {
		t.Fatalf("codemode failed: %s", result.Content)
	}
	if result.Content != "NotCapable" {
		t.Fatalf("direct file access was not denied: %q", result.Content)
	}
}

func TestFilteredRegistryNarrowsCodeModeTools(t *testing.T) {
	if _, err := exec.LookPath("deno"); err != nil {
		t.Skip("Deno is not installed")
	}

	reg := EmptyRegistry()
	reg.Add(codeModeTestTool{}, hiddenCodeModeTestTool{})
	reg.Add(NewCodeMode(reg))
	filtered := reg.Filter([]string{"codemode", "echo"})
	cm, ok := filtered.Get("codemode")
	if !ok {
		t.Fatal("filtered registry lost codemode")
	}
	cm.(interface{ SetToolCaller(ToolCaller) }).SetToolCaller(func(_ context.Context, name string, _ map[string]any, _ protocol.EmitFunc) protocol.ToolResult {
		if name != "echo" {
			return protocol.ToolResult{IsError: true, Content: "tool is outside the filtered registry: " + name}
		}
		return okResult("ok")
	})
	result, err := cm.Execute(context.Background(), map[string]any{
		"code": `return ALL_TOOLS.map((tool) => tool.name);`,
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.IsError {
		t.Fatalf("codemode returned error: %s", result.Content)
	}
	if result.Content != "[\n  \"echo\"\n]" {
		t.Fatalf("filtered tool list = %q", result.Content)
	}
}

type hiddenCodeModeTestTool struct{}

func (hiddenCodeModeTestTool) Name() string { return "hidden" }
func (hiddenCodeModeTestTool) Description() string {
	return "Must not be available through a filtered codemode."
}
func (hiddenCodeModeTestTool) Schema() json.RawMessage {
	return EmptySchema()
}
func (hiddenCodeModeTestTool) Execute(context.Context, map[string]any) (protocol.ToolResult, error) {
	return okResult("hidden"), nil
}
