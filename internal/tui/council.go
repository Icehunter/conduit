package tui

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"sync"
	"time"
	"unicode"

	tea "charm.land/bubbletea/v2"

	"github.com/icehunter/conduit/internal/agent"
	"github.com/icehunter/conduit/internal/api"
	"github.com/icehunter/conduit/internal/auth"
	"github.com/icehunter/conduit/internal/permissions"
	"github.com/icehunter/conduit/internal/secure"
	"github.com/icehunter/conduit/internal/settings"
	"github.com/icehunter/conduit/internal/tools/planmodetool"
)

// councilAgreeRE matches <council-agree/> tolerating whitespace and case variants.
var councilAgreeRE = regexp.MustCompile(`(?i)<\s*council-agree\s*/?\s*>`)

// councilMember tracks a single council participant across debate rounds.
type councilMember struct {
	label        string      // display name (model name or provider key)
	model        string      // model identifier passed to RunSubAgentTyped
	role         string      // optional role: "architect", "skeptic", "perf-reviewer"
	client       *api.Client // provider-specific client; nil = inherit parent's client
	active       bool
	lastResponse string    // most recent response text; used to build critique context
	usage        api.Usage // accumulated usage across all rounds
	durationMs   int64     // total wall-clock time for this member
}

// councilMemberStats is the per-member summary forwarded to councilDoneMsg.
type councilMemberStats struct {
	Label      string
	Usage      api.Usage
	CostUSD    float64
	DurationMs int64
}

// roundResult is sent by goroutines in runRoundParallel.
type roundResult struct {
	idx    int
	result agent.SubAgentResult
	agreed bool
	err    error
}

// runRoundParallel runs one round of the council debate in parallel across all
// active members. Each goroutine applies the per-member timeout derived from
// the parent context. Responses and ejections are prog.Send-ed immediately so
// verbose-mode chat updates stream in real time.
func runRoundParallel(
	parentCtx context.Context,
	loop *agent.Loop,
	members []councilMember,
	promptFn func(councilMember) string,
	tools []string,
	timeout time.Duration,
	prog *tea.Program,
) (allAgreed bool, atLeastOne bool) {
	ch := make(chan roundResult, len(members))
	var wg sync.WaitGroup
	for i := range members {
		if !members[i].active {
			continue
		}
		wg.Add(1)
		go func(idx int, m councilMember) {
			defer wg.Done()
			if parentCtx.Err() != nil {
				ch <- roundResult{idx: idx, err: parentCtx.Err()}
				return
			}
			// timeout == 0 means no per-member cap; use the parent context directly.
			var ctx context.Context
			var cancel context.CancelFunc
			if timeout > 0 {
				ctx, cancel = context.WithTimeout(parentCtx, timeout)
			} else {
				ctx, cancel = context.WithCancel(parentCtx)
			}
			defer cancel()
			r, err := loop.RunSubAgentTyped(ctx, promptFn(m), agent.SubAgentSpec{
				Model:            m.model,
				Mode:             permissions.ModeBypassPermissions,
				Tools:            tools,
				Client:           m.client,
				DisableModeTools: true,
			})
			if err == nil && substantiveLen(r.Text) < councilMinResponseChars {
				err = errEmptyCouncilResponse
			}
			agreed := err == nil && councilAgreeRE.MatchString(r.Text)
			if prog != nil {
				if err != nil {
					reason := err.Error()
					switch {
					case errors.Is(err, context.DeadlineExceeded):
						reason = "timeout"
					case errors.Is(err, errEmptyCouncilResponse):
						reason = "empty response"
					}
					prog.Send(councilMemberEjectedMsg{label: m.label, reason: reason})
				} else {
					prog.Send(councilMemberResponseMsg{label: m.label, text: r.Text, agreed: agreed})
				}
			}
			ch <- roundResult{idx: idx, result: r, agreed: agreed, err: err}
		}(i, members[i])
	}
	wg.Wait()
	close(ch)

	allAgreed = true
	atLeastOne = false
	for rr := range ch {
		if rr.err != nil {
			members[rr.idx].active = false
		} else {
			atLeastOne = true
			members[rr.idx].lastResponse = rr.result.Text
			members[rr.idx].usage.InputTokens += rr.result.Usage.InputTokens
			members[rr.idx].usage.OutputTokens += rr.result.Usage.OutputTokens
			members[rr.idx].usage.CacheCreationInputTokens += rr.result.Usage.CacheCreationInputTokens
			members[rr.idx].usage.CacheReadInputTokens += rr.result.Usage.CacheReadInputTokens
			members[rr.idx].durationMs += rr.result.DurationMs
			if !rr.agreed {
				allAgreed = false
			}
		}
	}
	if !atLeastOne {
		allAgreed = false
	}
	return allAgreed, atLeastOne
}

