# 采集共享实例一致性与生命周期边界优化执行计划

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development 或 superpowers:executing-plans。步骤使用 checkbox 跟踪。

**Goal:** 在现有共享采集模型基础上，补齐 Completion 的完整关联校验与原子提交、多目标失败重试验证、Dataset 多 Tag 对象并集的 period completion barrier、Collector-owned Dataset 删除边界，并将 Dataset period 完成判定收敛为 Storage DataNode 本地的同步提交能力，满足个人量化系统的简洁性、一致性与正式发布门禁。

**Architecture:** 保持 CollectionTask、Tag、TaskInstance、WriteTarget、CollectorRun 的职责划分。Completion 先验证 Batch→BatchItem/Instance→WriteTarget→Dataset 的完整关联，再以 Batch terminal CAS 作为事务门槛，在同一事务中提交目标状态、重试项、实例和 Batch 状态。CollectionTask 的全部 Tag 展开后按规范 series identity 求并集，canonical sort 后分配稠密 `series_index`，并对有序 `series_key` 集合计算 `series_hash`；同一对象集合不因 Run 重复复制。Dataset 是 Storage DataNode 的最小路由与一致性单元，不允许一个 Dataset 拆分到多个 DataNode。Scheduler 在 period 开始前通过 `EnsureDatasetPeriod` 把 `series_hash + expected_count + deadline` 初始化到该 Dataset 所在 DataNode；每个 SCF 只接收自己负责采集的 items，并在 `CommitTimeSeriesBatch` 中携带这些 row 的 `series_index`。DataNode 使用 Pebble 同一个 Batch 原子提交 K 线 KV 与 bitmap OR Merge；不持久化 SuccessCount，commit 后读取合并 bitmap 并 `popcount` 判断是否全部完成。`waiting→complete` 时再以幂等 finalization 写 status + completion outbox；后台 finalizer 负责 crash recovery 与 deadline degraded。Dataset 完成计算不再依赖 Collector 异步消费全部 `DatasetRowsUpserted`，仅保留异步最终通知。CollectorRun 不保存完整 series snapshot，planning 使用统一 cutoff + `definition_hash/series_hash` 防止同一轮混用新旧集合。

**Tech Stack:** Go、GORM、SQLite、Protobuf、Vue 3、Vitest。

---

## 当前基线

已确认的现状：

- CollectionTask 不绑定 Provider 或 MarketType。
- Tag 固定绑定单一 source 和 market_type。
- TaskInstance 表示共享 Provider 请求。
- WriteTarget 表示任务 Dataset/View 的独立写入目标。
- Fetch retry 按 InstanceID 扇出到全部启用目标。
- Write-target retry 只针对指定 WriteTarget。
- Scheduler 已采用先完成目标规划、再创建 Batch、最后 dispatch 的两阶段流程。
- DefinitionHash 包含输出字段，输出字段属于任务定义。
- Dataset period 完成不是 Batch、TaskInstance 或单个 WriteTarget 完成的同义词；完成条件是该 Dataset 对应任务全部 Tag 展开后的规范 series 并集全部真实写入对应 period。
- 任务当前对象集合维护在 `t_collector_task_series`：canonical sort 后分配稠密 `series_index=0..N-1`，并对排序后的 `series_key` 集合计算稳定 `series_hash`；集合不变时 hash/index 映射保持不变，不为每个 CollectorRun 重复复制全部对象。
- Dataset 是 Storage DataNode 的最小路由和一致性单元：一个 Dataset 只能绑定一个 DataNode；K 线 KV、period progress bitmap 和完成 outbox 都落在该 DataNode 的同一个 Pebble DB。
- Scheduler 在 period 开始前调用 `EnsureDatasetPeriod` 写入 `DatasetPeriodExpectation(series_hash, expected_count, deadline)`；Storage 不解析 Tag，也不自行计算 expected_count。
- 每个 SCF 只携带本次负责的标的及其 Dataset 维度 `series_index`；共享 Provider fetch 扇出到不同 WriteTarget 时，各 Dataset 可以拥有不同的 series_index/series_hash。
- SCF 通过 `CommitTimeSeriesBatch` 上报结果；DataNode 用同一个 Pebble Batch 原子提交 K 线和 bitmap OR Merge。Dataset 完成计算不再消费全部 `DatasetRowsUpserted`，而是在 commit 后读取 bitmap 并 popcount；只在最终状态变化时写 completion/degraded outbox。
- 不持久化 SuccessCount；bitmap 是完成进度的唯一真相，重复上报通过 OR Merge 天然幂等。
- CollectionTask 创建/修改从下一采集周期生效；Run 规划固定 task_id + definition_hash + series_hash，恢复时发现集合 hash 已变化则终止旧 planning Run 并由下一轮重新规划，不追求同一 Run 的重型成员快照恢复。
- 不做历史数据迁移，按新项目策略重建数据库。

