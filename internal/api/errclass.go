package api

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"io"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/icehunter/conduit/internal/sse"
)

// ErrStreamTruncated means a stream ended before its terminal event:
// message_stop, or a provider's own end marker. Stream.Next wraps
// sse.ErrTruncated with it. Converters for other wire dialects close their
// pipe with an error wrapping it when the upstream stops without finishing.
var ErrStreamTruncated = errors.New("api: stream ended before message_stop")

// ErrStreamIdleTimeout means no bytes arrived on a stream for
// streamIdleTimeout, so the connection was torn down. The error chain also
// holds the context.Canceled used to unblock the read; test for this sentinel
// before treating an error as a user cancel.
var ErrStreamIdleTimeout = errors.New("api: stream idle timeout")

// ErrorClass is the recovery category Classify assigns to an error.
type ErrorClass int

const (
	// ClassUnknown is an error no rule recognises.
	ClassUnknown ErrorClass = iota
	// ClassTransient may succeed if the same request is sent again: 5xx,
	// overload, network failures, idle timeouts and truncated streams.
	ClassTransient
	// ClassRateLimited is a 429 or a rate-limit or quota message.
	ClassRateLimited
	// ClassContextOverflow means the request does not fit the model's limits
	// and needs compaction or a smaller max_tokens.
	ClassContextOverflow
	// ClassAuth is a 401/403 or another credential failure.
	ClassAuth
	// ClassInvalidRequest is a request the server will keep rejecting.
	ClassInvalidRequest
	// ClassCancelled is a context cancellation or deadline.
	ClassCancelled
	// ClassToolUseFlow is a 400 about tool_use / tool_result pairing.
	ClassToolUseFlow
	// ClassPrefixLock is the 400 for a thinking block signed in another
	// conversation (APIStatusError.IsPrefixLockRejection).
	ClassPrefixLock
)

func (c ErrorClass) String() string {
	switch c {
	case ClassUnknown:
		return "unknown"
	case ClassTransient:
		return "transient"
	case ClassRateLimited:
		return "rate_limited"
	case ClassContextOverflow:
		return "context_overflow"
	case ClassAuth:
		return "auth"
	case ClassInvalidRequest:
		return "invalid_request"
	case ClassCancelled:
		return "cancelled"
	case ClassToolUseFlow:
		return "tool_use_flow"
	case ClassPrefixLock:
		return "prefix_lock"
	}
	return "ErrorClass(" + strconv.Itoa(int(c)) + ")"
}

// Classified.Reason values. They are stable tags that callers switch on, for
// example to pick the retry status text.
const (
	ReasonRateLimit         = "rate_limit"
	ReasonOverloaded        = "overloaded"
	ReasonServerError       = "server_error"
	ReasonNetwork           = "network"
	ReasonIdleTimeout       = "idle_timeout"
	ReasonStreamTruncated   = "stream_truncated"
	ReasonStreamError       = "stream_error"
	ReasonPromptTooLong     = "prompt_too_long"
	ReasonRequestTooLarge   = "request_too_large"
	ReasonMaxTokensOverflow = "max_tokens_overflow"
	ReasonAuth              = "auth"
	ReasonInvalidRequest    = "invalid_request"
	ReasonModelNotFound     = "model_not_found"
	ReasonTLS               = "tls"
	ReasonToolFlow          = "tool_flow"
	ReasonPrefixLock        = "prefix_lock"
	ReasonCancelled         = "cancelled"
)

// MaxTokensOverflow holds the counts from a rejected max_tokens: the request
// fits when Input + MaxTokens <= Limit. Input is 0 when the server did not
// report it, and only a lower bound for vLLM's "at least N input tokens".
// Input >= Limit means the prompt alone overflows, so lowering max_tokens
// cannot help. For OpenAI's "supports at most N completion tokens", Limit is
// the model's output cap rather than its context window.
type MaxTokensOverflow struct {
	Input, MaxTokens, Limit int
}

