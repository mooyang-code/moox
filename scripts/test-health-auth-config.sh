#!/usr/bin/env bash
set -euo pipefail

ROOT=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)

"${ROOT}/scripts/test-trpc-plugin-config.sh" >/dev/null

printf 'PASS: Prometheus plugin listeners are complete and loopback-only\n'
