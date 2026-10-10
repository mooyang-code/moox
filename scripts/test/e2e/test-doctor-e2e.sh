#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../../.." && pwd)"
storage_worktree_before="$(git -C "${ROOT}" status --short -- modules/storage)"

(cd "${ROOT}/modules/monitor" && go test -count=1 ./internal/doctor ./internal/rpc ./test)
(cd "${ROOT}/modules/cli" && go test -count=1 ./internal/doctor ./internal/command ./test -run 'StorageDatasetActivation|DoctorBootstrap|StorageMetadataClient|TestDoctor|TestValidateDoctorFlags|Bootstrap|LocalHealth')
(cd "${ROOT}/packages/doctor" && go test -count=1 ./...)
(cd "${ROOT}/packages/report" && go test -count=1 ./...)

grep -q 'bootstrap.storage_dataset_activation' "${ROOT}/modules/cli/internal/doctor/storage_activation.go"
grep -q 'CheckDatasetActivation' "${ROOT}/modules/cli/internal/doctor/storage_activation.go"
if rg -n 'ActivateDataset\(' "${ROOT}/modules/cli/internal/doctor/storage_activation.go"; then
  echo "Doctor storage activation observations must not activate Datasets" >&2
  exit 1
fi

# 部署时每个组件都带上身份环境变量（Doctor 按它关联上报与部署）。
grep -Fq '"MOOX_SERVICE_NAME=" + id' "${ROOT}/modules/cli/internal/setup/release/components.go"
grep -Fq '"MOOX_INSTANCE_ID=" + id + "@" + r.host.ID' "${ROOT}/modules/cli/internal/setup/release/components.go"

storage_worktree_after="$(git -C "${ROOT}" status --short -- modules/storage)"
if [[ "${storage_worktree_after}" != "${storage_worktree_before}" ]]; then
  echo "Doctor E2E must not modify modules/storage" >&2
  exit 1
fi

echo "doctor focused Go tests and deployment contract checks passed"
