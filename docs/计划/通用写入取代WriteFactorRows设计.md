# 通用写入取代 WriteFactorRows 设计

状态：设计稿 v1（2026-10-27），待 Codex 审查，未编码。项目尚未上线，协议、事件、存储格式直接修改，不考虑兼容与迁移。

## 0. 目标

因子结果与其他时序数据在数据层面没有区别（同样的行键、字段 KV、行变更事件、View 消费）。`WriteFactorRows` 多出来的是几项写入约定，这些约定本身是通用能力。本设计把它们并入 `UpsertFields`，删除 `WriteFactorRows` 两层接口，因子引擎改用 `UpsertFields` 写结果。

要求保留的能力：

1. 按提交 ID 幂等：同一提交内容相同返回成功，内容不同返回冲突。
2. 一次写入只占一个 outbox 位置（一期一条行变更事件），之后的完成标记一定排在它后面。
3. 校验：行必须属于请求声明的数据集，字段必须是数据集的 active 列且类型匹配，同一行字段不重复，同一请求行键不重复；允许显式 NULL 覆盖旧值。
4. 准备阶段在 `outboxMu` 之外（O9 的收益不能丢）。

不在范围内（见第 6 节）：`source_event_id` 与 `commit_id` 合并、`CommitTimeSeriesBatch` 的改造、`write_owner` 权限模型改造、`normalizeStockCNSeriesTags`。

## 1. 现状对比

| 项 | `WriteFactorRows` | `UpsertFields` |
|---|---|---|
| 数据集范围 | 请求顶层 `space_id` + `dataset_id`，行必须一致 | 行可以跨数据集，主库按数据集拆开逐个下发，失败时部分成功 |
| 幂等 | `commit_id` + 内容指纹回执（`commit_receipts.go`）：同内容成功、不同内容 `CommitConflictError`；回执全局键、永不过期，删数据集时全表扫 | 可选 `source_event_id`：按（来源 ID，数据集）记"已处理"标记，已处理即跳过、不比内容；按时间索引 7 天清理 |
| outbox | 一次写入一条事件，事件 ID 为 outbox 位置 | 每个数据集一条事件；带来源 ID 时事件 ID 由来源 ID 派生 |
| 准备与锁 | O9 后准备在 `outboxMu` 外 | 无来源 ID：准备在锁外；有来源 ID：整条路径在锁内 |
| 校验 | 主库：调用方必须是 factor；数据集 `dataset_role/owner_module/write_owner` 必须是 factor、状态 active；字段必须是 active 列；不允许行属性；字段非空。数据节点：行键不重复、字段不重复 | 主库：`ValidateRows`（数据集 active、行键类型、频率、字段已登记且为 active 列、类型匹配）；`validateDatasetWriteOwner`（`dataset_role = factor_result` 一律拒绝并要求走 `WriteFactorRows`；`write_owner = factor` 只许因子写）。数据节点：字段不重复、行至少一个字段或属性 |
| 变更日志 | 不记 | 记（时序行） |
| 事件字段 | `write_kind = factor_result` | `write_source`（调用方自报） |
| 服务目录 | factor-mgr 身份可调 | console、moox-cli、collector、collector-stream、monitor 可调 |

`write_kind` 只被 `packages/events` 校验格式，没有任何消费方读取；`privilegedWriteSource` 只为阻止别的调用方自报 `write_source = factor_result` 冒充因子写入而存在。

现有 `UpsertFields` 调用方都是单数据集写入：Monitor 主机指标按数据集分组逐个写；Collector 的 `kline_committer` 逐数据集写；CLI `import` 与 Web 数据导入页一次只写一个数据集。

## 2. 设计

### 2.1 协议

`PrimaryUpsertFieldsReq` 与 DataNode `UpsertFieldsReq` 改为单数据集请求：

```protobuf
message PrimaryUpsertFieldsReq {
  common.AuthInfo auth_info = 1;
  string space_id = 2;
  string dataset_id = 3;
  repeated RowFieldUpsert rows = 4;
  // 可选。同一数据集内同一 commit_id 只生效一次：内容相同返回成功，内容不同返回 CONFLICT。
  string commit_id = 5;
  // 可选。同一数据集内同一来源事件只处理一次：已处理即返回成功，不比较内容。与 commit_id 互斥。
  string source_event_id = 6;
  string write_source = 7;
}
message PrimaryUpsertFieldsRsp {
  common.RetInfo ret_info = 1;
  repeated RowKey keys = 2;
}
```