## 文件范围

### Completion / Dataset Period Commit & Final Notification

- 修改 modules/collector/internal/marketfetch/completion.go
- 修改 modules/collector/internal/store/fetch_batch.go
- 修改 modules/collector/internal/store/write_target.go
- 修改 modules/collector/internal/store/fetch_retry.go
- 修改 modules/collector/internal/marketfetch/scheduler.go：生成 DatasetPeriodExpectation、SCF 子集 items、WriteTarget 的 dataset-specific series_index/series_hash
- 新增/修改 Storage RPC 与 protobuf：`EnsureDatasetPeriod`、`CommitTimeSeriesBatch`
- 修改 modules/storage/internal/service/primarystore：PrimaryStore 路由到 Dataset 所在 DataNode，并实现两个新接口的授权/校验
- 修改 modules/storage/internal/service/datanode/pebble/store.go：period expectation/progress key、bitmap Merger、K 线 + bitmap Merge 原子 batch
- 修改 DataNode Pebble 打开配置：注册 bitmap OR Merger；确认当前 Pebble v1.1.5 下 Merge 的恢复/compaction 行为测试通过
- 修改 modules/storage/internal/service/datanode/outbox：仅在 complete/degraded finalization 时产生最终通知，复用现有 Pebble outbox relay
- 新增 DataNode period finalizer：修复“数据+bitmap 已提交但完成 outbox 尚未生成”的 crash window，并按 deadline 将 waiting 转 degraded
- 移除 Dataset 完成判定对 `modules/collector/internal/marketfetch/storage_write_consumer.go`、`period_readiness.go`、`period_reporter.go` 的依赖；若这些文件无其它职责则删除对应 readiness 投影代码/表，否则保留非完成判定用途
- 测试 Collector completion/fanout、Storage CommitTimeSeriesBatch、bitmap merge 幂等、complete/degraded finalizer、crash recovery、outbox 唯一通知
- 测试 modules/collector/internal/store/fetch_batch_test.go 与 Storage DataNode/Pebble 相关测试

### Dataset 生命周期

- 修改 modules/storage/internal/service/metadata/sqlite/crud_dataset.go
- 修改 modules/storage/internal/service/catalog/validate.go
- 修改 Collector TaskResult 删除流程
- 增加 Storage 和 Collector 删除边界测试

### Task Series / Run 生效边界与发布

- 修改 modules/collector/internal/marketfetch/scheduler.go
- 修改 modules/collector/internal/store/run.go
- 新增或扩展 task series domain/store：维护 `t_collector_task_series` 的 deterministic `series_index` 与任务级 `series_hash`
- 修改 modules/collector/schema/collector.sql
- 修改 modules/collector/internal/store/database.go，并补空库初始化测试
- 修改 modules/collector/internal/rpc/service.go
- 更新设计文档、部署说明和验收记录
- Storage schema 仅在 Dataset 删除保护确需新增持久化字段/约束时修改，不为本计划额外引入 snapshot 表

---

## Task 1：Completion 完整身份与关联校验

- [x] 增加非法 completion 测试：Batch 不存在、Batch identity 不匹配、Instance 不属于 BatchItem、WriteTarget 不属于 Instance、TargetResult.dataset_id 与 WriteTarget.dataset_id 不一致、Space 不匹配、目标已解绑。
- [x] Handler 先加载 Batch 并校验不依赖可变关系的 envelope identity：batch_id、schedule_id、batch_kind、frequency 等；任何校验失败不得产生副作用。
- [x] 在 Store/事务层提供 CompletionScope 校验，按 space_id + batch_id 约束 BatchItem/Instance/WriteTarget/Dataset 关系；事务外查询只用于诊断，不作为最终授权依据，避免 TOCTOU。
- [x] 调整 completion 顺序：加载 Batch → 校验 envelope identity → 构造候选 effects → 进入 CompleteWithEffects 事务 → Batch terminal CAS → 事务内再次校验 BatchItem/Instance/WriteTarget/Dataset → 应用 effects。
- [x] 在 Batch CAS 和全部关联校验成功前，禁止更新任何 WriteTarget、RetryItem 或 TaskInstance。
- [x] 运行：

    cd modules/collector
    go test ./internal/marketfetch -run 'Test.*Completion|Test.*Identity' -count=1

