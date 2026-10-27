#!/usr/bin/env bash
set -euo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/../../.." && pwd -P)"

storage_subjects=(
  'moox.event.storage.dataset.rows.upserted.v2.>'
  'moox.event.storage.collector.period.completed.v1.>'
  'moox.event.storage.dataset.factor_period.computed.v1.>'
  'moox.event.storage.view.data.ready.v1.>'
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

deprecated_merge_subject='moox.event.storage.merge.period.completed.v1.>'
for config in \
  "${repo_root}/modules/eventbus/config/app.yaml" \
  "${repo_root}/modules/admin/cmd/cli/eventbus_credentials.go"; do
  if grep -Fq "${deprecated_merge_subject}" "${config}"; then
    echo "storage consistency contract failed: retired Merge subject remains in ${config}" >&2
    exit 1
  fi
done

# View 消费者按数据集动态创建（view_<哈希>），ACL 靠 MOOX_STORAGE 流上的通配符放行；因子引擎的消费者仍是固定名字。
for subject in 'CONSUMER.CREATE.MOOX_STORAGE.>' 'CONSUMER.MSG.NEXT.MOOX_STORAGE.>' 'factor_collector_period_v1'; do
  grep -Fq "${subject}" "${repo_root}/modules/admin/cmd/cli/eventbus_credentials.go" || {
    echo "storage consistency contract failed: generated EventBus ACL is missing ${subject}" >&2
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