DataNode `UpsertFieldsReq` 同样加 `space_id`、`dataset_id`、`commit_id`（保留 `node_id`）。

- `space_id`、`dataset_id` 必填，每一行的键必须与之一致，否则 `INVALID_PARAM`。主库不再按数据集拆分、不再有"部分成功"。
- `commit_id` 与 `source_event_id` 同时出现时返回 `INVALID_PARAM`。
- 响应 `keys` 始终是请求中全部行的规范化键（包括重复提交命中、来源事件已处理的情况），调用方用它核对行数。
- 删除 `PrimaryWriteFactorRowsReq/Rsp`、`WriteFactorRowsReq/Rsp` 与两层 `rpc WriteFactorRows`。
- 删除 `DatasetRowsUpserted.write_kind`（`packages/storagepb/storage_events.proto`）、`RowsUpserted.write_kind`（`rows.proto`）及 `packages/events` 中的校验。

### 2.2 主库流程（`primarystore/service.go` 的 `UpsertFields`）

1. 参数：`space_id`、`dataset_id`、`rows` 非空；`commit_id`、`source_event_id` 互斥；各自格式校验沿用现有规则（`commit_id` 用 `validateCommitID`）。
2. 鉴权：`authorizeRequest`；只读凭据拒绝（现有逻辑）。
3. 行的数据集一致性：每行 `key.space_id/dataset_id` 等于请求顶层。
4. `normalizeStockCNSeriesTags`（不在本设计范围，原样保留）。
5. 写入方：`validateDatasetWriteOwner` 删除 `dataset_role = factor_result` 要求走 `WriteFactorRows` 的分支，保留 `write_owner = factor` 只许因子身份写的规则。因子结果数据集创建时都带 `write_owner = factor`（`WriteFactorRows` 现在就强制这一点），所以权限不变：只有因子能写因子结果。
6. 校验：`ValidateRows`（O2 的请求内缓存校验器）。它已覆盖"数据集 active、行键类型、频率、字段为 active 列、类型匹配、显式 NULL 合法"，因子路径独有的 `factorResultColumnNames` 与重复的 active 检查删除。
7. 路由到数据集所在的数据节点，原样下发 `space_id/dataset_id/commit_id/source_event_id/write_source`。
8. 删除 `rejectUnauthorizedUpsertSemantics` / `privilegedWriteSource`（`write_kind` 删除后它不再保护任何东西）。

因子路径原来还拒绝行属性、要求每行有字段：前者是因子的业务约束，通用写入允许属性，因子引擎本来就不写属性，不再单独限制；后者由数据节点"至少一个字段或属性"覆盖。

### 2.3 数据节点流程（`pebble`）

一个入口，三种模式（无幂等键 / `commit_id` / `source_event_id`）共用：

```
UpsertFields(ctx, spaceID, datasetID, rows, commitID, sourceEventID, writeSource):
  datasetWriteMu.RLock
  normalized := normalizeWriteRows(rows)               // 规整、validateUpsert（字段不重复等）
  if commitID != "":
      要求行键不重复；fingerprint := 规范化排序后的内容摘要
  prepared := prepareFieldsWrite(...)                  // 锁外：编码 KV、构造并校验事件
  outboxMu.Lock
  if 数据集已删除: ErrDatasetDeleted
  if commitID != "":
      已有 commit 标记：摘要相同 → 返回成功；不同 → CommitConflictError
  if sourceEventID != "":
      已有来源标记 → 返回成功（与现在一致）
  分配 outbox 编号、写事件、写变更日志、写幂等标记（若有）→ 提交
```

