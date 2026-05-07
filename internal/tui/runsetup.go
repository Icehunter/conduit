package tui

import (
	"fmt"
	"sort"
	"strings"

	"github.com/icehunter/conduit/internal/api"
	"github.com/icehunter/conduit/internal/auth"
	"github.com/icehunter/conduit/internal/settings"
)

// SummarizeMessages renders the last n model-visible messages as a
// plain-text transcript for /memory extract. Tool blocks are flattened so
// the sub-agent sees a readable conversation, not raw JSON. Exported
// because cmd/conduit/main.go's auto-extract path also needs it.
func SummarizeMessages(history []api.Message, n int) string {
	if len(history) == 0 {
		return ""
	}
	start := 0
	if len(history) > n {
		start = len(history) - n
	}
	var sb strings.Builder
	for _, m := range history[start:] {
		role := strings.ToUpper(m.Role)
		sb.WriteString("---\n")
		sb.WriteString(role + ":\n")
		for _, b := range m.Content {
			switch b.Type {
			case "text":
				sb.WriteString(b.Text)
				sb.WriteString("\n")
			case "tool_use":
				fmt.Fprintf(&sb, "[tool_use %s]\n", b.Name)
			case "tool_result":
				txt := b.Text
				if len(txt) > 500 {
					txt = txt[:500] + "…"
				}
				sb.WriteString("[tool_result]\n")
				sb.WriteString(txt)
				sb.WriteString("\n")
			}
		}
	}
	return sb.String()
}

func configuredAccountProviders() []settings.ActiveProviderSettings {
	email := auth.ActiveEmail()
	store, err := auth.ListAccounts()
	if err != nil {
		return nil
	}
	var providers []settings.ActiveProviderSettings
	seen := map[string]bool{}
	addProvider := func(entry auth.AccountEntry) {
		kind := accountProviderKind(entry.Kind)
		if kind == "" || entry.Email == "" {
			return
		}
		key := kind + "\x00" + entry.Email
		if seen[key] {
			return
		}
		providers = append(providers, settings.ActiveProviderSettings{
			Kind:    kind,
			Account: entry.Email,
		})
		seen[key] = true
	}
	if entry, ok := store.Accounts[email]; ok {
		addProvider(entry)
	}
	var entries []auth.AccountEntry
	for _, entry := range store.Accounts {
		entries = append(entries, entry)
	}
	sort.Slice(entries, func(i, j int) bool {
		left := accountProviderKind(entries[i].Kind) + "\x00" + entries[i].Email
		right := accountProviderKind(entries[j].Kind) + "\x00" + entries[j].Email
		return left < right
	})
	for _, entry := range entries {
		addProvider(entry)
	}
	return providers
}

func accountProviderKind(accountKind string) string {
	if accountKind == auth.AccountKindAnthropicConsole {
		return settings.ProviderKindAnthropicAPI
	}
	if accountKind == "" || accountKind == auth.AccountKindClaudeAI {
		return settings.ProviderKindClaudeSubscription
	}
	return ""
}
