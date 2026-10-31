# 各模块怎么用（Agent 日常管理速查）

每个模块列出：做什么、在哪台机器、常用命令、细节文档。没有专用命令的管理接口用 `moox-cli gateway call`，请求写法见 [`api-recipes.md`](api-recipes.md)。
命令都在产物包（或仓库）根目录执行，默认读取当前目录的 `moox.toml`。交易模块尚未完工，这里不涉及。

## 数据模型（先理解这几个词）

| 概念 | 含义 |
| --- | --- |
| Space（空间） | 一组相互隔离的业务数据，默认有 `crypto`（加密货币）、`stockcn`（A 股，默认停用）等 |
| DataSource（数据来源） | 一个上游，如币安现货、币安合约 |
| Subject（标的） | 空间里的对象，如 `BTC-USDT`；Tag（标签）是一组标的，采集任务按标签选标的 |
| Dataset（数据集） | 某来源的一类数据（某频率的 K 线、因子结果等），绑定到 Storage 的 DataNode |
| Field / 列 | 数据集的字段；因子结果是结果数据集的列 |
| View | 面向查询的宽表，由 Storage 异步构建，可重建 |

## 采集

- **做什么**：按采集任务定时拉取或经 WebSocket 推送 K 线，写入 Storage；巡检缺口、补采、K 线合成（重采样）、每小时同步交易所标的。
- **在哪**：Collector 在管理台机；`collector-stream`（WS 推送与本地执行端）在海外机；可选的腾讯云 SCF 函数按地域部署。
- **常用**：

```bash
./bin/moox-cli collector status --space-id crypto                     # 总体健康、各数据集巡检进度与缺口
./bin/moox-cli collector task create --help                           # 新建 K 线采集任务
./bin/moox-cli collector kline check|backfill|backfill-status ...     # 检查与补采
./bin/moox-cli collector resample backfill|jobs|cancel ...            # 历史 K 线合成
./bin/moox-cli collector subjects exclude|unexclude|exclusions|sync   # 临时排除标的、立即同步标的
./bin/moox-cli collector ingest-mode get|set                          # WS 推送 / 定时拉取
./bin/moox-cli collector fetch-plan pause|resume                      # 暂停或恢复整个空间的采集
```

- 任务列表、修改、停用用 `gateway call trpc.moox.collector.CollectMgr`（见配方）。删除任务与结果不可恢复，先确认。
- 长历史导入：[`initial-import.md`](initial-import.md)。取 K 线数据：[`data-query.md`](data-query.md)。

## 存储（Storage）

- **做什么**：元数据目录（primary，SQLite）、字段级事实（node，Pebble）、可重建的 View（view，DuckDB/Bleve）。
- **在哪**：存储机，三个角色同机，DataNode ID 固定 `storage-node-0`（`setup init` 自动登记）。
- **常用**：`data kline get`、`data rows export` 读数据；`storage repair-view` / `force-rebuild-view` 修复 View（[`cli-operations.md`](cli-operations.md)、[`view-catchup.md`](view-catchup.md)）；`storage node update|delete` 改节点登记。
- **注意**：新建 View、加标的、调大 `view_rows_per_series` 都会增加存储机内存，按 [`storage.md`](storage.md) 估算并提醒用户。保留期等配置改在 `moox.toml` 后要重装对应单元才生效。
- 协议与命名规则：[`protocol.md`](protocol.md)。

## 因子

- **做什么**：用 YAML 表达式定义因子，按计算任务（源数据集 + 频率 → 结果数据集）在每个采集周期增量计算；支持历史补算和 Rust 写的 wasm 自定义算子。
- **在哪**：因子管理（factor-mgr）在管理台机；因子引擎在因子机（systemd 用户服务）。
- **常用**：写因子与转换 Python 因子按 [`factor-dsl.md`](factor-dsl.md)；批量配置写进 `moox.toml` 的 `[factors]` 后 `./bin/moox-cli setup factors`；
  任务启停、成员启停、补算用 `gateway call trpc.moox.factor.FactorMgr`；阶段控制 `./bin/moox-cli lifecycle status|check|init|start|confirm|pause|resume --task-id <任务>`；自定义算子 `./bin/moox-cli udf …`（[`udf.md`](udf.md)）。
