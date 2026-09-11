# K-line Freshness Metrics and Gap Audit Removal Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 删除 Collector 中自动周期 gap audit 及其 Storage 读放大路径；Storage 底层只提供通用的 View 数据进入/成功写入观测，不识别 K 线语义；由 Monitor 针对指定 View 判断 K 线业务时间是否持续停更并产生可恢复告警，最后完成正式编译、SCF 发布和 106 主机真实端到端验收。

> **最新方案决策（2026-09-09）：** Storage Primary、Storage 公共读写层和 Collector 都不承载 K 线监控特殊逻辑；不得在这些层判断 dataset 名称、调用 `observability.IsKlineDatasetID` 或维护 K 线专用指标。只有 View 在 active index 成功写入后，按通用 `dataset_id + view_id + subject_id + freq + series_tag` 上报 output `data_time` 观测。K 线语义、交易时段、freshness 窗口、连续失败/恢复和告警状态全部属于 Monitor。
>
> **采集与目录边界：** 全市场标的目录是独立的 daily Instrument SCF，每天执行一次完整快照；Sina、Tencent、EastMoney 等 Instrument API 可并行请求并合并为完整结果，单一来源失败时继续使用其他来源，不能用不完整快照替换当前 `ActiveInstrumentSet`。逐标的 K 线由独立的定时 Kline SCF 执行，不要求每个周期重新拉全市场目录；它消费最近一次成功的 active subject 集合，控制面 `/data/subjects` 的最近成功集合作为目录 API 暂时失败时的兜底。少量标的失败、缺失或单一 Provider 报错必须隔离到标的/Provider 候选链，不能阻塞整组或全市场采集。
>
> **告警成本与节奏：** 不再周期性扫描历史桶，也不依赖外部 HTTP `/metrics` 抓取来判断告警。现有 reporter/watchdog 的 30 秒周期只是 View 观测进入 Monitor 的传输与评估节奏，不是所有告警策略的统一固定周期；只有 K 线 freshness 默认使用该周期，其他 Monitor check 保留各自的调度配置。告警只比较指定 View 的最新 output `data_time`，允许“10:00 有、10:01 缺、10:02 恢复”，仅连续超过 freshness 窗口才 firing。

> **最新发布参数（2026-09-09）：** stockcn Kline Timer 正式目标为 `170` 个 SCF 函数，每个 Group 最多 `40` 个标的；独立 daily Instrument Timer 不计入该数量。170 是当前发布配置，不是运行时自动扩缩容条件；公网出口 IP 只作诊断，不作为启用门禁。Instrument 目录失败时 Kline 不重新拉全市场目录，优先消费最近成功的 `ActiveInstrumentSet`，再以控制面 `/data/subjects` 作为兜底；单标的或单 Provider 失败不得阻塞整组。

> **最新存储责任边界（2026-09-09）：** 删除所有 Storage Primary/公共存储层的 K 线监控特殊逻辑，包括 dataset 名称判断、`observability.IsKlineDatasetID`、Kline 专用指标和告警分支。只有 View 在 active index 成功写入后上报带 dataset/view/canonical subject/frequency/series tag 的通用 output `data_time` 观测；Monitor 监控配置指定 View 的 output watermark，负责交易时段、freshness 窗口和 firing/resolved。指标通过 EventBus/Monitor Consumer 进入 latest，不增加周期性 HTTP `/metrics` 抓取；30 秒只代表 K 线 freshness 默认评估节奏，其他告警保留各自周期。

> **最新发布编排结论（2026-09-09）：** `--enable-stockcn` 的一次性提交可能已完成 170 个 Timer/独立 Instrument fleet 更新，却在 InvokeFunction/canary 阶段长时间不返回；因此下一轮不能把 CLI 进程存活或部分 CloudNode 日志当作成功。发布必须分阶段持久化 package/job/function 状态：先 disabled fleet，再逐 job status/readback，再做独立 Instrument canary，再做 Kline canary，最后才允许按门禁启用 Timer/Rule；任一调用超时或结果未知都保持 disabled，并先清理半启用状态。每个阶段需要自己的超时、非敏感 artifact 和可重试边界，禁止再次无边界等待一个聚合命令。

> **执行状态（2026-09-09）：** 自动 gap audit 删除、Storage 通用 View output 观测、Primary 解耦和 Monitor 指定 View freshness 主链路已实现。最新独立 codeCR 未发现 P0/P1；此前发现的 4 个 P1 已全部处理：无 output 静默、动态 label/series 无界、退市/旧 subject 污染、evaluation interval 被 watchdog 绕过；active/replacement watermark 丢失和 disabled rule 不收敛也已处理。Monitor 现在从 Storage Metadata `ListDatasetSubjects` 分页读取并短缓存 active subject catalog，只对仍 active 的 canonical subject 判定 freshness；metadata 不可用时该组显式失败，不回退为健康。新增的 Monitor E2E 已覆盖 reporter-shaped snapshot -> JetStream -> Monitor latest -> Kline evaluator；2026-09-09 11:23 已补齐真实 Storage View -> Reporter -> EventBus -> Monitor latest 证据。第三次正式 stockcn publish 已完成 fleet 更新但 Instrument canary 在 7 分钟总预算内超时，尚无 SCF durable row/真实 request_id；已将 Instrument SCF 函数预算提高到 900 秒，下一轮必须重新编译、分阶段发布和验收，整体正式验收继续为 NO-GO。

> **最新运行负载结论（2026-09-09）：** 对上一次中止后的控制面只读盘点发现 `crypto` 的 5 条 Binance 规则仍启用，`stockcn` 的 `builtin-stockcn-kline-1m` 也因中止发生了部分启用；已通过 `DisableTaskRule` 逐条停用，保留历史数据和运行记录。Storage 日志进一步确认拖垮链路的不是已删除的 Collector gap audit，而是 Storage View 的历史 maintenance/backfill：`view_crypto_spot_kline_1m`、`view_mooxsys_service_metrics` 等任务持续做 Primary 历史扫描，出现百万行扫描上限、View series capacity、EventBus publish timeout 和 Collector 到 Storage 的 tRPC timeout。已在真实 Storage 主机持久化 `MOOX_STORAGE_VIEW_MAINTENANCE_DISABLED=1` 并仅重启 `storage-view`；启动日志确认 `historical maintenance disabled`，未删除 Primary/View 数据。第三次 publish 已更新 170 个 Kline Timer 和独立 Instrument Timer，但 Instrument canary 在有界阶段预算内超时，Rule/Timer 仍未启用；下一轮使用 900 秒独立 Instrument SCF 函数预算并继续只读回读，不能把 fleet 更新写成正式通过。

