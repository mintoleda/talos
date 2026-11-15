package tools

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/mintoleda/talos/internal/protocol"
)

const (
	codeModeDefaultTimeout = 30 * time.Second
	codeModeMaxTimeout     = 2 * time.Minute
	codeModeMaxCodeBytes   = 256 << 10
	codeModeMaxLineBytes   = 2 << 20
)

// ToolCaller executes a nested tool call through the session executor, so
// permission checks and tool events still apply to calls made from JavaScript.
type ToolCaller func(ctx context.Context, name string, args map[string]any, emit protocol.EmitFunc) protocol.ToolResult

type codeModeTool struct {
	registry *Registry
	runtime  string

	mu     sync.RWMutex
	caller ToolCaller
}

// NewCodeMode returns a JavaScript tool-calling environment backed by Deno.
// Deno is run without file, network, environment, subprocess, system, or FFI
// permissions; the script can reach Talos only through its registered tools.
func NewCodeMode(registry *Registry) Tool {
	runtime, _ := exec.LookPath("deno")
	return &codeModeTool{registry: registry, runtime: runtime}
}

func (t *codeModeTool) Name() string { return "codemode" }

func (t *codeModeTool) Description() string {
	return "Run JavaScript to call Talos tools through tools.<name>(args). Use top-level await, Promise.all, or Promise.allSettled to run calls concurrently and reduce/filter results before returning them. Scripts have no Deno file, network, environment, subprocess, system, or FFI permissions; tool calls still use Talos permission checks. Requires Deno."
}

func (t *codeModeTool) Schema() json.RawMessage {
	return json.RawMessage(`{
		"type": "object",
		"properties": {
			"code": {"type": "string", "description": "JavaScript source run as the body of an async function; return and top-level await are supported."},
			"timeout_ms": {"type": "integer", "description": "Optional script deadline in milliseconds (default 30000, maximum 120000)."}
		},
		"required": ["code"]
	}`)
}

func (t *codeModeTool) SetToolCaller(caller ToolCaller) {
	t.mu.Lock()
	t.caller = caller
	t.mu.Unlock()
}

func (t *codeModeTool) Execute(ctx context.Context, args map[string]any) (protocol.ToolResult, error) {
	return t.execute(ctx, args, nil)
}

func (t *codeModeTool) ExecuteWithEmit(ctx context.Context, args map[string]any, emit protocol.EmitFunc) (protocol.ToolResult, error) {
	return t.execute(ctx, args, emit)
}

func (t *codeModeTool) execute(ctx context.Context, args map[string]any, emit protocol.EmitFunc) (protocol.ToolResult, error) {
	code, err := str(args, "code")
	if err != nil {
		return errResult(err), nil
	}
	if strings.TrimSpace(code) == "" {
		return errResult(errors.New("code must not be empty")), nil
	}
	if len(code) > codeModeMaxCodeBytes {
		return errResult(fmt.Errorf("code exceeds %d-byte limit", codeModeMaxCodeBytes)), nil
	}
	if t.runtime == "" {
		return errResult(errors.New("codemode requires Deno on PATH (https://deno.com/)")), nil
	}

	timeout := codeModeDefaultTimeout
	if millis, present, err := intArg(args, "timeout_ms"); err != nil {
		return errResult(err), nil
	} else if present {
		if millis < 1 {
			return errResult(errors.New("timeout_ms must be positive")), nil
		}
		if millis > int(codeModeMaxTimeout/time.Millisecond) {
			timeout = codeModeMaxTimeout
		} else {
			timeout = time.Duration(millis) * time.Millisecond
		}
	}

	t.mu.RLock()
	caller := t.caller
	t.mu.RUnlock()
	if caller == nil {
		return errResult(errors.New("codemode tool dispatcher is not connected to the executor")), nil
	}

	result, err := t.run(ctx, code, timeout, caller, emit)
	if err != nil {
		return errResult(err), nil
	}
	return okResult(result), nil
}

type codeModeToolInfo struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	Parameters  json.RawMessage `json:"parameters,omitempty"`
}

