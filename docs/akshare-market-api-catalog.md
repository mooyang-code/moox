# 行情接口目录

本目录是运行时 Provider 的来源登记，不是 Python 依赖清单。AkShare、QUANTAXIS 和 easy_tdx 只用于核对接口语义；Collector 运行时使用 Go 实现。

| Market | Instrument | Provider/Source | 参考接口 | 运行状态 | 备注 |
| --- | --- | --- | --- | --- | --- |
| `stock_cn` | `equity` | `tdx/normal_7709` | `stock_zh_a_hist_min_em`、`QATdx`、`get_security_bars` | `enabled` | TCP 普通行情，单标的分页 |
| `stock_cn` | `equity` | `eastmoney/stock_cn_http` | `stock_zh_a_hist`、`stock_zh_a_hist_min_em` | `enabled` | `klt` 映射和 EM 字段顺序已实现 |
| `stock_cn` | `equity` | `sina` | `stock_zh_a_daily` | `catalog_only` | 新浪历史响应需要 JS 解码且成交额是独立请求，暂不启用 |
| `stock_cn` | `equity` | `tencent/stock_cn_http` | `stock_zh_a_hist_tx` | `enabled` | 已实现按年切片、JSONP 安全解析和成交量/成交额单位转换，仅支持不复权日线 |
| `stock_cn` | `index` | `tdx/normal_7709`、`eastmoney/index_http` | `get_index_bars`、`index_zh_a_hist_min_em` | `enabled` | TDX 指数响应的额外字段需单独对账 |
| `stock_cn` | `convertible_bond` | `eastmoney/convertible_bond_http` | `bond_zh_hs_cov_daily`、`bond_zh_hs_cov_min` | `enabled` | 与股票 Dataset 分离 |
| `stock_hk` | `equity` | `eastmoney/stock_hk_http` | `stock_hk_hist`、`stock_hk_hist_min_em` | `enabled` | 使用港股 `SecID` 转换 |
| `stock_hk` | `equity` | `sina` | `stock_hk_daily` | `catalog_only` | 待补充字段和半日市验证 |
| `stock_us` | `equity` | `eastmoney/stock_us_http` | `stock_us_hist`、`stock_us_hist_min_em` | `enabled` | 盘前盘后策略尚未作为默认采集范围 |
| `stock_us` | `equity` | `sina` | `stock_us_daily` | `catalog_only` | 待补充 DST 和交易所差异验证 |
| `stock_cn` | `index` | `csindex` | `index_hist_cni`、`stock_zh_index_hist_csindex` | `catalog_only` | 中证接口待接入 |
| `stock_cn` | `index` | `sw` | `index_hist_sw`、`index_hist_fund_sw` | `catalog_only` | 申万指数与基金指数分开登记 |

## 统一字段门槛

进入 `enabled` 的 Source 必须能证明：完整 OHLCV、amount 是否存在、成交量/成交额单位、时间标签、历史起始范围、分页上限和上游错误语义。缺失 amount 时写显式 null，不使用 `close * volume` 猜造。

以下接口只保留目录登记，不在缺少完整 OHLCV 证据时伪装成 K 线：`futures_*`、`option_*`、`fund_*`、`reits_*`、`forex_*`、`spot_*`。需要账户 token 的 Tushare 不登记。
