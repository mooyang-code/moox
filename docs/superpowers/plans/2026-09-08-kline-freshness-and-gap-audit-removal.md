# K-line Freshness Metrics and Gap Audit Removal Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 删除 Collector 中自动周期 gap audit 及其 Storage 读放大路径；Storage 底层只提供通用的 View 数据进入/成功写入观测，不识别 K 线语义；由 Monitor 针对指定 View 判断 K 线业务时间是否持续停更并产生可恢复告警，最后完成正式编译、SCF 发布和 106 主机真实端到端验收。

> **执行状态（2026-09-09）：** Task 1-8 的代码、测试和文档已实现；新增 generic View input/output/commit 三类指标，Storage Primary 已移除 K 线监控耦合，Monitor 仅按指定 View output `data_time` 评估。最新 codeCR 未发现 P0；其发现的 output 部分写入误推进、诊断样本污染、跨实例覆盖、开盘 warmup、calendar 校验和 replacement input 语义已修复，并补充临时 JetStream/SQLite 本地链路测试。Task 9 的正式部署与真实 SCF/Storage/View/Monitor 验收仍为未完成门禁，未以本地测试替代。

**Architecture:** Storage View 在事件进入并完成路由后上报通用 input watermark，在 active View index 实际成功写入后上报通用 output `data_time` 和 `commit_timestamp`；Storage Primary 不创建、不判断、不维护任何 K 线专用指标。指标标签固定为 `space_id + view_id + dataset_id + subject_id + freq + series_tag`，不使用 provider symbol。服务自身的 tRPC metrics timer 每 30 秒把这些值通过现有 MetricSnapshot/EventBus/Monitor Consumer 写入 latest；Monitor 不抓 HTTP `/metrics`，而是在同一 watchdog 周期读取指定 View 的 output latest，按 `view_id + frequency` 聚合，只对持续超过阈值的标的组告警，input/output 差异仅用于诊断。10:00 有数据、10:01 缺失、10:02 恢复不会触发告警，也不会扫描内部桶缺口。历史 Backfill、显式 GapRepair、HistoryPolicy 和 K 线 Pipeline 合同保留，但 Scheduler 不再自动查询 Storage 触发历史补采。

**Tech Stack:** Go modules、Prometheus client_golang、tRPC timer、NATS JetStream EventBus、GORM/SQLite、`packages/marketcalendar`、YAML 配置、现有 `moox-cli collector function publish`、Storage ReadTimeSeriesRows/CLI 数据读回。

---

## 1. 已确认的约束和验收口径

