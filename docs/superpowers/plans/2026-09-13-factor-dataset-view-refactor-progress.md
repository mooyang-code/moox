# 复合因子数据集与单数据集 View 重构进度

基线分支：`feature/mooyang`
基线 HEAD：`34a963d6`（开始实施时）

## 任务 01：协议与状态身份

状态：**已完成（代码与定向测试）**。部署、真实新周期 E2E、codeCR 仍属任务 18。

### 红灯证据

`packages/events` 中 `TestViewDataReadyContract` 最初失败于：

- 旧事件 `event.storage.view.source_subject.ready` 仍注册
- 新事件 `event.storage.collector.period.completed` 未注册

失败来自目标行为缺失，不是编译环境缺失。

### 实现

- 定义 `CollectorPeriodCompleted`、`MergePeriodCompleted`、`ViewDataReady`
- 统一完成标记：`batch_id`、`config_snapshot_id`、`dataset_id`、可读 `expected_scope_ref` + `universe_subject_ids`、状态、`committed_positions`
- `ViewDataReady` 关联 `completion_event_id`、`view_id`、`view_config_id`、dataset 范围与提交位置
- 删除 `DatasetPeriodCollected`、`ViewSourceSubjectReady`、`ViewSourcePeriodReady`、`ViewFactorPeriodReady` 及全部调用方，不留别名
- Storage marker RPC 同步为 `Report/AppendCollectorPeriodCompleted` 与 `Report/AppendMergePeriodCompleted`
- Factor 不再消费 subject-ready；时序/截面等待后续任务接 Dataset 变更与 ViewDataReady 屏障

### 验证命令与结果

```text
go test ./... -run 'TestViewDataReadyContract' -count=1
go test ./... -count=1
go test ./... -race -count=3
go test ./internal/... -count=1
CGO_ENABLED=1 go test ./internal/service/view -run 'TestHandle|TestViewDataReady|TestPeriod' -count=1
CGO_ENABLED=1 go test ./internal/trigger/... ./internal/rpc/... ./internal/storageio/... ./internal/bootstrap/... -count=1
go test ./internal/trigger/... ./internal/compiler/... ./internal/config/... ./internal/bootstrap/... -count=1
go test ./internal/service/sysdeploy/... ./cmd/cli/... -count=1
```

### 尚未解决的依赖

- Collector 上报的 `committed_positions` 仍可能是本地占位，真实 DataNode outbox 位置绑定属于任务 03/05
- ViewDataReady 尚未按多分区 applied 位置做可读屏障，属于任务 04
- 主体 ID 去 `-SPOT`/`-SWAP` 属于任务 05
- Merge 程序、mdataset、本机引擎部署与真实 E2E 属于 06—18

### 提交

`1e2ec2c5`

## 任务 02：Storage 单 Dataset View 模型

状态：**已完成（代码与定向/回归测试）**。部署、真实新周期 E2E、codeCR 仍属任务 18。

### 红灯证据

`modules/storage` 中 `TestSingleDatasetView` 最初失败于：`multiple source datasets are rejected`（多源列仍被接受）。失败来自目标行为缺失。

### 实现

- View proto/SQL 收敛为唯一 `c_dataset_id` / `dataset_id`；schema 版本 10→11
- 同 Dataset 可建多个投影 View；列 origin 指向其他 Dataset 时拒绝
- 删除一个 View 不删除 Dataset 或另一个 View
- 运行时只按所属 Dataset 路由事件，移除 JOIN/多源 enrich
- `WaitViewSyncPoint.dataset_ids` 保留为同步点 RPC，不是 View 模型
- 前端去掉「包含数据集」多选，创建/编辑只提交单个 `dataset_id`

### 验证命令与结果

```text
env CGO_ENABLED=1 go test ./internal/... -run 'TestSingleDatasetView' -count=1
env CGO_ENABLED=1 go test ./internal/service/view ./internal/service/catalog ./internal/service/metadata/sqlite ./schema -count=1
env CGO_ENABLED=1 go test -race ./internal/service/view ./internal/service/catalog ./internal/service/metadata/sqlite ./schema -count=3
go test ./internal/command/ -run 'TestSetupInit|TestMetadata|TestDefaultSetup' -count=1
pnpm test
```

上述命令均 PASS。

### 尚未解决的依赖

- ViewDataReady 多分区 applied 屏障属于任务 04
- 主体 ID 去尾缀、Merge、引擎隔离与正式发布属于 05—18

