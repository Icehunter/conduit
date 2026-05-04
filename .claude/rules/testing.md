---
paths: "**/*_test.go"
---

# Testing Standards

## Pattern

Use table-driven tests for any function with more than one meaningful scenario:

```go
func TestFoo(t *testing.T) {
    tests := []struct {
        name    string
        input   string
        want    string
        wantErr bool
    }{
        {"happy path", "valid", "result", false},
        {"empty input", "", "", true},
    }
    for _, tt := range tests {
        t.Run(tt.name, func(t *testing.T) {
            got, err := Foo(tt.input)
            if (err != nil) != tt.wantErr {
                t.Errorf("Foo() error = %v, wantErr %v", err, tt.wantErr)
            }
            if got != tt.want {
                t.Errorf("Foo() = %v, want %v", got, tt.want)
            }
        })
    }
}
```

## Rules

- **No `time.Sleep`** — use channels or `sync.WaitGroup` to synchronize
- **Race detector always** — tests that exercise goroutines must pass `go test -race`
- **Test all error paths** — not just the happy path; test nil inputs, empty slices, context cancellation
- **SSE / API fixtures** — use recorded SSE byte sequences in `testdata/` rather than live HTTP calls
- **Fuzz targets** — parsers (SSE, JSON-RPC, OAuth callback URL) need a `FuzzXxx(f *testing.F)` target

## What NOT to mock

- Standard library (`os`, `io`, `strings`, `bytes`) — use real implementations
- In-memory stores — use real structs with test data

## What TO mock

- HTTP clients — use `httptest.Server` or interface substitutions
- External processes — inject command via interface if the path matters
- Keychain / secure storage — the `secure.Storage` interface exists for this
