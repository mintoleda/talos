package tools

import (
	"context"
	"testing"
)

func TestWebSearchSchema(t *testing.T) {
	tool := NewWebSearch(WebSearchConfig{SearchURL: "https://example.com/search"})
	if tool.Name() != "web_search" {
		t.Errorf("expected web_search, got %s", tool.Name())
	}
	if tool.Description() == "" {
		t.Errorf("description should not be empty")
	}
	if len(tool.Schema()) == 0 {
		t.Errorf("schema should not be empty")
	}
}

func TestWebFetchSchema(t *testing.T) {
	tool := NewWebFetch(WebFetchConfig{})
	if tool.Name() != "web_fetch" {
		t.Errorf("expected web_fetch, got %s", tool.Name())
	}
	if tool.Description() == "" {
		t.Errorf("description should not be empty")
	}
	if len(tool.Schema()) == 0 {
		t.Errorf("schema should not be empty")
	}
}

func TestWebSearchExecuteMissingArg(t *testing.T) {
	tool := NewWebSearch(WebSearchConfig{})
	result, err := tool.Execute(context.Background(), map[string]any{})
	if err != nil {
		t.Fatal(err)
	}
	if !result.IsError {
		t.Errorf("expected error for missing query")
	}
}

func TestWebFetchExecuteMissingArg(t *testing.T) {
	tool := NewWebFetch(WebFetchConfig{})
	result, err := tool.Execute(context.Background(), map[string]any{})
	if err != nil {
		t.Fatal(err)
	}
	if !result.IsError {
		t.Errorf("expected error for missing url")
	}
}