### 提交

`bd680dee`

## 任务 03：基础输入提交和字段写权限

状态：**已完成（代码与定向/回归测试）**。部署、真实新周期 E2E、codeCR 仍属任务 18。

### 红灯证据

`modules/storage` 中 `TestInputCommit` 最初失败于 `input commit is not implemented`。失败来自目标行为缺失。

### 实现

- 新增受权限约束的 `CommitInput` / `PatchFactor` / `LookupWriteReceipt` RPC
- 完整基础字段与 `moox.input_ready` 原子提交；同 `commit_id` 幂等，冲突重试不改已提交基础内容
- 因子补丁只能写绑定拥有列，并发补丁互不覆盖，也不能清除 input_ready
- 变更事件 `write_kind` 由授权 RPC 推导，普通客户端伪造 `write_source=merge|factor` 或系统属性不能越权
- 成功写入后可按 `commit_id` 查询原收据

### 验证命令与结果

```text
env CGO_ENABLED=1 go test ./internal/... -run 'TestInputCommit' -count=1
env CGO_ENABLED=1 go test -race ./internal/service/datanode/pebble ./internal/service/primarystore ./internal/eventmapper ./internal/service/datanode -count=3
env CGO_ENABLED=1 go test ./cmd/server ./internal/eventmapper ./test ./internal/service/datanode/pebble ./internal/service/primarystore -count=1
go test ./... -count=1   # packages/events
```

上述命令均 PASS。

### 尚未解决的依赖

- ViewDataReady 多分区 applied 屏障属于任务 04
- Collector 周期事件、主体 ID 去尾缀属于任务 05
- Merge、引擎隔离、本机部署与真实 E2E 属于 06—18

### 提交

`3ec84f52`

## 任务 04：通用 ViewDataReady 应用屏障

状态：**已完成（代码与定向/回归测试）**。部署、真实新周期 E2E、codeCR 仍属任务 18。

### 红灯证据

`TestViewDataReadyFence` 最初失败于完成标记先到即发布。失败来自目标行为缺失。

### 实现

- 按 View 活动索引代际记录 `(node_id, store_id, sequence)` 应用水位
- 完成标记先入待发布队列，所需提交位置全部应用到当前索引后才发 ViewDataReady
- 过滤 View 使用自己的 `visible_scope`；重建切换索引后必须重新确认
- 同一完成事件发布重试保持稳定 event ID；行应用后回扫待发布项

### 验证命令与结果

```text
env CGO_ENABLED=1 go test ./internal/service/view -run 'TestViewDataReadyFence' -count=1
env CGO_ENABLED=1 go test ./internal/service/view -count=1
env CGO_ENABLED=1 go test -race ./internal/service/view -run 'TestViewDataReadyFence|TestHandleCollectorPeriodCompleted|TestHandleFactorPeriodComputed' -count=3
```

上述命令均 PASS。

### 尚未解决的依赖

- Merge、引擎隔离、本机部署与真实 E2E 属于 06—18

### 提交

`cc9c6f2c`

## 任务 05：Collector 周期事件与主体 ID 去尾缀

状态：**已完成（代码与定向/回归测试）**。部署、真实新周期 E2E、codeCR 仍属任务 18。

### 红灯证据

`modules/collector` 中 `TestCollectorCompleted` 最初失败于：

- `CanonicalCryptoSubjectID("0G-USDT-SPOT")` 仍返回 `0G-USDT-SPOT`
- 完成事件 `committed_positions` 使用本地占位 `collector/local`，而不是 DataNode outbox 坐标

失败来自目标行为缺失。

### 实现

- 协议与上报逻辑统一为 `CollectorPeriodCompleted`；全成功为 complete，超时为 degraded 且冻结名单不缩小；重启不改已冻结集合；重复 Flush 不新建批次
- 加密货币数据 ID 去掉 `-SPOT`/`-SWAP`，写入路径使用 `BASE-QUOTE`（例如 `0G-USDT`），不再加后缀
- 消费 `DatasetRowsUpserted` 时记录真实 `(node_id, store_id, sequence)`，完成事件携带该位置，不再伪造 collector 本地水位
- 全超时且无行写入时 `committed_positions` 可能为空；Storage 事件校验仍要求非空，该缺口在无写入周期才会出现，成功采集路径已绑定真实位置

### 验证命令与结果

