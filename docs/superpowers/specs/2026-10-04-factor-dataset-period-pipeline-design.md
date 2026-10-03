# 因子计算模块重构设计：数据集驱动的周期流水线

日期：2026-10-04

状态：设计基线，已确认关键取舍；本文描述目标架构，不表示功能已经实现。实施计划见
[因子计算模块重构执行计划](../plans/2026-10-04-factor-dataset-period-pipeline.md)。

## 1. 背景与目标

现有因子模块（约 2 万行非测试 Go 代码）同时维护三条触发链路、十余张本地账本表、控制面与引擎
双进程，以及由 `moox-merge` 构造的复合数据集（mdataset）字段映射，复杂度远超个人量化系统的需要：

- 时序因子按 `DatasetRowsUpserted(input_commit)` 逐行触发，截面因子按 `ViewDataReady(merge)` 周期触发，
  补算又是第三套任务构造；逐行触发迫使系统用 `t_factor_subject_runs/heads` 做准入去重，再用
  `t_factor_period_barriers/pairs` 汇总写入回执才能判断周期完成。
- mdataset 使用 `源数据集__字段` 前缀列，因子声明 `close` 后依赖 `ExpandPythonInputs`、
  `MapPythonInputColumns` 后缀匹配，`defaultPreferredMappedSource` 甚至硬编码了
  `mdataset_binance_kline_1m`。
- 输入列与因子列写在同一个 mdataset 中，需要 `write_kind` 防止回写再触发计算，需要 output manifest
  追踪动态 tag 输出以便清理。
- 控制面通过 NATS request-reply 向引擎同步版本化目录（catalogsync、artifacts、revision、engine_status）。
- `inputcache`（约 1.3k 行）被引擎强制禁用；`bootstrap.go` 中的旧单体 `Initialize` 已无调用方。

本设计目标：

1. 因子计算只与**数据集**交互：订阅源数据集周期采集完成事件，读取源数据集，写入结果数据集。
2. 一类因子（因子集）只绑定一个源数据集；多数据源通过同一数据集内的 `series_tag` 表达。
3. 收到周期完成信号后，按因子集合并参数取最大回看 N，一次读取 N 根历史 K 线并共享，并发计算全部因子。
4. 因子值与原始 K 线整行写入结果数据集，Storage 自动维护结果数据集的 View，策略等待 `ViewDataReady`。
5. 单进程、无本地运行账本、至少一次处理加幂等写入；目标非测试代码规模 5–6 千行。

## 2. 已确认决策

| 编号 | 决策 |
|---|---|
| D1 | 因子计算与 View 无关。View 是数据集的查询索引，只服务策略、人工和组合查询。 |
| D2 | 触发信号是源数据集的 `CollectorPeriodCompleted`，历史数据从 Primary 读取源数据集。 |
| D3 | 回看窗口按事件 `period_time` 反算 N 个周期时间点，直接按时间范围读取数据集。 |
| D4 | 结果数据集的 View 由 Storage 自动创建并随数据集加列，因子不持有 view_id。 |
| D5 | 策略继续等待结果 View 的 `ViewDataReady(completion_kind=factor_period.computed)`。 |
| D6 | 一个数据集只属于一个 DataNode，激活后不可变更；迁移是离线运维操作。 |
| D7 | 控制面与引擎合并为单进程 `moox-factor`。 |
| D8 | 每个 `(space, 源数据集, 频率)` 只有一个因子集，对应唯一结果数据集。 |
| D9 | 结果数据集携带源数据集的全部业务列。 |
| D10 | 下线 `moox-merge`；现货与永续写入同一多 tag 源数据集。 |
| D11 | 删除 Storage 侧的因子元数据实体（Metadata `CreateFactor/UpdateFactor/GetFactor`）。 |
| D12 | 因子停用或删除时保留列、停止写入，不改 schema、不重建 View。 |
| D13 | 一期只支持加密货币空间（7×24 连续周期）；交易日历市场（A 股）列为 TODO。 |
| D14 | 补算范围由调用方决定，不自动向后扩展 lookback 影响范围。 |
| D15 | 因子集之间不设执行优先级。 |

## 3. 对旧方案的替代

本文替代以下文档中与因子计算相关的设计：

- `docs/因子计算模块设计.md`（实施完成后按本文重写）；
- `docs/因子视图驱动计算设计.md`（实施完成后删除）；
- `docs/superpowers/specs/2026-09-13-factor-dataset-view-refactor-design.md` 中的复合因子 Dataset、
  Merge 构造、控制面与引擎拆分、本地 DuckDB 缓存、回写同一 mdataset 等内容。

