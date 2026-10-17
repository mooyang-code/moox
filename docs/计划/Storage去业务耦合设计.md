# Storage 去业务耦合设计

状态：设计稿 v3（2026-10-17），尚未编码。项目尚未上线，接口、事件、元数据直接修改，不考虑兼容。v2（吸收 Codex 评审）：`source` 只表示完成机制，消费方按绑定的数据集筛选；`write_owner` 可授权修改，补上 Monitor 系统数据集；默认 View 的删除也由业务模块负责；`commit_id` 限定单数据集并重做回执；加列期间保留消费屏障；数据集列来源改为「字段 / 自定义 / 属性 / 系统」；补上遗漏的耦合（数据源例外、删除授权、保留策略包）；发布 C 改为有状态切换。v3（吸收 Codex 第三轮评审）：列来源只留「字段 / 自定义 / 系统」，自定义列不带 `origin_id`、不受来源唯一约束；一次性迁移同时改写列定义 JSON 与 `write_owner`，强制校验不设开关；`ViewDataReady` 升到 v2 并删除 `source`；发布 C 改为「暂停采集 → 全部停机的静默点快照 → 迁移 → 先建新消费者再启动生产者」，回退即恢复静默点快照。
与 [Storage 读接口收敛设计](Storage读接口收敛设计.md) 互补：那份收敛读接口，本设计处理写入、周期、事件、View 和元数据中的业务概念。
实施排在 [执行计划](Storage读接口收敛与数据归档执行计划.md) 的发布 C。

## 0. 原则

1. **Storage 只提供通用能力**：空间、数据集、行、周期、视图、事件。它不认识因子、采集、K 线、某个市场或某个数据集。
2. **权限只看「写入方」**：数据集声明 `write_owner`（允许写入的 AppId 列表），写入与周期报告只校验调用方是否在其中，精确匹配，没有别名。
3. **业务语义由业务模块定义和解释**：Storage 对业务数据只透传（不透明的字节或属性），不做校验、不做分支。
4. **同一能力一套接口**：幂等写入一套，周期完成一套，就绪事件一套。
5. **业务模块自己维护自己的资源**：默认 View、列、属性由创建数据集的模块通过 Metadata 接口维护，Storage 不替任何模块「托管」。

## 1. 现状盘点

### 1.1 身份与权限

| 位置 | 现在 | 问题 |
| --- | --- | --- |
| `primarystore/service.go` `isFactorAppID`、`isOwnedAppID` | `factor` 与 `moox-factor` 互为别名；`moox-<owner>` 视同 `<owner>` | 因子特例；别名规则让同一个身份有多个名字 |
| `primarystore/service.go` `validateDatasetWriteOwner` | `dataset_role = factor_result` 的数据集只许因子写；采集身份只能写 `collectorWritableDataset` 认可的数据集 | 写入权限按业务角色分支 |
| `primarystore/service.go` `collectorWritableDataset` | `owner_module = collector` 且 `dataset_role ∈ {raw_collection, kline_resample_result}` | 同上 |
| `primarystore/period.go` `validateCollectorPeriodDataset`、`rejectCollectorPeriodWriteCredential` | 周期账本只许采集身份、只许 `raw_collection` 数据集；写死拒绝 `scf-market-canary` | 账本是通用能力，却只给采集用；只读身份靠写死的名字拦截 |
| `primarystore/marker.go` `ownedDatasetNodeID`、`validateMarkerCaller` | 采集标记要求 `collector` + 两种角色；因子标记要求 `factor` + `factor_result` + `write_owner = factor` | 两套周期标记各有一套业务校验 |

### 1.2 写入