// councilMinResponseChars is the minimum non-whitespace character count for
// a council response to be considered substantive. Members returning less
// than this (e.g. silent failure, tool-call preamble only) are ejected from
// the round so they cannot poison synthesis or convergence scoring.
const councilMinResponseChars = 40

// errEmptyCouncilResponse is the sentinel ejection reason when a member
// returns an effectively empty answer.
var errEmptyCouncilResponse = errors.New("empty council response")

// substantiveLen returns the count of non-whitespace runes in s.
func substantiveLen(s string) int {
	n := 0
	for _, r := range s {
		if !unicode.IsSpace(r) {
			n++
		}
	}
	return n
}

// councilGroundingRule is appended to every council member prompt so that
// citations stay verifiable. Without this, members can copy each other's
// hallucinated file paths during critique rounds and false consensus emerges.
const councilGroundingRule = "Grounding rule: any file path, line number, function, or symbol you cite must either be confirmed by a tool call (Read/Grep/Glob) in this turn, or explicitly marked \"unverified — from memory\". Do not echo paths cited by other members without checking them yourself. "

// rolePreamble returns a role-specific instruction prefix for council prompts,
// followed by the universal grounding rule.
func rolePreamble(role string) string {
	switch role {
	case "architect":
		return "Focus on overall structure, component boundaries, and sequencing. " + councilGroundingRule
	case "skeptic":
		return "Challenge assumptions; identify what could go wrong; surface unstated requirements. " + councilGroundingRule
	case "perf-reviewer":
		return "Critique for runtime cost, hot paths, allocation patterns, and scalability. " + councilGroundingRule
	default:
		return councilGroundingRule
	}
}

// convergenceScore computes the average pairwise Jaccard similarity of all
// active member lastResponse strings. Returns 0 when fewer than 2 active members.
func convergenceScore(members []councilMember) float64 {
	var texts [][]string
	for _, m := range members {
		if m.active && m.lastResponse != "" {
			texts = append(texts, tokenize(m.lastResponse))
		}
	}
	if len(texts) < 2 {
		return 0
	}
	total := 0.0
	pairs := 0
	for i := 0; i < len(texts); i++ {
		for j := i + 1; j < len(texts); j++ {
			total += jaccard(texts[i], texts[j])
			pairs++
		}
	}
	if pairs == 0 {
		return 0
	}
	return total / float64(pairs)
}

func tokenize(s string) []string {
	// Strip markdown code fences before tokenizing.
	s = regexp.MustCompile("(?s)```[^`]*```").ReplaceAllString(s, " ")
	return strings.Fields(strings.ToLower(s))
}

func jaccard(a, b []string) float64 {
	setA := make(map[string]struct{}, len(a))
	for _, w := range a {
		setA[w] = struct{}{}
	}
	setB := make(map[string]struct{}, len(b))
	for _, w := range b {
		setB[w] = struct{}{}
	}
	inter := 0
	for w := range setA {
		if _, ok := setB[w]; ok {
			inter++
		}
	}
	union := len(setA) + len(setB) - inter
	if union == 0 {
		return 1
	}
	return float64(inter) / float64(union)
}

// buildSynthesisPrompt creates the synthesis prompt from the active member responses.
func buildSynthesisPrompt(preamble string, members []councilMember) string {
	var sb strings.Builder
	fmt.Fprintf(&sb, "%s\n\n", preamble)
	for _, mem := range members {
		if mem.lastResponse != "" {
			fmt.Fprintf(&sb, "=== %s ===\n%s\n\n", mem.label, mem.lastResponse)
		}
	}
	return sb.String()
}

