#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
bash "${ROOT}/scripts/ci/check-go-version.sh"
export GOTOOLCHAIN=local
VERSION="${VERSION:-dev}"
BUILD_TIME="$(date +"%Y-%m-%d_%H:%M:%S")"
GIT_COMMIT="${GIT_COMMIT:-$(git -C "${ROOT}" rev-parse --short HEAD 2>/dev/null || echo unknown)}"
BIN_DIR="${BIN_DIR:-${ROOT}/bin}"
# 版本号写两处：main.Version（有这个变量的 main 包：CLI、因子引擎、归档 worker 等）和 packages/report.BuildVersion
# （各组件经 report 上报给 Monitor 的版本；tRPC 服务的 main 包没有 Version 变量，只写前者时它们上报的都是 dev）。
# -X 指向没有链接进来的包或不存在的变量时会被忽略，所以两项对所有目标都带上。
REPORT_VERSION_VAR="github.com/mooyang-code/moox/packages/report.BuildVersion"
LDFLAGS="-X main.Version=${VERSION} -X ${REPORT_VERSION_VAR}=${VERSION} -X main.BuildTime=${BUILD_TIME} -X main.GitCommit=${GIT_COMMIT}"
# GO_STRIP=1 去掉符号表与调试信息（发布产物用，体积约减半；panic 堆栈仍然完整）。
if [[ "${GO_STRIP:-}" == "1" ]]; then LDFLAGS="-s -w ${LDFLAGS}"; fi
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
        -ldflags "${LDFLAGS}" \
        -o "${BIN_DIR}/$(binary_name "${output}")" "${package}"
    else
      GOOS="${TARGET_GOOS}" GOARCH="${TARGET_GOARCH}" CGO_ENABLED="${cgo}" go build \
        -ldflags "${LDFLAGS}" \
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
	    for role in primary view; do
	      if ((${#tags[@]})); then
	        GOOS="${TARGET_GOOS}" GOARCH="${TARGET_GOARCH}" CGO_ENABLED="${storage_cgo}" go build "${tags[@]}" \
	          -ldflags "${LDFLAGS}" \
	          -o "${BIN_DIR}/$(binary_name moox-storage-${role})" ./cmd/server
	      else
	        GOOS="${TARGET_GOOS}" GOARCH="${TARGET_GOARCH}" CGO_ENABLED="${storage_cgo}" go build \
	          -ldflags "${LDFLAGS}" \
	          -o "${BIN_DIR}/$(binary_name moox-storage-${role})" ./cmd/server
	      fi
	    done
	  )
}

build_storage_cli() {
  build_go modules/storage ./cmd/cli moox-storage-cli 0
}

# 管理台前端内嵌在 moox-admin 里（modules/admin/internal/webui），一律按当前源码现场构建：
# 默认先用 build-web-docker.sh 从当前源码构建 web/dist；调用方已经构建好 web/dist 时设 MOOX_WEB_DIST_READY=1 跳过这一步。
# 编译前把 web/dist 拷进 webui/dist，编译完删掉，只留占位的 .gitkeep，不改动任何已提交的文件，
# 下一次不经本脚本的编译也不会带上过时的前端（2026-11-10 发生过按提交编译带上过时前端、线上整体回退）。
build_admin_server() {
  if [[ "${MOOX_WEB_DIST_READY:-}" != "1" ]]; then
    bash "${ROOT}/scripts/build/build-web-docker.sh"
  fi
  if [[ ! -d "${ROOT}/web/dist/static/js" ]]; then
    echo "web/dist 不完整（没有 static/js）：先用 scripts/build/build-web-docker.sh 构建前端" >&2
    exit 1
  fi
  local webui="${ROOT}/modules/admin/internal/webui/dist"
  (
    clean_webui() { find "${webui}" -mindepth 1 -maxdepth 1 ! -name .gitkeep -exec rm -rf {} +; }
    trap clean_webui EXIT
    clean_webui
    cp -R "${ROOT}/web/dist/." "${webui}/"
    build_go modules/admin ./cmd/server moox-admin 0
  )
  echo "==> moox-admin 内嵌前端：$(grep -o 'static/js/index-[^"]*\.js' "${ROOT}/web/dist/index.html" | head -1)"
}

build_hostagent() {
  [[ "${TARGET_GOOS}" == "linux" ]] || { echo "moox-host-agent supports linux only" >&2; exit 1; }
  case "${TARGET_GOARCH}" in amd64|arm64) ;; *) echo "moox-host-agent supports amd64/arm64 only" >&2; exit 1 ;; esac
  build_go modules/hostagent ./cmd/server moox-host-agent 0
}

