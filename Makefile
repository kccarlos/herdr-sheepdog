MODULE     := github.com/kccarlos/herdr-sheepdog
BINARY     := sheepdog
CMD        := ./cmd/sheepdog

VERSION    := $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
COMMIT     := $(shell git rev-parse --short HEAD 2>/dev/null || echo none)
DATE       := $(shell date -u +%Y-%m-%dT%H:%M:%SZ)

LDFLAGS    := -X 'main.version=$(VERSION)' -X 'main.commit=$(COMMIT)' -X 'main.date=$(DATE)'

.PHONY: build test test-web lint vet cross clean install run-dev changelog eval eval-live release-sign

build:
	go build -ldflags "$(LDFLAGS)" -o bin/$(BINARY) $(CMD)

# Copies the already-built binary to ~/.local/bin/ (expected on PATH).
# Unlinks first: cp over an existing file keeps the inode, and macOS then
# SIGKILLs the new binary because its cached code signature no longer matches.
install: build
	mkdir -p $(HOME)/.local/bin
	rm -f $(HOME)/.local/bin/$(BINARY)
	cp bin/$(BINARY) $(HOME)/.local/bin/$(BINARY)

# A daemon you can poke from /debug without a mic, herdr, or a Gemini key.
run-dev: build
	./bin/$(BINARY) serve --voice=off --herdr=mock --dev

# Regenerates CHANGELOG.md from the full commit history (there are no
# tags yet; see scripts/changelog.sh).
# Signs and notarizes the darwin binary of a published release on this Mac
# and replaces its tarball and checksums.txt (docs/signing.md). The release
# workflow runs on ubuntu only, so the darwin asset is unsigned until then.
#   make release-sign TAG=v0.1.0 [DRY_RUN=1]
release-sign:
	@test -n "$(TAG)" || { echo "usage: make release-sign TAG=v0.1.0 [DRY_RUN=1]"; exit 2; }
	./scripts/release-sign.sh $(if $(DRY_RUN),--dry-run) $(TAG)

changelog:
	{ echo "# Changelog"; echo; ./scripts/changelog.sh; } > CHANGELOG.md

test:
	go test -race ./...

# Browser-side unit tests (web/voice/*.test.mjs, web/debug/*.test.mjs; plain
# node --test, no npm). Skipped when node is not on PATH so `make test`
# stays Go-only.
test-web:
	@if command -v node >/dev/null 2>&1; then \
		node --test 'web/voice/*.test.mjs' 'web/debug/*.test.mjs'; \
	else \
		echo "test-web: node not found, skipping"; \
	fi

lint:
	golangci-lint run

# Conversation eval sets (testdata/eval): offline — the assistant cases
# are only validated, every orchestrator case (svp, regressions, extracted,
# expanded) runs through the orchestrator with a scripted fake model
# (internal/eval), and the phrasing checks are listed as skipped. The L0
# gate of docs/iteration-2.md; see docs/roles.md "The L0 eval corpus".
# `eval sim` runs every simulated-user scenario (testdata/eval/sim) on the
# real supervisor with the scripted orchestrator and user: a harness check of the
# scenario files (the live campaign is `eval sim --live`, docs/iteration-2.md).
# `bench validate` checks the voice bench's scenarios and openings manifest
# (testdata/bench, docs/bench/README.md) without any model.
eval: build
	./bin/$(BINARY) eval --set all
	./bin/$(BINARY) eval sim
	./bin/$(BINARY) bench validate

# Same, but calls the real model (gemini-3.5-flash, plain text, never
# Live audio): needs a Gemini key (GEMINI_API_KEY or --env-file) and
# burns real quota (the free tier's, if that's what's configured) — run
# it sparingly, it never hot-retries on a 429. Only the two small
# hand-written sets: the corpus is hundreds of cases (docs/iteration-2.md
# rule 6: live runs ≤ 30 cases, when the orchestrator says so; use
# --set regressions or --filter for more).
eval-live: build
	./bin/$(BINARY) eval --set assistant --live --env-file .env
	./bin/$(BINARY) eval --set svp --live --env-file .env

vet:
	go vet ./...

cross:
	mkdir -p dist
	CGO_ENABLED=0 GOOS=darwin GOARCH=arm64 go build -ldflags "$(LDFLAGS)" -o dist/$(BINARY)-darwin-arm64 $(CMD)
	CGO_ENABLED=0 GOOS=linux  GOARCH=amd64 go build -ldflags "$(LDFLAGS)" -o dist/$(BINARY)-linux-amd64  $(CMD)

clean:
	rm -rf bin dist
