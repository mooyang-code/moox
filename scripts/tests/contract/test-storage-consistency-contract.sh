#!/usr/bin/env bash
set -euo pipefail

script_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd -P)"
if [[ "$(basename "${script_dir}")" == "contract" ]]; then
  # Direct invocation from scripts/tests/contract.
  repo_root="$(cd "${script_dir}/../../.." && pwd -P)"
else
  # Keep the compatibility wrapper at scripts/test-storage-consistency-contract.sh usable.
  repo_root="$(cd "${script_dir}/.." && pwd -P)"
fi

storage_subjects=(
  'moox.storage.dataset.rows.upserted.v2.>'
  'moox.storage.dataset.period.collected.v1.>'
  'moox.storage.view.source_period.ready.v1.>'
  'moox.storage.dataset.factor_period.computed.v1.>'
  'moox.storage.view.factor_period.ready.v1.>'
  'moox.storage.dataset.sync_point.v1.>'
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

for durable in storage_view_period_v1 factor_view_ready_v1; do
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
