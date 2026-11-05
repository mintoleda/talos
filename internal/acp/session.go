package acp

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/mintoleda/talos/internal/mcp"
	"github.com/mintoleda/talos/internal/models"
	"github.com/mintoleda/talos/internal/protocol"
	"github.com/mintoleda/talos/internal/session"
	"github.com/mintoleda/talos/internal/skills"
	"github.com/mintoleda/talos/internal/stack"
)

// acpSession is one ACP session: a Talos transcript plus a lazily built
// runtime stack. Model and thinking settings are applied when the stack is built.
type acpSession struct {
	agent      *Agent
	id         string
	cwd        string
	projectDir string
	mcp        []mcp.ServerConfig

	// turnMu serializes prompts. Holding it is the only way to touch the
	// runtime's loop and prompt builder, which RunTurn reads unsynchronized.
	turnMu sync.Mutex

	mu    sync.Mutex // guards everything below
	built *stack.Built
	// Desired settings. Applied to the runtime at build time and at turn
	// boundaries (see applySettings).
	provider    string
	model       string
	thinking    string
	cancel      context.CancelFunc
	allowAlways map[string]bool
	closed      bool
}

func (s *acpSession) update(u SessionUpdate) {
	_ = s.agent.conn.Notify("session/update", SessionNotification{SessionID: s.id, Update: u})
}

// state reports models and config options for session/new|load.
func (s *acpSession) state(catalog []models.Entry) (*SessionModes, *SessionModels, []ConfigOption) {
	s.mu.Lock()
	defer s.mu.Unlock()
	current := s.provider + "/" + s.model
	ms := &SessionModels{CurrentModelID: current}
	seen := false
	for _, e := range catalog {
		full := e.Full()
		seen = seen || full == current
		ms.AvailableModels = append(ms.AvailableModels, ModelInfo{ModelID: full, Name: full})
	}
	if !seen {
		ms.AvailableModels = append([]ModelInfo{{ModelID: current, Name: current}}, ms.AvailableModels...)
	}
	return nil, ms, []ConfigOption{thinkingOption(s.model, s.thinking)}
}

// ensureBuilt assembles the runtime on first use. Callers hold turnMu, so
// builds never race each other; s.mu is not held while MCP servers connect,
// so cancel and close stay responsive. ctx bounds only connection setup.
func (s *acpSession) ensureBuilt(ctx context.Context) (*stack.Built, error) {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return nil, errSessionClosed
	}
	if b := s.built; b != nil {
		s.mu.Unlock()
		return b, s.applySettings(b)
	}
	opts := stack.BuildOpts{
		Cfg:        s.agent.cfg,
		Dir:        s.cwd,
		ProjectDir: s.projectDir,
		SessionID:  s.id,
		Provider:   s.provider,
		Model:      s.model,
		MCPServers: s.mcp,
	}
	s.mu.Unlock()

	b, err := stack.Build(ctx, opts)
	if err != nil {
		return nil, err
	}
	if ctx.Err() != nil {
		// Cancelled mid-connect: MCP servers may be missing. Discard so the
		// next prompt builds a complete stack.
		b.Close()
		return nil, ctx.Err()
	}
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		b.Close()
		return nil, errSessionClosed
	}
	s.built = b
	s.mu.Unlock()
	return b, s.applySettings(b)
}

var errSessionClosed = errors.New("session closed")

