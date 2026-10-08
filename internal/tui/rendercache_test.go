package tui

import (
	"testing"

	tea "charm.land/bubbletea/v2"
)

func TestRenderCacheKeyedByInputs(t *testing.T) {
	base := renderKey{msg: Message{Role: RoleUser, Content: "hi"}, width: 80, gen: 1}
	changedMsg := base
	changedMsg.msg.Content = "hi there"
	changedWidth := base
	changedWidth.width = 100
	changedExpanded := base
	changedExpanded.msg.ToolExpanded = true
	changedGen := base
	changedGen.gen = 2
	changedVerbose := base
	changedVerbose.verbose = true

	tests := []struct {
		name    string
		second  renderKey
		wantHit bool
	}{
		{"same key hits", base, true},
		{"content change misses", changedMsg, false},
		{"width change misses", changedWidth, false},
		{"expanded change misses", changedExpanded, false},
		{"theme generation change misses", changedGen, false},
		{"verbose change misses", changedVerbose, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := newRenderCache()
			calls := 0
			render := func() string { calls++; return "out" }
			c.get(0, base, render)
			c.get(0, tt.second, render)
			if hit := calls == 1; hit != tt.wantHit {
				t.Errorf("hit = %v, want %v (render calls %d)", hit, tt.wantHit, calls)
			}
		})
	}
}

func TestRefreshViewportReusesRenderedRows(t *testing.T) {
	m := idleModel()
	next, _ := m.handleWindowSize(tea.WindowSizeMsg{Width: 100, Height: 40})
	m = next
	m.messages = []Message{
		{Role: RoleUser, Content: "question"},
		{Role: RoleAssistant, Content: "answer **bold**"},
		{Role: RoleTool, ToolName: "Bash", ToolInput: `{"command":"ls"}`, Content: "a\nb"},
	}
	m.refreshViewport()
	hits0, misses0 := m.renderCache.hits, m.renderCache.misses
	m.refreshViewport()
	if m.renderCache.misses != misses0 {
		t.Errorf("second refresh re-rendered rows: misses %d -> %d", misses0, m.renderCache.misses)
	}
	if m.renderCache.hits <= hits0 {
		t.Errorf("second refresh made no cache hits")
	}
	m.messages[1].Content += " more"
	m.refreshViewport()
	if m.renderCache.misses != misses0+1 {
		t.Errorf("changing one message should miss exactly once, misses %d -> %d", misses0, m.renderCache.misses)
	}
}
