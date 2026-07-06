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
  (cd "${ROOT}" && make proto)
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
    local storage_cgo="${STORAGE_CGO_ENABLED:-${CGO_ENABLED:-1}}"
    if [[ -n "${STORAGE_BUILD_TAGS:-}" ]]; then
      GOOS="${TARGET_GOOS}" GOARCH="${TARGET_GOARCH}" CGO_ENABLED="${storage_cgo}" go build -tags "${STORAGE_BUILD_TAGS}" \
        -ldflags "-X main.Version=${VERSION} -X main.BuildTime=${BUILD_TIME} -X main.GitCommit=${GIT_COMMIT}" \
        -o "${BIN_DIR}/moox-storage" ./cmd/server
    else
      GOOS="${TARGET_GOOS}" GOARCH="${TARGET_GOARCH}" CGO_ENABLED="${storage_cgo}" go build \
        -ldflags "-X main.Version=${VERSION} -X main.BuildTime=${BUILD_TIME} -X main.GitCommit=${GIT_COMMIT}" \
        -o "${BIN_DIR}/moox-storage" ./cmd/server
    fi
  )
}

build_storage_cli() {
  build_go modules/storage ./cmd/cli moox-storage-cli 1
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
    build_go modules/admin ./cmd/server moox-admin 0
    build_go modules/admin ./cmd/cli moox-admin-cli 0
    build_web_host
    build_go modules/cloudnode ./cmd/server moox-cloudnode 0
    build_go modules/cloudnode ./cmd/cli moox-cloudnode-cli 0
    build_go modules/collector ./cmd/server moox-collector 0
    build_go modules/collector ./cmd/cli moox-collector-cli 0
    build_go modules/collector ./cmd/scf moox-collector-scf 0
    build_go modules/factor ./cmd/server moox-factor 0
    build_go modules/factor ./cmd/cli moox-factor-cli 0
    build_go modules/trade ./cmd/server moox-trade 0
    build_go modules/trade ./cmd/cli moox-trade-cli 0
    build_storage
    build_storage_cli
    ;;
  cli)
    build_go modules/cli ./cmd/moox-cli moox-cli 0
    ;;
  admin)
    build_go modules/admin ./cmd/server moox-admin 0
    build_go modules/admin ./cmd/cli moox-admin-cli 0
    ;;
  admin-cli)
    build_go modules/admin ./cmd/cli moox-admin-cli 0
    ;;
  cloudnode)
    build_go modules/cloudnode ./cmd/server moox-cloudnode 0
    build_go modules/cloudnode ./cmd/cli moox-cloudnode-cli 0
    ;;
  cloudnode-cli)
    build_go modules/cloudnode ./cmd/cli moox-cloudnode-cli 0
    ;;
  collector)
    build_go modules/collector ./cmd/server moox-collector 0
    build_go modules/collector ./cmd/cli moox-collector-cli 0
    ;;
  collector-cli)
    build_go modules/collector ./cmd/cli moox-collector-cli 0
    ;;
  collector-scf)
    build_go modules/collector ./cmd/scf moox-collector-scf 0
    ;;
  factor)
    build_go modules/factor ./cmd/server moox-factor 0
    build_go modules/factor ./cmd/cli moox-factor-cli 0
    ;;
  trade)
    build_go modules/trade ./cmd/server moox-trade 0
    build_go modules/trade ./cmd/cli moox-trade-cli 0
    ;;
  trade-cli)
    build_go modules/trade ./cmd/cli moox-trade-cli 0
    ;;
  storage)
    build_storage
    build_storage_cli
    ;;
  storage-cli)
    build_storage_cli
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
