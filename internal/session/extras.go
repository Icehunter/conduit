package session

import (
	"encoding/json"
	"os"
	"strings"
	"time"
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
	return titleFromText(firstUserText)
}

// titleFromText derives a display title from a user message.
// Slash commands get descriptive names; long messages are truncated.
var slashTitles = map[string]string{
	"/init":       "Initialize CLAUDE.md",
	"/review":     "Code review",
	"/commit":     "Create commit",
	"/fix":        "Fix issue",
	"/pr-comments": "Address PR comments",
	"/compact":    "Compact session",
	"/diff":       "View diff",
}

// promptPrefixTitles maps the opening words of known injected prompts to display names.
// These are the first ~40 chars of the prompts in commands/prompts.go.
var promptPrefixTitles = []struct {
	prefix string
	title  string
}{
	{"Set up a minimal CLAUDE.md for this repo", "Initialize CLAUDE.md"},
	{"Create a git commit for the current changes", "Create commit"},
	{"Address the review comments on the current pull request", "Address PR comments"},
	{"You are an expert code reviewer", "Code review"},
	{"Please fix the following issue:", "Fix issue"},
	{"Please look at the current state of the codebase", "Fix issues"},
	{"Enter plan mode.", "Plan mode session"},
}

func titleFromText(text string) string {
	text = strings.TrimSpace(text)
	if text == "" {
		return ""
	}
	// Check for slash commands.
	if strings.HasPrefix(text, "/") {
		cmd := strings.Fields(text)[0]
		if label, ok := slashTitles[cmd]; ok {
			return label
		}
		// Unknown slash command — use command name capitalized.
		name := strings.TrimPrefix(cmd, "/")
		if name != "" {
			return strings.ToUpper(name[:1]) + name[1:]
		}
	}
	// Reverse-map injected prompt text to a friendly name.
	for _, pp := range promptPrefixTitles {
		if strings.HasPrefix(text, pp.prefix) {
			return pp.title
		}
	}
	// Truncate to 60 runes.
	runes := []rune(text)
	if len(runes) <= 60 {
		return text
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

// AppendTag persists a tag label for the session. Empty tag clears it.
// Mirrors src/utils/sessionStorage.ts saveTag — tag entries are appended
// to the JSONL transcript and the most recent value wins on read.
func (s *Session) AppendTag(tag string) error {
	return s.Append(Entry{Type: "tag", Title: tag})
}

// LoadTag returns the current tag for a session, or "" if untagged.
func LoadTag(path string) (string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return "", nil
		}
		return "", err
	}
	var tag string
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		var entry Entry
		if err := json.Unmarshal([]byte(line), &entry); err != nil {
			continue
		}
		if entry.Type == "tag" {
			tag = entry.Title
		}
	}
	return tag, nil
}

// Activity summarizes a session's temporal footprint.
type Activity struct {
	FirstActivity time.Time
	LastActivity  time.Time
	MessageCount  int
}

// IdleSince returns how long since the last recorded activity, or 0 if none.
func (a Activity) IdleSince(now time.Time) time.Duration {
	if a.LastActivity.IsZero() {
		return 0
	}
	return now.Sub(a.LastActivity)
}

// LoadActivity walks the JSONL and returns first/last entry timestamps and
// message count. Mirrors the temporal half of src/utils/sessionActivity.ts —
// we do not run the heartbeat timer (that's a remote/bridge feature) but we
// expose enough for /session to display "active for X" / "idle for Y".
func LoadActivity(path string) (Activity, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return Activity{}, nil
		}
		return Activity{}, err
	}
	var act Activity
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		var entry Entry
		if err := json.Unmarshal([]byte(line), &entry); err != nil {
			continue
		}
		if entry.Timestamp == 0 {
			continue
		}
		ts := time.UnixMilli(entry.Timestamp)
		if act.FirstActivity.IsZero() || ts.Before(act.FirstActivity) {
			act.FirstActivity = ts
		}
		if ts.After(act.LastActivity) {
			act.LastActivity = ts
		}
		if entry.Type == "message" {
			act.MessageCount++
		}
	}
	return act, nil
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
