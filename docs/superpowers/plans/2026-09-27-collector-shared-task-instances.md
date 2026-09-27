# 采集任务与共享采集实例实施计划

> **实施要求：** 按阶段逐项执行并勾选。每个阶段先补充失败测试，再实现最小改动并运行该阶段验证。该计划覆盖 Storage、Collector 调度/持久化、前端和正式发布准备。

**目标：** 调整标签、采集任务与任务实例的数据模型，使单个采集任务可以跨多个 Provider；相同采集请求全局只执行一次，并将结果写入多个任务各自的 Dataset/View。

**架构：** Tag 固定一个数据源和市场类型。CollectionTask 只保存用户定义的采集方案及所选 Tag，不绑定 Provider。调度器展开所有启用任务，按请求语义生成全局唯一的 TaskInstance；每个 TaskInstance 关联多个 WriteTarget，分别保存目标任务、Dataset、View、字段投影和写入状态。

**技术栈：** Go、tRPC Proto、GORM、SQLite、Storage Metadata RPC、Vue 3、TypeScript、Vitest。

---

## 当前实施状态（2026-09-27）

- 阶段一至阶段九的编码实现已完成并通过对应单测/构建验证。
- `CollectorRun` 已由 Scheduler 创建；RunRepository 在每轮调度前收敛旧 Run、当前 Tick 结束时汇总当前 Run，状态覆盖 `planned / active / succeeded / partial_failed / failed`。manual replay/backfill/repair 可通过 `RunRepository.Create` 显式创建新 Run，同一 `request_key` 可在不同 Run 再次执行。
- TaskInstance 已持久化 `provider_symbol`、`target_data_time`、`series_tag` 和规范化请求参数快照；`request_key` 在 `bar_limit` 等响应影响参数确定后计算。
- 禁用任务的 WriteTarget 在无 planned/dispatched batch 后由 Scheduler 安全 detach；对应 target retry 一并清理，共享实例及其它启用目标保留。
- 已验证：Collector `go test -count=1 ./...`、Storage metadata/catalog 测试、相关前端 Vitest、`vue-tsc --noEmit`、Web `build:dev`。
- 阶段十仍保留正式环境 reset、发布和线上冒烟未完成。全量 `pnpm test` 当前还有一个与本次改造无关、仓库 HEAD 已存在的 `tests/storage-view-browse.spec.ts` 源码字符串契约失败，因此未勾选全量 Web test/build:prod 项。

## 已确认的产品规则

- 这是新项目，不做旧字段兼容、双写或历史数据迁移；开发/验证数据可删除后按新 Schema 重建。
- 新建 Tag 必须绑定一个数据源；Tag 同时拥有市场类型。不能让采集任务覆盖 Tag 的数据源或市场类型。`source_id` 与 `market_type` 均属于 Tag 路由身份，创建后不可修改；需要变更时创建新 Tag。
- Tag ID 按用户要求检查当前系统是否已存在；计划按系统全局唯一处理，而不是仅在单个 Space 内唯一。若内置数据需要多 Space 共用相同 ID，应改为全局共享 Tag 实体，不得悄悄把唯一性降为 Space 内唯一。
- 一个 CollectionTask 可以绑定多个 Tag，允许跨市场、跨 Provider；创建时始终只生成一个 CollectionTask。
- CollectionTask 使用 `xid` 生成 task_id；一个采集任务对应一个 Dataset 和一个 View，View ID 为 `view_{taskID}_{taskType}_{frequency}`。
- 一个 TaskInstance 是单个 Provider 的原子请求；相同请求在全局计划中只保留一个实例。实例可由多个 CollectionTask 共享，并可写入多个 WriteTarget。
- 同一 Dataset 内跨 Provider 的相同 Subject 必须由规范 `series_tag` 隔离；Storage RowKey、TaskInstance freshness 与 period readiness 使用同一 series identity，不能只按 Subject 判定完成。
- crypto 实时 Kline 使用 Scheduler-owned SCF Invoke pool 并通过 EventBus 返回批次完成事件；stockcn 暂时保留 Timer-owned 实时 Kline。crypto 发布前关闭遗留 Timer Trigger，禁止双采集。
- Provider 请求状态属于 TaskInstance；写入状态、错误和重试属于各 WriteTarget。一个目标失败不得回滚其他目标的成功状态。
- 输出字段选择器展示全局字段目录，不限制字段数量或类别；不因任务类型而隐藏/禁选字段。方法确实不能输出所选字段时，服务端需返回明确校验错误。
- 采集范围对需要标的的任务必填；没有标的概念的方法可为空。
- 结果存储配置是可选高级项；默认使用系统配置。新建任务不展示“启用状态”，创建后默认启用。

