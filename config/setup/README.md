# 系统初始化配置

这个目录是发布包和 `moox-cli setup init --config-dir` 使用的版本化初始化种子，包含三个固定种子文件；
因子由用户配置文件 `moox.toml` 的 `[factors]` 配置控制：

| 文件 | 消费方 | 内容 |
| --- | --- | --- |
| `metadata.yaml` | `moox-cli setup init` | Admin 业务 Space 与 Storage 元数据 |
| `dataset-health-policy.yaml` | Monitor | 实时 Dataset 健康阈值 |
| `collection-tasks.yaml` | Collector | 默认采集任务 |

`metadata.yaml` 的业务 Space 包含 `stockcn`、`stockhk`、`stockus` 和 `crypto`。`mooxsys` 带
`attributes.scope: internal`，只进入 Storage，不显示在管理台业务空间选择器中。

SCF 采集函数的云账号、地域网络与空间在根目录 `moox.toml` 的 `[scf_plan]` 声明；函数数量由 Collector 按标的数与每函数标的数自动计算，每个空间使用独立 namespace。
发布校验会拒绝包含辅助函数后超过配额的配置。

初始化可重复执行。已有资源与声明一致时报告 unchanged；契约不一致时失败，不覆盖已有
配置和数据。Dataset 先以 disabled 创建，通过 Storage 激活检查后由 `setup init`
显式激活。

需要清空全部行情与因子数据时，按 `docs/部署与运维.md`「清空全部数据重新积累」的顺序操作；
数据集、View、标的等定义保留，不需要重新导入本目录。

```bash
moox-cli setup init \
  --file ./moox.toml \
  --config-dir ./config/setup \
  --storage-host storage
```

`--storage-host` 必须是 `moox.toml` 中已经部署 Storage 的主机名。`setup init` 读取
`metadata.yaml` 创建 Admin 业务空间并完成 Storage 元数据初始化；Monitor 的
`dataset-health-policy.yaml` 和 Collector 的 `collection-tasks.yaml` 由打包与原生安装流程放入各组件配置目录，分别在模块启动时读取。

启用 SCF 时，每个采集地域都要有一台同地域、带私网地址的 Access 主机；函数在该地域 VPC 里经
Access 私网地址认领和写入，规则见 `skills/moox/references/private-network.md`。

因子配置示例位于仓库根目录的 `moox.toml.example`：`[[factors.tasks]]` 声明因子任务（`id`、`name`、`space_id`、`source_dataset_id`、`freq`），
任务的成员用内嵌的 `members` 数组写：`{ factor = "ema", params = { span = 20, warmup = 50000 } }` 是固定成员，`{ factor = "ma_fast", var = true }` 是可变成员（由关联的策略逐期提交参数）；
`[[factors.definitions]]` 声明因子定义（`factor_id` 加因子库文件）。示例把币安现货、合约 1m 采集任务结果（`dataset_dasftksvjhj2jom4vhd0`、`dataset_dasftksvjhj2jom4vhdg`）
各接一个因子任务。将示例复制为 `moox.toml` 后，`setup init` 会导入因子定义、创建任务与成员并启用它们；因子库文件路径相对
`factors.source_dir` 解析。若不需要因子，设置 `factors.enabled = false`。如果 Storage
已存在且 `setup init` 因元数据契约差异停止，可改用 `moox-cli setup factors --file ./moox.toml`
单独导入因子。`setup factors` 按任务 ID 查找或创建任务，按推导出的成员键查找或加入成员，逐项报告「已存在 / 新建 / 冲突」并打印每个成员的成员键与列名；
配置里已经去掉的成员只报告、不自动移除，某一项冲突不影响其余项。