```text
env CGO_ENABLED=1 go test ./internal/... -run 'TestCollectorCompleted' -count=1
env CGO_ENABLED=1 go test -race ./internal/marketfetch ./internal/store ./internal/sources/binance -run 'TestCollectorCompleted|TestPeriodReporter|TestPeriodReadiness' -count=3
env CGO_ENABLED=1 go test ./internal/marketfetch ./internal/sources/binance ./internal/store ./internal/marketwiring -count=1
```

上述命令均 PASS。

### 尚未解决的依赖

- mdataset 定义、Merge 程序、引擎隔离、前端与正式发布属于 06—18
- 全超时无写入周期的完成标记仍需 marker 自身 outbox 位置才能通过事件校验

### 提交

`6ebea25b`

## 任务 06：mdataset 配置与权威边界

状态：**已完成（代码与定向/回归测试）**。部署、真实新周期 E2E、codeCR 仍属任务 18。

### 红灯证据

`TestMergedDatasetDefinition` 最初因类型与仓库方法不存在而无法编译；补齐 API 后，启用后改来源被拒绝于输入语义校验。失败来自目标行为缺失。

### 实现

- 保存来源、键规则、对象集、`merge_mode`、`<源DatasetID>__<字段>` 映射及不可变配置快照
- 不同频率、周期边界或字段目标冲突拒绝；启用后拒绝改变输入语义
- 缓存身份包含 Dataset ID 与 Storage schema，相同 schema 的不同 Dataset 不共享
- Storage 资源按 Dataset ID 幂等对账，已创建则不再重复 Ensure
- 示例定义将 `dataset_binance_spot_kline_1m` 与 `dataset_binance_swap_kline_1m` 合并为 `mdataset_binance_kline_1m`

### 验证命令与结果

```text
env CGO_ENABLED=1 go test ./internal/... -run 'TestMergedDatasetDefinition' -count=1
env CGO_ENABLED=1 go test -race ./internal/domain ./internal/store -run 'TestMergedDatasetDefinition' -count=3
```

上述命令均 PASS。

### 尚未解决的依赖

- Merge 程序、引擎隔离、前端与正式发布属于 07—18

### 提交

`193a912c`

## 任务 07：独立 Merge 程序和持久化行聚合

状态：**已完成（代码与定向/回归测试）**。周期账本、部署、真实新周期 E2E、codeCR 仍属任务 08—18。

### 红灯证据

`TestMergeAssembler` 最初因 `Assembler`/`Ledger`/`ApplyArrival` 不存在而无法编译。失败来自目标行为缺失。

### 实现

- 独立 SQLite 账本记录源到达与提交，不与控制面共享数据库
- 全部必需源字段到齐后按 `<源DatasetID>__<字段>` 提交一次完整输入；重复到达幂等
- 另一对象缺源不阻塞当前对象；重启后可从账本恢复已到达的源
- 不完整字段不计入到齐；`merge_mode=custom` 系统零写入
- 消费源 Dataset 行变更，忽略 `factor_patch`/`input_commit`；成功处理后 ACK
- 独立进程 `cmd/merge` 通过受权 `CommitInput` 写入 `mdataset_binance_kline_1m`

### 验证命令与结果

```text
env CGO_ENABLED=1 go test ./internal/merge -run 'TestMergeAssembler' -count=1
env CGO_ENABLED=1 go test -race ./internal/merge -run 'TestMergeAssembler' -count=3
env CGO_ENABLED=1 go test ./internal/merge -count=1
env CGO_ENABLED=1 go build ./cmd/merge
```

上述命令均 PASS。

### 尚未解决的依赖

- Merge 周期冻结名单、超时 degraded、MergePeriodCompleted 属于任务 08
- 引擎隔离、时序/截面、前端与正式发布属于 09—18

### 提交

`924929c6`

## 任务 08：Merge 周期账本和超时

状态：**已完成（代码与定向/回归测试）**。部署、真实新周期 E2E、codeCR 仍属任务 09—18。

### 红灯证据

`TestMergePeriodLedger` 最初因 `PeriodLedger` 不存在而无法编译。失败来自目标行为缺失。

### 实现

- 周期开始冻结对象名单；部分成功截止后 degraded，失败对象仍在完整预期名单中
- 关闭后迟到提交不改写终态，不补写缺失对象空行
- 上报失败可在重启后重试；收据持久化后才报告 `MergePeriodCompleted`
- 空对象集可明确 complete 结束且不伪造输入行
- 源 Collector 完成不能代替 Merge 完成
- `ReportMergePeriodCompleted` 仅接受 merge 调用方

