# Storage 去业务耦合设计

状态：设计 v22（2026-10-27），未编码。v22：第 5 步补 Collector 两个新只读接口的服务目录声明与 ACL。v21：按 Codex 审查补全 storage-view 直连 Metadata 的验签范围（含周期状态与同步点）与启动时 `ListViews` 的签名密钥。v20：按用户决定 storage-view 调 Metadata 改为直连、走服务签名，网关 ACL 删除它访问 Storage 的规则，只保留调 Admin 走网关（3.1）。v19：按 Codex 审查补 factor-mgr 删 `write_owner`、storage-view 直连 PrimaryStore 写同步点不经网关。v18：按用户决定删除 `write_owner`，写入权限只靠网关 ACL（读写分开、写方法按实际调用放行），不再统一 AppId（3.1）。v17：按 Codex 复审落实三处代码衔接——`write_owner` 检查放在有调用方身份的 Catalog 层、单模板要改的三处旧拓扑校验、重采样触发器入参换新事件类型。v16：按 Codex 全文审查修正 `write_owner` 经 `CreateDataset` 绕过、单一消费模板的启动与路由发现、重采样归入 `ledger` 并换装其触发消费者。v15：按 Codex 复审补 Web 标签表单的选项来源（Collector `ListSubjectListers`）与属性维护任务的属性键。v14：按 Codex 审查 v13 补齐第 5 步——标签新增属性存储、运行状态经 Collector 接口供 Monitor 与 Web、修订号与成员失效时间、删除过滤与 `subject_listing` 全链路、序列身份论据更正。v13：按讨论细化 3.6——标的同步配置写进标签属性、只有运行状态回到 Collector 的库，标的与数据来源的业务字段改为属性，序列身份只用两项。v12：View 的 durable 改名为 `view_<数据集哈希>`；全量复查 Storage 后新增两处耦合（标的、标签与数据来源目录；周期账本的序列身份），列为编码第 5 步（2.6、2.7、3.6）。v11：按用户决定：View 不再为新列阻塞消费（新列在扩展完成前直接裁掉，作为系统边界说明），消费只保留一种模板和全局并发配置（3.4）。v10：按 Codex 审查 v9 修订：全新部署改用《部署与运维》的全新安装流程，补齐各步的改动清单。

- v9：用户决定列来源 `FACTOR` 改名为 `CUSTOM`，并接受**全部删除、全新部署**。于是四步只作为编码顺序，最后一次性全新部署（第 4 节）；v7、v8 为分步在线发布准备的过渡（属性缺省推断、消费者从流末尾重建、旧完成标记不转换、模块间换装顺序）全部删去。
- v7、v8：按 Codex 三轮审查修订（见第 7 节）。
- v6：按当前代码重写，删去已经完成的部分（第 1 节）；按用户原则（单用户；不过度设计；不写迁移命令、不设计回退）删去 v5 的迁移命令、静默点快照与回退、新旧事件版本并存。

v1～v5 的审查记录见文末。与 [Storage 读接口收敛设计](Storage读接口收敛设计.md) 互补：那份收敛读接口，本设计处理写入权限、周期、事件、View 和元数据中的业务概念。

## 0. 原则

1. **Storage 只提供通用能力**：空间、数据集、行、周期、视图、事件。它不认识因子、采集、K 线、某个市场或某个数据集。
2. **写入权限只在网关**：哪个模块能调哪些写方法，由服务目录的网关 ACL 决定；Storage 不再判断「谁能写哪个数据集」。
3. **业务语义由业务模块定义和解释**：Storage 对业务数据只透传（不透明的字节或属性），不做校验、不做分支。
4. **同一能力一套接口**：幂等写入一套，周期完成一套，就绪事件一套。
5. **业务模块自己维护自己的资源**：默认 View、列、属性由创建数据集的模块通过 Metadata 接口维护，Storage 不替任何模块「托管」。

## 1. 已经完成（不再处理）

| 项 | 完成情况 |
|---|---|
| 幂等写入只有一套 | `WriteFactorRows` 已删除，因子改用 `UpsertFields(commit_id)`；`UpsertFields` 一次只写一个数据集；`source_event_id`（含 `CommitTimeSeriesBatch`）、`write_source`、`write_kind` 与来源事件去重已删除（[通用写入设计](通用写入取代WriteFactorRows设计.md)，2026-10-27 上线）。与 v5 的差别：commit 标记永久有效、只随删除数据集清理，`CommitTimeSeriesBatch` 不加 `commit_id`（周期账本本身保证幂等） |
| View 写死的因子属性键 | `factorAttributeKeys` 已随读接口收敛删除 |
| 多输出因子的列来源唯一约束 | 元数据 v14 已把来源唯一索引改为只约束非因子列（`schema/metadata.sql:635`） |

## 2. 现状：剩余耦合

### 2.1 写入权限

| 位置 | 现在 | 问题 |
|---|---|---|
| `primarystore/service.go:204` `isFactorAppID`、`:213` `isOwnedAppID` | `factor` 与 `moox-factor` 互为别名；`moox-<owner>` 视同 `<owner>` | 一个身份有多个名字 |
| `primarystore/service.go:222` `validateDatasetWriteOwner` | 因子结果按 `dataset_role`、`owner_module`、`write_owner` 三项判定只许因子写，且因子只能写自己的结果；采集身份只能写 `collectorWritableDataset` 认可的数据集 | 按业务角色分支 |
| `primarystore/period.go:150`、`:193` | 周期账本只许采集身份、只许 `raw_collection`；写死拒绝 `scf-market-canary`（`service.go:135` 同） | 账本是通用能力却只给采集用；只读身份靠写死名字拦截 |
| `primarystore/marker.go:24`、`:52`、`:157` | 采集完成标记要求 `collector` + 两种角色；因子完成标记要求 `factor` + `factor_result` | 两套周期标记各有一套业务校验 |
| `primarystore/dataset_admin.go:109` | 清空、恢复行按因子结果、采集数据集分别授权 | 同上 |
| `catalog/metadata_catalog.go:118`、`:209`、`:226` | 只有采集所有的数据集可以不填 `data_source_id`；删除数据集按因子（写死 `moox-factor`）、采集分别授权 | 同上 |
| `metadata/dataset_attributes.go:10`、`metadata/sqlite/crud_dataset.go:211` | `dataset_role`、`owner_module`、`write_owner`、`source_dataset_id` 等创建后不可修改，原本没有的值也加不上；因子结果的 `write_owner` 必须恰好是 `factor` | 业务属性被 Storage 锁死，业务模块无法维护自己的属性 |
| `catalog/activation.go:182`、`metadata/sqlite/crud_dataset.go:440` | 激活、保存数据集时同样按采集归属决定 `data_source_id` 是否必填 | 与创建时的例外是同一个分支，分散在三处 |
| 各写入方的 AppId | Monitor 写入用 `monitor`（`monitor/internal/hostmetrics/storage_writer.go:35`）；因子、采集的代码里 `factor` / `moox-factor`、`collector` / `moox-collector` 两种写法并存 | 靠别名规则才能对上 |

### 2.2 周期完成与事件

| 位置 | 现在 | 问题 |
|---|---|---|
| `ReportCollectorPeriodCompleted`、`ReportFactorPeriodComputed`、`GetFactorPeriodComputed`；DataNode `AppendCollectorPeriodCompleted`、`AppendFactorPeriodComputed`、`GetFactorPeriodComputedMarker` | 两种业务各一套报告与查询 | "某数据集这一期完成了"是同一件事 |
| 事件 `CollectorPeriodCompleted`（账本判定）、`FactorPeriodComputed` | 后者带 `factors[]`（状态、`definition_hash`、`member_key`、`gap_subjects`）；`packages/events/validation.go:109`、`:330` 替因子校验这些字段 | 因子内部状态进入 Storage 协议 |
| `ViewDataReady` | 带 `completion_kind`（采集 / 因子）与 `factors[]`；Strategy 按 `completion_kind` 只接受一种来源（`strategy/internal/input/resolve.go:207`、`trigger/handler.go:229`） | View 就绪事件携带因子结构 |

