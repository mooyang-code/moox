# MooX 可配置 K 线重采样 Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 在 Collector 中增加由已落盘 K 线 Dataset 生成任意整分钟周期 K 线的重采样能力。用户选择一个当前 active 的结果 K 线 Dataset 作为输入，并把结果写入带周期后缀的独立 Dataset，例如 `dataset_spot_kline_derived_4h`。

**Architecture:** 复用 Collector 现有 `TaskRule -> TaskInstance -> PeriodReadiness` 控制面，不新增规则、Job、Item 或 Backfill 配置表。`data_type=kline_resample` 的 TaskRule 保存声明式配置；每个 subject 对应一个 TaskInstance，其 `c_result` 保存可恢复游标、lease、重试和当前历史回填状态。独立一分钟 scanner 只选择到期 TaskInstance，本地 worker 从 Storage Primary 精确读取完整源窗口，执行 OHLCV 重采样并幂等写入目标 Dataset。

**Tech Stack:** Go 1.25、tRPC-Go Timer、SQLite/GORM、Storage Metadata/PrimaryStore RPC、`github.com/avast/retry-go`、NATS JetStream、Prometheus、Vue 3、TypeScript、Vitest。

> **执行状态（2026-08-29）：** Collector 控制面、resample worker、PeriodReadiness、动态 View 接入、规则页面、模块级 E2E、生产二进制升级及真实 5m 目标 View 查询已完成。生产验证已覆盖历史桶与源数据持续到达后的新实时桶：`dataset_spot_kline_derived_5m` 的 BTC-USDT 任务从 13:25 推进到 13:40，Primary 与 View 的最新 bucket 一致；源数据缺失的其他历史桶仍按合同保持 `waiting_source` 并自动重试。旧的卡住规则已禁用，隔离验证规则 `builtin-binance-spot-kline-resample-e2e-5m` 保持 `enabled/ready`。

**最终验证记录（2026-08-29）：**

- 本地发布包 `release/moox-20260829-resample-final3-linux-amd64.tar.gz` 已生成，包含 Collector、Storage、EventBus 等运行时二进制。针对最新提交，另从干净 `git archive HEAD` 编译并发布了 Collector、EventBus、Storage Primary 三个正式 Linux/amd64 二进制；生产 Collector/EventBus 的 SHA-256 分别为 `699a6f99f307490799ec01c6a896365f315d8c23d899dac40be0a2ec68310858`、`98ece8e866699de5a82c94753ca5b44d88dd64f4d7efdfc55f221285c0a25474`。完整 release 脚本的 final4 重试被共享工作区中与本变更无关的 Factor/storage 生成文件 import cycle 阻断，因此没有把失败的全量包宣称为发布产物；已部署的三项二进制和现有 CGO-enabled Storage View 均完成远端 hash/health 校验。
- 生产健康检查显示 Collector、Storage Primary/View/Node、EventBus 等就绪；Storage View 继续使用 CGO-enabled 构建，避免 DuckDB 运行时能力缺失。
- 真实 5m 校验：最新闭合源窗口 `[13:40,13:45)` 恰有 5 根 1m K 线，聚合 OHLCV 为 `open=77662.01, high=77662.01, low=77662, close=77662, volume=4.01431, quote_volume=311759.36915519997`；`dataset_spot_kline_derived_5m` Primary 行逐字段相等，目标行 `trade_num=981`。任务结果为 `idle`，`last_success_bucket=2026-08-29T13:40:00Z`，下一实时桶为 `13:45`。View smoke 读到 5 行，Primary/View 最新时间均为 `2026-08-29T13:40:00Z`，最新行字段完全一致。

---

## 0. 已确认决策

### 0.1 术语合同

- 中文功能名统一为“**K线重采样**”。
- Collector data type 使用 `kline_resample`，本地 provider 使用 `moox`。
- Collector 内部 Go package 使用 `resample`，核心函数使用 `Bars`，结果类型使用 `Result`。
- 持久化 data type、RPC、YAML、Timer、指标、日志和事件标识使用 `kline_resample`，保留 K 线业务边界；内部包名不重复业务类型。
- Dataset 名称保留已经确认的派生语义：`dataset_spot_kline_derived_4h`、`swap_kline_derived_90m`。`derived` 表示数据来源性质，`resample` 表示生成方式，两者不冲突。

### 0.2 Source Dataset 与 DataSource ID

- 创建重采样规则时，用户选择的是 `source_dataset_id`，不是 Storage 的 `data_source_id`。
- Source 下拉列表来自当前 Space 的 Dataset，仅显示 `status=active && data_kind=time_series` 且具备标准 K 线字段的结果 Dataset。
- Source Dataset 可以是交易所直接采集结果，例如 `dataset_binance_spot_kline_1m`，也可以是已经存在的统一行情结果，例如 `dataset_spot_kline_1h`。
- V1 不允许把 `attributes.dataset_role=kline_resample_result` 的目标 Dataset 再作为另一个重采样规则的 source，避免链式依赖、循环和级联修订。
- Source Dataset 本身不写入任何下游重采样配置；一个 source 可以被多个 4H、6H、90m 规则独立引用。
- Target Dataset 的直接生产者是 MooX 内部计算，因此 `data_source_id` 不机械继承 source。`crypto` Space 使用当前 active、`kind=internal` 的 `data_source_id=crypto`。
- 原始交易场所继续由 `series_tag` 表达，例如 `venue:binance`；source DataSource ID 作为 target lineage attribute 保存。

### 0.3 配置归属

完整可执行配置归现有 `t_collector_task_rules`：

```json
{
  "data_type": "kline_resample",
  "provider": "moox",
  "market_type": "spot",
  "collect_params": {
    "source_dataset_id": "dataset_binance_spot_kline_1m",
    "source_frequency": "1m",
    "source_series_tag": "venue:binance",
    "target_dataset_id": "dataset_spot_kline_derived_4h",
    "target_frequency": "4H",
    "alignment": "epoch_utc",
    "settle_delay_ms": 10000
  }
}
```

Target Dataset attributes 只保存不可变血缘镜像，不作为 scheduler 配置真值：

