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
		c.applyHeaders(req.Header, "")
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
