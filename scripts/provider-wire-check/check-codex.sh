#!/usr/bin/env bash
set -euo pipefail

source "$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)/common.sh" "$@"

echo "== ChatGPT / Codex provider wire check =="

codex_go="${REPO_ROOT}/internal/provider/codex/codex.go"
codex_test="${REPO_ROOT}/internal/provider/codex/codex_test.go"
auth_go="${REPO_ROOT}/internal/app/auth.go"
responses_go="${REPO_ROOT}/internal/api/openairesponses.go"
compat_go="${REPO_ROOT}/internal/api/openaicompat.go"
stream_test="${REPO_ROOT}/internal/api/stream_test.go"

require_path "$codex_go" "Conduit Codex provider"
assert_contains "$codex_go" "app_EMoamEEZ73f0CkXaXp7hrann" "Codex OAuth client id"
assert_contains "$codex_go" "https://auth.openai.com" "Codex OAuth issuer"
assert_contains "$codex_go" "https://chatgpt.com/backend-api/codex" "Codex backend base URL"
assert_contains "$codex_go" "OAuthPort    = 1455" "Codex fixed OAuth callback port"
assert_contains "$codex_go" "codex_cli_simplified_flow" "Codex auth simplified-flow parameter"
assert_contains "$codex_go" "id_token_add_organizations" "Codex auth organization-token parameter"
assert_contains "$codex_go" "originator\", \"opencode" "Codex auth originator reference"
assert_contains "$codex_go" "ChatGPT-Account-Id" "Codex account header"
assert_contains "$codex_test" "originator = %q, want opencode" "Codex OAuth originator regression test"
assert_contains "$auth_go" "OpenAIResponsesSystemAsInstructions" "Codex system-as-instructions transport config"
assert_contains "$auth_go" "OpenAIResponsesOmitMaxOutputTokens" "Codex max_output_tokens omission config"
assert_contains "$auth_go" "OpenAIResponsesStore" "Codex store=false transport config"
assert_contains "$responses_go" "Instructions    string" "Responses instructions field"
assert_contains "$responses_go" "contentType != \"\"" "Responses missing content-type tolerance"
assert_contains "$compat_go" "normalizeOpenAIToolSchema" "OpenAI tool schema normalizer"
assert_contains "$compat_go" "usage_not_included" "ChatGPT Plus/Pro plan-gating error normalization"
assert_contains "$stream_test" "TestStreamMessage_OpenAIResponsesCodexShape" "Codex Responses request-shape regression test"
assert_contains "$stream_test" "TestStreamMessage_OpenAIResponsesAllowsMissingContentType" "Codex missing content-type SSE regression test"

if optional_repo "$OPENCODE_REPO" "opencode"; then
  codex_ts="${OPENCODE_REPO}/packages/opencode/src/plugin/codex.ts"
  openai_options="${OPENCODE_REPO}/packages/llm/src/providers/openai-options.ts"
  require_path "$codex_ts" "OpenCode Codex plugin"
  assert_contains "$codex_ts" "app_EMoamEEZ73f0CkXaXp7hrann" "OpenCode Codex OAuth client id reference"
  assert_contains "$codex_ts" "CODEX_API_ENDPOINT = \"https://chatgpt.com/backend-api/codex/responses\"" "OpenCode Codex endpoint reference"
  assert_contains "$codex_ts" "OAUTH_PORT = 1455" "OpenCode Codex fixed callback port reference"
  assert_contains "$codex_ts" "originator: \"opencode\"" "OpenCode Codex auth originator reference"
  assert_contains "$codex_ts" "ChatGPT-Account-Id" "OpenCode Codex account header reference"
  [[ -e "$openai_options" ]] && assert_contains "$openai_options" "store: false" "OpenCode Responses store=false option reference"
fi

if [[ -d "$CODEX_REPO" ]]; then
  ok "codex reference present: $CODEX_REPO"
  grep -RFiq -- "chatgpt.com/backend-api/codex" "$CODEX_REPO" 2>/dev/null || fail "codex checkout present but no Codex backend references were found"
  ok "codex checkout contains Codex backend reference material"
else
  skip "codex reference missing; optional because OpenCode is the active local reference"
fi

ok "ChatGPT / Codex provider wire check passed"