### 2.3 View

| 位置 | 现在 | 问题 |
|---|---|---|
| `catalog/factor_result_view.go`、`metadata/sqlite/crud_dataset.go:190`～`:250`、`:734`、`:773` | 因子结果数据集激活时 Storage 自动建并"托管"默认 View（`managed_by = storage`、`view_role = factor_result`）；加列、删除数据集时检查或删除托管 View | Storage 替因子维护资源；采集自己建 View，两种做法并存 |
| `view/maintenance_schema_extension.go:125`、`metadata/sqlite/crud_view_index.go:283`、`view/event_apply.go:444` | 数据集加列后原地扩展 View、事件出现新列时暂停消费，只对 `primary_dataset_role = factor_result` 生效 | 通用能力只给因子用 |
| `view/build.go:102`～`:115`、`:204`～`:245`、`:345` | 因子结果 View 重建时不复制活动索引、直接从主库历史重建，主库没有数据时允许先空着 | 重建策略按业务角色决定 |
| `view/maintenance.go:621` | 维护顺序按 `view_role`：`collection_browse` 最先、因子结果其次 | 同上 |
| `view/inventory_reconciler.go:305`、`config/loader.go:130`～`:210` | 数据集 ID 以 `dataset_factor_` 开头的走 `factor` 消费分区（单并发、`max_ack_pending = 1`）；默认分区配置写死 `mooxsys` 的四个主机指标数据集与 `crypto`、`stockcn`、`stockhk` 等空间 | 按数据集 ID 前缀决定投递方式；代码默认值里有具体空间和数据集 |

### 2.4 元数据

| 位置 | 现在 | 问题 |
|---|---|---|
| `catalog/validate.go:123`～`:152` | `dataset_role` 取值校验（`merged_factor` 已退役提示）、因子结果数据集 ID 必须以 `dataset_factor_` 开头 | 元数据校验按业务角色分支 |
| `catalog/factor_result_column.go`、`catalog/validate.go:168`～`:175`、`catalog/metadata_catalog.go:598` | 列来源 `FACTOR` 的列按 `column_kind`（`output` / `params`）校验 `factor_id`、`member_key`、`output_name`、列名拼接、`__params` 的类型；按这些属性识别「因子 View 列」 | 因子列的命名契约由 Storage 校验 |
| `catalog/validate.go:90`～`:120` | 列的显示名按「因子输出」与 `mooxsys` 空间分别决定，并校验因子列的填充值 | 同上 |
| `common.proto` `DatasetColumnOriginType` 与 `schema/metadata.sql:625` | 列来源为字段、**因子**、系统 | 因子是一种列来源 |
| `cmd/server/main.go:113` `ResultDatasetResolver` | 按 `dataset_role = factor_result` 与 `source_view_id` 找结果数据集 | 业务分支，而且已无调用方（死代码） |

### 2.5 写死的市场与数据集

| 位置 | 现在 |
|---|---|
| `primarystore/service.go` `normalizeStockCNSeriesTags` | 写死 `stockcn` 空间与 `dataset_stockcn_equity_kline_1m`，把空 `series_tag` 改写成 `default` |
| `packages/storagepolicy/policy.go:183`、`:245`、`:264` | 内置 stockcn、stockhk、stockus、`mooxsys` 的默认保留期，`mooxsys` 有专门豁免，并按每天交易分钟数估算根数参与校验；CLI `setup_retention.go:61` 用这个估算给出校验与提示 |

### 2.6 标的、标签与数据来源目录

| 位置 | 现在 | 问题 |
|---|---|---|
| `Tag`（`metadata.proto:167`、`schema/metadata.sql:285`）、`metadata/sqlite/crud_tag.go:30`、`:154` | 标签带 `source`、`market_type`（创建后不可改，取值写死为 `spot / swap / equity / etf / index / convertible_bond`）；带 `cron`、`timezone`、`last_run_at`、`last_status`、`last_error`；创建标签时按数据来源属性 `subject_listing` 校验来源 | 标签是"一组标的"，却装着采集的标的同步任务（何时跑、跑的结果、从哪个行情源拉什么品种） |
| `ReportTagRunFailure`、`ApplyTagSnapshot` 的 `run_at` | 采集的标的同步任务向 Storage 报告运行结果 | 采集任务的运行状态存在 Storage |
| `Subject`（`metadata.proto:199`、`schema/metadata.sql:252`） | 固定字段 `subject_type`（注释举例 stock、crypto_pair）、`market`、`currency`；`ListSubjects` 按 `market` 过滤 | 金融市场的概念成了 Storage 元数据的固定字段 |
| `DataSource`（`metadata.proto:141`、`schema/metadata.sql:221`～`:236`） | `kind` 的取值写死在 `CHECK`（exchange、vendor_api 等）；`market`；`config_json` 是"来源连接和采集配置" | 采集的连接配置存在 Storage |

### 2.7 周期账本的序列身份

| 位置 | 现在 | 问题 |
|---|---|---|
| `data_node.proto:31` `SeriesSnapshotEntry`、`packages/seriesset`、`datanode/period.go:26` | 登记序列快照时，每条序列的身份是 `provider`、`source_id`、`market_type`、`subject_id`、`series_tag` 五项，Storage 用五项重算 `series_hash` | 行的身份只有标的、频率、时间、`series_tag`；行情源、来源、品种是采集内部的维度，Storage 并不用它们定位行 |

另外复查过、不改的：`observability/view_subject_tracker.go:25` 的"标的落后容忍 5 分钟"注释写着与 Monitor 的 K 线规则对齐，但它本身是"落后一根之外再多 5 分钟"的通用阈值，只把注释改成通用表述；代码注释里提到 Collector、Monitor 等调用方的地方不算耦合。

## 3. 方案

### 3.1 写入权限只靠网关 ACL（编码第 1 步）

用户决定（2026-10-27）：删除数据集级的写入权限（原方案的 `write_owner`），写入权限只靠网关 ACL。