## 当前表结构核查结论

### Storage Metadata

- `t_tags.c_sources_json` 是多数据源 JSON，需替换为单值 `c_source_id`。`c_instrument_type` 当前已容纳 `spot/swap/equity/etf/index/...`，语义与讨论的市场类型重合；新模型应将其统一命名为 `c_market_type`，不要再并存一列含义重复的 `market_type` 与 `instrument_type`。
- `t_tags` 当前只有 `UNIQUE(c_space_id, c_tag_id)`。按“当前系统是否已存在这个 Tag”要求，需要增加全局 `UNIQUE(c_tag_id)`；保留 `UNIQUE(c_space_id, c_tag_id)` 以支持成员表的复合外键，并保留 `UNIQUE(c_space_id, c_tag_name)`。
- `t_tags.c_source_id` 应引用 `t_data_sources(c_space_id, c_data_source_id)` 并在删除来源时 `RESTRICT`；同时校验来源处于可用状态且与 Tag 市场类型兼容。
- `t_datasets.c_data_source_id` 当前为必填并引用单一来源。但跨 Provider 任务的结果 Dataset 不能正确声称只来自一个 Provider。Collector 结果 Dataset 应允许没有单一 `data_source_id`；普通来源型 Dataset 仍要求有效来源。来源权威关系由 Tag 保存，实例级 Provider 由 TaskInstance 保存。不要将多个来源拼成一个虚构 source ID。

### Collector 任务、实例及调度表

- `t_collector_tasks.c_provider` 与 `c_market_type` 是单 Provider/单市场任务假设，应从权威模型移除。Tag 选择不建议塞进不可查询的 JSON：新增 `t_collector_task_tags` 关系表，主键 `(c_space_id, c_task_id, c_tag_id)`，索引 `(c_space_id, c_tag_id)`；它引用本地 `t_collector_tasks`，不对另一个 Storage 数据库建跨库外键。Tag 存在性在创建/更新任务时通过 Storage RPC 校验。
- 新增 `t_collector_runs` 表达一次逻辑采集轮次。Scheduler/Planner 创建或取得 Run；Dispatcher 可将其推进到活动态；Reconciler 根据 TaskInstance/WriteTarget 汇总完成或降级状态。SCF、Provider Worker 和 Storage Writer 不直接更新 Run。定时轮次用规范逻辑键（例如 `space_id + run_type + frequency + target_time`）唯一仲裁，确保多个 Scheduler 并发触发同一轮时获得同一个 `run_id`；manual replay/backfill/repair 创建新的 Run。
- `t_collector_task_instances.c_task_id`、`c_dataset_id` 使实例只能属于一个任务和一个 Dataset，应移除。实例新增 `c_run_id` 与规范化后的 `c_request_key`，并对 `(c_space_id, c_run_id, c_request_key)` 建唯一约束；不要对 `(space_id,request_key)` 做永久全局唯一。Provider、source、market、method、subject/symbol、frequency、目标时间/范围和影响 Provider 响应的参数仍保存在实例上；`run_id` 不进入 `request_key` 本身。
- 新增 `t_collector_instance_write_targets`，按目标保存 `instance_id`、`task_id`、`dataset_id`、`view_id`、`output_fields_json`、目标状态、attempt、next_retry_at、错误和时间戳。唯一约束建议 `(c_space_id, c_instance_id, c_task_id)`：一个任务只有一个结果 Dataset/View，因此同一实例在一个任务下只需一个目标。该表是实例与任务之间的关联，不再新建重复的 task-instance join 表。
- `t_collector_fetch_batches.c_task_id`、`c_dataset_id` 当前把批次绑定到单任务/单 Dataset。批次可包含多个实例时，应移除这两个所有权字段；新增 `t_collector_fetch_batch_items` 将 batch 与 instance 关联，按 `(space_id, batch_id, instance_id)` 唯一，并保存批次项状态/错误。Batch 仍可按执行环境、函数、Provider/source、市场或频率分片，但分片维度必须和实际 SCF/Provider 调用限制一致。
- `t_collector_fetch_retry_items.c_task_id`、`c_dataset_id` 不足以表达共享实例重试。增加 `instance_id` 与重试范围 `fetch/write_target`；目标写入重试再带 `write_target_id`。Fetch 失败重试实例请求，目标失败重试仅作用于指定 WriteTarget。
- `t_period_readiness` 是 Dataset 维度；`t_period_readiness_items` 当前以 `(readiness_id, instance_id)` 唯一。共享实例要写多个 Dataset，因此 item 应改为引用 `write_target_id`，并按 `(readiness_id, write_target_id)` 唯一，避免一个 Dataset 的完成度被另一个 Dataset 的目标覆盖。
- 删除 `t_collector_result_view_reservations` 及其 Check/Reserve/Release 逻辑。结果 View 使用 `view_{taskID}_{taskType}_{frequency}`，正常任务之间不共享 View identity；并发重复任务由 `(space_id,c_definition_hash)` 唯一约束仲裁，Storage/Collector 对 task/view/dataset 的唯一索引仅作为资源 ID 冲突兜底。
- 全局唯一请求键必须排除 task/dataset/view/tag/output_fields；来源 Provider、market、method、subject/symbol、frequency、采集时间范围及所有改变 Provider 返回值的参数都必须纳入。