验收：非法关联返回错误；WriteTarget、RetryItem、TaskInstance、Batch 均无副作用。

## Task 2：Completion CAS + effects 原子提交

- [x] 扩展 FetchCompletionEffects，携带 WriteTargetStatusEffect、RetryItem 以及已有 TaskInstance freshness/status effects。
- [x] `FetchBatchRepository.CompleteWithEffects` 继续作为唯一事务 owner，使用同一个 `tx *gorm.DB` 调用关联校验和 WriteTarget/Retry/TaskInstance helper，禁止各 Repository 在 completion 内部再开启独立事务。
- [x] 事务第一道门必须是 Batch terminal CAS；保留现有 normal completion 与 first-late-completion 两套 CAS 语义，不把 `timed_out` 简单并入普通状态条件。CAS 未命中时不得应用任何 effects，并按现有状态区分幂等重复、已处理迟到回调和非法状态。
- [x] 在同一事务中完成：Batch CAS → BatchItem/Instance/WriteTarget/Dataset 关联校验 → WriteTarget 状态 → RetryItem → TaskInstance 状态/新鲜度；关联校验或任一步更新失败时 CAS 也随事务整体回滚。
- [x] 删除 handleCompletion 中事务前的独立 UpdateWriteTargetStatus 调用。
- [x] Dataset period progress 不进入 Collector Completion 事务；Provider/Batch completion 只更新执行状态。真实落盘与 period progress 由 Storage `CommitTimeSeriesBatch` 的 Pebble batch 原子推进，Collector 不再通过 DatasetRowsUpserted 投影完成度。
- [x] 增加故障注入测试，验证 WriteTarget、RetryItem、TaskInstance 或 Batch 任一步失败时全部回滚，并验证重复 completion 不重复应用 effects。
- [x] 运行：

    cd modules/collector
    go test ./internal/store -run 'Test.*Complete|Test.*Retry|Test.*Transaction' -count=1

验收：不存在“目标状态已落库但 RetryItem/TaskInstance/Batch 未落库”的中间状态；Batch completion 不直接产生 Dataset period complete。

## Task 3：多目标部分失败 E2E

- [x] 构造一个共享 Instance 和两个目标：Target A→Dataset A，Target B→Dataset B。
- [x] 模拟 A 成功、B 失败，断言 Instance fetch 成功、A 成功、B 失败。
- [x] 断言仅创建 RetryScope=write_target、WriteTargetID=B 的 RetryItem。
- [x] 调度重试，断言请求只携带 B，不重复写入 A。
- [x] 模拟 B 重试成功，断言 A/B/Instance 最终状态正确，且 RetryItem(B).status=succeeded。
- [x] 用 fake Storage 记录写次数：Dataset A 只写 1 次，Dataset B 初次失败后只在目标级 retry 再写 1 次，证明 sibling target 未重复写入。
- [x] 运行：

    cd modules/collector
    go test ./internal/marketfetch ./internal/store -run 'Test.*Target|Test.*Fanout|Test.*Retry' -count=1

## Task 4：Task Series、CommitTimeSeriesBatch 与 Dataset Period Barrier