// Classified is Classify's verdict on an error.
type Classified struct {
	Class ErrorClass
	// Reason is one of the Reason* tags; empty only for ClassUnknown.
	Reason string
	// Status is the HTTP status of an APIStatusError, otherwise 0.
	Status int
	// RetryAfter and ResetAt are copied from the APIStatusError; zero when
	// the response carried no usable header. RetryAfter is the server's value,
	// bounded only against overflow (about 24.8 days). A subscription limit can
	// ask for hours, so code that sleeps on it must apply its own cap, as CC
	// does by giving up past a threshold (x-cc-retry-after-too-long).
	RetryAfter time.Duration
	ResetAt    time.Time
	// NoRetry is set when the server sent x-should-retry: false.
	NoRetry bool
	// Overflow holds the counts of a max_tokens overflow, when they parsed.
	Overflow *MaxTokensOverflow
	// PromptTokens and PromptLimit come from "prompt is too long: N tokens >
	// M"; zero when the message carried no counts.
	PromptTokens, PromptLimit int
}

// Retryable reports whether sending the same request again can succeed.
func (c Classified) Retryable() bool {
	return (c.Class == ClassTransient || c.Class == ClassRateLimited) && !c.NoRetry
}

// Classify maps err to a recovery class. Typed checks run first, in order:
// the idle-timeout sentinel (its chain also holds context.Canceled), the
// caller's cancel or deadline (Go's network timeouts also match
// context.DeadlineExceeded but classify as network), *APIStatusError,
// *sse.Error, truncation, then TLS and network errors. Matching phrases in
// the lowercased error text is the last resort, for errors that only exist
// as strings.
func Classify(err error) Classified {
	if err == nil {
		return Classified{}
	}
	if errors.Is(err, ErrStreamIdleTimeout) {
		return Classified{Class: ClassTransient, Reason: ReasonIdleTimeout}
	}
	if errors.Is(err, ErrResponseHeaderTimeout) {
		return Classified{Class: ClassTransient, Reason: ReasonNetwork}
	}
	if errors.Is(err, context.Canceled) ||
		(errors.Is(err, context.DeadlineExceeded) && !isNetTimeout(err)) {
		return Classified{Class: ClassCancelled, Reason: ReasonCancelled}
	}
	var statusErr *APIStatusError
	if errors.As(err, &statusErr) {
		if statusErr == nil {
			return Classified{}
		}
		return classifyStatus(statusErr)
	}
	var streamErr *sse.Error
	if errors.As(err, &streamErr) {
		if streamErr == nil {
			return Classified{}
		}
		return classifyStreamError(streamErr)
	}
	if errors.Is(err, ErrStreamTruncated) || errors.Is(err, sse.ErrTruncated) {
		return Classified{Class: ClassTransient, Reason: ReasonStreamTruncated}
	}
	if errors.Is(err, sse.ErrLineTooLong) {
		return Classified{Class: ClassInvalidRequest, Reason: ReasonStreamError}
	}
	if isTLSError(err) {
		return Classified{Class: ClassInvalidRequest, Reason: ReasonTLS}
	}
	if isNetworkError(err) {
		return Classified{Class: ClassTransient, Reason: ReasonNetwork}
	}
	return classifyText(strings.ToLower(err.Error()))
}