// accumulateStats builds per-member stats for councilDoneMsg.
func accumulateStats(members []councilMember, synthesisResult agent.SubAgentResult, synthModel string) (
	totalUsage api.Usage, totalCost float64, perMember []councilMemberStats,
) {
	for _, m := range members {
		cost := api.CostUSDForModel(m.model, m.usage)
		perMember = append(perMember, councilMemberStats{
			Label: m.label, Usage: m.usage, CostUSD: cost, DurationMs: m.durationMs,
		})
		totalUsage.InputTokens += m.usage.InputTokens
		totalUsage.OutputTokens += m.usage.OutputTokens
		totalUsage.CacheCreationInputTokens += m.usage.CacheCreationInputTokens
		totalUsage.CacheReadInputTokens += m.usage.CacheReadInputTokens
		totalCost += cost
	}
	synthCost := api.CostUSDForModel(synthModel, synthesisResult.Usage)
	totalUsage.InputTokens += synthesisResult.Usage.InputTokens
	totalUsage.OutputTokens += synthesisResult.Usage.OutputTokens
	totalUsage.CacheCreationInputTokens += synthesisResult.Usage.CacheCreationInputTokens
	totalUsage.CacheReadInputTokens += synthesisResult.Usage.CacheReadInputTokens
	totalCost += synthCost
	return totalUsage, totalCost, perMember
}

