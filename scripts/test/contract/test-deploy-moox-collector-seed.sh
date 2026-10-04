#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../../.." && pwd -P)"
DEPLOY="${ROOT}/scripts/deploy/deploy-moox.sh"

grep -q 'collection-tasks.yaml' "${DEPLOY}"
grep -q -- '--seed-file ../config/setup/collection-tasks.yaml' "${DEPLOY}"
test -f "${ROOT}/config/setup/collection-tasks.yaml"

if grep -n -- 'INSERT OR REPLACE' "${DEPLOY}"; then
  echo "collector deployment must not replace user rules" >&2
  exit 1
fi

echo "collector seed deployment contract: ok"
