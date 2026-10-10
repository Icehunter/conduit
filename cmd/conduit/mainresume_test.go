package main

import (
	"errors"
	"testing"

	"github.com/icehunter/conduit/internal/api"
	"github.com/icehunter/conduit/internal/session"
)

func TestLatestSessionWithHistory(t *testing.T) {
	msgs := []api.Message{{Role: "user"}}
	files := map[string][]api.Message{"full.jsonl": msgs, "older.jsonl": msgs}
	load := func(path string) ([]api.Message, error) {
		if path == "broken.jsonl" {
			return nil, errors.New("bad json")
		}
		return files[path], nil
	}
	meta := func(names ...string) []session.SessionMeta {
		out := make([]session.SessionMeta, len(names))
		for i, n := range names {
			out[i] = session.SessionMeta{ID: n, FilePath: n}
		}
		return out
	}
	tests := []struct {
		name     string
		sessions []session.SessionMeta
		wantID   string
		wantOK   bool
	}{
		{"newest has history", meta("full.jsonl", "older.jsonl"), "full.jsonl", true},
		{"skips empty newest", meta("empty.jsonl", "full.jsonl"), "full.jsonl", true},
		{"skips unreadable", meta("broken.jsonl", "older.jsonl"), "older.jsonl", true},
		{"all empty", meta("empty.jsonl"), "", false},
		{"no sessions", nil, "", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, history, ok := latestSessionWithHistory(tt.sessions, load, func(string, ...any) {})
			if ok != tt.wantOK || got.ID != tt.wantID {
				t.Fatalf("got (%q, ok=%v), want (%q, ok=%v)", got.ID, ok, tt.wantID, tt.wantOK)
			}
			if ok && len(history) == 0 {
				t.Error("returned a session without its history")
			}
		})
	}
}
