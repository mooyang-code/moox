#!/usr/bin/env bash
# 推送前在本机复现 CI 的 verify-pr：先检查依赖，再运行与托管运行器相同的 make verify。
#
# 用法：
#   bash scripts/ci/local-verify.sh                 与 PR 上的 verify-pr 一致（跳过 proto-check）
#   bash scripts/ci/local-verify.sh --full          同 verify-trusted，额外执行 proto-check（要求已安装代码生成工具且工作区干净）
#   bash scripts/ci/local-verify.sh -- <target>...  只运行指定的 make 目标，例如 check-format check-lint
#
# 说明：带 linux 构建标签的测试在 macOS 上不会编译执行；它们由托管运行器覆盖
# （见 Makefile 里的 test-*-linux 目标）。
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
cd "${ROOT}"

skip_proto=1
targets=()
while [[ $# -gt 0 ]]; do
  case "$1" in
    --full) skip_proto=0; shift ;;
    --) shift; targets=("$@"); break ;;
    -h|--help) sed -n '2,11p' "${BASH_SOURCE[0]}"; exit 0 ;;
    *) echo "未知参数：$1（使用 --help 查看用法）" >&2; exit 2 ;;
  esac
done

# 与 CI 的 verify-trusted 一致：固定版本的 Go 若装在 ~/.local/go<版本> 下，优先使用。
pinned_go_bin="${HOME}/.local/go$(tr -d '[:space:]' < .go-version)/bin"
if [[ -d "${pinned_go_bin}" ]]; then
  export PATH="${pinned_go_bin}:${PATH}"
fi

missing=()
for name in git go node pnpm make rg python3 rsync jq; do
  command -v "${name}" >/dev/null 2>&1 || missing+=("${name}")
done
if ((${#missing[@]} > 0)); then
  echo "缺少依赖：${missing[*]}" >&2
  [[ " ${missing[*]} " == *" rg "* ]] && echo "提示：ripgrep 请用 brew install ripgrep 安装（shell 里的 rg 别名不会被 make 继承）。" >&2
  exit 1
fi

bash scripts/ci/check-go-version.sh

if ((${#targets[@]} == 0)); then
  targets=(verify)
fi
args=()
if ((skip_proto)); then
  args+=("SKIP_PROTO_CHECK=1")
fi

echo "==> make ${targets[*]} ${args[*]:-}"
exec make "${targets[@]}" ${args[@]+"${args[@]}"}