- [x] 先写 task-series union 测试：Tag A={BTC,ETH}、Tag B={ETH,SOL} 时集合必须只有 BTC/ETH/SOL；同一 subject 在不同 Provider/市场下保留为不同 series。
- [x] `t_collector_task_series` 保存任务当前 Tag 并集展开结果，至少包含 space_id、task_id、series_key、series_index、subject_id、source/provider route、market_type、provider_symbol、series_tag；`(space_id, task_id, series_key)` 与 `(space_id, task_id, series_index)` 唯一。
- [x] 每次重算 task series 时先按 canonical `series_key` 排序，再确定性分配 `series_index=0..N-1`；对排序后的 series_key 序列计算 `series_hash`。集合完全相同时不改 hash/index；有增删或 routing identity 变化时得到新 hash。
- [x] Dataset 是 DataNode 最小路由/一致性单元，一个 Dataset 不得拆到多个 DataNode；Dataset 的 period expectation、bitmap、status、deadline、completion/degraded outbox 与 K 线 KV 必须在同一 DataNode 的 Pebble 中持久化。
- [x] 新增 `DatasetPeriodExpectation` 与 `EnsureDatasetPeriod`：Scheduler 在 dispatch 前按 Dataset + frequency + period 初始化 `series_hash`、`expected_count`、deadline；调用必须幂等，相同 identity 重试成功，不同 hash/count 冲突直接拒绝。Storage 不读取 Tag，也不自行推导 expected_count。
- [x] Scheduler 拆分 SCF 请求时，每个 SCF 只携带自己负责采集的 items，不下发 Dataset 的全部几千个对象；每个 item 携带 Provider 采集所需 identity 与 Dataset 维度 `series_index`。公共字段只携带 dataset/period/series_hash/expected_count 等小量元数据。
- [x] 对共享 TaskInstance 的多 WriteTarget fanout，`series_index`/`series_hash` 属于 Dataset/WriteTarget 侧，不属于共享 Provider fetch identity；同一 BTC 在 Dataset A/B 可分别映射到 index 17/203。
- [x] 新增 Storage `CommitTimeSeriesBatch`：SCF 只上报本批实际采集结果，每个 row 携带 `series_index`；Storage 校验 series_hash 与 period expectation 一致、series_index < expected_count，并保持现有 required-fields/row validation。
- [x] 第一版 bitmap 使用一个紧凑 value，不做 shard；5000 series 约 625B、20000 series 约 2.5KB。只有实际规模达到需要降低 value 写放大时再引入 bitmap shard。
- [x] 在当前 Pebble v1.1.5 注册专用 bitmap OR Merger；`CommitTimeSeriesBatch` 构造本批 delta bitmap，并在一个 Pebble Batch 内提交 K 线 KV/history 与 `Merge(bitmap, delta)`。OR Merge 必须满足重复请求幂等和并发 Merge 不丢 bit，并补 compaction/reopen/recovery 测试。
- [x] 不持久化 `SuccessCount`；bitmap 是唯一完成进度。每次 K 线 + bitmap batch commit 成功后读取合并后的 bitmap，使用 popcount 与 `expected_count` 比较；成本只与 bitmap 字节数相关，不遍历 Dataset K 线或全部业务对象。
- [x] complete 检测采用两阶段：第一阶段原子提交 rows + bitmap Merge；第二阶段在 commit 后 `Get(bitmap) + popcount`。若已满，进入 period finalize 临界区，再次确认 status=waiting 后，以第二个 Pebble Batch 原子写 `status=complete + completion outbox`；并发多个“最后请求”只能产生一次最终通知。
- [x] 增加后台 period finalizer：启动/周期扫描只读取 waiting progress/deadline 索引，不扫描 K 线。若 crash 发生在 bitmap 已满但 outbox 尚未写入之间，finalizer 恢复后补 `complete + outbox`；deadline 到达且 bitmap 未满时写 `degraded + outbox`。
- [x] 最终通知继续复用现有 DataNode Pebble outbox relay；删除“每条 DatasetRowsUpserted 都由 Collector 异步消费并计算 readiness”的链路。若 DatasetRowsUpserted 仍被其它功能使用可以保留事件，但不得再作为 Dataset period completion 的依赖。
- [x] 增加测试：Tag union/index/hash 确定性；每个 SCF 只收到自身 items；不同 Dataset 的同一 series 可有不同 index；重复 Commit 不改变 bitmap 语义；并发 Merge 不丢位；最后一批只发一次 complete；commit 后 crash 可由 finalizer 补通知；deadline 产生 degraded。
- [x] 运行：

    cd modules/collector
    go test ./internal/marketfetch ./internal/store -run 'Test.*Series|Test.*Fanout|Test.*Scheduler' -count=1
    cd ../storage
    go test ./internal/service/primarystore ./internal/service/datanode/... -run 'Test.*CommitTimeSeries|Test.*Period|Test.*Bitmap|Test.*Outbox|Test.*Finalizer' -count=1

