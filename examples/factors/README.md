# Python 因子示例

当前公开示例统一实现 `compute(df, params)`，返回包含
`data_time`、`series_tag` 与全部输出列的 `pandas.DataFrame`：

- `timeseries/bias.py`：输入 `close`，按 `params.windows` 输出多个 Bias 列。
- `timeseries/cci.py`：输入 `high,low,close`，按 `params.window` 输出 `cci`。
- `timeseries/ma.py`：输入 `close`，按 `params.windows` 输出算术移动平均线。
- `timeseries/sma.py`：输入 `close`，按 `params.windows` 和 `params.m` 输出递推平滑移动平均线；默认配置读取 600 期，为 `SMA20` 提供 30 倍预热区间。

框架会额外提供只读保留列 `data_time`。每个输出必须是与输入 DataFrame
长度和 index 完全一致的 `pandas.Series`。

初始化时，`outputs` 中的技术列名会原样写入结果 Dataset 和 Result View
列的 `display_name`，例如 `bias_20`、`ma_20`，前端据此展示因子结果表头。
