package api

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/icehunter/conduit/internal/sse"
)

// testResponse builds a bare response; hdr alternates header names and values.
func testResponse(status int, hdr ...string) *http.Response {
	h := http.Header{}
	for i := 0; i+1 < len(hdr); i += 2 {
		h.Set(hdr[i], hdr[i+1])
	}
	return &http.Response{StatusCode: status, Header: h}
}

// apiStatusErr runs an Anthropic error envelope through newStatusError, the
// constructor real responses go through.
func apiStatusErr(status int, errType, msg string, hdr ...string) *APIStatusError {
	// Marshalling a struct of strings cannot fail.
	body, _ := json.Marshal(APIErrorEnvelope{Type: "error", Error: APIError{Type: errType, Message: msg}})
	return newStatusError(testResponse(status, hdr...), body, "")
}

// rawStatusErr is apiStatusErr for a body that is not an error envelope, such
// as a local server's plain-text 500.
func rawStatusErr(status int, body string) *APIStatusError {
	return newStatusError(testResponse(status), []byte(body), "")
}

func TestClassify(t *testing.T) {
	const msgURL = "https://api.anthropic.com/v1/messages"
	transient := func(reason string) Classified { return Classified{Class: ClassTransient, Reason: reason} }
	tests := []struct {
		name      string
		err       error
		want      Classified
		retryable bool
	}{
		{"nil", nil, Classified{}, false},

		// Typed sentinels, in Classify's order.
		{"idle timeout wrapping context.Canceled",
			fmt.Errorf("api: stream: idle timeout — no data received for %s: %w: %w",
				time.Minute, ErrStreamIdleTimeout, fmt.Errorf("sse: read: %w", context.Canceled)),
			transient(ReasonIdleTimeout), true},
		{"context canceled", fmt.Errorf("api: send: %w", context.Canceled),
			Classified{Class: ClassCancelled, Reason: ReasonCancelled}, false},
		{"deadline exceeded inside url.Error", &url.Error{Op: "Post", URL: msgURL, Err: context.DeadlineExceeded},
			Classified{Class: ClassCancelled, Reason: ReasonCancelled}, false},

		// *APIStatusError.
		{"400 prefix lock",
			apiStatusErr(400, "invalid_request_error", "thinking block at messages.2.content.0 was not created in this conversation"),
			Classified{Class: ClassPrefixLock, Reason: ReasonPrefixLock, Status: 400}, false},
		{"400 tool_use without tool_result",
			apiStatusErr(400, "invalid_request_error", "messages.3: `tool_use` ids were found without `tool_result` blocks immediately after: toolu_01"),
			Classified{Class: ClassToolUseFlow, Reason: ReasonToolFlow, Status: 400}, false},
		{"400 max_tokens overflow with counts",
			apiStatusErr(400, "invalid_request_error", "input length and `max_tokens` exceed context limit: 195000 + 21333 > 200000, decrease input length or `max_tokens` and try again"),
			Classified{Class: ClassContextOverflow, Reason: ReasonMaxTokensOverflow, Status: 400,
				Overflow: &MaxTokensOverflow{Input: 195000, MaxTokens: 21333, Limit: 200000}}, false},
		{"400 max_tokens overflow without backticks or counts",
			apiStatusErr(400, "invalid_request_error", "input length and max_tokens exceed context limit"),
			Classified{Class: ClassContextOverflow, Reason: ReasonMaxTokensOverflow, Status: 400}, false},
		{"400 prompt too long with counts",
			apiStatusErr(400, "invalid_request_error", "prompt is too long: 210000 tokens > 200000 maximum"),
			Classified{Class: ClassContextOverflow, Reason: ReasonPromptTooLong, Status: 400, PromptTokens: 210000, PromptLimit: 200000}, false},
		{"400 OpenAI messages resulted in",
			apiStatusErr(400, "invalid_request_error", "This model's maximum context length is 128000 tokens. However, your messages resulted in 130532 tokens."),
			Classified{Class: ClassContextOverflow, Reason: ReasonPromptTooLong, Status: 400, PromptTokens: 130532, PromptLimit: 128000}, false},
		{"400 vLLM max_tokens above max_model_len",
			rawStatusErr(400, `{"object":"error","message":"max_tokens=16000 cannot be greater than max_model_len=8192. Please request fewer output tokens.","type":"BadRequestError","param":null,"code":400}`),
			Classified{Class: ClassContextOverflow, Reason: ReasonMaxTokensOverflow, Status: 400,
				Overflow: &MaxTokensOverflow{MaxTokens: 16000, Limit: 8192}}, false},
		{"400 vLLM output tokens plus prompt",
			apiStatusErr(400, "BadRequestError", "This model's maximum context length is 16384 tokens. However, you requested 16000 output tokens and your prompt contains 5000 input tokens, for a total of 21000 tokens. Please reduce the length of the input prompt or the number of requested output tokens."),
			Classified{Class: ClassContextOverflow, Reason: ReasonMaxTokensOverflow, Status: 400,
				Overflow: &MaxTokensOverflow{Input: 5000, MaxTokens: 16000, Limit: 16384}}, false},
		{"400 vLLM at-least input tokens",
			apiStatusErr(400, "BadRequestError", "This model's maximum context length is 8192 tokens. However, you requested 8000 output tokens and your prompt contains at least 193 input tokens, for a total of at least 8193 tokens."),
			Classified{Class: ClassContextOverflow, Reason: ReasonMaxTokensOverflow, Status: 400,
				Overflow: &MaxTokensOverflow{Input: 193, MaxTokens: 8000, Limit: 8192}}, false},
		{"400 OpenAI messages and completion split",
			apiStatusErr(400, "invalid_request_error", "This model's maximum context length is 32768 tokens. However, you requested 42950 tokens (38854 in the messages, 4096 in the completion). Please reduce the length of the messages or completion."),
			Classified{Class: ClassContextOverflow, Reason: ReasonMaxTokensOverflow, Status: 400,
				Overflow: &MaxTokensOverflow{Input: 38854, MaxTokens: 4096, Limit: 32768}}, false},
		{"400 legacy completions split",
			apiStatusErr(400, "invalid_request_error", "This model's maximum context length is 4097 tokens, however you requested 4497 tokens (771 in your prompt; 3726 for the completion). Please reduce your prompt; or completion length."),
			Classified{Class: ClassContextOverflow, Reason: ReasonMaxTokensOverflow, Status: 400,
				Overflow: &MaxTokensOverflow{Input: 771, MaxTokens: 3726, Limit: 4097}}, false},
		{"400 SGLang input and completion",
			rawStatusErr(400, "Requested token count exceeds the model's maximum context length of 131072 tokens. You requested a total of 132528 tokens: 66992 tokens from the input messages and 65536 tokens for the completion. Please reduce the number of tokens in the input messages or the completion to fit within the limit."),
			Classified{Class: ClassContextOverflow, Reason: ReasonMaxTokensOverflow, Status: 400,
				Overflow: &MaxTokensOverflow{Input: 66992, MaxTokens: 65536, Limit: 131072}}, false},
		{"400 OpenAI output cap",
			apiStatusErr(400, "invalid_request_error", "max_tokens is too large: 65536. This model supports at most 32768 completion tokens, whereas you provided 65536."),
			Classified{Class: ClassContextOverflow, Reason: ReasonMaxTokensOverflow, Status: 400,
				Overflow: &MaxTokensOverflow{MaxTokens: 65536, Limit: 32768}}, false},
		{"400 llama.cpp counted context size",
			apiStatusErr(400, "exceed_context_size_error", "request (4297 tokens) exceeds the available context size (4096 tokens), try increasing it"),
			Classified{Class: ClassContextOverflow, Reason: ReasonPromptTooLong, Status: 400, PromptTokens: 4297, PromptLimit: 4096}, false},
		{"413 saying prompt is too long keeps the counts",
			apiStatusErr(413, "invalid_request_error", "prompt is too long: 1050000 tokens > 1000000 maximum"),
			Classified{Class: ClassContextOverflow, Reason: ReasonPromptTooLong, Status: 413, PromptTokens: 1050000, PromptLimit: 1000000}, false},
		{"400 throttling saying too many tokens",
			apiStatusErr(400, "ThrottlingException", "Too many tokens, please wait before trying again."),
			Classified{Class: ClassRateLimited, Reason: ReasonRateLimit, Status: 400}, true},
		{"400 Gemini input token count",
			apiStatusErr(400, "invalid_argument", "The input token count (1196265) exceeds the maximum number of tokens allowed (1048575)."),
			Classified{Class: ClassContextOverflow, Reason: ReasonPromptTooLong, Status: 400}, false},
		{"413 request too large",
			apiStatusErr(413, "request_too_large", "Request exceeds the maximum allowed number of bytes."),
			Classified{Class: ClassContextOverflow, Reason: ReasonRequestTooLarge, Status: 413}, false},
		{"500 local n_ctx overflow beats 5xx",
			rawStatusErr(500, "the request exceeds the available context size, try increasing it"),
			Classified{Class: ClassContextOverflow, Reason: ReasonPromptTooLong, Status: 500}, false},
		{"401", apiStatusErr(401, "authentication_error", "invalid x-api-key"),
			Classified{Class: ClassAuth, Reason: ReasonAuth, Status: 401}, false},
		{"403", apiStatusErr(403, "permission_error", "OAuth token does not meet scope requirement"),
			Classified{Class: ClassAuth, Reason: ReasonAuth, Status: 403}, false},
		{"404 not found", apiStatusErr(404, "not_found_error", "model: claude-nope"),
			Classified{Class: ClassInvalidRequest, Reason: ReasonModelNotFound, Status: 404}, false},
		{"400 model does not exist",
			apiStatusErr(400, "invalid_request_error", "The model `gpt-9` does not exist or you do not have access to it."),
			Classified{Class: ClassInvalidRequest, Reason: ReasonModelNotFound, Status: 400}, false},
		{"429 with retry-after", apiStatusErr(429, "rate_limit_error", "rate limited", "retry-after", "30"),
			Classified{Class: ClassRateLimited, Reason: ReasonRateLimit, Status: 429, RetryAfter: 30 * time.Second}, true},
		{"429 retry-after of hours is reported as sent",
			apiStatusErr(429, "rate_limit_error", "weekly limit reached", "retry-after", "10800"),
			Classified{Class: ClassRateLimited, Reason: ReasonRateLimit, Status: 429, RetryAfter: 3 * time.Hour}, true},
		{"429 saying too many tokens is a throttle, not an overflow",
			apiStatusErr(429, "throttling_error", "Too many tokens, please wait before trying again."),
			Classified{Class: ClassRateLimited, Reason: ReasonRateLimit, Status: 429}, true},
		{"429 with x-should-retry false",
			apiStatusErr(429, "rate_limit_error", "spend cap reached", "x-should-retry", "false"),
			Classified{Class: ClassRateLimited, Reason: ReasonRateLimit, Status: 429, NoRetry: true}, false},
		{"529 overloaded", apiStatusErr(529, "overloaded_error", "Overloaded"),
			Classified{Class: ClassTransient, Reason: ReasonOverloaded, Status: 529}, true},
		{"503 saying overloaded", rawStatusErr(503, "The engine is currently overloaded"),
			Classified{Class: ClassTransient, Reason: ReasonOverloaded, Status: 503}, true},
		{"502 plain body", rawStatusErr(502, "upstream unavailable"),
			Classified{Class: ClassTransient, Reason: ReasonServerError, Status: 502}, true},
		{"500 api_error", apiStatusErr(500, "api_error", "Internal server error"),
			Classified{Class: ClassTransient, Reason: ReasonServerError, Status: 500}, true},
		{"408", rawStatusErr(408, "request timeout"),
			Classified{Class: ClassTransient, Reason: ReasonServerError, Status: 408}, true},
		{"409", rawStatusErr(409, "conflict"),
			Classified{Class: ClassTransient, Reason: ReasonServerError, Status: 409}, true},
		{"500 with x-should-retry false", apiStatusErr(500, "api_error", "boom", "x-should-retry", "false"),
			Classified{Class: ClassTransient, Reason: ReasonServerError, Status: 500, NoRetry: true}, false},
		{"422", rawStatusErr(422, `{"detail":"field required"}`),
			Classified{Class: ClassInvalidRequest, Reason: ReasonInvalidRequest, Status: 422}, false},
		{"400 plain invalid request", apiStatusErr(400, "invalid_request_error", "temperature: must be <= 1"),
			Classified{Class: ClassInvalidRequest, Reason: ReasonInvalidRequest, Status: 400}, false},
		{"wrapped 429", fmt.Errorf("agent: stream: %w", apiStatusErr(429, "rate_limit_error", "slow down")),
			Classified{Class: ClassRateLimited, Reason: ReasonRateLimit, Status: 429}, true},

		// *sse.Error, received after a 200.
		{"sse overloaded_error", fmt.Errorf("drain: %w", &sse.Error{Type: "overloaded_error", Message: "Overloaded"}),
			transient(ReasonOverloaded), true},
		{"sse rate_limit_error", &sse.Error{Type: "rate_limit_error", Message: "slow down"},
			Classified{Class: ClassRateLimited, Reason: ReasonRateLimit}, true},
		{"sse api_error", &sse.Error{Type: "api_error", Message: "Internal server error"},
			transient(ReasonServerError), true},
		{"sse invalid_request_error with overflow text",
			&sse.Error{Type: "invalid_request_error", Message: "This endpoint's maximum context length is 32768 tokens"},
			Classified{Class: ClassContextOverflow, Reason: ReasonPromptTooLong}, false},
		{"sse invalid_request_error", &sse.Error{Type: "invalid_request_error", Message: "bad tool schema"},
			Classified{Class: ClassInvalidRequest, Reason: ReasonInvalidRequest}, false},
		{"sse authentication_error", &sse.Error{Type: "authentication_error", Message: "token revoked"},
			Classified{Class: ClassAuth, Reason: ReasonAuth}, false},
		{"sse unknown type", &sse.Error{Type: "error", Message: `{"type":"error","message":"?"}`},
			transient(ReasonStreamError), true},

		// Stream sentinels.
		{"converter truncation", fmt.Errorf("sse: read: %w", fmt.Errorf("api: openai-compatible stream: %w", ErrStreamTruncated)),
			transient(ReasonStreamTruncated), true},
		{"sse truncation", sse.ErrTruncated, transient(ReasonStreamTruncated), true},
		{"sse line too long", sse.ErrLineTooLong,
			Classified{Class: ClassInvalidRequest, Reason: ReasonStreamError}, false},

		// Network and TLS.
		{"net.OpError inside url.Error",
			fmt.Errorf("api: send: %w", &url.Error{Op: "Post", URL: msgURL,
				Err: &net.OpError{Op: "dial", Net: "tcp", Err: errors.New("boom")}}),
			transient(ReasonNetwork), true},
		{"server hung up before responding", fmt.Errorf("api: send: %w", &url.Error{Op: "Post", URL: msgURL, Err: io.EOF}),
			transient(ReasonNetwork), true},
		{"unexpected EOF", fmt.Errorf("sse: read: %w", io.ErrUnexpectedEOF), transient(ReasonNetwork), true},
		{"bare io.EOF is not a failure class", io.EOF, Classified{}, false},
		{"unknown certificate authority",
			&url.Error{Op: "Post", URL: msgURL, Err: &tls.CertificateVerificationError{Err: x509.UnknownAuthorityError{}}},
			Classified{Class: ClassInvalidRequest, Reason: ReasonTLS}, false},
		{"hostname mismatch", fmt.Errorf("api: send: %w", x509.HostnameError{Certificate: &x509.Certificate{}, Host: "example.com"}),
			Classified{Class: ClassInvalidRequest, Reason: ReasonTLS}, false},
		{"https against a plain-http server",
			&url.Error{Op: "Post", URL: "https://localhost:8080/v1", Err: tls.RecordHeaderError{Msg: "first record does not look like a TLS handshake"}},
			Classified{Class: ClassInvalidRequest, Reason: ReasonTLS}, false},

		// String fallback, including the strings today's failover check keys on.
		{"string 429", errors.New("api: openai-compatible: 429 Too Many Requests: slow down"),
			Classified{Class: ClassRateLimited, Reason: ReasonRateLimit}, true},
		{"string overloaded", errors.New("api: provider overloaded, try again"), transient(ReasonOverloaded), true},
		{"string quota", errors.New("api: insufficient_quota: You exceeded your current quota"),
			Classified{Class: ClassRateLimited, Reason: ReasonRateLimit}, true},
		{"string 503", errors.New("api: 503 Service Unavailable: "), transient(ReasonServerError), true},
		{"string 529", errors.New("api: status 529"), transient(ReasonOverloaded), true},
		{"string tool flow", errors.New("api: invalid_request_error: messages.1: tool_use ids were found without tool_result blocks"),
			Classified{Class: ClassToolUseFlow, Reason: ReasonToolFlow}, false},
		{"string context_length_exceeded",
			errors.New(`api: 400 Bad Request: {"error":{"code":"context_length_exceeded","message":"too long"}}`),
			Classified{Class: ClassContextOverflow, Reason: ReasonPromptTooLong}, false},
		{"string prompt-too-long counts beat a 429-looking number",
			errors.New("api: invalid_request_error: prompt is too long: 429 tokens > 200 maximum"),
			Classified{Class: ClassContextOverflow, Reason: ReasonPromptTooLong, PromptTokens: 429, PromptLimit: 200}, false},
		{"string rate limit beats too many tokens",
			errors.New("litellm.RateLimitError: Too many tokens, please wait before trying again"),
			Classified{Class: ClassRateLimited, Reason: ReasonRateLimit}, true},
		{"string model not found", errors.New("api: model_not_found: The model `x` does not exist"),
			Classified{Class: ClassInvalidRequest, Reason: ReasonModelNotFound}, false},
		{"string connection reset", errors.New("read: connection reset by peer"), transient(ReasonNetwork), true},
		{"string http2 GOAWAY",
			errors.New(`http2: server sent GOAWAY and closed the connection; LastStreamID=1, ErrCode=NO_ERROR, debug=""`),
			transient(ReasonNetwork), true},
		{"string OpenAI split whose total starts with 429",
			errors.New("api: invalid_request_error: This model's maximum context length is 32768 tokens. However, you requested 42950 tokens (38854 in the messages, 4096 in the completion). Please reduce the length of the messages or completion"),
			Classified{Class: ClassContextOverflow, Reason: ReasonMaxTokensOverflow,
				Overflow: &MaxTokensOverflow{Input: 38854, MaxTokens: 4096, Limit: 32768}}, false},
		{"string llama.cpp body with n_prompt_tokens 4297",
			errors.New(`api: 400 Bad Request: {"error":{"code":400,"message":"the request exceeds the available context size, try increasing it","type":"exceed_context_size_error","n_prompt_tokens":4297,"n_ctx":4096}}`),
			Classified{Class: ClassContextOverflow, Reason: ReasonPromptTooLong, PromptTokens: 4297, PromptLimit: 4096}, false},
		{"string 40100 is not a 401", errors.New("api: max_tokens is too large: 40100"), Classified{}, false},
		{"string 52900 is not a 529", errors.New("api: max_tokens is too large: 52900"), Classified{}, false},
		{"string 50300 is not a 503", errors.New("api: max_tokens is too large: 50300"), Classified{}, false},
		{"string 1429 is not a 429", errors.New("api: request used 1429 tokens"), Classified{}, false},
		{"string 1.429 is not a 429", errors.New("api: temperature 1.429 is out of range"), Classified{}, false},
		{"string overflow wording beats a quota help link",
			errors.New(`api: 400 Bad Request: {"error":{"code":400,"message":"The input token count (1196265) exceeds the maximum number of tokens allowed (1048575).","status":"INVALID_ARGUMENT","details":[{"@type":"type.googleapis.com/google.rpc.Help","links":[{"description":"Quotas and limits","url":"https://ai.google.dev/gemini-api/docs/quota"}]}]}}`),
			Classified{Class: ClassContextOverflow, Reason: ReasonPromptTooLong}, false},
		{"string JSON code 429", errors.New(`api: 400 Bad Request: {"error":{"code":429,"message":"slow down"}}`),
			Classified{Class: ClassRateLimited, Reason: ReasonRateLimit}, true},
		{"string throttling saying too many tokens",
			errors.New("api: ThrottlingException: Too many tokens, please wait before trying again"),
			Classified{Class: ClassRateLimited, Reason: ReasonRateLimit}, true},
		{"string array index 429 is not a status",
			errors.New("api: invalid_request_error: Invalid 'messages[429].content': string too long"),
			Classified{Class: ClassUnknown}, false},
		{"string array index 401 is not auth",
			errors.New("api: invalid_request_error: Invalid 'messages[401].content': string too long"),
			Classified{Class: ClassUnknown}, false},
		{"string explicit 429 beats context wording",
			errors.New(`api: 429 Too Many Requests: {"error":{"message":"Rate limit exceeded: context length 50000 exceeds your per-minute budget"}}`),
			Classified{Class: ClassRateLimited, Reason: ReasonRateLimit}, true},
		{"string vllm 0.10-0.17 max_tokens wording",
			errors.New("api: 400: 'max_tokens' or 'max_completion_tokens' is too large: 16000. This model's maximum context length is 16384 tokens and your request has 5000 input tokens (16000 > 16384 - 5000)"),
			Classified{Class: ClassContextOverflow, Reason: ReasonMaxTokensOverflow, Overflow: &MaxTokensOverflow{Input: 5000, MaxTokens: 16000, Limit: 16384}}, false},
		{"string too many tokens alone is an overflow",
			errors.New("api: too many tokens: total number of tokens in the prompt cannot exceed 4081 - received 4985"),
			Classified{Class: ClassContextOverflow, Reason: ReasonPromptTooLong}, false},
		{"string auth", errors.New("api: authentication_error: invalid x-api-key"),
			Classified{Class: ClassAuth, Reason: ReasonAuth}, false},
		{"string unknown", errors.New("api: something unexpected"), Classified{}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := Classify(tt.err)
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("Classify() = %+v (overflow %+v)\nwant %+v (overflow %+v)", got, got.Overflow, tt.want, tt.want.Overflow)
			}
			if got.Retryable() != tt.retryable {
				t.Errorf("Retryable() = %v, want %v", got.Retryable(), tt.retryable)
			}
		})
	}
}

