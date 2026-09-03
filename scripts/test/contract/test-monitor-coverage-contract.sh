#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../../.." && pwd)"

(cd "${ROOT}/packages/doctor" && go test -count=1 ./...)
(cd "${ROOT}/modules/admin" && go test -count=1 ./internal/service/sysdeploy)

python3 - "${ROOT}" <<'PY'
import pathlib
import re
import sys

import yaml

root = pathlib.Path(sys.argv[1])
manifest = yaml.safe_load((root / "packages/doctor/components.yaml").read_text())
seed = yaml.safe_load((root / "config/setup/service-deployments.yaml").read_text())
defaults = (root / "modules/admin/internal/service/sysdeploy/defaults.go").read_text()
policy_path = root / "config/setup/dataset-health-policy.yaml"
policy_text = policy_path.read_text()
policy = yaml.safe_load(policy_text)

all_components = {
    item["service_name"]: item
    for item in manifest["components"]
}
components = {
    item["service_name"]: item
    for item in manifest["components"]
    if item.get("required_in_default_profile")
}
seed_processes = {
    item["name"]: item
    for item in seed["services"]
    if item.get("deployment_mode") == "process" and item.get("status") == "active"
}
default_names = set(re.findall(r'deployment\("([^"]+)"', defaults))

if not set(components).issubset(seed_processes):
    missing = sorted(set(components) - set(seed_processes))
    raise SystemExit(f"manifest/seed process mismatch: missing={missing}")
unknown = sorted(set(seed_processes) - set(all_components))
if unknown:
    raise SystemExit(f"seed contains active process absent from manifest: {unknown}")

missing_defaults = sorted(set(components) - default_names)
if missing_defaults:
    raise SystemExit(f"SysDeploy defaults missing manifest services: {missing_defaults}")

for name, component in components.items():
    service = seed_processes[name]
    extra = service.get("extra_config") or {}
    health_url = extra.get("health_url", "")
    if not health_url.endswith(component["health_path"]):
        raise SystemExit(
            f"{name}: seed health_url {health_url!r} does not match {component['health_path']!r}"
        )
    if extra.get("monitor_enabled") is not True:
        raise SystemExit(f"{name}: seed monitoring must be enabled")
    if component["transport"] not in {"reporter", "host_snapshot", "health_only"}:
        raise SystemExit(f"{name}: unsupported transport {component['transport']!r}")

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

print(f"monitor coverage contract: {len(components)} independent processes")
PY
