# CloudNode JobItem Runtime Simplification Plan

Date: 2026-07-04

## 1. 背景

当前 CloudNode 异步执行模型已收敛为 JobItem，但早期设计里保留了偏平台化的 `owner_service`、`owner_ref`、`deployment_id`、`lease_timeout_ms` 等概念。这个模型有较强的通用任务平台倾向，但 MooX 当前是个人项目，目标是代码简洁、可维护、本地可运行，不追求高可用和复杂调度。

本轮重构不继续扩展通用队列能力，而是把模型收敛为采集场景更直接的 Job / JobItem：

- Collector 生成一次采集 Job。
- Collector 将 Job 拆成多个可独立执行的 JobItem。
- CloudNode 保存、派发、恢复 JobItem。
- CloudRuntime 按 `job_type` 找到业务 handler 执行。
- 业务 handler 将数据写入 Storage。
- CloudRuntime 只把执行摘要和错误回报给 CloudNode。

## 2. 设计目标

- 保留异步模型，删除同步/异步统一执行模型的规划。
- 保留 SQLite 作为唯一队列存储，不引入 MySQL、Redis、RabbitMQ、Kafka 或云队列。
- Runtime 保持 `poll -> execute -> report` 三步，不感知 lease、directive、复杂调度策略。
- CloudNode 只记录 JobItem 生命周期、attempt、错误和执行摘要，不存储大体量业务数据。
- 业务数据由业务 handler 直接写入 Storage。

## 3. 模块边界

```text
modules/collector
  采集规则、Job 生成、JobItem 拆分、提交 JobItem
  按具体采集任务组织 jobs/kline、jobs/symbol 等子目录
  注册 collect.* handler，handler 执行业务并写入 Storage

modules/cloudnode
  云节点管理：云账户、节点、心跳、发布、部署
  代码包管理：上传、版本、COS、下载 URL
  JobItem 队列：提交、派发、上报、取消、查询、attempt

packages/cloudruntime
  云函数运行时框架
  注册 job_type -> handler
  Poll JobItem、执行 handler、Report 状态
```

目标代码结构：

```text
modules/cloudnode/
  cmd/moox-cloudnode/
  config/
  schema/cloudnode.sql
  proto/cloudnode.proto
  internal/
    bootstrap/
    rpc/
      server.go
      node.go
      account.go
      package.go
      job_item.go
      invocation.go
      convert.go
    repository/
      models.go
      node.go
      account.go
      package.go
      job_item.go
      invocation.go
    service/
      job_item.go
      package.go
    providers/tencent-scf/

packages/cloudruntime/
  runtime.go
  registry.go
  handler.go
  client.go
  README.md

modules/collector/
  internal/
    jobs/
      registry.go
      kline/
        params.go
        planner.go
        handler.go
        result.go
      symbol/
        params.go
        planner.go
        handler.go
        result.go
    taskpublisher/
    taskrunner/
    sources/
    reporter/
    rpc/
    domain/
    repository/
    app/
```

Collector 的 `jobs/*/planner.go` 负责把采集规则拆成 JobItem；`jobs/*/handler.go` 负责执行一个 JobItem 并写入 Storage；`sources/` 只保留交易所 API 和底层数据访问能力。

## 4. 术语

| 术语 | 含义 |
|---|---|
| `job` | Collector 生成的一次采集作业，包含多个可独立执行的原子项 |
| `job_id` | 一次采集 Job 的 ID |
| `job_item` | 最小可执行单元，CloudNode 派发给云函数执行 |
| `job_item_id` | JobItem 唯一 ID，同时作为幂等键 |
| `job_type` | Runtime handler 注册键，例如 `collect.kline`、`collect.symbol` |
| `code_package_id` | 目标代码包 ID，用于派发到运行对应代码包的云函数节点 |
| `attempt` | JobItem 的一次执行尝试 |
| `attempt_no` | JobItem 的执行尝试序号，用于拒绝过期上报 |
| `recover_at` | 服务端内部恢复时间，超时未上报则回收 RUNNING JobItem |

