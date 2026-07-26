#!/usr/bin/env bash
set -euo pipefail

cd "$(git rev-parse --show-toplevel)"

failures=0
check_absent() {
  local label=$1
  shift
  local matches
  if matches=$(rg -n "$@" 2>/dev/null); then
    printf '%s\n%s\n' "$label" "$matches" >&2
    failures=1
  fi
}

check_absent "protocol reserved declarations are forbidden" \
  '^[[:space:]]*reserved([[:space:]]|$)' \
  --glob '*.proto' --glob '*.thrift' modules packages
check_absent "deprecated protocol fields are forbidden" \
  '\[[[:space:]]*deprecated[[:space:]]*=[[:space:]]*true[[:space:]]*\]' \
  --glob '*.proto' --glob '*.thrift' modules packages
check_absent "retired compatibility symbols remain" \
  'migrateTaskInstanceSchema|DropLegacyHostSampleTables|EnsureMetricRuleStateColumns|LegacyKVEntry|LEGACY_PACKAGE_TYPE|legacyKey\(|releaseLiveGate|FRAME_READY[[:space:]]*=' \
  --glob '*.go' --glob '*.py' --glob '*.ts' --glob '!**/*_test.go' modules packages web
check_absent "retired Collector callback fields remain" \
  'ServerIP|ServerPort|server_ip|server_port' \
  --glob '*.go' modules/collector
check_absent "retired schema cleanup remains" \
  'DROP TABLE IF EXISTS (t_user_actions|t_collector_execution_logs|t_factor_runs)' \
  --glob '*.sql' modules

exit "$failures"
