package commands

import (
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"time"

	"github.com/icehunter/conduit/internal/session"
)

// SessionState holds mutable session state that slash commands can read/modify.
type SessionState struct {
	GetCost     func() string
	GetVimMode  func() bool
	SetVimMode  func(bool)
	GetEffort   func() string
	SetEffort   func(string)
	GetFast     func() bool
	SetFast     func(bool)
	Logout      func() error
	GetHistory  func() []string // message contents for /files, /context
	GetCwd      func() string
	// Rewind removes the last n conversation turns from in-memory history.
	// Returns the number of turns actually removed.
	Rewind      func(n int) int
	// SearchTranscript searches all session transcripts for cwd and returns results.
	SearchTranscript func(term string) string
	// GetTokens returns current (inputTokens, outputTokens, costUSD) from LiveState.
	GetTokens func() (int, int, float64)
	// GetStatus returns a one-line status string (model, mode, session ID, cost, context %).
	GetStatus   func() string
	// GetTasks returns a formatted list of active TaskTool tasks.
	GetTasks    func() string
	// GetAgents returns a formatted list of active sub-agents.
	GetAgents   func() string
	// GetLastThinking returns the last assistant thinking blocks as text.
	GetLastThinking func() string
	// GetColor returns the current ANSI color toggle state.
	GetColor    func() bool
	// SetColor sets the ANSI color toggle.
	SetColor    func(bool)
	// CopyLastResponse copies the last assistant response to clipboard.
	// Returns "" on success, error message otherwise.
	CopyLast    func() string
}

