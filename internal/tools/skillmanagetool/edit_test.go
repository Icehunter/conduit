package skillmanagetool

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/icehunter/conduit/internal/skillusage"
)

const baseSkill = "---\nname: demo\ndescription: Use when demoing.\n---\n## When to Use\nAlways.\n\n## Steps\n1. one\n2. two\n\n### Detail\nnested\n\n## Notes\nlast\n"

// env isolates the conduit dir and returns a tool plus the project skill dir.
func env(t *testing.T, opts ...Option) (*Tool, string) {
	t.Helper()
	cwd := t.TempDir()
	t.Setenv("CONDUIT_CONFIG_DIR", filepath.Join(t.TempDir(), ".conduit"))
	return New(cwd, opts...), filepath.Join(cwd, ".claude", "skills", "demo")
}

func mustCreate(t *testing.T, tk *Tool) {
	t.Helper()
	if text, isErr := mustExec(t, tk, map[string]any{"action": "create", "name": "demo", "content": baseSkill}); isErr {
		t.Fatalf("create: %s", text)
	}
}

func readFile(t *testing.T, p string) string {
	t.Helper()
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestCreateUpdate_RejectsInvalidContent(t *testing.T) {
	tests := []struct {
		name    string
		content string
		wantErr string
	}{
		{"no frontmatter", "# Title\nbody", "frontmatter"},
		{"missing description", "---\nname: demo\n---\nbody", "description"},
		{"missing name", "---\ndescription: d\n---\nbody", "name"},
		{"bad name", "---\nname: Bad Name\ndescription: d\n---\nbody", "name"},
		{"long description", "---\nname: demo\ndescription: " + strings.Repeat("d", 1025) + "\n---\nbody", "limit"},
		{"oversized", "---\nname: demo\ndescription: d\n---\n" + strings.Repeat("x", 100_001), "limit"},
	}
	for _, tt := range tests {
		t.Run("create/"+tt.name, func(t *testing.T) {
			tk, dir := env(t)
			text, isErr := mustExec(t, tk, map[string]any{"action": "create", "name": "demo", "content": tt.content})
			if !isErr || !strings.Contains(text, tt.wantErr) {
				t.Fatalf("isErr=%v text=%q", isErr, text)
			}
			if _, err := os.Stat(dir); !os.IsNotExist(err) {
				t.Error("rejected create left files behind")
			}
		})
		t.Run("update/"+tt.name, func(t *testing.T) {
			tk, dir := env(t)
			mustCreate(t, tk)
			text, isErr := mustExec(t, tk, map[string]any{"action": "update", "name": "demo", "content": tt.content})
			if !isErr || !strings.Contains(text, tt.wantErr) {
				t.Fatalf("isErr=%v text=%q", isErr, text)
			}
			if got := readFile(t, filepath.Join(dir, "SKILL.md")); got != baseSkill {
				t.Error("rejected update modified the file")
			}
		})
	}
}

func TestNameTraversalRejected(t *testing.T) {
	for _, name := range []string{"../evil", "a/b", "..", "Upper", ""} {
		for _, action := range []string{"create", "view", "update", "patch", "write_file", "remove_file", "promote"} {
			tk, _ := env(t)
			text, isErr := mustExec(t, tk, map[string]any{"action": action, "name": name, "content": baseSkill,
				"old_string": "a", "new_string": "b", "file_path": "references/a.md"})
			if !isErr {
				t.Errorf("%s with name %q succeeded: %s", action, name, text)
			}
		}
	}
}

func TestPatch_String(t *testing.T) {
	tests := []struct {
		name    string
		in      map[string]any
		want    string // substring expected in the resulting file; "" = file must be unchanged
		wantErr string
	}{
		{"replace once", map[string]any{"old_string": "1. one", "new_string": "1. ONE"}, "1. ONE\n2. two", ""},
		{"delete via empty new_string", map[string]any{"old_string": "2. two\n", "new_string": ""}, "1. one\n\n### Detail", ""},
		{"ambiguous", map[string]any{"old_string": "\n", "new_string": " "}, "", "matches"},
		{"replace_all", map[string]any{"old_string": "## ", "new_string": "## X ", "replace_all": true}, "## X Steps", ""},
		{"not found", map[string]any{"old_string": "absent text", "new_string": "x"}, "", "not found"},
		{"not found with hint", map[string]any{"old_string": "1. one\nmissing line", "new_string": "x"}, "", "line \"1. one\" exists"},
		{"identical", map[string]any{"old_string": "1. one", "new_string": "1. one"}, "", "unchanged"},
		{"missing new_string", map[string]any{"old_string": "1. one"}, "", "new_string"},
		{"neither old_string nor section", map[string]any{"new_string": "x"}, "", "exactly one"},
		{"both old_string and section", map[string]any{"old_string": "a", "section": "Steps", "new_string": "x"}, "", "exactly one"},
		{"breaking frontmatter rejected", map[string]any{"old_string": "name: demo\n", "new_string": ""}, "", "nothing written"},
		{"empty body rejected", map[string]any{"old_string": baseSkill[strings.Index(baseSkill, "## When"):], "new_string": ""}, "", "nothing written"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tk, dir := env(t)
			mustCreate(t, tk)
			in := map[string]any{"action": "patch", "name": "demo"}
			for k, v := range tt.in {
				in[k] = v
			}
			text, isErr := mustExec(t, tk, in)
			got := readFile(t, filepath.Join(dir, "SKILL.md"))
			if tt.wantErr != "" {
				if !isErr || !strings.Contains(text, tt.wantErr) {
					t.Fatalf("isErr=%v text=%q, want error containing %q", isErr, text, tt.wantErr)
				}
				if got != baseSkill {
					t.Errorf("failed patch modified the file:\n%s", got)
				}
				return
			}
			if isErr {
				t.Fatalf("unexpected error: %s", text)
			}
			if !strings.Contains(got, tt.want) {
				t.Errorf("file missing %q:\n%s", tt.want, got)
			}
		})
	}
}

