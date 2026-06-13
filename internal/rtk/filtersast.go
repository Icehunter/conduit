// Derived from RTK (https://github.com/rtk-ai/rtk).
// Copyright 2024 rtk-ai and rtk-ai Labs
// Licensed under the Apache License, Version 2.0; see LICENSE-APACHE.
// This file has been modified from the original Rust source.

package rtk

import (
	"encoding/json"
	"fmt"
	"strings"
)

// filterAstGrep compresses ast-grep JSON stream output to "file:line: text"
// summaries. Falls back to pass-through for non-JSON lines.
// Caps output at 100 matches to prevent token bloat.
func filterAstGrep(_ string, output string) string {
	lines := strings.Split(output, "\n")
	var results []string
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		var m struct {
			File  string `json:"file"`
			Range struct {
				Start struct {
					Line int `json:"line"`
				} `json:"start"`
			} `json:"range"`
			Text string `json:"text"`
		}
		if err := json.Unmarshal([]byte(line), &m); err == nil && m.File != "" {
			results = append(results, fmt.Sprintf("%s:%d: %s", m.File, m.Range.Start.Line+1, strings.TrimSpace(m.Text)))
		} else if line != "" {
			results = append(results, line)
		}
	}
	if len(results) > 100 {
		return strings.Join(results[:100], "\n") + fmt.Sprintf("\n[%d more matches omitted by RTK]", len(results)-100)
	}
	return strings.Join(results, "\n")
}
