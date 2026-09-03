#!/usr/bin/env bash
#
# export.sh - Build the Docker image and export it to a local tar archive.

set -euo pipefail

if [[ "${1:-}" != "" ]]; then
  echo "Error: unknown option '${1}'."
  echo "Usage: ./export.sh"
  exit 1
fi

IMAGE_TAG="localhost/cli-proxy-api:latest"
OUTPUT_FILE="cli-proxy-api.tar"

VERSION="$(git describe --tags --always --dirty)"
COMMIT="$(git rev-parse --short HEAD)"
BUILD_DATE="$(date -u +%Y-%m-%dT%H:%M:%SZ)"

echo "Building with the following info:"
echo "  Version: ${VERSION}"
echo "  Commit: ${COMMIT}"
echo "  Build Date: ${BUILD_DATE}"
echo "----------------------------------------"

export CLI_PROXY_IMAGE="${IMAGE_TAG}"

echo "Building the Docker image..."
docker compose build \
  --build-arg VERSION="${VERSION}" \
  --build-arg COMMIT="${COMMIT}" \
  --build-arg BUILD_DATE="${BUILD_DATE}"

echo "Saving ${IMAGE_TAG} to ${OUTPUT_FILE}..."
docker save -o "${OUTPUT_FILE}" "${IMAGE_TAG}"

echo "Export complete: ${OUTPUT_FILE}"