## 代码范围索引

### Tag / Storage

- Proto：`modules/storage/proto/metadata.proto`；生成代码：`modules/storage/proto/storagegen/metadata.pb.go`。
- 表定义：`modules/storage/schema/metadata.sql`、`modules/storage/internal/service/metadata/sqlite/store.go`。
- 服务及 DAO：`modules/storage/internal/service/metadata/tag.go`、`modules/storage/internal/service/metadata/sqlite/crud_tag.go`、`modules/storage/internal/service/catalog/tag_catalog.go`、`modules/storage/internal/service/catalog/validate.go`。
- Dataset 来源校验及创建：更新 Storage Dataset CRUD/catalog 代码中对 `data_source_id` 的必填校验，确认普通 Dataset 与 Collector 结果 Dataset 的规则边界。
- 前端：`web/src/api/storage/types.ts`、`web/src/api/storage/metadata.ts`、`web/src/views/data/subjects/tag-form.ts`、`web/src/views/data/subjects/tags-tab.vue`。
- 测试：`modules/storage/internal/service/metadata/sqlite/crud_tag_test.go`、Storage catalog 验证测试、`web/src/views/data/subjects/tag-form.test.ts`、`web/src/api/storage/tags.test.ts`。

### CollectionTask / 结果资源

- Proto：`modules/collector/proto/collector.proto`；生成目录：`modules/collector/proto/collectorgen/`。
- 模型与服务：`modules/collector/internal/domain/task.go`、`modules/collector/internal/store/task.go`、`modules/collector/internal/rpc/service.go`。
- 结果资源：`modules/collector/internal/planner/taskresult/result.go`。
- Schema：`modules/collector/schema/collector.sql`、`modules/collector/internal/store/database.go`、`modules/collector/internal/bootstrap/bootstrap.go`。
- 前端：`web/src/api/collector/index.ts`、`web/src/views/collector/collection-tasks/collection-task-params.ts`、`web/src/views/collector/collection-tasks/collection-tasks.vue`。
- 测试：`modules/collector/internal/domain/task_test.go`、`modules/collector/internal/store/task_test.go`、`modules/collector/internal/rpc/service_validation_test.go`、`modules/collector/internal/planner/taskresult/result_test.go`、`web/src/views/collector/collection-tasks/collection-task-params.test.ts`、`collection-tasks.test.ts`。

### TaskInstance / 批次 / 扇出

