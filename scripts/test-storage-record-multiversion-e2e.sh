#!/usr/bin/env bash
set -euo pipefail

# Keep this check bounded and hermetic. The service-level tests exercise the
# same Access -> PrimaryStore -> journal -> Bleve contracts without requiring a
# shared developer NATS daemon or a fixed port allocation.
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "${ROOT}"

(cd modules/storage && go test -race -count=1 \
  ./internal/infra/device/pebble \
  ./internal/services/primary \
  ./internal/services/access \
  ./internal/services/view \
  ./internal/services/view/builder \
  ./internal/infra/device/bleve)
(cd modules/collector && go test -count=1 ./internal/sources/binance)
bash scripts/test-reset-storage-data.sh
printf 'test-storage-record-multiversion-e2e: PASS\n'
