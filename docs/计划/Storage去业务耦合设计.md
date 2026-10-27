# Storage 去业务耦合设计

状态：设计 v6（2026-10-27），未编码。v6 按当前代码重写：

- 删去已经完成的部分（第 1 节）；
- 按用户的三条原则（单用户；不过度设计；未上线，不写迁移命令、不设计回退）删去 v5 的一次性迁移命令、静默点快照与回退、新旧事件版本与消费者并存；
- 把剩余耦合拆成四步，每一步可以单独发布（第 4 节）。

v1～v5 的审查记录见文末。与 [Storage 读接口收敛设计](Storage读接口收敛设计.md) 互补：那份收敛读接口，本设计处理写入权限、周期、事件、View 和元数据中的业务概念。

## 0. 原则

1. **Storage 只提供通用能力**：空间、数据集、行、周期、视图、事件。它不认识因子、采集、K 线、某个市场或某个数据集。
2. **权限只看「写入方」**：数据集声明 `write_owner`（允许写入的 AppId 列表），写入、周期报告、清空与恢复只校验调用方是否在其中。
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
| `metadata/dataset_attributes.go:10` | `dataset_role`、`owner_module`、`write_owner`、`source_dataset_id` 等创建后不可修改 | 业务属性被 Storage 锁死，业务模块无法维护自己的属性 |

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
| `view/build.go:345`、`view/maintenance.go:630` | 因子结果 View 的重建走特殊分支（从主库历史重建或先空着等因子写入） | 同上 |
| `view/inventory_reconciler.go:305`、`config/loader.go:130`～`:210` | 数据集 ID 以 `dataset_factor_` 开头的走 `factor` 消费分区（单并发、`max_ack_pending = 1`）；默认分区配置写死 `mooxsys` 的四个主机指标数据集与 `crypto`、`stockcn`、`stockhk` 等空间 | 按数据集 ID 前缀决定投递方式；代码默认值里有具体空间和数据集 |

### 2.4 元数据

| 位置 | 现在 | 问题 |
|---|---|---|
| `catalog/validate.go:123`～`:152` | `dataset_role` 取值校验（`merged_factor` 已退役提示）、因子结果数据集 ID 必须以 `dataset_factor_` 开头 | 元数据校验按业务角色分支 |
| `catalog/factor_result_column.go`、`catalog/validate.go:174`、`catalog/metadata_catalog.go:598` | 列来源 `FACTOR` 的列按 `column_kind`（`output` / `params`）校验 `factor_id`、`member_key`、`output_name`、列名拼接、`__params` 的类型 | 因子列的命名契约由 Storage 校验 |
| `common.proto` `DatasetColumnOriginType` 与 `schema/metadata.sql:625` | 列来源为字段、**因子**、系统 | 因子是一种列来源 |
| `cmd/server/main.go:113` `ResultDatasetResolver` | 按 `dataset_role = factor_result` 与 `source_view_id` 找结果数据集 | 业务分支，而且已无调用方（死代码） |

### 2.5 写死的市场与数据集

| 位置 | 现在 |
|---|---|
| `primarystore/service.go` `normalizeStockCNSeriesTags` | 写死 `stockcn` 空间与 `dataset_stockcn_equity_kline_1m`，把空 `series_tag` 改写成 `default` |
| `packages/storagepolicy/policy.go:183`、`:264` | 内置 stockcn、stockhk、stockus 的默认保留期，并按每天交易分钟数估算根数参与校验 |

## 3. 方案

### 3.1 写入方（第 1 步）

