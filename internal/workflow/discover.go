package workflow

import (
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/icehunter/conduit/internal/settings"
)

// Saved is a workflow script found on disk.
type Saved struct {
	// Name is meta.name when the script loads, else the file stem.
	Name string
	Path string
	Meta Meta
	// Err is non-empty when the file could not be loaded; the entry is kept
	// so the UI can show the broken script instead of silently hiding it.
	Err string
}

// SavedDir is the user-level directory saved workflows are written to.
func SavedDir() string {
	return filepath.Join(settings.ConduitDir(), "workflows")
}

// Discover returns saved workflows. Discovery order (later entries override
// earlier ones with the same name):
//  1. ~/.conduit/workflows/*.js
//  2. ~/.claude/workflows/*.js
//  3. <cwd>/.claude/workflows/*.js
//
// Symlinked files are followed, matching the skills and agents loaders.
func Discover(cwd string) []Saved {
	dirs := []string{
		SavedDir(),
		filepath.Join(settings.ClaudeDir(), "workflows"),
	}
	if cwd != "" {
		dirs = append(dirs, filepath.Join(cwd, ".claude", "workflows"))
	}
	byName := map[string]Saved{}
	var order []string
	for _, dir := range dirs {
		entries, err := os.ReadDir(dir)
		if err != nil {
			continue
		}
		for _, e := range entries {
			if !strings.HasSuffix(e.Name(), ".js") {
				continue
			}
			path := filepath.Join(dir, e.Name())
			info, err := os.Stat(path)
			if err != nil || info.IsDir() {
				continue
			}
			s := loadSaved(path)
			if _, seen := byName[s.Name]; !seen {
				order = append(order, s.Name)
			}
			byName[s.Name] = s
		}
	}
	out := make([]Saved, 0, len(order))
	for _, n := range order {
		out = append(out, byName[n])
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// Find returns the saved workflow named name, or nil.
func Find(cwd, name string) *Saved {
	for _, s := range Discover(cwd) {
		if s.Name == name && s.Err == "" {
			return &s
		}
	}
	return nil
}

func loadSaved(path string) Saved {
	stem := strings.TrimSuffix(filepath.Base(path), ".js")
	src, err := os.ReadFile(path) //nolint:gosec // workflow files are user-owned
	if err != nil {
		return Saved{Name: stem, Path: path, Err: err.Error()}
	}
	sc, err := Load(string(src))
	if err != nil {
		return Saved{Name: stem, Path: path, Err: err.Error()}
	}
	return Saved{Name: sc.Meta.Name, Path: path, Meta: sc.Meta}
}
