#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
cd "$ROOT/modules/storage"

# Optional deployment credentials are accepted by path only and never printed.
if [[ -n "${MOOX_STORAGE_CUSTOM_TOML:-}" && ! -f "${MOOX_STORAGE_CUSTOM_TOML}" ]]; then
  echo "custom storage config does not exist" >&2
  exit 1
fi

env GOCACHE="${GOCACHE:-/tmp/moox-gocache}" go test -count=1 ./internal/service/datanode -run TestTwoDataNodesHostIndependentDatasets
env GOCACHE="${GOCACHE:-/tmp/moox-gocache}" go test -count=1 ./internal/service/e2e -run TestPrimaryDataNodeViewFlow
