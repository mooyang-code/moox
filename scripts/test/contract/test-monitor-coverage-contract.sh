#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../../.." && pwd)"

(cd "${ROOT}/packages/doctor" && go test -count=1 ./...)
(cd "${ROOT}/modules/admin" && go test -count=1 ./internal/service/sysdeploy)

python3 - "${ROOT}" <<'PY'
import pathlib
import sys

import yaml

root = pathlib.Path(sys.argv[1])
catalog = yaml.safe_load((root / "packages/servicecatalog/catalog.yaml").read_text())
policy_path = root / "config/setup/dataset-health-policy.yaml"
policy_text = policy_path.read_text()
policy = yaml.safe_load(policy_text)
components = {item["id"]: item for item in catalog["components"]}
if (root / "packages/doctor/components.yaml").exists():
    raise SystemExit("Doctor must use the shared component catalog")
for name, component in components.items():
    doctor = component["doctor"]
    if doctor["transport"] not in {"reporter", "host_snapshot", "health_only"}:
        raise SystemExit(f"{name}: unsupported transport")
    if not set(doctor.get("dependencies", [])).issubset(components):
        raise SystemExit(f"{name}: unknown component dependency")
    if component["health"]["kind"] not in {"readyz", "https", "none"}:
        raise SystemExit(f"{name}: unsupported health kind")
proxy = components["console-proxy"]
health = proxy["health"]
if proxy["doctor"]["transport"] != "health_only" or health["kind"] != "readyz" or health["port"] != 19528 or health.get("loopback") is not True:
    raise SystemExit("console-proxy diagnostics contract changed")

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

for module, registration, inventory_path, constructor in (
    ("collector", "internal/bootstrap/bootstrap.go", "internal/observability/realtime_inventory.go", "NewRealtimeInventory"),
    ("factor", "internal/bootstrap/metrics_reporter.go", "internal/bootstrap/dataset_observer.go", "newFactorDatasetObserver"),
):
    bootstrap = (root / "modules" / module / registration).read_text()
    inventory = (root / "modules" / module / inventory_path).read_text()
    if "NewDatasetMetrics" not in bootstrap or constructor not in bootstrap:
        raise SystemExit(f"{module}: DatasetMetrics inventory is not registered")
    if "ReplaceExpected" not in inventory:
        raise SystemExit(f"{module}: realtime inventory does not replace expected datasets")

print(f"monitor coverage contract: {len(components)} independent processes")
PY
