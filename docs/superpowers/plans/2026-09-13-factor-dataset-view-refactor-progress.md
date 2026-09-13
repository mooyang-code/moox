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
- View 仍按 `dataset_ids[]` 聚合后再发 `ViewDataReady`，单 Dataset 模型属于任务 02
- ViewDataReady 尚未按多分区 applied 位置做可读屏障，属于任务 04
- 主体 ID 去 `-SPOT`/`-SWAP` 属于任务 05
- Merge 程序、mdataset、本机引擎部署与真实 E2E 属于 06—18

### 提交

见本任务提交号（写入后回填）。
