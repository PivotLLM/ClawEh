.PHONY: all build claw-auth install uninstall uninstall-all clean help test test-race test-coverage test-cover-html test-regression generate vet fmt lint fix deps update-deps check run frontend frontend-deps build-linux-arm build-linux-arm64 build-linux-mipsle build-pi-zero build-all

# Binary names
BINARY_NAME=claw
BUILD_DIR=build
CMD_DIR=.
MAIN_GO=main.go

# Version
VERSION?=$(shell git describe --tags --always --dirty 2>/dev/null || echo "dev")
GIT_COMMIT=$(shell git rev-parse --short=8 HEAD 2>/dev/null || echo "dev")
BUILD_TIME=$(shell date +%FT%T%z)
GO_VERSION=$(shell $(GO) version | awk '{print $$3}')
CONFIG_PKG=github.com/PivotLLM/ClawEh/pkg/config
LDFLAGS=-ldflags "-X $(CONFIG_PKG).Version=$(VERSION) -X $(CONFIG_PKG).GitCommit=$(GIT_COMMIT) -X $(CONFIG_PKG).BuildTime=$(BUILD_TIME) -X $(CONFIG_PKG).GoVersion=$(GO_VERSION) -s -w"

# Go variables
GO?=CGO_ENABLED=0 go
GOFLAGS?=-v -tags stdjson

# Patch MIPS LE ELF e_flags (offset 36) for NaN2008-only kernels (e.g. Ingenic X2600).
#
# Bytes (octal): \004 \024 \000 \160  →  little-endian 0x70001404
#   0x70000000  EF_MIPS_ARCH_32R2   MIPS32 Release 2
#   0x00001000  EF_MIPS_ABI_O32     O32 ABI
#   0x00000400  EF_MIPS_NAN2008     IEEE 754-2008 NaN encoding
#   0x00000004  EF_MIPS_CPIC        PIC calling sequence
#
# Go's GOMIPS=softfloat emits no FP instructions, so the NaN mode is irrelevant
# at runtime — this is purely an ELF metadata fix to satisfy the kernel's check.
# patchelf cannot modify e_flags; dd at a fixed offset is the most portable way.
#
# Ref: https://codebrowser.dev/linux/linux/arch/mips/include/asm/elf.h.html
define PATCH_MIPS_FLAGS
	@if [ -f "$(1)" ]; then \
		printf '\004\024\000\160' | dd of=$(1) bs=1 seek=36 count=4 conv=notrunc 2>/dev/null || \
		{ echo "Error: failed to patch MIPS e_flags for $(1)"; exit 1; }; \
	else \
		echo "Error: $(1) not found, cannot patch MIPS e_flags"; exit 1; \
	fi
endef

# Golangci-lint
GOLANGCI_LINT?=golangci-lint

# Installation
INSTALL_PREFIX?=$(HOME)/.local
INSTALL_BIN_DIR=$(INSTALL_PREFIX)/bin
INSTALL_TMP_SUFFIX=.new

# Data directory
CLAW_HOME?=$(HOME)/.claw
WORKSPACE_DIR?=$(CLAW_HOME)/workspace
WORKSPACE_SKILLS_DIR=$(WORKSPACE_DIR)/skills
BUILTIN_SKILLS_DIR=$(CURDIR)/skills

# OS detection
UNAME_S:=$(shell uname -s)
UNAME_M:=$(shell uname -m)

# Platform-specific settings
ifeq ($(UNAME_S),Linux)
	PLATFORM=linux
	ifeq ($(UNAME_M),x86_64)
		ARCH=amd64
	else ifeq ($(UNAME_M),aarch64)
		ARCH=arm64
	else ifeq ($(UNAME_M),armv81)
		ARCH=arm64
	else ifeq ($(UNAME_M),loongarch64)
		ARCH=loong64
	else ifeq ($(UNAME_M),riscv64)
		ARCH=riscv64
	else ifeq ($(UNAME_M),mipsel)
		ARCH=mipsle
	else
		ARCH=$(UNAME_M)
	endif
else ifeq ($(UNAME_S),Darwin)
	PLATFORM=darwin
	ifeq ($(UNAME_M),x86_64)
		ARCH=amd64
	else ifeq ($(UNAME_M),arm64)
		ARCH=arm64
	else
		ARCH=$(UNAME_M)
	endif
