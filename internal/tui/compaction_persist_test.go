package tui

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/icehunter/conduit/internal/agent"
	"github.com/icehunter/conduit/internal/api"
	"github.com/icehunter/conduit/internal/commands"
	"github.com/icehunter/conduit/internal/compact"
	"github.com/icehunter/conduit/internal/session"
	"github.com/icehunter/conduit/internal/sessionsearch"
	"github.com/icehunter/conduit/internal/tool"
)

func tuiText(role, s string) api.Message {
	return api.Message{Role: role, Content: []api.ContentBlock{{Type: "text", Text: s}}}
}

func msgText(m api.Message) string {
	var sb strings.Builder
	for _, b := range m.Content {
		sb.WriteString(b.Text)
	}
	return sb.String()
}

// sseReply is a complete text response on the Anthropic stream.
func sseReply(text string) string {
	return "event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"id\":\"m\",\"type\":\"message\",\"role\":\"assistant\",\"model\":\"m\",\"content\":[],\"stop_reason\":null,\"usage\":{\"input_tokens\":10,\"output_tokens\":0}}}\n\n" +
		"event: content_block_start\ndata: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"text\",\"text\":\"\"}}\n\n" +
		"event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"" + text + "\"}}\n\n" +
		"event: content_block_stop\ndata: {\"type\":\"content_block_stop\",\"index\":0}\n\n" +
		"event: message_delta\ndata: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"end_turn\"},\"usage\":{\"output_tokens\":5}}\n\n" +
		"event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n"
}

// scriptedAnthropic answers each request with the next canned response.
func scriptedAnthropic(t *testing.T, responses ...func(http.ResponseWriter)) *api.Client {
	t.Helper()
	var mu sync.Mutex
	n := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.ReadAll(r.Body)
		mu.Lock()
		i := min(n, len(responses)-1)
		n++
		mu.Unlock()
		responses[i](w)
	}))
	t.Cleanup(srv.Close)
	return api.NewClient(api.Config{BaseURL: srv.URL, AuthToken: "t"}, srv.Client())
}

func overflowResponse(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusBadRequest)
	_, _ = io.WriteString(w, `{"type":"error","error":{"type":"invalid_request_error","message":"prompt is too long: 210000 tokens > 200000 maximum"}}`)
}

func streamResponse(body string) func(http.ResponseWriter) {
	return func(w http.ResponseWriter) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, body)
	}
}

