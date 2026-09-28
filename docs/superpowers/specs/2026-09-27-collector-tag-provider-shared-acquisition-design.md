# 采集任务标签来源绑定与跨任务共享采集设计

## 背景

当前标签的 `Tag.sources` 是多值配置，主要服务于自动同步或探测标签成员；采集任务另外保存 Provider。调度器按任务分别解析标签成员，并把每个采集项直接绑定到一个结果 Dataset。因此，当多个任务的标签范围有交集时，同一 Provider、标的、频率和目标时间可能被重复请求。

现有结果身份也由标签、任务类型和频率参与生成。随着一个任务可以绑定多个标签、一个标签固定绑定一个 Provider，以及一次采集需要写入多个任务 Dataset，结果身份和采集执行身份需要分开建模。

## 目标

1. 一个标签只绑定一个固定数据源；标签成员列表与成员详情使用该来源，不再维护两套来源配置。
2. 采集任务只绑定一个或多个标签，允许跨 Provider；每个标签固定绑定一个来源，Provider 从标签来源推导，不能在任务表单中覆盖。
3. 一个采集任务对应一个结果 View；一个任务可以包含多个标签。
4. 采集任务定义与 Provider 无关；任务可包含多个 Provider 的标签，不按 Provider 拆分用户任务。
5. 调度器跨任务合并完全相同的采集请求，使同一个标的尽量只请求 Provider 一次，再将结果写入所有目标 Dataset。
6. 每个目标任务保留自己的 Dataset、View、输出字段配置和写入状态。
7. 重试时允许重新请求 Provider；同一轮调度中的相同采集项只执行一次。

## 非目标

- 不保证 Provider 请求在崩溃、超时或写入重试后全局 exactly-once；采集结果不做跨重试持久化缓存。
- 不合并不同 Provider、不同标的、不同采集方法、不同频率或不同目标时间的请求。
- 不把多个任务的输出 Dataset 或 View 合并成共享存储对象。
- 不允许任务修改标签所绑定的 Provider。

## 核心概念

- **DataSource / Provider**：实际提供成员详情和采集数据的来源。标签绑定唯一 DataSource；DataSource 标识映射到 Collector 已注册的 Provider。
- **Tag**：一个特定市场类型/数据对象下的 Subject 集合，并固定绑定一个 DataSource。任务通过 Tag 确定采集范围，以及该范围对应的市场类型和 Provider。
- **CollectionTask（采集任务）**：用户定义、可独立启停的采集方案，配置采集方法、必需参数、采集范围（标签）及结果输出。它可以包含多个标签，也可以跨多个 Provider；任务本身不绑定 Provider。每个采集任务拥有自己的结果 Dataset 和唯一 View。
- **CollectorRun（采集轮次）**：一次逻辑调度/执行上下文，对应 `t_collector_runs`。定时调度、手工重放、补采或修复都先创建一个 Run；同一逻辑定时轮次由数据库唯一键仲裁并复用同一个 `run_id`。Run 由 Collector Scheduler/Planner 创建，由 Dispatcher 标记进入活动态，由 Reconciler 根据子实例和目标状态汇总为完成或降级；SCF/Provider Worker/Storage Writer 不直接更新 Run。
- **TaskInstance（采集实例）**：某个 CollectorRun 内的原子 Provider 请求。同一 Run 中语义相同的请求全局去重后只保留一个实例；该实例可以写入多个任务的目标 Dataset。
- **WriteTarget**：一次采集结果需要落入的目标任务 Dataset。一个 TaskInstance 可关联多个 WriteTarget。
- **TaskSeries**：CollectionTask 当前全部 Tag 成员按规范 series identity 求并集后的持久化集合。集合 canonical sort 后分配稠密 `series_index`，并由有序 `series_key` 计算 `series_hash`；它是 Dataset period expected set 与 Scheduler planning 的共同成员来源，不为每个 Run 重复复制。

## 数据模型

### Tag

将 `Tag.sources`（repeated）收敛为 `Tag.source`（single），并使用单数 `source` / `source_id` 语义贯穿 API、前端、存储层和同步器。Storage 表中使用 `c_source_id` 单值列。Tag ID 按当前系统全局唯一；Tag 市场字段统一使用 `market_type`，不再额外保留含义重叠的 `instrument_type`。