build_collector_market_data_scf() {
  [[ "${TARGET_GOOS}" == "linux" && "${TARGET_GOARCH}" == "amd64" ]] || {
    echo "moox-collector-scf supports linux/amd64 only" >&2
    exit 1
  }
  build_go modules/collector ./cmd/scf/market_data moox-collector-scf 0
}

case "${TARGET_MODULE}" in
  all)
    build_go modules/cli ./cmd/moox-cli moox-cli 0
    build_admin_server
    build_go modules/admin ./cmd/cli moox-admin-cli 0
    build_go modules/hostgateway ./cmd/server moox-host-gateway 0
    build_go modules/hostgateway ./cmd/cli moox-host-gateway-cli 0
    build_go modules/eventbus ./cmd/server moox-eventbus 0
    build_go modules/egressproxy ./cmd/server moox-egress-proxy 0
    build_go modules/collector ./cmd/server moox-collector 0
    build_go modules/collector ./cmd/cli moox-collector-cli 0
    build_go modules/collector ./cmd/stream moox-collector-stream 0
    build_go modules/factor ./cmd/mgr moox-factor-mgr 0
    build_go modules/factor ./cmd/engine moox-factor-engine 0
    build_go modules/archive ./cmd/mgr moox-archive-mgr 0
    build_go modules/archive ./cmd/worker moox-archive-worker 0
    build_go modules/strategy ./cmd/server moox-strategy 0
    build_go modules/trade ./cmd/server moox-trade 0
    build_go modules/monitor ./cmd/server moox-monitor 0
    if [[ "${TARGET_GOOS}" == "linux" ]]; then build_hostagent; fi
    build_storage
    build_storage_cli
    ;;
  cli)
    build_go modules/cli ./cmd/moox-cli moox-cli 0
    ;;
  admin)
    build_admin_server
    build_go modules/admin ./cmd/cli moox-admin-cli 0
    ;;
  admin-cli)
    build_go modules/admin ./cmd/cli moox-admin-cli 0
    ;;
  host-gateway)
    build_go modules/hostgateway ./cmd/server moox-host-gateway 0
    build_go modules/hostgateway ./cmd/cli moox-host-gateway-cli 0
    ;;
  eventbus)
    build_go modules/eventbus ./cmd/server moox-eventbus 0
    ;;
  egress-proxy)
    build_go modules/egressproxy ./cmd/server moox-egress-proxy 0
    ;;
  collector)
    build_go modules/collector ./cmd/server moox-collector 0
    build_go modules/collector ./cmd/cli moox-collector-cli 0
    ;;
  collector-cli)
    build_go modules/collector ./cmd/cli moox-collector-cli 0
    ;;
  collector-stream)
    # 纯 Go，可交叉编译；部署在海外机（香港）的 collector-stream 单元。
    build_go modules/collector ./cmd/stream moox-collector-stream 0
    ;;
  collector-scf)
    build_collector_market_data_scf
    ;;
  factor-mgr)
    build_go modules/factor ./cmd/mgr moox-factor-mgr 0
    ;;
  factor-engine)
    build_go modules/factor ./cmd/engine moox-factor-engine 0
    ;;
  archive-mgr)
    build_go modules/archive ./cmd/mgr moox-archive-mgr 0
    ;;
  archive-worker)
    # 纯 Go（暂存库是 SQLite），可在任意平台交叉编译 Linux / macOS / Windows 版本。
    build_go modules/archive ./cmd/worker moox-archive-worker 0
    ;;
  strategy)
    build_go modules/strategy ./cmd/server moox-strategy 0
    ;;
  trade)
    build_go modules/trade ./cmd/server moox-trade 0
    ;;
  storage)
    build_storage
    build_storage_cli
    ;;
  storage-cgo)
    build_storage
    ;;
  storage-primary)
    build_go modules/storage ./cmd/server moox-storage-primary 1
    ;;
  storage-cli)
    build_storage_cli
    ;;
  monitor)
    build_go modules/monitor ./cmd/server moox-monitor 0
    ;;
  hostagent)
    build_hostagent
    ;;
  *)
    echo "unknown build target: ${TARGET_MODULE}" >&2
    exit 1
    ;;
esac

echo "==> binaries written to ${BIN_DIR}"