else
	PLATFORM=$(UNAME_S)
	ARCH=$(UNAME_M)
endif

# Cross-compilation: override PLATFORM and/or ARCH (and optionally GOARM/GOMIPS)
# on the command line to build for another target. PLATFORM maps to GOOS and ARCH
# to GOARCH; both default to the detected host, so a plain `make build` is
# unchanged. Examples:
#   make build PLATFORM=linux   ARCH=arm64
#   make build PLATFORM=linux   ARCH=arm    GOARM=7
#   make build PLATFORM=windows ARCH=amd64
#   make build PLATFORM=darwin  ARCH=arm64
# Note: linux/mipsle needs the ELF e_flags patch — use `make build-linux-mipsle`.
GOARM?=
GOMIPS?=
BUILD_ENV=GOOS=$(PLATFORM) GOARCH=$(ARCH)$(if $(GOARM), GOARM=$(GOARM))$(if $(GOMIPS), GOMIPS=$(GOMIPS))

BINARY_PATH=$(BUILD_DIR)/$(BINARY_NAME)-$(PLATFORM)-$(ARCH)
CLAW_AUTH_NAME=claw-auth
CLAW_AUTH_PATH=$(BUILD_DIR)/$(CLAW_AUTH_NAME)-$(PLATFORM)-$(ARCH)

# Frontend / embedded SPA
FRONTEND_DIR=web/frontend
FRONTEND_NODE_MODULES=$(FRONTEND_DIR)/node_modules
EMBED_DIR=web/backend/dist
EMBED_INDEX=$(EMBED_DIR)/index.html
# Source files that, when changed, should retrigger the frontend build.
FRONTEND_SOURCES=$(shell find $(FRONTEND_DIR)/src $(FRONTEND_DIR)/public 2>/dev/null) \
	$(FRONTEND_DIR)/index.html \
	$(FRONTEND_DIR)/package.json \
	$(FRONTEND_DIR)/pnpm-lock.yaml \
	$(FRONTEND_DIR)/vite.config.ts \
	$(FRONTEND_DIR)/tsconfig.json \
	$(FRONTEND_DIR)/tsconfig.app.json \
	$(FRONTEND_DIR)/tsconfig.node.json

# Default target
all: build

## build: Build the claw and claw-auth binaries (frontend SPA embedded) for current platform.
build: $(EMBED_INDEX) generate
	@echo "Building $(BINARY_NAME) for $(PLATFORM)/$(ARCH)..."
	@mkdir -p $(BUILD_DIR)
	@$(BUILD_ENV) $(GO) build $(GOFLAGS) $(LDFLAGS) -o $(BINARY_PATH) ./$(CMD_DIR)
	@echo "Build complete: $(BINARY_PATH)"
	@ln -sf $(BINARY_NAME)-$(PLATFORM)-$(ARCH) $(BUILD_DIR)/$(BINARY_NAME)
	@echo "Building $(CLAW_AUTH_NAME) for $(PLATFORM)/$(ARCH)..."
	@$(BUILD_ENV) $(GO) build $(GOFLAGS) $(LDFLAGS) -o $(CLAW_AUTH_PATH) ./cmd/claw-auth
	@echo "Build complete: $(CLAW_AUTH_PATH)"
	@ln -sf $(CLAW_AUTH_NAME)-$(PLATFORM)-$(ARCH) $(BUILD_DIR)/$(CLAW_AUTH_NAME)

## claw-auth: Build the standalone claw-auth OAuth helper CLI (runs on the user's own computer).
claw-auth:
	@echo "Building $(CLAW_AUTH_NAME) for $(PLATFORM)/$(ARCH)..."
	@mkdir -p $(BUILD_DIR)
	@$(BUILD_ENV) $(GO) build $(GOFLAGS) $(LDFLAGS) -o $(CLAW_AUTH_PATH) ./cmd/claw-auth
	@echo "Build complete: $(CLAW_AUTH_PATH)"
	@ln -sf $(CLAW_AUTH_NAME)-$(PLATFORM)-$(ARCH) $(BUILD_DIR)/$(CLAW_AUTH_NAME)

## frontend: Build the SPA into the Go embed source (web/backend/dist).
frontend: $(EMBED_INDEX)

