#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../../.." && pwd -P)"
TMP_ROOT="$(mktemp -d "${TMPDIR:-/tmp}/moox-factor-engine-pkg.XXXXXX")"
trap 'rm -rf "${TMP_ROOT}"' EXIT

STAGE="${TMP_ROOT}/service"
mkdir -p "${STAGE}/bin" "${STAGE}/config" "${STAGE}/pyworker" "${STAGE}/python-runtime"
printf '#!/usr/bin/env bash\nexit 0\n' >"${STAGE}/bin/moox-factor-engine"
chmod +x "${STAGE}/bin/moox-factor-engine"
cp "${ROOT}/modules/factor/config/engine-app.yaml" "${STAGE}/config/engine-app.yaml"
cp "${ROOT}/modules/factor/config/engine-trpc.yaml" "${STAGE}/config/engine-trpc.yaml"
cp "${ROOT}/scripts/deploy/factor-engine/start.sh" "${STAGE}/start.sh"
cp "${ROOT}/scripts/deploy/factor-engine/stop.sh" "${STAGE}/stop.sh"
cp "${ROOT}/scripts/deploy/factor-engine/healthcheck.sh" "${STAGE}/healthcheck.sh"
chmod +x "${STAGE}/start.sh" "${STAGE}/stop.sh" "${STAGE}/healthcheck.sh"
printf 'pandas>=2.2,<3\nnumpy>=2,<3\n' >"${STAGE}/pyworker/runtime-requirements.txt"
printf 'print("worker")\n' >"${STAGE}/pyworker/worker.py"
printf 'PROTOCOL = 1\n' >"${STAGE}/python-runtime/moox_pyruntime.py"

OUTPUT="${TMP_ROOT}/moox-factor-engine.zip"
bash "${ROOT}/scripts/build/package-service.sh" --service-dir "${STAGE}" --output "${OUTPUT}"
[[ "$(stat -f '%Lp' "${OUTPUT}" 2>/dev/null || stat -c '%a' "${OUTPUT}")" == "600" ]]

UNPACKED="${TMP_ROOT}/unpacked"
mkdir -p "${UNPACKED}"
unzip -q "${OUTPUT}" -d "${UNPACKED}"
for path in \
  bin/moox-factor-engine \
  config/engine-app.yaml \
  config/engine-trpc.yaml \
  start.sh \
  stop.sh \
  healthcheck.sh \
  pyworker/worker.py \
  pyworker/runtime-requirements.txt \
  python-runtime/moox_pyruntime.py
do
  [[ -e "${UNPACKED}/${path}" ]] || {
    echo "missing engine package entry: ${path}" >&2
    exit 1
  }
done
for blocked in data logs run secrets certs; do
  if [[ -e "${UNPACKED}/${blocked}" ]]; then
    echo "engine package must not contain ${blocked}" >&2
    exit 1
  fi
done

grep -Fq 'cache.enabled: false' "${UNPACKED}/config/engine-app.yaml" || \
  grep -Fq 'enabled: false' "${UNPACKED}/config/engine-app.yaml"
grep -Fq 'credential_file: ~/.config/moox/eventbus/factor-engine-eventbus.yaml' "${UNPACKED}/config/engine-app.yaml"
grep -Fq 'factor-engine-eventbus.yaml' "${UNPACKED}/start.sh"
grep -Fq 'MOOX_FACTOR_EVENTBUS_CREDENTIAL_FILE' "${UNPACKED}/start.sh"
grep -Fq 'MOOX_EVENTBUS_NATS_URL' "${UNPACKED}/start.sh"
grep -Fq 'credential_eventbus_url' "${UNPACKED}/start.sh"
grep -Fq 'python3 -m venv' "${UNPACKED}/start.sh"
grep -Fq -- '--no-index' "${UNPACKED}/start.sh"
grep -Fq 'runtime-requirements.txt' "${UNPACKED}/start.sh"
! grep -Fq '/data/moox/storage/secrets' "${UNPACKED}/start.sh"
! grep -Fq '/data/moox/prod/secrets' "${UNPACKED}/start.sh"
! grep -Fq 'trpc.moox.factor.FactorMgr' "${UNPACKED}/config/engine-trpc.yaml"
! grep -Fq 'ensure_factor_python' "${UNPACKED}/start.sh"
grep -Fq '11415' "${UNPACKED}/config/engine-trpc.yaml"
grep -Fq 'http://127.0.0.1:11415/readyz' "${UNPACKED}/healthcheck.sh"
grep -Fq 'stop.sh' "${UNPACKED}/start.sh"
grep -Fq '11415' "${UNPACKED}/start.sh"
bash -n "${UNPACKED}/start.sh"
bash -n "${UNPACKED}/stop.sh"
bash -n "${UNPACKED}/healthcheck.sh"

PACKAGER="${ROOT}/scripts/build/package-factor-engine.sh"
bash -n "${PACKAGER}"
grep -Fq 'scripts/build/package-service.sh' "${PACKAGER}"
grep -Fq 'modules/factor/config/engine-app.yaml' "${PACKAGER}"
grep -Fq 'scripts/deploy/factor-engine/start.sh' "${PACKAGER}"
grep -Fq 'packages/pyruntime/python' "${PACKAGER}"
grep -Fq 'MOOX_LINUX_CGO_TARGET=factor-engine' "${PACKAGER}"
grep -Fq 'scripts/build/build-storage-linux.sh' "${PACKAGER}"
grep -Fq 'pip download' "${PACKAGER}"
grep -Fq 'manylinux2014_x86_64' "${PACKAGER}"
! grep -Eq 'secrets|certs' "${PACKAGER}"

echo "factor engine package contract passed"