> **最新真实环境证据（2026-09-09 07:45，Asia/Shanghai）：** Storage 按代码 commit `8bc52c941ad796db7d4fc1eb2a974b6e4ece2e99` 发布，Linux Storage 三个 binary hash 一致；部署窗口内 `setup verify-storage --host storage` 已通过，证明 Node/Primary/View `ready`、Schema v10、DataNode `storage-node-0/READY`、20 datasets。随后发现 `MOOX_STORAGE_VIEW_MAINTENANCE_DISABLED` 没有进入 storage-view 进程，已在远端 `config/runtime.env` 持久化为 `1` 并重启；07:42:10 日志确认 historical maintenance disabled，未删除数据，但 NATS consumer 仍有一次 rebind/disconnect，需要后续 readiness 复核。Control `setup deploy-control` 返回 `ready`。
>
> 本轮真实 SCF 首次命令于 06:37:55 提交，59 分 48 秒无结果后中止。第二次命令于 11:28:51 提交，控制面日志推进到全部 170 个 Kline Timer 和独立 Instrument Timer 的更新，但约 36 分钟仍未产生 summary/JSON，也未返回 `SUCCESS` job、`success=true` canary 或 request_id，已中止。Primary 只读回到 `600000.XSHG` 最新 `data_time=2026-09-08T06:59:00Z`，查询 `2026-09-09T00:00:00Z` 以后返回 0 行；View `view_stockcn_equity_kline_1m` 读回仍返回 `PrimaryStore/ReadTimeSeriesRows HTTP 500`，因此没有 View durable-row 证据。Monitor SQLite 当前仍记录 stockcn collector `down/尚未上报`，没有 stockcn 的通用 View freshness metric family；结论仍为正式环境 `NO-GO`。部分 fleet 可能已被控制面更新，下一步必须先只读盘点并清理半启用状态，再修复 InvokeFunction/SCF 响应链路后重试，不能直接勾选发布完成。

> **最新指标上报排障结论（2026-09-09）：** EventBus broker、Storage View consumer 和 metrics publisher ACL 均已通过只读/小消息探针；历史 archive consumer 已按“先停历史任务”的决定删除，未删除业务数据。真正持续失败的是通用 Storage metrics reporter 的 timer context：Storage View 完整 `/metrics` 快照约 517 KB，采集/编码耗时超过原有 10 秒 timer timeout，最终表现为 `jetstream publish timeout: context deadline exceeded`。这不是 K 线专用逻辑，也不是把 30 秒告警周期改成 60 秒；已把 Storage View、Primary、Node 四份 metrics timer timeout 统一调整为 60 秒，仍保持每 30 秒触发。正式 Storage release 已部署到 `146.56.196.204`，三角色二进制 SHA256 均为 `34ae37c6161be9fbd352f979aabe6623499ba94cc3baf73ef18e1d2554e63be5`；View/Primary/Node health 已读回，Monitor 已在 2026-09-09 11:23 收到 `storage-view@storage` 的新 snapshot，且包含 `dataset_binance_spot_kline_1m` 的 View output data-time family。此前 11:18-11:20 的 timeout 属于发布链路恢复前窗口，不能作为新版本持续失败证据；生产 Storage metrics 门禁已通过，SCF/stockcn 门禁仍未通过。

> **最新 codeCR 增量结论（2026-09-09）：** 发现 30 秒 cron 与 60 秒 timeout 组合下，`report.Handler.Handle` 的互斥锁会让慢的 Gather/Publish 任务在 cron goroutine 中排队，存在 goroutine、CPU 和内存逐步放大的 P1 风险。已增加原子 `inFlight` 门禁：已有报告执行时后续 tick 直接跳过，并补充并发调用和阻塞解除后的恢复测试；原有 publisher 互斥仍保留。另发现 Instrument canary 的 5 次重试可能把单次 7 分钟 HTTP 超时放大为约 35 分钟；已增加整个 canary 阶段的 7 分钟 context 预算，使用 request-local control client，且错误保留 node/shard/attempt 与底层 InvokeFunction 上下文。独立复审确认无 P0/P1，canary 相关 P2 已处理。仍保留 P2：Prometheus `Gather()` 接口本身不可取消，若自定义 Collector 永久阻塞，单个 Handler 会停止发布直到该调用返回或进程重启；生产验收需观察新部署后的连续周期，不能把本地测试写成永久阻塞已解决。

> **最新独立 codeCR 结论（2026-09-09）：** P1-1：View 没有 output watermark 时不能静默跳过，否则 View route/consumer/写入故障没有 Kline 告警；已改为 `no_observation` 失败。P1-2：六个动态 label 未限长且 GaugeVec 无上限，可能让 Reporter snapshot 超过 100000 samples；已限制单进程观测 series 为 20000、label 为 256 bytes，并由 Monitor 保持 100000 行有界读。P1-3：旧 producer/退市 subject 可能留在 MetricLatest/GaugeVec；已增加 Metadata `ListDatasetSubjects` 分页、active catalog 短缓存、无 active 观测的 stale 计数和 catalog error 显式失败。P1-4：`evaluation_interval` 过去只写 check metadata，实际每 30 秒仍 Evaluate；已在 business freshness reporter 内按 interval 节流，并覆盖测试。P2-1：A/B replacement 写入失败可能掩盖 active A 已成功写入的 output watermark；已提前上报 active 成功部分。P2-2：禁用/删除 Kline rule 后 firing state 可能不恢复；已增加 `disabled`/`no_longer_expected` 收敛。P2-3：本地 E2E 仍是 reporter-shaped snapshot -> JetStream -> Monitor latest -> evaluator，未覆盖同一条真实 Storage View -> Reporter -> EventBus -> Monitor -> notification 闭环；保留为正式验收未完成项。以上结论覆盖代码、符号和触发条件；本地单测不能替代 P2-3 或正式 SCF/Storage readback。

**Architecture:** Storage View 消费 Primary 变更，在 active View index 实际成功写入后上报通用 output `data_time`；Storage Primary、公共 Storage 层和 Collector 不创建、不判断、不维护任何 K 线专用指标。指标标签固定为 `space_id + view_id + dataset_id + subject_id + freq + series_tag`，不使用 provider symbol。服务自身的 tRPC metrics timer 每 30 秒把这些值通过现有 MetricSnapshot/EventBus/Monitor Consumer 写入 latest；Monitor 不抓 HTTP `/metrics`，而是在自身配置的 watchdog 周期读取指定 View 的 output latest，按 `view_id + frequency` 聚合，只对持续超过阈值的标的组告警。10:00 有数据、10:01 缺失、10:02 恢复不会触发告警，也不会扫描内部桶缺口；30 秒只是 K 线 freshness 的默认观测/评估周期，不约束其他告警。历史 Backfill、显式 GapRepair、HistoryPolicy 和 K 线 Pipeline 合同保留，但 Scheduler 不再自动查询 Storage 触发历史补采。

**Tech Stack:** Go modules、Prometheus client_golang、tRPC timer、NATS JetStream EventBus、GORM/SQLite、`packages/marketcalendar`、YAML 配置、现有 `moox-cli collector function publish`、Storage ReadTimeSeriesRows/CLI 数据读回。

---

> **最新发布链路修复（2026-09-09）：** codeCR 发现 900 秒 Instrument SCF 预算仍会被 Tencent SDK、CloudNode tRPC、Admin BFF 和 Gateway 的 360 秒 transport timeout 截断；已修复为 provider、CloudNode、Admin BFF、Gateway route 统一 960 秒，并已在 106 正式主机更新相关 Linux binary、配置和 route，回读 route timeout 为 960000。Reporter 重叠 tick 已改为显式 ErrInFlight，Monitor 保留上一轮 readiness，不把跳过误判为新鲜成功。第四次 publish 使用重新编译 CLI，artifact 为 artifacts/kline-freshness/20260909144844-publish.json；最终 canary、durable row 和 Monitor latest 回读完成前仍保持 NO-GO。

## 1. 已确认的约束和验收口径