// classifyStatus classifies a non-2xx response. Auth and rate-limit statuses
// win over message wording (a 429 saying "too many tokens" is a throttle, not
// an overflow); overflow wording wins over 5xx, because local servers answer
// an n_ctx overflow with a 500. A 413 is an overflow either way; its wording
// only refines the reason, as in CC. Rate-limit or throttling wording on any
// other status is a rate limit.
func classifyStatus(e *APIStatusError) Classified {
	c := Classified{
		Status:     e.Status,
		RetryAfter: e.RetryAfter,
		ResetAt:    e.ResetAt,
		NoRetry:    strings.EqualFold(e.ShouldRetry, "false"),
	}
	text := strings.ToLower(e.Type + ": " + e.Message)
	switch {
	case e.IsPrefixLockRejection():
		c.Class, c.Reason = ClassPrefixLock, ReasonPrefixLock
	case isBadRequest(e) && mentionsToolFlow(text):
		c.Class, c.Reason = ClassToolUseFlow, ReasonToolFlow
	case e.Status == http.StatusUnauthorized || e.Status == http.StatusForbidden:
		c.Class, c.Reason = ClassAuth, ReasonAuth
	case e.Status == http.StatusTooManyRequests:
		c.Class, c.Reason = ClassRateLimited, ReasonRateLimit
	case e.Status == http.StatusRequestEntityTooLarge:
		if !matchOverflow(&c, text) {
			c.Class, c.Reason = ClassContextOverflow, ReasonRequestTooLarge
		}
	case matchOverflow(&c, text):
		// matchOverflow filled in c.
	case mentionsRateLimit(text):
		c.Class, c.Reason = ClassRateLimited, ReasonRateLimit
	case e.Status == http.StatusNotFound || mentionsModelNotFound(text):
		c.Class, c.Reason = ClassInvalidRequest, ReasonModelNotFound
	case e.Status == 529 || e.Type == "overloaded_error":
		c.Class, c.Reason = ClassTransient, ReasonOverloaded
	case e.Status == http.StatusRequestTimeout || e.Status == http.StatusConflict || e.Status >= 500:
		c.Class, c.Reason = ClassTransient, ReasonServerError
		if strings.Contains(text, "overloaded") {
			c.Reason = ReasonOverloaded
		}
	case e.Status >= 400:
		c.Class, c.Reason = ClassInvalidRequest, ReasonInvalidRequest
	}
	return c
}

// isBadRequest mirrors the gate of the agent's old string check for
// tool-flow errors: a 400, or an invalid/bad request error type.
func isBadRequest(e *APIStatusError) bool {
	return e.Status == http.StatusBadRequest ||
		e.Type == "invalid_request_error" || e.Type == "bad_request_error"
}

// classifyStreamError classifies an `event: error` received after a 200. The
// OpenAI-dialect converters map provider errors onto the same Anthropic types
// (rate_limit_error, overloaded_error, api_error, authentication_error,
// invalid_request_error), so they classify like native ones.
func classifyStreamError(e *sse.Error) Classified {
	var c Classified
	switch e.Type {
	case "rate_limit_error":
		c.Class, c.Reason = ClassRateLimited, ReasonRateLimit
		return c
	case "authentication_error", "permission_error":
		c.Class, c.Reason = ClassAuth, ReasonAuth
		return c
	}
	if matchOverflow(&c, strings.ToLower(e.Type+": "+e.Message)) {
		return c
	}
	switch e.Type {
	case "overloaded_error":
		c.Class, c.Reason = ClassTransient, ReasonOverloaded
	case "api_error", "timeout_error":
		c.Class, c.Reason = ClassTransient, ReasonServerError
	case "not_found_error":
		c.Class, c.Reason = ClassInvalidRequest, ReasonModelNotFound
	case "invalid_request_error", "billing_error":
		c.Class, c.Reason = ClassInvalidRequest, ReasonInvalidRequest
	default:
		c.Class, c.Reason = ClassTransient, ReasonStreamError
	}
	return c
}

// isTLSError reports certificate and handshake-framing failures. Retrying
// does not fix them; the trust store or base URL scheme has to change.
func isTLSError(err error) bool {
	var unknownAuthority x509.UnknownAuthorityError
	var hostname x509.HostnameError
	var invalid x509.CertificateInvalidError
	var verification *tls.CertificateVerificationError
	var recordHeader tls.RecordHeaderError
	return errors.As(err, &unknownAuthority) ||
		errors.As(err, &hostname) ||
		errors.As(err, &invalid) ||
		errors.As(err, &verification) ||
		errors.As(err, &recordHeader)
}

// isNetworkError reports connection-level failures: socket errors, timeouts,
// a body cut short, and a server that hung up before responding (the HTTP
// client reports that as a *url.Error wrapping io.EOF).
func isNetworkError(err error) bool {
	if errors.Is(err, io.ErrUnexpectedEOF) {
		return true
	}
	var opErr *net.OpError
	if errors.As(err, &opErr) {
		return true
	}
	var urlErr *url.Error
	if errors.As(err, &urlErr) && urlErr != nil && errors.Is(urlErr.Err, io.EOF) {
		return true
	}
	var netErr net.Error
	return errors.As(err, &netErr) && netErr.Timeout()
}

