#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../../.." && pwd -P)"
TMP_ROOT="$(mktemp -d "${TMPDIR:-/tmp}/moox-factor-deploy.XXXXXX")"
FIXTURE_ROOT="${TMP_ROOT}/repo"
ARCHIVE="${TMP_ROOT}/factor.tar.gz"
trap 'rm -rf "${TMP_ROOT}"' EXIT

mkdir -p "${FIXTURE_ROOT}/scripts/lib" "${FIXTURE_ROOT}/scripts/deps" \
  "${FIXTURE_ROOT}/scripts/deploy" "${FIXTURE_ROOT}/scripts/runtime" \
  "${FIXTURE_ROOT}/deploy" "${FIXTURE_ROOT}/modules" "${FIXTURE_ROOT}/packages" "${FIXTURE_ROOT}/bin"
cp "${ROOT}/scripts/deploy/deploy-moox.sh" "${FIXTURE_ROOT}/scripts/deploy/deploy-moox.sh"
ln -s "${ROOT}/scripts/runtime/moox-storage-auth-check.sh" "${FIXTURE_ROOT}/scripts/runtime/moox-storage-auth-check.sh"
ln -s "${ROOT}/scripts/runtime/moox-storage-auth-rotate.sh" "${FIXTURE_ROOT}/scripts/runtime/moox-storage-auth-rotate.sh"
ln -s "${ROOT}/scripts/runtime/moox-log-rotate.sh" "${FIXTURE_ROOT}/scripts/runtime/moox-log-rotate.sh"
ln -s "${ROOT}/scripts/deploy/install-caddy-ca.sh" "${FIXTURE_ROOT}/scripts/deploy/install-caddy-ca.sh"
ln -s "${ROOT}/scripts/lib/caddy-managed.sh" "${FIXTURE_ROOT}/scripts/lib/caddy-managed.sh"
ln -s "${ROOT}/scripts/lib/loopback-listeners.sh" "${FIXTURE_ROOT}/scripts/lib/loopback-listeners.sh"
ln -s "${ROOT}/scripts/deps/caddy-v2.11.4-checksums.txt" "${FIXTURE_ROOT}/scripts/deps/caddy-v2.11.4-checksums.txt"
ln -s "${ROOT}/deploy/caddy" "${FIXTURE_ROOT}/deploy/caddy"
ln -s "${ROOT}/modules/gateway" "${FIXTURE_ROOT}/modules/gateway"
ln -s "${ROOT}/modules/cli" "${FIXTURE_ROOT}/modules/cli"
ln -s "${ROOT}/modules/factor" "${FIXTURE_ROOT}/modules/factor"
ln -s "${ROOT}/packages/doctor" "${FIXTURE_ROOT}/packages/doctor"
mkdir -p "${FIXTURE_ROOT}/packages/pyruntime"
ln -s "${ROOT}/packages/pyruntime/python" "${FIXTURE_ROOT}/packages/pyruntime/python"
ln -s "${ROOT}/examples" "${FIXTURE_ROOT}/examples"
mkdir -p "${FIXTURE_ROOT}/config"
ln -s "${ROOT}/config/setup" "${FIXTURE_ROOT}/config/setup"

for binary in moox-gateway moox-gateway-cli moox-factor-mgr moox-factor-mgr-cli; do
  printf '#!/usr/bin/env bash\nexit 0\n' >"${FIXTURE_ROOT}/bin/${binary}"
  chmod +x "${FIXTURE_ROOT}/bin/${binary}"
done

mkdir "${TMP_ROOT}/fake-path"
for command in ssh scp rsync; do
  printf '#!/usr/bin/env bash\necho "%s unexpectedly invoked" >&2\nexit 97\n' "${command}" >"${TMP_ROOT}/fake-path/${command}"
  chmod +x "${TMP_ROOT}/fake-path/${command}"
done

deploy_args=(
  --package-only --archive "${ARCHIVE}" --target localhost \
  --dir "${TMP_ROOT}/deploy" --stage "${TMP_ROOT}/stage" \
  --goos linux --goarch amd64 --skip-build --node-id factor-contract \
  --gateway-control-url http://127.0.0.1:11000 \
  --no-admin --no-storage --no-storage-access --no-archive --no-eventbus --no-cloudnode \
  --no-collector --no-strategy --no-trade --no-monitor --no-hostagent
)
if PATH="${TMP_ROOT}/fake-path:${PATH}" "${FIXTURE_ROOT}/scripts/deploy/deploy-moox.sh" "${deploy_args[@]}" \
  >/dev/null 2>&1; then
  echo "factor-only package accepted a missing MOOX_STORAGE_PRIMARY_AUTH_SECRET" >&2
  exit 1
