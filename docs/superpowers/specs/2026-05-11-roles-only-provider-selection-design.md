# Roles-only provider selection (remove ActiveProvider)

## Summary
Remove the legacy `activeProvider` field and use role bindings as the single source of truth for provider + model selection. Every role resolves via `roles[role]` with a fallback to `roles[default]`. Council mode continues to use explicit `CouncilProviders` and `CouncilSynthesizer` keys and is not role-based.

## Goals
- Remove `activeProvider` from settings, config, and runtime state.
- Ensure every agent/subagent selects provider+model based on its assigned role.
- Preserve council mode’s explicit provider-key roster and synthesizer behavior.
- Provide clear validation/UI errors when role/default is missing.

## Non-Goals
- Changing council roster format or switching council to role-based selection.
- Adding new provider kinds or reworking provider auth.

## Architecture
- **Settings**: remove `ActiveProvider` from `Settings` and `Merged` structs.
- **Config persistence**: drop `activeProvider` from `ConduitConfig` and migration paths.
- **Role resolution**: `ProviderForRole` resolves `roles[role]` → providers map, falls back to `roles[default]`.
- **TUI**: remove `activeProvider` state and startup wiring; apply local mode via role-bound MCP provider only.

## Data flow
- **Startup**: load settings → canonicalize providers/roles → use roles for any provider resolution.
- **Runtime**: each agent/subagent uses its assigned role to resolve provider/model on demand.
- **Fallback**: if role is unset, resolve `roles[default]`; if default unset or invalid, show a clear UI error and block model-dependent actions.

## Error handling
- Invalid or missing role references surface in the provider UI as validation warnings.
- If both role and default are unset, show an error and require configuration before sending model requests.

## Council mode
- `CouncilProviders` and `CouncilSynthesizer` remain explicit provider keys.
- `buildCouncilRoster` and `buildSynthesizerClient` continue to resolve provider keys directly.

## Testing
- Update settings tests to remove `ActiveProvider` cases.
- Add tests covering role fallback to default provider key.
- TUI tests for provider resolution and error surfacing when default missing.
- Council tests remain unchanged but must pass to confirm no regressions.
