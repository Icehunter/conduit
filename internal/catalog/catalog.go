// Package catalog maintains a local model capability database sourced from
// OpenRouter's public API and supplemented by a built-in Anthropic snapshot.
// It is intentionally side-effect-free on import — nothing is fetched or read
// until the caller explicitly asks.
package catalog

import (
	"strings"
	"time"
)

// ModelInfo describes one model's capabilities.
type ModelInfo struct {
	ID              string    `json:"id"`
	Name            string    `json:"name"`
	Provider        string    `json:"provider"`
	ContextWindow   int       `json:"contextWindow"`
	InputCostPer1M  float64   `json:"inputCostPer1M"`  // USD per 1M input tokens
	OutputCostPer1M float64   `json:"outputCostPer1M"` // USD per 1M output tokens
	ToolUse         bool      `json:"toolUse"`
	Vision          bool      `json:"vision"`
	Thinking        bool      `json:"thinking"`
	FetchedAt       time.Time `json:"fetchedAt"`
}

// Catalog is a snapshot of available models.
type Catalog struct {
	Models    []ModelInfo `json:"models"`
	FetchedAt time.Time   `json:"fetchedAt"`
	Source    string      `json:"source"` // "builtin" | "openrouter" | "cache"
}

// Lookup returns the ModelInfo for the given model ID (exact or fuzzy match).
// Handles provider-prefixed IDs ("anthropic/claude-opus-4-7"), picker keys
// ("provider:openai-compatible.work.gpt-5.5"), case differences, and dot/dash
// variants ("claude-sonnet-4.6" vs "claude-sonnet-4-6").
// Returns false if not found.
func (c *Catalog) Lookup(id string) (ModelInfo, bool) {
	if c == nil {
		return ModelInfo{}, false
	}
	id = strings.ToLower(strings.TrimSpace(id))
	if id == "" {
		return ModelInfo{}, false
	}

	queries := lookupAliases(id)

	// First loop: exact match on raw or prefix-stripped form.
	for _, m := range c.Models {
		mID := strings.ToLower(strings.TrimSpace(m.ID))
		for _, query := range queries {
			if mID == query {
				return m, true
			}
		}
	}

	// Second loop: strip provider prefix from stored ID, then match with
	// separator normalization so "claude-sonnet-4.6" matches "claude-sonnet-4-6".
	for _, m := range c.Models {
		mID := strings.ToLower(strings.TrimSpace(m.ID))
		stripped := mID
		if idx := strings.Index(mID, "/"); idx >= 0 {
			stripped = mID[idx+1:]
		}
		strippedNorm := normalizeSeparators(stripped)
		for _, query := range queries {
			if stripped == query || strippedNorm == normalizeSeparators(query) {
				return m, true
			}
		}
	}

	return ModelInfo{}, false
}

func lookupAliases(id string) []string {
	aliases := []string{id}
	if idx := strings.Index(id, "/"); idx >= 0 {
		aliases = append(aliases, id[idx+1:])
	}
	for {
		idx := strings.Index(id, ".")
		if idx < 0 || idx+1 >= len(id) {
			break
		}
		id = id[idx+1:]
		aliases = append(aliases, id)
	}

	out := aliases[:0]
	seen := make(map[string]bool, len(aliases))
	for _, alias := range aliases {
		if alias == "" || seen[alias] {
			continue
		}
		seen[alias] = true
		out = append(out, alias)
	}
	return out
}

// normalizeSeparators replaces dots with dashes so "4.6" and "4-6" compare equal.
func normalizeSeparators(s string) string {
	return strings.ReplaceAll(s, ".", "-")
}

// ForProvider returns all models for the given provider (case-insensitive).
func (c *Catalog) ForProvider(provider string) []ModelInfo {
	if c == nil {
		return nil
	}
	provider = strings.ToLower(provider)
	var out []ModelInfo
	for _, m := range c.Models {
		if strings.ToLower(m.Provider) == provider {
			out = append(out, m)
		}
	}
	return out
}

// IsStale reports whether the catalog is older than ttl.
func (c *Catalog) IsStale(ttl time.Duration) bool {
	if c == nil || c.FetchedAt.IsZero() {
		return true
	}
	return time.Since(c.FetchedAt) > ttl
}

// Builtin returns the baked-in catalog snapshot. Never fails.
func Builtin() *Catalog {
	models := builtinModels()
	now := time.Now()
	for i := range models {
		models[i].FetchedAt = now
	}
	return &Catalog{Models: models, FetchedAt: now, Source: "builtin"}
}