// handleCouncilFlow starts the council debate. Called from the Update handler
// when a councilStartMsg arrives. Returns immediately — all debate work runs
// in a tea.Cmd goroutine that sends messages back via prog.Send.
func (m Model) handleCouncilFlow(msg councilStartMsg) (Model, tea.Cmd) {
	m.councilReply = msg.reply

	providerKeys := append([]string(nil), m.councilProviders...)
	providers := cloneProviderMap(m.providers)
	accountProviders := configuredAccountProviders()
	loop := m.cfg.Loop
	prog := *m.cfg.Program
	seedPlan := msg.seedPlan
	newAPIClient := m.cfg.NewAPIClient
	newProviderClient := m.cfg.NewProviderAPIClient

	maxRounds := 4
	memberTimeout := 30 * time.Second
	var convergenceThreshold float64
	var synthesizerKey string
	synthesizerMaxTokens := 8192
	councilRoles := map[string]string{}
	if s, err := settings.Load(""); err == nil {
		maxRounds = s.EffectiveCouncilMaxRounds()
		memberTimeout = s.EffectiveCouncilMemberTimeout()
		convergenceThreshold = s.CouncilConvergenceThreshold
		synthesizerKey = s.CouncilSynthesizer
		synthesizerMaxTokens = s.EffectiveCouncilSynthesizerMaxTokens()
		if s.CouncilRoles != nil {
			councilRoles = s.CouncilRoles
		}
	}

	ctx, cancel := context.WithCancel(context.Background())
	m.councilCancel = cancel

	cmd := func() tea.Msg {
		defer cancel()

		members := buildCouncilRoster(providerKeys, providers, accountProviders,
			councilRoles, newAPIClient, newProviderClient)
		if len(members) == 0 {
			prog.Send(councilDoneMsg{plan: seedPlan})
			return nil
		}

		// Round 0: parallel propose.
		prog.Send(councilRoundStartMsg{round: 0, total: maxRounds, active: countActive(members)})
		proposePromptFn := func(m councilMember) string {
			return fmt.Sprintf(
				"%sYou are participating in a multi-model planning council. "+
					"Here is the implementation task:\n\n%s\n\n"+
					"Provide your implementation plan. "+
					"Be specific about architecture, components, and sequencing.",
				rolePreamble(m.role), seedPlan,
			)
		}
		runRoundParallel(ctx, loop, members, proposePromptFn,
			[]string{"Read", "Glob", "Grep", "WebFetch", "WebSearch"}, memberTimeout, prog)

		if ctx.Err() != nil {
			prog.Send(councilDoneMsg{err: fmt.Errorf("council cancelled")})
			return nil
		}

		// Rounds 1..maxRounds: parallel critique.
		flowRoundsRun := 0
		flowConverged := false
		if countActive(members) > 1 {
			for round := 1; round <= maxRounds; round++ {
				prog.Send(councilRoundStartMsg{round: round, total: maxRounds, active: countActive(members)})
				debateCtx := buildDebateContext(members)
				critiquePromptFn := func(m councilMember) string {
					return fmt.Sprintf(
						"%sHere are the other council members' plans:\n\n%s\n\n"+
							"Critique the plans, identify strengths and weaknesses, "+
							"and either propose improvements or signal agreement by "+
							"including <council-agree/> in your response if you are "+
							"satisfied with the current direction.",
						rolePreamble(m.role), debateCtx,
					)
				}
				allAgreed, atLeastOne := runRoundParallel(ctx, loop, members, critiquePromptFn,
					[]string{"Read", "Glob", "Grep"}, memberTimeout, prog)
				flowRoundsRun++
				if ctx.Err() != nil {
					break
				}

				// Similarity-based early convergence.
				if convergenceThreshold > 0 && !allAgreed && convergenceScore(members) >= convergenceThreshold {
					allAgreed = true
					flowConverged = true
					prog.Send(councilMemberResponseMsg{
						label:  "council",
						text:   fmt.Sprintf("(convergence detected — similarity ≥ %.0f%%)", convergenceThreshold*100),
						agreed: true,
					})
				}

				if !atLeastOne || allAgreed || countActive(members) <= 1 {
					if allAgreed {
						flowConverged = true
					}
					break
				}
			}
		}

		if ctx.Err() != nil {
			prog.Send(councilDoneMsg{err: fmt.Errorf("council cancelled")})
			return nil
		}

		// Bail out early if no member produced a response — don't run synthesis
		// with an empty prompt (the synthesizer will just say it sees nothing).
		if !anyMemberResponded(members) {
			prog.Send(councilDoneMsg{err: fmt.Errorf("all council members failed to respond — check timeout (CouncilMemberTimeoutSec in conduit.json) or provider availability")})
			return nil
		}

		// Voting pass (weighted synthesis) when ≥3 active members.
		if countActive(members) >= 3 {
			members = runVotingPass(ctx, loop, members, memberTimeout, prog)
		}

		// Synthesis.
		prog.Send(councilSynthesisStartMsg{})
		synthPreamble := "The following council members participated in a planning debate:\n"
		synthPrompt := buildSynthesisPrompt(synthPreamble, members) +
			"Synthesise the above plans into a single coherent implementation plan. " +
			"Incorporate the strongest elements from each perspective. " +
			"Be specific about architecture, components, and sequencing."

		synthClient, synthModel := buildSynthesizerClient(synthesizerKey, providers, accountProviders,
			newAPIClient, newProviderClient, loop)
		flowStart := time.Now()
		synthResult, synthErr := loop.RunSubAgentTyped(ctx, synthPrompt, agent.SubAgentSpec{
			Mode:       permissions.ModeBypassPermissions,
			Tools:      []string{"Read", "Glob", "Grep"},
			Background: true,
			Client:     synthClient,
			Model:      synthModel,
			MaxTokens:  synthesizerMaxTokens,
		})
		synthesis := strings.TrimSpace(synthResult.Text)
		// Surface synthesis failure explicitly. Falling back to seedPlan
		// (the user's question) silently turned the question into the "plan"
		// the user was asked to approve — a confusing dead end.
		if synthErr != nil {
			return councilDoneMsg{err: fmt.Errorf("council synthesis failed: %w", synthErr)}
		}
		if synthesis == "" {
			return councilDoneMsg{err: fmt.Errorf("council synthesis returned an empty plan (synthesizer=%s); try raising councilSynthesizerMaxTokens or narrowing the question", synthModel)}
		}

		totalUsage, totalCost, perMember := accumulateStats(members, synthResult, synthModel)
		_ = persistCouncilTranscript(councilTranscriptArgs{
			question:  seedPlan,
			members:   members,
			synthesis: synthesis,
			usage:     totalUsage,
			costUSD:   totalCost,
		})
		appendCouncilLogEntry(councilLogEntry{
			Kind:         "flow",
			Members:      len(members),
			RoundsRun:    flowRoundsRun,
			Converged:    flowConverged,
			CostUSD:      totalCost,
			InputTokens:  totalUsage.InputTokens,
			OutputTokens: totalUsage.OutputTokens,
			DurationMs:   time.Since(flowStart).Milliseconds(),
		})
		return councilDoneMsg{plan: synthesis, usage: totalUsage, costUSD: totalCost, perMember: perMember}
	}

	return m, cmd
}

