# MooX 可配置 K 线聚合 Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 在 Collector 中增加由已落盘源 K 线生成任意整分钟派生 K 线的能力，实时聚合、短重试、停机追赶和历史回填共用同一套执行内核，并把每个目标周期写入独立 Dataset，例如 `spot_kline_derived_4h`。

**Architecture:** Collector 新增独立的 `klineaggregation` 子系统。每分钟第 10 秒运行的全局 scanner 只计算到期桶并把稳定 bucket job 写入 Collector SQLite；单实例本地 worker 从 Storage Primary 精确读取源窗口，完成 OHLCV 聚合、幂等写入派生 Dataset，并在完整成功后发布目标周期完成标记。Storage View 增加动态路由清单协调器，使运行时创建的派生 Dataset/View 不依赖进程重启即可接入增量消费。实时任务、最近桶修订和显式历史回填只改变任务来源与优先级，不复制聚合算法。

**Tech Stack:** Go 1.25、tRPC-Go Timer、SQLite/GORM、Storage Metadata/PrimaryStore RPC、`github.com/avast/retry-go`、NATS JetStream、Prometheus、Vue 3、TypeScript、Vitest。

---

## 0. 已确认决策

### 0.1 V1 能力边界

- 仅支持 24x7 行情，不处理股票交易时段、节假日、夏令时或本地交易日。
- 周期不是固定枚举；用户可输入任意正整数分钟周期，并可用 `m/h/d` 表示。
- 同一个周期保留两种明确表示，禁止混用：Storage/RowKey 使用现有规范值，例如 `240m -> 4H`、`1440m -> 1D`、`90m -> 90m`；Dataset/View ID 后缀和 UI 展示使用小写 slug，例如 `4h`、`1d`、`90m`。
- 最小目标周期为 1 分钟，目标周期必须大于源周期，且满足 `target_duration % source_duration == 0`。
- V1 最大目标周期为 30 天，单个目标桶最多展开 10,080 根源 K 线；两项限制分别控制 Dataset/View 命名和 Primary 精确读取成本。
- 固定周期统一采用 Unix epoch 的 UTC 网格。`7m` 是连续 7 分钟桶，不保证每天 UTC 00:00 重置；`10h` 同理。
- V1 不支持 `W/M/Y` 日历周期，不把 `1M` 当作 `30d`，也不支持秒级周期。
- V1 禁止“派生 Dataset 再作为源 Dataset”，避免循环依赖和级联修订；只允许基础事实 Dataset 到一级派生 Dataset。

### 0.2 Dataset 命名

- 每个目标周期使用独立 Dataset，而不是把所有派生频率写进同一个 Dataset。
- 默认目标 Dataset ID：`<market>_kline_derived_<frequency_slug>`。
- 现货 4 小时示例：`spot_kline_derived_4h`。
- 永续 4 小时示例：`swap_kline_derived_4h`。
- 默认 View ID：`<target_dataset_id>_view`，例如 `spot_kline_derived_4h_view`。
- 创建前 UI 展示自动生成的 ID，并允许用户修改；自定义 ID 必须为 lower snake case、最长 25 字符，并以规范化周期后缀结尾。创建后不可修改。
- 一个目标 Dataset 只允许一个启用中的聚合规则。若要从另一个来源生成同周期结果，必须使用另一个目标 Dataset，避免 `DatasetPeriodCollected` 在相同 `dataset/frequency/period` 下冲突。

### 0.3 运行位置

- V1 使用 Collector 进程内本地 worker，不复用现有 `marketfetch` SCF Handler，也不新增独立 SCF。
- 原因是聚合只访问 MooX Storage，不需要交易所地域出口；本地 worker 可直接复用 Collector SQLite checkpoint、Storage 鉴权和监控，历史回填也不需要把进度塞进 SCF Environment。
- scanner 和 worker 是独立组件：scanner 不读取 K 线、不等待、不写目标行；worker 不计算下一到期周期。
- 保持单实例设计，不增加分布式锁、全局 exactly-once 或 DLQ。SQLite 状态迁移和 Storage 幂等 Upsert 是必要的恢复边界。

### 0.4 K 线计算合同

对目标桶 `[bucket_start, bucket_end)`，按 `data_time` 升序排列完整源行：

| 目标字段 | 计算方式 |
| --- | --- |
| `open` | 第一根源 K 线的 `open` |
| `high` | 所有源 K 线 `high` 的最大值 |
| `low` | 所有源 K 线 `low` 的最小值 |
| `close` | 最后一根源 K 线的 `close` |
| `volume` | 所有源 K 线 `volume` 求和 |
| `quote_volume` | 所有源 K 线 `quote_volume` 求和 |
| `trade_num` | 所有源 K 线 `trade_num` 求和 |

目标 RowKey 固定为：

```text
space_id      = rule.space_id
dataset_id    = rule.target_dataset_id
subject_id    = source subject_id
freq          = rule.target_frequency       # Storage 规范值，例如 4H
data_time     = bucket_start UTC
series_tag    = rule.source_series_tag
```

`raw_payload` 不进入派生 Dataset。目标行 attributes 写入 `aggregation_rule_id`、`aggregation_rule_version`、`source_dataset_id`、`source_frequency`、`source_window_end` 和 `source_hash`，用于来源追踪及跨本地任务清理周期的幂等判断。只要任意预期源行不存在、字段不全、时间不对齐或 series tag 不匹配，就不写该 subject 的部分目标 K 线。

### 0.5 实时、修订与历史语义

- 新规则默认从最近 3 个已闭合目标桶开始，避免创建规则后只看到下一根数据。
- 每次 scanner 都重新检查最近 `repair_lookback_buckets` 个已闭合桶；输入哈希不变时不写 Storage，输入变化时覆盖同一目标 RowKey。
- 超出最近修订窗口的迟到修正由显式历史回填处理。
- 实时桶只有所有 subject 完成后才发布 `DatasetPeriodCollected(status=complete)`；短重试耗尽时不发布 degraded marker。
- 历史回填默认不发布逐周期 marker，避免因子模块被大量旧周期触发；回填完成后发布 Dataset sync point，供 View 水位验收。
- 已发布完成 marker 后发生的最近桶修订只更新目标行和 View，不重新发布 marker。依赖该周期的历史因子需要显式 Recalc；新增可修订 marker 协议不在 V1 范围内。

## 1. 目标数据流

```text
用户创建聚合规则
  -> Collector 校验源 Dataset/频率/series_tag
  -> 幂等创建 spot_kline_derived_4h + View
  -> Storage View 动态路由协调器接入新 Dataset，发布 route-ready sync point，并完成初始 View rebuild
  -> 规则进入 active

每分钟第 10 秒
  -> scanner 计算 effective_now = now - settle_delay
  -> 按 next_bucket_start 补建所有已到期 bucket job（单轮有上限）
  -> 最近 N 个完成桶标记为 repair_pending
  -> 唤醒本地 worker，timer 返回

worker
  -> claim 一个 bucket job
  -> 每批最多 50 个 subject
  -> PrimaryStore.ReadFields 精确分片读取预期源 RowKey
  -> 缺行时 250ms/500ms/1s/2s context-aware 退避
  -> 完整 subject 执行 OHLCV 聚合
  -> source_event_id 幂等写目标 Dataset
  -> 全部 item 完成后发布 realtime complete marker

历史回填
  -> 创建 [start,end) backfill request
  -> 生成同一种 bucket job，使用低优先级
  -> 复用同一 worker/kernel
  -> 完成后 AppendDatasetSyncPoint 并等待 View 追平
```

## 2. 持久化状态

### 2.1 聚合规则

