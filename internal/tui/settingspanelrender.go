package tui

// View-side rendering for the settings panel.

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"charm.land/lipgloss/v2"

	"github.com/icehunter/conduit/internal/lsp"
	"github.com/icehunter/conduit/internal/session"
	"github.com/icehunter/conduit/internal/settings"
)

// ──────────────────────────────────────────────────────────────────────────────
// Renderer — matches plugin panel style exactly
// ──────────────────────────────────────────────────────────────────────────────

func (m Model) renderSettingsPanel() string {
	p := m.settingsPanel
	if p == nil {
		return ""
	}

	if p.getStatus != nil {
		snap := p.getStatus()
		p.rebuildConfigItemsFromSnap(snap)
		p.applyFilter()
	}

	w := m.width
	w = max(w, 10)
	panelH := m.panelHeight() - 1
	if panelH < 8 {
		panelH = m.panelHeight()
	}
	// lipgloss v2's Width() is total block width (including border + padding).
	// Outer style: Width(w-2), border 1 each side (2), padding 2 each side (4)
	// → content area = (w-2) - 2 - 4 = w - 8. v1 was w-6 because Width was
	// content-only there.
	innerW := w - 8

	var sb strings.Builder

	// ── Crush-style panel header + tab selector ────────────────────────────
	title := panelTitle("Settings")
	tabs := settingsColorTabs(settingsTabNames, int(p.tab))

	ornW := innerW - lipgloss.Width(title) - lipgloss.Width(tabs) - 4
	ornW = max(ornW, 6)
	sb.WriteString(title + surfaceSpaces(2) + ornamentGradientText(renderSlashFill(ornW)) + surfaceSpaces(2) + tabs)
	sb.WriteString("\n\n")

	contentH := panelH - 3
	contentH = max(contentH, 4)

	// ── Tab body ───────────────────────────────────────────────────────────────
	switch p.tab {
	case settingsTabStatus:
		m.renderSettingsStatus(&sb, p, innerW, contentH)
	case settingsTabConfig:
		m.renderSettingsConfig(&sb, p, innerW, contentH)
	case settingsTabStats:
		m.renderSettingsStats(&sb, p, innerW, contentH)
	case settingsTabUsage:
		m.renderSettingsUsage(&sb, p, innerW, contentH)
	case settingsTabProviders:
		m.renderSettingsProviders(&sb, p, innerW, contentH)
	case settingsTabAccounts:
		m.renderSettingsAccounts(&sb, p, innerW, contentH)
	}

	return panelFrameStyle(w, panelH).Render(sb.String())
}

func settingsColorTabs(labels []string, active int) string {
	parts := make([]string, 0, len(labels))
	for i, label := range labels {
		if i == active {
			parts = append(parts, styleStatusAccent.Render(label))
		} else {
			parts = append(parts, stylePickerDesc.Render(label))
		}
	}
	return strings.Join(parts, surfaceSpaces(2))
}

// ── Status tab ────────────────────────────────────────────────────────────────

func (m Model) renderSettingsStatus(sb *strings.Builder, p *settingsPanelState, innerW, _ int) {
	snap := statusSnapshot{}
	if p.getStatus != nil {
		snap = p.getStatus()
	}

	bold := fgOnBg(colorFg).Bold(true)
	dim := stylePickerDesc

	row := func(label, value string) {
		sb.WriteString(bold.Render(label+":") + surfaceSpaces(1) + fgOnBg(colorFg).Render(value) + "\n")
	}

	authStatus := dim.Render("not found")
	if snap.authenticated {
		authStatus = "CLAUDE_CODE_OAUTH_TOKEN"
	}
	row("Auth token", authStatus)

	modelName := snap.model
	if modelName == "" {
		modelName = "claude-sonnet-4-6"
	}
	row("Model", modelName+" · "+dim.Render(modelDescription(modelName)))

	sb.WriteByte('\n')
	row("Version", m.cfg.Version)
	row("Session ID", truncateStr(snap.sessionID, 40))

	title := ""
	if p.sessPath != "" {
		title = session.ExtractTitle(p.sessPath)
	}
	if title == "" {
		title = dim.Render("/rename to add a name")
	}
	row("Session name", title)

	cwd, _ := os.Getwd()
	row("cwd", truncateStr(cwd, innerW-8))

	sb.WriteByte('\n')
	if p.getMCPInfo != nil {
		mcpRows := p.getMCPInfo()
		connected := 0
		for _, r := range mcpRows {
			if strings.Contains(r.status, "connected") {
				connected++
			}
		}
		row("MCP servers", fmt.Sprintf("%d connected · /mcp", connected))
	}

	if m.lspManager != nil {
		statuses := m.lspManager.Statuses()
		if len(statuses) > 0 {
			// Sort lang keys for stable display order.
			keys := make([]string, 0, len(statuses))
			for k := range statuses {
				keys = append(keys, k)
			}
			// Simple insertion sort — at most ~15 entries.
			for i := 1; i < len(keys); i++ {
				for j := i; j > 0 && keys[j] < keys[j-1]; j-- {
					keys[j], keys[j-1] = keys[j-1], keys[j]
				}
			}
			lspParts := make([]string, 0, len(keys))
			for _, k := range keys {
				s := statuses[k]
				switch s {
				case lsp.StatusConnected:
					lspParts = append(lspParts, lipgloss.NewStyle().Foreground(lipgloss.Color("#3fb950")).Render(k+" ✓"))
				case lsp.StatusConnecting:
					lspParts = append(lspParts, dim.Render(k+" …"))
				case lsp.StatusBroken:
					lspParts = append(lspParts, lipgloss.NewStyle().Foreground(colorError).Render(k+" ✗"))
				case lsp.StatusDisabled:
					lspParts = append(lspParts, dim.Render(k+" (disabled)"))
				}
			}
			row("LSP servers", strings.Join(lspParts, "  "))
		} else {
			row("LSP servers", dim.Render("none active · tools start on first use"))
		}
	}

	sb.WriteByte('\n')
	var sources []string
	cwd2, _ := os.Getwd()
	for _, f := range []struct{ path, label string }{
		{settings.ConduitSettingsPath(), "Conduit settings"},
		{filepath.Join(cwd2, ".claude", "settings.json"), "Project settings"},
		{filepath.Join(cwd2, ".claude", "settings.local.json"), "Project local settings"},
	} {
		if _, err := os.Stat(f.path); err == nil {
			sources = append(sources, f.label)
		}
	}
	if len(sources) == 0 {
		sources = []string{"none"}
	}
	row("Settings", strings.Join(sources, ", "))

	sb.WriteByte('\n')
	sb.WriteString(dim.Render("Platform: "+runtime.GOOS+"/"+runtime.GOARCH+" · Go: "+runtime.Version()) + "\n")
}