1. **删除范围是代码删除，不是增加默认关闭开关。** 删除 `gapAudit*` 定时调度、Storage latest/range read 适配器、环境变量门禁、相关测试和运行文档。不能保留 `MOOX_COLLECTOR_GAP_AUDIT_DISABLED` 作为死配置。
2. **保留范围明确。** 不删除 `domain.HistoryPolicy`、`BatchKindBackfill`、`BatchKindGapRepair`、KlinePipeline 对这些 batch kind 的解析、显式 Backfill/GapRepair 入口、period readiness 和现有 retry/cleanup。
3. **指标值语义不可混用。** View input/output 的 `last_data_time` 都表示收到/写入的业务 `data_time` 最大值；output `last_commit_timestamp` 是 active View index 成功提交的 UTC 墙钟时间。Monitor 只用 output `data_time` 判定 K 线 freshness，input 和 commit 只用于区分“事件尚未到达 View”“已到达但未成功落 View”以及“落库停止”。
4. **指标标签统一。** `subject_id` 使用 Storage canonical subject，不使用 Sina/Tencent/EastMoney provider symbol；空 `series_tag` 统一为 `default`。通用 View 指标同时携带 `view_id` 和 `dataset_id`，不在 Storage 层通过名称判断是否为 K 线。
5. **告警不做逐分钟桶审计。** 只看每个已观测 series 的最新业务时间。单个内部桶短暂缺失只要最新时间在 freshness 窗口内就算健康；长时间没有新 K 线才失败。
6. **告警基数受控。** 指标可以按标的上报，但告警状态按 `space_id + view_id + freq` 聚合，每组一个 Monitor check/state；告警正文最多包含 20 个按 subject 排序的 stale subject，另带 `stale_count`，不创建 5000 个 SQLite check。
7. **交易时段。** crypto 规则全天生效；stockcn 规则只在 `cn_stock` 交易日且 `09:30-11:30` 或 `13:00-15:00` 生效，午休、收盘、周末、节假日不判 stale。日历超出覆盖范围时输出 `calendar_unknown` 并跳过该轮 K 线告警，不伪造健康或失败。
8. **timer 口径。** K 线 View freshness 继续由 Monitor 现有 30 秒 watchdog/metrics timer 驱动，不新增外部 Prometheus 抓取；这不表示所有告警策略都固定 30 秒，也不把 `/metrics` 当生产告警源。
9. **生产验收是真实验收。** 本地单测、临时 SQLite、mock EventBus、SCF package build 都不能替代真实 Tencent Cloud publish、CloudNode/SCF readback、SCF -> Primary durable row、View durable row、Monitor latest 和告警状态。没有控制面/腾讯云/106 SSH 凭证时，结论只能是“未完成生产验收”。

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
- Keep: `modules/storage/internal/service/primarystore/service.go` 的既有通用 DatasetMetrics；不得新增或保留 Primary Kline 专用分支。

**Monitor freshness 和告警：**

- Modify: `modules/monitor/internal/metrics/message_store.go`、`modules/monitor/internal/metrics/query.go`：增加带 metric-name 白名单、上限和稳定排序的 latest 批量读取，不允许无界 SQL 扫描。
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

- [ ] **Step 1: 保存当前状态，不修改业务文件。**

```bash
git status --short
git rev-parse --abbrev-ref HEAD
git rev-parse HEAD
rg -n "gapAudit|GapAudit|MOOX_COLLECTOR_GAP_AUDIT_DISABLED|LatestTimeSeriesTime|ReadTimeSeriesRows" \
  modules/collector docs config scripts
```

Expected: 当前工作树的未提交修改被记录；分支和 commit 可回溯；输出中明确区分 Collector 私有调用和其他模块的公共 `ReadTimeSeriesRows` 使用。

- [ ] **Step 2: 为删除后的接口写静态边界检查。** 将检查放入 Collector package test 或 shell contract，断言 `Scheduler` 不再包含 `lastGapAudit`、`gapAuditCursorID`、`auditGaps` 和环境变量名，同时断言 `HistoryPolicy`、`BatchKindBackfill`、`BatchKindGapRepair` 仍存在。

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

- [ ] **Step 1: 删除调度状态和审计调用。** 从 `Scheduler` 删除 `lastGapAudit`、`gapAuditCursorID`，从 `RunOnce` 删除按 5 分钟执行 `auditGaps` 的分支；删除 `gapAuditDisabled` 和 `os` import。保留 normal realtime plan、assignment refresh、retry cleanup。

- [ ] **Step 2: 删除审计实现及其仅被审计使用的辅助函数。** 删除 `gapAuditPlan`、`timeSeriesRangeReader`、`auditGaps`、`gapAuditFrequencyDuration`、`gapAuditCoverageStart`、`findEarliestMissingBucket`、`gapAuditExpectedBuckets`、`buildGapAuditPlan*`、`gapAuditSeriesTag`、`gapAuditThreshold`。如果 `historyPolicyStart` 或 `gapRepairFloor` 删除后没有非审计调用，也一并删除；不要删除 `HistoryPolicy` 的 domain validation 或 Pipeline batch handling。

