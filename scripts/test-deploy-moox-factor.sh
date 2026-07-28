#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
TMP_ROOT="$(mktemp -d "${TMPDIR:-/tmp}/moox-factor-deploy.XXXXXX")"
FIXTURE_ROOT="${TMP_ROOT}/repo"
ARCHIVE="${TMP_ROOT}/factor.tar.gz"
trap 'rm -rf "${TMP_ROOT}"' EXIT

mkdir -p "${FIXTURE_ROOT}/scripts/lib" "${FIXTURE_ROOT}/scripts/deps" \
  "${FIXTURE_ROOT}/deploy" "${FIXTURE_ROOT}/modules" "${FIXTURE_ROOT}/packages" "${FIXTURE_ROOT}/bin"
cp "${ROOT}/scripts/deploy-moox.sh" "${FIXTURE_ROOT}/scripts/deploy-moox.sh"
cp "${ROOT}/scripts/moox-factor-run-once.sh" "${FIXTURE_ROOT}/scripts/moox-factor-run-once.sh"
ln -s "${ROOT}/scripts/install-caddy-ca.sh" "${FIXTURE_ROOT}/scripts/install-caddy-ca.sh"
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

for binary in moox-gateway moox-gateway-cli moox-factor moox-factor-cli; do
  printf '#!/usr/bin/env bash\nexit 0\n' >"${FIXTURE_ROOT}/bin/${binary}"
  chmod +x "${FIXTURE_ROOT}/bin/${binary}"
done

mkdir "${TMP_ROOT}/fake-path"
for command in ssh scp rsync; do
  printf '#!/usr/bin/env bash\necho "%s unexpectedly invoked" >&2\nexit 97\n' "${command}" >"${TMP_ROOT}/fake-path/${command}"
  chmod +x "${TMP_ROOT}/fake-path/${command}"
done

PATH="${TMP_ROOT}/fake-path:${PATH}" "${FIXTURE_ROOT}/scripts/deploy-moox.sh" \
  --package-only --archive "${ARCHIVE}" --target localhost \
  --dir "${TMP_ROOT}/deploy" --stage "${TMP_ROOT}/stage" \
  --goos linux --goarch amd64 --skip-build --node-id factor-contract \
  --gateway-control-url http://127.0.0.1:11000 \
  --no-admin --no-storage --no-archive --no-eventbus --no-cloudnode \
  --no-collector --no-strategy --no-monitor >/dev/null

mkdir "${TMP_ROOT}/unpacked"
tar -C "${TMP_ROOT}/unpacked" -xzf "${ARCHIVE}"
UNPACKED="$(cd "${TMP_ROOT}/unpacked" && pwd -P)"

for path in \
  bin/moox-factor \
  bin/moox-factor-cli \
  bin/moox-factor-run-once \
  factor/config/app.yaml \
  factor/config/trpc_go.yaml \
  factor/pyworker/worker.py \
  factor/pyworker/requirements.txt \
  factor/pyworker/runtime-requirements.txt \
  factor/factors/Bias.py \
  python-runtime/moox_pyruntime/protocol.py \
  secrets/gateway-factor.key \
  secrets/gateway-service.env \
  certs/gateway/peers.pem
do
  [[ -s "${UNPACKED}/${path}" ]] || {
    echo "missing or empty Factor deployment artifact: ${path}" >&2
    exit 1
  }
done

mkdir -p "${UNPACKED}/data/factor/venv/bin"
printf '#!/usr/bin/env bash\nexit 0\n' >"${UNPACKED}/data/factor/venv/bin/python"
chmod +x "${UNPACKED}/data/factor/venv/bin/python"
factor_secret="$(tr -d '\r\n' <"${UNPACKED}/secrets/gateway-factor.key")"
grep -Fxq 'MOOX_GATEWAY_NODE_ID=factor-contract' "${UNPACKED}/secrets/gateway-service.env"

