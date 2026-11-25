#!/usr/bin/env bash
# check-proto-time-fields.sh 的契约测试：合规的写法通过，四条规则各有一个违例被拦下，例外表与乙类白名单生效。
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../../.." && pwd)"
SCRIPT="${ROOT}/scripts/check/check-proto-time-fields.sh"
TMP="$(cd "$(mktemp -d "${TMPDIR:-/tmp}/moox-proto-time.XXXXXX")" && pwd)"
trap 'rm -rf "${TMP}"' EXIT
fail() { echo "FAIL: $*" >&2; exit 1; }

cat >"${TMP}/storage_events.proto" <<'PROTO'
syntax = "proto3";
package demo;

message Good {
  // created_at 是创建时刻（Unix 毫秒）。
  int64 created_at = 1;
  int64 period_time = 2; // Unix 秒，乙类白名单
  // duration_ms 是耗时，不是时刻：注释写秒也不管。
  int64 duration_ms = 3;
  string data_time = 4;
}

message CollectorKlineBackfilled {
  // start、end 是 Unix 秒（乙类白名单）。
  int64 start = 1;
  int64 end = 2;
}
PROTO
"${SCRIPT}" "${TMP}/storage_events.proto" >/dev/null || fail "合规写法应通过"

cat >"${TMP}/monitor.proto" <<'PROTO'
syntax = "proto3";
package demo;

message HealthDataset {
  // 甲类例外：值是 data_time 文本。
  string output_watermark_at = 1;
}
PROTO
"${SCRIPT}" "${TMP}/monitor.proto" >/dev/null || fail "甲类例外表里的 _at 文本字段应通过"

expect_violation() {
  local rule="$1" body="$2" file="${TMP}/bad.proto" out
  printf 'syntax = "proto3";\npackage demo;\n%s\n' "${body}" >"${file}"
  if out="$("${SCRIPT}" "${file}" 2>&1)"; then
    fail "规则${rule}应拦下：${body}"
  fi
  grep -q "${rule}" <<<"${out}" || fail "规则${rule}的报错缺失：${out}"
}

expect_violation "①" $'import "google/protobuf/timestamp.proto";\nmessage M { google.protobuf.Timestamp created_at = 1; }'
expect_violation "②" $'message M { string updated_at = 1; }'
expect_violation "②" $'message HealthDataset { string output_watermark_at = 1; }'
expect_violation "③" $'message M {\n  // expires_at 是过期时间（Unix 秒）。\n  int64 expires_at = 1;\n}'
expect_violation "③" $'message M { int64 logged_at = 1; // 纳秒\n}'
expect_violation "④" $'message M { int64 delist_at_ms = 1; }'
expect_violation "④" $'message M { int64 last_heartbeat_unix = 1; }'

echo "check-proto-time-fields 契约测试通过"