func TestPatch_Section(t *testing.T) {
	tests := []struct {
		name    string
		section string
		body    string
		want    string // full expected file; "" with wantErr
		wantErr string
	}{
		{"middle section replaces nested subsections", "Steps", "1. only", "## Steps\n1. only\n\n## Notes", ""},
		{"last section", "Notes", "new notes", "## Notes\nnew notes\n", ""},
		{"nested heading stops at next peer", "Detail", "deeper", "### Detail\ndeeper\n\n## Notes", ""},
		{"case insensitive and hashes tolerated", "## steps", "x", "## Steps\nx\n\n## Notes", ""},
		{"not found lists headings", "Missing", "x", "", "headings are: When to Use, Steps, Detail, Notes"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tk, dir := env(t)
			mustCreate(t, tk)
			text, isErr := mustExec(t, tk, map[string]any{"action": "patch", "name": "demo", "section": tt.section, "new_string": tt.body})
			got := readFile(t, filepath.Join(dir, "SKILL.md"))
			if tt.wantErr != "" {
				if !isErr || !strings.Contains(text, tt.wantErr) {
					t.Fatalf("isErr=%v text=%q", isErr, text)
				}
				return
			}
			if isErr {
				t.Fatalf("unexpected error: %s", text)
			}
			if !strings.Contains(got, tt.want) {
				t.Errorf("want %q in:\n%s", tt.want, got)
			}
			if !strings.HasPrefix(got, "---\nname: demo") {
				t.Error("frontmatter damaged")
			}
		})
	}
}

func TestPatchSection_IgnoresFencedHeadings(t *testing.T) {
	text := "## A\n```\n## B\n```\nbody\n## C\nc\n"
	got, err := patchSection(text, "A", "new")
	if err != nil {
		t.Fatal(err)
	}
	if got != "## A\nnew\n\n## C\nc\n" {
		t.Errorf("got %q", got)
	}
	if _, err := patchSection(text, "B", "x"); err == nil {
		t.Error("heading inside a fence must not be addressable")
	}
}

func TestPatchSection_AmbiguousHeading(t *testing.T) {
	if _, err := patchSection("## A\n1\n## A\n2\n", "A", "x"); err == nil || !strings.Contains(err.Error(), "ambiguous") {
		t.Errorf("err = %v", err)
	}
}

