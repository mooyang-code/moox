#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../../.." && pwd)"
storage_worktree_before="$(git -C "${ROOT}" status --short -- modules/storage)"

(cd "${ROOT}/modules/monitor" && go test -count=1 ./internal/doctor ./internal/rpc ./test)
(cd "${ROOT}/modules/cli" && go test -count=1 ./internal/doctor ./internal/command ./test -run 'StorageDatasetActivation|DoctorBootstrap|StorageMetadataClient|TestDoctor|TestValidateDoctorFlags')
(cd "${ROOT}/packages/doctor" && go test -count=1 ./...)
(cd "${ROOT}/packages/report" && go test -count=1 ./...)

grep -q 'bootstrap.storage_dataset_activation' "${ROOT}/modules/cli/internal/doctor/storage_activation.go"
grep -q 'CheckDatasetActivation' "${ROOT}/modules/cli/internal/doctor/storage_activation.go"
if rg -n 'ActivateDataset\(' "${ROOT}/modules/cli/internal/doctor/storage_activation.go"; then
  echo "Doctor storage activation observations must not activate Datasets" >&2
  exit 1
fi

bash -n "${ROOT}/scripts/deploy/deploy-moox.sh"
grep -q 'MOOX_SERVICE_NAME=${service_name}' "${ROOT}/scripts/deploy/deploy-moox.sh"
# Catalog/Doctor and release-byte identity are checked by packages/doctor.
# The deployment inventory migrates to HostPlacements in the gateway plan.
if [[ -e "${ROOT}/packages/doctor/components.yaml" ]]; then
  echo "Doctor must not retain a duplicate component catalog" >&2
  exit 1
fi

storage_worktree_after="$(git -C "${ROOT}" status --short -- modules/storage)"
if [[ "${storage_worktree_after}" != "${storage_worktree_before}" ]]; then
  echo "Doctor E2E must not modify modules/storage" >&2
  exit 1
fi

echo "doctor focused Go tests and deployment contract checks passed"
