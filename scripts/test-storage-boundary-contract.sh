#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "${ROOT}"

fail() {
  printf 'storage boundary contract failed: %s\n' "$*" >&2
  exit 1
}

assert_no_match() {
  local pattern="$1"
  shift
  if rg -n --hidden --glob '!docs/superpowers/**' --glob '!**/*_test.go' --glob '!**/*pb.go' --glob '!**/*trpc.go' --glob '!web/node_modules/**' --glob '!node_modules/**' "${pattern}" "$@"; then
    fail "forbidden active-tree match: ${pattern}"
  fi
}

[[ -f docs/存储层架构.md ]] || fail 'docs/存储层架构.md is missing'
grep -Fq '[存储层架构](存储层架构.md)' docs/架构总览.md || fail 'architecture overview does not link storage layer architecture'

[[ ! -d modules/storage/internal/core ]] || fail 'storage internal/core must be absent'
[[ ! -d modules/storage/internal/infra ]] || fail 'storage internal/infra must be absent'
[[ ! -d modules/storage/internal/viewrow ]] || fail 'storage internal/viewrow must be absent'
[[ ! -d modules/storage/internal/response ]] || fail 'storage internal/response must be absent'
[[ ! -d modules/storage/internal/errorcode ]] || fail 'storage internal/errorcode must be absent'
[[ ! -d modules/storage/internal/rpcresult ]] || fail 'storage internal/rpcresult must be absent'

assert_no_match 'trpc\.moox\.storage\.(Access|AccessScan)|Write(TimeSeries|Record)Rows|WritePrimaryRows|ReadPrimaryRows|WriteViewIndex|ViewIndexBatch\b|active_coverage_(start|end)|factkey|factvalue|DataChange|Envelope(Publisher)?|PublishEnvelope|RawEnvelope' modules/storage packages/gatewayproxy modules/admin modules/gateway web/src/api/storage examples
assert_no_match 'FactKey|RowMarker|content_hash|FACT_VERSION_IMMUTABLE|node_sequence|source_sequence|DatasetProgress|GetDatasetProgress|ViewIndexSourceProgress|expected_last_applied_sequence|base_progress|MergeRows|DeleteRows|ReadRows|ScanRows|Snapshot|keep_days|\brequired\b|\bdimensions\b' modules/storage/proto modules/storage/schema modules/storage/internal/service
assert_no_match 'storage[_-]access|moox-storage-access|ProjectionReader|internal/(core|infra)/' modules/storage packages/gatewayproxy modules/admin modules/gateway web/src/api/storage examples
assert_no_match 'context\.Background\(\)' modules/storage packages/gatewayproxy modules/admin modules/gateway --glob '*.go' --glob '!**/*_test.go' --glob '!**/*pb.go' --glob '!**/*trpc.go'
assert_no_match 'packages/crypto|package crypto' modules/storage packages/gatewayproxy modules/admin modules/gateway

while IFS= read -r match; do
  file="${match%%:*}"
  [[ "${file}" == modules/storage/internal/service/datashard/* ]] || fail "Pebble import outside DataShard: ${match}"
done < <(rg -n 'cockroachdb/pebble' modules/storage --glob '*.go' || true)

while IFS= read -r match; do
  file="${match%%:*}"
  [[ "${file}" == modules/storage/internal/service/viewindex/* ]] || fail "DuckDB/Bleve import outside ViewIndex: ${match}"
done < <(rg -n 'go-duckdb|blevesearch/bleve' modules/storage --glob '*.go' --glob '!**/*_test.go' || true)

rg -n 'RowKey|ViewIndexApplyBatch|ShardCheckpointUpdate|ViewSchemaHash|indexed_from|indexed_to' modules/storage/internal/service/viewindex modules/storage/proto >/dev/null || fail 'ViewIndex write contract is not present'
rg -n 'AllowedCallers|allowed_callers|AllowsCaller' packages/gatewayproxy modules/admin/internal/service/sysdeploy modules/gateway/internal >/dev/null || fail 'Gateway caller ACL is not present'

echo 'storage boundary contract passed'