- 模型：`modules/collector/internal/domain/task_instance.go`、`modules/collector/internal/domain/fetch_batch.go`。
- Repository：`modules/collector/internal/store/task_instance.go`、`fetch_batch.go`、`fetch_retry.go`、`database.go`。
- 规划执行：`modules/collector/internal/marketfetch/scheduler.go`、`reconciler.go`、`assignment.go`、`environment.go`、`kline_pipeline.go`。
- 实例 API/页面：`modules/collector/proto/collector.proto`、`web/src/api/collector/index.ts`、`web/src/views/collector/task-instances/task-instances.vue`。
- 测试：`modules/collector/internal/domain/task_instance_test.go`、`modules/collector/internal/store/task_instance_test.go`、`fetch_batch_test.go`、`database_test.go`、`modules/collector/internal/marketfetch/scheduler_test.go`、`reconciler_test.go`、`kline_pipeline_test.go`。新增目标重试测试文件 `modules/collector/internal/store/fetch_retry_test.go`。

## 实施阶段

### 阶段一：调整 Tag 和 Storage 数据表

**涉及文件：** 本节“Tag / Storage”列出的 Proto、Schema、DAO、catalog、前端文件及测试。

- [x] 在 Tag Proto 中将 `repeated sources` 改成单值 `source`，将 `instrument_type` 统一为 `market_type`；如该字段实际承载的是产品/标的类型而非市场，先用现有枚举和 Provider 配置确认语义，再确定一个唯一字段，不能保留两个重复维度。
- [x] 在 `metadata.sql` 和 SQLite fresh schema 中把 `c_sources_json` 改成 `c_source_id TEXT NOT NULL`，将 `c_instrument_type` 改成 `c_market_type TEXT NOT NULL`；约束 `c_source_id` 同 Space 的 DataSource 外键，`ON DELETE RESTRICT`。
- [x] 在 `t_tags` 增加全局 `UNIQUE(c_tag_id)`；保留 Space+Tag 复合唯一键以支持 `t_subject_tags` 的复合外键，并保留 Space+TagName 唯一键。更新种子 Tag，消除跨 Space 的重复 Tag ID。
- [x] Tag 新建时校验 ID 全局未占用、source 存在且启用、market_type 合法并与 source 兼容；source 必填。`source_id` 与 `market_type` 创建后不可修改，更新请求若试图改变任一字段则明确拒绝并提示创建新 Tag。并发重复由唯一索引兜底，返回可区分的 Tag ID 冲突错误。
- [x] Tag 编辑器改为单选数据源和市场类型；编辑时显示冲突错误，保留用户输入。
- [x] 为 DAO 增加测试：全局重复 Tag ID（即使不同 Space 也拒绝）、重复名称、source 缺失/无效/停用、market/source 不兼容、有效创建与更新。
- [x] 生成 Storage Proto：`make -C modules/storage/proto`。
- [x] 在 `modules/storage/` 运行 `go test ./internal/service/metadata/sqlite ./internal/service/catalog`；前端阶段完成后运行对应 Tag Vitest。

### 阶段二：让跨 Provider CollectionTask 有正确 Dataset 语义

**涉及文件：** `modules/storage` Dataset 创建/校验代码、Collector taskresult 代码和相关测试。

- [x] Collector 结果 Dataset 不绑定单一 DataSource。优先将 `c_data_source_id` 改为可空并仅允许 Collector-owned Dataset 为空；普通用户创建的来源型 Dataset 仍要求有效来源。同步 Proto/API 的空值语义与 metadata 外键约束。
- [x] Dataset 通过 `attributes.collector_task_id` 和 `subject_tags` 关联任务/标签；Provider 来源只从 Tag 和 TaskInstance 获取，不伪造合并来源。
- [x] 添加 Storage 测试：Collector-owned Dataset 可无单一 source 创建；普通 Dataset 缺少 source 仍失败；有 source 的 Dataset 外键校验仍有效。
- [x] 添加 taskresult planner 测试：跨两个 source 的 Tag 创建一个结果 Dataset 和一个 View，Dataset 不带虚构 Provider/source。
- [x] 检查 DataSource 删除限制与 Collector-owned Dataset 行为：没有 Tag 引用的 DataSource 不应仅因无单一来源的 Collector 结果 Dataset 而被阻止删除。

### 阶段三：重塑 CollectionTask Schema、API 和 task_id

**涉及文件：** Collector Proto、domain/store/RPC、taskresult、`collector.sql`、前端任务表单与测试。

