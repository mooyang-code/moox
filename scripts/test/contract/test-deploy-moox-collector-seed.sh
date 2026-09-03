#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../../.." && pwd -P)"
DEPLOY="${ROOT}/scripts/deploy/deploy-moox.sh"

grep -q 'collector-rules.yaml' "${DEPLOY}"
grep -q -- '--seed-file ../config/setup/collector-rules.yaml' "${DEPLOY}"
test -f "${ROOT}/config/setup/collector-rules.yaml"

if grep -n -- 'INSERT OR REPLACE' "${DEPLOY}"; then
  echo "collector deployment must not replace user rules" >&2
  exit 1
fi

echo "collector seed deployment contract: ok"
