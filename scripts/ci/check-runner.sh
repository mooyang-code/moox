#!/usr/bin/env bash
set -euo pipefail

required_commands=(git curl jq gh go node pnpm make rsync rg python3)
for command_name in "${required_commands[@]}"; do
  command -v "${command_name}" >/dev/null || {
    echo "missing self-hosted runner dependency: ${command_name}" >&2
    exit 1
  }
done

go version
node --version
pnpm --version
gh --version | head -1

python3 - <<'PY'
import importlib

for name in ("numpy", "pandas"):
    importlib.import_module(name)
print("python runtime: numpy and pandas available")
PY

available_kb="$(df -Pk "${GITHUB_WORKSPACE:-.}" | awk 'NR == 2 { print $4 }')"
if [[ -z "${available_kb}" || "${available_kb}" -lt 524288 ]]; then
  echo "self-hosted runner needs at least 512 MiB free in the workspace filesystem" >&2
  exit 1
fi