## frontend-deps: Install frontend npm dependencies.
frontend-deps: $(FRONTEND_NODE_MODULES)

$(FRONTEND_NODE_MODULES): $(FRONTEND_DIR)/package.json $(FRONTEND_DIR)/pnpm-lock.yaml
	@echo "Installing frontend dependencies (pnpm install)..."
	@cd $(FRONTEND_DIR) && pnpm install
	@touch $(FRONTEND_NODE_MODULES)

$(EMBED_INDEX): $(FRONTEND_SOURCES) | $(FRONTEND_NODE_MODULES)
	@echo "Building frontend SPA into $(EMBED_DIR)..."
	@cd $(FRONTEND_DIR) && pnpm run build:backend

## install: Build and install claw to $(INSTALL_BIN_DIR)
install: build
	@echo "Installing $(BINARY_NAME)..."
	@mkdir -p $(INSTALL_BIN_DIR)
	@cp $(BINARY_PATH) $(INSTALL_BIN_DIR)/$(BINARY_NAME)$(INSTALL_TMP_SUFFIX)
	@chmod +x $(INSTALL_BIN_DIR)/$(BINARY_NAME)$(INSTALL_TMP_SUFFIX)
	@mv -f $(INSTALL_BIN_DIR)/$(BINARY_NAME)$(INSTALL_TMP_SUFFIX) $(INSTALL_BIN_DIR)/$(BINARY_NAME)
	@echo "Installed: $(INSTALL_BIN_DIR)/$(BINARY_NAME)"

## uninstall: Remove claw from system
uninstall:
	@echo "Uninstalling $(BINARY_NAME)..."
	@rm -f $(INSTALL_BIN_DIR)/$(BINARY_NAME)
	@echo "Removed binaries from $(INSTALL_BIN_DIR)"
	@echo "Note: Data directory $(CLAW_HOME) was not removed. Run 'make uninstall-all' to remove everything."

## uninstall-all: Remove claw and all data
uninstall-all:
	@echo "Removing binaries..."
	@rm -f $(INSTALL_BIN_DIR)/$(BINARY_NAME)
	@echo "Removing data directory..."
	@rm -rf $(CLAW_HOME)
	@echo "Removed: $(CLAW_HOME)"
	@echo "Complete uninstallation done!"

## generate: Run go generate (refreshes embedded onboard workspace)
generate:
	@echo "Run generate..."
	@rm -r ./internal/onboard/workspace 2>/dev/null || true
	@$(GO) generate ./...
	@echo "Run generate complete"

## build-linux-arm: Build for Linux ARMv7 (e.g. Raspberry Pi Zero 2 W 32-bit)
build-linux-arm: $(EMBED_INDEX) generate
	@echo "Building for linux/arm (GOARM=7)..."
	@mkdir -p $(BUILD_DIR)
	GOOS=linux GOARCH=arm GOARM=7 $(GO) build $(LDFLAGS) -o $(BUILD_DIR)/$(BINARY_NAME)-linux-arm ./$(CMD_DIR)
	@echo "Build complete: $(BUILD_DIR)/$(BINARY_NAME)-linux-arm"

## build-linux-arm64: Build for Linux ARM64 (e.g. Raspberry Pi Zero 2 W 64-bit)
build-linux-arm64: $(EMBED_INDEX) generate
	@echo "Building for linux/arm64..."
	@mkdir -p $(BUILD_DIR)
	GOOS=linux GOARCH=arm64 $(GO) build $(LDFLAGS) -o $(BUILD_DIR)/$(BINARY_NAME)-linux-arm64 ./$(CMD_DIR)
	@echo "Build complete: $(BUILD_DIR)/$(BINARY_NAME)-linux-arm64"

## build-linux-mipsle: Build for Linux MIPS32 LE
build-linux-mipsle: $(EMBED_INDEX) generate
	@echo "Building for linux/mipsle (softfloat)..."
	@mkdir -p $(BUILD_DIR)
	GOOS=linux GOARCH=mipsle GOMIPS=softfloat $(GO) build $(LDFLAGS) -o $(BUILD_DIR)/$(BINARY_NAME)-linux-mipsle ./$(CMD_DIR)
	$(call PATCH_MIPS_FLAGS,$(BUILD_DIR)/$(BINARY_NAME)-linux-mipsle)
	@echo "Build complete: $(BUILD_DIR)/$(BINARY_NAME)-linux-mipsle"