- [ ] **Step 3: 收缩 Collector 私有 StorageReader。** 从 `modules/collector/internal/marketfetch/contracts.go` 的 `StorageReader` 删除 `LatestTimeSeriesTime` 与 `ReadTimeSeriesRows`；从 `marketstorage/storage.go` 删除对应实现和注释。用 `rg` 确认 Collector 不再通过 Storage 做周期 latest/range 查询；其他模块的公共 Storage RPC 不动。

- [ ] **Step 4: 清理测试而不是把删除的函数换成空实现。** 删除 `TestGapAuditDisabledFromEnvironment`、`TestGapAuditThreshold*`、`TestBuildGapAuditPlan*`、`TestGapAuditExpected*`、range reader stub 和同名辅助。保留 `contracts_test.go` 中 Backfill/GapRepair batch request contract、`collect_params_test.go` 的 HistoryPolicy validation、`kline_pipeline_test.go` 的显式 Backfill/GapRepair pipeline tests。

- [ ] **Step 5: 删除过时运行注释和 active 文档承诺。** 更新 Collector bootstrap 中关于慢 gap audit 的注释；将现行文档改为“Monitor freshness 只告警，历史补采必须由显式任务/人工触发”。历史验证记录可以保留事实，但不得再被运行文档描述为自动恢复机制。

- [ ] **Step 6: 运行删除阶段测试和全仓搜索。**

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

- [ ] **Step 1: 先写通用指标 contract 测试。** 测试固定三个不含 Kline 语义的 metric name：

```text
moox_storage_view_dataset_input_last_data_time_seconds{space_id,view_id,dataset_id,subject_id,freq,series_tag}
moox_storage_view_dataset_output_last_data_time_seconds{space_id,view_id,dataset_id,subject_id,freq,series_tag}
moox_storage_view_dataset_output_last_commit_timestamp_seconds{space_id,view_id,dataset_id,subject_id,freq,series_tag}
```

测试断言：空 `series_tag` 变为 `default`；空 subject/frequency/dataset/view 返回结构化 error 并跳过该 series；较旧 `data_time` 不覆盖较新值；一个 subject 的更新不改变另一个 subject；output `commit_timestamp` 使用 active View 成功提交时间而不是业务时间。

- [ ] **Step 2: 实现通用 View observer。** 在 `ViewMetrics` 或独立通用 observability helper 中提供 input/output observe API。API 不接收、不判断 `Kline` 类型，不调用 `IsKlineDatasetID`/`IsKlineViewID`，也不通过 dataset/view 名称推断业务语义。保留现有低基数 View 运行指标。

- [ ] **Step 3: 定义上报边界。** 事件完成 dataset route/filter 后、尝试写 index 前更新 input；只有 active index 的实际成功写入且产生完整写入时更新 output `data_time` 和 `commit_timestamp`。rebuild/next index、零写入、部分写入和失败不得推进 output。

- [ ] **Step 4: 运行共享 contract 测试。**

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

- [ ] **Step 1: 删除 Primary 专用注入和分支。** 移除 `KlineMetrics` service option、默认 registry wiring、`klineGroups` 聚合和 `observability.IsKlineDatasetID(...)` 条件。Primary 不识别 Kline dataset，不按 subject 维护 Kline freshness。

- [ ] **Step 2: 保留已有通用 DatasetMetrics。** `DatasetMetrics.ObserveRun` 仍可记录通用 dataset 级 rows/result/watermark，但不得新增 Kline 专用 label、metric name 或告警语义。

- [ ] **Step 3: 更新测试和静态边界检查。** 删除 Primary Kline gauge 测试，增加静态检查确保 `modules/storage/internal/service/primarystore` 不再引用 `KlineMetrics`、`IsKlineDatasetID` 或 `storage_kline_`。

- [ ] **Step 4: 运行 Primary 回归。**

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

