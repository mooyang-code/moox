#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
TMP="$(mktemp -d "${TMPDIR:-/tmp}/moox-e2e-scf-resident.XXXXXX")"
trap 'rm -rf "${TMP}"' EXIT
deploy="${TMP}/deploy"
mkdir -p "${deploy}/bin" "${deploy}/secrets" "${deploy}/certs/gateway"
cat >"${deploy}/secrets/gateway-service.env" <<'EOF'
MOOX_GATEWAY_NODE_ID=gateway-test
MOOX_GATEWAY_SERVICE_KEY_ID=ignored
MOOX_GATEWAY_SERVICE_SECRET_KEY=ignored
EOF
cat >"${deploy}/secrets/gateway-collector.key" <<'EOF'
test-secret-never-print
EOF
chmod 0600 "${deploy}/secrets/gateway-collector.key"
printf '%s\n' '-----BEGIN CERTIFICATE-----' 'Y2VydA==' '-----END CERTIFICATE-----' >"${deploy}/certs/gateway/peers.pem"
cat >"${deploy}/bin/moox-collector-scf" <<'EOF'
#!/usr/bin/env bash
set -euo pipefail
[[ "${MOOX_GATEWAY_SERVICE_KEY_ID}" == collector ]]
[[ "${MOOX_GATEWAY_SERVICE_SECRET_KEY}" == test-secret-never-print ]]
[[ "${MOOX_SPACE_ID}" == crypto ]]
[[ -n "${MOOX_GATEWAY_CA_PEM_B64}" ]]
[[ -z "${MOOX_GATEWAY_CA_FILE:-}" ]]
[[ "$*" != *"-once"* ]]
[[ "$*" == *"-resident"* ]]
printf '%s\n' "$*" >"${MOOX_E2E_TEST_ARGS}"
while :; do sleep 1; done
EOF
chmod +x "${deploy}/bin/moox-collector-scf"

args="${TMP}/args"
MOOX_E2E_TEST_ARGS="${args}" "${ROOT}/examples/e2e/run-scf-resident.sh" "${deploy}" collector-node crypto &
runner_pid=$!
trap 'kill "${runner_pid}" 2>/dev/null || true; wait "${runner_pid}" 2>/dev/null || true; rm -rf "${TMP}"' EXIT
for _ in $(seq 1 30); do
  [[ -s "${args}" ]] && break
  sleep 0.1
done
[[ -s "${args}" ]]
grep -Fq -- '-service-gateway-target http://127.0.0.1:11002' "${args}"
grep -Fq -- '-node-id collector-node' "${args}"
grep -Fq -- '-resident' "${args}"
! grep -Fq -- '-once' "${args}"
kill "${runner_pid}"
wait "${runner_pid}" 2>/dev/null || true

help="$("${ROOT}/examples/e2e/run.sh" --help)"
grep -Fq -- 'Runtime/assert timeout. Default: 120.' <<<"${help}"
grep -Fq -- 'start resident collector SCF runtime' "${ROOT}/examples/e2e/run.sh"

echo 'E2E resident SCF contract passed'