- **Storage 内不再有按调用方的写入判断**。删除：`validateDatasetWriteOwner`、`factorOwnedResult`、`collectorWritableDataset`、`datasetRowsOwner`（`primarystore/service.go:222` 起）；周期账本只许采集身份与 `raw_collection` 的检查（`primarystore/period.go:150`、`:193`）；完成标记按身份与数据集角色的检查（`primarystore/marker.go:24`、`:52`、`:157`、`:240`）；清空与恢复按因子、采集分别授权（`primarystore/dataset_admin.go:109`）；删除数据集按写死的 `moox-factor` 与采集分别授权（`catalog/metadata_catalog.go:20`、`:209`～`:260`）；写死拒绝 `scf-market-canary` 的两处（`primarystore/service.go:135`、`period.go:151`，代码与配置里已无这个身份）；别名规则 `isFactorAppID`、`isOwnedAppID`。
- 请求体里的 AppId 只用于服务签名（各服务的共享密钥 HMAC）与日志，不参与授权；因此不必统一各模块的 AppId 写法，`moox-factor`、`moox-collector` 照旧可用。只用于运维的入口（DataNode 登记、手工重建等，见 `metadata_data_node.go:40`、`metadata_space_view.go:215`）的身份校验不变，它们不是数据集写入权限。
- **网关 ACL 按实际调用收紧**（`packages/servicecatalog/catalog.yaml`）：现在 Metadata 服务把 `CreateDataset`、`UpdateDataset`、`DeleteDataset`、`UpsertTag` 等写方法与读方法放在同一条规则里，一起放行给 collector、factor-mgr、monitor、strategy、storage-view（`catalog.yaml:1431` 一带）。改为读、写分开：读方法照旧；写方法（Metadata 的建、改、删，PrimaryStore 的 `UpsertFields`、`RegisterSeriesSnapshot`、`CommitTimeSeriesBatch`、周期报告、`DeleteDatasetRows`、`RestoreDatasetRows`）只放行编码时按调用点核对出的模块。只读的 strategy 不放行任何写方法。运维身份（`moox-cli`、`console`）照旧，`AppendDatasetSyncPoint` 等 CLI 导入要用的写方法保留。
- **Storage 内部组件之间直连、走服务签名，不经网关 ACL**（用户决定，2026-10-27）：
  - storage-view 调 PrimaryStore 本来就直连（`cmd/server/main.go:353`，读历史、写路由就绪同步点 `AppendDatasetSyncPoint`），不动。
  - storage-view 调 Metadata（读数据集、列等元数据，回报索引构建状态）由经网关（`main.go:347` 的 `openStorageViewGateway`、`viewservice.NewGatewayMetadataClient`）改为直连 Metadata 端口，与调 PrimaryStore 一样带服务签名（`MOOX_STORAGE_VIEW_AUTH_SECRET`）；目标地址进 storage-view 配置，默认本机（storage-view 与 storage-primary 本来就同机部署）。Metadata 对 storage-view 实际调用的全部方法（`view/metadata_gateway.go` 里的全集：元数据读取、索引构建回报、重建日志、View 周期状态 `UpsertViewPeriodDatasetState`、同步点 `RecordViewSyncPoint` 等）统一按这个签名认证；现在只有索引回报方法校验它（`metadata_view_index.go:65`），周期状态与同步点不校验（`metadata_view_period.go:11`），一起补上。其中共享的读方法对其他经网关的调用方照旧可用。storage-view 启动时调 `ListViews` 的那次请求（`main.go:464`）现在用 Primary 密钥签名，同步改用 View 密钥，否则加了验签后会启动失败。
  - 网关 ACL 删除 storage-view 作为调用方访问 Storage 服务的规则（Metadata 的 `catalog.yaml:1479`、`:1517`，PrimaryStore 读方法的 `:1620`；后者它本来就直连，用不到）。storage-view 只保留调 Admin 的 EventBus 检查与清理走网关（`catalog.yaml:345`），这是跨模块调用。storage-view 对外提供的 DataView 查询照旧经网关被其他模块调用。
- 已知取舍：同一个写入方能写所有数据集，模块因 bug 写错数据集 ID 时 Storage 不拦。单用户、自己维护的几个模块之间可以接受。
- `data_source_id` 对所有数据集都可选，填写时校验引用存在；创建、激活、保存三处（`metadata_catalog.go:130`、`activation.go:182`、`crud_dataset.go:440`）一起改，结果一致。
- 删除 `dataset_attributes.go` 的不可变约束与 `crud_dataset.go:211` 对因子结果的判断：数据集属性全部由业务模块自己维护，Storage 不读取、不锁定。
- `write_owner` 属性本身一起删掉：factor-mgr 建结果数据集时不再写它（`factor/internal/catalog/service.go:673` 一带），对账时也不再要求它等于 `factor`（`reconcile.go:88`）。代码里其余写到它的只有上面要删的 Storage 检查。

### 3.2 写入方的数据规范与保留策略（编码第 1 步）

- `normalizeStockCNSeriesTags` 删除：A 股写入方（Collector）自己填写 `series_tag`，Storage 只校验格式。
- `packages/storagepolicy` 只保留通用规则（按空间、频率解析保留期）。各市场与 `mooxsys` 的默认保留期移到部署配置（`moox.toml` 的 `[storage_retention.spaces.*]`），删除 `mooxsys` 的专门豁免与按交易分钟数估算根数的校验；CLI `setup retention` 只校验格式、显示配置值，不再估算根数。
- 删除死代码 `ResultDatasetResolver`。

### 3.3 元数据校验与列契约（编码第 2 步）

- 删除 `dataset_role` 取值校验、`dataset_factor_` 前缀规则，以及列显示名按因子输出与 `mooxsys` 的分支、因子列填充值的校验（`validate.go:90`～`:120`、`:168`～`:175`）：显示名由建列的模块写入，Storage 不推断。
- 因子列的命名与类型契约（`column_kind`、`member_key`、`output_name`、`__params`）从 Storage 移到 factor-mgr：factor-mgr 建列时自己保证，Storage 只校验通用规则（列名唯一、值类型合法、字段列引用存在）。
- **列来源统一为「字段 / 自定义 / 系统」**：`DATASET_COLUMN_ORIGIN_TYPE_FACTOR` 改名为 `DATASET_COLUMN_ORIGIN_TYPE_CUSTOM`（`common.proto:154`），表示数据集自己定义的值列、身份就是列名、**不带 `origin_id`**；生产者信息（因子 ID、成员键、输出名）由写入方放在列属性里，Storage 不解释。
  - `schema/metadata.sql`：`CHECK (c_origin_type IN ('field', 'custom', 'system'))`；来源唯一索引只约束字段列（`WHERE c_origin_type = 'field'`）。改表结构，全新部署。
  - 元数据读写（`metadata/sqlite/crud_helpers.go:271` 等）、种子（`bootstrap/metadata/seed.go:348`）、`metadata_catalog.go:598` 的因子列分支同步修改。
  - 上层：factor-mgr 建因子列用 `CUSTOM`、不填 `origin_id`，按列判定成员归属的地方（`factor/internal/catalog/service.go:640` 读 `OriginID`）改读列属性 `member_key`；Archive 读列来源的地方（`archive/internal/worker/source/source.go:104`、`:261`）改为 `CUSTOM`；CLI 的元数据导入导出与初始化预检、Web（`api/storage/types.ts` 与用到列来源的页面）改为 `FIELD / CUSTOM / SYSTEM`；重新生成 proto 代码，相关测试一并改。

### 3.4 View 由业务模块维护（编码第 3 步）