- **注意**：被启用成员引用的定义不能改；长回看因子受保留期、`window.lookback`、`compute.max_memory` 三道限制。

## 策略

- **做什么**：声明式 DSL（`rank` / `signal` / `tranche` 规则、止损止盈、模板），每个周期在因子结果上求值，输出截面目标权重；可历史回放。
- **在哪**：管理台机。
- **常用**：写与校验 DSL、创建定义与实例按 [`strategy-dsl.md`](strategy-dsl.md)；定义、实例、结果、回放用 `gateway call trpc.moox.strategy.StrategyMgr`；
  逐期回放（因子 → 策略 → 纸面成交）用 `./bin/moox-cli replay start|status|report …`。迁移整套 Python 选币策略：[`strategy-migration.md`](strategy-migration.md)。
- **注意**：一个因子任务只能关联一个实例；改定义前先停用引用它的实例。不挂账户的实例只计算目标、不下单。

## 监控

- **做什么**：组件健康、数据新鲜度、主机资源、告警推送（企业微信 / 飞书）。管理台首页的“需要处理”来自这里。
- **常用**：`./bin/moox-cli doctor alerts`（[`monitor-attention.md`](monitor-attention.md)）、`doctor bootstrap`、`doctor diagnose`；`monitor mute|unmute` 临时屏蔽推送；告警渠道以 `moox.toml` 为准，`config notification` 同步。

## 网关与接入

- **主机网关**：每台主机一个，校验签名和服务目录 ACL 后转发到本机服务；管理台与 CLI 的所有调用都经过它。
- **Access**：外部签名调用入口（SCF、因子引擎、Skill 读数据），存储机与海外机各一个。
- **EventBus**：模块间异步事件（NATS JetStream），默认在存储机。凭据轮换必须按 [`eventbus-credentials.md`](eventbus-credentials.md) 同步到所有客户端。
- **出口代理**：海外机上为采集提供受限的 HTTPS 出口与 DNS。
- SCF 与私有网络：[`private-network.md`](private-network.md)。

## 主机与运维

- 新增、部署、启停、移除主机和部署，外部程序（因子引擎、归档 Worker）的安装与退役：[`host-registry.md`](host-registry.md)。管理台对这些只读。
- 升级已安装的单元：[`native-release.md`](native-release.md)；主机代理：[`host-agent.md`](host-agent.md)。
- 腾讯云：`setup firewall`（防火墙）、`setup reboot-host`（重启无响应的主机）、`ops tencent cls …`（日志服务，查询见 [`cls-query.md`](cls-query.md)）。

## 归档（只有命令行）

定时把事实数据按日 / 月导出成 CSV（或 CSV.GZ），Worker 跑在因子机。

```bash
./bin/moox-cli archive tasks prepare --space-id crypto --dataset-id <数据集>   # 看频率、保留期与推荐执行计划
./bin/moox-cli archive tasks create --space-id crypto --dataset-id <数据集> --cron "0 30 2 * * *"
./bin/moox-cli archive overview | runs list | partitions list|get
./bin/moox-cli archive tasks backfill|run|update|delete ...
```

## 系统暂停、恢复与重置（只有命令行）

```bash
./bin/moox-cli system status
./bin/moox-cli system pause      # 从下游到上游逐个暂停
./bin/moox-cli system resume     # 先追赶补采，全部追平后统一放行
./bin/moox-cli system reset plan|apply|status|abort   # 删除运行数据回到“刚部署完”的状态，破坏性，必须先得到用户明确确认
```

## 空间、密钥与配置

- 空间：`gateway call trpc.moox.admin.SpaceMgr`（见配方）。
- 密钥（交易所、云账户等）：请用户在管理台“设置 › 密钥”自己填写，Agent 只看列表。
- `moox.toml`：只能由用户在 `config init` / `config edit` 的本机页面里查看和修改；`config backup` 备份到管理台，换电脑 `config restore`。
