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
grep -q 'modules/trade/config/.' "${ROOT}/scripts/release.sh"
grep -q 'RELEASE_ROOT}/trade/config' "${ROOT}/scripts/release.sh"