- [ ] **Step 1: 写 active-index 边界测试。** 构造一轮 index apply：事件进入并路由后推进 input；A/B rebuild 阶段不推进 output；active index commit 成功后按 subject/frequency/series_tag 推进 output；一次 active write error 不推进 output；旧周期 apply 不回退 watermark。

- [ ] **Step 2: 验证任意 dataset/view 均可观测。** 测试使用不含 `kline` 的 dataset/view ID，证明 Storage 只按通用行结构上报，不依赖名称启发式，也不把非 Kline 数据误判为特殊对象。

- [ ] **Step 3: 从实际 rows 提取 canonical dimensions。** 按 View 的真实 `view_id`、事件的 `dataset_id`、row 的 canonical subject、View frequency 和 series_tag 分组并取最大 `data_time`。非法 row 只跳过对应 series，不阻塞同一批其他标的。

- [ ] **Step 4: 验证 View 阶段。**

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

- [ ] **Step 1: 写 latest 读取测试。** 给 SQLite 插入 5000 个通用 View output samples 和少量无关 samples，断言查询只返回 input/output freshness 白名单 family、按 `(metric_name, labels_json, series_id)` 稳定排序、默认上限覆盖全市场规模、超过上限返回显式错误而不是无界读取。

- [ ] **Step 2: 增加有界 API。** 在 `MetricMessageStore` 增加以下精确方法签名：

```go
func (r *MetricMessageStore) ListLatestByMetricNames(
    ctx context.Context,
    names []string,
    limit int,
) ([]MetricLatest, error)
```

只接受固定的通用 View freshness metric name；SQL 使用 `WHERE c_metric_name IN (...)`、稳定排序和明确 limit。`QueryService` 透传该有界能力，供 bootstrap 使用，不能暴露任意 SQL 或任意无界 metric scan。Monitor 再按配置的 `view_id` 和 `dataset_id` 过滤，不要求 Storage 判断 Kline。

- [ ] **Step 3: 写 evaluator 的业务时间测试。** 固定 `now=2026-09-08T10:02:30Z`：

  - 10:00 有数据、10:01 没有、10:02 有数据：`data_time_age <= stale_after`，返回 healthy，不产生 stale subject。
  - 一个 subject 的 View output `data_time` 超过阈值：只让对应 view/dataset/freq group 失败，diagnostic 带 `stale_count` 和该 subject。
  - View input 仍推进但 output 停止时，告警原因应指出 View 写入链路延迟；input 和 commit 只能作为诊断，不能代替 output 业务时间。
  - `commit_timestamp` 新鲜但 `data_time` 旧：失败原因是 `business_data_stale`，并附 commit age；不能把墙钟 commit 当成新 K 线。
  - 没有任何指定 View output sample：返回“无观测样本”，不创建健康 item；交给既有 Dataset freshness 发现从未运行的 dataset。

- [ ] **Step 4: 实现市场规则和 session gate。** `KlineFreshnessRule` 至少包含 `SpaceID`、`ViewID`、可选 `DatasetID`、`Frequency`、`MarketID`、`CalendarID`、`Timezone`、`Sessions`、`StaleAfter`、`Enabled`。crypto 无 calendar 全天评估；stockcn 使用 `marketcalendar.Load("cn_stock")` 和配置 session window；午休/收盘/非交易日返回 `skipped_market_closed`，calendar out-of-coverage 返回 `skipped_calendar_unknown`。

- [ ] **Step 5: 实现有界 stale 聚合。** 解析 `labels_json`，只接受完整 canonical labels；按 `SpaceID + ViewID + DatasetID + Frequency` 分组，只使用 output `data_time` 判定 stale。每组输出一个 `KlineFreshnessReport`：`Success`、`Reason`、`StaleCount`、`ObservedCount`、`OldestDataTime`、`LatestInputTime`、`LatestCommitTime` 和最多 20 个排序后的 `subject_id`。不要为每个 subject 构造 `domain.Check`。