```yaml
attributes:
  owner_module: collector
  managed_by: collector
  dataset_role: kline_resample_result
  resample_rule_id: <rule_id>
  source_dataset_id: dataset_binance_spot_kline_1m
  source_data_source_id: binance
  source_freq: 1m
  source_series_tag: venue:binance
  target_freq: 4H
  alignment: epoch_utc
```

enabled、settle delay、全局 repair lookback、retry、lease 和 backfill 状态不得写入 Dataset attributes。Metadata revision 是数据定义版本，不承担每分钟执行 checkpoint。`dataset_role` 在 V1 只用于分类、血缘和禁止链式重采样，不代表 Storage 写权限。

同一个 target Dataset 只允许一个 enabled resample Rule，避免两个执行配置覆盖相同 RowKey，并避免相同 Dataset/frequency/period 的 marker 所有权冲突。

### 0.4 数据库复用边界

V1 **不新增数据库表**：

| 现有表 | 重采样用途 |
| --- | --- |
| `t_collector_task_rules` | 一条 `kline_resample` Rule 保存一个 source、target 和目标周期 |
| `t_collector_task_instances` | 每个 `rule + target Dataset + subject + series_tag + target frequency` 一个稳定任务 |
| `t_period_readiness` | 一个实时 target Dataset/frequency/period 的完整性和 marker 状态 |
| `t_period_readiness_items` | 当期预期 subject TaskInstance 快照及成功状态 |

只扩展现有字段和状态：

- TaskRule 只增加 `prepare_state` 和 `last_error`，不新建 resample rule 表或配置 hash 列；
- TaskInstance 继续使用 `c_result` 保存小型、版本化运行状态，不新建 progress/job/item 表；
- PeriodReadiness 增加 `work_type`，区分现有 collection 与 resample 的失败和 marker 语义；
- 历史回填状态保存在各 TaskInstance 的 `c_result.backfill`，不新建 backfill run 表。

这种取舍意味着：一个 subject 同时最多执行一个 target bucket，同一规则同时最多有一个历史回填，不提供永久逐 bucket 审计列表。个人量化规模优先使用简单、可恢复的顺序执行；以后确实需要多 bucket 并行时，再单独引入一张 `t_collector_resample_jobs`，不提前建设。

### 0.5 周期合同

- 用户可输入任意正整数分钟周期，并可用 `m/h/d` 表示。
- 同一周期有两种表示：Storage/RowKey 使用规范值，例如 `4H`、`1D`、`90m`；Dataset ID suffix 和 UI 展示使用小写 `4h`、`1d`、`90m`。
- 目标周期必须大于源周期且满足 `target_duration % source_duration == 0`。
- 单个目标桶最多展开 10,080 根源 K 线；目标周期最长 30 天。
- 固定周期采用 Unix epoch UTC 网格。`7m` 是连续 7 分钟桶，不保证每天 UTC 00:00 重置。
- V1 不支持股票 session、节假日、DST、秒级周期及 `W/M/Y` 日历周期。
- `settle_delay_ms` 为非负整数，最大 24 小时；未填写时使用进程级默认值，显式填写 `0` 表示不等待。
- Storage 周期单位区分大小写：`m` 是分钟，`M` 是月份；重采样只接受 `m/h/d` 的固定整分钟周期。

### 0.6 Source Dataset 校验

Source 必须同时满足：

1. active、time_series，与 target 位于同一 Space；
2. `freqs` 精确包含所选 Storage source frequency；
3. active columns 包含 `open/high/low/close/volume/quote_volume/trade_num`；
4. `attributes.market_type` 与 Rule 一致；
5. 用户明确选择一个 `source_series_tag`，V1 不自动把同一 subject 的 Binance/OKX 流混在一起；
6. source 与 target Dataset ID 不同，source 不是 resample 输出；
7. source keep_duration 足以覆盖用户请求的历史回填区间。

### 0.7 K 线计算合同

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

`raw_payload` 不进入目标 Dataset。目标 row attributes 写入 `resample_rule_id`、`source_dataset_id`、`source_freq`、`source_window_end` 和 `source_hash`。任意预期源行不存在、字段不全、时间不连续或 series tag 不匹配时，不写该 subject 的部分目标 K 线。

### 0.8 实时、修订和历史语义

- 新规则首次扫描从全局 `repair_lookback_buckets` 指定的最近 N 个已闭合目标桶开始。
- 每分钟按照 Collector 全局 `kline_resample.repair_lookback_buckets` 重新检查最近 N 个闭合桶；值为 `0` 时关闭自动近期修订。target `source_hash` 相同则不写，变化时覆盖同一 RowKey。
- 实时周期只有全部预期 subject 成功后才发布一次 complete marker；缺行和永久失败不发布 degraded marker。
- 历史回填不发布逐周期 marker；全部 TaskInstance 完成后追加 `source=catchup` 的 sync point 并等待 View 追平。
- 已发布 marker 后发生近期修订只更新 Primary/View，不重写 marker；历史 Factor 由显式 Recalc 处理。

## 1. 目标数据流

```text
用户在现有采集规则页面选择“K线重采样”
  -> 选择 active source Kline Dataset + source frequency + series tag
  -> 填写 target period，预览 dataset_spot_kline_derived_4h
  -> CreateTaskRule(data_type=kline_resample, provider=moox)
  -> Collector 幂等创建 target Dataset/columns/View
  -> Storage View 动态接入 target，并消费 route-ready sync point
  -> Rule prepare_state=ready
  -> Planner 按 source active subjects 生成/更新 TaskInstances

每分钟第 10 秒（上一次 tick 未结束时跳过，不排队）
  -> scanner 在 scan_timeout 内计算已闭合 target bucket
  -> 优先 claim realtime due TaskInstance
  -> 其次检查 recent repair（每 subject 每 tick 至多一个桶）
  -> 最后 claim backfill TaskInstance
  -> 单次 tick 的总 claim 不超过 max_claims_per_tick
  -> timer callback 返回

本地 worker
  -> 从 TaskInstance.c_result 恢复 active bucket/lease/attempt
  -> PrimaryStore.ReadFields 精确读取源 RowKeys
  -> 缺行时 250ms/500ms/1s/2s context-aware 退避
  -> resample.Bars
  -> 比较目标 row source_hash
  -> hash 变化才 Upsert target
  -> CAS 推进 TaskInstance 游标
  -> realtime item 成功后更新 PeriodReadiness

历史回填
  -> StartKlineResampleBackfill 为本 Rule 的 TaskInstances 设置同一 request/range
  -> 接口拒绝尚未闭合（含 settle delay）或超出 source Dataset keep_duration 的范围；keep_duration=0 表示永久保留
  -> realtime 始终优先，空闲时每个 subject 顺序推进 backfill cursor
  -> 请求开始后的新增 subject 不加入当前 request 快照，不阻塞当前回填完成
  -> 所有 subject 到达 end 后追加 catchup sync point
  -> WaitViewSyncPoint ready 后批量完成 backfill 状态
```