| 旧设计 | 新设计 |
|---|---|
| Merge 构造 mdataset，字段加前缀 | 多来源写入同一数据集，以 `series_tag` 区分 |
| 时序按行事件触发，截面按 Merge 关联的 `ViewDataReady` 触发 | 时序与截面统一由源数据集 `CollectorPeriodCompleted` 触发 |
| 读取源 View（DuckDB） | 读取源数据集 Primary |
| 因子 binding（因子 × Source View × 频率） | 因子集（源数据集 × 频率）+ 因子隶属关系 |
| 因子列回写源 mdataset | 写入独立结果数据集（携带列 + 因子列） |
| PeriodBarrier 账本 + 写入回执 + output manifest | 内存周期任务，一次上报 |
| 控制面 + 引擎 + 目录同步 | 单进程 |

## 4. 总体架构

```text
┌──────────┐ 写行（多 series_tag）  ┌──────────────────────────────────────────┐
│Collector │──────────────────────▶│ 源数据集 dataset_binance_kline_1m（DataNode A）│
│          │ ReportCollector...    │ outbox: rows… → CollectorPeriodCompleted(T) │
└──────────┘──────────────────────▶└───────────────────┬──────────────────────┘
                                                       │ JetStream
                                                       ▼
┌────────────────────────────── moox-factor（单进程）──────────────────────────────┐
│ trigger：订阅 collector.period.completed.<space>.<源数据集>                          │
│   └▶ 因子集 lane（串行）→ PeriodJob(T)                                              │
│        plan → load（Primary 读源数据集）→ compute（Python 池）→ assemble             │
│        → write（结果数据集）→ report（FactorPeriodComputed）→ ACK                   │
│ rpc：因子与因子集管理、补算、状态        recalc：范围模式（复用同一流水线）           │
└──────────────────────────────────────────────────────┬─────────────────────────┘
                                                       ▼
                         ┌───────────────────────────────────────────────────┐
                         │ 结果数据集 dataset_factor_binance_kline_1m（DataNode A）│
                         │ outbox: rows… → FactorPeriodComputed(T)            │
                         └───────────────────┬───────────────────────────────┘
                                             ▼ Storage 自动维护的默认 View
                               ViewDataReady(completion_kind=factor_period.computed)
                                             ▼
                                         Strategy 读取结果 View
```

| 参与方 | 负责 | 不负责 |
|---|---|---|
| Collector | 写源数据集；按 `(subject, series_tag)` 统计周期完成；上报 `CollectorPeriodCompleted` | 因子 |
| Storage | 数据集读写与 outbox 顺序；周期完成标记；结果数据集默认 View；`ViewDataReady` | 因子业务 |
| Factor | 因子集配置；周期计算；写结果数据集；上报 `FactorPeriodComputed`；补算 | View 与索引 |
| Strategy | 等待 `ViewDataReady(kind=factor)` 后读取结果 View | 因子计算 |

## 5. 正确性基础

### 5.1 一个数据集一个 DataNode

数据集元数据中的 `data_node_id` 是唯一 owner：

- 创建数据集必须指定 `data_node_id`，激活后不可修改；
- `RebindDatasetDataNode` 仅作为离线运维操作：暂停写入方、迁移、从下一周期恢复；
- Storage 拒绝跨节点写入和对已激活数据集的 owner 变更。

由此，同一数据集的行事件、周期完成标记、SyncPoint 在同一个 outbox 中全序排列。

### 5.2 标记排在本周期行之后

- 源侧：Collector 先提交本周期行，再调用 `ReportCollectorPeriodCompleted`；DataNode 在同一 outbox
  追加标记。Factor 收到标记时，本周期行在 Primary 上一定可读，不需要等待 View。
- 结果侧：Factor 先写完本周期全部结果行，再调用 `ReportFactorPeriodComputed`；结果 View 按数据集
  队列串行消费，先应用行再处理标记，因此发布 `ViewDataReady` 时结果一定可读。

源数据集与结果数据集可以位于不同 DataNode；因子完成标记只约束结果数据集自身的行。默认把结果
数据集放在源数据集所在节点。

### 5.3 语义

- **至少一次 + 幂等**：durable consumer，上报成功后才 ACK；重投时整周期重算，写入使用确定性
  commit_id，结果一致。