func modelDescription(model string) string {
	switch {
	case strings.Contains(model, "opus"):
		return "Most capable model"
	case strings.Contains(model, "sonnet"):
		return "Best for everyday tasks"
	case strings.Contains(model, "haiku"):
		return "Fastest and most compact"
	default:
		return "Configured model"
	}
}

// ── Config tab ────────────────────────────────────────────────────────────────

func (m Model) renderSettingsConfig(sb *strings.Builder, p *settingsPanelState, _, contentH int) {
	// Show focus state in header hint.
	if p.cfgFocus == configFocusHeader {
		sb.WriteString(stylePickerDesc.Render("  ↓ to navigate settings") + "\n\n")
	} else if p.search != "" {
		sb.WriteString(styleStatusAccent.Render("  Filter: "+p.search) +
			stylePickerDesc.Render("  Backspace clear · ↑ tabs") + "\n\n")
	} else {
		sb.WriteString(stylePickerDesc.Render("  Type to filter · ↑ to tabs · ←/→ cycle enum · Enter toggle") + "\n\n")
	}

	visible := contentH - 2
	visible = max(visible, 3)

	start := 0
	if p.cfgFocus == configFocusList && p.selected >= visible {
		start = p.selected - visible + 1
	}

	count := 0
	for i := start; i < len(p.filteredIdx) && count < visible; i++ {
		item := p.configItems[p.filteredIdx[i]]
		isSel := p.cfgFocus == configFocusList && i == p.selected

		cursor := "  "
		if isSel {
			cursor = styleStatusAccent.Render("❯ ")
		}

		var line string
		// Always wrap label/value in explicit fg styles so they render with
		// theme colors instead of inheriting the terminal's default fg
		// (which is light on dark terminals — invisible on light theme).
		labelStyle := fgOnBg(colorFg)
		valueStyle := fgOnBg(colorFg).Bold(true) // values stand out with bold
		switch item.kind {
		case "bool":
			dot := stylePickerDesc.Render("○")
			if item.on {
				dot = styleStatusAccent.Render("●")
			}
			label := labelStyle.Render(item.label)
			if isSel {
				label = styleStatusAccent.Render(item.label)
			}
			line = cursor + dot + surfaceSpaces(1) + label
		case "enum":
			label := labelStyle.Render(item.label)
			if isSel {
				label = styleStatusAccent.Render(item.label)
			}
			var val string
			if isSel {
				val = styleStatusAccent.Render("‹ " + item.value + " ›")
			} else {
				val = valueStyle.Render(item.value)
			}
			line = cursor + label + surfaceSpaces(2) + val
		case "info":
			label := labelStyle.Render(item.label)
			if isSel {
				label = styleStatusAccent.Render(item.label)
			}
			val := valueStyle.Render(item.value)
			line = cursor + label + surfaceSpaces(2) + val
		}
		sb.WriteString(line + "\n")
		count++
	}

	remaining := len(p.filteredIdx) - start - count
	if remaining > 0 {
		sb.WriteString(stylePickerDesc.Render(fmt.Sprintf("\n  ↓ %d more below", remaining)))
	}
}
