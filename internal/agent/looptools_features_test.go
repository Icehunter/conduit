package agent

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"strings"
	"sync"
	"testing"

	"github.com/icehunter/conduit/internal/api"
	"github.com/icehunter/conduit/internal/permissions"
	"github.com/icehunter/conduit/internal/settings"
	"github.com/icehunter/conduit/internal/tool"
	"github.com/icehunter/conduit/internal/ttsr"
)

// recTool records the input and agent scope of each call and answers with a
// fixed result. It can hold per-scope state, like the REPL tool.
type recTool struct {
	name     string
	schema   string
	result   string
	readOnly bool

	mu       sync.Mutex
	inputs   []json.RawMessage
	scopes   []string
	sessions []string
	released []string
}

func (r *recTool) Name() string        { return r.name }
func (r *recTool) Description() string { return "rec" }
func (r *recTool) InputSchema() json.RawMessage {
	if r.schema == "" {
		return json.RawMessage(`{"type":"object"}`)
	}
	return json.RawMessage(r.schema)
}
func (r *recTool) IsReadOnly(json.RawMessage) bool        { return r.readOnly }
func (r *recTool) IsConcurrencySafe(json.RawMessage) bool { return false }
func (r *recTool) Execute(ctx context.Context, in json.RawMessage) (tool.Result, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.inputs = append(r.inputs, in)
	r.scopes = append(r.scopes, tool.ScopeFromContext(ctx))
	r.sessions = append(r.sessions, tool.SessionIDFromContext(ctx))
	return tool.TextResult(r.result), nil
}
func (r *recTool) ReleaseScope(id string) {
	r.mu.Lock()
	r.released = append(r.released, id)
	r.mu.Unlock()
}

func (r *recTool) calls() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.inputs)
}

const recSchema = `{"type":"object","properties":{
	"count":{"type":"integer"},"flag":{"type":"boolean"},"tags":{"type":"array","items":{"type":"string"}},"cmd":{"type":"string"}},
	"required":["cmd"]}`

func hookCfg(matcher, command string) *settings.HooksSettings {
	return &settings.HooksSettings{PreToolUse: []settings.HookMatcher{{
		Matcher: matcher,
		Hooks:   []settings.Hook{{Type: "command", Command: command}},
	}}}
}

// toolResultText returns the text of the "t1" tool_result in history.
func toolResultText(t *testing.T, msgs []api.Message) string {
	t.Helper()
	for _, m := range msgs {
		for _, b := range m.Content {
			if b.Type == "tool_result" && b.ToolUseID == "t1" {
				return b.ResultContent
			}
		}
	}
	t.Fatal("no tool_result for t1 in history")
	return ""
}

func runLoop(t *testing.T, lp *Loop) ([]api.Message, []LoopEvent) {
	t.Helper()
	var evs []LoopEvent
	msgs, err := lp.Run(context.Background(), []api.Message{user("go")}, func(ev LoopEvent) { evs = append(evs, ev) })
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	return msgs, evs
}

func TestLoop_CoercesToolArguments(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want map[string]any
	}{
		{"scalars", `{"cmd":"x","count":"42","flag":"true"}`, map[string]any{"cmd": "x", "count": float64(42), "flag": true}},
		{"bare string for array", `{"cmd":"x","tags":"solo"}`, map[string]any{"cmd": "x", "tags": []any{"solo"}}},
		{"correct types pass through", `{"cmd":"x","count":3}`, map[string]any{"cmd": "x", "count": float64(3)}},
		{"uncoercible stays", `{"cmd":"x","count":"lots"}`, map[string]any{"cmd": "x", "count": "lots"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rt := &recTool{name: "Rec", schema: recSchema, result: "ok", readOnly: true}
			reg := tool.NewRegistry()
			reg.Register(rt)
			lp, srv := newTestLoopWithConfig(t, []string{singleToolUseSSEWithInput("Rec", "t1", tt.in), textOnlySSE("done")}, reg, LoopConfig{})
			defer srv.Close()
			msgs, _ := runLoop(t, lp)
			if rt.calls() != 1 {
				t.Fatalf("calls = %d", rt.calls())
			}
			var got map[string]any
			if err := json.Unmarshal(rt.inputs[0], &got); err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("tool saw %v, want %v", got, tt.want)
			}
			// History keeps what the model emitted.
			var modelIn map[string]any
			for _, m := range msgs {
				for _, b := range m.Content {
					if b.Type == "tool_use" {
						modelIn = b.Input
					}
				}
			}
			var emitted map[string]any
			_ = json.Unmarshal([]byte(tt.in), &emitted)
			if !reflect.DeepEqual(modelIn, emitted) {
				t.Errorf("history input = %v, want the model's %v", modelIn, emitted)
			}
		})
	}
}

