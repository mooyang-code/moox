#!/usr/bin/env bash
set -euo pipefail
ROOT=$(cd "$(dirname "${BASH_SOURCE[0]}")/../../.." && pwd)
SCRIPT="${ROOT}/skills/moox/scripts/caddy-prerequisite.sh"
out=$("${SCRIPT}" status --target localhost --deploy-dir "${TMPDIR:-/tmp}/moox-caddy-absent-$$")
grep -Fq '"version":"v2.11.4"' <<<"${out}"
grep -Fq '"installed":false' <<<"${out}"
grep -Fq 'BatchMode=yes' "${SCRIPT}"
grep -Fq 'caddy-managed.sh' "${SCRIPT}"
printf 'PASS: skill prerequisite delegates to managed helper\n'
