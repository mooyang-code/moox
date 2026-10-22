#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../../.." && pwd)"

# 包含 monitor_coverage_contract_test.go：组件目录、数据集健康策略、数据集指标登记的契约检查。
(cd "${ROOT}/packages/doctor" && go test -count=1 ./...)
(cd "${ROOT}/modules/admin" && go test -count=1 ./internal/service/sysdeploy)