- [ ] **Step 6: 实现异常策略。** malformed labels、非法 unix value、future data_time 超过 10 分钟、未知 frequency 只记录该 series 的结构化错误并跳过，不让一个坏 series 阻塞其他标的；若一个 group 的全部 series 都被跳过，返回 no-observation，不产生告警。

- [ ] **Step 7: 运行 evaluator 单测。**

```bash
go test -count=1 ./modules/monitor/internal/metrics -run 'KlineFreshness|ListLatestByMetricNames'
```

Expected: transient hole PASS；stale/recovery、View input/output 隔离、stock calendar 和 subject 限长 PASS；测试不会访问 Storage RPC。

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

- [ ] **Step 1: 增加严格 YAML 配置。** 使用以下结构作为唯一配置契约，禁止未知字段：

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

规则必须指定唯一的 `view_id`；`dataset_id` 可选，用于进一步限定 View 输入 dataset；不再存在 `scope` 字段，也禁止配置 Primary 规则。stockcn 必须有 calendar/session/timezone；crypto 不允许配置 stock session。校验 `evaluation_interval >= 30s`，`stale_after >= 2 * evaluation_interval`，`max_subjects_per_alert` 在 1 到 100 之间，规则 key 唯一；运行时按该 interval 设置 Kline check 的调度周期，默认仍为 30s。

- [ ] **Step 2: 写配置测试。** 覆盖缺字段、view_id 唯一性、可选 dataset_id、stock session 解析、calendar id、重复规则、太小 stale_after、负 duration、超过 subject limit、废弃 `scope` 字段和 `KnownFields(true)` 拒绝拼写错误。

- [ ] **Step 3: 注入现有 watchdog，而不是创建第二个 timer。** 给 `buildBusinessFreshnessReporter` 增加一个可选 Kline evaluator，初始化时使用 `runtime.MetricStores.Messages`/现有 `metricsQuery`；watchdog 仍由原 `registerMonitorScheduleTimers` 调用。每轮先执行现有 overview，再合并 Kline reports，最后复用已有 `CheckRepository`、`ResultRepository`、`resultHook` 和 `alerting.Evaluator`。

- [ ] **Step 4: 定义稳定 check identity。** Kline check ID 格式固定为：

```text
kline_freshness:<space_id>:<view_id>:<freq>
```

Name 使用中文 `K线 View 新鲜度 <space_id> <view_id> <freq>`；SpaceID 使用配置 space。一个 View/frequency group 永远只有一个 check/state，不使用 subject 拼接 check ID。

- [ ] **Step 5: 为 Kline check 设置默认告警去抖。** 在 `ensureDefaultCheckAlertRules` 中识别 `kline_freshness:`：`failure_threshold=2`、`success_threshold=2`、`minimum_reminder_interval_seconds=300`、`send_on_resolved=true`。其他 business/external check 的既有阈值不改。告警 payload/正文包含 `reason`、`stale_count`、`observed_count`、`oldest_data_time`、`latest_input_time`、`latest_commit_time` 和已排序的最多 20 个标的。

- [ ] **Step 6: 对无观测/市场关闭进行稳定处理。** 市场关闭或 calendar unknown 不提交失败结果，不增长 failure counter；有过往 firing state 时也不在收盘时立即发送恢复/失败通知，下一次 active session 再正常评估。没有观测样本时保留既有 Dataset freshness 语义，不创建临时健康成功。

- [ ] **Step 7: 验证 alert state。**

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

- [ ] **Step 1: 文档化生产指标契约。** 明确写出三个通用 View input/output metric name、六个 identity labels、`data_time`/`commit_timestamp` 的差异、30 秒 EventBus pipeline、告警按指定 View group 聚合、最多 20 个 stale subject、stockcn session gate、crypto 全天和“无内部桶扫描”。明确 Storage 不判断 Kline，Kline 只存在于 Monitor 规则。

- [ ] **Step 2: 文档化故障判断。** 加入以下诊断顺序：

