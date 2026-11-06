# View 单索引与定时删除最旧数据执行计划

执行计划 v2（2026-10-30，尚未编码）。v2 相对 v1：配置项命名为 `delete_oldest_batch_size`，明确每次只删每个序列最旧的数据；索引不再分槽位、上线时删除全部 View 索引并从 Primary 重建；加列检查与定时删除合并为一个每分钟第 10 秒的任务、删除每 2 分钟一轮；每批删除行数可配置。代码基线 `main@e0316b98`（PR #83 合入后，线上 Storage 三个组件均为 `cb1f8ddd`）。

## 1. 背景与目标

现在每个 View 有 A/B 两个索引槽位，以下情况都会新建另一个槽位的索引：
- 单序列超过 `trim_bars`（1.2 万根）时裁剪回 1 万根；
- 文件超过 `max_view_file_bytes`；
- 改了定义；
- 活动索引丢失或损坏；
- 手动触发重建。

新建时从 Primary 回填，期间实时写入要同时写两个索引，回填完成后切换过去。这套机制带来了几类成本：
- 元数据里有构建表、重建日志表、两套修订号；
- View 进程里有双写、切换、旧索引退役与延迟删除、写入进度继承、构建中断恢复、容量扫描与门槛；
- 每次按根数裁剪都要从 Primary 重读全部数据，1m 因子 View 约 520 万行，约每 33 小时一次，期间内存和磁盘都翻倍。

目标：每个 View 从头到尾只有一个索引。
- 裁剪改为每分钟定时分批删除；
- 加列在原表上做；
- 其他定义变化不再支持，需要时新建 View；
- 删除整套 A/B 切换机制。

## 2. 已确认的约束

| 项目 | 规则 |
| --- | --- |
| 删列、改列名（英文名） | 系统不允许 |
| 改列类型 | 不允许；类型在建列时固定，换类型就加一个新名字的列 |
| 加列 | 允许，只能追加，旧行上新列为空，不从 Primary 回填历史值 |
| 改数据集、主键维度（grain keys）、引擎、`columns_explicit` | 不允许；要改就新建 View，验证后删除旧 View |
| 索引丢失、损坏 | 不做自动处理（小概率）；需要时用运维命令删除索引，按首次建索引重建 |
| 文件超过 `max_view_file_bytes` | 只告警，不重建 |
| 手动重建 | 去掉（管理台按钮、`RequestViewRebuild`） |
| 首次建索引期间 | View 未就绪，读取方等待（与现在一致） |
| 历史数据兼容 | 不兼容：上线时删除全部 View 索引，从 Primary 重建；索引不再分槽位 |
| `max_age` | 仍然只过滤写入，不并入定时删除 |

## 3. 实测依据（2026-10-30，本机）

测试数据：线上 `view_factor_swap_1m` 索引副本（523 个序列、34 列），参数与线上一致（`memory_limit=967MB`、`threads=1`）。

**定时分批删除**（从 1.2 万根删回 1 万根，再模拟 300 轮“每分钟新增一根、删一根”，删除期间实时写入并发）：
- 做法：先不持锁，按序列查删除分界（`ORDER BY data_time LIMIT 1 OFFSET k`，只读）；再持写锁，在一个事务里逐序列执行 `DELETE ... data_time < 分界` 并减计数。每批最多 64 个序列、2000 行，每个索引每分钟最多 2 万行。

| | 按时间排列（线上布局，实时追加形成） | 按序列排列（回填形成） |
| --- | --- | --- |
| 积压 105 万行 | 18 秒删完 | — |
| 稳态每批持写锁 | p50 28ms，最长 39ms | p50 34ms，最长 54ms |
| 锁外查分界，每批 | 约 70ms | 约 180ms |
| 实时写入等锁 | 约 0 | 约 0 |
| 实时写入最长一次 | 130ms（碰上自动检查点） | 435ms（碰上自动检查点） |
| 结果 | 每序列精确 1 万根 | 每序列精确 1 万根 |
| 文件大小 | 2011→2104MB 后不再增长 | 删除后一次检查点涨到 2338MB，之后不再增长 |

