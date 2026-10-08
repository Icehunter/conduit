package compact

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"unicode/utf8"

	"github.com/icehunter/conduit/internal/api"
)

// fakeSummarizer serves one canned SSE summarization response and records what
// it was sent.
type fakeSummarizer struct {
	mu       sync.Mutex
	text     string
	stop     string
	noStop   bool // omit message_stop (truncated stream)
	prompt   string
	system   string
	hints    []api.WireHints
	requests int
}

func (f *fakeSummarizer) client(t *testing.T) *api.Client {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var req struct {
			System   []struct{ Text string } `json:"system"`
			Messages []struct {
				Content []struct{ Text string } `json:"content"`
			} `json:"messages"`
		}
		_ = json.Unmarshal(body, &req)
		f.mu.Lock()
		f.requests++
		if len(req.System) > 0 {
			f.system = req.System[0].Text
		}
		if len(req.Messages) > 0 && len(req.Messages[0].Content) > 0 {
			f.prompt = req.Messages[0].Content[0].Text
		}
		f.mu.Unlock()
		w.Header().Set("Content-Type", "text/event-stream")
		esc, _ := json.Marshal(f.text)
		fmt.Fprint(w, "event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"id\":\"m\",\"usage\":{\"input_tokens\":123,\"cache_read_input_tokens\":7}}}\n\n")
		fmt.Fprint(w, "event: content_block_start\ndata: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"text\",\"text\":\"\"}}\n\n")
		fmt.Fprintf(w, "event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":%s}}\n\n", esc)
		fmt.Fprint(w, "event: content_block_stop\ndata: {\"type\":\"content_block_stop\",\"index\":0}\n\n")
		stop := f.stop
		if stop == "" {
			stop = "end_turn"
		}
		fmt.Fprintf(w, "event: message_delta\ndata: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":%q},\"usage\":{\"output_tokens\":45}}\n\n", stop)
		if !f.noStop {
			fmt.Fprint(w, "event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n")
		}
	}))
	t.Cleanup(srv.Close)
	return api.NewClient(api.Config{
		BaseURL: srv.URL,
		APIKey:  "k",
		RequestHeaders: func(req *api.MessageRequest) map[string]string {
			f.mu.Lock()
			defer f.mu.Unlock()
			if req.Hints != nil {
				f.hints = append(f.hints, *req.Hints)
			}
			return nil
		},
	}, srv.Client())
}

func umsg(s string) api.Message {
	return api.Message{Role: "user", Content: []api.ContentBlock{{Type: "text", Text: s}}}
}
func amsg(s string) api.Message {
	return api.Message{Role: "assistant", Content: []api.ContentBlock{{Type: "text", Text: s}}}
}
func toolCall(id, name string, in map[string]any) api.Message {
	return api.Message{Role: "assistant", Content: []api.ContentBlock{{Type: "tool_use", ID: id, Name: name, Input: in}}}
}
func toolResult(id, out string, isErr bool) api.Message {
	return api.Message{Role: "user", Content: []api.ContentBlock{{Type: "tool_result", ToolUseID: id, ResultContent: out, IsError: isErr}}}
}

func convo() []api.Message {
	return []api.Message{
		umsg("please fix internal/foo/bar.go"),
		toolCall("t1", "Read", map[string]any{"file_path": "/repo/internal/foo/bar.go"}),
		toolResult("t1", "package foo", false),
		amsg("done reading"),
		umsg("now run the tests"),
		amsg("running"),
	}
}

func onlyText(m api.Message) string {
	var sb strings.Builder
	for _, b := range m.Content {
		sb.WriteString(b.Text)
	}
	return sb.String()
}

