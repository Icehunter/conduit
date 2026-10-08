package commands

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/icehunter/conduit/internal/skillusage"
)

func TestSkillsHistoryAndRevert(t *testing.T) {
	t.Setenv("CONDUIT_CONFIG_DIR", filepath.Join(t.TempDir(), ".conduit"))
	p := filepath.Join(t.TempDir(), "skills", "demo", "SKILL.md")
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	for _, c := range []string{"v1", "v2"} {
		if err := skillusage.TrackEdit(skillusage.ActorCurator, skillusage.OpUpdate, "demo", p, func() error {
			return os.WriteFile(p, []byte(c), 0o644)
		}); err != nil {
			t.Fatal(err)
		}
	}

	hist := skillsHistory("demo")
	if hist.Type != "text" || !strings.Contains(hist.Text, "curator") || !strings.Contains(hist.Text, "demo") {
		t.Fatalf("history = %+v", hist)
	}
	if got := skillsHistory("other"); !strings.Contains(got.Text, "No recorded") {
		t.Errorf("filtered history = %q", got.Text)
	}

	id := skillusage.LedgerList("demo", 1)[0].ID
	if got := skillsRevert(id); got.Type != "text" {
		t.Fatalf("revert = %+v", got)
	}
	if b, _ := os.ReadFile(p); string(b) != "v1" {
		t.Errorf("file = %q", b)
	}
	for _, bad := range []string{"", "nope"} {
		if got := skillsRevert(bad); got.Type != "error" {
			t.Errorf("revert(%q) = %+v", bad, got)
		}
	}
}
