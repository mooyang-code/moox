#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "${ROOT}/modules/storage"
env GOCACHE="${GOCACHE:-/tmp/moox-gocache}" go test -tags storage_consistency_contract -count=1 ./test -run StorageConsistencyContract
