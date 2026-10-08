package main

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/icehunter/conduit/internal/pendingedits"
	"github.com/icehunter/conduit/internal/settings"
	"github.com/icehunter/conduit/internal/tui"
)

func TestConfiguredCodingMainDoesNotHideExplicitSelection(t *testing.T) {
	s := &settings.Merged{Roles: map[string]string{settings.RoleMain: "main", settings.RoleDefault: "default"}, Providers: map[string]settings.ActiveProviderSettings{
		"main":    {Kind: settings.ProviderKindOpenAICompatible, Model: "invalid-main"},
		"default": {Kind: settings.ProviderKindOpenAICompatible, Model: "fallback", BaseURL: "http://localhost:8000/v1"},
	}}
	p, err := configuredCodingMain(s)
	if err != nil || p == nil || p.Model != "invalid-main" {
		t.Fatalf("explicit selection lost: provider=%+v error=%v", p, err)
	}
	delete(s.Providers, "main")
	if _, err := configuredCodingMain(s); err == nil {
		t.Fatal("missing explicit provider silently fell back")
	}
}

func TestPrepareVerifiedEditsFlushesOnlyExplicitDecision(t *testing.T) {
	for _, explicit := range []bool{false, true} {
		t.Run(map[bool]string{false: "dismissed", true: "confirmed"}[explicit], func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "file.go")
			table := pendingedits.NewTable()
			_ = table.Stage(pendingedits.Entry{Path: path, NewContent: []byte("approved")})
			hook := &tui.DiffReviewHook{AskReview: func(context.Context) tui.DiffReviewResult {
				entries := table.Drain()
				return tui.DiffReviewResult{Approved: entries, Original: entries, ExplicitDecision: explicit}
			}}
			err := prepareVerifiedEdits(context.Background(), table, hook, nil)
			data, readErr := os.ReadFile(path)
			if explicit {
				if err != nil || readErr != nil || string(data) != "approved" {
					t.Fatalf("err=%v read=%v data=%q", err, readErr, data)
				}
			} else {
				if err == nil || !os.IsNotExist(readErr) || table.Len() != 1 {
					t.Fatalf("err=%v read=%v remaining=%d", err, readErr, table.Len())
				}
			}
		})
	}
}
