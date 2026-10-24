# 系统初始化配置

这个目录是发布包和 `moox-cli setup init --config-dir` 使用的版本化初始化种子，只识别四个固定文件；
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
  --storage-host control
```

`--storage-host` 必须是 `moox.toml` 中已经部署 Storage 的主机名。`setup init` 读取
`metadata.yaml` 完成 Storage 初始化；Monitor、Admin 和 Collector 分别读取其余三个
职责专属配置文件。

启用 SCF 时，每个采集地域都要有一台同地域、带私网地址的 Access 主机；函数在该地域 VPC 里经
Access 私网地址认领和写入，规则见 `skills/moox/references/private-network.md`。

默认因子配置位于仓库根目录的 `moox.toml.example`，当前把 `xbx.yaml` 里的 `Bias`、`Cci`、`MinMax`、`QuoteVolumeMean`，
以及 `common.yaml` 里的常用技术指标（MA、EMA、MACD、RSI、ROC、BOLL、ATR、收益率波动、VWAP）
加入币安现货、合约 1m 采集任务结果（`dataset_dasftksvjhj2jom4vhd0`、`dataset_dasftksvjhj2jom4vhdg`）的因子集。示例用 `[[factors.definitions]]`
声明因子、用 `[[factors.members]]` 声明因子集成员。将示例复制为
`moox.toml` 后，`setup init` 会导入因子定义、创建成员并启用它们；因子库文件路径相对
`factors.source_dir` 解析。若不需要默认因子，设置 `factors.enabled = false`。如果 Storage
已存在且 `setup init` 因元数据契约差异停止，可改用 `moox-cli setup factors --file ./moox.toml`
单独导入因子。已经在运行的环境要补上后来新增的默认因子时，先执行
`moox-cli config migrate --file ./moox.toml --add-default-factors`（预览，确认后加 `--yes` 写回，原文件自动备份），
把样例里有、配置里还没有的定义与成员补进 `moox.toml`，再执行 `moox-cli setup factors --file ./moox.toml` 导入；
已有的定义与成员不会被改动。