### 验证命令与结果

```text
env CGO_ENABLED=1 go test ./internal/merge -run 'TestMergePeriodLedger' -count=1
env CGO_ENABLED=1 go test -race ./internal/merge -run 'TestMergePeriodLedger|TestMergeAssembler' -count=3
env CGO_ENABLED=1 go test ./internal/merge -count=1
env CGO_ENABLED=1 go test ./internal/service/primarystore -count=1
```

上述命令均 PASS。

### 尚未解决的依赖

- 引擎隔离、时序读 Primary、缓存、截面、补算、前端与正式发布属于 09—18
- mdataset `object_set` 仍需由控制面提供，Merge 才能按配置冻结真实宇宙

### 提交

`cb007ebf`

## 任务 09：控制面与运行资源彻底隔离

状态：**已完成（代码与定向/回归测试）**。部署、真实新周期 E2E、codeCR 仍属任务 10—18。

### 红灯证据

角色边界行为此前已部分存在；本任务用 `TestRoleBoundary` 固化：控制面无 Python、独立数据库、缺网关节点拒绝启动、漏快照可从 SQLite 恢复、关闭先停准入再释放库。

### 实现

- 控制面 `InitializeControl` 不启动 Python 与实时消费者
- 控制/引擎/Merge 使用独立 SQLite 路径与凭据
- 缺 `gateway_node_id` 时拒绝打开资源
- 引擎关闭顺序为 cancel → 停消费者 → 停目录同步 → 关闭 SQLite
- 控制面重启后目录快照仍可从同一 catalog 库恢复

### 验证命令与结果

```text
env CGO_ENABLED=1 go test ./internal/bootstrap -run 'TestRoleBoundary' -count=1
env CGO_ENABLED=1 go test -race ./internal/bootstrap -run 'TestRoleBoundary' -count=3
```

上述命令均 PASS。

### 尚未解决的依赖

- 时序改读 mdataset Primary、缓存、截面、补算、前端与正式发布属于 10—18

### 提交

`b182476b`

## 任务 10：Dataset 驱动时序触发

状态：**已完成（代码与定向/回归测试）**。部署、真实新周期 E2E、codeCR 仍属任务 18。

### 红灯证据

`TestDatasetRowsTrigger` 最初因 `NewDatasetRowsRunner` / `DatasetRowsDeliverPolicy` 不存在而无法编译。失败来自目标行为缺失。

### 实现

- 引擎只消费 `write_kind=input_commit` 且 `moox.input_ready=true` 的 `DatasetRowsUpserted`
- 因子回写 `factor_patch`、未绑定 Dataset、未就绪行和过期 `moox.binding_version` 均忽略
- 任务身份按 Dataset、业务键、配置快照和绑定代际确定；重复源事件与重启后账本不重复执行
- 两个对象独立入队，不等待全集
- 删除任务身份对 InputContract/ActiveIndex 的依赖；账本 scope 改为 Dataset + 快照
- 首次 durable 使用 `DeliverNew`；已有 durable 保留原投递策略与积压
- `StartEngineSubject` 启动 Dataset 行消费者，不再返回空 stub

### 验证命令与结果

```text
env CGO_ENABLED=1 go test ./internal/trigger -run 'TestDatasetRowsTrigger' -count=1
env CGO_ENABLED=1 go test -race ./internal/trigger ./internal/store ./internal/bootstrap -run 'TestDatasetRowsTrigger|TestSubjectAdmission|TestRoleBoundary|TestInitializeEngine' -count=3
```

上述命令均 PASS。

### 尚未解决的依赖

- Primary 历史窗口读取与 Python ABI 属于任务 11
- 缓存、截面、周期屏障、补算、前端与正式发布属于 12—18

### 提交

`721d4f2f`

## 任务 11：Primary 历史读取与统一 Python ABI

状态：**已完成（代码与定向/回归测试）**。部署、真实新周期 E2E、codeCR 仍属任务 18。

### 红灯证据

`TestDatasetWindow` 最初因 `ReadDatasetWindow` / `RequireDatasetLookback` / `ValidateDatasetOutputs` 不存在而无法编译。失败来自目标行为缺失。

### 实现

