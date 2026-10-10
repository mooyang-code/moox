#!/usr/bin/env bash
set -euo pipefail
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd -P)"
MOOX_PACKAGE_CLI="${MOOX_CLI:-${ROOT}/bin/moox-cli}"
if [[ ! -x "${MOOX_PACKAGE_CLI}" ]]; then
  printf '%s\n' 'package-deployment: build a native moox-cli first, or set MOOX_CLI to its path' >&2
  exit 1
fi
cd "${ROOT}"
exec "${MOOX_PACKAGE_CLI}" setup package "$@"