// applySettings brings the runtime's model and thinking level in line with
// the desired settings. Caller holds turnMu (no turn is running). On a failed
// model switch the desired model reverts to the running one.
func (s *acpSession) applySettings(b *stack.Built) error {
	s.mu.Lock()
	pName, pModel, thinking := s.provider, s.model, s.thinking
	s.mu.Unlock()

	var err error
	if pName != b.Cfg.Provider || pModel != b.Cfg.Model {
		var warn error
		warn, err = stack.SwitchModel(b.Cfg, b.Loop, b.PromptBuilder, b.Prices, b.MemStore, false, pName, pModel)
		if warn != nil {
			fmt.Fprintf(os.Stderr, "[warning] %v\n", warn)
		}
		if err == nil {
			session.EnsureSessionMeta(b.Session, s.cwd, s.projectDir, b.Cfg.Provider, b.Cfg.Model)
		}
		s.mu.Lock()
		s.provider, s.model = b.Cfg.Provider, b.Cfg.Model
		s.mu.Unlock()
	}
	if thinking != "" && thinking != b.PromptBuilder.ThinkingLevel() {
		b.PromptBuilder.SetThinkingLevel(thinking)
	}
	return err
}

// withIdleRuntime runs fn with the built runtime when no turn is in flight.
// It reports false (and skips fn) when a turn is running or nothing is built;
// the change is then applied at the next turn boundary.
func (s *acpSession) withIdleRuntime(fn func(b *stack.Built) error) (bool, error) {
	if !s.turnMu.TryLock() {
		return false, nil
	}
	defer s.turnMu.Unlock()
	s.mu.Lock()
	b := s.built
	s.mu.Unlock()
	if b == nil {
		return false, nil
	}
	return true, fn(b)
}

func (s *acpSession) cancelTurn() {
	s.mu.Lock()
	cancel := s.cancel
	s.mu.Unlock()
	if cancel != nil {
		cancel()
	}
}

func (s *acpSession) close() {
	// Mark closed before cancelling so a prompt that holds turnMu but has
	// not yet registered its cancel func refuses to start.
	s.mu.Lock()
	s.closed = true
	cancel := s.cancel
	s.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	s.turnMu.Lock() // wait for the running turn to unwind
	defer s.turnMu.Unlock()
	s.mu.Lock()
	b := s.built
	s.built = nil
	s.mu.Unlock()
	if b != nil {
		b.Close()
	}
}

// setModel accepts "provider/model" or a bare model id on the current
// provider. When idle the switch happens (and is validated) now; during a
// turn it is deferred to the next turn boundary.
func (s *acpSession) setModel(id string) error {
	s.desireModel(id)
	_, err := s.withIdleRuntime(s.applySettings)
	return err
}

func (s *acpSession) desireModel(id string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	pName, pModel := s.provider, id
	if p, m, ok := strings.Cut(id, "/"); ok {
		pName, pModel = p, m
	}
	s.provider, s.model = pName, pModel
}

func (s *acpSession) setConfigOption(id, value string) (any, error) {
	if id != "thought_level" {
		return nil, &RPCError{Code: codeInvalidParams, Message: "unknown config option: " + id}
	}
	s.mu.Lock()
	opt := thinkingOption(s.model, "")
	valid := false
	for _, o := range opt.Options {
		valid = valid || o.Value == value
	}
	if !valid {
		s.mu.Unlock()
		return nil, &RPCError{Code: codeInvalidParams, Message: "unsupported thinking level: " + value}
	}
	s.thinking = value
	opts := []ConfigOption{thinkingOption(s.model, s.thinking)}
	s.mu.Unlock()
	_, _ = s.withIdleRuntime(s.applySettings)
	return SetConfigOptionResponse{ConfigOptions: opts}, nil
}

// ---- commands ----

var builtinCommands = []AvailableCommand{
	{Name: "compact", Description: "Fold older conversation into a summary", Input: &AvailableCommandInput{Hint: "optional focus"}},
	{Name: "model", Description: "List models, or switch with provider/model", Input: &AvailableCommandInput{Hint: "provider/model"}},
	{Name: "mcp", Description: "List connected MCP servers and their tools"},
	{Name: "subagents", Description: "Toggle subagent delegation on/off"},
}

func (s *acpSession) skills() []skills.Skill {
	found, _ := skills.Scan([]skills.Dir{
		{Path: filepath.Join(s.agent.cfg.BaseDir, "skills"), Label: "global"},
		{Path: filepath.Join(s.cwd, ".talos", "skills"), Label: "project"},
	})
	return found
}

