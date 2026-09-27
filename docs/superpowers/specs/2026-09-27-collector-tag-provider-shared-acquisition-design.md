# 采集任务标签来源绑定与跨任务共享采集设计

## 背景

当前标签的 `Tag.sources` 是多值配置，主要服务于自动同步或探测标签成员；采集任务另外保存 Provider。调度器按任务分别解析标签成员，并把每个采集项直接绑定到一个结果 Dataset。因此，当多个任务的标签范围有交集时，同一 Provider、标的、频率和目标时间可能被重复请求。

现有结果身份也由标签、任务类型和频率参与生成。随着一个任务可以绑定多个标签、一个标签固定绑定一个 Provider，以及一次采集需要写入多个任务 Dataset，结果身份和采集执行身份需要分开建模。

## 目标

1. 一个标签只绑定一个固定数据源；标签成员列表与成员详情使用该来源，不再维护两套来源配置。
2. 采集任务只绑定一个或多个标签，Provider 从标签来源推导，不能在任务表单中覆盖。
3. 一个采集任务对应一个结果 View；一个任务可以包含多个标签。
4. 一次表单提交包含多个 Provider 的标签时，按 Provider 自动分组，为每组创建独立采集任务。
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
- **Tag**：一个 Subject 集合及其固定 DataSource。任务通过 Tag 确定标的范围和 Provider。
- **CollectionTask**：一个可独立启停、配置采集方法、参数和标签范围的任务。一个任务只对应一个 Provider，可包含同 Provider 的多个标签，并拥有独立结果 Dataset 和 View。
- **AcquisitionInstance**：调度器将所有启用任务展开后，按采集语义合并得到的一次 Provider 请求，不属于任何单一 CollectionTask。
- **WriteTarget**：一次采集结果需要落入的目标任务 Dataset。一个 AcquisitionInstance 可关联多个 WriteTarget。

## 数据模型

### Tag

将 `Tag.sources`（repeated）收敛为 `Tag.source`（single），并使用单数 `source` / `source_id` 语义贯穿 API、前端、存储层和同步器。

约束：

- 新建或更新标签必须提供一个有效来源；不允许空值或多个来源。
- 自动标签的成员同步、手工标签的有效性探测、成员详情查询都使用该来源。
- 标签来源创建后不可由采集任务覆盖。修改标签来源应触发标签重新同步；页面需先列出关联任务及影响，再允许管理员确认修改。
- 任务创建时，任一所选标签缺少来源、来源已停用或来源未映射到可用 Collector Provider，整体校验失败并列出标签名称和缺失原因。

`Tag.source` 表示单一权威来源，不再表达多个列表源合并。

### CollectionTask

- 保存 `tag_ids` 作为任务采集范围的权威配置；结果 Dataset 的 `subject_tags` 是供查询和存储读取的镜像，不作为任务配置的唯一来源。
- `provider` 由所选标签的 `source` 推导并持久化为运行快照；任务执行期间不重新解释成用户可覆盖参数。
- 一个任务内所有标签必须绑定同一个 Provider。跨 Provider 选择由任务创建工作流分组后生成多个任务。
- 任务的采集方法、频率、输出字段及其他方法参数保持任务级配置。
- 标签范围及其 Provider 对已创建任务视为身份配置；需要变更时创建新任务，避免任务身份、View 和调度实例漂移。

### CollectionTask ID 与结果 View

- `task_id` 由服务端生成 6 位小写字母和数字，字符集为 `[a-z0-9]`。
- 使用密码学安全随机源生成候选 ID，并在同一 Space 内检查任务 ID / 结果资源冲突；冲突时重新生成，达到有界重试次数仍冲突则返回清晰错误。
- Storage / Collector 数据库必须保留唯一约束作为并发保护。生成 ID 与任务记录创建的最终唯一性由数据库约束兜底。
- 一个任务只创建一个结果 View，格式为 `view_{taskID}_{taskType}_{frequency}`，例如 `view_a7k2m9_kline_1m`。
- View ID 不再包含 Tag ID。Dataset ID 继续任务级隔离；不同任务即使标签、Provider、方法和频率相同，也拥有各自 Dataset / View。
- 因 View ID 改为随机任务身份，同一标签集合、Provider、任务类型和频率的重复任务检测应独立于 View ID。完全相同的任务定义应拒绝重复创建；存在部分标签重叠的不同任务允许创建，以支持下文的共享采集。