验收：Dataset complete 由该 DataNode 本地 bitmap barrier 推导；SCF 不携带全量 Dataset 对象；K 线与 bitmap 进度同一个 Pebble batch 原子提交；不维护 SuccessCount、不扫描全部 K 线、不依赖 Collector 的 DatasetRowsUpserted readiness consumer。

## Task 5：Collector-owned Dataset 删除边界

- [x] 统一使用 attributes.owner_module=collector 识别 Collector Dataset。
- [x] 先复用现有 Storage internal service-auth 机制：Collector 调用使用 `AuthInfo{app_id, app_key}` 的现有签名规则，Storage 复用已有 secret/serviceAuthKey 校验路径；本计划不新增独立 timestamp/nonce/HMAC 协议。
- [x] 禁止仅凭 auth_info.app_id="collector" 判断身份；未签名、签名错误或非 Collector 服务身份删除 Collector-owned Dataset 时拒绝。
- [x] 收敛删除顺序：disable → drain planned/dispatched batch → 删除目标级 retry → 删除任务 WriteTarget → 确认无其他引用 → 删除 View/Dataset → 删除 Task。
- [x] Dataset 仍被其他任务 WriteTarget 引用时禁止删除，作为跨模块不变量保护；普通 Dataset 删除行为保持不变。
- [x] 增加测试：仅伪造 collector app_id 失败、合法现有 service-auth 凭据成功、未 drain 删除失败、正常 Collector 删除成功、仍有其他引用时保留、普通 Dataset 行为不变。
- [x] 运行：

    cd modules/storage
    go test ./internal/service/metadata/sqlite ./internal/service/catalog -count=1
    cd ../collector
    go test ./internal/planner/taskresult ./internal/store -count=1

## Task 6：轻量 CollectorRun 生效边界与 series_hash 检查

- [x] 不新增 RunTaskSnapshot / RunSeriesSnapshot 成员表；CollectorRun 只保存执行轮次本身，避免每分钟为几千个 series 重复复制成员。
- [x] 将 Run 的不可变 `c_create_time` 作为 planning cutoff（若现有字段语义不足再增加单一 `planning_cutoff_at`，但不增加成员快照表）。所有 Scheduler 获取同一个 scheduled Run 时使用同一个 cutoff。
- [x] 当前 Run 只允许规划 cutoff 时已经存在且当前仍 enabled 的任务；cutoff 后新建、重新启用、definition 修改或 task-series hash 变化的任务不加入该 Run，统一从下一 Run 生效。
- [x] Scheduler 对每个 eligible task 一次性读取 `definition_hash`、`series_hash`、`expected_count` 和对应 `t_collector_task_series`；本次 planning 内固定使用这组 identity，不在同一 Run 中重新展开 Tag 或追加入新 series。
- [x] TaskInstance/Provider 请求只从 `t_collector_task_series` 生成；同一 Run 内相同 request_key 仍按现有共享实例规则复用。`series_hash/series_index` 只用于 Dataset/WriteTarget 写入与 period expectation，不污染 Provider request_key。
- [x] Task series 重算事务必须原子替换 series rows、series_index 映射、series_hash 和可用于 cutoff 判断的修改时间；如果 planner 读取集合后、写 Instance/EnsureDatasetPeriod 前发现 `definition_hash` 或 `series_hash` 已变化，则放弃该 task 在当前 Run 的本次规划，不混合两个集合。
- [x] 对 crash recovery 采用个人系统的简化策略：旧 Run 恢复时若任务当前 definition/series hash 已晚于 cutoff 或无法与已规划内容一致确认，则将该 Run/任务标记 stale/failed，由下一轮重新规划；不要求恢复宕机前完整的几千对象成员集合。
- [x] 已经进入 planned/dispatched Batch 的旧 hash 请求允许按其持久化 Instance/WriteTarget/period expectation 完成；后续 Tag 变化不得中途重写旧 period 的 hash、index 或 expected_count。
- [x] 任务创建/修改成功提示“变更成功，将从下一采集周期开始生效”。
- [x] 增加并发测试：Scheduler A/B 获取同一 Run 后，cutoff 后新建任务或 series_hash 变化均不得由 B 追加进旧 Run；下一 Run 使用新 hash；同一 Run 的共享 request_key 仍只产生一个 TaskInstance。
- [x] 在设计文档中写明：“CollectionTask/Tag 变更从下一采集周期生效；CollectorRun 以统一 planning cutoff + definition_hash/series_hash 防止同一 Run 混用不同对象集合，不保存完整 series snapshot。”