// RegisterSessionCommands registers all session-dependent slash commands.
func RegisterSessionCommands(r *Registry, state *SessionState) {
	if state == nil {
		state = &SessionState{}
	}

	// /cost
	r.Register(Command{
		Name:        "cost",
		Description: "Show total cost and duration of the current session",
		Handler: func(string) Result {
			if state.GetCost != nil {
				return Result{Type: "text", Text: state.GetCost()}
			}
			return Result{Type: "text", Text: "Cost tracking not available."}
		},
	})

	// /logout
	r.Register(Command{
		Name:        "logout",
		Description: "Sign out from your Anthropic account",
		Handler: func(string) Result {
			if state.Logout != nil {
				if err := state.Logout(); err != nil {
					return Result{Type: "error", Text: fmt.Sprintf("Logout failed: %v", err)}
				}
				return Result{Type: "text", Text: "Logged out. Use /login to sign in again."}
			}
			return Result{Type: "text", Text: "Logout not available in this session."}
		},
	})

	// /vim
	r.Register(Command{
		Name:        "vim",
		Description: "Toggle between Vim and Normal editing modes",
		Handler: func(string) Result {
			if state.GetVimMode == nil || state.SetVimMode == nil {
				return Result{Type: "text", Text: "Vim mode not available."}
			}
			next := !state.GetVimMode()
			state.SetVimMode(next)
			if next {
				return Result{Type: "text", Text: "Vim mode enabled. Press Esc for normal mode, i to insert."}
			}
			return Result{Type: "text", Text: "Vim mode disabled."}
		},
	})

	// /effort
	r.Register(Command{
		Name:        "effort",
		Description: "Set effort level: low, normal, high, or max",
		Handler: func(args string) Result {
			args = strings.TrimSpace(strings.ToLower(args))
			valid := map[string]string{
				"low":    "Low effort — faster, less thorough",
				"normal": "Normal effort — balanced speed and quality",
				"high":   "High effort — comprehensive with extensive testing",
				"max":    "Max effort — maximum capability with deepest reasoning",
			}
			if args == "" {
				current := "normal"
				if state.GetEffort != nil {
					current = state.GetEffort()
				}
				var sb strings.Builder
				sb.WriteString(fmt.Sprintf("Current effort: %s\n\nAvailable levels:\n", current))
				for _, level := range []string{"low", "normal", "high", "max"} {
					marker := "  "
					if level == current {
						marker = "▶ "
					}
					sb.WriteString(fmt.Sprintf("%s%s — %s\n", marker, level, valid[level]))
				}
				return Result{Type: "text", Text: strings.TrimRight(sb.String(), "\n")}
			}
			desc, ok := valid[args]
			if !ok {
				return Result{Type: "error", Text: fmt.Sprintf("Unknown effort level %q. Use: low, normal, high, max", args)}
			}
			if state.SetEffort != nil {
				state.SetEffort(args)
			}
			return Result{Type: "text", Text: fmt.Sprintf("Set effort level to %s: %s", args, desc)}
		},
	})

	// /fast
	r.Register(Command{
		Name:        "fast",
		Description: "Toggle fast mode (uses Haiku for faster, cheaper responses)",
		Handler: func(string) Result {
			if state.GetFast == nil || state.SetFast == nil {
				return Result{Type: "text", Text: "Fast mode not available."}
			}
			next := !state.GetFast()
			state.SetFast(next)
			if next {
				return Result{Type: "text", Text: "Fast mode enabled — using Haiku for faster responses."}
			}
			return Result{Type: "text", Text: "Fast mode disabled — using default model."}
		},
	})

	// /diff
	r.Register(Command{
		Name:        "diff",
		Description: "View uncommitted changes (git diff)",
		Handler: func(args string) Result {
			cwd := "."
			if state.GetCwd != nil {
				cwd = state.GetCwd()
			}
			cmd := exec.Command("git", "-C", cwd, "diff", "--stat")
			out, err := cmd.Output()
			if err != nil {
				return Result{Type: "error", Text: fmt.Sprintf("git diff failed: %v", err)}
			}
			if len(out) == 0 {
				return Result{Type: "text", Text: "No uncommitted changes."}
			}
			return Result{Type: "text", Text: strings.TrimSpace(string(out))}
		},
	})

	// /doctor
	r.Register(Command{
		Name:        "doctor",
		Description: "Diagnose and verify your Claude Code installation",
		Handler: func(string) Result {
			var sb strings.Builder
			sb.WriteString("Claude Code Diagnostics\n\n")

			// Binary
			exe, _ := os.Executable()
			sb.WriteString(fmt.Sprintf("Binary: %s\n", exe))
			sb.WriteString(fmt.Sprintf("Platform: %s/%s\n", runtime.GOOS, runtime.GOARCH))

			// git
			if _, err := exec.LookPath("git"); err == nil {
				out, _ := exec.Command("git", "--version").Output()
				sb.WriteString(fmt.Sprintf("git: %s", strings.TrimSpace(string(out))))
			} else {
				sb.WriteString("git: not found")
			}
			sb.WriteByte('\n')

			// rg
			if _, err := exec.LookPath("rg"); err == nil {
				sb.WriteString("ripgrep: found ✓")
			} else {
				sb.WriteString("ripgrep: not found (GrepTool will use grep fallback)")
			}
			sb.WriteByte('\n')

			// Credentials hint — actual check is via keyring, just note the state
			sb.WriteString("Credentials: use /login if not authenticated")
			sb.WriteByte('\n')

			return Result{Type: "text", Text: strings.TrimRight(sb.String(), "\n")}
		},
	})

	// /files
	r.Register(Command{
		Name:        "files",
		Description: "List all files currently referenced in the conversation",
		Handler: func(string) Result {
			if state.GetHistory == nil {
				return Result{Type: "text", Text: "No files in current context."}
			}
			// Scan history messages for file tool calls.
			seen := map[string]bool{}
			var files []string
			for _, msg := range state.GetHistory() {
				// Simple heuristic: look for absolute paths.
				words := strings.Fields(msg)
				for _, w := range words {
					if strings.HasPrefix(w, "/") && !seen[w] {
						seen[w] = true
						files = append(files, w)
					}
				}
			}
			if len(files) == 0 {
				return Result{Type: "text", Text: "No files referenced in current context."}
			}
			return Result{Type: "text", Text: "Files in context:\n  " + strings.Join(files, "\n  ")}
		},
	})

	// /export — write conversation markdown to disk
	r.Register(Command{
		Name:        "export",
		Description: "Export the current conversation to a markdown file",
		Handler: func(args string) Result {
			path := strings.TrimSpace(args)
			if path == "" {
				path = "claude-conversation.md"
			}
			return Result{Type: "export", Text: path}
		},
	})

	// /rename — session persistence not yet implemented
	r.Register(Command{
		Name:        "rename",
		Description: "Rename the current conversation (coming soon)",
		Handler: func(args string) Result {
			return Result{Type: "text", Text: "Conversation naming requires session persistence, which is not yet implemented."}
		},
	})

	// /feedback
	r.Register(Command{
		Name:        "feedback",
		Description: "Submit feedback about Claude Code",
		Handler: func(string) Result {
			url := "https://github.com/anthropics/claude-code/issues"
			openBrowser(url)
			return Result{Type: "text", Text: "Opening feedback page: " + url}
		},
	})

	// /release-notes
	r.Register(Command{
		Name:        "release-notes",
		Description: "View the latest Claude Code release notes",
		Handler: func(string) Result {
			url := "https://github.com/anthropics/claude-code/releases"
			openBrowser(url)
			return Result{Type: "text", Text: "Opening release notes: " + url}
		},
	})

	// /memory
	r.Register(Command{
		Name:        "memory",
		Description: "Edit Claude memory files (~/.claude/MEMORY.md)",
		Handler: func(string) Result {
			home, _ := os.UserHomeDir()
			path := home + "/.claude/MEMORY.md"
			editor := os.Getenv("EDITOR")
			if editor == "" {
				editor = "nano"
			}
			return Result{Type: "text", Text: fmt.Sprintf("To edit memory: %s %s", editor, path)}
		},
	})

	// /context
	r.Register(Command{
		Name:        "context",
		Description: "Show current context window usage breakdown",
		Handler: func(string) Result {
			const maxCtx = 200000
			var inputTokens, outputTokens int
			var costUSD float64
			if state.GetTokens != nil {
				inputTokens, outputTokens, costUSD = state.GetTokens()
			} else if state.GetHistory != nil {
				// Fallback: estimate from history chars.
				msgs := state.GetHistory()
				total := 0
				for _, m := range msgs {
					total += len(m)
				}
				inputTokens = total / 4
			}

			pct := 0
			if inputTokens > 0 {
				pct = inputTokens * 100 / maxCtx
				if pct > 100 {
					pct = 100
				}
			}

			bar := makeBar(pct, 40)
			var sb strings.Builder
			sb.WriteString("Context window usage\n\n")
			sb.WriteString(fmt.Sprintf("  Input tokens:  %d / %d (%d%%)\n", inputTokens, maxCtx, pct))
			sb.WriteString(fmt.Sprintf("  %s\n\n", bar))
			if outputTokens > 0 {
				sb.WriteString(fmt.Sprintf("  Output tokens: %d\n", outputTokens))
			}
			if costUSD > 0 {
				sb.WriteString(fmt.Sprintf("  Estimated cost: $%.4f\n", costUSD))
			}
			remaining := maxCtx - inputTokens
			if remaining < 0 {
				remaining = 0
			}
			sb.WriteString(fmt.Sprintf("  Remaining: ~%d tokens", remaining))
			return Result{Type: "text", Text: sb.String()}
		},
	})

	// /stats
	r.Register(Command{
		Name:        "stats",
		Description: "Show Claude Code usage statistics for this session",
		Handler: func(string) Result {
			if state.GetCost != nil {
				return Result{Type: "text", Text: "Session stats:\n" + state.GetCost()}
			}
			return Result{Type: "text", Text: "Stats not available."}
		},
	})

	// /usage
	r.Register(Command{
		Name:        "usage",
		Description: "Show plan usage limits",
		Handler: func(string) Result {
			return Result{Type: "text", Text: "View your usage and limits at: https://claude.ai/settings/limits"}
		},
	})

	// /resume — show navigable session picker
	r.Register(Command{
		Name:        "resume",
		Description: "Resume a previous conversation",
		Handler: func(args string) Result {
			cwd := "."
			if state.GetCwd != nil {
				cwd = state.GetCwd()
			}
			sessions, err := session.List(cwd)
			if err != nil || len(sessions) == 0 {
				return Result{Type: "text", Text: "No previous sessions found for this directory.\nTip: use --continue / -c when starting claude to resume the latest session automatically."}
			}
			// Cap to 20 most recent sessions.
			if len(sessions) > 20 {
				sessions = sessions[:20]
			}
			// Encode sessions as tab-separated lines for the TUI to parse.
			// Format: "<filePath>\t<age>\t<title>"
			var lines []string
			for _, s := range sessions {
				age := time.Since(s.Modified).Round(time.Minute).String()
				title := session.ExtractTitle(s.FilePath)
				if title == "" {
					title = s.ID[:min(8, len(s.ID))]
				}
				lines = append(lines, s.FilePath+"\t"+age+"\t"+title)
			}
			return Result{Type: "resume-pick", Text: strings.Join(lines, "\n")}
		},
	})

	// /search <term> — scan JSONL transcripts for matching turns
	r.Register(Command{
		Name:        "search",
		Description: "Search conversation history for a term. Usage: /search <term>",
		Handler: func(args string) Result {
			term := strings.TrimSpace(args)
			if term == "" {
				return Result{Type: "error", Text: "Usage: /search <term>"}
			}
			if state.SearchTranscript != nil {
				return Result{Type: "text", Text: state.SearchTranscript(term)}
			}
			// Fallback: search current session files.
			cwd := "."
			if state.GetCwd != nil {
				cwd = state.GetCwd()
			}
			sessions, err := session.List(cwd)
			if err != nil || len(sessions) == 0 {
				return Result{Type: "text", Text: "No sessions found for this directory."}
			}
			var sb strings.Builder
			found := 0
			for _, s := range sessions {
				results, err := session.Search(s.FilePath, term)
				if err != nil || len(results) == 0 {
					continue
				}
				title := session.ExtractTitle(s.FilePath)
				if title == "" {
					title = s.ID[:min(8, len(s.ID))]
				}
				sb.WriteString(fmt.Sprintf("─── %s ───\n", title))
				for _, r := range results {
					sb.WriteString(fmt.Sprintf("[%s] %s\n\n", r.Role, r.Text))
					found++
				}
			}
			if found == 0 {
				return Result{Type: "text", Text: fmt.Sprintf("No matches found for %q.", term)}
			}
			return Result{Type: "text", Text: strings.TrimRight(sb.String(), "\n")}
		},
	})

	// /rewind [n] — remove the last n turns (default 1) from in-memory history
	r.Register(Command{
		Name:        "rewind",
		Description: "Rewind conversation by N turns (default 1). /rewind 3 removes last 3 exchanges.",
		Handler: func(args string) Result {
			if state.Rewind == nil {
				return Result{Type: "text", Text: "Rewind is not available in this session."}
			}
			n := 1
			if trimmed := strings.TrimSpace(args); trimmed != "" {
				if _, err := fmt.Sscanf(trimmed, "%d", &n); err != nil || n < 1 {
					return Result{Type: "error", Text: "Usage: /rewind [n] — n must be a positive integer"}
				}
			}
			removed := state.Rewind(n)
			return Result{Type: "rewind", Text: fmt.Sprintf("%d", removed)}
		},
	})

	// /keybindings
	r.Register(Command{
		Name:        "keybindings",
		Description: "Show current keybindings",
		Handler: func(string) Result {
			text := strings.TrimSpace("Keybindings:\n" +
				"  Enter          Send message\n" +
				"  Shift+Enter    New line in input\n" +
				"  Ctrl+C         Interrupt current response\n" +
				"  Ctrl+Y         Copy last code block\n" +
				"  Ctrl+C (idle)  Exit\n" +
				"  ↑↓             Navigate command picker\n" +
				"  Tab            Complete slash command / navigate picker\n" +
				"  1/2/3          Quick-select permission options\n" +
				"  Escape         Close picker / dismiss permission prompt")
			return Result{Type: "text", Text: text}
		},
	})

	// /theme
	r.Register(Command{
		Name:        "theme",
		Description: "Change the terminal theme",
		Handler: func(args string) Result {
			return Result{Type: "text", Text: "Theme switching not yet implemented.\nThe TUI uses a dark coral theme by default."}
		},
	})

	// /status — one-liner: model, mode, session ID, cost, context %
	r.Register(Command{
		Name:        "status",
		Description: "Show current model, mode, session, cost, and context usage",
		Handler: func(string) Result {
			if state.GetStatus != nil {
				return Result{Type: "text", Text: state.GetStatus()}
			}
			return Result{Type: "text", Text: "Status not available."}
		},
	})

	// /tasks — list active TaskTool tasks
	r.Register(Command{
		Name:        "tasks",
		Description: "List active tasks created by the TaskCreate tool",
		Handler: func(string) Result {
			if state.GetTasks != nil {
				return Result{Type: "text", Text: state.GetTasks()}
			}
			return Result{Type: "text", Text: "No active tasks."}
		},
	})

	// /agents — list active sub-agents
	r.Register(Command{
		Name:        "agents",
		Description: "List active sub-agents in the current session",
		Handler: func(string) Result {
			if state.GetAgents != nil {
				return Result{Type: "text", Text: state.GetAgents()}
			}
			return Result{Type: "text", Text: "No active sub-agents."}
		},
	})

	// /thinkback — show last assistant thinking blocks
	r.Register(Command{
		Name:        "thinkback",
		Description: "Show the last assistant thinking blocks (if any)",
		Handler: func(string) Result {
			if state.GetLastThinking != nil {
				text := state.GetLastThinking()
				if text == "" {
					return Result{Type: "text", Text: "No thinking blocks in the last response."}
				}
				return Result{Type: "text", Text: text}
			}
			return Result{Type: "text", Text: "Thinking blocks not available."}
		},
	})

	// /color — toggle ANSI color output on/off
	r.Register(Command{
		Name:        "color",
		Description: "Toggle ANSI color output on/off",
		Handler: func(string) Result {
			if state.GetColor == nil || state.SetColor == nil {
				return Result{Type: "text", Text: "Color toggle not available."}
			}
			next := !state.GetColor()
			state.SetColor(next)
			if next {
				return Result{Type: "text", Text: "ANSI color output enabled."}
			}
			return Result{Type: "text", Text: "ANSI color output disabled."}
		},
	})

	// /copy — copy last assistant response to clipboard
	r.Register(Command{
		Name:        "copy",
		Description: "Copy the last assistant response to clipboard",
		Handler: func(string) Result {
			if state.CopyLast == nil {
				return Result{Type: "text", Text: "Copy not available in this session."}
			}
			if msg := state.CopyLast(); msg != "" {
				return Result{Type: "error", Text: msg}
			}
			return Result{Type: "flash", Text: "✓ Copied to clipboard"}
		},
	})
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func makeBar(pct, width int) string {
	filled := width * pct / 100
	if filled > width {
		filled = width
	}
	return "[" + strings.Repeat("█", filled) + strings.Repeat("░", width-filled) + "]"
}

func openBrowser(url string) {
	var cmd string
	switch runtime.GOOS {
	case "darwin":
		cmd = "open"
	case "linux":
		cmd = "xdg-open"
	default:
		return
	}
	_ = exec.Command(cmd, url).Start()
}