// announceCommands sends available_commands_update. Call it after the
// session/new|load response so the client knows the session id; clients
// (Paseo) wait for this update before showing the command list.
func (s *acpSession) announceCommands() {
	cmds := append([]AvailableCommand(nil), builtinCommands...)
	for _, sk := range s.skills() {
		if isBuiltin(sk.Name) {
			continue
		}
		cmds = append(cmds, AvailableCommand{Name: sk.Name, Description: sk.Description, Input: &AvailableCommandInput{Hint: "arguments"}})
	}
	s.update(SessionUpdate{SessionUpdate: "available_commands_update", AvailableCommands: cmds})
}

func isBuiltin(name string) bool {
	for _, c := range builtinCommands {
		if c.Name == name {
			return true
		}
	}
	return false
}

// slash handles built-in commands. ok is false when text is not a built-in;
// skill commands are rewritten to the "skill:<name>" form the prompt teaches.
func (s *acpSession) slash(ctx context.Context, text string) (reply string, rewritten string, ok bool) {
	fields := strings.Fields(strings.TrimPrefix(text, "/"))
	if len(fields) == 0 {
		return "", "", false
	}
	name, rest := fields[0], strings.TrimSpace(strings.TrimPrefix(strings.TrimPrefix(text, "/"), fields[0]))
	switch name {
	case "compact":
		b, err := s.ensureBuilt(ctx)
		if err != nil {
			return "compact: " + err.Error(), "", true
		}
		cctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
		defer cancel()
		summary, err := b.Loop.CompactNow(cctx, rest)
		switch {
		case err != nil:
			return "compact failed: " + err.Error(), "", true
		case summary == "":
			return "nothing to compact", "", true
		}
		return "compacted earlier conversation:\n\n" + summary, "", true
	case "model":
		if rest != "" {
			// prompt holds turnMu, so apply directly rather than deferring.
			s.desireModel(rest)
			s.mu.Lock()
			b := s.built
			s.mu.Unlock()
			if b != nil {
				if err := s.applySettings(b); err != nil {
					return "switch model: " + err.Error(), "", true
				}
			}
			s.mu.Lock()
			cur := s.provider + "/" + s.model
			s.mu.Unlock()
			return "switched to " + cur, "", true
		}
		var sb strings.Builder
		sb.WriteString("Available models:")
		for _, e := range s.agent.modelCatalog(ctx) {
			sb.WriteString("\n- " + e.Full())
		}
		return sb.String(), "", true
	case "mcp":
		b, err := s.ensureBuilt(ctx)
		if err != nil {
			return "mcp: " + err.Error(), "", true
		}
		return b.MCPManager.Status(), "", true
	case "subagents":
		b, err := s.ensureBuilt(ctx)
		if err != nil {
			return "subagents: " + err.Error(), "", true
		}
		if b.AgentBuilder == nil {
			return "subagents not enabled in config", "", true
		}
		on := !b.PromptBuilder.SubagentEnabled()
		b.PromptBuilder.SetSubagentEnabled(on)
		if on {
			return "subagents: on", "", true
		}
		return "subagents: off", "", true
	}
	for _, sk := range s.skills() {
		if sk.Name == name {
			return "", strings.TrimSpace("skill:" + name + " " + rest), false
		}
	}
	return "", "", false
}

// ---- prompt ----

