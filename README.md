# conduit

**A local-first, provider-aware coding agent for the terminal.**

Conduit began as a Go port of Claude Code. It still keeps that compatibility where it matters: Claude OAuth, Anthropic wire format, the familiar tool surface, project memory, sessions, plugins, and MCP. It has also grown into its own agent runtime: Conduit-owned config, multi-account switching, role-based model assignment, OpenAI-compatible providers such as Gemini, and local/MCP-backed models can all live in one TUI.

The goal is simple: keep the Claude Code ergonomics, make the runtime fast and hackable, and let you choose where each kind of work runs.

<p align="center">
  <img src="internal/assets/conduit.png" alt="Conduit logo" width="200" />
</p>

<p align="center">
  <a href="https://github.com/icehunter/conduit/releases"><img src="https://img.shields.io/github/v/release/icehunter/conduit?style=flat-square&color=5e6ad2" alt="Release" /></a>
  <a href="https://github.com/icehunter/conduit/actions"><img src="https://img.shields.io/github/actions/workflow/status/icehunter/conduit/ci.yml?style=flat-square" alt="CI" /></a>
  <a href="https://goreportcard.com/report/github.com/icehunter/conduit"><img src="https://goreportcard.com/badge/github.com/icehunter/conduit?style=flat-square" alt="Go Report" /></a>
  <a href="LICENSE"><img src="https://img.shields.io/github/license/icehunter/conduit" alt="License" /></a>
</p>

<p align="center">
  <a href="https://star-history.com/#icehunter/conduit&Date">
    <picture>
      <source media="(prefers-color-scheme: dark)" srcset="https://api.star-history.com/svg?repos=icehunter/conduit&type=Date&theme=dark" />
      <source media="(prefers-color-scheme: light)" srcset="https://api.star-history.com/svg?repos=icehunter/conduit&type=Date" />
      <img alt="Star history for icehunter/conduit" src="https://api.star-history.com/svg?repos=icehunter/conduit&type=Date" width="600" />
    </picture>
  </a>
</p>

---

## Why conduit?

| | Claude Code | conduit |
|---|---|---|
| Cold start | ~800ms | <30ms |
| Idle memory | ~200MB | <80MB |
| Binary size | ~206MB | ~30MB |
| Dependencies | Node.js + Bun runtime | None (static binary) |
| Multi-account | Restart required | `/account` panel, live switch |
| Providers | Claude-focused | Claude subscription, Anthropic API, OpenAI-compatible, local MCP |
| Role routing | Single active model | Default, main, background, planning, implement |
| Token savings | External `rtk` subprocess | In-process RTK filter |

---

## Features

### Core agent

- **Full Claude Code parity** — 249/264 scoped features implemented (94%)
- **Provider-aware routing** — assign models per role from `/models`
- **OpenAI-compatible providers** — use API-key providers such as Gemini through a local credential alias
- **Local model support** — route turns to MCP-backed local providers, including model servers on another machine
- **Streaming SSE** — real-time token-by-token output with cost tracking
- **Auto-compact** — context window managed automatically; manual `/compact`
- **Thinking mode** — extended reasoning via `/effort low|normal|high|max`
- **Fast mode** — `/fast` toggles Haiku for faster/cheaper turns
- **Coordinator mode** — Claude as orchestrator managing parallel sub-agents
- **Exponential backoff** — automatic retry on 429 with jitter

### Tools (30+ built-in)

