package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

// TestListModelsForOpenAICompatible_LlamaCpp parses ctx-size from status.args.
func TestListModelsForOpenAICompatible_LlamaCpp(t *testing.T) {
	const body = `{
		"data": [{
			"id": "dagger-27b",
			"name": "Dagger Qwen3.6 27B",
			"owned_by": "llamacpp",
			"object": "model",
			"status": {
				"value": "unloaded",
				"args": "--host 127.0.0.1 --ctx-size 131072 --model /models/dagger.gguf --n-gpu-layers all",
				"preset": "[dagger-27b]\nctx-size = 131072\nn-gpu-layers = all\n"
			},
			"architecture": {
				"input_modalities": ["text", "image"],
				"output_modalities": ["text"]
			}
		}]
	}`

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(body))
	}))
	defer srv.Close()

	models, err := ListModelsForOpenAICompatible(context.Background(), srv.URL, srv.Client())
	if err != nil {
		t.Fatalf("ListModelsForOpenAICompatible: %v", err)
	}
	if len(models) != 1 {
		t.Fatalf("got %d models, want 1", len(models))
	}

	m := models[0]
	if m.ID != "dagger-27b" {
		t.Errorf("ID = %q, want dagger-27b", m.ID)
	}
	if m.ContextWindow != 131072 {
		t.Errorf("ContextWindow = %d, want 131072 (from status.args --ctx-size)", m.ContextWindow)
	}
	if m.Provider != "llamacpp" {
		t.Errorf("Provider = %q, want llamacpp", m.Provider)
	}
	if !m.Vision {
		t.Error("Vision = false, want true (input_modalities contains image)")
	}
}

// TestListModelsForOpenAICompatible_PresetFallback extracts ctx-size from preset when args lacks it.
func TestListModelsForOpenAICompatible_PresetFallback(t *testing.T) {
	const body = `{
		"data": [{
			"id": "my-local-7b",
			"name": "My Local 7B",
			"owned_by": "llamacpp",
			"object": "model",
			"status": {
				"value": "loaded",
				"args": "--host 0.0.0.0 --model /models/7b.gguf --n-gpu-layers all",
				"preset": "[my-local-7b]\nctx-size = 65536\nn-gpu-layers = 99\n"
			}
		}]
	}`

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(body))
	}))
	defer srv.Close()

	models, err := ListModelsForOpenAICompatible(context.Background(), srv.URL, srv.Client())
	if err != nil {
		t.Fatalf("ListModelsForOpenAICompatible: %v", err)
	}
	if len(models) != 1 {
		t.Fatalf("got %d models, want 1", len(models))
	}
	if models[0].ContextWindow != 65536 {
		t.Errorf("ContextWindow = %d, want 65536 (from status.preset ctx-size)", models[0].ContextWindow)
	}
}

// TestListModelsForOpenAICompatible_StandardOpenAI uses context_window field.
func TestListModelsForOpenAICompatible_StandardOpenAI(t *testing.T) {
	const body = `{
		"data": [{
			"id": "gpt-4.1-mini",
			"name": "GPT-4.1 Mini",
			"owned_by": "openai",
			"object": "model",
			"max_completion_tokens": 65536
		}]
	}`

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(body))
	}))
	defer srv.Close()

	models, err := ListModelsForOpenAICompatible(context.Background(), srv.URL, srv.Client())
	if err != nil {
		t.Fatalf("ListModelsForOpenAICompatible: %v", err)
	}
	if len(models) != 1 {
		t.Fatalf("got %d models, want 1", len(models))
	}
	if models[0].ContextWindow != 0 {
		t.Errorf("ContextWindow = %d, want 0 (no context_window or ctx-size in this response)", models[0].ContextWindow)
	}
	if models[0].Provider != "openai" {
		t.Errorf("Provider = %q, want openai", models[0].Provider)
	}
}

// TestListModelsForOpenAICompatible_ThinkingInference detects reasoning output modality.
func TestListModelsForOpenAICompatible_ThinkingInference(t *testing.T) {
	const body = `{
		"data": [{
			"id": "local-mtp-7b",
			"name": "Local MTP 7B",
			"owned_by": "llamacpp",
			"object": "model",
			"status": {
				"value": "loaded",
				"args": "--host 127.0.0.1 --ctx-size 32768 --spec-type draft-mtp --spec-draft-n-max 4"
			},
			"architecture": {
				"input_modalities": ["text"],
				"output_modalities": ["text", "reasoning"]
			}
		}]
	}`

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(body))
	}))
	defer srv.Close()

	models, err := ListModelsForOpenAICompatible(context.Background(), srv.URL, srv.Client())
	if err != nil {
		t.Fatalf("ListModelsForOpenAICompatible: %v", err)
	}
	if len(models) != 1 {
		t.Fatalf("got %d models, want 1", len(models))
	}
	if !models[0].Thinking {
		t.Error("Thinking = false, want true (output_modalities contains reasoning)")
	}
	if models[0].ContextWindow != 32768 {
		t.Errorf("ContextWindow = %d, want 32768", models[0].ContextWindow)
	}
}

