MODULE     := github.com/kccarlos/herdr-sheepdog
BINARY     := sheepdog
CMD        := ./cmd/sheepdog

VERSION    := $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
COMMIT     := $(shell git rev-parse --short HEAD 2>/dev/null || echo none)
DATE       := $(shell date -u +%Y-%m-%dT%H:%M:%SZ)

LDFLAGS    := -X 'main.version=$(VERSION)' -X 'main.commit=$(COMMIT)' -X 'main.date=$(DATE)'

.PHONY: build test test-web lint vet cross clean

build:
	go build -ldflags "$(LDFLAGS)" -o bin/$(BINARY) $(CMD)

test:
	go test -race ./...

# Browser-side unit tests (web/voice/*.test.mjs, plain node --test; no
# npm). Skipped when node is not on PATH so `make test` stays Go-only.
test-web:
	@if command -v node >/dev/null 2>&1; then \
		node --test 'web/voice/*.test.mjs'; \
	else \
		echo "test-web: node not found, skipping"; \
	fi

lint:
	golangci-lint run

vet:
	go vet ./...

cross:
	mkdir -p dist
	CGO_ENABLED=0 GOOS=darwin GOARCH=arm64 go build -ldflags "$(LDFLAGS)" -o dist/$(BINARY)-darwin-arm64 $(CMD)
	CGO_ENABLED=0 GOOS=linux  GOARCH=amd64 go build -ldflags "$(LDFLAGS)" -o dist/$(BINARY)-linux-amd64  $(CMD)

clean:
	rm -rf bin dist
