package tui

import (
	"fmt"
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/icehunter/conduit/internal/settings"
)

// pickerItem is one row in a small selection picker.
// JSON tags let commands construct payloads with json.Marshal directly.
type pickerItem struct {
	Value   string `json:"value"` // dispatched as `/<kind> <value>` on Enter
	Label   string `json:"label"` // human-readable display
	Section bool   `json:"section,omitempty"`
}

// pickerState drives the small select-one overlay used by /theme, /model,
// and /output-style. The picker has no awareness of what each kind does:
// on Enter it dispatches `/<kind> <value>` back through the command
// registry, so the underlying command does the actual work.
type pickerState struct {
	kind     string       // "theme" | "model" | "output-style"
	title    string       // header line
	items    []pickerItem // options (caller-ordered)
	selected int          // current cursor row
	current  string       // value to highlight as "active"
	role     string       // provider role for model picker
}

func (m Model) handlePickerKey(msg tea.KeyPressMsg) (Model, tea.Cmd) {
	p := m.picker
	switch msg.String() {
	case "up":
		p.selected = previousPickerSelectable(p.items, p.selected)
	case "down":
		p.selected = nextPickerSelectable(p.items, p.selected)
	case "home", "g":
		p.selected = firstPickerSelectable(p.items)
	case "end", "G":
		p.selected = lastPickerSelectable(p.items)
	case "tab", "right":
		if p.kind == "model" {
			p.role = nextProviderRole(p.role)
			p.current = m.providerValueForRole(p.role)
			p.selected = selectedPickerIndex(p.items, p.current)
		}
	case "shift+tab", "left":
		if p.kind == "model" {
			p.role = previousProviderRole(p.role)
			p.current = m.providerValueForRole(p.role)
			p.selected = selectedPickerIndex(p.items, p.current)
		}
	case "space":
		// Model picker: apply to the current role but keep picker open so the
		// user can assign models to multiple roles without reopening each time.
		// Other pickers: space behaves like enter (apply + close).
		if p.selected < 0 || p.selected >= len(p.items) || p.items[p.selected].Section {
			break
		}
		if p.kind == "model" {
			picked := p.items[p.selected].Value
			role := p.role
			if role == "" {
				role = settings.RoleDefault
			}
			if m.cfg.Commands != nil {
				if res, ok := m.cfg.Commands.Dispatch("/model --role " + role + " " + picked); ok {
					m, _ = m.applyCommandResult(res)
				}
			}
			// Refresh the ● marker to reflect the new assignment.
			if m.picker == nil {
				return m, nil
			}
			m.picker.current = m.providerValueForRole(m.picker.role)
			return m, nil
		}
		// Non-model pickers: apply + close.
		m.picker = nil
		if m.cfg.Commands != nil {
			if res, ok := m.cfg.Commands.Dispatch("/" + p.kind + " " + p.items[p.selected].Value); ok {
				return m.applyCommandResult(res)
			}
		}
		return m, nil
	case "enter":
		if p.selected < 0 || p.selected >= len(p.items) {
			return m, nil
		}
		if p.items[p.selected].Section {
			return m, nil
		}
		picked := p.items[p.selected].Value
		kind := p.kind
		role := p.role
		if role == "" {
			role = settings.RoleDefault
		}
		m.picker = nil
		if m.cfg.Commands == nil {
			return m, nil
		}
		if kind == "model" {
			picked = "--role " + role + " " + picked
		}
		if res, ok := m.cfg.Commands.Dispatch("/" + kind + " " + picked); ok {
			return m.applyCommandResult(res)
		}
		return m, nil
	case "esc", "ctrl+c", "q":
		m.picker = nil
		m.refreshViewport()
		return m, nil
	}
	m.picker = p
	return m, nil
}

func (m Model) renderPicker() string {
	p := m.picker
	if p == nil {
		return ""
	}
	if p.kind == "model" {
		return m.renderModelPicker()
	}
	var sb strings.Builder
	sb.WriteString(styleStatusAccent.Render(p.title))
	sb.WriteString("\n\n")

	for i, it := range p.items {
		if it.Section {
			if i > 0 {
				sb.WriteByte('\n')
			}
			sb.WriteString(styleStatusAccent.Render(it.Label) + "\n")
			continue
		}
		marker := "  "
		if it.Value == p.current {
			marker = "● "
		}
		label := marker + it.Label
		if i == p.selected {
			sb.WriteString(stylePickerItemSelected.Render("❯ "+label) + "\n")
		} else {
			sb.WriteString(stylePickerItem.Render("  "+label) + "\n")
		}
	}
	sb.WriteString("\n" + stylePickerDesc.Render("↑/↓ navigate · Enter select · Escape cancel"))

	return sb.String()
}