- 数据集属性 `write_owner`：允许写入的 AppId 列表（逗号分隔），精确匹配、没有别名。所有写入类接口只看它：`UpsertFields`、`RegisterSeriesSnapshot`、`CommitTimeSeriesBatch`、周期完成报告、`DeleteDatasetRows`、`RestoreDatasetRows`、`DeleteDataset`。读取不受限制。
- 操作员身份（`moox-cli`、`console`）是唯一的超级用户，不受 `write_owner` 限制（导入、手工修数据）。
- 每个模块只用一个 AppId，删除 `isFactorAppID`、`isOwnedAppID` 的别名规则。实施时先清点各写入方实际使用的 AppId，统一成 `moox-<模块>`。
- 只读身份（如 `scf-market-canary`）不在任何数据集的 `write_owner` 里，自然写不了，删除写死的名字。
- 删除 `collectorWritableDataset`、`factorOwnedResult`、`datasetRowsOwner` 与标记接口里按 `owner_module` / `dataset_role` 的分支；`data_source_id` 对所有数据集都可选，填写时校验引用存在。
- 删除 `dataset_attributes.go` 的不可变约束。`write_owner` 只允许当前 `write_owner` 中的调用方或操作员修改；其余业务属性由业务模块自己维护，Storage 不读取。
- 已有数据集的 `write_owner`：
  - factor-mgr、Collector 的启动对账把自己数据集的 `write_owner` 改成规范值；
  - Monitor 的系统数据集由 `config/setup/metadata.yaml` 补上 `write_owner: moox-monitor`，用现有的 `moox-cli metadata apply` 应用；
  - 发布顺序是先发业务模块与元数据（只补属性，旧 Storage 不读），再发 Storage 的新校验。不写迁移命令。
- 已知限制（不处理）：AppId 是请求体自报、用主库共享密钥签名，`write_owner` 只能防误写、不能防伪造。单用户系统可以接受。

### 3.2 写入方的数据规范与保留策略（第 1 步）

- `normalizeStockCNSeriesTags` 删除：A 股写入方（Collector）自己填写 `series_tag`，Storage 只校验格式。
- `packages/storagepolicy` 只保留通用规则（按空间、频率解析保留期）。各市场的默认保留期移到部署配置（`moox.toml` 的 `[storage_retention.spaces.*]`），删除按交易分钟数估算根数的校验。
- 删除死代码 `ResultDatasetResolver`。

### 3.3 元数据校验与列契约（第 2 步）

- 删除 `dataset_role` 取值校验、`dataset_factor_` 前缀规则。
- 因子列的命名与类型契约（`column_kind`、`member_key`、`output_name`、`__params`）从 Storage 移到 factor-mgr：factor-mgr 建列时自己保证，Storage 只校验通用规则（列名唯一、值类型合法、字段列引用存在）。
- 列来源 `FACTOR` 暂不改名（见第 6 节）：Storage 对它只保留通用含义——数据集自己定义的值列、可以没有字段引用，不再有因子专属校验。

### 3.4 View 由业务模块维护（第 3 步）

- **默认 View 由 factor-mgr 自己建、改、删**：删除 `factor_result_view.go`、数据集激活与加列时对托管 View 的检查、删除数据集时自动删托管 View 的逻辑。factor-mgr 与 Collector 一样用 `CreateView`、`UpsertViewColumn`、`DeleteView`。已有的托管 View 由 factor-mgr 启动对账接管（`managed_by` 改为 `factor-mgr`、补上 `follow_dataset_columns`）。删除因子结果数据集的顺序：删默认 View → 清空行 → 删数据集（`t_views` 外键 `RESTRICT` 要求先删 View）。
- **View 跟随数据集加列**：View 属性 `follow_dataset_columns = true` 时，数据集加列后原地扩展 View，复用现有扩展机制；扩展提交条件、事件出现未扩展列时暂停消费的屏障、`maintenance_schema_extension.go` 的目标判定都改看这个属性，不再看 `primary_dataset_role`。
- **重建**：`build.go`、`maintenance.go` 中"因子结果 View 从主库历史重建、或先空着等写入"的分支，改为看 View 是否声明 `follow_dataset_columns`（主数据集的列由写入方动态增加）；实施时核对这条分支在采集 View 上是否同样适用，适用就直接成为通用行为。
- **投递方式由数据集声明**：数据集属性 `view_delivery`：`parallel`（默认）或 `serial`（单并发、`max_ack_pending = 1`）。删除按 `dataset_factor_` 前缀选分区的逻辑；分区模板改名为 `parallel`、`serial`。factor-mgr 为因子结果数据集设置 `serial`。
  - 现有 durable 名（`storage_view_factor`、`storage_view_misc` 及按数据集动态建的）不改，避免重建消费者、重放历史事件；只改模板的选择方式。
  - 代码默认分区配置里写死的空间与 `mooxsys` 数据集删掉，由部署配置提供。

### 3.5 周期完成统一（第 4 步）

一个周期完成有两种来源，统一成一种事件、一套接口：

