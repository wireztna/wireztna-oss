VERSION ?= $(shell cat VERSION 2>/dev/null || echo "dev")
COMMIT  ?= $(shell git rev-parse --short HEAD 2>/dev/null || echo "unknown")
DATE    ?= $(shell date -u +"%Y-%m-%dT%H:%M:%SZ")
LDFLAGS := -ldflags "-s -w -X github.com/wireztna/proxy/internal/version.Version=$(VERSION) -X github.com/wireztna/proxy/internal/version.Commit=$(COMMIT) -X github.com/wireztna/proxy/internal/version.Date=$(DATE)"

BINARY := wzctl

.PHONY: build build-all clean test fmt vet

## build: Build for current platform
build:
	CGO_ENABLED=0 go build $(LDFLAGS) -o bin/$(BINARY) .

## build-all: Cross-compile for all platforms
build-all: build-linux build-darwin build-windows

build-linux:
	CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build $(LDFLAGS) -o bin/$(BINARY)-linux-amd64 .
	CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build $(LDFLAGS) -o bin/$(BINARY)-linux-arm64 .

build-darwin:
	CGO_ENABLED=0 GOOS=darwin GOARCH=amd64 go build $(LDFLAGS) -o bin/$(BINARY)-darwin-amd64 .
	CGO_ENABLED=0 GOOS=darwin GOARCH=arm64 go build $(LDFLAGS) -o bin/$(BINARY)-darwin-arm64 .
	# Ad-hoc codesign for macOS Sequoia+ (prevents SIGKILL on unsigned binaries)
	-codesign -s - bin/$(BINARY)-darwin-amd64 2>/dev/null || true
	-codesign -s - bin/$(BINARY)-darwin-arm64 2>/dev/null || true

build-windows:
	CGO_ENABLED=0 GOOS=windows GOARCH=amd64 go build $(LDFLAGS) -o bin/$(BINARY)-windows-amd64.exe .

## test: Run all tests
test:
	go test -v ./...

## fmt: Format code
fmt:
	go fmt ./...

## vet: Run go vet
vet:
	go vet ./...

## clean: Remove build artifacts
clean:
	rm -rf bin/
