#!/usr/bin/env bash
set -euo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../../.." && pwd)"
TMP_DIR="$(mktemp -d)"

cleanup() {
  rm -rf "${TMP_DIR}"
}
trap cleanup EXIT INT TERM

cd "${REPO_ROOT}"
go -C modules/storage test -c -o "${TMP_DIR}/storage-period-helper" ./cmd/server
go -C modules/hostgateway build -o "${TMP_DIR}/gateway-period-helper" ./cmd/e2e-helper
go -C modules/access build -o "${TMP_DIR}/moox-access" ./cmd/server

MOOX_PERIOD_E2E_RUN=1 \
MOOX_PERIOD_E2E_STORAGE_HELPER_BINARY="${TMP_DIR}/storage-period-helper" \
MOOX_PERIOD_E2E_GATEWAY_HELPER_BINARY="${TMP_DIR}/gateway-period-helper" \
MOOX_PERIOD_E2E_ACCESS_BINARY="${TMP_DIR}/moox-access" \
go -C modules/collector test -count=1 -timeout=2m -v ./internal/marketfetch -run '^TestPeriodStorageRPCE2E$' | tee "${TMP_DIR}/period-e2e.log"

grep -Fq -- '--- PASS: TestPeriodStorageRPCE2E' "${TMP_DIR}/period-e2e.log"
for scenario in \
  cleanup-complete-degraded-terminal-deletion \
  cleanup-marker-id-payload-outbox-immutable \
  cleanup-retention \
  cleanup-unresolved-work-retention \
  cleanup-waiting-not-found-network-retention \
  duplicate-subject-two-series \
  failure-first-report-missed-deadline \
  failure-pending-network-recovery \
  failure-receipts \
  failure-recorded-replay-after-deadline \
  frozen-current-next-universe \
  initial-plus-three-retry-exhaustion \
  metadata-dual-provider-marker-subject-dedup \
  metadata-dual-provider-membership \
  mixed-recorded-missed-receipts \
  snapshot-freeze \
  success-bit-replay-after-deadline \
  timer-completion-persistence \
  timer-concurrent-same-request-claim \
  timer-empty-bars-not-success \
  timer-expired-unclaimed-recovery \
  timer-first-late-commit-no-success \
  timer-nontrading-historical-commit \
  timer-oldest-first \
  timer-publish-failure-timeout-three-retries \
  timer-restart-claim \
  timer-restart-single-manifest \
  timer-unreachable-claim-no-writer \
  timer-wrong-runtime-completion-no-effects \
  wrong-series-index-rejection; do
  if ! grep -Fq -- "SCENARIO PASS ${scenario}" "${TMP_DIR}/period-e2e.log"; then
    printf 'Collector period E2E did not execute scenario: %s\n' "${scenario}" >&2
    exit 1
  fi
  printf 'Collector period E2E scenario passed: %s\n' "${scenario}"
done
CGO_ENABLED=1 go -C modules/storage test -count=1 -timeout=1m -v ./internal/service/e2e -run '^TestCollectorPeriodMarkerJetStreamAckE2E$' | tee "${TMP_DIR}/period-marker-jetstream-e2e.log"
grep -Fq -- 'SCENARIO PASS collector-period-marker-jetstream-ack' "${TMP_DIR}/period-marker-jetstream-e2e.log"
printf 'Collector period native RPC E2E passed.\n'