1. **删除范围是代码删除，不是增加默认关闭开关。** 删除 `gapAudit*` 定时调度、Storage latest/range read 适配器、环境变量门禁、相关测试和运行文档。不能保留 `MOOX_COLLECTOR_GAP_AUDIT_DISABLED` 作为死配置。
2. **保留范围明确。** 不删除 `domain.HistoryPolicy`、`BatchKindBackfill`、`BatchKindGapRepair`、KlinePipeline 对这些 batch kind 的解析、显式 Backfill/GapRepair 入口、period readiness 和现有 retry/cleanup。
3. **指标值语义不可混用。** View output `last_data_time` 表示 active index 成功写入的业务 `data_time` 最大值。Monitor 只用该 output `data_time` 判定 K 线 freshness，不把服务墙钟时间或其他模块水位误当成业务数据新鲜度。
4. **指标标签统一。** `subject_id` 使用 Storage canonical subject，不使用 Sina/Tencent/EastMoney provider symbol；空 `series_tag` 统一为 `default`。通用 View 指标同时携带 `view_id` 和 `dataset_id`，不在 Storage 层通过名称判断是否为 K 线。
5. **告警不做逐分钟桶审计。** 只看每个已观测 series 的最新业务时间。单个内部桶短暂缺失只要最新时间在 freshness 窗口内就算健康；长时间没有新 K 线才失败。
6. **告警基数受控。** 指标可以按标的上报，但告警状态按 `space_id + view_id + freq` 聚合，每组一个 Monitor check/state；告警正文最多包含 20 个按 subject 排序的 stale subject，另带 `stale_count`，不创建 5000 个 SQLite check。
7. **交易时段。** crypto 规则全天生效；stockcn 规则只在 `cn_stock` 交易日且 `09:30-11:30` 或 `13:00-15:00` 生效，午休、收盘、周末、节假日不判 stale。日历超出覆盖范围时输出 `calendar_unknown` 并跳过该轮 K 线告警，不伪造健康或失败。
8. **timer 口径。** K 线 View freshness 继续由 Monitor 现有 30 秒 watchdog/metrics timer 驱动，不新增外部 Prometheus 抓取；这不表示所有告警策略都固定 30 秒，也不把 `/metrics` 当生产告警源。
9. **生产验收是真实验收。** 本地单测、临时 SQLite、mock EventBus、SCF package build 都不能替代真实 Tencent Cloud publish、CloudNode/SCF readback、SCF -> Primary durable row、View durable row、Monitor latest 和告警状态。没有控制面/腾讯云/106 SSH 凭证时，结论只能是“未完成生产验收”。
10. **目录与 K 线采集解耦。** 全市场 Instrument 目录每天由独立 SCF 完整刷新；Kline Timer 不因每周期目录 API 失败而清空或阻塞，优先消费最近一次成功的 `ActiveInstrumentSet`，必要时使用控制面 `/data/subjects` 最近成功集合。目录快照允许多 Provider 并行合并，但必须以完整性校验通过的结果替换旧版本；单标的/单 Provider 失败只能触发有界 fallback 或该标的失败，不得阻塞其他标的。

## 2. 文件责任分解

**删除自动 gap audit：**

- Modify: `modules/collector/internal/marketfetch/scheduler.go`：删除周期审计常量、状态、环境变量门禁、审计计划类型、Storage latest/range 查询、缺口扫描和自动 GapRepair 计划；保留普通 realtime planning、显式 batch 合同和 cleanup。
- Modify: `modules/collector/internal/marketfetch/contracts.go`：从 Collector 私有 `StorageReader` 移除只被自动审计使用的 `LatestTimeSeriesTime` 与 `ReadTimeSeriesRows`。
- Modify: `modules/collector/internal/marketstorage/storage.go`：删除 Collector 专用的 latest/range read 封装；保留 WriteTimeSeriesRows 和 Storage writer 所需公共读写依赖。
- Modify: `modules/collector/internal/marketfetch/scheduler_test.go`：删除 `gapAudit*` 测试、range reader stub 和环境变量测试；保留或迁移 HistoryPolicy/BatchKind 的纯领域测试。
- Modify: `modules/collector/internal/bootstrap/bootstrap.go`：删除“慢 Storage gap audit 不阻塞协调循环”的过时说明；保留仍适用于普通 Storage/assignment 调用的 timeout 说明。
- Modify: `config/setup/service-deployments.yaml`：仅在确认该 `ReadTimeSeriesRows` entry 是 Collector 私有路由且没有 Monitor/Factor/Archive/CLI 依赖时删除；如果它仍是公共 Gateway ACL，保留，不把公共 Storage RPC 误删。
- Modify: `docs/superpowers/plans/2026-08-29-stock-cn-1m-multi-provider-scf.md`、`docs/validation/stock-cn-1m-canary.md`、`docs/operations/monitoring.md`：删除“自动 gap audit 会周期修复缺口”的现行承诺，改为“freshness 告警 + 显式人工/作业 Backfill/GapRepair”。

**Storage 通用 View 观测：**

- Delete: `modules/storage/internal/observability/kline_metrics.go`、`modules/storage/internal/observability/kline_metrics_test.go`：删除 Kline 专用类型、指标、dataset/view 名称判断和测试。
- Modify: `modules/storage/internal/observability/view_metrics.go`：增加通用 View dataset input/output watermark API，指标名不包含 `kline`；保留现有低基数 View 运行指标。
- Modify: `modules/storage/internal/service/view/event_apply.go`：事件完成 View 路由后记录 input；仅 active index 的实际成功写入记录 output；rebuild/next index、零写入和失败不推进 output。
- Modify: `modules/storage/internal/service/view/service.go`、`modules/storage/cmd/server/main.go`：移除 KlineMetrics 注入，改为通用 View observer wiring。
- Modify: `modules/storage/internal/service/view/*_test.go`、`modules/storage/internal/observability/view_metrics_test.go`：验证任意时序 dataset 的 input/output 观测、active index 边界、subject/frequency/series_tag、业务时间和 commit 时间。
- Modify: `modules/storage/config/storage_view/trpc_go.yaml`、`modules/storage/config/trpc_go.yaml`、`modules/storage/config/trpc_go.primary.yaml`、`modules/storage/config/trpc_go.node.yaml`：保持 metrics reporter 每 30 秒触发，但将 timer timeout 设为 60 秒，覆盖大 View 快照的采集、编码和发布耗时；不得把该通用性能参数实现为 K 线特例。
- Keep: `modules/storage/internal/service/primarystore/service.go` 的既有通用 DatasetMetrics；不得新增或保留 Primary Kline 专用分支。

**Monitor freshness 和告警：**

- Modify: `modules/monitor/internal/metrics/message_store.go`、`modules/monitor/internal/metrics/query.go`：增加带 metric-name 白名单、上限和稳定排序的 latest 批量读取，不允许无界 SQL 扫描。
- Modify: `modules/monitor/internal/metrics/storage.go`、`modules/monitor/internal/metrics/query.go`、`modules/monitor/internal/metrics/kline_freshness.go`：从 Metadata `ListDatasetSubjects` 获取 active canonical subject catalog，分页、短缓存并设置容量上限；只在 Monitor 过滤退市/归档 subject，catalog 查询失败时该 freshness group 明确失败。
- Create: `modules/monitor/internal/metrics/kline_freshness.go`：实现标签解析、按规则过滤、市场时段判断、分组 stale 计算、诊断摘要和 no-data 行为。
- Create: `modules/monitor/internal/metrics/kline_freshness_test.go`：覆盖 transient hole、持续 stale、View input/output 隔离、crypto/stockcn session、calendar unknown、限长 subject 列表、commit/data 时间区分。
- Modify: `modules/monitor/internal/config/config.go`、`modules/monitor/internal/config/config_test.go`、`modules/monitor/config/app.yaml`：增加 `kline_freshness` 配置和严格校验。
- Modify: `modules/monitor/internal/bootstrap/business_freshness.go`、`modules/monitor/internal/bootstrap/bootstrap.go`：把 KlineFreshness report 合并到现有 business freshness，不创建第二个 timer；只在有已观测 series 时创建稳定 group check。
- Modify: `modules/monitor/internal/bootstrap/default_alerts.go`、`modules/monitor/internal/bootstrap/default_alerts_test.go`：为 `kline_freshness:` check 使用 2 次失败/2 次成功的默认去抖，复用现有 alert state、notification、reminder、resolved 链路。
- Modify: `modules/monitor/internal/metrics/message_store.go`、`modules/monitor/internal/metrics/message_store_test.go`：把通用 View input/output freshness family 识别为 monotonic，拒绝旧 snapshot 覆盖新业务/commit 时间。
- Modify: `modules/monitor/schema/schema_test.go`：保持已删除 metric-rule 表断言，不重新引入没有使用者的通用 PromQL 规则表。