func TestPatch_ReferencesFile(t *testing.T) {
	tk, dir := env(t)
	mustCreate(t, tk)
	if text, isErr := mustExec(t, tk, map[string]any{"action": "write_file", "name": "demo", "file_path": "references/topic.md", "content": "alpha beta"}); isErr {
		t.Fatal(text)
	}
	if text, isErr := mustExec(t, tk, map[string]any{"action": "patch", "name": "demo", "file_path": "references/topic.md", "old_string": "beta", "new_string": "gamma"}); isErr {
		t.Fatal(text)
	}
	if got := readFile(t, filepath.Join(dir, "references", "topic.md")); got != "alpha gamma" {
		t.Errorf("got %q", got)
	}
	// Other paths cannot be patched.
	for _, fp := range []string{"../x.md", "scripts/a.sh", "/etc/passwd"} {
		if text, isErr := mustExec(t, tk, map[string]any{"action": "patch", "name": "demo", "file_path": fp, "old_string": "a", "new_string": "b"}); !isErr {
			t.Errorf("patch file_path %q accepted: %s", fp, text)
		}
	}
}

func TestWriteRemoveFile(t *testing.T) {
	tests := []struct {
		name    string
		action  string
		path    string
		content string
		wantErr string
	}{
		{"write ok", "write_file", "references/a.md", "x", ""},
		{"write nested ok", "write_file", "references/sub/a.md", "x", ""},
		{"write outside references", "write_file", "scripts/a.sh", "x", "references/"},
		{"write SKILL.md via write_file", "write_file", "SKILL.md", "x", "references/"},
		{"write traversal", "write_file", "references/../../x", "x", "relative"},
		{"write absolute", "write_file", "/tmp/x", "x", "relative"},
		{"write missing path", "write_file", "", "x", "file_path"},
		{"write empty content", "write_file", "references/a.md", "", "content"},
		{"write too big", "write_file", "references/a.md", strings.Repeat("x", 1<<20+1), "limit"},
		{"remove missing", "remove_file", "references/none.md", "", "not an existing file"},
		{"remove outside references", "remove_file", "SKILL.md", "", "references/"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tk, dir := env(t)
			mustCreate(t, tk)
			text, isErr := mustExec(t, tk, map[string]any{"action": tt.action, "name": "demo", "file_path": tt.path, "content": tt.content})
			if tt.wantErr != "" {
				if !isErr || !strings.Contains(text, tt.wantErr) {
					t.Fatalf("isErr=%v text=%q", isErr, text)
				}
				if got := readFile(t, filepath.Join(dir, "SKILL.md")); got != baseSkill {
					t.Error("SKILL.md modified by rejected call")
				}
				return
			}
			if isErr {
				t.Fatal(text)
			}
			if got := readFile(t, filepath.Join(dir, filepath.FromSlash(tt.path))); got != tt.content {
				t.Errorf("content = %q", got)
			}
		})
	}
	t.Run("remove existing", func(t *testing.T) {
		tk, dir := env(t)
		mustCreate(t, tk)
		mustExec(t, tk, map[string]any{"action": "write_file", "name": "demo", "file_path": "references/a.md", "content": "x"})
		if text, isErr := mustExec(t, tk, map[string]any{"action": "remove_file", "name": "demo", "file_path": "references/a.md"}); isErr {
			t.Fatal(text)
		}
		if _, err := os.Stat(filepath.Join(dir, "references", "a.md")); !os.IsNotExist(err) {
			t.Error("file still exists")
		}
	})
	t.Run("unknown skill", func(t *testing.T) {
		tk, _ := env(t)
		if _, isErr := mustExec(t, tk, map[string]any{"action": "write_file", "name": "ghost", "file_path": "references/a.md", "content": "x"}); !isErr {
			t.Error("write_file to a missing skill must fail")
		}
	})
}

func TestLintWarningsAreReportedWithoutBlocking(t *testing.T) {
	tk, dir := env(t)
	noSection := "---\nname: demo\ndescription: A powerful tool.\n---\nSee references/gone.md for more.\n"
	text, isErr := mustExec(t, tk, map[string]any{"action": "create", "name": "demo", "content": noSection})
	if isErr {
		t.Fatalf("lint must not block: %s", text)
	}
	for _, rule := range []string{"missing-section", "description-marketing", "dangling-reference"} {
		if !strings.Contains(text, rule) {
			t.Errorf("missing warning %s in %q", rule, text)
		}
	}
	if _, err := os.Stat(filepath.Join(dir, "SKILL.md")); err != nil {
		t.Fatal(err)
	}
	// Writing the referenced file resolves the dangling warning and a later
	// patch does not re-report standing findings.
	mustExec(t, tk, map[string]any{"action": "write_file", "name": "demo", "file_path": "references/gone.md", "content": "x"})
	text, _ = mustExec(t, tk, map[string]any{"action": "patch", "name": "demo", "old_string": "more", "new_string": "details"})
	if strings.Contains(text, "missing-section") || strings.Contains(text, "dangling-reference") {
		t.Errorf("standing findings re-reported: %q", text)
	}
}

