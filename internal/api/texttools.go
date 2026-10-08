package api

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
)

// In-text tool calling for OpenAI-compatible servers or models without native
// tools. It follows oh-my-pi's hermes and qwen3 dialects
// (packages/ai/src/dialect/{hermes,qwen3}.ts): the tool schemas go into the
// system prompt, the model writes each call as a <tool_call> block wrapping a
// JSON object, and results come back in <tool_response> blocks.

// Values for Config.ToolCallFormat.
const (
	ToolCallFormatNative = "native"
	ToolCallFormatHermes = "hermes"
	ToolCallFormatQwen3  = "qwen3"
	// ToolCallFormatAuto is native until the server rejects tools, then hermes.
	ToolCallFormatAuto = "auto"
)

const (
	toolCallOpenTag    = "<tool_call>"
	toolCallCloseTag   = "</tool_call>"
	toolCallInvalidTag = "invalid tool call: "
	// maxToolCallBody bounds one <tool_call> body; a longer one is not a call.
	maxToolCallBody = 1 << 20
)

// normalizeToolCallFormat lower-cases and validates a configured format;
// empty and unknown values mean native.
func normalizeToolCallFormat(format string) string {
	switch f := strings.ToLower(strings.TrimSpace(format)); f {
	case ToolCallFormatHermes, ToolCallFormatQwen3, ToolCallFormatAuto:
		return f
	default:
		return ToolCallFormatNative
	}
}

// textToolSyntax is the in-text syntax a resolved format selects, "" for
// native. auto is not a syntax: openAIChatBody resolves it first.
func textToolSyntax(format string) string {
	switch f := strings.ToLower(strings.TrimSpace(format)); f {
	case ToolCallFormatHermes, ToolCallFormatQwen3:
		return f
	default:
		return ""
	}
}

// textToolCall is a call parsed out of assistant text. When invalid is set the
// block had a name but its arguments were not a JSON object; args then holds
// text that is not valid JSON, so the agent loop answers the call with its
// invalid-arguments error result.
type textToolCall struct {
	name    string
	args    string
	invalid bool
}

// textToolEvent is plain text or a call.
type textToolEvent struct {
	text string
	call *textToolCall
}

// textToolParser extracts <tool_call> blocks from streamed assistant content.
// Input may be split anywhere; output does not depend on how it is chunked.
//
// A call opens only at the start of a line (leading blanks aside) or right
// after another call, so a tag the model quotes mid-sentence, or one copied
// from a file it read, stays text. Lines inside a fenced code block (opened
// by a line of three or more backticks) are text too. Inline code spans are
// not tracked: a tag that follows prose or a backtick on its line is text
// anyway, and an unmatched backtick has no effect on later lines.
//
// Inside a call the closing tag counts only outside JSON strings, so an
// argument may contain "</tool_call>". If no such close exists by the end of
// the input, the first plain close tag is used instead, which recovers calls
// whose JSON has an unescaped quote.
type textToolParser struct {
	buf    []byte
	inCall bool
	out    []textToolEvent

	// Line state outside a call. lineLen is the bytes of the current line
	// already passed on; 0 means the next byte starts a line.
	lineLen   int
	fenceLen  int // backticks that opened the current fence; 0 outside
	lead      int // leading spaces of the line; -1 when indented 4 or more
	ticks     int // leading backtick run
	ticksDone bool
	afterTick bool // a non-blank byte followed the run
	tickLater bool // a backtick followed it

	// Mid-line calls held until the rest of the reply shows whether they were
	// quoted: see consumePending.
	pending   bool
	tentative bool // the call being read is held rather than emitted
	pend      []textToolEvent
	pendRaw   []byte

	// Scan state inside a call: bytes of buf already examined, and whether
	// the scan is inside a JSON string.
	scanned int
	inStr   bool
	esc     bool
}

func (p *textToolParser) feed(chunk string) []textToolEvent {
	p.buf = append(p.buf, chunk...)
	p.consume(false)
	return p.take()
}

// flush ends the input. An unclosed call is parsed as far as it goes.
func (p *textToolParser) flush() []textToolEvent {
	p.consume(true)
	return p.take()
}

func (p *textToolParser) take() []textToolEvent {
	out := p.out
	p.out = nil
	return out
}

