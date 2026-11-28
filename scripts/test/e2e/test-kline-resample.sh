#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/../../.." && pwd)"
cd "$ROOT_DIR"

# 按前缀匹配全部 K 线重采样端到端测试；测试改名后一个都匹配不到时 go test 仍返回成功，所以这里显式判失败。
output="$(go test ./modules/collector/test -run '^TestKlineResample' -count=1 -v 2>&1)" || {
  printf '%s\n' "$output"
  exit 1
}
printf '%s\n' "$output"
if ! grep -q '^--- PASS: TestKlineResample' <<<"$output"; then
  echo "没有匹配到任何 K 线重采样端到端测试" >&2
  exit 1
fi