fi
expected_storage_primary_secret="factor-storage-contract-secret"
expected_storage_view_secret="factor-storage-view-contract-secret"
MOOX_STORAGE_PRIMARY_AUTH_SECRET="${expected_storage_primary_secret}" \
MOOX_STORAGE_VIEW_AUTH_SECRET="${expected_storage_view_secret}" \
  PATH="${TMP_ROOT}/fake-path:${PATH}" \
  "${FIXTURE_ROOT}/scripts/deploy/deploy-moox.sh" "${deploy_args[@]}" >/dev/null

mkdir "${TMP_ROOT}/unpacked"
tar -C "${TMP_ROOT}/unpacked" -xzf "${ARCHIVE}"
UNPACKED="$(cd "${TMP_ROOT}/unpacked" && pwd -P)"

for path in \
  bin/moox-factor-mgr \
  bin/moox-factor-mgr-cli \
  factor-mgr/config/app.yaml \
  factor-mgr/config/trpc_go.yaml \
  factor-mgr/pyworker/worker.py \
  factor-mgr/pyworker/requirements.txt \
  factor-mgr/pyworker/runtime-requirements.txt \
  factor-mgr/factors/Bias.py \
  python-runtime/moox_pyruntime/protocol.py \
  secrets/gateway-factor.key \
  secrets/gateway-service.env \
  secrets/storage-internal-auth.env \
  certs/gateway/peers.pem
do
  [[ -s "${UNPACKED}/${path}" ]] || {
    echo "missing or empty Factor deployment artifact: ${path}" >&2
    exit 1
  }
done

storage_primary_secret="$(
  bash -c 'source "$1"; printf "%s" "${MOOX_STORAGE_PRIMARY_AUTH_SECRET}"' \
    _ "${UNPACKED}/secrets/storage-internal-auth.env"
)"
[[ -n "${storage_primary_secret}" ]]
[[ "${storage_primary_secret}" == "${expected_storage_primary_secret}" ]]
storage_view_secret="$(
  bash -c 'source "$1"; printf "%s" "${MOOX_STORAGE_VIEW_AUTH_SECRET}"' \
    _ "${UNPACKED}/secrets/storage-internal-auth.env"
)"
[[ "${storage_view_secret}" == "${expected_storage_view_secret}" ]]
grep -Fxq 'MOOX_GATEWAY_NODE_ID=factor-contract' "${UNPACKED}/secrets/gateway-service.env"
grep -Fq 'MOOX_FACTOR_STORAGE_GATEWAY_TARGET=${LOCAL_STORAGE_RPC_GATEWAY_TARGET}' "${UNPACKED}/start.sh"
grep -Fq 'MOOX_FACTOR_STORAGE_GATEWAY_NODE_ID=${LOCAL_STORAGE_GATEWAY_NODE_ID}' "${UNPACKED}/start.sh"
grep -Fq 'FACTOR_ENV+=("MOOX_FACTOR_EVENTBUS_CREDENTIAL_FILE=' "${UNPACKED}/start.sh"
grep -Fq 'FACTOR_ENV+=("MOOX_FACTOR_EVENTBUS_CREDENTIAL_FILE=${HOME}/.config/moox/eventbus/factor-eventbus.yaml")' "${UNPACKED}/start.sh"
grep -Fq 'FACTOR_EVENTBUS_URL_ENV="tls://127.0.0.1:${MOOX_EVENTBUS_PORT:-4222}"' "${UNPACKED}/start.sh"
grep -Fq 'MOOX_EVENTBUS_NATS_URL=${MOOX_FACTOR_EVENTBUS_URL:-${FACTOR_EVENTBUS_URL_ENV}}' "${UNPACKED}/start.sh"
grep -Fq 'MOOX_FACTOR_PYTHON_FACTORS_DIR=${MOOX_FACTOR_PYTHON_FACTORS_DIR:-${ROOT}/data/factor-mgr/factors}' "${UNPACKED}/start.sh"
if awk '/^start_factor_mgr\(\)/,/^start_strategy\(\)/' "${UNPACKED}/start.sh" | grep -q ensure_factor_python; then
  echo "control start_factor_mgr still installs Python compute dependencies" >&2
  exit 1
fi
grep -Fq 'MOOX_LOCAL_STORAGE_RPC_GATEWAY_TARGET=${quoted_local_storage_gateway_target}' "${FIXTURE_ROOT}/scripts/deploy/deploy-moox.sh"

grep -Fq 'factors_dir: ./data/factor/factors' "${UNPACKED}/factor-mgr/config/app.yaml"
grep -Fq 'workers: 8' "${UNPACKED}/factor-mgr/config/app.yaml"
! grep -Fq 'scheduler:' "${UNPACKED}/factor-mgr/config/app.yaml"

echo 'Factor deployment contract passed'
