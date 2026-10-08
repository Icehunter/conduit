package compact

import (
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/icehunter/conduit/internal/api"
	"github.com/icehunter/conduit/internal/transcript"
)

// Budgets for the deterministic <compaction-context> sections, in runes.
const (
	userMessagesBudget = 24_000
	userMessageMax     = 4_000
	anchorBudget       = 7_000
	anchorHeaderSlack  = 150
	anchorItemMax      = 200
)

// contextBudgets returns the user-message and anchor budgets, in runes. They
// shrink with the main model's window (window/8 and window/32, in characters)
// so that on a 32K model the summary message stays a fraction of the window
// instead of a fixed 31K characters. An unknown window keeps the full budgets.
func contextBudgets(o Options) (users, anchors int) {
	users, anchors = userMessagesBudget, anchorBudget
	if o.MainWindow > 0 {
		users = max(min(users, o.MainWindow/8), 1_000)
		anchors = max(min(anchors, o.MainWindow/32), anchorHeaderSlack+300)
	}
	return users, anchors
}

const (
	contextOpen  = "<compaction-context>"
	contextClose = "</compaction-context>"

	headingUsers    = "## User Messages (verbatim, newest first)"
	headingAnchors  = "## Anchor Index"
	headingRecovery = "## Context Recovery"
)

// Anchor kinds, in the order they render.
const (
	kindPath  = "Paths"
	kindURL   = "URLs"
	kindSHA   = "SHAs"
	kindRef   = "Issue refs"
	kindError = "Errors"
)

var anchorKinds = []string{kindPath, kindURL, kindSHA, kindRef, kindError}

var (
	urlRe  = regexp.MustCompile(`https?://[^\s<>"'` + "`" + `)\]}]+`)
	pathRe = regexp.MustCompile(`(?:^|[\s"'` + "`" + `(\[=:,])((?:(?:~|\.{1,2})?/(?:[\w@.+-]+/)*|(?:[\w@.+-]+/)+)[\w@+-][\w@.+-]*\.[A-Za-z][A-Za-z0-9]{0,9})\b`)
	shaRe  = regexp.MustCompile(`\b[0-9a-f]{7,40}\b`)
	refRe  = regexp.MustCompile(`(?:^|[^\w&#])(#\d{2,6})\b`)
	// Diagnostic shapes only; a successful file read full of the word
	// "error" must not flood the index.
	errPrefixRe = regexp.MustCompile(`(?i)^(error|fatal|panic)[:\[]|^traceback\b`)
	errShapeRe  = regexp.MustCompile(`^--- FAIL|^FAIL\b|\b\w+Error:|:\d+(?::\d+)?: error\b`)

	reminderRe    = regexp.MustCompile(`(?s)<system-reminder>.*?</system-reminder>`)
	countSuffixRe = regexp.MustCompile(`\s\[x(\d+)\]$`)
	userMsgRe     = regexp.MustCompile(`(?s)<user-message>\n(.*?)\n</user-message>`)

	// tagEscaper keeps quoted text from forging the structural tags this file
	// parses back on the next compaction.
	tagEscaper = strings.NewReplacer(
		"<user-message>", "&lt;user-message>",
		"</user-message>", "&lt;/user-message>",
		contextOpen, "&lt;compaction-context>",
		contextClose, "&lt;/compaction-context>",
		"<summary>", "&lt;summary>",
		"</summary>", "&lt;/summary>",
	)
)

// injectedPrefixes mark user-role text that conduit or the harness inserted,
// not something the user typed.
var injectedPrefixes = []string{
	"<system-reminder", "<task-notification", "<teammate-message",
	"<local-command", "<command-", "<summary>", "[Request interrupted",
}

// shellTools run commands whose output can carry compiler and test errors.
var shellTools = map[string]bool{"Bash": true, "PowerShell": true, "REPL": true}

// anchor is one entry of the anchor index.
type anchor struct {
	kind  string
	text  string
	count int
	pos   int // recency: higher is newer; carried-over anchors are negative
}

// prevContext is what a previous compaction left in its <compaction-context>.
type prevContext struct {
	users   []string // newest first
	anchors []anchor // rank order
}

func (a anchor) cost() int { return utf8.RuneCountInString(a.text) + 6 }

type anchorIndex struct {
	items map[string]*anchor
}

func newAnchorIndex() *anchorIndex { return &anchorIndex{items: map[string]*anchor{}} }

func (ix *anchorIndex) add(kind, text string, count, pos int) {
	text = strings.TrimSpace(text)
	if text == "" {
		return
	}
	text = transcript.Truncate(text, anchorItemMax)
	key := kind + "\x00" + text
	if a, ok := ix.items[key]; ok {
		a.count += count
		if pos > a.pos {
			a.pos = pos
		}
		return
	}
	ix.items[key] = &anchor{kind: kind, text: text, count: count, pos: pos}
}