func TestLoop_PreToolUseUpdatedInput(t *testing.T) {
	tests := []struct {
		name       string
		hook       string
		in         string
		wantCalls  int
		wantInput  map[string]any
		wantResult string // substring of the tool_result
	}{
		{"input replaced",
			`echo '{"hookSpecificOutput":{"hookEventName":"PreToolUse","updatedInput":{"cmd":"safe","count":"5"}}}'`,
			`{"cmd":"rm -rf /"}`, 1, map[string]any{"cmd": "safe", "count": float64(5)}, "ok"},
		{"replaced and allowed",
			`echo '{"hookSpecificOutput":{"permissionDecision":"allow","updatedInput":{"cmd":"safe"}}}'`,
			`{"cmd":"rm -rf /"}`, 1, map[string]any{"cmd": "safe"}, "ok"},
		{"missing required is denied",
			`echo '{"hookSpecificOutput":{"updatedInput":{"count":1}}}'`,
			`{"cmd":"x"}`, 0, nil, "failed schema validation: missing required cmd"},
		{"permissionDecision deny blocks",
			`echo '{"hookSpecificOutput":{"permissionDecision":"deny","permissionDecisionReason":"policy"}}'`,
			`{"cmd":"x"}`, 0, nil, "Tool blocked by hook: policy"},
		{"no directive leaves input",
			`echo '{}'`, `{"cmd":"x"}`, 1, map[string]any{"cmd": "x"}, "ok"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rt := &recTool{name: "Rec", schema: recSchema, result: "ok", readOnly: true}
			reg := tool.NewRegistry()
			reg.Register(rt)
			lp, srv := newTestLoopWithConfig(t, []string{singleToolUseSSEWithInput("Rec", "t1", tt.in), textOnlySSE("done")}, reg,
				LoopConfig{Hooks: hookCfg("Rec", tt.hook), SessionID: "s"})
			defer srv.Close()
			msgs, _ := runLoop(t, lp)
			if rt.calls() != tt.wantCalls {
				t.Fatalf("calls = %d, want %d", rt.calls(), tt.wantCalls)
			}
			if tt.wantInput != nil {
				var got map[string]any
				_ = json.Unmarshal(rt.inputs[0], &got)
				if !reflect.DeepEqual(got, tt.wantInput) {
					t.Errorf("tool saw %v, want %v", got, tt.wantInput)
				}
			}
			if res := toolResultText(t, msgs); !strings.Contains(res, tt.wantResult) {
				t.Errorf("result %q lacks %q", res, tt.wantResult)
			}
		})
	}
}

// A rewritten input is what the permission rules see: a deny rule on the new
// command applies, and one on the model's original does not.
func TestLoop_UpdatedInputIsWhatPermissionsCheck(t *testing.T) {
	tests := []struct {
		name      string
		deny      string
		wantCalls int
	}{
		{"rule on the rewritten command", "Bash(danger*)", 0},
		{"rule on the original command", "Bash(rm*)", 1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rt := &recTool{name: "Bash", schema: `{"type":"object","properties":{"command":{"type":"string"}},"required":["command"]}`, result: "ok"}
			reg := tool.NewRegistry()
			reg.Register(rt)
			gate := permissions.New(t.TempDir(), nil, permissions.ModeDefault, []string{"Bash"}, []string{tt.deny}, nil)
			hook := `echo '{"hookSpecificOutput":{"updatedInput":{"command":"danger zone"}}}'`
			lp, srv := newTestLoopWithConfig(t, []string{singleToolUseSSEWithInput("Bash", "t1", `{"command":"rm x"}`), textOnlySSE("done")}, reg,
				LoopConfig{Gate: gate, Hooks: hookCfg("Bash", hook), SessionID: "s"})
			defer srv.Close()
			runLoop(t, lp)
			if rt.calls() != tt.wantCalls {
				t.Errorf("calls = %d, want %d", rt.calls(), tt.wantCalls)
			}
		})
	}
}

