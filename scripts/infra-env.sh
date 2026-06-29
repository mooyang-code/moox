#!/usr/bin/env bash
# scripts/infra-env.sh —— legacy helper: 从 infra/infra*.yaml 获取旧开发脚本变量。
#   source scripts/infra-env.sh
#
# 导出变量（仅真实值，占位值不导出）。运行时服务部署信息请使用 t_service_deployments/SysDeploy。
#   REMOTE_HOST REMOTE_SSH STORAGE_URL XDATA_URL
#   ADMIN_GATEWAY_HOST ADMIN_GATEWAY_PORT WEB_HOST_HOST WEB_HOST_PORT
#   TRADE_HOST TRADE_PORT
#
# 依赖：本仓库 pkg/infraconfig/cmd/infra-export（go run）。

set -euo pipefail

# 解析仓库根目录（本脚本位于 <repo>/scripts/）。
_SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
_REPO_ROOT="$(cd "$_SCRIPT_DIR/.." && pwd)"

# 优先用已构建的 infra-export 二进制（若存在），否则 go run。
if [ -x "$_REPO_ROOT/bin/infra-export" ]; then
  eval "$("$_REPO_ROOT/bin/infra-export" -root "$_REPO_ROOT")"
else
  eval "$(cd "$_REPO_ROOT/pkg/infraconfig" && go run ./cmd/infra-export -root "$_REPO_ROOT")"
fi

unset _SCRIPT_DIR _REPO_ROOT
