.PHONY: all build test test-race lint vet fuzz tidy clean tools fmt fmt-check verify help

BIN    := bin/conduit
PKG    := ./...
GO     := go

VERSION    ?= $(shell cat VERSION 2>/dev/null || git describe --tags --always --dirty 2>/dev/null || echo "dev")
GIT_COMMIT ?= $(shell git rev-parse --short HEAD 2>/dev/null || echo "unknown")
BUILD_TIME ?= $(shell date -u '+%Y-%m-%dT%H:%M:%SZ')
LDFLAGS    := -ldflags "-s -w -X main.AppVersion=$(VERSION) -X main.GitCommit=$(GIT_COMMIT) -X main.BuildTime=$(BUILD_TIME)"

all: verify build

# Build the binary.
build:
	@mkdir -p bin
	$(GO) build -trimpath $(LDFLAGS) -o $(BIN) ./cmd/conduit
	install -m 0755 $(BIN) ./conduit

# Run all tests.
test:
	$(GO) test $(PKG)

# Run tests with the race detector.
test-race:
	$(GO) test -race -count=1 $(PKG)

# Run tests with HTML coverage report.
test-cover:
	$(GO) test -coverprofile=coverage.out $(PKG)
	$(GO) tool cover -html=coverage.out -o coverage.html
	@echo "Coverage report: coverage.html"

# Lint using the version pinned in go.mod — no global install required.
lint:
	$(GO) tool github.com/golangci/golangci-lint/v2/cmd/golangci-lint run

# Run go vet.
vet:
	$(GO) vet $(PKG)

# Format all Go files.
fmt:
	$(GO) fmt $(PKG)
	gofmt -s -w .

# Check formatting without modifying files (used by verify).
fmt-check:
	@test -z "$$(gofmt -l .)" || (echo "Unformatted files — run: make fmt" && exit 1)

# Full verification: fmt + vet + lint + tests.
verify:
	@$(MAKE) fmt-check
	@$(MAKE) vet
	@$(MAKE) lint
	@$(MAKE) test
	@echo "All checks passed."

# Pre-cache pinned dev tools (go tool caches on first use; this warms it).
tools:
	$(GO) tool github.com/golangci/golangci-lint/v2/cmd/golangci-lint --version

# Tidy go.mod and go.sum.
tidy:
	$(GO) mod tidy

# Run fuzz targets — see individual packages for target names.
fuzz:
	@echo "Run: go test -run=^$$ -fuzz=Fuzz<Name> -fuzztime=1m ./<pkg>"

# Remove build artefacts.
clean:
	rm -rf bin dist coverage.out coverage.html

help:
	@echo "Targets:"
	@echo "  all          verify + build (default)"
	@echo "  build        Build ./conduit binary"
	@echo "  test         go test ./..."
	@echo "  test-race    go test -race ./..."
	@echo "  test-cover   Tests + HTML coverage report"
	@echo "  lint         golangci-lint (pinned in go.mod via 'go tool')"
	@echo "  vet          go vet"
	@echo "  fmt          Format all Go files"
	@echo "  verify       fmt-check + vet + lint + test"
	@echo "  tools        Pre-cache pinned dev tools"
	@echo "  tidy         go mod tidy"
	@echo "  fuzz         Print fuzz usage hint"
	@echo "  clean        Remove bin/ and coverage files"
