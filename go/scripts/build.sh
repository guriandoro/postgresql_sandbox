#!/bin/sh
# build.sh — cross-compile pg_sandbox for macOS and Linux on amd64 + arm64.
#
# Mirrors `make build-all` for users who don't have GNU Make installed
# (e.g., a stock macOS shell). Pure POSIX sh; no bash-isms. Depends
# only on the Go toolchain and (optionally) git for the version stamp.
#
# Usage:
#   ./scripts/build.sh              # build all four target binaries
#   VERSION=1.2.3 ./scripts/build.sh
#   BIN_DIR=/tmp/out ./scripts/build.sh
#
# Exit status: 0 on success, non-zero on any build failure (the
# `set -e` halts the loop at the first error so we don't silently
# ship a half-built release).

set -eu

# Resolve the directory containing this script, then cd to the
# Go module root (one level up). This makes the script safe to run
# from any working directory.
script_dir=$(cd "$(dirname "$0")" && pwd)
cd "$script_dir/.."

BIN_DIR=${BIN_DIR:-bin}
BINARY=${BINARY:-pg_sandbox}
PKG=${PKG:-./cmd/$BINARY}

# Version + commit stamping. Match the Makefile exactly so binaries
# built either way are indistinguishable.
VERSION=${VERSION:-$(git describe --tags --always --dirty 2>/dev/null || echo dev)}
COMMIT=${COMMIT:-$(git rev-parse --short HEAD 2>/dev/null || echo unknown)}
LDFLAGS="-s -w -X main.version=$VERSION -X main.commit=$COMMIT"

# Platforms must match the Makefile's PLATFORMS list. If you change
# one, change both.
PLATFORMS="darwin-amd64 darwin-arm64 linux-amd64 linux-arm64"

mkdir -p "$BIN_DIR"

for plat in $PLATFORMS; do
    goos=${plat%-*}
    goarch=${plat#*-}
    out="$BIN_DIR/$BINARY-$goos-$goarch"
    echo "==> $out"
    GOOS="$goos" GOARCH="$goarch" CGO_ENABLED=0 \
        go build -trimpath -ldflags "$LDFLAGS" -o "$out" "$PKG"
done

echo "Built: $(ls "$BIN_DIR" | tr '\n' ' ')"