- **latest-wins**：完成事件表示“发布时结果可读”，不是历史快照。
- **无本地账本**：周期状态只存在于内存，任务结束即丢弃；SQLite 只保存配置和补算任务。
- **显式补算**：迟到数据、交易所修正、新增因子都通过补算处理，实时链路不自动修正。

## 6. 概念与命名

| 概念 | 定义 | 命名 |
|---|---|---|
| 源数据集 | 原始采集数据，`dataset_role=raw_collection` | 例 `dataset_binance_kline_1m` |
| 因子定义 FactorDef | 一个 Python 算法、一组静态参数、显式输入输出 | `factor_id` 全局唯一，`[A-Za-z_][A-Za-z0-9_]*` |
| 因子集 FactorSet | 绑定 `(space, 源数据集, 频率)` 的一组因子 | `fset_<源后缀>` |
| 结果数据集 | 因子集唯一输出：携带列 + 因子输出列 | `dataset_factor_<源后缀>` |
| 周期 T | 事件 `period_time`，UTC 周期起点 epoch 秒 | — |
| 周期任务 PeriodJob | 一个因子集在一个周期上的一次完整执行 | 仅存在于内存 |

`源后缀` = 源数据集 ID 去掉 `dataset_` 前缀；若后缀不以 `_<freq>` 结尾，则追加 `_<freq>`。
例：`dataset_binance_kline_1m` + `1m` → `fset_binance_kline_1m`、`dataset_factor_binance_kline_1m`；
`dataset_spot_kline` + `1h` → `fset_spot_kline_1h`、`dataset_factor_spot_kline_1h`。

列名规则：

- `subject_id`、`freq`、`data_time`、`series_tag` 为系统保留列，不能作为因子输出；
- 因子输出在因子集内唯一，且不得与源数据集业务列重名；
- 结果数据集列名不加前缀。

## 7. 数据模型（SQLite）

只保留三张表；`c_mtime` 触发器按 schema 规范补齐。

```sql
PRAGMA foreign_keys = ON;

-- 因子集：一个 (space, 源数据集, 频率) 只有一个因子集，对应唯一结果数据集
CREATE TABLE IF NOT EXISTS t_factor_sets (
    c_set_id TEXT NOT NULL PRIMARY KEY,
    c_space_id TEXT NOT NULL,
    c_source_dataset_id TEXT NOT NULL,
    c_freq TEXT NOT NULL,
    c_subject_mode TEXT NOT NULL DEFAULT 'all',
    c_subjects_json TEXT NOT NULL DEFAULT '[]',
    c_result_dataset_id TEXT NOT NULL,
    c_status TEXT NOT NULL DEFAULT 'pending',
    c_ctime DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    c_mtime DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    CHECK (c_subject_mode IN ('all', 'include')),
    CHECK (c_status IN ('pending', 'enabled', 'disabled')),
    UNIQUE (c_space_id, c_source_dataset_id, c_freq),
    UNIQUE (c_result_dataset_id)
);

-- 因子定义：每个因子只属于一个因子集
CREATE TABLE IF NOT EXISTS t_factor_defs (
    c_factor_id TEXT NOT NULL PRIMARY KEY,
    c_set_id TEXT NOT NULL,
    c_name TEXT NOT NULL,
    c_factor_type TEXT NOT NULL,
    c_source_code TEXT NOT NULL,
    c_source_hash TEXT NOT NULL,
    c_input_columns_json TEXT NOT NULL,
    c_outputs_json TEXT NOT NULL,
    c_params_json TEXT NOT NULL DEFAULT '{}',
    c_lookback_periods INTEGER NOT NULL,
    c_allow_partial_universe INTEGER NOT NULL DEFAULT 0,
    c_status TEXT NOT NULL DEFAULT 'disabled',
    c_ctime DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    c_mtime DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    CHECK (c_factor_type IN ('timeseries', 'cross_section')),
    CHECK (c_lookback_periods >= 1),
    CHECK (c_allow_partial_universe IN (0, 1)),
    CHECK (c_status IN ('enabled', 'disabled')),
    FOREIGN KEY (c_set_id) REFERENCES t_factor_sets (c_set_id)
);

CREATE INDEX IF NOT EXISTS idx_t_factor_defs_set ON t_factor_defs (c_set_id, c_status);

-- 补算任务：只用于异步受理、查询和取消
CREATE TABLE IF NOT EXISTS t_factor_recalc_jobs (
    c_job_id TEXT NOT NULL PRIMARY KEY,
    c_request_id TEXT NOT NULL,
    c_set_id TEXT NOT NULL,
    c_factor_ids_json TEXT NOT NULL DEFAULT '[]',
    c_subjects_json TEXT NOT NULL DEFAULT '[]',
    c_start_time INTEGER NOT NULL,
    c_end_time INTEGER NOT NULL,
    c_status TEXT NOT NULL DEFAULT 'accepted',
    c_progress_time INTEGER NOT NULL DEFAULT 0,
    c_error TEXT NOT NULL DEFAULT '',
    c_ctime DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    c_mtime DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    CHECK (c_status IN ('accepted', 'running', 'succeeded', 'failed', 'cancelled')),
    CHECK (c_end_time > c_start_time),
    UNIQUE (c_request_id)
);

CREATE INDEX IF NOT EXISTS idx_t_factor_recalc_jobs_status ON t_factor_recalc_jobs (c_status, c_mtime);
```

