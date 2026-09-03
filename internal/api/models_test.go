package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

// TestListModels_FixtureReplay mirrors the actual JSON shape captured from a
// live call to https://api.anthropic.com/v1/models against a real Max
// subscription (OAuth Bearer + anthropic-beta: oauth-2025-04-20, no other
// beta header needed) — verified this returns 200, not 401, for OAuth
// tokens, not just x-api-key.
func TestListModels_FixtureReplay(t *testing.T) {
	const fixture = `{
		"data": [
			{
				"id": "claude-fable-5-1",
				"display_name": "Claude Fable 5.1",
				"created_at": "2026-08-28T00:00:00Z",
				"max_input_tokens": 1000000,
				"max_tokens": 128000,
				"type": "model",
				"capabilities": {
					"image_input": {"supported": true},
					"thinking": {"supported": true, "types": {"adaptive": {"supported": true}, "enabled": {"supported": false}}}
				}
			},
			{
				"id": "claude-haiku-4-5-20251001",
				"display_name": "Claude Haiku 4.5",
				"created_at": "2025-10-15T00:00:00Z",
				"max_input_tokens": 200000,
				"max_tokens": 64000,
				"type": "model",
				"capabilities": {
					"image_input": {"supported": true},
					"thinking": {"supported": true, "types": {"adaptive": {"supported": false}, "enabled": {"supported": true}}}
				}
			}
		],
		"has_more": false,
		"first_id": "claude-fable-5-1",
		"last_id": "claude-haiku-4-5-20251001"
	}`

	var gotAuth, gotBeta, gotPath string
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/models", func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		gotBeta = r.Header.Get("anthropic-beta")
		gotPath = r.URL.Path
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(fixture))
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	c := NewClient(Config{
		BaseURL:     srv.URL,
		AuthToken:   "test-oauth-token",
		BetaHeaders: []string{"oauth-2025-04-20"},
	}, srv.Client())

	models, err := c.ListModels(context.Background())
	if err != nil {
		t.Fatalf("ListModels: %v", err)
	}

	if gotPath != "/v1/models" {
		t.Errorf("path = %q, want /v1/models", gotPath)
	}
	if gotAuth != "Bearer test-oauth-token" {
		t.Errorf("Authorization = %q, want OAuth Bearer", gotAuth)
	}
	if gotBeta == "" {
		t.Error("anthropic-beta header missing — OAuth requests must carry it")
	}

	if len(models) != 2 {
		t.Fatalf("got %d models, want 2", len(models))
	}

	fable := models[0]
	if fable.ID != "claude-fable-5-1" {
		t.Errorf("ID = %q, want claude-fable-5-1", fable.ID)
	}
	if fable.Name != "Claude Fable 5.1" {
		t.Errorf("Name = %q, want Claude Fable 5.1", fable.Name)
	}
	if fable.Provider != "anthropic" {
		t.Errorf("Provider = %q, want anthropic", fable.Provider)
	}
	if fable.ContextWindow != 1_000_000 {
		t.Errorf("ContextWindow = %d, want 1000000", fable.ContextWindow)
	}
	if !fable.Thinking {
		t.Error("Thinking = false, want true (capabilities.thinking.supported)")
	}
	if !fable.Vision {
		t.Error("Vision = false, want true (capabilities.image_input.supported)")
	}
	if !fable.ToolUse {
		t.Error("ToolUse = false, want true — every current Claude model supports tools")
	}
	// Pricing is not in this endpoint's response — ListModels must not invent it.
	if fable.InputCostPer1M != 0 || fable.OutputCostPer1M != 0 {
		t.Errorf("cost fields = %v/%v, want zero (filled in later by the caller, not ListModels)", fable.InputCostPer1M, fable.OutputCostPer1M)
	}

	// Haiku's capabilities.thinking.supported is true even though it only
	// supports the older "enabled" config (not "adaptive") — Thinking maps
	// the top-level supported flag, matching the real live response shape,
	// not a specific thinking-type flag.
	haiku := models[1]
	if !haiku.Thinking {
		t.Error("Haiku Thinking = false, want true per capabilities.thinking.supported=true")
	}
}

// TestListModels_Paginates verifies the after_id cursor loop actually walks
// multiple pages rather than stopping at the first.
func TestListModels_Paginates(t *testing.T) {
	page1 := `{"data":[{"id":"model-a","display_name":"A"}],"has_more":true,"last_id":"model-a"}`
	page2 := `{"data":[{"id":"model-b","display_name":"B"}],"has_more":false,"last_id":"model-b"}`

	var afterIDs []string
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/models", func(w http.ResponseWriter, r *http.Request) {
		after := r.URL.Query().Get("after_id")
		afterIDs = append(afterIDs, after)
		w.Header().Set("Content-Type", "application/json")
		if after == "" {
			_, _ = w.Write([]byte(page1))
		} else {
			_, _ = w.Write([]byte(page2))
		}
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	c := NewClient(Config{BaseURL: srv.URL, AuthToken: "t"}, srv.Client())
	models, err := c.ListModels(context.Background())
	if err != nil {
		t.Fatalf("ListModels: %v", err)
	}
	if len(models) != 2 {
		t.Fatalf("got %d models, want 2 (one per page)", len(models))
	}
	if models[0].ID != "model-a" || models[1].ID != "model-b" {
		t.Fatalf("got IDs %q, %q, want model-a, model-b in order", models[0].ID, models[1].ID)
	}
	if len(afterIDs) != 2 || afterIDs[0] != "" || afterIDs[1] != "model-a" {
		t.Fatalf("after_id sequence = %v, want [\"\", \"model-a\"]", afterIDs)
	}
}

// TestListModels_APIError verifies a non-2xx response surfaces as an error,
// not a silently-empty model list.
func TestListModels_APIError(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/models", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"type":"error","error":{"type":"authentication_error","message":"invalid token"}}`))
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	c := NewClient(Config{BaseURL: srv.URL, AuthToken: "t"}, srv.Client())
	_, err := c.ListModels(context.Background())
	if err == nil {
		t.Fatal("expected an error for a 401 response")
	}
}
