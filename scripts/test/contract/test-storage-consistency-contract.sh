#!/usr/bin/env bash
set -euo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/../../.." && pwd -P)"

storage_subjects=(
  'moox.event.storage.dataset.rows.upserted.v2.>'
  'moox.event.storage.dataset.period.collected.v1.>'
  'moox.event.storage.view.source_period.ready.v1.>'
  'moox.event.storage.dataset.factor_period.computed.v1.>'
  'moox.event.storage.view.factor_period.ready.v1.>'
  'moox.event.storage.dataset.sync_point.v1.>'
)
for subject in "${storage_subjects[@]}"; do
  grep -Fq "${subject}" "${repo_root}/modules/eventbus/config/app.yaml" || {
    echo "storage consistency contract failed: EventBus storage stream is missing ${subject}" >&2
    exit 1
  }
  grep -Fq "${subject}" "${repo_root}/modules/admin/cmd/cli/eventbus_credentials.go" || {
    echo "storage consistency contract failed: generated EventBus ACL is missing ${subject}" >&2
    exit 1
  }
done

for durable in storage_view_kline storage_view_factor storage_view_metrics storage_view_misc factor_view_ready_v1; do
  grep -Fq "${durable}" "${repo_root}/modules/admin/cmd/cli/eventbus_credentials.go" || {
    echo "storage consistency contract failed: generated EventBus ACL is missing durable ${durable}" >&2
    exit 1
  }
done

cd "${repo_root}/modules/storage"
env GOCACHE="${GOCACHE:-/tmp/moox-gocache}" go test -count=1 \
  ./internal/service/datanode/... \
  ./internal/service/primarystore \
  ./internal/service/viewindex/... \
  ./internal/service/view \
  ./test
