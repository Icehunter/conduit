package agent

import (
	"errors"
	"strings"

	"github.com/icehunter/conduit/internal/api"
)

// maxPrefixLockPartialStrips mirrors CC 2.1.266's Afs=2: the first two
// prefix-lock rejections strip from the block the server pointed at; after
// that (or when the header carries no block) every thinking block is stripped.
const maxPrefixLockPartialStrips = 2

// maxPrefixLockRecoveries bounds retries per turn. Two partial strips plus one
// full strip is the most CC ever does before surfacing the error.
const maxPrefixLockRecoveries = maxPrefixLockPartialStrips + 1

// thinkingRemovedPlaceholder replaces the content of an assistant message
// whose only blocks were thinking, so the role alternation stays valid.
const thinkingRemovedPlaceholder = "[Thinking removed]"

// prefixLockRejection returns the typed error when err is the API's strict
// prefix-lock 400 (a replayed thinking signature bound to another
// conversation), or nil for any other error.
func prefixLockRejection(err error) *api.APIStatusError {
	var se *api.APIStatusError
	if errors.As(err, &se) && se.IsPrefixLockRejection() {
		return se
	}
	return nil
}

// healPrefixLock returns a copy of msgs with thinking blocks stripped so the
// next request can pass the strict prefix lock. attempt is 1-based. When the
// server named a block and the partial budget isn't spent, thinking is
// stripped from that block onward; otherwise all thinking is stripped. scope
// is "partial" or "all"; changed is false when nothing could be stripped, in
// which case the caller should surface the error.
func healPrefixLock(msgs []api.Message, mm *api.ThinkingMismatch, attempt int) (out []api.Message, scope string, changed bool) {
	if mm != nil && mm.HasBlock && attempt <= maxPrefixLockPartialStrips {
		if ord, ok := thinkingOrdinal(msgs, mm.MessageIndex, mm.BlockIndex); ok {
			if out, changed = stripThinkingFrom(msgs, mm.MessageIndex, ord); changed {
				return out, "partial", true
			}
		}
	}
	out, changed = stripThinkingFrom(msgs, 0, 0)
	return out, "all", changed
}

// thinkingOrdinal converts the server's (message, content-index) coordinate
// into the ordinal of that thinking block within its message — the shape
// stripThinkingFrom needs. ok is false when the coordinate is out of range or
// doesn't land on a thinking block.
func thinkingOrdinal(msgs []api.Message, msgIdx, blockIdx int) (int, bool) {
	if msgIdx < 0 || msgIdx >= len(msgs) {
		return 0, false
	}
	content := msgs[msgIdx].Content
	if blockIdx < 0 || blockIdx >= len(content) || content[blockIdx].Type != "thinking" {
		return 0, false
	}
	ord := 0
	for _, b := range content[:blockIdx] {
		if b.Type == "thinking" {
			ord++
		}
	}
	return ord, true
}

// stripThinkingFrom removes thinking blocks from assistant messages at index
// msgIdx and later. In the first affected message only thinking blocks at
// ordinal >= fromOrd are removed (earlier ones are still valid for the
// prefix); every later message loses all of them. Empty-text blocks are
// dropped alongside so a message never carries only whitespace. Messages are
// copied on write; the input is never mutated.
func stripThinkingFrom(msgs []api.Message, msgIdx, fromOrd int) ([]api.Message, bool) {
	var out []api.Message
	for i := range msgs {
		if i < msgIdx {
			continue
		}
		ord := 0
		if i == msgIdx {
			ord = fromOrd
		}
		stripped, ok := stripMessageThinking(msgs[i], ord)
		if !ok {
			continue
		}
		if out == nil {
			out = make([]api.Message, len(msgs))
			copy(out, msgs)
		}
		out[i] = stripped
	}
	if out == nil {
		return msgs, false
	}
	return out, true
}

// stripMessageThinking mirrors CC's pAt: drop thinking blocks at ordinal >=
// fromOrd from an assistant message. ok is false when the message is
// unchanged (not assistant, or no thinking block at that ordinal).
func stripMessageThinking(m api.Message, fromOrd int) (api.Message, bool) {
	if m.Role != "assistant" {
		return m, false
	}
	cut := -1
	seen := 0
	for i, b := range m.Content {
		if b.Type != "thinking" {
			continue
		}
		if seen == fromOrd {
			cut = i
			break
		}
		seen++
	}
	if cut == -1 {
		return m, false
	}
	kept := make([]api.ContentBlock, 0, len(m.Content))
	for i, b := range m.Content {
		if fromOrd > 0 && i < cut {
			kept = append(kept, b)
			continue
		}
		if b.Type == "thinking" {
			continue
		}
		if b.Type == "text" && strings.TrimSpace(b.Text) == "" {
			continue
		}
		kept = append(kept, b)
	}
	if len(kept) == 0 {
		kept = append(kept, api.ContentBlock{Type: "text", Text: thinkingRemovedPlaceholder})
	}
	m.Content = kept
	return m, true
}