- 第一版把查分界放在锁内、每批 512 个序列，稳态每批持锁 0.7～0.9 秒，已否决。
- 每个 1m View 每分钟的删除工作约 1～2 秒（本机），5 个 1m View 合计 5～10 秒。线上 CPU 更慢，要留余量。
- 文件不会缩小：持续删除维持的文件约 2.1～2.3GB，比同样数据新建的文件（1.67GB）大 25%～40%，这是一次性代价。

**原地改列**（满量 1 万根，522 万行，1.67GB）：
- 加列 33～41ms，只改元数据，读写不受影响。线上 7 个因子 View 已经在走这条路径。
- 表上有 `idx_view_rows_data_time` 时，DuckDB 拒绝改类型、改名、删列（Dependency Error），这几项本计划都不允许，不再处理。
- 在同一文件里复制整张表：在 967MB 上限下内存不足，失败。

## 4. 目标设计

### 4.1 View 生命周期

1. **创建**：业务模块 `CreateView`，元数据写入 `c_status='building'`、活动索引为空。
2. **首次建索引**：storage-view 维护循环发现 `building` 的 View，执行以下步骤：
   1. 建索引。一个 View 只有一个索引，文件名由空间和 View ID 决定，不再有 `_a`/`_b` 槽位后缀；
   2. 挂上实时写入（这期间投递写进这个索引后即可确认）；
   3. 时序 View 从 Primary 按 `latest_per_series = bars` 回填，回填只补缺、不覆盖已有行；记录型（bleve）View 从空开始；
   4. 完成后在元数据里置 `c_status='active'`、写入 `c_active_index_id`。
   - 进程中途退出：删除半成品索引，下一轮从头建，不做断点续建。
   - 期间查询返回 `VIEW_NOT_READY`。
3. **加列**：元数据只接受追加列。storage-view 发现期望列多于已建列时，持写锁执行 `ALTER TABLE ADD COLUMN`，再提交到元数据（沿用现有原地扩列的写法，扩大到所有 View，不再要求 `follow_dataset_columns`）。
4. **日常**：实时写入 + 每分钟定时删除（4.2）+ 文件大小告警（4.4）。
5. **删除**：`DeleteView` 后 storage-view 删除索引文件，沿用现有删除流程。

### 4.2 定时删除最旧数据

- **触发**：storage-view 内只有一个按墙上时钟对齐的定时任务，每分钟第 10 秒执行（避开整点前后的周期提交和策略读取高峰），依次做两件事：
  1. **加列检查**（4.3）：每分钟都查，通常什么都不做；
  2. **删除最旧数据**：只在偶数分钟执行一轮，奇数分钟跳过。
  两件事在同一个任务里先后执行，不会同时进行。上一次任务没做完时跳过本次，不叠加。
  2 分钟一轮的理由：每轮的开销主要在“逐个序列查分界”（每个序列约 1ms），与每个序列删 1 根还是 2 根无关；2 分钟一轮的 CPU 开销约为每分钟一轮的一半，序列最多临时多出 1 根（10001 根）。
- **范围**：有频率的时序 View 的活动索引（DuckDB）；记录型 View 不处理。
- **删什么**：只删每个序列里**最旧的**时序数据，也就是按 `data_time` 从早到晚、排在最新 `bars` 根之前的那些行。每个序列最新的 `bars` 根永远不动，实时写入的新根也不会被删。
- **每个索引的一轮**：
  1. 查 `view_series_counts` 中 `row_count > bars` 的序列，每批最多 64 个序列、合计最多 `delete_oldest_batch_size` 行；
  2. 不持锁，逐序列查删除分界：第 k 旧的那根，k = 超出根数与本批剩余额度的较小值；
  3. 持该索引的写锁，开一个事务：逐序列 `DELETE FROM view_rows WHERE 序列键 AND data_time < 分界`，按实际删除行数减 `view_series_counts`，必要时把 `view_meta.indexed_from` 前移，然后提交；
  4. 释放写锁，进入下一批；每个索引每轮最多删 `delete_oldest_batch_size × 10` 行，超出部分留到下一轮。
- **正确性**：
  - 分界在锁外按快照计算，删除只针对比分界更旧的行；
  - 实时写入只追加最新的根，不会被误删；
  - 补采、导入写入恰好被删范围内的旧时间点时，结果取决于先后顺序，下一轮会再删掉，与现在 A/B 裁剪的语义一致。