## Task 7：Source capability 策略

- [x] 盘点结果：Storage seed 中存在尚未接入 subject lister 的 DataSource，且 Storage bootstrap 创建 Tag 早于 `collector-subject` 发布 `subject_listing`；因此当前不能全局 fail-closed。内建采集 Tag 使用的 `binance`（spot/swap）和 `eastmoney`（equity）由现有 subject lister 覆盖。
- [x] 最终策略固定为 **fail-open + 已声明时强校验 + planning 再校验**：缺失 `subject_listing` 时允许创建 Tag，但必须输出 warning；存在 capability 时 JSON 损坏或不支持目标 `market_type` 均直接拒绝。
- [x] Collector planning 继续从 Tag.source/market_type 解析实际 Provider route；找不到 source route、provider symbol 或 market 兼容性时跳过/拒绝该任务，不允许 capability 缺失静默 dispatch。
- [x] 将策略写入 Tag 设计文档，并覆盖：无 capability 可创建、已声明支持可创建、已声明不支持拒绝、损坏 capability 拒绝。
- [ ] 发布前验证 `collector-subject` 启动后内建 Tag source 均已写入预期 `subject_listing`，并确认 warning 中不存在计划外的新 source。

## Task 8：清理前端兼容字段

- [x] 保留 TaskInstance 页面展示 Provider、Source、MarketType。
- [x] CollectionTask 创建和更新 payload 不得从这些字段反推任务 Provider。
- [x] 将 kline_resample 所需的 provider、market_type、source_dataset_id、source_series_tag 等明确收敛到 Resample params 类型，不继续借用通用 CollectionTaskRecord 的任务级字段。
- [x] 在确认重采样创建/编辑/详情页面均只从 resample collect_params 读取后，删除 CollectionTaskRecord 中的 legacy Provider/MarketType 兼容读取。
- [x] 运行：

    cd web
    pnpm exec vue-tsc --noEmit
    pnpm test -- collection-tasks task-instances
    pnpm run build:prod

## Task 9：全量验证与发布门禁

- [x] 运行后端全量测试：

    cd modules/collector
    go test ./... -count=1
    cd ../storage
    go test ./... -count=1

- [x] 运行前端类型检查、全量测试和生产构建。
- [x] 使用空数据库/空 DataNode 重建验证：Collector Schema 的 Task/TaskSeries/Run/Instance/WriteTarget/Batch/Retry；Storage Dataset→DataNode 单节点约束；Pebble period expectation/bitmap/status/deadline/outbox key；确认不再依赖旧 PeriodReadiness projection 表来判定 Dataset complete，且没有引入 RunTaskSnapshot/RunSeriesSnapshot 表。
- [x] 验收 task series：多 Tag 并集去重、跨 Provider 同 subject 独立、canonical index 稳定、`series_hash` 对集合变化敏感且集合不变时稳定；几千 series 只在 task 当前集合中保存一次。
- [x] 验收 SCF payload：一个 Dataset 有数千 series 时，每个 SCF 只收到自身负责的子集；共享 fetch 扇出到多个 Dataset 时各自使用正确的 dataset-specific series_index/series_hash。
- [x] 验收 `EnsureDatasetPeriod`：expected_count/series_hash 由 Collector 注入；重复初始化幂等；同一 Dataset/frequency/period 的冲突 hash/count 被拒绝；period progress 与 Dataset K 线落在同一 DataNode。
- [x] 验收 `CommitTimeSeriesBatch`：K 线 KV 与 bitmap Merge 同一个 Pebble batch；重复/并发上报不丢 bit；不维护 SuccessCount；commit 后 popcount 得出正确完成状态；普通 commit 不产生海量行级 readiness 事件。
- [x] 验收最终通知：最后一批完成只生成一次 complete outbox；在 rows+bitmap commit 后、finalize 前模拟 crash，重启 finalizer 能补发；deadline 到达且 bitmap 未满时产生 degraded；outbox relay 重投不产生重复业务事件。
- [x] 验收运行链路：跨 Provider 多 Tag、跨任务共享 Instance、多 Dataset 扇出、目标级失败重试、非法 completion 无副作用、Completion effects 原子性、下一周期生效、planning hash 变化不混用、Collector Dataset 删除保护、View 命名和 DefinitionHash 去重。
- [x] 以下任一项失败不得发布：完整关联校验、Batch CAS + effects 原子提交、多目标 E2E、TaskSeries index/hash、EnsureDatasetPeriod、CommitTimeSeriesBatch 原子写、bitmap Merge 幂等、complete/degraded finalizer、series_hash 生效边界、Dataset 删除边界、空库/DataNode 初始化、Go 或前端构建。
- [ ] 正式环境按新项目流程执行：确认允许清理历史数据 → 停止旧调度入口 → 重建 Collector Schema 与 DataNode Pebble → 发布 Storage/Collector/Web → 初始化 DataSource、Tag、字段和采集方法 → 构建首批 task series → 验收首个 EnsureDatasetPeriod / CommitTimeSeriesBatch / CollectorRun 及首个 Dataset complete/degraded event → 开启新执行节点。


