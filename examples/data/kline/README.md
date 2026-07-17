# K 线测试样本

本目录保存从本机 `Documents/量化数据` 抽取并标准化的小规模真实行情样本，供
Storage、Factor、View 和数据导入测试使用。

## 文件

| 文件 | 元数据 Dataset | 频率 | 标的 | 数据范围 | 行数 |
| --- | --- | --- | --- | --- | ---: |
| `stock_cn/stock_kline_1d.csv` | `stock_cn/stock_kline` | `1d` | `sh600000`、`sh600519`、`sz000001` | 2026-07-02 至 2026-07-15 | 30 |
| `stock_cn/stock_kline_1h.csv` | `stock_cn/stock_kline` | `1h` | `sh600000`、`sh600519`、`sz000001` | 2026-07-16 | 12 |
| `crypto/binance_spot_kline_1h.csv` | `crypto/binance_spot_kline_1h` | `1h` | `BTC-USDT`、`ETH-USDT`、`SOL-USDT` | 2026-07-15 | 72 |
| `crypto/binance_perpetual_kline_1h.csv` | `crypto/binance_perpetual_kline_1h` | `1h` | `BTC-USDT`、`ETH-USDT`、`SOL-USDT` | 2026-07-15 | 72 |
| `crypto/okx_spot_kline_1h.csv` | `crypto/okx_spot_kline_1h` | `1h` | `BTC-USD`、`ETH-USD`、`SOL-USD` | 2026-07-15 | 72 |

本机 `OKX永续合约1小时数据-币对分类` 目录中没有可用 CSV，因此没有生成欧易
永续合约样本。

## 列约定

所有文件均为 UTF-8 CSV。前五列直接对应 Storage `TimeSeriesKey`：

- `space_id`
- `dataset_id`
- `subject_id`
- `freq`
- `data_time`

其余列与 `metadata-quant-initial.seed.yaml` 中对应 Dataset 的列定义一致。A 股时间
保留 `+08:00` 时区；加密货币时间使用 UTC `Z`。永续合约样本中的空
`funding_rate` 表示该小时源数据未提供资金费率，不应按零值处理。

样本只保留测试所需字段，不包含来源文件中的说明行、证券名称、均价、价差等扩展
字段。
