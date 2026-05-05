// Package migrations runs one-shot settings upgrades on startup.
// Each migration has a stable ID; completed IDs are written to
// ~/.claude/settings.json under "completedMigrations" (matching the TS
// implementation) so they never re-run across restarts.
//
// Mirrors src/migrations/ in the CC TypeScript source.
package migrations

import (
	"encoding/json"
	"os"
	"path/filepath"
)

// Migration is a one-shot upgrade to run against settings.json.
type Migration struct {
	ID  string
	Run func(claudeDir string) error
}

// all is the ordered list of migrations. Add new ones at the end.
var all = []Migration{
	modelNormalize,
}

// modelNormalize maps legacy explicit model strings to their current
// aliases. Models: sonnet-4-5 → sonnet-4-6 alias, opus-4 stings, etc.
// Only touches user-level settings (not project/.claude/settings.json).
var modelNormalize = Migration{
	ID: "model-normalize-v1",
	Run: func(claudeDir string) error {
		path := filepath.Join(claudeDir, "settings.json")
		data, err := os.ReadFile(path)
		if err != nil {
			return nil // missing file is fine
		}
		var raw map[string]json.RawMessage
		if err := json.Unmarshal(data, &raw); err != nil {
			return nil // malformed — don't corrupt
		}
		modelRaw, ok := raw["model"]
		if !ok {
			return nil
		}
		var model string
		if err := json.Unmarshal(modelRaw, &model); err != nil {
			return nil
		}

		// Normalize known legacy strings to current aliases.
		aliases := map[string]string{
			// Sonnet 4.5 explicit strings → alias for current Sonnet
			"claude-sonnet-4-5-20250929":     "claude-sonnet-4-6",
			"claude-sonnet-4-5-20250929[1m]": "claude-sonnet-4-6",
			"sonnet-4-5-20250929":            "claude-sonnet-4-6",
			"sonnet-4-5":                     "claude-sonnet-4-6",
			// Sonnet 3.5 variants → current Sonnet
			"claude-3-5-sonnet-20241022": "claude-sonnet-4-6",
			"claude-3-5-sonnet-20240620": "claude-sonnet-4-6",
			// Opus 3 → Opus 4
			"claude-3-opus-20240229": "claude-opus-4-7",
			// Haiku 3.5 → Haiku 4.5
			"claude-3-5-haiku-20241022": "claude-haiku-4-5-20251001",
		}
		newModel, found := aliases[model]
		if !found {
			return nil
		}

		raw["model"], _ = json.Marshal(newModel)
		out, err := json.MarshalIndent(raw, "", "  ")
		if err != nil {
			return nil
		}
		return os.WriteFile(path, out, 0o644)
	},
}

// Run executes all pending migrations for claudeDir. Already-completed
// migrations are skipped. Errors from individual migrations are logged
// but do not abort subsequent ones.
func Run(claudeDir string) {
	completed := loadCompleted(claudeDir)
	for _, m := range all {
		if completed[m.ID] {
			continue
		}
		_ = m.Run(claudeDir)
		completed[m.ID] = true
		saveCompleted(claudeDir, completed)
	}
}

func completedPath(claudeDir string) string {
	return filepath.Join(claudeDir, "settings.json")
}

func loadCompleted(claudeDir string) map[string]bool {
	data, err := os.ReadFile(completedPath(claudeDir))
	if err != nil {
		return map[string]bool{}
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		return map[string]bool{}
	}
	rawList, ok := raw["completedMigrations"]
	if !ok {
		return map[string]bool{}
	}
	var list []string
	if err := json.Unmarshal(rawList, &list); err != nil {
		return map[string]bool{}
	}
	out := map[string]bool{}
	for _, id := range list {
		out[id] = true
	}
	return out
}

func saveCompleted(claudeDir string, completed map[string]bool) {
	path := completedPath(claudeDir)
	data, err := os.ReadFile(path)

	var raw map[string]json.RawMessage
	if err == nil && len(data) > 0 {
		if err := json.Unmarshal(data, &raw); err != nil {
			return
		}
	} else if err != nil && !os.IsNotExist(err) {
		return
	}
	if raw == nil {
		raw = map[string]json.RawMessage{}
	}

	ids := make([]string, 0, len(completed))
	for id := range completed {
		ids = append(ids, id)
	}
	encoded, _ := json.Marshal(ids)
	raw["completedMigrations"] = encoded

	out, err := json.MarshalIndent(raw, "", "  ")
	if err != nil {
		return
	}
	_ = os.WriteFile(path, out, 0o644)
}
