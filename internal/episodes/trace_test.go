package episodes

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestTraceProvenanceDoesNotChangeConditioning(t *testing.T) {
	r := Request{Messages: []Message{{Role: "user", Content: "same"}}, ModelID: "qwen", ContextComplete: true, Parameters: map[string]any{"temperature": 0.2}}
	if ContextDigest(r) != digest(r) {
		t.Fatal("legacy conditioning changed")
	}
	r.Trace = &RequestTrace{Version: 1, RequestID: "first", EndpointID: "first-endpoint", FinishReason: "stop", Usage: map[string]any{"prompt_tokens": 123}}
	want := ContextDigest(r)
	r.Trace.RequestID = "second"
	r.Trace.Usage["prompt_tokens"] = 456
	if ContextDigest(r) != want {
		t.Fatal("request id/usage changed conditioning")
	}
	r.Trace.EndpointID = "other-endpoint"
	if ContextDigest(r) == want {
		t.Fatal("upstream endpoint change missing from conditioning")
	}
	r.Trace.EndpointID = "first-endpoint"
	r.Parameters["temperature"] = 0.4
	if ContextDigest(r) == want {
		t.Fatal("generation parameters missing from conditioning")
	}
}

func TestTraceProvenancePreservesCrossEpisodePreferences(t *testing.T) {
	fixture := exportFixture()
	chosen := []Event{fixture[0], fixture[1], fixture[3], fixture[4], fixture[6], fixture[7]}
	rejected := []Event{fixture[0], fixture[1], fixture[2], fixture[5], fixture[7]}
	for i := range rejected {
		rejected[i].EpisodeID = "rejected-episode"
	}
	for i, part := range [][]Event{chosen, rejected} {
		request := part[1].Payload.(Request)
		request.Trace = &RequestTrace{Version: 1, RequestID: []string{"first", "second"}[i], EndpointID: "same-upstream", FinishReason: "stop", Usage: map[string]any{"completion_tokens": 10 + i}}
		part[1].Payload = request
		candidate := part[2].Payload.(Candidate)
		candidate.ContextDigest = ContextDigest(request)
		part[2].Payload = candidate
	}
	dataset, err := Export(append(chosen, rejected...), ExportOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if len(dataset.DPO) != 1 || len(dataset.SFT) != 1 {
		t.Fatalf("per-call provenance blocked comparable samples: %+v", dataset.Manifest)
	}
}

func TestPrivacyPathTransformationMarksEpisodeRedacted(t *testing.T) {
	repo := t.TempDir()
	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{filepath.Join(repo, "source.go"), filepath.Join(home, "private", "source.go")} {
		r, err := New(Config{Enabled: true, IncludeContent: true, Dir: t.TempDir(), RepositoryRoot: repo, SessionID: "paths"})
		if err != nil {
			t.Fatal(err)
		}
		if err = r.Record(Event{Kind: KindRequest, EpisodeID: "e", Payload: Request{Messages: []Message{{Role: "user", Content: path}}, ContextComplete: true}}); err != nil {
			t.Fatal(err)
		}
		if err = r.Close(); err != nil {
			t.Fatal(err)
		}
		events, err := ReadFile(r.Path())
		if err != nil {
			t.Fatal(err)
		}
		if len(events) != 1 || !events[0].Redacted {
			t.Fatalf("changed conditioning not flagged: %+v", events)
		}
		b, err := os.ReadFile(r.Path())
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(b), path) {
			t.Fatal("absolute identity path persisted")
		}
	}
}