**文档、测试和发布：**

- Modify: `docs/运维/MooX指标监控.md`、`docs/operations/monitoring.md`：记录指标名、标签、30 秒链路、告警聚合、交易时段、无内部桶扫描和故障定位命令。
- Modify: `docs/采集任务管理.md`、`docs/validation/stock-cn-1m-canary.md`：记录独立 daily Instrument SCF、Kline Timer SCF、显式历史任务与正式验收证据。
- Create: `scripts/test/e2e/verify-kline-freshness-e2e.sh`：本地服务级闭环，使用临时 EventBus/SQLite，不使用生产凭证。
- Modify: `scripts/test/e2e/verify-observability-e2e.sh`：纳入 Kline freshness case 和 reporter payload 断言。
- Release artifacts: `artifacts/kline-freshness/` 仅保存脱敏 publish/status/readback JSON，不保存 token、SecretKey、Webhook 或签名 URL。

## 3. 分阶段执行任务

### Task 1: 建立基线并锁定删除边界

**Files:**
- Test/inspect: `modules/collector/internal/marketfetch/scheduler.go`
- Test/inspect: `modules/collector/internal/marketfetch/contracts.go`
- Test/inspect: `modules/collector/internal/marketstorage/storage.go`
- Test/inspect: `modules/collector/internal/store/task_rule.go`
- Test/inspect: `modules/collector/internal/marketfetch/period_readiness.go`

- [x] **Step 1: 保存当前状态，不修改业务文件。**

```bash
git status --short
git rev-parse --abbrev-ref HEAD
git rev-parse HEAD
rg -n "gapAudit|GapAudit|MOOX_COLLECTOR_GAP_AUDIT_DISABLED|LatestTimeSeriesTime|ReadTimeSeriesRows" \
  modules/collector docs config scripts
```

Expected: 当前工作树的未提交修改被记录；分支和 commit 可回溯；输出中明确区分 Collector 私有调用和其他模块的公共 `ReadTimeSeriesRows` 使用。

- [x] **Step 2: 为删除后的接口写静态边界检查。** Collector contract/E2E 已断言自动 gap audit 不存在，且 HistoryPolicy、Backfill、GapRepair 合同保留。

```bash
rg -n "lastGapAudit|gapAuditCursorID|func \(s \*Scheduler\) auditGaps|MOOX_COLLECTOR_GAP_AUDIT_DISABLED" modules/collector
rg -n "HistoryPolicy|BatchKindBackfill|BatchKindGapRepair" modules/collector/internal/domain modules/collector/internal/marketfetch
```

Expected before implementation: 第一条仍能找到旧代码，第二条能找到保留合同。该差异用于确认测试确实能检测目标删除，不把 HistoryPolicy 一并删掉。

### Task 2: 删除自动 gap audit 和 Collector 专用 Storage 读路径

**Files:**
- Modify: `modules/collector/internal/marketfetch/scheduler.go`
- Modify: `modules/collector/internal/marketfetch/contracts.go`
- Modify: `modules/collector/internal/marketstorage/storage.go`
- Modify: `modules/collector/internal/marketfetch/scheduler_test.go`
- Modify: `modules/collector/internal/bootstrap/bootstrap.go`
- Modify: `config/setup/service-deployments.yaml` only if the call-site audit proves it is Collector-private

- [x] **Step 1: 删除调度状态和审计调用。** 已从 Scheduler/RunOnce 删除自动 gap audit，保留 normal realtime plan、assignment refresh、retry cleanup。

- [x] **Step 2: 删除审计实现及其仅被审计使用的辅助函数。** 自动 gap audit 实现及专用辅助函数已删除；HistoryPolicy domain validation 和 Pipeline batch handling 保留。

- [x] **Step 3: 收缩 Collector 私有 StorageReader。** Collector 私有 latest/range 读路径已删除，公共 Storage RPC 未删除。

- [x] **Step 4: 清理测试而不是把删除的函数换成空实现。** 删除自动审计测试并保留 Backfill/GapRepair/HistoryPolicy 测试。

- [x] **Step 5: 删除过时运行注释和 active 文档承诺。** 运行文档已改为 Monitor 只告警，历史补采必须显式触发。

- [x] **Step 6: 运行删除阶段测试和全仓搜索。**

```bash
gofmt -w modules/collector/internal/marketfetch/scheduler.go \
  modules/collector/internal/marketfetch/contracts.go \
  modules/collector/internal/marketstorage/storage.go \
  modules/collector/internal/bootstrap/bootstrap.go
go test -count=1 ./modules/collector/internal/domain/... \
  ./modules/collector/internal/marketfetch/... \
  ./modules/collector/internal/marketstorage/...
rg -n "gapAudit|GapAudit|MOOX_COLLECTOR_GAP_AUDIT_DISABLED" modules/collector
```

Expected: Collector tests PASS；最后的 `rg` 无输出；显式 Backfill/GapRepair tests PASS；Scheduler 不再建立 Storage range-read 请求。

### Task 3: 建立通用 View freshness observation contract

**Files:**
- Delete: `modules/storage/internal/observability/kline_metrics.go`
- Delete: `modules/storage/internal/observability/kline_metrics_test.go`
- Modify: `modules/storage/internal/observability/view_metrics.go`
- Modify: `modules/storage/internal/service/view/event_apply.go`
- Modify: `modules/storage/internal/service/view/service.go`
- Modify: `modules/storage/cmd/server/main.go`
- Modify: `modules/storage/internal/observability/view_metrics_test.go`
- Modify: `modules/storage/internal/service/view/*_test.go`
- Modify: `modules/monitor/internal/metrics/message_store.go`
- Modify: `modules/monitor/internal/metrics/message_store_test.go`

- [x] **Step 1: 先写通用指标 contract 测试。** 测试固定的不含 Kline 语义的 View output metric name：

```text
moox_storage_view_dataset_output_last_data_time_seconds{space_id,view_id,dataset_id,subject_id,freq,series_tag}
```

测试断言：空 `series_tag` 变为 `default`；空 subject/frequency/dataset/view 返回结构化 error 并跳过该 series；较旧 `data_time` 不覆盖较新值；一个 subject 的更新不改变另一个 subject。

- [x] **Step 2: 实现通用 View observer。** `ViewMetrics` 已提供不识别业务语义的 output data-time observe API，并保留低基数 View 运行指标。

- [x] **Step 3: 定义上报边界。** 已覆盖 active index 成功写入后的 output data-time、partial write watermark 和 replacement failure；不再维护无告警消费者的 input/commit 逐标指标。

- [x] **Step 4: 运行共享 contract 测试。**

```bash
go test -count=1 ./modules/storage/internal/observability \
  ./modules/storage/internal/service/view
```