| 位置 | 现在 | 问题 |
| --- | --- | --- |
| `PrimaryStore.WriteFactorRows` → `DataNode.WriteFactorRows`（`pebble/factor_rows.go`） | 带 `commit_id` 的幂等批量写入：同一提交内容相同返回成功、不同返回冲突；一次写入只占一个 outbox 位置；事件带 `write_kind = factor_result` | 本质是通用的幂等写入，却只给因子用，名字也是因子的 |
| `UpsertFields`、`CommitTimeSeriesBatch` 的 `source_event_id` | 另一套去重：按来源事件 ID 记标记，保留 7 天 | 两套幂等机制并存 |
| `DatasetRowsUpserted.write_kind` | 只有因子写入会带，没有消费方读取 | 无用字段 |
| `primarystore/service.go` `normalizeStockCNSeriesTags` | 写死 `stockcn` 空间、`dataset_stockcn_equity_kline_1m` 数据集，把空的 `series_tag` 改写成默认值 | Storage 在改写某个业务数据集的数据 |

### 1.3 周期与事件

| 位置 | 现在 | 问题 |
| --- | --- | --- |
| 周期账本：`RegisterSeriesSnapshot`、`CommitTimeSeriesBatch`，DataNode 按快照与截止时间判定 complete / degraded | 判定结果发 `CollectorPeriodCompleted`（`collector.period.completed`），字段有 `batch_id`、`config_snapshot_id`、`collected_at` | 通用能力，事件却以采集命名 |
| `PrimaryStore.ReportCollectorPeriodCompleted` | 采集的本地重采样直接报告某周期完成 | 与因子的周期报告是同一件事 |
| `PrimaryStore.ReportFactorPeriodComputed` / `GetFactorPeriodComputed` | 因子报告某周期算完；事件 `FactorPeriodComputed`（`dataset.factor_period.computed`）带 `source_dataset_id`、`trigger_event_id`、`factors[]`（每个因子的状态、`source_hash`、`definition_hash`、`gap_subjects`） | 因子内部状态进入了 Storage 协议；`packages/events` 还替因子校验这些状态 |
| `ViewDataReady` | `completion_kind`（collector / factor）、`factors[]` | View 就绪事件携带因子结构；Strategy 按 `completion_kind` 区分触发来源 |

### 1.4 View

| 位置 | 现在 | 问题 |
| --- | --- | --- |
| `catalog/factor_result_view.go`、`metadata/sqlite/crud_dataset.go` | 因子结果数据集激活时，Storage 自动建并「托管」默认 View（`managed_by = storage`、`view_role = factor_result`）；加列前要求默认 View 已激活 | Storage 替因子模块维护资源；Collector 自己建 View，两种做法并存 |
| `view/maintenance_schema_extension.go` | 只有 `primary_dataset_role = factor_result` 的 View 才能在数据集加列后原地扩展 | 通用能力只给因子用 |
| `view/event_apply.go` `factorAttributeKeys` | 写死 `factor.source_hash` 等属性键，并按列属性 `origin_factor_id` 拼出 `factor.source_hash.<因子>` | View 认识因子的属性命名；而且这些键没有写入方（因子结果写入拒绝行属性）。发布 A 已随读接口收敛删除（收敛设计 2.4），本设计不再处理 |
| `view/inventory_reconciler.go`、`config/loader.go`、`eventconsumer/delivery_policy.go` | 数据集 ID 以 `dataset_factor_` 开头的走「factor」消费分区（单并发、`max_ack_pending = 1`）；配置校验写死三个分区名 | 按数据集 ID 前缀决定投递方式 |

### 1.5 元数据

| 位置 | 现在 | 问题 |
| --- | --- | --- |
| `catalog/validate.go` | `dataset_role` 取值校验（`merged_factor` 已退役提示、`factor_result` 必须以 `dataset_factor_` 开头）；列属性 `factor_output` 必须与显示名一致 | 元数据校验按业务角色分支 |
| 数据集属性 `dataset_role`、`owner_module`、`write_owner`、`source_dataset_id`；View 属性 `view_role`、`managed_by`、`primary_dataset_role` | Storage 代码中按这些属性分支约 140 处 | 业务属性被当成 Storage 的语义 |

