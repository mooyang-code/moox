#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
VERSION="${VERSION:-dev}"
BUILD_TIME="$(date +"%Y-%m-%d_%H:%M:%S")"
GIT_COMMIT="$(git -C "${ROOT}" rev-parse --short HEAD 2>/dev/null || echo unknown)"
BIN_DIR="${ROOT}/bin"
TARGET_GOOS="${TARGET_GOOS:-${GOOS:-$(go env GOOS)}}"
TARGET_GOARCH="${TARGET_GOARCH:-${GOARCH:-$(go env GOARCH)}}"
TARGET_MODULE="${1:-all}"

if [[ "${TARGET_MODULE}" == "proto" ]]; then
  (cd "${ROOT}/modules/storage" && make proto)
  (cd "${ROOT}/modules/admin/proto" && make all)
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

build_storage() {
  echo "==> build moox-storage"
  (
    cd "${ROOT}/modules/storage"
    if [[ -n "${STORAGE_BUILD_TAGS:-}" ]]; then
      GOOS="${TARGET_GOOS}" GOARCH="${TARGET_GOARCH}" CGO_ENABLED=1 go build -tags "${STORAGE_BUILD_TAGS}" \
        -ldflags "-X main.Version=${VERSION} -X main.BuildTime=${BUILD_TIME} -X main.GitCommit=${GIT_COMMIT}" \
        -o "${BIN_DIR}/moox-storage" ./cmd/moox-storage
    else
      GOOS="${TARGET_GOOS}" GOARCH="${TARGET_GOARCH}" CGO_ENABLED=1 go build \
        -ldflags "-X main.Version=${VERSION} -X main.BuildTime=${BUILD_TIME} -X main.GitCommit=${GIT_COMMIT}" \
        -o "${BIN_DIR}/moox-storage" ./cmd/moox-storage
    fi
  )
}

build_web_host() {
  echo "==> build moox-web-host"
  (
    cd "${ROOT}/web-host"
    GOOS="${TARGET_GOOS}" GOARCH="${TARGET_GOARCH}" CGO_ENABLED=0 go build \
      -ldflags "-X main.Version=${VERSION} -X main.BuildTime=${BUILD_TIME} -X main.GitCommit=${GIT_COMMIT}" \
      -o "${BIN_DIR}/moox-web-host" .
  )
}

case "${TARGET_MODULE}" in
  all)
    build_go modules/cli ./cmd/moox-cli moox-cli 0
    build_go modules/admin ./cmd/moox-admin moox-admin 0
    build_go modules/collector ./cmd/moox-collector moox-collector 0
    build_go modules/factor ./cmd/moox-factor moox-factor 0
    build_go modules/trade ./cmd/moox-trade moox-trade 0
    build_storage
    ;;
  cli)
    build_go modules/cli ./cmd/moox-cli moox-cli 0
    ;;
  admin)
    build_go modules/admin ./cmd/moox-admin moox-admin 0
    ;;
  collector)
    build_go modules/collector ./cmd/moox-collector moox-collector 0
    ;;
  factor)
    build_go modules/factor ./cmd/moox-factor moox-factor 0
    ;;
  trade)
    build_go modules/trade ./cmd/moox-trade moox-trade 0
    ;;
  account)
    echo "==> skip moox-account: modules/account not present in this repo" >&2
    ;;
  storage)
    build_storage
    ;;
  web-host)
    build_web_host
    ;;
  *)
    echo "unknown build target: ${TARGET_MODULE}" >&2
    exit 1
    ;;
esac

echo "==> binaries written to ${BIN_DIR}"