func TestLoop_HookAskForcesPrompt(t *testing.T) {
	tests := []struct {
		name      string
		hook      string
		extraHook string // a second hook, run after the first
		readOnly  bool
		wantAsked int
	}{
		{"ask on a read-only tool prompts", `echo '{"hookSpecificOutput":{"permissionDecision":"ask"}}'`, "", true, 1},
		{"ask beats allow rule", `echo '{"hookSpecificOutput":{"permissionDecision":"ask"}}'`, "", false, 1},
		{"allow does not prompt", `echo '{"hookSpecificOutput":{"permissionDecision":"allow"}}'`, "", false, 0},
		{"read-only without hook does not prompt", `echo '{}'`, "", true, 0},
		{"ask outranks another hook's allow", `echo '{"hookSpecificOutput":{"permissionDecision":"ask"}}'`,
			`echo '{"hookSpecificOutput":{"permissionDecision":"allow"}}'`, false, 1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rt := &recTool{name: "Write", result: "ok", readOnly: tt.readOnly}
			reg := tool.NewRegistry()
			reg.Register(rt)
			gate := permissions.New(t.TempDir(), nil, permissions.ModeDefault, []string{"Write"}, nil, nil)
			asked := 0
			hc := hookCfg("Write", tt.hook)
			if tt.extraHook != "" {
				hc.PreToolUse = append(hc.PreToolUse, hookCfg("Write", tt.extraHook).PreToolUse...)
			}
			lp, srv := newTestLoopWithConfig(t, []string{singleToolUseSSEWithInput("Write", "t1", `{"file_path":"a.txt"}`), textOnlySSE("done")}, reg, LoopConfig{
				Gate: gate, Hooks: hc, SessionID: "s",
				AskPermission: func(context.Context, string, string) (bool, bool) { asked++; return false, false },
			})
			defer srv.Close()
			runLoop(t, lp)
			if asked != tt.wantAsked {
				t.Errorf("asked = %d, want %d", asked, tt.wantAsked)
			}
		})
	}
}

func TestLoop_RedactsSecretsInToolResults(t *testing.T) {
	secret := "AKIA" + "IOSFODNN7EXAMPLE"
	tests := []struct {
		name       string
		redact     func() bool
		wantMasked bool
	}{
		{"default on", nil, true},
		{"explicitly on", func() bool { return true }, true},
		{"configured off", func() bool { return false }, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rt := &recTool{name: "Rec", result: "creds: " + secret, readOnly: true}
			reg := tool.NewRegistry()
			reg.Register(rt)
			lp, srv := newTestLoopWithConfig(t, []string{singleToolUseSSEWithInput("Rec", "t1", `{}`), textOnlySSE("done")}, reg,
				LoopConfig{RedactSecrets: tt.redact})
			defer srv.Close()
			msgs, evs := runLoop(t, lp)
			res := toolResultText(t, msgs)
			if masked := !strings.Contains(res, secret); masked != tt.wantMasked {
				t.Errorf("history masked = %v, want %v (%q)", masked, tt.wantMasked, res)
			}
			for _, ev := range evs {
				if ev.Type == EventToolResult && strings.Contains(ev.ResultText, secret) == tt.wantMasked {
					t.Errorf("UI event masked = %v, want %v", !strings.Contains(ev.ResultText, secret), tt.wantMasked)
				}
			}
			if tt.wantMasked && !strings.Contains(res, "[REDACTED:aws-access-key]") {
				t.Errorf("no placeholder in %q", res)
			}
		})
	}
}