- **参数**：
  - `delete_oldest_batch_size`：每批最多删除的最旧行数，可配置，**默认 2000**（实测值），写在 `config/storage_view/trpc_go.yaml` 的 `storage.view` 下，与 `backfill_page_size` 并列，取值 1～20000。调小则单批持锁更短、积压删得更慢；
  - 每轮上限为 `delete_oldest_batch_size × 10`，每批序列数 64、第 10 秒、2 分钟一轮写在代码里；
  - `bars` 仍取存储策略的 `view.bars`。
- **观测**：
  - 指标：每轮删除行数、单批持锁时长、积压序列数；
  - 日志：首轮积压和异常耗时（单批持锁超过 200ms）打日志。
- **`indexed_from` 的更新方式**：实现时先测 `SELECT min(data_time)` 在满量索引上的耗时。几十毫秒以内就每轮更新一次；否则改用本轮各序列分界的最小值近似。

### 4.3 加列

- 元数据侧（`UpdateView`、`UpsertViewColumn`、`UpsertDatasetColumn` 引起的跟随更新）只接受“已建列不变 + 末尾追加”。删列、改名、改类型、改顺序一律拒绝，返回明确的中文错误。
- View 侧：每分钟第 10 秒的定时任务先做加列检查（4.2），比较期望列与 `view_columns`，持写锁 `ADD COLUMN`，再提交元数据。中途退出时，下一轮按“物理列已含新列”的状态补提交（沿用 `recoverViewSchemaExtension` 的思路，但只剩这一种情况）。
- 修订号：只保留一个 `c_view_revision`，每次加列加 1，与 DuckDB `view_meta.view_version` 对齐。查询在执行期间检测到修订号变化时返回 `VIEW_NOT_READY`，这一点保持现状。

### 4.4 文件大小告警

- 所有 View 统一比较文件大小与 `max_view_file_bytes`，超过时导出指标并打一次日志。现有的 `permanent_view_capacity_over_limit` 改为不区分记录型与时序型的 `view_file_over_limit`。
- Monitor 增加对应告警规则。实现时先确认 Monitor 读取 Storage 指标的方式，现在这个指标没有告警规则。

### 4.5 元数据模型（schema v15 → v16）

- 删除 `t_view_index_builds`、`t_view_rebuild_logs` 及其索引。
- 上线时直接执行新的 v16 schema 定义改造 View 相关表（见第 7 节），不写兼容迁移代码。
- `t_views` 的改动：
  - 删除 `c_desired_view_revision`、`c_active_slot`（不再有槽位）、`idx_t_views_revision_pending`；
  - `c_active_view_revision` 改名为 `c_view_revision`；
  - `c_status` 继续用 `building` / `active` / `disabled` / `deleted`。
- `t_view_period_dataset_states`、`t_view_sync_points` 保留（周期就绪与同步点和 A/B 无关）。实现时确认其中没有按槽位区分的字段。
- 按 `modules/storage/schema/metadata.sql` 的格式规范改写，逐个载入空 SQLite 校验。

### 4.6 接口变化

- **删除 Metadata RPC**：`RequestViewRebuild`、`ClaimViewIndexBuild`、`UpdateViewIndexBuild`、`ActivateViewIndex`、`FailViewIndexBuild`、`ListViewRebuildLogs`、`CreateViewRebuildLog`、`UpdateViewRebuildLog`、`UpsertSkippedViewRebuildLog`。
- **改为单索引语义的 Metadata RPC**：
  - 新增 `CompleteViewInitialBuild`：storage-view 首次建索引完成后调用，把 View 置为 active，写入活动索引与已建列；
  - `CommitViewSchemaExtension`：加列提交，去掉 desired/active 两个修订号的比较，改为比较已建列与修订号。
- **删除 proto 消息与枚举**：`ViewIndexBuild`、`ViewRebuildLog`、`ViewRebuildTriggerReason`、`ViewRebuildResult` 及对应的 Req/Rsp；`View` 消息里的 `index_build`、`desired_view_revision`、`active_slot` 字段（按项目规则不保留 `reserved`）。
- **ViewIndex 服务**（`view_index.proto`）：`PrepareViewIndex`、`ApplyViewIndex`、`StatViewIndex`、`RemoveViewIndex`、`ListViewIndexes` 保留，去掉其中只为 A/B 服务的参数。实现时逐个确认调用方。
- **`packages/servicecatalog/catalog.yaml`**：同步删除上述方法。目录有变化，`catalog-changed` 会要求整体发布（见第 7 节）。

