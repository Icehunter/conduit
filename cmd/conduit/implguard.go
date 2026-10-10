package main

import (
	"github.com/icehunter/conduit/internal/implguard"
	"github.com/icehunter/conduit/internal/mcp"
	"github.com/icehunter/conduit/internal/permissions"
	"github.com/icehunter/conduit/internal/settings"
	"github.com/icehunter/conduit/internal/tools/localimplementtool"
)

// providerReachable reports whether any of the implement candidates is served
// by a connected MCP server right now.
type providerReachable func(candidates []*settings.ActiveProviderSettings) bool

// mcpImplementReachable probes the manager through localimplementtool.ResolveFirst,
// the resolution LocalImplement registers with, so the guard is active exactly
// when LocalImplement is registered.
func mcpImplementReachable(mgr *mcp.Manager) providerReachable {
	return func(candidates []*settings.ActiveProviderSettings) bool {
		_, ok := localimplementtool.ResolveFirst(mgr, candidates)
		return ok
	}
}

// newImplementGuard builds the session's implement-role guard. Settings are
// reloaded and MCP reachability is probed on every check, so role edits and
// servers that connect late or drop mid-session apply without a restart.
func newImplementGuard(cwd string, gate *permissions.Gate, verifiedCoding bool, mgr *mcp.Manager) *implguard.Guard {
	reachable := mcpImplementReachable(mgr)
	return implguard.NewStatus(func() implguard.Status {
		return implementGuardCheck(settings.Load, cwd, gate.Mode(), verifiedCoding, reachable)
	})
}

// implementGuardCheck loads settings and decides. A load error fails closed:
// the guard stays active, since the implement role cannot be ruled out.
func implementGuardCheck(load func(string) (*settings.Merged, error), cwd string, mode permissions.Mode, verifiedCoding bool, reachable providerReachable) implguard.Status {
	merged, err := load(cwd)
	if err != nil {
		return implguard.Status{Active: true, Label: "implement provider"}
	}
	return implementGuardActive(merged, mode, verifiedCoding, reachable)
}

// implementGuardActive decides whether another provider owns code changes, and
// carries that provider's ref for messages.
//
// It is active when an implement provider resolves and the agent is not
// running on it. An MCP implement provider can never run the agent loop (it is
// reached through LocalImplement), so it is always another provider, but it
// only owns changes while a connected server exposes its implement tool: with
// none reachable the guard is inactive and Unreachable is set. A rolePools
// pool for implement counts as reachable when any member resolves, in the
// same order LocalImplement tries them. Any other
// provider is the one the agent runs on exactly when the permission mode maps
// to the implement role, mirroring tui.Model.currentProviderRole.
func implementGuardActive(merged *settings.Merged, mode permissions.Mode, verifiedCoding bool, reachable providerReachable) implguard.Status {
	impl, ok := merged.ProviderForRole(settings.RoleImplement)
	if !ok {
		return implguard.Status{}
	}
	ref := merged.Roles[settings.RoleImplement]
	if impl.Kind == settings.ProviderKindMCP {
		if reachable != nil && reachable(merged.ImplementCandidates()) {
			return implguard.Status{Active: true, Label: ref}
		}
		return implguard.Status{Label: ref, Unreachable: true}
	}
	role := runningRole(mode, verifiedCoding)
	if role == settings.RoleImplement || merged.Roles[role] == ref {
		return implguard.Status{Label: ref}
	}
	return implguard.Status{Active: true, Label: ref}
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

// implementCandidatesFor reloads settings and returns the implement providers
// LocalImplement may resolve against. When settings cannot supply any, the
// provider known at startup (nil for none) stands in.
func implementCandidatesFor(cwd string, fallback *settings.ActiveProviderSettings) []*settings.ActiveProviderSettings {
	if latest, err := settings.Load(cwd); err == nil {
		if candidates := latest.ImplementCandidates(); len(candidates) > 0 {
			return candidates
		}
	}
	if fallback == nil {
		return nil
	}
	return []*settings.ActiveProviderSettings{fallback}
}