func TestLoop_TTSRToolArgsInterrupt(t *testing.T) {
	rt := &recTool{name: "Write", result: "ok", readOnly: true}
	reg := tool.NewRegistry()
	reg.Register(rt)
	lp, srv := newTestLoop(t, []string{
		singleToolUseSSEWithInput("Write", "t1", `{"file_path":"a/b.go","content":"panic(1)"}`),
		textOnlySSE("fine, no panic"),
	}, reg)
	defer srv.Close()
	lp.SetTTSRRules([]ttsr.Rule{{
		Name: "no-panic", Pattern: regexp.MustCompile(`panic\(`), Correction: "Return errors instead of panicking.",
		Tools: []string{"Write"}, Paths: []string{"**/*.go"},
	}})
	msgs, evs := runLoop(t, lp)
	if rt.calls() != 0 {
		t.Errorf("tool ran %d times despite the interrupt", rt.calls())
	}
	var fired int
	for _, ev := range evs {
		if ev.Type == EventTTSR && ev.TTSRRule == "no-panic" {
			fired++
		}
	}
	if fired != 1 {
		t.Errorf("TTSR events = %d, want 1", fired)
	}
	if !historyHas(msgs, "Return errors instead of panicking.") {
		t.Error("correction missing from history")
	}
	for _, m := range msgs {
		for _, b := range m.Content {
			if b.Type == "tool_use" {
				t.Errorf("interrupted tool_use survived in history: %+v", b)
			}
		}
	}
}

func TestLoop_TTSRWarnDoesNotInterrupt(t *testing.T) {
	rt := &recTool{name: "Write", result: "ok", readOnly: true}
	reg := tool.NewRegistry()
	reg.Register(rt)
	lp, srv := newTestLoop(t, []string{
		singleToolUseSSEWithInput("Write", "t1", `{"file_path":"a.go","content":"// TODO later"}`),
		textOnlySSE("done"),
	}, reg)
	defer srv.Close()
	lp.SetTTSRRules([]ttsr.Rule{{
		Name: "todo", Pattern: regexp.MustCompile(`TODO`), Correction: "Leave no TODOs.",
		Scope: ttsr.ScopeToolArgs, Mode: ttsr.ModeWarn,
	}})
	msgs, evs := runLoop(t, lp)
	if rt.calls() != 1 {
		t.Errorf("tool calls = %d, want 1 (warn must not interrupt)", rt.calls())
	}
	for _, ev := range evs {
		if ev.Type == EventTTSR {
			t.Errorf("warn rule produced a restart event: %+v", ev)
		}
	}
	if !historyHas(msgs, "Leave no TODOs.") {
		t.Error("warning not handed to the model")
	}
}

func TestLoop_RepetitionGuardInterrupts(t *testing.T) {
	loop := strings.Repeat("I will check the configuration file again. ", 90)
	tests := []struct {
		name      string
		guard     func() bool
		wantFires int
	}{
		{"default on", nil, 1},
		{"disabled", func() bool { return false }, 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			lp, srv := newTestLoopWithConfig(t, []string{textOnlySSE(loop), textOnlySSE("moving on")}, tool.NewRegistry(),
				LoopConfig{RepetitionGuard: tt.guard})
			defer srv.Close()
			msgs, evs := runLoop(t, lp)
			fires := 0
			for _, ev := range evs {
				if ev.Type == EventTTSR && ev.TTSRRule == "repetition-guard" {
					fires++
				}
			}
			if fires != tt.wantFires {
				t.Errorf("repetition fires = %d, want %d", fires, tt.wantFires)
			}
			if tt.wantFires > 0 {
				for _, m := range msgs {
					for _, b := range m.Content {
						if strings.Contains(b.Text, "configuration file again") {
							t.Errorf("looped text kept in history: %.60q", b.Text)
						}
					}
				}
			}
		})
	}
}

