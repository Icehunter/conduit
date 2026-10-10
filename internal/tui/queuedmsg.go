package tui

import (
	"fmt"
	"net/url"
	"strings"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/icehunter/conduit/internal/api"
	"github.com/icehunter/conduit/internal/attach"
)

// queuedMessage is a message held back until the running turn ends. It owns
// the attachments pasted with it, so they ride with the text they were pasted
// beside rather than attaching to a later, unrelated message.
type queuedMessage struct {
	text   string
	images []*attach.Image
	pdfs   []*attach.PDF
	// content, when set, is a complete user turn already built and shown (a
	// steering message the loop never delivered). It is sent as is, with no
	// new transcript row and no hook screening: both happened at steer time.
	content []api.ContentBlock
}

// hasPendingAttachments reports whether images or PDFs await the next send.
func (m Model) hasPendingAttachments() bool {
	return len(m.pendingImages)+len(m.pendingPDFs) > 0
}

// takeQueuedMessage builds a queued entry for text, taking the pending
// attachments with it.
func (m Model) takeQueuedMessage(text string) (Model, queuedMessage) {
	q := queuedMessage{text: text, images: m.pendingImages, pdfs: m.pendingPDFs}
	m.pendingImages = nil
	m.pendingPDFs = nil
	return m, q
}

// drainPending starts the first queued message when the input is empty. The
// bool reports whether one was taken.
func (m Model) drainPending() (Model, tea.Cmd, bool) {
	if len(m.pendingMessages) == 0 || strings.TrimSpace(m.input.Value()) != "" {
		return m, nil, false
	}
	next := m.pendingMessages[0]
	m.pendingMessages = m.pendingMessages[1:]
	if next.content != nil {
		m.history = append(m.history, api.Message{Role: "user", Content: next.content})
		m.promptRow = 0
		m, cmd := m.beginTurn()
		return m, cmd, true
	}
	// Its attachments go back in front of anything pasted since, then the
	// ordinary submit path sends them with the text.
	m.pendingImages = append(append([]*attach.Image(nil), next.images...), m.pendingImages...)
	m.pendingPDFs = append(append([]*attach.PDF(nil), next.pdfs...), m.pendingPDFs...)
	m.input.SetValue(next.text)
	return m, m.replaySubmit(), true
}

// reclaimSteer takes back a steering message the loop never delivered (the
// turn ended first) and queues it at the front, so it is sent as the next user
// turn instead of lingering until some later tool batch picks it up.
func (m Model) reclaimSteer() Model {
	if m.cfg.Loop == nil {
		return m
	}
	steer := m.cfg.Loop.ClearSteer()
	if len(steer) == 0 {
		return m
	}
	m.pendingMessages = append([]queuedMessage{{content: steer}}, m.pendingMessages...)
	m.flashMsg = fmt.Sprintf("[queued — %d pending]", len(m.pendingMessages))
	return m
}

// restoreQueueToInput returns an undelivered steer and every queued message to
// the input after a cancel. A cancel means stop: nothing typed during the
// turn may start a new one until the user sends it again.
func (m Model) restoreQueueToInput() Model {
	var steer []api.ContentBlock
	if m.cfg.Loop != nil {
		steer = m.cfg.Loop.ClearSteer()
	}
	queued := m.pendingMessages
	if len(steer) > 0 {
		queued = append([]queuedMessage{{content: steer}}, queued...)
	}
	m.pendingMessages = nil
	if len(queued) == 0 {
		return m
	}
	var texts []string
	var images []*attach.Image
	var pdfs []*attach.PDF
	for _, q := range queued {
		text, imgs, docs := q.text, q.images, q.pdfs
		if q.content != nil {
			// Its row was shown at steer time, but the model never saw it.
			text, imgs, docs = unpackUserContent(q.content)
			m.removeUserRowWithText(text)
		}
		if strings.TrimSpace(text) != "" {
			texts = append(texts, text)
		}
		images = append(images, imgs...)
		pdfs = append(pdfs, docs...)
	}
	m.pendingImages = append(images, m.pendingImages...)
	m.pendingPDFs = append(pdfs, m.pendingPDFs...)
	if len(texts) > 0 {
		m.restoreInput(strings.Join(texts, "\n"))
	}
	m.flashMsg = "interrupted — unsent message returned to the input"
	return m
}

