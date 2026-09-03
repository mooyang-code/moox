#!/usr/bin/env bash
set -euo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../../.." && pwd -P)"

# This hermetic test runs the real View Maintainer path with a real DuckDB
# engine and two series: one offender (max+1 rows) and one below the limit.
# It verifies the single-series trigger, audit details, A/B activation and
# replacement writes and per-series row limits without requiring production
# credentials or EventBus.
(cd "${REPO_ROOT}/modules/storage" && CGO_ENABLED=1 go test ./internal/service/view -run '^TestSeriesCapacityMaintainerRebuildsWhenOneSeriesExceedsLimit$' -count=1 -v)

printf '%s\n' "storage view series capacity e2e passed"