// scanText records paths, URLs, SHAs and issue refs found in text.
func (ix *anchorIndex) scanText(text string, pos int) {
	for _, u := range urlRe.FindAllString(text, -1) {
		ix.add(kindURL, strings.TrimRight(u, ".,;:!?"), 1, pos)
	}
	text = urlRe.ReplaceAllString(text, " ")
	for _, m := range pathRe.FindAllStringSubmatch(text, -1) {
		ix.add(kindPath, m[1], 1, pos)
	}
	for _, s := range shaRe.FindAllString(text, -1) {
		if hasDigit(s) && hasLetter(s) {
			ix.add(kindSHA, s, 1, pos)
		}
	}
	for _, m := range refRe.FindAllStringSubmatch(text, -1) {
		ix.add(kindRef, m[1], 1, pos)
	}
}

// scanErrors records error-looking lines of a tool result.
func (ix *anchorIndex) scanErrors(text string, isError bool, pos int) {
	first := true
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		if errPrefixRe.MatchString(line) || errShapeRe.MatchString(line) || (isError && first) {
			ix.add(kindError, line, 1, pos)
		}
		first = false
	}
}

func hasDigit(s string) bool  { return strings.ContainsAny(s, "0123456789") }
func hasLetter(s string) bool { return strings.ContainsAny(s, "abcdef") }

// scanMessages indexes every block of msgs; pos grows with the message index.
func (ix *anchorIndex) scanMessages(msgs []api.Message) {
	names := map[string]string{} // tool_use id -> tool name
	for i, m := range msgs {
		pos := i + 1
		for _, b := range m.Content {
			switch b.Type {
			case "text":
				ix.scanText(b.Text, pos)
			case "tool_use":
				names[b.ID] = b.Name
				if len(b.Input) > 0 {
					if raw, err := json.Marshal(b.Input); err == nil {
						ix.scanText(string(raw), pos)
					}
				}
			case "tool_result":
				text := transcript.ToolResultText(b)
				ix.scanText(text, pos)
				if b.IsError || shellTools[names[b.ToolUseID]] {
					ix.scanErrors(text, b.IsError, pos)
				}
			}
		}
	}
}

// ranked returns the items by count, then recency, then text.
func (ix *anchorIndex) ranked() []anchor {
	out := make([]anchor, 0, len(ix.items))
	for _, a := range ix.items {
		out = append(out, *a)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].count != out[j].count {
			return out[i].count > out[j].count
		}
		if out[i].pos != out[j].pos {
			return out[i].pos > out[j].pos
		}
		if out[i].kind != out[j].kind {
			return out[i].kind < out[j].kind
		}
		return out[i].text < out[j].text
	})
	return out
}

// selectAnchors keeps the best-ranked anchors that fit the budget.
func selectAnchors(ranked []anchor, budget int) []anchor {
	remaining := budget - anchorHeaderSlack
	var kept []anchor
	for _, a := range ranked {
		c := a.cost()
		if a.count > 1 {
			c += len(strconv.Itoa(a.count)) + 5
		}
		if c > remaining {
			continue
		}
		remaining -= c
		kept = append(kept, a)
	}
	return kept
}

func renderAnchors(kept []anchor) string {
	var sb strings.Builder
	for _, kind := range anchorKinds {
		var lines []string
		for _, a := range kept {
			if a.kind != kind {
				continue
			}
			line := "- " + tagEscaper.Replace(a.text)
			if a.count > 1 {
				line += fmt.Sprintf("  [x%d]", a.count)
			}
			lines = append(lines, line)
		}
		if len(lines) == 0 {
			continue
		}
		sb.WriteString("### " + kind + "\n" + strings.Join(lines, "\n") + "\n")
	}
	return strings.TrimRight(sb.String(), "\n")
}

// userQuotes returns the user-typed text of msgs, newest first. Tool results
// and injected reminders are skipped; the previous summary message is too.
func userQuotes(msgs []api.Message) []string {
	var out []string
	for i := len(msgs) - 1; i >= 0; i-- {
		if msgs[i].Role != "user" {
			continue
		}
		var parts []string
		for _, b := range msgs[i].Content {
			if b.Type != "text" {
				continue
			}
			text := strings.TrimSpace(reminderRe.ReplaceAllString(b.Text, ""))
			if text == "" || hasInjectedPrefix(text) {
				continue
			}
			parts = append(parts, text)
		}
		if len(parts) > 0 {
			out = append(out, strings.Join(parts, "\n"))
		}
	}
	return out
}

func hasInjectedPrefix(text string) bool {
	for _, p := range injectedPrefixes {
		if strings.HasPrefix(text, p) {
			return true
		}
	}
	return false
}

// selectQuotes applies the per-quote and total budgets to candidates, which
// are ordered newest first, dropping duplicates.
func selectQuotes(candidates []string, budget int) []string {
	var out []string
	seen := map[string]bool{}
	total := 0
	for _, q := range candidates {
		q = transcript.Truncate(strings.TrimSpace(q), userMessageMax)
		if q == "" || seen[q] {
			continue
		}
		n := utf8.RuneCountInString(q)
		if total+n > budget {
			break
		}
		seen[q] = true
		total += n
		out = append(out, q)
	}
	return out
}

