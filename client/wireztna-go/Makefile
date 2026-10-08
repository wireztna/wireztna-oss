# WireZTNA Client — Build targets

BINARY_NAME = wireztna
DESKTOP_NAME = wireztna-desktop
AUTH_NAME = wireztna-auth
DOTNET ?= dotnet
GO_WINRES ?= $(shell go env GOPATH)/bin/go-winres
AUTH_PROJECT = ../wireztna-auth/WireZTNA.Auth.csproj
VERSION ?= $(shell cat VERSION 2>/dev/null || git describe --tags --always --dirty 2>/dev/null || echo "dev")
COMMIT  ?= $(shell git rev-parse --short HEAD 2>/dev/null || echo "unknown")
DATE    ?= $(shell date -u '+%Y-%m-%dT%H:%M:%SZ')

LDFLAGS = -s -w \
	-X github.com/wireztna/client/pkg/version.Version=$(VERSION) \
	-X github.com/wireztna/client/pkg/version.Commit=$(COMMIT) \
	-X github.com/wireztna/client/pkg/version.BuildDate=$(DATE)

# Windows GUI flag hides the console window for the tray app
LDFLAGS_GUI = $(LDFLAGS) -H windowsgui

.PHONY: build build-all build-desktop build-auth-windows build-windows build-msi-windows macos-runtime macos-pkg macos-local-pkg macos-local-clean winres clean test lint install help

## build: Build CLI for current OS/arch
build:
	go build -ldflags "$(LDFLAGS)" -o bin/$(BINARY_NAME) .

## build-desktop: Build the system tray app for current OS/arch
build-desktop:
	go build -ldflags "$(LDFLAGS)" -o bin/$(DESKTOP_NAME) ./cmd/desktop

## build-all: Cross-compile CLI for macOS, Linux, Windows (amd64 + arm64)
build-all:
	GOOS=darwin  GOARCH=amd64 go build -ldflags "$(LDFLAGS)" -o bin/$(BINARY_NAME)-$(VERSION)-darwin-amd64 .
	GOOS=darwin  GOARCH=arm64 go build -ldflags "$(LDFLAGS)" -o bin/$(BINARY_NAME)-$(VERSION)-darwin-arm64 .
	GOOS=linux   GOARCH=amd64 go build -ldflags "$(LDFLAGS)" -o bin/$(BINARY_NAME)-$(VERSION)-linux-amd64 .
	GOOS=linux   GOARCH=arm64 go build -ldflags "$(LDFLAGS)" -o bin/$(BINARY_NAME)-$(VERSION)-linux-arm64 .
	GOOS=windows GOARCH=amd64 go build -ldflags "$(LDFLAGS)" -o bin/$(BINARY_NAME)-$(VERSION)-windows-amd64.exe .
	GOOS=windows GOARCH=arm64 go build -ldflags "$(LDFLAGS)" -o bin/$(BINARY_NAME)-$(VERSION)-windows-arm64.exe .

## build-all-desktop: Cross-compile tray app for all platforms
build-all-desktop:
	GOOS=darwin  GOARCH=amd64 go build -ldflags "$(LDFLAGS)" -o bin/$(DESKTOP_NAME)-darwin-amd64 ./cmd/desktop
	GOOS=darwin  GOARCH=arm64 go build -ldflags "$(LDFLAGS)" -o bin/$(DESKTOP_NAME)-darwin-arm64 ./cmd/desktop
	GOOS=linux   GOARCH=amd64 go build -ldflags "$(LDFLAGS)" -o bin/$(DESKTOP_NAME)-linux-amd64 ./cmd/desktop
	GOOS=windows GOARCH=amd64 go build -ldflags "$(LDFLAGS_GUI)" -o bin/$(DESKTOP_NAME)-windows-amd64.exe ./cmd/desktop

## build-auth-windows: Publish the native WPF authentication helper
build-auth-windows:
	@mkdir -p bin/windows/auth-publish
	$(DOTNET) publish $(AUTH_PROJECT) -c Release -r win-x64 --self-contained true \
		-p:Version=$(VERSION) -p:AssemblyVersion=$(VERSION).0 \
		-p:FileVersion=$(VERSION).0 -p:InformationalVersion=$(VERSION) \
		-o bin/windows/auth-publish
	@cp bin/windows/auth-publish/$(AUTH_NAME).exe bin/windows/$(AUTH_NAME).exe

## build-windows: Build service, tray, and WPF auth helper ready for MSI
build-windows: winres build-auth-windows
	@mkdir -p bin/windows
	GOOS=windows GOARCH=amd64 go build -ldflags "$(LDFLAGS)" -o bin/windows/$(BINARY_NAME).exe .
	GOOS=windows GOARCH=amd64 go build -ldflags "$(LDFLAGS_GUI)" -o bin/windows/$(DESKTOP_NAME).exe ./cmd/desktop
	@cp internal/wgnt/wireguard.dll bin/windows/
	@cp packaging/windows/wireztna.ico bin/windows/
	@echo "Windows binaries ready in bin/windows/"

## build-msi-windows: Invoke the canonical fail-closed MSI builder
build-msi-windows: build-windows
	@mkdir -p dist
	./packaging/windows/build-msi.sh "$(VERSION)" bin/windows "dist/wireztna-$(VERSION)-windows-amd64.msi"

## macos-runtime: Build the pinned self-contained Apple Silicon runtime
macos-runtime:
	./packaging/macos/build-runtime.sh $(if $(SOURCE_CACHE),--source-cache "$(SOURCE_CACHE)") $(if $(GO_MODULE_CACHE),--go-module-cache "$(GO_MODULE_CACHE)") $(if $(OFFLINE),--offline)