- **默认 View 由 factor-mgr 自己建、改、删**：删除 `factor_result_view.go`、数据集激活与加列时对托管 View 的检查（含激活重试，`metadata_catalog.go:335`）、删除数据集时自动删托管 View 的逻辑。factor-mgr 与 Collector 一样用 `CreateView`、`UpsertViewColumn`、`DeleteView`（factor-mgr 的 Metadata 客户端 `storageio/client.go:27` 要补这几个方法）。删除因子结果数据集的顺序：删默认 View → 清空行 → 删数据集（`t_views` 外键 `RESTRICT` 要求先删 View）。View 属性里不再有 `managed_by`、`view_role = factor_result`、`primary_dataset_role` 这类 Storage 读取的业务标记。
- **View 跟随数据集加列**：View 属性 `follow_dataset_columns = true` 时，数据集加列后原地扩展 View，复用现有扩展机制；扩展提交条件（`crud_view_index.go:283`）与 `maintenance_schema_extension.go:125` 的目标判定都改看这个属性。**删除事件出现未扩展列时暂停消费的屏障**（`event_apply.go:444` 的 `validateFactorResultEventColumns`），View 不再为等扩展而阻塞消费，见下一条的系统边界。
- **系统边界：新列在 View 扩展完成之前的值不进 View**（用户决定，2026-10-27）。数据集加列后，View 的扩展在维护循环里异步完成，通常几秒到几十秒。这段时间里到达的行事件按 View 当前的列投影应用，新列直接裁掉，与 View 处理任何不在投影里的列一样；扩展完成后的事件才带上新列。这些事件不会把新列写进 View，不补数（之后同一行若再被更新或重算，会带上新列），主库（Pebble）里的值不受影响。对因子来说，就是新成员在 View 里晚生效一两期，策略在这几期读到空值。需要这几期的值时，扩展完成后对那几期执行 `recalc`，重写的行会带上新列。
- **重建方式单独声明**：View 属性 `rebuild_source`：`active_then_primary`（默认，先复制活动索引再从主库补）或 `primary`（不复制活动索引、只从主库历史重建，主库没有数据时允许先空着）。`build.go` 按这个属性选分支，factor-mgr 为默认 View 设置 `primary`。
- **维护顺序由 View 声明**：View 属性 `maintenance_priority`（整数，越小越先，默认 50）取代 `maintenance.go:621` 按 `view_role` 的排序；Collector 的浏览 View 设 0，factor-mgr 的默认 View 设 10。
- **只有一种消费方式，并发由全局配置决定**：每个数据集本来就有自己的动态 durable（不同数据集之间并行消费）。删除 `factor` / `misc` 两个分区模板与静态 durable、按 `dataset_factor_` 前缀选模板的逻辑；只保留一个消费模板，配置项 `fetch_batch`、`max_workers`、`max_ack_pending` 全局默认都是 1（同一数据集内按顺序处理，避免同一行的两次更新先后颠倒）。动态 durable 统一命名为 `view_<数据集哈希>`，名字里不带业务；EventBus 权限里按前缀放行的规则同步改为 `view_`。同步修改 `packages/events/storage_consumers.go`、Storage 的默认配置与部署配置（`config/loader.go:130`～`:210`、`config/storage.yaml:27`）、按名称分支的代码（`cmd/server/main.go:600`、`cmd/cli/reconcile_view_consumers.go:161`）、Admin 生成 EventBus 权限的代码（`admin/cmd/cli/eventbus_credentials.go`）及其测试。代码默认配置里写死的空间与 `mooxsys` 数据集删掉：单一模板不再有静态路由，旧拓扑的校验三处一起改为只校验并发项：配置加载（`config/loader.go:194`、`:224` 只认两种分区 durable、要求非因子分区有路由）、服务入口的拓扑校验（`cmd/server/main.go:464`）、动态协调器要求 `misc` 与 `factor` 两个模板同时存在的检查（`inventory_reconciler.go:426`）；动态协调器（`inventory_reconciler.go:278`）不再按配置的空间或数据集白名单过滤，凡是活动 View 引用的数据集都建 durable。导入历史时的 View 追平速度上线后实测，不够再调大全局并发。

### 3.5 周期完成统一（编码第 4 步）

一个周期完成有两种来源，统一成一种事件、一套接口：

| 来源 | 谁判定 | 接口 |
|---|---|---|
| `ledger` | DataNode 按登记的序列快照与截止时间判定（现在的采集与重采样：都登记快照并调用 `CommitTimeSeriesBatch`，`resample/engine.go:307`、`:395`） | `RegisterSeriesSnapshot`、`CommitTimeSeriesBatch`（不变） |
| `producer` | 写入方自己报告（现在只有因子） | `ReportDatasetPeriodCompleted`；查询 `GetDatasetPeriodCompleted` |

`source` 只描述"这一期是怎么判定完成的"，不是业务分类；消费方按自己绑定的数据集与频率筛选，两种来源都接受。

事件 `DatasetPeriodCompleted`：

| 字段 | 说明 |
|---|---|
| `dataset_id`、`frequency`、`period_time` | 哪个数据集的哪一期 |
| `status` | `complete` / `degraded` |
| `source` | `ledger` / `producer` |
| `universe_subject_ids`、`failed_subjects` | 本期应有的标的与失败的标的 |
| `committed_positions` | 本期写入在 outbox 中的位置，View 用来确认数据已进入索引 |
| `snapshot_ref` | `ledger` 来源时为序列快照引用（替代 `batch_id`、`config_snapshot_id`、`expected_scope_ref`） |
| `cause_event_id` | `producer` 来源时为触发本次计算的事件 ID（替代 `trigger_event_id`），用于幂等查询 |
| `completed_at` | 完成时间 |
| `detail_type`、`detail` | 写入方自定义的明细（不透明字节），Storage 原样保存并透传，不解析 |

- Collector 里 `ReportCollectorPeriodCompleted` 的客户端代码（`marketstorage/storage.go:134`、`storageio/gateway.go:194`）已无调用方，随接口一起删除。Collector 的重采样触发消费者（`resample/eventconsumer/consumers.go:43`、`:138`）改订阅 `DatasetPeriodCompleted`，按源数据集与频率筛选后交给 `PeriodTrigger`；消费者接口（`consumers.go:23`）与 `PeriodTrigger.OnPeriod`（`resample/triggers.go:96`）的入参及字段读取一起改为新事件类型；EventBus 权限同步。
- 删除 `CollectorPeriodCompleted`、`FactorPeriodComputed` 两种事件，`ReportCollectorPeriodCompleted`、`ReportFactorPeriodComputed`、`GetFactorPeriodComputed` 三个接口，以及 DataNode 对应的三个方法（合并为 `AppendDatasetPeriodCompleted`、`GetDatasetPeriodCompleted`）。
- 因子把每个因子的状态（现在的 `FactorPeriodState`）放进 `detail`，`detail_type = moox.factor.PeriodDetail.v1`；`PeriodDetail` 定义在因子现有的 proto（`modules/factor/proto`，Strategy 已经依赖它，`strategy/go.mod:8`），不新建包；`packages/events` 中的因子状态校验移到因子模块。
- `ViewDataReady`：删除 `completion_kind`，`factors[]` 改为透传 `detail_type` 与 `detail`，其余字段不变；载荷不兼容，事件名不变、版本升为 v2（主题 `moox.event.storage.view.data.ready.v2.<空间>.<View>`）。Strategy 按绑定的 View 与数据集判断事件是否相关，不再按来源区分；`detail_type` 是因子明细时解码。

### 3.6 目录模型去掉市场与采集概念（编码第 5 步）

原则：Storage 只保留它自己要用的东西（集合、成员、行的身份）；业务含义放进写入方维护的 `attributes`，Storage 原样保存、不解释；业务任务的运行状态回到业务模块自己的库。

**标签只是一组标的**

- `Tag` 只保留 `tag_id`、`tag_name`、`description`、`mode`、`builtin`、成员统计与 `attributes`。`attributes` 是新增的：`metadata.proto` 的 `Tag` 加字段，`t_tags` 加 `c_attrs_json`，`crud_tag.go` 读写、元数据缓存、Storage 自己的种子初始化（`bootstrap/metadata/seed.go:126`）与 Collector 的种子解析一起改。
- 删除 `source`（含 `t_tags.c_source_id` 及其外键）、`market_type`（含取值白名单 `crud_tag.go:30` 与不可变约束）、`cron`、`timezone`、`last_run_at`、`last_status`、`last_error`；删除 `ReportTagRunFailure`。
- `ApplyTagSnapshot` 去掉 `run_at`，只表示"把成员设成这份快照"；被移出的成员的 `inactive_at` 取 Storage 收到快照的时间。
- 标的修订号（`t_space_revisions.c_subjects_revision`，Strategy 靠它刷新标签缓存，`strategy/internal/input/cache.go:104`）的触发器由监听 `c_market_type`（`metadata.sql:461`）改为监听 `c_attrs_json`。
- 删除 `subject_listing` 能力的整条链路：Storage 的校验（`crud_tag.go:154`）、Collector 启动时的能力登记（`bootstrap/subject_sync.go:50`、`subjectsync/storage_client.go:107`）、CLI 元数据比较里的特殊处理（`metadata_implementation.go:604`）。来源与品种组合是否支持，仍由 Collector 的 lister 判断（`subjectsync/lister.go:28`）；Web 标签表单原来从数据来源的 `subject_listing` 属性生成可选的来源与品种（`tags-tab.vue:169`），改为调 Collector 新增的只读接口 `ListSubjectListers`，返回 lister 支持的（来源，品种）组合。