| 来源 | 谁判定 | 接口 |
|---|---|---|
| `ledger` | DataNode 按登记的序列快照与截止时间判定（现在的采集周期账本） | `RegisterSeriesSnapshot`、`CommitTimeSeriesBatch`（不变） |
| `producer` | 写入方自己报告（现在的重采样与因子） | `ReportDatasetPeriodCompleted`；查询 `GetDatasetPeriodCompleted` |

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

- 删除 `CollectorPeriodCompleted`、`FactorPeriodComputed` 两种事件，`ReportCollectorPeriodCompleted`、`ReportFactorPeriodComputed`、`GetFactorPeriodComputed` 三个接口，以及 DataNode 对应的三个方法（合并为 `AppendDatasetPeriodCompleted`、`GetDatasetPeriodCompleted`）。
- 因子把每个因子的状态（现在的 `FactorPeriodState`）放进 `detail`，`detail_type = moox.factor.PeriodDetail.v1`；这个 proto 移到新建的 `packages/factorpb`，由因子写入、Strategy 读取；`packages/events` 中的因子状态校验一并移走。
- `ViewDataReady`：删除 `completion_kind`，`factors[]` 改为透传 `detail_type` 与 `detail`，其余字段不变。Strategy 按绑定的 View 与数据集判断事件是否相关，不再按来源区分；`detail_type` 是因子明细时解码。

## 4. 处理顺序

按风险从小到大排成四步，每一步单独发布、单独验收；前一步不完成不影响后一步的设计，但建议按顺序做，后面的步骤会少处理一些分支。

| 步 | 内容 | 涉及模块 | 协议 / 事件变化 | 发布方式 |
|---|---|---|---|---|
| 1 | 写入方 `write_owner`（3.1）；写入方数据规范、保留策略、死代码（3.2） | Storage、factor-mgr、Collector、Monitor 元数据、`packages/storagepolicy`、部署配置 | 无（只有校验与属性） | 先发业务模块与元数据补属性，再发 Storage；不需要暂停 |
| 2 | 元数据校验与列契约（3.3） | Storage、factor-mgr | 无 | factor-mgr 先发（建列时自己保证契约），再发 Storage；不需要暂停 |
| 3 | View 由业务模块维护、跟随加列、投递由数据集声明（3.4） | Storage（view、catalog、metadata）、factor-mgr、部署配置 | 无（View 属性与内部逻辑） | factor-mgr 先发并接管已有托管 View、设置 `view_delivery`，再发 Storage；Storage 重启选在两个 5 分钟边界之间 |
| 4 | 周期完成与就绪事件统一（3.5） | Storage、因子引擎、factor-mgr、Collector（重采样）、Strategy、`packages/events`、`packages/storagepb`、新建 `packages/factorpb`、服务目录 | 有：事件名、接口、`ViewDataReady` 载荷都变 | `moox-cli system pause` 排空 → 全部换装 → `system resume`（与 2026-10-27 通用写入发布相同） |

理由：

- 第 1 步把"谁能写"从业务角色收敛到 `write_owner`，后面三步删除业务分支时都不用再顾及权限；
- 第 2、3 步只动元数据与 View 的内部逻辑，接口不变，可以单独回到上一个二进制；
- 第 4 步改协议和事件，一次性换装所有生产者和消费者，放在最后、在前三步稳定之后做。

### 4.1 第 4 步的发布与数据处理

- **排空**：`system pause` 按下游到上游停下策略、Trade、因子引擎、采集；确认 DataNode outbox 为空、各 View 水位追平、待发布就绪队列为空、各 durable 无积压后再换装。
- **换装**：Storage 三组件、服务目录涉及的网关 / Admin / Monitor / Access、因子引擎、factor-mgr、Collector、collector-stream、Strategy。新事件名对应新主题；因子引擎、Strategy 订阅新事件需要新的 durable（`DeliverNew`），在恢复前随新版本启动建好；旧 durable 不再使用，换装后删除。
- **View 消费者**：View 的消费过滤主题要包含新事件名，换装后执行 `moox-storage-cli reconcile-view-consumers` 核对过滤范围。
- **DataNode 里旧格式的完成标记**：不转换。新代码只读新格式；排空后已发布的旧标记只被"按触发事件查询本期冻结全集"用到（因子截面补算、幂等查询）。新版本上线前的周期如果之后发现空洞，自动截面补算取不到冻结全集会放弃，需要时用 `recalc` 手工补算。项目未上线，可以接受。
- **恢复**：`system resume`；栅栏处按惯例先 `--release-partial --yes` 放行因子引擎与 Trade，因子补完暂停区间后再 `resume` 放行策略。
- 不做回退方案：出问题时修代码重新发布。

