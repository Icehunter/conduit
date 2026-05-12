package models

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
)

type geminiModel struct {
	Name                       string   `json:"name"`
	SupportedGenerationMethods []string `json:"supportedGenerationMethods"`
}

type geminiModelsResponse struct {
	Models []geminiModel `json:"models"`
}

func DiscoverGeminiModels(ctx context.Context, baseURL, apiKey string) ([]string, error) {
	if strings.TrimSpace(apiKey) == "" {
		return nil, fmt.Errorf("gemini: missing api key")
	}
	baseURL = strings.TrimSpace(baseURL)
	if baseURL == "" {
		return nil, fmt.Errorf("gemini: missing base url")
	}
	url := strings.TrimRight(baseURL, "/") + "/models"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, fmt.Errorf("gemini: build request: %w", err)
	}
	q := req.URL.Query()
	q.Set("key", apiKey)
	req.URL.RawQuery = q.Encode()
	req.Header.Set("Accept", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("gemini: fetch: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("gemini: server returned %d", resp.StatusCode)
	}

	var payload geminiModelsResponse
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		return nil, fmt.Errorf("gemini: decode: %w", err)
	}

	models := make([]string, 0, len(payload.Models))
	for _, model := range payload.Models {
		if !supportsGeminiGeneration(model.SupportedGenerationMethods, "generateContent") {
			continue
		}
		id := strings.TrimSpace(model.Name)
		if id == "" {
			continue
		}
		id = strings.TrimPrefix(id, "models/")
		if id == "" {
			continue
		}
		models = append(models, id)
	}
	return models, nil
}

func supportsGeminiGeneration(methods []string, target string) bool {
	for _, method := range methods {
		if method == target {
			return true
		}
	}
	return false
}