`t_kline_aggregation_rules` 使用 `(c_space_id, c_rule_id)` 唯一，并对 `(c_space_id, c_target_dataset_id)` 建唯一索引。核心字段如下：

```text
c_space_id
c_rule_id
c_rule_version
c_market_type
c_source_dataset_id
c_source_frequency
c_source_series_tag
c_target_dataset_id
c_target_view_id
c_target_frequency
c_alignment              # 固定 epoch_utc
c_settle_delay_ms
c_repair_lookback_buckets
c_next_bucket_start
c_enabled
c_provision_state        # pending/waiting_view/ready/error
c_last_error
c_creator
c_ctime/c_mtime
```

源/目标 Dataset、源/目标频率、series tag 和 alignment 创建后不可修改；调整这些字段必须创建新规则。允许修改 `settle_delay_ms`、`repair_lookback_buckets` 和 enabled。

### 2.2 Bucket job 与 item

`t_kline_aggregation_jobs` 使用 `(space_id, rule_id, rule_version, job_key)` 唯一。实时与最近修订共享 `job_key=live:<bucket_start>`；历史回填使用 `job_key=backfill:<backfill_id>:<bucket_start>`，允许两个独立回填请求覆盖同一桶而不争用进度：

```text
c_job_id
c_job_key
c_space_id
c_rule_id/c_rule_version
c_bucket_start/c_bucket_end
c_origin                 # realtime/repair/backfill
c_priority               # realtime=100, repair=80, backfill=10
c_status                 # pending/running/waiting/complete/failed
c_attempt
c_expected_count/c_success_count/c_not_ready_count/c_failed_count
c_next_retry_at
c_lease_started_at
c_last_error
c_marker_state           # none/pending/reported
c_backfill_id
c_ctime/c_mtime/c_completed_at
```

`t_kline_aggregation_job_items` 使用 `(job_id, subject_id, series_tag)` 唯一：

```text
c_job_id
c_subject_id/c_series_tag
c_state                  # pending/running/not_ready/complete/failed
c_attempt
c_input_hash
c_source_event_id
c_next_retry_at
c_last_error
c_mtime/c_completed_at
```

Catalog 在规则准备和每次扫描前按页协调 source/target Subject binding：新增 active source subject 会幂等绑定到 target，并进入之后的新桶；source binding 被 disabled 后不进入新桶，但既有目标数据不删除。Bucket job 创建时快照当时 active subject 清单，保证一个 job 的 `expected_count` 执行期间不漂移；如需给旧桶补入后来新增的标的，用户创建历史回填。

### 2.3 历史回填请求

`t_kline_aggregation_backfills` 使用 `(space_id, backfill_id)` 为主唯一键，并对 `(space_id, request_id)` 建唯一索引：

```text
c_backfill_id
c_request_id
c_space_id/c_rule_id/c_rule_version
c_start_time/c_end_time
c_status                 # pending/running/complete/partial_failed/failed
c_publish_markers        # V1 UI 固定 false
c_total_jobs/c_complete_jobs/c_failed_jobs
c_sync_point_id
c_last_error
c_creator
c_ctime/c_mtime/c_completed_at
```

## 3. 文件结构

| 路径 | 责任 |
| --- | --- |
| `modules/collector/internal/klineaggregation/frequency.go` | 固定周期解析、规范化、整除和 UTC epoch 桶边界 |
| `modules/collector/internal/klineaggregation/naming.go` | 目标 Dataset/View ID 生成与校验 |
| `modules/collector/internal/klineaggregation/bar.go` | 纯 OHLCV 聚合、完整性检查和输入哈希 |
| `modules/collector/internal/klineaggregation/catalog.go` | 派生 Dataset、字段、Subject binding 和 View 的幂等协调 |
| `modules/collector/internal/klineaggregation/storage.go` | Primary 精确分片读、目标幂等 Upsert、marker/sync point |
| `modules/collector/internal/klineaggregation/scanner.go` | 每分钟到期桶扫描、checkpoint 推进和最近桶修订 |
| `modules/collector/internal/klineaggregation/worker.go` | 本地有界 worker、claim、短退避和跨 tick 恢复 |
| `modules/collector/internal/klineaggregation/backfill.go` | 历史区间校验、任务生成和完成汇总 |
| `modules/collector/internal/klineaggregation/metrics.go` | 聚合 backlog、延迟、缺行和执行结果指标 |
| `modules/collector/internal/domain/kline_aggregation.go` | rule/job/item/backfill 持久化领域模型 |
| `modules/collector/internal/store/kline_aggregation_rule.go` | 聚合规则 repository |
| `modules/collector/internal/store/kline_aggregation_job.go` | bucket job/item 原子状态迁移 repository |
| `modules/collector/internal/store/kline_aggregation_backfill.go` | 回填请求 repository |
| `modules/collector/internal/rpc/kline_aggregation.go` | 聚合规则、任务和回填管理 RPC |
| `modules/collector/schema/collector.sql` | 三组聚合表、索引、约束和 mtime trigger |
| `modules/collector/proto/collector.proto` | 聚合规则、job、backfill 消息与 RPC |
| `modules/collector/internal/bootstrap/config.go` | worker/scanner 配置及严格 YAML 校验 |
| `modules/collector/internal/bootstrap/bootstrap.go` | catalog、scanner、worker、RPC 和 timer 装配 |
| `modules/collector/config/trpc_go.yaml` | 独立的第 10 秒全局聚合 timer |
| `modules/collector/config/app.yaml` | 聚合默认运行参数 |
| `modules/collector/internal/observability/realtime_inventory.go` | 将 ready 且 enabled 的派生 Dataset/频率加入实时预期清单 |
| `modules/storage/internal/service/view/inventory_reconciler.go` | 动态发现 active View Dataset，并协调增量消费路由和初始 rebuild |
| `modules/storage/cmd/server/main.go` | 把 View consumer 的一次性启动改为可重建的受管生命周期 |
| `modules/storage/internal/config/loader.go` | View 动态清单刷新间隔与严格配置校验 |
| `web/src/views/collector/kline-aggregation/` | 规则列表、创建表单、任务详情和回填入口 |
| `web/src/views/collector/task-management/index.vue` | 新增“K线聚合”页签 |
| `web/src/api/collector/kline-aggregation.ts` | 聚合 RPC 请求和类型 |
| `docs/内置市场行情采集架构.md` | 用户可见的数据流、周期合同和故障语义 |

## 4. 实施任务

### Task 1: 固定周期、对齐与命名纯函数

**Files:**
- Create: `modules/collector/internal/klineaggregation/frequency.go`
- Create: `modules/collector/internal/klineaggregation/frequency_test.go`
- Create: `modules/collector/internal/klineaggregation/naming.go`
- Create: `modules/collector/internal/klineaggregation/naming_test.go`

- [ ] **Step 1: 写周期规范化失败测试**

测试必须覆盖：

```go
require.Equal(t, FixedFrequency{Storage: "4H", Slug: "4h", Duration: 4*time.Hour}, mustParse(t, "240m"))
require.Equal(t, FixedFrequency{Storage: "4H", Slug: "4h", Duration: 4*time.Hour}, mustParse(t, "4H"))
require.Equal(t, FixedFrequency{Storage: "90m", Slug: "90m", Duration: 90*time.Minute}, mustParse(t, "90m"))
require.Equal(t, FixedFrequency{Storage: "1D", Slug: "1d", Duration: 24*time.Hour}, mustParse(t, "24h"))
require.Error(t, validatePair("1h", "90m"))
require.NoError(t, validatePair("30m", "90m"))
require.Error(t, validatePair("1m", "1M"))
require.Error(t, validatePair("1m", "31d"))
```