// isNetTimeout reports whether an error that matches context.DeadlineExceeded
// came from the network stack or the HTTP client's own limits (Dialer
// timeouts, Client.Timeout, Transport.ResponseHeaderTimeout). Go's timeout
// errors match the sentinel through their Is methods, but they are worth a
// retry. The caller's context deadline arrives as the sentinel itself, bare
// or wrapped, and inside a *url.Error from the HTTP client.
func isNetTimeout(err error) bool {
	var opErr *net.OpError
	if errors.As(err, &opErr) {
		return true
	}
	var urlErr *url.Error
	if errors.As(err, &urlErr) && urlErr != nil {
		err = urlErr.Err // *url.Error only relays the Timeout() of its Err
	}
	var netErr net.Error
	// Identity, not errors.Is: Go's timeout errors also match the sentinel.
	return errors.As(err, &netErr) && netErr.Timeout() && netErr != context.DeadlineExceeded //nolint:errorlint // identity on purpose
}

// classifyText is the fallback for errors that only exist as strings; text
// is lowercased. Overflow messages are matched before rate-limit wording,
// except the ambiguous "too many tokens", which a throttle also says: it
// counts as an overflow only when no rate-limit check claimed the text first.
func classifyText(text string) Classified {
	var c Classified
	codes := statusCodesIn(text)
	switch {
	case matchCountedOverflow(&c, text):
		// matchCountedOverflow filled in c.
	case mentionsToolFlow(text) && (codes["400"] || containsAny(text, "invalid_request_error", "bad_request_error")):
		c.Class, c.Reason = ClassToolUseFlow, ReasonToolFlow
	case codes["429"]:
		// An explicit 429 is a rate limit even if the body mentions context.
		c.Class, c.Reason = ClassRateLimited, ReasonRateLimit
	case matchOverflowPhrase(&c, text):
		// matchOverflowPhrase filled in c.
	case mentionsRateLimit(text):
		c.Class, c.Reason = ClassRateLimited, ReasonRateLimit
	case containsAny(text, ambiguousOverflowPhrases...):
		c.Class, c.Reason = ClassContextOverflow, ReasonPromptTooLong
	case mentionsModelNotFound(text):
		c.Class, c.Reason = ClassInvalidRequest, ReasonModelNotFound
	case codes["529"] || strings.Contains(text, "overloaded"):
		c.Class, c.Reason = ClassTransient, ReasonOverloaded
	case codes["503"] || containsAny(text, serverErrorPhrases...):
		c.Class, c.Reason = ClassTransient, ReasonServerError
	case containsAny(text, networkPhrases...):
		c.Class, c.Reason = ClassTransient, ReasonNetwork
	case codes["401"] || codes["403"] || containsAny(text, authPhrases...):
		c.Class, c.Reason = ClassAuth, ReasonAuth
	}
	return c
}

// statusCodeRe finds the HTTP statuses classifyText acts on. A code glued to
// a word, a longer number or a decimal ("4297 tokens", "40100", "1.429") is
// not a status.
var statusCodeRe = regexp.MustCompile(`(?:^|[\s:"'(])(400|401|403|429|503|529)\b`)

// statusCodesIn returns the statusCodeRe codes that appear in text.
func statusCodesIn(text string) map[string]bool {
	codes := make(map[string]bool)
	for _, m := range statusCodeRe.FindAllStringSubmatch(text, -1) {
		codes[m[1]] = true
	}
	return codes
}

// Phrase lists for classifyText; statuses are matched by statusCodeRe.
var (
	rateLimitPhrases = []string{
		"too many requests", "rate limit", "rate_limit", "ratelimit", "quota",
		"resource_exhausted", "resource has been exhausted", "throttl",
	}
	serverErrorPhrases = []string{
		"internal server error", "bad gateway", "service unavailable", "gateway timeout",
		"server_error", "server error", "temporarily unavailable",
	}
	networkPhrases = []string{
		"connection reset", "connection refused", "connection closed", "broken pipe",
		"unexpected eof", "stream error", "goaway", "server closed", "timeout", "timed out",
	}
	authPhrases = []string{
		"authentication", "unauthorized", "forbidden", "permission_error", "invalid api key",
		"invalid_api_key", "invalid x-api-key", "incorrect api key",
	}
)

