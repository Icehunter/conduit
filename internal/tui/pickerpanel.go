package tui

import (
	"fmt"
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/icehunter/conduit/internal/catalog"
	"github.com/icehunter/conduit/internal/permissions"
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
	kind             string       // "theme" | "model" | "output-style"
	title            string       // header line
	items            []pickerItem // options (caller-ordered)
	allItems         []pickerItem // unfiltered options for type-to-filter pickers
	selected         int          // current cursor row
	current          string       // value to highlight as "active"
	role             string       // provider role for model picker
	filter           string       // live filter text for model picker
	showCapabilities bool         // ? key toggles capability badges in model picker
}

func (m Model) handlePickerKey(msg tea.KeyPressMsg) (Model, tea.Cmd) {
	p := m.picker
	switch msg.String() {
	case "up":
		p.selected = previousPickerSelectable(p.items, p.selected)
		m.picker = p
		return m, nil
	case "down":
		p.selected = nextPickerSelectable(p.items, p.selected)
		m.picker = p
		return m, nil
	case "home":
		p.selected = firstPickerSelectable(p.items)
		m.picker = p
		return m, nil
	case "end":
		p.selected = lastPickerSelectable(p.items)
		m.picker = p
		return m, nil
	case "tab", "right":
		if p.kind == "model" {
			next := nextProviderRole(p.role)
			p.role = next
			if next == roleCouncil {
				// Council tab: multi-select — no single current value.
				p.current = ""
				p.selected = firstPickerSelectable(p.items)
			} else {
				p.current = m.providerValueForRole(next)
				p.selected = selectedPickerIndex(p.items, p.current)
			}
		}
		m.picker = p
		return m, nil
	case "shift+tab", "left":
		if p.kind == "model" {
			prev := previousProviderRole(p.role)
			p.role = prev
			if prev == roleCouncil {
				// Council tab: multi-select — no single current value.
				p.current = ""
				p.selected = firstPickerSelectable(p.items)
			} else {
				p.current = m.providerValueForRole(prev)
				p.selected = selectedPickerIndex(p.items, p.current)
			}
		}
		m.picker = p
		return m, nil
	case "space":
		// Model picker (council tab): toggle provider in/out of council roster.
		// Model picker (other tabs): apply to role but keep open.
		// Mode picker: apply mode + close.
		// Other pickers: space behaves like enter (apply + close).
		if p.selected < 0 || p.selected >= len(p.items) || p.items[p.selected].Section {
			break
		}
		if p.kind == "mode" {
			picked := p.items[p.selected].Value
			m.picker = nil
			m.applyPermissionMode(permissions.Mode(picked))
			return m, m.rebuildSystemCmd()
		}
		if p.kind == "model" && p.role == roleCouncil {
			// Toggle this provider key in m.councilProviders.
			val := p.items[p.selected].Value
			found := false
			for i, cp := range m.councilProviders {
				if cp == val {
					m.councilProviders = append(m.councilProviders[:i], m.councilProviders[i+1:]...)
					found = true
					break
				}
			}
			if !found {
				m.councilProviders = append(m.councilProviders, val)
			}
			// Keep picker open; re-point to same state.
			m.picker = p
			return m, nil
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
		if p.kind == "mode" {
			picked := p.items[p.selected].Value
			m.picker = nil
			m.applyPermissionMode(permissions.Mode(picked))
			return m, m.rebuildSystemCmd()
		}
		if p.kind == "model" && p.role == roleCouncil {
			// Save the current council roster and close.
			roster := append([]string(nil), m.councilProviders...)
			_ = settings.UpdateConduitConfig(func(cfg *settings.ConduitConfig) {
				cfg.CouncilProviders = roster
			})
			m.picker = nil
			m.refreshViewport()
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
	case "esc", "ctrl+c":
		if p.kind == "model" && p.filter != "" {
			p.filter = ""
			p.items = pickerAllItems(p)
			p.selected = selectedPickerIndex(p.items, p.current)
			m.picker = p
			m.refreshViewport()
			return m, nil
		}
		if p.kind == "model" && p.role == roleCouncil {
			roster := append([]string(nil), m.councilProviders...)
			_ = settings.UpdateConduitConfig(func(cfg *settings.ConduitConfig) {
				cfg.CouncilProviders = roster
			})
		}
		m.picker = nil
		m.refreshViewport()
		return m, nil
	case "q":
		if p.kind == "model" {
			break
		}
		m.picker = nil
		m.refreshViewport()
		return m, nil
	case "?":
		if p.kind == "model" {
			p.showCapabilities = !p.showCapabilities
			m.picker = p
			m.refreshViewport()
			return m, nil
		}
	case "backspace":
		if p.kind == "model" && p.filter != "" {
			p.filter = dropLastRune(p.filter)
			p.items = filterPickerItems(pickerAllItems(p), p.filter)
			p.selected = firstPickerSelectable(p.items)
			if p.selected < 0 {
				p.selected = 0
			}
			m.picker = p
			m.refreshViewport()
			return m, nil
		}
	}
	if p.kind == "model" {
		if text := pickerFilterText(msg); text != "" {
			p.filter += text
			p.items = filterPickerItems(pickerAllItems(p), p.filter)
			p.selected = firstPickerSelectable(p.items)
			if p.selected < 0 {
				p.selected = 0
			}
			m.picker = p
			m.refreshViewport()
			return m, nil
		}
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
	if p.kind == "mode" {
		return m.renderModePicker()
	}
	contentW := floatingInnerWidth(m.width, floatingPickerSpec)
	contentW = max(contentW, 20)
	var sb strings.Builder
	sb.WriteString(styleStatusAccent.Render(truncatePlainToWidth(p.title, contentW)))
	sb.WriteString("\n\n")

	for i, it := range p.items {
		if it.Section {
			if i > 0 {
				sb.WriteByte('\n')
			}
			sb.WriteString(styleStatusAccent.Render(truncatePlainToWidth(it.Label, contentW)) + "\n")
			continue
		}
		marker := "  "
		if it.Value == p.current {
			marker = "● "
		}
		label := marker + truncatePlainToWidth(it.Label, max(contentW-4, 8))
		if i == p.selected {
			sb.WriteString(stylePickerItemSelected.Render("❯ "+label) + "\n")
		} else {
			sb.WriteString(stylePickerItem.Render("  "+label) + "\n")
		}
	}
	sb.WriteString("\n" + stylePickerDesc.Render("↑/↓ navigate · Enter select · Escape cancel"))

	return sb.String()
}

// renderModePicker renders the 5-item permission mode picker.
func (m Model) renderModePicker() string {
	p := m.picker
	if p == nil {
		return ""
	}
	var sb strings.Builder
	sb.WriteString(styleStatusAccent.Render("Switch Mode"))
	sb.WriteString("\n\n")

	for i, it := range p.items {
		marker := "○ "
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
	contentW := floatingInnerWidth(m.width, floatingModelPickerSpec) - floatingBodyPadX*2 - 2
	contentW = max(contentW, 40)
	headerW := floatingInnerWidth(m.width, floatingModelPickerSpec) - floatingHeaderPadX*2
	if headerW < contentW {
		headerW = contentW
	}

	var sb strings.Builder
	title := panelTitle("Switch Model")
	tabs := renderProviderRoleTabs(p.role)
	ornW := headerW - lipgloss.Width(title) - lipgloss.Width(tabs) - 10
	ornW = max(ornW, 6)
	sb.WriteString(title + surfaceSpaces(2) + ornamentGradientText(renderSlashFill(ornW)) + surfaceSpaces(2) + tabs)

	bodyRows := modelPickerBodyRows()
	listRows := bodyRows - 6
	listRows = max(listRows, 6)
	// When capabilities are shown, the selected row gains an extra info line —
	// reserve one row so it isn't clipped by the body row budget.
	capListRows := listRows
	if p.showCapabilities {
		capListRows = max(listRows-1, 4)
	}
	start, end := modelPickerWindow(p.items, p.selected, capListRows)
	body := []string{
		"",
		stylePickerDesc.Render("› " + providerRolePrompt(p.role)),
		"",
	}
	if p.filter != "" {
		body[2] = stylePickerDesc.Render(truncatePlainToWidth(fmt.Sprintf("  Filter: %q (%d matches)", p.filter, countPickerSelectable(p.items)), contentW))
	}
	if start > 0 {
		body = append(body, stylePickerDesc.Render(truncatePlainToWidth(fmt.Sprintf("  ↑ %d more above", start), contentW)))
	} else {
		body = append(body, "")
	}
	if p.role == roleCouncil {
		body = append(body, renderModelPickerCouncilRows(p.items, m.councilProviders, p.selected, start, end, contentW)...)
	} else {
		body = append(body, renderModelPickerRows(p.items, p.current, p.selected, start, end, contentW, m.catalogData, p.showCapabilities)...)
	}
	if countPickerSelectable(p.items) == 0 {
		body = append(body, stylePickerDesc.Render(truncatePlainToWidth("  No models match the filter.", contentW)))
	}
	for modelPickerBodyListRows(body) < capListRows {
		body = append(body, "")
	}
	if end < len(p.items) {
		body = append(body, stylePickerDesc.Render(truncatePlainToWidth(fmt.Sprintf("  ↓ %d more below", len(p.items)-end), contentW)))
	} else {
		body = append(body, "")
	}
	// bodyRows includes 1 line for the footer.
	for len(body) < bodyRows-1 {
		body = append(body, "")
	}
	capHint := ""
	if m.catalogData != nil {
		if p.showCapabilities {
			capHint = " · ? hide caps"
		} else {
			capHint = " · ? capabilities"
		}
	} else {
		capHint = " · ? (refresh catalog first)"
	}
	if p.role == roleCouncil {
		body = append(body, stylePickerDesc.Render(truncatePlainToWidth("Type filter · ↑/↓ choose · Space toggle · Enter save · Esc close", contentW)))
	} else {
		body = append(body, stylePickerDesc.Render(truncatePlainToWidth("Type filter · ↑/↓ choose · Tab role · Enter assign · Esc close"+capHint, contentW)))
	}
	if len(body) > bodyRows {
		body = body[:bodyRows]
		if p.role == roleCouncil {
			body[len(body)-1] = stylePickerDesc.Render(truncatePlainToWidth("Type filter · ↑/↓ choose · Space toggle · Enter save · Esc close", contentW))
		} else {
			body[len(body)-1] = stylePickerDesc.Render(truncatePlainToWidth("Type filter · ↑/↓ choose · Tab role · Enter assign · Esc close"+capHint, contentW))
		}
	}
	sb.WriteString("\n" + strings.Join(body, "\n"))
	return sb.String()
}

// renderModelPickerCouncilRows renders items for the council multi-select tab.
// Items show ● if their Value is in councilProviders, ○ otherwise.
func renderModelPickerCouncilRows(items []pickerItem, councilProviders []string, selected, start, end, contentW int) []string {
	inCouncil := make(map[string]bool, len(councilProviders))
	for _, cp := range councilProviders {
		inCouncil[cp] = true
	}
	start, end = clampPickerRange(items, start, end)
	rows := make([]string, 0, end-start)
	for i := start; i < end; i++ {
		it := items[i]
		if it.Section {
			if i > start {
				rows = append(rows, "")
			}
			rows = append(rows, renderModelPickerSection(it.Label, false, contentW))
			continue
		}
		rows = append(rows, renderModelPickerRow(it, i == selected, inCouncil[it.Value], contentW))
	}
	return rows
}

func renderModelPickerRows(items []pickerItem, current string, selected, start, end, contentW int, cat *catalog.Catalog, showCaps bool) []string {
	start, end = clampPickerRange(items, start, end)
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
		isSelected := i == selected
		rows = append(rows, renderModelPickerRow(it, isSelected, it.Value == current, contentW))
		// Capability badge: emitted as a separate slice entry so the body
		// line-count arithmetic stays accurate (lipgloss clips by line count).
		if showCaps && isSelected && cat != nil {
			modelID := modelIDFromPickerValue(it.Value)
			if info, ok := cat.Lookup(modelID); ok {
				if badge := formatModelCapBadge(info); badge != "" {
					rows = append(rows, stylePickerDesc.Render(badge))
				}
			}
		}
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
	visibleLines = max(visibleLines, 6)
	if len(items) == 0 {
		return 0, 0
	}
	if selected < 0 {
		selected = firstPickerSelectable(items)
	}
	if selected < 0 || selected >= len(items) {
		return 0, 0
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
	start = max(start, 0)
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

func clampPickerRange(items []pickerItem, start, end int) (int, int) {
	if len(items) == 0 {
		return 0, 0
	}
	start = max(start, 0)
	if start > len(items) {
		start = len(items)
	}
	if end < start {
		end = start
	}
	if end > len(items) {
		end = len(items)
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

// roleCouncil is the virtual role name used for the council multi-select tab.
const roleCouncil = "council"

var providerRoleOrder = []string{
	settings.RoleDefault,
	settings.RoleMain,
	settings.RoleBackground,
	settings.RolePlanning,
	settings.RoleImplement,
	roleCouncil,
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
		roleCouncil:             "Council",
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
	case roleCouncil:
		return "Council Members — toggle providers with Space"
	default:
		return "Choose a provider for default permission mode"
	}
}

func renderModelPickerSection(label string, configured bool, width int) string {
	status := ""
	if configured {
		status = surfaceSpaces(2) + styleModeCyan.Render("✓") + surfaceSpaces(1) + stylePickerDesc.Render("configured")
	}
	statusW := lipgloss.Width(status)
	labelMax := width - statusW - 8
	labelMax = max(labelMax, 8)
	label = truncatePlainToWidth(label, labelMax)
	labelPart := stylePickerDesc.Render(label)
	ruleW := width - lipgloss.Width(labelPart) - statusW - 4
	ruleW = max(ruleW, 4)
	return labelPart + surfaceSpaces(2) + panelRule(ruleW) + status
}

func renderModelPickerRow(it pickerItem, selected, current bool, width int) string {
	marker := "  "
	if current {
		marker = "● "
	}
	providerText := modelPickerProviderText(it.Value)
	providerW := min(max(lipgloss.Width(providerText), 10), max(width/4, 10))
	provider := modelPickerProviderLabelWithWidth(it.Value, providerW)
	prefix := "  "
	if selected {
		prefix = "❯ "
	}
	gap := 2
	labelW := width - lipgloss.Width(prefix) - gap - providerW
	labelW = max(labelW, 8)
	label := padPlainToWidth(truncatePlainToWidth(marker+it.Label, labelW), labelW)
	line := prefix + label + surfaceSpaces(gap) + provider
	if selected {
		return stylePickerItemSelected.Render(line)
	}
	return stylePickerItem.Render(line)
}

func modelPickerProviderLabel(value string) string {
	return modelPickerProviderLabelWithWidth(value, lipgloss.Width(modelPickerProviderText(value)))
}

func modelPickerProviderLabelWithWidth(value string, width int) string {
	return stylePickerDesc.Render(padPlainToWidth(truncatePlainToWidth(modelPickerProviderText(value), width), width))
}

func modelPickerProviderText(value string) string {
	if strings.HasPrefix(value, "local:") {
		return "MCP"
	}
	if strings.HasPrefix(value, "provider:anthropic-api.") {
		return "Anthropic API"
	}
	if strings.HasPrefix(value, "provider:openai-compatible.") {
		return "OpenAI"
	}
	if strings.HasPrefix(value, "provider:claude-subscription.") {
		return "Claude"
	}
	if strings.HasPrefix(value, "anthropic-api:") {
		return "Anthropic API"
	}
	return "Claude"
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

func countPickerSelectable(items []pickerItem) int {
	count := 0
	for _, item := range items {
		if !item.Section {
			count++
		}
	}
	return count
}

func pickerAllItems(p *pickerState) []pickerItem {
	if p == nil || len(p.allItems) == 0 {
		if p == nil {
			return nil
		}
		return p.items
	}
	return p.allItems
}

func filterPickerItems(items []pickerItem, query string) []pickerItem {
	query = strings.ToLower(strings.TrimSpace(query))
	if query == "" {
		return items
	}
	out := make([]pickerItem, 0, len(items))
	for i := 0; i < len(items); i++ {
		item := items[i]
		if !item.Section {
			if pickerItemMatches(item, query) {
				out = append(out, item)
			}
			continue
		}
		start := len(out)
		out = append(out, item)
		for i+1 < len(items) && !items[i+1].Section {
			i++
			if pickerItemMatches(items[i], query) {
				out = append(out, items[i])
			}
		}
		if len(out) == start+1 {
			out = out[:start]
		}
	}
	return out
}

func pickerItemMatches(item pickerItem, query string) bool {
	haystack := strings.ToLower(item.Label + " " + item.Value + " " + modelIDFromPickerValue(item.Value))
	return strings.Contains(haystack, query)
}

func pickerFilterText(msg tea.KeyPressMsg) string {
	if msg.Mod != 0 {
		return ""
	}
	if msg.Text != "" {
		if isPrintableFilterText(msg.Text) {
			return msg.Text
		}
		return ""
	}
	if msg.Code >= 32 && msg.Code != 127 {
		return string(msg.Code)
	}
	return ""
}

func isPrintableFilterText(s string) bool {
	for _, r := range s {
		if r < 32 || r == 127 {
			return false
		}
	}
	return s != ""
}

func dropLastRune(s string) string {
	r := []rune(s)
	if len(r) == 0 {
		return ""
	}
	return string(r[:len(r)-1])
}

// modelIDFromPickerValue extracts a bare model ID from a picker value string
// of the form "claude-subscription:model-id", "anthropic-api:model-id",
// or "provider:key" (falls back to the value itself).
func modelIDFromPickerValue(value string) string {
	for _, prefix := range []string{
		"claude-subscription:",
		"anthropic-api:",
		"provider:claude-subscription.",
		"provider:anthropic-api.",
		"provider:openai-compatible.openai.",
		"provider:openai-compatible.gemini.",
		"provider:openai-compatible.google.",
		"provider:openai-compatible.openrouter.",
	} {
		if strings.HasPrefix(value, prefix) {
			// For "provider:..." keys the remainder is "kind.account.model" — extract
			// the last segment after the final dot, or return as-is.
			rest := strings.TrimPrefix(value, prefix)
			if prefix == "claude-subscription:" || prefix == "anthropic-api:" || strings.HasPrefix(prefix, "provider:openai-compatible.") {
				return rest
			}
			// provider key format: kind.account.model — take the last component.
			parts := strings.Split(rest, ".")
			return parts[len(parts)-1]
		}
	}
	return value
}

// formatModelCapBadge formats a single-line capability summary for a model.
// Example: "1M ctx · $3/$15 /1M · tools vision"
func formatModelCapBadge(info catalog.ModelInfo) string {
	var parts []string

	// Context window.
	ctx := info.ContextWindow
	switch {
	case ctx >= 1_000_000:
		parts = append(parts, fmt.Sprintf("%dM ctx", ctx/1_000_000))
	case ctx >= 1_000:
		parts = append(parts, fmt.Sprintf("%dk ctx", ctx/1_000))
	}

	// Pricing.
	if info.InputCostPer1M > 0 || info.OutputCostPer1M > 0 {
		in := formatCost(info.InputCostPer1M)
		out := formatCost(info.OutputCostPer1M)
		parts = append(parts, fmt.Sprintf("$%s/$%s /1M", in, out))
	}

	// Capability flags — compact text, no emoji.
	var flags []string
	if info.ToolUse {
		flags = append(flags, "tools")
	}
	if info.Vision {
		flags = append(flags, "vision")
	}
	if info.Thinking {
		flags = append(flags, "thinking")
	}
	if len(flags) > 0 {
		parts = append(parts, strings.Join(flags, " "))
	}

	if len(parts) == 0 {
		return ""
	}
	return "    " + strings.Join(parts, " · ")
}

// formatCost renders a per-1M USD price compactly (drops trailing zeros).
func formatCost(v float64) string {
	if v == float64(int(v)) {
		return fmt.Sprintf("%d", int(v))
	}
	return fmt.Sprintf("%.2g", v)
}