cat >"${UNPACKED}/bin/moox-factor-cli" <<EOF
#!/usr/bin/env bash
set -euo pipefail
python3 -c 'import os, sys; sys.path.insert(0, os.environ["MOOX_PYTHON_RUNTIME_PATH"]); import moox_pyruntime'
env | sort >"${UNPACKED}/captured.env"
printf '%s\\n' "\$@" >"${UNPACKED}/captured.argv"
EOF
chmod +x "${UNPACKED}/bin/moox-factor-cli"

env -i HOME="${HOME}" PATH="${PATH}" "${UNPACKED}/bin/moox-factor-run-once" \
  --space crypto \
  --dataset prices \
  --subject BTC-USDT \
  --freq 1m \
  --start-time 2026-07-28T00:00:00Z \
  --end-time 2026-07-28T00:01:00Z

grep -Fxq "MOOX_FACTOR_DB_PATH=${UNPACKED}/data/factor/factor.db" "${UNPACKED}/captured.env"
grep -Fxq "MOOX_FACTOR_ENGINE_PYTHON_BIN=${UNPACKED}/data/factor/venv/bin/python" "${UNPACKED}/captured.env"
grep -Fxq "MOOX_FACTOR_ENGINE_WORKER_PATH=${UNPACKED}/factor/pyworker/worker.py" "${UNPACKED}/captured.env"
grep -Fxq "MOOX_FACTOR_ENGINE_FACTORS_DIR=${UNPACKED}/factor/factors" "${UNPACKED}/captured.env"
grep -Fxq "MOOX_PYTHON_RUNTIME_PATH=${UNPACKED}/python-runtime" "${UNPACKED}/captured.env"
grep -Fxq 'MOOX_FACTOR_STORAGE_RPC_GATEWAY_TARGET=ip://127.0.0.1:11003' "${UNPACKED}/captured.env"
grep -Fxq 'MOOX_FACTOR_STORAGE_RPC_GATEWAY_NODE_ID=factor-contract' "${UNPACKED}/captured.env"
grep -Fxq 'MOOX_GATEWAY_SERVICE_KEY_ID=factor' "${UNPACKED}/captured.env"
grep -Fxq 'MOOX_GATEWAY_CALLER=factor' "${UNPACKED}/captured.env"
grep -Fxq "MOOX_GATEWAY_SERVICE_SECRET_KEY=${factor_secret}" "${UNPACKED}/captured.env"
grep -Fxq "MOOX_GATEWAY_CA_FILE=${UNPACKED}/certs/gateway/peers.pem" "${UNPACKED}/captured.env"
cat >"${UNPACKED}/expected.argv" <<EOF
run-once
--config
${UNPACKED}/factor/config/app.yaml
--space
crypto
--dataset
prices
--subject
BTC-USDT
--freq
1m
--start-time
2026-07-28T00:00:00Z
--end-time
2026-07-28T00:01:00Z
EOF
cmp "${UNPACKED}/expected.argv" "${UNPACKED}/captured.argv"

mv "${UNPACKED}/python-runtime" "${UNPACKED}/python-runtime.missing"
if env -i HOME="${HOME}" PATH="${PATH}" "${UNPACKED}/bin/moox-factor-run-once" \
  --space crypto --dataset prices --subject BTC-USDT --freq 1m \
  --start-time 2026-07-28T00:00:00Z --end-time 2026-07-28T00:01:00Z \
  >/dev/null 2>&1; then
  echo "packaged Factor wrapper accepted a missing Python runtime directory" >&2
  exit 1
fi
mv "${UNPACKED}/python-runtime.missing" "${UNPACKED}/python-runtime"

printf 'line-one\nline-two\n' >"${UNPACKED}/secrets/gateway-factor.key"
if env -i HOME="${HOME}" PATH="${PATH}" "${UNPACKED}/bin/moox-factor-run-once" \
  --space crypto --dataset prices --subject BTC-USDT --freq 1m \
  --start-time 2026-07-28T00:00:00Z --end-time 2026-07-28T00:01:00Z \
  >/dev/null 2>&1; then
  echo "packaged Factor wrapper accepted a multi-line Gateway secret" >&2
  exit 1
fi

echo 'Factor deployment contract passed'
