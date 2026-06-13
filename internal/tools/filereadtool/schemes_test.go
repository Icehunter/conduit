package filereadtool

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"strings"
	"testing"
)

// --- helpers ----------------------------------------------------------------

func rawInput(t *testing.T, v any) json.RawMessage {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("json.Marshal: %v", err)
	}
	return b
}

// plainDialHTTPFetch returns an httpFetchFunc replacement that uses a plain
// net.Dialer (no SSRF guard), enabling tests to reach httptest servers on
// 127.0.0.1.
func plainDialHTTPFetch(ctx context.Context, rawURL, _ string) (string, error) {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.DialContext = (&net.Dialer{}).DialContext
	client := &http.Client{Transport: transport}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return "", fmt.Errorf("plainDialHTTPFetch: %w", err)
	}
	resp, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("plainDialHTTPFetch: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		return "", fmt.Errorf("plainDialHTTPFetch: HTTP %d", resp.StatusCode)
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("plainDialHTTPFetch: read: %w", err)
	}
	return string(body), nil
}

// --- dispatch table test ----------------------------------------------------

func TestDispatch_Schemes(t *testing.T) {
	cannedPR := `{"number":42,"title":"Fix the thing","state":"OPEN","url":"https://github.com/owner/repo/pull/42","body":"A fix."}`
	cannedIssue := `{"number":7,"title":"Bug report","state":"OPEN","url":"https://github.com/owner/repo/issues/7","body":"It is broken.","comments":[]}`

	// httptest server for http/https dispatch.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		_, _ = w.Write([]byte("hello from httptest"))
	}))
	defer srv.Close()

	tests := []struct {
		name        string
		filePath    string
		setupRunCmd func()
		wantHandled bool
		wantError   bool
		wantContain string
	}{
		{
			name:     "pr scheme routed to gh",
			filePath: "pr://owner/repo/42",
			setupRunCmd: func() {
				runCmdFunc = func(_ context.Context, _ string, _ ...string) ([]byte, error) {
					return []byte(cannedPR), nil
				}
			},
			wantHandled: true,
			wantContain: "Fix the thing",
		},
		{
			name:     "issue scheme routed to gh",
			filePath: "issue://owner/repo/7",
			setupRunCmd: func() {
				runCmdFunc = func(_ context.Context, _ string, _ ...string) ([]byte, error) {
					return []byte(cannedIssue), nil
				}
			},
			wantHandled: true,
			wantContain: "Bug report",
		},
		{
			name:        "http scheme routed to webfetch",
			filePath:    srv.URL, // http://127.0.0.1:PORT
			setupRunCmd: func() {},
			wantHandled: true,
			wantContain: "hello from httptest",
		},
		{
			name:        "local path not handled",
			filePath:    "/tmp/some/file.go",
			setupRunCmd: func() {},
			wantHandled: false,
		},
		{
			name:        "relative path not handled",
			filePath:    "relative/path.txt",
			setupRunCmd: func() {},
			wantHandled: false,
		},
		{
			name:        "unknown scheme falls through to local",
			filePath:    "ftp://some/path",
			setupRunCmd: func() {},
			wantHandled: false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			// Capture and restore globals per-subtest so a panicking subtest
			// cannot leave them dirty for subsequent subtests.
			origLookPath := lookPathFunc
			origRunCmd := runCmdFunc
			origFetch := httpFetchFunc
			t.Cleanup(func() {
				lookPathFunc = origLookPath
				runCmdFunc = origRunCmd
				httpFetchFunc = origFetch
			})

			// Inject gh stubs so tests don't require gh to be installed.
			lookPathFunc = func(string) (string, error) { return "/usr/bin/gh", nil }
			httpFetchFunc = plainDialHTTPFetch

			tc.setupRunCmd()
			result, handled := dispatch(context.Background(), tc.filePath)
			if handled != tc.wantHandled {
				t.Fatalf("dispatch(%q) handled=%v, want %v", tc.filePath, handled, tc.wantHandled)
			}
			if !tc.wantHandled {
				return
			}
			if tc.wantError && !result.IsError {
				t.Errorf("want IsError=true; got: %v", result.Content)
			}
			if !tc.wantError && result.IsError {
				t.Errorf("unexpected IsError; content: %s", result.Content[0].Text)
			}
			if tc.wantContain != "" {
				text := result.Content[0].Text
				if !strings.Contains(text, tc.wantContain) {
					t.Errorf("result missing %q; got:\n%s", tc.wantContain, text)
				}
			}
		})
	}
}

