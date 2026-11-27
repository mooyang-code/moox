#!/usr/bin/env bash
# 契约：Go 工作区测试的分片没有遗漏也没有重复（各片合起来正好是全部模块），非法的分片参数会报错。
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../../.." && pwd)"
script="${ROOT}/scripts/test/contract/test-go-workspace.sh"

all="$(MOOX_GO_SHARD_LIST=1 bash "${script}" | sort)"
[[ -n "${all}" ]] || { echo "没有列出任何模块" >&2; exit 1; }

for total in 1 2 3 4 7; do
  merged=""
  for index in $(seq 1 "${total}"); do
    part="$(MOOX_GO_SHARD="${index}/${total}" MOOX_GO_SHARD_LIST=1 bash "${script}")"
    merged+="${part}"$'\n'
  done
  merged="$(printf '%s' "${merged}" | sed '/^$/d' | sort)"
  [[ "${merged}" == "${all}" ]] || { echo "分成 ${total} 片后与全部模块不一致" >&2; exit 1; }
done

# 每个模块只属于一片
dup="$(for index in 1 2 3; do MOOX_GO_SHARD="${index}/3" MOOX_GO_SHARD_LIST=1 bash "${script}"; done | sort | uniq -d)"
[[ -z "${dup}" ]] || { echo "有模块出现在多个分片里：${dup}" >&2; exit 1; }

for bad in 0/3 4/3 1/0 abc 1-3 /3 1/; do
  if MOOX_GO_SHARD="${bad}" MOOX_GO_SHARD_LIST=1 bash "${script}" >/dev/null 2>&1; then
    echo "非法的分片参数 ${bad} 应该报错" >&2
    exit 1
  fi
done

go_version="$(tr -d '[:space:]' < "${ROOT}/.go-version")"
if [[ -d "${HOME}/.local/go${go_version}/bin" ]]; then
  export PATH="${HOME}/.local/go${go_version}/bin:${PATH}"
fi
root_pkgs="$(cd "${ROOT}" && GOTOOLCHAIN=local go list ./... | sort)"
shard_pkgs_raw="$(while IFS= read -r m; do (cd "${ROOT}/${m}" && GOTOOLCHAIN=local go list ./...); done <<<"${all}")"
dup_pkgs="$(printf '%s\n' "${shard_pkgs_raw}" | sort | uniq -d)"
[[ -z "${dup_pkgs}" ]] || { echo "有 Go 包被多个模块目录重复覆盖：${dup_pkgs}" >&2; exit 1; }
shard_pkgs="$(printf '%s\n' "${shard_pkgs_raw}" | sort)"
[[ "${shard_pkgs}" == "${root_pkgs}" ]] || {
  echo "test-go-workspace.sh 的 MODULES 清单与 go list ./... 不一致" >&2
  diff -u <(printf '%s\n' "${root_pkgs}") <(printf '%s\n' "${shard_pkgs}") >&2 || true
  exit 1
}

echo "go workspace shard contract passed"
