package tui

import "github.com/icehunter/conduit/internal/tokens"

// tallyTokens estimates token usage from conversation history using
// cl100k_base — the tokenizer Claude approximates for billing. Falls
// back to chars/4 if the encoder fails to initialize (offline first run).
func (m *Model) tallyTokens() {
	var inputTok, outputTok int
	for _, msg := range m.history {
		t := 0
		for _, b := range msg.Content {
			t += tokens.Estimate(b.Text)
		}
		if msg.Role == "assistant" {
			outputTok += t
		} else {
			inputTok += t
		}
	}
	m.totalInputTokens = inputTok + outputTok // billing input = full context
	m.totalOutputTokens = outputTok
	// Opus 4.7: $15/M input + $75/M output.
	m.costUSD = float64(inputTok)*15.0/1_000_000 + float64(outputTok)*75.0/1_000_000
	m.syncLive()
}

// syncLive pushes frequently-read fields into the thread-safe LiveState bag
// so command callbacks running outside the Bubble Tea event loop always see
// current values, not the stale initial snapshot from New().
func (m *Model) syncLive() {
	if m.cfg.Live == nil {
		return
	}
	m.cfg.Live.SetModelName(m.activeModelDisplayName())
	if provider, ok := m.activeMCPProvider(); ok {
		m.cfg.Live.SetLocalMode(true, provider.Server)
	} else {
		m.cfg.Live.SetLocalMode(false, "")
	}
	m.cfg.Live.SetPermissionMode(m.permissionMode)
	m.cfg.Live.SetTokens(m.totalInputTokens, m.totalOutputTokens, m.costUSD)
	m.cfg.Live.SetRateLimitWarning(m.rateLimitWarning)
	if m.cfg.Session != nil {
		m.cfg.Live.SetSessionID(m.cfg.Session.ID)
		m.cfg.Live.SetSessionFile(m.cfg.Session.FilePath)
	}
}