### 4.7 对其他模块的影响

| 模块 | 改动 |
| --- | --- |
| Collector（`planner/taskresult/result.go:789`） | 现在用 `ReplaceColumns: true` 在任务收窄输出字段时删掉 View 列。改为只追加；不再输出的字段留在 View 里，新行为空 |
| Collector（`resample/catalog.go`） | `waitTargetViewRevision` 依赖 desired/active 两个修订号比较；单修订号下没有“目标修订号”可等，改为等待 View 的已建列包含刚加的列、且 `c_status='active'` |
| Factor（`catalog/reconcile.go`、`storageio/result_view.go`） | 去掉 `follow_dataset_columns`、`rebuild_source` 属性（加列对所有 View 生效，首次建索引一律从 Primary 回填）；`maintenance_priority` 保留，用于冷启动时的建索引顺序 |
| Strategy（`replay/runner.go:243`） | 现在假定“同一代索引只追加、不删除行”，只在代次变化时复查回放区间。定时删除后最旧的行会持续消失，改为每次执行回放都复查起点（或按 `indexed_from` 截断）。代次 `ViewGeneration` 改为“活动索引 ID + 首次建成时间”，只在 View 被删索引重建时变化 |
| 管理台（`web/src/views/data/view-browse/index.vue`、`api/storage/metadata.ts`、`types.ts`、`http.ts`） | 删除“重建视图”按钮、构建日志弹窗和相关接口与类型；View 状态显示“建索引中 / 已就绪” |
| Admin（`internal/console/policy.go`） | 删除 `RequestViewRebuild`、`ListViewRebuildLogs` 的放行项 |
| CLI（`modules/storage/cmd/cli/repair_view.go`、`reset_view_consumers.go`） | `repair-view` 去掉强制重建、清理非活动槽位等选项，只保留“重置消费者”和“删除索引并重新首次建”；`reset-view-consumers --reset-all-storage` 的等待条件改为全部 View 回到 active |
| CLI 配置（`modules/cli/internal/setup/config/config.go`、`configinit/editform.go`） | 删除 `capacity_check_interval`、`capacity_check_jitter`、`trim_bars` 的生成与默认值；`max_view_file_bytes` 保留（告警阈值） |
| 存储策略（`packages/storagepolicy/policy.go`） | 删除 `TrimBars`、`TrimBarsFor`、`CapacityCheckInterval`、`CapacityCheckJitter`；`MaxViewFileBytes` 改为告警阈值的说明 |
| Storage 配置（`config/storage.yaml`、`config/storage_view/trpc_go.yaml`、`internal/config/loader.go`、`cmd/server/main.go`） | 新增 `delete_oldest_batch_size`（未配置时取默认 2000；取值 1～20000，越界时启动报错）；删除 `rebuild_max_pending`、`rebuild_idle_checks`；`backfill_page_size`、`backfill_request_interval` 保留，仅用于首次建索引 |
| 文档与 skill | `docs/模块/存储.md` 6.1 节、`docs/部署与运维.md` 的 View 内存估算与重置说明、`skills/moox/SKILL.md` 的 View 内存、`skills/moox/references/api-recipes.md` 的重建日志条目 |

## 5. 删除清单（Storage View 进程）