### 1.6 其他

| 位置 | 现在 | 问题 |
| --- | --- | --- |
| `catalog/metadata_catalog.go:130` `CreateDataset` | 只有采集所有的数据集可以不填 `data_source_id` | 按业务角色给例外 |
| `catalog/metadata_catalog.go:205` `DeleteDataset` | 删除权限按因子结果、采集数据集分别校验 | 同上 |
| `metadata/dataset_attributes.go` | `dataset_role`、`owner_module`、`write_owner`、`source_dataset_id`、`collector_task_id`、`resample_task_id` 不可修改（从空值补上也不行） | 业务属性被 Storage 锁死，业务模块无法调整自己的属性，`write_owner` 也无法从旧值迁移 |
| `common.proto` `DatasetColumnOriginType` | 列来源为字段、**因子**或系统列；SQL 与校验中有对应分支 | 因子是一种列来源 |
| `packages/storagepolicy/policy.go:126`、`:188` | 内置 stockcn、stockhk、stockus 的默认保留期；按市场每天交易分钟数估算根数并参与校验 | 共享策略包里有具体市场 |
| `config/setup/metadata.yaml:311` 起 | 五个系统数据集（Monitor 写入）没有 `write_owner` | 收紧写入权限后会写不进去 |
| `observability/view_metrics.go`、`cmd/cli` | 部分标签以 collector 命名；有按因子结果数据集分支的维护逻辑 | 业务命名 |

## 2. 通用模型

### 2.1 写入方

- 数据集属性 `write_owner`：允许写入的 AppId 列表（逗号分隔），由创建数据集的模块填写。例如采集结果为 `moox-collector`，因子结果为 `moox-factor`，
  Monitor 写入的系统数据集为 `moox-monitor`（`config/setup/metadata.yaml` 同步补上）。
- `write_owner` 可以修改，但只允许当前 `write_owner` 中的调用方或全局管理员修改；删除 `metadata/dataset_attributes.go` 对业务属性
  （`dataset_role`、`owner_module`、`source_dataset_id`、`collector_task_id`、`resample_task_id`）的不可变约束，它们由业务模块自己维护。
- 已有数据集的 `write_owner` 由 2.5 的一次性迁移按 `owner_module` 规范（`factor` → `moox-factor`、`collector` → `moox-collector`、
  `monitor` → `moox-monitor`）：现在因子数据集存的是 `factor`，采集与 Monitor 的数据集没有这个属性，按上一条的授权规则业务模块自己改不了。
  迁移之后新版本从启动起就强制校验，不设临时放开的开关。
- 删除数据集、删除行、恢复行同样要求调用方在 `write_owner` 中（或全局管理员），删除 `DeleteDataset` 中按因子、采集分别授权的分支；
  `data_source_id` 对所有数据集都可选，填写时校验引用存在。
- 以下操作要求调用方 AppId 在 `write_owner` 中：`UpsertFields`、`CommitTimeSeriesBatch`、`RegisterSeriesSnapshot`、`ReportDatasetPeriodCompleted`、`DeleteDatasetRows`、
  `RestoreDatasetRows`。读取不受限制。
- 每个模块只用一个 AppId，删除所有别名规则。只读身份（如 SCF 行情巡检）不在任何数据集的 `write_owner` 里，自然不能写，不再写死名字。
- `dataset_role`、`owner_module`、`source_dataset_id` 等属性仍可由业务模块写入，但只供业务模块自己使用，Storage 不读取、不校验。

### 2.2 幂等写入

- `UpsertFields` 收窄为**一次只写一个数据集**（请求顶层带 `space_id`、`dataset_id`，行的数据集必须与之一致），不再跨数据集拆写、部分成功；
  调用方按数据集拆分请求。