func TestCompactWithOptions_Outcomes(t *testing.T) {
	tests := []struct {
		name         string
		text         string
		stop         string
		noStop       bool
		wantErr      bool
		wantFallback bool
		wantSummary  string // substring
	}{
		{name: "normal", text: "<analysis>a</analysis><summary>REAL SUMMARY</summary>", wantSummary: "REAL SUMMARY"},
		{name: "no tag uses rest without analysis", text: "<analysis>scratch</analysis>just prose here", wantSummary: "just prose here"},
		{name: "unclosed summary tag", text: "<summary>cut off text", wantSummary: "cut off text"},
		{name: "analysis only falls back", text: "<analysis>only scratch</analysis>", wantFallback: true, wantSummary: "Automatic fallback summary"},
		{name: "unclosed analysis falls back", text: "<analysis>never closed", wantFallback: true, wantSummary: "Automatic fallback summary"},
		{name: "empty summary tags fall back", text: "<summary>  </summary>", wantFallback: true, wantSummary: "Automatic fallback summary"},
		{name: "empty output falls back", text: "", wantFallback: true, wantSummary: "Automatic fallback summary"},
		{name: "max_tokens accepted and marked", text: "<summary>partial</summary>", stop: "max_tokens", wantSummary: "[summary truncated]"},
		{name: "truncated stream is an error", text: "<summary>partial", noStop: true, wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := &fakeSummarizer{text: tt.text, stop: tt.stop, noStop: tt.noStop}
			res, err := CompactWithOptions(context.Background(), f.client(t), convo(), Options{Kind: KindAuto})
			if tt.wantErr {
				if err == nil {
					t.Fatal("expected error")
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			// Invariant: a nil error means a non-empty history (bug #3).
			if len(res.NewHistory) < 1 {
				t.Fatal("nil error but empty NewHistory")
			}
			if res.Fallback != tt.wantFallback {
				t.Errorf("Fallback = %v, want %v", res.Fallback, tt.wantFallback)
			}
			if !strings.Contains(res.Summary, tt.wantSummary) {
				t.Errorf("Summary = %q, want substring %q", res.Summary, tt.wantSummary)
			}
			first := onlyText(res.NewHistory[0])
			if !strings.HasPrefix(first, "<summary>\n") || !strings.Contains(first, "Please continue from here.") {
				t.Errorf("summary message malformed: %q", first)
			}
		})
	}
}

func TestCompactWithOptions_TailAndUsageAndHints(t *testing.T) {
	f := &fakeSummarizer{text: "<summary>S</summary>"}
	res, err := CompactWithOptions(context.Background(), f.client(t), convo(), Options{Kind: KindAuto, SessionID: "sess-9"})
	if err != nil {
		t.Fatal(err)
	}
	// Default tail budget keeps everything from the first assistant message
	// that fits; the head must hold at least the first user message.
	if res.TailLen < 1 || len(res.NewHistory) != 1+res.TailLen {
		t.Fatalf("TailLen = %d, history = %d", res.TailLen, len(res.NewHistory))
	}
	if res.NewHistory[1].Role != "assistant" {
		t.Errorf("tail must start on an assistant message, got %q", res.NewHistory[1].Role)
	}
	if res.Usage.InputTokens != 123 || res.Usage.OutputTokens != 45 || res.Usage.CacheReadInputTokens != 7 {
		t.Errorf("usage = %+v", res.Usage)
	}
	if len(f.hints) != 1 {
		t.Fatalf("hints = %d", len(f.hints))
	}
	h := f.hints[0]
	if h.RequestClass != "compaction" || h.Compaction != KindAuto || h.SessionID != "sess-9" || h.UserTurn {
		t.Errorf("hints = %+v", h)
	}
	if !strings.Contains(f.system, "summarizing a coding conversation") {
		t.Errorf("system prompt lost its marker phrase")
	}
}

func TestCompactWithOptions_TinyTailBudgetKeepsLastAssistant(t *testing.T) {
	f := &fakeSummarizer{text: "<summary>S</summary>"}
	res, err := CompactWithOptions(context.Background(), f.client(t), convo(), Options{TailTokens: 1})
	if err != nil {
		t.Fatal(err)
	}
	if res.TailLen != 1 || onlyText(res.NewHistory[1]) != "running" {
		t.Errorf("minimal tail should be the last assistant message, got TailLen=%d", res.TailLen)
	}
}

func TestCompactWithOptions_NothingToCompact(t *testing.T) {
	tests := []struct {
		name string
		msgs []api.Message
	}{
		{"previous summary then recent exchange", []api.Message{umsg("<summary>\nold\n</summary>\n\nAbove is a summary of our conversation so far."), amsg("ok")}},
		{"assistant first", []api.Message{amsg("hi"), umsg("x")}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := &fakeSummarizer{text: "<summary>S</summary>"}
			_, err := CompactWithOptions(context.Background(), f.client(t), tt.msgs, Options{})
			if !IsNothingToCompact(err) {
				t.Fatalf("err = %v, want nothing-to-compact", err)
			}
			if f.requests != 0 {
				t.Errorf("made %d API calls for nothing to compact", f.requests)
			}
		})
	}
}

