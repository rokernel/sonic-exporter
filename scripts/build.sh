#!/usr/bin/env bash
set -euo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
OUTPUT_DIR="${REPO_ROOT}/build"
OUTPUT_BIN="${OUTPUT_DIR}/sonic-exporter"
TARGET_OS="${TARGET_OS:-linux}"
TARGET_ARCH="${TARGET_ARCH:-amd64}"

mkdir -p "${OUTPUT_DIR}"

echo "Building sonic-exporter for ${TARGET_OS}/${TARGET_ARCH} (static)..."
CGO_ENABLED=0 GOOS="${TARGET_OS}" GOARCH="${TARGET_ARCH}" go build -trimpath -ldflags "-s -w" -o "${OUTPUT_BIN}" "${REPO_ROOT}/cmd/sonic-exporter"

echo "Done: ${OUTPUT_BIN}"