func (m Model) renderModelPicker() string {
	p := m.picker
	if p == nil {
		return ""
	}
	contentW := floatingInnerWidth(m.width, floatingModelPickerSpec) - floatingBodyPadX*2
	if contentW < 40 {
		contentW = 40
	}
	headerW := floatingInnerWidth(m.width, floatingModelPickerSpec) - floatingHeaderPadX*2
	if headerW < contentW {
		headerW = contentW
	}

	var sb strings.Builder
	title := panelTitle("Switch Model")
	tabs := renderProviderRoleTabs(p.role)
	ornW := headerW - lipgloss.Width(title) - lipgloss.Width(tabs) - 6
	if ornW < 6 {
		ornW = 6
	}
	sb.WriteString(title + surfaceSpaces(2) + ornamentGradientText(renderSlashFill(ornW)) + surfaceSpaces(2) + tabs)

	bodyRows := modelPickerBodyRows()
	listRows := bodyRows - 6
	if listRows < 6 {
		listRows = 6
	}
	start, end := modelPickerWindow(p.items, p.selected, listRows)
	body := []string{
		"",
		stylePickerDesc.Render("› " + providerRolePrompt(p.role)),
		"",
	}
	if start > 0 {
		body = append(body, stylePickerDesc.Render(fmt.Sprintf("  ↑ %d more above", start)))
	} else {
		body = append(body, "")
	}
	body = append(body, renderModelPickerRows(p.items, p.current, p.selected, start, end, contentW)...)
	for modelPickerBodyListRows(body) < listRows {
		body = append(body, "")
	}
	if end < len(p.items) {
		body = append(body, stylePickerDesc.Render(fmt.Sprintf("  ↓ %d more below", len(p.items)-end)))
	} else {
		body = append(body, "")
	}
	for len(body) < bodyRows-1 {
		body = append(body, "")
	}
	body = append(body, stylePickerDesc.Render("↑/↓ choose · Tab role · Enter assign · Esc close"))
	if len(body) > bodyRows {
		body = body[:bodyRows]
		body[len(body)-1] = stylePickerDesc.Render("↑/↓ choose · Tab role · Enter assign · Esc close")
	}
	sb.WriteString("\n" + strings.Join(body, "\n"))
	return sb.String()
}

func renderModelPickerRows(items []pickerItem, current string, selected, start, end, contentW int) []string {
	rows := make([]string, 0, end-start)
	for i := start; i < end; i++ {
		it := items[i]
		if it.Section {
			if i > start {
				rows = append(rows, "")
			}
			rows = append(rows, renderModelPickerSection(it.Label, sectionHasCurrent(items, i, current), contentW))
			continue
		}
		rows = append(rows, renderModelPickerRow(it, i == selected, it.Value == current, contentW))
	}
	return rows
}

func modelPickerBodyListRows(body []string) int {
	if len(body) <= 4 {
		return 0
	}
	return len(body) - 4
}

func modelPickerBodyRows() int {
	rows := floatingModelPickerSpec.maxHeight - floatingWindowStyle().GetVerticalFrameSize() - 1 - floatingBodyPadY*2
	if rows < 10 {
		return 10
	}
	return rows
}

func modelPickerWindow(items []pickerItem, selected, visibleLines int) (int, int) {
	if visibleLines < 6 {
		visibleLines = 6
	}
	if selected < 0 {
		selected = firstPickerSelectable(items)
	}
	start := selected
	used := 0
	for start > 0 {
		next := pickerItemLineHeight(items, start-1, start)
		if used+next > visibleLines/2 {
			break
		}
		used += next
		start--
	}
	if start > 0 {
		for i := start; i >= 0; i-- {
			if items[i].Section {
				start = i
				break
			}
		}
	}
	if start < 0 {
		start = 0
	}
	end := start
	for end < len(items) && modelPickerRenderedLines(items, start, end+1) <= visibleLines {
		end++
	}
	if end <= selected {
		end = selected + 1
	}
	for modelPickerRenderedLines(items, start, end) > visibleLines && start < selected {
		start++
	}
	return start, end
}

func modelPickerRenderedLines(items []pickerItem, start, end int) int {
	lines := 0
	for i := start; i < end && i < len(items); i++ {
		lines += pickerItemLineHeight(items, i, start)
	}
	return lines
}

func pickerItemLineHeight(items []pickerItem, index, start int) int {
	if index < 0 || index >= len(items) {
		return 0
	}
	if items[index].Section && index > start {
		return 2
	}
	return 1
}

func modelPickerVisibleLines(height int) int {
	visible := height / 3
	if visible < 8 {
		return 8
	}
	if visible > 15 {
		return 15
	}
	return visible
}

var providerRoleOrder = []string{
	settings.RoleDefault,
	settings.RoleMain,
	settings.RoleBackground,
	settings.RolePlanning,
	settings.RoleImplement,
}

