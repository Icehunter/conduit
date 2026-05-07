package keybindings

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
)

// rawFile mirrors File but lets us decode `null` action values, which
// can't go through map[string]string. Each block becomes a Block + a
// list of unbound keys.
type rawFile struct {
	Schema   string     `json:"$schema,omitempty"`
	Docs     string     `json:"$docs,omitempty"`
	Bindings []rawBlock `json:"bindings"`
}

type rawBlock struct {
	Context  string                     `json:"context"`
	Bindings map[string]json.RawMessage `json:"bindings"`
}

// LoadUserFile reads keybindings.json at path and returns parsed blocks.
// Missing file → no error, empty result. Returns a wrapped error if the
// JSON is malformed or contains an unknown shape.
func LoadUserFile(path string) ([]Block, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, nil
		}
		return nil, fmt.Errorf("read keybindings file %s: %w", path, err)
	}
	return ParseUserJSON(data)
}

// ParseUserJSON is exposed for tests and for callers (like /doctor) that
// want to validate JSON without writing a file first.
func ParseUserJSON(data []byte) ([]Block, error) {
	var raw rawFile
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil, fmt.Errorf("parse keybindings JSON: %w", err)
	}
	out := make([]Block, 0, len(raw.Bindings))
	for _, blk := range raw.Bindings {
		b := Block{Context: blk.Context, Bindings: map[string]string{}}
		for keystr, val := range blk.Bindings {
			s := string(val)
			if s == "null" {
				b.Unbinds = append(b.Unbinds, keystr)
				continue
			}
			var action string
			if err := json.Unmarshal(val, &action); err != nil {
				return nil, fmt.Errorf("parse keybinding %q in context %s: %w", keystr, blk.Context, err)
			}
			b.Bindings[keystr] = action
		}
		out = append(out, b)
	}
	return out, nil
}

// UserFilePath returns keybindings.json inside the supplied config directory.
// Conduit passes ~/.conduit first and falls back to ~/.claude for legacy users.
func UserFilePath(configDir string) string {
	return filepath.Join(configDir, "keybindings.json")
}

// LoadAll loads defaults + user bindings, with user entries appended last
// so they shadow defaults in the resolver's last-wins lookup.
func LoadAll(configDir string) ([]Binding, error) {
	all := ParseBlocks(DefaultBlocks())
	user, err := LoadUserFile(UserFilePath(configDir))
	if err != nil {
		return all, err
	}
	all = append(all, ParseBlocks(user)...)
	return all, nil
}
