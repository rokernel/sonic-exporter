#!/usr/bin/env bash
set -euo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
DEFAULT_IMAGE_NAME="sonic-exporter"
DEFAULT_IMAGE_TAG="smoke-test"
DEFAULT_PORT="19101"
IMAGE_NAME="${IMAGE_NAME-${DEFAULT_IMAGE_NAME}}"
IMAGE_TAG="${IMAGE_TAG-${DEFAULT_IMAGE_TAG}}"
IMAGE_REF="${IMAGE_NAME}:${IMAGE_TAG}"
PORT="${DEFAULT_PORT}"
DRY_RUN="false"
BUILD_IMAGE="true"
CONTAINER_NAME="sonic-exporter-smoke-$$"
METRICS_FILE=""

usage() {
    cat <<'EOF'
Usage: scripts/smoke-image.sh [--image <image>] [--no-build] [--port <host_port>] [--dry-run]

Builds or reuses a local Docker image, runs sonic-exporter, and checks /metrics.
EOF
}

print_cmd() {
    local -a cmd=()
    local arg

    for arg in "$@"; do
        cmd+=("$(printf '%q' "$arg")")
    done

    printf '%s\n' "${cmd[*]}"
}

die() {
    printf 'Error: %s\n' "$1" >&2
    exit 1
}

cleanup() {
    if [[ -n "${METRICS_FILE}" && -f "${METRICS_FILE}" ]]; then
        rm -f "${METRICS_FILE}"
    fi

    if [[ "${DRY_RUN}" == "false" ]]; then
        docker rm -f "${CONTAINER_NAME}" >/dev/null 2>&1 || true
    fi
}

print_failure_logs() {
    printf 'Container logs from %s:\n' "${CONTAINER_NAME}" >&2
    docker logs "${CONTAINER_NAME}" >&2 || true
}

trap cleanup EXIT

while (($# > 0)); do
    case "$1" in
        --image)
            [[ $# -ge 2 ]] || die "--image requires a value"
            IMAGE_REF="$2"
            BUILD_IMAGE="false"
            shift 2
            ;;
        --no-build)
            BUILD_IMAGE="false"
            shift
            ;;
        --port)
            [[ $# -ge 2 ]] || die "--port requires a value"
            PORT="$2"
            shift 2
            ;;
        --dry-run)
            DRY_RUN="true"
            shift
            ;;
        -h|--help)
            usage
            exit 0
            ;;
        *)
            die "unknown argument: $1"
            ;;
    esac
done

[[ "${PORT}" =~ ^[0-9]+$ ]] || die "--port must be a number"
[[ "${PORT}" != "0" ]] || die "--port must be greater than zero"
[[ -n "${IMAGE_REF}" ]] || die "image must not be empty"

BUILD_SCRIPT="${REPO_ROOT}/scripts/build-image.sh"
RUN_CMD=(docker run -d --name "${CONTAINER_NAME}" -p "${PORT}:9101" "${IMAGE_REF}")
CURL_CMD=(curl -fsS "http://127.0.0.1:${PORT}/metrics")
LOGS_CMD=(docker logs "${CONTAINER_NAME}")
RM_CMD=(docker rm -f "${CONTAINER_NAME}")

if [[ "${DRY_RUN}" == "true" ]]; then
    if [[ "${BUILD_IMAGE}" == "true" ]]; then
        print_cmd IMAGE_NAME="${IMAGE_NAME}" IMAGE_TAG="${IMAGE_TAG}" bash "${BUILD_SCRIPT}" --dry-run
    else
        printf '%s\n' "# using existing image: ${IMAGE_REF}"
    fi
    print_cmd "${RUN_CMD[@]}"
    print_cmd "${CURL_CMD[@]}"
    print_cmd "${LOGS_CMD[@]}"
    print_cmd "${RM_CMD[@]}"
    exit 0
fi

if [[ "${BUILD_IMAGE}" == "true" ]]; then
    IMAGE_NAME="${IMAGE_NAME}" IMAGE_TAG="${IMAGE_TAG}" bash "${BUILD_SCRIPT}"
fi

METRICS_FILE="$(mktemp)"

if ! docker run -d --name "${CONTAINER_NAME}" -p "${PORT}:9101" "${IMAGE_REF}" >/dev/null; then
    print_failure_logs
    exit 1
fi

attempt=1
max_attempts=30
success="false"

while (( attempt <= max_attempts )); do
    if curl -fsS "http://127.0.0.1:${PORT}/metrics" >"${METRICS_FILE}"; then
        if grep -q '^# HELP ' "${METRICS_FILE}"; then
            success="true"
            break
        fi
    fi

    sleep 1
    attempt=$((attempt + 1))
done

if [[ "${success}" != "true" ]]; then
    printf 'Smoke test failed after %s attempts\n' "${max_attempts}" >&2
    print_failure_logs
    exit 1
fi

printf 'Smoke test passed on http://127.0.0.1:%s/metrics\n' "${PORT}"
