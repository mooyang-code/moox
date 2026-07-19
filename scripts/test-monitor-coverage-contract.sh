#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"

(cd "${ROOT}/packages/doctor" && go test -count=1 ./...)
(cd "${ROOT}/modules/admin" && go test -count=1 ./internal/service/sysdeploy)

python3 - "${ROOT}" <<'PY'
import pathlib
import re
import sys

import yaml

root = pathlib.Path(sys.argv[1])
manifest = yaml.safe_load((root / "packages/doctor/components.yaml").read_text())
seed = yaml.safe_load((root / "examples/service-deployments.seed.yaml").read_text())
defaults = (root / "modules/admin/internal/service/sysdeploy/defaults.go").read_text()

components = {
    item["service_name"]: item
    for item in manifest["components"]
    if item.get("required_in_default_profile")
}
seed_processes = {
    item["name"]: item
    for item in seed["services"]
    if item.get("deployment_mode") == "process"
}
default_names = set(re.findall(r'deployment\("([^"]+)"', defaults))

if set(components) != set(seed_processes):
    missing = sorted(set(components) - set(seed_processes))
    extra = sorted(set(seed_processes) - set(components))
    raise SystemExit(f"manifest/seed process mismatch: missing={missing}, extra={extra}")

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

print(f"monitor coverage contract: {len(components)} independent processes")
PY