- 时序窗口在 `SourceDataset` 且无 InputContract 时改为 Primary 精确键读取，不再走 View 范围扫描
- 窗口拒绝未来行；缺历史由 `RequireDatasetLookback` 明确失败；空对象集合不发起 Primary 请求
- JSON / SQL NULL / int64 精度在 DataFrame 中保留
- Python context 使用 `config_snapshot_id`，删除 `input_contract_version`；`factor_type` 只在 factor 对象上
- 截面/时序输出校验越界对象与重复键；Python 错误不写成功输出

### 验证命令与结果

```text
env CGO_ENABLED=1 go test ./internal/... -run 'TestDatasetWindow' -count=1
env CGO_ENABLED=1 go test ./internal/storageio ./internal/engine ./internal/taskrunner ./internal/domain ./internal/trigger -count=1
env CGO_ENABLED=1 go test -race ./internal/storageio ./internal/engine ./internal/taskrunner -run 'TestDatasetWindow|TestTaskContext|TestEncodeJSON|TestWriteFactorPatch|TestReadPeriod' -count=3
```

上述命令均 PASS。

### 尚未解决的依赖

- 缓存 Dataset 化、容量维护、截面、周期屏障、补算、前端与正式发布属于 12—18

### 提交

`a00215f1`

## 任务 12：缓存 Dataset 化和惰性回源

状态：**已完成（代码与定向/回归测试）**。部署、真实新周期 E2E、codeCR 仍属任务 18。

### 红灯证据

`TestDatasetCache` 最初因 `DatasetCache` / `RegisterSchema` 不存在而无法编译。失败来自目标行为缺失。

### 实现

- 缓存身份改为 `SourceKey{SpaceID, DatasetID}` + Storage schema 标识，相同 schema 的不同 Dataset 不混用
- 冷缓存回源一次后命中；删除缓存键后再次回源得到最新值
- 新 schema 打开空代际；半字段/空值行不能当作完整输入命中
- 确认缺失的源键不会无限重试 Primary
- 引擎 Storage 客户端挂上 DatasetCache；窗口读取携带 `StorageSchemaID`

### 验证命令与结果

```text
env CGO_ENABLED=1 go test ./internal/storageio ./internal/inputcache -run 'TestDatasetCache|TestManager' -count=1
env CGO_ENABLED=1 go test ./internal/storageio ./internal/inputcache ./internal/taskrunner ./internal/trigger ./internal/bootstrap ./internal/engine -count=1
env CGO_ENABLED=1 go test -race ./internal/storageio ./internal/inputcache -run 'TestDatasetCache|TestManagerContract' -count=3
```

上述命令均 PASS。

### 尚未解决的依赖

- 缓存容量维护、截面、周期屏障、补算、前端与正式发布属于 13—18

### 提交

`36bdf250`

## 任务 13：缓存容量维护

状态：**已完成（代码与定向/回归测试）**。部署、真实新周期 E2E、codeCR 仍属任务 18。

### 红灯证据

`TestCacheCapacityPolicy` 以计划要求的筛选前缀新增用例，锁定默认 2233s 首次延后、多 Dataset 共用目录预算、N 行仍超字节则停填充、旧读者不踩关闭文件、取消重建可恢复、读取不刷新淘汰时间。

### 实现

- 复用已有 DuckDB 维护：`check_interval=2233s`、首次延后、禁止重入
- 目录总字节含活动/退役/WAL/临时文件，多个 Dataset 共用同一 MaxBytes
- 超限按本地写入时间保留 N 行并换新文件；重建后仍超限则 `PauseWrites`，不再无限重建
- 读取窗口不更新 `__moox_cache_updated_at`；取消的重建保留原代际

### 验证命令与结果

```text
env CGO_ENABLED=1 go test ./internal/inputcache -run 'TestCacheCapacityPolicy' -count=1
env CGO_ENABLED=1 go test ./internal/inputcache -count=1
env CGO_ENABLED=1 go test -race ./internal/inputcache -run 'TestCacheCapacityPolicy' -count=3
```

上述命令均 PASS。

### 尚未解决的依赖

- 截面、周期屏障、补算、前端与正式发布属于 14—18

### 提交

`7cf58d75`

## 任务 14：截面计算与通用可读事件

状态：**已完成（代码与定向/回归测试）**。部署、真实新周期 E2E、codeCR 仍属任务 18。

### 红灯证据

