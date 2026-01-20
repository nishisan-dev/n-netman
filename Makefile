.PHONY: all build test test-coverage lint clean install

# Build variables
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo "dev")
COMMIT ?= $(shell git rev-parse --short HEAD 2>/dev/null || echo "unknown")
BUILD_DATE ?= $(shell date -u +"%Y-%m-%dT%H:%M:%SZ")
LDFLAGS := -ldflags "-X main.version=$(VERSION) -X main.commit=$(COMMIT) -X main.buildDate=$(BUILD_DATE)"

# Go settings
GOBIN ?= $(shell go env GOPATH)/bin

all: build

## build: Build daemon and CLI binaries
build:
	go build $(LDFLAGS) -o bin/nnetd ./cmd/nnetd
	go build $(LDFLAGS) -o bin/nnet ./cmd/nnet

## test: Run unit tests
test:
	go test -v -race ./...

## test-coverage: Run tests with coverage report
test-coverage:
	go test -v -race -coverprofile=coverage.out ./...
	go tool cover -html=coverage.out -o coverage.html
	@echo "Coverage report: coverage.html"

## test-integration: Run integration tests (requires root)
test-integration:
	@if [ "$$(id -u)" != "0" ]; then echo "Integration tests require root"; exit 1; fi
	go test -v -tags=integration ./...

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
	protoc --go_out=. --go-grpc_out=. api/v1/nnetman.proto

## install: Install binaries to GOBIN
install: build
	cp bin/nnetd $(GOBIN)/
	cp bin/nnet $(GOBIN)/

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
