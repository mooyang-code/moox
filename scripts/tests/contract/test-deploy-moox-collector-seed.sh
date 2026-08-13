#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd -P)"
if [[ "$(basename "${SCRIPT_DIR}")" == "contract" ]]; then
  ROOT="$(cd "${SCRIPT_DIR}/../../.." && pwd -P)"
else
  ROOT="$(cd "${SCRIPT_DIR}/.." && pwd -P)"
fi
DEPLOY="${ROOT}/scripts/deploy-moox.sh"

grep -q 'collector-rules.yaml' "${DEPLOY}"
grep -q -- '--seed-file ../examples/setup/default/collector-rules.yaml' "${DEPLOY}"
test -f "${ROOT}/examples/setup/default/collector-rules.yaml"

if grep -n -- 'INSERT OR REPLACE' "${DEPLOY}"; then
  echo "collector deployment must not replace user rules" >&2
  exit 1
fi

echo "collector seed deployment contract: ok"
