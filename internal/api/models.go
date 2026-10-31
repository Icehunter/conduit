package api

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/icehunter/conduit/internal/catalog"
)

// ListModels calls the real Anthropic GET /v1/models endpoint and returns the
// account-visible model list. Uses the same auth (OAuth Bearer or x-api-key
// via applyHeaders) and retry/401-refresh (doWithRetryAndAuth) as every other
// request — no separate auth path. Verified live: this endpoint works with
// OAuth Max/Pro tokens, not just API keys, and needs no beta header.
//
// Pricing is not returned by this endpoint (confirmed against the live
// response and Anthropic's own docs) — callers must fill InputCostPer1M /
// OutputCostPer1M from elsewhere (see catalog.Merge, which uses the pricing
// table in cost.go).
func (c *Client) ListModels(ctx context.Context) ([]catalog.ModelInfo, error) {
	var out []catalog.ModelInfo
	afterID := ""
	for {
		page, hasMore, lastID, err := c.listModelsPage(ctx, afterID)
		if err != nil {
			return nil, err
		}
		out = append(out, page...)
		if !hasMore || lastID == "" {
			return out, nil
		}
		afterID = lastID
	}
}

func (c *Client) listModelsPage(ctx context.Context, afterID string) (page []catalog.ModelInfo, hasMore bool, lastID string, err error) {
	url := strings.TrimRight(c.cfg.BaseURL, "/") + "/v1/models?limit=100"
	if afterID != "" {
		url += "&after_id=" + afterID
	}
	resp, err := c.doWithRetryAndAuth(ctx, func() (*http.Response, error) {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
		if err != nil {
			return nil, fmt.Errorf("api: build models request: %w", err)
		}
		// model="" — this is not a Messages request, so the model-scoped beta
		// filtering applyHeaders also does is a no-op here.
		c.applyHeaders(req.Header, "", nil, nil)
		return c.http.Do(req)
	})
	if err != nil {
		return nil, false, "", err
	}
	defer func() { _, _ = io.Copy(io.Discard, resp.Body); _ = resp.Body.Close() }()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, false, "", c.decodeError(resp)
	}

	var parsed modelsListResponse
	if err := json.NewDecoder(resp.Body).Decode(&parsed); err != nil {
		return nil, false, "", fmt.Errorf("api: decode models response: %w", err)
	}

	page = make([]catalog.ModelInfo, 0, len(parsed.Data))
	for _, m := range parsed.Data {
		page = append(page, catalog.ModelInfo{
			ID:            m.ID,
			Name:          m.DisplayName,
			Provider:      "anthropic",
			ContextWindow: m.MaxInputTokens,
			ToolUse:       true, // every current Claude model supports tool use
			Vision:        m.Capabilities.ImageInput.Supported,
			Thinking:      m.Capabilities.Thinking.Supported,
		})
	}
	return page, parsed.HasMore, parsed.LastID, nil
}

// modelsListResponse is the shape of GET /v1/models, verified against the
// live endpoint (not just documentation).
type modelsListResponse struct {
	Data    []modelsListEntry `json:"data"`
	HasMore bool              `json:"has_more"`
	LastID  string            `json:"last_id"`
}

type modelsListEntry struct {
	ID             string               `json:"id"`
	DisplayName    string               `json:"display_name"`
	MaxInputTokens int                  `json:"max_input_tokens"`
	MaxTokens      int                  `json:"max_tokens"`
	Capabilities   modelsListCapability `json:"capabilities"`
}

type modelsListCapability struct {
	ImageInput struct {
		Supported bool `json:"supported"`
	} `json:"image_input"`
	Thinking struct {
		Supported bool `json:"supported"`
	} `json:"thinking"`
}

// ListModelsForOpenAICompatible fetches and parses the /v1/models endpoint for
// an OpenAI-compatible server (llama.cpp, LM Studio, vLLM, etc.). It returns
// one catalog.ModelInfo per model, extracting context window from whichever
// source the server provides:
//
//   - llama.cpp: parses "--ctx-size N" from status.args
//   - standard OpenAI: uses the "context_window" field (when present)
//   - fallback: 0 (caller may fill from catalog or settings)
//
// This endpoint is always a single-page GET (no pagination) and does not
// require the auth/retry machinery used by the Anthropic endpoint.
// normalizeOpenAICompatibleURL strips a trailing "/v1" suffix so callers
// can safely append any OpenAI-compatible API path.  Users sometimes
// configure base URLs like "http://host:port/v1/" when tools (Ollama,
// LM Studio, etc.) display that path — this helper makes both formats
// work.
func normalizeOpenAICompatibleURL(baseURL string) string {
	u := strings.TrimRight(baseURL, "/")
	if strings.HasSuffix(u, "/v1") {
		u = u[:len(u)-3]
	}
	return u
}