- [x] 从 `t_collector_tasks`、CollectionTask Proto 和 Go domain 中移除权威 `provider` 与 `market_type`；保留 `data_type`、名称、说明、`collect_params`、启用状态、结果 Dataset/View 等任务级字段。
- [x] 新增 `t_collector_task_tags(c_space_id, c_task_id, c_tag_id, c_ctime)`，主键 `(c_space_id,c_task_id,c_tag_id)`，索引 `(c_space_id,c_tag_id)`；对本地任务建级联外键，不跨库引用 Storage Tag。任务 API 在写入关系前通过 Storage 批量校验所有 Tag。
- [x] 使用 `github.com/rs/xid` 的 `xid.New().String()` 生成 task_id；加入 `modules/collector/go.mod`/`go.sum`。唯一约束仍由 `(space_id, task_id)` 保护。
- [x] `taskresult` 使用 `view_{taskID}_{taskType}_{frequency}` 生成唯一 View；每个用户任务创建独立 Dataset/View。结果存储高级配置为空时用系统默认值。
- [x] 为采集任务定义规范化摘要 `c_definition_hash`，内容包含排序后的 Tag IDs、task type、frequency、输出字段及影响 Provider 请求的参数；对 `(space_id,c_definition_hash)` 建唯一索引，拒绝完全重复定义，允许部分 Tag 重叠。重复任务并发保护依赖该唯一约束，不再通过共享 View ID 或 `t_collector_result_view_reservations` 间接实现。
- [x] 任务新建表单用 Tag 多选，展示 Tag 来源/市场只读摘要；任务本身不显示 Provider/市场选择器和启用状态；“采集范围”按方法决定必填或允许空。
- [x] 增加测试：XID 长度/字符集、单任务接受跨市场/跨 Provider Tag、缺失 Tag/source 拒绝、exact definition 冲突、部分重叠允许、View ID 精确命名、新建默认启用。
- [x] 生成 Collector Proto：`make -C modules/collector/proto`。从 `modules/collector/` 运行 `go test ./internal/domain ./internal/store ./internal/rpc ./internal/planner/taskresult`。

### 阶段四：拆分 TaskInstance 与 WriteTarget 表

**涉及文件：** `domain/task_instance.go`、`domain/fetch_batch.go`、Collector store/schema/bootstrap 及相关测试。

- [x] 新建 `t_collector_runs`：至少包含 `space_id`、`run_id`、`run_type`、`frequency`/调度粒度、`target_time` 或等价逻辑轮次字段、状态、错误摘要及时间戳。定时 Run 的逻辑唯一键由数据库仲裁；manual replay/backfill/repair 创建新的 `run_id`。Run 状态由 Scheduler/Planner 创建、Dispatcher 推进活动态、Reconciler 汇总终态。
- [x] `t_collector_task_instances` 保留原子请求字段：`space_id`、`run_id`、`instance_id`、`request_key`、Provider、source、market、data type/method、subject/symbol、frequency、目标时间/范围、规范化请求参数、fetch 状态/attempt/结果/时间戳。删除单任务的 `c_task_id` 和单 Dataset 的 `c_dataset_id`。
- [x] 为 `(c_space_id,c_instance_id)` 保留唯一索引；新增 `(c_space_id,c_run_id,c_request_key)` 唯一索引；不要新增 `(c_space_id,c_request_key)` 永久唯一。增加 Provider+状态、市场+方法+subject/frequency 等真实列表查询所需索引，不给每个字段盲目建索引。
- [x] 新建 `t_collector_instance_write_targets`，至少包含 `c_space_id`、稳定 `c_write_target_id`、`c_instance_id`、`c_task_id`、`c_dataset_id`、`c_view_id`、`c_output_fields_json`、`c_status`、attempt/next_retry/error、ctime/mtime。唯一约束 `(c_space_id,c_instance_id,c_task_id)`，按 `(c_space_id,c_task_id,c_status)` 和 `(c_space_id,c_dataset_id,c_status)` 建查询索引；本地实例/任务外键 `ON DELETE CASCADE`。
- [x] 将 `WriteTarget` 作为实例与任务的唯一关系表；不建重复 `TaskInstanceTask` 表。实例没有目标时禁止 dispatch；清理孤儿时先检查活动批次和重试。
- [x] 规范化请求参数 JSON（稳定 key 排序、统一大小写/默认值）后计算 request_key。必须纳入 Provider/source、market、method/data type、subject/provider symbol、frequency、时间范围和请求影响参数；排除 run/task/dataset/view/tag/output fields。`run_id` 是执行上下文，只与 request_key 共同参与实例唯一约束，不参与请求语义 hash。
- [x] 更新 `Store.DeleteTaskRuntime`：删除指定任务的 WriteTargets，不删除被其他任务引用的实例；仅在无 WriteTarget 且无活动 batch/retry 时清理实例。
- [x] 测试：同一 run 内相同 request key 并发只插入一个实例；不同 run 允许相同 request key；一实例多个目标；目标 upsert 幂等；删一个任务目标保留其他目标；目标状态分别更新。
- [x] 在临时全新数据库上验证 Schema；不实现旧 Schema 迁移。从 `modules/collector/` 运行 `go test ./internal/domain ./internal/store`。

