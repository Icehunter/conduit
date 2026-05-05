// Package session implements conversation persistence.
//
// Storage layout mirrors the real Claude Code (src/utils/sessionStorage.ts):
//
//	~/.claude/projects/<sanitized-cwd>/<session-id>.jsonl
//
// Each line of the JSONL file is one Entry. On startup we can read any
// previous session's JSONL and restore its message history, enabling
// --continue and /resume.
package session

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/icehunter/conduit/internal/api"
)

const maxSanitizedLength = 200

// Entry is one line in the JSONL transcript file.
type Entry struct {
	Type        string          `json:"type"`
	SessionID   string          `json:"sessionId"`
	Timestamp   int64           `json:"ts,omitempty"`
	UUID        string          `json:"uuid,omitempty"`
	ParentUUID  string          `json:"parentUuid,omitempty"`
	CreatedAt   string          `json:"timestamp,omitempty"`
	IsSidechain bool            `json:"isSidechain,omitempty"`
	Message     json.RawMessage `json:"message,omitempty"`
	Summary     string          `json:"summary,omitempty"`
	Title       string          `json:"customTitle,omitempty"`
	// Mode is set on type="session_settings" entries to persist session-level
	// settings (e.g. "coordinator" or "normal") across resume.
	Mode string `json:"mode,omitempty"`
}

// Session manages the JSONL transcript for one conversation.
type Session struct {
	ID         string
	ProjectDir string
	FilePath   string
}

// New creates a new session rooted at cwd, using sessionID as the file name.
// ProjectDir returns the directory where session files for cwd are stored.
func ProjectDir(cwd, home string) string {
	return filepath.Join(home, ".claude", "projects", sanitizePath(cwd))
}

// FromFile wraps an existing JSONL file as a Session so new turns can be appended to it.
func FromFile(filePath string) *Session {
	base := filepath.Base(filePath)
	id := strings.TrimSuffix(base, ".jsonl")
	return &Session{
		ID:         id,
		ProjectDir: filepath.Dir(filePath),
		FilePath:   filePath,
	}
}

func New(cwd, sessionID string) (*Session, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, fmt.Errorf("session: home dir: %w", err)
	}
	projectsDir := filepath.Join(home, ".claude", "projects")
	sanitized := sanitizePath(cwd)
	projectDir := filepath.Join(projectsDir, sanitized)
	if err := os.MkdirAll(projectDir, 0o700); err != nil {
		return nil, fmt.Errorf("session: mkdir %s: %w", projectDir, err)
	}
	return &Session{
		ID:         sessionID,
		ProjectDir: projectDir,
		FilePath:   filepath.Join(projectDir, sessionID+".jsonl"),
	}, nil
}

// Append writes one entry to the JSONL file.
func (s *Session) Append(entry Entry) error {
	entry.SessionID = s.ID
	if entry.Timestamp == 0 {
		entry.Timestamp = time.Now().UnixMilli()
	}
	b, err := json.Marshal(entry)
	if err != nil {
		return fmt.Errorf("session: marshal: %w", err)
	}
	f, err := os.OpenFile(s.FilePath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return fmt.Errorf("session: open: %w", err)
	}
	defer f.Close()
	_, err = fmt.Fprintf(f, "%s\n", b)
	return err
}

// SetMode persists a session-level mode setting (e.g. "coordinator" or "normal").
func (s *Session) SetMode(mode string) error {
	return s.Append(Entry{Type: "session_settings", Mode: mode})
}

// ReadMode scans the session JSONL and returns the most recent mode value
// from a "session_settings" entry. Returns "" if no mode has been set.
func (s *Session) ReadMode() string {
	f, err := os.Open(s.FilePath)
	if err != nil {
		return ""
	}
	defer f.Close()
	mode := ""
	dec := json.NewDecoder(f)
	for {
		var e Entry
		if err := dec.Decode(&e); err != nil {
			break
		}
		if e.Type == "session_settings" && e.Mode != "" {
			mode = e.Mode
		}
	}
	return mode
}

