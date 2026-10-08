package plugins

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/icehunter/conduit/internal/settings"
)

func TestMergeHooksFrom_UserPromptSubmit(t *testing.T) {
	hook := func(cmd string) []settings.HookMatcher {
		return []settings.HookMatcher{{Hooks: []settings.Hook{{Type: "command", Command: cmd}}}}
	}
	base := &settings.HooksSettings{UserPromptSubmit: hook("base"), TaskCompleted: hook("done")}
	ps := []*Plugin{{Hooks: settings.HooksSettings{UserPromptSubmit: hook("plugin")}}}

	got := MergeHooksFrom(ps, base)
	if len(got.UserPromptSubmit) != 2 || got.UserPromptSubmit[0].Hooks[0].Command != "base" || got.UserPromptSubmit[1].Hooks[0].Command != "plugin" {
		t.Errorf("UserPromptSubmit = %+v, want base then plugin", got.UserPromptSubmit)
	}
	if len(got.TaskCompleted) != 1 {
		t.Errorf("TaskCompleted = %+v, want the base entry kept", got.TaskCompleted)
	}
}

func TestLoadHooks_TagsAndMergesEveryEvent(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "hooks"), 0o755); err != nil {
		t.Fatal(err)
	}
	const hooksJSON = `{"hooks":{
		"TeammateIdle":[{"hooks":[{"type":"command","command":"idle"}]}],
		"TaskCreated":[{"hooks":[{"type":"command","command":"created"}]}],
		"TaskCompleted":[{"hooks":[{"type":"command","command":"completed"}]}],
		"UserPromptSubmit":[{"hooks":[{"type":"command","command":"prompt"}]}]}}`
	if err := os.WriteFile(filepath.Join(dir, "hooks", "hooks.json"), []byte(hooksJSON), 0o644); err != nil {
		t.Fatal(err)
	}
	h := loadHooks(dir)
	merged := MergeHooksFrom([]*Plugin{{Hooks: h}}, nil)
	for name, ms := range map[string][]settings.HookMatcher{
		"TeammateIdle": merged.TeammateIdle, "TaskCreated": merged.TaskCreated,
		"TaskCompleted": merged.TaskCompleted, "UserPromptSubmit": merged.UserPromptSubmit,
	} {
		if len(ms) != 1 || ms[0].PluginRoot != dir {
			t.Errorf("%s = %+v, want one matcher tagged with the plugin root", name, ms)
		}
	}
}