- `UpsertFields` 与 `CommitTimeSeriesBatch` 增加 `commit_id`（可选）：回执按 `(space_id, dataset_id, commit_id)` 定位，指纹包含操作类型与完整载荷；
  内容相同返回成功、内容不同返回冲突；写入、回执与 outbox 在同一个 Pebble 批次里提交，一次写入只占一个 outbox 位置。
- 回执记录写入时间，保留 7 天后清理（复用现有过期清理的调度）。现有的提交回执没有数据集作用域、也没有过期时间，按上面的规则重做，不直接沿用。
- 删除 `WriteFactorRows`（两层）、`source_event_id` 与「已处理来源事件」标记、`DatasetRowsUpserted.write_kind`。
- 因子按期写结果改为 `UpsertFields(commit_id = <期的提交 ID>)`。

### 2.3 周期完成

一个周期完成有两种来源，统一成一种事件、一套接口：

| 来源 | 谁判定 | 接口 |
| --- | --- | --- |
| `ledger` | DataNode 按登记的序列快照与截止时间判定（现在的采集周期账本） | `RegisterSeriesSnapshot`、`CommitTimeSeriesBatch`（不变） |
| `producer` | 写入方自己报告（现在的重采样与因子） | `ReportDatasetPeriodCompleted`；查询用 `GetDatasetPeriodCompleted` |

`source` 只描述「这一期是怎么判定完成的」，**不是**业务分类：重采样结果属于 `producer`，但下游对它的处理与原始采集一样。
消费方按自己绑定的数据集（及频率）筛选完成事件、两种 `source` 都接受；需要业务明细时按 `detail_type` 判断能否解码。

事件 `DatasetPeriodCompleted`（主题 `moox.event.storage.dataset.period.completed.v1.<空间>.<数据集>`）：

| 字段 | 说明 |
| --- | --- |
| `dataset_id`、`frequency`、`period_time` | 哪个数据集的哪一期 |
| `status` | `complete` / `degraded` |
| `source` | `ledger` / `producer` |
| `universe_subject_ids`、`failed_subjects` | 本期应有的标的与失败的标的 |
| `committed_positions` | 本期写入在 outbox 中的位置，View 用来确认数据已进入索引 |
| `snapshot_ref` | `ledger` 来源时为登记的序列快照引用（替代 `batch_id`、`config_snapshot_id`、`expected_scope_ref`） |
| `cause_event_id` | `producer` 来源时为触发本次计算的事件 ID（替代 `trigger_event_id`），用于幂等查询 |
| `completed_at` | 完成时间 |
| `detail_type`、`detail` | 写入方自定义的明细（不透明字节），Storage 原样保存并透传，不解析 |

- 删除 `CollectorPeriodCompleted`、`FactorPeriodComputed` 两种事件，`ReportCollectorPeriodCompleted`、`ReportFactorPeriodComputed`、`GetFactorPeriodComputed`
  三个接口，以及 DataNode 的 `AppendCollectorPeriodCompleted`、`AppendFactorPeriodComputed`、`GetFactorPeriodComputedMarker`（合并为
  `AppendDatasetPeriodCompleted`、`GetDatasetPeriodCompleted`）。
- 因子把每个因子的状态（现在的 `FactorPeriodState`）放进 `detail`，`detail_type` 为 `moox.factor.PeriodDetail.v1`。这个 proto 移到因子自己的包
  （新建 `packages/factorpb`），由因子写入、Strategy 读取；`packages/events` 中针对因子状态的校验一并移过去。

### 2.4 View 就绪事件

`ViewDataReady` 只描述「这一期的数据已进入这个 View」：