// TestClassify_TypedNilErrors: a nil pointer of a typed error must classify
// as unknown, not panic in a method call on it.
func TestClassify_TypedNilErrors(t *testing.T) {
	tests := []struct {
		name string
		err  error
	}{
		{"*APIStatusError", (*APIStatusError)(nil)},
		{"wrapped *APIStatusError", fmt.Errorf("x: %w", (*APIStatusError)(nil))},
		{"*sse.Error", (*sse.Error)(nil)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := Classify(tt.err); !reflect.DeepEqual(got, Classified{}) {
				t.Errorf("Classify() = %+v, want unknown", got)
			}
		})
	}
}

// TestClassify_NetworkTimeouts drives real timeouts through net and net/http.
// Go's own timeout errors also match context.DeadlineExceeded through their Is
// methods; they must still classify as transient network errors, while the
// caller's own context deadline or cancel stays a cancel.
func TestClassify_NetworkTimeouts(t *testing.T) {
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/body" {
			// Headers go out; the body never arrives.
			w.WriteHeader(http.StatusOK)
			if f, ok := w.(http.Flusher); ok {
				f.Flush()
			}
		}
		select {
		case <-release:
		case <-r.Context().Done():
		}
	}))
	defer srv.Close()
	defer close(release) // runs first, so stalled handlers return before Close waits on them

	newTransport := func(t *testing.T) *http.Transport {
		tr := &http.Transport{}
		t.Cleanup(tr.CloseIdleConnections)
		return tr
	}
	// call sends a request that must fail and returns the error from Do or
	// from reading the body.
	call := func(t *testing.T, ctx context.Context, c *http.Client, path string) error {
		t.Helper()
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, srv.URL+path, nil)
		if err != nil {
			t.Fatalf("NewRequest: %v", err)
		}
		resp, err := c.Do(req)
		if err != nil {
			return err
		}
		defer func() { _ = resp.Body.Close() }()
		if _, err := io.ReadAll(resp.Body); err != nil {
			return err
		}
		t.Fatal("request completed; want a timeout")
		return nil
	}
	network := Classified{Class: ClassTransient, Reason: ReasonNetwork}
	cancelled := Classified{Class: ClassCancelled, Reason: ReasonCancelled}

	tests := []struct {
		name string
		do   func(t *testing.T) error
		// wantDeadline pins the precondition that makes the case interesting:
		// the error matches context.DeadlineExceeded.
		wantDeadline bool
		want         Classified
	}{
		{"Client.Timeout awaiting headers", func(t *testing.T) error {
			return call(t, context.Background(), &http.Client{Timeout: 20 * time.Millisecond, Transport: newTransport(t)}, "/")
		}, true, network},
		{"Client.Timeout reading the body", func(t *testing.T) error {
			return call(t, context.Background(), &http.Client{Timeout: 100 * time.Millisecond, Transport: newTransport(t)}, "/body")
		}, true, network},
		{"Transport.ResponseHeaderTimeout", func(t *testing.T) error {
			tr := newTransport(t)
			tr.ResponseHeaderTimeout = 20 * time.Millisecond
			return call(t, context.Background(), &http.Client{Transport: tr}, "/")
		}, true, network},
		{"Dialer deadline", func(t *testing.T) error {
			// A deadline already in the past fails the dial deterministically.
			conn, err := (&net.Dialer{Deadline: time.Now().Add(-time.Second)}).DialContext(context.Background(), "tcp", srv.Listener.Addr().String())
			if err == nil {
				_ = conn.Close()
				t.Fatal("dial succeeded; want a timeout")
			}
			return err
		}, true, network},
		{"caller's context deadline", func(t *testing.T) error {
			ctx, cancel := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
			defer cancel()
			return call(t, ctx, &http.Client{Transport: newTransport(t)}, "/")
		}, true, cancelled},
		{"caller's context deadline while reading the body", func(t *testing.T) error {
			ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
			defer cancel()
			return call(t, ctx, &http.Client{Transport: newTransport(t)}, "/body")
		}, true, cancelled},
		{"caller's cancel", func(t *testing.T) error {
			ctx, cancel := context.WithCancel(context.Background())
			cancel()
			return call(t, ctx, &http.Client{Transport: newTransport(t)}, "/")
		}, false, cancelled},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.do(t)
			t.Logf("error: %v", err)
			if got := errors.Is(err, context.DeadlineExceeded); got != tt.wantDeadline {
				t.Fatalf("precondition: errors.Is(%v, context.DeadlineExceeded) = %v, want %v", err, got, tt.wantDeadline)
			}
			if got := Classify(err); !reflect.DeepEqual(got, tt.want) {
				t.Errorf("Classify(%v) = %+v, want %+v", err, got, tt.want)
			}
		})
	}
}

