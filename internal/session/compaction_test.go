package session

import (
	"os"
	"strings"
	"testing"

	"github.com/icehunter/conduit/internal/api"
)

func textMsg(role, text string) api.Message {
	return api.Message{Role: role, Content: []api.ContentBlock{{Type: "text", Text: text}}}
}

func msgTexts(msgs []api.Message) string {
	out := make([]string, 0, len(msgs))
	for _, m := range msgs {
		out = append(out, m.Content[0].Text)
	}
	return strings.Join(out, ",")
}

func TestAppendCompaction_Boundaries(t *testing.T) {
	tests := []struct {
		name        string
		compactions [][]string
		before      []string
		after       []string
		want        string
	}{
		{"no boundary", nil, []string{"a", "b", "c"}, nil, "a,b,c"},
		{"one boundary", [][]string{{"S1", "tail1"}}, []string{"a", "b", "c"}, []string{"d"}, "S1,tail1,d"},
		{"two boundaries", [][]string{{"S1", "t1"}, {"S2", "t2"}}, []string{"a", "b"}, []string{"e"}, "S2,t2,e"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := newTestSession(t)
			for _, x := range tt.before {
				if err := s.AppendMessage(textMsg("user", x)); err != nil {
					t.Fatal(err)
				}
			}
			for _, c := range tt.compactions {
				var hist []api.Message
				for _, x := range c {
					hist = append(hist, textMsg("user", x))
				}
				if err := s.AppendCompaction("sum", hist, 0); err != nil {
					t.Fatal(err)
				}
			}
			for _, x := range tt.after {
				if err := s.AppendMessage(textMsg("user", x)); err != nil {
					t.Fatal(err)
				}
			}
			got, err := s.LoadMessages()
			if err != nil {
				t.Fatal(err)
			}
			if g := msgTexts(got); g != tt.want {
				t.Fatalf("loaded %s, want %s", g, tt.want)
			}
			data, _ := os.ReadFile(s.FilePath)
			for _, x := range tt.before {
				if !strings.Contains(string(data), `"text":"`+x+`"`) {
					t.Errorf("older message %q was removed from disk", x)
				}
			}
		})
	}
}

func TestLoadMessages_UUIDChainAroundBoundary(t *testing.T) {
	tests := []struct {
		name  string
		lines []string
		want  string
	}{
		{"chain before boundary ignored", []string{
			`{"type":"user","uuid":"u1","message":{"role":"user","content":"old1"}}`,
			`{"type":"assistant","uuid":"u2","parentUuid":"u1","message":{"role":"assistant","content":[{"type":"text","text":"old2"}]}}`,
			`{"type":"summary","summary":"S"}`,
			`{"type":"compact_boundary"}`,
			`{"type":"message","message":{"role":"user","content":"new1"}}`,
		}, "new1"},
		{"chain after boundary does not reach back", []string{
			`{"type":"user","uuid":"u1","message":{"role":"user","content":"old1"}}`,
			`{"type":"compact_boundary"}`,
			`{"type":"user","uuid":"u2","parentUuid":"u1","message":{"role":"user","content":"new1"}}`,
			`{"type":"assistant","uuid":"u3","parentUuid":"u2","message":{"role":"assistant","content":[{"type":"text","text":"new2"}]}}`,
		}, "new1,new2"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			path := t.TempDir() + "/s.jsonl"
			writeJSONL(t, path, tt.lines...)
			got, err := LoadMessages(path)
			if err != nil {
				t.Fatal(err)
			}
			if g := msgTexts(got); g != tt.want {
				t.Fatalf("loaded %q, want %q", g, tt.want)
			}
		})
	}
}