| Tool | What it does |
|------|-------------|
| `BashTool` | Run shell commands with RTK token compression |
| `FileReadTool` | Read files with line-range support |
| `FileWriteTool` | Write files with diff preview |
| `FileEditTool` | Surgical edits by exact string replacement |
| `GrepTool` | Regex search across files |
| `GlobTool` | File discovery by pattern |
| `AgentTool` | Spawn sub-agents for parallel work |
| `WebFetchTool` | Fetch URLs with content extraction |
| `WebSearchTool` | Web search via Anthropic's native search API |
| `NotebookEditTool` | Edit Jupyter notebooks cell by cell |
| `REPLTool` | Persistent REPL sessions (Python, Node, Ruby…) |
| `TodoWriteTool` | Structured task tracking |
| `TaskCreate/Get/List/Update/Stop` | Sub-agent task lifecycle |
| `EnterPlanMode` / `ExitPlanMode` | Propose-only mode (no file edits) |
| `EnterWorktree` / `ExitWorktree` | Git worktree switching |
| `AskUserQuestion` | Pause for user input mid-turn |
| `ConfigTool` | Read/write conduit settings |
| `MCPTool` | Call any MCP server tool |
| `ListMcpResources` / `ReadMcpResource` | MCP resource access |
| `SkillTool` | Execute installed skills |
| `SleepTool` | Pause between actions |
| `LSPTool` | Language server queries: hover, definition, references, diagnostics |
| `ToolSearchTool` | Search available tools by name or description |
| `SyntheticOutputTool` | Inject synthetic tool results (coordinator signalling) |

### MCP (Model Context Protocol)

- **stdio, HTTP, SSE transports** — connect any MCP server
- **Conduit-owned MCP config** — `~/.conduit/mcp.json` overlays imported Claude MCP settings
- **Plugin system** — install from marketplace (`claude-plugins-official`), enable/disable plugins, and manage plugin MCP servers
- **Resource listing and reading** — `ListMcpResources`, `ReadMcpResource`
- **MCP panel** — `/mcp` opens full browser with per-server tool lists and reconnect
- **Plugin panel** — `/plugin` browses marketplace, manages installed plugins

### Memory & context

- **CLAUDE.md loading** — walks from cwd to root, injects as system blocks
- **`@include` directives** — compose CLAUDE.md files across directories
- **Auto-memory** — summarises conversations to `MEMORY.md`; injected on next session
- **Memory scanning** — `/memory scan` finds stale or contradicted facts
- **Relevant memory search** — surfaces related memories by keyword on session start

### TUI

- **Bubble Tea v2** — native Shift+Enter via Kitty keyboard protocol
- **Full GFM markdown** — tables, task lists, strikethrough, blockquotes, code blocks with syntax
- **Light and dark themes** — `/theme` picker with 6 built-in palettes (dark, light, daltonized variants, ANSI variants), custom theme support
- **Output styles** — `/output-style` for different response modes (`default`, `Explanatory`, `Learning`; plugins can add more)
- **Custom keybindings** — `~/.conduit/keybindings.json` remaps actions, with Claude keybindings imported as fallback
- **Status bar** — provider, model, mode, context %, cost, and rate-limit indicator
- **Coordinator footer** — live sub-agent progress during multi-agent runs

### Accounts and providers

- **Platform keychain storage** — macOS Keychain, Linux libsecret, Windows Credential Manager via `go-keyring`
- **`/account` panel** — add, switch, remove, and delete accounts without restarting
- **Conduit-owned account registry** — account metadata lives in `~/.conduit/conduit.json`; credentials stay in the keychain
- **Provider registry** — Claude subscription, Anthropic API, OpenAI-compatible, and MCP providers can be assigned to roles
- **Role presets** — default, main, background, planning, and implement roles make it easy to run expensive work on one model and smaller work somewhere else

### In-process RTK (token savings)