type codeModeHostMessage struct {
	Type   string             `json:"type"`
	ID     string             `json:"id,omitempty"`
	Name   string             `json:"name,omitempty"`
	Args   map[string]any     `json:"args,omitempty"`
	Code   string             `json:"code,omitempty"`
	Tools  []codeModeToolInfo `json:"tools,omitempty"`
	Value  json.RawMessage    `json:"value,omitempty"`
	Output []string           `json:"output,omitempty"`
	Logs   []string           `json:"logs,omitempty"`
	Error  string             `json:"error,omitempty"`
}

func (t *codeModeTool) run(parent context.Context, code string, timeout time.Duration, caller ToolCaller, emit protocol.EmitFunc) (string, error) {
	ctx, cancel := context.WithTimeout(parent, timeout)
	defer cancel()
	callsCtx, cancelCalls := context.WithCancel(ctx)
	defer cancelCalls()

	hostFile, err := os.CreateTemp("", "talos-codemode-*.js")
	if err != nil {
		return "", fmt.Errorf("create JavaScript host script: %w", err)
	}
	hostPath := hostFile.Name()
	defer os.Remove(hostPath)
	if _, err := io.WriteString(hostFile, codeModeHostScript); err != nil {
		_ = hostFile.Close()
		return "", fmt.Errorf("write JavaScript host script: %w", err)
	}
	if err := hostFile.Close(); err != nil {
		return "", fmt.Errorf("close JavaScript host script: %w", err)
	}

	cmd := exec.CommandContext(ctx, t.runtime,
		"run",
		"--no-prompt", "--no-config", "--no-remote", "--no-npm", "--no-lock", "--no-check",
		"--v8-flags=--max-old-space-size=128",
		hostPath,
	)
	// Do not give the script access to Talos' environment. `deno run` starts
	// without granting filesystem, network, env, subprocess, system, or FFI access.
	cmd.Env = []string{"DENO_NO_PROMPT=1", "NO_COLOR=1"}

	stdin, err := cmd.StdinPipe()
	if err != nil {
		return "", fmt.Errorf("start JavaScript runtime: %w", err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return "", fmt.Errorf("capture JavaScript output: %w", err)
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return "", fmt.Errorf("capture JavaScript errors: %w", err)
	}
	if err := cmd.Start(); err != nil {
		return "", fmt.Errorf("start Deno: %w", err)
	}

	var stderrBuf cappedWriter
	stderrBuf.max = 16 << 10
	stderrDone := make(chan struct{})
	go func() {
		defer close(stderrDone)
		_, _ = io.Copy(&stderrBuf, stderr)
	}()

	var infos []codeModeToolInfo
	for _, tool := range t.registry.All() {
		if tool == nil || tool.Name() == "codemode" {
			continue
		}
		infos = append(infos, codeModeToolInfo{
			Name:        tool.Name(),
			Description: tool.Description(),
			Parameters:  tool.Schema(),
		})
	}
	sort.Slice(infos, func(i, j int) bool { return infos[i].Name < infos[j].Name })

	writer := bufio.NewWriter(stdin)
	writeMu := sync.Mutex{}
	writeMessage := func(message codeModeHostMessage) error {
		writeMu.Lock()
		defer writeMu.Unlock()
		if err := json.NewEncoder(writer).Encode(message); err != nil {
			return err
		}
		return writer.Flush()
	}
	if err := writeMessage(codeModeHostMessage{Type: "start", Code: code, Tools: infos}); err != nil {
		_ = stdin.Close()
		_ = cmd.Wait()
		return "", fmt.Errorf("send JavaScript program to Deno: %w", err)
	}

	var calls sync.WaitGroup
	callSlots := make(chan struct{}, 32)
	scanner := bufio.NewScanner(stdout)
	scanner.Buffer(make([]byte, 4096), codeModeMaxLineBytes)
	var final *codeModeHostMessage
	var protocolErr error
	for scanner.Scan() {
		var message codeModeHostMessage
		if err := json.Unmarshal(scanner.Bytes(), &message); err != nil {
			protocolErr = fmt.Errorf("invalid codemode response: %w", err)
			break
		}
		switch message.Type {
		case "call":
			if message.ID == "" || message.Name == "" {
				protocolErr = errors.New("invalid codemode tool-call request")
				break
			}
			select {
			case callSlots <- struct{}{}:
			case <-ctx.Done():
				protocolErr = ctx.Err()
				break
			}
			if protocolErr != nil {
				break
			}
			calls.Add(1)
			go func(message codeModeHostMessage) {
				defer calls.Done()
				defer func() { <-callSlots }()
				res := caller(callsCtx, message.Name, message.Args, emit)
				response := codeModeHostMessage{Type: "response", ID: message.ID}
				if res.IsError {
					response.Error = res.Content
				} else {
					response.Value = mustJSON(map[string]any{"content": res.Content})
				}
				_ = writeMessage(response)
			}(message)
		case "result", "error":
			final = &message
			cancelCalls()
			_ = stdin.Close()
		case "":
			protocolErr = errors.New("empty codemode response")
		default:
			protocolErr = fmt.Errorf("unknown codemode response type %q", message.Type)
		}
		if final != nil || protocolErr != nil {
			break
		}
	}

	if protocolErr != nil || final == nil {
		if protocolErr != nil {
			cancel()
		}
		cancelCalls()
		_ = stdin.Close()
		// Drain stdout/stderr before Wait so a noisy child cannot deadlock on a
		// full pipe. scanner may have buffered bytes, so keep consuming it.
		for scanner.Scan() {
		}
	}
	calls.Wait()
	_ = stdin.Close()
	// Finish draining the pipe after a normal result as well. User code can
	// write directly to Deno.stdout; those bytes are ignored after the protocol
	// result and cannot block the subprocess from exiting.
	for scanner.Scan() {
	}
	waitErr := cmd.Wait()
	<-stderrDone

	if protocolErr != nil {
		return "", protocolErr
	}
	if final == nil {
		if ctx.Err() != nil {
			return "", fmt.Errorf("codemode timed out or was cancelled: %w", ctx.Err())
		}
		stderrText := strings.TrimSpace(stderrBuf.String())
		if stderrText != "" {
			return "", fmt.Errorf("Deno exited without a result: %s", stderrText)
		}
		if waitErr != nil {
			return "", fmt.Errorf("Deno exited without a result: %w", waitErr)
		}
		return "", errors.New("Deno exited without a result")
	}
	if final.Type == "error" {
		partial := formatCodeModeOutput(final.Output, final.Logs)
		if partial != "(script completed with no output)" {
			return "", fmt.Errorf("%s\nScript error: %s", partial, final.Error)
		}
		return "", errors.New(final.Error)
	}
	if waitErr != nil && ctx.Err() != nil {
		return "", fmt.Errorf("codemode timed out or was cancelled: %w", ctx.Err())
	}
	return formatCodeModeOutput(final.Output, final.Logs), nil
}

func (t *codeModeTool) WithRegistry(registry *Registry) Tool {
	return NewCodeMode(registry)
}

func mustJSON(value any) json.RawMessage {
	data, err := json.Marshal(value)
	if err != nil {
		return json.RawMessage("null")
	}
	return data
}

func formatCodeModeOutput(output, logs []string) string {
	var parts []string
	for _, item := range output {
		parts = append(parts, item)
	}
	if len(logs) > 0 {
		parts = append(parts, "[console]\n"+strings.Join(logs, "\n"))
	}
	if len(parts) == 0 {
		return "(script completed with no output)"
	}
	return strings.Join(parts, "\n")
}

const codeModeHostScript = `
const decoder = new TextDecoder();
const encoder = new TextEncoder();
const inputReader = Deno.stdin.readable.getReader();
const outputWriter = Deno.stdout.writable.getWriter();
let inputBuffer = "";
let inputDone = false;
async function readLine() {
  for (;;) {
    const newline = inputBuffer.indexOf("\n");
    if (newline >= 0) {
      const line = inputBuffer.slice(0, newline);
      inputBuffer = inputBuffer.slice(newline + 1);
      return line;
    }
    if (inputDone) {
      if (!inputBuffer) return null;
      const tail = inputBuffer;
      inputBuffer = "";
      return tail;
    }
    const { value, done } = await inputReader.read();
    if (done) {
      inputDone = true;
      inputBuffer += decoder.decode();
    } else {
      inputBuffer += decoder.decode(value, { stream: true });
    }
  }
}
async function send(message) {
  await outputWriter.write(encoder.encode(JSON.stringify(message) + "\n"));
}
const firstLine = await readLine();
if (firstLine === null) throw new Error("missing codemode startup message");
const startup = JSON.parse(firstLine);
const specs = startup.tools || [];
const pending = new Map();
let nextID = 0;
async function receiveResponses() {
  for (;;) {
    const line = await readLine();
    if (line === null) return;
    let message;
    try { message = JSON.parse(line); } catch { continue; }
    if (message.type !== "response") continue;
    const waiting = pending.get(message.id);
    if (!waiting) continue;
    pending.delete(message.id);
    if (message.error) waiting.reject(new Error(message.error));
    else waiting.resolve(message.value);
  }
}
const responseLoop = receiveResponses();
function callTool(name, args = {}) {
  if (!args || typeof args !== "object" || Array.isArray(args)) {
    return Promise.reject(new Error("tool arguments must be an object"));
  }
  const id = String(++nextID);
  const promise = new Promise((resolve, reject) => {
    pending.set(id, { resolve, reject });
    send({ type: "call", id, name, args }).catch((error) => {
      pending.delete(id);
      reject(error);
    });
  });
  // Mark an intentionally unawaited call as handled; awaiting it still rejects.
  promise.catch(() => {});
  return promise;
}
function jsName(name) { return name.replace(/[^A-Za-z0-9_$]/g, "_"); }
const tools = Object.create(null);
const exposedNames = new Set();
for (const spec of specs) {
  const key = jsName(spec.name);
  if (!exposedNames.has(key)) {
    exposedNames.add(key);
    Object.defineProperty(tools, key, { enumerable: true, value: (args) => callTool(spec.name, args) });
  }
}
Object.defineProperty(tools, "invoke", { enumerable: false, value: callTool });
Object.freeze(tools);
const ALL_TOOLS = specs.map(({ name, description }) => ({ name, description }));
function describeTool(name) { return specs.find((tool) => tool.name === name); }
function searchTools(query, options = {}) {
  const terms = String(query || "").toLowerCase().trim().split(/\s+/).filter(Boolean);
  const limit = Math.max(1, Math.min(100, Math.trunc(options.limit || 8)));
  const namespace = options.namespace ? String(options.namespace) : "";
  const candidates = namespace ? specs.filter((tool) => tool.name.startsWith("mcp__" + namespace + "__")) : specs;
  const matches = candidates.map((tool) => {
    const name = tool.name.toLowerCase();
    const description = tool.description.toLowerCase();
    let score = 0;
    for (const term of terms) {
      if (name === term) score += 8;
      else if (name.includes(term)) score += 4;
      if (description.includes(term)) score += 1;
    }
    return { tool, score };
  }).filter((item) => item.score > 0);
  matches.sort((a, b) => b.score - a.score || a.tool.name.localeCompare(b.tool.name));
  return matches.slice(0, limit).map(({ tool }) => ({ name: tool.name, description: tool.description }));
}
function describeNamespace(name) {
  const prefix = "mcp__" + String(name) + "__";
  const matched = specs.filter((tool) => tool.name.startsWith(prefix));
  return matched.length ? { name: String(name), tools: matched } : undefined;
}
const output = [];
const logs = [];
function printable(value) {
  if (typeof value === "string") return value;
  if (value === undefined) return "undefined";
  try { return JSON.stringify(value, null, 2); }
  catch { return String(value); }
}
function text(value) { output.push(printable(value)); }
const consoleAPI = Object.freeze({
  log: (...values) => logs.push(values.map(printable).join(" ")),
  info: (...values) => logs.push(values.map(printable).join(" ")),
  warn: (...values) => logs.push(values.map(printable).join(" ")),
  error: (...values) => logs.push(values.map(printable).join(" ")),
  debug: (...values) => logs.push(values.map(printable).join(" ")),
});
const exitSentinel = {};
function exit() { throw exitSentinel; }
const AsyncFunction = Object.getPrototypeOf(async function () {}).constructor;
try {
  const run = new AsyncFunction("tools", "ALL_TOOLS", "searchTools", "describeTool", "describeNamespace", "text", "console", "exit", startup.code);
  const returned = await run(tools, ALL_TOOLS, searchTools, describeTool, describeNamespace, text, consoleAPI, exit);
  if (returned !== undefined) text(returned);
  await send({ type: "result", output, logs });
} catch (error) {
  if (error === exitSentinel) await send({ type: "result", output, logs });
  else await send({ type: "error", error: error instanceof Error ? error.message : String(error), output, logs });
}
await inputReader.cancel();
await outputWriter.close();
Deno.exit(0);
`
