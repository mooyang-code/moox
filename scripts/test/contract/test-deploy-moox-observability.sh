#!/usr/bin/env bash
set -euo pipefail
REPO="$(cd "$(dirname "${BASH_SOURCE[0]}")/../../.." && pwd -P)"
DEPLOY="${REPO}/scripts/deploy/deploy-moox.sh"
ROOT="$(mktemp -d)"
trap 'rm -rf "${ROOT}"' EXIT
mkdir -p "${ROOT}/config"

generation="$(awk '/^  # Keep explicit policy independent/{copy=1} copy && /^  if .*WITH_COLLECTOR/{exit} copy{print}' "${DEPLOY}")"
test -n "${generation}"
STAGE_DIR="${ROOT}/stage"
mkdir -p "${STAGE_DIR}/config"
WITH_MONITOR=1
MOOX_OBSERVABILITY_CONFIG_EXPLICIT=0
eval "${generation}"
test ! -e "${STAGE_DIR}/config/monitor-runtime.env"
MOOX_OBSERVABILITY_CONFIG_EXPLICIT=1
for policy in all new; do
  MOOX_OBSERVABILITY_DELIVER_POLICY="${policy}"
  eval "${generation}"
  test "$(cat "${STAGE_DIR}/config/monitor-runtime.env")" = "MOOX_OBSERVABILITY_DELIVER_POLICY=${policy}"
done
rm "${STAGE_DIR}/config/monitor-runtime.env"
WITH_MONITOR=0
eval "${generation}"
test ! -e "${STAGE_DIR}/config/monitor-runtime.env"

# Exercise the actual generated startup policy loader without starting services.
loader="$(awk '/^if \[\[ -r "\$\{ROOT\}\/config\/monitor-runtime.env"/{copy=1} copy && /^if \[\[ -r "\$\{ROOT\}\/config\/resources.env"/{exit} copy{print}' "${DEPLOY}")"
test -n "${loader}"
unset MOOX_OBSERVABILITY_DELIVER_POLICY
eval "${loader}"
test "${MOOX_OBSERVABILITY_DELIVER_POLICY}" = all
printf 'MOOX_OBSERVABILITY_DELIVER_POLICY=new\n' >"${ROOT}/config/monitor-runtime.env"
MOOX_OBSERVABILITY_DELIVER_POLICY=all
eval "${loader}"
test "${MOOX_OBSERVABILITY_DELIVER_POLICY}" = new
printf 'MOOX_OBSERVABILITY_DELIVER_POLICY=typo\n' >"${ROOT}/config/monitor-runtime.env"
if (eval "${loader}") 2>/dev/null; then
  echo 'invalid persisted policy was accepted' >&2
  exit 1
fi
if MOOX_OBSERVABILITY_DELIVER_POLICY=typo bash "${DEPLOY}" --help >/dev/null 2>&1; then
  echo 'invalid packaging policy was accepted' >&2
  exit 1
fi
grep -Fq 'MOOX_OBSERVABILITY_DELIVER_POLICY=all' "${DEPLOY}"
grep -Fq '"MOOX_OBSERVABILITY_DELIVER_POLICY=${MOOX_OBSERVABILITY_DELIVER_POLICY}"' "${DEPLOY}"
grep -Fq "rsync_excludes+=(--exclude '/config/monitor-runtime.env')" "${DEPLOY}"
echo 'observability deployment contract passed'
