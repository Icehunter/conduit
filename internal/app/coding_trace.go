package app

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"math"
	"regexp"

	"github.com/icehunter/conduit/internal/episodes"
)

// Version 1 binds the inline code request to local-router's published expansion.
// A router that changes this conditioning must update the trace protocol.
const localTraceCodeSuffix = "\n\n---\n\nReturn only code. No prose, no fences unless syntactically required by the language."

var (
	traceRequestID  = regexp.MustCompile(`^[A-Za-z0-9_-]{1,128}$`)
	traceEndpointID = regexp.MustCompile(`^sha256:[a-f0-9]{64}$`)
)

type localRouterTrace struct {
	Version    int    `json:"version"`
	RequestID  string `json:"request_id"`
	EndpointID string `json:"endpoint_id"`
	Complete   *bool  `json:"complete"`
	Request    struct {
		Model      string             `json:"model"`
		Messages   []episodes.Message `json:"messages"`
		Parameters map[string]any     `json:"parameters"`
	} `json:"request"`
	Response struct {
		Content      *string         `json:"content"`
		FinishReason *string         `json:"finish_reason"`
		Usage        json.RawMessage `json:"usage"`
	} `json:"response"`
}

func validateCodingTrace(raw json.RawMessage, draft, model, prompt string) (episodes.Request, error) {
	bad := func() (episodes.Request, error) {
		return episodes.Request{}, errors.New("coding provider: missing or invalid requested local-router trace")
	}
	var envelope struct {
		Trace *localRouterTrace `json:"local_router_trace"`
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&envelope) != nil || decoder.Decode(new(any)) != io.EOF || !uniqueTraceKeys(raw) {
		return bad()
	}
	t := envelope.Trace
	if t == nil || t.Version != 1 || t.Complete == nil || !traceRequestID.MatchString(t.RequestID) || !traceEndpointID.MatchString(t.EndpointID) || model == "" || t.Request.Model != model {
		return bad()
	}
	messages := t.Request.Messages
	if len(messages) != 2 || messages[0].Role != "system" || messages[0].Content == "" || messages[1].Role != "user" || messages[1].Content != prompt+localTraceCodeSuffix || !validTraceParameters(t.Request.Parameters) {
		return bad()
	}
	if t.Response.Content == nil || *t.Response.Content != draft || len(t.Response.Usage) == 0 {
		return bad()
	}
	var usage map[string]any
	if json.Unmarshal(t.Response.Usage, &usage) != nil || !validTraceUsage(usage) {
		return bad()
	}
	finishReason := ""
	if t.Response.FinishReason != nil {
		finishReason = *t.Response.FinishReason
	}
	return episodes.Request{
		Messages: messages, ModelID: t.Request.Model, Parameters: t.Request.Parameters,
		ContextComplete: *t.Complete && finishReason == "stop",
		Trace:           &episodes.RequestTrace{Version: t.Version, RequestID: t.RequestID, EndpointID: t.EndpointID, FinishReason: finishReason, Usage: usage},
	}, nil
}

func validTraceParameters(p map[string]any) bool {
	if len(p) != 6 && len(p) != 7 {
		return false
	}
	tokens, ok := p["max_tokens"].(float64)
	if !ok || tokens <= 0 || math.Trunc(tokens) != tokens {
		return false
	}
	for _, key := range []string{"temperature", "top_p", "top_k", "min_p", "repeat_penalty"} {
		n, ok := p[key].(float64)
		if !ok || n < 0 || (key == "top_k" && math.Trunc(n) != n) || ((key == "top_p" || key == "min_p") && n > 1) || ((key == "temperature" || key == "repeat_penalty") && n > 2) {
			return false
		}
	}
	if value, exists := p["chat_template_kwargs"]; exists {
		kwargs, ok := value.(map[string]any)
		if !ok || len(kwargs) != 1 {
			return false
		}
		if _, ok := kwargs["enable_thinking"].(bool); !ok {
			return false
		}
	} else if len(p) != 6 {
		return false
	}
	return true
}

// Usage is numeric accounting only; never retain strings that could carry a
// provider's private reasoning or credentials under unexpected usage fields.
func validTraceUsage(usage map[string]any) bool {
	for _, value := range usage {
		switch v := value.(type) {
		case float64:
			if v < 0 || math.Trunc(v) != v {
				return false
			}
		case map[string]any:
			if !validTraceUsage(v) {
				return false
			}
		case nil:
		default:
			return false
		}
	}
	return true
}

// encoding/json accepts duplicate object keys. Reject them at every depth so
// a trace has one unambiguous interpretation across the two runtimes.
func uniqueTraceKeys(raw []byte) bool {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	var value func() bool
	value = func() bool {
		token, err := decoder.Token()
		if err != nil {
			return false
		}
		switch token {
		case json.Delim('{'):
			keys := map[string]bool{}
			for decoder.More() {
				token, err = decoder.Token()
				key, ok := token.(string)
				if err != nil || !ok || keys[key] {
					return false
				}
				keys[key] = true
				if !value() {
					return false
				}
			}
			end, err := decoder.Token()
			return err == nil && end == json.Delim('}')
		case json.Delim('['):
			for decoder.More() {
				if !value() {
					return false
				}
			}
			end, err := decoder.Token()
			return err == nil && end == json.Delim(']')
		}
		return true
	}
	return value()
}
