package agenttool

// Phase 3c tests: agenttool routes to SpawnTeammate when team.IsActive().
//
// team.IsActive() is a global atomic. Each test saves/restores the flag via
// t.Cleanup — tests within a package run sequentially (no t.Parallel) so
// there is no race between tests.

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/icehunter/conduit/internal/team"
)

// setTeamActive enables/disables the global team flag for the duration of
// the test, restoring the previous value via t.Cleanup.
func setTeamActive(t *testing.T, on bool) {
	t.Helper()
	prev := team.IsActive()
	team.SetActive(on)
	t.Cleanup(func() { team.SetActive(prev) })
}

func teamTestInput(v any) json.RawMessage {
	b, _ := json.Marshal(v)
	return b
}

// ─── Phase 3c tests ────────────────────────────────────────────────────────────

// TestAgentTool_Teams_Inactive: when teams off, spawnTeammate is ignored; runAgent runs.
func TestAgentTool_Teams_Inactive(t *testing.T) {
	setTeamActive(t, false)

	agentCalled := false
	spawnCalled := false

	at := New(
		func(_ context.Context, _ string) (string, error) { agentCalled = true; return "done", nil },
		nil, nil,
	).WithSpawnTeammate(func(_ context.Context, _, _ string) (string, error) {
		spawnCalled = true
		return "id", nil
	})

	res, err := at.Execute(context.Background(), teamTestInput(Input{Prompt: "hello"}))
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if res.IsError {
		t.Fatalf("unexpected ErrorResult: %v", res.Content)
	}
	if !agentCalled {
		t.Error("runAgent should be called when teams inactive")
	}
	if spawnCalled {
		t.Error("spawnTeammate must not be called when teams inactive")
	}
}

// TestAgentTool_Teams_Active_Spawns: when teams on + spawnTeammate set, returns
// "launched" result without calling runAgent.
func TestAgentTool_Teams_Active_Spawns(t *testing.T) {
	setTeamActive(t, true)

	agentCalled := false
	var capturedName, capturedPrompt string

	at := New(
		func(_ context.Context, _ string) (string, error) { agentCalled = true; return "done", nil },
		nil, nil,
	).WithSpawnTeammate(func(_ context.Context, name, prompt string) (string, error) {
		capturedName = name
		capturedPrompt = prompt
		return "child-id-1", nil
	})

	res, err := at.Execute(context.Background(), teamTestInput(Input{Prompt: "build a widget"}))
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if res.IsError {
		t.Fatalf("unexpected ErrorResult: %v", res.Content)
	}
	if agentCalled {
		t.Error("runAgent must not be called when teams active and spawnTeammate is set")
	}
	if capturedPrompt != "build a widget" {
		t.Errorf("capturedPrompt = %q; want %q", capturedPrompt, "build a widget")
	}
	if capturedName == "" {
		t.Error("spawnTeammate received empty name; expected auto-generated name")
	}
	if !strings.Contains(res.Content[0].Text, "launched") {
		t.Errorf("result text %q should contain 'launched'", res.Content[0].Text)
	}
}

// TestAgentTool_Teams_NameFromInput: explicit name in input is forwarded to spawnTeammate.
func TestAgentTool_Teams_NameFromInput(t *testing.T) {
	setTeamActive(t, true)

	var capturedName string
	at := New(nil, nil, nil).WithSpawnTeammate(func(_ context.Context, name, _ string) (string, error) {
		capturedName = name
		return "id", nil
	})

	res, err := at.Execute(context.Background(), teamTestInput(Input{Prompt: "hi", Name: "my-worker"}))
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if res.IsError {
		t.Fatalf("unexpected ErrorResult: %v", res.Content)
	}
	if capturedName != "my-worker" {
		t.Errorf("capturedName = %q; want %q", capturedName, "my-worker")
	}
}

// TestAgentTool_Teams_AutoGeneratesName: two calls without a name get distinct auto-names.
func TestAgentTool_Teams_AutoGeneratesName(t *testing.T) {
	setTeamActive(t, true)

	var names []string
	at := New(nil, nil, nil).WithSpawnTeammate(func(_ context.Context, name, _ string) (string, error) {
		names = append(names, name)
		return "id", nil
	})

	for range 2 {
		res, err := at.Execute(context.Background(), teamTestInput(Input{Prompt: "work"}))
		if err != nil {
			t.Fatalf("Execute: %v", err)
		}
		if res.IsError {
			t.Fatalf("unexpected ErrorResult: %v", res.Content)
		}
	}

	if len(names) != 2 {
		t.Fatalf("expected 2 names; got %v", names)
	}
	if names[0] == names[1] {
		t.Errorf("auto-generated names must be unique; both = %q", names[0])
	}
	for _, n := range names {
		if !strings.HasPrefix(n, "teammate-") {
			t.Errorf("auto-name %q should start with 'teammate-'", n)
		}
	}
}

// TestAgentTool_Teams_SpawnError: when spawnTeammate returns an error, Execute returns ErrorResult.
func TestAgentTool_Teams_SpawnError(t *testing.T) {
	setTeamActive(t, true)

	at := New(nil, nil, nil).WithSpawnTeammate(func(_ context.Context, _, _ string) (string, error) {
		return "", context.DeadlineExceeded
	})

	res, err := at.Execute(context.Background(), teamTestInput(Input{Prompt: "oops"}))
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if !res.IsError {
		t.Error("expected ErrorResult when spawnTeammate returns error")
	}
}

// TestAgentTool_Teams_NilSpawnFallsThrough: when teams active but spawnTeammate
// is nil, Execute falls through to runAgent (graceful degradation).
func TestAgentTool_Teams_NilSpawnFallsThrough(t *testing.T) {
	setTeamActive(t, true)

	agentCalled := false
	at := New(
		func(_ context.Context, _ string) (string, error) { agentCalled = true; return "ok", nil },
		nil, nil,
	)
	// No WithSpawnTeammate call — spawnTeammate is nil.

	res, err := at.Execute(context.Background(), teamTestInput(Input{Prompt: "fallback"}))
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if res.IsError {
		t.Fatalf("unexpected ErrorResult: %v", res.Content)
	}
	if !agentCalled {
		t.Error("runAgent should be called as fallback when spawnTeammate is nil")
	}
}

// TestAgentTool_Teams_NameInSchema: JSON schema includes the name property.
func TestAgentTool_Teams_NameInSchema(t *testing.T) {
	at := New(nil, nil, nil)
	schema := string(at.InputSchema())
	if !strings.Contains(schema, `"name"`) {
		t.Error("InputSchema() should include 'name' property")
	}
}

// TestAgentTool_Teams_CounterIncrementsPerCall: two un-named calls to the same
// tool produce distinct auto-names (teammate-N, teammate-N+1).
func TestAgentTool_Teams_CounterIncrementsPerCall(t *testing.T) {
	setTeamActive(t, true)

	var got []string
	at := New(nil, nil, nil).WithSpawnTeammate(func(_ context.Context, name, _ string) (string, error) {
		got = append(got, name)
		return "id", nil
	})

	for range 3 {
		res, err := at.Execute(context.Background(), teamTestInput(Input{Prompt: "work"}))
		if err != nil {
			t.Fatalf("Execute: %v", err)
		}
		if res.IsError {
			t.Fatalf("unexpected error: %v", res.Content)
		}
	}

	if len(got) != 3 {
		t.Fatalf("expected 3 names; got %v", got)
	}
	// All must be distinct.
	seen := map[string]bool{}
	for _, n := range got {
		if seen[n] {
			t.Errorf("duplicate auto-name %q in %v", n, got)
		}
		seen[n] = true
	}
}
