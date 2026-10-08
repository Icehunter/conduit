package api

import (
	"net"
	"net/url"
	"strings"
)

// reasoningEffort maps a thinking budget onto the OpenAI effort scale:
// ≥8000 tokens is high, ≥2000 medium, anything else low. "" means thinking is
// off and no effort is sent.
func reasoningEffort(t *ThinkingConfig) string {
	switch {
	case t == nil || t.BudgetTokens <= 0:
		return ""
	case t.BudgetTokens >= 8000:
		return "high"
	case t.BudgetTokens >= 2000:
		return "medium"
	default:
		return "low"
	}
}

// reasoningCapableModel reports whether model belongs to a family that takes a
// reasoning effort: OpenAI o-series and gpt-5, gpt-oss, DeepSeek, Qwen3/QwQ,
// Grok 3 mini and 4, Gemini 2.5 and 3, GLM 4.5+, Kimi K2 thinking. Vendor
// prefixes ("openai/gpt-5", "accounts/x/models/qwen3-8b") are ignored. Other
// models, gpt-4o for one, answer an unknown reasoning_effort with a 400.
func reasoningCapableModel(model string) bool {
	m := strings.ToLower(strings.TrimSpace(model))
	if i := strings.LastIndex(m, "/"); i >= 0 {
		m = m[i+1:]
	}
	for _, o := range []string{"o1", "o3", "o4"} {
		if m == o || strings.HasPrefix(m, o+"-") {
			return true
		}
	}
	return hasAnyPrefix(m, "gpt-5", "gpt-oss", "deepseek", "qwen3", "qwq", "grok-3-mini", "grok-4",
		"gemini-2.5", "gemini-3", "glm-4.5", "glm-4.6", "glm-4.7", "glm-5", "kimi-k2-thinking", "kimi-k2.5")
}

func hasAnyPrefix(s string, prefixes ...string) bool {
	for _, p := range prefixes {
		if strings.HasPrefix(s, p) {
			return true
		}
	}
	return false
}

// sendsReasoningEffort reports whether a request for model at baseURL carries
// reasoning effort. Local and LAN servers always get it: llama.cpp and the
// like ignore what they don't know, and their model names say little.
func sendsReasoningEffort(baseURL, model string) bool {
	return isLocalBaseURL(baseURL) || reasoningCapableModel(model)
}

// mentionsReasoningEffort reports whether a 400 body complains about the
// reasoning parameter, the cue to resend without it.
func mentionsReasoningEffort(body []byte) bool {
	s := strings.ToLower(string(body))
	return strings.Contains(s, "reasoning_effort") ||
		strings.Contains(s, "reasoning") && containsAny(s, "unsupported", "not supported", "unrecognized", "unknown")
}

// extraBodyDisablesThinking reports whether ExtraBody turns reasoning off in
// one of the common dialects (llama.cpp/vLLM chat_template_kwargs, DashScope
// enable_thinking, Z.AI/Kimi thinking.type, OpenRouter reasoning.enabled), in
// which case no reasoning_effort is added on top.
func extraBodyDisablesThinking(extra map[string]any) bool {
	if on, ok := extra["enable_thinking"].(bool); ok && !on {
		return true
	}
	if kwargs, ok := extra["chat_template_kwargs"].(map[string]any); ok {
		if on, ok := kwargs["enable_thinking"].(bool); ok && !on {
			return true
		}
	}
	if thinking, ok := extra["thinking"].(map[string]any); ok && thinking["type"] == "disabled" {
		return true
	}
	if reasoning, ok := extra["reasoning"].(map[string]any); ok {
		if on, ok := reasoning["enabled"].(bool); ok && !on {
			return true
		}
	}
	return false
}

// Values for Config.ReasoningReplay.
const (
	ReasoningReplayAuto = "auto"
	ReasoningReplayOn   = "on"
	ReasoningReplayOff  = "off"
)

