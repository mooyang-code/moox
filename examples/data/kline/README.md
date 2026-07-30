# K 线测试样本

> 本文中的 `series_tag` 列和共享 crypto Dataset 是当前样本契约。

本目录保存从本机 `Documents/量化数据` 抽取并标准化的小规模真实行情样本，供
Storage、Factor、View 和数据导入测试使用。

## 文件

| 文件 | 元数据 Dataset | 频率 | 标的 | 数据范围 | 行数 |
| --- | --- | --- | --- | --- | ---: |
| `stock_cn/stock_kline_1d.csv` | `stock_cn/stock_kline` | `1d` | 100 只股票 | 2026-07-02 至 2026-07-16 | 1,100 |
| `stock_cn/stock_kline_1h.csv` | `stock_cn/stock_kline` | `1h` | 100 只股票 | 2026-07-16 完整交易日 | 400 |
| `crypto/binance_spot_kline_1h.csv` | `crypto/spot_kline_1h` | `1h` | 10 个交易对 | 2026-07-15 完整自然日 | 240 |
| `crypto/binance_perpetual_kline_1h.csv` | `crypto/perpetual_kline_1h` | `1h` | 10 个交易对 | 2026-07-15 完整自然日 | 240 |
| `crypto/okx_spot_kline_1h.csv` | `crypto/spot_kline_1h` | `1h` | 10 个交易对 | 2026-07-15 完整自然日 | 240 |

A 股样本按市场分层抽取：沪市主板 25 只、深市主板 25 只、创业板 20 只、
科创板 20 只、北交所 10 只。入选标的均具备连续 11 个日线交易日，小时样本包含
2026-07-16 当日全部 4 根 K 线。

币安样本覆盖 `ADA`、`AVAX`、`BNB`、`BTC`、`DOGE`、`DOT`、`ETH`、`LINK`、
`SOL`、`XRP` 的 USDT 交易对。欧易样本选择对应的 USD 交易对，其中以 `LTC`
替代欧易未提供的 `BNB-USD`。每个交易对均包含当日 24 根小时 K 线。

本机 `OKX永续合约1小时数据-币对分类` 目录中没有可用 CSV，因此没有生成欧易
永续合约样本。

## 列约定

所有文件均为 UTF-8 CSV。前六列直接对应 Storage `TimeSeriesKey`：

- `space_id`
- `dataset_id`
- `subject_id`
- `freq`
- `data_time`
- `series_tag`

股票样本的 `series_tag` 为空；Binance/OKX 样本分别为 `venue:binance` 和
`venue:okx`。其余列与 `setup/default/metadata.yaml` 中对应 Dataset 的列定义
一致。A 股时间保留 `+08:00` 时区；加密货币时间使用 UTC `Z`。永续合约样本中的空
`funding_rate` 表示该小时源数据未提供资金费率，不应按零值处理。

样本只保留测试所需字段，不包含来源文件中的说明行、证券名称、均价、价差等扩展
字段。
