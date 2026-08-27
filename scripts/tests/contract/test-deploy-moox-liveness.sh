#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../../.." && pwd -P)"
SCRIPT="${ROOT}/scripts/deploy-moox.sh"

grep -Fq 'factor) url=http://127.0.0.1:11414/healthz ;;' "${SCRIPT}"
grep -Fq 'storage-node) url=http://127.0.0.1:20212/healthz ;;' "${SCRIPT}"
! grep -Fq 'factor|storage-node) return 1' "${SCRIPT}"

body="$(awk '/^probe_liveness\(\)/,/^}/' "${SCRIPT}")"
grep -Fq 'sign_health_request GET /healthz' <<<"${body}"
grep -Fq 'probe_service "${name}"' <<<"${body}"

echo 'deploy liveness contract passed'
