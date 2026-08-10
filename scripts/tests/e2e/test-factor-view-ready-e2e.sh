#!/usr/bin/env bash
set -euo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../../.." && pwd -P)"

cd "${REPO_ROOT}/modules/factor"
go test ./test -run '^TestViewReady(Combination(PeriodChain|FailureReportsDegradedMarker)|Pipeline(OverlapsReadsAndRetriesTimeoutAtTail|FinalReadTimeoutDegradesOnlyFailedSubject))$' -count=1 -v

# When a deployment root is supplied, also run the real Storage-backed test.
# Keeping this opt-in makes the contract check useful on a developer laptop
# while preserving the same entry point for release acceptance.
if [[ "${MOOX_RUN_REAL_FACTOR_E2E:-0}" == "1" ]]; then
  exec "${REPO_ROOT}/scripts/tests/e2e/test-factor-storage-e2e.sh"
fi