约束：

- 新建或更新标签必须提供一个有效来源；不允许空值或多个来源。
- 自动标签的成员同步、手工标签的有效性探测、成员详情查询都使用该来源。
- 标签来源创建后不可由采集任务覆盖，且 `source_id` 创建后不可修改；如需更换来源必须创建新 Tag，并由使用方显式切换到新 Tag。`market_type` 同样属于 Tag 的路由身份，变更时创建新 Tag，避免既有任务的 Provider/市场语义静默漂移。
- 任务创建时，任一所选标签缺少来源、来源已停用或来源未映射到可用 Collector Provider，整体校验失败并列出标签名称和缺失原因。

`Tag.source` 表示单一权威来源，不再表达多个列表源合并。

### Tag 市场类型与数据对象

- `market_type` 属于 Tag，因为 Tag 的成员集合是在特定市场/数据对象语义下定义的；不同市场可能对应不同的标的体系、ID 规则和 Provider symbol 映射。当前 `instrument_type` 字段若表达相同维度，应重命名为 `market_type`，不再双存。
- Tag 的市场类型应在创建时确定，并与该 Tag 的成员来源、数据对象类型校验兼容。采集任务绑定多个 Tag 时允许跨市场、跨 Provider。
- 生成 TaskInstance 时，以 Tag 的市场类型和来源为依据确定实际路由；CollectionTask 的采集方法及参数必须支持对应的 Tag。
- 同一 TaskInstance 只服务一个市场类型；市场类型属于请求键。任务列表可从关联 Tag 汇总展示市场类型，不能把单一任务级 market_type 当作所有标签的权威值。

### 表结构关系

- `t_tags` 使用单值 `c_source_id` 和唯一 `c_market_type`；`c_tag_id` 全局唯一。Tag 与 DataSource 在同 Space 建复合外键，防止来源被删除或引用到其他 Space。
- `t_collector_tasks` 不保存 Provider/market；用 `t_collector_task_tags` 关系表保存一个任务的多个 Tag。
- `t_collector_task_series` 保存任务当前 Tag 并集的规范 series 集合，包含 `series_key/series_index/subject/provider/source/market/provider_symbol/series_tag`；任务行保存当前 `series_hash`。集合不变时不重写 hash/index，集合变化时原子替换 TaskSeries 并更新 task mtime/hash。
- `t_collector_runs` 保存一次逻辑采集轮次。定时轮次以 `(space_id, run_type, frequency, target_time)` 或等价的规范逻辑键唯一，多个 Scheduler 并发触发同一轮时必须取得同一个 `run_id`；manual replay/backfill/repair 创建新的 Run。
- `t_collector_task_instances` 保存某个 Run 内去重后的单 Provider 请求，不直接拥有单一 task 或 Dataset；实例通过 `run_id` 归属执行轮次。`t_collector_instance_write_targets` 保存其各任务写入目标和目标级状态。
- Batch 与 Instance 通过 batch-item 表关联；RetryItem 通过 instance 关联 fetch retry，或通过 write_target 关联目标写入 retry。
- Dataset period 完成不由 Batch/Instance/WriteTarget 状态推导。Collector 将 TaskSeries 的 `series_hash + expected_count` 初始化到目标 Dataset 所在 DataNode，实际 Storage 写入以 `series_index` 推进该 Dataset/period 的 bitmap barrier。
- Collector 结果 Dataset 不应被标记为单一 Provider/DataSource 的数据集；Storage Dataset 来源列需要支持 Collector-owned 输出的无单一来源语义。

### CollectionTask

- 任务不绑定单一 Provider，允许绑定不同 Provider 的多个标签；Provider 由每个标签的固定来源推导。
- 保存 `tag_ids` 作为任务采集范围的权威配置；结果 Dataset 的 `subject_tags` 是供查询和存储读取的镜像，不作为任务配置的唯一来源。
- 不在任务级保存唯一 `provider`。生成实例时，根据标签来源、市场类型及采集方法解析对应 Provider。
- 同一任务可以覆盖多个 Provider；调度器按可执行请求原子化生成实例，并可跨任务合并语义相同的请求。
- 任务的采集方法、频率、输出字段及其他方法参数保持任务级配置。
- 标签范围及其 Provider 对已创建任务视为身份配置；需要变更时创建新任务，避免任务身份、View 和调度实例漂移。

