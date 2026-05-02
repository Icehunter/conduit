package auth

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeBrowser performs the redirect against the listener instead of really
// opening a browser, so the flow can complete in a unit test.
type fakeBrowser struct {
	t      *testing.T
	client *http.Client

	mu  sync.Mutex
	got string
}

func (b *fakeBrowser) Open(authURL string) error {
	b.mu.Lock()
	b.got = authURL
	b.mu.Unlock()

	// Pretend the user authorized: parse the redirect_uri + state from the
	// auth URL and hit it with a code.
	u, err := url.Parse(authURL)
	if err != nil {
		return err
	}
	q := u.Query()
	redirect := q.Get("redirect_uri")
	state := q.Get("state")

	cb, err := url.Parse(redirect)
	if err != nil {
		return err
	}
	cbQ := cb.Query()
	cbQ.Set("code", "AUTH_CODE_FROM_FAKE_BROWSER")
	cbQ.Set("state", state)
	cb.RawQuery = cbQ.Encode()

	go func() {
		// Tiny delay to let the test's Wait() register before the request hits.
		time.Sleep(20 * time.Millisecond)
		resp, err := b.client.Get(cb.String()) //nolint:noctx
		if err != nil {
			return
		}
		_, _ = io.Copy(io.Discard, resp.Body)
		_ = resp.Body.Close()
	}()
	return nil
}

type captureDisplay struct {
	auto, manual string
	openErr      error
}

func (d *captureDisplay) Show(a, m string)              { d.auto, d.manual = a, m }
func (d *captureDisplay) BrowserOpenFailed(err error)   { d.openErr = err }

func TestLoginFlow_HappyPath(t *testing.T) {
	tokenSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		if body["grant_type"] != "authorization_code" {
			t.Errorf("grant_type = %v", body["grant_type"])
		}
		if body["code"] != "AUTH_CODE_FROM_FAKE_BROWSER" {
			t.Errorf("code = %v; want from fake browser", body["code"])
		}
		_, _ = io.WriteString(w, `{
			"access_token": "AT",
			"refresh_token": "RT",
			"expires_in": 3600,
			"token_type": "bearer",
			"scope": "user:profile user:inference"
		}`)
	}))
	defer tokenSrv.Close()

	cfg := ProdConfig
	cfg.TokenURL = tokenSrv.URL + "/v1/oauth/token"
	cfg.ClaudeAISuccessURL = "https://example.com/success-claude-ai"

	tc := NewTokenClient(cfg, tokenSrv.Client())
	browser := &fakeBrowser{t: t, client: &http.Client{
		Timeout: 2 * time.Second,
		// Don't follow the success redirect into example.com.
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}}
	display := &captureDisplay{}

	flow := &LoginFlow{
		Cfg:     cfg,
		Tokens:  tc,
		Browser: browser,
		Display: display,
	}

	tok, err := flow.Login(context.Background(), LoginOptions{
		LoginWithClaudeAI: true,
		Timeout:           3 * time.Second,
	})
	if err != nil {
		t.Fatalf("Login: %v", err)
	}
	if tok.AccessToken != "AT" || tok.RefreshToken != "RT" {
		t.Errorf("tokens = %+v", tok)
	}

	browser.mu.Lock()
	defer browser.mu.Unlock()
	if !strings.HasPrefix(browser.got, cfg.ClaudeAIAuthorizeURL) {
		t.Errorf("browser opened: %q", browser.got)
	}
	if display.auto == "" || display.manual == "" {
		t.Errorf("display didn't capture both URLs: auto=%q manual=%q", display.auto, display.manual)
	}
	if !strings.Contains(display.manual, "platform.claude.com%2Foauth%2Fcode%2Fcallback") {
		t.Errorf("manual URL missing manual redirect: %s", display.manual)
	}
}

func TestLoginFlow_TimeoutWhenNoCallback(t *testing.T) {
	tokenSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, `{"access_token":"x","refresh_token":"r","expires_in":1,"token_type":"bearer","scope":""}`)
	}))
	defer tokenSrv.Close()

	cfg := ProdConfig
	cfg.TokenURL = tokenSrv.URL + "/v1/oauth/token"
	tc := NewTokenClient(cfg, tokenSrv.Client())

	flow := &LoginFlow{Cfg: cfg, Tokens: tc} // no browser, no display

	_, err := flow.Login(context.Background(), LoginOptions{
		LoginWithClaudeAI: true,
		SkipBrowserOpen:   true,
		Timeout:           80 * time.Millisecond,
	})
	if err == nil {
		t.Fatal("expected timeout error")
	}
	if !strings.Contains(err.Error(), "deadline") && !strings.Contains(err.Error(), "context") {
		t.Errorf("err = %v; should reference deadline/context", err)
	}
}