// TestListModelsForOpenAICompatible_ParseArgs verifies --ctx-size parsing.
func TestListModelsForOpenAICompatible_ParseArgs(t *testing.T) {
	tests := []struct {
		args   []string
		expect int
	}{
		{[]string{"--ctx-size", "131072", "--batch", "512"}, 131072},
		{[]string{"--ctx-size", "8192"}, 8192},
		{[]string{"--host", "127.0.0.1", "--ctx-size", "4096", "--n-gpu-layers", "all"}, 4096},
		{[]string{"--host", "127.0.0.1"}, 0},
		{nil, 0},
	}
	for _, tt := range tests {
		got := parseArgsCtxSize(tt.args)
		if got != tt.expect {
			t.Errorf("parseArgsCtxSize(%q) = %d, want %d", tt.args, got, tt.expect)
		}
	}
}

// TestListModelsForOpenAICompatible_ArgsAsArray covers the newer llama.cpp
// builds that serialize status.args as a JSON array of strings instead of a
// single space-separated string. Both forms must parse.
func TestListModelsForOpenAICompatible_ArgsAsArray(t *testing.T) {
	const body = `{
		"data": [{
			"id": "dagger-27b",
			"name": "Dagger Qwen3.6 27B",
			"owned_by": "llamacpp",
			"object": "model",
			"status": {
				"value": "unloaded",
				"args": ["C:\\Users\\Public\\Downloads\\LLAMA\\llama-server.exe", "--host", "127.0.0.1", "--jinja", "--ctx-size", "131072", "--reasoning-effort", "medium"]
			}
		}]
	}`

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(body))
	}))
	defer srv.Close()

	models, err := ListModelsForOpenAICompatible(context.Background(), srv.URL, srv.Client())
	if err != nil {
		t.Fatalf("ListModelsForOpenAICompatible: %v", err)
	}
	if len(models) != 1 {
		t.Fatalf("got %d models, want 1", len(models))
	}
	if models[0].ID != "dagger-27b" {
		t.Errorf("ID = %q, want dagger-27b", models[0].ID)
	}
	if models[0].ContextWindow != 131072 {
		t.Errorf("ContextWindow = %d, want 131072 (from status.args array --ctx-size)", models[0].ContextWindow)
	}
}

// TestListModelsForOpenAICompatible_ParsePreset verifies ctx-size parsing from preset.
func TestListModelsForOpenAICompatible_ParsePreset(t *testing.T) {
	tests := []struct {
		preset string
		expect int
	}{
		{"[model]\nctx-size = 65536\nn-gpu-layers = all\n", 65536},
		{"ctx-size = 8192\nmmap = false\n", 8192},
		{"ctx-size=4096", 4096},
		{"[model]\nn-gpu-layers = all\n", 0},
	}
	for _, tt := range tests {
		got := parsePresetCtxSize(tt.preset)
		if got != tt.expect {
			t.Errorf("parsePresetCtxSize(%q) = %d, want %d", tt.preset, got, tt.expect)
		}
	}
}

// TestListModelsForOpenAICompatible_ServerError returns an error for non-2xx.
func TestListModelsForOpenAICompatible_ServerError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		w.Write([]byte(`{"error":{"message":"not found"}}`))
	}))
	defer srv.Close()

	_, err := ListModelsForOpenAICompatible(context.Background(), srv.URL, srv.Client())
	if err == nil {
		t.Fatal("expected error for 404 response")
	}
}

// TestListModelsForOpenAICompatible_BaseURLWithV1Suffix normalizes a base URL
// that already includes the /v1 prefix (common when users copy the URL from
// tools like LM Studio or Ollama) so the models endpoint is still correct.
func TestListModelsForOpenAICompatible_BaseURLWithV1Suffix(t *testing.T) {
	var capturedPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		capturedPath = r.URL.Path
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"data":[{"id":"test-model","object":"model","owned_by":"test"}]}`))
	}))
	defer srv.Close()

	// User configures baseURL as "http://host:port/v1/" (common in UI hints).
	baseURL := srv.URL + "/v1/"
	_, err := ListModelsForOpenAICompatible(context.Background(), baseURL, srv.Client())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if capturedPath != "/v1/models" {
		t.Errorf("path = %q, want /v1/models", capturedPath)
	}

	// Also test without trailing slash.
	capturedPath = ""
	_, err = ListModelsForOpenAICompatible(context.Background(), srv.URL+"/v1", srv.Client())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if capturedPath != "/v1/models" {
		t.Errorf("path = %q, want /v1/models", capturedPath)
	}

	// Test with /v1 suffix but no trailing slash.
	capturedPath = ""
	_, err = ListModelsForOpenAICompatible(context.Background(), srv.URL+"/v1", srv.Client())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if capturedPath != "/v1/models" {
		t.Errorf("path = %q, want /v1/models", capturedPath)
	}
}
