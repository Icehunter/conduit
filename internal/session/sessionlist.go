package session

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/icehunter/conduit/internal/api"
	"github.com/icehunter/conduit/internal/settings"
)

// FilterUnresolvedToolUses drops orphan tool call blocks from message
// history. Anthropic's API rejects history with either kind of orphan:
//
//   - tool_use in an assistant message with no matching tool_result in any
//     subsequent user message (stream errored before tools could run).
//   - tool_result in a user message whose tool_use_id has no matching tool_use
//     in any prior assistant message (happens when transcript chain
//     reconstruction picks a branch that excludes the assistant turn).
//
// server_tool_use blocks are treated as always-resolved: the API manages their
// execution and result injection, so they are never orphaned from conduit's
// perspective. *_tool_result blocks (web_search_tool_result, etc.) are
// similarly passed through untouched — they are API-managed, not client-managed.
//
// If filtering empties a message entirely it is dropped.
// Mirrors src/utils/messages.ts filterUnresolvedToolUses.
func FilterUnresolvedToolUses(msgs []api.Message) []api.Message {
	// Pass 1: collect tool_use IDs that have a matching tool_result.
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

	// Pass 2: drop orphan tool_use blocks from assistant messages.
	// server_tool_use blocks are always kept — the server handles their results.
	pass1 := make([]api.Message, 0, len(msgs))
	for _, m := range msgs {
		if m.Role != "assistant" {
			pass1 = append(pass1, m)
			continue
		}
		filtered := make([]api.ContentBlock, 0, len(m.Content))
		for _, b := range m.Content {
			if b.Type == "tool_use" && !resolvedIDs[b.ID] {
				continue // orphan client-managed tool_use; drop
			}
			// server_tool_use is always preserved: the API resolves it and injects
			// the result automatically, so there is never a client-side tool_result
			// to match against.
			filtered = append(filtered, b)
		}
		if len(filtered) == 0 {
			continue
		}
		m.Content = filtered
		pass1 = append(pass1, m)
	}

	// Pass 3: collect tool_use IDs that survived into pass1 assistant messages.
	definedIDs := make(map[string]bool)
	for _, m := range pass1 {
		if m.Role != "assistant" {
			continue
		}
		for _, b := range m.Content {
			if b.Type == "tool_use" && b.ID != "" {
				definedIDs[b.ID] = true
			}
		}
	}

	// Pass 4: drop tool_result blocks from user messages whose tool_use_id has
	// no corresponding tool_use in the (now-filtered) assistant messages.
	// Server tool result blocks (*_tool_result) are always kept — they are
	// API-managed and do not follow the client tool_use/tool_result pairing.
	out := make([]api.Message, 0, len(pass1))
	for _, m := range pass1 {
		if m.Role != "user" {
			out = append(out, m)
			continue
		}
		filtered := make([]api.ContentBlock, 0, len(m.Content))
		for _, b := range m.Content {
			if b.Type == "tool_result" && !definedIDs[b.ToolUseID] {
				continue // orphan client-managed tool_result; drop
			}
			// *_tool_result blocks (web_search_tool_result, etc.) are always kept.
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
	dir := ProjectDirInConfig(cwd, settings.ConduitDir())
	out, err := listDir(dir)
	if err != nil {
		return nil, err
	}
	legacyDir := LegacyProjectDirInConfig(cwd, settings.ClaudeDir())
	if filepath.Clean(legacyDir) == filepath.Clean(dir) {
		return out, nil
	}
	legacy, err := listDir(legacyDir)
	if err != nil {
		return nil, err
	}
	if len(out) == 0 {
		return legacy, nil
	}
	out = appendMissingSessions(out, legacy...)
	sort.Slice(out, func(i, j int) bool {
		return out[i].Modified.After(out[j].Modified)
	})
	return out, nil
}

func appendMissingSessions(primary []SessionMeta, fallback ...SessionMeta) []SessionMeta {
	seen := make(map[string]bool, len(primary))
	for _, s := range primary {
		seen[s.ID] = true
	}
	for _, s := range fallback {
		if seen[s.ID] {
			continue
		}
		primary = append(primary, s)
		seen[s.ID] = true
	}
	return primary
}

func listDir(dir string) ([]SessionMeta, error) {
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

// djb2Hash mirrors the TS djb2Hash function exactly, iterating over UTF-16
// code units (matching JS charCodeAt behavior) rather than Unicode code points.
// For BMP characters (U+0000–U+FFFF) this is the same; for supplementary
// characters the Go rune is split into a surrogate pair to match JS.
func djb2Hash(s string) int32 {
	var hash uint32
	for _, r := range s {
		if r < 0x10000 {
			// BMP code point: directly a single UTF-16 code unit.
			hash = (hash << 5) - hash + uint32(r)
		} else {
			// Supplementary code point: split into surrogate pair.
			r -= 0x10000
			high := uint32(0xD800 + (r>>10)&0x3FF)
			low := uint32(0xDC00 + r&0x3FF)
			hash = (hash << 5) - hash + high
			hash = (hash << 5) - hash + low
		}
	}
	return int32(hash)
}

func abs32(n int32) int32 {
	if n < 0 {
		return -n
	}
	return n
}
