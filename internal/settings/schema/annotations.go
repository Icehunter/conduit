package schema

import "github.com/icehunter/conduit/internal/settings"

// permissionModes are the values permissions.Mode defines
// (internal/permissions/permissions.go) plus the legacy "auto" that
// cmd/conduit/mainrepl.go still normalises to bypassPermissions.
//
// "dontAsk" is a Claude Code mode (EXTERNAL_PERMISSION_MODES) that conduit
// does not act on; it is listed so shared settings files do not error.
var permissionModes = Strings("default", "acceptEdits", "acceptEditsLive", "bypassPermissions", "plan", "council", "auto", "dontAsk")

// mcpTransports are the transports internal/mcp openClient dials
// (case-insensitively; the schema lists the lowercase spellings).
//
// sse-ide, ws-ide, sdk and claudeai-proxy are Claude Code internal transports
// that conduit does not dial; they are listed so CC-written files do not
// error. Runtime matching is case-insensitive, so the schema is too.
var mcpTransports = []string{"", "stdio", "sse", "http", "ws", "websocket", "sse-ide", "ws-ide", "sdk", "claudeai-proxy"}

// sharedAnnotations decorate types that appear in more than one document.
func sharedAnnotations() Annotations {
	return Annotations{
		"Permissions.Allow":          {Description: "Permission rules that run without prompting, e.g. \"Bash(git status:*)\"."},
		"Permissions.Deny":           {Description: "Permission rules that are always refused."},
		"Permissions.Ask":            {Description: "Permission rules that always prompt, even where an allow rule matches."},
		"Permissions.DefaultMode":    {Description: "Permission mode a session starts in.", Enum: append(Strings(""), permissionModes...)},
		"Permissions.AdditionalDirs": {Description: "Extra directories file tools may operate in without prompting."},

		"Hook.Type":           {Description: "How the hook runs. An empty or missing type means command. mcp_tool is a Claude Code type that conduit does not run.", Enum: Strings("", "command", "http", "prompt", "agent", "mcp_tool")},
		"Hook.StatusMessage":  {Description: "Spinner text shown while the hook runs."},
		"Hook.If":             {Description: "Permission-rule expression that gates whether the hook fires."},
		"Hook.TimeoutSecs":    {Description: "Per-hook timeout override, in seconds."},
		"Hook.Once":           {Description: "Remove the hook after its first execution."},
		"Hook.Async":          {Description: "Fire and forget; do not wait for the hook."},
		"Hook.Command":        {Description: "Shell command (type \"command\")."},
		"Hook.URL":            {Description: "POST target (type \"http\")."},
		"Hook.Headers":        {Description: "Extra request headers (type \"http\")."},
		"Hook.AllowedEnvVars": {Description: "Environment variables that may be interpolated into headers (type \"http\")."},
		"Hook.Prompt":         {Description: "LLM prompt, may contain $ARGUMENTS (type \"prompt\" or \"agent\")."},
		"Hook.Model":          {Description: "Model override (type \"prompt\" or \"agent\")."},

		"HookMatcher":         {Description: "A tool-name matcher and the hooks it triggers."},
		"HookMatcher.Matcher": {Description: "Tool name or glob; alternatives separated by \"|\". Empty matches everything."},
		"HookMatcher.Hooks":   {Description: "Hooks to run when the matcher applies."},

		"HooksSettings.PreToolUse":       {Description: "Run before a tool call; can allow, deny, ask or rewrite its input."},
		"HooksSettings.PostToolUse":      {Description: "Run after a tool call completes."},
		"HooksSettings.SessionStart":     {Description: "Run when a session starts, resumes, clears or compacts."},
		"HooksSettings.Stop":             {Description: "Run when the agent finishes responding."},
		"HooksSettings.TeammateIdle":     {Description: "Run when an agent-team teammate goes idle."},
		"HooksSettings.TaskCreated":      {Description: "Run when a task is created."},
		"HooksSettings.TaskCompleted":    {Description: "Run when a task is completed."},
		"HooksSettings.UserPromptSubmit": {Description: "Run before a prompt reaches the model; can block it, add context or rewrite it. Matchers are ignored."},

		"ActiveProviderSettings":                       {Description: "One named provider in the registry."},
		"ActiveProviderSettings.Kind":                  {Description: "Provider kind.", Enum: Strings(settings.ProviderKindClaudeSubscription, settings.ProviderKindAnthropicAPI, settings.ProviderKindOpenAICompatible, settings.ProviderKindMCP)},
		"ActiveProviderSettings.Model":                 {Description: "Model identifier sent to the provider."},
		"ActiveProviderSettings.Account":               {Description: "Account key or email (claude-subscription, anthropic-api)."},
		"ActiveProviderSettings.Credential":            {Description: "Credential alias (openai-compatible, anthropic-api). An alias, never the API key itself."},
		"ActiveProviderSettings.BaseURL":               {Description: "Absolute http(s) base URL (openai-compatible)."},
		"ActiveProviderSettings.ContextWindow":         {Description: "Context window size in tokens, when the server does not report it."},
		"ActiveProviderSettings.Server":                {Description: "MCP server name (mcp)."},
		"ActiveProviderSettings.DirectTool":            {Description: "MCP tool used for direct prompts (mcp)."},
		"ActiveProviderSettings.ImplementTool":         {Description: "MCP tool used for implementation tasks (mcp)."},
		"ActiveProviderSettings.ExtraBody":             {Description: "Vendor-specific request fields merged into every request as a JSON merge patch: objects merge, other values replace, null deletes (openai-compatible)."},
		"ActiveProviderSettings.ReasoningReplay":       {Description: "When an assistant turn's reasoning is sent back as reasoning_content (openai-compatible). Empty means auto: local/LAN servers and model families that require it.", Schema: CaseInsensitive("", "auto", "on", "off")},
		"ActiveProviderSettings.ToolCallFormat":        {Description: "How tool calls are carried (openai-compatible). native uses the tools request field; hermes and qwen3 put schemas in the system prompt and parse <tool_call> blocks; auto is native until the server rejects tools. Empty means native.", Schema: CaseInsensitive("", "native", "hermes", "qwen3", "auto")},
		"ActiveProviderSettings.ResponseHeaderTimeout": {Description: "Seconds an openai-compatible provider may take to send response headers. 0 or unset means no limit."},
	}
}