Conduit ships with [RTK (Rust Token Killer)](https://github.com/rtk-ai/rtk) ported to Go as a zero-overhead in-process filter. Every `BashTool` result is passed through the filter before being sent to the model.

Typical savings:

- `git status` → 90% reduction
- `cargo test` → 85% (test failures only)
- `npm install` → 80%
- `eslint` → 75%

View your savings: type `/rtk gain` inside the TUI.

### Hooks

- **PreToolUse / PostToolUse / SessionStart / Stop** — shell, HTTP, prompt, and agent hooks
- **Async hooks** — PostToolUse hooks run in background without blocking the next turn
- **Desktop notifications** — notify on turn complete (macOS native + Linux `notify-send`)

### Session management

- **JSONL transcripts** — every session saved to `~/.conduit/projects/<path>/<id>.jsonl`, with existing Claude history imported on resume
- **`/resume`** — fuzzy-search all past sessions, load with full history
- **`/search`** — search across all session transcripts for a term
- **Cost persistence** — accumulated cost tracked per-session, shown in `/status`
- **Session recording** — `/record` captures terminal output in asciicast v2 format

---

## Installation

### Homebrew (macOS / Linux)

```sh
brew install icehunter/tap/conduit
```

### Go install

```sh
go install github.com/icehunter/conduit/cmd/conduit@latest
```

### Pre-built binary

Download from [Releases](https://github.com/icehunter/conduit/releases) for your platform:

```sh
curl -L https://github.com/icehunter/conduit/releases/latest/download/conduit-darwin-arm64 -o conduit
chmod +x conduit
mv conduit /usr/local/bin/
```

### Build from source

```sh
git clone https://github.com/icehunter/conduit
cd conduit
make build
# binary at ./conduit
```

---

## Quick start

```sh
# Start an interactive session
conduit

# Sign in with your Claude Max / Teams subscription (inside the TUI)
/login

# Assign providers/models by role
/models

# Configure accounts, providers, plugins, stats, and usage
/config

# One-shot prompt (no TUI)
conduit --print "explain this codebase"

# Continue last session
conduit --continue

# Resume a specific session
conduit --resume <session-id>
```

---

## Slash commands

| Command | Description |
|---------|-------------|
| `/help` | List all commands |
| `/commands` | Open the slash command picker |
| `/model` | Switch model (claude-opus-4-7, claude-sonnet-4-6, …) |
| `/models` | Open the role-aware provider/model picker |
| `/fast` | Toggle fast mode (⚡ badge in status bar) |
| `/effort <level>` | Set thinking budget: low / normal / high / max |
| `/compact` | Summarise and compress conversation history |
| `/clear` | Clear conversation and start fresh |
| `/plan` | Enter plan mode — Claude proposes, doesn't edit |
| `/permissions` | Manage tool permission mode |
| `/account` | Add, switch, or remove accounts |
| `/login` | Sign in to Claude |
| `/logout` | Sign out current account |
| `/config` | Full settings panel (Status · Config · Stats · Usage · Accounts · Providers) |
| `/status` | Current model, mode, session, cost, context usage |
| `/theme` | Switch colour theme |
| `/output-style` | Switch output formatting style |
| `/mcp` | Browse MCP servers and their tools |
| `/plugin` | Browse and manage plugins |
| `/memory` | View, search, and scan MEMORY.md |
| `/resume` | Load a past session |
| `/search <term>` | Search all session transcripts |
| `/record` | Start / stop session recording (asciicast v2) |
| `/files` | Files read or written this session |
| `/diff` | Git diff of files edited this session |
| `/context` | Current context window usage breakdown |
| `/color` | Toggle ANSI colour output on/off |
| `/rename` | Set a title for the current session |
| `/rewind <n>` | Roll back the last n turns (default 1) |
| `/commit` | Create a git commit with a generated message |
| `/cost` | Token and cost breakdown |
| `/usage` | Rate limit quota and burn rate |
| `/toggle-usage` | Toggle Claude plan usage footer and background fetching |
| `/stats` | Per-tool usage counts |
| `/session` | Session ID, path, message count, duration |
| `/tasks` | Active sub-agent tasks |
| `/agents` | Active sub-agents |
| `/thinkback` | Show last thinking blocks |
| `/copy` | Copy last assistant response to clipboard |
| `/rtk` | RTK token-savings commands |
| `/doctor` | Diagnose: auth, MCP connectivity, settings |
| `/keybindings` | Show active keybinding map |
| `/buddy` | Meet your companion |
| `/coordinator` | Toggle coordinator mode |
| `/hooks` | Manage PreToolUse / PostToolUse hooks |
| `/init` | Create a CLAUDE.md for this project |
| `/review` | Compact and summarise what was done |
| `/exit` | Quit |

---

## Configuration

### Settings file

`~/.claude/settings.json` — all settings managed via `/config` panel or edited directly.

Key settings:

```json
{
  "model": "claude-sonnet-4-6",
  "defaultPermissionMode": "default",
  "autoCompactEnabled": true,
  "alwaysThinkingEnabled": true,
  "theme": "dark",
  "outputStyle": "default",
  "env": {
    "MY_TOKEN": "..."
  }
}
```

### CLAUDE.md

Place a `CLAUDE.md` in any directory — conduit reads from the file's directory up to the repo root and your home directory, injecting each as a system context block. Use `@path/to/file` inside to include another file inline.

### Custom keybindings

`~/.claude/keybindings.json`:

```json
{
  "compact": ["ctrl+k"],
  "command:clear": ["ctrl+l"],
  "submit": ["enter"]
}
```

### Hooks

```json
// ~/.claude/settings.json
{
  "hooks": {
    "PreToolUse": [
      {
        "matcher": "BashTool",
        "hooks": [{ "type": "command", "command": "echo pre-bash" }]
      }
    ],
    "PostToolUse": [
      {
        "matcher": "*",
        "hooks": [{ "type": "command", "command": "notify.sh", "async": true }]
      }
    ]
  }
}
```

Hook types: `command` (shell), `http` (POST JSON to URL), `prompt` (inject result as context), `agent` (spawn sub-agent).

### MCP servers

```json
// ~/.claude/settings.json
{
  "mcpServers": {
    "context7": {
      "command": "npx",
      "args": ["-y", "@upstash/context7-mcp"]
    },
    "playwright": {
      "command": "npx",
      "args": ["@playwright/mcp@latest"]
    }
  }
}
```

---

## Themes

Built-in themes (matching Claude Code's palette set):

| Name | Description |
|------|-------------|
| `dark` | Default dark theme |
| `light` | Light theme |
| `dark-daltonized` | Dark with colour-blindness-friendly palette (alias: `dark-accessible`) |
| `light-daltonized` | Light with colour-blindness-friendly palette (alias: `light-accessible`) |
| `dark-ansi` | Dark using terminal's 16 ANSI colours |
| `light-ansi` | Light using terminal's 16 ANSI colours |

Switch with `/theme`, or set `"theme": "dark-ansi"` in `settings.json`.

Custom themes can be defined under `"themes"` in `settings.json`:

```json
{
  "themes": {
    "my-theme": {
      "primary": "#c0caf5",
      "secondary": "#565f89",
      "accent": "#7aa2f7",
      "background": "#1a1b26"
    }
  }
}
```

---

## RTK — in-process token compression

Conduit ships with [RTK (Rust Token Killer)](https://github.com/rtk-ai/rtk) — originally a Rust CLI by [rtk-ai](https://www.rtk-ai.app/) — ported to Go and fused directly into the process. It intercepts `BashTool` results before they reach the model and applies command-aware compression rules.

Inside the TUI:

```
/rtk gain      # cumulative savings across all sessions
/rtk discover  # scan transcripts for missed compression opportunities
```

RTK covers 70 command patterns including: git, cargo, npm/yarn/pnpm, pytest, eslint, dotnet, aws CLI, ls, and structured log output.

---

## Multi-agent / coordinator mode

```
/coordinator
```

Enables coordinator mode: Claude acts as an orchestrator that spawns sub-agents via `AgentTool`. Sub-agents run in parallel, each with their own tool context and history. The coordinator footer in the status bar shows live agent progress.

---

## Building from source

Requirements: Go 1.26+

```sh
make build      # builds to ./conduit
make test       # go test ./...
make test-race  # go test -race ./...
make lint       # golangci-lint run
```

---

## Parity

Conduit implements 249/264 scoped Claude Code features (94%). The remaining features are Claude-internal or require the VS Code/JetBrains bridge:

- Bridge (VS Code / JetBrains JSON-RPC) — use real Claude Code for IDE integration
- Remote agents / ULTRAPLAN — Anthropic-hosted, bridge-dependent
- Voice recording — no portable Go audio; would require cgo + whisper.cpp
- Team swarm (SendMessageTool, TeamCreateTool) — requires Anthropic teammate mailbox
- GrowthBook feature flags / KAIROS — Anthropic-internal
- Vim mode — 1,513 LOC port of CC vim bindings; deferred

Full breakdown: [PARITY.md](PARITY.md)

---

## License

MIT — see [LICENSE](LICENSE).

Conduit is an independent project and is not affiliated with or endorsed by Anthropic. Claude® is a trademark of Anthropic, PBC.
