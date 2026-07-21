#!/usr/bin/env bash
set -euo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "${repo_root}/modules/storage"
env GOCACHE="${GOCACHE:-/tmp/moox-gocache}" go test -count=1 \
  ./internal/service/datanode/... \
  ./internal/service/primarystorev2 \
  ./internal/service/viewindex/... \
  ./internal/service/viewv2 \
  ./test