// overflowPhrases are context-overflow wordings from Anthropic, OpenAI, vLLM,
// SGLang, llama.cpp, Ollama, LM Studio and Gemini, lowercased.
var overflowPhrases = []string{
	"prompt is too long",
	"prompt too long",
	"context_length_exceeded",
	"context length",
	"context window",
	"context size",
	"max_model_len",
	"maximum model length",
	"n_ctx",
	"input is too long",
	"maximum allowed input length",
	"exceeds the maximum number of tokens",
	"reduce the length of the messages",
	"maximum prompt length",
}

// ambiguousOverflowPhrases also appear in throttling messages ("Too many
// tokens, please wait before trying again"), so they mean an overflow only
// when the text has no rate-limit wording.
var ambiguousOverflowPhrases = []string{"too many tokens"}

var (
	// maxTokensOverflowRes match a rejected input + max_tokens > limit, most
	// specific first. Named groups input, max and limit; a group a pattern
	// lacks reads as 0.
	maxTokensOverflowRes = []*regexp.Regexp{
		// Anthropic; CC parses the same message for its max_tokens retry.
		regexp.MustCompile(`input length and .?max_tokens.? exceed context limit: (?P<input>\d+) \+ (?P<max>\d+) > (?P<limit>\d+)`),
		// vLLM. "at least N input tokens" is a lower bound.
		regexp.MustCompile(`maximum context length is (?P<limit>\d+) tokens\. however, you requested (?P<max>\d+) output tokens and your prompt contains (?:at least )?(?P<input>\d+) input tokens`),
		// OpenAI, older vLLM and llama-cpp-python; the second form is the
		// legacy completions wording "(771 in your prompt; 3726 for the completion)".
		regexp.MustCompile(`maximum context length is (?P<limit>\d+) tokens[.,] however,? you requested \d+ tokens \((?P<input>\d+) (?:tokens )?in (?:the messages|your prompt|the prompt)\s*[;,]\s*(?P<max>\d+) (?:tokens )?(?:in|for) the completion\)`),
		// SGLang.
		regexp.MustCompile(`maximum context length of (?P<limit>\d+) tokens\. you requested a total of \d+ tokens: (?P<input>\d+) tokens from the input messages and (?P<max>\d+) tokens for the completion`),
		// vLLM, max_tokens alone above the window.
		regexp.MustCompile(`max_(?:completion_)?tokens=(?P<max>\d+) cannot be greater than max_model_len=(?P<limit>\d+)`),
		// vLLM 0.10.1-0.17: "'max_tokens' or 'max_completion_tokens' is too
		// large: 16000. This model's maximum context length is 16384 tokens
		// and your request has 5000 input tokens".
		regexp.MustCompile(`is too large: (?P<max>\d+)\. this model's maximum context length is (?P<limit>\d+) tokens and your request has (?P<input>\d+) input tokens`),
		// OpenAI and Azure: Limit is the model's output cap.
		regexp.MustCompile(`max_(?:completion_)?tokens is too large: (?P<max>\d+)\. this model supports at most (?P<limit>\d+) completion tokens`),
		// Anthropic without counts; CC's own detector keys on the phrase alone.
		regexp.MustCompile(`input length and .?max_tokens.? exceed context limit`),
	}
	// promptOverflowRes match a prompt that is over the window by itself.
	// Named groups tokens and limit.
	promptOverflowRes = []*regexp.Regexp{
		// Anthropic; CC's own parser.
		regexp.MustCompile(`prompt is too long[^0-9]*(?P<tokens>\d+)\s*tokens?\s*>\s*(?P<limit>\d+)`),
		// OpenAI.
		regexp.MustCompile(`maximum context length is (?P<limit>\d+) tokens\. however, your messages resulted in (?P<tokens>\d+) tokens`),
		// llama.cpp.
		regexp.MustCompile(`request \((?P<tokens>\d+) tokens\) exceeds the available context size \((?P<limit>\d+) tokens\)`),
	}
	// llama.cpp's exceed_context_size_error body also carries the counts as
	// JSON fields. They survive only when the raw body is in the error text.
	llamaPromptTokensRe = regexp.MustCompile(`"n_prompt_tokens"\s*:\s*(\d+)`)
	llamaContextSizeRe  = regexp.MustCompile(`"n_ctx"\s*:\s*(\d+)`)
)

