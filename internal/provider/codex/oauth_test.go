package codex

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

// fakeJWT builds an unsigned JWT whose payload carries the ChatGPT account ID.
func fakeJWT(accountID string) string {
	payload, _ := json.Marshal(map[string]any{
		jwtClaimPath: map[string]any{"chatgpt_account_id": accountID},
	})
	return "eyJhbGciOiJub25lIn0." + base64.RawURLEncoding.EncodeToString(payload) + ".sig"
}

func TestGeneratePKCE(t *testing.T) {
	v, c, err := GeneratePKCE()
	if err != nil {
		t.Fatal(err)
	}
	if len(v) != 43 {
		t.Fatalf("verifier length = %d, want 43", len(v))
	}
	sum := sha256.Sum256([]byte(v))
	if c != base64.RawURLEncoding.EncodeToString(sum[:]) {
		t.Fatal("challenge is not S256(verifier)")
	}
	v2, _, _ := GeneratePKCE()
	if v == v2 {
		t.Fatal("verifiers must be random")
	}
	s1, _ := newState()
	s2, _ := newState()
	if len(s1) != 32 || s1 == s2 {
		t.Fatalf("bad state %q / %q", s1, s2)
	}
}

func TestAuthorizeURL(t *testing.T) {
	o := NewOAuth()
	raw := o.AuthorizeURL("chal", "st")
	u, err := url.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	if u.Scheme+"://"+u.Host+u.Path != "https://auth.openai.com/oauth/authorize" {
		t.Fatalf("base = %s", raw)
	}
	want := map[string]string{
		"response_type":              "code",
		"client_id":                  "app_EMoamEEZ73f0CkXaXp7hrann",
		"redirect_uri":               "http://localhost:1455/auth/callback",
		"scope":                      "openid profile email offline_access",
		"code_challenge":             "chal",
		"code_challenge_method":      "S256",
		"state":                      "st",
		"id_token_add_organizations": "true",
		"codex_cli_simplified_flow":  "true",
		"originator":                 Originator,
	}
	q := u.Query()
	for k, v := range want {
		if q.Get(k) != v {
			t.Errorf("%s = %q, want %q", k, q.Get(k), v)
		}
	}
}

func TestParseAuthorizationInput(t *testing.T) {
	cases := []struct{ in, code, state string }{
		{"", "", ""},
		{"  http://localhost:1455/auth/callback?code=abc&state=xyz  ", "abc", "xyz"},
		{"http://localhost:1455/auth/callback?state=xyz", "", "xyz"},
		{"abc#xyz", "abc", "xyz"},
		{"code=abc&state=xyz", "abc", "xyz"},
		{"?code=abc", "abc", ""},
		{"plaincode", "plaincode", ""},
	}
	for _, c := range cases {
		code, state := ParseAuthorizationInput(c.in)
		if code != c.code || state != c.state {
			t.Errorf("Parse(%q) = (%q,%q), want (%q,%q)", c.in, code, state, c.code, c.state)
		}
	}
}

func TestAccountIDFromToken(t *testing.T) {
	id, err := AccountIDFromToken(fakeJWT("acct_1"))
	if err != nil || id != "acct_1" {
		t.Fatalf("got %q %v", id, err)
	}
	for _, bad := range []string{"", "a.b", "a.!!!.c", "e30." + base64.RawURLEncoding.EncodeToString([]byte(`{"sub":"x"}`)) + ".s"} {
		if _, err := AccountIDFromToken(bad); err == nil {
			t.Errorf("expected error for %q", bad)
		}
	}
}

// tokenServer fakes /oauth/token, recording the last form posted.
func tokenServer(t *testing.T, access string, status int) (*httptest.Server, *url.Values) {
	t.Helper()
	var last url.Values
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/oauth/token" || r.Method != http.MethodPost {
			http.NotFound(w, r)
			return
		}
		if ct := r.Header.Get("Content-Type"); ct != "application/x-www-form-urlencoded" {
			t.Errorf("content-type = %q", ct)
		}
		_ = r.ParseForm()
		last = r.PostForm
		if status != http.StatusOK {
			w.WriteHeader(status)
			_, _ = w.Write([]byte(`{"error":"invalid_grant","error_description":"bad"}`))
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"access_token": access, "refresh_token": "new-refresh", "expires_in": 3600,
		})
	}))
	t.Cleanup(srv.Close)
	return srv, &last
}

