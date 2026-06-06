# Makefile for the Go port of postgresql_sandbox.
#
# All targets are POSIX-friendly and rely only on the Go toolchain
# (plus standard `git` for the version stamp). No external Go modules
# are fetched — `GOFLAGS=-mod=vendor` would also work if we ever
# vendor, but for now the stdlib-only policy means there's nothing
# to vendor.
#
# Targets:
#   build      — build the local-arch binary into bin/pg_sandbox
#   build-all  — cross-compile all four release binaries into bin/
#   test       — go test ./...
#   vet        — go vet ./...
#   lint       — go vet + golangci-lint (if installed)
#   fmt        — gofmt -s -w .
#   clean      — remove bin/
#   help       — print this list

# ---- Configuration ----

# BIN_DIR is where compiled binaries land. Relative to this Makefile.
BIN_DIR     ?= bin

# BINARY is the executable name (without arch suffix).
BINARY      ?= pg_sandbox

# PKG is the import path of the main package.
PKG         ?= ./cmd/$(BINARY)

# VERSION is derived from `git describe`. Falls back to "dev" when
# git isn't available (e.g. tarball install). Override via env or arg.
VERSION     ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)

# COMMIT is the short SHA. Falls back to "unknown" on tarball.
COMMIT      ?= $(shell git rev-parse --short HEAD 2>/dev/null || echo unknown)

# LDFLAGS:
#   -s -w           : strip symbol/DWARF tables; trims the binary.
#   -X main.version : inject the version string at link time.
#   -X main.commit  : inject the short commit at link time.
LDFLAGS     := -s -w -X main.version=$(VERSION) -X main.commit=$(COMMIT)

# Cross-compile matrix. Format: GOOS-GOARCH per line.
PLATFORMS   := darwin-amd64 darwin-arm64 linux-amd64 linux-arm64

# Default target is `build` (build the local-arch binary) so a fresh
# clone of this repo can do `cd go && make` and get something useful.
.DEFAULT_GOAL := build

# ---- Phony targets ----

.PHONY: help build build-all test vet lint fmt clean

help:
	@echo 'Targets:'
	@echo '  build      Build the local-arch binary into $(BIN_DIR)/$(BINARY)'
	@echo '  build-all  Cross-compile release binaries for: $(PLATFORMS)'
	@echo '  test       Run all Go tests'
	@echo '  vet        Run go vet ./...'
	@echo '  lint       Run go vet + golangci-lint (if installed)'
	@echo '  fmt        Run gofmt -s -w .'
	@echo '  clean      Remove $(BIN_DIR)/'

build:
	@mkdir -p $(BIN_DIR)
	go build -trimpath -ldflags='$(LDFLAGS)' -o $(BIN_DIR)/$(BINARY) $(PKG)

# Cross-compile every entry in $(PLATFORMS). We loop in shell rather
# than expanding into per-target rules because the matrix is small
# and the resulting Makefile stays easier to read.
build-all:
	@mkdir -p $(BIN_DIR)
	@set -e ; \
	for plat in $(PLATFORMS) ; do \
	  goos=$${plat%-*} ; goarch=$${plat#*-} ; \
	  out=$(BIN_DIR)/$(BINARY)-$$goos-$$goarch ; \
	  echo "==> $$out" ; \
	  GOOS=$$goos GOARCH=$$goarch CGO_ENABLED=0 \
	    go build -trimpath -ldflags='$(LDFLAGS)' -o $$out $(PKG) ; \
	done

test:
	go test ./...

vet:
	go vet ./...

# We run go vet unconditionally and only invoke golangci-lint if it
# is on PATH, so contributors without the linter installed are not
# blocked from running `make lint`.
lint: vet
	@if command -v golangci-lint >/dev/null 2>&1 ; then \
	  golangci-lint run ; \
	else \
	  echo 'golangci-lint not installed; skipping (install: https://golangci-lint.run/)' ; \
	fi

fmt:
	gofmt -s -w .

clean:
	rm -rf $(BIN_DIR)