func nextProviderRole(role string) string {
	return providerRoleAt(role, 1)
}

func previousProviderRole(role string) string {
	return providerRoleAt(role, -1)
}

func providerRoleAt(role string, delta int) string {
	if role == "" {
		role = settings.RoleDefault
	}
	idx := 0
	for i, r := range providerRoleOrder {
		if r == role {
			idx = i
			break
		}
	}
	idx = (idx + delta + len(providerRoleOrder)) % len(providerRoleOrder)
	return providerRoleOrder[idx]
}

func renderProviderRoleTabs(active string) string {
	if active == "" {
		active = settings.RoleDefault
	}
	labels := map[string]string{
		settings.RoleDefault:    "Default",
		settings.RoleMain:       "Main",
		settings.RoleBackground: "Background",
		settings.RolePlanning:   "Planning",
		settings.RoleImplement:  "Implement",
	}
	activeIdx := 0
	tabLabels := make([]string, 0, len(providerRoleOrder))
	for i, role := range providerRoleOrder {
		if role == active {
			activeIdx = i
		}
		tabLabels = append(tabLabels, labels[role])
	}
	return settingsColorTabs(tabLabels, activeIdx)
}

func providerRolePrompt(role string) string {
	switch role {
	case settings.RoleMain:
		return "Choose a provider for main agent tasks"
	case settings.RoleBackground:
		return "Choose a provider for background, simple tasks"
	case settings.RolePlanning:
		return "Choose a provider for planning and architecture tasks"
	case settings.RoleImplement:
		return "Choose a provider for bounded implementation offload tasks"
	default:
		return "Choose a provider for default permission mode"
	}
}

func renderModelPickerSection(label string, configured bool, width int) string {
	status := ""
	if configured {
		status = surfaceSpaces(2) + styleModeCyan.Render("✓") + surfaceSpaces(1) + stylePickerDesc.Render("configured")
	}
	labelPart := stylePickerDesc.Render(label)
	ruleW := width - lipgloss.Width(labelPart) - lipgloss.Width(status) - 4
	if ruleW < 4 {
		ruleW = 4
	}
	return labelPart + surfaceSpaces(2) + panelRule(ruleW) + status
}

func renderModelPickerRow(it pickerItem, selected, current bool, width int) string {
	marker := "  "
	if current {
		marker = "● "
	}
	label := marker + it.Label
	provider := modelPickerProviderLabel(it.Value)
	gap := width - lipgloss.Width("❯ "+label) - lipgloss.Width(provider) - 2
	if gap < 2 {
		gap = 2
	}
	line := "  " + label + surfaceSpaces(gap) + provider
	if selected {
		return stylePickerItemSelected.Render("❯ " + label + surfaceSpaces(gap) + provider)
	}
	return stylePickerItem.Render(line)
}

func modelPickerProviderLabel(value string) string {
	if strings.HasPrefix(value, "local:") {
		return stylePickerDesc.Render("MCP")
	}
	if strings.HasPrefix(value, "provider:anthropic-api.") {
		return stylePickerDesc.Render("Anthropic API")
	}
	if strings.HasPrefix(value, "provider:openai-compatible.") {
		return stylePickerDesc.Render("OpenAI compat")
	}
	if strings.HasPrefix(value, "provider:claude-subscription.") {
		return stylePickerDesc.Render("Claude")
	}
	if strings.HasPrefix(value, "anthropic-api:") {
		return stylePickerDesc.Render("Anthropic API")
	}
	return stylePickerDesc.Render("Claude")
}

func sectionHasCurrent(items []pickerItem, sectionIndex int, current string) bool {
	for i := sectionIndex + 1; i < len(items); i++ {
		if items[i].Section {
			return false
		}
		if items[i].Value == current {
			return true
		}
	}
	return false
}

func firstPickerSelectable(items []pickerItem) int {
	for i, it := range items {
		if !it.Section {
			return i
		}
	}
	return 0
}

func lastPickerSelectable(items []pickerItem) int {
	for i := len(items) - 1; i >= 0; i-- {
		if !items[i].Section {
			return i
		}
	}
	return 0
}

func previousPickerSelectable(items []pickerItem, selected int) int {
	for i := selected - 1; i >= 0; i-- {
		if !items[i].Section {
			return i
		}
	}
	return selected
}

func nextPickerSelectable(items []pickerItem, selected int) int {
	for i := selected + 1; i < len(items); i++ {
		if !items[i].Section {
			return i
		}
	}
	return selected
}

func selectedPickerIndex(items []pickerItem, value string) int {
	for i, it := range items {
		if !it.Section && it.Value == value {
			return i
		}
	}
	return firstPickerSelectable(items)
}
