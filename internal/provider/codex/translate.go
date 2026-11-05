package codex

import (
	"encoding/json"
	"fmt"
	"regexp"
	"slices"
	"strings"

	"github.com/mintoleda/talos/internal/jsonutil"
	"github.com/mintoleda/talos/internal/protocol"
	"github.com/mintoleda/talos/internal/provider"
)

const defaultInstructions = "You are a helpful assistant."

var emptyObjectSchema = json.RawMessage(`{"type":"object","properties":{}}`)

func buildBody(req protocol.Request, cacheKey string) ([]byte, error) {
	instructions := req.System
	if instructions == "" {
		instructions = defaultInstructions
	}
	body := responsesRequest{
		Model:             req.Model,
		Store:             false,
		Stream:            true,
		Instructions:      instructions,
		Input:             buildInput(req),
		Text:              textOptions{Verbosity: "low"},
		Include:           []string{"reasoning.encrypted_content"},
		PromptCacheKey:    cacheKey,
		ToolChoice:        "auto",
		ParallelToolCalls: true,
		Reasoning:         reasoningFor(req.Model, req.ThinkingLevel),
	}
	for _, t := range req.Tools {
		params := t.Parameters
		if len(params) == 0 {
			params = emptyObjectSchema
		}
		body.Tools = append(body.Tools, functionTool{
			Type:        "function",
			Name:        t.Name,
			Description: t.Description,
			Parameters:  params,
		})
	}
	return json.Marshal(body)
}

// reasoningFor maps talos's abstract thinking level onto Codex's reasoning
// effort, following pi's thinkingLevelMap for these models: minimal→low,
// off→"none" (or no reasoning field when the model cannot disable it).
func reasoningFor(model, level string) *reasoning {
	if level == "" || level == provider.ThinkingOff {
		if !slices.Contains(provider.SupportedLevels(model), provider.ThinkingOff) {
			return nil
		}
		return &reasoning{Effort: "none"}
	}
	effort := level
	if level == provider.ThinkingMinimal {
		effort = "low"
	}
	return &reasoning{Effort: effort, Summary: "auto"}
}

func buildInput(req protocol.Request) []inputItem {
	var items []inputItem
	for i, fm := range req.Messages {
		m := fm.Msg
		switch m.Role {
		case protocol.RoleUser:
			if parts := userParts(m.Content); len(parts) > 0 {
				items = append(items, inputItem{Role: "user", Content: parts})
			}
		case protocol.RoleAssistant:
			textIdx := 0
			for _, b := range m.Content {
				switch b.Type {
				case protocol.BlockText:
					if b.Text == "" {
						continue
					}
					// store=false: ids are only for pairing within the request,
					// so a stable synthetic one (like pi's msg_pi_N) suffices.
					id := fmt.Sprintf("msg_talos_%d", i)
					if textIdx > 0 {
						id = fmt.Sprintf("msg_talos_%d_%d", i, textIdx)
					}
					textIdx++
					items = append(items, inputItem{
						Type:   "message",
						Role:   "assistant",
						Status: "completed",
						ID:     id,
						Content: []contentPart{{
							Type:        "output_text",
							Text:        b.Text,
							Annotations: json.RawMessage(`[]`),
						}},
					})
				case protocol.BlockToolUse:
					if b.ToolUse == nil {
						continue
					}
					items = append(items, inputItem{
						Type:      "function_call",
						CallID:    normalizeCallID(b.ToolUse.ID),
						Name:      b.ToolUse.Name,
						Arguments: argsToString(b.ToolUse.Args),
					})
				}
			}
		case protocol.RoleTool:
			for _, b := range m.Content {
				if b.Type != protocol.BlockToolResult || b.ToolResult == nil {
					continue
				}
				out := b.ToolResult.Content
				if strings.TrimSpace(out) == "" {
					out = "(no tool output)"
				}
				items = append(items, inputItem{
					Type:   "function_call_output",
					CallID: normalizeCallID(b.ToolResult.ToolUseID),
					Output: &out,
				})
			}
		}
	}
	if parts := userParts(req.Volatile); len(parts) > 0 {
		items = append(items, inputItem{Role: "user", Content: parts})
	}
	return items
}

func userParts(blocks []protocol.ContentBlock) []contentPart {
	var parts []contentPart
	for _, b := range blocks {
		switch b.Type {
		case protocol.BlockText:
			if b.Text != "" {
				parts = append(parts, contentPart{Type: "input_text", Text: b.Text})
			}
		case protocol.BlockImage:
			if b.Image != nil {
				parts = append(parts, contentPart{
					Type:     "input_image",
					Detail:   "auto",
					ImageURL: "data:" + b.Image.MediaType + ";base64," + b.Image.Data,
				})
			}
		}
	}
	return parts
}

var invalidIDChars = regexp.MustCompile(`[^a-zA-Z0-9_-]`)

// normalizeCallID makes a tool-call ID acceptable to the Responses API, which
// limits call_id to 64 [a-zA-Z0-9_-] characters. IDs from other providers
// (e.g. after a mid-session /model switch) can violate that. pi-style
// "call|item" IDs keep only the call part.
func normalizeCallID(id string) string {
	id, _, _ = strings.Cut(id, "|")
	id = invalidIDChars.ReplaceAllString(id, "_")
	if len(id) > 64 {
		id = id[:64]
	}
	return strings.TrimRight(id, "_")
}

func argsToString(args map[string]any) string {
	if args == nil {
		return "{}"
	}
	b, err := jsonutil.MarshalDeterministic(args)
	if err != nil {
		return "{}"
	}
	return string(b)
}
