#!/usr/bin/env bash
set -euo pipefail

source "$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)/common.sh" "$@"

echo "== GitHub Copilot provider wire check =="

copilot_go="${REPO_ROOT}/internal/provider/copilot/copilot.go"
auth_go="${REPO_ROOT}/internal/app/auth.go"
copilot_test="${REPO_ROOT}/internal/provider/copilot/copilot_test.go"

require_path "$copilot_go" "Conduit Copilot provider"
assert_contains "$copilot_go" "Iv1.b507a08c87ecfe98" "GitHub OAuth client id"
assert_contains "$copilot_go" "https://github.com/login/device/code" "GitHub device-code endpoint"
assert_contains "$copilot_go" "https://github.com/login/oauth/access_token" "GitHub token endpoint"
assert_contains "$copilot_go" "https://api.github.com/copilot_internal/v2/token" "Copilot token endpoint"
assert_contains "$copilot_go" "https://api.githubcopilot.com/models" "Copilot model discovery endpoint"
assert_contains "$copilot_go" "Editor-Version" "Copilot runtime editor header"
assert_contains "$copilot_go" "Copilot-Integration-Id" "Copilot integration header"
assert_contains "$copilot_go" "OpenAI-Intent" "Copilot intent header"
assert_contains "$copilot_go" "x-initiator" "Copilot initiator header"
assert_contains "$auth_go" "copilot.ShouldUseResponsesAPI" "Copilot responses route selection"
assert_contains "$auth_go" "copilot.UsesMessagesAPI" "Copilot Claude messages route selection"
assert_contains "$auth_go" "StripCacheControlScope" "Copilot Claude cache-control normalization"
assert_contains "$copilot_test" "gpt-5-mini" "Copilot route regression test covers gpt-5-mini exception"

if optional_repo "$OPENCODE_REPO" "opencode"; then
  sdk_dir="${OPENCODE_REPO}/packages/opencode/src/provider/sdk/copilot"
  plugin_auth="${OPENCODE_REPO}/packages/opencode/src/plugin/github-copilot/copilot.ts"
  plugin_models="${OPENCODE_REPO}/packages/opencode/src/plugin/github-copilot/models.ts"
  llm_provider="${OPENCODE_REPO}/packages/llm/src/providers/github-copilot.ts"
  require_path "$sdk_dir" "OpenCode Copilot SDK"
  require_path "$plugin_auth" "OpenCode Copilot auth plugin"
  assert_contains "$plugin_auth" "https://api.githubcopilot.com" "OpenCode Copilot API base reference"
  assert_contains "$plugin_auth" "x-initiator" "OpenCode Copilot initiator header reference"
  [[ -e "$plugin_models" ]] && assert_contains "$plugin_models" "github-copilot" "OpenCode Copilot model plugin reference"
  [[ -e "$plugin_models" ]] && assert_contains "$plugin_models" "supported_endpoints" "OpenCode Copilot endpoint metadata reference"
  [[ -e "$llm_provider" ]] && assert_contains "$llm_provider" "github-copilot" "OpenCode Copilot LLM provider reference"
fi

if [[ -d "$CRUSH_REPO" ]]; then
  ok "crush reference present: $CRUSH_REPO"
  if grep -RFiq -- "copilot" "$CRUSH_REPO" 2>/dev/null; then
    ok "crush contains Copilot reference material"
  else
    fail "crush checkout present but no Copilot references were found"
  fi
elif [[ "$STRICT" == "1" ]]; then
  fail "crush reference missing in strict mode: $CRUSH_REPO"
else
  skip "crush reference missing; optional for default Copilot check"
fi

ok "GitHub Copilot provider wire check passed"
