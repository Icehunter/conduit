package tui

import (
	tea "charm.land/bubbletea/v2"
	"context"
	"errors"
	"strings"
	"testing"

	uv "github.com/charmbracelet/ultraviolet"
	"github.com/charmbracelet/x/ansi"

	"github.com/icehunter/conduit/internal/api"
	"github.com/icehunter/conduit/internal/attach"
	"github.com/icehunter/conduit/internal/permissions"
)

func testImage(path string) *attach.Image {
	return &attach.Image{Data: "AAAA", MediaType: "image/png", Path: path}
}

func blockTypes(blocks []api.ContentBlock) []string {
	out := make([]string, len(blocks))
	for i, b := range blocks {
		out[i] = b.Type
	}
	return out
}

func hasEmptyText(blocks []api.ContentBlock) bool {
	for _, b := range blocks {
		if b.Type == "text" && b.Text == "" {
			return true
		}
	}
	return false
}

func TestHandleAgentDone_ReclaimsUndeliveredSteer(t *testing.T) {
	steer := []api.ContentBlock{
		{Type: "image", Source: &api.ImageSource{Type: "base64", MediaType: "image/png", Data: "AAAA"}},
		{Type: "text", Text: "also this"},
	}
	tests := []struct {
		name  string
		msg   agentDoneMsg
		draft string
	}{
		{"normal end", agentDoneMsg{}, ""},
		{"error", agentDoneMsg{err: errors.New("boom")}, ""},
		{"draft keeps it queued", agentDoneMsg{}, "typing"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m, lp := wakeModel(t)
			m.running = true
			m.messages = []Message{{Role: RoleUser, Content: "also this"}} // rendered at steer time
			lp.InjectSteerContent(steer)
			if tt.draft != "" {
				m.input.SetValue(tt.draft)
			}
			rows := countUserRows(m.messages)
			m.msgTurnID(&tt.msg)

			m2, cmd := m.handleAgentDone(tt.msg)
			if lp.ClearSteer() != nil {
				t.Fatal("steer still pending in the loop after the turn ended")
			}
			if tt.draft != "" {
				if m2.running || len(m2.pendingMessages) != 1 || m2.pendingMessages[0].content == nil {
					t.Fatalf("running=%v pending=%+v, want the steer queued", m2.running, m2.pendingMessages)
				}
				return
			}
			if !m2.running || cmd == nil || len(m2.pendingMessages) != 0 {
				t.Fatalf("running=%v pending=%d, want the steer sent as the next turn", m2.running, len(m2.pendingMessages))
			}
			last := m2.history[len(m2.history)-1]
			if last.Role != "user" || len(last.Content) != 2 || last.Content[0].Type != "image" || last.Content[1].Text != "also this" {
				t.Errorf("history tail = %+v, want the steer with its image", last)
			}
			if got := countUserRows(m2.messages); got != rows {
				t.Errorf("You rows = %d, want %d (no duplicate)", got, rows)
			}
			m2.cancelTurn()
		})
	}
}

func TestReclaimedSteerGoesFirst(t *testing.T) {
	m, lp := wakeModel(t)
	m.pendingMessages = []queuedMessage{{text: "later"}}
	lp.InjectSteerContent([]api.ContentBlock{{Type: "text", Text: "steered"}})
	m = m.reclaimSteer()
	if len(m.pendingMessages) != 2 || m.pendingMessages[0].content == nil || m.pendingMessages[1].text != "later" {
		t.Errorf("pending = %+v, want the steer ahead of the earlier queue", m.pendingMessages)
	}
}

// msgTurnID makes the done message match the model's turn.
func (m Model) msgTurnID(msg *agentDoneMsg) { msg.turnID = m.turnID }