// handleCouncilChat runs the council debate for a direct user chat message.
func (m Model) handleCouncilChat(msg councilChatMsg) (Model, tea.Cmd) {
	providerKeys := append([]string(nil), m.councilProviders...)
	providers := cloneProviderMap(m.providers)
	accountProviders := configuredAccountProviders()
	loop := m.cfg.Loop
	prog := *m.cfg.Program
	question := msg.question
	newAPIClient := m.cfg.NewAPIClient
	newProviderClient := m.cfg.NewProviderAPIClient

	// Create the reply channel that the plan-approval picker will write to
	// once the user chooses an option. The goroutine below reads the decision
	// and applies the permission mode change back to the TUI via prog.Send.
	replyCh := make(chan planmodetool.PlanApprovalDecision, 1)
	var replyWrite chan<- planmodetool.PlanApprovalDecision = replyCh
	go func() {
		d := <-replyCh
		if d.Approved {
			mode := d.Mode
			if mode == "" {
				mode = permissions.ModeBypassPermissions
			}
			prog.Send(setPermissionModeMsg{mode: mode})
		}
	}()

	maxRounds := 4
	memberTimeout := 30 * time.Second
	var convergenceThreshold float64
	var synthesizerKey string
	synthesizerMaxTokens := 8192
	councilRoles := map[string]string{}
	if s, err := settings.Load(""); err == nil {
		maxRounds = s.EffectiveCouncilMaxRounds()
		memberTimeout = s.EffectiveCouncilMemberTimeout()
		convergenceThreshold = s.CouncilConvergenceThreshold
		synthesizerKey = s.CouncilSynthesizer
		synthesizerMaxTokens = s.EffectiveCouncilSynthesizerMaxTokens()
		if s.CouncilRoles != nil {
			councilRoles = s.CouncilRoles
		}
	}

	ctx, cancel := context.WithCancel(context.Background())
	m.councilCancel = cancel

	cmd := func() tea.Msg {
		defer cancel()

		members := buildCouncilRoster(providerKeys, providers, accountProviders,
			councilRoles, newAPIClient, newProviderClient)
		if len(members) == 0 {
			replyWrite <- planmodetool.PlanApprovalDecision{Approved: false}
			return councilChatDoneMsg{err: fmt.Errorf("no council members configured — add providers in /model → Council tab")}
		}

		// Round 0: parallel propose.
		prog.Send(councilRoundStartMsg{round: 0, total: maxRounds, active: countActive(members)})
		proposePromptFn := func(m councilMember) string {
			return fmt.Sprintf(
				"%sAnswer the following question concisely and directly:\n\n%s",
				rolePreamble(m.role), question,
			)
		}
		runRoundParallel(ctx, loop, members, proposePromptFn,
			[]string{"Read", "Glob", "Grep", "WebFetch", "WebSearch"}, memberTimeout, prog)

		if ctx.Err() != nil {
			replyWrite <- planmodetool.PlanApprovalDecision{Approved: false}
			return councilChatDoneMsg{}
		}

		// Rounds 1..maxRounds: parallel critique.
		chatRoundsRun := 0
		chatConverged := false
		if countActive(members) > 1 {
			for round := 1; round <= maxRounds; round++ {
				prog.Send(councilRoundStartMsg{round: round, total: maxRounds, active: countActive(members)})
				debateCtx := buildDebateContext(members)
				critiquePromptFn := func(m councilMember) string {
					return fmt.Sprintf(
						"%sThe question was: %q\n\nHere are the other council members' answers:\n\n%s\n\n"+
							"Critique the answers, identify the strongest points, and either propose "+
							"a better synthesis or signal agreement by including <council-agree/> if "+
							"you are satisfied with the current direction.",
						rolePreamble(m.role), question, debateCtx,
					)
				}
				allAgreed, atLeastOne := runRoundParallel(ctx, loop, members, critiquePromptFn,
					[]string{"Read", "Glob", "Grep"}, memberTimeout, prog)
				chatRoundsRun++
				if ctx.Err() != nil {
					break
				}

				// Similarity-based early convergence.
				if convergenceThreshold > 0 && !allAgreed && convergenceScore(members) >= convergenceThreshold {
					allAgreed = true
					chatConverged = true
					prog.Send(councilMemberResponseMsg{
						label:  "council",
						text:   fmt.Sprintf("(convergence detected — similarity ≥ %.0f%%)", convergenceThreshold*100),
						agreed: true,
					})
				}

				if !atLeastOne || allAgreed || countActive(members) <= 1 {
					if allAgreed {
						chatConverged = true
					}
					break
				}
			}
		}

		if ctx.Err() != nil {
			return councilChatDoneMsg{err: fmt.Errorf("council cancelled")}
		}

		// Bail out early if no member produced a response — synthesizing an
		// empty prompt yields the confusing "I don't see any responses" reply.
		if !anyMemberResponded(members) {
			return councilChatDoneMsg{err: fmt.Errorf("all council members failed to respond — check timeout (CouncilMemberTimeoutSec in conduit.json) or provider availability")}
		}

		// Voting pass when ≥3 active members.
		if countActive(members) >= 3 {
			members = runVotingPass(ctx, loop, members, memberTimeout, prog)
		}

		// Synthesis.
		prog.Send(councilSynthesisStartMsg{})
		synthPreamble := fmt.Sprintf("Multiple AI models were asked: %q\n\nHere are their responses:\n", question)
		synthPrompt := buildSynthesisPrompt(synthPreamble, members) +
			"Synthesise the above responses into a single comprehensive answer. " +
			"Incorporate the strongest points from each perspective. " +
			"Be direct and concise — this is a chat answer, not a formal plan."

		synthClient, synthModel := buildSynthesizerClient(synthesizerKey, providers, accountProviders,
			newAPIClient, newProviderClient, loop)
		chatStart := time.Now()
		synthResult, synthErr := loop.RunSubAgentTyped(ctx, synthPrompt, agent.SubAgentSpec{
			Mode:       permissions.ModeBypassPermissions,
			Tools:      []string{"Read", "Glob", "Grep"},
			Background: true,
			Client:     synthClient,
			Model:      synthModel,
			MaxTokens:  synthesizerMaxTokens,
		})
		synthesis := synthResult.Text
		if synthErr != nil || synthesis == "" {
			var fallback strings.Builder
			for _, mem := range members {
				if mem.lastResponse != "" {
					fmt.Fprintf(&fallback, "**%s**: %s\n\n", mem.label, mem.lastResponse)
				}
			}
			synthesis = strings.TrimSpace(fallback.String())
		}
		if synthesis == "" {
			replyWrite <- planmodetool.PlanApprovalDecision{Approved: false}
			return councilChatDoneMsg{err: fmt.Errorf("council produced no responses — all members may have timed out or been ejected")}
		}

		totalUsage, totalCost, perMember := accumulateStats(members, synthResult, synthModel)
		_ = persistCouncilTranscript(councilTranscriptArgs{
			question:  question,
			members:   members,
			synthesis: synthesis,
			usage:     totalUsage,
			costUSD:   totalCost,
		})
		appendCouncilLogEntry(councilLogEntry{
			Kind:         "chat",
			Members:      len(members),
			RoundsRun:    chatRoundsRun,
			Converged:    chatConverged,
			CostUSD:      totalCost,
			InputTokens:  totalUsage.InputTokens,
			OutputTokens: totalUsage.OutputTokens,
			DurationMs:   time.Since(chatStart).Milliseconds(),
		})
		return councilChatDoneMsg{synthesis: synthesis, usage: totalUsage, costUSD: totalCost, perMember: perMember, reply: replyWrite}
	}

	return m, cmd
}

