#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../../.." && pwd)"
COLLECTOR="${ROOT}/modules/collector"

for token in \
  gapAudit \
  GapAudit \
  MOOX_COLLECTOR_GAP_AUDIT_DISABLED \
  auditGaps \
  lastGapAudit \
  gapAuditCursorID
do
  if matches="$(rg -n -F -- "$token" "$COLLECTOR" || true)"; then
    if [[ -n "$matches" ]]; then
      printf 'collector still contains forbidden gap-audit token %q:\n%s\n' "$token" "$matches" >&2
      exit 1
    fi
  fi
done

for token in HistoryPolicy BatchKindBackfill BatchKindGapRepair; do
  if ! rg -n -F -- "$token" "$COLLECTOR/internal/domain" "$COLLECTOR/internal/marketfetch" >/dev/null; then
    printf 'collector is missing preserved contract %q\n' "$token" >&2
    exit 1
  fi
done

printf 'collector no-gap-audit contract passed (local source check only)\n'
