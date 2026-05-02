package auth

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

// TestCallbackListener_Success: a request to /callback with the correct
// state returns the authorization code via Wait, and the listener parks
// the response for a later success redirect (matches AuthCodeListener
// .handleSuccessRedirect in src/services/oauth/auth-code-listener.ts).
func TestCallbackListener_Success(t *testing.T) {
	l, err := NewCallbackListener("/callback")
	if err != nil {
		t.Fatalf("NewCallbackListener: %v", err)
	}
	defer l.Close()

	const state = "state-xyz"
	port := l.Port()

	resCh := make(chan string, 1)
	errCh := make(chan error, 1)
	go func() {
		code, err := l.Wait(context.Background(), state)
		if err != nil {
			errCh <- err
			return
		}
		resCh <- code
	}()

	url := fmt.Sprintf("http://localhost:%d/callback?code=THE_CODE&state=%s", port, state)
	client := &http.Client{
		Timeout: 2 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}

	// Issue the request in a goroutine because the handler will park on
	// the response until SendSuccessRedirect closes it out.
	type httpResult struct {
		resp *http.Response
		err  error
	}
	httpCh := make(chan httpResult, 1)
	go func() {
		resp, err := client.Get(url) //nolint:noctx
		httpCh <- httpResult{resp, err}
	}()

	// Wait should return as soon as the handler parks the response.
	select {
	case got := <-resCh:
		if got != "THE_CODE" {
			t.Errorf("code = %q; want THE_CODE", got)
		}
	case err := <-errCh:
		t.Fatalf("Wait err: %v", err)
	case <-time.After(2 * time.Second):
		t.Fatal("Wait timed out")
	}

	if !l.HasPendingResponse() {
		t.Error("listener should have a pending response after success")
	}

	l.SendSuccessRedirect("https://example.com/done")

	// The handler now unblocks; the redirect response arrives.
	select {
	case r := <-httpCh:
		if r.err != nil {
			t.Fatalf("client.Get: %v", r.err)
		}
		io.Copy(io.Discard, r.resp.Body) //nolint:errcheck
		r.resp.Body.Close()
		if r.resp.StatusCode != http.StatusFound {
			t.Errorf("status = %d; want 302", r.resp.StatusCode)
		}
		if loc := r.resp.Header.Get("Location"); loc != "https://example.com/done" {
			t.Errorf("Location = %q", loc)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("HTTP response did not return after SendSuccessRedirect")
	}

	if l.HasPendingResponse() {
		t.Error("pending response should clear after SendSuccessRedirect")
	}
}

func TestCallbackListener_RejectsBadState(t *testing.T) {
	l, err := NewCallbackListener("/callback")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()

	errCh := make(chan error, 1)
	go func() {
		_, err := l.Wait(context.Background(), "expected-state")
		errCh <- err
	}()

	url := fmt.Sprintf("http://localhost:%d/callback?code=c&state=WRONG", l.Port())
	resp, err := http.Get(url) //nolint:noctx,gosec
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("status = %d; want 400", resp.StatusCode)
	}

	select {
	case err := <-errCh:
		if err == nil {
			t.Fatal("expected error on bad state")
		}
		if !strings.Contains(err.Error(), "state") {
			t.Errorf("err = %v; should mention state", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Wait timed out")
	}
}

func TestCallbackListener_RejectsMissingCode(t *testing.T) {
	l, err := NewCallbackListener("/callback")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()

	errCh := make(chan error, 1)
	go func() {
		_, err := l.Wait(context.Background(), "s")
		errCh <- err
	}()

	url := fmt.Sprintf("http://localhost:%d/callback?state=s", l.Port())
	resp, err := http.Get(url) //nolint:noctx,gosec
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("status = %d; want 400", resp.StatusCode)
	}

	select {
	case err := <-errCh:
		if err == nil {
			t.Fatal("expected error on missing code")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Wait timed out")
	}
}

func TestCallbackListener_404OnOtherPaths(t *testing.T) {
	l, err := NewCallbackListener("/callback")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()

	go func() {
		// Wait will not return for this case — we'll cancel via Close.
		_, _ = l.Wait(context.Background(), "s")
	}()

	url := fmt.Sprintf("http://localhost:%d/some-other-path", l.Port())
	resp, err := http.Get(url) //nolint:noctx,gosec
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("status = %d; want 404", resp.StatusCode)
	}
}

func TestCallbackListener_ContextCancelStopsWait(t *testing.T) {
	l, err := NewCallbackListener("/callback")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()

	ctx, cancel := context.WithCancel(context.Background())
	errCh := make(chan error, 1)
	go func() {
		_, err := l.Wait(ctx, "s")
		errCh <- err
	}()

	cancel()

	select {
	case err := <-errCh:
		if err == nil {
			t.Fatal("expected error from cancelled context")
		}
		if !errors.Is(err, context.Canceled) {
			t.Errorf("err = %v; want context.Canceled in chain", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Wait did not unblock on context cancel")
	}
}

func TestCallbackListener_DoubleWaitErrors(t *testing.T) {
	l, err := NewCallbackListener("/callback")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()

	// First Wait blocks; cancel quickly.
	ctx1, cancel1 := context.WithCancel(context.Background())
	go func() {
		_, _ = l.Wait(ctx1, "S")
	}()
	// Give the first Wait time to set waited = true.
	time.Sleep(20 * time.Millisecond)

	// Second Wait should error immediately.
	_, err = l.Wait(context.Background(), "S")
	if err == nil {
		t.Fatal("expected error on second Wait")
	}
	cancel1()
}

func TestCallbackListener_ManualPaste(t *testing.T) {
	l, err := NewCallbackListener("/callback")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()

	// Register synchronously so SubmitManualCode can validate state without
	// racing the Wait goroutine.
	if err := l.Register("STATE"); err != nil {
		t.Fatal(err)
	}

	resCh := make(chan string, 1)
	errCh := make(chan error, 1)
	go func() {
		code, err := l.Wait(context.Background(), "STATE")
		if err != nil {
			errCh <- err
			return
		}
		resCh <- code
	}()

	if err := l.SubmitManualCode("STATE", "MANUAL_CODE"); err != nil {
		t.Fatalf("SubmitManualCode: %v", err)
	}

	select {
	case got := <-resCh:
		if got != "MANUAL_CODE" {
			t.Errorf("code = %q; want MANUAL_CODE", got)
		}
	case err := <-errCh:
		t.Fatalf("Wait err: %v", err)
	case <-time.After(2 * time.Second):
		t.Fatal("Wait timed out")
	}
}
