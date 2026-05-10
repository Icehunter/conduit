---
paths: "**/*.go"
---

# Using Claude Code as a Behavioral Reference

Conduit maintains wire compatibility with Claude Code (Anthropic OAuth, API headers, billing block, request shape, plugin format) but is free to diverge in behavior, features, and architecture. The CC TypeScript source and decoded v2.1.126 bundle are **references**, not mandates.

## When to Consult CC Source

- You need to understand what a feature is supposed to do
- You're unsure whether behavior is intentional or accidental in CC
- You want to match CC's observable wire format exactly

## Source Priority (for reference purposes)

1. **Decoded bundle** (see README.md for path) — actual v2.1.126 runtime behavior
2. **TS source** (see README.md for path) — naming, intent, structure
3. **PARITY.md** — existing Go mapping decisions and recorded divergences

## How to Implement a Feature

1. Check `STATUS.md` — may already exist or be intentionally stubbed
2. Consult CC source if the feature has CC precedent; understand the intent
3. Implement in idiomatic Go — you don't need to match CC's exact approach
4. If you diverge intentionally on wire/auth behavior, note it in `COMPATIBILITY.md`
5. Write tests, update `STATUS.md`

## COMPATIBILITY.md Update Format

Add a row to the "Intentional divergences" table:

```markdown
| Area | CC behavior | Conduit behavior | Why |
```

For wire sync notes (new CC version), add a section:

```markdown
### X.Y.Z → X.Y.Z+1 (date)
| Item | Action |
```

## STATUS.md Update Format

Find the right product-area section and update the row:

```markdown
| Feature name | ✅ | package path and one-line note |
```

## Common Go Translation Notes

- **Node stream semantics** differ from Go's `io.Reader` — SSE chunks can span multiple reads
- **`setTimeout(f, 0)` in TS** → `tea.Tick(0, ...)` or just a synchronous command in Go
- **Class methods** become struct methods — if the TS class has shared state, put it on the struct
- **TypeScript union types** → Go interface or sum-type pattern (tagged struct + const iota)
- **Use standard JSON** — this repo uses Go's `encoding/json`; don't churn to an alternate without a benchmark