说明：

- 不设 `UNIQUE(c_name)`：同一 Python 模块配不同参数可注册为多个因子；冲突由“因子集内输出唯一”约束。
- `c_allow_partial_universe` 取代原 `params_json.allow_degraded`，仅对截面因子有意义。
- 源码以 SQLite 为准；运行时物化为不可变文件 `<factors_dir>/<name>/<source_hash>.py` 供 Python 加载。
- 删除的表：`t_factor_subject_runs`、`t_factor_subject_heads`、`t_factor_subject_gc`、
  `t_factor_period_barriers`、`t_factor_period_pairs`、`t_factor_period_gc`、`t_factor_output_manifests`、
  `t_factor_merged_datasets`、`t_factor_merged_dataset_snapshots`、`t_factor_catalog`、
  `t_factor_engine_status`、`t_factor_bindings`。

## 8. 协议

### 8.1 FactorMgr RPC

| 分组 | RPC | 语义 |
|---|---|---|
| 因子集 | `CreateFactorSet` | 校验源数据集 → 写入 pending → 创建并激活结果数据集 → enabled |
| | `UpdateFactorSet` | 只允许修改 `subject_mode/subjects` |
| | `SetFactorSetStatus` | enabled ⇄ disabled；disabled 后不再处理该数据集事件 |
| | `DeleteFactorSet` | 必须无因子；结果数据集默认保留，`purge=true` 时删除 |
| | `GetFactorSet` / `ListFactorSets` | 返回因子集、成员因子、最近周期运行摘要 |
| 因子 | `CreateFactor` | 必须指定 `set_id`；创建后为 disabled |
| | `UpdateFactor` | 只允许 disabled 时修改 |
| | `SetFactorStatus` | 启用：加列 → enabled → 自动补算；停用：停止写入 |
| | `DeleteFactor` | 只允许删除 disabled 因子；保留结果列 |
| | `GetFactor` / `ListFactors` | 按 `set_id`、状态过滤 |
| 补算 | `RecalcFactors` / `GetRecalcJob` / `CancelRecalcJob` | 异步任务 |
| 运行 | `GetStatus` | 消费者、Python 池、各 lane 状态和最近周期 |

```protobuf
message FactorSet {
  string set_id = 1;
  string space_id = 2;
  string source_dataset_id = 3;
  string freq = 4;
  string subject_mode = 5;
  repeated string subjects = 6;
  string result_dataset_id = 7;
  string status = 8;
  string created_at = 9;
  string updated_at = 10;
}

message FactorDef {
  string factor_id = 1;
  string set_id = 2;
  string name = 3;
  string factor_type = 4;
  string source_code = 5;
  string source_hash = 6;
  repeated string input_columns = 7;
  repeated string outputs = 8;
  string params_json = 9;
  int32 lookback_periods = 10;
  bool allow_partial_universe = 11;
  string status = 12;
  string created_at = 13;
  string updated_at = 14;
}

message RecalcFactorsReq {
  string set_id = 1;
  repeated string factor_ids = 2;
  repeated string subjects = 3;
  string start_time = 4;
  string end_time = 5;
  string request_id = 6;
}
```

### 8.2 事件

消费：`event.storage.collector.period.completed` v1，过滤主题按启用中的因子集生成：
`moox.event.storage.collector.period.completed.v1.<space>.<source_dataset_id>`；durable 名称
`factor_collector_period_v1`。

发布（经 Storage RPC 追加到结果数据集 outbox）：`FactorPeriodComputed`，payload 以因子为单位：