func historyHas(msgs []api.Message, sub string) bool {
	for _, m := range msgs {
		for _, b := range m.Content {
			if strings.Contains(b.Text, sub) {
				return true
			}
		}
	}
	return false
}

// A sub-agent runs its tools under its own scope and its scope is released
// when it ends; the root agent's calls carry none.
func TestSubAgentGetsOwnToolScope(t *testing.T) {
	rt := &recTool{name: "Rec", result: "ok", readOnly: true}
	reg := tool.NewRegistry()
	reg.Register(rt)
	lp, srv := newTestLoopWithConfig(t, []string{
		singleToolUseSSEWithInput("Rec", "p1", `{}`), textOnlySSE("parent done"),
		singleToolUseSSEWithInput("Rec", "c1", `{}`), textOnlySSE("child done"),
		singleToolUseSSEWithInput("Rec", "c2", `{}`), textOnlySSE("child2 done"),
	}, reg, LoopConfig{})
	defer srv.Close()

	runLoop(t, lp)
	if _, err := lp.RunSubAgentTyped(context.Background(), "first", SubAgentSpec{}); err != nil {
		t.Fatalf("sub-agent 1: %v", err)
	}
	if _, err := lp.RunSubAgentTyped(context.Background(), "second", SubAgentSpec{}); err != nil {
		t.Fatalf("sub-agent 2: %v", err)
	}
	rt.mu.Lock()
	defer rt.mu.Unlock()
	if len(rt.scopes) != 3 {
		t.Fatalf("scopes = %v, want 3 calls", rt.scopes)
	}
	if rt.scopes[0] != "" {
		t.Errorf("root agent scope = %q, want none", rt.scopes[0])
	}
	if rt.scopes[1] == "" || rt.scopes[2] == "" || rt.scopes[1] == rt.scopes[2] {
		t.Errorf("sub-agent scopes %q, %q: want two distinct non-empty ids", rt.scopes[1], rt.scopes[2])
	}
	want := []string{rt.scopes[1], rt.scopes[2]}
	if !reflect.DeepEqual(rt.released, want) {
		t.Errorf("released = %v, want %v", rt.released, want)
	}
}

func TestLoop_RepetitionGuardWatchesThinking(t *testing.T) {
	loop := strings.Repeat("Let me reconsider the configuration file once more. ", 90)
	lp, srv := newTestLoop(t, []string{
		sseMessage("end_turn", sseThinkingBlock(0, loop), sseTextBlock("answer")),
		textOnlySSE("moving on"),
	}, tool.NewRegistry())
	defer srv.Close()
	_, evs := runLoop(t, lp)
	fires := 0
	for _, ev := range evs {
		if ev.Type == EventTTSR && ev.TTSRRule == "repetition-guard" {
			fires++
		}
	}
	if fires != 1 {
		t.Errorf("repetition fires on a thinking loop = %d, want 1", fires)
	}
}

// A hook "allow" does not skip an explicit ask rule.
func TestLoop_HookAllowDoesNotSkipAskRule(t *testing.T) {
	tests := []struct {
		name      string
		ask       []string
		wantAsked int
	}{
		{"ask rule matches", []string{"Write(*.env)"}, 1},
		{"no ask rule", nil, 0},
		{"ask rule for something else", []string{"Write(*.md)"}, 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rt := &recTool{name: "Write", result: "ok"}
			reg := tool.NewRegistry()
			reg.Register(rt)
			gate := permissions.New(t.TempDir(), nil, permissions.ModeDefault, nil, nil, tt.ask)
			asked := 0
			hook := `echo '{"hookSpecificOutput":{"permissionDecision":"allow"}}'`
			lp, srv := newTestLoopWithConfig(t, []string{singleToolUseSSEWithInput("Write", "t1", `{"file_path":"prod.env"}`), textOnlySSE("done")}, reg, LoopConfig{
				Gate: gate, Hooks: hookCfg("Write", hook), SessionID: "s",
				AskPermission: func(context.Context, string, string) (bool, bool) { asked++; return false, false },
			})
			defer srv.Close()
			runLoop(t, lp)
			if asked != tt.wantAsked {
				t.Errorf("asked = %d, want %d", asked, tt.wantAsked)
			}
		})
	}
}

