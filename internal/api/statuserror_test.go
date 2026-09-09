package api

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestParseThinkingMismatch(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want *ThinkingMismatch
	}{
		{"empty", "", nil},
		{"block and kind", "block=messages.3.content.1;kind=signature_mismatch",
			&ThinkingMismatch{MessageIndex: 3, BlockIndex: 1, HasBlock: true, Kind: "signature_mismatch"}},
		{"block only", "block=messages.0.content.0",
			&ThinkingMismatch{HasBlock: true}},
		{"kind only", "kind=prefix_drift",
			&ThinkingMismatch{Kind: "prefix_drift"}},
		{"whitespace tolerated", " block = messages.12.content.4 ; kind = foo ",
			&ThinkingMismatch{MessageIndex: 12, BlockIndex: 4, HasBlock: true, Kind: "foo"}},
		{"first key wins", "block=messages.1.content.1;block=messages.9.content.9",
			&ThinkingMismatch{MessageIndex: 1, BlockIndex: 1, HasBlock: true}},
		{"malformed block, valid kind", "block=messages.x.content.1;kind=bad_sig",
			&ThinkingMismatch{Kind: "bad_sig"}},
		{"malformed block, no kind", "block=messages.1.content", nil},
		{"kind with uppercase rejected", "kind=Signature", nil},
		{"kind too long rejected", "kind=" + strings.Repeat("a", 41), nil},
		{"digits beyond 6 rejected", "block=messages.1234567.content.1", nil},
		{"oversized value", "kind=" + strings.Repeat("a", 2100), nil},
		{"garbage", "=;;=;no_equals", nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ParseThinkingMismatch(tt.in)
			if (got == nil) != (tt.want == nil) {
				t.Fatalf("ParseThinkingMismatch(%q) = %+v; want %+v", tt.in, got, tt.want)
			}
			if got != nil && *got != *tt.want {
				t.Errorf("ParseThinkingMismatch(%q) = %+v; want %+v", tt.in, *got, *tt.want)
			}
		})
	}
}

func FuzzParseThinkingMismatch(f *testing.F) {
	f.Add("block=messages.3.content.1;kind=signature_mismatch")
	f.Add("kind=x")
	f.Add("block=messages.999999.content.999999")
	f.Add(";;;===")
	f.Fuzz(func(t *testing.T, s string) {
		got := ParseThinkingMismatch(s)
		if got == nil {
			return
		}
		if !got.HasBlock && got.Kind == "" {
			t.Fatalf("non-nil result with neither block nor kind: %+v", got)
		}
		if got.HasBlock && (got.MessageIndex < 0 || got.BlockIndex < 0) {
			t.Fatalf("negative index: %+v", got)
		}
		if got.Kind != "" && (len(got.Kind) > 40 || strings.ContainsFunc(got.Kind, func(r rune) bool {
			return r != '_' && (r < 'a' || r > 'z')
		})) {
			t.Fatalf("kind escaped the [a-z_]{1,40} bound: %q", got.Kind)
		}
	})
}

func TestAPIStatusError_IsPrefixLockRejection(t *testing.T) {
	tests := []struct {
		name string
		err  *APIStatusError
		want bool
	}{
		{"nil receiver", nil, false},
		{"400 not created in this conversation", &APIStatusError{Status: 400, Message: "thinking block was Not Created In This Conversation"}, true},
		{"400 bound to a different conversation", &APIStatusError{Status: 400, Message: "signature is bound to a different conversation"}, true},
		{"400 other message", &APIStatusError{Status: 400, Message: "prompt is too long"}, false},
		{"500 with phrase", &APIStatusError{Status: 500, Message: "not created in this conversation"}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.err.IsPrefixLockRejection(); got != tt.want {
				t.Errorf("IsPrefixLockRejection() = %v; want %v", got, tt.want)
			}
		})
	}
}

// TestStreamMessage_PrefixLockErrorIsTyped — a 400 carrying the mismatch
// header must surface as *APIStatusError with the header parsed, and the
// Error() string must keep the historical "api: <code> <text>: <type>: <msg>"
// shape that substring-based callers depend on.
func TestStreamMessage_PrefixLockErrorIsTyped(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("anthropic-thinking-prefix-mismatch", "block=messages.2.content.0;kind=signature_mismatch")
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"type":"error","error":{"type":"invalid_request_error","message":"thinking block at messages.2.content.0 was not created in this conversation"}}`))
	}))
	defer srv.Close()

	c := NewClient(Config{BaseURL: srv.URL, AuthToken: "t"}, srv.Client())
	_, err := c.StreamMessage(t.Context(), &MessageRequest{Model: "m", MaxTokens: 1, Messages: []Message{{Role: "user", Content: []ContentBlock{{Type: "text", Text: "hi"}}}}})
	if err == nil {
		t.Fatal("expected error")
	}
	var se *APIStatusError
	if !errors.As(err, &se) {
		t.Fatalf("error is %T, want *APIStatusError: %v", err, err)
	}
	if se.Status != 400 || se.Type != "invalid_request_error" {
		t.Errorf("status/type = %d/%q", se.Status, se.Type)
	}
	if !se.IsPrefixLockRejection() {
		t.Error("IsPrefixLockRejection() = false")
	}
	if se.ThinkingMismatch == nil || !se.ThinkingMismatch.HasBlock || se.ThinkingMismatch.MessageIndex != 2 || se.ThinkingMismatch.Kind != "signature_mismatch" {
		t.Errorf("ThinkingMismatch = %+v", se.ThinkingMismatch)
	}
	want := "api: 400 Bad Request: invalid_request_error: thinking block at messages.2.content.0 was not created in this conversation"
	if err.Error() != want {
		t.Errorf("Error() =\n %q\nwant\n %q", err.Error(), want)
	}
}

// TestCreateMessage_NonJSONErrorBody — a non-envelope body keeps the raw text
// as Message and the flattened form without a type segment.
func TestCreateMessage_NonJSONErrorBody(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
		_, _ = w.Write([]byte("  upstream unavailable \n"))
	}))
	defer srv.Close()

	c := NewClient(Config{BaseURL: srv.URL, AuthToken: "t"}, srv.Client())
	_, err := c.CreateMessage(t.Context(), &MessageRequest{Model: "m", MaxTokens: 1, Messages: []Message{{Role: "user", Content: []ContentBlock{{Type: "text", Text: "hi"}}}}})
	var se *APIStatusError
	if !errors.As(err, &se) {
		t.Fatalf("error is %T: %v", err, err)
	}
	if se.Status != 502 || se.Type != "" || se.Message != "upstream unavailable" || se.ThinkingMismatch != nil {
		t.Errorf("unexpected: %+v", se)
	}
	if err.Error() != "api: 502 Bad Gateway: upstream unavailable" {
		t.Errorf("Error() = %q", err.Error())
	}
}
