# K 线测试样本

本目录保存从本机 `Documents/量化数据` 抽取并标准化的小规模真实行情样本，供
Storage 和数据导入测试使用。

## 文件

| 文件 | 元数据 Dataset | 频率 | 标的 | 数据范围 | 行数 |
| --- | --- | --- | --- | --- | ---: |
| `stockcn/stockcn_equity_kline_1d.csv` | `stockcn/dataset_stockcn_equity_kline_1d` | `1d` | 100 只股票 | 2026-07-02 至 2026-07-16 | 1,100 |
| `stockcn/stockcn_equity_kline_1h.csv` | `stockcn/dataset_stockcn_equity_kline_1h` | `1h` | 100 只股票 | 2026-07-16 完整交易日 | 400 |

A 股样本按市场分层抽取：沪市主板 25 只、深市主板 25 只、创业板 20 只、
科创板 20 只、北交所 10 只。入选标的均具备连续 11 个日线交易日，小时样本包含
2026-07-16 当日全部 4 根 K 线。

## 列约定

所有文件均为 UTF-8 CSV。前六列直接对应 Storage `TimeSeriesKey`：

- `space_id`
- `dataset_id`
- `subject_id`
- `freq`
- `data_time`
- `series_tag`

每个 Dataset 只有一个频率，样本按频率写入各自的 Dataset。默认种子只有 A 股 1m
行情 Dataset，导入前需按 `dataset_stockcn_equity_kline_1m` 的列定义创建上表中的
1d、1h Dataset。股票样本的 `series_tag` 为空。A 股时间保留 `+08:00` 时区。

样本只保留测试所需字段，不包含来源文件中的说明行、证券名称、均价、价差等扩展
字段。
