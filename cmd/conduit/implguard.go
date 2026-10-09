package main

import (
	"github.com/icehunter/conduit/internal/implguard"
	"github.com/icehunter/conduit/internal/permissions"
	"github.com/icehunter/conduit/internal/settings"
)

// newImplementGuard builds the session's implement-role guard. Settings are
// reloaded on every check so role edits apply without a restart.
func newImplementGuard(cwd string, gate *permissions.Gate, verifiedCoding bool) *implguard.Guard {
	return implguard.New(func() (bool, string) {
		return implementGuardCheck(settings.Load, cwd, gate.Mode(), verifiedCoding)
	})
}

// implementGuardCheck loads settings and decides. A load error fails closed:
// the guard stays active, since the implement role cannot be ruled out.
func implementGuardCheck(load func(string) (*settings.Merged, error), cwd string, mode permissions.Mode, verifiedCoding bool) (bool, string) {
	merged, err := load(cwd)
	if err != nil {
		return true, "implement provider"
	}
	return implementGuardActive(merged, mode, verifiedCoding)
}

// implementGuardActive reports whether another provider owns code changes,
// and that provider's ref for messages.
//
// It is active when an implement provider resolves and the agent is not
// running on it. An MCP implement provider can never run the agent loop (it is
// reached through LocalImplement), so it is always another provider. Any other
// provider is the one the agent runs on exactly when the permission mode maps
// to the implement role, mirroring tui.Model.currentProviderRole.
func implementGuardActive(merged *settings.Merged, mode permissions.Mode, verifiedCoding bool) (bool, string) {
	impl, ok := merged.ProviderForRole(settings.RoleImplement)
	if !ok {
		return false, ""
	}
	ref := merged.Roles[settings.RoleImplement]
	if impl.Kind == settings.ProviderKindMCP {
		return true, ref
	}
	role := runningRole(mode, verifiedCoding)
	if role == settings.RoleImplement || merged.Roles[role] == ref {
		return false, ref
	}
	return true, ref
}

// runningRole is the role the agent loop runs on in a permission mode; it
// mirrors tui.Model.currentProviderRole.
func runningRole(mode permissions.Mode, verifiedCoding bool) string {
	if verifiedCoding && mode != permissions.ModePlan && mode != permissions.ModeCouncil {
		return settings.RoleMain
	}
	switch mode {
	case permissions.ModePlan, permissions.ModeCouncil:
		return settings.RolePlanning
	case permissions.ModeAcceptEdits, permissions.ModeBypassPermissions:
		// The MCP case never reaches here; implementGuardActive returned early.
		return settings.RoleImplement
	default:
		return settings.RoleDefault
	}
}