## 2. 持久化合同

### 2.1 TaskRule 扩展

`t_collector_task_rules` 增加：

```text
c_prepare_state    # pending/waiting_view/ready/error；普通采集规则固定 ready
c_last_error
```

一条 resample Rule 只对应一个 target Dataset/target frequency。source Dataset、source frequency、source series tag、target Dataset、target frequency 和 alignment 从 Rule 创建成功起就不可原地修改；变更这些语义需创建新 Rule 和新 target Dataset。Update RPC 必须读取旧 Rule，对规范化后的不可变字段逐项比较；只允许修改 enabled 和 settle delay。repair lookback 是 Collector 全局运行策略，不属于 Rule 身份。

`rule_id` 是 Rule 唯一的语义身份，禁用后的 ID 不得复用于另一套不可变配置，因此 V1 不保存第二个配置版本或配置 hash。目标 row 的 `source_hash` 仍保留，它描述源窗口内容而不是 Rule 配置，用于发现源 K 线修订并保证覆盖写幂等。

### 2.2 TaskInstance 身份与 Result

`t_collector_task_instances` 继续保存：

```text
c_dataset_id       = target Dataset
c_subject_id       = source subject
c_frequency        = target Storage frequency
c_function_name    = collector_local_resample
c_task_params      = 规范化 Rule collect_params
```

Resample StableTaskID 额外包含 `source_series_tag`。现有 kline/symbol TaskID 不变化。

`c_result` 使用带版本的严格 JSON：

```json
{
  "schema_version": 1,
  "state": "idle|running|waiting_source|failed|disabled",
  "state_version": 12,
  "active_origin": "realtime|repair|backfill",
  "active_bucket": "2026-08-29T00:00:00Z",
  "lease_until": "2026-08-29T04:02:00Z",
  "attempt": 2,
  "next_retry_at": "2026-08-29T04:01:05Z",
  "realtime_next_bucket": "2026-08-29T04:00:00Z",
  "last_success_bucket": "2026-08-29T00:00:00Z",
  "last_error": "",
  "backfill": {
    "request_id": "bf-20260829-001",
    "start": "2026-08-01T00:00:00Z",
    "end": "2026-08-08T00:00:00Z",
    "next_bucket": "2026-08-03T08:00:00Z",
    "state": "running|waiting_source|syncing|complete|canceled|failed"
  }
}
```

Repository 对 `state/state_version/lease_until` 做条件更新，禁止普通 `Save` 整块覆盖并发状态。超过 lease 的 running task 可由同一实例重启后回收。

### 2.3 PeriodReadiness 复用

`t_period_readiness` 增加：

```text
c_work_type        # collection/resample
```

状态扩展为：

```text
c_status       waiting/complete/degraded/failed
c_report_state waiting/pending/reported/suppressed
```

- 现有 collection 行保持当前 deadline/degraded 语义；
- resample 行的 expected items 来自当时 active TaskInstance 快照；
- 全部 success 时 complete + marker pending；
- source retention 已过仍不完整时 failed + report suppressed；
- backfill 和 repair 不创建新的 marker readiness。

### 2.4 Target Dataset 模板

以 `dataset_spot_kline_derived_4h` 为例：

```yaml
dataset_id: dataset_spot_kline_derived_4h
data_source_id: crypto
data_kind: time_series
data_node_id: <same as source Dataset>
keep_duration: 4320h
freqs: [4H]
attributes:
  owner_module: collector
  managed_by: collector
  market_type: spot
  storage_model: wide_common_metrics
  dataset_role: kline_resample_result
  resample_rule_id: <rule_id>
  source_dataset_id: dataset_binance_spot_kline_1m
  source_data_source_id: binance
  source_freq: 1m
  source_series_tag: venue:binance
  target_freq: 4H
  alignment: epoch_utc
```

View ID 为 `view_spot_kline_derived_4h`，filter 必须为 `{"freq":"4H"}`。Dataset ID 使用小写 `4h` suffix，但 Metadata frequency、View filter、RowKey 和事件 payload 使用 `4H`。

## 3. 文件结构

| 路径 | 责任 |
| --- | --- |
| `modules/collector/internal/resample/frequency.go` | 周期解析、Storage 值/ID slug 和 UTC bucket |
| `modules/collector/internal/resample/naming.go` | target Dataset/View ID |
| `modules/collector/internal/resample/bar.go` | 纯 OHLCV 重采样和 source hash |
| `modules/collector/internal/resample/catalog.go` | source 校验、target 元数据和 subject 协调 |
| `modules/collector/internal/resample/preparer.go` | pending/waiting_view Rule 的异步幂等准备和状态推进 |
| `modules/collector/internal/resample/storage.go` | Primary 精确读取、target hash 比对和 Upsert |
| `modules/collector/internal/resample/planner.go` | Rule 到 TaskInstance 的稳定展开 |
| `modules/collector/internal/resample/scanner.go` | realtime/repair/backfill 优先级选择 |
| `modules/collector/internal/resample/worker.go` | TaskInstance claim、retry、lease 和游标推进 |
| `modules/collector/internal/resample/backfill.go` | TaskInstance 内嵌 backfill 状态协调 |
| `modules/collector/internal/domain/kline_resample.go` | collect params 与 result JSON 类型 |
| `modules/collector/internal/store/task_rule.go` | prepare state、错误状态和不可变配置更新校验 |
| `modules/collector/internal/store/task_instance.go` | resample result CAS、claim 和 backfill 批量更新 |
| `modules/collector/internal/store/period_readiness.go` | resample complete/suppressed marker 语义 |
| `modules/collector/internal/rpc/kline_resample.go` | backfill/status RPC；Rule CRUD 继续复用现有 RPC |
| `modules/storage/internal/service/view/inventory_reconciler.go` | 运行时 target View 动态消费接入 |
| `web/src/views/collector/collector-rules/` | 在现有规则管理中增加 K线重采样类型 |

