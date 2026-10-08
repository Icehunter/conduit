package compact

import (
	"fmt"
	"sort"
	"strings"

	"github.com/icehunter/conduit/internal/api"
	"github.com/icehunter/conduit/internal/transcript"
)

const (
	localSnippetChars    = 400
	localSnippetCount    = 3
	maxRecoveredSumChars = 16_000
)

// LocalSummary builds a deterministic summary without a model call. It is the
// fallback when the summarizer returns nothing usable, so compaction can still
// shrink the history instead of failing or wiping it. It lists the message
// counts, the tools used and the last assistant outputs; the verbatim user
// messages and anchors are added separately by the compaction-context block.
func LocalSummary(msgs []api.Message, o Options) string {
	users, assistants := 0, 0
	tools := map[string]int{}
	var snippets []string
	for _, m := range msgs {
		switch m.Role {
		case "user":
			users++
		case "assistant":
			assistants++
		}
		var text []string
		for _, b := range m.Content {
			switch b.Type {
			case "tool_use":
				tools[b.Name]++
			case "text":
				if m.Role == "assistant" && strings.TrimSpace(b.Text) != "" {
					text = append(text, strings.TrimSpace(b.Text))
				}
			}
		}
		if len(text) > 0 {
			snippets = append(snippets, transcript.Truncate(strings.Join(text, "\n"), localSnippetChars))
		}
	}

	var sb strings.Builder
	sb.WriteString("## Current State\n")
	sb.WriteString("- Automatic fallback summary: the summarizer model returned nothing usable, so this was built without a model.\n")
	fmt.Fprintf(&sb, "- %d earlier messages were compacted (%d user, %d assistant).\n", len(msgs), users, assistants)
	if o.Kind != "" {
		fmt.Fprintf(&sb, "- Compaction kind: %s.\n", o.Kind)
	}
	if len(tools) > 0 {
		names := make([]string, 0, len(tools))
		for n := range tools {
			names = append(names, n)
		}
		sort.Slice(names, func(i, j int) bool {
			if tools[names[i]] != tools[names[j]] {
				return tools[names[i]] > tools[names[j]]
			}
			return names[i] < names[j]
		})
		parts := make([]string, len(names))
		for i, n := range names {
			parts[i] = fmt.Sprintf("%s x%d", n, tools[n])
		}
		sb.WriteString("- Tools used: " + strings.Join(parts, ", ") + ".\n")
	}
	if len(snippets) > localSnippetCount {
		snippets = snippets[len(snippets)-localSnippetCount:]
	}
	if len(snippets) > 0 {
		sb.WriteString("\n## Last Assistant Output\n")
		for _, s := range snippets {
			sb.WriteString("- " + strings.ReplaceAll(s, "\n", "\n  ") + "\n")
		}
	}
	sb.WriteString("\n## Exact Next Steps\n")
	sb.WriteString("- Re-read the user messages and anchor index below, then continue the most recent request.\n")
	return strings.TrimSpace(sb.String())
}
