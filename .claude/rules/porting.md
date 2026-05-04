---
paths: "**/*.go"
---

# Porting from TypeScript / Decoded Bundle

## Source of Truth Priority

1. **Decoded bundle** (see README.md for path) — actual v2.1.126 behavior wins on conflicts
2. **TS source** (see README.md for path) — naming, intent, structure
3. **PARITY.md** — existing Go mapping decisions

## How to Port a Feature

1. Find the TS source file by name (see README.md for location)
2. Cross-reference the decoded bundle chunk(s) noted in PARITY.md
3. Understand the behavior from both — decoded wins where they differ
4. Write the failing Go test first (captures the expected behavior)
5. Implement minimally to make the test pass
6. Update PARITY.md with the TS file path → Go package mapping
7. Update STATUS.md to mark the feature ✅ or 🔶

## PARITY.md Update Format

```markdown
| `src/tools/BashTool/BashTool.ts` | `internal/tools/bashtool/` | ✅ |
```

## STATUS.md Update Format

```markdown
| BashTool | `internal/tools/bashtool/` | ✅ Complete |
```

## Common Gotchas

- **`encoding/json` is banned** — the TS source uses standard JSON; Go uses `github.com/goccy/go-json` (same API, depguard enforces this)
- **Node stream semantics** differ from Go's `io.Reader` — SSE chunks can span multiple reads
- **`setTimeout(f, 0)` in TS** → `tea.Tick(0, ...)` or just a synchronous command in Go
- **Class methods** become struct methods — if the TS class has shared state, put it on the struct
- **TypeScript union types** → Go interface or sum-type pattern (tagged struct + const iota)
