package codex

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/mintoleda/talos/internal/config"
)

// OAuth constants mirror pi's openai-codex flow (and the official Codex CLI):
// the same public client ID, scopes, loopback redirect and device endpoints.
const (
	ClientID           = "app_EMoamEEZ73f0CkXaXp7hrann"
	DefaultAuthBaseURL = "https://auth.openai.com"
	RedirectURI        = "http://localhost:1455/auth/callback"
	DeviceVerification = DefaultAuthBaseURL + "/codex/device"
	Scope              = "openid profile email offline_access"

	// Originator is sent on the authorize URL and on every Codex request.
	// The backend requires one; it identifies the client, not the user.
	Originator = "talos"

	defaultCallbackAddr = "127.0.0.1:1455"
	callbackPath        = "/auth/callback"
	jwtClaimPath        = "https://api.openai.com/auth"
	deviceCodeTimeout   = 15 * time.Minute
	minPollInterval     = time.Second
	slowDownIncrement   = 5 * time.Second
)

// ErrLoginCancelled is returned when the login context is cancelled.
var ErrLoginCancelled = errors.New("login cancelled")

// OAuth runs the ChatGPT sign-in flows and token refreshes. The zero value is
// not usable; call NewOAuth. Fields may be overridden (tests point them at
// httptest servers).
type OAuth struct {
	// AuthBaseURL hosts /oauth/authorize, /oauth/token and the device
	// endpoints. RedirectURI is always the fixed localhost:1455 value because
	// the client registration only allows that one.
	AuthBaseURL string
	// CallbackAddr is where the browser-flow loopback server listens.
	CallbackAddr string
	HTTP         *http.Client
}

// NewOAuth returns an OAuth configured for auth.openai.com.
func NewOAuth() *OAuth {
	return &OAuth{
		AuthBaseURL:  DefaultAuthBaseURL,
		CallbackAddr: defaultCallbackAddr,
		HTTP:         &http.Client{Timeout: 30 * time.Second},
	}
}

func (o *OAuth) tokenURL() string { return strings.TrimRight(o.AuthBaseURL, "/") + "/oauth/token" }

// ---------------------------------------------------------------------------
// PKCE, state and authorize URL
// ---------------------------------------------------------------------------

// GeneratePKCE returns a base64url verifier (32 random bytes) and its S256
// challenge.
func GeneratePKCE() (verifier, challenge string, err error) {
	var b [32]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", "", err
	}
	verifier = base64.RawURLEncoding.EncodeToString(b[:])
	sum := sha256.Sum256([]byte(verifier))
	return verifier, base64.RawURLEncoding.EncodeToString(sum[:]), nil
}

func newState() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(b[:]), nil
}

// AuthorizeURL builds the browser sign-in URL for the given PKCE challenge
// and state.
func (o *OAuth) AuthorizeURL(challenge, state string) string {
	q := url.Values{}
	q.Set("response_type", "code")
	q.Set("client_id", ClientID)
	q.Set("redirect_uri", RedirectURI)
	q.Set("scope", Scope)
	q.Set("code_challenge", challenge)
	q.Set("code_challenge_method", "S256")
	q.Set("state", state)
	q.Set("id_token_add_organizations", "true")
	q.Set("codex_cli_simplified_flow", "true")
	q.Set("originator", Originator)
	return strings.TrimRight(o.AuthBaseURL, "/") + "/oauth/authorize?" + q.Encode()
}

// ParseAuthorizationInput extracts the code (and state, when present) from
// what a user pastes: a full redirect URL, a "code#state" pair, a query
// string, or a bare code.
func ParseAuthorizationInput(input string) (code, state string) {
	value := strings.TrimSpace(input)
	if value == "" {
		return "", ""
	}
	if u, err := url.Parse(value); err == nil && u.Scheme != "" && u.Host != "" {
		q := u.Query()
		return q.Get("code"), q.Get("state")
	}
	if c, s, ok := strings.Cut(value, "#"); ok {
		return c, s
	}
	if strings.Contains(value, "code=") {
		q, err := url.ParseQuery(strings.TrimPrefix(value, "?"))
		if err == nil {
			return q.Get("code"), q.Get("state")
		}
	}
	return value, ""
}