**标的同步配置写进标签属性**，由 Collector 解释，标签仍只在种子里定义一处：

```yaml
tag_id: binance_spot
mode: auto
attributes:
  market_type: spot
  sync.source: binance
  sync.cron: "13 * * * *"
  sync.timezone: UTC
```

没有选"同步配置整体搬到 Collector 配置"：那样一个标签要在两处定义（Storage 建集合、Collector 配同步），容易对不上。

**运行状态回到 Collector 自己的库**

- Collector 新增一张表，按（空间，`tag_id`）记录上次运行时间、结果与错误；`subjectsync/tag_runner.go` 的调度判断改读这张表，失败记在这张表里并打日志、出指标（`subjectsync/metrics.go`），不再报给 Storage。
- Collector 新增只读接口 `ListTagSyncStatus(space_id)` 返回这张表。Monitor 的标签检查（`bootstrap/market_canary.go:31`）照旧做三项：`failed` 与 `stale` 改用这个接口的结果，`stale` 的周期从标签属性 `sync.cron`、`sync.timezone` 算；`no_active_members` 仍用 Storage 的成员统计。Web 的标签页（`views/data/subjects/tags-tab.vue:54`）同样调这个接口显示运行状态。

**标的的业务字段放进属性**

- `Subject` 只保留 `subject_id`、`name`、`timezone`、`status`、`attributes`；`subject_type`、`market`、`currency` 改为属性键。时区是时间序列对象的通用信息，保留为字段。
- `TagSnapshotItem` 同样改为 `subject_id`、`name`、`timezone`、`attributes`。与现在一样，快照只为新出现的标的建 Subject 并带上属性；已有标的的属性由 Collector 的属性维护任务（`subjectsync/attr_runner.go:117`）更新，这个任务生成的属性同样要带上 `subject_type`、`market`、`currency`（现在只有 `base`、`quote`、`pair` 或 `exchange`），两条路径写出的属性键一致。
- 列表与搜索去掉这些字段：`ListSubjectsRequest` 删除 `market`、`subject_type` 过滤（`metadata.proto:787`），SQLite 的标的列表（`crud_subject.go:46`）、标签成员搜索（`crud_tag_member.go:49`）与元数据缓存（`cache/store.go:315`）同步修改；关键词搜索只匹配 ID 与名称。Web 拿到列表后在本地筛选（每个空间几百到一两千个标的）。现有 Go 调用方只按 ID 查标的，不依赖这些过滤。

**数据来源只是一个可引用的名字**

- `DataSource` 只保留 `data_source_id`、`name`、`status`、`attributes`；删除 `kind` 及其 `CHECK`、`market`、`timezone`、`config_json`。采集的连接配置本来就在 Collector 的源配置里（`collector/configs/sources/**`）。`ListDataSources` 删除 `kind`、`market` 过滤，关键词搜索只匹配 ID 与名称。

**序列身份只用 `subject_id` 与 `series_tag`**

- `SeriesSnapshotEntry` 只保留 `subject_id`、`series_tag`；`packages/seriesset` 的 `Series` 与规范键同步改为两项，Collector（`marketfetch/plan_desired.go:196`、重采样）与 Storage（`datanode/period.go:26`）用同一份实现，`series_hash` 自然一致。
- 依据：周期账本核对行时本来只用这两项（`DatasetPeriodSeries`，同一份快照里 `subject_id + series_tag` 不能重复，`pebble/period_snapshot.go:101`）；行的身份也只有标的、频率、时间、`series_tag`，快照里的另外三项从来不参与行的身份。
- `series_tag` 由 Collector 决定，Storage 不解释。加密货币由 `defaultMarketSeriesTag`（`kline_pipeline.go:678`）生成：普通现货是 `venue:<provider>`，其他品种或来源再追加 `market:`、`source:`，同一个数据集里不同路由不会撞；重采样沿用源序列的 `series_tag`（`resample/engine.go:403`）。A 股固定写 `default`（`marketfetch/series_tag.go:12`）：同一只股票不论从哪个行情源采到，都写同一行，来源记在行字段里（`kline_pipeline.go:753`）。这是有意共享的序列身份，两项快照与现在的行为一致。
- Collector 内部仍记录每个标的用哪个行情源与来源（计划记录 `plan_record.go`），只是不再交给 Storage。

**表结构与上层**

- `t_tags`、`t_subjects`、`t_data_sources` 删列与 `CHECK`，`t_tags` 加 `c_attrs_json`，随全新部署生效。
- Collector：标签固定字段的所有读取点改读属性——标的同步的调度与筛选（`subjectsync/tag_runner.go`、`syncer.go:30`）、采集计划分组（`marketfetch/task_groups.go:95`）、建任务校验（`rpc/service.go:1119`）、重采样的来源推导与任务结果记录；运行状态表与 `ListTagSyncStatus`、`ListSubjectListers` 两个只读接口（服务目录 `catalog.yaml:627` 一带的 Collector 服务同步声明这两个方法、标为只读，ACL 放行 Monitor 与 Web 所用的 `console` 身份）；属性维护任务补齐属性键；快照写属性；序列快照只交两项；删除 `subject_listing` 能力登记；种子与元数据导入。
- Strategy：`input/client.go:207` 改读 `tag.attributes["market_type"]`。
- Monitor：标签检查按上文改用 Collector 的运行状态与标签属性。
- Trade：经核对不读 Storage 的标签，无需改动（Trade 自己协议里的 `market_type` 与本步无关）。
- CLI：元数据导入导出（`metadata_implementation.go`，含删除 `subject_listing` 特殊比较）、`metadata tag clone`、`collector subject exclusions`。
- Web：`api/storage/types.ts`、`metadata.ts` 与标的、标签、数据来源页面改读属性、本地筛选；标签页的运行状态与新建标签时的来源、品种选项改调 Collector。
- 种子：`config/setup/metadata.yaml` 的标签、标的、数据来源改为属性写法。

## 4. 编码顺序与发布

### 4.1 编码顺序

按风险从小到大分五步编码，每一步独立提交、独立通过测试与 Codex 审查；后面的步骤在前一步的基础上做，少处理一些分支：

| 步 | 内容 | 涉及模块 |
|---|---|---|
| 1 | 写入权限只靠网关 ACL、storage-view 调 Metadata 改直连（3.1）；写入方数据规范、保留策略、死代码（3.2） | Storage、服务目录（网关 ACL）、factor-mgr（删 `write_owner`）、Collector（A 股 `series_tag`）、`packages/storagepolicy`、CLI、部署配置 |
| 2 | 元数据校验与列契约、列来源改名（3.3） | Storage、factor-mgr、Archive、CLI、Web |
| 3 | View 由业务模块维护、跟随加列、重建方式与维护顺序、单一消费模板（3.4） | Storage（view、catalog、metadata、server、cli、配置）、factor-mgr、Collector、`packages/events`、Admin（EventBus 权限生成）、部署配置 |
| 4 | 周期完成与就绪事件统一（3.5） | Storage、因子引擎、factor-mgr、Collector（重采样）、Strategy、`packages/events`、`packages/storagepb`、因子 proto、服务目录、EventBus 流配置、Admin（EventBus 权限生成里 Storage、Factor、Strategy 的主题与消费者）及其测试 |
| 5 | 目录模型去掉市场与采集概念、序列身份只用标的与 `series_tag`（3.6） | Storage（metadata、datanode、缓存、种子初始化）、`packages/seriesset`、Collector、服务目录（Collector 新增的两个只读方法）、Strategy、Monitor、CLI、Web、种子 |