## 验收记录模板

- Completion 完整关联校验：通过；覆盖 unknown/mismatched envelope、Instance 非 BatchItem、Target 非 Instance、Dataset/Space 不匹配、已解绑目标，非法 completion 零副作用。
- Completion CAS + effects 原子提交：通过；Batch terminal CAS、关联校验、Target/Retry/Instance effects 同一 GORM transaction，故障注入与 duplicate completion 测试通过。
- 多目标部分失败 E2E：通过；A 成功/B 失败只创建 B 的 write_target retry，B 重试成功后 A 未重复写。
- TaskSeries 多 Tag 并集 / series_index / series_hash：通过；重叠 series 去重、跨 Provider 同 Subject 独立、canonical index/hash 稳定，成员变化产生新 hash。
- SCF 子集 items 与多 Dataset fanout index 映射：通过；共享 BTC Provider request 只有一个 Instance，不同 Dataset 使用各自 series_index/series_hash/expected_count。
- EnsureDatasetPeriod / DatasetPeriodExpectation：通过；幂等初始化及冲突 hash/count 拒绝测试通过。
- CommitTimeSeriesBatch rows + bitmap Merge 原子提交：通过；Storage DataNode 同一 Pebble Batch 写 rows/history 与 bitmap Merge。
- Pebble bitmap Merger 幂等/并发/reopen：通过；重复 commit、32 路并发 bit、compact/reopen 测试通过。
- Dataset complete/degraded finalizer 与 outbox 唯一通知：通过；最后一批只生成一次 complete，crash-window reopen 可恢复，deadline 产生 degraded。
- 跨 Provider 同 subject series 隔离：通过；规范 series_key 包含 Provider/Source/Market/Subject/series_tag。
- Collector Dataset 删除边界与服务鉴权：通过；仅 app_id/错误 HMAC/其它服务身份拒绝，signed `moox-collector` 允许；共享 Dataset WriteTarget 引用时删除前拒绝。
- CollectorRun planning cutoff + series_hash 与下一周期生效：通过；同分钟 series 变化不追加旧 Run，下一 Run 使用新集合；同时修复 SQLite mtime trigger 秒级截断问题。
- Source capability 策略：固定为 fail-open + 已声明时强校验 + planning 再校验；无/有/错误 capability 单测通过；正式环境注册结果待发布时验证。
- 前端类型检查：通过 `pnpm exec vue-tsc --noEmit`。
- 前端测试：通过，72 test files / 278 tests。
- 前端生产构建：通过，`WEB_BUILD_RC=0`；仅现有 Browserslist/Sass/chunk-size/lottie eval warning。
- Go 全量测试：Collector `go test ./... -count=1` 全包通过；Storage `go test ./... -count=1` 全包通过。
- 空库 Schema 初始化：通过；包含 TaskSeries/Run/Instance/WriteTarget/Batch/Retry，明确不存在 RunTaskSnapshot/RunSeriesSnapshot；空 Pebble period tests 通过。
- 正式环境发布：未执行。
- 发布时间：待发布。
- 发布版本：待发布。
- 备注：`collector-subject` 上线后仍需确认内建 Tag source 的 `subject_listing` 已登记且无计划外 fail-open warning。
