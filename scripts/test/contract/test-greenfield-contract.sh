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

check_absent "reserved protocol declarations and markers are forbidden" \
  '\b[Rr]eserved\b' \
  --glob '*.proto' --glob '*.thrift' .
check_absent "deprecated protocol declarations and markers are forbidden" \
  '\b[Dd]eprecated\b' \
  --glob '*.proto' --glob '*.thrift' .
check_absent "retired compatibility symbols remain" \
  'migrateTaskInstanceSchema|DropLegacyHostSampleTables|EnsureMetricRuleStateColumns|LegacyKVEntry|LEGACY_PACKAGE_TYPE|legacyKey\(|releaseLiveGate|FRAME_READY[[:space:]]*=' \
  --glob '*.go' --glob '*.py' --glob '*.ts' --glob '!**/*_test.go' modules packages web
check_absent "retired Collector callback fields remain" \
  'ServerIP|ServerPort|server_ip|server_port' \
  --glob '*.go' modules/collector
check_absent "retired schema cleanup remains" \
  'DROP TABLE IF EXISTS (t_user_actions|t_collector_execution_logs|t_factor_runs)' \
  --glob '*.sql' modules

check_absent "retired frontend routes remain in active code or documentation" \
  '/(settings/service-deployments|data/datasets|ops/resource-monitor|ops/service-monitor|ops/metric-monitor)' \
  --glob '*.ts' --glob '*.vue' --glob '*.md' \
  web/src/router web/src/api/modules/system web/tests/*.e2e.spec.ts docs/*.md examples

check_absent "unenforced Dataset uniqueness contract remains" \
  'is_unique|c_is_unique|IsUnique|GetIsUnique' \
  --glob '*.proto' --glob '*.go' --glob '*.sql' --glob '*.ts' --glob '*.vue' --glob '*.yaml' \
  modules packages web/src examples
check_absent "single-value row operation contract remains" \
  'RowFieldOperation|ROW_FIELD_OPERATION' \
  --glob '*.proto' --glob '*.go' --glob '*.ts' --glob '*.vue' \
  modules/storage packages/storagepb web/src

node scripts/check/check-collector-planned-node-removal.mjs

# Do not gate the greenfield contract on x/tools/deadcode. MooX exposes
# package-internal APIs through tRPC registration and retains bounded manual
# catch-up/E2E helpers alongside the Timer runtime; deadcode's Go 1.25 roots
# cannot see those reflective/test entry points and reports them as unreachable.
# Module-local go test/go vet and the explicit contract checks above remain the
# authoritative gates for this repository.

exit "$failures"