## macos-pkg: Build a generic unsigned Apple Silicon PKG
macos-pkg:
	./packaging/macos/build-local-pkg.sh --version "$(VERSION)" $(if $(RUNTIME_DIR),--runtime-dir "$(RUNTIME_DIR)") $(if $(OUTPUT),--output "$(OUTPUT)")

## macos-local-pkg: Backward-compatible alias for the generic unsigned PKG
macos-local-pkg: macos-pkg

## macos-local-clean: Remove only local macOS package build output
macos-local-clean:
	/bin/rm -rf dist/macos dist/macos-runtime-arm64

## winres: Generate Windows resource files (.syso) for icon/manifest/version embedding
winres:
	@echo "Generating Windows resource files..."
	@test -x "$(GO_WINRES)" || { echo "Installing go-winres v0.3.3..."; go install github.com/tc-hib/go-winres@v0.3.3; }
	cd . && $(GO_WINRES) make --arch amd64
	cd cmd/desktop && $(GO_WINRES) make --arch amd64
	@echo "Windows resources generated (icon, manifest, version info)"

## install: Build and install CLI to /usr/local/bin
install: build
	sudo cp bin/$(BINARY_NAME) /usr/local/bin/$(BINARY_NAME)
	@echo "Installed to /usr/local/bin/$(BINARY_NAME)"

## install-desktop: Build and install both CLI and tray app
install-desktop: build build-desktop
	sudo cp bin/$(BINARY_NAME) /usr/local/bin/$(BINARY_NAME)
	sudo cp bin/$(DESKTOP_NAME) /usr/local/bin/$(DESKTOP_NAME)
	@echo "Installed wireztna + wireztna-desktop to /usr/local/bin/"

## test: Run tests
test:
	go test -v ./...

## lint: Run linter
lint:
	golangci-lint run ./...

## sign-all: Sign all binaries in bin/ with Sigstore (keyless, opens browser for OIDC)
sign-all:
	@command -v cosign >/dev/null 2>&1 || { echo "ERROR: cosign not installed. Run: brew install cosign"; exit 1; }
	@echo "Signing all binaries in bin/ with Sigstore (keyless mode)..."
	@echo "This will open your browser for GitHub OIDC authentication."
	@for f in bin/$(BINARY_NAME)-*; do \
		[ -f "$$f" ] || continue; \
		echo "  Signing $$f ..."; \
		cosign sign-blob "$$f" --yes --bundle "$$f.bundle" || exit 1; \
	done
	@echo "All binaries signed. Bundle files (.bundle) created alongside each binary."

## sign-msi: Sign a specific MSI file (pass MSI=path/to/file.msi)
sign-msi:
	@test -n "$(MSI)" || { echo "Usage: make sign-msi MSI=dist/wireztna-0.9.15-windows-amd64.msi"; exit 1; }
	@command -v cosign >/dev/null 2>&1 || { echo "ERROR: cosign not installed. Run: brew install cosign"; exit 1; }
	cosign sign-blob "$(MSI)" --yes --bundle "$(MSI).bundle"
	@echo "Signed: $(MSI).bundle"

## verify: Verify a signed binary (pass FILE=path IDENTITY=email-or-url ISSUER=oidc-issuer)
## Example: make verify FILE=bin/wireztna-0.9.15-linux-amd64 IDENTITY=user@github.com ISSUER=https://github.com/login/oauth
verify:
	@test -n "$(FILE)" || { echo "Usage: make verify FILE=bin/wireztna-0.9.15-linux-amd64 IDENTITY=user@github.com ISSUER=https://github.com/login/oauth"; exit 1; }
	@test -n "$(IDENTITY)" || { echo "ERROR: IDENTITY required (email or workflow URL)"; exit 1; }
	@test -n "$(ISSUER)" || { echo "ERROR: ISSUER required (OIDC issuer URL)"; exit 1; }
	cosign verify-blob "$(FILE)" --bundle "$(FILE).bundle" \
		--certificate-identity="$(IDENTITY)" \
		--certificate-oidc-issuer="$(ISSUER)"

## clean: Remove build artifacts
clean:
	rm -rf bin/ dist/

## package-downloads: Create downloads tarball for broker deployment (includes APK + signatures)
package-downloads: build-all build-msi-windows
	@mkdir -p dist/downloads
	cp bin/$(BINARY_NAME)-$(VERSION)-darwin-amd64 bin/$(BINARY_NAME)-$(VERSION)-darwin-arm64 \
	   bin/$(BINARY_NAME)-$(VERSION)-linux-amd64 bin/$(BINARY_NAME)-$(VERSION)-linux-arm64 \
	   bin/$(BINARY_NAME)-$(VERSION)-windows-amd64.exe bin/$(BINARY_NAME)-$(VERSION)-windows-arm64.exe \
	   dist/downloads/
	@# Include Sigstore signature bundles if present
	@cp bin/$(BINARY_NAME)-$(VERSION)-*.bundle dist/downloads/ 2>/dev/null || true
	@# Include the current versioned MSI; only its signature remains optional
	cp dist/wireztna-$(VERSION)-windows-amd64.msi dist/downloads/
	@cp dist/wireztna-$(VERSION)-windows-amd64.msi.bundle dist/downloads/ 2>/dev/null || true
	@# Include Android APK if available
	@cp ../wireztna-android/app/build/outputs/apk/release/wireztna-android-*.apk dist/downloads/ 2>/dev/null || true
	@cp bin/wireztna-android-*.apk dist/downloads/ 2>/dev/null || true
	@echo "Downloads package ready in dist/downloads/"
	@ls dist/downloads/

## help: Show this help
help:
	@grep -E '^## ' Makefile | sed 's/## //' | column -t -s ':'