理由：第 1 步把"谁能写"整个交给网关，Storage 里按业务角色的权限分支一次删光，后面几步删除业务分支时都不用再顾及权限；第 2、3 步只动元数据与 View；第 4 步改协议和事件；第 5 步与前四步互不依赖，放在最后做，也可以在第 1 步之后任意时间做。

### 4.2 一次性全新部署

五步编码全部完成后，一次发布：清空生产、按全新安装流程重装（用户已接受删除全部线上数据）。全新安装的步骤以《部署与运维》第 2 节及其"2026-10-24 全新部署补充"为准，那次主机登记统一就是这样上线的；这里只写本次要注意的地方。不写迁移代码，也不用 `system reset`（它按旧表结构清记录、不重建 Storage 元数据库，也不清 EventBus 流的旧主题）。

1. **清空前核对**：Trade 只有纸面账户、没有实盘持仓与未完成订单；`moox.toml`（本机，加密）与各主机的 SSH 访问可用。
2. **停止并清空**：停四台主机（control、storage、overseas、factor）上的全部组件，清空部署根 `/data/moox`；本机的安装状态也一起清掉：`~/.local/state/moox/setup/<绑定摘要>`（否则会复用上次保存的安装制品）与 `~/.config/moox` 里旧控制面的 CA 绑定。这一步把所有状态一起删掉：各模块的库（含 Admin、Collector、factor-mgr、Strategy、Trade、Monitor、Archive、CloudFunction）、Storage 元数据库、Pebble 与 View 索引、EventBus 流与消费者、因子机本地存储与补算账本、归档目录。因子机清空后按补充说明重建 `/data/moox`。
3. **全新安装**（首次安装不带 `--release`，按《部署与运维》第 2 节的顺序）：`setup bootstrap --stage core` → 各业务主机 `setup deploy-host` → EventBus 单元 → storage 单元（注册 DataNode、`setup apply`、导入并激活 `mooxsys` 与业务空间的元数据）→ Access、egress-proxy → `setup bootstrap --stage control`（internal TLS 带 `--create-proxy-ca`）→ 创建业务空间 → overseas 的 collector-stream 与 Trade（在 control 就绪之后）→ factor 的因子引擎与 archive-worker（外部程序）。EventBus 的流配置与凭据由新版本的安装生成，自然是新主题、新 durable。
4. **采集就绪**：默认采集任务由 Collector 首次启动写入（不足时 `moox-collector-cli init --seed-file` 补种）；crypto 摄取方式切到 collector-stream；确认实时 K 线在写入、周期账本正常定稿。
5. **导入历史**：按 `skills/moox/references/initial-import.md` 导入长历史（永续 501 个标的的 5m 等）。这时还没有因子任务与策略实例，满足导入的前置条件；等 View 追平。
6. **建因子与策略**：导入完成、View 追平后再 `setup factors`（它会建任务并启用成员，所以必须放在导入之后），factor-mgr 同时建结果数据集与默认 View；因子引擎自动推进任务生命周期；再建策略定义与实例并启用。
7. **验收**：`doctor alerts` 为 0；采集 → 账本完成 → 因子 → View 就绪 → Strategy 全链路一期；第 5 节各项。

不做回退方案：出问题时修代码重新部署。

### 4.3 各步对上层模块的改动

| 模块 | 第 1 步 | 第 2 步 | 第 3 步 | 第 4 步 |
|---|---|---|---|---|
| factor-mgr | 建结果数据集与对账都不再带 `write_owner` | 建列时自己保证因子列契约与显示名；列来源用 `CUSTOM` | Metadata 客户端补 View 的建、改、删；自己建、改、删默认 View（`follow_dataset_columns = true`、`rebuild_source = primary`、`maintenance_priority = 10`）；删除数据集按 3.4 的顺序 | 定义 `PeriodDetail` |
| 因子引擎 | — | — | — | 报告改为 `ReportDatasetPeriodCompleted`（`detail` 为 `PeriodDetail`）；幂等查询改为 `GetDatasetPeriodCompleted`；触发改为订阅 `DatasetPeriodCompleted`，按绑定的源数据集与频率筛选，两种来源都接受 |
| Collector / collector-stream | A 股写入自己填 `series_tag` | — | 浏览 View 设置 `maintenance_priority = 0` | 重采样仍走账本（`ledger`）；重采样触发消费者改订阅 `DatasetPeriodCompleted`；删除无调用方的 `ReportCollectorPeriodCompleted` 客户端 |
| Strategy | — | — | — | 订阅 `ViewDataReady` v2；按绑定的 View 与数据集判断相关性，不再按 `completion_kind`；因子明细从 `detail` 解码 |
| packages / CLI / Web | `storagepolicy` 去掉市场与 `mooxsys` 豁免；CLI `setup retention` 不再估算根数 | CLI、Web 改为 `FIELD / CUSTOM / SYSTEM` | `events` 的 View durable 改名 | `events` 换注册表、删因子校验；`storagepb` 同步 |
| 部署配置 | `moox.toml` 补各市场与 `mooxsys` 保留期 | — | View 消费配置只剩并发项 | EventBus 流主题 |
| 服务目录 | 网关 ACL 读写分开、写方法按实际调用放行 | — | — | PrimaryStore 方法改名与删除；外部身份白名单同步 |

第 5 步对上层模块的改动见 3.6 末尾的「表结构与上层」。

## 5. 测试

- 第 1 步：Storage 内不再有按调用方的写入判断（任何通过网关的写入方都能写任意数据集，包括数据行、周期账本、标记、清空与恢复、删除数据集）；网关对只读模块（strategy、storage-view）拒绝所有写方法，对各写入方只放行它实际调用的写方法，全链路（采集、重采样、因子、Monitor 系统数据、CLI 导入与运维）都不被误拦；`data_source_id` 在创建、激活、保存三处结果一致；A 股写入由 Collector 填 `series_tag`；保留期从部署配置读取；storage-view 不经网关直连 Metadata，它调用的每个方法正确签名可用、错误签名被拒，启动时的 `ListViews` 正常；网关 ACL 里没有 storage-view 访问 Storage 的规则，storage-view 仍能经网关调 Admin 的 EventBus 检查与清理。
- 第 2 步：factor-mgr 建出的因子列满足契约、来源为 `CUSTOM`、不带 `origin_id`；多输出因子与 `__params` 照常可建；多个自定义列互不冲突、同一字段在一个数据集里仍只能出现一次；CLI 导出再导入一致；Web 显示正确。
- 第 3 步：factor-mgr 建、改、删默认 View；加列后扩展完成之前到达的事件按当前投影应用、新列被裁掉且不阻塞消费，扩展完成之后的事件带上新列；`rebuild_source` 两种重建各自正确；维护顺序按 `maintenance_priority`；没有静态路由时 Storage 能启动，活动 View 引用的每个数据集都建出 durable，消费并发按全局配置生效、动态 durable 名不带业务；只有默认 View 时因子结果数据集能完整删除。
- 第 4 步：`ledger` 判定与现在的账本一致；`producer` 报告与查询一致；`detail` 原样透传到 `ViewDataReady`；源 K 线周期完成（`ledger`）触发重采样、重采样结果周期完成（`ledger`）→ 因子计算（`producer`）→ Strategy 触发；采集 → 账本完成 → 因子 → View 就绪 → Strategy 全链路。
- 第 5 步：标签属性的保存、读取与种子导入，改属性会递增标的修订号、Strategy 刷新缓存并从属性读到 `market_type`；Collector 按 `sync.*` 调度标的同步、运行结果写进自己的表，`ListTagSyncStatus` 驱动 Monitor 的 `failed`/`stale` 检查与 Web 标签页，没有运行记录的标签按 `stale` 处理（与现在一致）；Web 用 `ListSubjectListers` 的选项能新建自动标签；快照移出的成员 `inactive_at` 为收到快照的时间；快照新建的标的与属性维护任务更新的已有标的都带上 `subject_type`、`market`、`currency` 属性；标的、数据来源的业务字段读写走属性、列表与搜索只按 ID 和名称；删除 `subject_listing` 后 Collector 正常启动；加密货币（含重采样）与 A 股两类生产者的序列快照只交两项，Collector 与 Storage 算出的 `series_hash` 一致、周期正常定稿；Web 的标的、标签、数据来源页面正常。
- 部署：在测试环境把 4.2 走一遍（清空、全新安装、小规模历史导入、建因子与策略、全链路一期）。

