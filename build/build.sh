#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
VERSION="${VERSION:-dev}"
BUILD_TIME="$(date +"%Y-%m-%d_%H:%M:%S")"
GIT_COMMIT="$(git -C "${ROOT}" rev-parse --short HEAD 2>/dev/null || echo unknown)"
BIN_DIR="${ROOT}/bin"
TARGET_GOOS="${TARGET_GOOS:-${GOOS:-$(go env GOOS)}}"
TARGET_GOARCH="${TARGET_GOARCH:-${GOARCH:-$(go env GOARCH)}}"

if [[ "${1:-}" == "proto" ]]; then
  (cd "${ROOT}/modules/storage" && make proto)
  (cd "${ROOT}/modules/control/proto" && make all)
  exit 0
fi

mkdir -p "${BIN_DIR}"

build_go() {
  local module="$1"
  local package="$2"
  local output="$3"
  local cgo="${4:-0}"
  local tags="${5:-}"

  echo "==> build ${output} (${TARGET_GOOS}/${TARGET_GOARCH})"
  (
    cd "${ROOT}/${module}"
    if [[ -n "${tags}" ]]; then
      GOOS="${TARGET_GOOS}" GOARCH="${TARGET_GOARCH}" CGO_ENABLED="${cgo}" go build -tags "${tags}" \
        -ldflags "-X main.Version=${VERSION} -X main.BuildTime=${BUILD_TIME} -X main.GitCommit=${GIT_COMMIT}" \
        -o "${BIN_DIR}/${output}" "${package}"
    else
      GOOS="${TARGET_GOOS}" GOARCH="${TARGET_GOARCH}" CGO_ENABLED="${cgo}" go build \
        -ldflags "-X main.Version=${VERSION} -X main.BuildTime=${BUILD_TIME} -X main.GitCommit=${GIT_COMMIT}" \
        -o "${BIN_DIR}/${output}" "${package}"
    fi
  )
}

build_go modules/cli ./cmd/moox-cli moox-cli 0
build_go modules/control ./cmd/moox-server moox-server 0
build_go modules/collector ./cmd/moox-collector moox-collector 0
build_go modules/factor ./cmd/moox-factor moox-factor 0
build_go modules/order ./cmd/moox-order moox-order 0
build_go modules/account ./cmd/moox-account moox-account 0

echo "==> build moox-storage"
(
  cd "${ROOT}/modules/storage"
  if [[ -n "${STORAGE_BUILD_TAGS:-}" ]]; then
    GOOS="${TARGET_GOOS}" GOARCH="${TARGET_GOARCH}" CGO_ENABLED="${STORAGE_CGO_ENABLED:-1}" go build -tags "${STORAGE_BUILD_TAGS}" \
      -ldflags "-X main.Version=${VERSION} -X main.BuildTime=${BUILD_TIME} -X main.GitCommit=${GIT_COMMIT}" \
      -o "${BIN_DIR}/moox-storage" ./cmd/moox-storage
  else
    GOOS="${TARGET_GOOS}" GOARCH="${TARGET_GOARCH}" CGO_ENABLED="${STORAGE_CGO_ENABLED:-1}" go build \
      -ldflags "-X main.Version=${VERSION} -X main.BuildTime=${BUILD_TIME} -X main.GitCommit=${GIT_COMMIT}" \
      -o "${BIN_DIR}/moox-storage" ./cmd/moox-storage
  fi
)

echo "==> binaries written to ${BIN_DIR}"
