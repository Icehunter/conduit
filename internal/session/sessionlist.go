package session

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/icehunter/conduit/internal/api"
)

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
