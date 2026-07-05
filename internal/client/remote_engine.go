package client

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/mintoleda/talos/internal/client/rpc"
	"github.com/mintoleda/talos/internal/models"
	"github.com/mintoleda/talos/internal/protocol"
	"github.com/mintoleda/talos/internal/server"
	"github.com/mintoleda/talos/internal/tui/dialogs"
)

// RemoteEngine is the socket-backed Engine implementation.
type RemoteEngine struct {
	conn          *server.ClientConn
	events        <-chan protocol.Event
	thinkingLevel string
}

func NewRemoteEngine(conn *server.ClientConn, events <-chan protocol.Event) *RemoteEngine {
	return &RemoteEngine{conn: conn, events: events}
}

func (e *RemoteEngine) Submit(blocks []protocol.ContentBlock) {
	_ = e.conn.Send(blocksText(blocks))
}

func (e *RemoteEngine) Interrupt() {
	_ = e.conn.Interrupt()
}

func (e *RemoteEngine) Approve(ok bool, plan []byte) {
	_ = e.conn.Approve(ok, plan)
}

func (e *RemoteEngine) Steer(blocks []protocol.ContentBlock) {
	_ = e.conn.Steer(blocksText(blocks))
}

func (e *RemoteEngine) WithdrawSteer() []protocol.ContentBlock {
	var out rpc.BlocksResult
	if err := e.request(rpc.WithdrawSteer, nil, &out); err != nil {
		return nil
	}
	return out.Blocks
}

func (e *RemoteEngine) PendingSteers() int {
	return 0
}

func (e *RemoteEngine) NewSession() (string, error) {
	var out struct {
		ID string `json:"id"`
	}
	if err := e.request(rpc.NewSession, nil, &out); err != nil {
		return "", err
	}
	return out.ID, nil
}

func (e *RemoteEngine) Resume(id string) (string, []protocol.FrozenMessage, error) {
	var out rpc.ResumeResult
	if err := e.request(rpc.Resume, rpc.ResumeParams{ID: id}, &out); err != nil {
		return "", nil, err
	}
	return out.ID, out.History, nil
}

func (e *RemoteEngine) ListSessions() ([]dialogs.SessionEntry, error) {
	var out rpc.SessionsResult
	if err := e.request(rpc.ListSessions, nil, &out); err != nil {
		return nil, err
	}
	return out.Sessions, nil
}

func (e *RemoteEngine) DeleteSession(id string) error {
	return e.request(rpc.DeleteSession, rpc.DeleteSessionParams{ID: id}, nil)
}

func (e *RemoteEngine) ListModels() ([]models.Entry, error) {
	var out rpc.ModelsResult
	if err := e.request(rpc.ListModels, nil, &out); err != nil {
		return nil, err
	}
	return out.Models, nil
}

func (e *RemoteEngine) SwitchModel(provider, model string) error {
	return e.request(rpc.SwitchModel, rpc.SwitchModelParams{Provider: provider, Model: model}, nil)
}

func (e *RemoteEngine) CycleThinking() (string, error) {
	var out rpc.LevelResult
	if err := e.request(rpc.CycleThinking, nil, &out); err != nil {
		return "", err
	}
	e.thinkingLevel = out.Level
	return out.Level, nil
}

func (e *RemoteEngine) CurrentThinkingLevel() string {
	if e.thinkingLevel != "" {
		return e.thinkingLevel
	}
	var out rpc.LevelResult
	if err := e.request(rpc.CurrentThinking, nil, &out); err != nil {
		return ""
	}
	e.thinkingLevel = out.Level
	return out.Level
}

func (e *RemoteEngine) Compact(focus string) error {
	return e.request(rpc.Compact, rpc.CompactParams{Focus: focus}, nil)
}

func (e *RemoteEngine) Stats() (int, int, int, float64, error) {
	var out rpc.StatsResult
	if err := e.request(rpc.Stats, nil, &out); err != nil {
		return 0, 0, 0, 0, err
	}
	return out.Input, out.Output, out.CacheMiss, out.Cost, nil
}

func (e *RemoteEngine) LoginProviders() ([]dialogs.LoginProvider, error) {
	var out rpc.LoginProvidersResult
	if err := e.request(rpc.LoginProviders, nil, &out); err != nil {
		return nil, err
	}
	return out.Providers, nil
}

func (e *RemoteEngine) Login(provider, key string) error {
	return e.request(rpc.Login, rpc.LoginParams{Provider: provider, Key: key}, nil)
}

func (e *RemoteEngine) MCPStatus() (string, error) {
	var out rpc.StatusResult
	if err := e.request(rpc.MCPStatus, nil, &out); err != nil {
		return "", err
	}
	return out.Status, nil
}

func (e *RemoteEngine) MCPCount() int {
	var out rpc.CountResult
	if err := e.request(rpc.MCPCount, nil, &out); err != nil {
		return 0
	}
	return out.Count
}

func (e *RemoteEngine) CancelSubagent(id string) {
	_ = e.request(rpc.CancelSubagent, rpc.CancelSubagentParams{ID: id}, nil)
}

func (e *RemoteEngine) History() ([]protocol.FrozenMessage, error) {
	var out rpc.HistoryResult
	if err := e.request(rpc.History, nil, &out); err != nil {
		return nil, err
	}
	return out.History, nil
}

func (e *RemoteEngine) ListFiles(prefix string) ([]string, error) {
	var out rpc.ListFilesResult
	if err := e.request(rpc.ListFiles, rpc.ListFilesParams{Prefix: prefix}, &out); err != nil {
		return nil, err
	}
	return out.Files, nil
}

func (e *RemoteEngine) ResolveInput(text string) ([]protocol.ContentBlock, string, error) {
	var out rpc.ResolveInputResult
	if err := e.request(rpc.ResolveInput, rpc.ResolveInputParams{Text: text}, &out); err != nil {
		return nil, text, err
	}
	return out.Blocks, out.Display, nil
}

func (e *RemoteEngine) PushInstruction() (string, string, error) {
	var out rpc.PushInstructionResult
	if err := e.request(rpc.PushInstruction, nil, &out); err != nil {
		return "", "", err
	}
	return out.Message, out.Notice, nil
}

func (e *RemoteEngine) Events() <-chan protocol.Event {
	return e.events
}

func (e *RemoteEngine) Close() {}

func (e *RemoteEngine) request(method string, params any, out any) error {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	raw, err := e.conn.Request(ctx, method, params)
	if err != nil {
		return err
	}
	if out == nil {
		return nil
	}
	if len(raw) == 0 {
		return fmt.Errorf("%s returned empty result", method)
	}
	return json.Unmarshal(raw, out)
}

func blocksText(blocks []protocol.ContentBlock) string {
	parts := make([]string, 0, len(blocks))
	for _, b := range blocks {
		if b.Type == protocol.BlockText && b.Text != "" {
			parts = append(parts, b.Text)
		}
	}
	return strings.Join(parts, "\n")
}
