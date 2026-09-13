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
