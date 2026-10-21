# WS 行情推送采集实施清单

> 依据[执行计划](WS行情推送采集执行计划.md) v4 与[设计](WS行情推送采集设计.md) v4 逐条展开，兼作实施进度表。项目尚未上线，按用户说明删去纯为兼容或平滑迁移而设的步骤（见末节），功能条目不删。状态：☐ 未做、☑ 已完成、△ 部分完成（附说明）。

## 阶段 0：前置核对

- ☑ 合约 WS 限额：官方文档（USDⓈ-M Futures → WebSocket Market Streams → Connect）为每连接 1024 流、入站 10 条/秒、24 小时寿命、服务端每 3 分钟 ping、10 分钟不回 pong 断开；建连频率未写明。适配器 `Limits` 按现货的每秒 5 条保守取值，建连保护沿用每 5 分钟 300 次。
- ☐ 海外机出站测速（两个 1m、两个 1h 数据集串行提交耗时 < `commit_reserve_ms`）：需要在 compute-1 上执行，放到发布阶段。

## 阶段 1：SCF 合约数据修复

- ☐ `SCFPlanSpaceConfig.Markets`（`scf_plan.spaces.<空间>.markets.<市场>.fetch`、`lateness_s`），`fetchFor(spaceID, marketType)`；期望状态按函数市场取拉取参数。
- ☐ `PlanFetch.NotBeforeMS`（0 或 1000～12000）；`plan_runtime` 在错峰之后再等到收盘 + `not_before_ms`。
- ☐ 默认配置：crypto 合约 `not_before_ms: 8000`、`lateness_s: 20`，现货 `not_before_ms: 3000`。
- ☐ 测试：按市场覆盖、`plan_desired` 两市场不同、`not_before_ms` 范围校验、`plan_runtime` 取数开始不早于收盘 + 8 秒。
- ☐ 发布：先升函数包、再装 Collector 与配置；历史数据重拉（10-16 起到切换的 P，合约 1m/1h、现货 1m），因子补算。

## 阶段 2：框架与币安适配器

- ☐ `marketdata.Instrument.Pair`，币安合约快照填入；采集计划带给 stream（`PlanSubject`/计划里的 pair 映射）。
- ☐ `internal/stream`：`source.go`、`conn.go`、`manager.go`、`gate.go`、`planclient.go`、`metrics.go`。
- ☐ `marketfetch.KlineBatch` 与 `KlineCommitter`（`CommitPeriod`、`Upsert`），`KlinePipeline.Execute` 改为"取数 + 提交"；逐字节一致测试。
- ☐ stream 计划条目上限（4000），`MaxPlanItems` 200 只对 SCF。
- ☐ 本地执行端：`serverless/market_data` 抽出 `Execute(ctx, event, env)`，去掉进程环境依赖；`MarketFetchExecutor.Invoke` RPC；独立并发上限与 HTTP 客户端；测试。
- ☐ 币安适配器 `sources/binance/stream.go`（现货 kline、合约 continuousKline），`binance.yaml` 的 `stream` 段。
- ☐ 测试：报文解码与 REST 字段一致、大小写键回归；连接管理（断线、ping、订阅报错、轮换无重复无缺失、订阅耗时）；下架探测（`!contractInfo`、连续 3 个 1m 周期无推送）；闸门（到齐、D 时提交、迟到增量提交、去重、跨 P、整点 1m 先于 1h）；`-race`。

## 阶段 3：Collector

- ☐ 表 `t_collector_ingest_modes`、`t_collector_stream_workers`。
- ☐ 配置 `default_ingest`、`default_executor`、`stream.{heartbeat_timeout_seconds, lateness_s, commit_reserve_ms}`；初值规则（已在计划里的数据集按现状落库）。
- ☐ 对账器执行端抽象：scf / stream / local 计划项，SCF 专属步骤只作用于 scf 项；stream/local 以心跳确认。
- ☐ 截止跟随采集方式：`OldLatenessS`、`WindowEnd` 用旧值、交接中的数据集在 drain 改属性；新数据集直接取新值。
- ☐ 交接（prepare → switch → drain，P 至少 3 分钟）；交接与标的变更分开；`--force`（含已打开的计划）；Collector 重启续跑。
- ☐ drain 结束后 SCF 函数去掉数据集、全部去掉后停用定时器。
- ☐ 补采：`datasetMarket` 解析三类执行端；`Invoker` 按数据集 `executor` 选 SCF 或本地执行端。
- ☐ 下架处理：`ReportStreamPeriods` 带疑似下架标的 → 立即同步相应自动标签；快照取数重试 3 次、失败保持成员。
- ☐ `StreamCollector` 服务（`StreamHeartbeat`、`PullStreamPlan`、`ReportStreamPeriods`）；`CollectMgr.SetIngestMode`/`GetIngestMode`；服务目录 ACL。
- ☐ 周期统计进 Collector 指标（不落库）。
- ☐ `moox-cli collector ingest-mode get|set`（打印 P 与 stream 状态、离线拒绝、`--force`）。
- ☐ 运行状态页"函数"区显示 stream 实例、连接、最近周期。
- ☐ 测试：执行计划 5.6 全部场景。