func TestLedger_ActorsAndRollbackViaTool(t *testing.T) {
	tests := []struct {
		name  string
		opts  []Option
		ctx   context.Context
		actor string
	}{
		{"user default", nil, context.Background(), skillusage.ActorUser},
		{"agent provenance", []Option{WithAgentProvenance()}, context.Background(), skillusage.ActorAgent},
		{"explicit option", []Option{WithActor("custom")}, context.Background(), "custom"},
		{"context overrides", []Option{WithAgentProvenance()}, skillusage.WithActor(context.Background(), skillusage.ActorCurator), skillusage.ActorCurator},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tk, dir := env(t, tt.opts...)
			run := func(in map[string]any) (string, bool) {
				raw, _ := json.Marshal(in)
				res, err := tk.Execute(tt.ctx, raw)
				if err != nil {
					t.Fatal(err)
				}
				var sb strings.Builder
				for _, b := range res.Content {
					sb.WriteString(b.Text)
				}
				return sb.String(), res.IsError
			}
			run(map[string]any{"action": "create", "name": "demo", "content": baseSkill})
			run(map[string]any{"action": "patch", "name": "demo", "old_string": "1. one", "new_string": "1. ONE"})

			entries := skillusage.LedgerList("demo", 0)
			if len(entries) != 2 || entries[0].Op != skillusage.OpPatch || entries[1].Op != skillusage.OpCreate {
				t.Fatalf("ledger = %+v", entries)
			}
			for _, e := range entries {
				if e.Actor != tt.actor {
					t.Errorf("actor = %q, want %q", e.Actor, tt.actor)
				}
			}
			hist, _ := run(map[string]any{"action": "history", "name": "demo"})
			if !strings.Contains(hist, entries[0].ID) || !strings.Contains(hist, "[reversible]") {
				t.Errorf("history = %q", hist)
			}
			if text, isErr := run(map[string]any{"action": "rollback", "id": entries[0].ID}); isErr {
				t.Fatalf("rollback: %s", text)
			}
			if got := readFile(t, filepath.Join(dir, "SKILL.md")); got != baseSkill {
				t.Errorf("rollback did not restore the file:\n%s", got)
			}
		})
	}
}

func TestLedger_AllWriteActionsRecorded(t *testing.T) {
	tk, _ := env(t, WithAgentProvenance())
	steps := []map[string]any{
		{"action": "create", "name": "demo", "content": baseSkill},
		{"action": "update", "name": "demo", "content": strings.Replace(baseSkill, "Always.", "Often.", 1)},
		{"action": "patch", "name": "demo", "old_string": "Often.", "new_string": "Sometimes."},
		{"action": "write_file", "name": "demo", "file_path": "references/a.md", "content": "x"},
		{"action": "remove_file", "name": "demo", "file_path": "references/a.md"},
		{"action": "promote", "name": "demo"},
	}
	for _, s := range steps {
		if text, isErr := mustExec(t, tk, s); isErr {
			t.Fatalf("%v: %s", s["action"], text)
		}
	}
	entries := skillusage.LedgerList("demo", 0)
	ops := make([]string, 0, len(entries))
	for _, e := range entries {
		ops = append(ops, e.Op)
	}
	want := []string{"promote", "remove_file", "write_file", "patch", "update", "create"}
	if strings.Join(ops, ",") != strings.Join(want, ",") {
		t.Errorf("ops = %v, want %v", ops, want)
	}
}

func TestRollback_Errors(t *testing.T) {
	tk, _ := env(t)
	if text, isErr := mustExec(t, tk, map[string]any{"action": "rollback"}); !isErr || !strings.Contains(text, "id") {
		t.Errorf("missing id: %q", text)
	}
	if _, isErr := mustExec(t, tk, map[string]any{"action": "rollback", "id": "nope"}); !isErr {
		t.Error("unknown id must fail")
	}
	if text, _ := mustExec(t, tk, map[string]any{"action": "history"}); !strings.Contains(text, "No recorded") {
		t.Errorf("empty history = %q", text)
	}
}