const awsKeyForMask = "AKIA" + "IOSFODNN7EXAMPLE"

// File contents are shown verbatim; only other tools' output is masked.
func TestLoop_FileReadResultsAreNotRedacted(t *testing.T) {
	tests := []struct {
		tool       string
		input      string
		wantMasked bool
	}{
		{"Read", `{"file_path":"/tmp/x.env"}`, false},
		{"Read", `{"file_path":"https://example.com/x"}`, true},
		{"Read", `{"file_path":"HTTP://example.com/x"}`, true},
		{"Read", `{"file_path":"pr://o/r/1"}`, true},
		{"Read", `{"file_path":"issue://o/r/1"}`, true},
		{"Bash", `{}`, true},
		{"WebFetch", `{}`, true},
		{"mcp__srv__tool", `{}`, true},
	}
	for _, tt := range tests {
		t.Run(tt.tool+tt.input, func(t *testing.T) {
			rt := &recTool{name: tt.tool, result: "k=" + awsKeyForMask, readOnly: true}
			reg := tool.NewRegistry()
			reg.Register(rt)
			lp, srv := newTestLoopWithConfig(t, []string{singleToolUseSSEWithInput(tt.tool, "t1", tt.input), textOnlySSE("done")}, reg, LoopConfig{})
			defer srv.Close()
			msgs, _ := runLoop(t, lp)
			res := toolResultText(t, msgs)
			if masked := !strings.Contains(res, awsKeyForMask); masked != tt.wantMasked {
				t.Errorf("masked = %v, want %v (%q)", masked, tt.wantMasked, res)
			}
		})
	}
}

// A model that copies a masked line into an edit must not overwrite the real
// secret with the placeholder.
func TestLoop_EditsCarryingMaskAreRejected(t *testing.T) {
	dir := t.TempDir()
	withMask := filepath.Join(dir, "has_mask.txt")
	plain := filepath.Join(dir, "plain.txt")
	if err := os.WriteFile(withMask, []byte("x [REDACTED:aws-access-key] y"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(plain, []byte("hello"), 0o600); err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name      string
		tool      string
		input     string
		wantCalls int
	}{
		{"write with placeholder", "Write", `{"file_path":"` + plain + `","content":"k=[REDACTED:aws-access-key]"}`, 0},
		{"edit new_string with placeholder", "Edit", `{"file_path":"` + plain + `","old_string":"hello","new_string":"[REDACTED:jwt]"}`, 0},
		{"edit old_string with placeholder", "Edit", `{"file_path":"` + plain + `","old_string":"[REDACTED:jwt]","new_string":"x"}`, 0},
		{"hashedit with placeholder", "HashEdit", `{"file_path":"` + plain + `","edits":[{"anchor":"1#ab","new_lines":"[REDACTED:jwt]"}]}`, 0},
		{"notebook edit with placeholder", "NotebookEdit", `{"notebook_path":"` + plain + `","new_source":"[REDACTED:jwt]"}`, 0},
		{"placeholder already in the file", "Edit", `{"file_path":"` + withMask + `","old_string":"[REDACTED:aws-access-key]","new_string":"[REDACTED:aws-access-key] z"}`, 1},
		{"other placeholder not in the file", "Edit", `{"file_path":"` + withMask + `","old_string":"x","new_string":"[REDACTED:jwt]"}`, 0},
		{"missing file", "Write", `{"file_path":"` + filepath.Join(dir, "new.txt") + `","content":"[REDACTED:jwt]"}`, 0},
		{"skill update with placeholder", "SkillManage", `{"action":"update","name":"zz-nonexistent","content":"k=[REDACTED:aws-access-key]"}`, 0},
		{"skill write_file with placeholder", "SkillManage", `{"action":"write_file","name":"zz-nonexistent","file_path":"references/a.md","content":"[REDACTED:jwt]"}`, 0},
		{"skill update without placeholder", "SkillManage", `{"action":"update","name":"zz-nonexistent","content":"plain"}`, 1},
		{"plain edit", "Edit", `{"file_path":"` + plain + `","old_string":"hello","new_string":"bye"}`, 1},
		{"not an edit tool", "Bash", `{"command":"echo [REDACTED:jwt]"}`, 1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rt := &recTool{name: tt.tool, result: "ok", readOnly: true}
			reg := tool.NewRegistry()
			reg.Register(rt)
			lp, srv := newTestLoopWithConfig(t, []string{singleToolUseSSEWithInput(tt.tool, "t1", tt.input), textOnlySSE("done")}, reg, LoopConfig{})
			defer srv.Close()
			msgs, _ := runLoop(t, lp)
			if rt.calls() != tt.wantCalls {
				t.Fatalf("calls = %d, want %d", rt.calls(), tt.wantCalls)
			}
			if tt.wantCalls == 0 && !strings.Contains(toolResultText(t, msgs), "Secrets are masked in tool output") {
				t.Errorf("result lacks the explanation: %q", toolResultText(t, msgs))
			}
		})
	}

	t.Run("allowed when redaction is off", func(t *testing.T) {
		rt := &recTool{name: "Write", result: "ok", readOnly: true}
		reg := tool.NewRegistry()
		reg.Register(rt)
		lp, srv := newTestLoopWithConfig(t, []string{singleToolUseSSEWithInput("Write", "t1", `{"file_path":"`+plain+`","content":"[REDACTED:jwt]"}`), textOnlySSE("done")}, reg,
			LoopConfig{RedactSecrets: func() bool { return false }})
		defer srv.Close()
		runLoop(t, lp)
		if rt.calls() != 1 {
			t.Errorf("calls = %d, want 1", rt.calls())
		}
	})
}