- [ ] **Step 2: 运行红灯**

Run: `cd modules/collector && go test -count=1 ./internal/klineaggregation -run 'Test(Frequency|AggregationPair)'`

Expected: FAIL，因为周期函数尚不存在。

- [ ] **Step 3: 实现唯一固定周期合同**

公开给子系统内部使用的接口固定为：

```go
type FixedFrequency struct {
    Storage  string        // Metadata freq、RowKey.freq 和事件 payload，例如 4H
    Slug     string        // Dataset/View ID 后缀和 UI 展示，例如 4h
    Duration time.Duration
}

func ParseFixedFrequency(raw string) (FixedFrequency, error)
func ValidateAggregationPair(source, target FixedFrequency) error
func BucketAt(effectiveNow, origin time.Time, target FixedFrequency) (start, end time.Time)
```

`origin` 在 V1 必须为 `time.Unix(0, 0).UTC()`。`BucketAt` 使用整数纳秒差计算，不使用 `now.Minute()%N`。规则落库保存 Storage 规范值，生成 ID 时只使用 `Slug`。

- [ ] **Step 4: 写命名失败测试并实现**

```go
require.Equal(t, "spot_kline_derived_4h", DefaultTargetDatasetID("spot", "4h"))
require.Equal(t, "swap_kline_derived_90m_view", DefaultTargetViewID("swap_kline_derived_90m"))
require.NoError(t, ValidateTargetIDs("spot_kline_derived_4h", "spot_kline_derived_4h_view", "4h"))
require.Error(t, ValidateTargetIDs("spot_kline_derived", "spot_kline_derived_view", "4h"))
```

自定义 Dataset ID 最长 25 字符；View ID 必须等于 Dataset ID 加 `_view`，从而满足 Storage 的 30 字符限制。

- [ ] **Step 5: 运行绿灯并提交**

Run: `cd modules/collector && go test -count=1 ./internal/klineaggregation`

Expected: PASS。

Commit: `feat(collector): define kline aggregation periods`

### Task 2: 增加聚合协议、SQLite schema 和 repositories

**Files:**
- Modify: `modules/collector/proto/collector.proto`
- Modify: generated files under `modules/collector/proto/collectorgen/`
- Create: `modules/collector/internal/domain/kline_aggregation.go`
- Create: `modules/collector/internal/domain/kline_aggregation_test.go`
- Modify: `modules/collector/schema/collector.sql`
- Modify: `modules/collector/internal/store/database.go`
- Create: `modules/collector/internal/store/kline_aggregation_rule.go`
- Create: `modules/collector/internal/store/kline_aggregation_rule_test.go`
- Create: `modules/collector/internal/store/kline_aggregation_job.go`
- Create: `modules/collector/internal/store/kline_aggregation_job_test.go`
- Create: `modules/collector/internal/store/kline_aggregation_backfill.go`
- Create: `modules/collector/internal/store/kline_aggregation_backfill_test.go`
- Modify: `modules/collector/internal/store/database_test.go`

- [ ] **Step 1: 写 proto descriptor 和 schema 红灯测试**

协议新增以下 RPC：

```protobuf
rpc ListKlineAggregationRules(ListKlineAggregationRulesReq) returns (ListKlineAggregationRulesRsp);
rpc GetKlineAggregationRule(GetKlineAggregationRuleReq) returns (GetKlineAggregationRuleRsp);
rpc CreateKlineAggregationRule(CreateKlineAggregationRuleReq) returns (CreateKlineAggregationRuleRsp);
rpc UpdateKlineAggregationRule(UpdateKlineAggregationRuleReq) returns (UpdateKlineAggregationRuleRsp);
rpc DisableKlineAggregationRule(DisableKlineAggregationRuleReq) returns (DisableKlineAggregationRuleRsp);
rpc ListKlineAggregationJobs(ListKlineAggregationJobsReq) returns (ListKlineAggregationJobsRsp);
rpc CreateKlineAggregationBackfill(CreateKlineAggregationBackfillReq) returns (CreateKlineAggregationBackfillRsp);
rpc ListKlineAggregationBackfills(ListKlineAggregationBackfillsReq) returns (ListKlineAggregationBackfillsRsp);
```

descriptor 测试断言 `KlineAggregationRule` 显式包含源/目标 Dataset、源/目标频率、series tag、settle delay、repair lookback、provision state 和 last error；不得把规则塞回无类型的 `google.protobuf.Struct`。

- [ ] **Step 2: 运行红灯**

Run: `cd modules/collector/proto && go test -count=1 ./... -run KlineAggregation`

Run: `cd modules/collector && go test -count=1 ./internal/store -run KlineAggregation`

Expected: FAIL，协议消息和三组表尚不存在。

- [ ] **Step 3: 定义领域状态和 schema 约束**

状态常量只允许本计划第 2 节列出的值。Schema 必须包含：

- rule 的 target Dataset 唯一索引；
- job 的 rule version + job key 唯一索引；
- job `status/priority/next_retry_at` due 索引；
- item `job_id/state/next_retry_at` due 索引；
- backfill 的 `space_id/request_id` 唯一索引；
- backfill `status/ctime` 索引；
- job/item/backfill 到 rule/job 的外键；
- 每个表的 `CHECK` 状态约束与标准 mtime trigger。

- [ ] **Step 4: 实现 repository 原子操作**

repository 必须提供：

```go
CreateRule(ctx, rule)
UpdateMutableRule(ctx, spaceID, ruleID, settleDelay, repairLookback, enabled)
MarkProvisioned(ctx, spaceID, ruleID, targetDatasetID, targetViewID)
MarkProvisionError(ctx, spaceID, ruleID, err)
EnsureBucketJob(ctx, rule, bucket, origin, backfillID, subjects)
ClaimDueItems(ctx, now, subjectLimit, staleAfter)
MarkItemsNotReady(ctx, itemIDs, nextRetryAt, err)
CompleteItemsAndJob(ctx, ...)
RequeueRecentBuckets(ctx, rule, starts)
CreateBackfill(ctx, request)
RefreshBackfillProgress(ctx, backfillID)
CleanupCompleted(ctx, beforeJobs, beforeBackfills, limit)
```

`EnsureBucketJob` 必须在一个 SQLite transaction 中插入 parent 和 subject items；重复 tick 返回已有 job，不重复 item。

- [ ] **Step 5: 覆盖恢复竞态**

测试证明：

- 重复创建同一 bucket 是 no-op；
- `pending/not_ready -> running` claim 是条件更新；
- 超过 `stale_running_after` 的 running item 可被同实例重启后回收；
- complete item 不被普通实时 scan 回退；repair 明确请求时才重新置 pending；
- 更新规则不能修改 source/target identity；
- 禁用规则后不再 claim 新 item，但已写入的数据不删除。
- completed jobs/items 和 completed backfills 按保留期分批清理，pending/running/waiting/failed 记录不被误删；清理后重新检查目标桶仍通过目标行 `source_hash` 幂等收敛。

- [ ] **Step 6: 生成 proto、运行绿灯并提交**

Run: `cd modules/collector/proto && make all`

Run: `cd modules/collector && go test -count=1 ./internal/domain ./internal/store ./proto/...`

Expected: PASS，schema 可重复应用到空 SQLite。

Commit: `feat(collector): persist kline aggregation work`

### Task 3: 幂等协调派生 Dataset、字段、Subject 和 View

**Files:**
- Modify: `modules/collector/internal/planner/storagesource/source.go`
- Modify: `modules/collector/internal/planner/storagesource/source_test.go`
- Create: `modules/collector/internal/klineaggregation/catalog.go`
- Create: `modules/collector/internal/klineaggregation/catalog_test.go`