## 5. 命名决策

| 当前/曾讨论字段 | 目标字段 | 决策 |
|---|---|---|
| `task_id` | `job_id` | 避免和 collector 既有 task_rule/task_instance 混用 |
| `atomic_task_id` | `job_item_id` | 表达“Job 的一个原子执行项” |
| 旧异步执行项 ID | `job_item_id` | 协议和表结构统一使用 JobItem 语义 |
| `workload_type` | `job_type` | 与 runtime 注册 handler 的概念对齐 |
| `deployment_id` | `code_package_id` | JobItem 关心代码包能力，不关心一次部署动作 |
| `payload` | `params` | 表示业务 handler 的输入参数 |
| `payload_schema_version` | 删除 | 新项目不做多版本 schema 协商 |
| `owner_service` / `owner_ref` | 删除 | 当前只有 collector 提交，`job_id`/`job_item_id` 足够 |
| `idempotency_key` | 删除 | `job_item_id` 承担幂等语义 |
| `max_inflight` | 删除 | 先依赖 SCF 并发、Poll limit 和任务切分粒度 |

## 6. 目标状态机

```text
PENDING -> RUNNING -> SUCCESS
              |
              -> FAILED

PENDING -> CANCELED

RUNNING 超过 recover_at:
  当前 attempt 标记为 LOST
  JobItem 回到 PENDING
  下一次 Poll 成功派发时 attempt_no + 1
```

约束：

- `attempt_no` 只在 JobItem 成功派发给节点时递增。
- Report 必须带回 Poll 时收到的 `attempt_no`。
- Report 的 `attempt_no` 不匹配时返回冲突，runtime 丢弃这次结果。
- Canceled 只允许从 PENDING 进入；RUNNING 不主动中断，等待自然结束。
- JobItem 最大执行时长应小于等于 SCF 超时。Collector planner 要负责把任务切小。

`attempt` 是同一个 JobItem 的一次执行尝试。它解决三个问题：

- 防止过期结果覆盖新状态：旧节点迟到上报时，`attempt_no` 不匹配，CloudNode 拒绝该结果。
- 记录重试历史：可以看到每次尝试在哪个节点执行、何时开始结束、为什么失败。
- 控制最终失败：超过服务端默认最大尝试次数后，JobItem 进入 FAILED。

## 7. 明确删除或暂缓

本轮不做以下能力：

```text
RenewLease RPC
LEASED 状态
每任务 lease_timeout_ms
Directive 控制指令
routing labels
protocol_version
payload_schema_version
owner_service / owner_ref
idempotency_key
max_inflight 配额
bytes payload + versioned proto
MySQL / Redis / 消息队列后端
InvokeSync 与 Poll 生命周期统一
```

可以保留为观测信息：

- `runtime_version` 可继续在节点心跳里上报，用于管理台查看云函数包版本，不参与 JobItem 调度。

## 8. 目标协议摘要

服务方法建议收敛为：

```text
SubmitJobItems
PollJobItems
ReportJobItemStatus
CancelJobItem
GetJobItem
ListJobItems
ListJobItemAttempts
```

JobItem 输入字段：

```text
space_id
job_id
job_item_id
job_type
code_package_id
params
priority
```

Poll 返回字段：

```text
space_id
job_id
job_item_id
job_type
code_package_id
params
attempt_no
```

Report 字段：

```text
space_id
node_id
job_item_id
attempt_no
status        // SUCCESS 或 FAILED
error_kind    // RETRYABLE 或 PERMANENT，FAILED 时必填
error_code
error_message
result_summary
```

查询详情字段：

```text
space_id
job_id
job_item_id
job_type
code_package_id
priority
status
running_node
attempt_no
recover_at
result_summary
last_error_kind
last_error_code
last_error_message
create_time
start_time
finish_time
```

所有时间字段使用 `google.protobuf.Timestamp`。

分页统一使用 `common.Page` 和 `common.PageResult`，对齐仓库现有协议；不单独引入 `page_size` / `page_token` 字段。`common.Page` 已支持 `page`、`size` 和 `cursor`。