func (s *acpSession) prompt(ctx context.Context, blocks []ContentBlock) (any, error) {
	s.turnMu.Lock()
	defer s.turnMu.Unlock()

	// Register cancellation before any work (slash commands, building) so
	// session/cancel and close reach every phase of the turn.
	turnCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return nil, errSessionClosed
	}
	s.cancel = cancel
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		s.cancel = nil
		s.mu.Unlock()
	}()

	if len(blocks) > 0 && blocks[0].Type == "text" && strings.HasPrefix(blocks[0].Text, "/") {
		reply, rewritten, handled := s.slash(turnCtx, blocks[0].Text)
		if handled {
			if turnCtx.Err() != nil {
				return PromptResponse{StopReason: StopCancelled}, nil
			}
			s.update(SessionUpdate{SessionUpdate: "agent_message_chunk", Content: ptr(textContent(reply))})
			return PromptResponse{StopReason: StopEndTurn}, nil
		}
		if rewritten != "" {
			blocks = append([]ContentBlock{textContent(rewritten)}, blocks[1:]...)
		}
	}

	input, err := toProtocolBlocks(blocks)
	if err != nil {
		return nil, invalidParams(err)
	}
	b, err := s.ensureBuilt(turnCtx)
	if turnCtx.Err() != nil {
		return PromptResponse{StopReason: StopCancelled}, nil
	}
	if err != nil {
		return nil, err
	}

	if b.Checkpointer != nil {
		_, _ = b.Checkpointer.Snapshot("before-run")
	}
	t := &turn{s: s, ctx: turnCtx, tools: map[string]*toolState{}}
	runErr := b.Loop.RunTurn(turnCtx, input, t.emit)
	_ = session.TouchSessionMeta(s.id)

	resp := PromptResponse{StopReason: StopEndTurn, Usage: t.promptUsage()}
	switch {
	case turnCtx.Err() != nil:
		resp.StopReason = StopCancelled
	case runErr != nil && strings.Contains(runErr.Error(), "iteration limit"):
		resp.StopReason = StopMaxTurnRequests
	case runErr != nil:
		return nil, runErr
	default:
		resp.StopReason = mapStopReason(t.stopReason)
	}
	return resp, nil
}

func mapStopReason(r string) string {
	switch r {
	case "length", "max_tokens":
		return StopMaxTokens
	case "refusal", "content_filter":
		return StopRefusal
	}
	return StopEndTurn
}

func ptr[T any](v T) *T { return &v }

// toProtocolBlocks converts an ACP prompt into Talos user content. Resources
// become inline text so every provider sees them; images stay images.
func toProtocolBlocks(blocks []ContentBlock) ([]protocol.ContentBlock, error) {
	var out []protocol.ContentBlock
	for _, b := range blocks {
		switch b.Type {
		case "text":
			out = append(out, protocol.ContentBlock{Type: protocol.BlockText, Text: b.Text})
		case "image":
			if b.Data == "" {
				if b.URI != "" {
					out = append(out, protocol.ContentBlock{Type: protocol.BlockText, Text: "[image: " + b.URI + "]"})
				}
				continue
			}
			out = append(out, protocol.ContentBlock{Type: protocol.BlockImage, Image: &protocol.ImageBlock{MediaType: b.MimeType, Data: b.Data}})
		case "resource_link":
			name := b.Name
			if name == "" {
				name = b.URI
			}
			out = append(out, protocol.ContentBlock{Type: protocol.BlockText, Text: fmt.Sprintf("[referenced: %s (%s)]", name, uriPath(b.URI))})
		case "resource":
			if b.Resource == nil {
				continue
			}
			if b.Resource.Text != "" {
				out = append(out, protocol.ContentBlock{Type: protocol.BlockText, Text: fmt.Sprintf("<file path=%q>\n%s\n</file>", uriPath(b.Resource.URI), b.Resource.Text)})
			} else {
				out = append(out, protocol.ContentBlock{Type: protocol.BlockText, Text: fmt.Sprintf("[binary resource: %s]", b.Resource.URI)})
			}
		case "audio":
			// Not supported (promptCapabilities.audio is false).
		default:
			return nil, fmt.Errorf("unsupported content type %q", b.Type)
		}
	}
	if len(out) == 0 {
		return nil, errors.New("empty prompt")
	}
	return out, nil
}

func uriPath(uri string) string {
	return strings.TrimPrefix(uri, "file://")
}

// ---- turn event translation ----

