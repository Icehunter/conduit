package permissions

import "testing"

func TestProtectedWrite_AstGrepAndSkillManage(t *testing.T) {
	cwd := t.TempDir()
	tests := []struct {
		tool  string
		input string
		want  bool
	}{
		{"AstGrep", ".git/hooks/pre-commit", true},
		{"AstGrep", "src/a.go", false},
		{"AstGrep", "", false}, // read-only search carries no write target
		{"SkillManage", ".vscode/skills/x/SKILL.md", true},
		{"SkillManage", ".claude/skills/x/SKILL.md", false},
		{"SkillManage", "", false},
	}
	for _, tt := range tests {
		t.Run(tt.tool+"/"+tt.input, func(t *testing.T) {
			g := New(cwd, nil, ModeBypassPermissions, nil, nil, nil)
			if got := g.IsProtectedWrite(tt.tool, tt.input); got != tt.want {
				t.Fatalf("IsProtectedWrite = %v, want %v", got, tt.want)
			}
			g = New(cwd, nil, ModeAcceptEdits, []string{tt.tool}, nil, nil)
			got := g.Check(tt.tool, tt.input)
			if tt.want && got != DecisionAsk {
				t.Fatalf("Check on protected path = %v, want Ask even with an allow rule", got)
			}
		})
	}
}
