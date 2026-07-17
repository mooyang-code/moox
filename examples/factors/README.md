# Python 因子示例

本目录按计算范围区分两类因子：

- `timeseries/`：对单个标的、按时间升序的数据计算时序因子。
- `sections/`：对同一时刻的多标的数据计算截面因子。

因子文件遵循 MooX Python 因子协议。时序因子提供
`signal(df, n, factor_name)`，截面因子还通过 `get_factor_list(n)` 声明前置
时序因子。

## 输入字段

常规时序因子使用以下字段的子集：

- `candle_begin_time`
- `open`、`high`、`low`、`close`
- `volume`、`quote_volume`
- `symbol`、`symbol_spot`、`symbol_swap`

额外数据依赖由文件内的 `extra_data_dict` 声明：

- `CorrBTC` 需要 `btc_close`。
- `CirculatingMcap` 需要 `circulating_supply`。

`SelectCoin` 将参数作为标的名称使用，不适用于当前仅接受整数参数的 MooX
因子定义；它保留为协议和策略迁移示例。

截面因子要求输入包含 `candle_begin_time`、`symbol`、`is_spot`，以及对应参数的
`QuoteVolumeMean_<n>` 前置因子列。