type toolState struct {
	name   string
	args   map[string]any
	output strings.Builder
	sentAt time.Time
}

type turn struct {
	s   *acpSession
	ctx context.Context

	mu         sync.Mutex // tools run in parallel and emit concurrently
	tools      map[string]*toolState
	usage      protocol.Usage
	stopReason string
}

func (t *turn) emit(ev protocol.Event) {
	s := t.s
	switch e := ev.(type) {
	case protocol.TextDelta:
		s.update(SessionUpdate{SessionUpdate: "agent_message_chunk", Content: ptr(textContent(e.Text))})
	case protocol.ThinkingDelta:
		s.update(SessionUpdate{SessionUpdate: "agent_thought_chunk", Content: ptr(textContent(e.Text))})
	case protocol.ToolStarted:
		t.mu.Lock()
		t.tools[e.ID] = &toolState{name: e.Name, args: e.Args}
		t.mu.Unlock()
		s.update(toolStart(e.ID, e.Name, s.cwd, e.Args))
	case protocol.ToolOutputDelta:
		t.mu.Lock()
		st, ok := t.tools[e.ID]
		if !ok {
			t.mu.Unlock()
			return
		}
		st.output.WriteString(e.Text)
		send := time.Since(st.sentAt) > 250*time.Millisecond
		if send {
			st.sentAt = time.Now()
		}
		out := st.output.String()
		t.mu.Unlock()
		if send {
			if len(out) > maxToolOutput {
				out = out[len(out)-maxToolOutput:]
			}
			tc := textContent(out)
			s.update(SessionUpdate{SessionUpdate: "tool_call_update", ToolCallID: e.ID, Status: ToolInProgress,
				ToolContent: []ToolCallContent{{Type: "content", Content: &tc}}})
		}
	case protocol.ToolFinished:
		t.mu.Lock()
		st := t.tools[e.ID]
		delete(t.tools, e.ID)
		t.mu.Unlock()
		if st == nil {
			st = &toolState{}
		}
		s.update(toolFinish(e.ID, st.name, s.cwd, st.args, e.Result))
	case protocol.PermissionRequested:
		t.permission(e)
	case protocol.Notice:
		// Context-fullness warnings are covered by usage_update.
		if e.Level == "debug" || e.Text == "" || strings.HasPrefix(e.Text, "context ") {
			return
		}
		s.update(SessionUpdate{SessionUpdate: "agent_thought_chunk", Content: ptr(textContent("\n[talos " + e.Level + "] " + e.Text + "\n"))})
	case protocol.PromptEstimate:
		if e.ContextLimit > 0 {
			s.update(SessionUpdate{SessionUpdate: "usage_update", Used: ptr(e.PromptTokens), Size: ptr(e.ContextLimit)})
		}
	case protocol.TurnEnded:
		t.mu.Lock()
		t.usage = e.Usage
		t.stopReason = e.StopReason
		t.mu.Unlock()
		if limit := s.contextLimit(); e.ContextTokens > 0 && limit > 0 {
			s.update(SessionUpdate{SessionUpdate: "usage_update", Used: ptr(e.ContextTokens), Size: ptr(limit)})
		}
	}
}

func (s *acpSession) contextLimit() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.built == nil {
		return 0
	}
	return s.built.PromptBuilder.ContextLimit()
}

func (t *turn) promptUsage() *PromptUsage {
	t.mu.Lock()
	defer t.mu.Unlock()
	u := t.usage
	if u.PromptTokens == 0 && u.CompletionTokens == 0 {
		return nil
	}
	return &PromptUsage{
		TotalTokens:       u.PromptTokens + u.CompletionTokens,
		InputTokens:       u.PromptTokens,
		OutputTokens:      u.CompletionTokens,
		CachedReadTokens:  u.CachedPromptTokens,
		CachedWriteTokens: u.CacheCreationTokens,
	}
}

func permissionKey(e protocol.PermissionRequested) string {
	return e.ToolName + "\x00" + e.Command
}

