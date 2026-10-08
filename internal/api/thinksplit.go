package api

import "strings"

const (
	thinkOpenTag  = "<think>"
	thinkCloseTag = "</think>"
)

// thinkSegment is one piece of thinkSplitter output.
type thinkSegment struct {
	thinking bool
	text     string
}

// thinkSplitter separates <think>…</think> reasoning that a model writes into
// its visible content (a local server running without a reasoning parser, a
// gateway that passes the raw text through) from the answer. It follows
// oh-my-pi's leaked-thinking scanner (packages/ai/src/dialect/thinking.ts):
// a tag counts only outside Markdown code spans and fences, so prose or code
// that mentions <think> stays visible. Input may be split anywhere; a tail
// that could still grow into a tag or a backtick run is held until the next
// feed or flush.
//
// A <think> opens reasoning only in leading position (no visible text yet,
// whitespace aside) or right after a </think>, the way interleaved-reasoning
// models write it. Anywhere else it is prose that happens to mention the tag.
//
// With implicit set the model's chat template already put <think> in the
// prompt (DeepSeek-R1 distills, Qwen), so the output starts mid-reasoning and
// a </think> arrives with no opener. Output is then held until that </think>,
// a <think>, the end of input or implicitHoldLimit bytes, whichever is first;
// text before the </think> is reasoning.
//
// Whitespace around a reasoning block is dropped the way chat templates drop
// it: before the first block when nothing visible precedes it, after a closing
// tag, and at both ends of the reasoning. Input without <think> comes back
// unchanged.
type thinkSplitter struct {
	implicit    bool   // output may start inside reasoning, with no opening tag
	leadingDone bool   // the implicit-reasoning check has resolved
	buf         string // input not yet emitted
	inThink     bool
	codeTicks   int  // backticks that opened the current code span or fence; 0 outside code
	codeFenced  bool // the code region is a fenced block, which closes on a fence line
	lineIndent  int  // leading spaces on the current visible line; -1 once other text appears

	visibleStarted bool   // a non-space visible character has been emitted
	pendingSpace   string // leading visible whitespace, held until we know whether <think> follows
	afterThink     bool   // drop visible whitespace up to the next non-space character
	thinkStarted   bool   // the open block has emitted a non-space character
	thinkSpace     string // trailing reasoning whitespace, held until more reasoning follows

	out []thinkSegment
}

// feed consumes the next chunk and returns the segments it completed.
func (s *thinkSplitter) feed(chunk string) []thinkSegment {
	s.buf += chunk
	s.consume(false)
	return s.take()
}

// flush ends the input: held text is emitted, an unclosed reasoning block
// ends where the input does.
func (s *thinkSplitter) flush() []thinkSegment {
	s.consume(true)
	if s.pendingSpace != "" {
		s.emit(false, s.pendingSpace)
		s.pendingSpace = ""
	}
	s.thinkSpace = ""
	return s.take()
}

func (s *thinkSplitter) take() []thinkSegment {
	out := s.out
	s.out = nil
	return out
}

// implicitHoldLimit bounds how much output an implicit splitter holds while
// waiting for a </think>. Implicit mode only runs for local servers with
// thinking requested, where parser-less R1/Qwen reasoning often runs several
// KiB; a smaller cap would show it as visible text with a literal </think>.
const implicitHoldLimit = 32 << 10

// resolveLeading handles output that may begin inside reasoning. It reports
// whether consume should return because the buffer is still being held.
func (s *thinkSplitter) resolveLeading(final bool) (hold bool) {
	closeAt := indexOutsideCode(s.buf, thinkCloseTag)
	openAt := indexOutsideCode(s.buf, thinkOpenTag)
	switch {
	case closeAt >= 0 && (openAt < 0 || closeAt < openAt):
		s.thinking(s.buf[:closeAt])
		s.buf = s.buf[closeAt+len(thinkCloseTag):]
		s.thinkStarted = false
		s.thinkSpace = ""
		s.afterThink = true
		s.leadingDone = true
		return false
	case openAt >= 0 || final || len(s.buf) > implicitHoldLimit:
		s.leadingDone = true
		return false
	}
	return true
}

// endLeading abandons the wait for an implicit </think>; held output goes out
// as ordinary content.
func (s *thinkSplitter) endLeading() []thinkSegment {
	if !s.implicit || s.leadingDone {
		return nil
	}
	s.leadingDone = true
	s.consume(false)
	return s.take()
}

