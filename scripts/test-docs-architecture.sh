#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "${ROOT}"

workspace_count=0
while IFS= read -r module_path; do
  [[ -n "${module_path}" ]] || continue
  workspace_count=$((workspace_count + 1))
  grep -Fq "| \`${module_path}\` |" docs/架构总览.md || {
    echo "docs/架构总览.md missing go.work module: ${module_path}" >&2
    exit 1
  }
done < <(awk '/^use \(/ {on=1; next} on && /^\)/ {exit} on {gsub(/^[[:space:]]*\.\//, ""); if (length) print}' go.work)

[[ "${workspace_count}" -eq 41 ]] || {
  echo "unexpected go.work module count: ${workspace_count}" >&2
  exit 1
}

grep -Fq '分布在 `modules/`、`packages/` 和仓库根级运行模块中' docs/架构总览.md
! grep -Fq 'internal/services/' docs/架构总览.md
! grep -Fq 'proto/gen/' docs/架构总览.md

for module in gateway strategy archive; do
  grep -Fq "[${module}](./${module}/)" modules/README.md
done
grep -Fq '因子定义、调度和 Python worker 计算' modules/README.md
! grep -Fq '占位，待扩展' modules/README.md
grep -Fq '每台服务器的 `moox-gateway`' modules/README.md

grep -Fq 'strategy/strategy-cli' README.md
grep -Fq 'Strategy、Monitor、Storage 和 Archive' README.md

grep -Fq 'RPC `11430`，健康检查 `11431`' docs/策略模块架构设计.md
grep -Fq '工程接入状态' docs/策略模块架构设计.md
grep -Fq '构建、发布和标准部署链路已接入' docs/策略模块架构设计.md
! grep -Fq '建议 HTTP 端口 `11408`' docs/策略模块架构设计.md

grep -Fq '派生写入成功后才返回 success' docs/存储引擎架构.md
grep -Fq '`Nak` 并由 JetStream 重投' docs/存储引擎架构.md

echo 'architecture documentation contract passed'