// AccountIDFromToken reads the ChatGPT account ID from the access token's
// "https://api.openai.com/auth".chatgpt_account_id claim. The signature is
// not verified; the token is only ever sent back to OpenAI.
func AccountIDFromToken(token string) (string, error) {
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return "", errors.New("access token is not a JWT")
	}
	seg := strings.TrimRight(parts[1], "=")
	payload, err := base64.RawURLEncoding.DecodeString(seg)
	if err != nil {
		if payload, err = base64.RawStdEncoding.DecodeString(seg); err != nil {
			return "", fmt.Errorf("decode access token: %w", err)
		}
	}
	var claims map[string]json.RawMessage
	if err := json.Unmarshal(payload, &claims); err != nil {
		return "", fmt.Errorf("decode access token claims: %w", err)
	}
	var auth struct {
		ChatGPTAccountID string `json:"chatgpt_account_id"`
	}
	if raw, ok := claims[jwtClaimPath]; ok {
		_ = json.Unmarshal(raw, &auth)
	}
	if auth.ChatGPTAccountID == "" {
		return "", errors.New("access token has no ChatGPT account ID")
	}
	return auth.ChatGPTAccountID, nil
}

// ---------------------------------------------------------------------------
// Token endpoint
// ---------------------------------------------------------------------------

// Exchange trades an authorization code for credentials.
func (o *OAuth) Exchange(ctx context.Context, code, verifier, redirectURI string) (config.OAuthCredential, error) {
	form := url.Values{}
	form.Set("grant_type", "authorization_code")
	form.Set("client_id", ClientID)
	form.Set("code", code)
	form.Set("code_verifier", verifier)
	form.Set("redirect_uri", redirectURI)
	return o.token(ctx, form, "exchange")
}

// Refresh obtains new credentials from a refresh token. OpenAI rotates the
// refresh token, so the result must be persisted.
func (o *OAuth) Refresh(ctx context.Context, refreshToken string) (config.OAuthCredential, error) {
	form := url.Values{}
	form.Set("grant_type", "refresh_token")
	form.Set("refresh_token", refreshToken)
	form.Set("client_id", ClientID)
	return o.token(ctx, form, "refresh")
}

func (o *OAuth) token(ctx context.Context, form url.Values, op string) (config.OAuthCredential, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, o.tokenURL(), strings.NewReader(form.Encode()))
	if err != nil {
		return config.OAuthCredential{}, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := o.HTTP.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return config.OAuthCredential{}, ErrLoginCancelled
		}
		return config.OAuthCredential{}, fmt.Errorf("openai-codex token %s: %w", op, err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	if resp.StatusCode != http.StatusOK {
		return config.OAuthCredential{}, fmt.Errorf("openai-codex token %s failed (%d): %s", op, resp.StatusCode, oauthErrorText(body, resp.Status))
	}
	var tok struct {
		AccessToken  string   `json:"access_token"`
		RefreshToken string   `json:"refresh_token"`
		ExpiresIn    *float64 `json:"expires_in"`
	}
	if err := json.Unmarshal(body, &tok); err != nil {
		return config.OAuthCredential{}, fmt.Errorf("openai-codex token %s: decode response: %w", op, err)
	}
	// Never echo the body here: on success it contains tokens.
	if tok.AccessToken == "" || tok.RefreshToken == "" || tok.ExpiresIn == nil {
		return config.OAuthCredential{}, fmt.Errorf("openai-codex token %s response missing fields", op)
	}
	accountID, err := AccountIDFromToken(tok.AccessToken)
	if err != nil {
		return config.OAuthCredential{}, fmt.Errorf("openai-codex token %s: %w", op, err)
	}
	return config.OAuthCredential{
		Access:    tok.AccessToken,
		Refresh:   tok.RefreshToken,
		Expires:   time.Now().Add(time.Duration(*tok.ExpiresIn * float64(time.Second))).UnixMilli(),
		AccountID: accountID,
	}, nil
}

