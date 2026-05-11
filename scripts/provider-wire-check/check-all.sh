#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"

include_claude=0
pass_args=()

while (($#)); do
  case "$1" in
    --include-claude)
      include_claude=1
      ;;
    *)
      pass_args+=("$1")
      ;;
  esac
  shift
done

if ((${#pass_args[@]})); then
  "${SCRIPT_DIR}/check-copilot.sh" "${pass_args[@]}"
  "${SCRIPT_DIR}/check-codex.sh" "${pass_args[@]}"
else
  "${SCRIPT_DIR}/check-copilot.sh"
  "${SCRIPT_DIR}/check-codex.sh"
fi

if [[ "$include_claude" == "1" || "${RUN_CLAUDE_WIRE:-0}" == "1" ]]; then
  "${SCRIPT_DIR}/check-claude.sh"
else
  echo "↷ Claude wire check skipped here; run make wire-claude or make wire-all"
fi