func testOAuth(base string) *OAuth {
	o := NewOAuth()
	o.AuthBaseURL = base
	o.CallbackAddr = "127.0.0.1:0"
	return o
}

func TestExchangeAndRefresh(t *testing.T) {
	srv, last := tokenServer(t, fakeJWT("acct_9"), http.StatusOK)
	o := testOAuth(srv.URL)

	before := time.Now()
	cred, err := o.Exchange(context.Background(), "the-code", "the-verifier", RedirectURI)
	if err != nil {
		t.Fatal(err)
	}
	if cred.AccountID != "acct_9" || cred.Refresh != "new-refresh" {
		t.Fatalf("cred = %+v", cred)
	}
	exp := time.UnixMilli(cred.Expires)
	if exp.Before(before.Add(59*time.Minute)) || exp.After(time.Now().Add(61*time.Minute)) {
		t.Fatalf("expires = %v", exp)
	}
	f := *last
	if f.Get("grant_type") != "authorization_code" || f.Get("code") != "the-code" ||
		f.Get("code_verifier") != "the-verifier" || f.Get("redirect_uri") != RedirectURI || f.Get("client_id") != ClientID {
		t.Fatalf("exchange form = %v", f)
	}

	if _, err := o.Refresh(context.Background(), "old-refresh"); err != nil {
		t.Fatal(err)
	}
	f = *last
	if f.Get("grant_type") != "refresh_token" || f.Get("refresh_token") != "old-refresh" || f.Get("client_id") != ClientID {
		t.Fatalf("refresh form = %v", f)
	}
}

func TestTokenErrors(t *testing.T) {
	srv, _ := tokenServer(t, fakeJWT("a"), http.StatusBadRequest)
	_, err := testOAuth(srv.URL).Refresh(context.Background(), "r")
	if err == nil || !strings.Contains(err.Error(), "invalid_grant") {
		t.Fatalf("err = %v", err)
	}

	// A token without the account claim is rejected.
	srv2, _ := tokenServer(t, "not-a-jwt", http.StatusOK)
	if _, err := testOAuth(srv2.URL).Refresh(context.Background(), "r"); err == nil {
		t.Fatal("expected error for token without account id")
	}
}

func TestBrowserLoginCallback(t *testing.T) {
	srv, last := tokenServer(t, fakeJWT("acct_b"), http.StatusOK)
	o := testOAuth(srv.URL)
	b, err := o.StartBrowserLogin(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !b.Listening {
		t.Fatal("callback server should be listening")
	}
	u, _ := url.Parse(b.URL)
	state := u.Query().Get("state")

	cbBase := "http://" + listenerAddr(t, b) + callbackPath

	// Wrong state is rejected without completing the flow.
	resp, err := http.Get(cbBase + "?code=c&state=wrong")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("wrong state status = %d", resp.StatusCode)
	}

	resp, err = http.Get(cbBase + "?code=good-code&state=" + state)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("callback status = %d", resp.StatusCode)
	}

	cred, err := b.Wait(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if cred.AccountID != "acct_b" {
		t.Fatalf("cred = %+v", cred)
	}
	f := *last
	if f.Get("code") != "good-code" || f.Get("code_verifier") != b.verifier {
		t.Fatalf("exchange form = %v", f)
	}
	// The server is shut down once Wait returns.
	if _, err := http.Get(cbBase); err == nil {
		t.Fatal("callback server still running after Wait")
	}
}

