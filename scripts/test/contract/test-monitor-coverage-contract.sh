#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../../.." && pwd)"

# 组件和探测方式只来自组件目录，Monitor 按部署生成检查；这里校验两边的单测和数据集健康策略。
(cd "${ROOT}/packages/doctor" && go test -count=1 ./...)
(cd "${ROOT}/packages/servicecatalog" && go test -count=1 ./...)
(cd "${ROOT}/modules/monitor" && go test -count=1 ./internal/placement)

python3 - "${ROOT}" <<'PY'
import pathlib
import sys

import yaml

root = pathlib.Path(sys.argv[1])
policy_path = root / "config/setup/dataset-health-policy.yaml"
policy_text = policy_path.read_text()
policy = yaml.safe_load(policy_text)

if policy.get("version") != 2:
    raise SystemExit("monitor policy must use version 2")
if "crosses_storage_deferred" in policy_text:
    raise SystemExit("monitor policy still contains crosses_storage_deferred")
for removed in ("canary_subject_id", "market_price_change_ratio", "market_volume_ratio"):
    if removed in policy_text:
        raise SystemExit(f"Dataset health policy contains unused field {removed}")
defaults_policy = ((policy.get("realtime_timeseries") or {}).get("defaults") or {})
required_defaults = {
    "run_missed_intervals",
    "success_missed_intervals",
    "watermark_periods",
    "minimum_watermark_lag",
}
if set(defaults_policy) != required_defaults:
    raise SystemExit(
        f"monitor policy defaults mismatch: got={sorted(defaults_policy)}"
    )

# 两个模块都要登记数据集指标，并把「预期的实时数据集」整体替换进去：
# Collector 在 internal/observability 里维护清单，Factor 在 internal/bootstrap 的观察者里维护。
collector = root / "modules/collector/internal"
collector_bootstrap = (collector / "bootstrap/bootstrap.go").read_text()
collector_inventory = (collector / "observability/realtime_inventory.go").read_text()
if "NewDatasetMetrics" not in collector_bootstrap or "NewRealtimeInventory" not in collector_bootstrap:
    raise SystemExit("collector: DatasetMetrics inventory is not registered")
if "ReplaceExpected" not in collector_inventory:
    raise SystemExit("collector: realtime inventory does not replace expected datasets")

factor = root / "modules/factor/internal/bootstrap"
factor_reporter = (factor / "metrics_reporter.go").read_text()
factor_observer = (factor / "dataset_observer.go").read_text()
if "NewDatasetMetrics" not in factor_reporter:
    raise SystemExit("factor: DatasetMetrics is not registered")
if "ReplaceExpected" not in factor_observer:
    raise SystemExit("factor: dataset observer does not replace expected datasets")

print("monitor coverage contract passed")
PY
