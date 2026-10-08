package agent

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/icehunter/conduit/internal/api"
)

// TestBuildSystemBlocks_ReachOpenAICompatibleModels sends the real system
// blocks through an OpenAI-compatible client: the agent prompt, output
// guidance and CLAUDE.md instructions must reach the model; only the billing
// header and the Claude identity block stay on the Anthropic wire.
func TestBuildSystemBlocks_ReachOpenAICompatibleModels(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("CONDUIT_CONFIG_DIR", home+"/.conduit")
	claudeMd := "Codebase and user instructions are shown below.\n\nContents of /repo/CLAUDE.md:\n\nAlways run make verify."
	blocks := BuildSystemBlocks("", claudeMd, "", nil)

	var system string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Messages []struct{ Role, Content string } `json:"messages"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		if len(body.Messages) > 0 && body.Messages[0].Role == "system" {
			system = body.Messages[0].Content
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"id":"c","model":"m","choices":[{"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}]}`)
	}))
	defer srv.Close()
	c := api.NewClient(api.Config{ProviderKind: api.ProviderKindOpenAICompatible, BaseURL: srv.URL, APIKey: "k"}, srv.Client())
	if _, err := c.CreateMessage(context.Background(), &api.MessageRequest{
		Model:    "qwen3",
		System:   blocks,
		Messages: []api.Message{user("hi")},
	}); err != nil {
		t.Fatal(err)
	}

	for _, keep := range []string{
		strings.TrimSpace(MinimalAgentSystemPrompt),
		strings.TrimSpace(MinimalOutputGuidance),
		"Contents of /repo/CLAUDE.md",
		"Always run make verify.",
	} {
		if !strings.Contains(system, keep) {
			t.Errorf("system message lacks %.60q", keep)
		}
	}
	for _, drop := range []string{MinimalIdentitySystem, "x-anthropic-billing-header"} {
		if strings.Contains(system, drop) {
			t.Errorf("system message leaks %q", drop)
		}
	}
}