## 4. 实施任务

### Task 1: 固定 resample 术语、周期和命名合同

**Files:**
- Create: `modules/collector/internal/resample/frequency.go`
- Create: `modules/collector/internal/resample/frequency_test.go`
- Create: `modules/collector/internal/resample/naming.go`
- Create: `modules/collector/internal/resample/naming_test.go`

- [ ] **Step 1: 写周期解析红灯测试**

```go
require.Equal(t, FixedFrequency{Storage: "4H", Slug: "4h", Duration: 4*time.Hour}, mustParse(t, "240m"))
require.Equal(t, FixedFrequency{Storage: "4H", Slug: "4h", Duration: 4*time.Hour}, mustParse(t, "4H"))
require.Equal(t, FixedFrequency{Storage: "90m", Slug: "90m", Duration: 90*time.Minute}, mustParse(t, "90m"))
require.Error(t, validatePair("1H", "90m"))
require.NoError(t, validatePair("30m", "90m"))
require.Error(t, validatePair("1m", "1M"))
require.Error(t, validatePair("1m", "31d"))
```

- [ ] **Step 2: 实现固定合同**

```go
type FixedFrequency struct {
    Storage  string
    Slug     string
    Duration time.Duration
}

func ParseFixedFrequency(raw string) (FixedFrequency, error)
func ValidateResamplePair(source, target FixedFrequency) error
func BucketAt(effectiveNow, origin time.Time, target FixedFrequency) (start, end time.Time)
```

`origin=time.Unix(0,0).UTC()`；使用整数 duration 计算，不使用 `now.Minute()%N`。

- [ ] **Step 3: 锁定命名**

```go
require.Equal(t, "dataset_spot_kline_derived_4h", DefaultTargetDatasetID("spot", "4h"))
require.Equal(t, "view_spot_kline_derived_4h", DefaultTargetViewID("dataset_spot_kline_derived_4h"))
```

自定义 Dataset ID 最长 25 字符、lower snake case、以 frequency slug 结尾；View 固定追加 `_view`。

- [ ] **Step 4: 运行测试并提交**

Run: `cd modules/collector && go test -count=1 ./internal/resample`

Commit: `feat(collector): define kline resample periods`

### Task 2: 复用 TaskRule，增加 kline_resample 类型

**Files:**
- Modify: `modules/collector/schema/collector.sql`
- Modify: `modules/collector/internal/domain/task_rule.go`
- Modify: `modules/collector/internal/domain/collect_params.go`
- Modify: `modules/collector/internal/domain/collect_params_test.go`
- Create: `modules/collector/internal/domain/kline_resample.go`
- Create: `modules/collector/internal/domain/kline_resample_test.go`
- Modify: `modules/collector/internal/store/task_rule.go`
- Modify: `modules/collector/internal/store/task_rule_test.go`
- Modify: `modules/collector/internal/store/database.go`
- Modify: `modules/collector/proto/collector.proto`
- Modify: generated files under `modules/collector/proto/collectorgen/`
- Create: `modules/collector/internal/jobs/resample/definition.go`
- Create: `modules/collector/internal/jobs/resample/definition_test.go`
- Modify: `modules/collector/internal/jobs/jobdef/definition.go`
- Modify: `modules/collector/internal/jobs/registry.go`
- Modify: `modules/collector/internal/rpc/service.go`
- Modify: `modules/collector/internal/rpc/service_validation_test.go`

- [ ] **Step 1: 写 schema 和参数红灯测试**

断言 TaskRule 只新增 `prepare_state` 和 `last_error`，`CollectParams` 对 `kline_resample` 接受本计划第 0.3 节字段，继续 `DisallowUnknownFields`。普通 kline/symbol 的 JSON 合同不得被放宽。

- [ ] **Step 2: 增加本地执行模式**

`JobDefinition` 增加 `ExecutionMode`：现有 kline/symbol 为 `cloud_invoke`，kline_resample 为 `collector_local`。`validateTaskRule` 仅对 cloud 类型要求 SCF JobRoute，本地类型不得注册假 route。

- [ ] **Step 3: 复用 Rule CRUD**

继续使用 `CreateTaskRule/UpdateTaskRule/DisableTaskRule`，不新增独立 Resample Rule CRUD。Create 顺序：

1. 校验 resample 参数和 source Dataset；
2. 规范化 source/target frequency、series tag 和 alignment；
3. 以 `prepare_state=pending` 创建现有 TaskRule 并立即返回 rule ID；
4. 唤醒本地 preparer，异步幂等准备 target Dataset/View；
5. View 未 ready 时进入 waiting_view，preparer 定时复查，成功后进入 ready；
6. 失败时保存 error/last_error，允许通过现有 Update/“重试准备”动作重新进入 pending。

- [ ] **Step 4: 限制更新语义**

Rule 创建成功后，Update 立即锁定 source/target frequency、series tag、alignment 和 target Dataset。RPC 先读取旧 Rule，再比较规范化字段；任何不可变字段改动都返回明确错误，要求创建新 Rule/target。只允许修改 enabled 和 settle delay。测试必须覆盖 pending、waiting_view、ready 三种 prepare state，不能留下准备阶段的配置变更窗口。

- [ ] **Step 5: 隔离 marketfetch**

现有 Scheduler 只遍历 `ExecutionMode=cloud_invoke` Rule。SCF assignment 清理必须限定现有 kline/symbol 类型，不能删除或改写 `collector_local_resample` TaskInstance。

- [ ] **Step 6: 运行测试并提交**

Run: `cd modules/collector && go test -count=1 ./internal/domain ./internal/jobs/... ./internal/store ./internal/rpc -run 'Resample|TaskRule|JobDefinition'`

Commit: `feat(collector): register kline resample rules`

### Task 3: 复用 TaskInstance 保存可恢复执行状态

**Files:**
- Modify: `modules/collector/internal/domain/task_instance.go`
- Modify: `modules/collector/internal/domain/task_instance_test.go`
- Modify: `modules/collector/internal/store/task_instance.go`
- Modify: `modules/collector/internal/store/task_instance_test.go`
- Create: `modules/collector/internal/resample/planner.go`
- Create: `modules/collector/internal/resample/planner_test.go`

