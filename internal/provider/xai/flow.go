package xai

import (
	"context"
	"errors"
	"fmt"
	"html"
	"net"
	"net/http"
	"time"

	"github.com/icehunter/conduit/internal/auth"
)

const (
	// callbackPath must stay exactly "/callback": xAI registers the shared
	// client's loopback redirect URIs by path, with any port allowed.
	callbackPath = "/callback"

	// accountsOrigin serves the consent screen, which may hand the code to the
	// loopback server with a cross-origin fetch instead of a redirect.
	accountsOrigin = "https://accounts.x.ai"
)

// BrowserFlow is the PKCE authorization-code flow: a loopback listener on an
// OS-assigned port receives the redirect.
type BrowserFlow struct {
	authURL     string
	verifier    string
	state       string
	redirectURI string
	server      *http.Server
	result      chan callbackResult
}

type callbackResult struct {
	code, state, errCode, errDesc string
}

// StartBrowserFlow binds the loopback listener and prepares the authorization URL.
func StartBrowserFlow() (*BrowserFlow, error) {
	verifier, err := auth.GenerateVerifier()
	if err != nil {
		return nil, err
	}
	state, err := auth.GenerateState()
	if err != nil {
		return nil, err
	}
	nonce, err := auth.GenerateState()
	if err != nil {
		return nil, err
	}
	ln, err := (&net.ListenConfig{}).Listen(context.Background(), "tcp", "127.0.0.1:0")
	if err != nil {
		return nil, fmt.Errorf("xai: listen for OAuth callback: %w", err)
	}
	port := ln.Addr().(*net.TCPAddr).Port //nolint:errcheck
	f := &BrowserFlow{
		verifier:    verifier,
		state:       state,
		redirectURI: fmt.Sprintf("http://127.0.0.1:%d%s", port, callbackPath),
		result:      make(chan callbackResult, 1),
	}
	f.authURL = BuildAuthorizeURL(f.redirectURI, auth.S256Challenge(verifier), state, nonce)
	mux := http.NewServeMux()
	mux.HandleFunc(callbackPath, f.handleCallback)
	f.server = &http.Server{Handler: mux, ReadHeaderTimeout: 5 * time.Second}
	go func() { _ = f.server.Serve(ln) }()
	return f, nil
}

// URL is the authorization URL to open in a browser.
func (f *BrowserFlow) URL() string { return f.authURL }

// RedirectURI is the loopback URI presented to the authorization server.
func (f *BrowserFlow) RedirectURI() string { return f.redirectURI }

// Close stops the listener. It is safe to call more than once.
func (f *BrowserFlow) Close() {
	if f != nil && f.server != nil {
		_ = f.server.Close()
	}
}

// Wait blocks for the redirect, validates it and exchanges the code.
func (f *BrowserFlow) Wait(ctx context.Context) (*TokenResponse, error) {
	var res callbackResult
	select {
	case res = <-f.result:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	if res.errCode != "" {
		return nil, fmt.Errorf("xai: authorization failed: %s: %s", res.errCode, res.errDesc)
	}
	if res.state != f.state {
		return nil, errors.New("xai: authorization state mismatch")
	}
	if res.code == "" {
		return nil, errors.New("xai: authorization response contained no code")
	}
	return ExchangeCode(ctx, res.code, f.redirectURI, f.verifier)
}

func (f *BrowserFlow) handleCallback(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Access-Control-Allow-Origin", accountsOrigin)
	w.Header().Set("Access-Control-Allow-Private-Network", "true")
	if r.Method == http.MethodOptions {
		w.Header().Set("Access-Control-Allow-Methods", "GET, OPTIONS")
		w.WriteHeader(http.StatusNoContent)
		return
	}
	q := r.URL.Query()
	res := callbackResult{code: q.Get("code"), state: q.Get("state"), errCode: q.Get("error"), errDesc: q.Get("error_description")}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	// A request that is not this flow's own redirect (wrong state, no code) is
	// refused and never reaches Wait, so a stray hit cannot end the sign-in.
	switch {
	case res.errCode != "" && res.state == f.state:
		w.WriteHeader(http.StatusBadRequest)
		_, _ = fmt.Fprintf(w, "<html><body><h3>Grok sign-in failed</h3><p>%s</p></body></html>", html.EscapeString(res.errCode+": "+res.errDesc))
	case res.state != f.state || res.code == "":
		w.WriteHeader(http.StatusBadRequest)
		_, _ = fmt.Fprint(w, "<html><body><h3>Invalid sign-in response</h3></body></html>")
		return
	default:
		_, _ = fmt.Fprint(w, "<html><body><h3>Grok connected</h3><p>You can close this tab and return to conduit.</p></body></html>")
	}
	select {
	case f.result <- res:
	default:
	}
}