- [ ] **Step 1: 写 catalog 红灯测试**

用 fake Metadata client 覆盖：

- source Dataset 必须 active、time_series、包含 source frequency 和 7 个标准字段；
- source Dataset 的 `attributes.derived_kind=kline_aggregation` 被拒绝；
- `source_series_tag` 为空被拒绝；
- target 不存在时按固定顺序创建；
- target 已存在且合同一致时幂等复用；
- target 已存在但 source、frequency、market、DataNode 或字段合同不一致时失败，不覆盖用户元数据。

- [ ] **Step 2: 运行红灯**

Run: `cd modules/collector && go test -count=1 ./internal/klineaggregation -run Catalog`

Expected: FAIL，因为 catalog coordinator 尚不存在。

- [ ] **Step 3: 扩展窄 Metadata adapter**

仅暴露聚合需要的方法：

```go
type Catalog interface {
    GetDataset(context.Context, string, string) (*storagepb.Dataset, error)
    ListDatasetColumns(context.Context, string, string) ([]*storagepb.DatasetColumn, error)
    ListDatasetSubjects(context.Context, string, string) ([]*storagepb.DatasetSubject, error)
    CreateDataset(context.Context, *storagepb.Dataset) (*storagepb.Dataset, error)
    UpsertDatasetColumn(context.Context, *storagepb.DatasetColumn) error
    BindDatasetSubject(context.Context, *storagepb.DatasetSubject) error
    CheckDatasetActivation(context.Context, string, string) error
    ActivateDataset(context.Context, string, string, uint64) (*storagepb.Dataset, error)
    CreateView(context.Context, *storagepb.View) (*storagepb.View, error)
    UpsertViewColumn(context.Context, *storagepb.ViewColumn) error
}
```

不要把完整 Metadata proxy 暴露给 scanner/worker。

- [ ] **Step 4: 实现目标元数据模板**

以 `spot_kline_derived_4h` 为例：

```yaml
dataset_id: spot_kline_derived_4h
data_source_id: crypto_market
data_kind: time_series
data_node_id: <same as source Dataset>
keep_duration: 4320h
freqs: [4H]
attributes:
  market_type: spot
  storage_model: wide_common_metrics
  derived_kind: kline_aggregation
  source_dataset_id: binance_spot_kline_1m
  source_frequency: 1m
  source_series_tag: venue:binance
  target_frequency: 4H
  alignment: epoch_utc
```

顺序固定为：创建 disabled Dataset -> upsert 7 列 -> 复制 active Subject bindings -> `CheckDatasetActivation` -> CAS 激活 Dataset -> 创建 View -> upsert 7 个 View columns。View 使用 `filter_json={"freq":"4H"}`、标准四个 grain keys 和目标 Dataset 作为 primary。Dataset ID 虽以小写 `4h` 结尾，但 Metadata `freqs`、View filter、RowKey 和事件 payload 必须全部使用 `4H`。

Catalog 另提供 `ReconcileSubjects`：按页读取 source binding，把新增 active subject 幂等绑定到 target，把 source 已 disabled 的 binding 同步为 disabled。scanner 每个 rule 每轮最多执行一次协调并复用结果创建本轮 jobs；协调失败时不推进 checkpoint，避免目标行已写但 View grain 缺少 subject 元数据。

- [ ] **Step 5: 处理跨 RPC 部分成功**

Collector 不模拟跨服务 transaction。规则先以 `provision_state=pending` 落库；Dataset/View 元数据就绪后进入 `waiting_view`。route-ready request ID 固定为 `kline-aggregation-route:<rule_id>:<rule_version>:<desired_view_revision>`；只有 `WaitViewSyncPoint` 确认该 request ID 已被目标 View 消费，且 `active_index_id` 非空、`active_view_revision == desired_view_revision` 时才进入 `ready`。这两个条件分别证明增量路由和初始索引均已就绪。协调任一步失败时保存 `provision_state=error/last_error`。再次创建同合同规则或点击“重试准备”会从 Metadata 当前状态继续，只有 ready 规则才允许 scanner 调度。

- [ ] **Step 6: 运行绿灯并提交**

Run: `cd modules/collector && go test -count=1 ./internal/planner/storagesource ./internal/klineaggregation -run 'Catalog|DerivedDataset'`

Expected: PASS。

Commit: `feat(collector): provision derived kline datasets`

### Task 4: 让 Storage View 动态接入运行时新建的派生 Dataset

**Files:**
- Create: `modules/storage/internal/service/view/inventory_reconciler.go`
- Create: `modules/storage/internal/service/view/inventory_reconciler_test.go`
- Modify: `modules/storage/internal/service/view/consume.go`
- Modify: `modules/storage/internal/service/view/consume_test.go`
- Modify: `modules/storage/cmd/server/main.go`
- Modify: `modules/storage/cmd/server/main_test.go`
- Modify: `modules/storage/internal/config/loader.go`
- Modify: `modules/storage/internal/config/loader_test.go`
- Modify: `modules/storage/config/storage.yaml`

- [ ] **Step 1: 用当前启动快照行为写红灯测试**

证明 Storage View 当前只在启动时 `ListViews(status=active)` 并展开 wildcard route：进程启动后新增 `spot_kline_derived_4h_view` 时，旧 consumer options 不包含 `crypto_market/spot_kline_derived_4h`。测试同时锁定两个安全要求：一个 Dataset 只能属于一个 partition；清单刷新失败时继续保留上一套健康 consumer，不把所有增量消费停掉。

- [ ] **Step 2: 把 wildcard catch-all 改成动态 Dataset consumer 模板**

把 `validateStorageViewConsumerPartitions` 中的 active View 分页扫描、managed Dataset 去重、wildcard 展开和拓扑校验抽成纯度较高的 builder：

```go
type ManagedViewInventory struct {
    Hash     string
    Views    []*storagepb.View
    Datasets []storageconfig.StorageViewConsumerDataset
}

func BuildManagedViewInventory(ctx context.Context, metadata storagepb.MetadataClientProxy, base viewservice.EventConsumerOptions) (ManagedViewInventory, error)
```

Hash 对排序后的 `space_id/dataset_id/view_id/desired_revision` 计算，避免 Metadata 返回顺序变化导致无意义协调。现有 kline/factor/system_metrics 等 exact route 继续使用共享 partition consumer；`misc` 的 `dataset_id: "*"` 不再在启动时展开成一个不可变过滤器，而是作为动态模板：每个未被 exact route 认领的 managed Dataset 使用一个独立 consumer。这样任意新 Dataset 不需要修改已有 durable 的 immutable `FilterSubjects`，也不会与原有 exact partition 重复消费。

- [ ] **Step 3: 实现动态 Dataset consumer 生命周期**

`inventory_reconciler` 默认每 30 秒重新扫描 active View。Hash 不变时 no-op；变化时先执行 route ownership 校验，再为新增 catch-all Dataset 启动一个过滤该 Dataset 四类事件的 consumer：

```text
partition_id = misc:<first-16-hex(sha256(space_id + "\x00" + dataset_id))>
durable      = <configured-misc-durable>-<same-hash>
```

durable 必须满足 JetStream 名称长度和字符限制，并在重启后稳定。Dataset 不再被任何 active View 引用时只停止本地拉取，不立即删除 durable；再次出现时继续绑定，初始 rebuild 负责补齐停用期间的历史。新增 consumer bind 失败不影响已有 exact/dynamic consumer。协调状态受单进程 mutex 保护，但 Metadata RPC、consumer bind 和等待不得持有 View 索引写锁。