## build-pi-zero: Build for Raspberry Pi Zero 2 W (32-bit and 64-bit)
build-pi-zero: build-linux-arm build-linux-arm64
	@echo "Pi Zero 2 W builds: $(BUILD_DIR)/$(BINARY_NAME)-linux-arm (32-bit), $(BUILD_DIR)/$(BINARY_NAME)-linux-arm64 (64-bit)"

## build-all: Build claw for all platforms
build-all: $(EMBED_INDEX) generate
	@echo "Building for multiple platforms..."
	@mkdir -p $(BUILD_DIR)
	GOOS=linux GOARCH=amd64 $(GO) build $(LDFLAGS) -o $(BUILD_DIR)/$(BINARY_NAME)-linux-amd64 ./$(CMD_DIR)
	GOOS=linux GOARCH=arm GOARM=7 $(GO) build $(LDFLAGS) -o $(BUILD_DIR)/$(BINARY_NAME)-linux-arm ./$(CMD_DIR)
	GOOS=linux GOARCH=arm64 $(GO) build $(LDFLAGS) -o $(BUILD_DIR)/$(BINARY_NAME)-linux-arm64 ./$(CMD_DIR)
	GOOS=linux GOARCH=loong64 $(GO) build $(LDFLAGS) -o $(BUILD_DIR)/$(BINARY_NAME)-linux-loong64 ./$(CMD_DIR)
	GOOS=linux GOARCH=riscv64 $(GO) build $(LDFLAGS) -o $(BUILD_DIR)/$(BINARY_NAME)-linux-riscv64 ./$(CMD_DIR)
	GOOS=linux GOARCH=mipsle GOMIPS=softfloat $(GO) build $(LDFLAGS) -o $(BUILD_DIR)/$(BINARY_NAME)-linux-mipsle ./$(CMD_DIR)
	$(call PATCH_MIPS_FLAGS,$(BUILD_DIR)/$(BINARY_NAME)-linux-mipsle)
	GOOS=linux GOARCH=arm GOARM=7 $(GO) build $(LDFLAGS) -o $(BUILD_DIR)/$(BINARY_NAME)-linux-armv7 ./$(CMD_DIR)
	GOOS=darwin GOARCH=arm64 $(GO) build $(LDFLAGS) -o $(BUILD_DIR)/$(BINARY_NAME)-darwin-arm64 ./$(CMD_DIR)
	GOOS=windows GOARCH=amd64 $(GO) build $(LDFLAGS) -o $(BUILD_DIR)/$(BINARY_NAME)-windows-amd64.exe ./$(CMD_DIR)
	GOOS=netbsd GOARCH=amd64 $(GO) build $(LDFLAGS) -o $(BUILD_DIR)/$(BINARY_NAME)-netbsd-amd64 ./$(CMD_DIR)
	GOOS=netbsd GOARCH=arm64 $(GO) build $(LDFLAGS) -o $(BUILD_DIR)/$(BINARY_NAME)-netbsd-arm64 ./$(CMD_DIR)
	@echo "All builds complete"

## clean: Remove all build artifacts (Go binaries, frontend dist, embedded SPA)
clean:
	@echo "Cleaning build artifacts..."
	@rm -rf $(BUILD_DIR)
	@rm -rf $(EMBED_DIR)
	@rm -rf $(FRONTEND_DIR)/dist
	@echo "Clean complete (node_modules left in place)"

## vet: Run go vet for static analysis
vet: generate
	@$(GO) vet ./...

## test: Test Go code
test: generate
	@$(GO) test ./...

## test-race: Run full test suite with race detector
test-race: generate
	@go test -race -count=1 -timeout=300s ./...

## test-coverage: Run tests with coverage summary (per-package and overall)
test-coverage: generate
	@$(GO) test -coverprofile=coverage.out -count=1 -timeout=300s ./... 2>&1
	@echo ""
	@echo "Per-package coverage (sorted by percentage):"
	@go tool cover -func=coverage.out | grep -v "^total:" | awk '{print $$3, $$1}' | sort -t'%' -k1 -n | awk '{print $$2, $$1}'
	@echo ""
	@echo "Overall total:"
	@go tool cover -func=coverage.out | grep "^total:"
	@rm -f coverage.out

