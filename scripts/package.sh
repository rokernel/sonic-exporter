#!/usr/bin/env bash
set -euo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
BUILD_SCRIPT="${REPO_ROOT}/scripts/build.sh"
OUTPUT_DIR="${REPO_ROOT}/build"

VERSION="${VERSION:-$(git -C "${REPO_ROOT}" rev-parse --short HEAD)}"
TARGET_OS="${TARGET_OS:-linux}"
TARGET_ARCH="${TARGET_ARCH:-amd64}"

BIN_NAME="sonic-exporter"
PKG_BASE="${BIN_NAME}-${VERSION}-${TARGET_OS}-${TARGET_ARCH}"
STAGE_DIR="${OUTPUT_DIR}/${PKG_BASE}"
ARCHIVE_PATH="${OUTPUT_DIR}/${PKG_BASE}.tar.gz"
CHECKSUM_PATH="${ARCHIVE_PATH}.sha256"

"${BUILD_SCRIPT}"

rm -rf "${STAGE_DIR}"
mkdir -p "${STAGE_DIR}"

cp "${OUTPUT_DIR}/${BIN_NAME}" "${STAGE_DIR}/${BIN_NAME}"

tar -C "${OUTPUT_DIR}" -czf "${ARCHIVE_PATH}" "${PKG_BASE}"
sha256sum "${ARCHIVE_PATH}" > "${CHECKSUM_PATH}"

rm -rf "${STAGE_DIR}"

printf "Package: %s\nChecksum: %s\n" "${ARCHIVE_PATH}" "${CHECKSUM_PATH}"
