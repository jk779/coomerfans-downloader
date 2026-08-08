BINARY_NAME := coomerfans-downloader
OUT_DIR ?= dist
VERSION ?= dev
TARGETS ?= darwin/arm64 linux/amd64 windows/amd64

.DEFAULT_GOAL := build

.PHONY: build help

## build: Cross-compile all platforms in TARGETS.
build:
	@mkdir -p "$(OUT_DIR)"
	@echo "Building version $(VERSION)..."
	@set -e; \
	for target in $(TARGETS); do \
		goos=$${target%/*}; goarch=$${target#*/}; \
		if [ "$$goos" = "$$goarch" ] || [ -z "$$goos" ] || [ -z "$$goarch" ]; then \
			echo "invalid target format: '$$target' (expected GOOS/GOARCH)" >&2; exit 1; \
		fi; \
		ext=; [ "$$goos" = "windows" ] && ext=.exe; \
		output="$(OUT_DIR)/$(BINARY_NAME)-$$goos-$$goarch$$ext"; \
		printf "  building %-20s -> %s\\n" "$$goos/$$goarch" "$$output"; \
		GOOS="$$goos" GOARCH="$$goarch" go build -ldflags "-X main.version=$(VERSION)" -o "$$output" .; \
	done
	@echo "Done. Binaries in $(OUT_DIR)/"

## help: Show this help and common build commands.
help:
	@printf '%s\n' \
		'Usage:' \
		'  make [build] [VERSION=X.Y.Z] [TARGETS="GOOS/GOARCH ..."]' \
		'' \
		'Default targets: darwin/arm64, linux/amd64, windows/amd64' \
		'' \
		'Examples:' \
		'  make' \
		'  make VERSION=1.2.0' \
		'  make TARGETS="darwin/arm64 windows/arm64" VERSION=1.2.0'