func TestErrorClassString(t *testing.T) {
	tests := []struct {
		class ErrorClass
		want  string
	}{
		{ClassUnknown, "unknown"},
		{ClassTransient, "transient"},
		{ClassRateLimited, "rate_limited"},
		{ClassContextOverflow, "context_overflow"},
		{ClassAuth, "auth"},
		{ClassInvalidRequest, "invalid_request"},
		{ClassCancelled, "cancelled"},
		{ClassToolUseFlow, "tool_use_flow"},
		{ClassPrefixLock, "prefix_lock"},
		{ErrorClass(99), "ErrorClass(99)"},
	}
	for _, tt := range tests {
		if got := tt.class.String(); got != tt.want {
			t.Errorf("ErrorClass(%d).String() = %q, want %q", int(tt.class), got, tt.want)
		}
	}
}

func TestNewStatusErrorHeaders(t *testing.T) {
	inTwoHours := time.Now().Add(2 * time.Hour).UTC().Format(http.TimeFormat)
	reset := time.Date(2026, 10, 7, 18, 0, 0, 0, time.UTC)
	tests := []struct {
		name string
		hdr  []string
		// wantRetryAfter is exact unless minRetryAfter is set, in which case
		// the value must fall in [minRetryAfter, wantRetryAfter].
		wantRetryAfter  time.Duration
		minRetryAfter   time.Duration
		wantResetAt     time.Time
		wantShouldRetry string
	}{
		{name: "no headers"},
		{name: "retry-after-ms float", hdr: []string{"retry-after-ms", "1500.5"}, wantRetryAfter: 1500500 * time.Microsecond},
		{name: "retry-after-ms beats retry-after", hdr: []string{"retry-after-ms", "250", "retry-after", "30"}, wantRetryAfter: 250 * time.Millisecond},
		{name: "zero retry-after-ms falls through", hdr: []string{"retry-after-ms", "0", "retry-after", "2"}, wantRetryAfter: 2 * time.Second},
		{name: "unparsable retry-after-ms falls through", hdr: []string{"retry-after-ms", "soon", "retry-after", "3"}, wantRetryAfter: 3 * time.Second},
		{name: "retry-after seconds", hdr: []string{"retry-after", "60"}, wantRetryAfter: time.Minute},
		{name: "retry-after fractional seconds", hdr: []string{"retry-after", "0.5"}, wantRetryAfter: 500 * time.Millisecond},
		{name: "retry-after HTTP date", hdr: []string{"retry-after", inTwoHours},
			minRetryAfter: 2*time.Hour - time.Minute, wantRetryAfter: 2 * time.Hour},
		{name: "retry-after date in the past", hdr: []string{"retry-after", "Thu, 01 Jan 2015 00:00:00 GMT"}},
		{name: "negative retry-after", hdr: []string{"retry-after", "-5"}},
		{name: "non-finite retry-after", hdr: []string{"retry-after", "+Inf"}},
		{name: "retry-after-ms beyond the SDK cap", hdr: []string{"retry-after-ms", "1e300"}},
		{name: "unified reset in Unix seconds", hdr: []string{"anthropic-ratelimit-unified-reset", "1786147200"}, wantResetAt: time.Unix(1786147200, 0)},
		{name: "unified reset RFC 3339", hdr: []string{"anthropic-ratelimit-unified-reset", "2026-10-07T18:00:00Z"}, wantResetAt: reset},
		{name: "unified reset HTTP date", hdr: []string{"anthropic-ratelimit-unified-reset", "Wed, 07 Oct 2026 18:00:00 GMT"}, wantResetAt: reset},
		{name: "unified reset zero", hdr: []string{"anthropic-ratelimit-unified-reset", "0"}},
		{name: "unified reset garbage", hdr: []string{"anthropic-ratelimit-unified-reset", "tomorrow"}},
		{name: "x-should-retry false", hdr: []string{"x-should-retry", "false"}, wantShouldRetry: "false"},
		{name: "x-should-retry normalised", hdr: []string{"x-should-retry", " True "}, wantShouldRetry: "true"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := apiStatusErr(http.StatusTooManyRequests, "rate_limit_error", "slow down", tt.hdr...)
			if tt.minRetryAfter > 0 {
				if e.RetryAfter < tt.minRetryAfter || e.RetryAfter > tt.wantRetryAfter {
					t.Errorf("RetryAfter = %v, want within [%v, %v]", e.RetryAfter, tt.minRetryAfter, tt.wantRetryAfter)
				}
			} else if e.RetryAfter != tt.wantRetryAfter {
				t.Errorf("RetryAfter = %v, want %v", e.RetryAfter, tt.wantRetryAfter)
			}
			if !e.ResetAt.Equal(tt.wantResetAt) {
				t.Errorf("ResetAt = %v, want %v", e.ResetAt, tt.wantResetAt)
			}
			if e.ShouldRetry != tt.wantShouldRetry {
				t.Errorf("ShouldRetry = %q, want %q", e.ShouldRetry, tt.wantShouldRetry)
			}
			// The headers must not leak into the flattened text.
			if want := "api: 429 Too Many Requests: rate_limit_error: slow down"; e.Error() != want {
				t.Errorf("Error() = %q, want %q", e.Error(), want)
			}
		})
	}
}

