# 因子示例

因子用表达式定义（语法与算子清单见 [`docs/模块/因子.md`](../../docs/模块/因子.md)），
写在 YAML 因子库文件里，一个文件可以包含多个因子：

```yaml
- factor_id: bias
  name: Bias
  spec: |
    params:
      window: {type: int, min: 2, max: 120, default: 20}
    output: close / ts_mean(close, window) - 1
```

[`timeseries.yaml`](./timeseries.yaml) 收录了几个因子示例：

- `bias`：输入 `close`，`window` 是可调参数；只有一个输出，写 `output`，结果列就是成员键（参数全为默认值时叫 `bias`）。
- `cci`：输入 `high,low,close`，单输出。
- `ma_fast`、`ma_slow`：需要两条各自调参的均线时，注册两个因子，在策略里各自作为可变成员。
- `channel`：多输出因子，写 `outputs`，输出名 `high`、`low` 不带因子前缀，结果列是 `channel__high`、`channel__low`。

类型、输入列、输出列与回看都由编译推导，不需要手填。`params` 声明可调参数的类型、范围和默认值，策略每期可以在范围内调整，回看按范围上限推导。
因子 ID、输出名、参数名都是小写，词与词之间用单个下划线；成员键与列名的拼接规则见
[因子文档](../../docs/模块/因子.md)「成员键与列名」。`category_id`/`category_name` 是因子分类（可选）。

导入方式：

- `moox-cli setup factors`：在 `moox.toml` 的 `[[factors.definitions]]` 中写 `factor_id` 与 `file = "timeseries.yaml"`；
- `moox-factor-mgr-cli import --file timeseries.yaml --factor-id bias`，或 `import-catalog --dir <目录>` 导入目录下全部 `*.yaml`。
