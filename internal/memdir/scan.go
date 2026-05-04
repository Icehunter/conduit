package memdir

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// MemoryFile is one memory file's metadata.
type MemoryFile struct {
	Path    string
	Name    string // filename without directory
	ModTime time.Time
	Size    int64
	Type    string // "user" | "feedback" | "project" | "reference" | "unknown"
}

// ScanMemories lists all memory files in the memory directory for cwd.
// Returns files sorted by modification time (newest first).
func ScanMemories(cwd string) ([]MemoryFile, error) {
	dir := Path(cwd)
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}

	var files []MemoryFile
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".md") {
			continue
		}
		if e.Name() == EntrypointName {
			continue // skip MEMORY.md index
		}
		info, _ := e.Info()
		modTime := time.Time{}
		var size int64
		if info != nil {
			modTime = info.ModTime()
			size = info.Size()
		}
		files = append(files, MemoryFile{
			Path:    filepath.Join(dir, e.Name()),
			Name:    e.Name(),
			ModTime: modTime,
			Size:    size,
			Type:    inferMemoryType(e.Name()),
		})
	}

	sort.Slice(files, func(i, j int) bool {
		return files[i].ModTime.After(files[j].ModTime)
	})
	return files, nil
}

// inferMemoryType guesses the memory type from the filename prefix.
func inferMemoryType(name string) string {
	name = strings.ToLower(name)
	switch {
	case strings.HasPrefix(name, "user_") || strings.HasPrefix(name, "user-"):
		return "user"
	case strings.HasPrefix(name, "feedback_") || strings.HasPrefix(name, "feedback-"):
		return "feedback"
	case strings.HasPrefix(name, "project_") || strings.HasPrefix(name, "project-"):
		return "project"
	case strings.HasPrefix(name, "reference_") || strings.HasPrefix(name, "reference-"):
		return "reference"
	default:
		return "unknown"
	}
}

// FormatMemoryList returns a human-readable summary of memory files.
func FormatMemoryList(files []MemoryFile) string {
	if len(files) == 0 {
		return "No memory files found."
	}
	var sb strings.Builder
	fmt.Fprintf(&sb, "Memory files (%d):\n\n", len(files))
	for _, f := range files {
		age := formatAge(time.Since(f.ModTime))
		fmt.Fprintf(&sb, "  [%s] %s — %s (%d bytes)\n",
			f.Type, f.Name, age, f.Size)
	}
	return strings.TrimRight(sb.String(), "\n")
}

// RelevantMemories finds memory files that mention any of the given keywords.
// Simple keyword matching — no embeddings required.
func RelevantMemories(cwd string, keywords []string) ([]MemoryFile, error) {
	files, err := ScanMemories(cwd)
	if err != nil || len(files) == 0 {
		return nil, err
	}

	if len(keywords) == 0 {
		return files, nil
	}

	lowerKeys := make([]string, len(keywords))
	for i, k := range keywords {
		lowerKeys[i] = strings.ToLower(k)
	}

	var relevant []MemoryFile
	for _, f := range files {
		data, err := os.ReadFile(f.Path)
		if err != nil {
			continue
		}
		content := strings.ToLower(string(data))
		for _, k := range lowerKeys {
			if strings.Contains(content, k) {
				relevant = append(relevant, f)
				break
			}
		}
	}
	return relevant, nil
}

// formatAge returns a human-readable duration string.
func formatAge(d time.Duration) string {
	switch {
	case d < time.Minute:
		return "just now"
	case d < time.Hour:
		return fmt.Sprintf("%dm ago", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh ago", int(d.Hours()))
	case d < 7*24*time.Hour:
		return fmt.Sprintf("%dd ago", int(d.Hours()/24))
	default:
		return fmt.Sprintf("%dw ago", int(d.Hours()/(24*7)))
	}
}
