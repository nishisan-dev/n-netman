.PHONY: all build test test-coverage test-integration test-integration-netns lint clean install

# Build variables
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo "dev")
COMMIT ?= $(shell git rev-parse --short HEAD 2>/dev/null || echo "unknown")
BUILD_DATE ?= $(shell date -u +"%Y-%m-%dT%H:%M:%SZ")
LDFLAGS := -ldflags "-X main.version=$(VERSION) -X main.commit=$(COMMIT) -X main.buildDate=$(BUILD_DATE)"

# Go settings
GOBIN ?= $(shell go env GOPATH)/bin

all: build

## build: Build daemon, CLI and VM agent binaries
build:
	go build $(LDFLAGS) -o bin/nnetd ./cmd/nnetd
	go build $(LDFLAGS) -o bin/nnet ./cmd/nnet
	go build $(LDFLAGS) -o bin/nnet-agent ./cmd/nnet-agent

## test: Run unit tests
test:
	go test -v -race ./...

## test-coverage: Run tests with coverage report
test-coverage:
	go test -v -race -coverprofile=coverage.out ./...
	go tool cover -html=coverage.out -o coverage.html
	@echo "Coverage report: coverage.html"

## test-integration: Run integration tests (needs CAP_NET_ADMIN, i.e. root)
test-integration:
	@if [ "$$(id -u)" != "0" ]; then \
		echo "Integration tests need CAP_NET_ADMIN."; \
		echo "Run as root, or without root via: make test-integration-netns"; \
		exit 1; \
	fi
	go test -v -race -tags=integration ./...

## test-integration-netns: Run integration tests without root, in a throwaway
## network namespace. The binary is compiled outside the namespace because the
## Go toolchain may not be reachable inside it (e.g. a snap install).
test-integration-netns:
	@mkdir -p bin
	go test -c -race -tags=integration -o bin/agent.integration.test ./internal/agent
	unshare --net --user --map-root-user sh -c 'ip link set lo up; ./bin/agent.integration.test -test.v -test.timeout 180s'

## lint: Run linters
lint:
	@which golangci-lint > /dev/null || (echo "Installing golangci-lint..." && go install github.com/golangci/golangci-lint/cmd/golangci-lint@latest)
	golangci-lint run ./...

## fmt: Format code
fmt:
	go fmt ./...
	goimports -w .

## tidy: Tidy go.mod
tidy:
	go mod tidy

## proto: Generate protobuf code
proto:
	protoc --go_out=. --go_opt=paths=source_relative \
		--go-grpc_out=. --go-grpc_opt=paths=source_relative \
		api/v1/nnetman.proto
	protoc --go_out=. --go_opt=paths=source_relative \
		api/inject/v1/inject.proto

## install: Install binaries to GOBIN
install: build
	cp bin/nnetd $(GOBIN)/
	cp bin/nnet $(GOBIN)/
	cp bin/nnet-agent $(GOBIN)/

## clean: Remove build artifacts
clean:
	rm -rf bin/
	rm -f coverage.out coverage.html

## help: Show this help
help:
	@echo "n-netman Makefile"
	@echo ""
	@echo "Usage: make [target]"
	@echo ""
	@echo "Targets:"
	@grep -E '^## ' Makefile | sed 's/## /  /'