// matchOverflow sets c to ClassContextOverflow when text (lowercased) is an
// overflow message, and reports whether it did. The ambiguous phrases count
// only without rate-limit wording.
func matchOverflow(c *Classified, text string) bool {
	if matchCountedOverflow(c, text) || matchOverflowPhrase(c, text) {
		return true
	}
	if containsAny(text, ambiguousOverflowPhrases...) && !mentionsRateLimit(text) {
		c.Class, c.Reason = ClassContextOverflow, ReasonPromptTooLong
		return true
	}
	return false
}

// matchCountedOverflow handles the overflow messages that carry token counts,
// filling the counts in when they parse.
func matchCountedOverflow(c *Classified, text string) bool {
	for _, re := range maxTokensOverflowRes {
		if m := re.FindStringSubmatch(text); m != nil {
			c.Class, c.Reason = ClassContextOverflow, ReasonMaxTokensOverflow
			if n, ok := namedCounts(re, m, "input", "max", "limit"); ok {
				c.Overflow = &MaxTokensOverflow{Input: n[0], MaxTokens: n[1], Limit: n[2]}
			}
			return true
		}
	}
	for _, re := range promptOverflowRes {
		if m := re.FindStringSubmatch(text); m != nil {
			c.Class, c.Reason = ClassContextOverflow, ReasonPromptTooLong
			if n, ok := namedCounts(re, m, "tokens", "limit"); ok {
				c.PromptTokens, c.PromptLimit = n[0], n[1]
			}
			return true
		}
	}
	if strings.Contains(text, "exceed_context_size") {
		tokens, limit := llamaPromptTokensRe.FindStringSubmatch(text), llamaContextSizeRe.FindStringSubmatch(text)
		if tokens != nil && limit != nil {
			c.Class, c.Reason = ClassContextOverflow, ReasonPromptTooLong
			if n, ok := atoiAll(tokens[1], limit[1]); ok {
				c.PromptTokens, c.PromptLimit = n[0], n[1]
			}
			return true
		}
	}
	return false
}

// matchOverflowPhrase handles the unambiguous overflow wording that carries
// no counts.
func matchOverflowPhrase(c *Classified, text string) bool {
	switch {
	case strings.Contains(text, "request_too_large"):
		c.Class, c.Reason = ClassContextOverflow, ReasonRequestTooLarge
	case containsAny(text, overflowPhrases...):
		c.Class, c.Reason = ClassContextOverflow, ReasonPromptTooLong
	default:
		return false
	}
	return true
}

func mentionsRateLimit(text string) bool {
	return containsAny(text, rateLimitPhrases...)
}

func mentionsToolFlow(text string) bool {
	return containsAny(text, "tool_use", "tool use", "tool_result", "tool result")
}

func mentionsModelNotFound(text string) bool {
	return containsAny(text, "model_not_found", "model not found", "no such model", "unknown model") ||
		(strings.Contains(text, "model") && strings.Contains(text, "does not exist"))
}

func containsAny(s string, subs ...string) bool {
	for _, sub := range subs {
		if strings.Contains(s, sub) {
			return true
		}
	}
	return false
}

// namedCounts parses the named groups of match m, in names order. A name the
// pattern does not define reads as 0. It reports false for a pattern without
// groups, or a number too large for an int.
func namedCounts(re *regexp.Regexp, m []string, names ...string) ([]int, bool) {
	if re.NumSubexp() == 0 {
		return nil, false
	}
	ss := make([]string, len(names))
	for k, name := range names {
		ss[k] = "0"
		if i := re.SubexpIndex(name); i >= 0 {
			ss[k] = m[i]
		}
	}
	return atoiAll(ss...)
}

// atoiAll parses every string as an int, reporting false if any fails
// (empty, or too large for an int).
func atoiAll(ss ...string) ([]int, bool) {
	out := make([]int, len(ss))
	for i, s := range ss {
		n, err := strconv.Atoi(s)
		if err != nil {
			return nil, false
		}
		out[i] = n
	}
	return out, true
}
