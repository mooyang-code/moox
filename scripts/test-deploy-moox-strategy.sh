#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
TMP_ROOT="$(mktemp -d "${TMPDIR:-/tmp}/moox-strategy-deploy.XXXXXX")"
FIXTURE_ROOT="${TMP_ROOT}/repo"
ARCHIVE="${TMP_ROOT}/strategy.tar.gz"
trap 'rm -rf "${TMP_ROOT}"' EXIT

mkdir -p "${FIXTURE_ROOT}/scripts/lib" "${FIXTURE_ROOT}/scripts/deps" \
  "${FIXTURE_ROOT}/deploy" "${FIXTURE_ROOT}/modules" "${FIXTURE_ROOT}/packages" "${FIXTURE_ROOT}/bin"
cp "${ROOT}/scripts/deploy-moox.sh" "${FIXTURE_ROOT}/scripts/deploy-moox.sh"
ln -s "${ROOT}/scripts/install-caddy-ca.sh" "${FIXTURE_ROOT}/scripts/install-caddy-ca.sh"
ln -s "${ROOT}/scripts/lib/caddy-managed.sh" "${FIXTURE_ROOT}/scripts/lib/caddy-managed.sh"
ln -s "${ROOT}/scripts/lib/loopback-listeners.sh" "${FIXTURE_ROOT}/scripts/lib/loopback-listeners.sh"
ln -s "${ROOT}/scripts/deps/caddy-v2.11.4-checksums.txt" "${FIXTURE_ROOT}/scripts/deps/caddy-v2.11.4-checksums.txt"
ln -s "${ROOT}/deploy/caddy" "${FIXTURE_ROOT}/deploy/caddy"
ln -s "${ROOT}/modules/gateway" "${FIXTURE_ROOT}/modules/gateway"
ln -s "${ROOT}/modules/cli" "${FIXTURE_ROOT}/modules/cli"
ln -s "${ROOT}/modules/strategy" "${FIXTURE_ROOT}/modules/strategy"
ln -s "${ROOT}/packages/doctor" "${FIXTURE_ROOT}/packages/doctor"
mkdir -p "${FIXTURE_ROOT}/packages/pyruntime"
ln -s "${ROOT}/packages/pyruntime/python" "${FIXTURE_ROOT}/packages/pyruntime/python"
ln -s "${ROOT}/examples" "${FIXTURE_ROOT}/examples"

for binary in moox-gateway moox-gateway-cli moox-strategy moox-strategy-cli; do
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
  --goos linux --goarch amd64 --skip-build --node-id strategy \
  --gateway-control-url http://127.0.0.1:11000 \
  --no-admin --no-storage --no-archive --no-eventbus --no-cloudnode \
  --no-collector --no-factor --no-monitor >/dev/null

mkdir "${TMP_ROOT}/unpacked"
tar -C "${TMP_ROOT}/unpacked" -xzf "${ARCHIVE}"

for path in \
  bin/moox-strategy \
  bin/moox-strategy-cli \
  strategy/config/app.yaml \
  strategy/config/trpc_go.yaml \
  strategy/pyworker/worker.py \
  strategy/pyworker/runtime-requirements.txt \
  strategy/pysdk/moox_strategy/types.py \
  strategy/strategies/example/strategy.yaml \
  python-runtime/moox_pyruntime/protocol.py
do
  [[ -e "${TMP_ROOT}/unpacked/${path}" ]] || { echo "missing Strategy deployment artifact: ${path}" >&2; exit 1; }
done

grep -q '^database: ../data/strategy/strategy.sqlite$' "${TMP_ROOT}/unpacked/strategy/config/app.yaml"
grep -q '^python_bin: ../data/strategy/venv/bin/python$' "${TMP_ROOT}/unpacked/strategy/config/app.yaml"
grep -q '^  credential_file: ""$' "${TMP_ROOT}/unpacked/strategy/config/app.yaml"
grep -q 'WITH_STRATEGY="${MOOX_WITH_STRATEGY:-1}"' "${TMP_ROOT}/unpacked/start.sh"
grep -q 'start_strategy' "${TMP_ROOT}/unpacked/start.sh"
grep -q 'MOOX_PYTHON_RUNTIME_PATH=${ROOT}/python-runtime' "${TMP_ROOT}/unpacked/start.sh"
grep -q 'strategy) url=http://127.0.0.1:11431/readyz' "${TMP_ROOT}/unpacked/healthcheck.sh"
grep -q 'stop_service "strategy"' "${TMP_ROOT}/unpacked/stop.sh"
! find "${TMP_ROOT}/unpacked/strategy" -type f \( -name '*.pyc' -o -name '*.sqlite' -o -name '*.db' \) -print -quit | grep -q .

echo 'Strategy deployment contract passed'
