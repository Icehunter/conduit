# Provider model discovery Implementation Plan

> **For agentic workers:** REQUIRED: Use superpowers:subagent-driven-development (if subagents available) or superpowers:executing-plans to implement this plan. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Discover and persist provider models on add, refresh per provider instance every 24h, show discovery errors in picker, and apply Copilot headers to all Copilot calls without fallback lists.

**Architecture:** Add a per-provider instance model discovery service that fetches models via provider-specific endpoints, persists models into `providers`, tracks `lastFetchedAt` per provider key, and surfaces discovery errors in the model picker. Update Copilot headers in all HTTP calls; remove fallback model usage.

**Tech Stack:** Go, Bubble Tea TUI, settings persistence, tests via `go test`.

---

## File Structure (planned changes)

- Create:
  - `internal/provider/models/discovery.go` (provider model discovery + refresh orchestration)
  - `internal/provider/models/discovery_test.go`
- Modify:
  - `internal/settings/conduitconfig.go` (store per-provider discovery metadata)
  - `internal/settings/settingsprovider.go` (persist models + refresh timestamps)
  - `internal/commands/builtin.go` (use discovery error rows in picker payload)
  - `internal/tui/commandresultshandlers.go` (render discovery error rows under provider sections)
  - `internal/tui/providerspanel.go` (trigger discovery after add)
  - `internal/provider/copilot/copilot.go` (apply full headers on all Copilot API calls)
  - `internal/provider/copilot/copilot_test.go`
  - `internal/provider/openai/openai.go` (if exists: hook discovery into OpenAI-compatible clients)
  - `internal/provider/gemini/gemini.go` (if exists: discovery helper)
  - `internal/tui/render_test.go` (picker row rendering for error row)
- Docs:
  - `docs/superpowers/specs/2026-05-11-provider-model-discovery-design.md`

---

## Chunk 1: Discovery metadata + persistence

### Task 1: Add discovery metadata fields

**Files:**
- Modify: `internal/settings/conduitconfig.go`
- Test: `internal/settings/settings_test.go`

- [ ] **Step 1: Write failing test for new metadata**

```go
func TestConduitConfig_ProviderDiscoveryMetadata(t *testing.T) {
    cfg := ConduitConfig{
        ProviderModelDiscovery: map[string]ProviderDiscoveryState{
            "openai-compatible.gemini": {LastFetchedAt: time.Unix(1, 0), LastError: "boom"},
        },
    }
    data, err := json.Marshal(cfg)
    if err != nil {
        t.Fatalf("marshal: %v", err)
    }
    var decoded ConduitConfig
    if err := json.Unmarshal(data, &decoded); err != nil {
        t.Fatalf("unmarshal: %v", err)
    }
    if decoded.ProviderModelDiscovery["openai-compatible.gemini"].LastError != "boom" {
        t.Fatalf("missing discovery metadata: %#v", decoded.ProviderModelDiscovery)
    }
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/settings -run ProviderDiscoveryMetadata`
Expected: FAIL (field missing).

- [ ] **Step 3: Add metadata struct + config fields**

