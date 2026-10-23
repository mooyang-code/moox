# 因子示例

因子用表达式定义（语法与算子清单见 [`docs/模块/因子.md`](../../docs/模块/因子.md)），
写在 YAML 因子库文件里，一个文件可以包含多个因子：

```yaml
- factor_id: Bias
  name: Bias
  spec: |
    params:
      window: {type: int, min: 2, max: 120, default: 20}
    outputs:
      bias: close / ts_mean(close, window) - 1
```

[`timeseries.yaml`](./timeseries.yaml) 收录了几个时序因子示例：

- `Bias`：输入 `close`，输出 `bias`，`window` 是可调参数。
- `Cci`：输入 `high,low,close`，输出 `cci`。
- `ma`：输入 `close`，输出算术移动平均线 `ma`；同一定义可用不同成员别名加入因子集多次（如 `ma_fast`、`ma_slow`），各自调参。
- `sma`：输入 `close`，输出递推平滑移动平均线 `sma`，`n` 可调，`m` 固定为 1。

类型、输入列、输出列与回看都由编译推导，不需要手填。`params` 声明可调参数的类型、范围和默认值，策略每期可以在范围内调整（见
[因子文档第 11 节](../../docs/模块/因子.md)），回看按范围上限推导。`outputs` 中的技术列名固定，会写入结果 Dataset 和 Result View 列的
`display_name`，例如 `bias`、`ma`；用别名加入因子集时列名是 `<别名>_<输出名>`（别名与因子 ID 相同时就是输出名），
这一期实际用的参数在列 `<别名>__<参数名>` 里。`category_id`/`category_name` 是因子分类（可选）。

导入方式：

- `moox-cli setup factors`：在 `moox.toml` 的 `[[factors.definitions]]` 中写 `factor_id` 与 `file = "timeseries.yaml"`；
- `moox-factor-mgr-cli import --file timeseries.yaml --factor-id Bias`，或 `import-catalog --dir <目录>` 导入目录下全部 `*.yaml`。