// runVotingPass runs one extra scoring round where each member ranks the other
// members' proposals. Results are used to reorder the synthesis prompt so that
// highest-voted plans appear first. Skipped when ctx is already cancelled.
func runVotingPass(
	ctx context.Context,
	loop *agent.Loop,
	members []councilMember,
	timeout time.Duration,
	prog *tea.Program,
) []councilMember {
	if ctx.Err() != nil {
		return members
	}

	// Assign stable IDs to each active member's plan.
	type planEntry struct{ id, label, text string }
	var plans []planEntry
	for i, m := range members {
		if m.active && m.lastResponse != "" {
			plans = append(plans, planEntry{
				id:    fmt.Sprintf("plan_%d", i),
				label: m.label,
				text:  m.lastResponse,
			})
		}
	}
	if len(plans) < 2 {
		return members
	}

	var planBlock strings.Builder
	for _, p := range plans {
		fmt.Fprintf(&planBlock, "=== %s (id: %s) ===\n%s\n\n", p.label, p.id, p.text)
	}

	// votes[planID] accumulates weighted scores.
	votes := make(map[string]float64, len(plans))
	var mu sync.Mutex
	var wg sync.WaitGroup

	for i := range members {
		if !members[i].active {
			continue
		}
		wg.Add(1)
		go func(m councilMember) {
			defer wg.Done()
			tctx, cancel := context.WithTimeout(ctx, timeout)
			defer cancel()
			votePrompt := fmt.Sprintf(
				"You are reviewing these plans from your fellow council members (excluding your own):\n\n%s\n\n"+
					"Respond with a single JSON object inside <council-vote>...</council-vote> tags, "+
					"mapping each plan id to a weight between 0.0 and 1.0 that reflects its quality. "+
					"Weights must sum to 1.0. Example: <council-vote>{\"plan_0\":0.6,\"plan_1\":0.4}</council-vote>",
				planBlock.String(),
			)
			r, err := loop.RunSubAgentTyped(tctx, votePrompt, agent.SubAgentSpec{
				Model:  m.model,
				Mode:   permissions.ModeBypassPermissions,
				Client: m.client,
			})
			if err != nil {
				return
			}
			// Parse <council-vote>{...}</council-vote>.
			voteRE := regexp.MustCompile(`(?s)<council-vote>\s*(\{[^}]+\})\s*</council-vote>`)
			if match := voteRE.FindStringSubmatch(r.Text); len(match) > 1 {
				parsed := parseVoteJSON(match[1])
				mu.Lock()
				for id, w := range parsed {
					votes[id] += w
				}
				mu.Unlock()
			}
		}(members[i])
	}
	wg.Wait()

	if ctx.Err() != nil {
		return members
	}

	// Normalise votes — divide by number of voters.
	voters := float64(countActive(members))
	if voters > 0 {
		for id := range votes {
			votes[id] /= voters
		}
	}

	// Reorder plans by descending vote weight, updating member order in-place.
	// Members with zero / unknown votes go last.
	planVote := func(idx int) float64 {
		id := fmt.Sprintf("plan_%d", idx)
		if v, ok := votes[id]; ok {
			return v
		}
		return 0
	}
	// Sort member indices by vote descending using a simple insertion sort
	// (N is always small — ≤10 members).
	indices := make([]int, len(members))
	for i := range indices {
		indices[i] = i
	}
	for i := 1; i < len(indices); i++ {
		for j := i; j > 0 && planVote(indices[j]) > planVote(indices[j-1]); j-- {
			indices[j], indices[j-1] = indices[j-1], indices[j]
		}
	}
	sorted := make([]councilMember, len(members))
	for i, idx := range indices {
		sorted[i] = members[idx]
	}
	_ = prog // suppress unused warning
	return sorted
}

