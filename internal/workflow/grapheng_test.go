package workflow

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// schemaStub builds a minimal value satisfying schema: every required
// property, one element per array, first enum value, type-based defaults.
func schemaStub(schema json.RawMessage) any {
	var s map[string]any
	if err := json.Unmarshal(schema, &s); err != nil {
		return map[string]any{}
	}
	return stubFor(s)
}

func stubFor(s map[string]any) any {
	if enum, ok := s["enum"].([]any); ok && len(enum) > 0 {
		return enum[0]
	}
	switch s["type"] {
	case "object":
		out := map[string]any{}
		props, _ := s["properties"].(map[string]any)
		req, _ := s["required"].([]any)
		for _, r := range req {
			name, _ := r.(string)
			if p, ok := props[name].(map[string]any); ok {
				out[name] = stubFor(p)
			} else {
				out[name] = "x"
			}
		}
		return out
	case "array":
		if items, ok := s["items"].(map[string]any); ok {
			return []any{stubFor(items)}
		}
		return []any{}
	case "number", "integer":
		return 1
	case "boolean":
		return true
	default:
		return "x"
	}
}

type stubHost struct{}

func (stubHost) RunAgent(_ context.Context, req AgentRequest) AgentResult {
	if req.Schema != nil {
		return AgentResult{Value: schemaStub(req.Schema), OutputTokens: 1}
	}
	return AgentResult{Value: "text", OutputTokens: 1}
}

func (stubHost) ResolveWorkflow(context.Context, WorkflowRef) (string, error) {
	return "", errors.New("no child workflows in this test")
}

func graphEngScript(t *testing.T, name string) *Script {
	t.Helper()
	path := filepath.Join("..", "..", "..", "graph-eng", "workflows", name)
	src, err := os.ReadFile(path)
	if err != nil {
		t.Skipf("graph-eng not available: %v", err)
	}
	s, err := Load(string(src))
	if err != nil {
		t.Fatal(err)
	}
	return s
}

// TestGraphEng_AllScriptsComplete runs each real graph-eng workflow to
// completion against a schema-satisfying stub host. This catches API-surface
// gaps (a global the scripts use that the runtime does not provide) and
// promise-plumbing bugs that unit tests with hand-written scripts miss.
func TestGraphEng_AllScriptsComplete(t *testing.T) {
	for _, name := range []string{"graph-audit.js", "graph-research.js", "graph-sweep.js", "graph-hunt.js", "graph-panel.js"} {
		t.Run(name, func(t *testing.T) {
			sc := graphEngScript(t, name)
			r := &Run{ID: "e2e-" + name}
			out, err := Execute(context.Background(), r, sc, Options{
				Host: stubHost{},
				Args: json.RawMessage(`{"target":"internal/tui","question":"where is the null bug","decision":"pick a queue","what":"races","cap":2,"angles":2,"maxAgents":12}`),
			})
			if err != nil {
				t.Fatalf("Execute: %v\nlogs: %+v", err, r.Snapshot().Logs)
			}
			var v map[string]any
			if err := json.Unmarshal(out, &v); err != nil {
				t.Fatalf("result is not a JSON object: %s", out)
			}
			s := r.Snapshot()
			if s.Status != RunDone || len(s.Nodes) == 0 {
				t.Errorf("status=%s nodes=%d", s.Status, len(s.Nodes))
			}
			for _, n := range s.Nodes {
				if n.Phase == "" {
					t.Errorf("node %d (%s) has no phase", n.Seq, n.Label)
				}
			}
		})
	}
}

// auditHost scripts graph-audit's accounting edge cases.
type auditHost struct{}

