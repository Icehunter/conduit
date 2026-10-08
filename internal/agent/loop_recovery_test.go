package agent

import (
	"strings"
	"testing"

	"github.com/icehunter/conduit/internal/api"
)

// TestBuildContentBlocks_TextAndToolUse covers the happy path —
// a text block plus a fully-streamed tool_use produce two blocks.
func TestBuildContentBlocks_TextAndToolUse(t *testing.T) {
	metas := map[int]blockMeta{
		0: {blockType: "text"},
		1: {blockType: "tool_use", toolID: "toolu_01", toolName: "Bash"},
	}
	texts := map[int]*strings.Builder{
		0: bld("hello"),
		1: bld(`{"cmd":"ls"}`),
	}

	got := buildContentBlocks(metas, texts, map[int]string{})
	if len(got) != 2 {
		t.Fatalf("expected 2 blocks; got %d", len(got))
	}
	if got[0].Type != "text" || got[0].Text != "hello" {
		t.Errorf("blocks[0] = %+v; want text 'hello'", got[0])
	}
	if got[1].Type != "tool_use" || got[1].ID != "toolu_01" || got[1].Name != "Bash" {
		t.Errorf("blocks[1] = %+v; want tool_use toolu_01 Bash", got[1])
	}
	if got[1].Input["cmd"] != "ls" {
		t.Errorf("blocks[1].Input = %+v; want {cmd: ls}", got[1].Input)
	}
}

// TestBuildContentBlocks_DropsEmptyText — partial recovery skips blocks
// whose text is empty (would fail validation on resume).
func TestBuildContentBlocks_DropsEmptyText(t *testing.T) {
	metas := map[int]blockMeta{
		0: {blockType: "text"},
		1: {blockType: "text"},
	}
	texts := map[int]*strings.Builder{
		0: bld(""),
		1: bld("kept"),
	}

	got := buildContentBlocks(metas, texts, map[int]string{})
	if len(got) != 1 {
		t.Fatalf("expected 1 block (empty dropped); got %d", len(got))
	}
	if got[0].Text != "kept" {
		t.Errorf("got %q; want 'kept'", got[0].Text)
	}
}

// TestBuildBlocks_KeepsInvalidToolUse — a tool_use whose JSON input was cut
// off or is not an object is kept with an empty input and reported as invalid,
// so the loop can answer it with an error result instead of losing the call
// and leaving its UI row running forever.
func TestBuildBlocks_KeepsInvalidToolUse(t *testing.T) {
	tests := []struct {
		name  string
		input string
		valid bool
	}{
		{"truncated mid-string", `{"cmd":"l`, false},
		{"truncated mid-object", `{"cmd":"ls",`, false},
		{"not json", `ls -la`, false},
		{"array not object", `["ls"]`, false},
		{"complete", `{"cmd":"ls"}`, true},
		{"empty means no args", ``, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			metas := map[int]blockMeta{
				0: {blockType: "text"},
				1: {blockType: "tool_use", toolID: "toolu_bad", toolName: "Bash"},
			}
			texts := map[int]*strings.Builder{0: bld("before"), 1: bld(tt.input)}

			blocks, invalid := buildBlocks(metas, texts, map[int]string{})
			if len(blocks) != 2 || blocks[1].Type != "tool_use" || blocks[1].ID != "toolu_bad" {
				t.Fatalf("blocks = %+v; want the text and the tool_use kept", blocks)
			}
			if blocks[1].Input == nil {
				t.Errorf("Input is nil; want a non-nil map so the block marshals as {}")
			}
			if tt.valid {
				if len(invalid) != 0 {
					t.Errorf("invalid = %v; want none", invalid)
				}
				return
			}
			if len(blocks[1].Input) != 0 {
				t.Errorf("Input = %v; want empty for an invalid call", blocks[1].Input)
			}
			if len(invalid) != 1 || invalid[0] != "toolu_bad" {
				t.Errorf("invalid = %v; want [toolu_bad]", invalid)
			}
		})
	}
}

// TestBuildContentBlocks_PreservesOrder — block index ordering is
// preserved so text streamed before a tool_use ends up before it.
func TestBuildContentBlocks_PreservesOrder(t *testing.T) {
	metas := map[int]blockMeta{
		2: {blockType: "text"},
		0: {blockType: "text"},
		1: {blockType: "tool_use", toolID: "t", toolName: "Bash"},
	}
	texts := map[int]*strings.Builder{
		0: bld("first"),
		1: bld("{}"),
		2: bld("third"),
	}

	got := buildContentBlocks(metas, texts, map[int]string{})
	if len(got) != 3 {
		t.Fatalf("expected 3 blocks; got %d", len(got))
	}
	if got[0].Text != "first" || got[2].Text != "third" {
		t.Errorf("order broken: %v", got)
	}
}

// TestBuildContentBlocks_EmptyToolUseInput — a tool_use with no JSON
// streamed yet (only content_block_start, no deltas) should still emit
// with an empty {} input rather than be dropped, since "{}" parses fine.
func TestBuildContentBlocks_EmptyToolUseInput(t *testing.T) {
	metas := map[int]blockMeta{
		0: {blockType: "tool_use", toolID: "toolu", toolName: "Bash"},
	}
	texts := map[int]*strings.Builder{
		0: bld(""), // no deltas yet — buildContentBlocks defaults to "{}"
	}

	got := buildContentBlocks(metas, texts, map[int]string{})
	if len(got) != 1 {
		t.Fatalf("expected 1 block; got %d", len(got))
	}
	if got[0].Type != "tool_use" {
		t.Errorf("type = %q; want tool_use", got[0].Type)
	}
}

func bld(s string) *strings.Builder {
	b := &strings.Builder{}
	b.WriteString(s)
	return b
}

// TestBuildContentBlocks_ThinkingBlocks — a signed thinking block is preserved
// even with no text (Fable emits these), while a block with neither text nor
// signature is dropped: it carries nothing the API can replay.
func TestBuildContentBlocks_ThinkingBlocks(t *testing.T) {
	metas := map[int]blockMeta{
		0: {blockType: "thinking"},
		1: {blockType: "thinking"},
		2: {blockType: "text"},
	}
	texts := map[int]*strings.Builder{
		0: bld(""),
		1: bld(""),
		2: bld("answer"),
	}
	sigs := map[int]string{0: "CAQSkBUKEAgR"}

	got := buildContentBlocks(metas, texts, sigs)
	if len(got) != 2 {
		t.Fatalf("expected 2 blocks (unsigned empty thinking dropped); got %d: %+v", len(got), got)
	}
	if got[0].Type != "thinking" || got[0].Signature != "CAQSkBUKEAgR" || got[0].Thinking != "" {
		t.Errorf("blocks[0] = %+v; want empty signed thinking block", got[0])
	}
	if got[1].Type != "text" {
		t.Errorf("blocks[1] = %+v; want text", got[1])
	}
}

// buildContentBlocks is buildBlocks without the invalid-tool list.
func buildContentBlocks(metas map[int]blockMeta, blockTexts map[int]*strings.Builder, blockSignatures map[int]string) []api.ContentBlock {
	blocks, _ := buildBlocks(metas, blockTexts, blockSignatures)
	return blocks
}
