#!/usr/bin/env bash
# proto 时间字段写法检查（3H 字段清单 1.2 节）：禁 Timestamp、禁 string 的 _at、毫秒字段注释不得写秒、名字不带单位后缀。
# 规则①（禁 Timestamp）对全部 proto 生效；规则②～④只对 STRICT 范围生效：
#   - 默认只查已改完的 E 组事件 proto（STRICT_FILES）；
#   - --all 或 MOOX_PROTO_TIME_SCOPE=all 对全部 proto 生效。其余 proto 的时刻字段改完后把 DEFAULT_SCOPE 改成 all，并删掉 STRICT_FILES。
# 用法：check-proto-time-fields.sh [--all] [proto 文件...]（给了文件就只查这些文件，且全部按 strict 查，供契约测试用）。
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
cd "${ROOT}"

DEFAULT_SCOPE="e"
STRICT_FILES=(
  modules/collector/internal/marketfetchpb/market_fetch_events.proto
  modules/storage/proto/dataset_markers.proto
  packages/commonpb/moox_common.proto
  packages/events/proto/eventpb/event_message.proto
  packages/metricspb/metrics.proto
  packages/storagepb/storage_events.proto
  packages/tradeeventpb/trade_events.proto
)

scope="${MOOX_PROTO_TIME_SCOPE:-${DEFAULT_SCOPE}}"
files=()
for arg in "$@"; do
  case "${arg}" in
    --all) scope="all" ;;
    *) files+=("${arg}") ;;
  esac
done

if ((${#files[@]} > 0)); then
  strict=("${files[@]}")
else
  while IFS= read -r file; do
    files+=("${file}")
  done < <(git ls-files '*.proto' | grep -v -e '^third_party/' -e '/google/protobuf/' -e '/node_modules/' | sort)
  if [[ "${scope}" == "all" ]]; then
    strict=("${files[@]}")
  else
    strict=("${STRICT_FILES[@]}")
  fi
fi

if ! python3 scripts/check/check-proto-time-fields.py --files "${files[@]}" --strict "${strict[@]}"; then
  echo "proto 时间字段检查未通过（规则见 docs 3H 字段清单 1.2 节与 scripts/check/check-proto-time-fields.py）" >&2
  exit 1
fi
echo "proto 时间字段检查通过（strict 范围：${scope}）"