// AppendMessage serializes an api.Message and appends it.
func (s *Session) AppendMessage(msg api.Message) error {
	raw, err := json.Marshal(msg)
	if err != nil {
		return err
	}
	return s.Append(Entry{Type: "message", Message: raw})
}

// SetTitle persists a human-readable conversation title.
func (s *Session) SetTitle(title string) error {
	return s.Append(Entry{Type: "custom-title", Title: title})
}

// SetSummary persists a compaction summary.
func (s *Session) SetSummary(summary string) error {
	return s.Append(Entry{Type: "summary", Summary: summary})
}

// Snapshot returns the current message count — used as a rewind point.
// We use the file line count as a proxy. Returns 0 if unavailable.
func (s *Session) Snapshot() int {
	data, err := os.ReadFile(s.FilePath)
	if err != nil {
		return 0
	}
	return strings.Count(string(data), "\n")
}

// LoadMessages reads the JSONL file and returns the message history.
func (s *Session) LoadMessages() ([]api.Message, error) {
	return LoadMessages(s.FilePath)
}

// LoadMessages reads a JSONL transcript at path and returns its messages.
// Output passes through FilterUnresolvedToolUses so a partial assistant
// message persisted by conversation recovery (a tool_use with no matching
// tool_result) doesn't poison the next API call on /resume.
func LoadMessages(path string) ([]api.Message, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("session: read %s: %w", path, err)
	}
	msgs := transcriptMessagesFromJSONL(data)
	return FilterUnresolvedToolUses(msgs), nil
}

type transcriptRecord struct {
	entry Entry
	msg   api.Message
	line  int
}

func transcriptMessagesFromJSONL(data []byte) []api.Message {
	var records []transcriptRecord
	byUUID := make(map[string]transcriptRecord)
	bridges := make(map[string]string)
	var legacy []transcriptRecord

	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		var entry Entry
		if err := json.Unmarshal([]byte(line), &entry); err != nil {
			continue
		}
		if !isLoadableTranscriptEntry(entry.Type) || len(entry.Message) == 0 {
			addTranscriptBridge(bridges, entry)
			continue
		}
		msg, ok := entryAPIMessage(entry)
		if !ok {
			addTranscriptBridge(bridges, entry)
			continue
		}
		rec := transcriptRecord{entry: entry, msg: msg, line: len(records) + len(legacy)}
		if entry.UUID != "" && (entry.Type == "user" || entry.Type == "assistant") {
			records = append(records, rec)
			byUUID[entry.UUID] = rec
			continue
		}
		legacy = append(legacy, rec)
	}

	if len(records) == 0 {
		return recordsToMessages(legacy)
	}

	chain := buildLatestTranscriptChain(records, byUUID, bridges)
	lastLine := -1
	if len(chain) > 0 {
		lastLine = chain[len(chain)-1].line
	}
	for _, rec := range legacy {
		if rec.line > lastLine {
			chain = append(chain, rec)
		}
	}
	return recordsToMessages(chain)
}

func isLoadableTranscriptEntry(typ string) bool {
	return typ == "message" || typ == "user" || typ == "assistant"
}

func addTranscriptBridge(bridges map[string]string, entry Entry) {
	if entry.UUID == "" {
		return
	}
	bridges[entry.UUID] = entry.ParentUUID
}

func recordsToMessages(records []transcriptRecord) []api.Message {
	out := make([]api.Message, 0, len(records))
	for _, rec := range records {
		out = append(out, rec.msg)
	}
	return out
}

