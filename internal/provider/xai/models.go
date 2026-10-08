package xai

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/icehunter/conduit/internal/catalog"
)

// DefaultContextWindow is used for a model the listing gives no length for.
const DefaultContextWindow = 256000

type modelsResponse struct {
	Data []struct {
		ID            string `json:"id"`
		ContextLength int    `json:"context_length"`
		// CompletionTextTokenPrice is set only for text-generating models;
		// image and video models carry other prices. Its presence is the
		// language-model filter.
		CompletionTextTokenPrice *int64 `json:"completion_text_token_price"`
		// PromptImageTokenPrice above zero marks models taking image input.
		PromptImageTokenPrice int64 `json:"prompt_image_token_price"`
		Capabilities          *struct {
			ReasoningEffort []string `json:"reasoning_effort"`
		} `json:"capabilities"`
	} `json:"data"`
}

// FetchModels lists the text models the account's plan grants.
func FetchModels(ctx context.Context, accessToken string) ([]catalog.ModelInfo, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(apiBaseURL(), "/")+"/models", nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Authorization", "Bearer "+accessToken)
	resp, err := httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("xai: fetch model catalog: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, fmt.Errorf("xai: read model catalog: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("xai: model catalog: %s", resp.Status)
	}
	var payload modelsResponse
	if err := json.Unmarshal(body, &payload); err != nil {
		return nil, fmt.Errorf("xai: decode model catalog: %w", err)
	}
	now := time.Now()
	var out []catalog.ModelInfo
	for _, m := range payload.Data {
		if m.ID == "" || m.CompletionTextTokenPrice == nil {
			continue
		}
		window := m.ContextLength
		if window <= 0 {
			window = DefaultContextWindow
		}
		out = append(out, catalog.ModelInfo{
			ID: m.ID, Name: modelName(m.ID), Provider: ProviderID, ContextWindow: window,
			ToolUse: true, Vision: m.PromptImageTokenPrice > 0,
			Thinking:  m.Capabilities != nil && len(m.Capabilities.ReasoningEffort) > 0,
			FetchedAt: now, Origin: "live",
		})
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("xai: model catalog was empty")
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].ID > out[j].ID })
	return out, nil
}

// LiveModels lists this credential's models from the xAI API. An error means
// the caller should use FallbackModels.
func (a *Authorizer) LiveModels(ctx context.Context) ([]catalog.ModelInfo, error) {
	cred, err := a.EnsureFresh(ctx)
	if err != nil {
		return nil, err
	}
	return FetchModels(ctx, cred.AccessToken)
}

// modelName turns "grok-4-fast" into "Grok 4 Fast"; the listing has no names.
func modelName(id string) string {
	var words []string
	for _, w := range strings.Split(id, "-") {
		if w != "" {
			words = append(words, strings.ToUpper(w[:1])+w[1:])
		}
	}
	return strings.Join(words, " ")
}

// FallbackModels is used when the live listing is unavailable.
func FallbackModels() []catalog.ModelInfo {
	now := time.Now()
	mk := func(id string, vision bool) catalog.ModelInfo {
		return catalog.ModelInfo{ID: id, Name: modelName(id), Provider: ProviderID, ContextWindow: DefaultContextWindow, ToolUse: true, Vision: vision, Thinking: true, FetchedAt: now}
	}
	return []catalog.ModelInfo{mk("grok-4", true), mk("grok-code-fast-1", false)}
}

// ContextWindowForModel is the window for a model: current when set, else the
// fallback entry's, else DefaultContextWindow.
func ContextWindowForModel(model string, current int) int {
	if current > 0 {
		return current
	}
	for _, info := range FallbackModels() {
		if strings.EqualFold(info.ID, model) {
			return info.ContextWindow
		}
	}
	return DefaultContextWindow
}