// --- gh-missing graceful message --------------------------------------------

func TestGHMissing_GracefulError(t *testing.T) {
	origLookPath := lookPathFunc
	t.Cleanup(func() { lookPathFunc = origLookPath })

	lookPathFunc = func(string) (string, error) {
		return "", errors.New("gh not found")
	}

	result, handled := dispatch(context.Background(), "pr://owner/repo/1")
	if !handled {
		t.Fatal("pr:// should be handled even when gh is absent")
	}
	if !result.IsError {
		t.Fatal("expected IsError=true when gh is missing")
	}
	if !strings.Contains(result.Content[0].Text, "gh CLI not found") {
		t.Errorf("expected 'gh CLI not found' in error; got: %s", result.Content[0].Text)
	}
}

// --- pr:// parsing edge cases -----------------------------------------------

func TestPRDispatch_Parsing(t *testing.T) {
	origLookPath := lookPathFunc
	origRunCmd := runCmdFunc
	t.Cleanup(func() {
		lookPathFunc = origLookPath
		runCmdFunc = origRunCmd
	})

	lookPathFunc = func(string) (string, error) { return "/usr/bin/gh", nil }

	canned := `{"number":99,"title":"Valid","state":"OPEN","url":"https://github.com/owner/repo/pull/99","body":""}`
	runCmdFunc = func(_ context.Context, _ string, _ ...string) ([]byte, error) {
		return []byte(canned), nil
	}

	tests := []struct {
		name        string
		path        string
		wantError   bool
		wantContain string
	}{
		{
			name:        "missing number segment",
			path:        "pr://owner/repo/",
			wantError:   true,
			wantContain: "invalid pr",
		},
		{
			name:        "non-numeric number",
			path:        "pr://owner/repo/abc",
			wantError:   true,
			wantContain: "invalid pr number",
		},
		{
			name:        "too few path segments",
			path:        "pr://owner/repo",
			wantError:   true,
			wantContain: "invalid pr",
		},
		{
			name:        "zero pr number",
			path:        "pr://owner/repo/0",
			wantError:   true,
			wantContain: "invalid pr number",
		},
		{
			name:      "valid pr",
			path:      "pr://owner/repo/99",
			wantError: false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			result, handled := dispatch(context.Background(), tc.path)
			if !handled {
				t.Fatal("pr:// should always be handled")
			}
			if tc.wantError && !result.IsError {
				t.Errorf("want IsError=true; got content: %v", result.Content)
			}
			if !tc.wantError && result.IsError {
				t.Errorf("unexpected error: %s", result.Content[0].Text)
			}
			if tc.wantContain != "" && !strings.Contains(result.Content[0].Text, tc.wantContain) {
				t.Errorf("expected %q in result; got: %s", tc.wantContain, result.Content[0].Text)
			}
		})
	}
}

// --- gh via injectable command runner returning canned JSON -----------------

func TestGHPR_RendersMarkdown(t *testing.T) {
	origLookPath := lookPathFunc
	origRunCmd := runCmdFunc
	t.Cleanup(func() {
		lookPathFunc = origLookPath
		runCmdFunc = origRunCmd
	})

	lookPathFunc = func(string) (string, error) { return "/usr/bin/gh", nil }
	runCmdFunc = func(_ context.Context, _ string, _ ...string) ([]byte, error) {
		return []byte(`{"number":101,"title":"My PR Title","state":"MERGED","url":"https://github.com/a/b/pull/101","body":"**Fix:** something important"}`), nil
	}

	result := handleGH(context.Background(), "a/b/101", "pr")
	if result.IsError {
		t.Fatalf("unexpected error: %s", result.Content[0].Text)
	}
	text := result.Content[0].Text
	for _, want := range []string{"# PR #101", "My PR Title", "MERGED", "something important"} {
		if !strings.Contains(text, want) {
			t.Errorf("expected %q in output; got:\n%s", want, text)
		}
	}
}

