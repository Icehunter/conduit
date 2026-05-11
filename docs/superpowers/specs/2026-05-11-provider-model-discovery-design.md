# Provider model discovery on add + scheduled refresh

## Summary
Add first-class model discovery for OpenAI-compatible providers (Gemini, OpenRouter, custom endpoints) when a provider is added, persist discovered models into the provider registry, and refresh per-provider models asynchronously every 24 hours. If discovery fails, keep the provider visible and surface a clear error message in the model picker. Remove all fallback model lists. Apply Copilot/Codex wire headers to all Copilot API calls.

## Goals
- Discover models immediately after provider add (API key/baseURL) and persist them into `providers`.
- Refresh model lists asynchronously per provider instance (credential+baseURL) every 24 hours.
- No fallback model lists anywhere; failed discovery should not invent models.
- Model picker should still show the provider when discovery fails and display a clear error row.
- Apply Copilot wire headers to every Copilot call to avoid missing `Editor-Version` errors.

## Non-Goals
- No UI redesign beyond adding a single error row in the model picker.
- No automatic removal of user-entered models outside discovery refresh logic.
- No change to Claude subscription model list.

## Data Flow
### Provider add
1. User adds provider (credential + baseURL).
2. Persist provider entry with baseURL/credential (without model) if needed.
3. Discover models for that provider:
   - Gemini: `GET https://generativelanguage.googleapis.com/v1beta/models?key=GEMINI_API_KEY`
   - OpenAI-compatible: `GET <baseURL>/models` with API key header.
4. Persist discovered models into `providers` with keys `openai-compatible.<credential>.<model>`.

### Scheduled refresh
- For each provider instance (unique credential+baseURL), if `now - lastFetchedAt >= 24h`, start a background refresh.
- Refresh writes the new model set (add new, remove stale) and updates `lastFetchedAt`.
- Cache key is provider instance key, not provider kind.

### Model picker
- Populate from configured providers.
- If a provider instance has no discovered models or last discovery failed, show an error row under that provider section (e.g., “Model discovery failed: <message>”).

## Error Handling
- Discovery failures are surfaced in the model picker as a single error row per provider instance.
- No fallback list; do not insert synthetic models.

## Copilot wire headers
Apply the Copilot header set to all Copilot API calls:
- User-Agent: `GitHubCopilotChat/0.32.4`
- Editor-Version: `vscode/1.105.1`
- Editor-Plugin-Version: `copilot-chat/0.32.4`
- Copilot-Integration-Id: `vscode-chat`

## Testing
- Unit tests for discovery parsing and registry updates.
- Picker tests verifying error row appears on discovery failure.
- Copilot tests ensure headers are sent and no fallback usage.

## Risks
- Multiple provider instances require careful cache keying to avoid cross-contamination.
- Asynchronous refresh must not block TUI updates.
