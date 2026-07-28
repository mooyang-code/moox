#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"

(cd "${ROOT}/packages/doctor" && go test -count=1 ./...)
grep -q 'moox_gateway' "${ROOT}/examples/service-deployments.seed.yaml"
for contract in \
  'packages/doctor/components.yaml' \
  'packages/doctor/report.schema.json' \
  'modules/cli/config/cli.yaml' \
  'examples/monitor-pipelines.yaml'; do
  grep -q "${contract}" "${ROOT}/scripts/release.sh" || {
    echo "missing Doctor release contract: ${contract}" >&2
    exit 1
  }
  grep -q "${contract}" "${ROOT}/scripts/deploy-moox.sh" || {
    echo "missing Doctor deploy contract: ${contract}" >&2
    exit 1
  }
done

unfrozen="--no-""frozen-lockfile"
floating_statik="statik@""latest"
if rg -n "pnpm install .*${unfrozen}|${floating_statik}" "${ROOT}/scripts" "${ROOT}/web-host/Makefile"; then
  echo "release tooling contains an unpinned dependency" >&2
  exit 1
fi

grep -q 'set -euo pipefail' "${ROOT}/scripts/release.sh"
grep -q 'SKIP_WEB_ASSETS' "${ROOT}/scripts/release.sh"
grep -q 'binary_name' "${ROOT}/scripts/build.sh"
grep -q 'RELEASE_PLATFORMS' "${ROOT}/scripts/release-matrix.sh"
grep -q 'github.com/rakyll/statik@v0.1.7' "${ROOT}/scripts/release.sh"
grep -q 'go run github.com/rakyll/statik@v0.1.7' "${ROOT}/scripts/deploy-moox.sh"
grep -q 'modules/trade/config/.' "${ROOT}/scripts/release.sh"
grep -q 'RELEASE_ROOT}/trade/config' "${ROOT}/scripts/release.sh"
grep -q 'build_go modules/strategy ./cmd/server moox-strategy' "${ROOT}/scripts/build.sh"
grep -q 'build_go modules/strategy ./cmd/cli moox-strategy-cli' "${ROOT}/scripts/build.sh"
for contract in \
  'RELEASE_ROOT}/strategy/bin' \
  'copy_binary moox-strategy ' \
  'copy_binary moox-strategy-cli ' \
  'modules/strategy/config/.' \
  'modules/strategy/pyworker/.' \
  'packages/pyruntime/python/.' \
  'RELEASE_ROOT}/factor/python-runtime/' \
  'RELEASE_ROOT}/strategy/python-runtime/' \
  'modules/strategy/pysdk/.' \
  'modules/strategy/strategies/example/.'; do
  grep -q "${contract}" "${ROOT}/scripts/release.sh" || {
    echo "missing Strategy release contract: ${contract}" >&2
    exit 1
  }
done
tmp_strategy="$(mktemp -d "${TMPDIR:-/tmp}/moox-strategy-release.XXXXXX")"
tmp_factor="$(mktemp -d "${TMPDIR:-/tmp}/moox-factor-wrapper.XXXXXX")"
tmp_factor="$(cd "${tmp_factor}" && pwd -P)"
trap 'rm -rf "${tmp_strategy}" "${tmp_factor}"' EXIT
mkdir -p \
  "${tmp_strategy}/factor/python-runtime" \
  "${tmp_strategy}/strategy/pyworker" \
  "${tmp_strategy}/strategy/python-runtime"
cp -R "${ROOT}/packages/pyruntime/python/." "${tmp_strategy}/factor/python-runtime/"
cp "${ROOT}/modules/strategy/pyworker/worker.py" "${tmp_strategy}/strategy/pyworker/worker.py"
cp -R "${ROOT}/packages/pyruntime/python/." "${tmp_strategy}/strategy/python-runtime/"
PYTHONPATH="${tmp_strategy}/factor/python-runtime" python3 -c 'from moox_pyruntime import protocol; assert protocol.TYPE_RUN'
python3 - "${tmp_strategy}/strategy/pyworker/worker.py" <<'PY'
import importlib.util
import pathlib
import sys

worker = pathlib.Path(sys.argv[1])
spec = importlib.util.spec_from_file_location("moox_strategy_release_worker", worker)
module = importlib.util.module_from_spec(spec)
spec.loader.exec_module(module)
assert module.HELLO
PY
grep -q "name __pycache__ -o -name .pytest_cache" "${ROOT}/scripts/release.sh"
grep -q "name '\*.pyc' -o -name '\*.sqlite' -o -name '\*.db'" "${ROOT}/scripts/release.sh"
bash -n \
  "${ROOT}/scripts/build.sh" \
  "${ROOT}/scripts/moox-factor-run-once.sh" \
  "${ROOT}/scripts/package-service.sh" \
  "${ROOT}/scripts/release.sh" \
  "${ROOT}/scripts/release-matrix.sh"
"${ROOT}/scripts/test-package-service.sh"
matrix_output="$(VERSION=test RELEASE_PLATFORMS=linux/amd64,darwin/arm64,windows/amd64 "${ROOT}/scripts/release-matrix.sh" --dry-run)"
grep -q 'release test (linux/amd64' <<<"${matrix_output}"
grep -q 'release test (darwin/arm64' <<<"${matrix_output}"
grep -q 'release test (windows/amd64' <<<"${matrix_output}"