func (p *textToolParser) text(s string) {
	if s == "" {
		return
	}
	if n := len(p.out); n > 0 && p.out[n-1].call == nil {
		p.out[n-1].text += s
		return
	}
	p.out = append(p.out, textToolEvent{text: s})
}

func (p *textToolParser) consume(final bool) {
	for len(p.buf) > 0 {
		if p.inCall {
			if !p.consumeCall(final) {
				return
			}
			continue
		}
		if p.pending {
			if !p.consumePending(final) {
				return
			}
			continue
		}
		if p.lineLen == 0 && p.fenceLen == 0 {
			switch p.atLineStart(final) {
			case lineHold:
				return
			case lineCall:
				continue
			}
		}
		if !p.consumeLine(final) {
			return
		}
	}
	if final && p.inCall {
		p.consumeCall(true)
	}
	if final && p.pending && !p.inCall {
		p.resolvePending(true)
	}
}

// A call that starts mid-line is accepted only if nothing but whitespace and
// more calls follow it to the end of the reply: a real call ends the turn,
// while a quoted excerpt is followed by prose. Such calls are held in pend,
// with their source text in pendRaw, until the next non-blank text decides.

// consumePending reads what follows a held call: blanks and further calls
// keep it open, anything else turns the held region back into text. It
// reports whether to keep going.
func (p *textToolParser) consumePending(final bool) bool {
	i := 0
	for i < len(p.buf) && (p.buf[i] == ' ' || p.buf[i] == '\t' || p.buf[i] == '\r' || p.buf[i] == '\n') {
		i++
	}
	if i > 0 {
		ws := string(p.buf[:i])
		p.pend = append(p.pend, textToolEvent{text: ws})
		p.pendRaw = append(p.pendRaw, ws...)
		p.buf = p.buf[i:]
	}
	rest := p.buf
	switch {
	case len(rest) == 0:
		return false
	case bytes.HasPrefix(rest, []byte(toolCallOpenTag)):
		p.buf = p.buf[len(toolCallOpenTag):]
		p.openCall(true)
		return true
	case !final && bytes.HasPrefix([]byte(toolCallOpenTag), rest):
		return false
	}
	p.resolvePending(false)
	return true
}

// resolvePending releases the held region as calls (the reply ended after
// it) or as the text it was written as.
func (p *textToolParser) resolvePending(asCalls bool) {
	if asCalls {
		for _, ev := range p.pend {
			if ev.call == nil {
				p.text(ev.text)
			} else {
				p.out = append(p.out, ev)
			}
		}
	} else {
		p.text(string(p.pendRaw))
		p.lineLen = 1
		if n := len(p.pendRaw); n > 0 && p.pendRaw[n-1] == '\n' {
			p.lineLen = 0
		}
	}
	p.pend, p.pendRaw, p.pending = nil, nil, false
}

func (p *textToolParser) openCall(tentative bool) {
	p.inCall, p.tentative = true, tentative
	p.scanned, p.inStr, p.esc = 0, false, false
}

type lineStart int

const (
	linePlain lineStart = iota // not a call: pass the line on as text
	lineHold                   // the buffer ends where the answer is still open
	lineCall                   // a call opened
)

// maxCallIndent bounds the blanks before a call tag; more than that is prose.
const maxCallIndent = 64

// atLineStart decides whether the line at the head of buf opens a call.
func (p *textToolParser) atLineStart(final bool) lineStart {
	i := 0
	for i < len(p.buf) && (p.buf[i] == ' ' || p.buf[i] == '\t') {
		i++
	}
	rest := p.buf[i:]
	switch {
	case bytes.HasPrefix(rest, []byte(toolCallOpenTag)):
		p.text(string(p.buf[:i]))
		p.buf = p.buf[i+len(toolCallOpenTag):]
		p.openCall(false)
		return lineCall
	case i > maxCallIndent:
		return linePlain
	case !final && bytes.HasPrefix([]byte(toolCallOpenTag), rest):
		return lineHold
	}
	return linePlain
}