```go
type ProviderDiscoveryState struct {
    LastFetchedAt time.Time `json:"lastFetchedAt,omitempty"`
    LastError     string    `json:"lastError,omitempty"`
}

type ConduitConfig struct {
    // ...
    ProviderModelDiscovery map[string]ProviderDiscoveryState `json:"providerModelDiscovery,omitempty"`
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/settings -run ProviderDiscoveryMetadata`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add internal/settings/conduitconfig.go internal/settings/settings_test.go
# no commit per instructions
```

---

## Chunk 2: Provider model discovery service

### Task 2: Implement discovery logic (Gemini + OpenAI-compatible)

**Files:**
- Create: `internal/provider/models/discovery.go`
- Test: `internal/provider/models/discovery_test.go`

- [ ] **Step 1: Write failing test for Gemini /models discovery**

```go
func TestDiscoverGeminiModels(t *testing.T) {
    srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        if r.URL.Path != "/v1beta/models" {
            t.Fatalf("path = %q", r.URL.Path)
        }
        if r.URL.Query().Get("key") != "test-key" {
            t.Fatalf("missing key")
        }
        _, _ = w.Write([]byte(`{"models":[{"name":"models/gemini-1.5-pro","supportedGenerationMethods":["generateContent"],"inputTokenLimit":1000}]}`))
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
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/provider/models -run DiscoverGeminiModels`
Expected: FAIL.

- [ ] **Step 3: Implement Gemini discovery**

```go
func DiscoverGeminiModels(ctx context.Context, baseURL, apiKey string) ([]string, error) {
    req, _ := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(baseURL, "/")+"/models", nil)
    q := req.URL.Query()
    q.Set("key", apiKey)
    req.URL.RawQuery = q.Encode()
    // decode JSON, filter by supportedGenerationMethods includes "generateContent"
}
```

- [ ] **Step 4: Implement OpenAI-compatible discovery**

```go
func DiscoverOpenAICompatibleModels(ctx context.Context, baseURL, apiKey string) ([]string, error) {
    req, _ := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(baseURL, "/")+"/models", nil)
    req.Header.Set("Authorization", "Bearer "+apiKey)
    req.Header.Set("Accept", "application/json")
    // decode {data:[{id:string}]} or {models:[{id:string}]}
}
```

- [ ] **Step 5: Run tests**

Run: `go test ./internal/provider/models -run Discover`
Expected: PASS

---

## Chunk 3: Persist discovered models + 24h refresh

### Task 3: Persist models and discovery state

**Files:**
- Modify: `internal/settings/settingsprovider.go`
- Test: `internal/settings/settingsprovider_test.go`

- [ ] **Step 1: Write failing test for refresh metadata**

```go
func TestUpdateProviderDiscoveryState(t *testing.T) {
    dir := t.TempDir()
    t.Setenv("CONDUIT_CONFIG_DIR", filepath.Join(dir, ".conduit"))
    if err := UpdateProviderDiscoveryState("openai-compatible.gemini", time.Unix(2,0), ""); err != nil {
        t.Fatalf("update: %v", err)
    }
    cfg, _ := LoadConduitConfig()
    if cfg.ProviderModelDiscovery["openai-compatible.gemini"].LastFetchedAt.IsZero() {
        t.Fatalf("missing timestamp")
    }
}
```

- [ ] **Step 2: Run test to verify failure**

Run: `go test ./internal/settings -run UpdateProviderDiscoveryState`
Expected: FAIL.

- [ ] **Step 3: Implement update helpers**

```go
func UpdateProviderDiscoveryState(key string, fetchedAt time.Time, errMsg string) error
func ShouldRefreshProviderModels(state ProviderDiscoveryState, now time.Time) bool
```

- [ ] **Step 4: Run test**

Run: `go test ./internal/settings -run UpdateProviderDiscoveryState`
Expected: PASS

---

## Chunk 4: TUI integration + picker error rows

### Task 4: Trigger discovery after add, display failures

**Files:**
- Modify: `internal/tui/providerspanel.go`
- Modify: `internal/tui/commandresultshandlers.go`
- Test: `internal/tui/providerspanel_test.go`

- [ ] **Step 1: Add discovery trigger after provider add**

- For OpenAI-compatible provider add, run discovery in a goroutine, persist models, update lastFetchedAt or lastError.

- [ ] **Step 2: Add picker error rows**

- Extend picker payload to include a sentinel row (e.g. `Value:"error:..."` + label) under provider sections when discovery error exists.

- [ ] **Step 3: Run tests**

Run: `go test ./internal/tui -run ProviderDiscovery`
Expected: PASS.

---

## Chunk 5: Copilot headers across all calls

### Task 5: Apply headers to Copilot API calls

**Files:**
- Modify: `internal/provider/copilot/copilot.go`
- Test: `internal/provider/copilot/copilot_test.go`

- [ ] **Step 1: Update header functions**

- Ensure `ModelDiscoveryHeaders` includes full header set.
- Ensure all Copilot API requests use `Headers()` or the full set.

- [ ] **Step 2: Add/adjust tests**

- Verify headers include `Editor-Version`, `Editor-Plugin-Version`, `Copilot-Integration-Id`, `User-Agent` in model discovery fetch.

- [ ] **Step 3: Run tests**

Run: `go test ./internal/provider/copilot -run Headers`
Expected: PASS

---

## Chunk 6: Full verification

- [ ] **Step 1: Run package tests**

Run: `go test ./internal/provider/models ./internal/settings ./internal/tui ./internal/provider/copilot`
Expected: PASS

- [ ] **Step 2: Run full suite**

Run: `make verify`
Expected: PASS
