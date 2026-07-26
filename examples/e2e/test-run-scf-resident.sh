#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
TMP="$(mktemp -d "${TMPDIR:-/tmp}/moox-e2e-scf-resident.XXXXXX")"
trap 'rm -rf "${TMP}"' EXIT
deploy="${TMP}/deploy"
mkdir -p "${deploy}/bin" "${deploy}/secrets" "${deploy}/certs/gateway" "${deploy}/collector/configs" "${TMP}/eventbus"
cat >"${deploy}/secrets/gateway-service.env" <<'EOF'
MOOX_GATEWAY_NODE_ID=gateway-test
MOOX_GATEWAY_SERVICE_KEY_ID=ignored
MOOX_GATEWAY_SERVICE_SECRET_KEY=ignored
EOF
cat >"${deploy}/secrets/gateway-collector.key" <<'EOF'
test-secret-never-print
EOF
cat >"${deploy}/secrets/storage-internal-auth.env" <<'EOF'
MOOX_STORAGE_PRIMARY_AUTH_SECRET=storage-secret-never-print
EOF
chmod 0600 "${deploy}/secrets/gateway-collector.key"
chmod 0600 "${deploy}/secrets/storage-internal-auth.env"
printf '%s\n' '-----BEGIN CERTIFICATE-----' 'Y2VydA==' '-----END CERTIFICATE-----' >"${deploy}/certs/gateway/peers.pem"
cat >"${TMP}/eventbus/cloudnode-worker.yaml" <<'EOF'
version: 1
urls: [tls://127.0.0.1:4222]
username: cloudnode-worker
token: eventbus-secret-never-print
ca_file: ca.pem
EOF
chmod 0600 "${TMP}/eventbus/cloudnode-worker.yaml"
cat >"${deploy}/bin/moox-collector-scf" <<'EOF'
#!/usr/bin/env bash
set -euo pipefail
[[ "${MOOX_GATEWAY_SERVICE_KEY_ID}" == collector ]]
[[ "${MOOX_GATEWAY_SERVICE_SECRET_KEY}" == test-secret-never-print ]]
[[ "${MOOX_STORAGE_PRIMARY_AUTH_SECRET}" == storage-secret-never-print ]]
[[ "${MOOX_SPACE_ID}" == crypto ]]
[[ -n "${MOOX_GATEWAY_CA_PEM_B64}" ]]
[[ -z "${MOOX_GATEWAY_CA_FILE:-}" ]]
[[ "${MOOX_EVENTBUS_CREDENTIAL_FILE}" == "${MOOX_E2E_TEST_EVENTBUS_CREDENTIAL_FILE}" ]]
if [[ "$(pwd -P)" != "${MOOX_E2E_TEST_COLLECTOR_CONFIG_DIR}" ]]; then
  exit 42
fi
[[ "$*" != *"-once"* ]]
[[ "$*" == *"-resident"* ]]
printf '%s\n' "$*" >"${MOOX_E2E_TEST_ARGS}"
while :; do sleep 1; done
EOF
chmod +x "${deploy}/bin/moox-collector-scf"

args="${TMP}/args"
collector_config_dir="$(cd "${deploy}/collector/configs" && pwd -P)"
MOOX_E2E_TEST_ARGS="${args}" \
MOOX_E2E_TEST_EVENTBUS_CREDENTIAL_FILE="${TMP}/eventbus/cloudnode-worker.yaml" \
MOOX_E2E_TEST_COLLECTOR_CONFIG_DIR="${collector_config_dir}" \
MOOX_E2E_EVENTBUS_CREDENTIAL_FILE="${TMP}/eventbus/cloudnode-worker.yaml" \
  "${ROOT}/examples/e2e/run-scf-resident.sh" "${deploy}" collector-node crypto &
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

if MOOX_E2E_EVENTBUS_CREDENTIAL_FILE="${TMP}/eventbus/missing.yaml" \
  "${ROOT}/examples/e2e/run-scf-resident.sh" "${deploy}" collector-node crypto \
  >"${TMP}/missing-output" 2>&1; then
  echo 'missing EventBus credential unexpectedly accepted' >&2
  exit 1
fi
grep -Fq 'missing EventBus credential file' "${TMP}/missing-output"
! grep -Rq -- 'eventbus-secret-never-print' "${TMP}/missing-output"
! grep -Rq -- 'storage-secret-never-print' "${TMP}/missing-output"

help="$("${ROOT}/examples/e2e/run.sh" --help)"
grep -Fq -- 'Runtime/assert timeout. Default: 120.' <<<"${help}"
grep -Fq -- 'start resident collector SCF runtime' "${ROOT}/examples/e2e/run.sh"
grep -Fq -- 'activate_storage_datasets' "${ROOT}/examples/e2e/run.sh"
import_line="$(grep -n -m1 'import_seed "metadata-quant-initial.seed.yaml"' "${ROOT}/examples/e2e/run.sh" | cut -d: -f1)"
activate_line="$(grep -n -m1 '^activate_storage_datasets$' "${ROOT}/examples/e2e/run.sh" | cut -d: -f1)"
setup_line="$(grep -n -m1 'log "prepare management/backend state' "${ROOT}/examples/e2e/run.sh" | cut -d: -f1)"
[[ "${import_line}" -lt "${activate_line}" && "${activate_line}" -lt "${setup_line}" ]]

echo 'E2E resident SCF contract passed'
