package agent

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
	"testing"

	"github.com/icehunter/conduit/internal/api"
	"github.com/icehunter/conduit/internal/permissions"
	"github.com/icehunter/conduit/internal/tool"
)

// systemRecorder records the text of each request's system blocks.
type systemRecorder struct {
	mu       sync.Mutex
	requests [][]string
}

func (s *systemRecorder) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	raw, _ := io.ReadAll(r.Body)
	var body struct {
		System []struct {
			Text string `json:"text"`
		} `json:"system"`
	}
	_ = json.Unmarshal(raw, &body)
	texts := make([]string, 0, len(body.System))
	for _, b := range body.System {
		texts = append(texts, b.Text)
	}
	s.mu.Lock()
	s.requests = append(s.requests, texts)
	s.mu.Unlock()
	w.Header().Set("Content-Type", "text/event-stream")
	_, _ = w.Write([]byte(textOnlySSE("done")))
}

func (s *systemRecorder) got() [][]string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([][]string(nil), s.requests...)
}

func newOverlayLoop(t *testing.T, rebuild func() []api.SystemBlock) (*Loop, *systemRecorder) {
	t.Helper()
	rec := &systemRecorder{}
	srv := httptest.NewServer(rec)
	t.Cleanup(srv.Close)
	c := api.NewClient(api.Config{BaseURL: srv.URL, AuthToken: "t"}, srv.Client())
	lp := NewLoop(c, tool.NewRegistry(), LoopConfig{
		Model: "m", MaxTokens: 1024,
		System:        []api.SystemBlock{{Type: "text", Text: "base"}},
		RebuildSystem: rebuild,
	})
	return lp, rec
}

func runTwice(t *testing.T, lp *Loop) {
	t.Helper()
	for i := 0; i < 2; i++ {
		if _, err := lp.Run(context.Background(), []api.Message{user("hi")}, func(LoopEvent) {}); err != nil {
			t.Fatalf("Run %d: %v", i+1, err)
		}
	}
}

// TestLoop_SystemOverlay_SurvivesRuns covers the plan-mode directive and the
// output style: both are overlay blocks that must follow the rebuilt base on
// every Run, never precede it.
func TestLoop_SystemOverlay_SurvivesRuns(t *testing.T) {
	tests := []struct {
		name    string
		overlay []string
		want    []string
	}{
		{"plan directive", []string{PlanModeDirective}, []string{"base2", PlanModeDirective}},
		{"output style", []string{"# Output style: x\n\nbe terse"}, []string{"base2", "# Output style: x\n\nbe terse"}},
		{"style then directive keeps order", []string{"style", "directive"}, []string{"base2", "style", "directive"}},
		{"no overlay", nil, []string{"base2"}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			lp, rec := newOverlayLoop(t, func() []api.SystemBlock {
				return []api.SystemBlock{{Type: "text", Text: "base2"}}
			})
			blocks := make([]api.SystemBlock, 0, len(tc.overlay))
			for _, o := range tc.overlay {
				blocks = append(blocks, api.SystemBlock{Type: "text", Text: o})
			}
			lp.SetSystemOverlay(blocks)
			runTwice(t, lp)
			reqs := rec.got()
			if len(reqs) != 2 {
				t.Fatalf("requests = %d, want 2", len(reqs))
			}
			for i, r := range reqs {
				if !reflect.DeepEqual(r, tc.want) {
					t.Errorf("request %d system = %q, want %q", i+1, r, tc.want)
				}
			}
		})
	}
}

// TestRunSubAgentTyped_SystemPromptSurvivesParentRebuild guards the child
// loop: a parent RebuildSystem must not wipe the typed prompt.
func TestRunSubAgentTyped_SystemPromptSurvivesParentRebuild(t *testing.T) {
	lp, rec := newOverlayLoop(t, func() []api.SystemBlock {
		return []api.SystemBlock{{Type: "text", Text: "rebuilt-parent"}}
	})
	lp.SetSystemOverlay([]api.SystemBlock{{Type: "text", Text: "parent-overlay"}})
	if _, err := lp.RunSubAgentTyped(context.Background(), "go", SubAgentSpec{SystemPrompt: "typed-prompt"}); err != nil {
		t.Fatalf("RunSubAgentTyped: %v", err)
	}
	reqs := rec.got()
	if len(reqs) != 1 {
		t.Fatalf("requests = %d, want 1", len(reqs))
	}
	want := []string{"base", "typed-prompt"}
	if !reflect.DeepEqual(reqs[0], want) {
		t.Errorf("child system = %q, want %q", reqs[0], want)
	}
}

// TestRunSubAgentWithModel_DoesNotRebuildSystem guards the other child path.
func TestRunSubAgentWithModel_DoesNotRebuildSystem(t *testing.T) {
	lp, rec := newOverlayLoop(t, func() []api.SystemBlock {
		return []api.SystemBlock{{Type: "text", Text: "rebuilt-parent"}}
	})
	if _, err := lp.RunSubAgent(context.Background(), "go"); err != nil {
		t.Fatalf("RunSubAgent: %v", err)
	}
	reqs := rec.got()
	if len(reqs) != 1 {
		t.Fatalf("requests = %d, want 1", len(reqs))
	}
	if !reflect.DeepEqual(reqs[0], []string{"base"}) {
		t.Errorf("child system = %q, want [base]", reqs[0])
	}
}