mkdir -p \
  "${tmp_factor}/bin" \
  "${tmp_factor}/data/factor/venv/bin" \
  "${tmp_factor}/factor/config" \
  "${tmp_factor}/factor/pyworker" \
  "${tmp_factor}/factor/factors" \
  "${tmp_factor}/python-runtime" \
  "${tmp_factor}/secrets" \
  "${tmp_factor}/certs/gateway"
cp -R "${ROOT}/packages/pyruntime/python/." "${tmp_factor}/python-runtime/"
cp "${ROOT}/scripts/moox-factor-run-once.sh" "${tmp_factor}/bin/moox-factor-run-once"
chmod +x "${tmp_factor}/bin/moox-factor-run-once"
printf '#!/usr/bin/env bash\nexit 0\n' >"${tmp_factor}/data/factor/venv/bin/python"
chmod +x "${tmp_factor}/data/factor/venv/bin/python"
printf 'database: {}\n' >"${tmp_factor}/factor/config/app.yaml"
printf '# worker\n' >"${tmp_factor}/factor/pyworker/worker.py"
printf 'test-factor-secret\n' >"${tmp_factor}/secrets/gateway-factor.key"
printf 'MOOX_GATEWAY_NODE_ID=test-node\n' >"${tmp_factor}/secrets/gateway-service.env"
printf 'test-ca\n' >"${tmp_factor}/certs/gateway/peers.pem"
cat >"${tmp_factor}/bin/moox-factor-cli" <<EOF
#!/usr/bin/env bash
set -euo pipefail
python3 -c 'import os, sys; sys.path.insert(0, os.environ["MOOX_PYTHON_RUNTIME_PATH"]); import moox_pyruntime'
env | sort >"${tmp_factor}/captured.env"
printf '%s\\n' "\$@" >"${tmp_factor}/captured.argv"
EOF
chmod +x "${tmp_factor}/bin/moox-factor-cli"

env -i HOME="${HOME}" PATH="${PATH}" "${tmp_factor}/bin/moox-factor-run-once" \
  --space crypto \
  --dataset prices \
  --subject BTC-USDT \
  --freq 1m \
  --start-time 2026-07-28T00:00:00Z \
  --end-time 2026-07-28T00:01:00Z

grep -Fxq "MOOX_FACTOR_DB_PATH=${tmp_factor}/data/factor/factor.db" "${tmp_factor}/captured.env"
grep -Fxq "MOOX_FACTOR_ENGINE_PYTHON_BIN=${tmp_factor}/data/factor/venv/bin/python" "${tmp_factor}/captured.env"
grep -Fxq "MOOX_FACTOR_ENGINE_WORKER_PATH=${tmp_factor}/factor/pyworker/worker.py" "${tmp_factor}/captured.env"
grep -Fxq "MOOX_FACTOR_ENGINE_FACTORS_DIR=${tmp_factor}/factor/factors" "${tmp_factor}/captured.env"
grep -Fxq "MOOX_PYTHON_RUNTIME_PATH=${tmp_factor}/python-runtime" "${tmp_factor}/captured.env"
grep -Fxq 'MOOX_FACTOR_STORAGE_RPC_GATEWAY_TARGET=ip://127.0.0.1:11003' "${tmp_factor}/captured.env"
grep -Fxq 'MOOX_FACTOR_STORAGE_RPC_GATEWAY_NODE_ID=test-node' "${tmp_factor}/captured.env"
grep -Fxq 'MOOX_GATEWAY_SERVICE_KEY_ID=factor' "${tmp_factor}/captured.env"
grep -Fxq 'MOOX_GATEWAY_CALLER=factor' "${tmp_factor}/captured.env"
grep -Fxq 'MOOX_GATEWAY_SERVICE_SECRET_KEY=test-factor-secret' "${tmp_factor}/captured.env"
grep -Fxq "MOOX_GATEWAY_CA_FILE=${tmp_factor}/certs/gateway/peers.pem" "${tmp_factor}/captured.env"
cat >"${tmp_factor}/expected.argv" <<EOF
run-once
--config
${tmp_factor}/factor/config/app.yaml
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
cmp "${tmp_factor}/expected.argv" "${tmp_factor}/captured.argv"
mv "${tmp_factor}/python-runtime" "${tmp_factor}/python-runtime.missing"
if env -i HOME="${HOME}" PATH="${PATH}" "${tmp_factor}/bin/moox-factor-run-once" \
  --space crypto --dataset prices --subject BTC-USDT --freq 1m \
  --start-time 2026-07-28T00:00:00Z --end-time 2026-07-28T00:01:00Z \
  >/dev/null 2>&1; then
  echo "factor wrapper accepted a missing Python runtime directory" >&2
  exit 1
fi
mv "${tmp_factor}/python-runtime.missing" "${tmp_factor}/python-runtime"
printf 'line-one\nline-two\n' >"${tmp_factor}/secrets/gateway-factor.key"
if env -i HOME="${HOME}" PATH="${PATH}" "${tmp_factor}/bin/moox-factor-run-once" \
  --space crypto --dataset prices --subject BTC-USDT --freq 1m \
  --start-time 2026-07-28T00:00:00Z --end-time 2026-07-28T00:01:00Z \
  >/dev/null 2>&1; then
  echo "factor wrapper accepted a multi-line Gateway secret" >&2
  exit 1
fi
grep -q 'moox-factor-run-once.sh.*STAGE_DIR.*/bin/moox-factor-run-once' "${ROOT}/scripts/deploy-moox.sh"
