#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"

unfrozen="--no-""frozen-lockfile"
floating_statik="statik@""latest"
if rg -n "pnpm install .*${unfrozen}|${floating_statik}" "${ROOT}/scripts" "${ROOT}/web-host/Makefile"; then
  echo "release tooling contains an unpinned dependency" >&2
  exit 1
fi

grep -q 'set -euo pipefail' "${ROOT}/scripts/release.sh"
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
  'RELEASE_ROOT}/strategy/python-runtime/' \
  'modules/strategy/pysdk/.' \
  'modules/strategy/strategies/example/.'; do
  grep -q "${contract}" "${ROOT}/scripts/release.sh" || {
    echo "missing Strategy release contract: ${contract}" >&2
    exit 1
  }
done
tmp_strategy="$(mktemp -d "${TMPDIR:-/tmp}/moox-strategy-release.XXXXXX")"
trap 'rm -rf "${tmp_strategy}"' EXIT
mkdir -p "${tmp_strategy}/strategy/pyworker" "${tmp_strategy}/strategy/python-runtime"
cp "${ROOT}/modules/strategy/pyworker/worker.py" "${tmp_strategy}/strategy/pyworker/worker.py"
cp -R "${ROOT}/packages/pyruntime/python/." "${tmp_strategy}/strategy/python-runtime/"
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