func ListModelsForOpenAICompatible(ctx context.Context, baseURL string, httpClient *http.Client) ([]catalog.ModelInfo, error) {
	url := normalizeOpenAICompatibleURL(baseURL) + "/v1/models"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, fmt.Errorf("api: build models request: %w", err)
	}

	resp, err := httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("api: fetch models: %w", err)
	}
	defer func() { _, _ = io.Copy(io.Discard, resp.Body); _ = resp.Body.Close() }()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<16))
		return nil, fmt.Errorf("api: models %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}

	var parsed openAIModelsResponse
	if err := json.NewDecoder(resp.Body).Decode(&parsed); err != nil {
		return nil, fmt.Errorf("api: decode models response: %w", err)
	}

	out := make([]catalog.ModelInfo, 0, len(parsed.Data))
	for _, m := range parsed.Data {
		info := catalog.ModelInfo{
			ID:       m.ID,
			Name:     m.Name,
			Provider: m.OwnedBy,
			Origin:   "live",
		}

		// Standard OpenAI field (most servers).
		if info.ContextWindow <= 0 && m.ContextWindow > 0 {
			info.ContextWindow = m.ContextWindow
		}

		// llama.cpp: extract ctx-size from status.args or status.preset.
		if info.ContextWindow <= 0 && m.Status != nil {
			if size := parseLlamaCtxSize(m.Status); size > 0 {
				info.ContextWindow = size
			}
		}

		// Infer vision from architecture input modalities.
		if m.Architecture != nil {
			for _, mod := range m.Architecture.InputModalities {
				if mod == "image" {
					info.Vision = true
					break
				}
			}
			for _, mod := range m.Architecture.OutputModalities {
				if mod == "reasoning" {
					info.Thinking = true
					break
				}
			}
		}

		out = append(out, info)
	}
	return out, nil
}

// parseLlamaCtxSize extracts --ctx-size from a llama.cpp status string.
func parseLlamaCtxSize(s *openAIModelStatus) int {
	if len(s.Args) > 0 {
		if size := parseArgsCtxSize(s.Args); size > 0 {
			return size
		}
	}
	if s.Preset != "" {
		if size := parsePresetCtxSize(s.Preset); size > 0 {
			return size
		}
	}
	return 0
}

// parseArgsCtxSize extracts --ctx-size from a space-separated argument string.
func parseArgsCtxSize(args []string) int {
	parts := args
	for i, p := range parts {
		if p == "--ctx-size" && i+1 < len(parts) {
			if n, err := parseInt(parts[i+1]); err == nil && n > 0 {
				return n
			}
		}
	}
	return 0
}

// parsePresetCtxSize extracts ctx-size from an INI-style preset block.
func parsePresetCtxSize(preset string) int {
	for _, line := range strings.Split(preset, "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "ctx-size") {
			parts := strings.SplitN(line, "=", 2)
			if len(parts) == 2 {
				if n, err := parseInt(strings.TrimSpace(parts[1])); err == nil && n > 0 {
					return n
				}
			}
		}
	}
	return 0
}

// openAIModelsResponse is the shape of GET /v1/models for OpenAI-compatible
// servers (llama.cpp, LM Studio, vLLM, etc.).
type openAIModelsResponse struct {
	Data []openAIModelEntry `json:"data"`
}

type openAIModelEntry struct {
	ID            string             `json:"id"`
	Name          string             `json:"name"`
	ContextWindow int                `json:"context_window"`
	OwnedBy       string             `json:"owned_by"`
	Architecture  *openAIModelArch   `json:"architecture"`
	Status        *openAIModelStatus `json:"status"`
	MaxCompletion int                `json:"max_completion_tokens"`
	Created       int64              `json:"created"`
	Object        string             `json:"object"`
	CanRemove     bool               `json:"can_remove"`
}

type openAIModelArch struct {
	InputModalities  []string `json:"input_modalities"`
	OutputModalities []string `json:"output_modalities"`
}

type openAIModelStatus struct {
	Value  string   `json:"value"`
	Args   jsonArgs `json:"args"`
	Preset string   `json:"preset"`
}

// jsonArgs accepts the llama.cpp `status.args` field in either form:
// older builds serialize it as a single space-separated string, newer
// builds as a JSON array of strings. Both normalize to a slice.
type jsonArgs []string

func (a *jsonArgs) UnmarshalJSON(b []byte) error {
	var arr []string
	if err := json.Unmarshal(b, &arr); err == nil {
		*a = arr
		return nil
	}
	var s string
	if err := json.Unmarshal(b, &s); err != nil {
		return err
	}
	*a = strings.Fields(s)
	return nil
}

// parseInt is a local helper to keep the package self-contained.
func parseInt(s string) (int, error) {
	var n int
	_, err := fmt.Sscanf(s, "%d", &n)
	return n, err
}
