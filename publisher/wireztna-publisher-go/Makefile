# WireZTNA Publisher — Build targets

BINARY_NAME = wireztna-publisher
VERSION ?= $(shell cat VERSION 2>/dev/null || echo "dev")
COMMIT  ?= $(shell git rev-parse --short HEAD 2>/dev/null || echo "unknown")
DATE    ?= $(shell date -u '+%Y-%m-%dT%H:%M:%SZ')

LDFLAGS = -s -w \
	-X github.com/wireztna/publisher/pkg/version.Version=$(VERSION) \
	-X github.com/wireztna/publisher/pkg/version.Commit=$(COMMIT) \
	-X github.com/wireztna/publisher/pkg/version.BuildDate=$(DATE)

.PHONY: build build-linux build-all clean test help

## build: Build for current OS/arch
build:
	go build -ldflags "$(LDFLAGS)" -o bin/$(BINARY_NAME) .

## build-linux: Cross-compile static binary for linux/amd64
build-linux:
	CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -ldflags "$(LDFLAGS)" -o bin/$(BINARY_NAME)-linux-amd64 .

## build-all: Cross-compile for linux amd64 + arm64
build-all:
	CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -ldflags "$(LDFLAGS)" -o bin/$(BINARY_NAME)-linux-amd64 .
	CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build -ldflags "$(LDFLAGS)" -o bin/$(BINARY_NAME)-linux-arm64 .

## clean: Remove build artifacts
clean:
	rm -rf bin/

## test: Run tests
test:
	go test -v ./...

## help: Show this help
help:
	@grep -E '^## ' Makefile | sed 's/## //' | column -t -s ':'