// parseVoteJSON parses a JSON object of {"plan_id": weight} pairs.
// Returns empty map on any parse error.
func parseVoteJSON(s string) map[string]float64 {
	out := map[string]float64{}
	// Simple manual parse: find "key": value pairs.
	re := regexp.MustCompile(`"([^"]+)":\s*([0-9.]+)`)
	for _, match := range re.FindAllStringSubmatch(s, -1) {
		if len(match) == 3 {
			var v float64
			if _, err := fmt.Sscanf(match[2], "%f", &v); err == nil {
				out[match[1]] = v
			}
		}
	}
	return out
}

// buildSynthesizerClient returns the client and model to use for the synthesis
// sub-agent. If synthesizerKey is set and resolves to a known provider, that
// client/model is used. Otherwise both return nil/empty (inherits parent loop).
func buildSynthesizerClient(
	synthesizerKey string,
	providers map[string]settings.ActiveProviderSettings,
	accountProviders []settings.ActiveProviderSettings,
	newAPIClient func(auth.PersistedTokens) *api.Client,
	newProviderClient func(settings.ActiveProviderSettings) (*api.Client, error),
	_ *agent.Loop,
) (client *api.Client, model string) {
	if synthesizerKey == "" {
		return nil, ""
	}
	bareKey := strings.TrimPrefix(synthesizerKey, "provider:")
	p, ok := providers[bareKey]
	if !ok {
		p, ok = resolveAccountProvider(synthesizerKey, accountProviders)
	}
	if !ok {
		return nil, ""
	}
	return buildCouncilClient(p, newAPIClient, newProviderClient), p.Model
}