Expected: PASS；Storage 测试中不存在 Kline 名称判断或 Kline 专用 API。

### Task 4: 删除 Storage Primary 的 Kline 语义耦合

**Files:**
- Modify: `modules/storage/internal/service/primarystore/service.go`
- Modify: `modules/storage/cmd/server/main.go`
- Modify: `modules/storage/internal/service/primarystore/service_test.go`
- Delete: `modules/storage/internal/observability/kline_metrics.go` if Task 3 has not deleted it
- Delete: `modules/storage/internal/observability/kline_metrics_test.go` if Task 3 has not deleted it

- [x] **Step 1: 删除 Primary 专用注入和分支。** Primary 已移除 Kline monitor wiring、`KlineMetrics` 和 `IsKlineDatasetID` 特殊判断。

- [x] **Step 2: 保留已有通用 DatasetMetrics。** 通用 DatasetMetrics 保留，未新增 Kline 专用指标。

- [x] **Step 3: 更新测试和静态边界检查。** Primary 静态边界检查和回归测试已通过。

- [x] **Step 4: 运行 Primary 回归。**

```bash
go test -count=1 ./modules/storage/internal/service/primarystore \
  ./modules/storage/internal/observability ./modules/storage/cmd/server
rg -n "KlineMetrics|IsKlineDatasetID|storage_kline_" \
  modules/storage/internal/service/primarystore modules/storage/cmd/server
```

Expected: Primary 通用 DatasetMetrics 测试 PASS；最后的 `rg` 无输出；不因删除 Kline 观测改变真实 DataNode 写入行为。

### Task 5: 验证 active View 通用 input/output 观测

**Files:**
- Modify: `modules/storage/internal/observability/view_metrics.go:18-120,230-237,350-404`
- Modify: `modules/storage/internal/service/view/service.go:27-64,134-171,296-307`
- Modify: `modules/storage/internal/service/view/event_apply.go:243-378`
- Modify: `modules/storage/cmd/server/main.go:323-428`
- Modify: `modules/storage/internal/observability/view_metrics_test.go`
- Modify: `modules/storage/internal/service/view/*_test.go`

- [x] **Step 1: 写 active-index 边界测试。** 已覆盖 input、active output/commit、replacement、partial write 和旧周期 watermark 不回退。

- [x] **Step 2: 验证任意 dataset/view 均可观测。** 已用非 Kline dataset/view ID 验证通用观测契约。

- [x] **Step 3: 从实际 rows 提取 canonical dimensions。** 已按真实 view/dataset/canonical subject/frequency/series_tag 提取并对非法 series 隔离。

- [x] **Step 4: 验证 View 阶段。**

```bash
gofmt -w modules/storage/internal/observability/view_metrics.go \
  modules/storage/internal/service/view/service.go \
  modules/storage/internal/service/view/event_apply.go \
  modules/storage/cmd/server/main.go
go test -count=1 ./modules/storage/internal/observability \
  ./modules/storage/internal/service/view
```

Expected: input/output metrics 可读；只有 active View 成功输出才推进 output；重建中、零写入和失败不会制造“新鲜”假象。

### Task 6: 增加 Monitor latest 批量读取和 KlineFreshness evaluator

**Files:**
- Modify: `modules/monitor/internal/metrics/message_store.go`
- Modify: `modules/monitor/internal/metrics/query.go`
- Create: `modules/monitor/internal/metrics/kline_freshness.go`
- Create: `modules/monitor/internal/metrics/kline_freshness_test.go`
- Modify: `modules/monitor/go.mod` and `modules/monitor/go.sum` only if `packages/marketcalendar` is not already available to this module

- [x] **Step 1: 写 latest 读取测试。** 已覆盖 freshness family 白名单、稳定排序、有界读取和超限错误。

- [x] **Step 2: 增加有界 API。** `MetricMessageStore` 已增加有界 latest 读取 API：

```go
func (r *MetricMessageStore) ListLatestByMetricNames(
    ctx context.Context,
    names []string,
    limit int,
) ([]MetricLatest, error)
```

只接受固定的通用 View freshness metric name；SQL 使用 `WHERE c_metric_name IN (...)`、稳定排序和明确 limit。`QueryService` 透传该有界能力，供 bootstrap 使用，不能暴露任意 SQL 或任意无界 metric scan。Monitor 再按配置的 `view_id` 和 `dataset_id` 过滤，不要求 Storage 判断 Kline。

- [x] **Step 3: 写 evaluator 的业务时间测试。** 已覆盖固定 now 下的 stale、recovery、no_observation 和异常值策略：

  - 10:00 有数据、10:01 没有、10:02 有数据：`data_time_age <= stale_after`，返回 healthy，不产生 stale subject。
  - 一个 subject 的 View output `data_time` 超过阈值：只让对应 view/dataset/freq group 失败，diagnostic 带 `stale_count` 和该 subject。
  - View input 仍推进但 output 停止时，告警原因应指出 View 写入链路延迟；input 和 commit 只能作为诊断，不能代替 output 业务时间。
  - 服务墙钟时间新鲜但业务 `data_time` 旧：失败原因是 `business_data_stale`；不能把墙钟时间当成新 K 线。
  - 没有任何指定 View output sample：enabled rule 返回 `no_observation` 失败，不能静默跳过；否则 View route、consumer 绑定或首次写入失败无法进入 Kline 告警链路。交易时段外仍由 session/calendar gate 跳过。

- [x] **Step 4: 实现市场规则和 session gate。** crypto 全天评估，stockcn 使用 `cn_stock` session/calendar，非交易时段和 calendar unknown 均不伪造失败。

- [x] **Step 5: 实现有界 stale 聚合。** 已按 group 聚合、只用 output data_time 判定，subject 诊断最多 20 个且按 canonical subject 排序。

- [x] **Step 6: 实现异常策略。** malformed/future/unknown series 隔离，enabled group 无 output 返回 `no_observation`，市场关闭/calendar unknown 跳过本轮。
- [x] **Step 7: 实现 active subject 生命周期过滤。** Monitor 通过 Storage Metadata `ListDatasetSubjects` 分页读取 active canonical subject，成功目录短缓存 1 分钟、上限 100000 个 subject；归档/停用/空 subject 不参与 freshness，metadata 不可用返回 `subject_catalog_unavailable` 失败，不把旧 series 当成健康或持续 stale。

- [x] **Step 8: 运行 evaluator 单测。**

```bash
go test -count=1 ./modules/monitor/internal/metrics -run 'KlineFreshness|ListLatestByMetricNames'
```

Expected: transient hole PASS；stale/recovery、View input/output 隔离、stock calendar、subject 限长和 retired subject 过滤 PASS；无 Storage adapter 的纯 evaluator 单测使用显式 fixture，带 Storage adapter 的测试覆盖 Metadata catalog 合同。

### Task 6.5: 修复通用 metrics reporter 的采集超时

**Files:**
- Modify: `modules/storage/config/storage_view/trpc_go.yaml`
- Modify: `modules/storage/config/trpc_go.yaml`
- Modify: `modules/storage/config/trpc_go.primary.yaml`
- Modify: `modules/storage/config/trpc_go.node.yaml`
- Modify: `packages/report/handler.go`
- Modify: `packages/report/handler_test.go`