// permission forwards an executor prompt to the client as
// session/request_permission and relays the decision back to the executor.
func (t *turn) permission(e protocol.PermissionRequested) {
	s := t.s
	allowed := false
	defer func() {
		if e.ReplyCh != nil {
			e.ReplyCh <- allowed
		}
	}()

	key := permissionKey(e)
	s.mu.Lock()
	allowed = s.allowAlways[key]
	s.mu.Unlock()
	if allowed {
		return
	}

	args := e.Args
	if args == nil {
		t.mu.Lock()
		if st := t.tools[e.ToolUseID]; st != nil {
			args = st.args
		}
		t.mu.Unlock()
	}
	reason := e.Reason
	if reason == "" {
		reason = e.ToolName + " requires approval"
	}
	tc := textContent(reason)
	always := "Always allow " + e.ToolName
	if e.Command != "" {
		always = "Always allow this command"
	}
	req := RequestPermissionRequest{
		SessionID: s.id,
		ToolCall: ToolCallUpdate{
			ToolCallID: e.ToolUseID,
			Title:      toolTitle(e.ToolName, args),
			Kind:       toolKind(e.ToolName),
			Status:     ToolPending,
			Content:    []ToolCallContent{{Type: "content", Content: &tc}},
			Locations:  toolLocations(e.ToolName, s.cwd, args),
			RawInput:   args,
		},
		Options: []PermissionOption{
			{OptionID: "allow_once", Name: "Allow", Kind: "allow_once"},
			{OptionID: "allow_always", Name: always, Kind: "allow_always"},
			{OptionID: "reject_once", Name: "Reject", Kind: "reject_once"},
		},
	}
	var resp RequestPermissionResponse
	if err := s.agent.conn.Call(t.ctx, "session/request_permission", req, &resp); err != nil {
		return
	}
	if resp.Outcome.Outcome != "selected" {
		return
	}
	switch resp.Outcome.OptionID {
	case "allow_once":
		allowed = true
	case "allow_always":
		allowed = true
		s.mu.Lock()
		s.allowAlways[key] = true
		s.mu.Unlock()
	}
}

// ---- history replay (session/load) ----

func (s *acpSession) replay(history []protocol.FrozenMessage) {
	type pending struct {
		name string
		args map[string]any
	}
	calls := map[string]pending{}
	for _, fm := range history {
		m := fm.Msg
		for _, b := range m.Content {
			switch b.Type {
			case protocol.BlockText:
				if b.Text == "" {
					continue
				}
				kind := "agent_message_chunk"
				if m.Role == protocol.RoleUser {
					kind = "user_message_chunk"
				}
				s.update(SessionUpdate{SessionUpdate: kind, Content: ptr(textContent(b.Text))})
			case protocol.BlockImage:
				if m.Role == protocol.RoleUser && b.Image != nil {
					s.update(SessionUpdate{SessionUpdate: "user_message_chunk", Content: &ContentBlock{Type: "image", Data: b.Image.Data, MimeType: b.Image.MediaType}})
				}
			case protocol.BlockToolUse:
				if b.ToolUse == nil {
					continue
				}
				tu := b.ToolUse
				calls[tu.ID] = pending{tu.Name, tu.Args}
				s.update(toolStart(tu.ID, tu.Name, s.cwd, tu.Args))
			case protocol.BlockToolResult:
				if b.ToolResult == nil {
					continue
				}
				p := calls[b.ToolResult.ToolUseID]
				delete(calls, b.ToolResult.ToolUseID)
				s.update(toolFinish(b.ToolResult.ToolUseID, p.name, s.cwd, p.args, *b.ToolResult))
			}
		}
	}
	// Calls without results were interrupted.
	for id := range calls {
		s.update(SessionUpdate{SessionUpdate: "tool_call_update", ToolCallID: id, Status: ToolFailed, RawOutput: map[string]any{"message": "interrupted"}})
	}
}