- 载荷不兼容，事件版本升到 v2（主题随之变为 `moox.event.storage.view.data.ready.v2.…`，见第 6 节）；
- 删除 `completion_kind`：消费方不按来源区分（见下），事件里不需要来源；
- `factors[]` 改为原样透传周期完成事件的 `detail_type` 与 `detail`；
- 其余字段（View、数据集、期、状态、可见范围、失败范围、写入位置、就绪时间、`universe_subject_ids`）不变。
- Strategy 不再用来源区分触发：它按绑定的 View 与数据集判断事件是否相关，`detail_type` 为 `moox.factor.PeriodDetail.v1` 时解码因子状态。
  现在「按数据集角色决定只接受哪种完成事件」的逻辑（`strategy/internal/input` 的 `TestResolveCompletionKindByDatasetRole`）随之删除。

### 2.5 View 的资源与投递

- **默认 View 由业务模块自己建、自己删**：删除 `catalog/factor_result_view.go`、数据集激活与加列时对默认 View 的检查，以及删除数据集时自动删除
  托管默认 View 的逻辑（`metadata/sqlite/crud_dataset.go:189`）。factor-mgr 与 Collector 一样，通过 `CreateView`、`UpsertViewColumn`、`DeleteView`
  维护自己的 View 的完整生命周期。删除因子结果数据集的顺序（可重试）：确认没有默认 View 以外的引用 → 删除默认 View → 物理清理行 → 删除数据集；
  否则数据已清空而元数据被 `t_views` 外键（`RESTRICT`）挡住删不掉。
- **数据集列的来源统一为「字段 / 自定义 / 系统」**：
  - `FIELD`（字段）：引用 Space 字段目录，`origin_id` 为字段 ID，不变；同一数据集内同一字段只能出现一次；
  - `CUSTOM`（自定义，由 `FACTOR` 改名）：数据集自己定义的值列，值与字段列一样按列名存取，列的身份就是列名；**不带 `origin_id`**。
    生产者信息由写入方放在列属性里（factor-mgr 已有 `origin_factor_id`、`factor_output`），Storage 不解释。现在一个因子的多个输出列都以因子 ID 为
    `origin_id`，撞上 `UNIQUE (c_space_id, c_dataset_id, c_origin_type, c_origin_id)`（`schema/metadata.sql:630`），多输出因子建不出第二列；
    改为只对字段列建部分唯一索引（`WHERE c_origin_type = 'field'`）。原来 `UpsertDatasetColumn` 对因子输出列的校验与显示名特例删除；
  - `SYSTEM`（系统），不变。

  不增加「属性」来源：行属性是写入方自己的来源信息，没有按列读取的需求（因子行属性本来就不存在，见 1.4）。SQL 与校验中的因子分支一并删除。
- **一次性元数据迁移（v13 → v14）**：`t_dataset_columns` 的 `CHECK (c_origin_type IN ('field', 'factor', 'system'))` 要改成 `('field', 'custom', 'system')`，
  SQLite 改不了 `CHECK`、也删不了表级 `UNIQUE`，只能重建表；schema 版本号升到 v14。迁移命令（`moox-storage-cli migrate-metadata-v14`）在一个事务里：
  - 重建 `t_dataset_columns`（新的 `CHECK` 与部分唯一索引，索引与触发器照新建库重建）；
  - `c_origin_type = 'factor'` 的行改为 `custom`、`c_origin_id` 清空，**同时改写 `c_attrs_json`**：列定义整条以 protojson 存在这一列里，枚举按名字序列化
    （`DATASET_COLUMN_ORIGIN_TYPE_FACTOR`），读取时 `DiscardUnknown` 会把认不出的枚举名静默丢成「未指定」（`metadata/sqlite/crud_helpers.go:37`）；
  - 按 2.1 规范已有数据集的 `write_owner`；
  - 写入版本号 14。失败时整体回滚，库保持 v13。

  命令只为这次发布存在，在 storage-primary 停机时执行，发布完成后删除；不需要重置元数据库。