- [x] **Step 1: 以真实快照大小定位超时边界。** 已确认完整 Storage View metrics 响应约 517 KB，完整采集耗时超过原 10 秒；EventBus broker、consumer 和小消息 publisher 探针正常，错误发生在 reporter 的过期 timer context。
- [x] **Step 2: 调整通用 timer timeout 并避免重入排队。** 四种 Storage 进程统一使用 `timeout: 60000`，network 仍为 `*/30 * * * * *`；report Handler 在已有报告执行时跳过后续 tick，避免慢 Gather/Publish 在 cron goroutine 中无界排队；不得通过关闭 reporter、降低 labels 或增加 K 线专用旁路规避问题。
- [x] **Step 3: 编译、部署并观察一个新时间窗口。** 已在正式 Storage 主机完成当前 release 的原子二进制/配置更新并重启三角色；View/Primary/Node readiness 均已通过。2026-09-09 11:22-11:23 的 Monitor `t_monitor_metric_ingest_messages` 连续收到 `storage-view@storage` snapshot，`t_monitor_metric_services` 更新到 `03:23:10Z`，且 `t_monitor_metric_latest` 已出现 View output data-time 和 `moox_storage_view_output_watermark_timestamp_seconds`；View `/metrics` 的 `moox_storage_report_errors_total=0`。旧的 11:18-11:20 timeout 不计入新窗口。

### Task 7: 把 Kline freshness 接入现有 Monitor business freshness/alerting

**Files:**
- Modify: `modules/monitor/internal/config/config.go`
- Modify: `modules/monitor/internal/config/config_test.go`
- Modify: `modules/monitor/config/app.yaml`
- Modify: `modules/monitor/internal/bootstrap/business_freshness.go`
- Modify: `modules/monitor/internal/bootstrap/bootstrap.go`
- Modify: `modules/monitor/internal/bootstrap/default_alerts.go`
- Modify: `modules/monitor/internal/bootstrap/default_alerts_test.go`
- Modify: `modules/monitor/internal/bootstrap/business_freshness_test.go`

- [x] **Step 1: 增加严格 YAML 配置。** 使用唯一配置契约并拒绝未知字段：

```yaml
kline_freshness:
  enabled: true
  evaluation_interval: 30s
  max_subjects_per_alert: 20
  rules:
    - enabled: true
      space_id: crypto
      view_id: view_crypto_spot_kline_1m
      dataset_id: dataset_binance_spot_kline_1m
      frequency: 1m
      market_id: crypto
      stale_after: 5m
    - enabled: true
      space_id: stockcn
      view_id: view_stockcn_equity_kline_1m
      dataset_id: dataset_stockcn_equity_kline
      frequency: 1m
      market_id: stockcn
      calendar_id: cn_stock
      timezone: Asia/Shanghai
      sessions: ["09:30-11:30", "13:00-15:00"]
      stale_after: 10m
```

规则必须指定唯一的 `dataset_id + view_id`；dataset 是 active subject catalog 的生命周期边界，不允许省略；不再存在 `scope` 字段，也禁止配置 Primary 规则。stockcn 必须有 calendar/session/timezone；crypto 不允许配置 stock session。校验 `evaluation_interval >= 30s`，`stale_after >= 2 * evaluation_interval`，`max_subjects_per_alert` 在 1 到 100 之间，规则 key 唯一；运行时按该 interval 设置 Kline check 的调度周期，默认仍为 30s。

- [x] **Step 2: 写配置测试。** 已覆盖缺字段、唯一性、stock session、calendar、时长、subject limit、废弃字段和拼写错误。

- [x] **Step 3: 注入现有 watchdog，而不是创建第二个 timer。** Kline evaluator 已接入现有 watchdog/metrics timer、repository 和 alerting evaluator。

- [x] **Step 4: 定义稳定 check identity。** Kline check ID 已固定为：

```text
kline_freshness:<space_id>:<view_id>:<freq>
```

Name 使用中文 `K线 View 新鲜度 <space_id> <view_id> <freq>`；SpaceID 使用配置 space。一个 View/frequency group 永远只有一个 check/state，不使用 subject 拼接 check ID。

- [x] **Step 5: 为 Kline check 设置默认告警去抖。** Kline freshness 已使用独立去抖和 bounded diagnostic，其他 check 阈值不改。

- [x] **Step 6: 对无观测/市场关闭进行稳定处理。** 已实现 market close/calendar unknown 跳过、active session `no_observation`、disabled rule `no_longer_expected` 收敛。

- [x] **Step 7: 验证 alert state。** 本地 alert state 测试已通过；正式闭环留在 Task 9。

```bash
go test -count=1 ./modules/monitor/internal/bootstrap \
  ./modules/monitor/internal/alerting \
  ./modules/monitor/internal/metrics
```

Expected: 两个连续 stale 周期才触发；两个连续健康周期 resolved；transient one-minute hole 不触发；通知失败时沿用已有重试/reminder 语义；business freshness check 总数不会按标的线性增长。

### Task 8: 更新运维文档、Schema/Contract 检查和回归用例

**Files:**
- Modify: `docs/运维/MooX指标监控.md`
- Modify: `docs/operations/monitoring.md`
- Modify: `docs/采集任务管理.md`
- Modify: `docs/validation/stock-cn-1m-canary.md`
- Modify: `docs/superpowers/plans/2026-08-29-stock-cn-1m-multi-provider-scf.md`
- Create: `scripts/test/e2e/verify-kline-freshness-e2e.sh`
- Modify: `scripts/test/e2e/verify-observability-e2e.sh`
- Modify: `modules/monitor/schema/schema_test.go` only to assert no retired metric-rule tables are reintroduced

- [x] **Step 1: 文档化生产指标契约。** 已写出一个通用 View output metric family、六个 identity labels、业务 `data_time`、EventBus pipeline、group 告警、subject 上限、session gate 和 Storage/Monitor 责任边界。

- [x] **Step 2: 文档化故障判断。** 已加入 Collector -> Primary -> View -> Reporter/EventBus -> Monitor 的诊断顺序：

```text
1. View output last_data_time 是否推进
2. Monitor MetricLatest 是否收到指定 View 的 output family
3. EventBus pending/redelivery、Reporter error、Storage/View 写入错误
4. 指定 View 的 Kline freshness alert state 是否 firing/resolved
```

禁止把“服务 `/metrics` HTTP 可访问”当作 Monitor 已收到指标，也禁止把服务墙钟时间或其他水位误判为 K 线业务时间新鲜。

- [x] **Step 3: 更新 stockcn 发布文档。** 已明确独立 daily Instrument Timer、定时 Kline 增量、多 Provider fallback、显式 Backfill/GapRepair 和 egress 仅诊断。

- [x] **Step 4: 编写本地端到端脚本。** `verify-kline-freshness-e2e.sh` 已使用临时目录/SQLite/EventBus 验证 Collector/Storage/Monitor 代码路径；它不冒充真实 Storage View -> Reporter -> Monitor -> notification 生产闭环。

  - active View apply 成功后产生 output；非 active/rebuild/失败不产生 output 推进；active A 成功但 replacement B 失败时仍保留 A 的 output watermark。
  - Reporter snapshot 通过 EventBus ingest 到 Monitor latest，labels JSON 保持 canonical。
  - 10:00/10:01/10:02 transient hole 不触发 alert；连续 stale 触发后连续恢复 resolved。
  - stockcn 午休、周末和 holiday 不触发 stale；crypto 同样 elapsed 会触发。
  - Binance/crypto 既有 metric、Kline pipeline、View output watermark 和 canary tests 继续通过。

- [x] **Step 5: 运行回归。** 本地 focused race tests 和 `scripts/test/e2e/verify-kline-freshness-e2e.sh` 已通过；`make release` 已通过，`make verify` 仍被既有 tradeeventpb deprecated protocol contract 阻断。

