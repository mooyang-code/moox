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

for module in ("collector", "factor"):
    bootstrap = (root / "modules" / module / "internal/bootstrap/bootstrap.go").read_text()
    inventory = (root / "modules" / module / "internal/observability/realtime_inventory.go").read_text()
    if "NewDatasetMetrics" not in bootstrap or "NewRealtimeInventory" not in bootstrap:
        raise SystemExit(f"{module}: DatasetMetrics inventory is not registered")
    if "ReplaceExpected" not in inventory:
        raise SystemExit(f"{module}: realtime inventory does not replace expected datasets")

print("monitor coverage contract passed")
PY
