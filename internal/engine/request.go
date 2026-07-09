package engine

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/mintoleda/talos/internal/client/rpc"
	"github.com/mintoleda/talos/internal/protocol"
	"github.com/mintoleda/talos/internal/session"
)

func rpcResult(v any, errs ...error) (json.RawMessage, error) {
	for _, err := range errs {
		if err != nil {
			return nil, err
		}
	}
	if v == nil {
		return json.RawMessage(`{}`), nil
	}
	return json.Marshal(v)
}

func decodeRPC[T any](raw json.RawMessage) (T, error) {
	var v T
	if len(raw) == 0 {
		return v, nil
	}
	err := json.Unmarshal(raw, &v)
	return v, err
}

// HandleRequest dispatches engine.* RPC methods for the daemon / attach path.
func (e *Engine) HandleRequest(ctx context.Context, method string, params json.RawMessage) (json.RawMessage, error) {
	switch method {
	case rpc.NewSession:
		id, err := e.NewSession()
		if err != nil {
			return nil, err
		}
		return rpcResult(struct {
			ID string `json:"id"`
		}{ID: id})
	case rpc.Resume:
		p, err := decodeRPC[rpc.ResumeParams](params)
		if err != nil {
			return nil, err
		}
		id, history, err := e.Resume(p.ID)
		if err != nil {
			return nil, err
		}
		return rpcResult(rpc.ResumeResult{ID: id, History: history})
	case rpc.ListSessions:
		sessions, err := e.ListSessions()
		if err != nil {
			return nil, err
		}
		return rpcResult(rpc.SessionsResult{Sessions: sessions})
	case rpc.DeleteSession:
		p, err := decodeRPC[rpc.DeleteSessionParams](params)
		if err != nil {
			return nil, err
		}
		return rpcResult(nil, session.DeleteSession(e.cwd, p.ID))
	case rpc.ListModels:
		models, err := e.ListModels()
		if err != nil {
			return nil, err
		}
		return rpcResult(rpc.ModelsResult{Models: models})
	case rpc.SwitchModel:
		p, err := decodeRPC[rpc.SwitchModelParams](params)
		if err != nil {
			return nil, err
		}
		if err := e.SwitchModel(p.Provider, p.Model); err != nil {
			return nil, err
		}
		e.Emit(protocol.ModelChanged{
			Provider:      e.cfg.Provider,
			Model:         e.cfg.Model,
			ThinkingLevel: e.pb.ThinkingLevel(),
		})
		return rpcResult(nil)
	case rpc.CycleThinking:
		level, err := e.CycleThinking()
		if err != nil {
			return nil, err
		}
		e.Emit(protocol.ModelChanged{
			Provider:      e.cfg.Provider,
			Model:         e.cfg.Model,
			ThinkingLevel: level,
		})
		return rpcResult(rpc.LevelResult{Level: level})
	case rpc.CurrentThinking:
		return rpcResult(rpc.LevelResult{Level: e.pb.ThinkingLevel()})
	case rpc.CyclePermissionMode:
		mode, err := e.CyclePermissionMode()
		if err != nil {
			return nil, err
		}
		e.Emit(protocol.PermissionModeChanged{Mode: mode})
		return rpcResult(rpc.LevelResult{Level: mode})
	case rpc.SetPermissionMode:
		p, err := decodeRPC[rpc.SetPermissionModeParams](params)
		if err != nil {
			return nil, err
		}
		if err := e.SetPermissionMode(p.Mode); err != nil {
			return nil, err
		}
		mode := e.PermissionMode()
		e.Emit(protocol.PermissionModeChanged{Mode: mode})
		return rpcResult(rpc.LevelResult{Level: mode})
	case rpc.PermissionMode:
		return rpcResult(rpc.LevelResult{Level: e.PermissionMode()})
	case rpc.ListCommands:
		return rpcResult(rpc.ListCommandsResult{Commands: e.ListCommands()})
	case rpc.TogglePanic:
		mode, err := e.TogglePanic()
		if err != nil {
			return nil, err
		}
		e.Emit(protocol.PermissionModeChanged{Mode: mode})
		return rpcResult(rpc.LevelResult{Level: mode})
	case rpc.WithdrawSteer:
		return rpcResult(rpc.BlocksResult{Blocks: e.WithdrawSteer()})
	case rpc.Compact:
		p, err := decodeRPC[rpc.CompactParams](params)
		if err != nil {
			return nil, err
		}
		go func() {
			compactCtx, cancel := context.WithTimeout(ctx, 120*time.Second)
			summary, err := e.lp.CompactNow(compactCtx, p.Focus)
			cancel()
			if err != nil {
				e.Emit(protocol.Notice{Level: "error", Text: "/compact failed: " + err.Error()})
			} else if summary == "" {
				e.Emit(protocol.Notice{Level: "info", Text: "nothing to compact"})
			} else {
				e.Emit(protocol.Notice{Level: "info", Text: "compacted oldest chunk - summary: " + summary})
			}
		}()
		return rpcResult(nil)
	case rpc.Stats:
		in, out, miss, cost, err := e.Stats()
		if err != nil {
			return nil, err
		}
		return rpcResult(rpc.StatsResult{
			Input:     in,
			Output:    out,
			CacheMiss: miss,
			Cost:      cost,
		})
	case rpc.LoginProviders:
		providers, err := e.LoginProviders()
		if err != nil {
			return nil, err
		}
		return rpcResult(rpc.LoginProvidersResult{Providers: providers})
	case rpc.Login:
		p, err := decodeRPC[rpc.LoginParams](params)
		if err != nil {
			return nil, err
		}
		return rpcResult(nil, e.Login(p.Provider, p.Key))
	case rpc.MCPStatus:
		status, err := e.MCPStatus()
		if err != nil {
			return nil, err
		}
		return rpcResult(rpc.StatusResult{Status: status})
	case rpc.MCPCount:
		return rpcResult(rpc.CountResult{Count: e.MCPCount()})
	case rpc.CancelSubagent:
		p, err := decodeRPC[rpc.CancelSubagentParams](params)
		if err != nil {
			return nil, err
		}
		e.CancelSubagent(p.ID)
		return rpcResult(nil)
	case rpc.History:
		history, err := e.History()
		if err != nil {
			return nil, err
		}
		return rpcResult(rpc.HistoryResult{History: history})
	case rpc.ListFiles:
		p, err := decodeRPC[rpc.ListFilesParams](params)
		if err != nil {
			return nil, err
		}
		files, err := e.ListFiles(p.Prefix)
		if err != nil {
			return nil, err
		}
		return rpcResult(rpc.ListFilesResult{Files: files})
	case rpc.ResolveInput:
		p, err := decodeRPC[rpc.ResolveInputParams](params)
		if err != nil {
			return nil, err
		}
		blocks, display, err := e.ResolveInput(p.Text)
		if err != nil {
			return nil, err
		}
		return rpcResult(rpc.ResolveInputResult{Blocks: blocks, Display: display})
	case rpc.PushInstruction:
		msg, notice, err := e.PushInstruction()
		if err != nil {
			return nil, err
		}
		return rpcResult(rpc.PushInstructionResult{Message: msg, Notice: notice})
	default:
		return nil, fmt.Errorf("unknown method %s", method)
	}
}
