#!/bin/sh
# Run the smoke test the way CI does.
#
# `go test ./...` does not cover the shared library: it builds with CGO disabled
# and never dlopen()s anything. This script builds the real .so and drives it
# over the ABI, so a broken ABI table or a bad registration envelope is caught
# before a tag is pushed.
#
# Usage:  sh scripts/run_smoke.sh [goarch]
set -eu

GOARCH_ARG="${1:-$(go env GOARCH)}"
DIST="dist-smoke-check"

cleanup() { rm -rf "$DIST"; }
trap cleanup EXIT INT TERM

mkdir -p "$DIST"

echo "== 构建 .so ($GOARCH_ARG) =="
CGO_ENABLED=1 CC=gcc GOOS=linux GOARCH="$GOARCH_ARG" \
  go build -buildmode=c-shared -trimpath -o "$DIST/tinker.so" .

echo "== 构建 smoke =="
go build -o "$DIST/smoke" ./smoke

echo "== 运行 smoke =="
./"$DIST/smoke" "$DIST/tinker.so"
