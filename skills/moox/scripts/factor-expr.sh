#!/usr/bin/env bash
# 因子表达式离线工具（packages/factorexpr/cmd/factorexpr）的入口：在 Skill 所在仓库里构建，再在调用方的工作目录运行，
# 相对路径按调用方的工作目录解析。用法见 references/factor-dsl.md：
#   factor-expr.sh functions [--json]
#   factor-expr.sh check --library lib.yaml [--factor-id ID]
#   factor-expr.sh eval --library lib.yaml --factor-id ID --input bars.csv --freq 1m --output dsl.csv
set -euo pipefail

SCRIPT_DIR="$(cd "${BASH_SOURCE[0]%/*}" && pwd -P)"
REPO_ROOT="$(cd "${SCRIPT_DIR}/../../.." && pwd -P)"
if [[ ! -f "${REPO_ROOT}/packages/factorexpr/cmd/factorexpr/main.go" ]]; then
  echo "找不到 packages/factorexpr/cmd/factorexpr：这个脚本要在 MooX 仓库里的 skills/moox 下运行" >&2
  exit 1
fi
command -v go >/dev/null || { echo "需要 Go 工具链（版本见仓库 .go-version）" >&2; exit 1; }

BIN_DIR="${XDG_CACHE_HOME:-${HOME}/.cache}/moox"
mkdir -p "${BIN_DIR}"
BIN="${BIN_DIR}/factorexpr"
# 每次都构建：有构建缓存时不到一秒，保证与仓库里的编译器、执行器一致。
(cd "${REPO_ROOT}" && go build -o "${BIN}" ./packages/factorexpr/cmd/factorexpr)
exec "${BIN}" "$@"
