#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
cd "$ROOT/modules/storage"

# An optional deployment file may contain credentials for the two remote
# nodes. It is intentionally never printed; the deterministic E2E below uses
# two isolated DataNode Pebble roots so it also runs in CI without secrets.
if [[ -n "${MOOX_STORAGE_CUSTOM_TOML:-}" && ! -f "${MOOX_STORAGE_CUSTOM_TOML}" ]]; then
  echo "custom storage config does not exist" >&2
  exit 1
fi

env GOCACHE="${GOCACHE:-/tmp/moox-gocache}" go test -count=1 ./internal/service/datanode -run TestTwoDataNodesHostIndependentDatasets
