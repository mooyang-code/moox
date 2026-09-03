#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../../.." && pwd)"
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

assert_no_legacy_series_identity() {
  # Match the removed wire/schema/API surface, not ordinary prose such as
  # "task frequency dimensions". Task 13 changes the default scope to "all"
  # after every direct consumer has crossed the atomic wire boundary.
  local pattern='GetDimensions\b|CanonicalDimensions\b|DimensionTags\b|SeriesTagName\b|Dimensions[[:space:]]+(\[\]string|map\[)|Dimensions[[:space:]]*:|dimensions_json\b|dimensions[[:space:]]*=|dimensions[[:space:]]*\??:|\.dimensions\b|"dimensions"[[:space:]]*:|json:"dimensions|parse[A-Za-z]*Dimensions\b|dataDimensions\b|series_tag_name|dimension_tags'
  local targets=(modules packages web/src examples skills/moox)
  local scope="${MOOX_SERIES_IDENTITY_SCOPE:-all}"
  if [[ "${scope}" == "all" ]]; then
    targets=(modules packages web/src examples skills/moox)
  elif [[ "${scope}" == "storage" ]]; then
    targets=(modules/storage packages/storagepb)
  else
    fail "MOOX_SERIES_IDENTITY_SCOPE must be storage or all"
  fi

  local matches
  matches="$(rg -n --hidden \
    --glob '!docs/superpowers/**' \
    --glob '!**/go.sum' \
    --glob '!web/node_modules/**' \
    --glob '!node_modules/**' \
    "${pattern}" \
    "${targets[@]}" || true)"
  while IFS= read -r match; do
    [[ -z "${match}" ]] && continue
    # Archive and DuckDB must fail closed on pre-v2 layouts. These exact files
    # are the only allowed source-level legacy schema sentinels.
    case "${match}" in
      modules/storage/internal/service/viewindex/duckdb/index_manager_test.go:*dimensions_json*|\
      modules/storage/internal/service/view/service_test.go:*dimensions_json*|\
      modules/archive/internal/parquetio/codec.go:*dimensions_json*|\
      modules/archive/internal/parquetio/schema.go:*dimensions_json*|\
      modules/archive/internal/parquetio/codec_test.go:*dimensions_json*|\
      modules/archive/internal/parquetio/schema_test.go:*dimensions_json*|\
      modules/archive/internal/bootstrap/app_test.go:*dimensions_json*)
        continue
        ;;
    esac
    printf '%s\n' "${match}" >&2
    fail "forbidden ${scope} legacy series identity interface remains"
  done <<<"${matches}"
  if [[ "${scope}" == "storage" ]]; then
    printf 'storage boundary contract: transitional series identity scope=storage\n'
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

assert_no_match 'trpc\.moox\.storage\.(Access|AccessScan)|Write(TimeSeries|Record)Rows|WritePrimaryRows|ReadPrimaryRows|WriteViewIndex|ViewIndexBatch\b|active_coverage_(start|end)|factkey|factvalue|DataChange|Envelope(Publisher)?|PublishEnvelope|RawEnvelope' modules/storage/internal/service/datanode modules/storage/internal/service/viewindex/model.go modules/storage/internal/service/viewindex/slots.go packages/gatewayproxy modules/admin modules/gateway web/src/api/storage examples
assert_no_match 'FactKey|RowMarker|content_hash|FACT_VERSION_IMMUTABLE|node_sequence|source_sequence|DatasetProgress|GetDatasetProgress|ViewIndexSourceProgress|expected_last_applied_sequence|base_progress|MergeRows|DeleteRows|ReadRows|ScanRows|keep_days' modules/storage/proto modules/storage/schema modules/storage/internal/service/datanode modules/storage/internal/service/viewindex/model.go modules/storage/internal/service/viewindex/slots.go
assert_no_match 'storage[_-]access|moox-storage-access|ProjectionReader|internal/(core|infra)/' modules/storage packages/gatewayproxy modules/admin modules/gateway web/src/api/storage examples
assert_no_match 'context\.Background\(\)' modules/storage packages/gatewayproxy modules/admin modules/gateway --glob '*.go' --glob '!**/*_test.go' --glob '!**/*pb.go' --glob '!**/*trpc.go'
assert_no_match 'packages/crypto|package crypto' modules/storage packages/gatewayproxy modules/admin modules/gateway
assert_no_legacy_series_identity

while IFS= read -r match; do
  file="${match%%:*}"
  [[ "${file}" == modules/storage/internal/service/datanode/* ]] || fail "Pebble import outside DataNode: ${match}"
done < <(rg -n 'cockroachdb/pebble' modules/storage/internal/service/datanode --glob '*.go' || true)

while IFS= read -r match; do
  file="${match%%:*}"
  [[ "${file}" == modules/storage/internal/service/viewindex/* ]] || fail "DuckDB/Bleve import outside ViewIndex: ${match}"
done < <(rg -n 'go-duckdb|blevesearch/bleve' modules/storage --glob '*.go' --glob '!**/*_test.go' || true)

rg -n 'RowKey|ViewIndexApplyBatch|ViewSchemaHash|ViewRevision|indexed_from|indexed_to' modules/storage/internal/service/viewindex modules/storage/proto >/dev/null || fail 'ViewIndex write contract is not present'
rg -n 'AllowedCallers|allowed_callers|AllowsCaller' packages/gatewayproxy modules/admin/internal/service/sysdeploy modules/gateway/internal >/dev/null || fail 'Gateway caller ACL is not present'

echo 'storage boundary contract passed'
