#!/usr/bin/env bash
set -euo pipefail

# Build the two pure Go artifacts on the developer/CI build host. This runner
# deliberately does no compilation; Linux may be a separate execution host.
# GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go -C modules/hostgateway build \
#   -ldflags '-X main.Version=control-e2e' -o /tmp/moox-host-gateway-e2e ./cmd/server
# GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go -C modules/admin test -c \
#   -tags hostgateway_e2e -o /tmp/moox-admin-gateway-e2e ./internal/service/gatewaycontrol

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../../.." && pwd)"
: "${MOOX_HOST_GATEWAY_E2E_BINARY:?provide the prebuilt production gateway executable}"
: "${MOOX_HOST_GATEWAY_E2E_TEST_BINARY:?provide the prebuilt tagged Admin test executable}"
if [[ "$(uname -s)" != Linux ]]; then
  printf 'This gate needs Linux loopback IPs 127.0.0.1 and 127.0.0.2; build locally, then run the artifacts on Linux.\n' >&2
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
  new-caller-next-snapshot-without-restart \
  placement-withdrawal-and-automatic-target-switch \
  normal-restart-and-live-instance-conflict \
  offline-routing-90-second-readiness-and-cache-restart; do
  grep -Fq -- "SCENARIO PASS ${scenario}" "${LOG}"
done
printf 'Real Admin and production host gateway E2E passed.\n'
