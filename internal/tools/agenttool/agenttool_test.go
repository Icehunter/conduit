package agenttool

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/icehunter/conduit/internal/tool"
)

// fakeRegistry implements Registry for testing.
type fakeRegistry struct {
	agents []AgentDef
}

func (r *fakeRegistry) FindAgent(name string) *AgentDef {
	for i := range r.agents {
		if strings.EqualFold(r.agents[i].QualifiedName, name) || strings.EqualFold(r.agents[i].Name, name) {
			return &r.agents[i]
		}
	}
	return nil
}

func (r *fakeRegistry) ListAgents() []AgentDef { return r.agents }

func runAgent(ctx context.Context, prompt string) (string, error) {
	return "generic: " + prompt, nil
}

func TestAgentTool_BasicPrompt(t *testing.T) {
	at := New(runAgent, nil, nil)
	raw, _ := json.Marshal(Input{Prompt: "hello"})
	res, err := at.Execute(context.Background(), raw)
	if err != nil {
		t.Fatal(err)
	}
	if res.IsError {
		t.Fatalf("unexpected error result: %v", res.Content)
	}
	if res.Content[0].Text != "generic: hello" {
		t.Errorf("got %q", res.Content[0].Text)
	}
}

func TestAgentTool_EmptyPromptErrors(t *testing.T) {
	at := New(runAgent, nil, nil)
	raw, _ := json.Marshal(Input{})
	res, _ := at.Execute(context.Background(), raw)
	if !res.IsError {
		t.Error("empty prompt should return error result")
	}
}

func TestAgentTool_SubagentTypeDispatch(t *testing.T) {
	var capturedSystemPrompt, capturedModel string
	var capturedTools []string

	reg := &fakeRegistry{agents: []AgentDef{{
		Name:          "reviewer",
		QualifiedName: "toolkit:reviewer",
		Description:   "Reviews code",
		SystemPrompt:  "You are a reviewer.",
		Model:         "opus",
		Tools:         []string{"Read", "Grep"},
	}}}

	runTyped := func(ctx context.Context, prompt, sysPrompt, model string, tools []string) (string, error) {
		capturedSystemPrompt = sysPrompt
		capturedModel = model
		capturedTools = tools
		return "reviewed: " + prompt, nil
	}

	at := New(runAgent, reg, runTyped)
	raw, _ := json.Marshal(Input{Prompt: "check code", SubagentType: "toolkit:reviewer"})
	res, err := at.Execute(context.Background(), raw)
	if err != nil {
		t.Fatal(err)
	}
	if res.IsError {
		t.Fatalf("unexpected error: %v", res.Content)
	}
	if res.Content[0].Text != "reviewed: check code" {
		t.Errorf("result = %q", res.Content[0].Text)
	}
	if capturedSystemPrompt != "You are a reviewer." {
		t.Errorf("systemPrompt = %q", capturedSystemPrompt)
	}
	if capturedModel != "opus" {
		t.Errorf("model = %q", capturedModel)
	}
	// Tools are resolved through resolveToolNames before dispatch.
	if len(capturedTools) != 2 {
		t.Errorf("tools = %v; want 2 entries", capturedTools)
	}
}

func TestAgentTool_SubagentTypeUnknownErrors(t *testing.T) {
	reg := &fakeRegistry{agents: []AgentDef{{Name: "a", QualifiedName: "p:a"}}}
	at := New(runAgent, reg, nil)
	raw, _ := json.Marshal(Input{Prompt: "x", SubagentType: "p:missing"})
	res, _ := at.Execute(context.Background(), raw)
	if !res.IsError {
		t.Error("unknown subagent_type should return error result")
	}
	if !strings.Contains(res.Content[0].Text, "unknown subagent_type") {
		t.Errorf("error text = %q", res.Content[0].Text)
	}
}

func TestAgentTool_SubagentTypeFallsBackToRunAgent(t *testing.T) {
	// When runTyped is nil, falls back to runAgent even for known subagent_type.
	reg := &fakeRegistry{agents: []AgentDef{{Name: "a", QualifiedName: "p:a", SystemPrompt: "sp"}}}
	at := New(runAgent, reg, nil)
	raw, _ := json.Marshal(Input{Prompt: "hi", SubagentType: "p:a"})
	res, _ := at.Execute(context.Background(), raw)
	if res.IsError {
		t.Fatalf("unexpected error: %v", res.Content)
	}
	// Falls back to generic runAgent.
	if !strings.HasPrefix(res.Content[0].Text, "generic:") {
		t.Errorf("expected generic fallback, got %q", res.Content[0].Text)
	}
}

func TestAgentTool_DescriptionListsAgents(t *testing.T) {
	reg := &fakeRegistry{agents: []AgentDef{
		{QualifiedName: "p:a", Description: "Agent A", Tools: []string{"Read"}},
		{QualifiedName: "p:b", Description: "Agent B"},
	}}
	at := New(runAgent, reg, nil)
	desc := at.Description()
	if !strings.Contains(desc, "p:a") {
		t.Error("description should list p:a")
	}
	if !strings.Contains(desc, "p:b") {
		t.Error("description should list p:b")
	}
	if !strings.Contains(desc, "Read") {
		t.Error("description should show tool allowlist")
	}
}

func TestResolveToolNames(t *testing.T) {
	tests := []struct {
		in   string
		want string
	}{
		// These match conduit's registered names exactly — no alias needed.
		{"Read", "Read"},
		{"read", "read"},
		{"Bash", "Bash"},
		{"Edit", "Edit"},
		{"Write", "Write"},
		{"Grep", "Grep"},
		{"Glob", "Glob"},
		{"Task", "Task"},
		{"WebFetch", "WebFetch"},
		{"WebSearch", "WebSearch"},
		{"TodoWrite", "TodoWrite"},
		// CC names that differ from conduit's registered names.
		{"NotebookEdit", "NotebookEdit"},
		{"LS", "Glob"}, // CC LS → nearest conduit equivalent
		{"ls", "Glob"},
		{"KillShell", "Bash"},
		{"BashOutput", "Bash"},
		{"unknown-tool", "unknown-tool"}, // pass-through
	}
	for _, tt := range tests {
		got := resolveToolNames([]string{tt.in})
		if len(got) != 1 || got[0] != tt.want {
			t.Errorf("resolveToolNames(%q) = %v; want [%s]", tt.in, got, tt.want)
		}
	}
}

func TestAgentTool_Name(t *testing.T) {
	at := New(runAgent, nil, nil)
	if at.Name() != "Task" {
		t.Errorf("Name = %q; want Task", at.Name())
	}
}

func TestAgentTool_InputSchema(t *testing.T) {
	at := New(runAgent, nil, nil)
	var schema map[string]any
	if err := json.Unmarshal(at.InputSchema(), &schema); err != nil {
		t.Fatalf("InputSchema is not valid JSON: %v", err)
	}
	props, ok := schema["properties"].(map[string]any)
	if !ok {
		t.Fatal("InputSchema missing properties")
	}
	if _, ok := props["subagent_type"]; !ok {
		t.Error("InputSchema missing subagent_type property")
	}
}

func TestAgentTool_IsReadOnly(t *testing.T) {
	at := New(runAgent, nil, nil)
	if at.IsReadOnly(json.RawMessage(`{}`)) {
		t.Error("AgentTool should not be read-only")
	}
}

// Ensure Tool satisfies tool.Tool at compile time.
var _ tool.Tool = (*Tool)(nil)