```text
1. View input last_data_time 是否推进
2. View output last_data_time 是否推进
3. View output last_commit_timestamp 是否推进
4. Monitor MetricLatest 是否收到三个通用 View freshness family
5. EventBus pending/redelivery、Reporter error、Storage/View 写入错误
6. 指定 View 的 Kline freshness alert state 是否 firing/resolved
```

禁止把“服务 `/metrics` HTTP 可访问”当作 Monitor 已收到指标，也禁止把 `commit_timestamp` 新鲜误判为 K 线业务时间新鲜。

- [ ] **Step 3: 更新 stockcn 发布文档。** 将 Instrument 全市场快照描述为独立 daily Timer/SCF；将 Kline Timer 描述为定时增量；Provider 失败不阻塞其他 provider 的合并；自动 gap audit 删除后，历史任务只有显式 Backfill/GapRepair；egress probe 只作诊断，不作 IP 数量门禁。

- [ ] **Step 4: 编写本地端到端脚本。** `verify-kline-freshness-e2e.sh` 必须使用临时目录/临时 SQLite/测试 EventBus，并验证：

  - View 事件进入并成功路由后产生 input；active apply 成功后产生 output；非 active/rebuild/失败不产生 output 推进。
  - Reporter snapshot 通过 EventBus ingest 到 Monitor latest，labels JSON 保持 canonical。
  - 10:00/10:01/10:02 transient hole 不触发 alert；连续 stale 触发后连续恢复 resolved。
  - stockcn 午休、周末和 holiday 不触发 stale；crypto 同样 elapsed 会触发。
  - Binance/crypto 既有 metric、Kline pipeline、View output watermark 和 canary tests 继续通过。

- [ ] **Step 5: 运行回归。**

```bash
bash scripts/test/e2e/verify-kline-freshness-e2e.sh
bash scripts/test/e2e/verify-observability-e2e.sh
go test -count=1 ./modules/collector/... \
  ./modules/storage/... \
  ./modules/monitor/... \
  ./packages/report/... \
  ./packages/marketcalendar/...
```

Expected: 新增 Kline case PASS；crypto 回归 PASS；Monitor retired metric-rule schema 断言 PASS；如果环境缺少 Docker/NATS/腾讯 SDK，只记录明确环境阻塞，不把 skip 当 PASS。

### Task 9: 正式编译、发布到 106 并做真实 SCF/Storage/View/Monitor 验收

**Files/Artifacts:**
- Use: `moox.toml`（只读输入，不打印、不提交、不修改；如另有本地部署配置，必须先复制为受支持的文件名并保持 0600，不能把 `custom.toml` 直接传给当前 Loader）
- Use: `bin/moox-cli`
- Use: `scripts/deploy/deploy-moox.sh`
- Create locally: `artifacts/kline-freshness/<timestamp>-publish.json`
- Create locally: `artifacts/kline-freshness/<timestamp>-readback.json`

- [ ] **Step 1: 生成可追溯正式构建。**

```bash
git status --short
git rev-parse HEAD
make verify
make release
sha256sum bin/moox-cli
```

Expected: working tree 状态、commit、release archive、CLI SHA 记录到脱敏 artifact；构建日志不包含 `moox.toml` 内容或任何 secret。

- [ ] **Step 2: 发布 106 主机服务包。** 先按现有部署脚本的 `--help` 确认参数，再使用 `moox.toml` 的 host/path 配置执行标准 deploy，不手工 scp 二进制。部署前保存当前 release/provenance 和 systemd 状态；允许重启 106，但不删除 Storage Primary 数据、View active index、Monitor SQLite 或 EventBus JetStream。

```bash
./scripts/deploy/deploy-moox.sh --help
./bin/moox-cli setup validate --file ./moox.toml
./bin/moox-cli setup status --file ./moox.toml
```