// buildCouncilRoster converts the TUI's provider key list into councilMember
// entries with per-member API clients so each member hits its own account.
func buildCouncilRoster(
	keys []string,
	providers map[string]settings.ActiveProviderSettings,
	accountProviders []settings.ActiveProviderSettings,
	councilRoles map[string]string,
	newAPIClient func(auth.PersistedTokens) *api.Client,
	newProviderClient func(settings.ActiveProviderSettings) (*api.Client, error),
) []councilMember {
	members := make([]councilMember, 0, len(keys))
	for _, key := range keys {
		bareKey := strings.TrimPrefix(key, "provider:")
		p, ok := providers[bareKey]
		if !ok {
			p, ok = resolveAccountProvider(key, accountProviders)
		}
		if !ok {
			continue
		}
		label := p.Model
		if label == "" {
			label = key
		}
		role := councilRoles[key]
		if role == "" {
			role = councilRoles[bareKey]
		}
		client := buildCouncilClient(p, newAPIClient, newProviderClient)
		members = append(members, councilMember{
			label:  label,
			model:  p.Model,
			role:   role,
			client: client,
			active: true,
		})
	}
	return members
}

// resolveAccountProvider finds an account-based provider whose canonical key
// matches the given council key. The expected key format is
// "provider:<kind>.<account>.<model>" — we derive the per-provider prefix and
// extract the trailing model rather than enumerating a hardcoded model list.
func resolveAccountProvider(key string, accountProviders []settings.ActiveProviderSettings) (settings.ActiveProviderSettings, bool) {
	for _, ap := range accountProviders {
		if ap.Account == "" {
			continue
		}
		probe := settings.ActiveProviderSettings{Kind: ap.Kind, Account: ap.Account, Model: "x"}
		canonical := "provider:" + settings.ProviderKey(probe)
		prefix := strings.TrimSuffix(canonical, "x")
		if !strings.HasPrefix(key, prefix) {
			continue
		}
		model := strings.TrimPrefix(key, prefix)
		if model == "" {
			continue
		}
		resolved := ap
		resolved.Model = model
		return resolved, true
	}
	return settings.ActiveProviderSettings{}, false
}

// buildCouncilClient creates an API client for a council member's provider.
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
	return nil
}

// anyMemberResponded returns true if at least one active member has a non-empty lastResponse.
func anyMemberResponded(members []councilMember) bool {
	for _, m := range members {
		if m.lastResponse != "" {
			return true
		}
	}
	return false
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

// trivialRE matches common short acknowledgements and greetings that do not
// benefit from a multi-model council debate. The pattern is a strict
// allowlist — it does not perform word-count heuristics.
var trivialRE = regexp.MustCompile(
	`(?i)^\s*(hi|hello|hey|thanks|thank you|ok|okay|yes|no|sure|bye|goodbye|great|perfect|got it|makes sense|understood|sounds good)[\s?!.]*$`,
)

// isCouncilTrivial reports whether a user message is too short or simple to
// warrant a full council debate. When true, the message is routed to the
// normal single-model agent loop instead.
func isCouncilTrivial(text string) bool {
	return trivialRE.MatchString(strings.TrimSpace(text))
}