func TestDrainPendingRestoresAttachments(t *testing.T) {
	m := idleModel()
	m.pendingImages = []*attach.Image{testImage("/new.png")}
	m.pendingMessages = []queuedMessage{{text: "queued", images: []*attach.Image{testImage("/old.png")}}}
	m2, cmd, ok := m.drainPending()
	if !ok || cmd == nil || m2.input.Value() != "queued" {
		t.Fatalf("ok=%v cmd=%v input=%q", ok, cmd != nil, m2.input.Value())
	}
	if len(m2.pendingImages) != 2 || m2.pendingImages[0].Path != "/old.png" {
		t.Errorf("pendingImages = %+v, want the queued image ahead of the new one", m2.pendingImages)
	}
}

func TestSubmitPaths_CarryAttachments(t *testing.T) {
	img := func() []*attach.Image { return []*attach.Image{testImage("/tmp/a.png")} }
	t.Run("steer while running", func(t *testing.T) {
		m := idleModel()
		m.running = true
		var got []api.ContentBlock
		m.cfg.SteerContent = func(c []api.ContentBlock) { got = c }
		m.pendingImages = img()
		m2, _ := submit(t, m, "look")
		if len(got) != 2 || got[0].Type != "image" || got[1].Text != "look" {
			t.Errorf("steered = %v", blockTypes(got))
		}
		row := m2.messages[len(m2.messages)-1]
		if row.Role != RoleUser || !strings.Contains(row.Attachments, "[Image #1]\t/tmp/a.png") {
			t.Errorf("row = %+v, want the attachment recorded", row)
		}
		if len(m2.pendingImages) != 0 {
			t.Error("image left pending after steering")
		}
	})
	t.Run("images only steer", func(t *testing.T) {
		m := idleModel()
		m.running = true
		var got []api.ContentBlock
		m.cfg.SteerContent = func(c []api.ContentBlock) { got = c }
		m.pendingImages = img()
		m2, _ := submit(t, m, "")
		if len(got) != 1 || got[0].Type != "image" || hasEmptyText(got) {
			t.Errorf("steered = %v, want a lone image", blockTypes(got))
		}
		if m2.messages[len(m2.messages)-1].Attachments == "" {
			t.Error("row has no attachment")
		}
	})
	t.Run("queued without steering", func(t *testing.T) {
		m := idleModel()
		m.running = true
		m.pendingImages = img()
		m2, _ := submit(t, m, "later")
		if len(m2.pendingMessages) != 1 || len(m2.pendingMessages[0].images) != 1 || len(m2.pendingImages) != 0 {
			t.Errorf("pending=%+v images=%d, want the image queued with its text", m2.pendingMessages, len(m2.pendingImages))
		}
	})
	t.Run("queued images only", func(t *testing.T) {
		m := idleModel()
		m.running = true
		m.pendingImages = img()
		m2, _ := submit(t, m, "")
		if len(m2.pendingMessages) != 1 || m2.pendingMessages[0].text != "" || len(m2.pendingMessages[0].images) != 1 {
			t.Errorf("pending = %+v", m2.pendingMessages)
		}
	})
	t.Run("council", func(t *testing.T) {
		m := idleModel()
		m.permissionMode = permissions.ModeCouncil
		m.pendingImages = img()
		m2, _ := submit(t, m, "compare postgres and mysql for our workload")
		if len(m2.pendingImages) != 0 {
			t.Error("council left the image pending")
		}
		last := m2.history[len(m2.history)-1]
		if got := blockTypes(last.Content); len(got) != 2 || got[0] != "image" || got[1] != "text" {
			t.Errorf("council history = %v", got)
		}
		if m2.messages[len(m2.messages)-1].Attachments == "" {
			t.Error("council row has no attachment")
		}
	})
	t.Run("local provider keeps them pending", func(t *testing.T) {
		m := idleModel()
		m.localMode = true
		m.pendingImages = img()
		m2, _ := submit(t, m, "hello")
		if len(m2.pendingImages) != 1 {
			t.Errorf("pendingImages = %d, want them kept", len(m2.pendingImages))
		}
		if !strings.Contains(m2.flashMsg, "not supported") {
			t.Errorf("flash = %q", m2.flashMsg)
		}
		if row := m2.messages[len(m2.messages)-1]; row.Role == RoleUser && row.Attachments != "" {
			t.Errorf("row claims attachments were sent: %+v", row)
		}
	})
}