```protobuf
message FactorPeriodState {
  string factor_id = 1;
  string status = 2;                     // complete | degraded | skipped
  repeated string failed_subjects = 3;
  string source_hash = 4;
}

message FactorPeriodComputed {
  string dataset_id = 1;                 // 结果数据集
  string source_dataset_id = 2;
  string frequency = 3;
  int64 period_time = 4;
  string status = 5;                     // complete | degraded
  repeated string universe_subject_ids = 6;
  repeated string failed_subjects = 7;   // 整个标的未算出（上游缺失或读取失败）
  repeated FactorPeriodState factors = 8;
  string trigger_event_id = 9;           // 源 CollectorPeriodCompleted 的 event_id
  google.protobuf.Timestamp computed_at = 10;
}
```

`ViewDataReady` 中的 `repeated FactorBindingPeriodState bindings` 改为
`repeated FactorPeriodState factors`，由 View 原样转发。

确定性 ID：

- 完成标记 event_id = `hash(space, 结果数据集, trigger_event_id, period_time)`；
- 同 ID 不同 payload 由 Storage 返回冲突；Factor 先用 `GetFactorPeriodComputed` 预检，避免重复上报。

### 8.3 Storage RPC

| RPC | 用途 | 改动 |
|---|---|---|
| `PrimaryStore.ReadTimeSeriesRows` | 多 selector + time_range + 列 + 分页读取源数据集 | 无 |
| `PrimaryStore.WriteFactorRows` | 批量整行 upsert 结果数据集，`write_kind=factor_result`，仅 factor 身份 | 新增，取代 `PatchFactor` |
| `PrimaryStore.ReportFactorPeriodComputed` / `GetFactorPeriodComputed` | 上报 / 预检 | payload 调整 |
| `Metadata.GetDataset` / `ListDatasetColumns` | 校验源数据集、获取携带列 | 无 |
| `Metadata.CreateDataset` / `UpsertDatasetColumn` / `ActivateDataset` | 结果数据集及列 | `factor_result` 激活时自动创建默认 View；加列只做增量 schema 变更 |

```protobuf
message PrimaryWriteFactorRowsReq {
  common.AuthInfo auth_info = 1;
  string space_id = 2;
  string dataset_id = 3;
  string commit_id = 4;                  // hash(set_id, period_time, 规范化行)
  repeated RowFieldUpsert rows = 5;
}

message PrimaryWriteFactorRowsRsp {
  common.RetInfo ret_info = 1;
  uint64 rows_written = 2;
}
```

## 9. 包设计

```text
modules/factor/
  cmd/server/          moox-factor
  cmd/cli/             moox-factor-cli：init / import / import-catalog / recalc / run-once / status
  pyworker/            Python worker
  factors/             示例与 XBX 因子
  internal/
    domain/            实体、校验、命名规则
    store/             SQLite 仓储（sets / defs / recalc_jobs）
    catalog/           因子与因子集生命周期、结果数据集对齐
    periodclock/       周期对齐与窗口反算
    trigger/           JetStream 消费、因子集 lane 调度
    pipeline/          plan / load / compute / assemble / write / report
    pyexec/            Python 进程池适配（基于 packages/pyruntime）
    storageio/         读取窗口、写结果行、上报完成标记、元数据
    recalc/            补算任务受理与执行
    rpc/               FactorMgr 实现
    bootstrap/         装配与生命周期
    observability/     指标与健康检查
```

| 包 | 职责 |
|---|---|
| domain | `FactorSet`、`FactorDef`、命名函数、纯函数校验 |
| store | 增删改查；按 `(space, dataset, freq)` 查因子集及其 enabled 因子 |
| catalog | 编排：校验 → SQLite → Storage 元数据对齐 → 自动补算 |
| periodclock | `Align`、`Next`、`Window(T, freq, N)`；一期只实现连续周期 |
| trigger | 解码事件、路由到因子集 lane、ACK/NAK |
| pipeline | `Run(ctx, Plan) → Outcome`，实时与补算共用 |
| pyexec | 调度空闲 worker、超时与崩溃替换 |
| storageio | 封装 Storage RPC，分页、重试分类、规范化 commit_id |
| recalc | 任务表、分块执行、进度、取消 |
| rpc | 参数校验，转发给 catalog/recalc |

## 10. 周期任务流程

### 10.1 接收

1. 解码 `CollectorPeriodCompleted`：space、dataset_id、frequency、period_time、status、
   universe_subject_ids、failed_subjects、event_id。
