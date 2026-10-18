# 因子示例

因子用表达式定义（语法与算子清单见 [`docs/模块/因子.md`](../../docs/模块/因子.md)），
写在 YAML 因子库文件里，一个文件可以包含多个因子：

```yaml
- factor_id: Bias
  name: Bias
  spec: |
    params: {window: [5, 20]}
    outputs:
      bias_{window}: close / ts_mean(close, window) - 1
```

[`timeseries.yaml`](./timeseries.yaml) 收录了几个时序因子示例：

- `Bias`：输入 `close`，按 `window` 输出多个 Bias 列。
- `Cci`：输入 `high,low,close`，输出 `cci`。
- `ma`：输入 `close`，按 `window` 输出算术移动平均线。
- `sma`：输入 `close`，输出递推平滑移动平均线 `SMA(close, 20, 1)`。

类型、输入列、输出列与回看都由编译推导，不需要手填。`outputs` 中的技术列名会原样写入
结果 Dataset 和 Result View 列的 `display_name`，例如 `bias_20`、`ma_20`，前端据此展示因子结果表头。

导入方式：

- `moox-cli setup factors`：在 `moox.toml` 的 `[[factors.definitions]]` 中写 `factor_id` 与 `file = "timeseries.yaml"`；
- `moox-factor-mgr-cli import --file timeseries.yaml --factor-id Bias`，或 `import-catalog --dir <目录>` 导入目录下全部 `*.yaml`。