- 单数据集请求以后，"一次请求里部分数据集已处理"的情况不存在，带来源 ID 的写入也可以在锁外准备，原来整条路径在锁内的分支删除（顺带解决 O9 时搁置的方案 B）。
- 重复提交与"已处理"命中时，锁外准备好的 batch 直接丢弃，代价只是白做一次准备。
- 事件 ID：`commit_id` 写入与无幂等键写入用 outbox 位置（与现在的因子写入一致）；`source_event_id` 写入保持由来源 ID 派生（现有行为，不改）。
- O9 的分段计时日志从 `factor_rows.go` 移到这个通用入口（阈值 50 ms，日志改名为"storage node 写入"，带模式与数据集）。

### 2.4 幂等标记统一存放

`commit_id` 标记放进现有的"已处理来源事件"键空间，删除 `commit_receipts.go` 的全局回执：

- 键：`processedEventPrefix / space / dataset / <种类>:<sha256(ID)>`，种类为 `source` 或 `commit`，两种 ID 不会串号。
- 值：创建时间（与现在相同）；`commit` 标记再加 32 字节内容摘要。
- 过期：沿用现有时间索引（`processedSourceEventTimeKey`）与 relay 里的定期清理，保留期同为 7 天（`ProcessedEventRetention`）。
- 删除数据集：沿用按数据集前缀清理已处理标记的逻辑，`cleanup.go` 里扫描全局回执的代码删除。

7 天远大于引擎的重试窗口（同一期的重试在几分钟内结束）。超过保留期再用同一 `commit_id` 写，会作为新写入再写一次——因子的实时 `commit_id` 是内容摘要、补算带盐，内容相同，结果不变。

### 2.5 变更日志

推荐：所有时序行写入一律记变更日志，不再按接口区分（因子写入原来不记）。

- 代价：一条记录按（标的，频率）合并时间范围，501 个标的约 30 KB。当前 5 个 5m 因子任务每天 1440 条，约 43 MB/天，7 天保留约 300 MB；恢复两个 1m 任务后再加约 2 倍。
- `ReadDatasetChanges` 按全局序号扫描、逐条反序列化后按数据集过滤，因子机每次拉数多扫因子数据集的条目：每 5 分钟约 150 KB，按现在 `factor_live_load` 的 `bounds` 15 ms 量级估算，增加不到 1 ms。
- 目前没有消费方读取因子结果数据集的变更日志（因子机本地副本只订阅源 K 线数据集），多出来的条目只占空间，不会改变任何读取结果。
- 备选：在数据集元数据上加一个"不记变更日志"的属性，由主库下发。增加一项元数据语义，暂不采用；若空间成为问题再加。

### 2.6 因子引擎

- `modules/factor/internal/storageio`：`WriteRows` 改调 `PrimaryStore.UpsertFields`（`space_id`、`dataset_id`、`commit_id`、`rows`），网关方法名改为 `UpsertFields`；行数核对改为 `len(rsp.Keys) == len(rows)`。
- 冲突错误码沿用 `CONFLICT`（数据节点 `errorCode` 已把 `CommitConflictError` 映射为它），引擎对它的处理不变；其余错误按现有重试逻辑。
- `pipeline`、`recalcexec` 里的 `RowsWritten` 统计保留（取 `len(keys)`）。

### 2.7 其他调用方

| 调用方 | 改动 |
|---|---|
| Monitor `hostmetrics/storage_writer.go` | 请求加 `space_id`、`dataset_id`（已按数据集分组） |
| Collector `marketstorage/storage.go` | `UpsertFields` / `UpsertFieldsWithSource` 加顶层数据集；调用方本来就逐数据集写，接口里取第一行的数据集并校验一致 |
| CLI `import.go` | 请求加顶层数据集 |
| Web `api/storage/access.ts`、`views/data/import/index.vue` | `upsertFields(spaceId, datasetId, rows)` |
| Storage `cmd/server/main.go` 的代理 | 透传，无逻辑改动 |

### 2.8 服务目录（`packages/servicecatalog/catalog.yaml`）

- PrimaryStore 的 `methods` 与 moox-cli 的 ACL 删除 `WriteFactorRows`。
- factor-mgr 调用方的 ACL 把 `WriteFactorRows` 换成 `UpsertFields`。
- 目录变化后 `native-release.sh catalog-changed` 返回 10，按发布手册整体发布。

### 2.9 删除清单