首次升级时，旧的共享 `misc` durable 不再绑定；当前所有 catch-all Dataset 都按“新增动态 Dataset”处理，先绑定新的稳定 durable，再执行一次 rebuild，以覆盖部署切换窗口。旧 durable 暂不自动删除，回滚旧版本时仍可继续使用。

为避免 consumer 接入期间丢失目标行，Collector 的规则仍停留在 `waiting_view`，不会写派生 Dataset。新路由 bound 后，协调器使用 Storage View 自身的 Primary 凭据向目标 Dataset 幂等追加 `source="catchup"` 的 route-ready sync point，request ID 使用上一段的确定性格式，然后对新增 View 发起一次初始 rebuild。Collector 只有在 `WaitViewSyncPoint` 和 active revision 两项都 ready 后才激活规则。因此首次写目标行发生在增量路由可用之后，不依赖“先写再补历史”来掩盖事件缺口。

- [ ] **Step 4: 增加严格配置和状态指标**

配置新增：

```yaml
storage:
  view:
    consumer_inventory_refresh_interval: 30s
```

要求 duration 为正且不小于 5 秒。新增低基数指标 `moox_storage_view_inventory_generation`、`moox_storage_view_inventory_datasets`、`moox_storage_view_dynamic_consumers`、`moox_storage_view_inventory_reconcile_total{result}`，并在健康信息中暴露当前 inventory hash、最近成功时间和最近错误；一次刷新失败不直接把 readiness 置失败，连续超过 5 个刷新周期未成功才降级。Dataset ID 不进入 Prometheus label。

- [ ] **Step 5: 覆盖动态创建、失败恢复和关闭竞态**

测试证明：

- 启动后新增派生 View 会创建一个 stable durable 的动态 Dataset consumer；
- 从旧共享 misc consumer 升级时，现有 catch-all View 经 rebuild 后无事件缺口，且旧 durable 保留以支持回滚；
- 相同 inventory 不重复 bind consumer；
- exact route Dataset 不会再被 catch-all 动态 consumer 认领；
- 非法重叠 route 被拒绝且已有 consumer 继续工作；
- Metadata 暂时失败、动态 consumer bind 失败、初始 rebuild 失败均可在下一轮恢复；
- route-ready sync point 在 consumer bound 之后发布，并能由 `WaitViewSyncPoint` 端到端证明目标 View 已消费；
- 服务关闭与 reconcile 同时发生时只关闭一次，不泄漏 forked JetStream client；
- 新 View `active_index_id` 非空且 revision 追平之前，Collector 规则不能进入 ready。

- [ ] **Step 6: 运行绿灯并提交**

Run: `cd modules/storage && go test -count=1 ./cmd/server ./internal/config ./internal/service/view/... -run 'Inventory|ConsumerPartition|DynamicView'`

Expected: PASS，新建 active View 最迟一个 refresh interval 后获得增量消费路由，无需重启 Storage View 进程。

Commit: `feat(storage): reconcile dynamic view consumers`

### Task 5: 实现纯聚合内核、Primary 精确读取和幂等写入

**Files:**
- Create: `modules/collector/internal/klineaggregation/bar.go`
- Create: `modules/collector/internal/klineaggregation/bar_test.go`
- Create: `modules/collector/internal/klineaggregation/storage.go`
- Create: `modules/collector/internal/klineaggregation/storage_test.go`

- [ ] **Step 1: 写 OHLCV 表驱动红灯测试**

至少覆盖 1m -> 4m、30m -> 90m、乱序输入、重复时间、缺最后一根、缺中间一根、字段不全、错误 series tag、负 volume 和 `trade_num` 溢出保护。

核心成功断言：

```go
require.Equal(t, first.Open, got.Open)
require.Equal(t, maxHigh, got.High)
require.Equal(t, minLow, got.Low)
require.Equal(t, last.Close, got.Close)
require.Equal(t, sumVolume, got.Volume)
require.Equal(t, sumQuoteVolume, got.QuoteVolume)
require.Equal(t, sumTradeNum, got.TradeNum)
```

- [ ] **Step 2: 运行红灯并实现纯函数**

Run: `cd modules/collector && go test -count=1 ./internal/klineaggregation -run AggregateBar`

Expected: 先 FAIL，完成后 PASS。

内核接口固定为：

```go
type SourceBar struct { /* key + 7 fields */ }
type AggregatedBar struct { /* target key + 7 fields + InputHash */ }

func ExpectedSourceTimes(start, end time.Time, source FixedFrequency) ([]time.Time, error)
func AggregateBar(spec RuleSpec, subjectID string, start, end time.Time, rows []SourceBar) (AggregatedBar, error)
```

输入哈希按 source `data_time` 排序后编码 key 和 7 个 typed value；浮点数使用 `math.Float64bits`，整数使用定长大端编码，避免依赖 JSON map 顺序。

- [ ] **Step 3: 写 Storage 分片读取红灯测试**

证明：

- 请求只使用 `PrimaryStore.ReadFields`，不从普通 View range read 判断完整性；
- 每次不超过 10,000 keys 且 `keys * 7 <= 100,000`；
- claim 按 `worker_max_source_keys_per_claim` 自适应 subject 数，普通周期最多 50 个 subject，极大周期至少 1 个 subject；keys 可跨请求拆分并正确回组；
- retry 只重新读取缺失 keys；
- `existing_keys` 存在但缺字段仍判定 not_ready。

- [ ] **Step 4: 实现目标哈希比对和批量 Upsert**

写入前先对 ready item 的目标 RowKey 调用一次 `ReadFields`，同时读取 row attributes 中的 `source_hash`。若目标 `source_hash == input_hash`，直接把 item 标记 complete，不再次写 Storage；这使幂等性不依赖 Collector SQLite job 是否仍保留，也不受 Primary `source_event_id` 去重窗口限制。目标不存在或 hash 变化时才进入批量 Upsert。

同一批 ready item 的 `source_event_id` 由下面内容确定性计算：

```text
rule_id + rule_version + bucket_start + sorted(target_row_key + input_hash)
```

批次先按 item ID 排序，再按固定 batch size 切片，保证同一 claim 重试的分组稳定。Storage 响应丢失后，重试先通过目标 `source_hash` 收敛；仍需重发时生成相同 ID。任一源输入修订后生成新 ID并覆盖同一 RowKey。每次写入必须传 `write_source=collector:kline_aggregation`，并写入第 0.4 节规定的 provenance attributes。

- [ ] **Step 5: 运行绿灯并提交**

Run: `cd modules/collector && go test -count=1 ./internal/klineaggregation -run 'AggregateBar|PrimaryRead|IdempotentWrite'`

Expected: PASS。

Commit: `feat(collector): aggregate stored kline windows`

### Task 6: 实现一分钟 due scanner 和持久化 checkpoint

**Files:**
- Create: `modules/collector/internal/klineaggregation/scanner.go`
- Create: `modules/collector/internal/klineaggregation/scanner_test.go`
- Modify: `modules/collector/internal/store/kline_aggregation_job.go`
- Modify: `modules/collector/internal/store/kline_aggregation_job_test.go`

- [ ] **Step 1: 写边界时间红灯测试**

以 `target=4h, settle=10s, origin=epoch UTC` 验证：

```text
tick 03:59:59 -> [00:00,04:00) 不到期
tick 04:00:09 -> effective_now 03:59:59，不到期
tick 04:00:10 -> effective_now 04:00:00，到期一次
tick 04:01:10 -> 不重复创建
服务停机至 12:00:10 -> 有界补建 [04,08) 和 [08,12)
```

另覆盖 `7m` 跨 UTC 午夜、`90m`、自定义历史 checkpoint 和 disabled/provision_error 规则。

