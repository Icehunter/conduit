package pendingedits

import (
	"strings"
	"testing"
)

func TestDiff_Identical(t *testing.T) {
	got := Diff([]byte("a\nb\nc\n"), []byte("a\nb\nc\n"))
	if len(got) != 3 {
		t.Fatalf("got %d lines, want 3", len(got))
	}
	for _, ln := range got {
		if ln.Op != DiffEqual {
			t.Errorf("expected all DiffEqual, got %v for %q", ln.Op, ln.Text)
		}
	}
}

func TestDiff_PureInsertion(t *testing.T) {
	got := Diff(nil, []byte("x\ny\n"))
	if len(got) != 2 {
		t.Fatalf("got %d lines, want 2", len(got))
	}
	for i, ln := range got {
		if ln.Op != DiffInsert {
			t.Errorf("line %d Op = %v, want DiffInsert", i, ln.Op)
		}
		if ln.NewLine != i+1 {
			t.Errorf("line %d NewLine = %d, want %d", i, ln.NewLine, i+1)
		}
	}
}

func TestDiff_PureDeletion(t *testing.T) {
	got := Diff([]byte("x\ny\n"), nil)
	if len(got) != 2 {
		t.Fatalf("got %d lines, want 2", len(got))
	}
	for _, ln := range got {
		if ln.Op != DiffDelete {
			t.Errorf("Op = %v, want DiffDelete", ln.Op)
		}
	}
}

func TestDiff_MidFileChange(t *testing.T) {
	orig := []byte("a\nb\nc\nd\n")
	updated := []byte("a\nB\nc\nd\n")
	got := Diff(orig, updated)
	// Expect: equal a, delete b, insert B, equal c, equal d.
	want := []DiffOp{DiffEqual, DiffDelete, DiffInsert, DiffEqual, DiffEqual}
	if len(got) != len(want) {
		t.Fatalf("got %d ops, want %d (got=%+v)", len(got), len(want), opSummary(got))
	}
	for i, ln := range got {
		if ln.Op != want[i] {
			t.Errorf("op[%d] = %v, want %v (text %q)", i, ln.Op, want[i], ln.Text)
		}
	}
}

func TestDiffString_FormatsUnifiedPrefixes(t *testing.T) {
	out := DiffString([]byte("a\nb\n"), []byte("a\nB\n"))
	// Order should be " a", "-b", "+B".
	lines := strings.Split(strings.TrimRight(out, "\n"), "\n")
	if len(lines) != 3 {
		t.Fatalf("DiffString lines = %d, want 3:\n%s", len(lines), out)
	}
	if lines[0] != " a" {
		t.Errorf("lines[0] = %q, want \" a\"", lines[0])
	}
	if lines[1] != "-b" {
		t.Errorf("lines[1] = %q, want \"-b\"", lines[1])
	}
	if lines[2] != "+B" {
		t.Errorf("lines[2] = %q, want \"+B\"", lines[2])
	}
}

func TestDiff_LargeFileFallback(t *testing.T) {
	var sb strings.Builder
	for i := 0; i <= MaxDiffLines; i++ {
		sb.WriteString("x\n")
	}
	got := Diff([]byte(sb.String()), nil)
	if len(got) != 1 {
		t.Fatalf("expected 1 placeholder line, got %d", len(got))
	}
	if !strings.Contains(got[0].Text, "too large") {
		t.Errorf("placeholder text: %q", got[0].Text)
	}
}

func TestDiff_EmptyBothSides(t *testing.T) {
	if got := Diff(nil, nil); len(got) != 0 {
		t.Errorf("empty/empty: %d lines, want 0", len(got))
	}
}

func TestDiff_TrailingNewlineNotCountedAsExtraLine(t *testing.T) {
	// "a\n" should be one line, not two.
	got := Diff([]byte("a\n"), []byte("a\n"))
	if len(got) != 1 {
		t.Errorf("trailing newline produced %d lines, want 1", len(got))
	}
}

func opSummary(lines []DiffLine) []string {
	out := make([]string, len(lines))
	for i, ln := range lines {
		var prefix byte
		switch ln.Op {
		case DiffInsert:
			prefix = '+'
		case DiffDelete:
			prefix = '-'
		default:
			prefix = ' '
		}
		out[i] = string(prefix) + ln.Text
	}
	return out
}