## 阶段 4：`collector-stream` 组件

- ☐ `cmd/stream` 主程序（`/healthz`、`/readyz`、`/metrics`，同进程挂 `MarketFetchExecutor`）、`config/stream/app.yaml`。
- ☐ 服务目录组件 `collector-stream`（`scope: any`、`replicas: single`、`required_in_default_profile: false`），Storage 权限，`MarketFetchExecutor` 服务。
- ☐ `unitdeploy`：control 上派生 `moox-collector` 应用密钥注入 `collector-stream`；`unitpackage` 加入组件；部署工具支持向已部署单元新增组件。
- ☐ `scripts/build`：`build.sh collector-stream`。
- ☐ `moox.toml.example` 与 setup 渲染：overseas 默认放 `collector-stream`；`default_ingest`/`default_executor` 渲染（crypto stream+local，其余 pull+scf）。
- ☐ Monitor 告警：stream 心跳中断 > 3 分钟；连续 3 个周期有失败标的（疑似下架不计）；因子水位停滞 > 30 分钟。
- ☐ 测试：严格配置解析、健康检查、setup 渲染、服务目录一致性。

## 阶段 5a：因子连续水位

- ☐ 详细设计（短文档）。
- ☐ `committed_through` 持久化；逐期处理与阻塞（零提交、缺失超阈值默认 10%）；当期降级不阻塞、失败标的记入空洞账本由 gapfill 只填充补算；确认缺失不阻塞。
- ☐ "周期已补齐"事件（补采发、因子收）与阻塞期间定时查 Storage 保底；阻塞 > 30 分钟告警。
- ☐ 核实线上 `gap.enabled`。

## 阶段 6：切换（线上）

- ☐ 切换前基线：策略 1m 整点产出延迟、SCF 写入完成相对收盘。
- ☐ `ingest-mode set --space-id crypto --mode stream --yes`，记录 P，跟踪交接。
- ☐ 验收：周期标记全 complete、写入完成 p99 ≤ 1 秒、截止 5/8 秒、策略延迟下降 ≥ 5 秒、截止时缺失中位 0、补采走 local 且因子水位等补齐后前进。
- ☐ 回退演练：切回 pull+scf、再切回 stream；`--force` 演练。

## 阶段 7：收尾

- ☐ 模块文档（采集、命令行工具、监控、部署与运维）；计划目录标"已实现"。
- ☐ 一周后复核（本会话外的待办）。

## 阶段 8：切换后的独立任务

- ☐ `marketdata.Catalog`；`NewSubjectListers`、`newMarketProvider`、`newStockCNProviderForSource` 改为从目录取；等价性测试。
- ☐ 采集名单干预：表 `t_collector_subject_exclusions`、三个 RPC、期望状态去掉排除、到期失效、CLI、活动日志、服务目录（含外部身份 `collector-operator`）、测试。

## 按"无需兼容"删去的步骤

- 阶段 1 发布第 2 步（先装能解析新字段、但配置未改的 Collector）与第 5 步（先恢复配置再回退二进制的顺序）：Collector 二进制与配置一起换。保留"函数包先于带 `not_before_ms` 的计划下发"：函数按未知字段拒绝解码计划，这是运行时约束，不是兼容层。
- 阶段 4 发布第 2 步（先在线上配置显式写 `pull`/`scf`）与第 4 步（回退时先恢复旧配置）：由"已在已生效计划里的数据集按现有执行端初始化采集方式"替代，升级本身不改变 crypto 的采集方式，切换只由阶段 6 的命令完成。
