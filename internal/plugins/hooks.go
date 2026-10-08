package plugins

import "github.com/icehunter/conduit/internal/settings"

// MergeHooksFrom returns a copy of base with each plugin's hook matchers appended.
// Plugin hooks are already tagged with PluginRoot and SourceFile by loadHooks, so
// FilterUntrustedHooks passes them through (they're not project-local).
// The base pointer may be nil; in that case only plugin hooks are returned.
func MergeHooksFrom(ps []*Plugin, base *settings.HooksSettings) *settings.HooksSettings {
	merged := &settings.HooksSettings{}
	if base != nil {
		merged.PreToolUse = append(merged.PreToolUse, base.PreToolUse...)
		merged.PostToolUse = append(merged.PostToolUse, base.PostToolUse...)
		merged.SessionStart = append(merged.SessionStart, base.SessionStart...)
		merged.Stop = append(merged.Stop, base.Stop...)
		merged.TeammateIdle = append(merged.TeammateIdle, base.TeammateIdle...)
		merged.TaskCreated = append(merged.TaskCreated, base.TaskCreated...)
		merged.TaskCompleted = append(merged.TaskCompleted, base.TaskCompleted...)
		merged.UserPromptSubmit = append(merged.UserPromptSubmit, base.UserPromptSubmit...)
	}
	for _, p := range ps {
		merged.PreToolUse = append(merged.PreToolUse, p.Hooks.PreToolUse...)
		merged.PostToolUse = append(merged.PostToolUse, p.Hooks.PostToolUse...)
		merged.SessionStart = append(merged.SessionStart, p.Hooks.SessionStart...)
		merged.Stop = append(merged.Stop, p.Hooks.Stop...)
		merged.TeammateIdle = append(merged.TeammateIdle, p.Hooks.TeammateIdle...)
		merged.TaskCreated = append(merged.TaskCreated, p.Hooks.TaskCreated...)
		merged.TaskCompleted = append(merged.TaskCompleted, p.Hooks.TaskCompleted...)
		merged.UserPromptSubmit = append(merged.UserPromptSubmit, p.Hooks.UserPromptSubmit...)
	}
	return merged
}