func TestCompactWithOptions_NoAssistantSummarizesEverything(t *testing.T) {
	f := &fakeSummarizer{text: "<summary>S</summary>"}
	res, err := CompactWithOptions(context.Background(), f.client(t), []api.Message{umsg("only a prompt")}, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if res.TailLen != 0 || len(res.NewHistory) != 1 {
		t.Errorf("TailLen=%d history=%d", res.TailLen, len(res.NewHistory))
	}
	if !strings.Contains(onlyText(res.NewHistory[0]), "only a prompt") {
		t.Error("the user's prompt must survive in the verbatim user messages")
	}
}

func TestCompactWithOptions_MergesPreviousCompaction(t *testing.T) {
	f1 := &fakeSummarizer{text: "<summary>FIRST</summary>"}
	first := []api.Message{
		umsg("original goal: fix https://example.com/issue/1 in internal/a/b.go"),
		toolCall("t1", "Bash", map[string]any{"command": "go test ./..."}),
		toolResult("t1", "FAIL: TestX\nsomething failed in internal/a/b.go", true),
		amsg("investigating"),
		umsg("second request about #1234"),
		amsg("working"),
	}
	r1, err := CompactWithOptions(context.Background(), f1.client(t), first, Options{TailTokens: 1})
	if err != nil {
		t.Fatal(err)
	}

	// Continue the conversation, then compact again on top of the first result.
	second := append([]api.Message{}, r1.NewHistory...)
	second = append(second,
		umsg("third request mentions internal/c/d.go and commit abc1234def"),
		amsg("ack"),
		umsg("fourth"),
		amsg("last"),
	)
	f2 := &fakeSummarizer{text: "<summary>SECOND</summary>"}
	r2, err := CompactWithOptions(context.Background(), f2.client(t), second, Options{TailTokens: 1, SessionID: "s1", RecoveryTools: []string{"session_search", "CCRRetrieve"}})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(f2.prompt, "<previous-summary>\nFIRST\n</previous-summary>") {
		t.Errorf("previous summary missing from prompt:\n%s", f2.prompt)
	}
	if strings.Contains(f2.prompt, "User: <summary>") {
		t.Error("the previous summary message was re-summarized as conversation")
	}
	ctx := onlyText(r2.NewHistory[0])
	for _, want := range []string{
		"original goal: fix https://example.com/issue/1", // carried over from compaction 1
		"third request mentions",                         // new
		"internal/a/b.go", "internal/c/d.go", "abc1234def", "#1234",
		"https://example.com/issue/1",
		"FAIL: TestX",
		`session_search(session_id="s1"`, "CCRRetrieve(handle)",
	} {
		if !strings.Contains(ctx, want) {
			t.Errorf("merged context missing %q\n%s", want, ctx)
		}
	}
	// newest first: the third request precedes the original goal
	if strings.Index(ctx, "third request") > strings.Index(ctx, "original goal") {
		t.Error("user messages are not newest-first")
	}
}

func TestCompactWithOptions_RecoveryFooterOnlyListsAvailableTools(t *testing.T) {
	tests := []struct {
		name    string
		tools   []string
		session string
		want    []string
		notWant []string
	}{
		{"none", nil, "s", nil, []string{"Context Recovery", "session_search", "CCRRetrieve"}},
		{"search only", []string{"session_search"}, "abc", []string{`session_search(session_id="abc"`}, []string{"CCRRetrieve"}},
		{"ccr only", []string{"CCRRetrieve"}, "abc", []string{"CCRRetrieve(handle)"}, []string{"session_search"}},
		{"unknown tool ignored", []string{"Bash"}, "abc", nil, []string{"Context Recovery"}},
		{"search without session id", []string{"session_search"}, "", []string{`session_search(query=`}, []string{"session_id="}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := &fakeSummarizer{text: "<summary>S</summary>"}
			res, err := CompactWithOptions(context.Background(), f.client(t), convo(), Options{RecoveryTools: tt.tools, SessionID: tt.session, TailTokens: 1})
			if err != nil {
				t.Fatal(err)
			}
			got := onlyText(res.NewHistory[0])
			for _, w := range tt.want {
				if !strings.Contains(got, w) {
					t.Errorf("missing %q", w)
				}
			}
			for _, w := range tt.notWant {
				if strings.Contains(got, w) {
					t.Errorf("unexpected %q", w)
				}
			}
		})
	}
}

