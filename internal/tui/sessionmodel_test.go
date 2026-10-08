package tui

import (
	"path/filepath"
	"testing"

	"github.com/icehunter/conduit/internal/agent"
	"github.com/icehunter/conduit/internal/api"
	"github.com/icehunter/conduit/internal/session"
	"github.com/icehunter/conduit/internal/settings"
	"github.com/icehunter/conduit/internal/tool"
)

func TestRestoreSessionModel(t *testing.T) {
	text := []api.Message{{Role: "assistant", Content: []api.ContentBlock{{Type: "text", Text: "hi"}}}}
	routed := []api.Message{{Role: "assistant", ProviderKind: "openai-compatible", Provider: "gpt", Content: text[0].Content}}
	tests := []struct {
		name      string
		want      string
		family    string
		history   []api.Message
		localMode bool
		wantModel string
	}{
		{"recorded model restored", "claude-opus-5-5", "anthropic", text, false, "claude-opus-5-5"},
		{"nothing recorded keeps current", "", "", text, false, "claude-sonnet-5-5"},
		{"provider-routed session keeps current", "claude-opus-5-5", "anthropic", routed, false, "claude-sonnet-5-5"},
		{"same model is a no-op", "claude-sonnet-5-5", "anthropic", text, false, "claude-sonnet-5-5"},
		{"recorded on another family keeps current", "gpt-5", "openai-compatible", text, false, "claude-sonnet-5-5"},
		{"current provider is local keeps current", "claude-opus-5-5", "anthropic", text, true, "claude-sonnet-5-5"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			lp := agent.NewLoop(nil, tool.NewRegistry(), agent.LoopConfig{Model: "claude-sonnet-5-5"})
			m := idleModel()
			m.modelName = "claude-sonnet-5-5"
			m.cfg.Loop = lp
			m.localMode = tt.localMode
			m.restoreSessionModel(tt.history, tt.want, tt.family)
			if m.modelName != tt.wantModel || lp.Model() != tt.wantModel {
				t.Errorf("model = %q / loop %q, want %q", m.modelName, lp.Model(), tt.wantModel)
			}
		})
	}
}

func TestRestoreSkipsWhenCurrentProviderIsNotAnthropic(t *testing.T) {
	lp := agent.NewLoop(nil, tool.NewRegistry(), agent.LoopConfig{Model: "gpt-5"})
	m := idleModel()
	m.modelName = "gpt-5"
	m.cfg.Loop = lp
	m.roles = map[string]string{settings.RoleDefault: "p"}
	m.providers = map[string]settings.ActiveProviderSettings{"p": {Kind: settings.ProviderKindOpenAICompatible, Model: "gpt-5", BaseURL: "https://x.test/v1", Credential: "k"}}
	if m.providerFamily() == providerFamilyAnthropic {
		t.Skip("provider not available in this configuration")
	}
	m.restoreSessionModel([]api.Message{{Role: "assistant"}}, "claude-opus-5-5", "anthropic")
	if lp.Model() != "gpt-5" {
		t.Errorf("claude model applied to a non-Anthropic client: %q", lp.Model())
	}
}

func TestSessionRecordsAndReadsModelFamily(t *testing.T) {
	sess := session.FromFile(filepath.Join(t.TempDir(), "s.jsonl"))
	if err := sess.SetModel("claude-opus-5-5", "anthropic"); err != nil {
		t.Fatal(err)
	}
	if model, fam := sess.ReadModel(); model != "claude-opus-5-5" || fam != "anthropic" {
		t.Errorf("ReadModel = %q, %q", model, fam)
	}
	msg := loadResumeMsg(sess.FilePath).(resumeLoadMsg)
	if msg.model != "claude-opus-5-5" || msg.modelFamily != "anthropic" {
		t.Errorf("load Cmd did not read the model: %+v", msg)
	}
}

func TestPersistNewMessagesRecordsModelOnce(t *testing.T) {
	sess := session.FromFile(filepath.Join(t.TempDir(), "s.jsonl"))
	m := idleModel()
	m.cfg.Session = sess
	m.modelName = "claude-opus-5-5"
	hist := []api.Message{{Role: "user", Content: []api.ContentBlock{{Type: "text", Text: "q"}}}}
	m.persistNewMessages(hist)
	m.persistNewMessages(hist)
	if got, _ := sess.ReadModel(); got != "claude-opus-5-5" {
		t.Fatalf("ReadModel = %q", got)
	}
	m.modelName = "claude-sonnet-5-5"
	m.persistNewMessages(hist)
	if got, _ := sess.ReadModel(); got != "claude-sonnet-5-5" {
		t.Errorf("model change not recorded, ReadModel = %q", got)
	}
}
