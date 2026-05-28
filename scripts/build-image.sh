#!/usr/bin/env bash
set -euo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
IMAGE_NAME="${IMAGE_NAME:-sonic-exporter}"
IMAGE_TAG="${IMAGE_TAG:-}"
PLATFORM="${PLATFORM:-linux/amd64}"
OUTPUT_DIR="${OUTPUT_DIR:-${REPO_ROOT}/build/docker}"
DRY_RUN="false"

usage() {
    cat <<'EOF'
Usage: IMAGE_TAG=<tag> [IMAGE_NAME=name] [PLATFORM=os/arch] [OUTPUT_DIR=dir] build-image.sh [--dry-run]
EOF
}

while (($# > 0)); do
    case "$1" in
        --dry-run)
            DRY_RUN="true"
            ;;
        -h|--help)
            usage
            exit 0
            ;;
        *)
            echo "Unknown argument: $1" >&2
            usage >&2
            exit 1
            ;;
    esac
    shift
done

if [[ -z "${IMAGE_TAG}" ]]; then
    echo "IMAGE_TAG is required" >&2
    exit 1
fi

if [[ "${IMAGE_TAG}" == "latest" ]]; then
    echo "IMAGE_TAG=latest is not allowed" >&2
    exit 1
fi

if [[ "${IMAGE_TAG}" =~ [[:space:]] ]]; then
    echo "IMAGE_TAG must not contain whitespace" >&2
    exit 1
fi

VERSION="${IMAGE_TAG}"
REVISION="$(git -C "${REPO_ROOT}" rev-parse --short HEAD)"
CREATED="$(date -u +%Y-%m-%dT%H:%M:%SZ)"
SOURCE="$(git -C "${REPO_ROOT}" remote get-url origin 2>/dev/null || true)"

if [[ -z "${SOURCE}" ]]; then
    SOURCE="unknown"
elif [[ "${SOURCE}" == git@*:* ]]; then
    SOURCE_HOST_PATH="${SOURCE#git@}"
    SOURCE_HOST="${SOURCE_HOST_PATH%%:*}"
    SOURCE_PATH="${SOURCE_HOST_PATH#*:}"
    SOURCE="https://${SOURCE_HOST}/${SOURCE_PATH}"
    SOURCE="${SOURCE%.git}"
else
    SOURCE="${SOURCE%.git}"
fi

IMAGE_REF="${IMAGE_NAME}:${IMAGE_TAG}"
SAFE_TAG="${IMAGE_TAG//\//_}"
SAFE_PLATFORM="${PLATFORM//\//_}"
TAR_PATH="${OUTPUT_DIR}/${IMAGE_NAME}-${SAFE_TAG}-${SAFE_PLATFORM}.docker.tar"
CHECKSUM_PATH="${TAR_PATH}.sha256"

build_cmd=(docker build --platform "${PLATFORM}" -t "${IMAGE_REF}" --build-arg "VERSION=${VERSION}" --build-arg "REVISION=${REVISION}" --build-arg "CREATED=${CREATED}" --build-arg "SOURCE=${SOURCE}" "${REPO_ROOT}")
save_cmd=(docker save -o "${TAR_PATH}" "${IMAGE_REF}")

if [[ "${DRY_RUN}" == "true" ]]; then
    printf '%s\n' "docker build --platform ${PLATFORM} -t ${IMAGE_REF} --build-arg VERSION=${VERSION} --build-arg REVISION=${REVISION} --build-arg CREATED=${CREATED} --build-arg SOURCE=${SOURCE} ${REPO_ROOT}"
    printf '%s\n' "docker image inspect --format '{{.Id}}' ${IMAGE_REF}"
    printf '%s\n' "docker save -o ${TAR_PATH} ${IMAGE_REF}"
    printf '%s\n' "sha256sum ${TAR_PATH} > ${CHECKSUM_PATH}"
    exit 0
fi

mkdir -p "${OUTPUT_DIR}"

echo "Building ${IMAGE_REF} for ${PLATFORM}..."
"${build_cmd[@]}"

IMAGE_ID="$(docker image inspect --format '{{.Id}}' "${IMAGE_REF}")"
echo "Image ID: ${IMAGE_ID}"

echo "Saving archive to ${TAR_PATH}..."
"${save_cmd[@]}"

sha256sum "${TAR_PATH}" > "${CHECKSUM_PATH}"

echo "Done: ${TAR_PATH}"
echo "Checksum: ${CHECKSUM_PATH}"