2. 用 `(space, dataset_id, frequency)` 查 enabled 因子集；没有则 ACK。
3. 预检 `GetFactorPeriodComputed(结果数据集, event_id, T)`；已存在则 ACK。
4. 投递到该因子集 lane；lane 内按到达顺序串行，lane 之间并行，没有优先级。

### 10.2 规划

- 冻结配置快照：开始时刻的 enabled 因子及其 source_hash、参数、lookback；执行中的配置变化从下一周期生效。
- 标的：`expected = universe ∩ 因子集标的范围`，`available = expected − 上游 failed`。
- 窗口：`N = max(lookback)`；`periodclock.Window(T, freq, N)` 得到 `[T₋ₙ₊₁ … T]`；读取范围
  `[T₋ₙ₊₁, T + freq)`。
- 列：源数据集全部业务列（元数据缓存，定期刷新）。
- 无 enabled 因子时仍上报 `complete`、`factors` 为空，保持下游链路推进。

### 10.3 读取

- `available` 按 `read_batch_subjects`（默认 100）分批；每批一次 `ReadTimeSeriesRows`：
  selectors 为该批标的、不设 series_tag，time_range 为读取范围，列为全部业务列，分页读完。
- 批间并发上限 `read_workers`（默认 4）；单批超时或临时错误重试 2 次，仍失败则该批标的计入
  `failed_subjects`，周期降级。
- 全部批次失败或 Storage 不可用：返回基础设施错误，NAK 延迟重投，不上报。
- 结果按标的组织为只读 `Frame`，所有因子共享，不复制。
- 缺失 K 线不补齐；期望时间序列通过 `context.period_times` 交给因子。

### 10.4 计算

- 时序因子：每个标的一次 Python 调用，请求携带该标的 Frame 和全部时序因子描述；Python 端按每个
  因子的 lookback 截取窗口、只投影其 `input_columns`，单因子失败互不影响。
- 截面因子：每个截面因子一次调用，传全部可用标的面板（按该因子 lookback 截取）。
  `allow_partial_universe=false` 且 `available ≠ expected` 时本周期 skipped；为 true 时按可用标的计算。
- 全部调用共享 Python 进程池（`python.workers`），单次调用超时 `python.task_timeout`。
- 计算失败（异常、超时、输出不合法）只影响“该因子 × 该标的”；截面失败影响该因子全部标的。

### 10.5 组装

- 只保留 `data_time == T` 的输出。
- 行身份 `(subject, T, series_tag)`：源数据集在 T 存在的 tag 行，携带列取自源行、因子列取自对应输出；
  因子派生的新 tag（例如 `binance-okx` 价差）只写因子列。
- NaN、±Inf 统一为 null；非数值为输出不合法。
- 失败因子在该标的源 tag 行上把输出列显式写 null。实时首次计算时 T 行无旧值；重算时 null 清除旧值。
- 每个周期 T 都是新的行键，实时链路不存在残留，不需要 manifest。

### 10.6 写入

- 按 `write_batch_rows`（默认 1000）分批调用 `WriteFactorRows`；`commit_id = hash(set_id, T, 批内规范化行)`。
- 写入失败重试 3 次，仍失败返回基础设施错误，NAK 重投整周期；已写批次由幂等覆盖。

### 10.7 上报与 ACK

- 所有因子 complete 且 `failed_subjects` 为空时周期为 `complete`，否则 `degraded`。
- `ReportFactorPeriodComputed` 成功后 ACK；失败返回基础设施错误，NAK 重投。
- 输出一条汇总日志：周期、耗时、标的数、各因子状态、读/算/写阶段耗时。

### 10.8 周期预算

- 单周期预算 `clamp(2 × freq, period_budget_min=60s, period_budget_max=15m)`。
- 超出预算：取消在途计算并记失败，已完成部分照常组装写入，上报 `degraded` 后 ACK。

## 11. Python 计算契约

入口固定为 `compute(df, params, context) -> pandas.DataFrame`。

| 项 | 时序因子 | 截面因子 |
|---|---|---|
| df 列 | `data_time, series_tag` + `input_columns` | `data_time, series_tag, subject_id` + `input_columns` |
| df 行 | 单标的、窗口内全部 tag，按 `(data_time, series_tag)` 升序 | 全部可用标的 |
| 输出列 | 恰好 `data_time, series_tag` + `outputs` | 恰好 `data_time, series_tag, subject_id` + `outputs` |
| 输出身份 | `(data_time, series_tag)` 唯一 | `(data_time, series_tag, subject_id)` 唯一，subject 必须在可用集合内 |

context：

