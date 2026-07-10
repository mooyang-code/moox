#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../../.." && pwd)"
SCRIPT="${ROOT}/skills/moox/scripts/hostagent-deploy.sh"
bash -n "${SCRIPT}"
grep -q 'id -u' "${SCRIPT}"
grep -q 'systemctl --user' "${SCRIPT}"
grep -q 'install -m 0600' "${SCRIPT}"
grep -q 'sub(/\\/$/' "${SCRIPT}"
if grep -q 'sudo' "${SCRIPT}"; then
  echo "host-agent deploy must remain rootless" >&2
  exit 1
fi
echo "host-agent deploy contract passed"