func countUserRows(msgs []Message) int {
	n := 0
	for _, msg := range msgs {
		if msg.Role == RoleUser {
			n++
		}
	}
	return n
}

func TestSubmit_ImagesOnly(t *testing.T) {
	m, _ := wakeModel(t)
	m.pendingImages = []*attach.Image{testImage("/tmp/a.png")}
	m2, cmd := submit(t, m, "")
	if !m2.running || cmd == nil {
		t.Fatalf("running=%v cmd=%v, want a turn", m2.running, cmd != nil)
	}
	last := m2.history[len(m2.history)-1]
	if got := blockTypes(last.Content); len(got) != 1 || got[0] != "image" {
		t.Errorf("content = %v, want one image and no empty text block", got)
	}
	if len(m2.inputHistory) != 0 {
		t.Errorf("inputHistory = %v, an empty prompt must not be remembered", m2.inputHistory)
	}
	row := m2.messages[len(m2.messages)-1]
	if row.Role != RoleUser || row.Content != "" || row.Attachments == "" {
		t.Errorf("row = %+v", row)
	}
	m2.cancelTurn()
}

func TestTakeUserContentIdx(t *testing.T) {
	tests := []struct {
		name      string
		text      string
		images    int
		wantTypes []string
		wantTyped int
	}{
		{"text only", "hi", 0, []string{"text"}, 0},
		{"image and text", "hi", 1, []string{"image", "text"}, 1},
		{"image only", "", 1, []string{"image"}, -1},
		{"two images only", "", 2, []string{"image", "image"}, -1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := idleModel()
			for range tt.images {
				m.pendingImages = append(m.pendingImages, testImage(""))
			}
			_, content, n, typed := m.takeUserContentIdx(tt.text)
			got := blockTypes(content)
			if strings.Join(got, ",") != strings.Join(tt.wantTypes, ",") || typed != tt.wantTyped || n != tt.images {
				t.Errorf("types=%v typed=%d n=%d, want %v %d %d", got, typed, n, tt.wantTypes, tt.wantTyped, tt.images)
			}
			if hasEmptyText(content) {
				t.Error("empty text block emitted")
			}
		})
	}
}

func TestRenderUserRow_AttachmentHyperlink(t *testing.T) {
	const width = 60
	plain := renderMessage(Message{Role: RoleUser, Content: "see"}, width, false)
	withImg := renderMessage(userRow("see", []*attach.Image{testImage("/tmp/my shot.png")}, nil), width, false)
	if !strings.Contains(withImg, "\x1b]8;;file:///tmp/my%20shot.png") {
		t.Fatalf("no OSC 8 link in %q", withImg)
	}
	if !strings.Contains(ansi.Strip(withImg), "[Image #1]") {
		t.Errorf("label missing in %q", withImg)
	}
	// Escape bytes add no width: the link row measures as its label.
	lines := strings.Split(withImg, "\n")
	if len(lines) != len(strings.Split(plain, "\n"))+1 {
		t.Fatalf("lines = %d, want one extra row for the link", len(lines))
	}
	linkRow := lines[len(lines)-1]
	if w := uvWidth(linkRow); w > width {
		t.Errorf("link row width %d exceeds %d", w, width)
	}

	// A file-less attachment is plain text; images-only has no empty body row.
	noPath := renderMessage(userRow("", []*attach.Image{testImage("")}, nil), width, false)
	if strings.Contains(noPath, "\x1b]8;") || !strings.Contains(ansi.Strip(noPath), "[Image #1]") {
		t.Errorf("pathless render = %q", noPath)
	}
	if strings.Count(noPath, "\n") != 0 {
		t.Errorf("images-only row spans %d lines, want 1", strings.Count(noPath, "\n")+1)
	}
}

