#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ROOT="$(cd "${SCRIPT_DIR}/../../.." && pwd)"

# This is the hermetic partition E2E gate. It starts an embedded JetStream
# server, publishes a blocked metrics delivery and verifies that the Kline
# durable continues independently. No production credentials or services are
# required; real deployment acceptance remains a separate operator gate.
(cd "${ROOT}/modules/storage" && CGO_ENABLED=1 go test ./internal/service/e2e -run '^TestViewConsumerPartitionsKeepKlineIndependentFromMetrics$' -count=1)
(cd "${ROOT}/modules/eventbus" && go test ./test -run '^TestRegistryReconcilesStorageFanoutTopologyAndDeduplicatesPublish$' -count=1)

printf '%s\n' "storage view consumer partition E2E passed"
