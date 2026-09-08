#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../../.." && pwd)"

printf 'K-line freshness local integration/contract verification\n'
printf 'This script exercises the local Storage/Monitor contracts only; it does not publish SCF or claim production readiness.\n'

printf '\n==> Collector gap-audit removal contract\n'
bash "${ROOT}/scripts/test/contract/test-collector-no-gap-audit.sh"

printf '\n==> Existing Monitor coverage contract\n'
bash "${ROOT}/scripts/test/contract/test-monitor-coverage-contract.sh"

printf '\n==> Collector HistoryPolicy and explicit Backfill/GapRepair tests\n'
(cd "${ROOT}/modules/collector" && go test -count=1 ./internal/domain ./internal/marketfetch)

printf '\n==> Monitor K-line freshness and market-calendar tests\n'
(cd "${ROOT}/modules/monitor" && go test -count=1 ./internal/config ./internal/metrics ./internal/bootstrap)

printf '\n==> Storage Primary/View K-line freshness tests\n'
(cd "${ROOT}/modules/storage" && go test -count=1 ./internal/observability ./internal/service/primarystore ./internal/service/view)

printf '\n==> Temporary JetStream/SQLite Storage View event pipeline\n'
(cd "${ROOT}/modules/storage" && CGO_ENABLED=1 go test -tags cgo -count=1 ./internal/service/e2e -run '^TestViewConsumerPartitionsKeepKlineIndependentFromMetrics$')

printf '\n==> Temporary JetStream/SQLite Monitor metric ingest\n'
(cd "${ROOT}/modules/monitor" && go test -count=1 ./test -run '^TestEventBusToMonitorHistoryFlow$')

printf '\nK-line freshness local integration/contract verification passed.\n'
printf 'The local run includes temporary JetStream/SQLite service-level flows; production SCF, Timer, Monitor, and Storage acceptance remain separate.\n'