### 阶段五：调整批次、重试和周期就绪表

**涉及文件：** `collector.sql`、`domain/fetch_batch.go`、store batch/retry、period readiness 实现与测试。

- [x] `t_collector_fetch_batches` 移除 `c_task_id` 与 `c_dataset_id` 的单所有者字段。批次按实际运行分片信息保存 schedule、function/node/region、Provider route、状态和计数。
- [x] 新建 `t_collector_fetch_batch_items`：batch_id + instance_id 关系、请求项状态/错误/完成时间；唯一 `(space,batch,instance)`，索引 instance 与 batch 查询。BatchInvocation 能稳定恢复其所有实例。
- [x] `t_collector_fetch_retry_items` 增加 `retry_scope`（fetch 或 write_target）、`instance_id`；目标重试必填 `write_target_id`，fetch 重试不绑定单一任务/Dataset。用 `(space,retry_key)` 幂等。
- [x] 更新 RetryItem DAO 事务：fetch 失败更新实例并为请求安排重试；目标写入失败只更新失败目标和对应目标重试，不改写已成功目标。
- [x] `t_period_readiness_items` 删除唯一依赖 instance 的关系，改存 `write_target_id` 并以 `(readiness_id,write_target_id)` 唯一；保存规范 `series_tag`，完成匹配使用 `subject_id + series_tag`。不得保留 `(readiness_id,subject_id)` 这类会把同一 Dataset 中不同 Provider 系列错误折叠的唯一约束。
- [x] 测试：批次包含跨任务多个实例；重复 batch item 被抑制；fetch retry 与 target retry 隔离；同一共享实例在两个 Dataset 的 readiness 分别完成；重复/迟到回调不回退成功目标。
- [x] 运行 `cd modules/collector && go test ./internal/store ./internal/marketfetch`。

### 阶段六：全局展开、去重与结果扇出

**涉及文件：** `marketfetch/scheduler.go`、`reconciler.go`、`assignment.go`、`environment.go`、`kline_pipeline.go`、相关领域测试。

