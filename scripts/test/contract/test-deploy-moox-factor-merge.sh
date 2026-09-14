#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../../.." && pwd -P)"
TMP_ROOT="$(mktemp -d "${TMPDIR:-/tmp}/moox-factor-merge-pkg.XXXXXX")"
trap 'rm -rf "${TMP_ROOT}"' EXIT

STAGE="${TMP_ROOT}/service"
mkdir -p "${STAGE}/bin" "${STAGE}/config"
printf '#!/usr/bin/env bash\nexit 0\n' >"${STAGE}/bin/moox-factor-merge"
chmod +x "${STAGE}/bin/moox-factor-merge"
cp "${ROOT}/modules/factor/config/merge-app.yaml" "${STAGE}/config/merge-app.yaml"
cp "${ROOT}/modules/factor/config/merge-trpc.yaml" "${STAGE}/config/merge-trpc.yaml"
cp "${ROOT}/scripts/deploy/factor-merge/start.sh" "${STAGE}/start.sh"
cp "${ROOT}/scripts/deploy/factor-merge/stop.sh" "${STAGE}/stop.sh"
cp "${ROOT}/scripts/deploy/factor-merge/healthcheck.sh" "${STAGE}/healthcheck.sh"
chmod +x "${STAGE}/start.sh" "${STAGE}/stop.sh" "${STAGE}/healthcheck.sh"

OUTPUT="${TMP_ROOT}/moox-factor-merge.zip"
bash "${ROOT}/scripts/build/package-service.sh" --service-dir "${STAGE}" --output "${OUTPUT}"
[[ "$(stat -f '%Lp' "${OUTPUT}" 2>/dev/null || stat -c '%a' "${OUTPUT}")" == "600" ]]

UNPACKED="${TMP_ROOT}/unpacked"
mkdir -p "${UNPACKED}"
unzip -q "${OUTPUT}" -d "${UNPACKED}"
for path in \
  bin/moox-factor-merge \
  config/merge-app.yaml \
  config/merge-trpc.yaml \
  start.sh \
  stop.sh \
  healthcheck.sh
do
  [[ -e "${UNPACKED}/${path}" ]] || {
    echo "missing merge package entry: ${path}" >&2
    exit 1
  }
done
for blocked in data logs run secrets certs pyworker python-runtime; do
  if [[ -e "${UNPACKED}/${blocked}" ]]; then
    echo "merge package must not contain ${blocked}" >&2
    exit 1
  fi
done

grep -Fq 'credential_file: ~/.config/moox/eventbus/factor-merge-eventbus.yaml' "${UNPACKED}/config/merge-app.yaml"
grep -Fq 'factor-merge-eventbus.yaml' "${UNPACKED}/start.sh"
grep -Fq 'MOOX_FACTOR_EVENTBUS_CREDENTIAL_FILE' "${UNPACKED}/start.sh"
grep -Fq 'MOOX_EVENTBUS_NATS_URL' "${UNPACKED}/start.sh"
grep -Fq 'credential_eventbus_url' "${UNPACKED}/start.sh"
! grep -Fq 'python3 -m venv' "${UNPACKED}/start.sh"
! grep -Fq '/data/moox/storage/secrets' "${UNPACKED}/start.sh"
! grep -Fq 'trpc.moox.factor.FactorMgr' "${UNPACKED}/config/merge-trpc.yaml"
grep -Fq '11416' "${UNPACKED}/config/merge-trpc.yaml"
grep -Fq 'http://127.0.0.1:11416/readyz' "${UNPACKED}/healthcheck.sh"
grep -Fq 'stop.sh' "${UNPACKED}/start.sh"
grep -Fq '11416' "${UNPACKED}/start.sh"
bash -n "${UNPACKED}/start.sh"
bash -n "${UNPACKED}/stop.sh"
bash -n "${UNPACKED}/healthcheck.sh"

PACKAGER="${ROOT}/scripts/build/package-factor-merge.sh"
bash -n "${PACKAGER}"
grep -Fq 'scripts/build/package-service.sh' "${PACKAGER}"
grep -Fq 'modules/factor/config/merge-app.yaml' "${PACKAGER}"
grep -Fq 'scripts/deploy/factor-merge/start.sh' "${PACKAGER}"
grep -Fq 'MOOX_LINUX_CGO_TARGET=factor-merge' "${PACKAGER}"
grep -Fq 'scripts/build/build-storage-linux.sh' "${PACKAGER}"
! grep -Eq 'secrets|certs' "${PACKAGER}"

echo "factor merge package contract passed"
