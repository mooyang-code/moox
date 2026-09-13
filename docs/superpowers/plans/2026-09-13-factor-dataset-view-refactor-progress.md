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
- 统一完成标记：`batch_id`、`config_snapshot_id`、`dataset_id`、可读 `expected_scope_ref` + `expected_subject_ids`、状态、`committed_positions`
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

（本提交）
