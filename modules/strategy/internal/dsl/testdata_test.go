package dsl

// exampleDSL 与设计文档 4.3 的示例一致，必须能原样解析、校验、编译。
const exampleDSL = `name: binance_spot_momentum_1h
bar: 1h

universe:
  min_age_bars: 240
  exclude_tags: [stablecoins]
  exclude: [BTC-USDT]

rules:
  - id: long_momentum
    name: 多头动量选币
    type: rank
    filter: "quote_volume_mean_20 > 2000000 && close > 0"
    score: "0.6 * rank(bias_q_20) + 0.4 * rank(quote_volume_mean_q_20)"
    select: {top: 5, buffer: 2}
    weight: {total: 0.8, method: equal, cap: 0.3}

  - id: btc_trend
    name: BTC 均线趋势跟随
    type: signal
    pool: [BTC-USDT]
    entry: "bars[-1].close <= bars[-1].ma_20 && bars[0].close > bars[0].ma_20"
    exit:  "bars[0].close < bars[0].ma_20"
    weight: {total: 0.2}

portfolio:
  leverage: 1
  max_weight: 0.3
  min_universe: 20
  max_missing: 0.2
`

var exampleColumns = []string{"open", "high", "low", "close", "volume", "quote_volume", "trade_num", "bias_q_20", "quote_volume_mean_20", "quote_volume_mean_q_20", "ma_20"}
