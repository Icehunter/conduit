package commands

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/icehunter/conduit/internal/memdir"
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
	// RenameSession sets the title of the current session.
	RenameSession func(title string) error
	// GetSessionFiles returns (reads, writes) file paths from session JSONL.
	GetSessionFiles func() (reads, writes []string)
	// GetRateLimitWarning returns the current rate limit warning string (empty if none).
	GetRateLimitWarning func() string
	// CheckAuth returns nil if the current bearer token is valid, error otherwise.
	CheckAuth func() error
	// GetSessionInfo returns session ID, file path, message count, and start time.
	GetSessionInfo func() (id, path string, messages int, startedAt time.Time)
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
		Description: "Show git diff of files edited this session (or all changes with --all)",
		Handler: func(args string) Result {
			cwd := "."
			if state.GetCwd != nil {
				cwd = state.GetCwd()
			}
			showAll := strings.TrimSpace(args) == "--all" || state.GetSessionFiles == nil

			var gitArgs []string
			if showAll {
				gitArgs = []string{"-C", cwd, "diff"}
			} else {
				_, writes := state.GetSessionFiles()
				if len(writes) == 0 {
					// Fall back to full diff if no session writes recorded yet.
					gitArgs = []string{"-C", cwd, "diff"}
				} else {
					gitArgs = append([]string{"-C", cwd, "diff", "--"}, writes...)
				}
			}

			out, err := exec.Command("git", gitArgs...).Output()
			if err != nil {
				// git diff exits non-zero when there are differences on some versions;
				// treat non-empty output as success.
				if len(out) == 0 {
					return Result{Type: "error", Text: fmt.Sprintf("git diff: %v", err)}
				}
			}
			if len(out) == 0 {
				return Result{Type: "text", Text: "No changes to session files."}
			}
			return Result{Type: "text", Text: "```diff\n" + strings.TrimSpace(string(out)) + "\n```"}
		},
	})

	// /doctor
	r.Register(Command{
		Name:        "doctor",
		Description: "Check auth, tools, and settings",
		Handler: func(string) Result {
			bold := "\033[1m"
			green := "\033[32m"
			red := "\033[31m"
			dim := "\033[2m"
			reset := "\033[0m"

			check := func(ok bool) string {
				if ok {
					return green + "✓" + reset
				}
				return red + "✗" + reset
			}
			row := func(lbl, val, hint string) string {
				return fmt.Sprintf("  %s%-14s%s %s%s\n", bold, lbl, reset, val, hint)
			}

			var sb strings.Builder
			sb.WriteString(bold + "Conduit diagnostics" + reset + "\n\n")

			exe, _ := os.Executable()
			sb.WriteString(row("Binary:", exe, ""))
			sb.WriteString(row("Platform:", runtime.GOOS+"/"+runtime.GOARCH, ""))
			sb.WriteByte('\n')

			authOK := false
			if state.CheckAuth != nil {
				authOK = state.CheckAuth() == nil
			}
			authHint := ""
			if !authOK {
				authHint = dim + "  (run /login)" + reset
			}
			sb.WriteString(row("Auth:", check(authOK), authHint))

			_, gitErr := exec.LookPath("git")
			sb.WriteString(row("git:", check(gitErr == nil), ""))

			_, rgErr := exec.LookPath("rg")
			rgHint := ""
			if rgErr != nil {
				rgHint = dim + "  (GrepTool uses grep fallback)" + reset
			}
			sb.WriteString(row("ripgrep:", check(rgErr == nil), rgHint))

			home, _ := os.UserHomeDir()
			_, settingsErr := os.Stat(home + "/.claude/settings.json")
			sb.WriteString(row("settings:", check(settingsErr == nil), ""))

			_, claudeErr := os.Stat(home + "/.claude.json")
			sb.WriteString(row("claude.json:", check(claudeErr == nil), ""))

			return Result{Type: "text", Text: strings.TrimRight(sb.String(), "\n")}
		},
	})

	// /config [get <key> | set <key> <value> | list]
	// With no args → opens full-screen Settings panel on Config tab.
	r.Register(Command{
		Name:        "config",
		Description: "Open settings panel, or: /config get <key> | set <key> <value> | list",
		Handler: func(args string) Result {
			home, _ := os.UserHomeDir()
			settingsPath := filepath.Join(home, ".claude", "settings.json")

			parts := strings.Fields(args)
			sub := ""
			if len(parts) > 0 {
				sub = strings.ToLower(parts[0])
			}

			// No args → open full-screen settings panel on Config tab.
			if sub == "" {
				return Result{Type: "settings-panel", Text: "config"}
			}

			switch sub {
			case "list":
				data, err := os.ReadFile(settingsPath)
				if err != nil {
					return Result{Type: "text", Text: "No settings file found at " + settingsPath}
				}
				var raw map[string]interface{}
				if err := json.Unmarshal(data, &raw); err != nil {
					return Result{Type: "error", Text: "settings.json parse error: " + err.Error()}
				}
				out, _ := json.MarshalIndent(raw, "", "  ")
				return Result{Type: "text", Text: "```json\n" + string(out) + "\n```"}

			case "get":
				if len(parts) < 2 {
					return Result{Type: "error", Text: "Usage: /config get <key>"}
				}
				key := parts[1]
				data, err := os.ReadFile(settingsPath)
				if err != nil {
					return Result{Type: "text", Text: key + ": (not set)"}
				}
				var raw map[string]interface{}
				_ = json.Unmarshal(data, &raw)
				val := getNestedKey(raw, key)
				out, _ := json.MarshalIndent(val, "", "  ")
				return Result{Type: "text", Text: key + ": " + string(out)}

			case "set":
				if len(parts) < 3 {
					return Result{Type: "error", Text: "Usage: /config set <key> <value>"}
				}
				key := parts[1]
				valueStr := strings.Join(parts[2:], " ")
				// Try to parse as JSON, fall back to plain string.
				var value interface{}
				if err := json.Unmarshal([]byte(valueStr), &value); err != nil {
					value = valueStr
				}
				if err := upsertSettingsKey(settingsPath, key, value); err != nil {
					return Result{Type: "error", Text: "failed to update settings: " + err.Error()}
				}
				return Result{Type: "flash", Text: "✓ Set " + key}

			default:
				return Result{Type: "error", Text: "Usage: /config list | get <key> | set <key> <value>"}
			}
		},
	})

	// /files
	r.Register(Command{
		Name:        "files",
		Description: "List files read and written this session",
		Handler: func(string) Result {
			if state.GetSessionFiles == nil {
				return Result{Type: "text", Text: "File tracking not available."}
			}
			reads, writes := state.GetSessionFiles()
			if len(reads) == 0 && len(writes) == 0 {
				return Result{Type: "text", Text: "No files accessed this session."}
			}
			var sb strings.Builder
			if len(writes) > 0 {
				sb.WriteString("Written:\n")
				for _, p := range writes {
					sb.WriteString("  " + p + "\n")
				}
			}
			if len(reads) > 0 {
				sb.WriteString("Read:\n")
				for _, p := range reads {
					sb.WriteString("  " + p + "\n")
				}
			}
			return Result{Type: "text", Text: strings.TrimRight(sb.String(), "\n")}
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

	// /rename <title>
	r.Register(Command{
		Name:        "rename",
		Description: "Set a title for the current conversation",
		Handler: func(args string) Result {
			title := strings.TrimSpace(args)
			if title == "" {
				return Result{Type: "error", Text: "Usage: /rename <title>"}
			}
			if state.RenameSession == nil {
				return Result{Type: "text", Text: "Session rename not available."}
			}
			if err := state.RenameSession(title); err != nil {
				return Result{Type: "error", Text: fmt.Sprintf("rename failed: %v", err)}
			}
			return Result{Type: "flash", Text: "Session renamed: " + title}
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

	// /memory [list|show|scan]
	r.Register(Command{
		Name:        "memory",
		Description: "Manage memory files. Subcommands: list (default), show, scan",
		Handler: func(args string) Result {
			sub := strings.ToLower(strings.TrimSpace(args))
			cwd := "."
			if state.GetCwd != nil {
				cwd = state.GetCwd()
			}

			switch sub {
			case "", "list":
				files, err := memdir.ScanMemories(cwd)
				if err != nil {
					return Result{Type: "error", Text: fmt.Sprintf("scan: %v", err)}
				}
				if len(files) == 0 {
					path := memdir.EntrypointPath(cwd)
					return Result{Type: "text", Text: fmt.Sprintf("No memory files yet.\nMEMORY.md location: %s", path)}
				}
				return Result{Type: "text", Text: memdir.FormatMemoryList(files)}

			case "show":
				content := memdir.BuildPrompt(cwd)
				if content == "" {
					return Result{Type: "text", Text: "No memory content found."}
				}
				// Strip the system prompt wrapper, show raw content.
				return Result{Type: "text", Text: content}

			case "scan":
				files, err := memdir.ScanMemories(cwd)
				if err != nil {
					return Result{Type: "error", Text: fmt.Sprintf("scan: %v", err)}
				}
				if len(files) == 0 {
					return Result{Type: "text", Text: "No memory files to scan."}
				}
				// Summarize stale or very old files.
				var sb strings.Builder
				sb.WriteString(fmt.Sprintf("Memory scan (%d files):\n\n", len(files)))
				for _, f := range files {
					age := time.Since(f.ModTime)
					status := "✓"
					if age > 30*24*time.Hour {
						status = "⚠ stale (>30d)"
					} else if age > 7*24*time.Hour {
						status = "~ aging (>7d)"
					}
					sb.WriteString(fmt.Sprintf("  %s [%s] %s\n", status, f.Type, f.Name))
				}
				return Result{Type: "text", Text: strings.TrimRight(sb.String(), "\n")}

			default:
				return Result{Type: "error", Text: "Usage: /memory [list|show|scan]"}
			}
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

	// /stats — opens settings panel on Stats tab
	r.Register(Command{
		Name:        "stats",
		Description: "Show usage stats (sessions, tokens, models) — Overview and Models subtabs",
		Handler: func(string) Result {
			return Result{Type: "settings-panel", Text: "stats"}
		},
	})

	// /usage
	// /usage — opens settings panel on Usage tab
	r.Register(Command{
		Name:        "usage",
		Description: "Show token usage and rate limit status",
		Handler: func(string) Result {
			return Result{Type: "settings-panel", Text: "usage"}
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
				age := formatSessionAge(time.Since(s.Modified))
				title := session.ExtractTitle(s.FilePath)
				if title == "" {
					title = "session " + s.ID[:min(8, len(s.ID))]
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

	// /session — show session ID, file path, message count, duration.
	r.Register(Command{
		Name:        "session",
		Description: "Show session ID, file path, message count, and duration",
		Handler: func(string) Result {
			if state.GetSessionInfo == nil {
				return Result{Type: "text", Text: "No session info available."}
			}
			id, path, msgs, startedAt := state.GetSessionInfo()
			var sb strings.Builder
			sb.WriteString(fmt.Sprintf("Session ID:   %s\n", id))
			if path != "" {
				sb.WriteString(fmt.Sprintf("File:         %s\n", path))
			}
			sb.WriteString(fmt.Sprintf("Messages:     %d\n", msgs))
			if !startedAt.IsZero() {
				dur := time.Since(startedAt)
				h := int(dur.Hours())
				m := int(dur.Minutes()) % 60
				if h > 0 {
					sb.WriteString(fmt.Sprintf("Duration:     %dh %dm\n", h, m))
				} else {
					sb.WriteString(fmt.Sprintf("Duration:     %dm\n", m))
				}
			}
			return Result{Type: "text", Text: strings.TrimRight(sb.String(), "\n")}
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

	// /status — opens full-screen settings panel on Status tab
	r.Register(Command{
		Name:        "status",
		Description: "Show full status panel (model, mode, session, MCP, diagnostics)",
		Handler: func(string) Result {
			return Result{Type: "settings-panel", Text: "status"}
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

// formatSessionAge returns a concise human-readable age string.
func formatSessionAge(d time.Duration) string {
	switch {
	case d < time.Minute:
		return "just now"
	case d < time.Hour:
		m := int(d.Minutes())
		if m == 1 {
			return "1m ago"
		}
		return fmt.Sprintf("%dm ago", m)
	case d < 24*time.Hour:
		h := int(d.Hours())
		if h == 1 {
			return "1h ago"
		}
		return fmt.Sprintf("%dh ago", h)
	case d < 7*24*time.Hour:
		days := int(d.Hours() / 24)
		if days == 1 {
			return "yesterday"
		}
		return fmt.Sprintf("%dd ago", days)
	case d < 30*24*time.Hour:
		weeks := int(d.Hours() / (24 * 7))
		if weeks == 1 {
			return "1w ago"
		}
		return fmt.Sprintf("%dw ago", weeks)
	default:
		months := int(d.Hours() / (24 * 30))
		if months == 1 {
			return "1mo ago"
		}
		return fmt.Sprintf("%dmo ago", months)
	}
}

func makeBar(pct, width int) string {
	filled := width * pct / 100
	if filled > width {
		filled = width
	}
	return "[" + strings.Repeat("█", filled) + strings.Repeat("░", width-filled) + "]"
}

// getNestedKey retrieves a dot-path key from a map (e.g. "permissions.allow").
func getNestedKey(m map[string]interface{}, key string) interface{} {
	parts := strings.SplitN(key, ".", 2)
	val, ok := m[parts[0]]
	if !ok {
		return nil
	}
	if len(parts) == 1 {
		return val
	}
	sub, ok := val.(map[string]interface{})
	if !ok {
		return nil
	}
	return getNestedKey(sub, parts[1])
}

// upsertSettingsKey writes key=value into the settings JSON file using raw-map
// preservation so unknown fields survive the round-trip.
func upsertSettingsKey(path string, key string, value interface{}) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	raw := make(map[string]json.RawMessage)
	if data, err := os.ReadFile(path); err == nil && len(data) > 0 {
		_ = json.Unmarshal(data, &raw)
	}
	parts := strings.SplitN(key, ".", 2)
	if len(parts) == 1 {
		encoded, err := json.Marshal(value)
		if err != nil {
			return err
		}
		raw[key] = encoded
	} else {
		// Nested key: read sub-object, update, write back.
		var sub map[string]interface{}
		if existing, ok := raw[parts[0]]; ok {
			_ = json.Unmarshal(existing, &sub)
		}
		if sub == nil {
			sub = make(map[string]interface{})
		}
		sub[parts[1]] = value
		encoded, err := json.Marshal(sub)
		if err != nil {
			return err
		}
		raw[parts[0]] = encoded
	}
	out, err := json.MarshalIndent(raw, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(out, '\n'), 0o644)
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
