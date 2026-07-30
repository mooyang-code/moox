#!/usr/bin/env bash
set -euo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"

grep -Fq 'subjects: ["moox.storage.dataset.rows.upserted.v2.>"]' \
  "${repo_root}/modules/eventbus/config/app.yaml" || {
  echo "storage consistency contract failed: EventBus storage stream is not v2" >&2
  exit 1
}
grep -Fq 'moox.storage.dataset.rows.upserted.v2.>' \
  "${repo_root}/modules/admin/cmd/cli/eventbus_credentials.go" || {
  echo "storage consistency contract failed: generated EventBus ACL is not v2" >&2
  exit 1
}

cd "${repo_root}/modules/storage"
env GOCACHE="${GOCACHE:-/tmp/moox-gocache}" go test -count=1 \
  ./internal/service/datanode/... \
  ./internal/service/primarystore \
  ./internal/service/viewindex/... \
  ./internal/service/view \
  ./test