// TestStreamMessage_StatusErrorCarriesRetryHeaders drives a real 429 through
// withRetry and checks the typed error carries the header fields while its
// flattened text keeps the historical shape. x-should-retry: false skips
// withRetry, so the client decodes the body and adds its fuller header suffix.
func TestStreamMessage_StatusErrorCarriesRetryHeaders(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("retry-after", "7")
		w.Header().Set("retry-after-ms", "6500")
		w.Header().Set("anthropic-ratelimit-unified-reset", "1786147200")
		w.Header().Set("x-should-retry", "false")
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = io.WriteString(w, `{"type":"error","error":{"type":"rate_limit_error","message":"slow down"}}`)
	}))
	defer srv.Close()

	c := NewClient(Config{BaseURL: srv.URL, AuthToken: "t"}, srv.Client())
	ctx := WithRetryHandler(t.Context(), func(RetryEvent) bool { return false })
	_, err := c.StreamMessage(ctx, &MessageRequest{Model: "m", MaxTokens: 1})
	var se *APIStatusError
	if !errors.As(err, &se) {
		t.Fatalf("error is %T, want *APIStatusError: %v", err, err)
	}
	if se.RetryAfter != 6500*time.Millisecond {
		t.Errorf("RetryAfter = %v, want 6.5s", se.RetryAfter)
	}
	if !se.ResetAt.Equal(time.Unix(1786147200, 0)) {
		t.Errorf("ResetAt = %v", se.ResetAt)
	}
	if se.ShouldRetry != "false" {
		t.Errorf("ShouldRetry = %q", se.ShouldRetry)
	}
	if want := "api: 429 Too Many Requests: rate_limit_error: slow down [retry-after=7s unified-reset=1786147200]"; err.Error() != want {
		t.Errorf("Error() = %q, want %q", err.Error(), want)
	}
	got := Classify(err)
	if got.Class != ClassRateLimited || !got.NoRetry || got.Retryable() {
		t.Errorf("Classify() = %+v, want rate limited with NoRetry", got)
	}
}

