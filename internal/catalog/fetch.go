package catalog

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"
)

const (
	// DefaultTTL is how long a cached catalog is considered fresh.
	DefaultTTL = 24 * time.Hour
	// fetchTimeout caps the HTTP request so slow networks don't stall the UI.
	fetchTimeout = 10 * time.Second
	// openRouterURL is the public, unauthenticated model list endpoint.
	openRouterURL = "https://openrouter.ai/api/v1/models"
)

// Fetch downloads a fresh catalog from OpenRouter and returns it.
// On failure it returns the built-in snapshot and the underlying error so callers
// can choose to surface a warning without blocking the workflow.
func Fetch(ctx context.Context) (*Catalog, error) {
	ctx, cancel := context.WithTimeout(ctx, fetchTimeout)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, openRouterURL, nil)
	if err != nil {
		return Builtin(), fmt.Errorf("catalog: build request: %w", err)
	}
	req.Header.Set("Accept", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return Builtin(), fmt.Errorf("catalog: fetch: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return Builtin(), fmt.Errorf("catalog: server returned %d", resp.StatusCode)
	}

	var payload openRouterResponse
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		return Builtin(), fmt.Errorf("catalog: decode: %w", err)
	}

	models := normalizeOpenRouter(payload)
	now := time.Now()
	cat := &Catalog{
		Models:    models,
		FetchedAt: now,
		Source:    "openrouter",
	}
	return cat, nil
}

// FetchAndCache downloads a fresh catalog, caches it to disk, and returns it.
// On any error it returns the built-in snapshot and the error.
func FetchAndCache(ctx context.Context, conduitDir string) (*Catalog, error) {
	cat, err := Fetch(ctx)
	if err != nil {
		return cat, err
	}
	if saveErr := SaveCache(conduitDir, cat); saveErr != nil {
		return cat, fmt.Errorf("catalog: save cache: %w", saveErr)
	}
	return cat, nil
}

// openRouterResponse is the top-level JSON shape from openrouter.ai/api/v1/models.
type openRouterResponse struct {
	Data []openRouterModel `json:"data"`
}

type openRouterModel struct {
	ID           string                `json:"id"`
	Name         string                `json:"name"`
	ContextLen   int                   `json:"context_length"`
	Pricing      openRouterPricing     `json:"pricing"`
	Architecture openRouterArch        `json:"architecture"`
	TopProvider  openRouterTopProvider `json:"top_provider"`
}

type openRouterPricing struct {
	Prompt     string `json:"prompt"`     // USD per token (string like "0.000003")
	Completion string `json:"completion"` // USD per token
}

type openRouterArch struct {
	Modality     string `json:"modality"` // e.g. "text+image->text"
	Tokenizer    string `json:"tokenizer"`
	InstructType string `json:"instruct_type"`
}

type openRouterTopProvider struct {
	MaxCompletionTokens int  `json:"max_completion_tokens"`
	IsModerated         bool `json:"is_moderated"`
}

func normalizeOpenRouter(payload openRouterResponse) []ModelInfo {
	now := time.Now()
	models := make([]ModelInfo, 0, len(payload.Data))
	for _, m := range payload.Data {
		info := ModelInfo{
			ID:              m.ID,
			Name:            m.Name,
			Provider:        providerFromID(m.ID),
			ContextWindow:   m.ContextLen,
			InputCostPer1M:  parsePricingPer1M(m.Pricing.Prompt),
			OutputCostPer1M: parsePricingPer1M(m.Pricing.Completion),
			ToolUse:         true, // OpenRouter doesn't surface this flag; assume true for chat models
			Vision:          strings.Contains(m.Architecture.Modality, "image"),
			FetchedAt:       now,
		}
		models = append(models, info)
	}
	return models
}

// providerFromID extracts the provider slug from "provider/model-name" IDs.
func providerFromID(id string) string {
	if idx := strings.Index(id, "/"); idx >= 0 {
		return id[:idx]
	}
	return "unknown"
}

// parsePricingPer1M converts an OpenRouter per-token price string (e.g.
// "0.000003") to USD per 1M tokens.
func parsePricingPer1M(s string) float64 {
	s = strings.TrimSpace(s)
	if s == "" || s == "0" {
		return 0
	}
	var v float64
	if _, err := fmt.Sscanf(s, "%f", &v); err != nil {
		return 0
	}
	return v * 1_000_000
}