目标分页形态：

```protobuf
message ListJobItemsReq {
  string space_id = 1;
  string job_id = 2;
  string job_type = 3;
  JobItemStatus status = 4;
  common.Page page = 5;
}

message ListJobItemsRsp {
  common.RetInfo ret_info = 1;
  repeated JobItemDetail items = 2;
  common.PageResult page = 3;
}
```

`ListJobItemAttempts` 默认不分页。一个 JobItem 的 attempt 数由服务端默认最大尝试次数限制，通常很小。

## 9. CloudRuntime 抽象

CloudRuntime 只提供运行时框架，不理解业务数据结构。

目标使用方式：

```go
runtime.Register("collect.kline", klineHandler)
runtime.Register("collect.symbol", symbolHandler)
runtime.Run(ctx)
```

handler 语义：

```text
输入：JobItem(params)
执行：解析 params，拉取业务数据，写入 Storage
输出：执行摘要，例如写入条数、时间范围、耗时
错误：区分 retryable / permanent
```

重要边界：

- 业务结果写入 Storage。
- CloudNode 只保存执行摘要、错误和 attempt。
- Runtime 不做续租，不处理控制指令，不参与复杂调度。

## 10. CloudNode 派发规则

默认派发规则保持简单：

```text
space_id 匹配
node_id 必须属于 space
节点运行的 package_id == job_item.code_package_id
节点 supported_workloads 包含 job_type
status = PENDING
ORDER BY priority DESC, create_time ASC
LIMIT poll_limit
```

派发成功后：

```text
status = RUNNING
running_node = node_id
attempt_no = attempt_no + 1
recover_at = now + 服务端配置的 recover_after
创建 attempt 记录
```

`recover_after` 由服务端配置，建议为：

```text
SCF 最大超时 + 60s 缓冲
```

## 11. SQLite 存储决策

本轮固定使用 SQLite：

- 单实例 CloudNode。
- 不支持多个 CloudNode 实例共享同一个 SQLite 文件。
- 使用 WAL、busy timeout 和必要索引。
- 队列表以 `space_id`、`job_item_id`、`status`、`recover_at`、`priority`、`create_time` 建索引。

可以在代码内部保留窄接口，便于测试和后续替换，但本轮不实现多后端适配。

## 12. 实施事项

- [ ] 更新 `cloudnode.proto`：统一 JobItem 协议，删除上述冗余字段和 RPC。
- [ ] 更新 CloudNode schema：将异步执行表和 attempt 表字段收敛到 JobItem 模型。
- [ ] 更新 CloudNode repository：实现 Submit、Poll、Report、Cancel、Query 和 RUNNING 超时回收。
- [ ] 更新 CloudRuntime：改为 JobItem runtime，支持 `job_type -> handler` 注册和标准执行结果。
- [ ] 更新 Collector taskpublisher：生成 `job_id`、`job_item_id`、`job_type`、`code_package_id`、`params`。
- [ ] 更新 Collector taskrunner：注册 `collect.*` handler，handler 写 Storage 并返回摘要。
- [ ] 更新 Web/CLI/API 类型和页面文案：使用 Job / JobItem / code package ID 语义。
- [ ] 更新文档：同步 `docs/云节点管理.md`、`docs/采集任务管理.md`、`packages/cloudruntime/README.md`。
- [ ] 增加测试：幂等提交、Poll 派发、超时回收、过期 attempt 拒绝、失败重试、Cancel PENDING、handler 注册缺失。

## 13. 验收标准

- CloudRuntime 代码路径只剩 `poll -> execute -> report`。
- 协议中不再暴露 lease、owner、schema version、routing labels、directive、max inflight。
- `job_item_id` 是 JobItem 幂等键。
- CloudNode 不保存业务大结果，只保存 `result_summary`。
- Collector handler 负责写 Storage。
- SQLite 是唯一队列存储。
- `InvokeSync` 不进入 collector 正式任务生命周期。
