#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"

(cd "${ROOT}/modules/monitor" && go test -count=1 ./internal/doctor ./internal/rpc ./test)
(cd "${ROOT}/modules/cli" && go test -count=1 ./internal/doctor ./internal/doctorclient ./internal/command ./test -run 'StorageDatasetActivation|DoctorBootstrap|StorageMetadataClient|TestDoctor|TestValidateDoctorFlags')
(cd "${ROOT}/packages/doctor" && go test -count=1 ./...)
(cd "${ROOT}/packages/report" && go test -count=1 ./...)

grep -q 'bootstrap.storage_dataset_activation' "${ROOT}/modules/cli/internal/doctor/storage_activation.go"
grep -q 'CheckDatasetActivation' "${ROOT}/modules/cli/internal/doctor/storage_activation.go"
if rg -n 'ActivateDataset\(' "${ROOT}/modules/cli/internal/doctor/storage_activation.go"; then
  echo "Doctor storage activation observations must not activate Datasets" >&2
  exit 1
fi

bash -n "${ROOT}/scripts/deploy-moox.sh"
grep -q 'MOOX_SERVICE_NAME=${service_name}' "${ROOT}/scripts/deploy-moox.sh"
python3 - "${ROOT}/packages/doctor/components.yaml" "${ROOT}/examples/service-deployments.seed.yaml" <<'PY'
import sys
import yaml

manifest = yaml.safe_load(open(sys.argv[1], encoding="utf-8"))
seed = yaml.safe_load(open(sys.argv[2], encoding="utf-8"))
services = {item["name"]: item for item in seed["services"]}
for component in manifest["components"]:
    service = services[component["service_name"]]
    if component.get("required_in_default_profile", False):
        assert service["status"] == "active", component["component_id"]
        assert service["deployment_mode"] == "process", component["component_id"]
    if service["status"] == "active":
        assert component.get("required_in_default_profile", False), component["component_id"]
PY

if git -C "${ROOT}" diff --name-only HEAD -- modules/storage | grep -q .; then
  echo "Doctor E2E must not modify modules/storage" >&2
  exit 1
fi

echo "doctor focused Go tests and deployment contract checks passed"