// TestCompactionPersistsAndResumes runs a real loop through a reactive
// compaction with the TUI's event handler and a temp session, then plays the
// events into the model the way Bubble Tea would. It covers bug 5 (auto and
// reactive compaction never reached the session file) end to end: the file is
// written before the TUI hears of the event, the model's history and
// persisted count follow it, later messages append after the compaction, and
// /resume restores the summary and the verbatim tail instead of the
// pre-compaction conversation.
func TestCompactionPersistsAndResumes(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("CONDUIT_CONFIG_DIR", home+"/.conduit")
	sess, err := session.New(t.TempDir(), "sess-compact")
	if err != nil {
		t.Fatalf("session.New: %v", err)
	}

	before := []api.Message{
		tuiText("user", "first question"),
		tuiText("assistant", "first answer"),
		tuiText("user", "second question"),
		tuiText("assistant", "second answer"),
		tuiText("user", "go on"),
	}
	for _, msg := range before[:4] {
		if err := sess.AppendMessage(msg); err != nil {
			t.Fatal(err)
		}
	}

	client := scriptedAnthropic(t,
		overflowResponse,
		streamResponse(sseReply("<summary>EARLIER WORK SUMMARY</summary>")),
		streamResponse(sseReply("all done")),
	)
	lp := agent.NewLoop(client, tool.NewRegistry(), agent.LoopConfig{
		Model: "m", MaxTokens: 1024, MaxTurns: 10, SessionID: sess.ID,
		Sleep: func(context.Context, time.Duration) error { return nil },
	})

	m := idleModel()
	m.cfg.Session = sess
	m.history = append([]api.Message(nil), before...)
	m.persistedCount = 4 // the new prompt is not written until the turn ends
	m.turnID = 1
	m.running = true

	var sent []tea.Msg
	var onDisk []api.Message // the session file as the compaction event was sent
	handler := m.loopEventHandler(func(msg tea.Msg) {
		if am, ok := msg.(agentMsg); ok && am.event.Type == agent.EventCompacted {
			onDisk, _ = session.LoadMessages(sess.FilePath)
		}
		sent = append(sent, msg)
	}, new(api.Usage), new(int))

	final, err := lp.Run(context.Background(), m.history, handler)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	if len(onDisk) == 0 || !strings.Contains(msgText(onDisk[0]), "EARLIER WORK SUMMARY") {
		t.Fatalf("session at the time of the event = %d messages, first %q; want the compaction already written", len(onDisk), firstText(onDisk))
	}

	var compactedEv agent.LoopEvent
	var model tea.Model = m
	for _, msg := range sent {
		if am, ok := msg.(agentMsg); ok && am.event.Type == agent.EventCompacted {
			compactedEv = am.event
		}
		model, _ = model.Update(msg)
	}
	m = model.(Model)

	if compactedEv.CompactKind != compact.KindReactive || compactedEv.CompactTailLen < 1 {
		t.Fatalf("event = kind %q tail %d", compactedEv.CompactKind, compactedEv.CompactTailLen)
	}
	// The trailing user message ("go on") is not in the file yet.
	if len(m.history) != len(compactedEv.History) || m.persistedCount != len(compactedEv.History)-1 {
		t.Errorf("after the event history = %d, persisted = %d, want %d and %d", len(m.history), m.persistedCount, len(compactedEv.History), len(compactedEv.History)-1)
	}
	var row string
	for _, msg := range m.messages {
		if strings.HasPrefix(msg.Content, "Context compacted") {
			row = msg.Content
		}
	}
	for _, want := range []string{"(reactive)", "kept " + itoa(compactedEv.CompactTailLen) + " recent messages", "summary by m"} {
		if !strings.Contains(row, want) {
			t.Errorf("row %q lacks %q", row, want)
		}
	}

	m, _ = m.handleAgentDone(agentDoneMsg{turnID: 1, history: final})
	resumed, err := session.LoadMessages(sess.FilePath)
	if err != nil {
		t.Fatal(err)
	}
	if len(resumed) != len(final) {
		t.Fatalf("resumed %d messages, final history has %d:\nresumed: %v\nfinal: %v", len(resumed), len(final), texts(resumed), texts(final))
	}
	for i := range final {
		if msgText(resumed[i]) != msgText(final[i]) {
			t.Errorf("message %d: resumed %q, want %q", i, msgText(resumed[i]), msgText(final[i]))
		}
	}
	if !strings.Contains(msgText(resumed[0]), "EARLIER WORK SUMMARY") || msgText(resumed[len(resumed)-1]) != "all done" {
		t.Errorf("resume = %v, want the summary first and the new reply last", texts(resumed))
	}
	// "first question" was summarized; "first answer" starts the verbatim tail.
	for _, msg := range resumed {
		if msgText(msg) == "first question" {
			t.Error("resume restored a pre-compaction message that the summary replaced")
		}
	}
	if len(m.history) != len(final) || m.persistedCount != len(final) {
		t.Errorf("history = %d, persisted = %d, want %d", len(m.history), m.persistedCount, len(final))
	}

	// /resume into a fresh model restores the same conversation.
	fresh := idleModel()
	fresh, _ = fresh.handleResumeLoad(resumeLoadMsg{msgs: resumed, filePath: sess.FilePath})
	if len(fresh.history) != len(final) || !strings.Contains(msgText(fresh.history[0]), "EARLIER WORK SUMMARY") {
		t.Errorf("resumed model history = %v", texts(fresh.history))
	}
}

func firstText(msgs []api.Message) string {
	if len(msgs) == 0 {
		return ""
	}
	return msgText(msgs[0])
}