若需要更新控制面/Storage/Monitor，使用仓库既有 `setup deploy-control`、`setup deploy-storage`、`setup deploy-service` 流程；每一步保存服务 health、监听端口、进程 SHA 和 rollback archive。服务未 ready 时停止后续 SCF 发布，不以“进程已启动”作为验收。

- [ ] **Step 3: 先停历史和 crypto 负载，再发布 stockcn。** 在 106 上确认历史队列/历史 worker 已停止，crypto 采集按当前运维决定保持 disabled；只保留 stockcn Instrument daily Timer 和 stockcn Kline Timer。记录停用前后的规则、Timer、durable consumer 数量，不能把停历史误写成代码验收通过。

- [ ] **Step 4: 用真实控制面提交 stockcn SCF publish。** 运行正式命令，使用短时环境变量提供 control/service auth，不把凭证写入参数日志：

```bash
./bin/moox-cli collector function publish submit \
  --file ./moox.toml \
  --space-id stockcn \
  --control-url "$MOOX_CONTROL_URL" \
  --enable-stockcn \
  > "artifacts/kline-freshness/$(date +%Y%m%d%H%M%S)-publish.json"
```

命令返回后，用返回的每个 `job_id` 执行 status readback，直到每个 job 为 `SUCCESS`；不能只看 CLI submit 返回成功。确认返回包含：正式 package ID、每个 region 的 Timer 数量、独立 `instrument_snapshot_daily` fleet、每地域 1 个 invoke canary、Timer trigger enabled、Rule enabled、没有依赖公网 IP 数量相等门禁。

- [ ] **Step 5: 验证真实 SCF canary。** Publish 流程自带 invoke canary；另外执行已存在的出口/Provider 诊断，并保存结果：

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

- [ ] **Step 6: 读回 Primary 和 View 的真实 durable row。** 等待至少三个 1 分钟 Timer 周期和两个 30 秒 Monitor reporter 周期，然后对一个 SH、一个 SZ、一个 BSE subject 做有界读回：

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

- [ ] **Step 7: 验证通用 View 指标和 Monitor latest。** 在 106 的 Monitor SQLite 使用只读连接检查固定 metric family、标签和时间，不直接修改数据库：

```sql
SELECT c_metric_name, c_labels_json, c_value, c_observed_at
FROM t_monitor_metric_latest
WHERE c_metric_name IN (
  'moox_storage_view_dataset_input_last_data_time_seconds',
  'moox_storage_view_dataset_output_last_data_time_seconds',
  'moox_storage_view_dataset_output_last_commit_timestamp_seconds'
)
ORDER BY c_metric_name, c_series_id
LIMIT 40;
```

证据必须证明：`subject_id` 为 canonical subject、`freq=1m`、`series_tag=default` 或明确 series tag；指定 View 的 input/output data time 已推进；output commit timestamp 与成功写入时间相符；Monitor `observed_at` 与最近 reporter 周期相符；没有超过 configured sample/label limit 的 reporter error。Primary Kline 指标不属于验收项。

- [ ] **Step 8: 验证告警不误报且能恢复。** 在真实环境不删除/篡改生产数据。用本地 E2E 证明 transient hole 和 recovery；生产只做只读核验：

```sql
SELECT c_space_id, c_check_id, c_status, c_failure_count,
       c_success_count, c_triggered_at, c_resolved_at
FROM t_monitor_alert_states
WHERE c_check_id LIKE 'kline_freshness:%'
ORDER BY c_mtime DESC
LIMIT 40;
```

正式验收窗口内，stockcn active session 的 freshness check 不得因 10:00/10:01/10:02 这种短缺口 firing；如果真实 View/Primary 持续停更，必须能看到对应 group 的 firing/resolved 状态和 bounded subject diagnostic。

