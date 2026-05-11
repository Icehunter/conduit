#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd -- "${SCRIPT_DIR}/../.." && pwd)"

STRICT=0
OPENCODE_REPO="${OPENCODE_REPO:-${REPO_ROOT}/../opencode}"
CRUSH_REPO="${CRUSH_REPO:-${REPO_ROOT}/../crush}"
CODEX_REPO="${CODEX_REPO:-${REPO_ROOT}/../codex}"

while (($#)); do
  case "$1" in
    --strict)
      STRICT=1
      ;;
    --opencode)
      shift
      OPENCODE_REPO="${1:?missing value for --opencode}"
      ;;
    --crush)
      shift
      CRUSH_REPO="${1:?missing value for --crush}"
      ;;
    --codex)
      shift
      CODEX_REPO="${1:?missing value for --codex}"
      ;;
    --repo-root)
      shift
      REPO_ROOT="${1:?missing value for --repo-root}"
      ;;
    *)
      echo "unknown argument: $1" >&2
      exit 2
      ;;
  esac
  shift
done

fail() {
  echo "✗ $*" >&2
  exit 1
}

ok() {
  echo "✓ $*"
}

skip() {
  echo "↷ $*"
}

require_path() {
  local path="$1"
  local label="$2"
  [[ -e "$path" ]] || fail "$label missing: $path"
  ok "$label present"
}

optional_repo() {
  local path="$1"
  local label="$2"
  if [[ -d "$path" ]]; then
    ok "$label reference present: $path"
    return 0
  fi
  if [[ "$STRICT" == "1" ]]; then
    fail "$label reference missing in strict mode: $path"
  fi
  skip "$label reference missing; set ${label^^}_REPO or pass --strict to require it"
  return 1
}

assert_contains() {
  local path="$1"
  local needle="$2"
  local label="$3"
  [[ -e "$path" ]] || fail "$label missing file: $path"
  grep -Fq -- "$needle" "$path" || fail "$label missing expected text: $needle"
  ok "$label"
}

assert_tree_contains() {
  local path="$1"
  local needle="$2"
  local label="$3"
  [[ -e "$path" ]] || fail "$label missing path: $path"
  if [[ -d "$path" ]]; then
    grep -RFq -- "$needle" "$path" || fail "$label missing expected text: $needle"
  else
    grep -Fq -- "$needle" "$path" || fail "$label missing expected text: $needle"
  fi
  ok "$label"
}
