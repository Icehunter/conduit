package session

import (
	"encoding/json"
	"os"
	"strings"
)

// CostSnapshot is the cost entry written to JSONL on each turn.
type CostSnapshot struct {
	InputTokens  int     `json:"inputTokens"`
	OutputTokens int     `json:"outputTokens"`
	CostUSD      float64 `json:"costUSD"`
}

// AppendCost writes a cost snapshot entry to the session JSONL.
func (s *Session) AppendCost(inputTokens, outputTokens int, costUSD float64) error {
	raw, err := json.Marshal(CostSnapshot{
		InputTokens:  inputTokens,
		OutputTokens: outputTokens,
		CostUSD:      costUSD,
	})
	if err != nil {
		return err
	}
	return s.Append(Entry{Type: "cost", Message: raw})
}

// LoadCost reads the last cost entry from a JSONL file.
// Returns zero CostSnapshot if none found (not an error).
func LoadCost(path string) (CostSnapshot, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return CostSnapshot{}, nil
		}
		return CostSnapshot{}, err
	}
	var last CostSnapshot
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		var entry Entry
		if err := json.Unmarshal([]byte(line), &entry); err != nil {
			continue
		}
		if entry.Type == "cost" && len(entry.Message) > 0 {
			var snap CostSnapshot
			if err := json.Unmarshal(entry.Message, &snap); err == nil {
				last = snap
			}
		}
	}
	return last, nil
}

// ExtractTitle returns the best title for a session:
// 1. The last custom-title entry if present.
// 2. The first user message text (truncated to 60 runes).
// 3. Empty string.
func ExtractTitle(path string) string {
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}

	var customTitle string
	var firstUserText string

	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		var entry Entry
		if err := json.Unmarshal([]byte(line), &entry); err != nil {
			continue
		}
		switch entry.Type {
		case "custom-title":
			customTitle = entry.Title
		case "message":
			if firstUserText == "" && len(entry.Message) > 0 {
				var msg struct {
					Role    string `json:"role"`
					Content []struct {
						Type string `json:"type"`
						Text string `json:"text"`
					} `json:"content"`
				}
				if err := json.Unmarshal(entry.Message, &msg); err == nil && msg.Role == "user" {
					for _, b := range msg.Content {
						if b.Type == "text" && b.Text != "" {
							firstUserText = b.Text
							break
						}
					}
				}
			}
		}
	}

	if customTitle != "" {
		return customTitle
	}
	if firstUserText == "" {
		return ""
	}
	// Truncate to 60 runes.
	runes := []rune(firstUserText)
	if len(runes) <= 60 {
		return firstUserText
	}
	return string(runes[:57]) + "…"
}

// SearchResult is one matching turn from a transcript search.
type SearchResult struct {
	Role string
	Text string
}

// Search scans a JSONL transcript for messages containing term (case-insensitive).
func Search(path string, term string) ([]SearchResult, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}

	lower := strings.ToLower(term)
	var results []SearchResult

	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		var entry Entry
		if err := json.Unmarshal([]byte(line), &entry); err != nil || entry.Type != "message" {
			continue
		}
		var msg struct {
			Role    string `json:"role"`
			Content []struct {
				Type string `json:"type"`
				Text string `json:"text"`
			} `json:"content"`
		}
		if err := json.Unmarshal(entry.Message, &msg); err != nil {
			continue
		}
		for _, b := range msg.Content {
			if b.Type == "text" && strings.Contains(strings.ToLower(b.Text), lower) {
				snippet := b.Text
				if len([]rune(snippet)) > 200 {
					snippet = string([]rune(snippet)[:197]) + "…"
				}
				results = append(results, SearchResult{Role: msg.Role, Text: snippet})
				break
			}
		}
	}
	return results, nil
}

// FileAccessEntry records a file read or write.
type FileAccessEntry struct {
	Op   string `json:"op"`   // "read" | "write"
	Path string `json:"path"`
}

// AppendFileAccess records that a file was read or written.
func (s *Session) AppendFileAccess(op, path string) error {
	raw, err := json.Marshal(FileAccessEntry{Op: op, Path: path})
	if err != nil {
		return err
	}
	return s.Append(Entry{Type: "file-access", Message: raw})
}

// LoadFileAccess reads all file access entries from a JSONL file.
func LoadFileAccess(path string) ([]FileAccessEntry, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}

	var out []FileAccessEntry
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		var entry Entry
		if err := json.Unmarshal([]byte(line), &entry); err != nil {
			continue
		}
		if entry.Type == "file-access" && len(entry.Message) > 0 {
			var fa FileAccessEntry
			if err := json.Unmarshal(entry.Message, &fa); err == nil {
				out = append(out, fa)
			}
		}
	}
	return out, nil
}
