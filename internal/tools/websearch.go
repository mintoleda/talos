package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/mintoleda/talos/internal/protocol"
)

type WebSearchConfig struct {
	SearchURL string // custom search endpoint (empty = DuckDuckGo HTML)
}

type webSearch struct {
	searchURL string
	client    *http.Client
}

func NewWebSearch(cfg WebSearchConfig) Tool {
	url := cfg.SearchURL
	if url == "" {
		url = "https://html.duckduckgo.com/html/"
	}
	return &webSearch{
		searchURL: url,
		client: &http.Client{
			Timeout: 15 * time.Second,
		},
	}
}

func (w *webSearch) Name() string { return "web_search" }

func (w *webSearch) Description() string {
	return "Search the web using DuckDuckGo (or a custom search endpoint). Returns HTML-formatted results."
}

func (w *webSearch) Schema() json.RawMessage {
	return json.RawMessage(`{
		"type": "object",
		"properties": {
			"query": {"type": "string", "description": "search query"}
		},
		"required": ["query"]
	}`)
}

func (w *webSearch) Execute(ctx context.Context, args map[string]any) (protocol.ToolResult, error) {
	q, err := str(args, "query")
	if err != nil {
		return errResult(err), nil
	}

	form := url.Values{"q": {q}}
	req, err := http.NewRequestWithContext(ctx, "POST", w.searchURL, strings.NewReader(form.Encode()))
	if err != nil {
		return errResult(fmt.Errorf("create request: %w", err)), nil
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("User-Agent", "Talos/1.0")

	resp, err := w.client.Do(req)
	if err != nil {
		return errResult(fmt.Errorf("search request: %w", err)), nil
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return errResult(fmt.Errorf("read response: %w", err)), nil
	}

	if resp.StatusCode != http.StatusOK {
		return errResult(fmt.Errorf("search returned status %d", resp.StatusCode)), nil
	}

	maxLen := 20000
	if len(body) > maxLen {
		body = body[:maxLen]
	}

	return okResult(string(body)), nil
}
