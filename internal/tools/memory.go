package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/mintoleda/talos/internal/memory"
	"github.com/mintoleda/talos/internal/protocol"
)

type memoryWriteTool struct{ store *memory.Store }
type memorySearchTool struct{ store *memory.Store }
type memoryDeleteTool struct{ store *memory.Store }
type memoryUpdateTool struct{ store *memory.Store }

func NewMemoryWrite(store *memory.Store) Tool   { return &memoryWriteTool{store: store} }
func NewMemorySearch(store *memory.Store) Tool  { return &memorySearchTool{store: store} }
func NewMemoryDelete(store *memory.Store) Tool  { return &memoryDeleteTool{store: store} }
func NewMemoryUpdate(store *memory.Store) Tool  { return &memoryUpdateTool{store: store} }

func (t *memoryWriteTool) Name() string { return "memory_write" }
func (t *memoryWriteTool) Description() string {
	return "Persist a durable project memory. Use for decisions, conventions, config values, user preferences, and gotchas that should survive future sessions. Do not save session-local facts."
}
func (t *memoryWriteTool) Schema() json.RawMessage {
	return json.RawMessage(`{
		"type":"object",
		"properties":{
			"text":{"type":"string","description":"One concise durable fact."},
			"category":{"type":"string","enum":["architecture","convention","config","preference","context"]},
			"tags":{"type":"array","items":{"type":"string"}},
			"importance":{"type":"number","minimum":0,"maximum":1}
		},
		"required":["text","category"]
	}`)
}
func (t *memoryWriteTool) Execute(_ context.Context, args map[string]any) (protocol.ToolResult, error) {
	text, err := str(args, "text")
	if err != nil {
		if legacy, legacyErr := str(args, "entry"); legacyErr == nil {
			text = legacy
		} else {
			return errResult(err), nil
		}
	}
	category, _ := str(args, "category")
	importance := 0.5
	if v, ok := args["importance"].(float64); ok {
		importance = v
	}
	var tags []string
	if raw, ok := args["tags"].([]any); ok {
		for _, v := range raw {
			if s, ok := v.(string); ok && s != "" {
				tags = append(tags, s)
			}
		}
	}
	e, err := t.store.Add(memory.Entry{Category: category, Text: text, Tags: tags, Importance: importance, Source: "agent"})
	if err != nil {
		return errResult(err), nil
	}
	return okResult("memory saved: " + e.ID), nil
}

func (t *memorySearchTool) Name() string { return "memory_search" }
func (t *memorySearchTool) Description() string {
	return "Search durable project memories before exploring unfamiliar areas or when recalling prior decisions, conventions, preferences, or gotchas."
}
func (t *memorySearchTool) Schema() json.RawMessage {
	return json.RawMessage(`{"type":"object","properties":{"query":{"type":"string"},"limit":{"type":"integer","minimum":1,"maximum":20}},"required":["query"]}`)
}
func (t *memorySearchTool) Execute(_ context.Context, args map[string]any) (protocol.ToolResult, error) {
	query, err := str(args, "query")
	if err != nil {
		return errResult(err), nil
	}
	limit := 10
	if v, ok := args["limit"].(float64); ok && v > 0 {
		limit = int(v)
	}
	rows := t.store.Search(query, limit)
	if len(rows) == 0 {
		return okResult("no memories found"), nil
	}
	var b strings.Builder
	for _, e := range rows {
		fmt.Fprintf(&b, "[%s] %s: %s\n", e.ID, e.Category, e.Text)
	}
	return okResult(strings.TrimSpace(b.String())), nil
}

func (t *memoryDeleteTool) Name() string { return "memory_delete" }
func (t *memoryDeleteTool) Description() string {
	return "Delete a stale or incorrect durable memory by ID."
}
func (t *memoryDeleteTool) Schema() json.RawMessage {
	return json.RawMessage(`{"type":"object","properties":{"id":{"type":"string"}},"required":["id"]}`)
}
func (t *memoryDeleteTool) Execute(_ context.Context, args map[string]any) (protocol.ToolResult, error) {
	id, err := str(args, "id")
	if err != nil {
		return errResult(err), nil
	}
	if err := t.store.Delete(id); err != nil {
		return errResult(err), nil
	}
	return okResult("memory deleted"), nil
}

func (t *memoryUpdateTool) Name() string { return "memory_update" }
func (t *memoryUpdateTool) Description() string {
	return "Update an existing durable project memory. Only the provided fields are changed; omit fields to leave them as-is. Use this when you learn that a previously saved memory is now inaccurate, needs rewording, or its importance should change."
}
func (t *memoryUpdateTool) Schema() json.RawMessage {
	return json.RawMessage(`{
		"type":"object",
		"properties":{
			"id":{"type":"string","description":"ID of the memory to update (returned by memory_write)."},
			"text":{"type":"string","description":"New text for the memory."},
			"category":{"type":"string","enum":["architecture","convention","config","preference","context"]},
			"tags":{"type":"array","items":{"type":"string"}},
			"importance":{"type":"number","minimum":0,"maximum":1}
		},
		"required":["id"]
	}`)
}
func (t *memoryUpdateTool) Execute(_ context.Context, args map[string]any) (protocol.ToolResult, error) {
	id, err := str(args, "id")
	if err != nil {
		return errResult(err), nil
	}
	err = t.store.Update(id, func(e *memory.Entry) {
		if text, ok := args["text"].(string); ok && text != "" {
			e.Text = text
		}
		if cat, ok := args["category"].(string); ok && cat != "" {
			e.Category = cat
		}
		if imp, ok := args["importance"].(float64); ok {
			e.Importance = imp
		}
		if raw, ok := args["tags"].([]any); ok {
			tags := make([]string, 0, len(raw))
			for _, v := range raw {
				if s, ok := v.(string); ok && s != "" {
					tags = append(tags, s)
				}
			}
			if len(tags) > 0 {
				e.Tags = tags
			}
		}
	})
	if err != nil {
		return errResult(err), nil
	}
	return okResult("memory updated: " + id), nil
}
