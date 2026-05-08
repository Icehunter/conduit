package tui

import (
	"encoding/json"
	"os"
	"path/filepath"
	"time"
)

type councilLogEntry struct {
	TS           string  `json:"ts"`
	Kind         string  `json:"kind"`
	Members      int     `json:"members"`
	RoundsRun    int     `json:"rounds_run"`
	Converged    bool    `json:"converged"`
	CostUSD      float64 `json:"cost_usd,omitempty"`
	InputTokens  int     `json:"input_tokens"`
	OutputTokens int     `json:"output_tokens"`
	DurationMs   int64   `json:"duration_ms"`
}

// appendCouncilLogEntry appends one JSONL record to ~/.conduit/council-log.jsonl.
// Best-effort: errors are silently discarded.
func appendCouncilLogEntry(e councilLogEntry) {
	e.TS = time.Now().UTC().Format(time.RFC3339)
	home, err := os.UserHomeDir()
	if err != nil {
		return
	}
	path := filepath.Join(home, ".conduit", "council-log.jsonl")
	b, err := json.Marshal(e)
	if err != nil {
		return
	}
	b = append(b, '\n')
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return
	}
	defer f.Close()
	_, _ = f.Write(b)
}
