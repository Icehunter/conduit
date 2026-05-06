package session

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"github.com/icehunter/conduit/internal/api"
)

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
	return api.Message{Role: role, Content: blocks, ProviderKind: entry.ProviderKind, Provider: entry.Provider}, true
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
