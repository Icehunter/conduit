package keybindings

import (
	"os"
	"path/filepath"
	"testing"

	tea "charm.land/bubbletea/v2"
)

func TestParseUserJSON_Basic(t *testing.T) {
	in := []byte(`{
	  "bindings": [
	    {"context": "Chat", "bindings": {"ctrl+k": "chat:cancel"}}
	  ]
	}`)
	got, err := ParseUserJSON(in)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Context != "Chat" {
		t.Fatalf("bad parse: %+v", got)
	}
	if got[0].Bindings["ctrl+k"] != "chat:cancel" {
		t.Errorf("missing binding: %+v", got[0])
	}
}

func TestParseUserJSON_NullUnbinds(t *testing.T) {
	in := []byte(`{
	  "bindings": [
	    {"context": "Global", "bindings": {"ctrl+c": null}}
	  ]
	}`)
	got, err := ParseUserJSON(in)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 {
		t.Fatalf("expected 1 block, got %d", len(got))
	}
	if len(got[0].Unbinds) != 1 || got[0].Unbinds[0] != "ctrl+c" {
		t.Errorf("expected ctrl+c unbind, got %+v", got[0].Unbinds)
	}
	if _, present := got[0].Bindings["ctrl+c"]; present {
		t.Errorf("null entry should not appear in Bindings map")
	}
}

func TestLoadUserFile_MissingReturnsEmpty(t *testing.T) {
	got, err := LoadUserFile(filepath.Join(t.TempDir(), "nonexistent.json"))
	if err != nil {
		t.Errorf("missing file should be a no-op, got error %v", err)
	}
	if len(got) != 0 {
		t.Errorf("missing file should return empty, got %+v", got)
	}
}

func TestLoadUserFile_ReadsAndParses(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "keybindings.json")
	body := []byte(`{
	  "bindings": [
	    {"context": "Chat", "bindings": {"ctrl+k": "chat:stash"}}
	  ]
	}`)
	if err := os.WriteFile(path, body, 0o644); err != nil {
		t.Fatal(err)
	}
	got, err := LoadUserFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Bindings["ctrl+k"] != "chat:stash" {
		t.Errorf("bad load: %+v", got)
	}
}

func TestLoadUserFile_MalformedJSON(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "keybindings.json")
	if err := os.WriteFile(path, []byte("{not json"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := LoadUserFile(path)
	if err == nil {
		t.Errorf("expected error on malformed JSON")
	}
}

func TestLoadAll_DefaultsPlusUserOverride(t *testing.T) {
	dir := t.TempDir()
	body := []byte(`{
	  "bindings": [
	    {"context": "Global", "bindings": {"ctrl+c": "custom:override"}}
	  ]
	}`)
	if err := os.WriteFile(filepath.Join(dir, "keybindings.json"), body, 0o644); err != nil {
		t.Fatal(err)
	}
	bindings, err := LoadAll(dir)
	if err != nil {
		t.Fatal(err)
	}
	r := NewResolver(bindings)
	res := r.Resolve(tea.KeyPressMsg{Code: 'c', Mod: tea.ModCtrl}, "Global")
	if res.Action != "custom:override" {
		t.Errorf("expected user override, got %+v", res)
	}
}
