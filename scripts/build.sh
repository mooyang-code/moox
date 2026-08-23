#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
VERSION="${VERSION:-dev}"
BUILD_TIME="$(date +"%Y-%m-%d_%H:%M:%S")"
GIT_COMMIT="${GIT_COMMIT:-$(git -C "${ROOT}" rev-parse --short HEAD 2>/dev/null || echo unknown)}"
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
        -o "${BIN_DIR}/$(binary_name "${output}")" "${package}"
    else
      GOOS="${TARGET_GOOS}" GOARCH="${TARGET_GOARCH}" CGO_ENABLED="${cgo}" go build \
        -ldflags "-X main.Version=${VERSION} -X main.BuildTime=${BUILD_TIME} -X main.GitCommit=${GIT_COMMIT}" \
        -o "${BIN_DIR}/$(binary_name "${output}")" "${package}"
    fi
  )
}

binary_name() {
  local name="$1"
  if [[ "${TARGET_GOOS}" == "windows" ]]; then
    printf '%s.exe' "${name}"
  else
    printf '%s' "${name}"
  fi
}

build_storage() {
	  echo "==> build storage processes"
	  (
	    cd "${ROOT}/modules/storage"
	    local storage_cgo="${STORAGE_CGO_ENABLED:-${CGO_ENABLED:-1}}"
	    local tags=()
	    if [[ -n "${STORAGE_BUILD_TAGS:-}" ]]; then tags=(-tags "${STORAGE_BUILD_TAGS}"); fi
	    for role in primary node view; do
	      if ((${#tags[@]})); then
	        GOOS="${TARGET_GOOS}" GOARCH="${TARGET_GOARCH}" CGO_ENABLED="${storage_cgo}" go build "${tags[@]}" \
	          -ldflags "-X main.Version=${VERSION} -X main.BuildTime=${BUILD_TIME} -X main.GitCommit=${GIT_COMMIT}" \
	          -o "${BIN_DIR}/$(binary_name moox-storage-${role})" ./cmd/server
	      else
	        GOOS="${TARGET_GOOS}" GOARCH="${TARGET_GOARCH}" CGO_ENABLED="${storage_cgo}" go build \
	          -ldflags "-X main.Version=${VERSION} -X main.BuildTime=${BUILD_TIME} -X main.GitCommit=${GIT_COMMIT}" \
	          -o "${BIN_DIR}/$(binary_name moox-storage-${role})" ./cmd/server
	      fi
	    done
	  )
}

build_storage_node() {
	local storage_cgo="${STORAGE_CGO_ENABLED:-${CGO_ENABLED:-1}}"
	(
		cd "${ROOT}/modules/storage"
		GOOS="${TARGET_GOOS}" GOARCH="${TARGET_GOARCH}" CGO_ENABLED="${storage_cgo}" go build \
			-ldflags "-X main.Version=${VERSION} -X main.BuildTime=${BUILD_TIME} -X main.GitCommit=${GIT_COMMIT}" \
			-o "${BIN_DIR}/$(binary_name moox-storage-node)" ./cmd/server
	)
}

build_storage_cli() {
  local storage_cgo="${STORAGE_CGO_ENABLED:-${CGO_ENABLED:-1}}"
  build_go modules/storage ./cmd/cli moox-storage-cli "${storage_cgo}"
}

build_web_host() {
  echo "==> build moox-web-host"
  (
    cd "${ROOT}/web-host"
    GOOS="${TARGET_GOOS}" GOARCH="${TARGET_GOARCH}" CGO_ENABLED=0 go build \
      -ldflags "-X main.Version=${VERSION} -X main.BuildTime=${BUILD_TIME} -X main.GitCommit=${GIT_COMMIT}" \
      -o "${BIN_DIR}/$(binary_name moox-web-host)" .
  )
}

build_archive() {
  build_go modules/archive ./cmd/server moox-archive 0
  build_go modules/archive ./cmd/cli moox-archive-cli 0
}

build_hostagent() {
  [[ "${TARGET_GOOS}" == "linux" ]] || { echo "moox-host-agent supports linux only" >&2; exit 1; }
  case "${TARGET_GOARCH}" in amd64|arm64) ;; *) echo "moox-host-agent supports amd64/arm64 only" >&2; exit 1 ;; esac
  build_go modules/hostagent ./cmd/server moox-host-agent 0
  build_go modules/hostagent ./cmd/cli moox-host-agent-cli 0
}

build_collector_scf() {
  [[ "${TARGET_GOOS}" == "linux" && "${TARGET_GOARCH}" == "amd64" ]] || {
    echo "moox-collector-scf supports linux/amd64 only" >&2
    exit 1
  }
  build_go modules/collector ./cmd/scf/crypto_market moox-collector-scf 0
}

case "${TARGET_MODULE}" in
  all)
    build_go modules/cli ./cmd/moox-cli moox-cli 0
    build_go modules/admin ./cmd/server moox-admin 0
    build_go modules/admin ./cmd/cli moox-admin-cli 0
    build_go modules/gateway ./cmd/server moox-gateway 0
    build_go modules/gateway ./cmd/cli moox-gateway-cli 0
    build_go modules/eventbus ./cmd/server moox-eventbus 0
    build_web_host
    build_go modules/cloudnode ./cmd/server moox-cloudnode 0
    build_go modules/cloudnode ./cmd/cli moox-cloudnode-cli 0
    build_go modules/collector ./cmd/server moox-collector 0
    build_go modules/collector ./cmd/cli moox-collector-cli 0
    build_go modules/factor ./cmd/server moox-factor 0
    build_go modules/factor ./cmd/cli moox-factor-cli 0
    build_go modules/strategy ./cmd/server moox-strategy 0
    build_go modules/strategy ./cmd/cli moox-strategy-cli 0
    build_go modules/trade ./cmd/server moox-trade 0
    build_go modules/trade ./cmd/cli moox-trade-cli 0
    build_go modules/monitor ./cmd/server moox-monitor 0
    build_go modules/monitor ./cmd/cli moox-monitor-cli 0
    if [[ "${TARGET_GOOS}" == "linux" ]]; then build_hostagent; fi
    build_storage
    build_storage_cli
    build_archive
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
  gateway)
    build_go modules/gateway ./cmd/server moox-gateway 0
    build_go modules/gateway ./cmd/cli moox-gateway-cli 0
    ;;
  eventbus)
    build_go modules/eventbus ./cmd/server moox-eventbus 0
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
    build_collector_scf
    ;;
  factor)
    build_go modules/factor ./cmd/server moox-factor 0
    build_go modules/factor ./cmd/cli moox-factor-cli 0
    ;;
  strategy)
    build_go modules/strategy ./cmd/server moox-strategy 0
    build_go modules/strategy ./cmd/cli moox-strategy-cli 0
    ;;
  strategy-cli)
    build_go modules/strategy ./cmd/cli moox-strategy-cli 0
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
  storage-node)
    build_storage_node
    ;;
  storage-cli)
    build_storage_cli
    ;;
  archive)
    build_archive
    ;;
  monitor)
    build_go modules/monitor ./cmd/server moox-monitor 0
    build_go modules/monitor ./cmd/cli moox-monitor-cli 0
    ;;
  monitor-cli)
    build_go modules/monitor ./cmd/cli moox-monitor-cli 0
    ;;
  hostagent)
    build_hostagent
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
