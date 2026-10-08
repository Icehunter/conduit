package episodes

import (
	"encoding/json"
	"reflect"
	"testing"
)

func exportFixture() []Event {
	req := Request{Messages: []Message{{Role: "user", Content: "Fix the function"}}, ContextComplete: true}
	ctx := ContextDigest(req)
	return []Event{
		{Kind: KindStart, EpisodeID: "e", Origin: OriginInteractive, Payload: Start{RepoID: "repo", TaskID: "task", SplitGroup: "task-family"}},
		{Kind: KindRequest, EpisodeID: "e", RequestID: "r", Payload: req},
		{Kind: KindCandidate, EpisodeID: "e", Payload: Candidate{ID: "bad", RequestID: "r", ContextDigest: ctx, BaseDigest: "base", WorkspaceDigest: "bad-tree", Completion: "bad code", Patch: "-a\n+b", Role: "implement", ModelID: "local"}},
		{Kind: KindCandidate, EpisodeID: "e", Payload: Candidate{ID: "good", RequestID: "r", ContextDigest: ctx, BaseDigest: "base", WorkspaceDigest: "good-tree", Completion: "good code", Patch: "-a\n+c", Role: "implement", ModelID: "local"}},
		{Kind: KindVerification, EpisodeID: "e", Payload: Verification{CandidateID: "good", WorkspaceDigest: "good-tree", PolicyDigest: "policy", Passed: true}},
		{Kind: KindDecision, EpisodeID: "e", Payload: Decision{CandidateID: "bad", Action: "rejected", Source: SourceExplicitHuman}},
		{Kind: KindDecision, EpisodeID: "e", Payload: Decision{CandidateID: "good", Action: "accepted", Source: SourceExplicitHuman}},
		{Kind: KindEnd, EpisodeID: "e", Payload: End{Complete: true}},
	}
}

func TestExportEligibility(t *testing.T) {
	tests := []struct {
		name     string
		edit     func([]Event)
		sft, dpo int
	}{
		{"eligible", func([]Event) {}, 1, 1},
		{"evaluation", func(e []Event) { e[0].Origin = OriginEvaluation }, 0, 0},
		{"redacted", func(e []Event) { e[2].Redacted = true }, 0, 0},
		{"incomplete", func(e []Event) { e[1].Incomplete = true }, 0, 0},
		{"router context absent", func(e []Event) { r := e[1].Payload.(Request); r.ContextComplete = false; e[1].Payload = r }, 0, 0},
		{"end missing", func(e []Event) { e[7].Payload = End{Complete: false} }, 0, 0},
		{"stale test", func(e []Event) { v := e[4].Payload.(Verification); v.WorkspaceDigest = "other"; e[4].Payload = v }, 0, 0},
		{"timeout approval", func(e []Event) { d := e[6].Payload.(Decision); d.Source = "timeout"; e[6].Payload = d }, 0, 0},
		{"different base", func(e []Event) { c := e[2].Payload.(Candidate); c.BaseDigest = "other"; e[2].Payload = c }, 1, 0},
		{"different conditioning", func(e []Event) { c := e[2].Payload.(Candidate); c.ContextDigest = "other"; e[2].Payload = c }, 1, 0},
		{"existing redaction marker", func(e []Event) { c := e[3].Payload.(Candidate); c.Completion = "[REDACTED:token]"; e[3].Payload = c }, 0, 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			events := exportFixture()
			tt.edit(events)
			got, err := Export(events, ExportOptions{})
			if err != nil {
				t.Fatal(err)
			}
			if len(got.SFT) != tt.sft || len(got.DPO) != tt.dpo {
				t.Fatalf("SFT=%d DPO=%d", len(got.SFT), len(got.DPO))
			}
		})
	}
}