### AcquisitionInstance 与 WriteTarget

建议将“Provider 请求实例”和“目标写入关系”分成两类持久化记录：

- `AcquisitionInstance`：保存稳定采集键、Provider、市场、标的、产品类型、采集方法、频率、目标时间/时间范围、请求参数、执行状态及实际 Provider 响应摘要。
- `WriteTarget`：保存采集实例 ID、目标 CollectionTask ID、Dataset ID、View ID、该目标的输出字段配置、写入状态、错误信息和重试信息。
- 一个 AcquisitionInstance 可关联 1..N 个 WriteTarget；一个 CollectionTask 的一个标的/频率/目标时间对应一个 WriteTarget。
- 写入状态按目标记录，不能用单个采集实例状态掩盖部分成功。例如 Dataset A 成功、Dataset B 失败时，A 保持成功，B 可重试。
- 结果行以目标 Dataset ID 构造独立 RowKey。向多个 Dataset 写入时按目标分别发起 Storage upsert；目标间互不覆盖。

实现阶段应先检查当前 `TaskInstance`、`BatchInvocation` 和 `RetryItem` 的职责，再决定是扩展现有表，还是新增表。需要保存稳定采集键、目标映射、每目标状态和重试信息；具体物理表名由实现计划确定。

## 创建任务流程

1. 用户选择一个或多个标签、执行任务类型、方法参数、频率、输出字段及范围内其他配置。
2. 服务端加载所有标签及其来源，校验标签有效、每个标签恰有一个可用来源、成员范围非空。
3. 按 Provider 对标签分组。每个 Provider 组创建一个 CollectionTask；同组标签放在同一任务中。
4. 在写入任何任务前，完成全部分组的来源、方法参数、频率、View 身份和任务重复性预校验。
5. 每个实际生成的 CollectionTask 分配独立 6 位 task_id、独立结果 Dataset 和一个 View。
6. 若多 Provider 分组中的某组写入失败，返回已成功任务和失败分组的完整明细；允许用户针对失败组重试，不删除已成功组。跨 Storage 与 Collector 的创建不是单一数据库事务，因此 UI 必须呈现部分成功状态。
7. 不允许出现“选中的某个标签被静默跳过”的成功结果。

## 全局采集分组与去重

每轮调度先展开所有启用 CollectionTask：读取任务绑定标签，解析有效成员，为每个成员构造目标写入关系。再将请求按采集语义分组。

**采集去重键**至少包含：

- Space；
- 固定 Provider / DataSource 及必要的来源配置版本；
- 采集方法和数据类型；
- 市场、产品类型；
- Subject ID 与 Provider Symbol；
- Frequency；
- 目标数据时间，或完整时间范围；
- 会改变 Provider 返回数据的参数，例如历史策略、bar limit、对齐和请求模式。

以下属性不属于采集去重键：

- CollectionTask ID；
- Dataset ID / View ID；
- Tag ID（标签只用于解析成员范围）；
- 输出字段集合（采集结果先保留标准化字段，再逐目标投影）。

因此，任务 1 包含 tag1、tag2，任务 2 包含 tag2、tag3 时，tag2 展开的相同 Subject 请求只生成一个 AcquisitionInstance；该实例关联两个任务对应的 WriteTarget。tag1、tag3 的独有标的各自产生请求。

若两个任务对同一 Provider / Subject 使用不同频率、时间点、历史范围或其他影响响应的参数，则生成不同 AcquisitionInstance。若 Provider 不同，即使 Subject 相同也不合并。

## 执行、扇出与重试

1. 每个 AcquisitionInstance 调用 Provider 一次，归一化为 Collector 内部标准行情结果。
2. 为每个 WriteTarget 单独套用输出字段投影，并生成包含目标 Dataset ID 的 RowKey 后写入。
3. 每个目标的 upsert 使用稳定行键，重复写入幂等。
4. 若 Provider 请求失败，按原采集实例重试；若部分目标写入失败，只将失败目标标记为待重试。重试时允许重新请求 Provider，再将结果仅扇出到待重试目标。
5. 不持久化 Provider 原始响应用于跨重试复用。由此，同一正常调度轮次的成功路径去重；网络错误、进程退出或 Storage 写入失败后可能发生重复 Provider 请求。
6. 采集实例的可观测状态同时展示请求结果和目标扇出摘要，例如 `fetch=success, targets=2/3`；目标失败需能定位到 Task、Dataset 和错误。

