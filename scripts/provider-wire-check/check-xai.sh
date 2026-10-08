#!/usr/bin/env bash
set -euo pipefail

source "$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)/common.sh" "$@"

echo "== Grok / xAI provider wire check =="

xai_go="${REPO_ROOT}/internal/provider/xai/xai.go"
flow_go="${REPO_ROOT}/internal/provider/xai/flow.go"
device_go="${REPO_ROOT}/internal/provider/xai/device.go"
models_go="${REPO_ROOT}/internal/provider/xai/models.go"
auth_go="${REPO_ROOT}/internal/app/auth.go"

require_path "$xai_go" "Conduit xAI provider"
assert_contains "$xai_go" "b1a00492-073a-47ea-816f-4c329264a828" "xAI OAuth client id"
assert_contains "$xai_go" "https://auth.x.ai" "xAI OAuth issuer"
assert_contains "$xai_go" "/oauth2/authorize" "xAI authorize endpoint"
assert_contains "$xai_go" "/oauth2/token" "xAI token endpoint"
assert_contains "$xai_go" "https://api.x.ai/v1" "xAI API base URL"
assert_contains "$xai_go" "grok-cli:access" "xAI shared-client scope"
assert_contains "$xai_go" "offline_access" "xAI refresh-token scope"
assert_contains "$xai_go" "code_challenge_method" "xAI PKCE parameter"
assert_contains "$flow_go" "callbackPath = \"/callback\"" "xAI registered loopback callback path"
assert_contains "$flow_go" "127.0.0.1:0" "xAI OS-assigned loopback port"
assert_contains "$flow_go" "https://accounts.x.ai" "xAI consent origin CORS"
assert_contains "$device_go" "/oauth2/device/code" "xAI device-code endpoint"
assert_contains "$device_go" "urn:ietf:params:oauth:grant-type:device_code" "xAI device grant type"
assert_contains "$models_go" "completion_text_token_price" "xAI language-model filter"
assert_contains "$auth_go" "xaiConfig" "xAI transport config"

if optional_repo "$CRUSH_REPO" "crush"; then
  grok_dir="${CRUSH_REPO}/internal/oauth/grok"
  if [[ -d "$grok_dir" ]]; then
    assert_contains "${grok_dir}/oauth.go" "b1a00492-073a-47ea-816f-4c329264a828" "crush xAI OAuth client id reference"
    assert_contains "${grok_dir}/oauth.go" "https://auth.x.ai" "crush xAI issuer reference"
    assert_contains "${grok_dir}/oauth.go" "grok-cli:access" "crush xAI scope reference"
    assert_contains "${grok_dir}/flow.go" "callbackPath = \"/callback\"" "crush xAI callback path reference"
    assert_contains "${grok_dir}/models.go" "https://api.x.ai/v1" "crush xAI API base reference"
  else
    skip "crush checkout predates Grok OAuth (internal/oauth/grok missing)"
  fi
fi

ok "Grok / xAI provider wire check passed"
