# 系统初始化配置

这个目录是发布包和 `moox-cli setup init --config-dir` 使用的版本化初始化种子，只识别四个固定文件；Python
因子由用户配置文件 `moox.toml` 的 `[factors]` 配置控制：

| 文件 | 消费方 | 内容 |
| --- | --- | --- |
| `metadata.yaml` | `moox-cli setup init` | Admin 业务 Space 与 Storage 元数据 |
| `dataset-health-policy.yaml` | Monitor | 实时 Dataset 健康阈值 |
| `service-deployments.yaml` | Admin 部署导入 | 默认服务和 RPC 端点 |
| `collection-tasks.yaml` | Collector | 默认采集任务 |

`metadata.yaml` 的业务 Space 包含 `stockcn`、`stockhk`、`stockus` 和 `crypto`。`mooxsys` 带
`attributes.scope: internal`，只进入 Storage，不显示在管理台业务空间选择器中。

根目录 `moox.toml` 的 `[scf_fetcher.tencent_limits]` 保存腾讯云 SCF 配额参考。默认值为每
地域 5 个命名空间、每命名空间 50 个函数、每分钟 burst 500，并记录已核对地域的账号级并发
内存配额。发布校验会使用其中的函数数上限；实际套餐或工单提额仍需以腾讯云实时账号配额为准。
建议每个 SCF Space 使用独立 `namespace`，并为发布器自动创建的 Invoke canary、stockcn
Instrument snapshot Timer 预留函数配额。首次发布时 CloudNode 会在目标地域幂等创建非
`default` namespace，旧 namespace 中的同前缀节点不会被误复用。
仓库实际 `moox.toml` 保留既有 `default` fleet 且默认关闭 SCF；重新启用前请先完成 namespace
迁移或地域节点重平衡，发布校验会拒绝包含辅助函数后超过配额的配置。

初始化可重复执行。已有资源与声明一致时报告 unchanged；契约不一致时失败，不覆盖已有
配置和数据。Dataset 先以 disabled 创建，通过 Storage 激活检查后由 `setup init`
显式激活。

需要清空并重建全部 Storage/View 数据时，使用 `moox-cli setup rebuild-storage`；命令默认只做
dry-run，确认目标和数量后加 `--yes` 执行全量删除、重新导入本目录的 `metadata.yaml` 并激活
Dataset。

```bash
moox-cli setup init \
  --file ./moox.toml \
  --config-dir ./config/setup \
  --storage-host control
```

`--storage-host` 必须是 `moox.toml` 中已经部署 Storage 的主机名。`setup init` 读取
`metadata.yaml` 完成 Storage 初始化；Monitor、Admin 和 Collector 分别读取其余三个
职责专属配置文件。

启用 SCF 时，`setup init` 会在写入元数据前发现 `storage_host` 的腾讯地域/VPC/子网，
并在结果中返回 `scf_routes`。后续发布命令按地域选择 Storage 私网或公网：

```bash
moox-cli setup scf-network-plan --file ./moox.toml
moox-cli collector function publish submit --file ./moox.toml --space-id crypto --same-region-first
```

同地域函数绑定 Storage VPC/子网并使用私网 `11003`；发布时先完整占满该地域配置的
节点数并完成 canary/回读，再继续其他地域。跨地域不创建 CCN，使用
`storage_gateway_host` 公网回退。
节点“占满”以该地域的 `function_count` 为准；如需集中内网流量，请在配置中提高
Storage 同地域的目标数量，CLI 不会自动挪动其他地域配额。

默认因子配置位于仓库根目录的 `moox.toml.example`，当前把 `Bias.py` 和 `Cci.py`
关联到 `crypto/view_binance_spot_kline_1m`（`1m`）。将示例复制为
`moox.toml` 后，`setup init` 会导入因子、创建绑定并启用它们；因子源文件路径相对
`factors.source_dir` 解析。若不需要默认因子，设置 `factors.enabled = false`。如果 Storage
已存在且 `setup init` 因元数据契约差异停止，可改用 `moox-cli setup factors --file ./moox.toml`
单独导入因子。