// oauthErrorText summarizes an error response without risking token leakage:
// it prefers the structured error fields and falls back to a truncated body.
func oauthErrorText(body []byte, status string) string {
	var e struct {
		Error            any    `json:"error"`
		ErrorDescription string `json:"error_description"`
	}
	if json.Unmarshal(body, &e) == nil && e.Error != nil {
		var parts []string
		switch v := e.Error.(type) {
		case string:
			parts = append(parts, v)
		case map[string]any:
			if c, ok := v["code"].(string); ok && c != "" {
				parts = append(parts, c)
			}
			if m, ok := v["message"].(string); ok && m != "" {
				parts = append(parts, m)
			}
		}
		if e.ErrorDescription != "" {
			parts = append(parts, e.ErrorDescription)
		}
		if len(parts) > 0 {
			return strings.Join(parts, ": ")
		}
	}
	s := strings.TrimSpace(string(body))
	if s == "" {
		return status
	}
	if len(s) > 512 {
		s = s[:512] + "…"
	}
	return s
}

// ---------------------------------------------------------------------------
// Browser flow
// ---------------------------------------------------------------------------

// BrowserLogin is an in-progress browser sign-in. Show URL to the user (and
// try to open it), then call Wait. Listening reports whether the loopback
// callback server is up; when false (port 1455 busy, e.g. the Codex CLI is
// running) the user must paste the redirect URL or code instead.
type BrowserLogin struct {
	URL       string
	Listening bool

	oauth    *OAuth
	state    string
	verifier string
	results  chan callbackResult
	srv      *http.Server
	addr     string        // bound callback address, when Listening
	closed   chan struct{} // closed by Close when the server was started
	once     sync.Once
}

type callbackResult struct {
	code string
	err  error
}

// StartBrowserLogin prepares PKCE/state and starts the callback server,
// which runs until Wait returns, Close is called, or ctx is cancelled.
func (o *OAuth) StartBrowserLogin(ctx context.Context) (*BrowserLogin, error) {
	if ctx.Err() != nil {
		return nil, ErrLoginCancelled
	}
	verifier, challenge, err := GeneratePKCE()
	if err != nil {
		return nil, err
	}
	state, err := newState()
	if err != nil {
		return nil, err
	}
	b := &BrowserLogin{
		URL:      o.AuthorizeURL(challenge, state),
		oauth:    o,
		state:    state,
		verifier: verifier,
	}
	addr := o.CallbackAddr
	if addr == "" {
		addr = defaultCallbackAddr
	}
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		// Port taken: fall back to the pasted redirect URL.
		return b, nil
	}
	b.results = make(chan callbackResult, 1)
	b.closed = make(chan struct{})
	b.srv = &http.Server{Handler: b.callbackHandler(), ReadHeaderTimeout: 10 * time.Second}
	b.Listening = true
	b.addr = ln.Addr().String()
	go func() { _ = b.srv.Serve(ln) }()
	// Never outlive the login: cancelling ctx shuts the server down even if
	// Wait is never called.
	go func() {
		select {
		case <-ctx.Done():
			b.Close()
		case <-b.closed:
		}
	}()
	return b, nil
}

func (b *BrowserLogin) callbackHandler() http.Handler {
	var mu sync.Mutex
	handled := false
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != callbackPath {
			writePage(w, http.StatusNotFound, "Callback route not found.", "")
			return
		}
		q := r.URL.Query()
		if q.Get("state") != b.state {
			writePage(w, http.StatusBadRequest, "State mismatch.", "")
			return
		}
		mu.Lock()
		defer mu.Unlock()
		if handled {
			writePage(w, http.StatusConflict, "This sign-in has already been handled.", "")
			return
		}
		if e := q.Get("error"); e != "" {
			desc := q.Get("error_description")
			if desc == "" {
				desc = e
			}
			handled = true
			writePage(w, http.StatusBadRequest, "OpenAI authorization failed.", desc)
			b.results <- callbackResult{err: fmt.Errorf("OpenAI authorization failed: %s", desc)}
			return
		}
		code := q.Get("code")
		if code == "" {
			writePage(w, http.StatusBadRequest, "Missing authorization code.", "")
			return
		}
		handled = true
		writePage(w, http.StatusOK, "Signed in to OpenAI. You may now close this page.", "")
		b.results <- callbackResult{code: code}
	})
}