- [ ] **Step 2: 运行红灯**

Run: `cd modules/collector && go test -count=1 ./internal/klineaggregation -run Scanner`

Expected: FAIL，因为 scanner 尚不存在。

- [ ] **Step 3: 实现 scanner**

算法固定为：

```text
effective_now = now - settle_delay
while next_bucket_start + target_duration <= effective_now:
    EnsureBucketJob(rule, next_bucket_start, realtime)
    next_bucket_start += target_duration
    stop when max_buckets_per_scan reached
```

同一 transaction 中成功插入/确认 job 后才能推进 rule checkpoint。scanner 每轮额外对最近 N 个闭合桶调用 `RequeueRecentBuckets`，但 input hash 相同的 complete item 在 worker 中直接结束，不写 Storage。

- [ ] **Step 4: 固定 scanner 不做的事**

测试通过 fake 证明 scanner 不调用 `ReadFields`、`UpsertFields`、`ReportDatasetPeriodCollected`，也不调用 `time.Sleep`。一次 scan 超过 deadline 时保留未推进 checkpoint，让下一 tick 继续。

- [ ] **Step 5: 运行绿灯并提交**

Run: `cd modules/collector && go test -count=1 ./internal/klineaggregation ./internal/store -run 'Scanner|Checkpoint|RecentRepair'`

Expected: PASS。

Commit: `feat(collector): schedule due kline aggregation buckets`

### Task 7: 实现本地 worker、指数退避和完整周期 marker

**Files:**
- Create: `modules/collector/internal/klineaggregation/worker.go`
- Create: `modules/collector/internal/klineaggregation/worker_test.go`
- Modify: `modules/collector/internal/klineaggregation/storage.go`
- Modify: `modules/collector/internal/store/kline_aggregation_job.go`

- [ ] **Step 1: 写 worker 状态机红灯测试**

覆盖：

```text
pending -> running -> complete
pending -> running -> not_ready(next minute) -> running -> complete
running process crash -> stale reclaim -> complete
permanent invalid row -> failed without partial target write
some subjects ready -> ready rows written, missing subjects remain not_ready
all expected items complete -> marker pending -> marker reported -> job complete
```

- [ ] **Step 2: 运行红灯**

Run: `cd modules/collector && go test -count=1 ./internal/klineaggregation -run Worker`

Expected: FAIL，因为 worker 尚不存在。

- [ ] **Step 3: 实现有界 worker**

- 单实例并发默认 2 个 bucket batch。
- 每次 claim 最多 50 个 subject items，并满足 `claimed_subjects * source_rows_per_subject <= worker_max_source_keys_per_claim`；当单 subject 已超过该预算时仍允许 claim 1 个，再拆成多个 `ReadFields` 请求。
- 排序为 priority DESC、bucket_start ASC、item ID ASC；实时工作优先于回填，但不能清空低优先级状态。
- 每个 batch 有独立 30 秒 context；预留最后 5 秒给 Storage write。
- worker 使用 buffered notify channel 加 5 秒数据库轮询兜底，进程重启后无需等待下一分钟 timer。

- [ ] **Step 4: 使用 retry-go 实现短等待**

完整性缺失时执行初次读取加 4 次重读，退避 `250ms, 500ms, 1s, 2s`。必须设置 `retry.Context(ctx)`；只对 not_ready 重试，schema/value 错误直接失败，Storage 网络错误由 Storage adapter 的既有重试处理。

短退避耗尽后：

```text
item.state = not_ready
item.next_retry_at = next whole minute + 5 seconds
job.state = waiting
```

不得 busy spin，不得在 SQLite transaction、scanner overlap guard 或现有 marketfetch Scheduler/Reconciler 锁内等待。跨 tick not_ready 可继续重试，直到缺失窗口的最早源 RowKey 已越过 source Dataset keep_duration；此时标记 `failed/source_expired`，不再制造无意义重试，并提示用户先恢复源数据再发起回填。

- [ ] **Step 5: 完成 marker 规则**

- 只有 origin 为 realtime 且所有 expected item complete 才报告 complete marker。
- marker 的 `period_time` 使用 bucket start Unix seconds。
- marker RPC 成功后再把 `marker_state` 改为 reported；响应丢失后允许用同一 payload 重试。
- 复用现有 `ensureDatasetPeriodMarkerAccepted` 语义：只有这次 terminal complete `ReportDatasetPeriodCollected` 返回的显式 conflict 视为相同逻辑周期已完成；其他 Storage RPC 的 conflict 不得吞掉。
- not_ready/failed 时不发布 degraded marker。
- repair/backfill 重写已存在行时不重复 marker。

- [ ] **Step 6: 运行绿灯并提交**

Run: `cd modules/collector && go test -count=1 ./internal/klineaggregation ./internal/store -run 'Worker|Retry|Marker|StaleClaim'`

Expected: PASS。

Commit: `feat(collector): execute kline aggregation jobs`

### Task 8: 实现管理 RPC、规则准备和历史回填

**Files:**
- Create: `modules/collector/internal/rpc/kline_aggregation.go`
- Create: `modules/collector/internal/rpc/kline_aggregation_test.go`
- Modify: `modules/collector/internal/rpc/service.go`
- Modify: `modules/collector/internal/rpc/service_validation_test.go`
- Create: `modules/collector/internal/klineaggregation/backfill.go`
- Create: `modules/collector/internal/klineaggregation/backfill_test.go`

- [ ] **Step 1: 写 Create/Update 规则红灯测试**

Create 流程必须证明：

1. 接受用户输入的 `4h/240m` 等别名，落库和 Metadata 统一为 Storage 规范值 `4H`，命名使用 slug `4h`，并校验 source/target 整除；
2. 生成或校验 target Dataset/View ID；
3. 拒绝相同 target Dataset 的第二个启用规则；
4. 先落 pending rule，再调用 catalog coordinator；
5. catalog 成功后先置 `waiting_view`，等待确定性 route-ready sync point 和初始 View rebuild 两项 ready 后再置 ready，并把初始 checkpoint 设为最近 3 个闭合桶的最早 bucket start；
6. catalog 失败时保留 error rule 和可操作错误，不生成 job；
7. Update 只接受 settle delay、repair lookback 和 enabled。

- [ ] **Step 2: 写 backfill 红灯测试**

请求合同：

```protobuf
message CreateKlineAggregationBackfillReq {
  string space_id = 1;
  string rule_id = 2;
  string request_id = 3; // 调用方生成的必填幂等键
  string start_time = 4; // inclusive RFC3339 UTC
  string end_time = 5;   // exclusive RFC3339 UTC
  bool publish_markers = 6; // V1 前端固定 false
  string creator = 7;
}
```

校验 start/end 精确对齐 target 网格、end 已闭合、start < end、桶数不超过 10,000，并按 source Dataset keep_duration 拒绝已确定无法读取的区间。重复相同 request ID 不重复建 job。

- [ ] **Step 3: 运行红灯**

Run: `cd modules/collector && go test -count=1 ./internal/rpc ./internal/klineaggregation -run 'KlineAggregationRule|Backfill'`

Expected: FAIL，RPC handler 和 backfill service 尚不存在。

- [ ] **Step 4: 实现查询和分页**

List rules/jobs/backfills 使用现有 `common.Page/PageResult`，允许按 rule、target Dataset、status、origin 和时间范围过滤。Job 详情返回缺失 subject 数和最多 100 条错误摘要，不把所有 item 塞进列表响应。

- [ ] **Step 5: 实现 backfill 完成同步**

backfill 所有 job complete 后：

