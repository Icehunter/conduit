package auth

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"sync"
	"time"
)

// CallbackListener is a temporary localhost HTTP server that receives the
// OAuth provider's redirect, parses ?code & ?state, validates state for CSRF
// protection, and delivers the authorization code to a single Wait() caller.
//
// It also supports a manual-paste fallback: when the user can't reach the
// browser-redirected callback (e.g. on a remote machine), they paste the
// code into the CLI which calls SubmitManualCode.
//
// Reference: src/services/oauth/auth-code-listener.ts (TS), behavior matched
// for our tests.
type CallbackListener struct {
	srv          *http.Server
	listener     net.Listener
	callbackPath string

	mu              sync.Mutex
	expectedState   string
	resolveCh       chan string // bounded buffer 1
	rejectCh        chan error  // bounded buffer 1
	pendingResp     http.ResponseWriter
	pendingRespDone chan struct{}
	closed          bool
	waited          bool
}

// ErrAlreadyWaited is returned by a second Wait call on the same listener.
var errAlreadyWaited = errors.New("auth: Wait already called on this listener")

// NewCallbackListener binds an OS-assigned localhost port and starts serving.
// The returned listener is ready to receive a redirect immediately.
func NewCallbackListener(callbackPath string) (*CallbackListener, error) {
	return NewCallbackListenerOnAddr("127.0.0.1:0", callbackPath)
}

// NewCallbackListenerOnAddr binds the supplied local address and starts
// serving. Product-account OAuth clients sometimes require an exact registered
// localhost redirect URI, so callers can request a fixed port when needed.
func NewCallbackListenerOnAddr(addr, callbackPath string) (*CallbackListener, error) {
	if callbackPath == "" {
		callbackPath = "/callback"
	}
	if addr == "" {
		addr = "127.0.0.1:0"
	}
	ln, err := net.Listen("tcp", addr) //nolint:noctx
	if err != nil {
		return nil, fmt.Errorf("auth: bind localhost listener: %w", err)
	}
	l := &CallbackListener{
		listener:     ln,
		callbackPath: callbackPath,
		resolveCh:    make(chan string, 1),
		rejectCh:     make(chan error, 1),
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/", l.handleRequest)
	l.srv = &http.Server{
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
	}
	go func() { _ = l.srv.Serve(ln) }()
	return l, nil
}

// Port returns the OS-assigned localhost port.
func (l *CallbackListener) Port() int {
	return l.listener.Addr().(*net.TCPAddr).Port //nolint:errcheck
}

// Register sets the expected state nonce. It must be called before any
// callback request can be matched (incoming requests with a different state
// are rejected) and before SubmitManualCode. Calling Wait without a prior
// Register implicitly registers using the state argument.
func (l *CallbackListener) Register(expectedState string) error {
	if expectedState == "" {
		return errors.New("auth: Register: empty state")
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.expectedState != "" && l.expectedState != expectedState {
		return errors.New("auth: Register: already registered with a different state")
	}
	l.expectedState = expectedState
	return nil
}

// Wait blocks until the authorization code is delivered (via redirect or
// manual paste) or the context is cancelled. It implicitly Register()s the
// expected state if not already set.
//
// Wait must be called at most once per CallbackListener; a second call
// returns errAlreadyWaited immediately rather than blocking forever.
func (l *CallbackListener) Wait(ctx context.Context, expectedState string) (string, error) {
	if err := l.Register(expectedState); err != nil {
		return "", err
	}

	l.mu.Lock()
	if l.waited {
		l.mu.Unlock()
		return "", errAlreadyWaited
	}
	l.waited = true
	l.mu.Unlock()

	select {
	case code := <-l.resolveCh:
		return code, nil
	case err := <-l.rejectCh:
		return "", err
	case <-ctx.Done():
		return "", fmt.Errorf("auth: callback wait cancelled: %w", ctx.Err())
	}
}

// HasPendingResponse reports whether the listener has parked an HTTP
// response that's still waiting for SendSuccessRedirect or SendErrorRedirect.
func (l *CallbackListener) HasPendingResponse() bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.pendingResp != nil
}

// SendSuccessRedirect 302s the parked response to the given URL.
// No-op if there is no pending response.
func (l *CallbackListener) SendSuccessRedirect(url string) {
	l.completePending(url)
}

// SendErrorRedirect 302s the parked response to the given URL (typically
// the same success page, optionally with an error indicator).
func (l *CallbackListener) SendErrorRedirect(url string) {
	l.completePending(url)
}

// SubmitManualCode delivers a manually pasted authorization code to the
// pending Wait. State must match the expectedState given to Wait.
func (l *CallbackListener) SubmitManualCode(state, code string) error {
	if code == "" {
		return errors.New("auth: SubmitManualCode: empty code")
	}
	l.mu.Lock()
	if state != l.expectedState {
		l.mu.Unlock()
		return errors.New("auth: SubmitManualCode: state mismatch")
	}
	l.mu.Unlock()
	select {
	case l.resolveCh <- code:
	default:
		return errors.New("auth: SubmitManualCode: code already submitted")
	}
	return nil
}

// Close shuts down the HTTP server. Safe to call multiple times.
func (l *CallbackListener) Close() error {
	l.mu.Lock()
	if l.closed {
		l.mu.Unlock()
		return nil
	}
	l.closed = true
	l.mu.Unlock()

	// If a redirect response is parked, complete it with a 204 so the
	// browser doesn't hang.
	l.completePending("")

	ctx, cancel := context.WithTimeout(context.Background(), 1*time.Second)
	defer cancel()
	return l.srv.Shutdown(ctx)
}

// handleRequest dispatches incoming HTTP requests. Only `callbackPath`
// matches; everything else 404s. Mirrors AuthCodeListener.handleRedirect.
func (l *CallbackListener) handleRequest(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != l.callbackPath {
		http.NotFound(w, r)
		return
	}
	q := r.URL.Query()
	code := q.Get("code")
	state := q.Get("state")

	if code == "" {
		http.Error(w, "Authorization code not found", http.StatusBadRequest)
		l.fail(errors.New("auth: callback received without code"))
		return
	}

	l.mu.Lock()
	expected := l.expectedState
	l.mu.Unlock()
	if state != expected {
		http.Error(w, "Invalid state parameter", http.StatusBadRequest)
		l.fail(errors.New("auth: callback state mismatch"))
		return
	}

	// Park the response so the orchestrator can later 302 the browser to
	// the success page once it has the tokens. We need the request handler
	// to stay alive — block on a done channel that completePending closes.
	done := make(chan struct{})
	l.mu.Lock()
	l.pendingResp = w
	l.pendingRespDone = done
	l.mu.Unlock()

	select {
	case l.resolveCh <- code:
	default:
		// Already resolved (e.g. by a manual paste). Send a generic OK.
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
		return
	}

	<-done
}

// completePending closes out the parked response. If url is non-empty the
// browser is 302-redirected; otherwise we send a 204 No Content.
func (l *CallbackListener) completePending(url string) {
	l.mu.Lock()
	w := l.pendingResp
	done := l.pendingRespDone
	l.pendingResp = nil
	l.pendingRespDone = nil
	l.mu.Unlock()

	if w == nil {
		return
	}
	if url != "" {
		w.Header().Set("Location", url)
		w.WriteHeader(http.StatusFound)
	} else {
		w.WriteHeader(http.StatusNoContent)
	}
	close(done)
}

// fail delivers an error to the Wait caller. Idempotent.
func (l *CallbackListener) fail(err error) {
	select {
	case l.rejectCh <- err:
	default:
	}
}