func texts(msgs []api.Message) []string {
	out := make([]string, len(msgs))
	for i, m := range msgs {
		out[i] = m.Role + ": " + msgText(m)
	}
	return out
}

func itoa(n int) string { return strconv.Itoa(n) }

// compactDone runs the command applyCompactResult returned and returns the
// compactDoneMsg it produced.
func compactDone(t *testing.T, cmd tea.Cmd) compactDoneMsg {
	t.Helper()
	var find func(tea.Cmd) (compactDoneMsg, bool)
	find = func(c tea.Cmd) (compactDoneMsg, bool) {
		if c == nil {
			return compactDoneMsg{}, false
		}
		switch msg := c().(type) {
		case compactDoneMsg:
			return msg, true
		case tea.BatchMsg:
			for _, sub := range msg {
				if done, ok := find(sub); ok {
					return done, true
				}
			}
		}
		return compactDoneMsg{}, false
	}
	done, ok := find(cmd)
	if !ok {
		t.Fatal("the command produced no compactDoneMsg")
	}
	return done
}

func TestManualCompact_AppendsToSession(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("CONDUIT_CONFIG_DIR", home+"/.conduit")
	sess, err := session.New(t.TempDir(), "sess-manual")
	if err != nil {
		t.Fatal(err)
	}
	hist := []api.Message{
		tuiText("user", "first question"),
		tuiText("assistant", "first answer"),
		tuiText("user", "second question"),
		tuiText("assistant", "second answer"),
	}
	for _, msg := range hist {
		if err := sess.AppendMessage(msg); err != nil {
			t.Fatal(err)
		}
	}

	m := idleModel()
	m.cfg.Session = sess
	m.cfg.APIClient = scriptedAnthropic(t, streamResponse(sseReply("<summary>MANUAL SUMMARY</summary>")))
	m.history = hist
	m.persistedCount = len(hist)

	m, cmd := m.applyCompactResult(commands.Result{})
	done := compactDone(t, cmd)
	if done.err != nil || done.persistErr != nil {
		t.Fatalf("compactDoneMsg = %+v", done)
	}
	m, _ = m.handleCompactDone(done)

	if len(m.history) != len(done.newHistory) || m.persistedCount != len(done.newHistory) {
		t.Errorf("history = %d, persisted = %d, want %d", len(m.history), m.persistedCount, len(done.newHistory))
	}
	resumed, err := session.LoadMessages(sess.FilePath)
	if err != nil {
		t.Fatal(err)
	}
	if len(resumed) != len(done.newHistory) || !strings.Contains(firstText(resumed), "MANUAL SUMMARY") {
		t.Errorf("resumed = %v, want the compacted history %d messages", texts(resumed), len(done.newHistory))
	}
	// The older messages stay in the file for session_search.
	raw, err := os.ReadFile(sess.FilePath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), "first question") {
		t.Error("manual /compact rewrote the file; the older messages should stay on disk")
	}
}

func TestManualCompact_EmptyHistoryIsRejected(t *testing.T) {
	m := idleModel()
	m.cfg.APIClient = scriptedAnthropic(t, streamResponse(sseReply("never")))
	m, cmd := m.applyCompactResult(commands.Result{})
	if cmd != nil || m.running {
		t.Errorf("empty history started a compaction (running=%v)", m.running)
	}
	if last := m.messages[len(m.messages)-1]; last.Content != "Nothing to compact." {
		t.Errorf("last row = %q", last.Content)
	}
}

// TestHandleCompactDone_NothingToCompact: a history that is all verbatim tail
// is not a failure.
func TestHandleCompactDone_NothingToCompact(t *testing.T) {
	_, err := compact.CompactWithOptions(context.Background(), nil, []api.Message{tuiText("assistant", "only")}, compact.Options{})
	if !compact.IsNothingToCompact(err) {
		t.Fatalf("setup: err = %v", err)
	}
	m := idleModel()
	m.running = true
	m.history = []api.Message{tuiText("assistant", "only")}
	m2, _ := m.handleCompactDone(compactDoneMsg{err: err})
	last := m2.messages[len(m2.messages)-1]
	if last.Role != RoleSystem || last.Content != "Nothing to compact yet." {
		t.Errorf("last row = %+v, want a system row 'Nothing to compact yet.'", last)
	}
	if m2.running || len(m2.history) != 1 {
		t.Errorf("running=%v history=%d, want idle with the history untouched", m2.running, len(m2.history))
	}
}