### CollectionTask ID 与结果 View

- `task_id` 由服务端使用 Go `xid` 库生成；XID 文本格式为 20 位小写字母和数字。
- 在同一 Space 内检查任务 ID / 结果资源冲突；冲突时重新生成，达到有界重试次数仍冲突则返回清晰错误。
- Storage / Collector 数据库必须保留唯一约束作为并发保护。生成 ID 与任务记录创建的最终唯一性由数据库约束兜底。
- 一个任务只创建一个结果 View，格式为 `view_{taskID}_{taskType}_{frequency}`，例如 `view_c9o1r5k8m2n4p6q7s0tu_kline_1m`。
- View ID 不再包含 Tag ID。Dataset ID 继续任务级隔离；不同任务即使标签、Provider、方法和频率相同，也拥有各自 Dataset / View。
- 因 View ID 改为随机任务身份，同一标签集合、任务类型和频率的重复任务检测应独立于 View ID。完全相同的任务定义应拒绝重复创建；存在部分标签重叠的不同任务允许创建，以支持下文的共享采集。

### TaskInstance、任务关联与 WriteTarget

将现有 `TaskInstance` 定义为可被多个采集任务复用的原子采集实例。实例记录 Provider 请求本身，目标写入关系独立保存：

- 一个 `CollectionTask` 可关联多个 `TaskInstance`；一个 `TaskInstance` 可被多个 `CollectionTask` 复用。这个多对多关系由 `WriteTarget.task_id` 表达，避免再维护一张重复的任务关联表；若运行排查需要记录实例由哪些 Tag 展开，可另存来源 Tag 关联。
- `TaskInstance` 是一个原子 Provider 请求，只绑定一个 Provider、市场类型、数据对象/标的、方法、频率及请求参数。同一 CollectorRun 内相同采集键只保留一个实例，不按来源任务复制实例；不同 Run 可再次产生相同 `request_key` 的新实例。
- 一个 `TaskInstance` 可以有多个 `WriteTarget`。每条目标记录包含所属 `task_id`、`dataset_id`、`view_id`、字段投影配置，以及独立的写入状态、错误和重试信息。`WriteTarget` 同时建立实例与目标任务的关联，不应只依赖 `TaskInstance.task_id` 或单个 `dataset_id` 字段表达。
- Provider 请求状态属于 `TaskInstance`；写入状态属于各 `WriteTarget`。例如 Dataset A 成功、Dataset B 失败时，实例 fetch 保持成功，A 保持成功，只有 B 可重试。
- 结果行以每个目标 Dataset ID 和规范化 series identity 构造独立 RowKey；series identity 至少区分会导致同一 Dataset 内并存的 Provider/Source/Market/Subject/Frequency 维度，避免同一任务跨 Provider 时相同 Subject+时间互相覆盖。写入多个 Dataset 时分别发起 Storage upsert，目标间互不覆盖。
- `BatchInvocation` 负责批量 Provider 调用时，应能引用多个 TaskInstance；`RetryItem` 应明确区分请求重试与目标写入重试，目标重试不能误将已成功目标重复标记为失败。

实现阶段检查现有 `TaskInstance`、`BatchInvocation` 和 `RetryItem` 的表结构与调用链，确定是扩展实例表并新增关联表/目标表，还是复用已有结构。逻辑上必须能表达：一个任务有多个实例、一个实例通过多个 WriteTarget 服务多个任务、一个实例写入多个目标，以及每个目标独立状态。

## 创建任务流程

1. 用户选择一个或多个标签、执行任务类型、方法参数、频率、输出字段及范围内其他配置；多个标签可以属于不同 Provider。
2. 服务端加载所有标签及其来源、市场类型和数据对象，校验标签有效、每个标签恰有一个可用来源、成员范围非空，并校验每个标签与采集方法兼容。
3. 创建一个 CollectionTask，不按 Provider 分裂；分配唯一 task_id、独立结果 Dataset 和一个 View。
4. 任务生成或调度时，按标签及其成员展开原子 TaskInstance。每个实例只对应一个 Provider 请求语义；不同标签的 Provider 可以不同。
5. 在实际执行前，跨所有启用任务对相同请求语义进行去重，并把其对应的目标 Dataset/View 关联到写入目标。
6. 任务创建为整体操作；来源、市场、方法参数、范围、View 身份或任务重复性校验失败时，不创建不完整任务。

