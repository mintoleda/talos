package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/mintoleda/talos/internal/protocol"
)

type WebFetchConfig struct{}

type webFetch struct {
	client *http.Client
}

func NewWebFetch(_ WebFetchConfig) Tool {
	return &webFetch{
		client: &http.Client{
			Timeout: 30 * time.Second,
		},
	}
}

func (w *webFetch) Name() string { return "web_fetch" }

func (w *webFetch) Description() string {
	return "Fetch a URL and return its body as text."
}

func (w *webFetch) Schema() json.RawMessage {
	return json.RawMessage(`{
		"type": "object",
		"properties": {
			"url": {"type": "string", "description": "the URL to fetch"}
		},
		"required": ["url"]
	}`)
}

func (w *webFetch) Execute(ctx context.Context, args map[string]any) (protocol.ToolResult, error) {
	u, err := str(args, "url")
	if err != nil {
		return errResult(err), nil
	}

	req, err := http.NewRequestWithContext(ctx, "GET", u, nil)
	if err != nil {
		return errResult(fmt.Errorf("create request: %w", err)), nil
	}
	req.Header.Set("User-Agent", "Talos/1.0")

	resp, err := w.client.Do(req)
	if err != nil {
		return errResult(fmt.Errorf("fetch: %w", err)), nil
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return errResult(fmt.Errorf("read body: %w", err)), nil
	}

	if resp.StatusCode != http.StatusOK {
		return errResult(fmt.Errorf("fetch returned status %d", resp.StatusCode)), nil
	}

	maxLen := 50000
	if len(body) > maxLen {
		body = body[:maxLen]
	}

	return okResult(string(body)), nil
}
