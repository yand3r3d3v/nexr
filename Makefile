# Development tasks for nexr. Run "make help" for the list.

GO            ?= go
BIN           ?= bin/nexr
MODULE        := github.com/yand3r3d3v/nexr
VERSION       ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
COMMIT        ?= $(shell git rev-parse HEAD 2>/dev/null)
DATE          ?= $(shell git log -1 --format=%cI 2>/dev/null)
LDFLAGS       := -s -w \
                 -X $(MODULE)/internal/buildinfo.Version=$(VERSION) \
                 -X $(MODULE)/internal/buildinfo.Commit=$(COMMIT) \
                 -X $(MODULE)/internal/buildinfo.Date=$(DATE)
NEXUS_VERSION ?= 3.96.3

.DEFAULT_GOAL := help
.PHONY: help build test test-race lint cover e2e e2e-down snapshot tidy clean

help: ## Show the available targets
	@awk 'BEGIN {FS = ":.*## "} /^[a-z0-9-]+:.*## / {printf "  %-10s %s\n", $$1, $$2}' $(MAKEFILE_LIST)

build: ## Build bin/nexr for this platform
	CGO_ENABLED=0 $(GO) build -trimpath -ldflags '$(LDFLAGS)' -o $(BIN) ./cmd/nexr

test: ## Run the tests
	$(GO) test ./...

test-race: ## Run the tests with the race detector
	$(GO) test -race ./...

lint: ## Check formatting and run go vet and golangci-lint
	@out=$$(gofmt -l .); if [ -n "$$out" ]; then echo "files need gofmt:"; echo "$$out"; exit 1; fi
	$(GO) vet ./...
	golangci-lint run

cover: ## Write a coverage report to coverage.html
	$(GO) test -coverpkg=./internal/... -coverprofile=coverage.out ./internal/...
	$(GO) tool cover -func=coverage.out | tail -n 1
	$(GO) tool cover -html=coverage.out -o coverage.html

e2e: build ## Run the end-to-end tests against Nexus in Docker (NEXUS_VERSION=3.96.3)
	@eval "$$(scripts/e2e-nexus.sh start $(NEXUS_VERSION))" && \
		NEXR_E2E_BINARY="$(CURDIR)/$(BIN)" $(GO) test -tags e2e -count=1 ./test/e2e/...

e2e-down: ## Remove the Nexus container of the end-to-end tests
	scripts/e2e-nexus.sh stop $(NEXUS_VERSION)

snapshot: ## Cross-compile all release targets into dist/
	goreleaser release --snapshot --clean

tidy: ## Tidy go.mod and go.sum
	$(GO) mod tidy

clean: ## Remove build and test output
	rm -rf bin dist coverage.out coverage.html
