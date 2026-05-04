---
paths: "**/*.go"
---

# Error Handling

## Wrapping

Always wrap errors with context using `%w`:

```go
if err := doThing(); err != nil {
    return fmt.Errorf("component: operation: %w", err)
}
```

Prefix with `"<package>: <what failed>: "` so the full chain reads left-to-right.

## No Silent Swallows

Never discard errors silently with `_` unless the function is explicitly fire-and-forget (documented):

```go
// bad
_, _ = fmt.Fprintf(w, "...")  // hiding a real write failure

// ok — intentional, documented
_ = os.Remove(tmpFile)  // best-effort cleanup; failure is non-fatal
```

## Context Cancellation

Always check `ctx.Err()` after operations that can be cancelled:

```go
if ctx.Err() != nil {
    return tool.ErrorResult("cancelled"), nil
}
```

## Tool Results vs Go Errors

Tools return `(tool.Result, error)` where:
- `error` — means the tool layer itself broke (should never happen in normal flow)
- `tool.Result{IsError: true}` — means the tool ran but the operation failed (normal; model sees this)

Don't return a Go `error` when the operation just failed — encode it in `tool.ErrorResult(...)`.

## Logging

Use `zerolog` structured fields — no `fmt.Println` or `log.Printf` in production code:

```go
log.Error().Err(err).Str("path", path).Msg("failed to read file")
```