- **View 跟随数据集加列**：View 属性 `follow_dataset_columns = true` 时，数据集新增列后原地扩展 View 的列（复用现有的 schema 扩展机制）：
  - 数据集加列在同一个元数据事务里，更新所有跟随它的 View 的列投影与期望版本；
  - 消费屏障保留并通用化：事件里出现活动 schema 还没有的列时，暂停该 View 的消费、等待扩展完成（现在只对因子结果 View 生效，见
    `view/event_apply.go` 的 `validateFactorResultEventColumns`），否则新列会被普通应用路径静默过滤；
  - 扩展提交条件（`metadata/sqlite/crud_view_index.go:279`）中的 `primary_dataset_role = factor_result` 改为 `follow_dataset_columns = true`；
  - 删除 `view/maintenance_schema_extension.go` 中对 `primary_dataset_role` 的判断。
- **投递方式由数据集声明**：数据集属性 `view_delivery`：`parallel`（默认）或 `serial`。`serial` 使用单并发、`max_ack_pending = 1` 的消费模板，
  适合事件大、要求严格串行的数据集。删除按 `dataset_factor_` 前缀分配消费分区的逻辑；配置中的分区模板改为 `parallel`、`serial`、`metrics` 三个，
  名称与业务无关。factor-mgr 为因子结果数据集设置 `serial`。

### 2.6 元数据校验

- 删除 `dataset_role` 取值校验、`dataset_factor_` 前缀规则、`factor_output` 显示名规则。
- `write_owner` 必须非空；`view_delivery`、`follow_dataset_columns` 的取值校验保留在 Storage（它们是 Storage 自己的语义）。

### 2.7 写入方负责的数据规范

`normalizeStockCNSeriesTags` 移到 Collector：A 股 1m 写入时由 Collector（SCF 与补采）自己填写默认 `series_tag`。Storage 只校验 `series_tag` 的格式。

### 2.8 保留策略包

`packages/storagepolicy` 只保留通用规则（按空间、频率解析保留期，全局默认覆盖全部频率）：

- 各市场的默认保留期从 `Default()` 移到部署配置（`moox.toml` 的 `[storage_retention.spaces.*]` 与 `moox.toml.example`），代码里只保留全天交易的全局默认；
- 删除按市场交易分钟数估算根数、并据此校验保留期的逻辑（`policy.go:188` 一带）：保留期够不够由部署配置的人负责，Storage 不做市场相关的推算。

## 3. 上层模块的改动

| 模块 | 改动 |
| --- | --- |
| factor-mgr | 创建因子结果数据集时设置 `write_owner = moox-factor`、`view_delivery = serial`；因子输出列的来源为 `CUSTOM`、不带 `origin_id`（因子 ID 留在列属性 `origin_factor_id`）；自己建、改、删默认 View（`follow_dataset_columns = true`）；删除因子结果数据集按 2.5 的顺序执行；启动对账时为已有数据集补齐 `view_delivery` 与 View |
| 因子引擎 | 写结果改为 `UpsertFields(commit_id)`；报告改为 `ReportDatasetPeriodCompleted`（`detail` 为 `factorpb.PeriodDetail`）；幂等查询改为 `GetDatasetPeriodCompleted`；触发改为订阅 `DatasetPeriodCompleted`（新的 durable 名），按因子集绑定的源数据集与频率筛选，`ledger`、`producer` 两种来源都接受（重采样结果是 `producer`）|
| Collector | 数据集设置 `write_owner = moox-collector`；`UpsertFields` 按数据集拆分请求；重采样的周期报告改为 `ReportDatasetPeriodCompleted`；A 股默认 `series_tag` 在写入方填写；去重改用 `commit_id` |
| Strategy | 订阅 `ViewDataReady` v2（新的 durable 名）；按绑定的 View 与数据集判断就绪事件是否相关，不再按来源区分；`detail_type` 为因子明细时解码（依赖 `packages/factorpb`）|
| Monitor | 主机指标写入的去重改用 `commit_id`；系统数据集的 `write_owner` 为 `moox-monitor`（已有库由迁移补上，新建库来自 `config/setup/metadata.yaml`） |
| CLI | 列来源的解析、元数据导入导出与初始化预检（`metadata_implementation.go:884`、`setup_init.go:523`）改为 `FIELD / CUSTOM / SYSTEM` |
| 前端 | `api/storage/types.ts` 与用到列来源的页面（因子结果、View 浏览、策略输入表）改为 `CUSTOM` |
| packages | `packages/events`：事件注册表换成 `DatasetPeriodCompleted`，删除因子状态校验；新建 `packages/factorpb`；`packages/storagepb` 同步 |
| 服务目录 | PrimaryStore 方法改名与删除；`factor-engine`、`scf-collector` 等外部身份的白名单同步 |