func TestExportDeterministicAndHeldout(t *testing.T) {
	events := exportFixture()
	first, err := Export(events, ExportOptions{})
	if err != nil {
		t.Fatal(err)
	}
	second, err := Export(events, ExportOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(first, second) {
		t.Fatal("nondeterministic")
	}
	got, err := Export(events, ExportOptions{HeldOutGroups: []string{"task-family"}})
	if err != nil {
		t.Fatal(err)
	}
	if len(got.SFT) != 0 || len(got.DPO) != 0 {
		t.Fatal("held-out leakage")
	}
	b, _ := json.Marshal(events)
	var decoded []Event
	if err := json.Unmarshal(b, &decoded); err != nil {
		t.Fatal(err)
	}
	third, err := Export(decoded, ExportOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(first, third) {
		t.Fatal("in-memory and journal payload differ")
	}
}

func TestExportRequiresTerminalLast(t *testing.T) {
	events := exportFixture()
	events = append(events, Event{Kind: KindTool, EpisodeID: "e", Payload: map[string]any{"mutation": true}})
	dataset, err := Export(events, ExportOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if len(dataset.SFT) != 0 {
		t.Fatal("post-completion mutation exported")
	}
}

func TestOfflineDecisionAndUnknownCandidate(t *testing.T) {
	for _, known := range []bool{true, false} {
		events := exportFixture()
		id := "good"
		if !known {
			id = "unknown"
		}
		events = append(events, Event{Kind: KindDecision, EpisodeID: "e", Payload: Decision{CandidateID: id, Action: "accepted", Source: SourceExplicitHuman}})
		dataset, err := Export(events, ExportOptions{})
		if err != nil {
			t.Fatal(err)
		}
		if (len(dataset.SFT) > 0) != known {
			t.Fatalf("known=%v SFT=%d", known, len(dataset.SFT))
		}
	}
}

func TestOmittedEpisodeDoesNotBlockIndependentExport(t *testing.T) {
	events := exportFixture()
	for i := range events {
		events[i].Redacted = true
		events[i].Incomplete = true
		events[i].Payload = map[string]any{"omitted": "content policy"}
	}
	valid := exportFixture()
	for i := range valid {
		valid[i].EpisodeID = "other"
	}
	events = append(events, valid...)
	dataset, err := Export(events, ExportOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if len(dataset.SFT) != 1 || dataset.SFT[0].Meta.EpisodeID != "other" {
		t.Fatalf("SFT=%+v", dataset.SFT)
	}
	events[0].Origin = OriginEvaluation
	dataset, err = Export(events, ExportOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if len(dataset.SFT) != 0 {
		t.Fatal("unknown evaluation group failed open")
	}
}

func TestCrossEpisodePreferences(t *testing.T) {
	for _, scenario := range []string{"same", "repo", "base", "context"} {
		t.Run(scenario, func(t *testing.T) {
			fixture := exportFixture()
			chosen := []Event{fixture[0], fixture[1], fixture[3], fixture[4], fixture[6], fixture[7]}
			rejected := []Event{fixture[0], fixture[1], fixture[2], fixture[5], fixture[7]}
			for i := range rejected {
				rejected[i].EpisodeID = "rejected-episode"
			}
			switch scenario {
			case "repo":
				start := rejected[0].Payload.(Start)
				start.RepoID = "other"
				rejected[0].Payload = start
			case "base":
				c := rejected[2].Payload.(Candidate)
				c.BaseDigest = "other"
				rejected[2].Payload = c
			case "context":
				r := rejected[1].Payload.(Request)
				r.Messages = []Message{{Role: "user", Content: "Different request"}}
				rejected[1].Payload = r
				c := rejected[2].Payload.(Candidate)
				c.ContextDigest = ContextDigest(r)
				rejected[2].Payload = c
			}
			dataset, err := Export(append(chosen, rejected...), ExportOptions{})
			if err != nil {
				t.Fatal(err)
			}
			if len(dataset.SFT) != 1 {
				t.Fatalf("SFT=%d", len(dataset.SFT))
			}
			if scenario == "same" {
				if len(dataset.DPO) != 1 || dataset.DPO[0].RejectedEpisodeID != "rejected-episode" {
					t.Fatalf("DPO=%+v", dataset.DPO)
				}
			} else if len(dataset.DPO) != 0 {
				t.Fatalf("mismatched %s pair accepted", scenario)
			}
		})
	}
}

func TestExportMultipleJournals(t *testing.T) {
	fixture := exportFixture()
	parts := [][]Event{{fixture[0], fixture[1], fixture[3], fixture[4], fixture[6], fixture[7]}, {fixture[0], fixture[1], fixture[2], fixture[5], fixture[7]}}
	paths := make([]string, 0, len(parts))
	for i, part := range parts {
		sessionID := []string{"chosen-session", "rejected-session"}[i]
		r, err := New(Config{Enabled: true, IncludeContent: true, Dir: t.TempDir(), RepositoryRoot: t.TempDir(), SessionID: sessionID})
		if err != nil {
			t.Fatal(err)
		}
		for _, event := range part {
			event.EpisodeID = sessionID
			if err := r.Record(event); err != nil {
				t.Fatal(err)
			}
		}
		if err := r.Close(); err != nil {
			t.Fatal(err)
		}
		paths = append(paths, r.Path())
	}
	dataset, err := ExportFiles(paths, ExportOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if len(dataset.SFT) != 1 || len(dataset.DPO) != 1 {
		t.Fatalf("counts SFT=%d DPO=%d", len(dataset.SFT), len(dataset.DPO))
	}
	reversed, err := ExportFiles([]string{paths[1], paths[0]}, ExportOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(dataset, reversed) {
		t.Fatal("input path order changes dataset")
	}
	if _, err := ExportFiles([]string{paths[0], paths[0]}, ExportOptions{}); err == nil {
		t.Fatal("duplicate journal accepted")
	}
}