func TestHandleCompactDone_FallbackNotice(t *testing.T) {
	m := idleModel()
	m.running = true
	m2, _ := m.handleCompactDone(compactDoneMsg{
		newHistory: []api.Message{tuiText("user", "<summary>local</summary>")},
		summary:    "local",
		fallback:   true,
	})
	var notice bool
	for _, msg := range m2.messages {
		notice = notice || msg.Content == compact.FallbackNotice
	}
	if !notice {
		t.Errorf("no %q row", compact.FallbackNotice)
	}
}

// TestManualCompact_SummarizerReturnsNothing is bug 3 end to end: a summary
// with no usable text used to give an empty history, which /compact then wrote
// over the session. The local summary stands in and the conversation survives.
func TestManualCompact_SummarizerReturnsNothing(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("CONDUIT_CONFIG_DIR", home+"/.conduit")
	sess, err := session.New(t.TempDir(), "sess-wipe")
	if err != nil {
		t.Fatal(err)
	}
	hist := []api.Message{
		tuiText("user", "first question"),
		tuiText("assistant", "first answer"),
		tuiText("user", "second question"),
		tuiText("assistant", "second answer"),
	}
	for _, msg := range hist {
		if err := sess.AppendMessage(msg); err != nil {
			t.Fatal(err)
		}
	}
	m := idleModel()
	m.cfg.Session = sess
	m.cfg.APIClient = scriptedAnthropic(t, streamResponse(sseReply("")))
	m.history = hist
	m.persistedCount = len(hist)

	m, cmd := m.applyCompactResult(commands.Result{})
	done := compactDone(t, cmd)
	if done.err != nil || !done.fallback || len(done.newHistory) == 0 {
		t.Fatalf("compactDoneMsg = err %v fallback %v history %d, want the local summary", done.err, done.fallback, len(done.newHistory))
	}
	m, _ = m.handleCompactDone(done)
	if len(m.history) == 0 {
		t.Fatal("the history was wiped")
	}
	resumed, err := session.LoadMessages(sess.FilePath)
	if err != nil {
		t.Fatal(err)
	}
	if len(resumed) == 0 || !strings.Contains(firstText(resumed), "first question") {
		t.Errorf("session after /compact = %v, want the summary with the user's messages", texts(resumed))
	}
}

func authFail(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusUnauthorized)
	_, _ = io.WriteString(w, `{"type":"error","error":{"type":"authentication_error","message":"bad"}}`)
}

// playEvents feeds what the loop handler sent into the model, as Bubble Tea
// would, running the session-writing commands Update returns (not the batches
// of window-title commands) and feeding their results back.
func playEvents(m Model, sent []tea.Msg) Model {
	var model tea.Model = m
	var later []tea.Cmd // commands finish after the events already queued
	for _, msg := range sent {
		var cmd tea.Cmd
		model, cmd = model.Update(msg)
		if _, isSave := msg.(compactSaveFailedMsg); isSave && cmd != nil {
			later = append(later, cmd)
		}
	}
	for _, cmd := range later {
		model, _ = model.Update(cmd())
	}
	return model.(Model)
}