func writePage(w http.ResponseWriter, status int, title, detail string) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	body := "<!doctype html><html><head><meta charset=\"utf-8\"><title>Talos</title></head>" +
		"<body style=\"font-family:sans-serif;margin:4em\"><h2>" + html.EscapeString(title) + "</h2>"
	if detail != "" {
		body += "<p>" + html.EscapeString(detail) + "</p>"
	}
	_, _ = io.WriteString(w, body+"</body></html>")
}

// Wait blocks until the browser hits the callback, the user submits pasted
// input on manual, or ctx is cancelled, then exchanges the code. The
// callback server is always shut down before Wait returns.
func (b *BrowserLogin) Wait(ctx context.Context, manual <-chan string) (config.OAuthCredential, error) {
	defer b.Close()
	var code string
	select {
	case <-ctx.Done():
		return config.OAuthCredential{}, ErrLoginCancelled
	case r := <-b.results:
		if r.err != nil {
			return config.OAuthCredential{}, r.err
		}
		code = r.code
	case in := <-manual:
		c, st := ParseAuthorizationInput(in)
		if st != "" && st != b.state {
			return config.OAuthCredential{}, errors.New("state mismatch")
		}
		if c == "" {
			return config.OAuthCredential{}, errors.New("missing authorization code")
		}
		code = c
	}
	return b.oauth.Exchange(ctx, code, b.verifier, RedirectURI)
}

// Close shuts down the callback server. Safe to call more than once.
func (b *BrowserLogin) Close() {
	b.once.Do(func() {
		if b.srv != nil {
			_ = b.srv.Close()
			close(b.closed)
		}
	})
}

// ---------------------------------------------------------------------------
// Device-code flow
// ---------------------------------------------------------------------------

// DeviceLogin is an in-progress device-code sign-in. Show UserCode and
// VerificationURI, then call Wait.
type DeviceLogin struct {
	UserCode        string
	VerificationURI string

	oauth        *OAuth
	deviceAuthID string
	interval     time.Duration
}

