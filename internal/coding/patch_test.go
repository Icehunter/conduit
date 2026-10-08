package coding

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestApplyDraftUsesCapturedPreimage(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "main.go")
	if err := os.WriteFile(path, []byte("return false\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	sources, err := ReadSources(root, []string{"main.go"})
	if err != nil {
		t.Fatal(err)
	}
	patch, err := ApplyDraft(context.Background(), root, sources, `{"edits":[{"path":"main.go","old":"false","new":"true"}]}`, func(context.Context, string) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "return true\n" || patch == "" {
		t.Fatalf("got %q patch %q", got, patch)
	}
	_, err = ApplyDraft(context.Background(), root, sources, `{"edits":[{"path":"main.go","old":"false","new":"true"}]}`, func(context.Context, string) error { return nil })
	if err == nil {
		t.Fatal("stale patch accepted")
	}
}

func TestInvalidDraftDoesNotMutateFiles(t *testing.T) {
	for _, draft := range []string{
		`{"edits":[{"path":"../escape","old":"","new":"bad"}]}`,
		`{"edits":[{"path":"a","old":"old","new":"new"},{"path":"unknown","old":"","new":"bad"}]}`,
		`{"edits":[{"path":"a","old":"missing","new":"bad"}]}`,
		`{"edits":[{"path":"a","old":"old","new":"new"}],"unexpected":true}`,
	} {
		t.Run(draft, func(t *testing.T) {
			root := t.TempDir()
			path := filepath.Join(root, "a")
			if err := os.WriteFile(path, []byte("old"), 0o600); err != nil {
				t.Fatal(err)
			}
			sources, err := ReadSources(root, []string{"a"})
			if err != nil {
				t.Fatal(err)
			}
			if _, err = ApplyDraft(context.Background(), root, sources, draft, func(context.Context, string) error { return nil }); err == nil {
				t.Fatal("invalid patch accepted")
			}
			got, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			if string(got) != "old" {
				t.Fatalf("mutated: %s", got)
			}
		})
	}
}

func TestApplyDraftRequiresAuthorization(t *testing.T) {
	root := t.TempDir()
	sources, err := ReadSources(root, []string{"new.go"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ApplyDraft(context.Background(), root, sources, `{"edits":[{"path":"new.go","old":"","new":"package main"}]}`, nil); err == nil {
		t.Fatal("unauthorized write succeeded")
	}
	if _, err := os.Stat(filepath.Join(root, "new.go")); !os.IsNotExist(err) {
		t.Fatal("file created without authorization")
	}
}

func TestReadSourcesRejectsSymlinksAndPolicy(t *testing.T) {
	root := t.TempDir()
	for _, path := range []string{"../outside", ".conduit/settings.json", ".git/config", "/tmp/outside"} {
		if _, err := ReadSources(root, []string{path}); err == nil {
			t.Fatalf("accepted %q", path)
		}
	}
	outside := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(root, "linked")); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	if _, err := ReadSources(root, []string{"linked/new.go"}); err == nil {
		t.Fatal("accepted symlink")
	}
}
