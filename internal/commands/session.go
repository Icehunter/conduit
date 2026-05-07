package commands

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"time"

	"github.com/icehunter/conduit/internal/keybindings"
	"github.com/icehunter/conduit/internal/memdir"
	rg "github.com/icehunter/conduit/internal/ripgrep"
	"github.com/icehunter/conduit/internal/session"
	"github.com/icehunter/conduit/internal/settings"
)

// SessionState holds mutable session state that slash commands can read/modify.
type SessionState struct {
	GetCost               func() string
	GetVimMode            func() bool
	SetVimMode            func(bool)
	GetEffort             func() string
	SetEffort             func(string)
	GetFast               func() bool
	SetFast               func(bool)
	GetUsageStatusEnabled func() bool
	SetUsageStatusEnabled func(bool) error
	Logout                func() error
	GetHistory            func() []string // message contents for /files, /context
	GetCwd                func() string
	// Rewind removes the last n conversation turns from in-memory history.
	// Returns the number of turns actually removed.
	Rewind func(n int) int
	// SearchTranscript searches all session transcripts for cwd and returns results.
	SearchTranscript func(term string) string
	// GetTokens returns current (inputTokens, outputTokens, costUSD) from LiveState.
	GetTokens func() (int, int, float64)
	// GetTurnCosts returns the per-turn cost deltas for the current session.
	GetTurnCosts func() []float64
	// GetStatus returns a one-line status string (model, mode, session ID, cost, context %).
	GetStatus func() string
	// GetTasks returns a formatted list of active TaskTool tasks.
	GetTasks func() string
	// GetAgents returns a formatted list of active sub-agents.
	GetAgents func() string
	// GetLastThinking returns the last assistant thinking blocks as text.
	GetLastThinking func() string
	// GetColor returns the current ANSI color toggle state.
	GetColor func() bool
	// SetColor sets the ANSI color toggle.
	SetColor func(bool)
	// CopyLastResponse copies the last assistant response to clipboard.
	// Returns "" on success, error message otherwise.
	CopyLast func() string
	// RenameSession sets the title of the current session.
	RenameSession func(title string) error
	// TagSession assigns a tag label to the current session. Empty clears.
	TagSession func(tag string) error
	// GetSessionTag returns the active tag for the current session ("" if none).
	GetSessionTag func() string
	// GetSessionFiles returns (reads, writes) file paths from session JSONL.
	GetSessionFiles func() (reads, writes []string)
	// GetRateLimitWarning returns the current rate limit warning string (empty if none).
	GetRateLimitWarning func() string
	// CheckAuth returns nil if the current bearer token is valid, error otherwise.
	CheckAuth func() error
	// ExtractMemory triggers the memory extraction sub-agent over the recent
	// conversation. Returns a brief status string (or error). Wired in run.go
	// because the sub-agent runner lives on the agent.Loop.
	ExtractMemory func() (string, error)
	// GetSessionInfo returns session ID, file path, message count, and start time.
	GetSessionInfo func() (id, path string, messages int, startedAt time.Time)
	// GetSessionActivity returns last-activity time for idle reporting in /session.
	GetSessionActivity func() time.Time
	// GetKeybindings returns the flat binding list from the active resolver so
	// /keybindings can show actual (including user-customized) bindings.
	GetKeybindings func() []keybindings.Binding
}