func TestClientProviderAccessors(t *testing.T) {
	tests := []struct {
		name           string
		client         *Client
		wantKind       string
		wantMessages   bool
		wantFirstParty bool
	}{
		{"default Anthropic", NewClient(Config{}, nil), "", true, true},
		{"api.anthropic.com", NewClient(Config{BaseURL: "https://api.anthropic.com"}, nil), "", true, true},
		{"Copilot Messages shim", NewClient(Config{BaseURL: "https://api.githubcopilot.com"}, nil), "", true, false},
		{"unrecognised kind falls back to Messages", NewClient(Config{ProviderKind: "bogus"}, nil), "", true, true},
		{"openai-compatible", NewClient(Config{ProviderKind: ProviderKindOpenAICompatible, BaseURL: "http://localhost:8080/v1"}, nil),
			ProviderKindOpenAICompatible, false, false},
		{"openai-responses on an Anthropic host", NewClient(Config{ProviderKind: ProviderKindOpenAIResponses, BaseURL: "https://api.anthropic.com"}, nil),
			ProviderKindOpenAIResponses, false, false},
		{"nil client", nil, "", false, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.client.ProviderKind(); got != tt.wantKind {
				t.Errorf("ProviderKind() = %q, want %q", got, tt.wantKind)
			}
			if got := tt.client.SpeaksAnthropicMessages(); got != tt.wantMessages {
				t.Errorf("SpeaksAnthropicMessages() = %v, want %v", got, tt.wantMessages)
			}
			if got := tt.client.FirstPartyAnthropic(); got != tt.wantFirstParty {
				t.Errorf("FirstPartyAnthropic() = %v, want %v", got, tt.wantFirstParty)
			}
		})
	}
}