- [ ] **Step 1: 写 Result JSON 红灯测试**

覆盖严格解析、schema version、未知字段拒绝、UTC 时间、非法 state/state version、空 backfill 和损坏 JSON。损坏 Result 不得静默重置并跳过历史，应把 TaskInstance 标记 failed 并报警。

- [ ] **Step 2: 实现稳定 TaskInstance 展开**

Planner 读取 source active subjects，为每个 subject 生成一个 target TaskInstance。TaskID 包含 rule、target Dataset、subject、source series tag 和 target frequency。source 新增 subject 时幂等增加 instance；disabled subject 标记 `is_deleted=true`，不删除历史 target 行。

- [ ] **Step 3: 实现条件状态更新**

Repository 提供：

```go
ClaimDueResampleTasks(ctx, now, origin, limit, leaseDuration)
CompleteResampleTask(ctx, taskID, expectedStateVersion, bucket, nextBucket, inputHash)
WaitResampleSource(ctx, taskID, expectedStateVersion, attempt, nextRetryAt, err)
FailResampleTask(ctx, taskID, expectedStateVersion, err)
RecoverExpiredResampleLeases(ctx, now, limit)
StartResampleBackfill(ctx, ruleID, request)
CancelResampleBackfill(ctx, ruleID, requestID)
CompleteResampleBackfillSync(ctx, ruleID, requestID)
```

所有更新带 `state_version` CAS；不得用 GORM `Save` 覆盖整个 result。

- [ ] **Step 4: 固定顺序执行取舍**

每个 TaskInstance 同时只处理一个 bucket。不同 subject 可由 worker pool 并行；同一 subject 的 realtime 优先于 repair，repair 优先于 backfill。测试证明不需要 bucket job 表仍可在重启后从 lease/cursor 恢复。

- [ ] **Step 5: 运行测试并提交**

Run: `cd modules/collector && go test -count=1 ./internal/domain ./internal/store ./internal/resample -run 'ResampleTask|Lease|Planner|CAS'`

Commit: `feat(collector): persist resample state in task instances`

### Task 4: 选择 active Kline Dataset 并准备 target Dataset/View

**Files:**
- Modify: `modules/collector/internal/planner/storagesource/source.go`
- Modify: `modules/collector/internal/planner/storagesource/source_test.go`
- Create: `modules/collector/internal/resample/catalog.go`
- Create: `modules/collector/internal/resample/catalog_test.go`
- Create: `modules/collector/internal/resample/preparer.go`
- Create: `modules/collector/internal/resample/preparer_test.go`

- [ ] **Step 1: 扩展窄 Metadata adapter**

增加分页 ListDatasets/ListDatasetColumns、CheckDatasetActivation、Create/Update Dataset、BindDatasetSubject、Create/Get View、UpsertViewColumn、RequestViewRebuild 和 WaitViewSyncPoint，只暴露重采样需要的方法。

- [ ] **Step 2: 写 source 选择红灯测试**

证明 UI/RPC 只返回 active time_series 标准 K 线 Dataset；source frequency 必须属于 Dataset freqs；7 个标准字段必须 active；market/series tag/retention 校验失败返回可操作错误。

- [ ] **Step 3: 固定 target DataSource 语义**

在 `crypto` 中校验 `data_source_id=crypto` 存在、active 且 kind=internal。即使 source 是 Binance Dataset，target 也不能伪装为 Binance 原生数据。source DataSource ID 写入 target attributes。

- [ ] **Step 4: 幂等创建 target**

顺序固定为：disabled Dataset -> 7 columns -> mirror active subject bindings -> CheckDatasetActivation -> CAS activate -> View -> 7 View columns。已存在且合同一致时复用；任一 lineage/frequency/field/DataNode 不一致时失败，不覆盖用户资源。

- [ ] **Step 5: 动态 View readiness fence**

Rule 进入 waiting_view。route-ready request ID 为 `kline-resample-route:<rule_id>:<target_dataset_revision>`；`WaitViewSyncPoint` ready 且 View active revision 追平 desired revision 后，Rule 才进入 ready。

preparer 使用 buffered notify channel 加 30 秒轮询兜底。每次只处理 bounded 数量的 pending/waiting_view Rule；error Rule 只有收到显式重试才重新执行。任何 Metadata/View RPC 都不得在 SQLite transaction 内调用。

- [ ] **Step 6: 运行测试并提交**

Run: `cd modules/collector && go test -count=1 ./internal/planner/storagesource ./internal/resample -run 'ResampleCatalog|SourceDataset|TargetDataset'`

Commit: `feat(collector): provision resampled kline datasets`

### Task 5: 让 Storage View 动态接入 target Dataset

**Files:**
- Create: `modules/storage/internal/service/view/inventory_reconciler.go`
- Create: `modules/storage/internal/service/view/inventory_reconciler_test.go`
- Modify: `modules/storage/internal/service/view/consume.go`
- Modify: `modules/storage/cmd/server/main.go`
- Modify: `modules/storage/cmd/server/main_test.go`
- Modify: `modules/storage/internal/config/loader.go`
- Modify: `modules/storage/internal/config/loader_test.go`
- Modify: `modules/storage/config/storage.yaml`

- [ ] **Step 1: 锁定当前启动快照问题**

测试证明进程启动后新增 active View 时，现有静态 consumer options 不包含新 target Dataset。

- [ ] **Step 2: 实现动态 catch-all consumer**

exact kline/factor routes 保持共享 partition；misc 分区声明的 wildcard 或 exact route space 内、且未被其它 exact route 认领的 active View Dataset 使用稳定 per-Dataset durable：

```text
partition_id = misc:<first-16-hex(sha256(space_id + "\x00" + dataset_id))>
durable      = <misc-durable>-<first-14-hex(same-hash)> (JetStream 32-character limit)
```

每 30 秒 reconcile。相同 inventory no-op；新增 Dataset bind 新 consumer；per-Dataset durable 首次使用 `deliver_policy=all`，覆盖旧共享 durable 的 pending 和历史序列，之后重启保持同一策略；移除时停止本地拉取但保留 durable，支持再次启用和回滚。动态 durable 使用 JetStream 合法的短后缀命名，`storage-eventbus` ACL 必须允许 `MOOX_STORAGE` 下动态 consumer 的 INFO/CREATE/DURABLE.CREATE/MSG.NEXT/ACK。旧 `storage_view_misc` durable 在迁移窗口只保留作回滚/人工 replay 安全网，不自动删除。