| 文件 | 删除或改动 |
| --- | --- |
| `view/maintenance.go`（1691 行） | 删除：容量扫描与调度（`beginSeriesCapacityCheck` 等）、积压与空闲门槛、冷却、`shouldRetryFailedBuildWithCause`、`catchUpAndActivateViewBuild`、`activateViewBuild`、`failInterruptedBuild`、`failBuild`、`discardFailedBuild` 中的构建部分、`needsCapacity*`。`maintainView` 只剩三件事：首次建索引、加列、挂接活动索引。`RestoreActiveViews` 只恢复 active 的 View，并删除 building 的半成品 |
| `view/build.go` | 删除：复制活动索引回填（`projectBackfillFields`、`enrichBackfillRows`）、`SwitchView`、`switchViewLocked`、`TrackViewBuild`、`failRuntimeBuild`、`AttachPendingViewBuild`、`MarkViewBuildReady`。保留 Primary 历史回填（首次建索引用） |
| `view/event_apply.go`、`view/period_event_apply.go`、`view/data_ready.go` | 删除替换索引（`runtime.next`）的双写、失败持久化、激活前确认等分支；运行时只剩 `active`，加上首次建索引期间的 `building` |
| `view/index_cleanup.go`（427 行） | 删除退役索引的延迟清理与候选观察；改为 `DeleteView` 后直接删除，加启动时清理“元数据里没有的索引文件” |
| `view/rebuild_log.go`（596 行） | 整个文件删除 |
| `view/maintenance_schema_extension.go` | 收敛为“追加列 + 中断补提交”，去掉 `follow_dataset_columns` 判断 |
| `view/ready_fence.go`、`view/service.go` | 删除写入进度在新旧索引间的继承（`inheritAppliedFence`）、索引代次、退役与准备中标记（`markIndexRetiring` 等）、`removeFailedBuildAtGeneration` |
| `view/metadata_direct.go` | 删除已删除 RPC 的转发 |
| `viewindex/model.go`、`viewindex/duckdb/index_manager.go` | 删除 `SeriesCapacity`、`SeriesCapacityResult`、`InactiveViewIndexID` 与槽位相关的索引 ID 解析，索引 ID 只由空间和 View ID 决定；新增定时删除用的“查分界”“按分界删除并减计数”两个引擎方法；`memory_limit.go` 注释改为“重建”只指首次建索引 |
| 元数据（`metadata/sqlite/crud_view_index.go`、`crud_view_rebuild.go`、`crud_view_rebuild_log.go`、`crud_view.go`、`crud_dataset.go`、`store.go`；`catalog/metadata_space_view.go` 等） | 删除构建与重建日志的存取；`UpdateView` 和列更新改为只接受追加；`bumpViewVersion` 改为加列时加修订号 |
| 测试 | 删除容量、双写、切换、退役、重建日志相关测试（`maintenance_capacity_cgo_test.go`、`review9_test.go` 中相关用例、`rebuild_log_test.go` 等）；保留并改写首次建索引、加列、事件写入、周期就绪的测试 |

## 6. 实施步骤

每步完成后 `go build ./...`、`go vet`、相关包测试通过，再进行下一步。

1. **定时删除（可单独先上）**
   - 实现：在 DuckDB 引擎里新增查分界、按分界删除两个方法，在 storage-view 加每分钟第 10 秒的定时任务（这一步里只有删除，偶数分钟执行；加列检查在第 3 步并入），新增 `delete_oldest_batch_size` 配置，指标和日志一并加上。这一步不改索引命名。
   - A/B 代码先不动，也不改 `trim_bars`。定时删除会把每个序列保持在 1 万根，永远到不了 1.2 万的触发线，按根数的重建自然不会再触发。
     不能手工改 `trim_bars`：它由 CLI 按 `TrimBarsFor(view.bars)` 生成，下次发布配置时会被改回 12000。
   - 测试：在 DuckDB 上补 cgo 测试，覆盖计数正确、覆盖起点前移、只删超出部分、跨批与每轮上限、`delete_oldest_batch_size` 生效、写锁持有时长；调度测试覆盖对齐第 10 秒、只在偶数分钟删除、上一次未完成时跳过。
   - 时间点：线上 1m View 都是 10-28 14:36（北京时间）前后从空开始的，10-30 时每个序列约 2900 根。
     - 约 11-04 13:00 前后到 1 万根，定时删除从这时才开始真正删数据，在此之前上线它什么都不做；
     - 约 11-05 22:00 前后会到 1.2 万根，触发旧的 A/B 裁剪。
     - **第 1 步要在 11-04 之前上线**，否则旧的 A/B 裁剪会先跑一次。按实测这次也撑得住（峰值为内存上限的 94%），但正是本计划要避免的。
   - 上线当天每个序列都还不到 1 万根，删除不会真正发生：用 `moox_storage_view_oldest_delete_last_round_timestamp_seconds` 每 2 分钟前移、启动日志“定时删除最旧数据已启动”确认任务在跑。K 线 1m View 比因子 View 早约 1 小时开始写，最早约 11-04 12:00（北京时间）到 1 万根。
   - 验收（看 11-04 起的头几轮）：单批持锁 p99 < 100ms；策略结果不出现 expired；每序列保持 1 万根；文件大小在一天内稳定。