func TestGHIssue_RendersMarkdownWithComments(t *testing.T) {
	origLookPath := lookPathFunc
	origRunCmd := runCmdFunc
	t.Cleanup(func() {
		lookPathFunc = origLookPath
		runCmdFunc = origRunCmd
	})

	lookPathFunc = func(string) (string, error) { return "/usr/bin/gh", nil }
	runCmdFunc = func(_ context.Context, _ string, _ ...string) ([]byte, error) {
		return []byte(`{"number":5,"title":"Crash bug","state":"OPEN","url":"https://github.com/a/b/issues/5","body":"Steps to reproduce","comments":[{"body":"I can reproduce","author":{"login":"alice"}}]}`), nil
	}

	result := handleGH(context.Background(), "a/b/5", "issue")
	if result.IsError {
		t.Fatalf("unexpected error: %s", result.Content[0].Text)
	}
	text := result.Content[0].Text
	for _, want := range []string{"# Issue #5", "Crash bug", "Steps to reproduce", "I can reproduce", "@alice"} {
		if !strings.Contains(text, want) {
			t.Errorf("expected %q in output; got:\n%s", want, text)
		}
	}
}

// --- http via httptest server -----------------------------------------------

func TestHTTPScheme_FetchesContent(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		_, _ = w.Write([]byte("content from test server"))
	}))
	defer srv.Close()

	origFetch := httpFetchFunc
	t.Cleanup(func() { httpFetchFunc = origFetch })
	httpFetchFunc = plainDialHTTPFetch

	result := handleHTTP(context.Background(), srv.URL)
	if result.IsError {
		t.Fatalf("unexpected error: %s", result.Content[0].Text)
	}
	if !strings.Contains(result.Content[0].Text, "content from test server") {
		t.Errorf("expected content in result; got: %s", result.Content[0].Text)
	}
}

func TestHTTPScheme_ServerError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	origFetch := httpFetchFunc
	t.Cleanup(func() { httpFetchFunc = origFetch })
	httpFetchFunc = plainDialHTTPFetch

	result := handleHTTP(context.Background(), srv.URL)
	if !result.IsError {
		t.Error("expected IsError=true for 500 response")
	}
}

// --- anchors field ----------------------------------------------------------

func TestAnchors_LinePrefix(t *testing.T) {
	f, err := os.CreateTemp(t.TempDir(), "anchors-*.txt")
	if err != nil {
		t.Fatal(err)
	}
	_, _ = f.WriteString("line one\nline two\nline three\n")
	f.Close()

	tl := New()
	result, err := tl.Execute(context.Background(), rawInput(t, map[string]any{
		"file_path": f.Name(),
		"anchors":   true,
	}))
	if err != nil {
		t.Fatal(err)
	}
	if result.IsError {
		t.Fatalf("IsError: %s", result.Content[0].Text)
	}

	text := result.Content[0].Text
	lines := strings.Split(strings.TrimRight(text, "\n"), "\n")
	if len(lines) != 3 {
		t.Fatalf("expected 3 lines, got %d:\n%s", len(lines), text)
	}

	expectedContent := []string{"line one", "line two", "line three"}

	// Each line should be: <7-char-anchor> TAB <line-number> TAB <content>
	for i, line := range lines {
		parts := strings.SplitN(line, "\t", 3)
		if len(parts) < 3 {
			t.Errorf("line %d: expected 3 tab-separated fields; got %q", i+1, line)
			continue
		}
		anchor := parts[0]
		if len(anchor) != 7 {
			t.Errorf("line %d: anchor %q should be 7 chars, got %d", i+1, anchor, len(anchor))
		}
		lineNo, parseErr := strconv.Atoi(strings.TrimSpace(parts[1]))
		if parseErr != nil || lineNo != i+1 {
			t.Errorf("line %d: expected line number %d in field[1]; got %q", i+1, i+1, parts[1])
		}
		if strings.TrimRight(parts[2], "\n") != expectedContent[i] {
			t.Errorf("line %d: expected content %q; got %q", i+1, expectedContent[i], parts[2])
		}
	}
}