## 6. 待确认

- 无。

## 7. 审查记录

v1～v5（2026-10-17）：吸收 Codex 五轮评审，主要涉及 `source` 的含义、`write_owner` 可授权修改、`commit_id` 限定单数据集、列来源、事件全部升版本与新旧消费者隔离、静默点切换与回退、周期完成标记迁移。其中迁移命令、静默点快照与回退、新旧版本并存在 v6 按用户原则删去；`commit_id` 与列来源唯一约束已分别在通用写入（2026-10-27）与元数据 v14 完成。

### v7（Codex 第一轮审查 v6，2026-10-27）：需要修改 → 已修改

| 意见 | 处置 |
|---|---|
| 第 1 步"先补 `write_owner` 再发 Storage"与不可变约束冲突（旧 Storage 不许修改、也加不上新值；`crud_dataset.go:211` 要求因子结果为 `factor`） | 接受。规范 AppId 改用已在用的短名（`factor`、`collector`、`monitor`），因子数据集不用改；空 `write_owner` 不限写入方、可首次设置；顺序改为业务模块 → Storage → 重启对账（3.1、第 4 节） |
| Monitor 实际用 `monitor`，与拟填的 `moox-monitor` 不一致 | 接受，见上 |
| 第 2 步漏了显示名按因子输出与 `mooxsys` 的分支、因子 View 列识别；`data_source_id` 还在激活与保存两处 | 接受，补入（2.4、3.1、3.3） |
| 第 3 步 factor-mgr 先接管会被旧 Storage 的托管校验拒绝 | 接受。改为先发 Storage，属性缺省时沿用原推断，再发 factor-mgr 接管（3.4、第 4 节） |
| `follow_dataset_columns` 不能同时决定重建策略；维护顺序也按 `view_role` | 接受。拆出 `rebuild_source` 与 `maintenance_priority`（3.4） |
| 第 4 步 View 消费者切换不完整：静态与动态 durable 都不能放宽过滤主题；EventBus 流配置与凭据要换；新 `ViewDataReady` 的事件名 | 接受。排空后删掉重建、从流末尾开始；流配置与凭据列入换装；`ViewDataReady` 升 v2（3.5、4.1） |
| "排空"不能证明因子已追平：暂停不等运行中的计算、暂停期间的事件被确认、无水位任务直接报追平 | 接受。写明等运行中期次结束；暂停区间由追赶按水位补算；无水位任务的边界（4.1） |
| `view_delivery` 不改 durable 名的前提；factor-mgr 客户端缺 View 方法；`storagepolicy` 的 `mooxsys` 与 CLI 估算；`PeriodDetail` 放现有因子 proto；`FACTOR` 暂不改名的表述 | 均接受（3.2、3.4、3.5、第 6 节） |

### v8（Codex 复审 v7，2026-10-27）：还需修改 → 已修改

| 意见 | 处置 |
|---|---|
| 旧 Storage 删除数据集只认 `moox-factor`、`moox-collector`，业务模块先改短名会让删除被拒 | 接受。第 1 步在两个边界之间连续换装，换装间隔里不做数据集删除（第 4 节） |
| 空 `write_owner` 不限写入，与"只读身份自然写不了"矛盾 | 接受。改为只读身份写不了声明了 `write_owner` 的数据集，测试预期同步（3.1、第 5 节） |
| 第 3 步缺省推断漏了 `collection_browse` 的维护顺序 0 | 接受，补入（第 4 节、第 5 节） |
| 动态 durable 现在强制 `DeliverAll`，重建时如何从流末尾开始、以后新建如何照旧 | 接受。一次性参数重建时从流末尾开始，平时照旧 `DeliverAll`（4.1） |
| 消费者建立、组件启动与确认的顺序不清楚 | 接受。写明暂停中不会产生事件、启动顺序与确认步骤（4.1） |
| （第三轮）DataNode 重启后会按截止时间结束到期周期并发布事件，"启动顺序不影响消费完整性"不成立 | 接受。改为先启动全部消费者并确认 durable，再启动 storage-node 与 storage-primary（4.1） |

### v9（用户决定，2026-10-27）

列来源 `FACTOR` 改名为 `CUSTOM`（3.3），用户接受全部删除、全新部署。四步改为编码顺序，最后一次性全新部署（4.2）；v7、v8 为分步在线发布写的过渡全部删去：属性缺省时的推断、消费者从流末尾重建与 `--recreate-mismatched-consumers`、旧完成标记不转换、模块之间的换装顺序、"换装间隔里不做数据集删除"、已有数据集补 `write_owner`。View 的 durable 也随之改名为 `parallel` / `serial`。

### v10（Codex 审查 v9，2026-10-27）：需要修改 → 已修改

| 意见 | 处置 |
|---|---|
| `system reset` 不按新表结构重建 Storage 元数据库；EventBus 在旧主题仍有消息时拒绝更新流，reset 也不清整条流；编排器只覆盖部分模块，Archive 目录、CloudFunction 库不在其中 | 接受。4.2 改为清空部署根后按《部署与运维》的全新安装流程重装，不用 `system reset` |
| 初始化与导入顺序冲突：`setup factors` 会建任务并启用成员；导入要求实时采集已在写入 | 接受。顺序改为采集就绪 → 导入 → View 追平 → `setup factors` 与策略（4.2） |
| 第 2 步漏了 Archive 对列来源的引用，factor-mgr 按 `OriginID` 判定成员归属 | 接受，补入（3.3、4.1） |
| 第 3、4 步 durable 与事件的改动清单不全（Storage 配置与按名称分支的代码、Admin 凭据生成） | 接受，补入（3.4、4.1） |
| 采集源配置与服务目录说明里还有 `moox-collector` | 接受，补入（3.1） |
| 换装清单补 archive-worker、CloudFunction；host-agent 不是业务库 | 接受。全新安装覆盖全部组件，清空步骤列出全部状态（4.2） |
| （复审 v10）本机安装状态 `~/.local/state/moox/setup`、`~/.config/moox` 会复用旧制品与旧 CA 绑定；安装顺序要包含 `bootstrap --stage core` 与创建业务空间；SCF 采集配置与 `scf_environment.go` 的身份校验 | 接受，补入（3.1、4.2） |

### v11（用户决定，2026-10-27）

- View 遇到未扩展的新列时不再暂停消费、不再重投，也不补数：新列直接裁掉，扩展完成前那几期的新列在 View 里为空，作为系统边界写入 3.4。理由：不让一个数据集的加列阻塞 View 的正常消费；因子晚生效一两期可以接受。
- 删除 `view_delivery` 属性与 parallel / serial 两种模板：每个数据集本来各有一个 durable，只保留一个消费模板和全局并发配置（默认 1）。

