#!/usr/bin/env bash
set -euo pipefail

# 编译并运行 scripts/test/gates/ 下起真实进程的联调（make test-e2e-gates）：
#   - EventBus 用主机证书（各系统都跑）；
#   - 真实 Admin 加主机网关（要 Linux 的回环地址 127.0.0.2，别的系统上跳过并说明）。
# 产物编到临时目录，结束后删除；全部是纯 Go，不需要 CGO，也不联网下载工具。

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../../.." && pwd)"
OUT="$(mktemp -d "${TMPDIR:-/tmp}/moox-e2e-gates.XXXXXX")"
trap 'rm -rf "${OUT}"' EXIT
export CGO_ENABLED=0

echo '==> 编译 EventBus 联调的产物'
go -C "${ROOT}/modules/eventbus" build -o "${OUT}/moox-eventbus" ./cmd/server
go -C "${ROOT}/modules/admin" test -c -tags eventbus_e2e -o "${OUT}/admin-eventbus-e2e" ./cmd/cli
MOOX_EVENTBUS_E2E_BINARY="${OUT}/moox-eventbus" MOOX_EVENTBUS_E2E_TEST_BINARY="${OUT}/admin-eventbus-e2e" \
  bash "${ROOT}/scripts/test/gates/test-eventbus-host-cert-e2e.sh"

if [[ "$(uname -s)" != Linux ]]; then
  echo '跳过真实 Admin 加主机网关的联调：它要 Linux 的回环地址 127.0.0.2，这台机器不是 Linux（PR 的 CI 在 Linux 运行器上执行）。'
  exit 0
fi

echo '==> 编译主机网关联调的产物'
go -C "${ROOT}/modules/hostgateway" build -ldflags '-X main.Version=control-e2e' -o "${OUT}/moox-host-gateway" ./cmd/server
go -C "${ROOT}/modules/admin" test -c -tags hostgateway_e2e -o "${OUT}/admin-gateway-e2e" ./internal/service/gatewaycontrol
MOOX_HOST_GATEWAY_E2E_BINARY="${OUT}/moox-host-gateway" MOOX_HOST_GATEWAY_E2E_TEST_BINARY="${OUT}/admin-gateway-e2e" \
  bash "${ROOT}/scripts/test/gates/test-host-gateway-control-e2e.sh"