## 全局采集分组与去重

Scheduler 在创建当前 scheduled Run 前刷新启用任务的 `t_collector_task_series`；随后以该 Run 不可变 `c_ctime` 作为 planning cutoff。当前 Run 只接纳 `create_time/modify_time <= cutoff` 且 `definition_hash/series_hash` 与 planning 初始读取一致的任务。cutoff 后新建、重新启用、定义变化或 Tag 成员变化的任务统一从下一 Run 生效。

Run 创建后 planning **只读取持久化 TaskSeries**，不再重新展开 Tag，也不向同一 Run 追加新成员。若 planning 期间检测到 task 的 definition/series hash 已变化，则当前 Run 视为 stale/failed，由下一轮重新规划；已经进入 planned/dispatched Batch 的旧 hash 请求按其持久化 Instance/WriteTarget/period expectation 收尾。这样避免为每分钟 Run 复制几千条成员，同时保证一个 Run 不混用新旧对象集合。

基于固定的 TaskSeries，为每个可执行原子项生成候选 TaskInstance 与目标写入关系，再按采集语义全局去重；每个唯一 TaskInstance 关联一个或多个 WriteTarget。TaskInstance 的数据库唯一性按 `(space_id, run_id, request_key)` 保证，仅约束同一逻辑轮次，不把历史轮次永久锁死。

**采集请求键**至少包含：

- Space；
- 固定 Provider / DataSource；
- 采集方法和数据类型；
- 市场、产品类型；
- Subject ID 与 Provider Symbol；
- Frequency；
- 目标数据时间，或完整时间范围；
- 会改变 Provider 返回数据的参数，例如历史策略、bar limit、对齐和请求模式。

以下属性不属于采集请求键：

- `run_id`；`run_id` 是执行上下文，与 `request_key` 共同构成数据库唯一键，但不属于 Provider 请求语义本身；
- CollectionTask ID / TaskInstance ID；
- Dataset ID / View ID；
- Tag ID（标签仅用于展开范围；若标签会改变 Provider symbol 映射或请求语义，则需把对应身份纳入键）；
- 输出字段集合（采集结果先保留标准化字段，再逐目标投影）。

因此，任务 1 包含 tag1、tag2，任务 2 包含 tag2、tag3 时，若 tag2 展开的请求语义相同，则只生成并执行一个 TaskInstance，并将结果扇出到两个任务的目标 Dataset。tag1、tag3 的独有请求单独执行。

若两个任务对同一 Provider / 市场类型 / Subject 使用不同频率、时间点、历史范围或其他影响响应的参数，则生成不同 TaskInstance。若 Provider 不同，即使 Subject 相同也不合并。

## 执行、扇出与重试

1. 每个唯一 TaskInstance 调用 Provider 一次，归一化为 Collector 内部标准结果，并将结果扇出到全部关联 WriteTarget。
2. 为每个 WriteTarget 单独套用输出字段投影，并按目标 Dataset ID + 规范化 series identity + 数据时间生成稳定 RowKey 后写入。
3. 每个目标的 upsert 使用稳定行键，重复写入幂等。
4. 若 Provider 请求失败，在同一个 Run、同一个 TaskInstance 上增加 attempt 并按原请求语义重试，不新建另一个语义重复实例；若部分目标写入失败，只将失败目标标记为待重试。目标重试允许重新请求 Provider，但结果仅扇出到待重试目标。
5. 不持久化 Provider 原始响应用于跨重试复用。由此，同一正常调度轮次的成功路径去重；网络错误、进程退出或 Storage 写入失败后可能发生重复 Provider 请求。手工重放、补采或修复通过创建新的 CollectorRun 表达，可再次生成相同 `request_key` 的新 TaskInstance。
6. TaskInstance 的可观测状态同时展示请求结果和目标扇出摘要，例如 `fetch=success, targets=2/3`；目标失败需能定位到 Task、Dataset 和错误。


## Dataset Period 完成屏障