func TestAnchors_False_NoPrefix(t *testing.T) {
	f, err := os.CreateTemp(t.TempDir(), "noanchors-*.txt")
	if err != nil {
		t.Fatal(err)
	}
	_, _ = f.WriteString("hello\n")
	f.Close()

	tl := New()
	result, err := tl.Execute(context.Background(), rawInput(t, map[string]any{
		"file_path": f.Name(),
	}))
	if err != nil {
		t.Fatal(err)
	}
	if result.IsError {
		t.Fatalf("IsError: %s", result.Content[0].Text)
	}

	text := result.Content[0].Text
	lines := strings.Split(strings.TrimRight(text, "\n"), "\n")
	if len(lines) != 1 {
		t.Fatalf("expected 1 line, got %d", len(lines))
	}
	// Standard format: "     1\thello" — exactly 2 tab-separated parts.
	parts := strings.SplitN(lines[0], "\t", 3)
	if len(parts) != 2 {
		t.Errorf("without anchors expected 2 tab-fields; got %q", lines[0])
	}
}

// --- Execute scheme dispatch integration ------------------------------------

func TestExecute_PRScheme(t *testing.T) {
	origLookPath := lookPathFunc
	origRunCmd := runCmdFunc
	t.Cleanup(func() {
		lookPathFunc = origLookPath
		runCmdFunc = origRunCmd
	})

	lookPathFunc = func(string) (string, error) { return "/usr/bin/gh", nil }
	runCmdFunc = func(_ context.Context, _ string, _ ...string) ([]byte, error) {
		return []byte(`{"number":1,"title":"Hello PR","state":"OPEN","url":"https://github.com/x/y/pull/1","body":""}`), nil
	}

	tl := New()
	result, err := tl.Execute(context.Background(), rawInput(t, map[string]any{
		"file_path": "pr://x/y/1",
	}))
	if err != nil {
		t.Fatal(err)
	}
	if result.IsError {
		t.Fatalf("IsError: %s", result.Content[0].Text)
	}
	if !strings.Contains(result.Content[0].Text, "Hello PR") {
		t.Errorf("expected title in result; got: %s", result.Content[0].Text)
	}
}

func TestExecute_IssueScheme(t *testing.T) {
	origLookPath := lookPathFunc
	origRunCmd := runCmdFunc
	t.Cleanup(func() {
		lookPathFunc = origLookPath
		runCmdFunc = origRunCmd
	})

	lookPathFunc = func(string) (string, error) { return "/usr/bin/gh", nil }
	runCmdFunc = func(_ context.Context, _ string, _ ...string) ([]byte, error) {
		return []byte(`{"number":2,"title":"Bug","state":"OPEN","url":"https://github.com/x/y/issues/2","body":"broken","comments":[]}`), nil
	}

	tl := New()
	result, err := tl.Execute(context.Background(), rawInput(t, map[string]any{
		"file_path": "issue://x/y/2",
	}))
	if err != nil {
		t.Fatal(err)
	}
	if result.IsError {
		t.Fatalf("IsError: %s", result.Content[0].Text)
	}
	if !strings.Contains(result.Content[0].Text, "Bug") {
		t.Errorf("expected title in result; got: %s", result.Content[0].Text)
	}
}

func TestExecute_HTTPScheme(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		_, _ = w.Write([]byte("http scheme test content"))
	}))
	defer srv.Close()

	origFetch := httpFetchFunc
	t.Cleanup(func() { httpFetchFunc = origFetch })
	httpFetchFunc = plainDialHTTPFetch

	tl := New()
	result, err := tl.Execute(context.Background(), rawInput(t, map[string]any{
		"file_path": srv.URL,
	}))
	if err != nil {
		t.Fatal(err)
	}
	if result.IsError {
		t.Fatalf("IsError: %s", result.Content[0].Text)
	}
	if !strings.Contains(result.Content[0].Text, "http scheme test content") {
		t.Errorf("expected content; got: %s", result.Content[0].Text)
	}
}

func TestExecute_LocalPath(t *testing.T) {
	f, err := os.CreateTemp(t.TempDir(), "local-*.txt")
	if err != nil {
		t.Fatal(err)
	}
	_, _ = f.WriteString("local file content\n")
	f.Close()

	tl := New()
	result, err := tl.Execute(context.Background(), rawInput(t, map[string]any{
		"file_path": f.Name(),
	}))
	if err != nil {
		t.Fatal(err)
	}
	if result.IsError {
		t.Fatalf("IsError: %s", result.Content[0].Text)
	}
	if !strings.Contains(result.Content[0].Text, "local file content") {
		t.Errorf("expected content; got: %s", result.Content[0].Text)
	}
}