`TestCrossSectionReady` 最初失败于 FactorPeriodComputed 对应的 ViewDataReady 仍会调度截面任务。失败来自目标行为缺失。

### 实现

- `ViewDataReady.completion_kind` 记录关联完成事件种类
- 截面只消费 `completion_kind=MergePeriodCompleted`（或 `recalc-` 补算触发）的 ViewDataReady
- 每个截面绑定一个面板任务，不再做主体×因子笛卡尔积
- 默认拒绝 degraded 面板；`params.allow_degraded=true` 时才计算残缺面板，并把失败集合放入任务上下文
- 引擎启动独立 ViewDataReady 消费者；时序仍只走 DatasetRows
- Recalc 同步点改为单 Dataset View 的 `dataset_id`

### 验证命令与结果

```text
env CGO_ENABLED=1 go test ./internal/... -run 'TestCrossSectionReady' -count=1
env CGO_ENABLED=1 go test ./internal/trigger/... ./internal/bootstrap ./internal/engine ./internal/taskrunner ./internal/rpc ./test -count=1
env CGO_ENABLED=1 go test -race ./internal/trigger/... ./internal/bootstrap ./internal/engine ./internal/taskrunner ./internal/rpc -count=3
go test ./... -count=1   # packages/events
env CGO_ENABLED=1 go test ./internal/service/view -run 'TestHandle|TestViewDataReady|TestPeriod' -count=1
```

上述命令均 PASS。

### 尚未解决的依赖

- 因子周期汇总屏障属于任务 15；截面 runner 仍会在截面任务结束后立即上报 FactorPeriodComputed
- 因子写回仍走 `UpsertFields` + `write_source=factor`，尚未统一 `PatchFactor`
- 补算、前端与正式发布属于 16—18

### 提交

`88612056`

## 任务 15：因子周期汇总与策略订阅

状态：**已完成（代码与定向/回归测试）**。部署、真实新周期 E2E、codeCR 仍属任务 18。

### 红灯证据

`TestFactorPeriodBarrier` 最初失败于冻结名单前到达的任务不会发布 FactorPeriodComputed，以及输入 ViewDataReady 仍会被因子策略接受。失败来自目标行为缺失。

### 实现

- 新增周期账本：冻结绑定×对象集合，容纳早到的时序终态，缺源记为 missing_input，输出收据确认后才发布 FactorPeriodComputed
- 绑定启停不改变已冻结周期；零绑定立即给出明确终态；清理不得越过最新已报告周期的重放窗口
- 引擎时序/截面共享同一账本；截面不再单独把 Merge 就绪当成因子周期完成
- 读取因子结果 View 的策略只接受 `completion_kind=FactorPeriodComputed` 的 ViewDataReady

### 验证命令与结果

```text
env CGO_ENABLED=1 go test ./internal/... -run 'TestFactorPeriodBarrier' -count=1
env CGO_ENABLED=1 go test ./internal/trigger ./internal/bootstrap ./internal/store ./schema ./internal/rpc ./internal/taskrunner -count=1
env CGO_ENABLED=1 go test -race ./internal/trigger ./internal/bootstrap -count=3
go test ./... -count=1   # modules/strategy
```

上述命令均 PASS。

### 尚未解决的依赖

- 因子写回仍走 `UpsertFields`，账本目前用任务来源/Merge 提交位置作为收据占位；补算、前端与正式发布属于 16—18

### 提交

`82125c77`

## 任务 16：补算、启停与状态接口

状态：**已完成（代码与定向/回归测试）**。部署、真实新周期 E2E、codeCR 仍属任务 18。

### 红灯证据

`TestRecalcTask` 最初失败于控制面 `RecalcFactor` 仍尝试本机计算（`recalc queue is not implemented` / 同步 `taskRunner.Run`）。失败来自目标行为缺失。

### 实现

- 控制面只 `Accept` 异步补算任务，返回 `job_id`/`status`；取消与查询走 `CancelRecalcJob`/`GetRecalcJob`
- 任务身份包含 Dataset、对象、周期范围、绑定快照与 request_id；重复请求幂等；取消不会变成成功
- 绑定停用后旧 generation 不能写入新输出；引擎离线时控制面仍受理
- 状态区分 `missing_input` / `algorithm_failure` / `view_waiting`
- 引擎经内部 NATS 认领并执行独立 `recalc` 批次，不发布 `FactorPeriodComputed`；心跳写入 desired/applied
- CLI 增加 `recalc` / `recalc-cancel` / `recalc-status`，不在控制面启动 Python

