package agent

import (
	"strings"
	"testing"
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

	got := buildContentBlocks(metas, texts)
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

	got := buildContentBlocks(metas, texts)
	if len(got) != 1 {
		t.Fatalf("expected 1 block (empty dropped); got %d", len(got))
	}
	if got[0].Text != "kept" {
		t.Errorf("got %q; want 'kept'", got[0].Text)
	}
}

// TestBuildContentBlocks_DropsTruncatedToolUse — a tool_use whose JSON
// input was cut off mid-stream is unparseable; we drop it rather than
// emit a half-formed call that the resume path would have to filter.
func TestBuildContentBlocks_DropsTruncatedToolUse(t *testing.T) {
	metas := map[int]blockMeta{
		0: {blockType: "tool_use", toolID: "toolu_bad", toolName: "Bash"},
	}
	texts := map[int]*strings.Builder{
		0: bld(`{"cmd":"l`), // truncated mid-string
	}

	got := buildContentBlocks(metas, texts)
	if len(got) != 0 {
		t.Fatalf("expected 0 blocks (truncated dropped); got %d (%+v)", len(got), got)
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

	got := buildContentBlocks(metas, texts)
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

	got := buildContentBlocks(metas, texts)
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
