#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"

echo "==> sync go workspace"
cd "${ROOT}"
go work sync

run_module_tests() {
  local module="$1"
  echo "==> test ${module}"
  (cd "${ROOT}/${module}" && go test ./...)
}

run_module_tests modules/cli
run_module_tests modules/control
run_module_tests modules/collector
run_module_tests modules/factor
run_module_tests modules/order
run_module_tests modules/account

echo "==> test modules/storage"
(cd "${ROOT}/modules/storage" && make test)

echo "==> all tests passed"