func uvWidth(s string) int {
	scr := uv.NewScreenBuffer(200, 1)
	uv.NewStyledString(s).Draw(scr, scr.Bounds())
	w := 0
	for x := 0; x < 200; x++ {
		if c := scr.CellAt(x, 0); c != nil && c.Content != " " && c.Content != "" {
			w = x + 1
		}
	}
	return w
}

// The compositor reads the escape into cell links rather than printing it.
func TestRenderUserRow_LinkSurvivesCompositor(t *testing.T) {
	row := renderMessage(userRow("", []*attach.Image{testImage("/tmp/a.png")}, nil), 60, false)
	scr := uv.NewScreenBuffer(60, 1)
	uv.NewStyledString(withSurfaceAfterReset(row)).Draw(scr, scr.Bounds())
	var linked, printed int
	var text strings.Builder
	for x := 0; x < 60; x++ {
		c := scr.CellAt(x, 0)
		if c == nil {
			continue
		}
		text.WriteString(c.Content)
		if c.Link.URL == "file:///tmp/a.png" {
			linked++
		}
		if strings.Contains(c.Content, "\x1b") {
			printed++
		}
	}
	if linked != len("[Image #1]") || printed != 0 {
		t.Errorf("linked cells = %d (want %d), escape cells = %d, text %q", linked, len("[Image #1]"), printed, text.String())
	}
}

// The full frame (viewport included) keeps the link on the cells.
func TestUserRowLink_InFullFrame(t *testing.T) {
	m := idleModel()
	next, _ := m.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
	m = next.(Model)
	m.messages = []Message{userRow("", []*attach.Image{testImage("/tmp/frame.png")}, nil)}
	m.refreshViewport()
	scr := uv.NewScreenBuffer(100, 30)
	m.Draw(scr, scr.Bounds())
	linked := 0
	for y := 0; y < 30; y++ {
		for x := 0; x < 100; x++ {
			if c := scr.CellAt(x, y); c != nil && c.Link.URL == "file:///tmp/frame.png" {
				linked++
			}
		}
	}
	if linked != len("[Image #1]") {
		t.Errorf("linked cells in frame = %d, want %d", linked, len("[Image #1]"))
	}
}

func TestSubmit_DirectTurnQueuesInsteadOfSteering(t *testing.T) {
	m, _ := wakeModel(t)
	m.running = true
	m.directTurn = true
	steered := false
	m.cfg.SteerContent = func([]api.ContentBlock) { steered = true }
	m.pendingImages = []*attach.Image{testImage("/tmp/a.png")}
	m2, _ := submit(t, m, "follow up")
	if steered {
		t.Fatal("steered a turn that has no agent loop to deliver it")
	}
	if len(m2.pendingMessages) != 1 || m2.pendingMessages[0].text != "follow up" || len(m2.pendingMessages[0].images) != 1 {
		t.Fatalf("pending = %+v, want the message queued with its image", m2.pendingMessages)
	}
}

func TestHandleLocalCallDone_DrainsQueue(t *testing.T) {
	m, _ := wakeModel(t)
	m.running = true
	m.directTurn = true
	m.turnID = 3
	m.pendingMessages = []queuedMessage{{text: "next"}}
	m2, cmd := m.handleLocalCallDone(localCallDoneMsg{turnID: 3, text: "ok"})
	if m2.directTurn {
		t.Error("directTurn left set")
	}
	if cmd == nil || m2.input.Value() != "next" || len(m2.pendingMessages) != 0 {
		t.Errorf("input=%q pending=%d, want the queued message replayed", m2.input.Value(), len(m2.pendingMessages))
	}
}

