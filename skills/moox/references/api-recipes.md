# 管理接口调用配方（gateway call）

管理台上的大部分操作没有专用命令，用 `moox-cli gateway call` 直接调用对应的内部服务。
`moox-cli` 的身份对这些方法都有权限（交易模块尚未完工，本文不涉及）。

## 通用做法

```bash
# 列出可调用的服务与方法（读内嵌服务目录，不连网），可带关键字过滤
./bin/moox-cli gateway methods
./bin/moox-cli gateway methods strategy

# 调用：服务写完整名称；--data 是 JSON，@文件 读文件，- 读标准输入；--space 是请求携带的空间（默认 crypto）
./bin/moox-cli gateway call trpc.moox.strategy.StrategyMgr ListStrategies --space crypto --data '{"page":{"page":1,"page_size":50}}'
```

- 命令在产物包（或仓库）根目录执行，经 `moox.toml` 里的控制机 SSH 隧道调用，不需要单独登录。
- JSON 字段名就是 proto 字段名（`snake_case`）；完整字段以 `modules/<模块>/proto/*.proto` 为准。返回里 `ret_info.code` 为 `0`（或 `SUCCESS`）才算成功。
- **写操作先给用户看要发的请求，确认后再执行**；`gateway methods` 标为“写”的都算。查询类可以直接执行。
- 带 DSL、长文本的请求先写成文件再用 `--data @文件`，不要在命令行里拼多行 YAML。
- 分页：策略服务用 `{"page":{"page":1,"page_size":50}}`；因子、采集、存储用 `{"page":{"page":1,"size":50}}`。

## 空间（trpc.moox.admin.SpaceMgr）

| 用途 | 方法 | 请求示例 |
| --- | --- | --- |
| 列出空间 | `ListSpaces` | `{}` |
| 新建空间 | `CreateSpace` | `{"space":{"space_id":"myspace","name":"我的空间","market":"crypto","timezone":"Asia/Shanghai"}}` |
| 修改 | `UpdateSpace` | `{"space":{"space_id":"myspace","name":"新名字"}}` |

新空间还要在 Storage 建元数据（数据来源、数据集、字段），通常从 `config/setup/metadata.yaml` 的写法复制，用 `moox-cli metadata apply` 导入。

## 数据目录（trpc.moox.storage.Metadata）

| 用途 | 方法 | 请求示例 |
| --- | --- | --- |
| 数据集列表 | `ListDatasets` | `{"space_id":"crypto","page":{"size":200}}` |
| 数据集的列 | `ListDatasetColumns` | `{"space_id":"crypto","dataset_id":"<数据集>"}` |
| 标的列表 | `ListSubjects` | `{"space_id":"crypto","page":{"size":200}}` |
| 标签、标签成员 | `ListTags` / `ListTagMembers` | `{"space_id":"crypto"}` / `{"space_id":"crypto","tag_id":"<标签>"}` |
| 数据来源 | `ListDataSources` | `{"space_id":"crypto"}` |
| View 列表、重建日志 | `ListViews` / `ListViewRebuildLogs` | `{"space_id":"crypto"}` / `{"space_id":"crypto","view_id":"<View>"}` |
| 存储节点 | `ListDataNodes` | `{}` |

标签成员的增删优先用 `moox-cli metadata tag members`；View 的修复与重建用 `moox-cli storage repair-view` 等专用命令（见 [`cli-operations.md`](cli-operations.md)）。

## 采集任务（trpc.moox.collector.CollectMgr）

