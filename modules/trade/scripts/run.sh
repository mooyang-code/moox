#!/usr/bin/env bash
# 本地构建并启动 moox-trade tRPC 服务。
set -euo pipefail

MOD_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$MOD_DIR"

# 生成 proto（tradegen 目录缺失时）
if [ ! -d proto/tradegen ] || [ -z "$(ls -A proto/tradegen 2>/dev/null)" ]; then
  echo "[run.sh] 生成 proto ..."
  make -C proto all
fi

echo "[run.sh] 构建 moox-trade ..."
go build -o bin/moox-trade ./cmd/moox-trade

mkdir -p data log

echo "[run.sh] 启动 moox-trade（端口 11200-11208） ..."
exec ./bin/moox-trade -conf=config/trpc_go.yaml