- [ ] **Step 3: 发布 route-ready fence**

consumer bound 后，Storage View 使用自身 Primary 凭据向 target Dataset 幂等追加 `source=catchup` 的 route-ready sync point，再请求初始 View rebuild。Collector 用第 4 Task 的 request ID 端到端确认路由已生效。

- [ ] **Step 4: 覆盖升级和失败恢复**

首次升级把旧共享 misc Dataset 的覆盖范围迁移到稳定 per-Dataset durable，并执行 rebuild。per-Dataset `all` replay 保证旧共享 durable 的 pending 不会因静态 partition 移除而丢失；Metadata 失败、bind 失败、rebuild 失败不停止已有健康 consumer，下一轮可恢复。旧 durable 待运维核验各 Dataset 已追平后再人工清理。

- [ ] **Step 5: 运行测试并提交**

Run: `cd modules/storage && go test -count=1 ./cmd/server ./internal/config ./internal/service/view/... -run 'Inventory|DynamicView|ConsumerPartition'`

Commit: `feat(storage): reconcile dynamic view consumers`

### Task 6: 实现 resample.Bars、Primary 精确读和幂等写

**Files:**
- Create: `modules/collector/internal/resample/bar.go`
- Create: `modules/collector/internal/resample/bar_test.go`
- Create: `modules/collector/internal/resample/storage.go`
- Create: `modules/collector/internal/resample/storage_test.go`

- [ ] **Step 1: 写表驱动红灯测试**

覆盖 1m->4m、30m->90m、乱序、重复时间、缺首/中/末行、字段不全、错误 series tag、负 volume 和 trade_num 溢出。

- [ ] **Step 2: 实现纯函数**

```go
type SourceBar struct { /* key + 7 fields */ }
type Result struct { /* target key + 7 fields + SourceHash */ }

func ExpectedSourceTimes(start, end time.Time, source FixedFrequency) ([]time.Time, error)
func Bars(spec RuleSpec, subjectID string, start, end time.Time, rows []SourceBar) (Result, error)
```

Source hash 对排序后的 RowKey 和 7 个 typed value 确定性编码，不依赖 JSON map 顺序。

- [ ] **Step 3: 实现有界 exact read**

只用 `PrimaryStore.ReadFields`。每次不超过 10,000 keys、100,000 key-field；按 `worker_max_source_keys_per_claim` 自适应 subject 数，长周期至少一次 claim 1 个 subject。

- [ ] **Step 4: 目标 hash 幂等**

写前读取目标 row attribute `source_hash`。hash 相同直接成功；不同才完整 Upsert 7 fields 和 provenance attributes。`source_event_id = hash(rule_id + target RowKey + source_hash)`；`write_source=collector:kline_resample`。

- [ ] **Step 5: 运行测试并提交**

Run: `cd modules/collector && go test -count=1 ./internal/resample -run 'Bars|PrimaryRead|IdempotentWrite'`

Commit: `feat(collector): resample stored kline windows`

### Task 7: 实现一分钟 scanner、worker 和 PeriodReadiness

**Files:**
- Create: `modules/collector/internal/resample/scanner.go`
- Create: `modules/collector/internal/resample/scanner_test.go`
- Create: `modules/collector/internal/resample/worker.go`
- Create: `modules/collector/internal/resample/worker_test.go`
- Modify: `modules/collector/internal/domain/period_readiness.go`
- Modify: `modules/collector/internal/store/period_readiness.go`
- Modify: `modules/collector/internal/store/period_readiness_test.go`
- Modify: `modules/collector/internal/marketfetch/period_reporter.go`
- Modify: `modules/collector/internal/marketfetch/period_reporter_test.go`

- [ ] **Step 1: 写时间边界红灯测试**

以 `target=4H, settle=10s` 验证：03:59:59/04:00:09 不到期，04:00:10 到期一次，停机到 12:00:10 后从 TaskInstance cursor 顺序追赶 04:00、08:00 两桶。全局 repair lookback 为 `3` 时枚举最近三个已闭合目标桶，为 `0` 时不生成 repair 工作且新 Rule 从 ready 后的下一闭合桶进入实时执行。

- [ ] **Step 2: 实现 scanner**

scanner 只读取 ready/enabled resample Rules 和 TaskInstance result，按 realtime > repair > backfill 选择 due tasks并 CAS claim。不得读取 K 线、写 target、sleep 或在 Timer callback 中执行历史循环。

- [x] **Step 3: 实现 worker 和 retry-go**

默认 worker concurrency=2。完整性缺失时执行 4 次 context-aware 重试，使用 `retry.Context(ctx)` 和指数退避；耗尽后保存 waiting_source，next retry 为当前失败时间之后的短延迟，避免阻塞分钟级 timer。

最早 source RowKey 已越过 source keep_duration 后，TaskInstance 进入 failed，PeriodReadiness report suppressed，提示恢复 source 后重新回填。

- [ ] **Step 4: 接入 PeriodReadiness**

实时 bucket 创建 work_type=resample 的 readiness 快照；每个 TaskInstance 成功写 target 后 MarkSubjectSuccess。全部成功后复用现有 reporter 发布 complete marker。repair/backfill 不创建或重置 marker。

- [ ] **Step 5: 覆盖崩溃窗口**

测试 Storage ACK 后、TaskInstance CAS 前崩溃：重试读到相同 target source hash 后直接推进游标；stale lease 可回收；重复 tick 不并发执行同一 subject。

- [ ] **Step 6: 运行测试并提交**

Run: `cd modules/collector && go test -count=1 ./internal/resample ./internal/store ./internal/marketfetch -run 'Scanner|Worker|Retry|Readiness|Marker'`

Commit: `feat(collector): execute kline resample tasks`

### Task 8: 在 TaskInstance Result 中实现可恢复历史回填

**Files:**
- Create: `modules/collector/internal/resample/backfill.go`
- Create: `modules/collector/internal/resample/backfill_test.go`
- Create: `modules/collector/internal/rpc/kline_resample.go`
- Create: `modules/collector/internal/rpc/kline_resample_test.go`
- Modify: `modules/collector/proto/collector.proto`
- Modify: generated files under `modules/collector/proto/collectorgen/`

