package anthropic

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/mintoleda/talos/internal/protocol"
	"github.com/mintoleda/talos/internal/provider"
)

type Client struct {
	baseURL string
	apiKey  string
	cfg     Config
	http    *http.Client
}

// New creates an Anthropic client. baseURL may be empty to use the default
// https://api.anthropic.com; apiKey is the x-api-key header.
func New(baseURL, apiKey string, cfg Config) provider.Provider {
	if baseURL == "" {
		baseURL = "https://api.anthropic.com"
	}
	return &Client{
		baseURL: strings.TrimRight(baseURL, "/"),
		apiKey:  apiKey,
		cfg:     cfg,
		http: &http.Client{
			Transport: &http.Transport{
				DialContext: (&net.Dialer{
					Timeout:   10 * time.Second,
					KeepAlive: 30 * time.Second,
				}).DialContext,
				MaxIdleConns:        10,
				IdleConnTimeout:     90 * time.Second,
				TLSHandshakeTimeout: 10 * time.Second,
			},
		},
	}
}

func (c *Client) StreamTurn(ctx context.Context, req protocol.Request) (<-chan protocol.ProviderEvent, error) {
	body, err := buildBody(req, c.cfg)
	if err != nil {
		return nil, fmt.Errorf("build body: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, "POST", c.baseURL+"/v1/messages", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("X-Api-Key", c.apiKey)
	httpReq.Header.Set("Accept", "text/event-stream")
	httpReq.Header.Set("Anthropic-Version", "2023-06-01")

	resp, err := c.http.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("http do: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		defer resp.Body.Close()
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 8<<10))
		return nil, fmt.Errorf("provider status %d: %s", resp.StatusCode, string(b))
	}

	out := make(chan protocol.ProviderEvent)
	go parseSSE(resp.Body, out)
	return out, nil
}

// ListModels is not supported by the Anthropic API; callers should use a
// hardcoded list or the Anthropic docs for available model IDs.
func (c *Client) ListModels(_ context.Context) ([]string, error) {
	return nil, fmt.Errorf("model listing not supported for anthropic provider")
}