- [x] 调度开始时先创建或取得本轮 `t_collector_runs` 记录。定时轮次通过规范逻辑唯一键 `INSERT ... ON CONFLICT`/事务仲裁，多个 Scheduler 必须获得同一 `run_id`；manual replay/backfill/repair 显式创建新 Run。随后批量加载全部启用任务及其 Tag 关系，再经 Storage RPC 读取 Tag 的单一 source/market 和成员；禁止任务级 Provider/market 覆盖 Tag 配置。
- [x] 校验所选 Tag 均存在、source 可用、成员范围合法，且 Tag market 与该方法/Provider 支持范围兼容。错误信息带 task 和 tag；不能静默跳过 Tag。
- [x] 对全部任务生成候选原子请求，按规范 request_key 在当前 Run 内全局分组。每个唯一请求按 `(space_id,run_id,request_key)` upsert 一个 TaskInstance，并为每个目的任务 upsert 一个 WriteTarget。规划完成后由 Planner 将 Run 标记为 planned/active；终态只由 Reconciler 根据实例和目标状态汇总。
- [x] reconciliation 只删除 scope 已移除/任务停用任务的 WriteTargets；同一实例仍有别的任务目标时不得删除或禁用实例。
- [x] 明确执行节点所有权：crypto Kline 从 Invoke pool 调度并要求 EventBus completion；stockcn realtime Kline 才走 Timer fleet。发布器为 crypto 按配置容量创建 Invoke functions，并在切换前禁用旧 Timer Trigger；Scheduler 不得把缺 EventBus 凭据的 Timer function 当作普通 crypto Invoke 节点。
- [x] Provider 成功后归一化一次结果，遍历所有待写目标：各自投影 output fields、按目标 Dataset + 规范化 series identity 构造 RowKey、分别 upsert，并各自更新状态。series identity 必须区分同一 Dataset 内可能并存的 Provider/Source/Market/Subject/Frequency，避免跨 Provider 标签写入同一任务 Dataset 时互相覆盖。
- [x] Provider 请求失败时在同一个 Run/TaskInstance 上增加 attempt 并驱动 fetch retry，不创建语义重复实例；部分目标写入失败时保持实例 fetch 成功，只重试失败目标。目标重试可重新抓 Provider，但只写待重试目标。手工重放/补采/修复通过新建 Run 表达。
- [x] 测试 tag1+tag2 / tag2+tag3 只请求一次共有成员；Provider/market/frequency/time/响应参数不同则实例分开；一任务跨 Provider 不拆任务；双 Dataset 扇出；字段投影不同；部分失败和迟到回调保持状态正确。
- [x] 运行 `cd modules/collector && go test ./internal/marketfetch ./internal/store ./internal/domain`，并编译实际 SCF consumer 包。

### 阶段七：更新任务创建前端

**涉及文件：** `web/src/api/collector/index.ts`、`collection-task-params.ts`、`collection-tasks.vue` 及对应测试。

- [x] 用 Tag 多选替换任务级 Provider/市场输入；每个 Tag 展示名称、source、market_type 摘要。
- [x] 按执行任务类型动态呈现该方法所需参数；这些参数属于方法，不得把 source/market 作为可覆盖任务参数。
- [x] “采集范围”对有标的概念的方法要求至少选择一个 Tag；对无标的的方法允许空。
- [x] 输出字段弹窗分组展示全局字段目录，不按任务类型过滤，不限制字段数量；保存所有勾选值。方法不支持字段时显示服务端明确校验错误，保留用户选择便于修改。
- [x] 删除新建弹窗“启用状态”；提交即启用。结果存储设置放入可选高级区，默认值由服务端应用。
- [x] 增加测试：跨 Provider Tag payload、按方法校验空/非空采集范围、任意多字段、不展示启用开关、默认结果配置和重复任务错误提示。
- [x] 运行 `pnpm --dir web test -- src/views/collector/collection-tasks/collection-task-params.test.ts src/views/collector/collection-tasks/collection-tasks.test.ts`。

### 阶段八：更新任务实例 API 和页面

**涉及文件：** Collector Proto/RPC、生成代码、`web/src/api/collector/index.ts`、`web/src/views/collector/task-instances/task-instances.vue`。

- [x] TaskInstance 响应保留实例级 Provider 请求信息和状态，删除单一 `task_id`/`dataset_id` 所有权，增加 WriteTarget 摘要数组（task、Dataset、View、字段数、写入状态、错误）。
- [x] 实例列表按 TaskInstance 一行展示，不按目标重复行；支持 task_id/Dataset 过滤时通过 WriteTarget 查询。
- [x] 详情/展开区展示全部目标和独立状态；从目标进入相应任务和结果 View。
- [x] 测试共享实例有多个 WriteTarget、按某目标任务过滤实例只显示一次、一个成功一个失败的 UI 状态。
- [x] 运行 `make -C modules/collector/proto`；从 `modules/collector/` 运行 `go test ./internal/rpc ./proto/...` 和相关前端 Vitest。

### 阶段九：清理 Schema 与全新启动验证

**涉及文件：** Storage/Collector Schema、SQLite fresh schema、bootstrap、种子和 setup/release 配置。