- [ ] **Step 1: 定义最小 RPC**

```protobuf
rpc StartKlineResampleBackfill(StartKlineResampleBackfillReq) returns (StartKlineResampleBackfillRsp);
rpc GetKlineResampleBackfill(GetKlineResampleBackfillReq) returns (GetKlineResampleBackfillRsp);
rpc CancelKlineResampleBackfill(CancelKlineResampleBackfillReq) returns (CancelKlineResampleBackfillRsp);
```

Start 必填 request_id、rule_id、UTC `[start,end)`；区间必须对齐 target 网格、end 已闭合、最多 10,000 buckets，并在 source retention 内。

- [ ] **Step 2: 固定一条 Rule 一个 active backfill**

Start 在一个 SQLite transaction 中给当前 active TaskInstances 写入同一 backfill request/range。相同 request 幂等；不同 request 在已有 active run 时返回 conflict。新加入的 subject 不自动加入正在运行的 backfill，用户可在结束后重新发起区间回填。

- [ ] **Step 3: 计算进度而非保存 run 表**

Get 按 rule/request 聚合 TaskInstance results，返回 total/running/waiting/complete/failed subject 数、最慢 cursor 和最多 100 条错误摘要。V1 不提供历史 run 列表和永久逐 bucket 审计。

- [ ] **Step 4: 完成 View fence**

所有 subject cursor 到达 end 后，协调器幂等调用 `AppendDatasetSyncPoint(source="catchup", request_id=request_id)`，再 `WaitViewSyncPoint`。ready 后批量把 TaskInstances 的 backfill state 改为 complete；响应丢失时重复同一 request 可恢复。

- [ ] **Step 5: 运行测试并提交**

Run: `cd modules/collector && go test -count=1 ./internal/resample ./internal/rpc ./internal/store -run 'ResampleBackfill|BackfillSync'`

Commit: `feat(collector): backfill resampled kline tasks`

### Task 9: 接入 timer、bootstrap、指标和 RealtimeInventory

**Files:**
- Modify: `modules/collector/config/trpc_go.yaml`
- Modify: `modules/collector/config/app.yaml`
- Modify: `modules/collector/internal/bootstrap/config.go`
- Modify: `modules/collector/internal/bootstrap/config_test.go`
- Modify: `modules/collector/internal/bootstrap/bootstrap.go`
- Modify: `modules/collector/internal/bootstrap/bootstrap_test.go`
- Modify: `modules/collector/internal/observability/realtime_inventory.go`
- Modify: `modules/collector/internal/observability/realtime_inventory_test.go`
- Create: `modules/collector/internal/resample/metrics.go`
- Create: `modules/collector/internal/resample/metrics_test.go`

- [ ] **Step 1: 增加严格配置**

```yaml
kline_resample:
  enabled: true
  scan_timeout: 30s
  worker_concurrency: 2
  max_claims_per_tick: 100
  worker_subject_batch_size: 50
  worker_job_timeout: 30s
  worker_poll_interval: 5s
  worker_max_source_keys_per_claim: 20000
  stale_running_after: 2m
  default_settle_delay: 10s
  repair_lookback_buckets: 3
  target_keep_duration: 4320h
```

所有 duration/数量为正，`worker_concurrency` 为 1..250，`max_claims_per_tick` 为 3..1000，用于限制单次 timer 扫描的总 claim 数，subject batch <=200；repair 每 tick 最多执行一个 worker batch；`repair_lookback_buckets` 允许 0..10，`0` 表示关闭自动近期修订；未知 YAML 字段继续失败。

- [ ] **Step 2: 注册独立 timer**

```yaml
- name: trpc.moox.collector.kline_resample.timer
  port: 11415
  network: "10 * * * * *"
  protocol: timer
  timeout: 10000
```

使用 `timerjob.New("collectorKlineResample", scanTimeout, scanner.Scan)`；不复用现有 schedule timer 的后台 goroutine。

- [ ] **Step 3: 生命周期和低基数指标**

```text
moox_collector_kline_resample_tasks_total{origin,result}
moox_collector_kline_resample_running_tasks{space_id,target_dataset_id}
moox_collector_kline_resample_waiting_source{space_id,target_dataset_id}
moox_collector_kline_resample_bucket_lag_seconds{space_id,target_dataset_id}
moox_collector_kline_resample_source_rows_missing_total{target_dataset_id}
moox_collector_kline_resample_duration_seconds{stage}
```

禁止 rule、subject、bucket、error 进入 label。

- [ ] **Step 4: 扩展 RealtimeInventory**

纳入 `data_type=kline_resample && prepare_state=ready && enabled=true` Rule 的 target Dataset/frequency。刷新失败保留上一快照。

- [ ] **Step 5: 运行测试并提交**

Run: `cd modules/collector && go test -count=1 ./internal/bootstrap ./internal/observability ./internal/resample`

Commit: `feat(collector): run kline resample workers`

### Task 10: 在现有采集规则页面增加 K线重采样

**Files:**
- Modify: `web/src/api/collector/index.ts`
- Modify: `web/src/views/collector/collector-rules/collector-rule-params.ts`
- Modify: `web/src/views/collector/collector-rules/collector-rule-params.test.ts`
- Modify: `web/src/views/collector/collector-rules/collector-rules.vue`
- Modify: `web/src/views/collector/collector-rules/collector-rules.test.ts`
- Create: `web/src/views/collector/collector-rules/resample-backfill.vue`
- Create: `web/src/views/collector/collector-rules/resample-backfill.test.ts`

- [ ] **Step 1: 复用现有 Rule 表单和列表**

Data type 新增“K线重采样”，不增加独立规则表或第三套 CRUD 页面。列表继续显示 Rule，resample 行额外展示 `source Dataset/frequency -> target Dataset/frequency`、prepare state、最新成功 bucket 和 backfill 状态。

- [ ] **Step 2: 实现 source Dataset 选择**

控件固定为：

- source Dataset：active Kline result Dataset select；
- source frequency：从 Dataset freqs 选择；
- source series tag：必填 select/input；
- target period：正整数 + 分钟/小时/天；
- target Dataset/View：实时预览，可在创建前修改 Dataset ID；
- settle delay、enabled；repair lookback 是 Collector 全局配置，不在单条 Rule 表单暴露。

