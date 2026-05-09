package commands

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

// humanDuration formats a Duration as "5s", "2m", "1h 13m", "3d 4h".
func humanDuration(d time.Duration) string {
	if d < time.Minute {
		s := int(d.Seconds())
		s = max(s, 1)
		return fmt.Sprintf("%ds", s)
	}
	if d < time.Hour {
		return fmt.Sprintf("%dm", int(d.Minutes()))
	}
	if d < 24*time.Hour {
		h := int(d.Hours())
		m := int(d.Minutes()) % 60
		if m == 0 {
			return fmt.Sprintf("%dh", h)
		}
		return fmt.Sprintf("%dh %dm", h, m)
	}
	days := int(d.Hours() / 24)
	h := int(d.Hours()) % 24
	if h == 0 {
		return fmt.Sprintf("%dd", days)
	}
	return fmt.Sprintf("%dd %dh", days, h)
}

// formatSessionAge returns a concise human-readable age string.
func formatSessionAge(d time.Duration) string {
	switch {
	case d < time.Minute:
		return "just now"
	case d < time.Hour:
		m := int(d.Minutes())
		if m == 1 {
			return "1m ago"
		}
		return fmt.Sprintf("%dm ago", m)
	case d < 24*time.Hour:
		h := int(d.Hours())
		if h == 1 {
			return "1h ago"
		}
		return fmt.Sprintf("%dh ago", h)
	case d < 7*24*time.Hour:
		days := int(d.Hours() / 24)
		if days == 1 {
			return "yesterday"
		}
		return fmt.Sprintf("%dd ago", days)
	case d < 30*24*time.Hour:
		weeks := int(d.Hours() / (24 * 7))
		if weeks == 1 {
			return "1w ago"
		}
		return fmt.Sprintf("%dw ago", weeks)
	default:
		months := int(d.Hours() / (24 * 30))
		if months == 1 {
			return "1mo ago"
		}
		return fmt.Sprintf("%dmo ago", months)
	}
}

func makeBar(pct, width int) string {
	filled := width * pct / 100
	if filled > width {
		filled = width
	}
	return "[" + strings.Repeat("█", filled) + strings.Repeat("░", width-filled) + "]"
}

// getNestedKey retrieves a dot-path key from a map (e.g. "permissions.allow").
func getNestedKey(m map[string]interface{}, key string) interface{} {
	parts := strings.SplitN(key, ".", 2)
	val, ok := m[parts[0]]
	if !ok {
		return nil
	}
	if len(parts) == 1 {
		return val
	}
	sub, ok := val.(map[string]interface{})
	if !ok {
		return nil
	}
	return getNestedKey(sub, parts[1])
}

// upsertSettingsKey writes key=value into the settings JSON file using raw-map
// preservation so unknown fields survive the round-trip.
func upsertSettingsKey(path string, key string, value interface{}) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	raw := make(map[string]json.RawMessage)
	if data, err := os.ReadFile(path); err == nil && len(data) > 0 {
		if err := json.Unmarshal(data, &raw); err != nil {
			return err
		}
	} else if err != nil && !os.IsNotExist(err) {
		return err
	}
	parts := strings.SplitN(key, ".", 2)
	if len(parts) == 1 {
		encoded, err := json.Marshal(value)
		if err != nil {
			return err
		}
		raw[key] = encoded
	} else {
		// Nested key: read sub-object, update, write back.
		var sub map[string]interface{}
		if existing, ok := raw[parts[0]]; ok {
			if err := json.Unmarshal(existing, &sub); err != nil {
				return err
			}
		}
		if sub == nil {
			sub = make(map[string]interface{})
		}
		sub[parts[1]] = value
		encoded, err := json.Marshal(sub)
		if err != nil {
			return err
		}
		raw[parts[0]] = encoded
	}
	out, err := json.MarshalIndent(raw, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(out, '\n'), 0o644)
}

func openBrowser(url string) {
	var cmd string
	switch runtime.GOOS {
	case "darwin":
		cmd = "open"
	case "linux":
		cmd = "xdg-open"
	default:
		return
	}
	_ = exec.Command(cmd, url).Start() //nolint:noctx
}

// countJSONLLines counts the number of non-empty lines in a JSONL file —
// each line is one message record, so this gives an approximate message count.
func countJSONLLines(path string) int {
	f, err := os.Open(path)
	if err != nil {
		return 0
	}
	defer f.Close()
	buf := make([]byte, 32*1024)
	count := 0
	for {
		n, err := f.Read(buf)
		for _, b := range buf[:n] {
			if b == '\n' {
				count++
			}
		}
		if err != nil {
			break
		}
	}
	return count
}

func sessionFootprintBytes(path string) int64 {
	var total int64
	if info, err := os.Stat(path); err == nil {
		total += info.Size()
	}
	sidecarDir := strings.TrimSuffix(path, filepath.Ext(path))
	_ = filepath.WalkDir(sidecarDir, func(_ string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		if info, err := d.Info(); err == nil {
			total += info.Size()
		}
		return nil
	})
	return total
}

func formatSessionFootprint(bytes int64) string {
	const unit = 1024
	if bytes < unit {
		return fmt.Sprintf("%d B", bytes)
	}
	value := float64(bytes)
	units := []string{"KB", "MB", "GB"}
	for _, suffix := range units {
		value /= unit
		if value < unit {
			return fmt.Sprintf("%.1f %s", value, suffix)
		}
	}
	return fmt.Sprintf("%.1f TB", value/unit)
}
