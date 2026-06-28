#!/bin/bash
# infra-env.sh：读取 infra 配置并 export 为 shell 变量，供部署脚本 source。
# 用法：source scripts/infra-env.sh
# 依赖：本机有 go（用 go run 解析配置，确保与运行时服务同一真相源）。
# 真实值取自 infra/infra.local.yaml（gitignored）；无该文件则用 infra/infra.yaml 占位默认。

__infra_env_script_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
__infra_env_repo_root="$(cd "$__infra_env_script_dir/.." && pwd)"

if ! command -v go >/dev/null 2>&1; then
  echo "infra-env: 未找到 go，无法解析 infra 配置" >&2
  return 1 2>/dev/null || exit 1
fi

__infra_export_out="$(cd "$__infra_env_repo_root" && go run ./pkg/infraconfig/cmd/infra-export 2>/dev/null)"
if [ -z "$__infra_export_out" ]; then
  echo "infra-env: 解析 infra 配置失败（检查 infra/infra.yaml 是否存在或 MOOX_INFRA_CONFIG 是否设置）" >&2
  return 1 2>/dev/null || exit 1
fi
eval "$__infra_export_out"
unset __infra_env_script_dir __infra_env_repo_root __infra_export_out
