#!/usr/bin/env bash
set -euo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/../../.." && pwd -P)"

# Stream 与权限都由 packages/events 的拓扑声明推出；生成的权限金样是两者的落地结果。
acl_golden="${repo_root}/modules/admin/cmd/cli/testdata/eventbus-acl.json"
storage_subjects=(
  'moox.event.storage.dataset.period.completed.v1.>'
  'moox.event.storage.view.data.ready.v2.>'
  # Storage 不再消费 MOOX_STORAGE；因子引擎的消费者是固定名字。
  'factor_collector_period_v1'
)
for subject in "${storage_subjects[@]}"; do
  grep -Fq "${subject}" "${acl_golden}" || {
    echo "storage consistency contract failed: generated EventBus ACL is missing ${subject}" >&2
    exit 1
  }
done

deprecated_merge_subject='moox.event.storage.merge'
for config in "${acl_golden}" "${repo_root}/packages/events/registry.go"; do
  if grep -Fq "${deprecated_merge_subject}" "${config}"; then
    echo "storage consistency contract failed: retired Merge subject remains in ${config}" >&2
    exit 1
  fi
done

cd "${repo_root}/modules/storage"
env GOCACHE="${GOCACHE:-/tmp/moox-gocache}" go test -count=1 \
  ./internal/service/datanode/... \
  ./internal/service/primarystore \
  ./internal/service/viewindex/... \
  ./internal/service/view \
  ./test
