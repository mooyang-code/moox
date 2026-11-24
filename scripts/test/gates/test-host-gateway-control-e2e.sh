#!/usr/bin/env bash
set -euo pipefail

# 真实 Admin 加生产主机网关进程的联调：验签、调用名单、外部入口与来源头规则、快照下发、部署撤回与重启。
# 这个脚本不编译，两个纯 Go 产物由 run-e2e-gates.sh（make test-e2e-gates）备好；要单独跑时：
# GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go -C modules/hostgateway build \
#   -ldflags '-X main.Version=control-e2e' -o /tmp/moox-host-gateway-e2e ./cmd/server
# GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go -C modules/admin test -c \
#   -tags hostgateway_e2e -o /tmp/moox-admin-gateway-e2e ./internal/service/gatewaycontrol

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../../.." && pwd)"
: "${MOOX_HOST_GATEWAY_E2E_BINARY:?要指向预先编译的 moox-host-gateway}"
: "${MOOX_HOST_GATEWAY_E2E_TEST_BINARY:?要指向带 hostgateway_e2e 标签预先编译的 Admin 测试程序}"
if [[ "$(uname -s)" != Linux ]]; then
  printf '这条联调要 Linux 的回环地址 127.0.0.1 与 127.0.0.2：在本机编译后把产物拿到 Linux 上运行。\n' >&2
  exit 1
fi
export MOOX_HOST_GATEWAY_E2E_TRPC_CONFIG="${ROOT}/modules/hostgateway/config/trpc_go.yaml"
LOG="$(mktemp)"
trap 'rm -f "${LOG}"' EXIT
"${MOOX_HOST_GATEWAY_E2E_TEST_BINARY}" -test.run '^TestHostGatewayRealAdminE2E$' -test.v -test.timeout 6m | tee "${LOG}"
grep -Fq -- '--- PASS: TestHostGatewayRealAdminE2E' "${LOG}"
for scenario in \
  object-and-exact-raw-pb-json-local-and-tls \
  tls-trust-plaintext-acl-and-durable-replay \
  external-entry-cross-host-forward-acl-and-origin-caller-rules \
  new-caller-next-snapshot-without-restart \
  deployment-withdrawal-and-automatic-target-switch \
  normal-restart-and-live-instance-conflict \
  offline-routing-90-second-readiness-and-cache-restart; do
  grep -Fq -- "SCENARIO PASS ${scenario}" "${LOG}"
done
printf '真实 Admin 加主机网关的联调通过。\n'