## 4. 数据处理

项目尚未上线，不写长期的兼容或迁移层；唯一的例外是 2.5 中重建 `t_dataset_columns` 的一次性迁移命令，发布完成后删除：

- 已有数据集的 `write_owner` 与因子输出列由 2.5 的迁移命令处理；`view_delivery` 由 factor-mgr 启动对账时补齐；
- Storage 托管的因子默认 View 改由 factor-mgr 接管：对账时把 View 属性 `managed_by` 改为 `factor-mgr`、补上 `follow_dataset_columns`；
- EventBus 中残留的旧事件（两种周期完成事件）在发布时清除对应主题，View 消费者按新事件重建（参照 `reset-view-consumers` 的流程，
  必要时 `repair-view`）。

## 5. 测试

- 写入权限：只有 `write_owner` 中的 AppId 能写；不再有别名；只读身份写入被拒；`write_owner` 只能由当前所有者或全局管理员修改；
  全新初始化（`metadata.yaml`）与已有数据集（迁移命令，`write_owner` 为旧值 `factor` 与为空两种）都得到正确的所有者。
- 幂等写入：同一 `commit_id` 相同内容成功、不同内容冲突；跨数据集同一 `commit_id` 互不影响；`UpsertFields` 与 `CommitTimeSeriesBatch` 用同一 `commit_id` 时按冲突处理；
  一次写入一个 outbox 位置；回执过期后可以再次写入；`UpsertFields` 拒绝跨数据集的请求。
- 删除：只有默认 View 时，因子结果数据集能完整删除；存在其他 View 时拒绝删除且事实保持完整；中途失败可以重试。
- 加列：加列后立刻写入新列、扩展还没完成时，新列不会被丢弃，也不会提前发出就绪事件。
- 重采样：重采样结果（`producer`）→ 因子按期计算 → Strategy 触发，端到端通过。
- 周期：`ledger` 判定与现在的账本行为一致；`producer` 报告与查询一致；`detail` 原样透传到 `ViewDataReady`。
- 列来源：多输出因子的各列都能建立；多个自定义列互不冲突；同一字段在一个数据集里仍只能出现一次；`CUSTOM` 经 CLI 导出后重新导入一致。
- View：`follow_dataset_columns` 的原地扩展；`serial` / `parallel` 投递按数据集属性生效。
- 端到端：采集 → 账本完成 → 因子触发 → 因子写结果与报告 → View 就绪 → Strategy 触发，全链路通过。
- 元数据迁移：v13 的库迁移到 v14 后，表、索引、触发器与按 v14 新建的库一致；原来的 `factor` 列变为 `custom`、`origin_id` 为空，新版 `Get/ListDatasetColumns`
  读出的来源正确，再次更新这些列成功；`write_owner` 按 `owner_module` 规范；迁移中途失败时库保持 v13，可以重跑。
- 切换演练（隔离环境）：静默期内 Storage 写入量与事件量为零；新版本启动后不会收到旧格式的事件；回退后旧版本不会收到新格式的事件，
  静默点之后的周期由旧版本重新完成。

## 6. 发布