// consumeLine passes buf on as text up to and including the next newline,
// tracking fences, and arms the next line at a newline.
func (p *textToolParser) consumeLine(final bool) bool {
	n := bytes.IndexByte(p.buf, '\n')
	end := len(p.buf)
	if n >= 0 {
		end = n + 1
	}
	tag := []byte(toolCallOpenTag)
	mid := -1
	if p.fenceLen == 0 {
		if at := bytes.Index(p.buf[:end], tag); at >= 0 {
			mid, end = at, at
		} else if n < 0 && !final {
			// The tail may still grow into a tag.
			end -= partialTagSuffix(string(p.buf[max(0, end-len(tag)+1):]), toolCallOpenTag)
		}
	}
	seg := p.buf[:end]
	body := seg
	if n >= 0 && mid < 0 {
		body = seg[:n]
	}
	for _, b := range body {
		p.lineByte(b)
	}
	p.text(string(seg))
	p.lineLen += len(seg)
	p.buf = p.buf[end:]
	switch {
	case mid >= 0:
		p.buf = p.buf[len(tag):]
		p.pending = true
		p.openCall(true)
	case n >= 0:
		p.endLine()
	case end == 0:
		return false
	}
	return true
}

// lineByte folds one byte of the current line into the fence-detection state.
func (p *textToolParser) lineByte(b byte) {
	if !p.ticksDone {
		switch {
		case b == ' ' && p.ticks == 0:
			if p.lead >= 0 {
				if p.lead++; p.lead > 3 {
					p.lead = -1
				}
			}
			return
		case b == '`':
			p.ticks++
			return
		}
		p.ticksDone = true
		if b == '\t' && p.ticks == 0 {
			p.lead = -1
		}
	}
	switch b {
	case '`':
		p.tickLater, p.afterTick = true, true
	case ' ', '\t', '\r':
	default:
		p.afterTick = true
	}
}

// endLine opens or closes a fence from the finished line and resets for the
// next one.
func (p *textToolParser) endLine() {
	if p.lead >= 0 {
		switch {
		case p.fenceLen > 0:
			if p.ticks >= p.fenceLen && !p.afterTick {
				p.fenceLen = 0
			}
		case p.ticks >= 3 && !p.tickLater:
			p.fenceLen = p.ticks
		}
	}
	p.lineLen, p.lead, p.ticks = 0, 0, 0
	p.ticksDone, p.afterTick, p.tickLater = false, false, false
}

// findClose scans the call body for the closing tag outside JSON strings,
// resuming where the last scan stopped. It returns the tag's index, -1 when
// it has not arrived, or -2 when the body has grown past maxToolCallBody.
func (p *textToolParser) findClose(final bool) int {
	for i := p.scanned; i < len(p.buf); i++ {
		if i > maxToolCallBody {
			return -2
		}
		c := p.buf[i]
		if p.inStr {
			switch {
			case p.esc:
				p.esc = false
			case c == '\\':
				p.esc = true
			case c == '"':
				p.inStr = false
			}
			continue
		}
		switch c {
		case '"':
			p.inStr = true
		case '<':
			rest := p.buf[i:]
			if bytes.HasPrefix(rest, []byte(toolCallCloseTag)) {
				p.scanned = i
				return i
			}
			if !final && bytes.HasPrefix([]byte(toolCallCloseTag), rest) {
				p.scanned = i
				return -1
			}
		}
	}
	p.scanned = len(p.buf)
	return -1
}

// consumeCall reads a call body; it reports whether the call ended so the
// scan can continue.
func (p *textToolParser) consumeCall(final bool) bool {
	end := p.findClose(final)
	if end == -1 && final {
		// No close outside a string: fall back to the first plain close tag.
		if i := bytes.Index(p.buf, []byte(toolCallCloseTag)); i >= 0 && i <= maxToolCallBody {
			end = i
		}
	}
	var ev textToolEvent
	var raw string
	switch {
	case end >= 0:
		raw = string(p.buf[:end])
		ev = p.finishCall(raw, true)
		raw += toolCallCloseTag
		p.buf = p.buf[end+len(toolCallCloseTag):]
	case end == -2:
		raw = string(p.buf[:maxToolCallBody])
		ev = p.finishCall(raw, false)
		p.buf = p.buf[maxToolCallBody:]
	case final:
		raw = string(p.buf)
		ev = p.finishCall(raw, false)
		p.buf = p.buf[:0]
	default:
		return false
	}
	raw = toolCallOpenTag + raw
	switch {
	case p.tentative || p.pending:
		p.pend = append(p.pend, ev)
		p.pendRaw = append(p.pendRaw, raw...)
		p.pending = true
	case ev.call != nil:
		p.out = append(p.out, ev)
	default:
		p.text(ev.text)
	}
	p.tentative = false
	p.inCall = false
	p.lineLen = 0
	return true
}