// reasoningPolicy says which assistant turns carry their reasoning back to an
// OpenAI-compatible server, in which field, and whether to ask Qwen chat
// templates to keep it.
type reasoningPolicy struct {
	toolTurns bool // replay on assistant turns that made tool calls
	allTurns  bool // replay on every assistant turn
	field     string
	// preserveThinking sends chat_template_kwargs.preserve_thinking, which
	// makes Qwen3.6+ templates render <think> for older turns too.
	preserveThinking bool
}

func (p reasoningPolicy) replays(hasToolCalls bool) bool {
	return p.allTurns || (hasToolCalls && p.toolTurns)
}

// reasoningPolicy resolves Config.ReasoningReplay for model, following
// oh-my-pi (5e006e7f81, ac958d5905, 3cc61d3014 and its compat resolver).
// "auto" replays on every turn for local and LAN servers, whose prefix KV
// cache only survives when the chat template re-renders the prior <think>
// blocks, and for DeepSeek and MiMo, which require reasoning on every
// assistant turn (api-docs.deepseek.com/guides/thinking_mode: with a tools
// parameter reasoning_content "must be fully passed back ... in all subsequent
// requests — even for turns where the model did not perform a tool call", else
// the API returns 400; without tools it is accepted and ignored, so the old
// deepseek-reasoner 400 on replayed reasoning no longer applies). Kimi and OpenRouter get it on tool-call turns, which
// they require there. Claude models are left out: their reasoning only replays
// with a signature, which this wire never has. Qwen's preserve_thinking
// follows the every-turn rule.
func (c *Client) reasoningPolicy(model string) reasoningPolicy {
	p := reasoningPolicy{field: c.replayField()}
	lower := strings.ToLower(model)
	switch strings.ToLower(strings.TrimSpace(c.cfg.ReasoningReplay)) {
	case ReasoningReplayOff:
		return p
	case ReasoningReplayOn:
		p.toolTurns, p.allTurns = true, true
	default:
		if strings.Contains(lower, "claude") {
			return p
		}
		p.allTurns = isLocalBaseURL(c.cfg.BaseURL) || containsAny(lower, "deepseek", "mimo")
		p.toolTurns = p.allTurns || strings.Contains(lower, "kimi") || baseURLHost(c.cfg.BaseURL) == "openrouter.ai"
	}
	p.preserveThinking = p.allTurns && containsAny(lower, "qwen", "qwq")
	return p
}

// replayField is the field replayed reasoning goes in: the one this provider
// streamed reasoning in, else reasoning_content.
func (c *Client) replayField() string {
	c.mu.Lock()
	field := c.reasoningField
	c.mu.Unlock()
	if field == "" {
		return "reasoning_content"
	}
	return field
}

// noteReasoningField records the delta field the provider streams reasoning
// in, for replayField.
func (c *Client) noteReasoningField(field string) {
	c.mu.Lock()
	c.reasoningField = field
	c.mu.Unlock()
}

// splitsThinkTags reports whether <think> blocks in visible content become
// thinking blocks. The official OpenAI endpoint never leaks reasoning tags, so
// it is left alone.
func (c *Client) splitsThinkTags() bool {
	return baseURLHost(c.cfg.BaseURL) != "api.openai.com"
}

// baseURLHost is the lowercased host name of baseURL, without the port.
func baseURLHost(baseURL string) string {
	u, err := url.Parse(strings.TrimSpace(baseURL))
	if err != nil {
		return ""
	}
	return strings.ToLower(u.Hostname())
}

// isLocalBaseURL reports whether baseURL points at this machine or the local
// network: localhost, *.local, or a loopback, private (RFC 1918 / ULA),
// link-local or unspecified address.
func isLocalBaseURL(baseURL string) bool {
	host := baseURLHost(baseURL)
	if host == "" {
		return false
	}
	if host == "localhost" || strings.HasSuffix(host, ".localhost") || strings.HasSuffix(host, ".local") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && (ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() || ip.IsUnspecified())
}