- `modules/storage/internal/service/primarystore/factor_rows.go`、`write_semantics.go`
- `modules/storage/internal/service/datanode/pebble/factor_rows.go`、`commit_receipts.go`（逻辑并入通用写入）、`WriteKindFactorResult`
- 数据节点 `Service.WriteFactorRows`、主库 `factorRowsDataNodeClient`
- `BuildDatasetRowsUpsertedMessageWithKind` 与 `write_kind` 相关构造参数
- 因子 `storageio` 的 `WriteFactorRows` 客户端方法
- 测试按新接口改写（见第 4 节），只为旧接口存在的测试删除

## 3. 风险与取舍

- **冲突检测保留**：因子的实时 `commit_id` 是内容摘要、补算带盐，理论上不会出现"同 ID 不同内容"，但它能在引擎或序列化出 bug 时立刻暴露问题，代价只是标记里多存 32 字节，保留。
- **`commit_id` 与 `source_event_id` 不合并**：两者语义不同。Collector 在 `WriteIDByContent = false` 时，同一来源事件重投会重新抓取，内容可能变化，需要"首次生效、重复跳过"；因子需要"同 ID 不同内容即报错"。强行合并，要么采集被误判冲突，要么因子失去冲突检测。《Storage 去业务耦合设计》2.2 计划让 `UpsertFields`、`CommitTimeSeriesBatch` 只保留 `commit_id`（内容不同即冲突），没考虑采集重投会改变内容，实施那一步时需要先解决这一点。
- **单数据集约束**：现有调用方都已是单数据集写入；跨数据集写入的原子性本来就不存在（主库逐个下发、部分成功），收窄后语义更清楚。
- **重复行键**：只对 `commit_id` 写入强制（指纹需要唯一键）；其他写入保持现状（后写覆盖先写），避免 Web/CLI 导入的历史文件突然被拒。

## 4. 测试

- 数据节点：`commit_id` 同内容重复提交成功且只有一个 outbox 条目；不同内容返回冲突；并发同一 `commit_id` 只提交一次、编号连续（O9 的测试改写）；`source_event_id` 重投跳过；两者互斥报错；`commit_id` 写入重复行键报错；显式 NULL 覆盖；commit 标记过期清理与删除数据集清理；带来源 ID 的写入准备在锁外（与 O9 同样的并发测试）。
- 主库：请求顶层数据集与行不一致报错；因子结果数据集只许因子身份写；字段不是 active 列报错；`write_kind` 删除后的事件校验。
- 因子引擎：`storageio` 改用 `UpsertFields` 的请求构造、行数核对与冲突处理。
- 其他调用方：Monitor、Collector、CLI 的请求带顶层数据集；Web 类型检查。
- 现有 `factor_rows_test.go`（两层）、`cleanup_test.go` 的回执用例按新接口改写。

## 5. 发布

接口不兼容（旧引擎调用的 `WriteFactorRows` 被删除，新主库要求顶层数据集），服务目录变化要求整体发布。顺序：

1. 暂停窗口选在一个 5 分钟边界、本期全部任务算完之后（约 B+10 秒）。
2. Storage 三个组件（同一份二进制，view → node → primary）。
3. 控制机：host-gateway/Access 的服务目录、collector、monitor、factor-mgr、Web 及其余组件按整体发布手册。
4. 因子引擎（因子机），`compute.workers: 32` 保留。
5. 验收：
   - 重启前一期到重启后第 3 期，5m 任务都有 `factor_period_done status=complete`；若有一期缺失，连续水位 15 分钟内追平。
   - `doctor alerts` 当前告警 0（Storage 重启后 1h 数据集的 producer stale 在下一个整点后消失）。
   - 采集：K 线周期提交、Monitor 主机指标写入正常；Web 数据导入页导入一个小文件。
   - 数据节点"storage node 写入"日志：因子写入等锁仍在 0～十几 ms。
   - 用同一 `commit_id` 手工重放一期（相同内容）返回成功、outbox 不增加。

## 6. 后续（不在本设计）

- `source_event_id` 与 `commit_id` 是否、如何合并（先解决采集重投内容变化的问题）。
- `CommitTimeSeriesBatch` 的周期批次在 `outboxMu` 内准备，若成为锁等待再处理。
- `write_owner` 精确匹配、去别名等权限改造（《Storage 去业务耦合设计》2.1）。