// TestReactiveCompactionThenHardError: the compaction block must not carry the
// pending prompt. Otherwise the failed turn leaves it in the file, persistedCount
// runs one ahead of the history the error path keeps, and the next prompt is
// never written.
func TestReactiveCompactionThenHardError(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("CONDUIT_CONFIG_DIR", home+"/.conduit")
	sess, err := session.New(t.TempDir(), "sess-harderr")
	if err != nil {
		t.Fatal(err)
	}
	before := []api.Message{
		tuiText("user", "first question"), tuiText("assistant", "first answer"),
		tuiText("user", "second question"), tuiText("assistant", "second answer"),
		tuiText("user", "FAILED PROMPT"),
	}
	for _, msg := range before[:4] {
		if err := sess.AppendMessage(msg); err != nil {
			t.Fatal(err)
		}
	}
	client := scriptedAnthropic(t, overflowResponse,
		streamResponse(sseReply("<summary>SUM</summary>")),
		authFail,
		streamResponse(sseReply("reply to new")))
	lp := agent.NewLoop(client, tool.NewRegistry(), agent.LoopConfig{Model: "m", MaxTokens: 1024, MaxTurns: 10, SessionID: sess.ID,
		Sleep: func(context.Context, time.Duration) error { return nil }})
	m := idleModel()
	m.cfg.Session = sess
	m.history = append([]api.Message(nil), before...)
	m.persistedCount = 4
	m.turnID = 1
	m.running = true

	var sent []tea.Msg
	h := m.loopEventHandler(func(msg tea.Msg) { sent = append(sent, msg) }, new(api.Usage), new(int))
	final, runErr := lp.Run(context.Background(), m.history, h)
	if runErr == nil {
		t.Fatal("expected the hard error")
	}
	m = playEvents(m, sent)
	m, _ = m.handleAgentDone(agentDoneMsg{turnID: 1, history: final, err: runErr})

	disk, _ := session.LoadMessages(sess.FilePath)
	for _, text := range texts(disk) {
		if strings.Contains(text, "FAILED PROMPT") {
			t.Errorf("the failed prompt reached the file: %v", texts(disk))
		}
	}

	// The next turn.
	m.history = append(m.history, tuiText("user", "NEW PROMPT"))
	m.turnID = 2
	final2, err2 := lp.Run(context.Background(), m.history, func(agent.LoopEvent) {})
	m, _ = m.handleAgentDone(agentDoneMsg{turnID: 2, history: final2, err: err2})
	disk, _ = session.LoadMessages(sess.FilePath)
	var haveNew, haveReply bool
	for _, text := range texts(disk) {
		haveNew = haveNew || strings.Contains(text, "NEW PROMPT")
		haveReply = haveReply || strings.Contains(text, "reply to new")
	}
	if !haveNew || !haveReply {
		t.Errorf("session after the next turn = %v, want NEW PROMPT and its reply", texts(disk))
	}
}

func TestPersistableCompaction(t *testing.T) {
	sum, a, u := tuiText("user", "<summary>"), tuiText("assistant", "a"), tuiText("user", "u")
	tests := []struct {
		name     string
		in       []api.Message
		tail     int
		wantLen  int
		wantTail int
	}{
		{"trailing user is trimmed", []api.Message{sum, a, u}, 2, 2, 1},
		{"trailing assistant stays", []api.Message{sum, u, a}, 2, 3, 2},
		{"summary alone stays", []api.Message{sum}, 0, 1, 0},
		{"empty", nil, 0, 0, 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, tail := persistableCompaction(tt.in, tt.tail)
			if len(got) != tt.wantLen || tail != tt.wantTail {
				t.Errorf("persistableCompaction = %d messages, tail %d; want %d, %d", len(got), tail, tt.wantLen, tt.wantTail)
			}
		})
	}
}

func TestPersistNewMessages_ClampsCount(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("CONDUIT_CONFIG_DIR", home+"/.conduit")
	sess, _ := session.New(t.TempDir(), "sess-clamp")
	m := idleModel()
	m.cfg.Session = sess
	m.persistedCount = 9
	m.persistNewMessages([]api.Message{tuiText("user", "a"), tuiText("assistant", "b")})
	if m.persistedCount != 2 {
		t.Errorf("persistedCount = %d, want clamped to 2", m.persistedCount)
	}
}

