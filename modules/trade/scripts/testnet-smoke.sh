#!/usr/bin/env bash
# 交易模块测试网冒烟：在币安现货、币安合约、OKX 模拟盘现货、OKX 模拟盘永续上各挂一笔被动限价买单，
# 走完提交、查询、私有流、同步、重启恢复和撤单清理。会向测试网下真实订单，由用户或维护者执行。
#
# 要在部署了 trade 单元的主机上跑：冒烟工具按 trade 单元的配置找网关客户端的 key_id 和签名密钥，
# 按 <配置目录>/../../host-gateway/config/app.yaml 找本机 host-gateway，再经它读交易所密钥。
#
# 必填环境变量：
#   MOOX_TRADE_TESTNET_CONFIRM=YES        确认允许向测试网下真实订单
#   MOOX_TRADE_TESTNET_CONFIG             已部署 trade 单元的配置文件（<单元目录>/config/app.yaml）；
#                                         仓库里的 modules/trade/config/app.yaml 没有 key_id，不能用
#   MOOX_BINANCE_TESTNET_SECRET_ID        币安现货测试网密钥的 Secret ID
#   MOOX_BINANCE_TESTNET_SWAP_SECRET_ID   币安合约测试网密钥的 Secret ID
#   MOOX_OKX_TESTNET_SECRET_ID            OKX 模拟盘密钥的 Secret ID（现货、永续共用）
# 可选环境变量：
#   MOOX_TRADE_TESTNET_MAX_NOTIONAL       现货测试单名义金额上限（按参考价算，默认 20）
#   MOOX_TRADE_TESTNET_SWAP_MAX_NOTIONAL  合约测试单名义金额上限（默认 250；币安 BTCUSDT 合约最小名义金额约 100）
#   MOOX_BINANCE_TESTNET_SYMBOL、MOOX_BINANCE_TESTNET_SWAP_SYMBOL、
#   MOOX_OKX_TESTNET_SYMBOL、MOOX_OKX_TESTNET_SWAP_SYMBOL  各市场的测试标的（交易所原生代码）
# 缺前置条件时输出 SKIP，以退出码 2 结束。
set -euo pipefail

skip() {
  echo "trade testnet smoke: SKIP $*" >&2
  exit 2
}

[[ "${MOOX_TRADE_TESTNET_CONFIRM:-}" == "YES" ]] ||
  skip "set MOOX_TRADE_TESTNET_CONFIRM=YES to permit real Testnet orders"
# 币安现货和合约测试网是两套域名和两把密钥：现货、合约各用各的测试网账户。
# OKX 模拟盘的一把密钥同时能做现货和永续，两个市场分别跑一遍。
[[ -n "${MOOX_BINANCE_TESTNET_SECRET_ID:-}" ]] ||
  skip "MOOX_BINANCE_TESTNET_SECRET_ID is required (Binance spot testnet key)"
[[ -n "${MOOX_BINANCE_TESTNET_SWAP_SECRET_ID:-}" ]] ||
  skip "MOOX_BINANCE_TESTNET_SWAP_SECRET_ID is required (Binance futures testnet key)"
[[ -n "${MOOX_OKX_TESTNET_SECRET_ID:-}" ]] ||
  skip "MOOX_OKX_TESTNET_SECRET_ID is required"
[[ -n "${MOOX_TRADE_TESTNET_CONFIG:-}" ]] ||
  skip "未设置 MOOX_TRADE_TESTNET_CONFIG：请指向已部署 trade 单元的配置文件（<单元目录>/config/app.yaml）"
[[ -f "$MOOX_TRADE_TESTNET_CONFIG" ]] ||
  skip "MOOX_TRADE_TESTNET_CONFIG 指向的文件不存在：$MOOX_TRADE_TESTNET_CONFIG"

script_dir=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)
repo_root=$(cd "$script_dir/../../.." && pwd)
work_dir=$(mktemp -d "${TMPDIR:-/tmp}/moox-trade-testnet.XXXXXX")
binary="$work_dir/testnet-smoke"
completed=0

cleanup() {
  if [[ "$completed" == "1" ]]; then
    rm -rf "$work_dir"
  else
    echo "trade testnet smoke: retained recovery files at $work_dir" >&2
  fi
}
trap cleanup EXIT INT TERM

