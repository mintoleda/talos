// Package codex implements the "OpenAI Codex (legacy)" provider: models
// served through a ChatGPT Plus/Pro subscription via OAuth, speaking the
// Responses API at chatgpt.com/backend-api/codex/responses. Wire behavior
// mirrors pi's openai-codex provider (SSE transport only).
package codex

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/mintoleda/talos/internal/config"
	"github.com/mintoleda/talos/internal/protocol"
	"github.com/mintoleda/talos/internal/provider"
	"github.com/mintoleda/talos/internal/version"
)

// refreshSkew is how close to expiry a token may get before it is refreshed
// ahead of a request.
const refreshSkew = 5 * time.Minute

// refreshMu serializes credential refreshes across every Client in the
// process, so parallel subagents sharing one login refresh only once (the
// refresh token rotates; a second concurrent refresh would fail).
var refreshMu sync.Mutex

// CredentialStore loads and persists the OAuth credential.
type CredentialStore interface {
	Load() (config.OAuthCredential, error)
	Save(config.OAuthCredential) error
}

type fileStore struct{ baseDir string }

// FileStore stores the credential in <baseDir>/auth.json under "openai-codex".
func FileStore(baseDir string) CredentialStore { return fileStore{baseDir: baseDir} }

func (s fileStore) Load() (config.OAuthCredential, error) {
	cred, ok := config.ReadOAuth(s.baseDir, ProviderName)
	if !ok {
		return config.OAuthCredential{}, errors.New("openai-codex: not logged in (run /login)")
	}
	return cred, nil
}

func (s fileStore) Save(cred config.OAuthCredential) error {
	return config.WriteOAuth(s.baseDir, ProviderName, cred)
}

type Client struct {
	baseURL  string
	store    CredentialStore
	oauth    *OAuth
	http     *http.Client
	cacheKey string
}

// New creates a Codex client reading credentials from baseDir/auth.json.
// baseURL may be empty for https://chatgpt.com/backend-api.
func New(baseURL, baseDir string) provider.Provider {
	return NewWithStore(baseURL, FileStore(baseDir), NewOAuth())
}