func TestSplitTail_Invariants(t *testing.T) {
	// Build conversations with tool pairs of varied sizes and check the split
	// for many budgets.
	build := func(n int, size int) []api.Message {
		var msgs []api.Message
		msgs = append(msgs, umsg("start"))
		for i := 0; i < n; i++ {
			id := fmt.Sprintf("t%d", i)
			msgs = append(msgs,
				toolCall(id, "Bash", map[string]any{"command": strings.Repeat("c", size)}),
				toolResult(id, strings.Repeat("r", size), false),
			)
			if i%2 == 1 {
				msgs = append(msgs, amsg(strings.Repeat("a", size)), umsg(strings.Repeat("u", size)))
			}
		}
		return append(msgs, amsg("final"))
	}
	for _, size := range []int{10, 400, 4000} {
		for _, n := range []int{1, 2, 5, 12} {
			msgs := build(n, size)
			for _, budget := range []int{0, 1, 50, 500, 5000, 50_000, 1 << 30} {
				k := splitTail(msgs, budget)
				name := fmt.Sprintf("size=%d n=%d budget=%d k=%d", size, n, budget, k)
				if k < 0 || k > len(msgs) {
					t.Fatalf("%s: out of range", name)
				}
				if k == len(msgs) {
					t.Fatalf("%s: conversation ends in an assistant message, tail cannot be empty", name)
				}
				if msgs[k].Role != "assistant" {
					t.Fatalf("%s: split lands on %q, want assistant", name, msgs[k].Role)
				}
				// No tool_result in the tail may lack its tool_use in the tail.
				uses := map[string]bool{}
				for _, m := range msgs[k:] {
					for _, b := range m.Content {
						if b.Type == "tool_use" {
							uses[b.ID] = true
						}
						if b.Type == "tool_result" && !uses[b.ToolUseID] {
							t.Fatalf("%s: tool_result %s split from its tool_use", name, b.ToolUseID)
						}
					}
				}
				// Budget respected unless the tail is already minimal.
				sum := 0
				for _, m := range msgs[k:] {
					sum += estimateMessageTokens(m)
				}
				if sum > budget {
					for j := k + 1; j < len(msgs); j++ {
						if msgs[j].Role == "assistant" {
							t.Fatalf("%s: tail is %d tokens (> budget) yet a smaller assistant-aligned tail exists", name, sum)
						}
					}
				}
				// Maximality: the previous assistant boundary must not fit.
				for j := k - 1; j >= 0; j-- {
					if msgs[j].Role != "assistant" {
						continue
					}
					bigger := 0
					for _, m := range msgs[j:] {
						bigger += estimateMessageTokens(m)
					}
					if bigger <= budget {
						t.Fatalf("%s: a longer tail from %d fits the budget (%d <= %d)", name, j, bigger, budget)
					}
					break
				}
			}
		}
	}
}