// The compaction block carries the provider label of the final assistant
// message, like the turn's own persistence does.
func TestCompactionPersistsProviderLabels(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("CONDUIT_CONFIG_DIR", home+"/.conduit")
	sess, _ := session.New(t.TempDir(), "sess-labels")
	m := idleModel()
	m.cfg.Session = sess
	m.turnProviderKind, m.turnProvider = "anthropic-api", "work"
	var sent []tea.Msg
	h := m.loopEventHandler(func(msg tea.Msg) { sent = append(sent, msg) }, new(api.Usage), new(int))
	h(agent.LoopEvent{
		Type: agent.EventCompacted, CompactKind: compact.KindAuto, CompactSummary: "s", CompactModel: "m", CompactTailLen: 1,
		History: []api.Message{tuiText("user", "<summary>s</summary>"), tuiText("assistant", "final")},
	})
	raw, err := os.ReadFile(sess.FilePath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), `"work"`) {
		t.Errorf("session file lacks the provider label:\n%s", raw)
	}
	m = playEvents(m, sent)
	if m.history[1].Provider != "work" {
		t.Errorf("model history label = %q", m.history[1].Provider)
	}
}

func TestCompactionSaveFailure(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("CONDUIT_CONFIG_DIR", home+"/.conduit")
	hist := []api.Message{tuiText("user", "<summary>s</summary>"), tuiText("assistant", "tail")}

	t.Run("unwritable file keeps persistedCount and says so", func(t *testing.T) {
		sess, _ := session.New(t.TempDir(), "sess-bad")
		sess.FilePath = t.TempDir() // a non-empty directory: neither append nor rewrite can work
		if err := os.WriteFile(sess.FilePath+"/keep", []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
		m := idleModel()
		m.cfg.Session = sess
		m.persistedCount = 7
		var sent []tea.Msg
		h := m.loopEventHandler(func(msg tea.Msg) { sent = append(sent, msg) }, new(api.Usage), new(int))
		h(agent.LoopEvent{Type: agent.EventCompacted, CompactSummary: "s", CompactTailLen: 1, History: hist})
		m = playEvents(m, sent)
		if m.persistedCount != 7 {
			t.Errorf("persistedCount = %d, want it left at 7 when nothing was saved", m.persistedCount)
		}
		var errRow bool
		for _, msg := range m.messages {
			errRow = errRow || (msg.Role == RoleError && strings.Contains(msg.Content, "Could not save the compaction"))
		}
		if !errRow {
			t.Error("no error row for the failed save")
		}
	})
	t.Run("a failed append falls back to rewriting the file", func(t *testing.T) {
		sess, _ := session.New(t.TempDir(), "sess-rewrite")
		_ = sess.AppendMessage(tuiText("user", "old"))
		m := idleModel()
		m.cfg.Session = sess
		m, cmd := m.handleCompactSaveFailed(compactSaveFailedMsg{err: io.ErrClosedPipe, summary: "s", history: hist})
		if cmd == nil || !m.compactUnsaved {
			t.Fatal("the rewrite should be a command, with the count held until it reports")
		}
		m = m.handleCompactRewriteDone(cmd().(compactRewriteDoneMsg))
		if m.compactUnsaved || m.persistedCount != len(hist) {
			t.Errorf("after the rewrite: unsaved %v, persisted %d, want false and %d", m.compactUnsaved, m.persistedCount, len(hist))
		}
		got, _ := session.LoadMessages(sess.FilePath)
		if len(got) != 2 || firstText(got) != "<summary>s</summary>" {
			t.Errorf("file after the rewrite = %v", texts(got))
		}
	})
}

// A turn that fails after a tool round ends its history in a tool_result,
// which must stay (it answers a tool_use). The next prompt is still written.
func TestErrorPathPersistsNextPrompt(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("CONDUIT_CONFIG_DIR", home+"/.conduit")
	use := api.Message{Role: "assistant", Content: []api.ContentBlock{{Type: "tool_use", ID: "t1", Name: "Read", Input: map[string]any{}}}}
	res := api.Message{Role: "user", Content: []api.ContentBlock{{Type: "tool_result", ToolUseID: "t1", ResultContent: "ok"}}}

	tests := []struct {
		name    string
		failed  []api.Message // history of the failed turn
		wantOn  []string      // texts on disk after it (prompts and replies only)
		wantLen int           // m.history length after it
	}{
		{"fails after a tool round", []api.Message{tuiText("user", "p1"), use, res}, []string{"p1"}, 3},
		{"fails before any reply", []api.Message{tuiText("user", "p1")}, nil, 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			sess, err := session.New(t.TempDir(), "sess-"+strings.ReplaceAll(tt.name, " ", "-"))
			if err != nil {
				t.Fatal(err)
			}
			m := idleModel()
			m.cfg.Session = sess
			m.history = []api.Message{tuiText("user", "p1")}
			m.turnID = 1
			m, _ = m.handleAgentDone(agentDoneMsg{turnID: 1, history: tt.failed, err: errors.New("agent: drain: boom")})
			if len(m.history) != tt.wantLen || m.persistedCount != len(m.history) {
				t.Fatalf("after the failure history = %d, persisted = %d, want both %d", len(m.history), m.persistedCount, tt.wantLen)
			}

			m.history = append(m.history, tuiText("user", "SECONDPROMPT"))
			full := append(append([]api.Message(nil), m.history...), tuiText("assistant", "answer2"))
			m.turnID = 2
			m, _ = m.handleAgentDone(agentDoneMsg{turnID: 2, history: full})
			onDisk, _ := session.LoadMessages(sess.FilePath)
			var got []string
			for _, msg := range onDisk {
				if t := msgText(msg); t != "" {
					got = append(got, t)
				}
			}
			want := append(append([]string(nil), tt.wantOn...), "SECONDPROMPT", "answer2")
			if strings.Join(got, "|") != strings.Join(want, "|") {
				t.Errorf("on disk = %v, want %v", got, want)
			}
		})
	}
}

