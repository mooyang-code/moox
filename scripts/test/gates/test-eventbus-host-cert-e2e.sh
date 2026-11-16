#!/usr/bin/env bash
set -euo pipefail

# 真实 EventBus 进程联调：服务端用所在主机的主机证书，客户端用 MooX CA 根证书校验，主题权限按角色令牌。
# 这个脚本不编译，先在构建机上备好两个产物（Linux 上跑就按 Linux 编译）：
# go -C modules/eventbus build -o /tmp/moox-eventbus-e2e ./cmd/server
# go -C modules/admin test -c -tags eventbus_e2e -o /tmp/moox-admin-eventbus-e2e ./cmd/cli

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../../.." && pwd)"
: "${MOOX_EVENTBUS_E2E_BINARY:?要指向预先编译的 moox-eventbus}"
: "${MOOX_EVENTBUS_E2E_TEST_BINARY:?要指向带 eventbus_e2e 标签预先编译的 Admin CLI 测试程序}"
export MOOX_EVENTBUS_E2E_CONFIG_DIR="${ROOT}/modules/eventbus/config"
LOG="$(mktemp)"
trap 'rm -f "${LOG}"' EXIT
"${MOOX_EVENTBUS_E2E_TEST_BINARY}" -test.run '^TestEventBusHostCertificateRealProcessE2E$' -test.v -test.timeout 5m | tee "${LOG}"
grep -Fq -- '--- PASS: TestEventBusHostCertificateRealProcessE2E' "${LOG}"
for scenario in \
  host-certificate-and-role-acl \
  certificate-from-another-ca-is-rejected \
  certificate-of-another-host-is-rejected \
  broker-without-certificate-does-not-start; do
  grep -Fq -- "SCENARIO PASS ${scenario}" "${LOG}"
done
printf '真实 EventBus 进程的主机证书联调通过。\n'