- [ ] **Step 9: 记录 crypto 回归和 rollback 条件。** Crypto 只做既有规则/Provider/Primary/View/Monitor 回归，不因本任务自动重新启用；记录 `dataset_binance_spot_kline_1m` 的最新 data_time、View 最新 data_time、metric latest 和当前规则状态。任何一项正式证据缺失、SCF job 为 PARTIAL、Primary 有行但 View 无行、Monitor 无三个 View freshness latest、或编译 provenance 不一致，都标记 `NO-GO`，恢复上一 package/service release，并保留证据，不宣称正式验收完成。

## 4. 完成定义（Definition of Done）

- [ ] `modules/collector` 中不存在自动 gap audit 实现、状态、环境变量和死接口；显式 HistoryPolicy/Backfill/GapRepair contract 与 Pipeline 测试仍 PASS。
- [ ] Storage Primary 不包含任何 Kline 专用分支、类型、指标或名称判断；既有通用 DatasetMetrics 不回归。
- [ ] Storage View 只通过通用 input/output observation 上报；只有 active View 成功提交后推进 output `last_data_time` 与 `last_commit_timestamp`，失败/重建阶段不推进。
- [ ] Monitor 通过现有 30 秒 EventBus reporter 链路接收并持久化三个通用 View freshness metric family；不依赖 HTTP `/metrics` 抓取。
- [ ] 告警按 group 聚合，subject 只出现在 bounded diagnostic；短暂内部缺口不 firing；连续 stale 可 firing，恢复可 resolved；stockcn 非交易时段不误报。
- [ ] 本地 Collector/Storage/Monitor/marketcalendar/crypto 回归和 observability E2E PASS，环境失败项单独标注。
- [ ] 106 主机服务包有可回滚 provenance，stockcn publish job 全部 SUCCESS，Instrument daily Timer 与 Kline Timer 分离且 readback 正确。
- [ ] 真实 SCF invoke canary 成功，真实 Primary durable row 和 `view_stockcn_equity_kline_1m` durable row 均读回，指定 View 的 Monitor SQLite latest/alert state 有独立证据。
- [ ] 没有真实控制面、Tencent Cloud、Storage 和 106 SSH 凭证时，最终状态必须写成“代码/本地验证完成，正式环境未验收”，不得用模拟结果替代。

## 5. 计划自审

- **需求覆盖：** 自动 gap audit 删除在 Task 1-2；通用 View input/output 观测和 Primary 解耦在 Task 3-5；30 秒现有链路、指定 View 的告警、session 和 transient hole 在 Task 6-7；文档和 crypto 回归在 Task 8；正式编译、SCF、Primary/View/Monitor E2E 在 Task 9。
- **一致性检查：** 三个通用 View metric name 和 labels 在约束、Storage、Monitor、SQL readback 四处一致；`data_time` 和 `commit_timestamp` 在实现和告警中不混用；告警 check ID 不包含 subject，避免 5000 条 state；Monitor 通过 `view_id` 配置识别 Kline，不依赖 Storage 名称启发式。
- **范围检查：** 没有重新引入已删除的 `t_monitor_metric_rules*` 通用规则表；Storage Primary 不出现 `KlineMetrics`、`IsKlineDatasetID` 或 `storage_kline_`；没有删除公共 Storage `ReadTimeSeriesRows`，只删除 Collector 私有自动审计适配器（除非调用图证明该路由确实是 Collector-private）；没有把 crypto 重新启用作为隐含副作用。
- **禁止的空泛步骤：** 每个实现步骤都绑定了文件、符号、测试或命令；实现时不得以“适当处理”“后续补充”替代上述具体契约。

## 6. 执行交接

计划完成后，按任务顺序执行并在每个 Task 后保留 commit/测试证据。推荐使用 `superpowers:subagent-driven-development`，每个 Task 由独立 subagent 实施后由主 Agent 复核；也可以使用 `superpowers:executing-plans` 在当前工作树分批执行。正式发布只在本地测试和独立代码审查通过后进行，且发布结果必须以真实 SCF/Storage/View/Monitor readback 为准。