func TestReadOnlyActions(t *testing.T) {
	tk := New("")
	for action, want := range map[string]bool{"list": true, "view": true, "history": true, "create": false, "patch": false, "rollback": false, "write_file": false} {
		raw := json.RawMessage(`{"action":"` + action + `"}`)
		if got := tk.IsReadOnly(raw); got != want {
			t.Errorf("IsReadOnly(%s) = %v", action, got)
		}
		if got := tk.IsConcurrencySafe(raw); got != want {
			t.Errorf("IsConcurrencySafe(%s) = %v", action, got)
		}
	}
}

func TestList_ShowsFrontmatterDescription(t *testing.T) {
	tk, _ := env(t)
	mustCreate(t, tk)
	text, _ := mustExec(t, tk, map[string]any{"action": "list"})
	if !strings.Contains(text, "demo — Use when demoing.") {
		t.Errorf("list = %q", text)
	}
}

func TestUpdate_FindsSkillInOtherScope(t *testing.T) {
	tk, _ := env(t)
	mustExec(t, tk, map[string]any{"action": "create", "name": "demo", "content": baseSkill, "scope": "global-conduit"})
	text, isErr := mustExec(t, tk, map[string]any{"action": "update", "name": "demo", "content": strings.Replace(baseSkill, "Always.", "Never.", 1)})
	if isErr {
		t.Fatal(text)
	}
	got, _ := mustExec(t, tk, map[string]any{"action": "view", "name": "demo"})
	if !strings.Contains(got, "Never.") {
		t.Errorf("view = %q", got)
	}
}

func TestMutatingActionsScopeResolution(t *testing.T) {
	mutations := map[string]map[string]any{
		"update":      {"action": "update", "name": "demo", "content": strings.Replace(baseSkill, "Always.", "Never.", 1)},
		"patch":       {"action": "patch", "name": "demo", "old_string": "Always.", "new_string": "Never."},
		"write_file":  {"action": "write_file", "name": "demo", "file_path": "references/a.md", "content": "x"},
		"remove_file": {"action": "remove_file", "name": "demo", "file_path": "references/a.md"},
	}
	for action, in := range mutations {
		t.Run(action+"/global needs explicit scope", func(t *testing.T) {
			tk, _ := env(t)
			t.Setenv("CLAUDE_CONFIG_DIR", t.TempDir())
			mustExec(t, tk, map[string]any{"action": "create", "name": "demo", "content": baseSkill, "scope": "global"})
			text, isErr := mustExec(t, tk, in)
			if !isErr || !strings.Contains(text, "scope=\"global\"") {
				t.Fatalf("implicit scope reached ~/.claude/skills: isErr=%v %q", isErr, text)
			}
			withScope := map[string]any{"scope": "global"}
			for k, v := range in {
				withScope[k] = v
			}
			if action == "remove_file" {
				mustExec(t, tk, map[string]any{"action": "write_file", "name": "demo", "file_path": "references/a.md", "content": "x", "scope": "global"})
			}
			if text, isErr := mustExec(t, tk, withScope); isErr {
				t.Fatalf("explicit global scope failed: %s", text)
			}
		})
		t.Run(action+"/ambiguous across scopes", func(t *testing.T) {
			tk, projDir := env(t)
			mustCreate(t, tk)
			mustExec(t, tk, map[string]any{"action": "create", "name": "demo", "content": baseSkill, "scope": "global-conduit"})
			text, isErr := mustExec(t, tk, in)
			if !isErr || !strings.Contains(text, "several scopes") {
				t.Fatalf("isErr=%v %q", isErr, text)
			}
			if got := readFile(t, filepath.Join(projDir, "SKILL.md")); got != baseSkill {
				t.Error("ambiguous mutation modified a file")
			}
			in2 := map[string]any{"scope": "project"}
			for k, v := range in {
				in2[k] = v
			}
			if action == "remove_file" {
				mustExec(t, tk, map[string]any{"action": "write_file", "name": "demo", "file_path": "references/a.md", "content": "x", "scope": "project"})
			}
			if text, isErr := mustExec(t, tk, in2); isErr {
				t.Fatalf("explicit scope should disambiguate: %s", text)
			}
		})
	}
}