1. `AppendDatasetSyncPoint(source="catchup", request_id=backfill_id)` 保存聚合来源；Storage 当前只接受 `import|catchup`，不得发明新的 source 枚举；
2. 保存 sync point ID；
3. 使用现有 `WaitViewSyncPoint` 有界等待目标 View；
4. ready 时标记 complete；超时时标记 partial_failed 并保留可重试的 sync point，不重写已完成 K 线。

- [ ] **Step 6: 运行绿灯并提交**

Run: `cd modules/collector && go test -count=1 ./internal/rpc ./internal/klineaggregation`

Expected: PASS。

Commit: `feat(collector): manage kline aggregation and backfills`

### Task 9: 接入独立 timer、bootstrap、配置、指标和健康信息

**Files:**
- Modify: `modules/collector/config/trpc_go.yaml`
- Modify: `modules/collector/config/app.yaml`
- Modify: `modules/collector/internal/bootstrap/config.go`
- Modify: `modules/collector/internal/bootstrap/config_test.go`
- Modify: `modules/collector/internal/bootstrap/bootstrap.go`
- Modify: `modules/collector/internal/bootstrap/bootstrap_test.go`
- Modify: `modules/collector/internal/observability/realtime_inventory.go`
- Modify: `modules/collector/internal/observability/realtime_inventory_test.go`
- Create: `modules/collector/internal/klineaggregation/metrics.go`
- Create: `modules/collector/internal/klineaggregation/metrics_test.go`

- [ ] **Step 1: 写严格配置红灯测试**

新增配置：

```yaml
kline_aggregation:
  enabled: true
  scan_timeout: 8s
  worker_concurrency: 2
  worker_subject_batch_size: 50
  worker_job_timeout: 30s
  worker_poll_interval: 5s
  worker_max_source_keys_per_claim: 20000
  max_buckets_per_scan: 24
  stale_running_after: 2m
  default_settle_delay: 10s
  default_repair_lookback_buckets: 3
  target_keep_duration: 4320h
  completed_job_retention: 168h
  completed_backfill_retention: 2160h
  cleanup_interval: 1h
```

验证所有 duration/数量为正，subject batch 不超过 200，`worker_max_source_keys_per_claim >= 10,000`，repair lookback 为 `1..10`，未知 YAML 字段继续失败。

- [ ] **Step 2: 注册独立 timer**

在 `trpc_go.yaml` 增加：

```yaml
- name: trpc.moox.collector.kline_aggregation.timer
  port: 11415
  network: "10 * * * * *"
  protocol: timer
  timeout: 10000
```

bootstrap 使用 `timerjob.New("collectorKlineAggregation", scan_timeout, scanner.Scan)` 注册同步 handler。不得复用当前 `collector.schedule.timer` 的 goroutine callback。

- [ ] **Step 3: 装配本地 worker 生命周期**

初始化顺序固定为 Store/schema -> Storage clients/catalog -> repositories -> worker -> scanner -> RPC service -> timer。初始化失败不启动 server；关闭时先停止 claim，再等待 in-flight job context 结束，最后关闭 SQLite。

scanner 同一生命周期内维护一个持久化 cleanup checkpoint，每到 `cleanup_interval` 调用 repository 分批删除过期 complete 记录，每批最多 1,000 个 parent 并级联 items。清理不与 K 线读取或 Storage 写入共用 transaction。

- [ ] **Step 4: 增加低基数指标**

```text
moox_collector_kline_aggregation_jobs_total{origin,result}
moox_collector_kline_aggregation_pending_jobs{space_id,target_dataset_id}
moox_collector_kline_aggregation_not_ready_items{space_id,target_dataset_id}
moox_collector_kline_aggregation_bucket_lag_seconds{space_id,target_dataset_id}
moox_collector_kline_aggregation_source_rows_missing_total{target_dataset_id}
moox_collector_kline_aggregation_duration_seconds{stage}
```

禁止把 rule ID、subject ID、error 文本放进 Prometheus label。健康接口展示 worker 是否启动和最老 pending bucket age，但 backlog 不直接让 Collector readiness 失败。

- [ ] **Step 5: 扩展实时预期清单**

`RealtimeInventory` 除现有启用中的 scheduled Kline TaskRule 外，还读取 `provision_state=ready && enabled=true` 的聚合规则，把 `(target_dataset_id, target_frequency)` 加入期望清单并按 Storage 规范频率去重。创建、启用、禁用或准备状态变化后调用 `MarkDirty`；刷新失败保留上一快照。测试覆盖原生 `4H` 采集和派生 `4H` Dataset 同时存在但 Dataset ID 不同，以及 disabled/waiting_view 规则不进入清单。

- [ ] **Step 6: 运行绿灯并提交**

Run: `cd modules/collector && go test -count=1 ./internal/bootstrap ./internal/klineaggregation`

Expected: PASS，重复 timer 调用由 `timerjob.Job` 跳过 overlap，worker 仍可继续执行。

Commit: `feat(collector): run kline aggregation worker`

### Task 10: 增加管理台 K 线聚合页签

**Files:**
- Create: `web/src/api/collector/kline-aggregation.ts`
- Create: `web/src/api/collector/kline-aggregation.test.ts`
- Create: `web/src/views/collector/kline-aggregation/index.vue`
- Create: `web/src/views/collector/kline-aggregation/rule-form.vue`
- Create: `web/src/views/collector/kline-aggregation/backfill-dialog.vue`
- Create: `web/src/views/collector/kline-aggregation/kline-aggregation.test.ts`
- Modify: `web/src/views/collector/task-management/index.vue`
- Modify: `web/src/views/collector/task-management/task-management.test.ts`

- [ ] **Step 1: 写表单和路由红灯测试**

测试断言“采集任务”下存在 `K线聚合` tab，并验证 Create request 精确包含 source Dataset/frequency/series tag、target frequency、自动 Dataset/View ID、settle delay 和 repair lookback。

- [ ] **Step 2: 实现创建表单**

控件固定为：

- source Dataset：仅显示 active time_series Dataset；
- source frequency：从所选 Dataset `freqs` 选择；
- source series tag：文本输入，默认按 DataSource 建议 `venue:<data_source_id>`；
- target period：正整数输入 + `分钟/小时/天` 单位选择；
- target Dataset/View：实时预览，可在创建前编辑 Dataset ID；
- settle delay：秒数 stepper，默认 10；
- repair lookback：桶数 stepper，默认 3；
- enabled：switch。

不使用固定周期下拉选项，不暴露 calendar/session alignment。

- [ ] **Step 3: 实现列表与状态操作**

列表显示 source -> target、周期比、准备状态、enabled、next bucket、最老 pending、最近错误。命令仅包括启用/禁用、重试准备、查看 jobs、创建回填；source/target identity 不提供编辑入口。

- [ ] **Step 4: 实现历史回填对话框**

使用 UTC 时间范围，前端先检查对齐并展示将创建的桶数；默认 `publish_markers=false` 且 V1 不显示可修改开关。提交后展示 backfill ID，通过列表轮询状态，不在浏览器中逐桶发请求。

- [ ] **Step 5: 运行绿灯和生产构建**

Run: `pnpm --dir web exec vitest run src/api/collector/kline-aggregation.test.ts src/views/collector/kline-aggregation/kline-aggregation.test.ts src/views/collector/task-management/task-management.test.ts`

Run: `pnpm --dir web run build:prod`

Expected: PASS，无类型或布局错误。

Commit: `feat(web): manage kline aggregation rules`

### Task 11: 增加端到端数据正确性和恢复测试

**Files:**
- Create: `modules/collector/test/kline_aggregation_e2e_test.go`
- Create: `scripts/tests/e2e/test-kline-aggregation.sh`
- Modify: `Makefile`

