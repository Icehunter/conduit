package tui

import (
	"context"
	"fmt"
	"strings"
	"sync"

	tea "charm.land/bubbletea/v2"

	"github.com/icehunter/conduit/internal/agent"
	"github.com/icehunter/conduit/internal/api"
	"github.com/icehunter/conduit/internal/auth"
	"github.com/icehunter/conduit/internal/permissions"
	"github.com/icehunter/conduit/internal/secure"
	"github.com/icehunter/conduit/internal/settings"
)

// councilMember tracks a single council participant across debate rounds.
type councilMember struct {
	label        string      // display name (model name or provider key)
	model        string      // model identifier passed to RunSubAgentTyped
	client       *api.Client // provider-specific client; nil = inherit parent's client
	active       bool
	lastResponse string // most recent response text; used to build critique context
}

// handleCouncilFlow starts the council debate. Called from the Update handler
// when a councilStartMsg arrives. Returns immediately — all debate work runs
// in a tea.Cmd goroutine that sends messages back via prog.Send.
func (m Model) handleCouncilFlow(msg councilStartMsg) (Model, tea.Cmd) {
	// Store the reply channel so councilDoneMsg handler can forward it.
	m.councilReply = msg.reply

	// Snapshot the data the goroutine needs from the model.
	providerKeys := append([]string(nil), m.councilProviders...)
	providers := cloneProviderMap(m.providers)
	loop := m.cfg.Loop
	prog := *m.cfg.Program
	seedPlan := msg.seedPlan
	newAPIClient := m.cfg.NewAPIClient
	newProviderClient := m.cfg.NewProviderAPIClient

	// Load max rounds from settings. Use 4 as the fallback.
	maxRounds := 4
	if s, err := settings.Load(""); err == nil {
		maxRounds = s.EffectiveCouncilMaxRounds()
	}

	cmd := func() tea.Msg {
		ctx := context.Background()

		// --- Build the roster ---
		members := buildCouncilRoster(providerKeys, providers, newAPIClient, newProviderClient)

		if len(members) == 0 {
			// No valid members: skip debate, forward seed plan.
			prog.Send(councilDoneMsg{plan: seedPlan, costUSD: 0.0})
			return nil
		}

		// --- Round 0: parallel propose ---
		seedPrompt := fmt.Sprintf(
			"You are participating in a multi-model planning council. "+
				"Here is the implementation task:\n\n%s\n\n"+
				"Provide your implementation plan. "+
				"Be specific about architecture, components, and sequencing.",
			seedPlan,
		)

		type proposeResult struct {
			idx  int
			text string
			err  error
		}

		resultCh := make(chan proposeResult, len(members))
		var wg sync.WaitGroup
		for i := range members {
			if !members[i].active {
				continue
			}
			wg.Add(1)
			go func(idx int, member councilMember) {
				defer wg.Done()
				text, err := runCouncilSubAgent(ctx, loop, member, seedPrompt,
					[]string{"Read", "Glob", "Grep", "WebFetch", "WebSearch"})
				resultCh <- proposeResult{idx: idx, text: text, err: err}
			}(i, members[i])
		}
		wg.Wait()
		close(resultCh)

		for r := range resultCh {
			if r.err != nil {
				members[r.idx].active = false
				prog.Send(councilMemberEjectedMsg{
					label:  members[r.idx].label,
					reason: r.err.Error(),
				})
			} else {
				members[r.idx].lastResponse = r.text
				prog.Send(councilMemberResponseMsg{
					label:  members[r.idx].label,
					text:   r.text,
					agreed: false,
				})
			}
		}

		// --- Rounds 1..maxRounds: sequential critique ---
		activeCount := countActive(members)
		if activeCount > 1 {
			for round := 1; round <= maxRounds; round++ {
				allAgreed := true
				atLeastOneActive := false

				debateContext := buildDebateContext(members)
				critiquePrompt := fmt.Sprintf(
					"Here are the other council members' plans:\n\n%s\n\n"+
						"Critique the plans, identify strengths and weaknesses, "+
						"and either propose improvements or signal agreement by "+
						"including <council-agree/> in your response if you are "+
						"satisfied with the current direction.",
					debateContext,
				)

				for i := range members {
					if !members[i].active {
						continue
					}
					atLeastOneActive = true

					text, err := runCouncilSubAgent(ctx, loop, members[i], critiquePrompt,
						[]string{"Read", "Glob", "Grep"})
					if err != nil {
						members[i].active = false
						prog.Send(councilMemberEjectedMsg{
							label:  members[i].label,
							reason: err.Error(),
						})
						continue
					}

					agreed := strings.Contains(text, "<council-agree/>")
					if !agreed {
						allAgreed = false
					}
					members[i].lastResponse = text
					prog.Send(councilMemberResponseMsg{
						label:  members[i].label,
						text:   text,
						agreed: agreed,
					})
				}

				activeCount = countActive(members)
				if !atLeastOneActive || allAgreed || activeCount <= 1 {
					break
				}
			}
		}

		// --- Synthesis ---
		prog.Send(councilSynthesisStartMsg{})

		var sb strings.Builder
		fmt.Fprintf(&sb, "The following council members participated in a planning debate:\n\n")
		for _, mem := range members {
			if mem.lastResponse != "" {
				fmt.Fprintf(&sb, "=== %s ===\n%s\n\n", mem.label, mem.lastResponse)
			}
		}
		fmt.Fprintf(&sb,
			"Synthesise the above plans into a single coherent implementation plan. "+
				"Incorporate the strongest elements from each perspective. "+
				"Be specific about architecture, components, and sequencing.",
		)

		synthesis, err := loop.RunSubAgentTyped(ctx, sb.String(), agent.SubAgentSpec{
			Mode:  permissions.ModeBypassPermissions,
			Tools: []string{"Read", "Glob", "Grep"},
		})
		if err != nil {
			// Fall back to seed plan if synthesis fails.
			synthesis = seedPlan
		}

		return councilDoneMsg{plan: synthesis, costUSD: 0.0}
	}

	return m, cmd
}