func TestBrowserLoginManualInput(t *testing.T) {
	srv, last := tokenServer(t, fakeJWT("acct_m"), http.StatusOK)
	b, err := testOAuth(srv.URL).StartBrowserLogin(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	u, _ := url.Parse(b.URL)
	state := u.Query().Get("state")

	manual := make(chan string, 1)
	manual <- "http://localhost:1455/auth/callback?code=pasted&state=" + state
	cred, err := b.Wait(context.Background(), manual)
	if err != nil {
		t.Fatal(err)
	}
	if cred.AccountID != "acct_m" || (*last).Get("code") != "pasted" {
		t.Fatalf("cred=%+v form=%v", cred, *last)
	}

	// State mismatch in pasted input fails.
	b2, _ := testOAuth(srv.URL).StartBrowserLogin(context.Background())
	manual <- "code=x&state=other"
	if _, err := b2.Wait(context.Background(), manual); err == nil || !strings.Contains(err.Error(), "state mismatch") {
		t.Fatalf("err = %v", err)
	}
}

func TestBrowserLoginCancel(t *testing.T) {
	b, err := testOAuth("http://127.0.0.1:1").StartBrowserLogin(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	cbBase := "http://" + listenerAddr(t, b) + callbackPath
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		_, err := b.Wait(ctx, nil)
		done <- err
	}()
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, ErrLoginCancelled) {
			t.Fatalf("err = %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Wait did not return after cancel")
	}
	if _, err := http.Get(cbBase); err == nil {
		t.Fatal("callback server still running after cancel")
	}
}

func TestBrowserLoginPortBusyFallsBackToPaste(t *testing.T) {
	busy := httptest.NewServer(http.NotFoundHandler())
	defer busy.Close()
	o := testOAuth("http://127.0.0.1:1")
	o.CallbackAddr = strings.TrimPrefix(busy.URL, "http://")
	b, err := o.StartBrowserLogin(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()
	if b.Listening {
		t.Fatal("expected Listening=false when the port is taken")
	}
}

func TestDeviceLogin(t *testing.T) {
	polls := 0
	var exchanged url.Values
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/accounts/deviceauth/usercode":
			var body map[string]string
			_ = json.NewDecoder(r.Body).Decode(&body)
			if body["client_id"] != ClientID {
				t.Errorf("client_id = %q", body["client_id"])
			}
			fmt.Fprint(w, `{"device_auth_id":"dev1","user_code":"ABCD-1234","interval":"0"}`)
		case "/api/accounts/deviceauth/token":
			var body map[string]string
			_ = json.NewDecoder(r.Body).Decode(&body)
			if body["device_auth_id"] != "dev1" || body["user_code"] != "ABCD-1234" {
				t.Errorf("poll body = %v", body)
			}
			polls++
			switch polls {
			case 1:
				w.WriteHeader(http.StatusForbidden)
			case 2:
				w.WriteHeader(http.StatusBadRequest)
				fmt.Fprint(w, `{"error":{"code":"deviceauth_authorization_pending"}}`)
			default:
				fmt.Fprint(w, `{"authorization_code":"devcode","code_verifier":"devverifier"}`)
			}
		case "/oauth/token":
			_ = r.ParseForm()
			exchanged = r.PostForm
			_ = json.NewEncoder(w).Encode(map[string]any{
				"access_token": fakeJWT("acct_d"), "refresh_token": "r", "expires_in": 60,
			})
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	o := testOAuth(srv.URL)
	d, err := o.StartDeviceLogin(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if d.UserCode != "ABCD-1234" || !strings.HasSuffix(d.VerificationURI, "/codex/device") {
		t.Fatalf("device = %+v", d)
	}
	d.interval = time.Millisecond
	cred, err := d.Wait(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if cred.AccountID != "acct_d" || polls != 3 {
		t.Fatalf("cred=%+v polls=%d", cred, polls)
	}
	if exchanged.Get("code") != "devcode" || exchanged.Get("code_verifier") != "devverifier" ||
		exchanged.Get("redirect_uri") != srv.URL+"/deviceauth/callback" {
		t.Fatalf("exchange form = %v", exchanged)
	}
}

func TestDeviceVerificationURIDefault(t *testing.T) {
	if DeviceVerification != "https://auth.openai.com/codex/device" {
		t.Fatal(DeviceVerification)
	}
}

func listenerAddr(t *testing.T, b *BrowserLogin) string {
	t.Helper()
	if b.addr == "" {
		t.Fatal("no listener address")
	}
	return b.addr
}

func TestBrowserLoginServerStopsOnContextCancel(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	b, err := testOAuth("http://127.0.0.1:1").StartBrowserLogin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	cbBase := "http://" + listenerAddr(t, b) + callbackPath
	cancel() // Wait is never called
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := http.Get(cbBase); err != nil {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("callback server still running after ctx cancel")
}
