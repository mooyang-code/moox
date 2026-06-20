#!/usr/bin/env bash
# storage 端到端测试运行脚本。
#
# 它会：自动编译 moox-storage、用独立端口/目录在本地拉起全部子服务、
# 以 tRPC 客户端依次验证 Metadata / Data / Query / Archive 各接口，
# 测试数据使用本机下载目录下的 AR-USDT.csv（K 线）。
#
# 用法：
#   ./tests/run_e2e.sh                 # 默认载入 500 行 K 线
#   MOOX_E2E_KLINE_LIMIT=0 ./tests/run_e2e.sh        # 载入全部行
#   MOOX_E2E_KLINE_CSV=/path/AR-USDT.csv ./tests/run_e2e.sh
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
MODULE_DIR="$(cd "$SCRIPT_DIR/.." && pwd)"

cd "$MODULE_DIR"

export CGO_ENABLED=1

echo "[e2e] module dir: $MODULE_DIR"
echo "[e2e] kline csv  : ${MOOX_E2E_KLINE_CSV:-$HOME/Downloads/AR-USDT.csv}"
echo "[e2e] kline limit: ${MOOX_E2E_KLINE_LIMIT:-500}"
echo "[e2e] running ..."

go test -tags e2e -timeout 600s -v ./tests/e2e/... "$@"