| 字段 | 类型 | 说明 |
|---|---|---|
| `period_time` | int | 目标周期 T |
| `frequency` | str | 频率 |
| `period_times` | list[str] | 本因子 lookback 对应的期望时间序列（RFC3339Nano） |
| `subject_id` | str | 仅时序因子 |
| `expected_subjects` | list[str] | 仅截面因子 |
| `available_subjects` | list[str] | 仅截面因子 |

请求帧：一份共享 frame + 因子列表，每项
`{factor_id, name, source_hash, source_path, factor_type, input_columns, outputs, params, lookback_periods}`；
响应按 `factor_id` 逐项返回 `ok/results` 或 `error`。删除单因子模式、`task_id/binding_id`、
`config_snapshot_id`、`missing_subjects`。传输继续使用 JSON。

因子独立性约束不变：因子只读取所属因子集源数据集中声明的输入列，不引用其他因子的源码或结果，
不提供 DAG；复合逻辑在单个 `compute` 中展开，`lookback_periods` 必须覆盖完整原始历史。

## 12. 结果数据集与 View

- 创建：`CreateDataset(dataset_id=dataset_factor_…, dataset_role=factor_result, write_owner=factor,
  data_node_id=源数据集节点, freqs=[freq], keep_duration=源数据集保留期)` → 写入列（携带列 + 已有因子
  输出）→ `ActivateDataset`。
- View：Storage 在 `factor_result` 数据集激活时自动创建默认 View（主键 subject/freq/data_time/series_tag，
  列随数据集增量同步）；`ActivateDataset` 成功即表示 View 可用。因子不持有 view_id。
- 启用因子：`UpsertDatasetColumn` 增加输出列，View 增量加列，不触发 A/B 重建。
- 源数据集新增列：`ReconcileSet` 同步到携带列（启动、生命周期操作、定期元数据检查时执行）。
- 停用或删除因子：保留列、停止写入，不改 schema、不重建 View。
- 写入权限：只有 factor 身份可以写 `factor_result` 数据集；导入和手工 upsert 被拒绝。

## 13. 生命周期

```text
因子集:  pending ──(结果数据集激活成功)──▶ enabled ⇄ disabled ──(无因子)──▶ 删除
因子:    disabled ──启用──▶ enabled ──停用──▶ disabled ──▶ 修改 / 删除
```

| 操作 | 步骤 | 生效 |
|---|---|---|
| 创建因子集 | 源数据集 active、freqs 含该频率、角色不是 factor_result → pending → 创建并激活结果数据集 → enabled → 刷新消费过滤 | 下一个周期事件 |
| 创建因子 | `input_columns ⊆ 源数据集列`；输出合规且不冲突；lookback ≥ 1；源码试 LOAD 成功 → disabled | — |
| 启用因子 | 加列 → enabled → 自动补算 `[now − 结果数据集保留期, 当前周期)`，仅该因子 | 实时从下一周期，历史由补算回填 |
| 修改因子 | 只能 disabled 时修改；流程“停用 → 修改 → 启用（自动补算）” | — |
| 停用因子集 | 移出消费过滤；在途周期允许完成 | 立即 |

生命周期操作与周期任务、补算块之间用因子集级锁串行。

## 14. 补算

- 请求：`set_id`、`[start, end)`、可选 `factor_ids`（默认全部 enabled）、可选 `subjects`、`request_id`。
- 执行：按 `recalc.chunk_periods`（默认 2000）切块顺序处理，完成一块更新 `c_progress_time`。
  每块内每个标的读取 `[块起点 − (N−1)·freq, 块终点)`；时序因子每标的一次调用算完整块；截面因子逐周期调用。
- 写入：只写指定因子的输出列加携带列；块内失败结果写 null。
- 不发布 `FactorPeriodComputed`。
- 与实时共享 Python 池；每块开始前获取因子集锁、结束释放，实时周期可插入块间执行。
- 影响范围由调用方决定（D14），系统不自动扩展。
- 自动触发：启用因子；创建因子集时回填保留期窗口。
- CLI：`run-once` 同步执行一次补算并打印结果；`recalc` 提交异步任务。

## 15. 失败语义