- [x] 删除新模型不再使用的 Tag `sources`、CollectionTask Provider/market、TaskInstance 单 task/dataset、batch/retry 单 task/dataset 列和旧索引；删除 `t_collector_result_view_reservations` 及对应代码。加入 `t_collector_runs`、TaskInstance `run_id` 以及新唯一约束。
- [x] 保证 `modules/collector/schema/collector.sql` 与运行时 bootstrap 建表完全一致；Storage `metadata.sql` 与 sqlite store fresh schema 对 Tag/Dataset 完全一致。启动时对新模型关键表/列做 fail-closed 契约检查；发现半迁移数据库直接提示 reset，不允许运行到业务 SQL 才报缺列。
- [x] 更新种子：Tag 全局 ID 不重复，每个 Tag 有且仅有一个有效 source 与 market_type；任务种子用 tag relation，不写任务 Provider/market；内置 CollectionTask 的固定 ID 也必须是合法 20 位 XID。
- [x] 不为被替换的数据结构添加历史迁移代码。新项目初始化直接创建最新 Schema；已有开发/验证目录按计划删除重建。不要让生产应用启动时自动删除数据库。
- [x] 全仓检索 `sources_json`、`GetSources`、任务级 `provider/market_type`、实例级 `c_task_id/c_dataset_id`，确认剩余命中属于 Tag/TaskInstance/WriteTarget 正确层级。
- [x] 测试空数据目录下 Storage 和 Collector 初始化、索引/外键/唯一约束、基础种子完整。

### 阶段十：端到端验收与正式发布

- [x] 从 `modules/storage/` 运行 `go test ./internal/service/metadata/sqlite ./internal/service/catalog`。
- [x] 从 `modules/collector/` 运行 `go test ./internal/domain ./internal/store ./internal/rpc ./internal/planner/taskresult ./internal/marketfetch ./internal/bootstrap`；实际已执行更强的 `go test -count=1 ./...`。
- [ ] 运行 `pnpm --dir web test` 和 `pnpm --dir web build:prod`。
- [ ] 使用空数据目录创建两个不同 Provider/market 的 Tag；创建一个多 Tag 采集任务，确认只得到一个 task_id、一个 Dataset、一个符合命名规则的 View。
- [ ] 创建第二个复用其中一个 Tag 的任务；确认同一个 CollectorRun 中共有请求只有一个 TaskInstance，且有两个 WriteTarget；不同 source、market、频率或参数则为不同实例。再创建 manual replay Run，确认允许为同一 request_key 产生新的 TaskInstance。
- [ ] 执行可控采集，确认 Provider 请求仅发一次、两个 Dataset 各自收到正确投影字段和 RowKey。
- [ ] 注入一个目标写入失败；确认另一个目标保持成功，重试只更新失败目标。
- [ ] 验证重复 Tag ID 全局冲突、重复任务定义冲突、部分 Tag 重叠允许、删除一个任务保留另一任务目标、全新 Schema 启动。
- [ ] 按新项目发布流程明确清空 Collector/Storage 正式项目数据目录，再运行初始化并检查 Schema/种子；构建、发布并做线上创建 Tag、创建任务、查看实例、结果落库的冒烟验证。删除数据仅由明确发布步骤执行，不加入日常启动流程。

## 阶段依赖

1. 阶段一 Tag/Storage → 阶段二 Dataset 来源语义 → 阶段三 CollectionTask/API。
2. 阶段四实例/目标表 → 阶段五批次、重试、周期完成度表 → 阶段六全局调度和扇出。
3. 阶段七任务 UI 依赖阶段一和阶段三；阶段八实例 UI 依赖阶段四至六。
4. 阶段九清理只在所有读写方切换后进行；阶段十最后执行。

## 数据关系摘要

```text
Tag (1 immutable source, 1 immutable market) ←→ CollectionTask (many tags, 1 Dataset/View)
CollectorRun (1 logical execution round) → TaskInstance (many, unique by run + request_key)
CollectionTask ←→ TaskInstance (many-to-many, through WriteTarget)
TaskInstance (1 Provider request in 1 Run) → WriteTarget (many)
WriteTarget (1 task + 1 Dataset + 1 View + independent write/retry state)
BatchInvocation → BatchItems → TaskInstance
PeriodReadiness (per Dataset/period) → ReadinessItems → WriteTarget
```