func merge(sets ...Annotations) Annotations {
	out := Annotations{}
	for _, s := range sets {
		for k, v := range s {
			out[k] = v
		}
	}
	return out
}

// SettingsAnnotations decorates settings.Settings.
func SettingsAnnotations() Annotations {
	return merge(sharedAnnotations(), Annotations{
		"Settings.Permissions":                {Description: "Tool permission rules and default mode."},
		"Settings.Hooks":                      {Description: "Lifecycle hooks, keyed by event name."},
		"Settings.Env":                        {Description: "Extra environment variables for tool execution."},
		"Settings.EnabledPlugins":             {Description: "Plugins to enable, keyed \"pluginName@marketplace\"."},
		"Settings.OnboardingComplete":         {Description: "Set once the first-run welcome screen has been dismissed."},
		"Settings.EnabledMcpjsonServers":      {Description: "Project .mcp.json servers approved to connect."},
		"Settings.DisabledMcpjsonServers":     {Description: "Project .mcp.json servers that never connect."},
		"Settings.EnableAllProjectMcpServers": {Description: "Approve every server in the project .mcp.json."},
		"Settings.Model":                      {Description: "Preferred model name."},
		"Settings.Providers":                  {Description: "Named provider registry (conduit only); roles reference these keys."},
		"Settings.Roles":                      {Description: "Maps a role (default, main, planning, background, implement) to a provider key."},
		"Settings.OutputStyle":                {Description: "Active output style name; built-ins are default, Explanatory and Learning, and custom styles come from output-style files."},
		"Settings.Theme":                      {Description: "Active palette name: dark, light, dark-daltonized, light-daltonized, dark-ansi, light-ansi, or a custom name from themes."},
		"Settings.UsageStatusEnabled":         {Description: "Show the Claude plan usage footer (conduit only)."},
		"Settings.ThemeOverrides":             {Description: "Per-field color tweaks on top of the named theme: lowercase palette field name to #RRGGBB or ANSI 0-15 (conduit only)."},
		"Settings.Themes":                     {Description: "Custom themes: theme name to a field map shaped like themeOverrides (conduit only)."},
	})
}