// unpackUserContent splits a built user turn back into its typed text and
// attachments, dropping the system reminders the loop adds.
func unpackUserContent(blocks []api.ContentBlock) (string, []*attach.Image, []*attach.PDF) {
	var parts []string
	var images []*attach.Image
	var pdfs []*attach.PDF
	for _, b := range blocks {
		switch {
		case b.Type == "text" && !isSystemReminderText(b.Text):
			parts = append(parts, b.Text)
		case b.Type == "image" && b.Source != nil:
			images = append(images, &attach.Image{Data: b.Source.Data, MediaType: b.Source.MediaType})
		case b.Type == "document" && b.Source != nil && b.Source.MediaType == "application/pdf":
			pdfs = append(pdfs, &attach.PDF{Data: b.Source.Data, MediaType: b.Source.MediaType})
		}
	}
	return strings.Join(parts, "\n"), images, pdfs
}

// removeUserRowWithText drops the newest user row showing text.
func (m *Model) removeUserRowWithText(text string) {
	for i := len(m.messages) - 1; i >= 0; i-- {
		if m.messages[i].Role == RoleUser && m.messages[i].Content == text {
			m.messages = append(m.messages[:i], m.messages[i+1:]...)
			return
		}
	}
}

// userRow builds the transcript row for a sent message and its attachments.
func userRow(text string, images []*attach.Image, pdfs []*attach.PDF) Message {
	atts := make([]attachmentRef, 0, len(images)+len(pdfs))
	for i, img := range images {
		atts = append(atts, attachmentRef{label: fmt.Sprintf("[Image #%d]", i+1), path: img.Path})
	}
	for i, pdf := range pdfs {
		atts = append(atts, attachmentRef{label: fmt.Sprintf("[PDF #%d]", i+1), path: pdf.Path})
	}
	return Message{Role: RoleUser, Content: text, Attachments: encodeAttachments(atts)}
}

// pendingUserRow is userRow over the attachments still pending, for call
// sites that build the row before the content consumes them.
func (m Model) pendingUserRow(text string) Message {
	return userRow(text, m.pendingImages, m.pendingPDFs)
}

// attachmentRef is one attachment shown on a user row; path is empty when no
// file backs it, and the label is then plain text.
type attachmentRef struct {
	label string
	path  string
}

// encodeAttachments flattens refs into a string so Message stays comparable.
func encodeAttachments(refs []attachmentRef) string {
	if len(refs) == 0 {
		return ""
	}
	clean := strings.NewReplacer("\n", " ", "\t", " ")
	parts := make([]string, len(refs))
	for i, r := range refs {
		parts[i] = clean.Replace(r.label) + "\t" + clean.Replace(r.path)
	}
	return strings.Join(parts, "\n")
}

func decodeAttachments(s string) []attachmentRef {
	if s == "" {
		return nil
	}
	lines := strings.Split(s, "\n")
	refs := make([]attachmentRef, 0, len(lines))
	for _, ln := range lines {
		label, path, _ := strings.Cut(ln, "\t")
		refs = append(refs, attachmentRef{label: label, path: path})
	}
	return refs
}

// renderAttachmentLinks lays the attachments out in rows no wider than width.
// Each file-backed label is an OSC 8 hyperlink; the compositor reads those
// into cell links, and the escapes add no width.
func renderAttachmentLinks(encoded string, width int) string {
	refs := decodeAttachments(encoded)
	if len(refs) == 0 {
		return ""
	}
	var lines []string
	var cur strings.Builder
	curW := 0
	for _, r := range refs {
		label := styleUserText.Render(r.label)
		if r.path != "" {
			u := url.URL{Scheme: "file", Path: r.path}
			label = ansi.SetHyperlink(u.String()) + styleUserText.Underline(true).Render(r.label) + ansi.ResetHyperlink()
		}
		w := ansi.StringWidth(r.label)
		if curW > 0 && curW+1+w > width {
			lines = append(lines, cur.String())
			cur.Reset()
			curW = 0
		}
		if curW > 0 {
			cur.WriteByte(' ')
			curW++
		}
		cur.WriteString(label)
		curW += w
	}
	lines = append(lines, cur.String())
	return strings.Join(lines, "\n")
}