// indexOutsideCode is the index of the first tag in s that is not inside a
// backtick code span or fence, or -1. An unterminated span runs to the end.
func indexOutsideCode(s, tag string) int {
	for i := 0; i < len(s); {
		if s[i] == '`' {
			n := backtickRunAt(s, i)
			end := backtickRunOfLength(s[i+n:], n)
			if end < 0 {
				return -1
			}
			i += n + end + n
			continue
		}
		if strings.HasPrefix(s[i:], tag) {
			return i
		}
		i++
	}
	return -1
}

func (s *thinkSplitter) consume(final bool) {
	for {
		if s.implicit && !s.leadingDone && !s.inThink {
			if s.resolveLeading(final) {
				return
			}
			continue
		}
		if s.inThink {
			end := strings.Index(s.buf, thinkCloseTag)
			if end < 0 {
				hold := 0
				if !final {
					hold = partialTagSuffix(s.buf, thinkCloseTag)
				}
				s.thinking(s.buf[:len(s.buf)-hold])
				s.buf = s.buf[len(s.buf)-hold:]
				return
			}
			s.thinking(s.buf[:end])
			s.buf = s.buf[end+len(thinkCloseTag):]
			s.inThink = false
			s.thinkSpace = ""
			s.afterThink = true
			continue
		}
		if s.codeTicks > 0 {
			if !s.consumeCode(final) {
				return
			}
			continue
		}
		if s.buf == "" {
			return
		}
		kind, at, ticks := scanThinkVisible(s.buf, final)
		switch kind {
		case thinkScanNone:
			s.visible(s.buf)
			s.buf = ""
			return
		case thinkScanHold:
			s.visible(s.buf[:at])
			s.buf = s.buf[at:]
			return
		case thinkScanTag:
			s.visible(s.buf[:at])
			if s.visibleStarted && !s.afterThink {
				// Prose mentioning the tag, not reasoning.
				s.visible(thinkOpenTag)
				s.buf = s.buf[at+len(thinkOpenTag):]
				continue
			}
			s.buf = s.buf[at+len(thinkOpenTag):]
			s.inThink = true
			s.thinkStarted = false
			s.thinkSpace = ""
			s.pendingSpace = ""
		case thinkScanCode:
			s.visible(s.buf[:at])
			fenced := ticks >= 3 && s.lineIndent >= 0 && s.lineIndent <= 3
			s.visible(s.buf[at : at+ticks])
			s.buf = s.buf[at+ticks:]
			s.codeTicks = ticks
			s.codeFenced = fenced
		}
	}
}

// consumeCode emits text inside a code region, where tags don't count. It
// reports whether the region closed; false means the rest is held.
func (s *thinkSplitter) consumeCode(final bool) bool {
	if s.codeFenced {
		if end := fenceCloseEnd(s.buf, s.codeTicks, final); end >= 0 {
			s.visible(s.buf[:end])
			s.buf = s.buf[end:]
			s.codeTicks, s.codeFenced = 0, false
			return true
		}
		if final {
			s.visible(s.buf)
			s.buf = ""
			s.codeTicks, s.codeFenced = 0, false
			return false
		}
		// Emit whole lines; the last one may still become the closing fence.
		if nl := strings.LastIndexByte(s.buf, '\n'); nl >= 0 {
			s.visible(s.buf[:nl+1])
			s.buf = s.buf[nl+1:]
		}
		return false
	}
	if end := backtickRunOfLength(s.buf, s.codeTicks); end >= 0 && (final || end+s.codeTicks < len(s.buf)) {
		s.visible(s.buf[:end+s.codeTicks])
		s.buf = s.buf[end+s.codeTicks:]
		s.codeTicks = 0
		return true
	}
	// No committed close: a trailing backtick run may still grow into one.
	hold := 0
	if !final {
		hold = trailingBackticks(s.buf)
	}
	s.visible(s.buf[:len(s.buf)-hold])
	s.buf = s.buf[len(s.buf)-hold:]
	if final {
		s.codeTicks = 0
	}
	return false
}

func (s *thinkSplitter) visible(text string) {
	if text == "" {
		return
	}
	s.lineIndent = trailingLineIndent(text, s.lineIndent)
	if s.afterThink {
		text = strings.TrimLeft(text, " \t\r\n")
		if text == "" {
			return
		}
		s.afterThink = false
	}
	if !s.visibleStarted {
		if strings.TrimLeft(text, " \t\r\n") == "" {
			s.pendingSpace += text
			return
		}
		text = s.pendingSpace + text
		s.pendingSpace = ""
		s.visibleStarted = true
	}
	s.emit(false, text)
}

