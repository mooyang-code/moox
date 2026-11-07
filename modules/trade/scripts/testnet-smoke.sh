#!/usr/bin/env bash
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

max_notional=${MOOX_TRADE_TESTNET_MAX_NOTIONAL:-20}
config="$repo_root/modules/trade/config/app.yaml"

run_exchange() {
  local exchange_name=$1
  local market=$2
  local secret_id=$3
  local symbol=$4
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