func TestSplitTail_Cases(t *testing.T) {
	tests := []struct {
		name   string
		msgs   []api.Message
		budget int
		want   int
	}{
		{"empty", nil, 100, 0},
		{"no assistant", []api.Message{umsg("a"), umsg("b")}, 100, 2},
		{"trailing user after last assistant is kept", []api.Message{umsg("a"), amsg("b"), umsg("c")}, 1, 1},
		{"pair kept whole", []api.Message{umsg("a"), toolCall("1", "T", nil), toolResult("1", "r", false), amsg("done")}, 1 << 20, 1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := splitTail(tt.msgs, tt.budget); got != tt.want {
				t.Errorf("splitTail = %d, want %d", got, tt.want)
			}
		})
	}
}

func TestTailBudget(t *testing.T) {
	tests := []struct {
		o    Options
		want int
	}{
		{Options{}, 20_000},
		{Options{TailTokens: 5000}, 5000},
		{Options{MainWindow: 200_000}, 20_000},
		{Options{MainWindow: 50_000}, 10_000},
		{Options{TailTokens: 50_000, MainWindow: 200_000}, 40_000},
	}
	for _, tt := range tests {
		if got := tailBudget(tt.o); got != tt.want {
			t.Errorf("tailBudget(%+v) = %d, want %d", tt.o, got, tt.want)
		}
	}
}

func TestFitTranscript(t *testing.T) {
	long := strings.Repeat("x", 450)
	var msgs []api.Message
	for i := 0; i < 40; i++ {
		id := fmt.Sprintf("t%d", i)
		msgs = append(msgs, toolCall(id, "Bash", map[string]any{"i": i}), toolResult(id, long, false))
	}
	msgs = append([]api.Message{umsg("the live task")}, msgs...)
	full := fitTranscript(msgs, 1<<30)
	shrunkTarget := utf8.RuneCountInString(full) - 100
	got := fitTranscript(msgs, shrunkTarget)
	if utf8.RuneCountInString(got) > shrunkTarget {
		t.Errorf("not capped: %d > %d", utf8.RuneCountInString(got), shrunkTarget)
	}
	if strings.Contains(got, "omitted") {
		t.Error("entries were dropped although shrinking tool results was enough")
	}
	if strings.Count(got, "xxxxxxxxxx") == 0 || strings.Contains(got, strings.Repeat("x", 250)) {
		t.Error("tool results should be shrunk to 200 chars")
	}

	tiny := 3000
	got = fitTranscript(msgs, tiny)
	if n := utf8.RuneCountInString(got); n > tiny {
		t.Errorf("not capped: %d > %d", n, tiny)
	}
	if !strings.Contains(got, "earlier entries omitted") {
		t.Error("missing omitted marker")
	}
	if !strings.Contains(got, "Active task") || !strings.Contains(got, "the live task") {
		t.Error("the active task must survive dropping")
	}
	if !strings.Contains(got, `"i":39`) {
		t.Error("newest entries must survive dropping")
	}
}

func TestSummarizerCapChars(t *testing.T) {
	tests := []struct {
		name  string
		o     Options
		extra int
		want  int
	}{
		{"explicit window", Options{SummarizerWindow: 100_000}, 0, (100_000 - 8192 - 4096) * 3},
		{"derived from model", Options{Model: "claude-haiku-4-5-20251001"}, 0, (200_000 - 8192 - 4096) * 3},
		{"extra subtracted", Options{SummarizerWindow: 100_000}, 1000, (100_000-8192-4096)*3 - 1000},
		{"tiny window floors", Options{SummarizerWindow: 1000}, 0, minTranscriptChars},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := summarizerCapChars(tt.o, tt.extra); got != tt.want {
				t.Errorf("got %d, want %d", got, tt.want)
			}
		})
	}
}

func TestLocalSummary_Deterministic(t *testing.T) {
	msgs := convo()
	a, b := LocalSummary(msgs, Options{Kind: KindAuto}), LocalSummary(msgs, Options{Kind: KindAuto})
	if a != b {
		t.Error("LocalSummary is not deterministic")
	}
	for _, want := range []string{"6 earlier messages", "Tools used: Read x1", "done reading"} {
		if !strings.Contains(a, want) {
			t.Errorf("missing %q in:\n%s", want, a)
		}
	}
	if LocalSummary(nil, Options{}) == "" {
		t.Error("LocalSummary of nothing must still be non-empty")
	}
}