2. **元数据只接受追加列**：`UpdateView`、`UpsertViewColumn`、数据集跟随更新拒绝非追加变化；Collector 改为只追加。
3. **加列对所有 View 生效，并入定时任务**：去掉 `follow_dataset_columns` 限制；加列检查从维护循环移到每分钟第 10 秒的定时任务里，排在删除之前；Factor 删除这个属性和 `rebuild_source`。
4. **首次建索引独立出来**：新增 `CompleteViewInitialBuild`；维护循环改为只处理 building / active；重启时删除半成品。
5. **删除 A/B 机制与槽位**：按第 5 节删除 View 进程代码；索引 ID 去掉槽位；删除元数据的构建与重建日志存取、RPC、proto、servicecatalog 条目；schema 升到 v16。
6. **调用方与周边**：Strategy 回放复查起点、代次改义；Collector 等待条件；管理台、Admin 放行、CLI repair-view / reset、CLI 配置、存储策略与 Storage 配置。
7. **文件超限告警**：指标改名，Monitor 加规则。
8. **文档**：第 4.7 节列出的文档和 skill；`docs/实现状态.md` 记一条。

第 1 步与后面几步之间只依赖 DuckDB 引擎的新方法，可以先上线观察一两天，再做 2～8 步。

## 7. 上线步骤

**第 1 步单独上线**（11-04 之前）：换 storage 二进制并重启 storage-view；同时换 strategy 二进制并重启 strategy（回放改为开始前总是复查起点，见 4.7 的 Strategy 一行，这一项从第 6 步提前到第 1 步）。不改任何配置，未配置 `delete_oldest_batch_size` 时取默认 2000。回退方法：用备份二进制替换回去。

**第 2～8 步一次上线**（服务目录有变化，必须整体发布；不兼容历史 View 数据，全部重建）：
1. 发布前备份 `storage_metadata.db` 与 View 索引目录（只用于回退）。
2. 暂停采集和因子写入，停 Storage 三个组件和依赖新接口的服务。
3. **删除全部 View 索引文件**（`storage-view/var/storage/view-indexes/` 下所有引擎的索引目录），以及 View 进程的写入进度与就绪队列落盘文件。
4. 手工执行一次性 SQL，把元数据升到 v16。保留 `t_views` 的 View 行（策略实例按 View ID 绑定），不写迁移代码：
   - 删除 `t_view_index_builds`、`t_view_rebuild_logs`；
   - 改 `t_views` 的列（删 `c_desired_view_revision`、`c_active_slot`，`c_active_view_revision` 改名为 `c_view_revision` 并置 1）；
   - 所有 View 置 `c_status='building'`、清空 `c_active_index_id` 与 `c_indexed_from`/`c_indexed_to`，已建列置为当前定义的列；
   - 清空 `t_view_sync_points` 中与旧索引相关的记录（实现时确认字段）；
   - 更新 `t_schema_meta`。
   - 先在备份副本上跑通，并逐个载入 schema 校验。
5. 整体发布全部二进制与管理台，启动 Storage，storage-view 按 `maintenance_priority` 逐个从 Primary 首次建索引（K 线优先）。全部建完前相关 View 不可读，策略会跳过周期；按 10-28 的经验，合计约十几分钟。恢复采集和因子写入。
6. 验收：
   - 全部 View 回到 active，索引文件名不带槽位后缀，查询正常；
   - 每序列根数不超过 `bars`，定时删除指标正常；
   - 策略回放能跑；
   - 管理台 View 页面没有重建入口；
   - 往一个因子 View 加列能原地完成。
7. 回退：恢复元数据库与索引目录的备份，换回旧二进制，整体重启。

## 8. 风险与待验证

- **长时间运行的碎片**：实测只跑了 300～600 轮。上线后盯文件大小，正常应稳定在 2.1～2.3GB（1m 因子 View）。如果持续增长，只能新建 View 来压缩。
- **线上 CPU**：线上比本机慢。如果第 10 秒开始的一轮在一分钟内做不完，就把每分钟上限调小，或者分散到多个秒点。
- **读取方需要超过 1 万根的情况**：现在 1 万到 1.2 万之间的旧行偶尔能读到，以后不会再有。实现时检查有没有调用方依赖这一点，包括因子长回看。长回看读的是因子本地存储，需要确认。
- **Strategy 回放**：起点附近的数据会持续被删。回放必须容忍起点前移，否则会出现时有时无的“数据不足”。
- **Collector 不再删列**：任务收窄输出字段后，旧列保留，新行为空。要确认查询和管理台展示能接受空列。
- **首次建索引期间不可读**：新建大 View 的这几分钟里，依赖它的策略会跳过周期，与现在一致。上线那次是全部 View 一起重建，影响时间更长（约十几分钟），要选在可以接受的时段做。

