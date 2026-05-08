# wire-check

Wire-fingerprint drift detector for conduit vs upstream Claude Code.

Detects when the installed `claude` binary's wire format (headers, billing block, beta list, OAuth constants, system prompt shape) has drifted from conduit's pinned constants, so you know what needs updating after a Claude Code release.

## Prerequisites

- `claude` binary on PATH (`which claude` must succeed)
- [`bun-demincer`](https://github.com/Icehunter/bun-demincer) cloned at `../bun-demincer` (or set `BUN_DEMINCER_DIR`)
- Node 18+

## Usage

```bash
# From conduit repo root:

# Full run: detect version, decode if new, extract fingerprint, diff vs conduit
make verify-wire

# Re-use an existing decoded-<version>/ without running the decode pipeline again
make verify-wire-fast

# Force re-decode of the current claude version even if already archived
make verify-wire-fresh

# Diff a historical version against conduit (no binary needed)
node scripts/wire-check/run.mjs --against 1.0.126

# Different bun-demincer path
BUN_DEMINCER_DIR=/path/to/bun-demincer make verify-wire
```

## What it checks

| Field | Source | Conduit file |
|---|---|---|
| `version` | `VERSION` in macro object | `cmd/conduit/main.go` `var Version` |
| `cch` (billing block) | `cch=XXXXX;` in billing template | `internal/agent/systemprompt.go` `BillingHeader` |
| `sdk_package_version` | `@anthropic-ai/sdk` version (Stainless) | `internal/api/client.go` `SDKPackageVersion` |
| `anthropic-version` header | String literal in SDK | `internal/api/client.go` `AnthropicVersion` |
| `oauth_client_id` | `client_id` in OAuth config | `internal/auth/config.go` `ClientID` |
| `token_url` | OAuth token endpoint | `internal/auth/config.go` `TokenURL` |
| `claude_ai_authorize_url` | OAuth authorize endpoint | `internal/auth/config.go` `ClaudeAIAuthorizeURL` |
| `oauth_beta_header` | `oauth-YYYY-MM-DD` string | `internal/app/auth.go` `betaHeaders` |
| `stainless_runtime_version` | Node runtime version (dynamic) | `internal/api/client.go` (hardcoded) |
| `beta_registry` | Full CC beta catalog (1394.js) | `internal/app/auth.go` `betaHeaders` |
| `oauth_scopes` | Scope constants | `internal/auth/config.go` `ScopesAll` |
| `tools` | Tool definitions w/ `inputSchema` | `internal/tools/*/` directories |
| `discovered_headers` | Broad scan of `anthropic-*`, `x-anthropic-*`, `x-claude-*` literals | Baseline in `extract.mjs` `KNOWN_HEADERS` |

## Severity levels

- **CHANGED** — values differ; conduit must be updated before the next release
- **NEW** — present upstream, not in conduit; review and add if always-on
- **DIVERGED** — known intentional divergence (not an error)
- **OK** — values match

## Exit codes

- `0` — only OK and DIVERGED rows
- `1` — one or more CHANGED or NEW rows

## History

Each run archives the extracted fingerprint to `history/<version>/wire-fingerprint.json` and records binary metadata (sha256, mtime, path, `claude -v` output) to `history/<version>/source.json`. The `history/index.json` ledger lists all recorded versions in order.

These files are committed to the repo so version-to-version drift is visible in `git log --all -- scripts/wire-check/history/`.

## Adding a new anchor

Open `anchors.json` and add an entry:

```json
"my_field": {
  "description": "What this extracts",
  "anchor": "exact string that must appear in the file",
  "regex": "regex with a capturing group",
  "group": 1,
  "exclude_vendor": true
}
```

The `anchor` is used to narrow which files to scan before applying the regex (performance filter). Test with:

```bash
node --input-type=module << 'EOF'
import path from 'path';
import { runExtract } from './scripts/wire-check/extract.mjs';
const scriptDir = path.resolve('scripts/wire-check');
const fp = runExtract({
  decodedDir: path.resolve('../bun-demincer/decoded'),
  version: 'test',
  historyDir: path.join(scriptDir, 'history'),
  scriptDir,
  outPath: '/dev/null',
});
console.log('my_field:', fp.my_field);
EOF
```

## Known limitations

- **Tool names** are extracted heuristically (`name:` property in files with both `userFacingName` and `inputSchema`). Some internal names may appear; some real tools may be missed. Treat the tool diff as informational, not authoritative.
- **Beta list** comparison is registry-based: the extractor finds all betas CC _defines_, not the exact subset CC _sends_ for a default request (which depends on runtime feature flags). Always review NEW betas before adding — some are feature-gated.
- **Discovered headers** may include headers from CC's internal admin API or bridge code that conduit doesn't need. Review the list and add confirmed-benign entries to `KNOWN_HEADERS` in `extract.mjs`.