```bash
bash scripts/test/e2e/verify-kline-freshness-e2e.sh
bash scripts/test/e2e/verify-observability-e2e.sh
go test -count=1 ./modules/collector/... \
  ./modules/storage/... \
  ./modules/monitor/... \
  ./packages/report/... \
  ./packages/marketcalendar/...
```

Expected: 新增 Kline case PASS；crypto 回归 PASS；Monitor retired metric-rule schema 断言 PASS。Monitor E2E 还必须证明 reporter-shaped snapshot 经 JetStream 写入 latest 并被 Kline evaluator 读取；当前仍未覆盖真实 Storage View 进程到 Reporter 的生产链路和 alert state 通知闭环，不能把临时测试写成生产验收。如果环境缺少 Docker/NATS/腾讯 SDK，只记录明确环境阻塞，不把 skip 当 PASS。

### Task 9: 正式编译、发布到 106 并做真实 SCF/Storage/View/Monitor 验收

**Files/Artifacts:**
- Use: `moox.toml`（只读输入，不打印、不提交、不修改；如另有本地部署配置，必须先复制为受支持的文件名并保持 0600，不能把 `custom.toml` 直接传给当前 Loader）
- Use: `bin/moox-cli`
- Use: `scripts/deploy/deploy-moox.sh`
- Create locally: `artifacts/kline-freshness/<timestamp>-publish.json`
- Create locally: `artifacts/kline-freshness/<timestamp>-readback.json`

- [x] **Step 1: 生成可追溯正式构建。** `make release` 已通过；`make verify` 已执行，但被既有 `packages/tradeeventpb/trade_events.proto` 的 deprecated protocol declarations contract 检查阻断，该失败不由本任务改动引入。Linux Storage binaries 在 compile host 构建并记录 provenance；最终 Storage 验收已通过。

```bash
git status --short
git rev-parse HEAD
make verify
make release
sha256sum bin/moox-cli
```

Expected: working tree 状态、commit、release archive、CLI SHA 记录到脱敏 artifact；构建日志不包含 `moox.toml` 内容或任何 secret。

- [x] **Step 2: 发布 106 主机服务包。** 控制面仍可连接；Storage 标准 SFTP 上传在大包传输阶段中断，随后使用同一 release archive 通过 rsync 续传并在 `146.56.196.204` 对三角色二进制/三份配置执行原子替换和重启，未删除 Storage Primary、View active index、Monitor SQLite 或 EventBus JetStream。三角色二进制 SHA256 均为 `34ae37c6161be9fbd352f979aabe6623499ba94cc3baf73ef18e1d2554e63be5`，View/Primary/Node readiness 已读回；不能表述为 CLI `setup deploy-storage` 全流程无中断通过。

```bash
./scripts/deploy/deploy-moox.sh --help
./bin/moox-cli setup validate --file ./moox.toml
./bin/moox-cli setup status --file ./moox.toml
```

若需要更新控制面/Storage/Monitor，使用仓库既有 `setup deploy-control`、`setup deploy-storage`、`setup deploy-service` 流程；每一步保存服务 health、监听端口、进程 SHA 和 rollback archive。服务未 ready 时停止后续 SCF 发布，不以“进程已启动”作为验收。

- [x] **Step 3: 先停历史和 crypto 负载，再发布 stockcn。** 控制面已停用 `crypto` 的 5 条 Binance Kline/Instrument 规则，以及上次中止残留的 `stockcn` Kline 规则；Storage 主机已将 `MOOX_STORAGE_VIEW_MAINTENANCE_DISABLED=1` 持久化到 `config/runtime.env`，重启 `storage-view` 后在 07:42:10 日志确认 historical maintenance disabled。该动作只停止运行负载，不删除历史数据；重启后的 NATS rebind/disconnect 仍须在下一轮发布前复核。stockcn 规则只有在后续 publish 的全部门禁通过后才允许重新启用。

- [ ] **Step 4: 用真实控制面提交 stockcn SCF publish。** 已进行三次真实提交：首次 59 分 48 秒无 summary 后中止；第二次于 11:28:51 更新了 170 个 Kline Timer 和独立 Instrument Timer，约 36 分钟无 summary 后中止；第三次于 12:34:27 完成 170 个 Kline Timer、独立 Instrument Timer 和 18 个 CloudNode job 更新，但 13:13:40 的 Instrument canary 在有界阶段预算内返回 `context deadline exceeded`，所以 Rule/Timer 未启用。本步骤仍未通过。下一次执行前必须只读回 170 个 Kline Timer、独立 Instrument Timer、每个 job、Timer enabled/assignment 和 Rule 状态，先清理半启用状态并复核 Storage readiness，再以 900 秒 Instrument SCF 函数预算提交；最终仍须确认所有 job `SUCCESS`。Instrument daily Timer 与 Kline Timer 必须是两个独立 fleet，标的目录失败时 Kline 继续使用最近成功集合，不得在 Kline SCF 内重新拉全市场目录。

```bash
./bin/moox-cli collector function publish submit \
  --file ./moox.toml \
  --space-id stockcn \
  --control-url "$MOOX_CONTROL_URL" \
  --enable-stockcn \
  > "artifacts/kline-freshness/$(date +%Y%m%d%H%M%S)-publish.json"
```

命令返回后，用返回的每个 `job_id` 执行 status readback，直到每个 job 为 `SUCCESS`；不能只看 CLI submit 返回成功。确认返回包含：正式 package ID、每个 region 的 Timer 数量、独立 `instrument_snapshot_daily` fleet、每地域 1 个 invoke canary、Timer trigger enabled、Rule enabled、没有依赖公网 IP 数量相等门禁。

- [ ] **Step 5: 验证真实 SCF canary。** 未通过。第三次 publish 已在 7 分钟总阶段预算内暴露真实失败：Instrument SCF `InvokeFunction` 返回 `context deadline exceeded`，仍没有 `success=true`、真实 provider request、`rows_written > 0` 或 request_id。独立 provider probe 显示 Sina 57 页/5559 标的/三交易所完整快照约 30 秒，EastMoney 当前总量校验失败，Tencent InstrumentFetcher 尚未实现；下一步需要用 900 秒函数预算重新执行，并从 SCF/Storage 日志确认是 Provider 读取还是 4000+ subject metadata 写入超时；随后才可进行 Kline canary 和 durable-row 闭环。`probe-egress` 仍只能作为诊断。

```bash
./bin/moox-cli collector function probe-egress \
  --control-url "$MOOX_CONTROL_URL" \
  --space-id stockcn \
  --file ./moox.toml \
  --service-access-key "$MOOX_SERVICE_ACCESS_KEY" \
  --service-secret-key "$MOOX_SERVICE_SECRET_KEY" \
  > "artifacts/kline-freshness/$(date +%Y%m%d%H%M%S)-egress.json"
```

Acceptance requires canary response `success=true`、真实 provider request、`rows_written > 0` 和 request_id；Sina 单接口失败不能阻塞 Tencent/EastMoney fallback 仍能形成成功 Kline；仅 `egress` 返回非空 IP 不算 Kline 验收。

- [ ] **Step 6: 读回 Primary 和 View 的真实 durable row。** stockcn 仍未通过：Primary `600000.XSHG` 的最新行仍为 `2026-09-08T06:59:00Z`，`2026-09-09T00:00:00Z` 后无行；`view_stockcn_equity_kline_1m` 读回 HTTP 500。当前新增证据只证明 crypto View 的 Monitor watermark 上报已恢复，不等价于 stockcn publish/canary 或 SH、SZ、BSE 的 Primary 与 active View 相同 `data_time` 证据。

