package models

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestDiscoverGeminiModels(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1beta/models" {
			t.Fatalf("path = %q", r.URL.Path)
		}
		if got := r.URL.Query().Get("key"); got != "test-key" {
			t.Fatalf("key = %q", got)
		}
		_, _ = w.Write([]byte(`{"models":[{"name":"models/gemini-1.5-pro","supportedGenerationMethods":["generateContent"],"inputTokenLimit":1000},{"name":"models/embedding-001","supportedGenerationMethods":["embedContent"]}]}`))
	}))
	t.Cleanup(srv.Close)

	models, err := DiscoverGeminiModels(context.Background(), srv.URL+"/v1beta", "test-key")
	if err != nil {
		t.Fatalf("discover: %v", err)
	}
	if len(models) != 1 || models[0] != "gemini-1.5-pro" {
		t.Fatalf("models = %#v", models)
	}
}