// NewWithStore creates a Codex client with an explicit credential store and
// OAuth endpoint configuration.
func NewWithStore(baseURL string, store CredentialStore, oauth *OAuth) *Client {
	if baseURL == "" {
		baseURL = DefaultBaseURL
	}
	return &Client{
		baseURL:  strings.TrimRight(baseURL, "/"),
		store:    store,
		oauth:    oauth,
		cacheKey: newCacheKey(),
		http: &http.Client{
			Transport: &http.Transport{
				Proxy: http.ProxyFromEnvironment,
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

// newCacheKey returns a per-client prompt_cache_key / session-id. A client
// lives for one session's provider (it is rebuilt on /model), so the key
// keeps the backend's prefix cache warm across turns, as pi's sessionId does.
func newCacheKey() string {
	var b [16]byte
	_, _ = rand.Read(b[:])
	return "talos-" + hex.EncodeToString(b[:])
}

// endpoint resolves the responses URL like pi's resolveCodexUrl.
func (c *Client) endpoint() string {
	switch {
	case strings.HasSuffix(c.baseURL, "/codex/responses"):
		return c.baseURL
	case strings.HasSuffix(c.baseURL, "/codex"):
		return c.baseURL + "/responses"
	default:
		return c.baseURL + "/codex/responses"
	}
}

// credential returns a usable credential, refreshing and persisting it when
// it expires within refreshSkew. With stale set (after a 401), it refreshes
// unless the stored token already differs from stale — another process or
// client refreshed in the meantime.
func (c *Client) credential(ctx context.Context, stale string) (config.OAuthCredential, error) {
	refreshMu.Lock()
	defer refreshMu.Unlock()

	cred, err := c.store.Load()
	if err != nil {
		return config.OAuthCredential{}, err
	}
	if stale != "" {
		if cred.Access != stale {
			return cred, nil
		}
	} else if time.Until(time.UnixMilli(cred.Expires)) > refreshSkew {
		return cred, nil
	}
	if cred.Refresh == "" {
		return config.OAuthCredential{}, errors.New("openai-codex: session expired (run /login)")
	}
	// Detach from the turn's cancellation: the refresh token rotates server
	// side, so abandoning the request midway could lose the login.
	rctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
	defer cancel()
	fresh, err := c.oauth.Refresh(rctx, cred.Refresh)
	if err != nil {
		return config.OAuthCredential{}, fmt.Errorf("%w (run /login if this persists)", err)
	}
	if err := c.store.Save(fresh); err != nil {
		return config.OAuthCredential{}, fmt.Errorf("openai-codex: save refreshed token: %w", err)
	}
	return fresh, nil
}

func (c *Client) StreamTurn(ctx context.Context, req protocol.Request) (<-chan protocol.ProviderEvent, error) {
	body, err := buildBody(req, c.cacheKey)
	if err != nil {
		return nil, fmt.Errorf("build body: %w", err)
	}
	cred, err := c.credential(ctx, "")
	if err != nil {
		return nil, err
	}
	resp, err := c.post(ctx, cred, body)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode == http.StatusUnauthorized {
		// The token may have been revoked or rotated elsewhere: refresh once.
		resp.Body.Close()
		if cred, err = c.credential(ctx, cred.Access); err != nil {
			return nil, err
		}
		if resp, err = c.post(ctx, cred, body); err != nil {
			return nil, err
		}
	}
	if resp.StatusCode != http.StatusOK {
		defer resp.Body.Close()
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 8<<10))
		return nil, fmt.Errorf("provider status %d: %s", resp.StatusCode, errorMessage(resp.StatusCode, b))
	}

	out := make(chan protocol.ProviderEvent)
	go parseStream(ctx, resp.Body, out)
	return out, nil
}

func (c *Client) post(ctx context.Context, cred config.OAuthCredential, body []byte) (*http.Response, error) {
	accountID := cred.AccountID
	if accountID == "" {
		id, err := AccountIDFromToken(cred.Access)
		if err != nil {
			return nil, fmt.Errorf("openai-codex: %w (run /login)", err)
		}
		accountID = id
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, c.endpoint(), bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	h := httpReq.Header
	h.Set("Authorization", "Bearer "+cred.Access)
	h.Set("chatgpt-account-id", accountID)
	h.Set("originator", Originator)
	h.Set("User-Agent", "talos/"+version.VERSION)
	h.Set("OpenAI-Beta", "responses=experimental")
	h.Set("Accept", "text/event-stream")
	h.Set("Content-Type", "application/json")
	h.Set("session-id", c.cacheKey)
	h.Set("x-client-request-id", c.cacheKey)

	resp, err := c.http.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("http do: %w", err)
	}
	return resp, nil
}

// errorMessage mirrors pi's parseErrorResponse: a friendly message for
// subscription usage limits, else the API's error message, else the body.
func errorMessage(status int, body []byte) string {
	var parsed struct {
		Error *struct {
			Code     string  `json:"code"`
			Type     string  `json:"type"`
			Message  string  `json:"message"`
			PlanType string  `json:"plan_type"`
			ResetsAt float64 `json:"resets_at"`
		} `json:"error"`
		Detail string `json:"detail"`
	}
	if json.Unmarshal(body, &parsed) == nil {
		if e := parsed.Error; e != nil {
			code := e.Code
			if code == "" {
				code = e.Type
			}
			if status == http.StatusTooManyRequests || strings.Contains(code, "usage_limit_reached") ||
				strings.Contains(code, "usage_not_included") || strings.Contains(code, "rate_limit_exceeded") {
				msg := "You have hit your ChatGPT usage limit"
				if e.PlanType != "" {
					msg += " (" + strings.ToLower(e.PlanType) + " plan)"
				}
				msg += "."
				if e.ResetsAt > 0 {
					mins := int(time.Until(time.Unix(int64(e.ResetsAt), 0)).Round(time.Minute).Minutes())
					if mins < 0 {
						mins = 0
					}
					msg += fmt.Sprintf(" Try again in ~%d min.", mins)
				}
				return msg
			}
			if e.Message != "" {
				return e.Message
			}
		}
		if parsed.Detail != "" {
			return parsed.Detail
		}
	}
	if s := strings.TrimSpace(string(body)); s != "" {
		return s
	}
	return http.StatusText(status)
}

// ListModels returns the static model catalogue; the subscription backend
// has no list endpoint.
func (c *Client) ListModels(context.Context) ([]string, error) {
	return ModelIDs(), nil
}