## 输出字段

- 字段目录继续展示当前空间的全部启用字段；可按需求配置每个目标任务的输出字段。
- 共享采集请求不按某个任务的字段选择裁剪 Provider 返回内容；每个 WriteTarget 独立投影，避免目标 A 的字段选择影响目标 B。
- 某目标选择了当前采集方法无法产出的字段时，服务端必须返回明确字段错误，不得静默丢弃选择或报告完整成功。
- 空输出字段集合表示使用该采集方法的默认输出字段。
- 当前各采集方法对任意字段值的映射能力需要按方法契约实现；字段目录“可见/可选”与采集方法“可产出”是两个不同能力，不得将未映射字段伪装成已写入结果。

## 标签来源迁移

当前 `Tag.sources` 允许多个值，历史标签可能绑定多个成员列表来源。迁移不得简单取第一项：

- 单来源标签可直接迁移为 `Tag.source`。
- 无来源标签保留为空，但禁止其参与新建采集任务，页面提示补齐。
- 多来源标签必须由管理员明确选定一个权威来源后再迁移；迁移报告列出标签及旧来源列表，未完成映射的标签保留阻止状态。
- 迁移验证成员列表及详情确实能由选定来源提供；如果旧模型依赖多源并集，该标签在新模型下的成员集合可能变化，应提供迁移前后差异清单。
- API 字段改名涉及 proto、生成代码、SQLite JSON 列、种子配置、CLI、标的同步器和前端，需要提供兼容读取窗口或一次性数据库迁移；写入统一使用单数 `source`。

## 错误处理与一致性

- 标签来源缺失、停用或 Provider 映射失败：拒绝创建对应任务分组，并指出标签和来源。
- 一个来源组内标签成员解析为空：拒绝创建该组，不生成空任务。
- 共享请求失败：所有关联 WriteTarget 记录 fetch 失败；不得只更新其中某个任务状态。
- 单目标写入失败：保留其它成功目标状态，只重试失败目标。
- 某任务被禁用或删除：在下一轮规划中移除它的 WriteTarget，不影响其它任务对同一 AcquisitionInstance 的目标引用。
- 相同稳定采集键只能有一个活动实例；并发调度由数据库唯一键或等效租约机制仲裁。
- 采集执行记录与目标映射必须保留足够时间，覆盖批次回调、目标重试和任务实例详情查询。

## 兼容与升级

- 已有任务的 Provider 与标签来源若一致，迁移后维持行为。
- 不一致任务需显式列出；不能静默把旧 Provider 替换成 Tag.source。建议创建前校验并输出差异报告，按标签来源重建相应任务。
- 旧 View ID 保持可读和可访问；新任务使用 6 位 Task ID 生成新 View ID，不自动重命名旧 View。
- 历史任务结果中的 persisted Dataset / View ID 继续作为权威身份，升级后查询、删除和回填都使用已保存 ID。
- Task ID 新格式只用于新生成任务；内部固定/内置 task_id 可保留旧格式，除非单独迁移。

## 验收标准

- Tag API 与页面只能读写单个 source；缺失 source 的标签不能创建采集任务。
- 多 Provider 标签一次提交后按 Provider 分组生成任务；同 Provider 多标签留在一个任务中；部分失败能准确展示成功与失败分组。
- 新生成 task_id 恰为 6 位 `[a-z0-9]`；并发冲突时不会生成重复 task_id。
- 每个任务恰有一个 `view_{taskID}_{taskType}_{frequency}` View，Dataset 和 View 不因标签重叠而共用。
- 同一轮计划中，跨任务相同采集键只生成一个 Provider 请求；不同 Provider、频率或目标时间分别请求。
- 一个共享请求能写入多个 Dataset，目标行的 Dataset ID、输出字段、View 关系各自正确。
- 某一个目标写入失败时，其它目标成功状态不回滚；重试仅处理失败目标，可再次抓取 Provider。
- 任务 ID 冲突、目标 Dataset 写入失败、Provider 请求失败、部分扇出失败和重复调度均能恢复且状态可观测。
- 多源旧标签迁移前有差异报告，未明确来源的标签不会被自动选源。