- 一个 CollectionTask 对应一个结果 Dataset；该 Dataset 的 expected set 是任务全部 Tag 展开后的 **规范 series 并集**，由 `t_collector_task_series` 表达。重叠 Tag 产生的同一规范 series 只计一次；同一 Subject 在不同 Provider/Source/Market 下仍是不同 series。
- Scheduler 在 dispatch 前调用 `EnsureDatasetPeriod`，向目标 Dataset 所在 DataNode 写入 `DatasetPeriodExpectation(space,dataset,frequency,period,series_hash,expected_count,deadline)`。相同 expectation 重试幂等；同一 Dataset/frequency/period 出现不同 hash/count 必须拒绝。
- WriteTarget 保存该 Dataset 自己的 `series_index/series_hash/expected_count`。共享 Provider fetch 扇出到多个 Dataset 时，同一 BTC 可以在 Dataset A/B 使用不同 index/hash；这些字段不进入 Provider `request_key`。
- SCF 只携带本批负责的 item，不携带 Dataset 全量成员。成功写入通过 `CommitTimeSeriesBatch` 携带 `series_index`，DataNode 在同一个 Pebble Batch 中提交 K 线 KV/history 与 bitmap OR Merge。bitmap 是 period 进度的唯一真相，不持久化 SuccessCount，也不扫描全部 K 线。
- rows+bitmap 提交后，DataNode 读取合并 bitmap 并 popcount；全部 bit 就绪时幂等 finalize 为 `complete` 并写 completion outbox。deadline 到达但仍缺数据时 finalize 为 `degraded`。后台 finalizer 负责 crash 后补齐“bitmap 已满但 outbox 未生成”的窗口。
- `DatasetRowsUpserted` 可继续服务 freshness/其它异步功能，但 **不再参与 market-fetch Dataset complete 判定**；BatchCompleted 也只能更新执行状态，不能直接产生 Dataset complete。

## 输出字段

- 字段目录继续展示当前空间的全部启用字段；可按需求配置每个目标任务的输出字段。
- 共享采集请求不按某个任务的字段选择裁剪 Provider 返回内容；每个 WriteTarget 独立投影，避免目标 A 的字段选择影响目标 B。
- 某目标选择了当前采集方法无法产出的字段时，服务端必须返回明确字段错误，不得静默丢弃选择或报告完整成功。
- 空输出字段集合表示使用该采集方法的默认输出字段。
- 当前各采集方法对任意字段值的映射能力需要按方法契约实现；字段目录“可见/可选”与采集方法“可产出”是两个不同能力，不得将未映射字段伪装成已写入结果。

## 新项目数据策略

- 不提供旧 `Tag.sources` 到 `Tag.source` 的兼容读取、双写窗口或历史标签迁移工具。
- 不提供旧 CollectionTask、TaskInstance、BatchInvocation、RetryItem 或结果资源的兼容层。
- 新模型落地时，开发和验证环境的旧标签、任务、实例及结果数据可以清空并按新结构重新创建；部署步骤明确数据库重置/初始化的执行边界。
- 发布构建必须确保全新初始化能创建完整 Schema、种子数据和必需的索引/唯一约束。

## 错误处理与一致性

- 标签来源缺失、停用或 Provider 映射失败：拒绝创建任务，并指出标签和来源。
- 任一必选标签的成员范围解析为空：拒绝创建任务，并明确指出标签。
- 共享请求失败：TaskInstance 及其所有关联 WriteTarget 记录 fetch 失败；不得只更新其中某个任务状态。
- 单目标写入失败：保留其它成功目标状态，只重试失败目标。
- 某任务被禁用或删除：在下一轮规划中移除它的 WriteTarget，不影响其它任务对同一 TaskInstance 的目标引用。
- 同一 CollectorRun 内相同稳定采集键只能有一个 TaskInstance；由 `UNIQUE(space_id, run_id, request_key)` 仲裁并发规划。不同 Run 可以拥有相同 `request_key`，用于下一轮调度、manual replay、backfill 或 repair。
- TaskInstance 保存 Provider Symbol、目标数据时间和规范化请求参数快照；`request_key` 必须在 bar limit、时间范围、snapshot 分片等影响响应的参数全部确定后计算，不能先算 identity 再修改请求。
- CollectorRun 由 Scheduler 写入并由 Collector 进程持续收敛状态：有待执行实例、活动 batch 或 retry 时为 active；所有实例/目标终态后汇总 succeeded/partial_failed/failed。Timer-owned stockcn 没有 BatchCompleted 时允许以实例 freshness 收敛。
- 禁用任务的 WriteTarget 在没有 planned/dispatched batch 后安全 detach；目标级 retry 同步清理，不删除仍被其它任务引用的共享 TaskInstance。
- 采集执行记录与目标映射必须保留足够时间，覆盖批次回调、目标重试和任务实例详情查询。
- `series_tag` 是实例与 Storage 行的 series identity 投影；TaskSeries 的规范 `series_key` 同时包含 Provider/Source/Market/Subject/series_tag。同一 Dataset 中相同 Subject 通过不同 Provider/Source/Market 进入时必须占用不同 series_index，禁止一条 Binance 写入满足 OKX 的 Dataset period bit。