// Repetition-guard interrupts do not use up the per-turn budget of rule fires.
func TestLoop_RepetitionFiresCountSeparately(t *testing.T) {
	loop := strings.Repeat("I will check the configuration file again. ", 90)
	lp, srv := newTestLoop(t, []string{
		textOnlySSE(loop), textOnlySSE(loop),
		textOnlySSE("TRIGGER one"), textOnlySSE("TRIGGER two"), textOnlySSE("TRIGGER three"),
		textOnlySSE("all good"),
	}, tool.NewRegistry())
	defer srv.Close()
	lp.SetTTSRRules([]ttsr.Rule{{Name: "trig", Pattern: regexp.MustCompile(`TRIGGER`), Correction: "no triggers"}})
	_, evs := runLoop(t, lp)
	counts := map[string]int{}
	for _, ev := range evs {
		if ev.Type == EventTTSR {
			counts[ev.TTSRRule]++
		}
	}
	if counts["repetition-guard"] != 2 || counts["trig"] != 3 {
		t.Errorf("fires = %v, want repetition-guard 2 and trig 3", counts)
	}
}

// Tools see the loop's live session id, which follows SetSessionID (an
// in-process /resume), not whatever was configured at startup.
func TestToolsReceiveLiveSessionID(t *testing.T) {
	rt := &recTool{name: "Rec", result: "ok", readOnly: true}
	reg := tool.NewRegistry()
	reg.Register(rt)
	lp, srv := newTestLoopWithConfig(t, []string{
		singleToolUseSSEWithInput("Rec", "a", `{}`), textOnlySSE("done"),
		singleToolUseSSEWithInput("Rec", "b", `{}`), textOnlySSE("done"),
	}, reg, LoopConfig{SessionID: "startup"})
	defer srv.Close()

	runLoop(t, lp)
	lp.SetSessionID("resumed")
	runLoop(t, lp)
	rt.mu.Lock()
	defer rt.mu.Unlock()
	if len(rt.sessions) != 2 || rt.sessions[0] != "startup" || rt.sessions[1] != "resumed" {
		t.Errorf("session ids seen by the tool = %v, want [startup resumed]", rt.sessions)
	}
}
