#!/bin/bash
# Session start — remind Claude of the mandatory workflow for conduit

cat <<'EOF'
conduit is a 1:1 Go port of Claude Code v2.1.126.

Before writing any code:
1. Check STATUS.md — many features are intentional stubs. Don't "fix" them.
2. Check PARITY.md — for the authoritative TS→Go mapping.
3. Run `make verify` (fmt-check + vet + lint + test) before marking anything done.

Key rules:
- Fidelity over cleverness — match TS behavior exactly
- RTK filtering is in-process (internal/rtk/) — don't shell out to rtk binary
- Update STATUS.md when you complete or discover a stub
- make verify must pass with zero lint errors and race-clean tests
EOF
