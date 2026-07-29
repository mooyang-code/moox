# Python 因子示例

当前公开示例统一实现 `compute(df, params)`，返回
`dict[str, pandas.Series]`：

- `timeseries/Bias.py`：输入 `close`，按 `params.windows` 输出多个 Bias 列。
- `timeseries/Cci.py`：输入 `high,low,close`，按 `params.window` 输出 `cci`。
- `timeseries/ExcessReturn.py`：输入 `nav,benchmark_return`，输出 `excess_return,rolling_rank`。

框架会额外提供只读保留列 `data_time`。每个输出必须是与输入 DataFrame
长度和 index 完全一致的 `pandas.Series`。