// StartDeviceLogin requests a user code.
func (o *OAuth) StartDeviceLogin(ctx context.Context) (*DeviceLogin, error) {
	base := strings.TrimRight(o.AuthBaseURL, "/")
	body, _ := json.Marshal(map[string]string{"client_id": ClientID})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, base+"/api/accounts/deviceauth/usercode", strings.NewReader(string(body)))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := o.HTTP.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return nil, ErrLoginCancelled
		}
		return nil, fmt.Errorf("openai-codex device code request: %w", err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	if resp.StatusCode == http.StatusNotFound {
		return nil, errors.New("OpenAI Codex device code login is not enabled for this server; use browser login")
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("openai-codex device code request failed (%d): %s", resp.StatusCode, oauthErrorText(raw, resp.Status))
	}
	var out struct {
		DeviceAuthID string          `json:"device_auth_id"`
		UserCode     string          `json:"user_code"`
		Interval     json.RawMessage `json:"interval"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, fmt.Errorf("openai-codex device code response: %w", err)
	}
	secs, ok := parseInterval(out.Interval)
	if out.DeviceAuthID == "" || out.UserCode == "" || !ok {
		return nil, errors.New("invalid OpenAI Codex device code response")
	}
	interval := time.Duration(secs * float64(time.Second))
	if interval < minPollInterval {
		interval = minPollInterval
	}
	verification := DeviceVerification
	if base != DefaultAuthBaseURL {
		verification = base + "/codex/device"
	}
	return &DeviceLogin{
		UserCode:        out.UserCode,
		VerificationURI: verification,
		oauth:           o,
		deviceAuthID:    out.DeviceAuthID,
		interval:        interval,
	}, nil
}

// parseInterval accepts the interval as a JSON number or numeric string.
func parseInterval(raw json.RawMessage) (float64, bool) {
	if len(raw) == 0 {
		return 0, false
	}
	var n float64
	if json.Unmarshal(raw, &n) != nil {
		var s string
		if json.Unmarshal(raw, &s) != nil {
			return 0, false
		}
		v, err := strconv.ParseFloat(strings.TrimSpace(s), 64)
		if err != nil {
			return 0, false
		}
		n = v
	}
	return n, n >= 0
}

// Wait polls until the user approves the code, then exchanges it.
func (d *DeviceLogin) Wait(ctx context.Context) (config.OAuthCredential, error) {
	deadline := time.Now().Add(deviceCodeTimeout)
	interval := d.interval
	slowDowns := 0
	for time.Now().Before(deadline) {
		if ctx.Err() != nil {
			return config.OAuthCredential{}, ErrLoginCancelled
		}
		status, code, verifier, err := d.poll(ctx)
		if err != nil {
			return config.OAuthCredential{}, err
		}
		switch status {
		case pollComplete:
			redirect := strings.TrimRight(d.oauth.AuthBaseURL, "/") + "/deviceauth/callback"
			return d.oauth.Exchange(ctx, code, verifier, redirect)
		case pollSlowDown:
			slowDowns++
			interval += slowDownIncrement
		}
		wait := interval
		if remaining := time.Until(deadline); remaining < wait {
			wait = remaining
		}
		if wait <= 0 {
			break
		}
		t := time.NewTimer(wait)
		select {
		case <-ctx.Done():
			t.Stop()
			return config.OAuthCredential{}, ErrLoginCancelled
		case <-t.C:
		}
	}
	if slowDowns > 0 {
		return config.OAuthCredential{}, errors.New("device flow timed out after slow_down responses; check the system clock")
	}
	return config.OAuthCredential{}, errors.New("device flow timed out")
}

type pollStatus int

const (
	pollPending pollStatus = iota
	pollSlowDown
	pollComplete
)

func (d *DeviceLogin) poll(ctx context.Context) (pollStatus, string, string, error) {
	body, _ := json.Marshal(map[string]string{"device_auth_id": d.deviceAuthID, "user_code": d.UserCode})
	endpoint := strings.TrimRight(d.oauth.AuthBaseURL, "/") + "/api/accounts/deviceauth/token"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(string(body)))
	if err != nil {
		return 0, "", "", err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := d.oauth.HTTP.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return 0, "", "", ErrLoginCancelled
		}
		return 0, "", "", fmt.Errorf("openai-codex device auth poll: %w", err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	if resp.StatusCode == http.StatusOK {
		var out struct {
			AuthorizationCode string `json:"authorization_code"`
			CodeVerifier      string `json:"code_verifier"`
		}
		if json.Unmarshal(raw, &out) != nil || out.AuthorizationCode == "" || out.CodeVerifier == "" {
			return 0, "", "", errors.New("invalid OpenAI Codex device auth token response")
		}
		return pollComplete, out.AuthorizationCode, out.CodeVerifier, nil
	}
	if resp.StatusCode == http.StatusForbidden || resp.StatusCode == http.StatusNotFound {
		return pollPending, "", "", nil
	}
	var e struct {
		Error any `json:"error"`
	}
	code := ""
	if json.Unmarshal(raw, &e) == nil {
		switch v := e.Error.(type) {
		case string:
			code = v
		case map[string]any:
			code, _ = v["code"].(string)
		}
	}
	switch code {
	case "deviceauth_authorization_pending":
		return pollPending, "", "", nil
	case "slow_down":
		return pollSlowDown, "", "", nil
	}
	return 0, "", "", fmt.Errorf("openai-codex device auth failed (%d): %s", resp.StatusCode, oauthErrorText(raw, resp.Status))
}
