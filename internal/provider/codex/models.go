package codex

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/icehunter/conduit/internal/api"
	"github.com/icehunter/conduit/internal/catalog"
)

// InputLimit is the largest prompt the Codex backend accepts. Its models
// advertise a larger context window, but input above this is rejected, so it
// is the window auto-compact works against.
const InputLimit = 272000

// modelsClientVersion is the codex client version sent to the models endpoint,
// which requires one and gates the catalog by it. conduit runs its own agent
// loop, so a deliberately high value asks for everything the plan grants.
const modelsClientVersion = "999.0.0"

var modelsBaseURL = CodexBaseURL

// nonAgenticMarkers name models that are not chat/agent models: batch, media,
// realtime, search, open-weights and chat-latest aliases.
var nonAgenticMarkers = []string{"batch", "image", "audio", "realtime", "transcribe", "tts", "search", "deep-research", "oss", "chat-latest", "-chat"}

// IsAgenticModel reports whether id is a Codex model conduit can drive as an
// agent: the gpt-5 and gpt-6 lines, without the -pro variants and the non-chat
// models.
func IsAgenticModel(id string) bool {
	id = strings.ToLower(strings.TrimSpace(strings.TrimPrefix(id, "openai/")))
	if !strings.HasPrefix(id, "gpt-5") && !strings.HasPrefix(id, "gpt-6") {
		return false
	}
	if id == "gpt-5-pro" || strings.HasSuffix(id, "-pro") || strings.Contains(id, "-pro-") {
		return false
	}
	for _, marker := range nonAgenticMarkers {
		if strings.Contains(id, marker) {
			return false
		}
	}
	return true
}

type modelsResponse struct {
	Models []struct {
		Slug                     string `json:"slug"`
		DisplayName              string `json:"display_name"`
		Visibility               string `json:"visibility"`
		ContextWindow            int    `json:"context_window"`
		SupportedReasoningLevels []struct {
			Effort string `json:"effort"`
		} `json:"supported_reasoning_levels"`
	} `json:"models"`
}

// FetchModels lists the models the ChatGPT plan grants through the Codex
// backend. Only entries the backend marks "list" and IsAgenticModel accepts
// are returned, with context windows capped at InputLimit.
func FetchModels(ctx context.Context, accessToken, accountID string) ([]catalog.ModelInfo, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(modelsBaseURL, "/")+"/models?client_version="+modelsClientVersion, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Authorization", "Bearer "+accessToken)
	req.Header.Set("originator", "conduit")
	if accountID != "" {
		req.Header.Set("ChatGPT-Account-Id", accountID)
	}
	resp, err := httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("codex: fetch model catalog: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, fmt.Errorf("codex: read model catalog: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("codex: model catalog: %s", resp.Status)
	}
	var payload modelsResponse
	if err := json.Unmarshal(body, &payload); err != nil {
		return nil, fmt.Errorf("codex: decode model catalog: %w", err)
	}
	now := time.Now()
	var out []catalog.ModelInfo
	for _, m := range payload.Models {
		if m.Slug == "" || m.Visibility != "list" || !IsAgenticModel(m.Slug) {
			continue
		}
		name := m.DisplayName
		if name == "" {
			name = m.Slug
		}
		window := m.ContextWindow
		if window <= 0 || window > InputLimit {
			window = InputLimit
		}
		out = append(out, catalog.ModelInfo{
			ID: m.Slug, Name: name, Provider: ProviderID, ContextWindow: window,
			ToolUse: true, Vision: true, Thinking: len(m.SupportedReasoningLevels) > 0,
			FetchedAt: now, Origin: "live",
		})
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("codex: model catalog was empty")
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].ID > out[j].ID })
	return out, nil
}

// LiveModelIDs lists this credential's models from the Codex backend. An error
// means the caller should use another source (the shared catalog or
// FallbackModelIDs).
func (a *Authorizer) LiveModelIDs(ctx context.Context) ([]string, error) {
	cred, err := a.EnsureFresh(ctx)
	if err != nil {
		return nil, err
	}
	models, err := FetchModels(ctx, cred.AccessToken, cred.Metadata["account_id"])
	if err != nil {
		return nil, err
	}
	ids := make([]string, 0, len(models))
	for _, m := range models {
		ids = append(ids, m.ID)
	}
	return ids, nil
}

// RequestHeaders returns the per-request headers for a Codex call: session_id
// is the conduit session the request belongs to (the same value as
// prompt_cache_key), else fallback.
func RequestHeaders(fallback string) func(*api.MessageRequest) map[string]string {
	return func(req *api.MessageRequest) map[string]string {
		id := fallback
		if req != nil && req.Hints != nil && req.Hints.SessionID != "" {
			id = req.Hints.SessionID
		}
		if id == "" {
			return nil
		}
		return map[string]string{"session_id": id}
	}
}