## 9. 不做的事

- 不做断点续建，不做从 Primary 回填新列的历史值，不做索引损坏的自动检测与修复。
- 不压缩文件，不对 `max_age` 做主动删除。
- 定时删除只开放 `delete_oldest_batch_size` 一个配置项，其余参数写在代码里，需要时再提出来。
- 不保留旧 View 索引数据，不兼容 `_a`/`_b` 槽位命名。

## 附：第 2～8 步的实现结果（2026-10-30）

### 与上文的差异

1. “建索引中”沿用 `c_status='active'` 且 `active_index_id` 为空，不用 `building` 状态，业务模块不用改。
2. 声明的列（`t_view_columns`，没有显式投影时取数据集的列）与已建列（`active_columns`）按列名比较，不比较修订号；`view_revision` 建成时为 1，每次加列加 1。`moox.index_generation` 在建成时写入，策略回放用它判断换代。
3. 保留 `follow_dataset_columns`：语义是“数据集加列时把新列加进 View 的声明”。原地加列本身对所有 View 生效；`rebuild_source` 删除，首次建索引一律从 Primary 回填（Primary 为空时从空开始）。
4. 只拒绝改数据集、引擎、主键维度；`moox.columns_explicit` 可以变，只要不删列。
5. `upsertViewColumn`：已有列的来源和类型不能改，排序保持原值，属性和上线时间可改；新列排在最后。`ReplaceColumns` 必须包含已有的声明列和已建列。Collector 的 `EnsureOutputFields` 改成只追加，任务收窄输出后旧列留着（新行为空）。
6. 数据集列被禁用只是逻辑上的：不删 View 列，只在 View 返回结果时过滤。
7. 首次建索引：运行时 `building` 是唯一写入目标，建成后转为 `active`；任何时刻只写一个索引。`PrepareViewIndex` 先删半成品（含 WAL）再准备，全程持运行时锁。回填失败丢弃半成品、下一轮重建；提交响应不明确时读回元数据，读不到就保留现场，下一轮按元数据决定，绝不在这里删索引。
8. 加列放在每分钟的定时任务里（`StartMinuteTasks`），先加列、偶数分钟再删除，同一任务里顺序执行，不会同时占写锁。崩溃恢复：物理修订号比元数据大 1 时补提交，这期间新声明的列接着追加（`ExtendColumns` 允许修订号相同、列是前缀）。维护读到的元数据早于刚提交的加列时，`AttachActiveView` 忽略旧快照。
9. Bleve 的字段映射建好后不能改：`ExtendColumns` 只更新索引自身记录的列清单，新列按动态映射写入，字符串列没有精确匹配的 keyword 字段。线上目前没有 Bleve 索引。
10. 孤儿索引清理保留（已删除 View 的文件靠它），去掉退役和代次；同时保护 `building`。
11. 文件超限告警：指标 `moox_storage_view_file_over_limit`（超限的 View 数量），Monitor 业务检查 `view_file:storage_view`。

### 上线步骤（要整体发布，View 不可读约十几分钟）

发布前：

- `moox.toml` 的 `[storage_view]` 删掉 `capacity_check_interval`、`capacity_check_jitter`（严格解码，留着会报错）。
- `storage-policy.json` 去掉 `trim_bars`、`capacity_check_interval`、`capacity_check_jitter`（严格解码）；线上文件要跟新二进制一起换，否则 storage-view 起不来。
- `config/storage.yaml`、`storage_view/trpc_go.yaml` 随包更新，去掉 `rebuild_max_pending`、`rebuild_idle_checks`（严格解码）。
- 服务目录有变化，要整体发布（`catalog-changed` 会提示）。

步骤：