func TestTailCopies(t *testing.T) {
	tests := []struct {
		name                    string
		preLen, onDisk, tailLen int
		want                    int
	}{
		{"whole tail already on disk", 6, 6, 3, 3},
		{"none of the tail on disk (end_turn)", 6, 3, 3, 0},
		{"part of the tail on disk", 6, 4, 3, 1},
		{"nothing on disk", 6, 0, 3, 0},
		{"more on disk than the history (clamped)", 4, 9, 2, 2},
		{"no tail", 4, 4, 0, 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tailCopies(tt.preLen, tt.onDisk, tt.tailLen); got != tt.want {
				t.Errorf("tailCopies(%d, %d, %d) = %d, want %d", tt.preLen, tt.onDisk, tt.tailLen, got, tt.want)
			}
		})
	}
}

// matchCount counts indexed messages containing q.
func matchCount(t *testing.T, db *sessionsearch.DB, q string) int {
	t.Helper()
	windows, err := db.Search(q, "", 20)
	if err != nil {
		t.Fatal(err)
	}
	n := 0
	for _, w := range windows {
		for _, msg := range w.Messages {
			if msg.IsMatch {
				n++
			}
		}
	}
	return n
}

// The messages of the turn that triggered an end_turn compaction reach the
// file only inside the compaction block; session_search must index them there,
// and must not index the tail messages that were already on disk twice.
func TestCompactionTailIsIndexed(t *testing.T) {
	for _, tt := range []struct {
		name      string
		persisted int // of the 4 pre-compaction messages, how many were on disk
		wantGamma int
		wantAlpha int
	}{
		{"turn not yet persisted", 2, 1, 1},
		{"everything already persisted", 4, 1, 1},
	} {
		t.Run(tt.name, func(t *testing.T) {
			home := t.TempDir()
			t.Setenv("HOME", home)
			t.Setenv("CONDUIT_CONFIG_DIR", home+"/.conduit")
			sess, err := session.New(t.TempDir(), "sess-index")
			if err != nil {
				t.Fatal(err)
			}
			pre := []api.Message{tuiText("user", "alphaprobe"), tuiText("assistant", "betaprobe"), tuiText("user", "gammaprobe"), tuiText("assistant", "deltaprobe")}
			for _, msg := range pre[:tt.persisted] {
				if err := sess.AppendMessage(msg); err != nil {
					t.Fatal(err)
				}
			}
			m := idleModel()
			m.cfg.Session = sess
			m.persistedCount = tt.persisted
			h := m.loopEventHandler(func(tea.Msg) {}, new(api.Usage), new(int))
			h(agent.LoopEvent{
				Type: agent.EventCompacted, CompactKind: compact.KindAuto, CompactSummary: "S", CompactModel: "m",
				CompactTailLen: 2, CompactPreLen: len(pre),
				History: []api.Message{tuiText("user", "<summary>S</summary>"), pre[2], pre[3]},
			})
			db, err := sessionsearch.Open(":memory:")
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = db.Close() })
			if err := db.Index(filepath.Dir(sess.FilePath)); err != nil {
				t.Fatal(err)
			}
			for q, want := range map[string]int{"gammaprobe": tt.wantGamma, "deltaprobe": 1, "alphaprobe": tt.wantAlpha} {
				if got := matchCount(t, db, q); got != want {
					t.Errorf("%s indexed %d times, want %d", q, got, want)
				}
			}
		})
	}
}