func (auditHost) RunAgent(_ context.Context, req AgentRequest) AgentResult {
	p := req.Prompt
	switch {
	case strings.HasPrefix(p, "Resolve the scope"):
		return AgentResult{Value: map[string]any{
			"files": []any{"a.go", "b.go", "c.go"}, "issue": "nil deref", "totalMatched": 10,
			"appliedLessons": []any{"use make test"},
		}}
	case strings.Contains(p, "FILE: a.go\n"):
		return AgentResult{Value: map[string]any{"findings": []any{
			map[string]any{"line": 10, "claim": "nil map write", "evidence": "m[k] = v", "severity": "high"},
		}}}
	case strings.Contains(p, "FILE: b.go\n"):
		return AgentResult{Err: errors.New("terminal API error")} // dead node
	case strings.Contains(p, "FILE: c.go\n"):
		return AgentResult{Value: map[string]any{"findings": []any{
			map[string]any{"line": 3, "claim": "unchecked err", "evidence": "_ = f()", "severity": "low"},
		}}}
	case strings.Contains(p, "file: a.go"):
		// Two of three lenses confirm → confirmed.
		holds := !strings.Contains(req.Label, "located")
		return AgentResult{Value: map[string]any{"holds": holds, "reason": req.Label}}
	case strings.Contains(p, "file: c.go"):
		// Only one verifier returns → below quorum → unverified.
		if strings.HasPrefix(req.Label, "correct") {
			return AgentResult{Value: map[string]any{"holds": true, "reason": "ok"}}
		}
		return AgentResult{Err: errors.New("verifier died")}
	case strings.HasPrefix(p, "Write one merged report"):
		return AgentResult{Value: map[string]any{
			"report":          "one confirmed finding",
			"lessonCandidate": map[string]any{"lesson": "run make test", "anchor": "make test"},
		}}
	}
	return AgentResult{Err: errors.New("unexpected prompt: " + p[:min(len(p), 60)])}
}

func (auditHost) ResolveWorkflow(context.Context, WorkflowRef) (string, error) {
	return "", errors.New("none")
}

// TestGraphEng_AuditAccounting is the runtime-side counterpart of graph-eng's
// guards.mjs: a dead auditor, a sub-quorum verification and a scoping cap
// must each be visible in the result, not folded into "clean".
func TestGraphEng_AuditAccounting(t *testing.T) {
	sc := graphEngScript(t, "graph-audit.js")
	r := &Run{ID: "audit"}
	out, err := Execute(context.Background(), r, sc, Options{Host: auditHost{}, Args: json.RawMessage(`{"cap": 3}`)})
	if err != nil {
		t.Fatalf("Execute: %v\nlogs: %+v", err, r.Snapshot().Logs)
	}
	var got struct {
		Issue                    string
		AppliedLessons           []string
		FilesAudited             int
		FilesExpected            int
		FilesSkippedByCap        int
		NodesThatReturnedNothing int
		Confirmed                []map[string]any
		Refuted                  []map[string]any
		Unverified               []map[string]any
		Report                   string
		RecordLesson             string
	}
	if err := json.Unmarshal(out, &got); err != nil {
		t.Fatal(err, string(out))
	}
	if got.FilesAudited != 2 || got.FilesExpected != 3 || got.NodesThatReturnedNothing != 1 {
		t.Errorf("audited=%d expected=%d dead=%d", got.FilesAudited, got.FilesExpected, got.NodesThatReturnedNothing)
	}
	if got.FilesSkippedByCap != 7 {
		t.Errorf("skippedByCap = %d, want 7", got.FilesSkippedByCap)
	}
	if len(got.Confirmed) != 1 || got.Confirmed[0]["file"] != "a.go" || got.Confirmed[0]["votesKept"] != float64(2) {
		t.Errorf("confirmed = %+v", got.Confirmed)
	}
	if len(got.Unverified) != 1 || got.Unverified[0]["file"] != "c.go" || got.Unverified[0]["votesCast"] != float64(1) {
		t.Errorf("unverified = %+v", got.Unverified)
	}
	if len(got.Refuted) != 0 {
		t.Errorf("refuted = %+v", got.Refuted)
	}
	if got.Report != "one confirmed finding" || !strings.Contains(got.RecordLesson, `graph-lesson record --lesson "run make test" --anchor "make test"`) {
		t.Errorf("report=%q recordLesson=%q", got.Report, got.RecordLesson)
	}
	if len(got.AppliedLessons) != 1 {
		t.Errorf("appliedLessons = %v", got.AppliedLessons)
	}

	snapLogs := r.Snapshot().Logs
	logText := make([]string, 0, len(snapLogs))
	for _, l := range snapLogs {
		logText = append(logText, l.Text)
	}
	joined := strings.Join(logText, "\n")
	for _, want := range []string{
		"WARNING: 1 of 3 audit nodes returned nothing",
		"Capped: auditing 3 of 10 matching files",
		"1 finding(s) could not be verified",
		"Applying 1 validated lesson(s)",
	} {
		if !strings.Contains(joined, want) {
			t.Errorf("log missing %q:\n%s", want, joined)
		}
	}
	s := r.Snapshot()
	var failed int
	for _, n := range s.Nodes {
		if n.Status == NodeFailed {
			failed++
		}
	}
	// 1 dead auditor + 2 dead verifiers on c.go.
	if failed != 3 {
		t.Errorf("failed nodes = %d, want 3", failed)
	}
}