1. 构建发布包：storage、collector、factor、strategy、admin、monitor、access、cli、web（proto 变了，用到 storagegen 的都要换）。
2. 停 Storage 单元全部组件（`storage-view`、`storage-node`、`storage-primary`）。
3. 备份元数据库 `storage-primary/var/storage/metadata/storage_metadata.db`。
4. 对元数据库执行下面的 SQL（主机没有 sqlite3，用 python3 的 sqlite3 模块；需要 SQLite ≥ 3.35）。
5. 删除 View 索引与进度文件：`storage-view/var/storage/view-indexes/` 下的 `duckdb/*`、`bleve/*`、`view-data-ready/*`。
6. 换二进制、配置和 `storage-policy.json`，启动 Storage，发布其余单元。
7. 观察：`t_views.c_active_index_id` 全部非空（空值表示还在建）；上次 19 个 View 约 26 分钟（当时是 A/B 回填，首次建索引耗时相近）。这段时间策略读 View 返回未就绪：读取重试，超过 `evaluation.attempt_budget` 后记 `skipped(infra_retry_exhausted)`，超过有效期的周期记 `expired`，都不会补算，也就不会下单。选一个能接受停算的时段，并提前确认持仓风险。#85 的重投路径只管模块未就绪，不管这种情况。
8. 验证：查询恢复、`moox_storage_view_oldest_delete_last_round_timestamp_seconds` 在推进、加一列能在一分钟内生效、线上写入错误为 0。

一次性 SQL（已在 v15 空库加样例数据上验证，新代码能打开并进入首次建索引）：

```sql
BEGIN IMMEDIATE;
DROP TABLE t_view_index_builds;
DROP TABLE t_view_rebuild_logs;
DROP INDEX idx_t_views_revision_pending;
ALTER TABLE t_views DROP COLUMN c_desired_view_revision;
ALTER TABLE t_views DROP COLUMN c_active_slot;
ALTER TABLE t_views RENAME COLUMN c_active_view_revision TO c_view_revision;
UPDATE t_views SET
    c_active_index_id = '',
    c_view_revision = 0,
    c_active_columns_json = '[]',
    c_active_view_schema_hash = '',
    c_indexed_from = '',
    c_indexed_to = '',
    c_attrs_json = json_remove(c_attrs_json,
        '$.active_index_id', '$.desired_view_revision', '$.active_view_revision', '$.active_slot',
        '$.active_columns', '$.active_view_schema_hash', '$.indexed_from', '$.indexed_to', '$.index_build',
        '$.attributes."moox.active_build_id"', '$.attributes."moox.manual_rebuild_revision"',
        '$.attributes.rebuild_source', '$.attributes."moox.active_dataset_id"',
        '$.attributes."moox.active_dataset_ids"', '$.attributes."moox.active_primary_dataset_id"');
UPDATE t_schema_meta SET c_value = '16' WHERE c_key = 'schema_version';
COMMIT;
```

线上核对（2026-10-30）：19 个 View 全部是 DuckDB、都有频率，没有记录型 View，全量从 Primary 重建不丢数据；存储机 python3 自带 SQLite 3.46，支持上面的 SQL；`storage-policy.json` 在 `storage-primary`、`storage-node`、`storage-view` 三个目录的 `config/` 下各有一份，要一起换。

审阅后补充的取舍：加列在持运行时锁和写锁之后重读元数据再算目标（维护循环和定时任务各拿自己的快照，用过期快照会让物理索引和元数据的哈希对不上）；`maintenanceReady` 删除，定时任务不再受单个 View 出错影响；新指标加进 `packages/report/monitored_metrics.go` 的上报清单。

第二轮审查的补充：`AttachActiveView` 在运行时锁内读当前契约；没能挂接的 View 继续占着数据集映射，行事件保持未确认（否则共用数据集时已挂接的 View 会把事件确认掉）；已有列的数据集列被禁用后，更新 View 只校验新列；加列在锁外读声明的列、锁内只用 `GetView` 重读 View；分区积压的 Pending、AckPending、OldestPendingAge 已无数据来源，从健康详情里删除。`--reset-view-indexes` 继续要求 `--deliver-policy=all`：时序 View 从 Primary 回填，但同数据集的其他 View 和记录型 View 要靠重放补齐，改成 `new` 会丢行。

用户确认（2026-10-30）：列删除只是逻辑上的，数据集列被禁用或所引用字段停用、删除时，从查询结果和 `ListViewColumns` 隐藏（`View.hidden_columns` 由元数据计算、不落库），字段管理展示删除状态；不考虑回滚，设计保持简洁。