## 执行节点模型

- crypto 实时 Kline 由 Collector Scheduler 统一规划并显式调用 SCF Invoke pool。Invoke market-fetcher 必须携带 EventBus/NATS 凭据，并在写入完成后发布 `MarketFetchBatchCompleted`；不得复用为了 4KB 环境预算而省略 EventBus 凭据的旧 Timer runtime。
- stockcn 当前继续使用 Tencent Timer fleet 承担实时 Kline；`InvokeNonRealtimeOnly` 只表示这种 Timer-owned Space。两种执行模型共享同一 TaskInstance/WriteTarget/Run 语义，不改变任务与结果模型。
- crypto 发布新 Invoke pool 前必须关闭该 Space 遗留的 Timer Trigger，防止发布窗口内 Scheduler Invoke 与 Tencent Timer 双采集。配置中的历史字段 `timer_function_count` 对 crypto 仅表示函数池容量，实际发布为 Invoke functions；stockcn 仍表示 Timer 数量。

## 部署与重建策略

- 本次按新项目模型直接调整 Proto、SQLite Schema、API 和前端，不保留旧字段兼容。
- 正式环境上线前确认采集历史数据和结果可按业务决策清空重建；发布流程需执行一次明确的数据目录/表重置和全新初始化，并校验基础标签、Provider、采集方法和字段配置。
- 应用启动后校验 Schema 版本和关键唯一约束；不得在运行中静默沿用旧表结构。

## 验收标准

- Tag API 与页面只能读写单个 source；缺失 source 的标签不能创建采集任务。
- 一个采集任务可以包含跨市场、跨 Provider 的多个标签；每个标签保留自己的市场类型和来源，实例按标签路由，不拆分用户任务。
- 新生成 task_id 使用 `xid` 格式（20 位小写字母和数字）；并发冲突时不会生成重复 task_id。系统内置任务种子也使用固定合法 XID，`task_name` 承担可读名称职责。
- 每个任务恰有一个 `view_{taskID}_{taskType}_{frequency}` View，Dataset 和 View 不因标签重叠而共用。
- 同一 CollectorRun 中，跨任务相同采集键只生成一个 TaskInstance，并满足 `(space_id, run_id, request_key)` 唯一；不同 Run 可以重新执行相同请求。不同 Provider、频率、市场类型或目标时间分别生成 TaskInstance。
- 一个 TaskInstance 能写入多个 Dataset，目标行的 Dataset ID、输出字段、View 关系各自正确。
- 某一个目标写入失败时，其它目标成功状态不回滚；重试仅处理失败目标，可再次抓取 Provider。
- 任务 ID 冲突、目标 Dataset 写入失败、Provider 请求失败、部分扇出失败和重复调度均能恢复且状态可观测。
- 全新数据库初始化得到的 Tag、CollectionTask、TaskSeries、CollectorRun、TaskInstance、WriteTarget 结构完整，无旧字段兼容依赖，且不存在 per-Run `RunTaskSnapshot/RunSeriesSnapshot` 成员表。
- CollectionTask/Tag 成员变化只从下一 CollectorRun 生效；同一 Run 不混用不同 `definition_hash/series_hash`。
- Dataset period complete 仅在 Storage DataNode bitmap 覆盖全部 `expected_count` series 后产生；重复/并发 commit、重启恢复和 deadline degraded 均保持幂等。
