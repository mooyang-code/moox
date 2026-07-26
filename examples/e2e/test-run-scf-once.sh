#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
TMP="$(mktemp -d "${TMPDIR:-/tmp}/moox-e2e-scf.XXXXXX")"
trap 'rm -rf "${TMP}"' EXIT
deploy="${TMP}/deploy"
mkdir -p "${deploy}/bin" "${deploy}/secrets" "${deploy}/certs/gateway"
cat >"${deploy}/secrets/gateway-service.env" <<'EOF'
MOOX_GATEWAY_NODE_ID=gateway-test
MOOX_GATEWAY_SERVICE_KEY_ID=test-key
MOOX_GATEWAY_SERVICE_SECRET_KEY=test-secret-never-print
EOF
cat >"${deploy}/secrets/gateway-collector.key" <<'EOF'
test-secret-never-print
EOF
chmod 0600 "${deploy}/secrets/gateway-collector.key"
printf '%s\n' '-----BEGIN CERTIFICATE-----' 'Y2VydA==' '-----END CERTIFICATE-----' >"${deploy}/certs/gateway/peers.pem"
cat >"${deploy}/bin/moox-collector-scf" <<'EOF'
#!/usr/bin/env bash
set -euo pipefail
[[ "${MOOX_GATEWAY_NODE_ID}" == gateway-test ]]
[[ "${MOOX_GATEWAY_SERVICE_KEY_ID}" == collector ]]
[[ "${MOOX_GATEWAY_SERVICE_SECRET_KEY}" == test-secret-never-print ]]
[[ "${MOOX_SPACE_ID}" == crypto ]]
[[ -n "${MOOX_GATEWAY_CA_PEM_B64}" ]]
[[ -z "${MOOX_GATEWAY_CA_FILE:-}" ]]
printf '%s\n' "$*" >"${MOOX_E2E_TEST_ARGS}"
EOF
chmod +x "${deploy}/bin/moox-collector-scf"

args="${TMP}/args"
MOOX_E2E_TEST_ARGS="${args}" "${ROOT}/examples/e2e/run-scf-once.sh" "${deploy}" 120s collector-node crypto >"${TMP}/output" 2>&1
grep -Fq -- '-service-gateway-target http://127.0.0.1:11002' "${args}"
grep -Fq -- '-node-id collector-node' "${args}"
grep -Fq -- '-timeout 120s' "${args}"
! grep -Rq -- 'test-secret-never-print' "${TMP}/output"

: >"${deploy}/secrets/gateway-collector.key"
if MOOX_E2E_TEST_ARGS="${args}" "${ROOT}/examples/e2e/run-scf-once.sh" "${deploy}" 120s collector-node crypto >"${TMP}/missing-output" 2>&1; then
  echo 'missing Gateway secret unexpectedly accepted' >&2
  exit 1
fi
grep -Fq 'missing MOOX_GATEWAY_SERVICE_SECRET_KEY' "${TMP}/missing-output"
! grep -Rq -- 'test-secret-never-print' "${TMP}/missing-output"

echo 'E2E SCF Gateway contract passed'