// TestSubAgent_ModeDirectiveInheritance: children inherit the plan/council
// directive only while their own gate is in that mode, never for background
// work, and never the output style.
func TestSubAgent_ModeDirectiveInheritance(t *testing.T) {
	const style = "# Output style: terse"
	tests := []struct {
		name       string
		parentMode permissions.Mode
		directive  string
		run        func(*Loop) error
		want       []string
	}{
		{"typed sub-agent in plan", permissions.ModePlan, PlanModeDirective,
			func(l *Loop) error {
				_, err := l.RunSubAgentTyped(context.Background(), "go", SubAgentSpec{})
				return err
			},
			[]string{"base", PlanModeDirective}},
		{"RunSubAgent in plan", permissions.ModePlan, PlanModeDirective,
			func(l *Loop) error { _, err := l.RunSubAgent(context.Background(), "go"); return err },
			[]string{"base", PlanModeDirective}},
		{"council", permissions.ModeCouncil, CouncilModeDirective,
			func(l *Loop) error { _, err := l.RunSubAgent(context.Background(), "go"); return err },
			[]string{"base", CouncilModeDirective}},
		{"background agent in plan", permissions.ModePlan, PlanModeDirective,
			func(l *Loop) error { _, err := l.RunBackgroundAgent(context.Background(), "go"); return err },
			[]string{"base"}},
		{"background sub-agent in plan", permissions.ModePlan, PlanModeDirective,
			func(l *Loop) error { _, err := l.RunBackgroundSubAgent(context.Background(), "go"); return err },
			[]string{"base"}},
		{"typed background spec in plan", permissions.ModePlan, PlanModeDirective,
			func(l *Loop) error {
				_, err := l.RunSubAgentTyped(context.Background(), "go", SubAgentSpec{Background: true})
				return err
			},
			[]string{"base"}},
		{"typed child switched to bypass", permissions.ModePlan, PlanModeDirective,
			func(l *Loop) error {
				_, err := l.RunSubAgentTyped(context.Background(), "go", SubAgentSpec{Mode: permissions.ModeBypassPermissions})
				return err
			},
			[]string{"base"}},
		{"stale directive but default-mode child", permissions.ModeDefault, PlanModeDirective,
			func(l *Loop) error { _, err := l.RunSubAgent(context.Background(), "go"); return err },
			[]string{"base"}},
		{"default mode parent", permissions.ModeDefault, "",
			func(l *Loop) error { _, err := l.RunSubAgent(context.Background(), "go"); return err },
			[]string{"base"}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			rec := &systemRecorder{}
			srv := httptest.NewServer(rec)
			t.Cleanup(srv.Close)
			c := api.NewClient(api.Config{BaseURL: srv.URL, AuthToken: "t"}, srv.Client())
			lp := NewLoop(c, tool.NewRegistry(), LoopConfig{
				Model: "m", MaxTokens: 1024,
				System: []api.SystemBlock{{Type: "text", Text: "base"}},
				Gate:   permissions.New("", nil, tc.parentMode, nil, nil, nil),
			})
			overlay := []api.SystemBlock{{Type: "text", Text: style}}
			var directive []api.SystemBlock
			if tc.directive != "" {
				directive = []api.SystemBlock{{Type: "text", Text: tc.directive}}
				overlay = append(overlay, directive...)
			}
			lp.SetSystemOverlay(overlay)
			lp.SetModeDirective(directive)
			if err := tc.run(lp); err != nil {
				t.Fatal(err)
			}
			reqs := rec.got()
			if len(reqs) != 1 || !reflect.DeepEqual(reqs[0], tc.want) {
				t.Errorf("child system = %q, want %q", reqs, tc.want)
			}
		})
	}
}

// TestRunSubAgentTyped_IsolatedCwdFromContext: Task(isolation: "worktree")
// passes the worktree through ctx; the child must be told it lives there.
func TestRunSubAgentTyped_IsolatedCwdFromContext(t *testing.T) {
	lp, rec := newOverlayLoop(t, nil)
	ctx := tool.WithIsolatedCwd(context.Background(), "/wt/iso")
	if _, err := lp.RunSubAgentTyped(ctx, "go", SubAgentSpec{}); err != nil {
		t.Fatalf("RunSubAgentTyped: %v", err)
	}
	reqs := rec.got()
	if len(reqs) != 1 || !strings.Contains(strings.Join(reqs[0], "\n"), "/wt/iso") {
		t.Errorf("child system should mention the worktree, got %q", reqs)
	}
}

func TestLoop_AnnounceModelNamesLiveModel(t *testing.T) {
	l := NewLoop(nil, nil, LoopConfig{Model: "m1", AnnounceModel: true, System: []api.SystemBlock{{Type: "text", Text: "base"}}})
	l.SetModel("m2")
	l.mu.RLock()
	system := l.composeSystemLocked()
	l.mu.RUnlock()
	last := system[len(system)-1].Text
	if len(system) != 2 || !strings.Contains(last, "model m2.") {
		t.Fatalf("system = %+v", system)
	}
}
