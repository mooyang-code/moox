# 系统初始化配置

这个目录是发布包和 `moox-cli setup init --config-dir` 使用的版本化初始化种子，只识别四个固定文件；Python
因子由用户配置文件 `moox.toml` 的 `[factors]` 配置控制：

| 文件 | 消费方 | 内容 |
| --- | --- | --- |
| `metadata.yaml` | `moox-cli setup init` | Admin 业务 Space 与 Storage 元数据 |
| `dataset-health-policy.yaml` | Monitor | 实时 Dataset 健康阈值 |
| `service-deployments.yaml` | Admin 部署导入 | 默认服务和 RPC 端点 |
| `collector-rules.yaml` | Collector | 默认采集规则 |

`metadata.yaml` 的业务 Space 包含 `stockcn`、`stockhk`、`stockus` 和 `crypto`。`mooxsys` 带
`attributes.scope: internal`，只进入 Storage，不显示在管理台业务空间选择器中。

初始化可重复执行。已有资源与声明一致时报告 unchanged；契约不一致时失败，不覆盖已有
配置和数据。Dataset 先以 disabled 创建，通过 Storage 激活检查后由 `setup init`
显式激活。

```bash
moox-cli setup init \
  --file ./moox.toml \
  --config-dir ./config/setup \
  --storage-host control
```

`--storage-host` 必须是 `moox.toml` 中已经部署 Storage 的主机名。`setup init` 读取
`metadata.yaml` 完成 Storage 初始化；Monitor、Admin 和 Collector 分别读取其余三个
职责专属配置文件。

默认因子配置位于仓库根目录的 `moox.toml.example`，当前把 `Bias.py` 和 `Cci.py`
关联到 `crypto/view_crypto_spot_kline_1m`（`1m`）。将示例复制为
`moox.toml` 后，`setup init` 会导入因子、创建绑定并启用它们；因子源文件路径相对
`factors.source_dir` 解析。若不需要默认因子，设置 `factors.enabled = false`。如果 Storage
已存在且 `setup init` 因元数据契约差异停止，可改用 `moox-cli setup factors --file ./moox.toml`
单独导入因子。