func TestStreamNext_WrapsTerminalErrors(t *testing.T) {
	tests := []struct {
		name          string
		body          string
		wantTruncated bool
		wantEOF       bool
		want          Classified
	}{
		{name: "cut off mid-event", body: "event: message_start\ndata: {\"type\":\"message_start\"",
			wantTruncated: true, want: Classified{Class: ClassTransient, Reason: ReasonStreamTruncated}},
		{name: "error event", body: "event: error\ndata: {\"type\":\"error\",\"error\":{\"type\":\"overloaded_error\",\"message\":\"Overloaded\"}}\n\n",
			want: Classified{Class: ClassTransient, Reason: ReasonOverloaded}},
		{name: "clean end", body: "", wantEOF: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := NewStreamFromReader(io.NopCloser(strings.NewReader(tt.body)))
			defer func() { _ = s.Close() }()
			_, err := s.Next()
			if err == nil {
				t.Fatal("Next() = nil error")
			}
			if got := errors.Is(err, ErrStreamTruncated); got != tt.wantTruncated {
				t.Errorf("errors.Is(err, ErrStreamTruncated) = %v, want %v (err %v)", got, tt.wantTruncated, err)
			}
			if tt.wantTruncated && !errors.Is(err, sse.ErrTruncated) {
				t.Errorf("err = %v, want it to keep wrapping sse.ErrTruncated", err)
			}
			if got := errors.Is(err, io.EOF); got != tt.wantEOF {
				t.Errorf("errors.Is(err, io.EOF) = %v, want %v", got, tt.wantEOF)
			}
			if got := Classify(err); !reflect.DeepEqual(got, tt.want) {
				t.Errorf("Classify() = %+v, want %+v", got, tt.want)
			}
		})
	}
}