// RegisterSessionCommands registers all session-dependent slash commands.
func RegisterSessionCommands(r *Registry, state *SessionState) {
	if state == nil {
		state = &SessionState{}
	}

	// /cost
	r.Register(Command{
		Name:        "cost",
		Description: "Show total cost and per-turn breakdown for this session",
		Handler: func(string) Result {
			if state.GetTokens == nil {
				return Result{Type: "text", Text: "Cost tracking not available."}
			}
			inTok, outTok, cost := state.GetTokens()
			if inTok == 0 && outTok == 0 && cost == 0 {
				return Result{Type: "text", Text: "No API calls made yet in this session."}
			}
			const labelW = 16
			var sb strings.Builder
			sb.WriteString(statusTitle("Session cost"))
			sb.WriteString(statusRow("Input tokens:", fmt.Sprintf("%d", inTok), "", labelW))
			sb.WriteString(statusRow("Output tokens:", fmt.Sprintf("%d", outTok), "", labelW))
			if cost > 0 {
				sb.WriteString(statusRow("Estimated cost:", fmt.Sprintf("$%.4f", cost), "", labelW))
			}
			// Per-turn breakdown if available.
			if state.GetTurnCosts != nil {
				turns := state.GetTurnCosts()
				if len(turns) > 0 {
					sb.WriteString("\nPer-turn breakdown:\n")
					for i, c := range turns {
						fmt.Fprintf(&sb, "  Turn %-3d  $%.4f\n", i+1, c)
					}
				}
			}
			return Result{Type: "text", Text: strings.TrimRight(sb.String(), "\n")}
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
				fmt.Fprintf(&sb, "Current effort: %s\n\nAvailable levels:\n", current)
				for _, level := range []string{"low", "normal", "high", "max"} {
					marker := "  "
					if level == current {
						marker = "❯ "
					}
					fmt.Fprintf(&sb, "%s%s — %s\n", marker, level, valid[level])
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

	// /toggle-usage
	r.Register(Command{
		Name:        "toggle-usage",
		Description: "Toggle Claude plan usage footer and background usage fetching",
		Handler: func(string) Result {
			if state.GetUsageStatusEnabled == nil || state.SetUsageStatusEnabled == nil {
				return Result{Type: "text", Text: "Usage footer toggle not available."}
			}
			next := !state.GetUsageStatusEnabled()
			if err := state.SetUsageStatusEnabled(next); err != nil {
				return Result{Type: "error", Text: fmt.Sprintf("toggle-usage: %v", err)}
			}
			if next {
				return Result{Type: "usage-toggle", Text: "on"}
			}
			return Result{Type: "usage-toggle", Text: "off"}
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

			out, err := exec.Command("git", gitArgs...).Output() //nolint:noctx
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
			exe, _ := os.Executable()
			const labelW = 14
			var sb strings.Builder
			sb.WriteString(statusTitle("Conduit diagnostics"))
			sb.WriteString(statusRow("Binary:", exe, "", labelW))
			sb.WriteString(statusRow("Platform:", runtime.GOOS+"/"+runtime.GOARCH, "", labelW))
			sb.WriteByte('\n')

			authOK := false
			if state.CheckAuth != nil {
				authOK = state.CheckAuth() == nil
			}
			authHint := ""
			if !authOK {
				authHint = "run /login"
			}
			sb.WriteString(statusRow("Auth:", statusCheck(authOK), authHint, labelW))

			_, gitErr := exec.LookPath("git")
			sb.WriteString(statusRow("git:", statusCheck(gitErr == nil), "", labelW))

			_, rgErr := exec.LookPath("rg")
			rgHint := ""
			if rgErr != nil {
				rgHint = "GrepTool uses grep fallback"
			}
			sb.WriteString(statusRow("ripgrep:", statusCheck(rgErr == nil), rgHint, labelW))

			home, _ := os.UserHomeDir()
			_, settingsErr := os.Stat(settings.ConduitSettingsPath())
			sb.WriteString(statusRow("settings:", statusCheck(settingsErr == nil), "", labelW))

			_, claudeErr := os.Stat(home + "/.claude.json")
			sb.WriteString(statusRow("claude.json:", statusCheck(claudeErr == nil), "", labelW))

			// Image paste availability.
			_, osascriptErr := exec.LookPath("osascript")
			imagePasteOK := osascriptErr == nil && runtime.GOOS == "darwin"
			if !imagePasteOK && runtime.GOOS == "linux" {
				_, xclipErr := exec.LookPath("xclip")
				_, wlErr := exec.LookPath("wl-paste")
				imagePasteOK = xclipErr == nil || wlErr == nil
			}
			imgHint := ""
			if !imagePasteOK {
				switch runtime.GOOS {
				case "darwin":
					imgHint = "osascript not found (unexpected)"
				case "windows":
					imgHint = "not supported on Windows"
				default:
					imgHint = "install xclip or wl-paste"
				}
			}
			sb.WriteString(statusRow("img paste:", statusCheck(imagePasteOK), imgHint, labelW))

			_ = sb // unused now — data goes to doctor-panel
			// Build structured check list for the TUI panel.
			type check struct {
				Label string
				OK    bool
				Hint  string
			}
			checks := []check{
				{"Auth", authOK, authHint},
				{"git", gitErr == nil, ""},
				{"ripgrep (rg)", rgErr == nil, rgHint},
				{"conduit.json", settingsErr == nil, ""},
				{"claude.json", claudeErr == nil, ""},
				{"img paste", imagePasteOK, imgHint},
			}
			rows := make([]string, 0, len(checks))
			for _, c := range checks {
				icon := "✅"
				if !c.OK {
					icon = "❌"
				}
				row := icon + " " + c.Label
				if c.Hint != "" {
					row += "  (" + c.Hint + ")"
				}
				rows = append(rows, row)
			}
			return Result{
				Type:  "doctor-panel",
				Text:  strings.Join(rows, "\n"),
				Model: exe + " · " + runtime.GOOS + "/" + runtime.GOARCH,
			}
		},
	})

	// /config [get <key> | set <key> <value> | list]
	// With no args → opens full-screen Settings panel on Config tab.
	r.Register(Command{
		Name:        "config",
		Description: "Open settings panel, or: /config get <key> | set <key> <value> | list | validate",
		Handler: func(args string) Result {
			settingsPath := settings.ConduitSettingsPath()

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
					return Result{Type: "error", Text: "conduit.json parse error: " + err.Error()}
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
				if err := json.Unmarshal(data, &raw); err != nil {
					return Result{Type: "error", Text: "conduit.json parse error: " + err.Error()}
				}
				val := getNestedKey(raw, key)
				out, _ := json.MarshalIndent(val, "", "  ")
				return Result{Type: "text", Text: key + ": " + string(out)}

			case "validate":
				cfg, err := settings.LoadConduitConfig()
				if err != nil {
					return Result{Type: "error", Text: "conduit.json parse error: " + err.Error()}
				}
				providers, roles, changed := settings.CanonicalizeProviderRegistry(cfg.Providers, cfg.Roles)
				if changed {
					cfg.Providers = providers
					cfg.Roles = roles
					if err := settings.SaveConduitConfig(cfg); err != nil {
						return Result{Type: "error", Text: "failed to repair provider keys: " + err.Error()}
					}
				}
				errs := settings.ValidateProviderRegistry(providers, roles)
				if len(errs) == 0 {
					if changed {
						return Result{Type: "text", Text: "Config OK. Provider keys were repaired."}
					}
					return Result{Type: "text", Text: "Config OK."}
				}
				var sb strings.Builder
				if changed {
					sb.WriteString("Provider keys were repaired.\n\n")
				}
				sb.WriteString("Config issues:\n")
				for _, err := range errs {
					sb.WriteString("- " + err.Error() + "\n")
				}
				return Result{Type: "error", Text: strings.TrimRight(sb.String(), "\n")}

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
				return Result{Type: "error", Text: "Usage: /config list | get <key> | set <key> <value> | validate"}
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

	// /tag [name]  — name "" or "clear" removes the tag.
	r.Register(Command{
		Name:        "tag",
		Description: "Tag the current session for later retrieval (empty arg clears)",
		Handler: func(args string) Result {
			if state.TagSession == nil {
				return Result{Type: "text", Text: "Session tagging not available."}
			}
			tag := strings.TrimSpace(args)
			if tag == "clear" {
				tag = ""
			}
			if err := state.TagSession(tag); err != nil {
				return Result{Type: "error", Text: fmt.Sprintf("tag failed: %v", err)}
			}
			if tag == "" {
				return Result{Type: "flash", Text: "Tag cleared"}
			}
			return Result{Type: "flash", Text: "Tagged: #" + tag}
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

	// /memory extract is wired in run.go because it needs the sub-agent runner.

	// /memory [list|show|scan|extract]
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
				fmt.Fprintf(&sb, "Memory scan (%d files):\n\n", len(files))
				for _, f := range files {
					age := time.Since(f.ModTime)
					status := "✓"
					if age > 30*24*time.Hour {
						status = "⚠ stale (>30d)"
					} else if age > 7*24*time.Hour {
						status = "~ aging (>7d)"
					}
					fmt.Fprintf(&sb, "  %s [%s] %s\n", status, f.Type, f.Name)
				}
				return Result{Type: "text", Text: strings.TrimRight(sb.String(), "\n")}

			case "extract":
				if state.ExtractMemory == nil {
					return Result{Type: "text", Text: "Memory extraction not available (no agent loop)."}
				}
				summary, err := state.ExtractMemory()
				if err != nil {
					return Result{Type: "error", Text: fmt.Sprintf("extract: %v", err)}
				}
				if summary == "" {
					summary = "Memory extraction complete."
				}
				return Result{Type: "text", Text: summary}

			default:
				return Result{Type: "error", Text: "Usage: /memory [list|show|scan|extract]"}
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
			fmt.Fprintf(&sb, "  Input tokens:  %d / %d (%d%%)\n", inputTokens, maxCtx, pct)
			fmt.Fprintf(&sb, "  %s\n\n", bar)
			if outputTokens > 0 {
				fmt.Fprintf(&sb, "  Output tokens: %d\n", outputTokens)
			}
			if costUSD > 0 {
				fmt.Fprintf(&sb, "  Estimated cost: $%.4f\n", costUSD)
			}
			remaining := maxCtx - inputTokens
			if remaining < 0 {
				remaining = 0
			}
			fmt.Fprintf(&sb, "  Remaining: ~%d tokens", remaining)
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
			// Encode sessions as tab-separated lines for the TUI to parse.
			// Format: "<filePath>\t<age>\t<title>\t<recordCount>\t<size>"
			var lines []string
			for _, s := range sessions {
				age := formatSessionAge(time.Since(s.Modified))
				title := session.ExtractTitle(s.FilePath)
				if title == "" {
					title = "session " + s.ID[:min(8, len(s.ID))]
				}
				if tag, _ := session.LoadTag(s.FilePath); tag != "" {
					title = "#" + tag + " · " + title
				}
				recordCount := countJSONLLines(s.FilePath)
				size := formatSessionFootprint(sessionFootprintBytes(s.FilePath))
				lines = append(lines, fmt.Sprintf("%s\t%s\t%s\t%d\t%s", s.FilePath, age, title, recordCount, size))
			}
			return Result{Type: "resume-pick", Text: strings.Join(lines, "\n")}
		},
	})

	// /search <term> — scan JSONL transcripts and open a navigable results panel.
	// Prefix with "rg:" to run a ripgrep file-content search in the cwd instead.
	r.Register(Command{
		Name:        "search",
		Description: "Search conversation history. Prefix rg: to search files in cwd. Usage: /search <term>",
		Handler: func(args string) Result {
			term := strings.TrimSpace(args)
			if term == "" {
				return Result{Type: "error", Text: "Usage: /search <term>  or  /search rg:<pattern>"}
			}
			cwd := "."
			if state.GetCwd != nil {
				cwd = state.GetCwd()
			}

			// rg: prefix → ripgrep file-content search in cwd.
			if strings.HasPrefix(term, "rg:") {
				pattern := strings.TrimSpace(strings.TrimPrefix(term, "rg:"))
				if pattern == "" {
					return Result{Type: "error", Text: "Usage: /search rg:<pattern>"}
				}
				if !rg.Available() {
					return Result{Type: "error", Text: "ripgrep (rg) not found — install with: brew install ripgrep"}
				}
				matches, err := rg.Search(pattern, cwd, 200)
				if err != nil {
					return Result{Type: "error", Text: fmt.Sprintf("rg error: %v", err)}
				}
				if len(matches) == 0 {
					return Result{Type: "text", Text: fmt.Sprintf("No matches for %q in %s", pattern, cwd)}
				}
				var lines []string
				for _, m := range matches {
					snippet := m.Content
					if len([]rune(snippet)) > 120 {
						snippet = string([]rune(snippet)[:120]) + "…"
					}
					// Use file path as "session", line number as context.
					lines = append(lines, fmt.Sprintf("%s\t%s\t%s\t%s\t%s",
						m.File, fmt.Sprintf("line %d", m.Line), cwd, "file", snippet))
				}
				return Result{Type: "search-panel", Text: strings.Join(lines, "\n"), Model: pattern + " (files)"}
			}

			sessions, err := session.List(cwd)
			if err != nil || len(sessions) == 0 {
				return Result{Type: "text", Text: "No sessions found for this directory."}
			}
			// Encode results as tab-separated: filePath\ttitle\tage\trole\tsnippet
			var lines []string
			for _, s := range sessions {
				results, err := session.Search(s.FilePath, term)
				if err != nil || len(results) == 0 {
					continue
				}
				title := session.ExtractTitle(s.FilePath)
				if title == "" {
					title = s.ID[:min(8, len(s.ID))]
				}
				age := formatSessionAge(time.Since(s.Modified))
				for _, r := range results {
					// Flatten snippet to one line for transport.
					snippet := strings.ReplaceAll(strings.TrimSpace(r.Text), "\n", " ↵ ")
					if len([]rune(snippet)) > 120 {
						snippet = string([]rune(snippet)[:120]) + "…"
					}
					lines = append(lines, fmt.Sprintf("%s\t%s\t%s\t%s\t%s",
						s.FilePath, title, age, r.Role, snippet))
				}
			}
			if len(lines) == 0 {
				return Result{Type: "text", Text: fmt.Sprintf("No matches found for %q.", term)}
			}
			return Result{Type: "search-panel", Text: strings.Join(lines, "\n"), Model: term}
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
			const labelW = 12
			var sb strings.Builder
			sb.WriteString(statusTitle("Session"))
			sb.WriteString(statusRow("ID:", id, "", labelW))
			if path != "" {
				sb.WriteString(statusRow("File:", path, "", labelW))
			}
			if state.GetSessionTag != nil {
				if tag := state.GetSessionTag(); tag != "" {
					sb.WriteString(statusRow("Tag:", "#"+tag, "", labelW))
				}
			}
			sb.WriteString(statusRow("Messages:", fmt.Sprintf("%d", msgs), "", labelW))
			if !startedAt.IsZero() {
				sb.WriteString(statusRow("Duration:", humanDuration(time.Since(startedAt)), "", labelW))
			}
			if state.GetSessionActivity != nil {
				if last := state.GetSessionActivity(); !last.IsZero() {
					idle := time.Since(last)
					if idle >= 30*time.Second {
						sb.WriteString(statusRow("Idle:", humanDuration(idle), "", labelW))
					}
				}
			}
			return Result{Type: "text", Text: strings.TrimRight(sb.String(), "\n")}
		},
	})

	// /keybindings
	r.Register(Command{
		Name:        "keybindings",
		Description: "Show current keybindings (including user customizations)",
		Handler: func(string) Result {
			var sb strings.Builder
			sb.WriteString("Keybindings\n\n")

			// Always show the fixed bindings that aren't in the resolver.
			sb.WriteString("  Fixed (not remappable):\n")
			sb.WriteString("    Shift+Enter      Insert newline\n")
			sb.WriteString("    Shift+↑/↓        Scroll chat history\n")
			sb.WriteString("    PgUp/PgDn         Page scroll\n")
			sb.WriteString("    Ctrl+V            Paste image from clipboard\n")
			sb.WriteString("    Ctrl+Y            Copy last code block\n")
			sb.WriteString("    1/2/3             Quick-select permission options\n\n")

			// Dynamic bindings from the resolver.
			if state.GetKeybindings != nil {
				bindings := state.GetKeybindings()
				// Group by context.
				byCtx := map[string][]keybindings.Binding{}
				order := []string{}
				seen := map[string]bool{}
				for _, b := range bindings {
					if !seen[b.Context] {
						seen[b.Context] = true
						order = append(order, b.Context)
					}
					byCtx[b.Context] = append(byCtx[b.Context], b)
				}
				for _, ctx := range order {
					bs := byCtx[ctx]
					sb.WriteString("  " + ctx + ":\n")
					for _, b := range bs {
						key := b.Keystroke.String()
						if b.Unbind {
							fmt.Fprintf(&sb, "    %-20s  (unbound)\n", key)
						} else {
							fmt.Fprintf(&sb, "    %-20s  %s\n", key, b.Action)
						}
					}
					sb.WriteByte('\n')
				}
				sb.WriteString("  Edit ~/.conduit/keybindings.json to customize. Conduit imports ~/.claude/keybindings.json only when no Conduit keybindings file exists.")
			}
			return Result{Type: "text", Text: strings.TrimRight(sb.String(), "\n")}
		},
	})

	// /theme is registered in RegisterMiscCommands (theme.Set + persist).

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