## test-cover-html: Generate HTML coverage report
test-cover-html: generate
	@$(GO) test -coverprofile=coverage.out ./... && go tool cover -html=coverage.out -o coverage.html
	@rm -f coverage.out
	@echo "Coverage report: $(CURDIR)/coverage.html"

## test-regression: Full regression gate — race detector + coverage enforcement (min 50%)
test-regression: generate
	@echo "Running regression test suite..."
	@go test -race -coverprofile=coverage.out -count=1 -timeout=300s ./...
	@echo ""
	@echo "Coverage summary:"
	@go tool cover -func=coverage.out | grep -v "^total:" | awk '{print $$3, $$1}' | sort -t'%' -k1 -n | awk '{print $$2, $$1}'
	@echo ""
	@go tool cover -func=coverage.out | grep "^total:"
	@TOTAL=$$(go tool cover -func=coverage.out | grep "^total:" | awk '{print $$3}' | tr -d '%'); \
	rm -f coverage.out; \
	if [ $$(echo "$$TOTAL < 50" | bc) -eq 1 ]; then \
		echo "Coverage $$TOTAL% is below minimum 50%"; exit 1; \
	fi
	@echo "Regression gate passed."

## fmt: Format Go code
fmt:
	@$(GOLANGCI_LINT) fmt

## lint: Run linters
lint:
	@$(GOLANGCI_LINT) run

## fix: Fix linting issues
fix:
	@$(GOLANGCI_LINT) run --fix

## deps: Download and verify dependencies (optional; only needed for offline-build prep or after go.mod changes — normal builds and tests fetch implicitly)
deps:
	@$(GO) mod download
	@$(GO) mod verify

## update-deps: Update dependencies
update-deps:
	@$(GO) get -u ./...
	@$(GO) mod tidy

## check: Run fmt, vet, and tests
check: fmt vet test

## run: Build and run claw
run: build
	@$(BUILD_DIR)/$(BINARY_NAME) $(ARGS)

## help: Show this help message
help:
	@echo "ClawEh Makefile"
	@echo ""
	@echo "Usage:"
	@echo "  make [target]"
	@echo ""
	@echo "Targets:"
	@grep -E '^## ' $(MAKEFILE_LIST) | sort | awk -F': ' '{printf "  %-20s %s\n", substr($$1, 4), $$2}'
	@echo ""
	@echo "Examples:"
	@echo "  make                    # Same as 'make build'"
	@echo "  make build              # Build claw (frontend SPA embedded) for current platform"
	@echo "  make install            # Build and install to $(INSTALL_BIN_DIR)"
	@echo "  make clean              # Remove all build artifacts (keeps node_modules)"
	@echo "  make uninstall          # Remove binaries"
	@echo "  make uninstall-all      # Remove binaries and data directory"
	@echo ""
	@echo "Cross-compilation (PLATFORM=GOOS, ARCH=GOARCH; default to host):"
	@echo "  make build PLATFORM=linux ARCH=arm64       # Linux ARM64"
	@echo "  make build PLATFORM=linux ARCH=arm GOARM=7 # Linux ARMv7"
	@echo "  make build PLATFORM=windows ARCH=amd64     # Windows x86-64"
	@echo "  make build PLATFORM=darwin ARCH=arm64      # macOS Apple Silicon"
	@echo ""
	@echo "Environment Variables:"
	@echo "  PLATFORM                # Target GOOS (default: host, $(PLATFORM))"
	@echo "  ARCH                    # Target GOARCH (default: host, $(ARCH))"
	@echo "  GOARM / GOMIPS          # Optional sub-arch (e.g. GOARM=7, GOMIPS=softfloat)"
	@echo "  INSTALL_PREFIX          # Installation prefix (default: ~/.local)"
	@echo "  CLAW_HOME               # Data directory (default: ~/.claw)"
	@echo "  VERSION                 # Version string (default: git describe)"
	@echo ""
	@echo "Current Configuration:"
	@echo "  Platform: $(PLATFORM)/$(ARCH)"
	@echo "  Binary:   $(BINARY_PATH)"
	@echo "  Install:  $(INSTALL_BIN_DIR)"
	@echo "  Data:     $(CLAW_HOME)"