// buildCouncilRoster converts the TUI's provider key list into councilMember
// entries with per-member API clients so each member hits its own account.
func buildCouncilRoster(
	keys []string,
	providers map[string]settings.ActiveProviderSettings,
	newAPIClient func(auth.PersistedTokens) *api.Client,
	newProviderClient func(settings.ActiveProviderSettings) (*api.Client, error),
) []councilMember {
	members := make([]councilMember, 0, len(keys))
	for _, key := range keys {
		p, ok := providers[key]
		if !ok {
			continue
		}
		label := p.Model
		if label == "" {
			label = key
		}
		client := buildCouncilClient(p, newAPIClient, newProviderClient)
		members = append(members, councilMember{
			label:  label,
			model:  p.Model,
			client: client,
			active: true,
		})
	}
	return members
}

// buildCouncilClient creates an API client for a council member's provider.
// Returns nil for MCP/local providers (they cannot participate via sub-agent API calls).
func buildCouncilClient(
	p settings.ActiveProviderSettings,
	newAPIClient func(auth.PersistedTokens) *api.Client,
	newProviderClient func(settings.ActiveProviderSettings) (*api.Client, error),
) *api.Client {
	switch p.Kind {
	case settings.ProviderKindClaudeSubscription, settings.ProviderKindAnthropicAPI:
		if newAPIClient == nil || p.Account == "" {
			return nil
		}
		tokens, err := auth.LoadForEmail(secure.NewDefault(), p.Account)
		if err != nil {
			return nil
		}
		return newAPIClient(tokens)
	case settings.ProviderKindOpenAICompatible:
		if newProviderClient == nil {
			return nil
		}
		client, err := newProviderClient(p)
		if err != nil {
			return nil
		}
		return client
	}
	return nil // MCP/local: caller inherits parent client (will likely fail for council)
}

// runCouncilSubAgent runs a single council member's sub-agent call using that
// member's own API client so it hits the correct provider account.
func runCouncilSubAgent(
	ctx context.Context,
	loop *agent.Loop,
	member councilMember,
	prompt string,
	tools []string,
) (string, error) {
	return loop.RunSubAgentTyped(ctx, prompt, agent.SubAgentSpec{
		Model:  member.model,
		Mode:   permissions.ModeBypassPermissions,
		Tools:  tools,
		Client: member.client,
	})
}

// countActive counts active (non-ejected) council members.
func countActive(members []councilMember) int {
	n := 0
	for _, m := range members {
		if m.active {
			n++
		}
	}
	return n
}

// buildDebateContext builds a text context block from all active member
// last responses for use in critique rounds.
func buildDebateContext(members []councilMember) string {
	var sb strings.Builder
	for _, m := range members {
		if m.active && m.lastResponse != "" {
			fmt.Fprintf(&sb, "===\n%s:\n%s\n\n", m.label, m.lastResponse)
		}
	}
	return sb.String()
}