- [ ] **Step 1: 增加模块内 E2E**

用真实 Collector SQLite 和可控 Storage fake 完成：

1. 写入 BTC/ETH 四根完整 1h 源 K 线；
2. scanner 在 04:00:10 创建一个 4h job；
3. worker 生成 `spot_kline_derived_4h` 两行；
4. 验证 OHLCV、`RowKey.freq=4H`、series tag、provenance attributes 和 source event ID；
5. 重复 tick 不增加写次数；
6. 修改第二根源 high 后 recent repair 覆盖相同目标 RowKey；
7. 缺 ETH 最后一根时先只写 BTC，不发 marker；补齐后写 ETH 并发一个 complete marker；
8. 模拟 worker crash 后 stale reclaim 收敛。

- [ ] **Step 2: 增加真实 Storage E2E**

脚本启动临时 Storage Metadata/Primary/DataNode、EventBus 和 Collector：

- 通过 Collector RPC 创建规则并自动创建 Dataset/View；
- 等待 Storage View 动态 inventory 包含目标 Dataset、route-ready sync point 被目标 View 消费且初始 rebuild 激活，规则从 waiting_view 进入 ready；
- 向 Primary 写 1m 源行；
- 触发 scanner/worker；
- 从目标 Primary 和 View 分别读取结果；
- 创建历史回填并等待 sync point；
- 校验目标 Dataset metadata 的 `freqs=[4H]`、7 列、`View filter={"freq":"4H"}` 和 subject binding。

- [ ] **Step 3: 接入质量门禁**

新增 `make test-kline-aggregation`，并接入 `verify-pr`。脚本必须有总超时、trap 清理临时目录/进程，不依赖外部 Binance 或腾讯云。

- [ ] **Step 4: 运行完整证明集**

Run: `cd modules/collector && go test -count=1 ./...`

Run: `make test-kline-aggregation`

Run: `./scripts/test-go-workspace.sh`

Run: `make verify-pr`

Run: `pnpm --dir web test && pnpm --dir web run build:prod`

Run: `git diff --check`

Expected: 全部 PASS。

Commit: `test(collector): verify kline aggregation pipeline`

### Task 12: 文档、codeCR、灰度与回滚验收

**Files:**
- Modify: `docs/内置市场行情采集架构.md`
- Modify: `docs/SUMMARY.md` only if the final documentation navigation requires a new standalone page
- Modify: `docs/operations/monitoring.md`
- Modify: implementation files only when review or acceptance exposes defects

- [ ] **Step 1: 更新用户和运维文档**

文档必须明确：

- 原生交易所 `4h` 采集与 `1h -> 4h` 自主聚合是两条不同链路；
- 任意周期的整除、UTC epoch 对齐和最大展开比例；
- `spot_kline_derived_4h` 命名规则，以及 ID slug `4h` 与 Storage frequency `4H` 的区别；
- scanner、worker、短退避、跨 tick not_ready 和历史回填；
- 运行时新增 View 的 inventory reconcile、初始 rebuild 和 waiting_view 准备状态；
- complete marker、最近修订和历史 Factor Recalc 限制；
- source retention 决定可回填范围。

- [ ] **Step 2: 启动全新的 codeCR Agent**

审查本计划全部实现，重点检查：

- bucket 边界和 off-by-one；
- 缺行时是否错误写部分 subject bar；
- timer/worker 是否在锁或 transaction 内等待；
- Storage exact-read 限额；
- source_event_id 是否会让同 Dataset 的不同写批次互相去重；
- marker 是否可能过早发布；
- Dataset/View 部分准备失败能否幂等恢复；
- backfill 是否会饿死 realtime；
- series tag 和 target Dataset 唯一性；
- 测试是否覆盖重启、重复 tick、迟到修订和 marker 冲突。

codeCR 结论必须附文件、符号或行号。主 Agent 独立核验每条 finding，修复后重跑 Task 11 全部证明。

- [ ] **Step 3: 灰度前备份与开关**

上线前备份 Collector SQLite。首次部署保持 `kline_aggregation.enabled=false`，确认 schema、RPC、目标 Metadata 权限和 metrics 正常后再开启。先创建一个 `binance_spot_kline_1m -> spot_kline_derived_4h` 规则，只选择少量测试 subject 的 source Dataset 副本。

- [ ] **Step 4: 连续三个 4h 桶验收**

每个桶核对：

- 源行数量精确为 `subject_count * 240`；
- 目标 Primary 和 View 行数等于 subject_count；
- 随机 5 个 subject 的 7 个字段与离线计算一致；
- 只有一个 complete marker；
- pending/not_ready 最终归零；
- bucket lag、worker duration 和 Storage 错误无持续异常。

- [ ] **Step 5: 历史回填验收**

回填最近 7 天的 4h 桶，确认任务总数、完成数、目标行数、sync point 和 View 水位一致。重复发起相同区间不产生不同值，修改一根源 K 后强制回填只覆盖受影响目标 RowKey。

- [ ] **Step 6: 回滚方案**

出现异常时先关闭聚合规则和全局配置开关，停止新 claim；等待 in-flight context 结束后回滚 Collector 二进制。保留派生 Dataset、View 和聚合 SQLite 表用于排查，不删除源 K 线，不影响现有 Binance 原生周期采集。确认修复后可从 checkpoint/历史回填继续，无需重建原始数据。

- [ ] **Step 7: 提交、推送和发布**

Run: `git status --short && git log -n 12 --oneline`

Run: `git push`

Expected: 实施任务均为独立可回退 commit，远端分支包含最终 codeCR 修复和文档；正式发布只在 Task 11 全量证明及灰度验收通过后进行。

## 5. 明确不纳入 V1

- 交易所 session calendar、节假日和时区日线。
- 秒级 target period。
- `W/M/Y` 日历周期或 `1M=30d` 近似。
- 派生 Dataset 作为另一个聚合规则的 source。
- 多 source/provider 同写一个 target Dataset。
- 分布式 worker、多实例抢占、独立 DLQ、全局 exactly-once。
- 修改已发布 `DatasetPeriodCollected` marker 或自动重算历史 Factor。
- 在现有腾讯 Timer SCF 中拉取源 K 线并聚合。

## 6. 完成标准

只有同时满足以下条件，功能才算完成：

1. 用户可创建 `1m -> 7m`、`30m -> 90m`、`1H -> 4H` 等合法规则，并得到带小写周期后缀的独立 target Dataset/View；Metadata、RowKey 和 View filter 使用 Storage 规范频率。
2. 非整除、日历周期、派生链和冲突 target Dataset 在创建阶段被明确拒绝。
3. 全局 1m timer 只建立持久化工作，不在 callback 中等待源 K 线。
4. 缺少最后一根或任意中间源 K 时不会写该 subject 的部分目标 K 线；短退避和下一 tick 重试最终可收敛。
5. 重复 tick、RPC 超时、Collector 重启和 Storage 响应丢失不会产生不同结果。
6. 历史回填与实时计算使用同一 `AggregateBar` 和 Storage writer。
7. 最近桶源数据修订会以 input hash 变化覆盖目标行；更老修订可通过显式回填修复。
8. 目标 complete marker 只在实时桶全部 subject 成功后发布一次。
9. 运行时创建的目标 View 无需重启 Storage View 进程即可进入动态消费清单；路由和初始 rebuild 未 ready 前 Collector 不写目标数据。
10. 模块测试、真实 Storage E2E、Workspace tests、`verify-pr`、Web tests/build 和独立 codeCR 全部通过。
11. 小范围灰度连续三个目标桶及一次 7 天历史回填完成数据对账。