| 场景 | 行为 |
|---|---|
| 事件重复投递 | 预检已上报则 ACK；否则重算，写入幂等 |
| 写到一半进程退出 | 重投后整周期重算覆盖 |
| 已上报但 ACK 前退出 | 预检命中，不重复计算 |
| 上游 degraded | 失败标的跳过并计入 failed_subjects；截面按 `allow_partial_universe` 处理 |
| 部分读取批次失败 | 该批标的失败，周期 degraded |
| Storage 读/写/上报整体不可用 | NAK 延迟重投，不上报 |
| 单因子异常或超时 | 该因子 × 标的写 null，周期 degraded |
| Python 进程崩溃 | pyexec 替换进程，本次调用失败 |
| 超出周期预算 | 已完成部分写入，其余失败，上报 degraded 后 ACK |
| 重启后积压 | durable 回放，各 lane 依次处理 |
| 迟到数据 / 交易所修正 | 显式补算 |
| 配置变化 | 下一周期生效 |
| 首次启动 | `DeliverNew`，不回放历史；历史靠补算 |

## 16. 容量估算（crypto 1m）

假设 500 个标的、2 个 tag、12 个因子、N = 200：

| 阶段 | 估算 |
|---|---|
| 读取 | 5 批 × 4 万行，每页 5000 行约 8 页；4 路并发 1–3 秒 |
| 内存 | 500 × 200 × 2 × 10 列 ≈ 200 万值，数十 MB |
| 计算 | 500 次时序调用 × 约 50ms，8 进程约 3–4 秒 |
| 写入 | 约 1000 行，1–2 次请求 |
| 合计 | 约 5–10 秒，低于 60 秒预算 |

二期优化（实测后再做）：内存维护每标的最近 N 根环形缓冲，每周期只读新增一根，重启或发现缺口时全量读取。

## 17. 配置

```yaml
database:
  path: ./data/factor/factor.db
storage:
  gateway_target: ip://127.0.0.1:11003
  gateway_node_id: ""
  key_id: factor
  hmac_key_file: ""
eventbus:
  urls: [nats://127.0.0.1:4222]
  credential_file: ~/.config/moox/eventbus/factor-eventbus.yaml
  fetch_max_wait: 10s
python:
  bin: python3
  worker_path: ./pyworker/worker.py
  factors_dir: ./data/factor/factors
  workers: 8
  task_timeout: 30s
pipeline:
  read_batch_subjects: 100
  read_workers: 4
  read_timeout: 20s
  write_batch_rows: 1000
  period_budget_min: 60s
  period_budget_max: 15m
recalc:
  chunk_periods: 2000
```

## 18. 可观测性

| 类型 | 内容 |
|---|---|
| 指标 | `factor_period_duration_seconds{set,stage}`、`factor_period_lag_seconds{set}`、`factor_period_total{set,status}`、`factor_failures_total{set,factor,reason}`、`factor_lane_backlog{set}`、`factor_python_busy`、`factor_last_period_time{set}` |
| 日志 | `factor_period_start`、`factor_period_done`、`factor_compute_failed`、`factor_read_retry`、`factor_write_retry`、`factor_report_failed`、`factor_recalc_progress` |
| 健康 | SQLite、EventBus 消费者、Python 池、Storage 写入（连续失败锁存）、lane 卡住（单周期超过预算 2 倍） |

日志不包含源码、凭证或请求体。

## 19. 上下游改造

| 模块 | 改造 |
|---|---|
| Collector | 现货与永续写入同一数据集 `dataset_binance_kline_1m`，series_tag 沿用 `defaultMarketSeriesTag`；周期完成期望项按 `(subject, series_tag)` 统计，全部 tag 终态后才上报 |
| Storage | 新增 `WriteFactorRows`；`factor_result` 激活自动建默认 View，加列增量同步不重建；`FactorPeriodComputed`/`ViewDataReady` 改为 `factors[]`；强制数据集单 DataNode；删除因子元数据实体；删除 Merge 事件处理、`mdataset_` 前缀和 `merged_factor` 角色 |
| Strategy | 读取结果 View；按 `factors[]` 判断降级；`factorio` 改为查询因子集；删除 merge 完成类型判断 |
| Merge | 删除模块、部署、配置、凭证 |
| Web | 因子页面改为“因子集 / 因子定义 / 补算任务 / 结果”，删除 bindings、construct、mdataset 页面 |
| CLI / Admin | `moox.toml` 因子配置改为因子集 + 因子；删除 merge 与 factor-engine 部署项；更新 EventBus 凭证中的 consumer 名称 |

## 20. TODO

- 交易日历市场（A 股分钟、日线）：`periodclock` 基于 `packages/marketcalendar` 与交易时段模板实现同一接口。
- 环形缓冲增量读取（视实测性能）。
- `committed_positions` 在单 DataNode 约束下简化为单个位置（Storage 侧独立排期）。