### 验证命令与结果

```text
env CGO_ENABLED=1 go test ./internal/... -run 'TestRecalcTask' -count=1
env CGO_ENABLED=1 go test ./internal/trigger ./internal/rpc ./internal/bootstrap ./internal/store ./schema ./cmd/cli -count=1
env CGO_ENABLED=1 go test -race ./internal/trigger ./internal/bootstrap -count=3
env CGO_ENABLED=1 go build ./cmd/server ./cmd/engine ./cmd/cli ./cmd/merge
```

上述命令均 PASS。

### 尚未解决的依赖

- 因子写回仍走 `UpsertFields`；正式发布与真实新周期 E2E 属于任务 18

### 提交

`b9990ca7`

## 任务 17：前端导航和基础资产归并

状态：**已完成（代码与定向/回归测试）**。部署、真实新周期 E2E、codeCR 仍属任务 18。

### 红灯证据

`pnpm exec playwright test tests/data-collection-navigation.spec.ts` 最初失败于：

- 顶层仍有「数据资产」，数据采集下没有基础数据集入口
- `/collector/data-management` 仍是「数据视图 / 数据集合」双 Tab，缺少 `aria-label="基础数据集"`
- 数据集详情没有「索引」页签，无法独立创建/删除同一 Dataset 的两个 View
- `/factor/datasets`、`/factor/construct`、`/factor/tasks` 路由不存在

失败来自目标行为缺失，不是编译环境缺失。

### 实现

- 删除顶层「数据资产」菜单；数据源、采集对象、基础字段、采集任务、基础数据集归入数据采集
- 因子计算增加复合因子数据集、构造配置、计算任务及补算；补算只受理异步 job，不在浏览器跑 Python
- 数据集详情「索引」页签支持同一 Dataset 多个单源 View；锁定 dataset 选择器；删除 View 不删除 Dataset
- 创建数据集后自动创建默认 View；跨接口失败可在详情/构造页「重试恢复」
- 列定义展示基础字段 / 因子输出归属

### 验证命令与结果

```text
pnpm exec playwright test tests/data-collection-navigation.spec.ts tests/factor-dataset-workflow.spec.ts
pnpm test
pnpm run check:menu
pnpm run check:data-browse
pnpm run build:prod
```

上述命令均 PASS。

### 尚未解决的依赖

- 因子写回仍走 `UpsertFields`；清理旧事件、独立构建、codeCR、本机引擎部署与真实新周期 E2E 属于任务 18

### 提交

`76b7d77e`

## 任务 18：清理、端到端与交付

状态：**已完成（代码、定向/契约测试、独立 codeCR、本机引擎/Merge 发布、真实新周期 E2E）**。codeCR 范围 `77073922` / `4019faac`；随后 overlay 了空位置屏障与 Binding 门，并补种引擎 mdataset catalog。工作区相对 HEAD 仍有未提交改动，本轮未 commit / push。

### 红灯证据

`modules/factor` 中 `TestDatasetPipeline/independent_programs_and_docs` 最初失败于 README 仍包含 `ViewSourceSubjectReady`，且未记录 `moox-factor-merge`。失败来自目标行为缺失，不是编译环境缺失。

### 实现

- 新增 `TestDatasetPipeline`：两来源两对象 Merge、超时 degraded、杀进程恢复、缓存满暂停写入、View 重建不挡时序、时序/截面/策略不回环
- 清理当前运行时文档与配置中的旧事件名；独立构建 control / engine / merge
- 新增 `moox-factor-merge` 打包与启停脚本，部署包不含凭证和 Python
- Merge 配置读取运行时环境变量（DB、Storage gateway、EventBus、HMAC）

### 验证命令与结果

```text
env CGO_ENABLED=1 go test ./internal/... -run 'TestDatasetPipeline' -count=1
env CGO_ENABLED=1 go test -race ./internal/merge ./internal/integration -count=3
bash scripts/test/contract/test-build-factor-engine.sh
bash scripts/test/contract/test-build-factor-merge.sh
bash scripts/test/contract/test-deploy-moox-factor-merge.sh
./scripts/build/build.sh factor-engine
./scripts/build/build.sh factor-merge
./scripts/build/build.sh factor
```