```bash
./bin/moox-cli data rows export \
  --dataset dataset_stockcn_equity_kline \
  --space stockcn \
  --subject 600000.XSHG \
  --freq 1m \
  --page-size 10 \
  --storage-url "$MOOX_STORAGE_URL" \
  --storage-auth-file "$MOOX_STORAGE_AUTH_FILE"

./bin/moox-cli data rows export \
  --dataset view_stockcn_equity_kline_1m \
  --space stockcn \
  --subject 600000.XSHG \
  --freq 1m \
  --page-size 10 \
  --storage-url "$MOOX_STORAGE_URL" \
  --storage-auth-file "$MOOX_STORAGE_AUTH_FILE"
```

实际字段名、Gateway route 和 Storage auth 按 `moox.toml` 既有配置解析；`data rows export` 的 selector range read 默认经过 active View，不能单独作为 Primary durable-row 证据。Primary 必须另外使用 Storage 内部 `storage-view` 身份调用 `ReadTimeSeriesRows` 的历史路由（或同等只读 DataNode history route），View 则使用 Access route 读取 active View；两份 request/response 都保存脱敏 JSON。Primary 和 View 都必须存在相同业务周期的 `data_time`，View 不得只返回旧 active index。

- [ ] **Step 7: 验证通用 View 指标和 Monitor latest。** 未通过正式窗口读回。Monitor SQLite 仍有 stockcn `dataset:collector:dataset_stockcn_equity_kline:1m` 为 `down/尚未上报`；没有本次 SCF 对应的 output metric family、canonical subject、`freq=1m`、`series_tag` 和 `observed_at` 证据。先部署并验证 60 秒通用 reporter timeout，再修复 SCF 与 View readback，最后复核 reporter/EventBus/Monitor 链路。K 线判断只能在 Monitor 完成，Storage/Primary/View 侧不得出现 Kline dataset 名称判断或专用告警分支。

```sql
SELECT c_metric_name, c_labels_json, c_value, c_observed_at
FROM t_monitor_metric_latest
WHERE c_metric_name IN (
  'moox_storage_view_dataset_output_last_data_time_seconds'
)
ORDER BY c_metric_name, c_series_id
LIMIT 40;
```

证据必须证明：`subject_id` 为 canonical subject、`freq=1m`、`series_tag=default` 或明确 series tag；指定 View 的 output data time 已推进；Monitor `observed_at` 与最近 reporter 周期相符；没有超过 configured sample/label limit 的 reporter error。Primary Kline 指标不属于验收项。

- [ ] **Step 8: 验证告警不误报且能恢复。** 本地 transient hole/recovery 已通过；生产只读结果显示 stockcn collector 仍为 `down/尚未上报`，只能证明现有告警能识别停更，不能证明 SCF 恢复后的 firing/resolved 闭环。必须在真实 View output watermark 推进后再次读回。

```sql
SELECT c_space_id, c_check_id, c_status, c_failure_count,
       c_success_count, c_triggered_at, c_resolved_at
FROM t_monitor_alert_states
WHERE c_check_id LIKE 'kline_freshness:%'
ORDER BY c_mtime DESC
LIMIT 40;
```

正式验收窗口内，stockcn active session 的 freshness check 不得因 10:00/10:01/10:02 这种短缺口 firing；如果真实 View/Primary 持续停更，必须能看到对应 group 的 firing/resolved 状态和 bounded subject diagnostic。

- [ ] **Step 9: 记录 crypto 回归和 rollback 条件。** 未完成。当前正式门禁仍为 `NO-GO`：stockcn SCF 无最终 summary/canary，Primary 未推进到当前交易日，View/Monitor 证据缺失；crypto 规则仍保持停用，生产 crypto 不能宣称回归通过。远端 collector 仍有旧 Binance marker 冲突告警，但这不改变“crypto 已停用”的回归口径。若后续任一正式门禁失败，必须关闭残留 stockcn Timer/规则、确认 Instrument Timer 状态，并恢复上一 package/service release，不能留下半启用状态。

## 4. 完成定义（Definition of Done）

- [ ] `modules/collector` 中不存在自动 gap audit 实现、状态、环境变量和死接口；显式 HistoryPolicy/Backfill/GapRepair contract 与 Pipeline 测试仍 PASS。
- [ ] Storage Primary 不包含任何 Kline 专用分支、类型、指标或名称判断；既有通用 DatasetMetrics 不回归。
- [ ] Storage View 只通过通用 output observation 上报；只有 active View 成功提交后推进 output `last_data_time`，失败/重建阶段不推进。
- [ ] Monitor 通过现有 30 秒 EventBus reporter 链路接收并持久化指定 View 的 output metric family；不依赖 HTTP `/metrics` 抓取。
- [ ] 告警按 group 聚合，subject 只出现在 bounded diagnostic；短暂内部缺口不 firing；连续 stale 可 firing，恢复可 resolved；stockcn 非交易时段不误报。
- [ ] 本地 Collector/Storage/Monitor/marketcalendar/crypto 回归和 observability E2E PASS，环境失败项单独标注。
- [ ] 106 主机服务包有可回滚 provenance，stockcn publish job 全部 SUCCESS，Instrument daily Timer 与 Kline Timer 分离且 readback 正确。
- [ ] Storage View/Primary/Node metrics timer 保持 30 秒调度且有足够的 60 秒单次执行预算；连续两个新周期成功上报，不能只证明 HTTP `/metrics` 可访问。
- [ ] 真实 SCF invoke canary 成功，真实 Primary durable row 和 `view_stockcn_equity_kline_1m` durable row 均读回，指定 View 的 Monitor SQLite latest/alert state 有独立证据。
- [ ] 没有真实控制面、Tencent Cloud、Storage 和 106 SSH 凭证时，最终状态必须写成“代码/本地验证完成，正式环境未验收”，不得用模拟结果替代。

## 5. 计划自审

- **需求覆盖：** 自动 gap audit 删除在 Task 1-2；通用 View input/output 观测和 Primary 解耦在 Task 3-5；30 秒现有链路、指定 View 的告警、session 和 transient hole 在 Task 6-7；文档和 crypto 回归在 Task 8；正式编译、SCF、Primary/View/Monitor E2E 在 Task 9。
- **一致性检查：** View output metric name 和 labels 在约束、Storage、Monitor、SQL readback 四处一致；只使用业务 `data_time` 判定 freshness；告警 check ID 不包含 subject，避免 5000 条 state；Monitor 通过 `view_id` 配置识别 Kline，不依赖 Storage 名称启发式。
- **范围检查：** 没有重新引入已删除的 `t_monitor_metric_rules*` 通用规则表；Storage Primary 不出现 `KlineMetrics`、`IsKlineDatasetID` 或 `storage_kline_`；没有删除公共 Storage `ReadTimeSeriesRows`，只删除 Collector 私有自动审计适配器（除非调用图证明该路由确实是 Collector-private）；没有把 crypto 重新启用作为隐含副作用。
- **禁止的空泛步骤：** 每个实现步骤都绑定了文件、符号、测试或命令；实现时不得以“适当处理”“后续补充”替代上述具体契约。

## 6. 执行交接

计划完成后，按任务顺序执行并在每个 Task 后保留 commit/测试证据。推荐使用 `superpowers:subagent-driven-development`，每个 Task 由独立 subagent 实施后由主 Agent 复核；也可以使用 `superpowers:executing-plans` 在当前工作树分批执行。正式发布只在本地测试和独立代码审查通过后进行，且发布结果必须以真实 SCF/Storage/View/Monitor readback 为准。