var toolCallNamePattern = regexp.MustCompile(`^\s*\{\s*"name"\s*:\s*("(?:\\.|[^"\\])*")`)

// finishCall turns a call body into a call, an invalid call (the name is
// known), or the original text when nothing can be made of it.
func (p *textToolParser) finishCall(body string, closed bool) textToolEvent {
	if call, ok := parseToolCallBody(body); ok {
		return textToolEvent{call: &call}
	}
	if m := toolCallNamePattern.FindStringSubmatch(body); m != nil {
		var name string
		if json.Unmarshal([]byte(m[1]), &name) == nil && name != "" {
			return textToolEvent{call: &textToolCall{
				name: name, args: toolCallInvalidTag + body, invalid: true,
			}}
		}
	}
	raw := toolCallOpenTag + body
	if closed {
		raw += toolCallCloseTag
	}
	return textToolEvent{text: raw}
}

// parseToolCallBody parses {"name":…,"arguments":{…}}. arguments may be an
// object or a JSON string holding one; absent or null means no arguments.
func parseToolCallBody(body string) (textToolCall, bool) {
	var parsed struct {
		Name       string          `json:"name"`
		Arguments  json.RawMessage `json:"arguments"`
		Parameters json.RawMessage `json:"parameters"`
	}
	if err := json.Unmarshal([]byte(strings.TrimSpace(body)), &parsed); err != nil || parsed.Name == "" {
		return textToolCall{}, false
	}
	raw := parsed.Arguments
	if len(raw) == 0 {
		raw = parsed.Parameters
	}
	if len(raw) > 0 && raw[0] == '"' {
		var inner string
		if err := json.Unmarshal(raw, &inner); err != nil {
			return textToolCall{}, false
		}
		raw = json.RawMessage(inner)
	}
	if t := strings.TrimSpace(string(raw)); t == "" || t == "null" {
		return textToolCall{name: parsed.Name, args: "{}"}, true
	}
	var args map[string]any
	if err := json.Unmarshal(raw, &args); err != nil || args == nil {
		return textToolCall{}, false
	}
	return textToolCall{name: parsed.Name, args: string(raw)}, true
}

// newTextToolIDs returns a generator of unique tool_use ids for one response.
func newTextToolIDs() func() string {
	var raw [6]byte
	prefix := "toolu_text"
	if _, err := rand.Read(raw[:]); err == nil {
		prefix += "_" + hex.EncodeToString(raw[:])
	}
	n := 0
	return func() string {
		n++
		return fmt.Sprintf("%s_%d", prefix, n)
	}
}

// textToolsPrompt is the system-prompt section that teaches the calling
// syntax and lists the tools.
func textToolsPrompt(syntax string, defs []ToolDef) string {
	tools := openAIToolsFromToolDefs(defs)
	if len(tools) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("# Tools\n\nYou can call functions to help with the request. The functions are listed inside <tools></tools>:\n\n<tools>\n")
	for _, tool := range tools {
		raw, err := json.Marshal(tool)
		if err != nil {
			continue
		}
		b.Write(raw)
		b.WriteByte('\n')
	}
	b.WriteString("</tools>\n\nEmit each call as a <tool_call> block wrapping a single-line JSON object with name and arguments:\n\n")
	b.WriteString("<tool_call>\n{\"name\":\"function_name\",\"arguments\":{\"arg\":\"value\"}}\n</tool_call>\n\n")
	b.WriteString("Results arrive later in a user turn as <tool_response> blocks:\n\n<tool_response>\nverbatim tool result\n</tool_response>\n\n")
	b.WriteString("Rules:\n")
	b.WriteString("- name MUST match a listed function; arguments is a JSON object, never a JSON string.\n")
	b.WriteString("- Argument string values use only normal JSON string escaping; never HTML-escape their contents.\n")
	b.WriteString("- Emit multiple calls as consecutive <tool_call> blocks; keep any prose outside them.\n")
	b.WriteString("- Read each <tool_response> in call order. NEVER emit <tool_response> yourself.\n")
	b.WriteString("- Write the complete call. NEVER announce a tool without emitting its <tool_call> block.\n")
	if syntax == ToolCallFormatQwen3 {
		b.WriteString("- Do any private reasoning in <think>...</think> before your tool calls, and NEVER put tool calls inside <think>.\n")
	}
	return b.String()
}

