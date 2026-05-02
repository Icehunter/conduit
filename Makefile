.PHONY: build test test-race lint vet fuzz tidy clean

BIN := bin/claude
PKG := ./...

build:
	@mkdir -p bin
	go build -trimpath -ldflags="-s -w" -o $(BIN) ./cmd/claude

test:
	go test $(PKG)

test-race:
	go test -race -count=1 $(PKG)

lint:
	golangci-lint run

vet:
	go vet $(PKG)

fuzz:
	@echo "Run individual fuzz targets via: go test -run=^$$ -fuzz=Fuzz<Name> -fuzztime=1m ./<pkg>"

tidy:
	go mod tidy

clean:
	rm -rf bin dist coverage.out coverage.html
