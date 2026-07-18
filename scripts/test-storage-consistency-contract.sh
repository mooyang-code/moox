#!/usr/bin/env bash
set -euo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "${repo_root}/modules/storage"
exec go test -tags storage_consistency_contract -count=1 ./test -run StorageConsistencyContract