// renderTextToolCall is one call as the model is told to write it.
func renderTextToolCall(name string, input map[string]any) string {
	if input == nil {
		input = map[string]any{}
	}
	raw, err := json.Marshal(struct {
		Name      string         `json:"name"`
		Arguments map[string]any `json:"arguments"`
	}{name, input})
	if err != nil {
		raw = []byte(fmt.Sprintf(`{"name":%q,"arguments":{}}`, name))
	}
	return toolCallOpenTag + "\n" + string(raw) + "\n" + toolCallCloseTag
}

// textToolMessage rewrites msg for the in-text syntax: tool_use blocks become
// <tool_call> text in the assistant turn, and tool_result blocks become one
// block of <tool_response> text ahead of the user's own text. The result has
// no tool blocks, so the ordinary conversion sends it as plain messages.
func textToolMessage(msg Message) Message {
	var tools bool
	for _, b := range msg.Content {
		if b.Type == "tool_use" || b.Type == "tool_result" {
			tools = true
			break
		}
	}
	if !tools {
		return msg
	}
	var out []ContentBlock
	var responses, texts, calls []string
	for _, b := range msg.Content {
		switch b.Type {
		case "tool_use":
			calls = append(calls, renderTextToolCall(b.Name, b.Input))
		case "tool_result":
			// A result must not close its own block and write a call.
			body := strings.ReplaceAll(toolResultText(b), "</tool_response>", `<\/tool_response>`)
			if b.IsError {
				body = "Error: " + body
			}
			responses = append(responses, "<tool_response>\n"+body+"\n</tool_response>")
		case "text":
			texts = append(texts, b.Text)
		default:
			out = append(out, b)
		}
	}
	if msg.Role == "assistant" {
		if len(texts) > 0 {
			calls = append([]string{strings.Join(texts, "\n\n")}, calls...)
		}
		texts = []string{strings.Join(calls, "\n")}
	} else if len(responses) > 0 {
		texts = append([]string{strings.Join(responses, "\n")}, texts...)
	}
	rewritten := msg
	rewritten.Content = append([]ContentBlock{{Type: "text", Text: strings.Join(texts, "\n\n")}}, out...)
	return rewritten
}

// mentionsToolsUnsupported reports whether an error body says the model or
// server takes no tools ("does not support tools" from Ollama, "tools param
// requires --jinja" from llama.cpp). Deliberately narrow: an error that merely
// mentions tools, such as a bad tool schema, keeps native tools.
func mentionsToolsUnsupported(body []byte) bool {
	s := strings.ToLower(string(body))
	if !containsAny(s, "tools", "tool use", "tool_use", "tool calling", "tool call", "function calling", "tool_choice") {
		return false
	}
	return containsAny(s, "does not support", "doesn't support", "not support", "unsupported", "not enabled", "not available", "requires --jinja", "--jinja")
}

// textToolContentBlocks converts a complete reply's visible text into text
// and tool_use blocks.
func textToolContentBlocks(visible string) []ContentBlock {
	var p textToolParser
	events := append(p.feed(visible), p.flush()...)
	nextID := newTextToolIDs()
	var blocks []ContentBlock
	addText := func(s string) {
		if s == "" {
			return
		}
		if n := len(blocks); n > 0 && blocks[n-1].Type == "text" {
			blocks[n-1].Text += s
			return
		}
		blocks = append(blocks, ContentBlock{Type: "text", Text: s})
	}
	for _, ev := range events {
		switch {
		case ev.call == nil:
			addText(ev.text)
		case ev.call.invalid:
			// A complete response has no argument stream for the agent loop
			// to reject, so the call runs with no input and its tool reports
			// the missing arguments.
			blocks = append(blocks, ContentBlock{Type: "tool_use", ID: nextID(), Name: ev.call.name, Input: map[string]any{}})
		default:
			var input map[string]any
			_ = json.Unmarshal([]byte(ev.call.args), &input)
			if input == nil {
				input = map[string]any{}
			}
			blocks = append(blocks, ContentBlock{Type: "tool_use", ID: nextID(), Name: ev.call.name, Input: input})
		}
	}
	return blocks
}