### 4.2 各步对上层模块的改动

| 模块 | 第 1 步 | 第 2 步 | 第 3 步 | 第 4 步 |
|---|---|---|---|---|
| factor-mgr | 对账把结果数据集的 `write_owner` 改为规范 AppId | 建列时自己保证因子列契约 | 自己建、改、删默认 View（`follow_dataset_columns = true`），接管已有托管 View；结果数据集设置 `view_delivery = serial`；删除数据集按 3.4 的顺序 | — |
| 因子引擎 | 使用规范 AppId | — | — | 报告改为 `ReportDatasetPeriodCompleted`（`detail` 为 `factorpb.PeriodDetail`）；幂等查询改为 `GetDatasetPeriodCompleted`；触发改为订阅 `DatasetPeriodCompleted`，按绑定的源数据集与频率筛选，两种来源都接受 |
| Collector / collector-stream | 对账规范 `write_owner`；A 股写入自己填 `series_tag` | — | — | 重采样的周期报告改为 `ReportDatasetPeriodCompleted` |
| Strategy | — | — | — | 订阅新的 `ViewDataReady`；按绑定的 View 与数据集判断相关性，不再按 `completion_kind`；因子明细从 `detail` 解码 |
| Monitor | `metadata.yaml` 中系统数据集补 `write_owner` | — | — | — |
| packages | `storagepolicy` 去掉市场 | — | — | `events` 换注册表、删因子校验；新建 `factorpb`；`storagepb` 同步 |
| 部署配置 | `moox.toml` 补各市场保留期 | — | View 消费分区配置补空间与数据集 | — |
| 服务目录 | — | — | — | PrimaryStore 方法改名与删除；外部身份白名单同步（需整体发布） |

## 5. 测试

- 第 1 步：只有 `write_owner` 中的 AppId 能写（数据行、周期账本、标记、清空与恢复、删除数据集）；没有别名；操作员身份不受限；只读身份写入被拒；`write_owner` 只能由当前所有者或操作员修改；factor-mgr、Collector 对账与 `metadata apply` 后已有数据集都有规范的 `write_owner`；A 股写入由 Collector 填 `series_tag`；保留期从部署配置读取。
- 第 2 步：factor-mgr 建出的因子列满足契约；Storage 不再拒绝非因子命名的自定义列；多输出因子与 `__params` 照常可建。
- 第 3 步：factor-mgr 建、改、删默认 View；接管已有托管 View；加列后立刻写入新列、扩展还没完成时新列不丢、不提前发就绪；`serial` / `parallel` 按数据集属性生效、durable 名不变；只有默认 View 时因子结果数据集能完整删除。
- 第 4 步：`ledger` 判定与现在的账本一致；`producer` 报告与查询一致；`detail` 原样透传到 `ViewDataReady`；重采样结果（`producer`）→ 因子计算 → Strategy 触发；采集 → 账本完成 → 因子 → View 就绪 → Strategy 全链路；暂停区间由恢复追赶补齐。

## 6. 待确认

- **列来源 `FACTOR` 是否改名为 `CUSTOM`**：改名要改 `schema/metadata.sql` 的 `CHECK`，并改写已存列定义里按名字序列化的枚举（`c_attrs_json`），按用户原则就是全新部署元数据库——空间、数据集、View、标的、标签都要重建，因子机与 Storage 里的长历史也要重新导入。本设计建议**暂不改名**：第 2 步之后 Storage 对它已经没有因子专属逻辑，名字只是历史遗留。等哪天需要全新部署时顺手改。
- **`write_owner` 是否改用网关验证过的身份**：单用户系统不需要防伪造，不做。

## 7. 审查记录

v1～v5（2026-10-17）：吸收 Codex 五轮评审，主要涉及 `source` 的含义、`write_owner` 可授权修改、`commit_id` 限定单数据集、列来源、事件全部升版本与新旧消费者隔离、静默点切换与回退、周期完成标记迁移。其中迁移命令、静默点快照与回退、新旧版本并存在 v6 按用户原则删去；`commit_id` 与列来源唯一约束已分别在通用写入（2026-10-27）与元数据 v14 完成。