### v12（2026-10-27）

- 用户决定：View 的 durable 名简化为 `view_<数据集哈希>`（3.4）。
- 全量复查 Storage 的代码、协议、表结构与配置，新增两处耦合：标的、标签与数据来源目录里的市场与采集概念（2.6），周期账本的序列身份带着采集的维度（2.7）；方案见 3.6，列为编码第 5 步。

### v13（用户同意，2026-10-27）

3.6 细化：标的同步配置（来源、品种、调度、时区）写进标签 `attributes` 由 Collector 解释，只有运行状态回到 Collector 自己的库；标的的 `subject_type`、`market`、`currency` 与数据来源的业务字段改为属性；序列身份只用 `subject_id` 与 `series_tag`。

### v14（Codex 审查 v13，2026-10-27）：需要修改 → 已修改

| 意见 | 处置 |
|---|---|
| 标签目前没有 `attributes`，属性存储链路缺失 | 3.6 补协议字段、`c_attrs_json`、读写、缓存与两套种子 |
| 运行状态移走后 Monitor 的 `failed`/`stale` 检查和 Web 标签页无处取值 | Collector 新增 `ListTagSyncStatus`，Monitor 与 Web 改调；周期从标签属性算 |
| 删 `subject_type` 等字段后 Storage 的过滤、搜索与缓存未列 | 补 `ListSubjectsRequest`、SQLite、缓存、关键词搜索的改动 |
| 修订号触发器监听要删的 `c_market_type`；`run_at` 被用来填 `inactive_at`；`c_source_id` 外键 | 触发器改监听 `c_attrs_json`；`inactive_at` 取收到快照的时间；删列连同外键 |
| Collector 读标签固定字段的地方与 `subject_listing` 链路列得不全 | 逐处列出；`subject_listing` 从 Storage、Collector、CLI 一起删 |
| 序列身份论据不准：现货只写 `venue:<provider>`、A 股早已是 `default` 且有意共享 | 更正为按实际生成规则说明，A 股写成有意共享的序列身份 |
| 第 4 节仍写"四步"、4.3 无第 5 步、测试顺序颠倒且第 5 步覆盖不足 | 改为五步；4.3 指向 3.6 的清单；测试按顺序并扩充 |
| 建议："新列永远为空"不准确 | 改为"这些事件不写新列，之后同一行再更新会带上" |
| 建议：快照对已有标的的属性如何处理 | 写明快照只为新标的建 Subject，已有标的由属性维护任务更新 |
| 建议：Trade 不读 Storage 标签 | 清单注明无需改动，4.1 涉及模块去掉 Trade |

### v15（Codex 复审 v14，2026-10-27）：需要修改 → 已修改

| 意见 | 处置 |
|---|---|
| Web 标签表单靠 `subject_listing` 生成来源与品种选项，删除后无法新建自动标签 | Collector 新增只读接口 `ListSubjectListers`，Web 改调；列入测试 |
| 属性维护任务只写 `base`/`quote`/`pair`/`exchange`，已有标的拿不到迁入属性的字段 | 属性维护任务同样生成 `subject_type`、`market`、`currency` |
| 建议：两条属性写入路径分别测试；无运行记录时的 `stale` 判定 | 写入第 5 步测试 |

其余意见已确认处理正确。

### v16（Codex 全文审查 v15，只看重要问题，2026-10-27）：需要修改 → 已修改

| 意见 | 处置 |
|---|---|
| `CreateDataset` 同 ID 转为更新，可绕过 `write_owner` 修改检查 | 检查放在两个入口共用的更新路径上；加测试 |
| 单一消费模板下启动校验仍要求两种分区与路由，动态协调器按白名单过滤 | 3.4 写明：零静态路由、启动只校验并发项、活动 View 引用的数据集都建 durable |
| 重采样触发消费者订阅的 `CollectorPeriodCompleted` 被删后实时重采样不触发 | 第 4 步改订阅 `DatasetPeriodCompleted` 并按源数据集与频率筛选 |
| 重采样实际走账本，不是 `producer` | 3.5、4.3、测试改为重采样属 `ledger`；`producer` 现在只有因子；删除无调用方的 Collector 报告客户端 |

### v17（Codex 复审 v16，只看重要问题，2026-10-27）：需要修改 → 已修改

| 意见 | 处置 |
|---|---|
| 存储层的 `UpsertDataset` 没有调用方身份，`write_owner` 检查放不进去 | 改为两个 RPC 入口在 Catalog 层共用同一个检查函数 |
| 单模板只改配置加载不够，服务入口与动态协调器也校验旧拓扑 | 3.4 列出三处校验一起改 |
| 重采样消费者接口与 `PeriodTrigger.OnPeriod` 入参仍是旧事件类型 | 第 4 步写明入参与字段读取一起改 |

重采样归入 `ledger` 及 4.3、第 5 节的对应改动已确认一致。

### v18（用户决定，2026-10-27）

删除数据集级写入权限 `write_owner`：网关 ACL 已经按方法限制谁能写，`write_owner` 只多防"写入方之间写错数据集"，单用户系统不值得一套数据集级权限。3.1 改为删除 Storage 内全部按调用方的写入判断、收紧网关 ACL（读写分开）；AppId 不再参与授权，不必统一写法。v16、v17 关于 `write_owner` 检查位置的处理随之作废。

### v19（Codex 审查 v18，只看重要问题，2026-10-27）：需要修改 → 已修改

| 意见 | 处置 |
|---|---|
| factor-mgr 建结果数据集写 `write_owner`、对账强制它等于 `factor`，第 1 步未列 | 第 1 步加 factor-mgr：生成与对账都删掉 |
| storage-view 直连 PrimaryStore 写 `AppendDatasetSyncPoint`，与"只读、不放行写方法"冲突 | 写明 Storage 内部组件直连不经网关、照旧；CLI 用的 `AppendDatasetSyncPoint` 在 ACL 中保留 |

Codex 确认：3.1 列的 Storage 内业务身份检查基本齐全；请求体 AppId 不参与路由、幂等键、changelog、事件与告警归属；服务目录能按方法分别授权；因子引擎经外部 `factor-engine` 身份访问。

### v20（用户决定，2026-10-27）

Storage 内部组件之间直连、走服务签名，不经网关 ACL：storage-view 调 Metadata 由经网关改为直连（与调 PrimaryStore 一致），网关 ACL 删除 storage-view 访问 Storage 服务的规则；调 Admin 是跨模块调用，仍走网关。

### v21（Codex 审查 v20，只看重要问题，2026-10-27）：需要修改 → 已修改

| 意见 | 处置 |
|---|---|
| 验签范围漏了 View 周期状态与同步点（这两个方法现在不验签） | 改为 storage-view 实际调用的全部 Metadata 方法都按 View 签名认证，补上这两个 |
| 启动时 `ListViews` 用 Primary 密钥签名，加验签后会启动失败 | 第 1 步同步改用 View 密钥 |

Codex 确认：v19 两项与代码吻合；Metadata 端口监听本机回环、部署要求 storage-view 与 storage-primary 同机、启动顺序 Primary 在前。

### v22（Codex 全文复审 v21，只看重要问题，2026-10-27）：需要修改 → 已修改

| 意见 | 处置 |
|---|---|
| Collector 新增的 `ListTagSyncStatus`、`ListSubjectListers` 未在服务目录声明，网关会拒绝 Monitor 与 Web 的调用 | 第 5 步补服务目录声明、只读标记与 ACL |

Codex 确认：v20、v21 两条意见已正确处理；其余各节未发现矛盾或遗留旧说法。