// buildContextBlock renders the <compaction-context> block for a compaction of
// head, merged with what the previous compaction carried. It returns "" when
// there is nothing to record.
func buildContextBlock(head []api.Message, prev prevContext, o Options) string {
	usersBudget, anchorsBudget := contextBudgets(o)
	quotes := selectQuotes(append(userQuotes(head), prev.users...), usersBudget)

	ix := newAnchorIndex()
	for i, a := range prev.anchors {
		ix.add(a.kind, a.text, a.count, -(i + 1))
	}
	ix.scanMessages(head)
	anchors := renderAnchors(selectAnchors(ix.ranked(), anchorsBudget))

	recovery := recoverySection(o)
	if len(quotes) == 0 && anchors == "" && recovery == "" {
		return ""
	}

	var sb strings.Builder
	sb.WriteString(contextOpen + "\n")
	if len(quotes) > 0 {
		sb.WriteString(headingUsers + "\n")
		for _, q := range quotes {
			sb.WriteString("<user-message>\n" + tagEscaper.Replace(q) + "\n</user-message>\n")
		}
		sb.WriteString("\n")
	}
	if anchors != "" {
		sb.WriteString(headingAnchors + "\n" + anchors + "\n\n")
	}
	if recovery != "" {
		sb.WriteString(headingRecovery + "\n" + recovery + "\n\n")
	}
	sb.WriteString(contextClose)
	return sb.String()
}

// recoverySection tells the model how to get back what compaction dropped,
// naming only tools that are actually available.
func recoverySection(o Options) string {
	has := map[string]bool{}
	for _, t := range o.RecoveryTools {
		has[t] = true
	}
	var lines []string
	if has["session_search"] {
		line := `- session_search(query="<keywords>") finds earlier messages`
		if o.SessionID != "" {
			line += fmt.Sprintf(`; session_search(session_id=%q, around_message_index=N) reads around a hit in this session`, o.SessionID)
		}
		lines = append(lines, line)
	}
	if has["CCRRetrieve"] {
		lines = append(lines, "- CCRRetrieve(handle) restores a tool output that was shortened to a handle")
	}
	if len(lines) == 0 {
		return ""
	}
	return "The full pre-compaction history is still on disk. To recover details:\n" + strings.Join(lines, "\n")
}

// extractPreviousContext reads the previous compaction out of msgs[0]: the
// model summary, and the carried-over user messages and anchors. ok is false
// when msgs[0] is not a compaction summary message.
func extractPreviousContext(msgs []api.Message) (summary string, prev prevContext, ok bool) {
	if len(msgs) == 0 || msgs[0].Role != "user" {
		return "", prevContext{}, false
	}
	for _, b := range msgs[0].Content {
		if b.Type != "text" || !isSummaryMessage(b.Text) {
			continue
		}
		after := b.Text
		if i := strings.Index(after, "</summary>"); i >= 0 {
			after = after[i+len("</summary>"):]
		}
		return extractSummary(b.Text), parseContextBlock(after), true
	}
	return "", prevContext{}, false
}

// summaryMarker ends every summary message conduit writes (and ended the
// legacy format). A first user message that merely starts with "<summary>"
// lacks it and is treated as ordinary conversation.
const summaryMarker = "Above is a summary of our conversation so far"

func isSummaryMessage(text string) bool {
	text = strings.TrimSpace(text)
	return strings.HasPrefix(text, "<summary>") && strings.Contains(text, summaryMarker)
}

// parseContextBlock parses the <compaction-context> block of a summary
// message. Legacy messages without one yield an empty prevContext.
func parseContextBlock(text string) prevContext {
	start := strings.Index(text, contextOpen)
	if start < 0 {
		return prevContext{}
	}
	body := text[start+len(contextOpen):]
	if end := strings.LastIndex(body, contextClose); end >= 0 {
		body = body[:end]
	}

	var prev prevContext
	for _, m := range userMsgRe.FindAllStringSubmatch(body, -1) {
		prev.users = append(prev.users, m[1])
	}
	rest := body
	if i := strings.LastIndex(body, "</user-message>"); i >= 0 {
		rest = body[i+len("</user-message>"):]
	}
	a := strings.Index(rest, headingAnchors)
	if a < 0 {
		return prev
	}
	section := rest[a+len(headingAnchors):]
	if r := strings.Index(section, headingRecovery); r >= 0 {
		section = section[:r]
	}
	kind := ""
	for _, line := range strings.Split(section, "\n") {
		line = strings.TrimRight(line, " \t")
		switch {
		case strings.HasPrefix(line, "### "):
			kind = strings.TrimPrefix(line, "### ")
		case strings.HasPrefix(line, "- ") && kind != "":
			item, count := strings.TrimPrefix(line, "- "), 1
			if m := countSuffixRe.FindStringSubmatch(item); m != nil {
				count, _ = strconv.Atoi(m[1])
				item = strings.TrimSuffix(item, m[0])
			}
			prev.anchors = append(prev.anchors, anchor{kind: kind, text: item, count: count})
		}
	}
	return prev
}