func TestCancelReturnsQueueToInput(t *testing.T) {
	steer := []api.ContentBlock{
		{Type: "image", Source: &api.ImageSource{Type: "base64", MediaType: "image/png", Data: "AAAA"}},
		{Type: "text", Text: "stop"},
	}
	cancelled := func() context.CancelFunc { return func() {} }
	tests := []struct {
		name   string
		cancel func(Model) Model
	}{
		{"ctrl+c key", func(m Model) Model {
			m2, _, _ := m.handleKeyBuiltins(tea.KeyPressMsg{Code: 'c', Mod: tea.ModCtrl})
			return m2
		}},
		{"interrupt", func(m Model) Model {
			m2, _ := m.handleInterrupt(tea.InterruptMsg{})
			return m2
		}},
		{"cancelled turn ends", func(m Model) Model {
			msg := agentDoneMsg{cancelled: true}
			m.msgTurnID(&msg)
			m2, _ := m.handleAgentDone(msg)
			return m2
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m, lp := wakeModel(t)
			m.running = true
			m.cancelTurn = cancelled()
			m.messages = []Message{{Role: RoleUser, Content: "stop"}} // rendered at steer time
			m.pendingMessages = []queuedMessage{{text: "and then this", images: []*attach.Image{testImage("")}}}
			lp.InjectSteerContent(steer)

			m2 := tt.cancel(m)
			if lp.ClearSteer() != nil {
				t.Error("steer still in the loop")
			}
			if m2.running || len(m2.pendingMessages) != 0 {
				t.Fatalf("running=%v pending=%d, want nothing started or queued", m2.running, len(m2.pendingMessages))
			}
			if got := m2.input.Value(); got != "stop\nand then this" {
				t.Errorf("input = %q, want the unsent text back in order", got)
			}
			if len(m2.pendingImages) != 2 {
				t.Errorf("pending images = %d, want both back", len(m2.pendingImages))
			}
			if countUserRows(m2.messages) != 0 {
				t.Error("the undelivered steer still shows as sent")
			}
			// The generic Update tail must not send it either.
			next, _ := m2.Update(tea.WindowSizeMsg{Width: 100, Height: 40})
			if nm := next.(Model); nm.running {
				t.Error("a cancelled message started a turn")
			}
		})
	}
}

func TestPastedImagesPruneErrorShowsSystemRow(t *testing.T) {
	m, _ := wakeModel(t)
	before := len(m.messages)
	next, _ := m.Update(pastedImagesPrunedMsg{err: errors.New("permission denied")})
	got := next.(Model).messages
	if len(got) != before+1 || got[len(got)-1].Role != RoleSystem || !strings.Contains(got[len(got)-1].Content, "permission denied") {
		t.Errorf("messages = %+v, want a system row carrying the prune error", got[before:])
	}
}

func TestLastAssistantAsksUser(t *testing.T) {
	asst := func(blocks ...api.ContentBlock) []api.Message {
		return []api.Message{{Role: "user", Content: []api.ContentBlock{{Type: "text", Text: "go"}}}, {Role: "assistant", Content: blocks}}
	}
	text := func(s string) api.ContentBlock { return api.ContentBlock{Type: "text", Text: s} }
	tests := []struct {
		name    string
		history []api.Message
		want    bool
	}{
		{"ends with a question", asst(text("Seven places use the old name.\n\nDo you want me to finish the rename, or revert it?")), true},
		{"question then a next line", asst(text("Done.\n\nShould I commit?\n\nNext: run the tests.")), true},
		{"bold question", asst(text("**Finish or revert?**")), true},
		{"status report", asst(text("Both tests are updated. I'll read those lines next.")), false},
		{"question only early on", asst(text("Why did it fail?\n\nCause found.\n\nFixed it.\n\nTests pass.")), false},
		{"url query", asst(text("See https://example.com/a?b=1 for details.")), false},
		{"question inside code", asst(text("Done.\n\n```go\nx := a?b\n```")), false},
		{"ended on a tool call", asst(api.ContentBlock{Type: "tool_use", Name: "Read"}), false},
		{"no assistant message", []api.Message{{Role: "user", Content: []api.ContentBlock{text("hi?")}}}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := lastAssistantAsksUser(tt.history); got != tt.want {
				t.Errorf("lastAssistantAsksUser = %v, want %v", got, tt.want)
			}
		})
	}
}