// Manual /compact summarizes through the loop's background target, so it
// honors the background role like auto-compaction does.
func TestCompactClientAndModel_UsesBackgroundTarget(t *testing.T) {
	active := api.NewClient(api.Config{BaseURL: "http://127.0.0.1:1", AuthToken: "t"}, nil)
	role := api.NewClient(api.Config{BaseURL: "http://127.0.0.1:2", APIKey: "k", ProviderKind: api.ProviderKindOpenAICompatible}, nil)
	lp := agent.NewLoop(active, tool.NewRegistry(), agent.LoopConfig{
		Model: "claude-sonnet-4-6",
		RoleResolver: func(string) (string, *api.Client, bool) {
			return "local-summarizer", role, true
		},
	})
	m := idleModel()
	m.cfg.Loop = lp
	m.cfg.APIClient = active
	c, model, err := m.compactClientAndModel()
	if err != nil || c != role || model != "local-summarizer" {
		t.Errorf("compactClientAndModel = (%p, %q, %v), want the background role's client and model", c, model, err)
	}
}

// /clear keeps appending to the same session file. The next turn must be
// written (persistedCount back to 0) and /resume must not bring the cleared
// conversation back; session_search still finds it.
func TestClearResetsPersistence(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("CONDUIT_CONFIG_DIR", home+"/.conduit")
	sess, err := session.New(t.TempDir(), "sess-clear")
	if err != nil {
		t.Fatal(err)
	}
	m := idleModel()
	m.cfg.Session = sess
	m.history = []api.Message{tuiText("user", "oldquestion"), tuiText("assistant", "oldanswer")}
	m.persistNewMessages(m.history)

	m, cmd := m.applyCommandResult(commands.Result{Type: "clear"})
	if m.persistedCount != 0 || len(m.history) != 0 || cmd == nil {
		t.Fatalf("after /clear persisted=%d history=%d cmd=%v", m.persistedCount, len(m.history), cmd != nil)
	}
	if done := cmd().(sessionResetDoneMsg); done.err != nil {
		t.Fatal(done.err)
	}

	m.history = []api.Message{tuiText("user", "newquestion"), tuiText("assistant", "newanswer")}
	m.persistNewMessages(m.history)
	got, err := session.LoadMessages(sess.FilePath)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(texts(got), "|") != "user: newquestion|assistant: newanswer" {
		t.Errorf("resumed = %v, want only the conversation after /clear", texts(got))
	}

	db, err := sessionsearch.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if err := db.Index(filepath.Dir(sess.FilePath)); err != nil {
		t.Fatal(err)
	}
	for _, q := range []string{"oldquestion", "newquestion", "newanswer"} {
		if matchCount(t, db, q) != 1 {
			t.Errorf("%s indexed %d times, want 1 (the cleared conversation stays searchable and the new one is not skipped)", q, matchCount(t, db, q))
		}
	}
}
