package catalog

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
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

// fetchURL returns the effective catalog URL.
// CONDUIT_CATALOG_URL overrides the default OpenRouter endpoint.
func fetchURL() string {
	if u := strings.TrimSpace(os.Getenv("CONDUIT_CATALOG_URL")); u != "" {
		return u
	}
	return openRouterURL
}

// Fetch downloads a fresh catalog and returns it.
// If CONDUIT_CATALOG_FILE is set, that local JSON file is read instead of
// making an HTTP request (useful for offline testing and private catalogs).
// If CONDUIT_CATALOG_URL is set, that URL is used instead of the default
// OpenRouter endpoint.
// On any failure, Fetch returns the built-in snapshot and the error so callers
// can surface a warning without blocking the workflow.
func Fetch(ctx context.Context) (*Catalog, error) {
	// Local file override — no network call.
	if path := strings.TrimSpace(os.Getenv("CONDUIT_CATALOG_FILE")); path != "" {
		data, err := os.ReadFile(path)
		if err != nil {
			return Builtin(), fmt.Errorf("catalog: read local file %s: %w", path, err)
		}
		var payload openRouterResponse
		if err := json.Unmarshal(data, &payload); err != nil {
			return Builtin(), fmt.Errorf("catalog: decode local file %s: %w", path, err)
		}
		models := normalizeOpenRouter(payload)
		return &Catalog{Models: models, FetchedAt: time.Now(), Source: "file"}, nil
	}

	ctx, cancel := context.WithTimeout(ctx, fetchTimeout)
	defer cancel()

	url := fetchURL()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
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
	cat := &Catalog{
		Models:    models,
		FetchedAt: time.Now(),
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