func buildLatestTranscriptChain(records []transcriptRecord, byUUID map[string]transcriptRecord, bridges map[string]string) []transcriptRecord {
	latest := transcriptRecord{}
	found := false
	for i := len(records) - 1; i >= 0; i-- {
		if records[i].entry.IsSidechain {
			continue
		}
		latest = records[i]
		found = true
		break
	}
	if !found {
		latest = records[len(records)-1]
	}

	var chain []transcriptRecord
	seen := make(map[string]bool)
	for {
		uuid := latest.entry.UUID
		if uuid == "" || seen[uuid] {
			break
		}
		seen[uuid] = true
		chain = append(chain, latest)
		parent := latest.entry.ParentUUID
		if parent == "" {
			break
		}
		next, ok := resolveTranscriptParent(parent, byUUID, bridges, seen)
		if !ok {
			break
		}
		latest = next
	}
	for i, j := 0, len(chain)-1; i < j; i, j = i+1, j-1 {
		chain[i], chain[j] = chain[j], chain[i]
	}
	return chain
}

func resolveTranscriptParent(parent string, byUUID map[string]transcriptRecord, bridges map[string]string, seen map[string]bool) (transcriptRecord, bool) {
	for parent != "" {
		if seen[parent] {
			return transcriptRecord{}, false
		}
		if next, ok := byUUID[parent]; ok {
			return next, true
		}
		bridged, ok := bridges[parent]
		if !ok {
			return transcriptRecord{}, false
		}
		seen[parent] = true
		parent = bridged
	}
	return transcriptRecord{}, false
}

func entryAPIMessage(entry Entry) (api.Message, bool) {
	var raw struct {
		Role    string          `json:"role"`
		Content json.RawMessage `json:"content"`
	}
	if err := json.Unmarshal(entry.Message, &raw); err != nil {
		return api.Message{}, false
	}
	role := raw.Role
	if role == "" && (entry.Type == "user" || entry.Type == "assistant") {
		role = entry.Type
	}
	if role != "user" && role != "assistant" {
		return api.Message{}, false
	}
	blocks := parseContentBlocks(raw.Content)
	if len(blocks) == 0 {
		return api.Message{}, false
	}
	return api.Message{Role: role, Content: blocks}, true
}

func parseContentBlocks(raw json.RawMessage) []api.ContentBlock {
	if len(raw) == 0 || string(raw) == "null" {
		return nil
	}
	var text string
	if err := json.Unmarshal(raw, &text); err == nil {
		return []api.ContentBlock{{Type: "text", Text: text}}
	}
	var raws []json.RawMessage
	if err := json.Unmarshal(raw, &raws); err != nil {
		return nil
	}
	blocks := make([]api.ContentBlock, 0, len(raws))
	for _, rb := range raws {
		if block, ok := parseContentBlock(rb); ok {
			blocks = append(blocks, block)
		}
	}
	return blocks
}

func parseContentBlock(raw json.RawMessage) (api.ContentBlock, bool) {
	var head struct {
		Type string `json:"type"`
	}
	if err := json.Unmarshal(raw, &head); err != nil || head.Type == "" {
		return api.ContentBlock{}, false
	}
	switch head.Type {
	case "text", "tool_use", "image", "document":
		var block api.ContentBlock
		if err := json.Unmarshal(raw, &block); err != nil {
			return api.ContentBlock{}, false
		}
		return block, true
	case "tool_result":
		return parseToolResultBlock(raw)
	default:
		return api.ContentBlock{}, false
	}
}

func parseToolResultBlock(raw json.RawMessage) (api.ContentBlock, bool) {
	var block struct {
		Type      string          `json:"type"`
		ToolUseID string          `json:"tool_use_id"`
		IsError   bool            `json:"is_error"`
		Content   json.RawMessage `json:"content"`
	}
	if err := json.Unmarshal(raw, &block); err != nil {
		return api.ContentBlock{}, false
	}
	result := api.ContentBlock{
		Type:      block.Type,
		ToolUseID: block.ToolUseID,
		IsError:   block.IsError,
	}
	var content string
	if err := json.Unmarshal(block.Content, &content); err == nil {
		result.ResultContent = content
		return result, true
	}
	if text := textFromContentArray(block.Content); text != "" {
		result.ResultContent = text
		return result, true
	}
	if len(block.Content) > 0 && string(block.Content) != "null" {
		result.ResultContent = string(block.Content)
	}
	return result, true
}

