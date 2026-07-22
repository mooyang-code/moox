#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
cd "${ROOT}"

echo "==> verify generated Proto output is clean"
make proto-check

echo "==> verify DataNode management contract"
bash scripts/test-storage-datanode-management-contract.sh

echo "==> run backend DataNode lifecycle E2E"
(cd modules/storage && CGO_ENABLED=1 go test -count=1 ./internal/service/e2e -run TestDataNodeManagementLifecycle -v)

echo "==> run Doctor read-only E2E"
bash scripts/test-doctor-e2e.sh

echo "storage DataNode management E2E: ok"