协议、事件、元数据都有变化，而且状态不只在 EventBus 里：DataNode 的周期账本、标记与 outbox，View 落盘的待发布就绪队列都持久化了原始事件；
SCF 定时器不经过 Collector、直接写 Storage（`collector/internal/marketfetch/plan_runtime.go:28`），DataNode 没有写入时也会按截止时间定稿周期并发事件
（`pebble/period_progress.go:602`）。所以发布 C 不做在线切换，而是在一个**全部停机的静默点**上切换，回退就是回到这个静默点。

**事件按版本隔离**：事件的主题带版本（`moox.<事件名>.v<版本>.<空间>.<数据集>`，`packages/events/registry.go:205`），解码时名称或版本不符直接拒绝。
发布 C 中载荷不兼容的事件都换成新主题：周期完成是新事件 `DatasetPeriodCompleted` v1，`ViewDataReady` 升到 v2；因子引擎和 Strategy 为新主题使用新的
durable 名，旧 durable 不动。这样新版本收不到旧格式的消息，回退后的旧版本也收不到新格式的消息，两个方向都不需要清除主题；
`MOOX_STORAGE` 流按 72 小时保留（`limits`），旧消息自然过期。View 的消费者照常用 `reset-view-consumers` 重建。

步骤：

1. **暂停采集**：`moox-cli collector fetch-plan pause`（发布 A 中加入，复用空间退役时停用定时器的 `DisableAll`，以云端读回确认全部定时器停用、在途调用结束；
   暂停状态存在 Collector 库里，重启后保持），对每个有采集计划的空间执行；
2. **停写、排空**：停 Collector、因子引擎、Strategy、Monitor；等 DataNode outbox 排空、各 View 水位追平、待发布就绪队列为空、各消费者没有积压；
   然后停 Storage 三个角色。此刻为静默点；
3. **快照**：复制 Storage 数据目录（元数据库、Pebble、View 索引；停机时复制是一致的，Pebble 的 SST 文件不可变，可以用硬链接，其余文件普通复制），
   以及 Collector、factor-mgr、Strategy 的库；
4. **迁移**：新版 `moox-storage-cli migrate-metadata-v14`（2.5）；
5. **先建消费者**：启动新版本的因子引擎与 Strategy，确认它们在新主题上的 durable 已建好（它们读 Storage 失败会退避重试，不影响建消费者）；
6. **启动生产者**：storage-node → storage-primary → 至少等 30 秒 → storage-view；Admin → 各主机 host-gateway → Monitor → Access；`reset-view-consumers` 重建 View 消费者；
   factor-mgr；Collector（滚动升级 SCF 代码包）；
7. **恢复采集**：确认全部 SCF 函数已是新代码包后，`moox-cli collector fetch-plan resume`；自动补采补上暂停期间的缺口。

回退：同样暂停采集、停全部组件，换回旧二进制，恢复第 3 步的快照；用旧版 CLI `reset-view-consumers` 重建 View 消费者；旧的因子引擎、Strategy 沿用旧 durable，
从静默点的位置继续，只收旧主题。静默点之后的周期由旧版本重新完成；这段时间写入的数据丢弃，由自动补采补回。
发布 C 稳定后，在 EventBus 主机上删除因子引擎、Strategy 的旧 durable（一次性运维）。

时间选在周末（股票市场都休市），暂停期间只有 crypto 留下需要补采的缺口。

## 7. 待确认

- `write_owner` 是否用网关验证过的调用方身份取代请求体里的 AppId：现在 Storage 的角色认证是「AppId + Primary 共享密钥的 HMAC」，
  持有密钥的人可以声明任何 AppId，因此 `write_owner` 只能防误写、不能防伪造。改用网关身份更可靠，但涉及 Access 转发时如何传递原始调用方，
  本设计先不做，单独评估。
- `view_delivery = serial` 是否还有必要：如果把「单个事件的大小上限」作为通用的消费参数，因子结果也许可以走默认投递，届时这个属性可以删除。
