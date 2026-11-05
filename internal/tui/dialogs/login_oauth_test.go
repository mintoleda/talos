package dialogs

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/mintoleda/talos/internal/provider/codex"
)

func testJWT(accountID string) string {
	payload, _ := json.Marshal(map[string]any{
		"https://api.openai.com/auth": map[string]any{"chatgpt_account_id": accountID},
	})
	return "e30." + base64.RawURLEncoding.EncodeToString(payload) + ".sig"
}

// fakeAuth serves the token and device endpoints of auth.openai.com.
func fakeAuth(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/oauth/token":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"access_token": testJWT("acct_tui"), "refresh_token": "r", "expires_in": 3600,
			})
		case "/api/accounts/deviceauth/usercode":
			fmt.Fprint(w, `{"device_auth_id":"d","user_code":"WXYZ-0000","interval":1}`)
		case "/api/accounts/deviceauth/token":
			fmt.Fprint(w, `{"authorization_code":"c","code_verifier":"v"}`)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

func oauthDialog(t *testing.T) *LoginDialog {
	t.Helper()
	srv := fakeAuth(t)
	d := NewLoginDialog([]LoginProvider{{Name: "openai-codex", Label: "chatgpt.com", OAuth: true}})
	d.width, d.height = 120, 40
	o := codex.NewOAuth()
	o.AuthBaseURL = srv.URL
	o.CallbackAddr = "127.0.0.1:0"
	d.oauth = o

	prev := openBrowser
	openBrowser = func(string) error { return nil }
	t.Cleanup(func() { openBrowser = prev; d.stopOAuth() })
	return d
}

func key(k tea.KeyType) tea.KeyMsg { return tea.KeyMsg{Type: k} }

// runCmd executes cmd, expanding batches, and returns the produced messages.
func runCmd(cmd tea.Cmd) []tea.Msg {
	if cmd == nil {
		return nil
	}
	msg := cmd()
	if batch, ok := msg.(tea.BatchMsg); ok {
		var out []tea.Msg
		for _, c := range batch {
			out = append(out, runCmd(c)...)
		}
		return out
	}
	if msg == nil {
		return nil
	}
	return []tea.Msg{msg}
}

func find[T any](msgs []tea.Msg) (T, bool) {
	for _, m := range msgs {
		if v, ok := m.(T); ok {
			return v, true
		}
	}
	var zero T
	return zero, false
}

func TestLoginDialogOAuthProviderShowsMethods(t *testing.T) {
	d := oauthDialog(t)
	d.Update(key(tea.KeyEnter))
	if d.step != loginStepMethod {
		t.Fatalf("step = %v, want method", d.step)
	}
	view := d.View()
	if !contains(view, "Browser login") || !contains(view, "Device code login") {
		t.Fatal("method choices missing from view")
	}
	d.Update(key(tea.KeyDown))
	if d.method != oauthDevice {
		t.Fatal("down should select device code")
	}
	d.Update(key(tea.KeyEsc))
	if d.step != loginStepPick {
		t.Fatal("esc should go back to the provider list")
	}
}

func TestLoginDialogBrowserFlowWithPastedRedirect(t *testing.T) {
	d := oauthDialog(t)
	var opened string
	openBrowser = func(u string) error { opened = u; return nil }

	d.Update(key(tea.KeyEnter)) // pick → method
	_, cmd := d.Update(key(tea.KeyEnter))
	started, ok := find[oauthStartedMsg](runCmd(cmd))
	if !ok || started.err != nil {
		t.Fatalf("start = %+v", started)
	}
	_, waitCmd := d.Update(started)
	if d.browser == nil || d.step != loginStepOAuth {
		t.Fatal("browser flow not in progress")
	}
	if !contains(strings.ReplaceAll(d.View(), "\n", ""), "client_id=") {
		t.Fatal("authorize URL not shown")
	}

	state := mustQuery(t, d.browser.URL, "state")
	d.pasteInput.SetValue("http://localhost:1455/auth/callback?code=pasted&state=" + state)
	d.Update(key(tea.KeyEnter))

	msgs := runCmd(waitCmd)
	if opened != d.browser.URL {
		t.Fatalf("browser opened %q", opened)
	}
	result, ok := find[oauthResultMsg](msgs)
	if !ok {
		t.Fatalf("no result in %v", msgs)
	}
	_, doneCmd := d.Update(result)
	if !d.done {
		t.Fatalf("dialog not done (err=%q)", d.errText)
	}
	done, ok := doneCmd().(LoginDoneMsg)
	if !ok || done.Provider != "openai-codex" || done.OAuth == nil || done.OAuth.AccountID != "acct_tui" || done.Key != "" {
		t.Fatalf("done = %+v", done)
	}
}

func TestLoginDialogBrowserFlowCancel(t *testing.T) {
	d := oauthDialog(t)
	d.Update(key(tea.KeyEnter))
	_, cmd := d.Update(key(tea.KeyEnter))
	started, _ := find[oauthStartedMsg](runCmd(cmd))
	_, waitCmd := d.Update(started)

	d.Update(key(tea.KeyEsc))
	if d.step != loginStepMethod || d.browser != nil {
		t.Fatal("esc should cancel the flow and return to method selection")
	}
	// The pending wait returns promptly with a cancellation that is ignored.
	result, ok := find[oauthResultMsg](runCmd(waitCmd))
	if !ok || result.err == nil {
		t.Fatalf("result = %+v", result)
	}
	if _, cmd := d.Update(result); cmd != nil || d.done {
		t.Fatal("cancelled result must be ignored")
	}
}

func TestLoginDialogDeviceFlow(t *testing.T) {
	d := oauthDialog(t)
	d.Update(key(tea.KeyEnter))
	d.Update(key(tea.KeyDown))
	_, cmd := d.Update(key(tea.KeyEnter))
	started, ok := find[oauthStartedMsg](runCmd(cmd))
	if !ok || started.err != nil || started.device == nil {
		t.Fatalf("start = %+v", started)
	}
	_, waitCmd := d.Update(started)
	view := d.View()
	if !contains(view, "WXYZ-0000") || !contains(view, "/codex/device") {
		t.Fatal("device code / verification URL not shown")
	}
	result, _ := find[oauthResultMsg](runCmd(waitCmd))
	_, doneCmd := d.Update(result)
	done, ok := doneCmd().(LoginDoneMsg)
	if !ok || done.OAuth == nil || done.OAuth.AccountID != "acct_tui" {
		t.Fatalf("done = %+v", done)
	}
}

func TestLoginDialogOAuthErrorShown(t *testing.T) {
	d := oauthDialog(t)
	d.Update(key(tea.KeyEnter))
	_, cmd := d.Update(key(tea.KeyEnter))
	started, _ := find[oauthStartedMsg](runCmd(cmd))
	_, waitCmd := d.Update(started)

	d.pasteInput.SetValue("code=x&state=wrong")
	d.Update(key(tea.KeyEnter))
	result, _ := find[oauthResultMsg](runCmd(waitCmd))
	d.Update(result)
	if d.done || !contains(d.View(), "state mismatch") {
		t.Fatalf("expected error view, done=%v err=%q", d.done, d.errText)
	}
	d.Update(key(tea.KeyEsc))
	if d.step != loginStepMethod {
		t.Fatal("esc after an error should return to method selection")
	}
}

func mustQuery(t *testing.T, raw, k string) string {
	t.Helper()
	u, err := url.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	return u.Query().Get(k)
}
