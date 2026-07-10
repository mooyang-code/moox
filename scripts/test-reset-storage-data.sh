#!/usr/bin/env bash
set -euo pipefail

ROOT="$(mktemp -d)"
trap 'rm -rf "${ROOT}"' EXIT
mkdir -p "${ROOT}/data/storage" "${ROOT}/data/storage/duckdb" "${ROOT}/data/storage/pebble"
printf 'old\n' > "${ROOT}/data/storage/duckdb/views.duckdb"

if scripts/reset-storage-view-indexes.sh --root "${ROOT}" --all-storage-data >/dev/null 2>&1; then
  echo 'reset unexpectedly accepted without --yes' >&2
  exit 1
fi
scripts/reset-storage-view-indexes.sh --root "${ROOT}" --all-storage-data --yes >/dev/null
[[ -d "${ROOT}/data/storage" ]]
[[ ! -e "${ROOT}/data/storage/duckdb/views.duckdb" ]]

mkdir -p "${ROOT}/data/storage"
if scripts/reset-storage-view-indexes.sh --root "${ROOT}/data/storage" --all-storage-data --yes >/dev/null 2>&1; then
  echo 'reset unexpectedly accepted a storage root as deploy root' >&2
  exit 1
fi

mkdir -p "${ROOT}/data/storage"
printf '%s\n' "$$" > "${ROOT}/storage.pid"
if scripts/reset-storage-view-indexes.sh --root "${ROOT}" --all-storage-data --yes >/dev/null 2>&1; then
  echo 'reset unexpectedly accepted a live managed pid' >&2
  exit 1
fi
rm -f "${ROOT}/storage.pid"

echo 'test-reset-storage-data: PASS'