| 用途 | 方法 / 命令 | 请求示例 |
| --- | --- | --- |
| 任务列表 | `GetTaskList` | `{"space_id":"crypto","page":{"size":100}}` |
| 任务详情 | `GetTaskDetail` | `{"space_id":"crypto","task_id":"<任务>"}` |
| 新建 K 线任务 | `moox-cli collector task create` | 见 `--help` |
| 修改任务 | `UpdateTask` | 先 `GetTaskDetail` 取回完整任务，改字段后整体提交 `{"space_id":…,"task_id":…,"task":{…}}` |
| 停用 | `DisableTask` | `{"space_id":"crypto","task_id":"<任务>"}` |
| 删除（连同结果） | `moox-cli collector task delete` | 不可恢复，先确认 |
| 运行状态、缺口、补采、重采样、标的排除、采集方式 | `moox-cli collector status / kline / resample / subjects / ingest-mode` | 见 [`modules.md`](modules.md#采集) |

## 因子（trpc.moox.factor.FactorMgr）

| 用途 | 方法 | 请求示例 |
| --- | --- | --- |
| 因子定义列表、详情 | `ListFactors` / `GetFactor` | `{}` / `{"factor_id":"ma"}` |
| 校验、试算表达式 | `ValidateFactorSpec` / `PreviewFactor` | 本地优先用 `scripts/factor-expr.sh check/eval`（见 [`factor-dsl.md`](factor-dsl.md)） |
| 计算任务列表 | `ListFactorTasks` | `{"page":{"size":100}}` |
| 新建计算任务 | `CreateFactorTask` | `{"factor_task":{"space_id":"crypto","source_dataset_id":"<源数据集>","freq":"1m","name":"<名称>"}}`；批量配置优先写进 `moox.toml` 再 `moox-cli setup factors` |
| 启用 / 停用任务 | `SetFactorTaskStatus` | `{"task_id":"<任务>","status":"enabled"}`（或 `disabled`） |
| 加成员 | `AddTaskMember` | `{"task_id":"<任务>","factor_id":"ma","kind":"fixed","params":{"window":20}}` |
| 成员启停 | `SetTaskMemberStatus` | `{"task_id":"<任务>","member_key":"ma__20","status":"enabled"}` |
| 删除任务 | `DeleteFactorTask` | `{"task_id":"<任务>","purge":false}`；`purge:true` 连结果一起删，不可恢复 |
| 补算 | `RecalcFactors` | `{"task_id":"<任务>","start_time":"2026-10-01T00:00:00Z","end_time":"2026-10-02T00:00:00Z","request_id":"<唯一 ID>"}`（左闭右开） |
| 补算作业 | `ListRecalcJobs` / `CancelRecalcJob` | `{"task_id":"<任务>"}` / `{"job_id":"<作业>"}` |
| 运行状态 | `GetStatus` | `{}` |

任务的阶段（就绪、试运行、运行、暂停）用 `moox-cli lifecycle`，自定义算子用 `moox-cli udf`（见 [`udf.md`](udf.md)）。

## 策略（trpc.moox.strategy.StrategyMgr）

完整的写法与创建顺序见 [`strategy-dsl.md`](strategy-dsl.md)。常用请求：

| 用途 | 方法 | 请求示例 |
| --- | --- | --- |
| 试算（不落库） | `ValidateStrategy` | `{"dsl_yaml":"<DSL 原文>","factor_task_id":"<因子任务>"}` |
| 新建定义 | `CreateStrategy` | `{"strategy":{"dsl_yaml":"<DSL 原文>"}}` |
| 修改定义 | `UpdateStrategy` | `{"strategy_id":"<ID>","dsl_yaml":"…"}`；引用它的实例要先停用 |
| 定义列表 / 详情 | `ListStrategies` / `GetStrategy` | `{"page":{"page":1,"page_size":50}}` / `{"strategy_id":"<ID>"}` |
| 新建实例 | `CreateStrategyInstance` | `{"instance":{"strategy_id":"<ID>","space_id":"crypto","factor_task_id":"<因子任务>"}}`（不挂账户＝观察实例） |
| 启用 / 停用实例 | `SetStrategyInstanceEnabled` | `{"instance_id":"<ID>","enabled":true}` |
| 实例列表 | `ListStrategyInstances` | `{"page":{"page":1,"page_size":50},"space_id":"crypto"}` |
| 每期结果 / 当前目标 | `ListStrategyResults` / `ListStrategyTargets` | `{"instance_id":"<ID>","page":{"page":1,"page_size":20}}` / `{"instance_id":"<ID>"}` |
| 历史回放 | `StartReplay` / `ListReplays` / `CancelReplay` | `{"strategy_id":"<ID>","factor_task_id":"<任务>","start_time":"…","end_time":"…"}` |
| 模板 | `ListStrategyTemplates` / `RenderStrategyTemplate` / `SaveStrategyTemplate` | 见 [`strategy-dsl.md`](strategy-dsl.md) |

一个因子任务只能被一个策略实例关联。

## 监控（trpc.moox.monitor.MonitorMgr）

| 用途 | 方法 / 命令 |
| --- | --- |
| 当前告警与不健康项 | `moox-cli doctor alerts`（见 [`monitor-attention.md`](monitor-attention.md)） |
| 数据到达历史（某天每期何时到） | `QueryPipelineArrivalHistory`：`{"space_id":"crypto","dataset_id":"<数据集>","freq":"1m","day":"2026-10-28"}` |
| 主机资源曲线 | `ListHostAgents` / `QueryHostMetricHistory` |
| 告警渠道 | 以 `moox.toml` 的 `[notification]` 为准，用 `moox-cli config notification` 同步 |
| 屏蔽告警推送 | `moox-cli monitor mute / unmute / mute-status` |

## 部署与运维（trpc.moox.ops.SysDeploy、ActivityLog）

| 用途 | 方法 / 命令 |
| --- | --- |
| 主机、部署、路由（只读） | `ListHosts` / `ListDeployments` / `GetHostRoutes` / `GetCatalog` |
| 主机与部署的增删、启停 | 只走 `moox-cli setup …`（见 [`host-registry.md`](host-registry.md)） |
| 活动日志 | `moox-cli activity list` |

## 密钥（trpc.moox.ops.SecretMgr）

只用 `ListSecrets` 查看有哪些密钥（不返回明文）。新建、修改密钥会让明文经过 Agent，**不要代用户写入**：请用户在管理台“设置 › 密钥”页自己填写。
