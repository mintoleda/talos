package agents

import (
	"encoding/json"
	"testing"
)

func TestParseFrontmatter(t *testing.T) {
	src := `---
name: tester
description: Runs the tests and reports failures.
tools: [read, bash]
subagents: [scout]
model: deepseek-chat
thinking: high
---

You are Tester.

Run the tests.
`
	d, err := parse("tester.md", []byte(src), "x")
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if d.Name != "tester" {
		t.Errorf("name = %q", d.Name)
	}
	if d.Description != "Runs the tests and reports failures." {
		t.Errorf("description = %q", d.Description)
	}
	if len(d.Tools) != 2 || d.Tools[0] != "read" || d.Tools[1] != "bash" {
		t.Errorf("tools = %v", d.Tools)
	}
	if len(d.Subagents) != 1 || d.Subagents[0] != "scout" {
		t.Errorf("subagents = %v", d.Subagents)
	}
	if d.Model != "deepseek-chat" || d.Thinking != "high" {
		t.Errorf("model/thinking = %q/%q", d.Model, d.Thinking)
	}
	if d.Prompt != "You are Tester.\n\nRun the tests." {
		t.Errorf("prompt = %q", d.Prompt)
	}
}

func TestParseNoFrontmatter(t *testing.T) {
	d, err := parse("bare.md", []byte("just a body, no frontmatter"), "x")
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if d.Name != "bare" {
		t.Errorf("name fallback = %q", d.Name)
	}
	if d.Prompt != "just a body, no frontmatter" {
		t.Errorf("prompt = %q", d.Prompt)
	}
}

func TestLoadBuiltins(t *testing.T) {
	defs, err := Load(nil)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	for _, name := range []string{"scout", "researcher", "worker"} {
		if _, ok := defs[name]; !ok {
			t.Errorf("missing builtin agent %q", name)
		}
	}

	// The "who may spawn whom" rule lives in the definitions: worker may spawn
	// scout and researcher, but no builtin may spawn worker.
	worker := defs["worker"]
	if got := worker.Subagents; len(got) != 2 || got[0] != "scout" || got[1] != "researcher" {
		t.Errorf("worker.subagents = %v", got)
	}
	if !contains(worker.Tools, "bash") {
		t.Errorf("worker should have bash; tools=%v", worker.Tools)
	}
	if contains(defs["scout"].Tools, "bash") {
		t.Errorf("scout must be read-only; tools=%v", defs["scout"].Tools)
	}
	for name, d := range defs {
		if contains(d.Subagents, "worker") {
			t.Errorf("agent %q must not be able to spawn worker", name)
		}
	}
}

func TestSpawnToolsSchema(t *testing.T) {
	defs, _ := Load(nil)
	b := NewBuilder(Config{Model: "deepseek-chat"}, defs)
	got := b.SpawnTools([]string{"scout", "worker", "does-not-exist"})
	if len(got) != 2 {
		t.Fatalf("want 2 spawn tools, got %d", len(got))
	}
	if got[0].Name() != "scout" || got[1].Name() != "worker" {
		t.Errorf("names = %q, %q", got[0].Name(), got[1].Name())
	}
	var schema map[string]any
	if err := json.Unmarshal(got[0].Schema(), &schema); err != nil {
		t.Fatalf("schema not valid JSON: %v", err)
	}
	req, _ := schema["required"].([]any)
	if len(req) != 1 || req[0] != "task" {
		t.Errorf("required = %v, want [task]", req)
	}
}

func contains(s []string, v string) bool {
	for _, x := range s {
		if x == v {
			return true
		}
	}
	return false
}