(
  cd "$repo_root"
  go build -o "$binary" ./modules/trade/cmd/testnet-smoke
)

spot_max_notional=${MOOX_TRADE_TESTNET_MAX_NOTIONAL:-20}
# 合约最小名义金额比现货高得多（币安 BTCUSDT 合约约 100），单独给上限。
swap_max_notional=${MOOX_TRADE_TESTNET_SWAP_MAX_NOTIONAL:-250}
config=$MOOX_TRADE_TESTNET_CONFIG
# 配置里的库路径一般是相对单元目录的，网关客户端把目录缓存放在库目录下：指到临时目录，
# 不在当前目录建 data、也不碰线上 trade 的缓存。冒烟自己的库由 --database 指定，不受影响。
export MOOX_TRADE_DB_PATH="$work_dir/gateway/trade.db"

run_exchange() {
  local exchange_name=$1
  local market=$2
  local secret_id=$3
  local symbol=$4
  local max_notional=$spot_max_notional
  if [[ "$market" == "SWAP" ]]; then
    max_notional=$swap_max_notional
  fi
  local lower
  lower=$(printf '%s-%s' "$exchange_name" "$market" | tr '[:upper:]' '[:lower:]')
  # 币安现货、合约测试网是两套账户，各用各的库；OKX 模拟盘一个账户同时做两个市场，两次演练共用一个库。
  local database="$work_dir/${lower}.db"
  if [[ "$exchange_name" == "OKX" ]]; then
    database="$work_dir/okx.db"
  fi
  local state="$work_dir/${lower}.json"
  local common=(
    --exchange "$exchange_name"
    --market "$market"
    --secret-id "$secret_id"
    --database "$database"
    --state "$state"
    --config "$config"
    --exchange-symbol "$symbol"
    --max-notional "$max_notional"
  )

  if ! "$binary" --phase submit "${common[@]}"; then
    if [[ -s "$state" ]]; then
      "$binary" --phase recover "${common[@]}" || true
    fi
    return 1
  fi
  if ! "$binary" --phase recover "${common[@]}"; then
    echo "trade testnet smoke: ${exchange_name} ${market} cleanup failed; state retained at ${state}" >&2
    return 1
  fi
}

binance_symbol=${MOOX_BINANCE_TESTNET_SYMBOL:-BTCUSDT}
binance_swap_symbol=${MOOX_BINANCE_TESTNET_SWAP_SYMBOL:-BTCUSDT}
okx_symbol=${MOOX_OKX_TESTNET_SYMBOL:-BTC-USDT}
okx_swap_symbol=${MOOX_OKX_TESTNET_SWAP_SYMBOL:-BTC-USDT-SWAP}
[[ "$binance_symbol" =~ ^[A-Z0-9]+$ ]] ||
  skip "MOOX_BINANCE_TESTNET_SYMBOL must be an uppercase native symbol"
[[ "$binance_swap_symbol" =~ ^[A-Z0-9]+$ ]] ||
  skip "MOOX_BINANCE_TESTNET_SWAP_SYMBOL must be an uppercase native symbol"
[[ "$okx_symbol" =~ ^[A-Z0-9-]+$ ]] ||
  skip "MOOX_OKX_TESTNET_SYMBOL must be an uppercase native symbol"
[[ "$okx_swap_symbol" =~ ^[A-Z0-9-]+$ ]] ||
  skip "MOOX_OKX_TESTNET_SWAP_SYMBOL must be an uppercase native symbol"

run_exchange BINANCE SPOT "$MOOX_BINANCE_TESTNET_SECRET_ID" "$binance_symbol"
echo "BINANCE PASS submit/query/stream/sync/restart/cleanup"
run_exchange BINANCE SWAP "$MOOX_BINANCE_TESTNET_SWAP_SECRET_ID" "$binance_swap_symbol"
echo "BINANCE SWAP PASS submit/query/stream/sync/restart/cleanup"
run_exchange OKX SPOT "$MOOX_OKX_TESTNET_SECRET_ID" "$okx_symbol"
echo "OKX PASS submit/query/stream/sync/restart/cleanup"
run_exchange OKX SWAP "$MOOX_OKX_TESTNET_SECRET_ID" "$okx_swap_symbol"
echo "OKX SWAP PASS submit/query/stream/sync/restart/cleanup"
completed=1