func FuzzClassifyMessage(f *testing.F) {
	f.Add(400, "invalid_request_error", "prompt is too long: 210000 tokens > 200000 maximum")
	f.Add(400, "invalid_request_error", "input length and `max_tokens` exceed context limit: 195000 + 21333 > 200000")
	f.Add(400, "invalid_request_error", "input length and max_tokens exceed context limit: 99999999999999999999999 + 1 > 2")
	f.Add(400, "invalid_request_error", "messages.1: tool_use ids were found without tool_result blocks")
	f.Add(400, "invalid_request_error", "thinking block was not created in this conversation")
	f.Add(429, "rate_limit_error", "Too many tokens, please wait")
	f.Add(500, "", "the request exceeds the available context size (n_ctx)")
	f.Add(529, "overloaded_error", "Overloaded")
	f.Add(400, "invalid_request_error", "maximum context length is 32768 tokens. however, you requested 42950 tokens (38854 in the messages, 4096 in the completion)")
	f.Add(400, "BadRequestError", "max_tokens=99999999999999999999 cannot be greater than max_model_len=8192")
	f.Add(400, "", `{"error":{"code":400,"type":"exceed_context_size_error","n_prompt_tokens":4297,"n_ctx":4096}}`)
	f.Add(0, "", "max_tokens is too large: 40100 :52900 status=503")
	f.Add(0, "", "")
	f.Fuzz(func(t *testing.T, status int, typ, msg string) {
		errs := []error{
			&APIStatusError{Status: status, Type: typ, Message: msg, text: msg},
			&sse.Error{Type: typ, Message: msg},
			errors.New(msg),
			fmt.Errorf("wrapped: %w", errors.New(typ+": "+msg)),
		}
		for _, err := range errs {
			c := Classify(err)
			if c.Class < ClassUnknown || c.Class > ClassPrefixLock {
				t.Fatalf("Classify(%q) class %d out of range", err, int(c.Class))
			}
			if (c.Class == ClassUnknown) != (c.Reason == "") {
				t.Fatalf("Classify(%q) = %v with reason %q: reason must be empty exactly for unknown", err, c.Class, c.Reason)
			}
			if c.Overflow != nil && c.Reason != ReasonMaxTokensOverflow {
				t.Fatalf("Classify(%q) set Overflow with reason %q", err, c.Reason)
			}
			if o := c.Overflow; o != nil && (o.Input < 0 || o.MaxTokens < 0 || o.Limit < 0) {
				t.Fatalf("Classify(%q) Overflow has a negative count: %+v", err, *o)
			}
			if c.PromptTokens < 0 || c.PromptLimit < 0 {
				t.Fatalf("Classify(%q) prompt counts negative: %d/%d", err, c.PromptTokens, c.PromptLimit)
			}
			if (c.PromptTokens != 0 || c.PromptLimit != 0) && c.Reason != ReasonPromptTooLong {
				t.Fatalf("Classify(%q) set prompt counts with reason %q", err, c.Reason)
			}
			if c.Retryable() && c.Class != ClassTransient && c.Class != ClassRateLimited {
				t.Fatalf("Classify(%q) = %v is retryable", err, c.Class)
			}
		}
	})
}