不展示 target DataSource 选择；界面只读显示“内部行情 `crypto`”，避免用户误选 Binance。

- [ ] **Step 3: 实现 backfill dialog**

UTC `[start,end)`，前端显示 bucket 数和 source retention 上限；同一 Rule 有 active backfill 时禁用再次创建。状态从 TaskInstances 聚合，不轮询逐 bucket API。

- [ ] **Step 4: 运行测试和构建**

Run: `pnpm --dir web exec vitest run src/views/collector/collector-rules/collector-rule-params.test.ts src/views/collector/collector-rules/collector-rules.test.ts src/views/collector/collector-rules/resample-backfill.test.ts`

Run: `pnpm --dir web run build:prod`

Commit: `feat(web): configure kline resample rules`

### Task 11: E2E、文档、codeCR 和灰度验收

**Files:**
- Create: `modules/collector/test/kline_resample_e2e_test.go`
- Create: `scripts/test/e2e/test-kline-resample.sh`
- Modify: `Makefile`
- Modify: `docs/内置市场行情采集架构.md`
- Modify: `docs/采集任务管理.md`
- Modify: `docs/operations/monitoring.md`

- [ ] **Step 1: 模块 E2E**

真实 Collector SQLite + 可控 Storage fake 验证：

1. 创建 kline_resample TaskRule，不产生任何新业务表；
2. Planner 生成 BTC/ETH TaskInstances；
3. 四根 1H 生成两行 `dataset_spot_kline_derived_4h`，RowKey.freq=4H；
4. 重复 tick 不增加 target 写；
5. 修改 source high 后 recent repair 覆盖同一 RowKey；
6. ETH 缺末行时只完成 BTC，不发 marker，补齐后收敛；
7. Storage ACK 后崩溃通过 target source hash 恢复；
8. stale TaskInstance lease 被回收；
9. backfill 中重启后从每个 subject cursor 恢复；
10. realtime 到期时优先于 active backfill。

- [ ] **Step 2: 真实 Storage E2E**

启动临时 Metadata/Primary/DataNode/View/EventBus/Collector：创建 Rule -> 自动 target Dataset/View -> 动态 consumer route-ready -> 写 source -> 重采样 -> Primary/View 对账 -> backfill sync fence。

- [ ] **Step 3: 完整证明集**

Run: `cd modules/collector && go test -count=1 ./...`

Run: `make test-kline-resample`

Run: `./scripts/test/contract/test-go-workspace.sh`

Run: `make verify-pr`

Run: `pnpm --dir web test && pnpm --dir web run build:prod`

Run: `git diff --check`

- [ ] **Step 4: 启动独立 codeCR Agent**

重点检查：Rule 执行模式隔离、TaskInstance JSON CAS、lease/重启、source Dataset/DataSource 语义、周期边界、缺行部分写、target hash 幂等、marker 过早发布、backfill 与 realtime 饥饿、动态 View 路由及测试缺口。结论必须附文件、符号或行号，主 Agent 独立核验后重跑全部证明。

- [ ] **Step 5: 灰度**

首次部署保持 `kline_resample.enabled=false`。选择 `dataset_binance_spot_kline_1m` 的少量测试 subject，创建 `dataset_spot_kline_derived_4h`，连续验收三个 4H bucket，再回填最近 7 天。

每个 bucket 核对 source 行数、target Primary/View 行数、随机 OHLCV、唯一 marker、TaskInstance lag 和 waiting_source。回填核对 request progress、sync point 和 View fence。

- [ ] **Step 6: 回滚**

先禁用 resample Rule 和全局开关，停止新 claim并等待 in-flight context；回滚 Collector/Storage View 二进制。保留 TaskRule、TaskInstance result、PeriodReadiness 和 target Dataset/View，修复后从现有 cursor 继续。旧共享 misc durable 在动态路由升级阶段保留，Storage View 可回滚。

Commit: `test(collector): verify kline resample pipeline`

## 5. 明确不纳入 V1

- Source Dataset 写回下游 resample 配置。
- Dataset attributes 作为 enabled/retry/checkpoint/backfill 真值。
- resample 输出再次作为 source 的链式重采样。
- 同一 Rule 同时运行多个历史回填。
- 同一 subject 多 bucket 并行、永久逐 bucket 审计列表。
- 新建 resample rule/job/item/backfill 数据库表。
- 股票 session calendar、节假日、DST、秒级或日历周期。
- 多 source/provider 同写一个 target Dataset。
- 分布式 worker、独立 DLQ、全局 exactly-once。
- 自动修改已发布 marker 或自动重算历史 Factor。
- 在现有腾讯 Timer SCF 中读取 Storage 并执行重采样。

## 6. 完成标准

1. 用户能在现有采集规则页面选择 active Kline Dataset，创建 `1m->7m`、`30m->90m`、`1H->4H` Rule。
2. target 名称包含周期 suffix，例如 `dataset_spot_kline_derived_4h`，其 DataSource 为内部 `crypto`，不是 source Dataset ID 或 Binance。
3. 完整 Rule 配置只保存在现有 TaskRule，全局 repair lookback 只保存在 Collector YAML；target attributes 只保存分类和血缘；V1 没有配置 hash，也没有新增数据库表。
4. 每个 subject 使用现有 TaskInstance result 保存游标、lease、retry 和 backfill，重启后可恢复。
5. 全局一分钟 timer 只 claim 工作，不读取 K 线或等待源闭合。
6. 缺任意源行不写该 subject 的部分 target；短退避和跨 tick retry 最终收敛。
7. 重复 tick、RPC 超时、Storage ACK 丢失和进程重启不产生不同结果。
8. recent repair 和历史 backfill 共用 `resample.Bars` 和 Storage writer。
9. 实时 complete marker 只在 PeriodReadiness 全部 subject success 后发布一次。
10. runtime target View 无需重启 Storage View 即可接入；route-ready 和 rebuild 完成前 Rule 不执行。
11. 模块测试、真实 Storage E2E、workspace tests、verify-pr、Web tests/build 和独立 codeCR 全部通过。
12. 小范围灰度连续三个 target bucket 和一次 7 天历史回填完成对账。
