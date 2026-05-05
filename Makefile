.PHONY: all build install test test-race lint vet fuzz tidy clean tools fmt fmt-check verify \
        version version-patch version-minor version-major help

BIN    := bin/conduit
PKG    := ./...
GO     := go
PREFIX ?= /usr/local

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

# Install the binary system-wide.
install: build
	install -d $(DESTDIR)$(PREFIX)/bin
	install -m 0755 $(BIN) $(DESTDIR)$(PREFIX)/bin/conduit

# Run all tests.
test:
	$(GO) test $(PKG)

# Run tests with the race detector.
test-race:
	$(GO) test -race -count=1 $(PKG)

# Run tests with HTML coverage report.
test-cover:
	$(GO) test -race -count=1 -coverprofile=coverage.out $(PKG)
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

# Full verification: fmt + vet + lint + race tests.
verify:
	@$(MAKE) fmt-check
	@$(MAKE) vet
	@$(MAKE) lint
	@$(MAKE) test-race
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

# Print current version.
version:
	@echo $(VERSION)

# Bump patch version (1.0.0 → 1.0.1), commit, tag, and push — triggers release workflow.
version-patch:
	@V=$$(cat VERSION); \
	MAJOR=$$(echo $$V | cut -d. -f1); \
	MINOR=$$(echo $$V | cut -d. -f2); \
	PATCH=$$(echo $$V | cut -d. -f3); \
	NEW="$$MAJOR.$$MINOR.$$((PATCH + 1))"; \
	echo $$NEW > VERSION; \
	git add VERSION && git commit -m "chore: bump version to $$NEW" && git tag "v$$NEW"; \
	git push && git push origin "v$$NEW"; \
	echo "Bumped $$V -> $$NEW (tagged and pushed v$$NEW)"

# Bump minor version (1.0.0 → 1.1.0), commit, tag, and push — triggers release workflow.
version-minor:
	@V=$$(cat VERSION); \
	MAJOR=$$(echo $$V | cut -d. -f1); \
	MINOR=$$(echo $$V | cut -d. -f2); \
	NEW="$$MAJOR.$$((MINOR + 1)).0"; \
	echo $$NEW > VERSION; \
	git add VERSION && git commit -m "chore: bump version to $$NEW" && git tag "v$$NEW"; \
	git push && git push origin "v$$NEW"; \
	echo "Bumped $$V -> $$NEW (tagged and pushed v$$NEW)"

# Bump major version (1.0.0 → 2.0.0), commit, tag, and push — triggers release workflow.
version-major:
	@V=$$(cat VERSION); \
	MAJOR=$$(echo $$V | cut -d. -f1); \
	NEW="$$((MAJOR + 1)).0.0"; \
	echo $$NEW > VERSION; \
	git add VERSION && git commit -m "chore: bump version to $$NEW" && git tag "v$$NEW"; \
	git push && git push origin "v$$NEW"; \
	echo "Bumped $$V -> $$NEW (tagged and pushed v$$NEW)"

help:
	@echo "Targets:"
	@echo "  all              verify + build (default)"
	@echo "  build            Build ./bin/conduit (and ./conduit at repo root)"
	@echo "  install          Build + install to PREFIX/bin (default: /usr/local/bin)"
	@echo "  test             go test ./..."
	@echo "  test-race        go test -race ./..."
	@echo "  test-cover       Tests + HTML coverage report"
	@echo "  lint             golangci-lint (pinned in go.mod via 'go tool')"
	@echo "  vet              go vet"
	@echo "  fmt              Format all Go files"
	@echo "  verify           fmt-check + vet + lint + test-race"
	@echo "  tools            Pre-cache pinned dev tools"
	@echo "  tidy             go mod tidy"
	@echo "  fuzz             Print fuzz usage hint"
	@echo "  clean            Remove bin/ and coverage files"
	@echo "  version          Print current version"
	@echo "  version-patch    Bump patch (1.0.0 → 1.0.1), tag, push → triggers release"
	@echo "  version-minor    Bump minor (1.0.0 → 1.1.0), tag, push → triggers release"
	@echo "  version-major    Bump major (1.0.0 → 2.0.0), tag, push → triggers release"
