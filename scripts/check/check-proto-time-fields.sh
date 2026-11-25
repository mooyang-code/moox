#!/usr/bin/env bash
# proto 时间字段写法检查（3H 字段清单 1.2 节）：禁 Timestamp、禁 string 的 _at、毫秒字段注释不得写秒、名字不带单位后缀。
# 四条规则对全部 proto 生效。
# 用法：check-proto-time-fields.sh [proto 文件...]（给了文件就只查这些文件，供契约测试用）。
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
cd "${ROOT}"

files=("$@")
if ((${#files[@]} == 0)); then
  while IFS= read -r file; do
    files+=("${file}")
  done < <(git ls-files '*.proto' | grep -v -e '^third_party/' -e '/google/protobuf/' -e '/node_modules/' | sort)
fi

if ! python3 scripts/check/check-proto-time-fields.py --files "${files[@]}" --strict "${files[@]}"; then
  echo "proto 时间字段检查未通过（规则见 docs 3H 字段清单 1.2 节与 scripts/check/check-proto-time-fields.py）" >&2
  exit 1
fi
echo "proto 时间字段检查通过"