上述命令均 PASS。`go test -race ./internal/store -count=3` 仍会因 `openTestDB` 内存库 `c_name` UNIQUE 冲突失败，属既有问题，不计入本任务回归。

### codeCR

- `34a963d6..0088a0fa`：**BLOCK**（引擎 `UpsertFields`、屏障收据、View 水位、Merge 宇宙）
- `0088a0fa..77073922`：**BLOCK**（Freeze 前丢收据、多行 `commit_id` 含 `/`、mdataset `CommitInput` 被拒）
- `77073922..4019faac`：**PASS**。生产顺序下收据可保留；`PatchFactor` commit_id 不含 `/`；Merge 可对共享 mdataset `CommitInput`，通用 Upsert 仍拒绝。剩余 Important：崩溃窗口丢收据、空 position 屏障、mapped 基列未自动建表。

### 正式发布与真实新周期 E2E（2026-09-15）

验收门槛：独立 codeCR Ready；本机发布 `moox-factor-engine` / `moox-factor-merge`；真实 1m 周期走通采集 → Merge `CommitInput` → 时序 `PatchFactor` → `FactorPeriodComputed` → `ViewDataReady` → 策略每周期只写一行。

现网 overlay（CST）：

- storage-view `146.56.196.204` `/data/moox/storage/bin/moox-storage-view` sha256 `7fbc19988f9f839eab7e4893c81f508fd07e87eca6fc5e32eaf86713d8109998`，pid 2147738，含 `writeFenceRequiresPositions` / `FactorBindingPeriodState`
- strategy `106.53.107.122` `/data/moox/prod/bin/moox-strategy` sha256 `8963ed643b47faf345a891742ba45f8aa30e5beb56395b67253fcf63e65ee9c4`，pid 3701199
- 本机 merge pid 52222 sha256 `9ed510397de120cf4ed8552759d2176bc945487a9753d07a762176eca5ccfffd`，consumer 仍为 `factor_merge_rows_v1`
- 本机 engine pid 65252，`t_factor_merged_datasets` 已种 `mdataset_binance_kline_1m` / `snap-1` / enabled=1；时序 consumer 仍为 `factor_dataset_rows_v1`，日志 `trigger_type=dataset_rows`

compile→本机 gzip/scp 曾因 180s 超时失败；改 rsync 后完成 overlay，现网 sha 与符号核验通过。

样本周期 `2026-09-15T06:48:00Z`（bar_end `06:49:00Z`）及后续连续周期：

- Merge：BTC/ETH 的 spot+swap arrival `c_complete=1` 后才 `CommitInput`，收据 `(storage-node-0, EGDAONV25MIQHQDAJY42RJCBMI, sequence>0)`
- 引擎：BTC 11 个 binding pair 全部 `complete` 且收据确认；barrier `degraded/reported`（宇宙 662 主体，alts `missing_input`）
- View `view_binance_kline_1m` 周期状态 event_id 与引擎 barrier `c_batch_id` 一致（例如 06:48 `storage-marker-3ef4eff98a7e856047ebb42f255a31a4`），即 `FactorPeriodComputed` 发布的 ViewDataReady
- 策略实例 `factor-result-e2e-instance-20260914`：`created_at>=2026-09-15T06:37:52Z` 后 `06:49`–`06:57` 各恰好 1 行，`targets=[{BTC-USDT,1}]`，`valid_until=bar_end+4m`

两来源两对象可核算样本仍由 `TestDatasetPipeline` 覆盖；现网 E2E 策略池为 BTC-USDT，ETH 仅验证 Merge 双源提交。

### 尚未解决的依赖

- EventBus 仍缺 `factor-merge-eventbus` 角色；引擎 `nats: permissions violation` 出现在 `moox.factor.internal.recalc.heartbeat`，不挡 DatasetRows
- 崩溃窗口下 `RecordCommit` 成功但 `NoteCommit` 失败会丢收据
- MetadataSync 不自动建 mapped 基列，新环境 `CommitInput` 可能被 schema 拦住
- 全市场 1m 宇宙 Freeze 后大量 alts `missing_input`：根因是 Freeze 使用现货∪合约并集，而 `CommitInput` 仍要求双源；已改为各源 `universe_subject_ids` 交集，单边标的不再进入 mdataset
- 策略偶发 `VIEW_NOT_READY` 索引代际变化，重试后仍每周期只写一行
- codeCR overlay 与引擎 catalog 补种尚未提交

### 提交

`4019faac`
