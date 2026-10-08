package episodes

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestDisabledRecorder(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "absent")
	r, err := New(Config{Dir: dir})
	if err != nil {
		t.Fatal(err)
	}
	if err := r.Record(Event{Kind: "anything", Payload: make(chan int)}); err != nil {
		t.Fatal(err)
	}
	if err := r.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Fatalf("disabled created directory: %v", err)
	}
}

func TestRecorderSanitizesEveryChannel(t *testing.T) {
	repo := t.TempDir()
	if err := os.WriteFile(filepath.Join(repo, "allowed.go"), []byte("package a"), 0600); err != nil {
		t.Fatal(err)
	}
	for _, content := range []bool{false, true} {
		t.Run(map[bool]string{false: "metadata", true: "content"}[content], func(t *testing.T) {
			r, err := New(Config{Enabled: true, IncludeContent: content, Dir: t.TempDir(), RepositoryRoot: repo, SessionID: "s", AllowedPaths: []string{"**"}})
			if err != nil {
				t.Fatal(err)
			}
			secret := "ghp_" + strings.Repeat("a", 36)
			err = r.Record(Event{Kind: KindRequest, EpisodeID: "e", Sources: []string{"allowed.go"}, Payload: map[string]any{"messages": []any{map[string]any{"content": secret + " password=hunter2 <think>hidden reasoning</think>"}}, "api_key": "shortsecret", "accessToken": "tiny", "path": filepath.Join(repo, "allowed.go")}})
			if err != nil {
				t.Fatal(err)
			}
			if err := r.Close(); err != nil {
				t.Fatal(err)
			}
			data, err := os.ReadFile(r.Path())
			if err != nil {
				t.Fatal(err)
			}
			for _, bad := range []string{secret, "shortsecret", "hunter2", "hidden reasoning", "tiny", repo} {
				if strings.Contains(string(data), bad) {
					t.Errorf("leaked %q", bad)
				}
			}
			var e Event
			if err := json.Unmarshal(data, &e); err != nil {
				t.Fatal(err)
			}
			if !e.Redacted {
				t.Error("missing redacted marker")
			}
		})
	}
}

func TestRecorderOrderAndRecovery(t *testing.T) {
	cfg := Config{Enabled: true, IncludeContent: true, Dir: t.TempDir(), RepositoryRoot: t.TempDir(), SessionID: "same"}
	r, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	for range 20 {
		wg.Go(func() {
			if err := r.Record(Event{Kind: KindStart, EpisodeID: "e", Payload: map[string]any{"count": 1}}); err != nil {
				t.Error(err)
			}
		})
	}
	wg.Wait()
	if err := r.Close(); err != nil {
		t.Fatal(err)
	}
	events, err := ReadFile(r.Path())
	if err != nil {
		t.Fatal(err)
	}
	for i, e := range events {
		if e.Seq != uint64(i+1) {
			t.Errorf("seq=%d", e.Seq)
		}
	}
	// A partial write is never repaired silently or promoted into training data.
	f, err := os.OpenFile(r.Path(), os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.WriteString(`{"kind":`); err != nil {
		t.Fatal(err)
	}
	if err = f.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadFile(r.Path()); err == nil {
		t.Fatal("accepted truncated ledger")
	}
	if _, err := New(cfg); err == nil {
		t.Fatal("resumed truncated ledger")
	}
}

func TestRecorderSourcePolicy(t *testing.T) {
	repo := t.TempDir()
	outside := t.TempDir()
	if err := os.WriteFile(filepath.Join(outside, "secret"), []byte("secret"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(repo, "link")); err != nil {
		t.Skip(err)
	}
	r, err := New(Config{Enabled: true, IncludeContent: true, Dir: t.TempDir(), RepositoryRoot: repo, SessionID: "s", AllowedPaths: []string{"**"}, ExcludedPaths: []string{"private/**"}})
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := r.Close(); err != nil {
			t.Error(err)
		}
	}()
	for _, path := range []string{"../secret", "link/secret", "private/a", ".env"} {
		if err := r.Record(Event{Kind: KindRequest, EpisodeID: "e", Sources: []string{path}, Payload: map[string]any{"content": "sensitive bytes"}}); err != nil {
			t.Fatal(err)
		}
	}
	events, err := ReadFile(r.Path())
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range events {
		if !e.Incomplete {
			t.Error("excluded source marked complete")
		}
		b, _ := json.Marshal(e.Payload)
		if strings.Contains(string(b), "sensitive bytes") {
			t.Error("excluded source leaked")
		}
	}
}

func TestRecorderExclusiveResumeAndRetention(t *testing.T) {
	cfg := Config{Enabled: true, IncludeContent: true, Dir: t.TempDir(), RepositoryRoot: t.TempDir(), SessionID: "s"}
	r, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := New(cfg); err == nil {
		t.Fatal("concurrent writer acquired same ledger")
	}
	if err := r.Record(Event{Kind: KindStart, EpisodeID: "e", Payload: Start{RepoID: "r", TaskID: "t", SplitGroup: "g"}}); err != nil {
		t.Fatal(err)
	}
	old := time.Now().AddDate(0, 0, -10)
	if err := os.Chtimes(r.Path(), old, old); err != nil {
		t.Fatal(err)
	}
	if err := Prune(cfg.Dir, 1, time.Now()); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(r.Path()); err != nil {
		t.Fatal("retention deleted active ledger")
	}
	if err := r.Close(); err != nil {
		t.Fatal(err)
	}
	r, err = New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if err := r.Record(Event{Kind: KindEnd, EpisodeID: "e", Payload: End{Complete: true}}); err != nil {
		t.Fatal(err)
	}
	if err := r.Close(); err != nil {
		t.Fatal(err)
	}
	events, err := ReadFile(r.Path())
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 2 || events[1].Seq != 2 {
		t.Fatal("resume sequence not preserved")
	}
	if err := os.Chtimes(r.Path(), old, old); err != nil {
		t.Fatal(err)
	}
	if err := Prune(cfg.Dir, 1, time.Now()); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(r.Path()); !os.IsNotExist(err) {
		t.Fatal("expired ledger not removed")
	}
}