func textFromContentArray(raw json.RawMessage) string {
	var blocks []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	if err := json.Unmarshal(raw, &blocks); err != nil {
		return ""
	}
	var parts []string
	for _, block := range blocks {
		if block.Type == "text" && block.Text != "" {
			parts = append(parts, block.Text)
		}
	}
	return strings.Join(parts, "\n")
}

// FilterUnresolvedToolUses drops orphan tool_use blocks from assistant
// messages — i.e. tool_use blocks whose ID has no matching tool_result in
// any subsequent user message. Anthropic's API rejects history with such
// orphans; they appear when the stream errors mid-turn before tools could
// run. Mirrors src/utils/messages.ts filterUnresolvedToolUses.
//
// If filtering empties an assistant message entirely (every block was an
// orphan tool_use), the message is dropped to avoid sending an empty
// content array.
func FilterUnresolvedToolUses(msgs []api.Message) []api.Message {
	resolvedIDs := make(map[string]bool)
	for _, m := range msgs {
		if m.Role != "user" {
			continue
		}
		for _, b := range m.Content {
			if b.Type == "tool_result" && b.ToolUseID != "" {
				resolvedIDs[b.ToolUseID] = true
			}
		}
	}

	out := make([]api.Message, 0, len(msgs))
	for _, m := range msgs {
		if m.Role != "assistant" {
			out = append(out, m)
			continue
		}
		filtered := make([]api.ContentBlock, 0, len(m.Content))
		for _, b := range m.Content {
			if b.Type == "tool_use" && !resolvedIDs[b.ID] {
				continue // orphan; drop
			}
			filtered = append(filtered, b)
		}
		if len(filtered) == 0 {
			continue
		}
		m.Content = filtered
		out = append(out, m)
	}
	return out
}

// List returns all session IDs for the given cwd, newest first.
func List(cwd string) ([]SessionMeta, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, err
	}
	sanitized := sanitizePath(cwd)
	dir := filepath.Join(home, ".claude", "projects", sanitized)
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var out []SessionMeta
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".jsonl") {
			continue
		}
		id := strings.TrimSuffix(e.Name(), ".jsonl")
		info, _ := e.Info()
		mod := time.Time{}
		if info != nil {
			mod = info.ModTime()
		}
		out = append(out, SessionMeta{
			ID:       id,
			FilePath: filepath.Join(dir, e.Name()),
			Modified: mod,
		})
	}
	// Sort newest first by modification time.
	sort.Slice(out, func(i, j int) bool {
		return out[i].Modified.After(out[j].Modified)
	})
	return out, nil
}

// SessionMeta is a lightweight descriptor for a past session.
type SessionMeta struct {
	ID       string
	FilePath string
	Modified time.Time
	Title    string
}

// sanitizePath converts an arbitrary path to a safe directory name.
// Mirrors sessionStoragePortable.ts sanitizePath + djb2Hash fallback.
func sanitizePath(s string) string {
	var b strings.Builder
	for _, r := range s {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
		} else {
			b.WriteRune('-')
		}
	}
	sanitized := b.String()
	if len(sanitized) <= maxSanitizedLength {
		return sanitized
	}
	h := djb2Hash(s)
	suffix := fmt.Sprintf("%x", abs32(h))
	return sanitized[:maxSanitizedLength] + "-" + suffix
}

// djb2Hash mirrors the TS djb2Hash function exactly.
func djb2Hash(s string) int32 {
	var hash int32
	for _, c := range s {
		hash = ((hash << 5) - hash + c)
	}
	return hash
}

func abs32(n int32) int32 {
	if n < 0 {
		return -n
	}
	return n
}
