package api

import (
	"strings"
	"testing"
)

// splitAll feeds chunks through a fresh splitter and returns the merged
// segments, rendered as "T:<thinking>" and "V:<visible>".
func splitAll(chunks ...string) []string {
	return splitAllWith(thinkSplitter{}, chunks...)
}

func splitAllWith(s thinkSplitter, chunks ...string) []string {
	segs := make([]thinkSegment, 0, len(chunks)+1)
	for _, c := range chunks {
		segs = append(segs, s.feed(c)...)
	}
	segs = append(segs, s.flush()...)
	return renderSegments(segs)
}

func renderSegments(segs []thinkSegment) []string {
	var out []string
	for _, seg := range segs {
		prefix := "V:"
		if seg.thinking {
			prefix = "T:"
		}
		if n := len(out); n > 0 && strings.HasPrefix(out[n-1], prefix) {
			out[n-1] += seg.text
			continue
		}
		out = append(out, prefix+seg.text)
	}
	return out
}

func TestThinkSplitter(t *testing.T) {
	tests := []struct {
		name   string
		chunks []string
		want   []string
	}{
		{"no tags", []string{"plain answer"}, []string{"V:plain answer"}},
		{"leading block", []string{"<think>reason</think>answer"}, []string{"T:reason", "V:answer"}},
		{"whitespace around block dropped", []string{"\n<think>\n  reason\n\n</think>\n\nanswer\n"}, []string{"T:reason", "V:answer\n"}},
		{"empty block (thinking disabled)", []string{"<think>\n\n</think>\n\nanswer"}, []string{"V:answer"}},
		{"tag split across chunks", []string{"<th", "ink>rea", "son</thi", "nk>ans", "wer"}, []string{"T:reason", "V:answer"}},
		{"char by char", strings.Split("<think>ab</think>cd", ""), []string{"T:ab", "V:cd"}},
		{"tag in prose stays visible", []string{"Before. <think>r</think> after"}, []string{"V:Before. <think>r</think> after"}},
		{"prose tag does not swallow the answer", []string{"The parser turns <think> into a thinking block. ", "Here is the rest."}, []string{"V:The parser turns <think> into a thinking block. Here is the rest."}},
		{"prose tag split across chunks", []string{"see <thi", "nk> ok"}, []string{"V:see <think> ok"}},
		{"leading whitespace then block", []string{"  \n<think>r</think>a"}, []string{"T:r", "V:a"}},
		{"inline code span keeps tag visible", []string{"use `<think>` tags"}, []string{"V:use `<think>` tags"}},
		{"double-backtick span", []string{"``a <think> b``<think>r</think>x"}, []string{"V:``a <think> b``<think>r</think>x"}},
		{"fenced code keeps tag visible", []string{"```\n<think>\n```\n<think>r</think>done"}, []string{"V:```\n<think>\n```\n<think>r</think>done"}},
		{"fence split across chunks", []string{"``", "`go\n<thi", "nk>\n`", "``\nok"}, []string{"V:```go\n<think>\n```\nok"}},
		{"indented fence opener is inline", []string{"x\n    ```\n<think>r</think>"}, []string{"V:x\n    ```\n<think>r</think>"}},
		{"unclosed block ends with input", []string{"<think>still going"}, []string{"T:still going"}},
		{"bare close stays visible", []string{"a</think>b"}, []string{"V:a</think>b"}},
		{"partial tag at end flushed as text", []string{"x <thi"}, []string{"V:x <thi"}},
		{"partial close at end is reasoning", []string{"<think>r </th"}, []string{"T:r </th"}},
		{"whitespace-only input kept", []string{"  \n"}, []string{"V:  \n"}},
		{"leading whitespace kept without a block", []string{"  ", "hi"}, []string{"V:  hi"}},
		{"nested open is reasoning text", []string{"<think>a<think>b</think>c"}, []string{"T:a<think>b", "V:c"}},
		{"interleaved blocks", []string{"<think>a</think>\n<think>b</think>y"}, []string{"T:ab", "V:y"}},
		{"tag after answer text is prose", []string{"<think>a</think>x<think>b</think>y"}, []string{"T:a", "V:x<think>b</think>y"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := splitAll(tt.chunks...)
			if strings.Join(got, "|") != strings.Join(tt.want, "|") {
				t.Errorf("segments = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestThinkSplitterImplicit(t *testing.T) {
	tests := []struct {
		name   string
		chunks []string
		want   []string
	}{
		{"close with no opener", []string{"reasoning here</think>answer"}, []string{"T:reasoning here", "V:answer"}},
		{"streamed", []string{"reason", "ing</th", "ink>\n\nans", "wer"}, []string{"T:reasoning", "V:answer"}},
		{"explicit open still works", []string{"<think>r</think>a"}, []string{"T:r", "V:a"}},
		{"no tags at all flushes as visible", []string{"plain ", "answer"}, []string{"V:plain answer"}},
		{"close after an opener-less answer is not retroactive", []string{"hello <think>x</think>y"}, []string{"V:hello <think>x</think>y"}},
		{"second close stays visible", []string{"r</think>a</think>b"}, []string{"T:r", "V:a</think>b"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := splitAllWith(thinkSplitter{implicit: true}, tt.chunks...)
			if strings.Join(got, "|") != strings.Join(tt.want, "|") {
				t.Errorf("segments = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestThinkSplitterImplicitHoldLimit(t *testing.T) {
	long := strings.Repeat("x", implicitHoldLimit+1)
	got := splitAllWith(thinkSplitter{implicit: true}, long, "</think>y")
	if len(got) != 1 || !strings.HasPrefix(got[0], "V:x") || !strings.HasSuffix(got[0], "</think>y") {
		t.Errorf("past the hold limit output must stay visible, got %d segments", len(got))
	}
}

// FuzzThinkSplitter checks that the output does not depend on where the input
// is cut, that input without an opening tag passes through unchanged, and
// that reasoning never contains a closing tag.
func FuzzThinkSplitter(f *testing.F) {
	for _, seed := range []string{
		"<think>reason</think>answer",
		"use `<think>` and ```\n<think>\n```\n<think>r</think>x",
		"\n<think>\n\n</think>\n\nanswer",
		"a</think>b<th",
		"``a <think> b``<think>r",
		"<think>a<think>b</think>  c",
		"prose <think> mentions it</think> still prose",
		"reasoning without opener</think>answer",
	} {
		f.Add(seed, []byte{1, 3, 2, 7}, false)
		f.Add(seed, []byte{1, 3, 2, 7}, true)
	}
	f.Fuzz(func(t *testing.T, input string, cuts []byte, implicit bool) {
		run := func(chunks ...string) []string {
			return splitAllWith(thinkSplitter{implicit: implicit}, chunks...)
		}
		whole := run(input)

		var chunks []string
		rest := input
		for i := 0; len(rest) > 0; i++ {
			n := len(rest)
			if len(cuts) > 0 {
				n = min(n, int(cuts[i%len(cuts)]%8)+1)
			}
			chunks = append(chunks, rest[:n])
			rest = rest[n:]
		}
		if got := run(chunks...); strings.Join(got, "\x00") != strings.Join(whole, "\x00") {
			t.Fatalf("chunked %q = %q, whole = %q", chunks, got, whole)
		}

		if !strings.Contains(input, thinkOpenTag) && !(implicit && strings.Contains(input, thinkCloseTag)) {
			want := []string{"V:" + input}
			if input == "" {
				want = nil
			}
			if strings.Join(whole, "\x00") != strings.Join(want, "\x00") {
				t.Fatalf("input without %s changed: %q -> %q", thinkOpenTag, input, whole)
			}
		}
		total := 0
		for _, seg := range whole {
			total += len(seg) - 2
			if strings.HasPrefix(seg, "T:") && strings.Contains(seg, thinkCloseTag) {
				t.Fatalf("reasoning holds a closing tag: %q", seg)
			}
		}
		if total > len(input) {
			t.Fatalf("output (%d bytes) longer than input (%d)", total, len(input))
		}
	})
}