func (s *thinkSplitter) thinking(text string) {
	if !s.thinkStarted {
		text = strings.TrimLeft(text, " \t\r\n")
		if text == "" {
			return
		}
		s.thinkStarted = true
	}
	body := strings.TrimRight(text, " \t\r\n")
	if body == "" {
		s.thinkSpace += text
		return
	}
	s.emit(true, s.thinkSpace+body)
	s.thinkSpace = text[len(body):]
}

func (s *thinkSplitter) emit(thinking bool, text string) {
	if n := len(s.out); n > 0 && s.out[n-1].thinking == thinking {
		s.out[n-1].text += text
		return
	}
	s.out = append(s.out, thinkSegment{thinking: thinking, text: text})
}

type thinkScanKind int

const (
	thinkScanNone thinkScanKind = iota
	thinkScanTag                // <think> at the index
	thinkScanCode               // a backtick run of ticks opens a code span or fence
	thinkScanHold               // the tail from the index may still grow into a tag or longer run
)

// scanThinkVisible finds the first boundary in visible text outside code: an
// opening tag, a backtick run, or (unless final) a tail that is a prefix of
// the tag or a backtick run that may continue in the next chunk.
func scanThinkVisible(buf string, final bool) (kind thinkScanKind, at, ticks int) {
	holdFrom := len(buf) - len(thinkOpenTag) + 1
	for i := 0; i < len(buf); i++ {
		switch buf[i] {
		case '<':
			if strings.HasPrefix(buf[i:], thinkOpenTag) {
				return thinkScanTag, i, 0
			}
			if !final && i >= holdFrom && strings.HasPrefix(thinkOpenTag, buf[i:]) {
				return thinkScanHold, i, 0
			}
		case '`':
			n := backtickRunAt(buf, i)
			if !final && i+n == len(buf) {
				return thinkScanHold, i, 0
			}
			return thinkScanCode, i, n
		}
	}
	return thinkScanNone, 0, 0
}

// partialTagSuffix is the length of the longest proper prefix of tag that
// ends s.
func partialTagSuffix(s, tag string) int {
	for n := min(len(s), len(tag)-1); n > 0; n-- {
		if strings.HasPrefix(tag, s[len(s)-n:]) {
			return n
		}
	}
	return 0
}

func backtickRunAt(s string, from int) int {
	end := from
	for end < len(s) && s[end] == '`' {
		end++
	}
	return end - from
}

// backtickRunOfLength is the index of the first maximal backtick run of
// exactly n backticks in s, or -1.
func backtickRunOfLength(s string, n int) int {
	for i := 0; i < len(s); {
		if s[i] != '`' {
			i++
			continue
		}
		run := backtickRunAt(s, i)
		if run == n {
			return i
		}
		i += run
	}
	return -1
}

func trailingBackticks(s string) int {
	n := 0
	for n < len(s) && s[len(s)-1-n] == '`' {
		n++
	}
	return n
}

// trailingLineIndent continues the leading-space count of the current line
// (prior) through text: -1 once a non-space character appears, reset at each
// newline.
func trailingLineIndent(text string, prior int) int {
	indent := prior
	if nl := strings.LastIndexByte(text, '\n'); nl >= 0 {
		indent = 0
		text = text[nl+1:]
	}
	for i := 0; i < len(text) && indent >= 0; i++ {
		if text[i] == ' ' {
			indent++
		} else {
			indent = -1
		}
	}
	return indent
}

// fenceCloseEnd is the index just past the first line of s that closes a
// fence opened with ticks backticks (only backticks, at least ticks of them),
// or -1. An unterminated last line only counts when final.
func fenceCloseEnd(s string, ticks int, final bool) int {
	for start := 0; start <= len(s); {
		nl := strings.IndexByte(s[start:], '\n')
		end := len(s)
		if nl >= 0 {
			end = start + nl
		}
		line := strings.TrimSpace(s[start:end])
		if len(line) >= ticks && strings.Trim(line, "`") == "" && (nl >= 0 || final) {
			if nl >= 0 {
				return end + 1
			}
			return len(s)
		}
		if nl < 0 {
			return -1
		}
		start = end + 1
	}
	return -1
}