// ConduitAnnotations decorates settings.ConduitConfig.
func ConduitAnnotations() Annotations {
	return merge(sharedAnnotations(), Annotations{
		"ConduitConfig.SchemaURL":                   {Description: "URL of the JSON Schema this file follows (editor hint; ignored at load)."},
		"ConduitConfig.SchemaVersion":               {Description: "Config format version, maintained by conduit."},
		"ConduitConfig.NumStartups":                 {Description: "Number of times conduit has started, maintained by conduit."},
		"ConduitConfig.Permissions":                 {Description: "Tool permission rules and default mode."},
		"ConduitConfig.Hooks":                       {Description: "Lifecycle hooks, keyed by event name."},
		"ConduitConfig.Env":                         {Description: "Extra environment variables for tool execution."},
		"ConduitConfig.Projects":                    {Description: "Per-project state keyed by absolute path, maintained by conduit."},
		"ConduitConfig.EnabledPlugins":              {Description: "Plugins to enable, keyed \"pluginName@marketplace\"."},
		"ConduitConfig.OnboardingComplete":          {Description: "Set once the first-run welcome screen has been dismissed."},
		"ConduitConfig.EnabledMcpjsonServers":       {Description: "Project .mcp.json servers approved to connect."},
		"ConduitConfig.DisabledMcpjsonServers":      {Description: "Project .mcp.json servers that never connect."},
		"ConduitConfig.EnableAllProjectMcpServers":  {Description: "Approve every server in the project .mcp.json."},
		"ConduitConfig.Model":                       {Description: "Preferred model name."},
		"ConduitConfig.Providers":                   {Description: "Named provider registry; roles reference these keys."},
		"ConduitConfig.Roles":                       {Description: "Maps a role (default, main, planning, background, implement) to a provider key."},
		"ConduitConfig.ProviderChains":              {Description: "Maps a role to an ordered list of provider keys to fail over to on 429/503/529."},
		"ConduitConfig.CouncilProviders":            {Description: "Provider keys that take part in council-mode debates. Empty disables council mode."},
		"ConduitConfig.CouncilMaxRounds":            {Description: "Maximum debate rounds before synthesis.", Default: 4},
		"ConduitConfig.CouncilMemberTimeoutSec":     {Description: "Seconds each council member call may run. 0 means no timeout."},
		"ConduitConfig.CouncilSynthesizer":          {Description: "Provider key used for synthesis. Empty uses the parent loop's background model."},
		"ConduitConfig.CouncilSynthesizerMaxTokens": {Description: "max_tokens for the synthesis call.", Default: 8192},
		"ConduitConfig.CouncilConvergenceThreshold": {Description: "Minimum Jaccard similarity (0-1) that ends a debate early. Zero or negative disables it."},
		"ConduitConfig.CouncilRoles":                {Description: "Maps a provider key to a council role such as architect, skeptic or perf-reviewer."},
		"ConduitConfig.OutputStyle":                 {Description: "Active output style name; built-ins are default, Explanatory and Learning, and custom styles come from output-style files."},
		"ConduitConfig.Theme":                       {Description: "Active palette name: dark, light, dark-daltonized, light-daltonized, dark-ansi, light-ansi, or a custom name from themes."},
		"ConduitConfig.UsageStatusEnabled":          {Description: "Show the Claude plan usage footer."},
		"ConduitConfig.AgentTeams":                  {Description: "Enable agent teams."},
		"ConduitConfig.ThemeOverrides":              {Description: "Per-field color tweaks on top of the named theme: lowercase palette field name to #RRGGBB or ANSI 0-15."},
		"ConduitConfig.Themes":                      {Description: "Custom themes: theme name to a field map shaped like themeOverrides."},
		"ConduitConfig.DisableMouse":                {Description: "Turn off terminal mouse capture so native selection works (tmux/ssh). Wheel scroll and click-to-expand are unavailable; use the keyboard bindings."},
		"ConduitConfig.Accounts":                    {Description: "Signed-in accounts, maintained by conduit."},
		"ConduitConfig.LSPServers":                  {Description: "Per-language-server overrides keyed by language key, e.g. \"go\" or \"typescript\"."},
		"ConduitConfig.ToolOutput":                  {Description: "Tool output truncation limits."},
		"ConduitConfig.Compaction":                  {Description: "Conversation compaction behavior."},
		"ConduitConfig.RedactSecrets":               {Description: "Mask secrets (API keys, tokens, private keys, password assignments) in tool results. Unset means on.", Default: true},
		"ConduitConfig.TTSR":                        {Description: "Mid-stream rule settings beyond the rule files themselves."},
		"ConduitConfig.WebSearch":                   {Description: "WebSearch tool provider chain."},

		"WebSearchConfig.Keyless":           {Description: "Keyless search tier (Parallel, DuckDuckGo, Mojeek), which sends the query to third parties. auto uses it only when no other provider is usable; always also falls back to it when a configured provider fails; never disables it.", Schema: CaseInsensitive("", settings.WebSearchKeylessAuto, settings.WebSearchKeylessAlways, settings.WebSearchKeylessNever), Default: settings.WebSearchKeylessAuto},
		"TTSRConfig.RepetitionGuard":        {Description: "Interrupt output that has degenerated into a repeating loop. Unset means on.", Default: true},
		"ToolOutputConfig.MaxLines":         {Description: "Lines before output is truncated to disk.", Default: 2000},
		"ToolOutputConfig.MaxBytes":         {Description: "Bytes before output is truncated to disk.", Default: 51200},
		"CompactionConfig.KeepRecent":       {Description: "Recent tool results preserved during micro-compaction.", Default: 5},
		"CompactionConfig.Reserved":         {Description: "Token budget reserved for model output when computing usable context.", Default: 20000},
		"CompactionConfig.Auto":             {Description: "Enable auto-compaction.", Default: true},
		"CompactionConfig.MaxTriggerTokens": {Description: "Cap on the input-token count at which auto-compaction fires. 0 or negative means no cap; unset uses the built-in cap."},
		"LSPServerOverride.Cmd":             {Description: "Server executable."},
		"LSPServerOverride.Args":            {Description: "Server arguments."},
		"LSPServerOverride.Env":             {Description: "Extra environment entries, in KEY=value form."},
		"LSPServerOverride.Disabled":        {Description: "Do not start this language server."},
	})
}

// MCPAnnotations decorates mcp.McpJSON and mcp.ServerConfig.
func MCPAnnotations() Annotations {
	return Annotations{
		"McpJSON.McpServers":   {Description: "MCP servers keyed by name."},
		"ServerConfig":         {Description: "How to connect to one MCP server."},
		"ServerConfig.Type":    {Description: "Transport. Empty or missing means stdio. Matched case-insensitively.", Schema: CaseInsensitive(mcpTransports...)},
		"ServerConfig.Command": {Description: "Executable to launch (stdio)."},
		"ServerConfig.Args":    {Description: "Arguments for the command (stdio)."},
		"ServerConfig.Env":     {Description: "Environment variables for the server process (stdio)."},
		"ServerConfig.URL":     {Description: "Server endpoint (sse, http, ws)."},
		"ServerConfig.Headers": {Description: "Extra request headers (sse, http, ws)."},
	}
}
