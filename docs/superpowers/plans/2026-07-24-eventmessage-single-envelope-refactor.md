# EventMessage 单信封事件系统重构实施计划

> **状态：历史执行记录，禁止把“当前执行状态”当作当前架构。**
> 本文记录重构过程中的阶段性目标；其中的 Market Tick/Streamcalc、共享 DLQ、
> YAML Registry 和已删除事件不再存在。当前运行契约以
> [协议设计](../../协议设计.md)、[架构总览](../../架构总览.md)和
> [Event System CR Remediation](2026-07-24-event-system-cr-remediation.md)
> 为准。

> **For agentic workers:** REQUIRED SUB-SKILL: Use `superpowers:subagent-driven-development` (recommended) or `superpowers:executing-plans` to implement this plan task-by-task. Use `superpowers:test-driven-development` for every behavioral change and `superpowers:verification-before-completion` before declaring completion.

**Goal:** 删除 `MooxMessage` 及其兼容链路，让所有经过 NATS JetStream 的业务消息统一使用一层 `EventMessage`、结构化 Protobuf payload 和唯一事件注册表；同时保留真正有价值的可靠性机制，删除重复信封、散落主题、JSON/BytesValue 包装和无调用能力。

**Architecture:** `packages/jetstream` 只负责原始字节、NATS Header、Pull Consumer、ACK/NAK/TERM 和 Runner；`packages/events` 负责 `EventMessage`、事件注册表、主题渲染、结构化 payload 编解码与业务契约校验；`modules/eventbus` 只负责依据事件注册表和运行配置对账 Stream/Consumer/KV。模块不得直接构造 NATS 业务主题或发布裸业务消息。

**Tech Stack:** Go 1.25, NATS JetStream, Protocol Buffers, embedded YAML event registry, Pebble/SQLite outbox and inbox, Go workspaces.

## 当前执行状态（2026-07-24）

本计划已在 `feature/mooyang` 工作树中完成主要代码闭环，当前实现以“新项目、无历史兼容”为准：

- EventBus 业务消息统一使用 `packages/events/eventpb.EventMessage`；`packages/messagepb` 不再参与生产代码。
- Market 链路为 `TickReceived -> Streamcalc -> MarketKlineClosed -> Storage PrimaryStore -> DatasetRowsUpserted`，Streamcalc 不再直接调用 Storage RPC。
- Streamcalc 的生产 Consumer 只绑定 `market.tick.received` family，不再消费自己发布的 `market.kline.closed`。
- Storage Kline Consumer 将源 `EventMessage.event_id` 传入 PrimaryStore；DataNode 在同一 Pebble batch 持久化 source-event marker、行变更和 outbox，ACK 失败重投不会重复产生 DatasetRowsUpserted。
- CloudNode 使用 typed payload 和 Registry 派生的精确 subject；Trade/Strategy outbox 持久化完整 deterministic EventMessage。
- Storage View、Monitor、Archive、Factor、Trade、CloudNode、Streamcalc 的 poison message 均先经共享 DLQ 发布，DLQ 失败时保留原消息可重试，发布成功后才 TERM。
- EventBus API 已收敛为 `ListEvents/EventInfo`，`EventInfo` 暴露 Registry owner，并增加生产 EventType 注册校验、EventBus topology gate、`packages/events/architecture_test.go` 与 `scripts/check/verify-event-contracts.sh` 门禁；EventType 声明与 `AllEventTypes` 不一致、生产代码直接构造 EventType 都会失败。

目标 Linux/CGO Storage 验收已在配置的远端编译机完成；其余跨进程全链路验收仍需按对应模块证据逐项执行。后续勾选项只允许在有命令输出或 E2E 证据时更新，不以静态代码阅读代替目标环境证据。

本次修复后的本机验证记录（2026-07-24）：

- `./scripts/test/contract/test-go-workspace.sh`：通过，包含各模块 `go test`、`go vet` 和生成代码模块检查。
- `./scripts/check/verify-event-contracts.sh`：通过。
- `packages/events`、`modules/eventbus`、`modules/streamcalc`：`go test -race ./...` 通过。
- `git diff --check`：通过。
- 目标 Linux/CGO Storage：已完成 Linux/amd64 编译；Storage E2E 四项测试全部通过。
- 目标 Linux EventMessage 链路：`modules/streamcalc/test` 的
  `TestCollectorEventToStreamcalcAggregationE2E` 也已在同一远端工作目录通过。
- 本机 CGO Storage 市场链路：`TestMarketKlineToStorageOutboxE2E` 通过，测试实际构建并启动 Streamcalc server，覆盖 Tick -> Streamcalc -> KlineConsumer -> PrimaryStore -> DataNode source marker/outbox -> relay 以及 View/Factor/Archive durable fan-out；测试关闭首次消费连接强制 ACK 失败，重新绑定同一 durable 验证 JetStream 重投，未产生第二条 outbox event。
- 真实部署拓扑、生产配置下的全进程联调：仍需单独执行；本地市场 E2E 已在同一个 embedded NATS 上运行真实 TickCollector ingress、Streamcalc、Archive、Factor 生产二进制和 Storage KlineConsumer。Collector ingress 使用 `modules/collector/testkit` 调用真实 TickCollector，不再直接调用 Publisher.Publish。
- source-event marker 使用时间有序索引并按 256 条批次清理，批次之间释放 DataNode outbox 写锁；EventType 已改为不暴露 name/version 字段的 opaque value，并补齐 alias/变量架构门禁。
- EventType 不再暴露 `EventTypeFromSchema` 动态构造器；拓扑按 registry 提供的 `FamilyPatternForSchema` 查询。市场 E2E 明确断言 Tick/Kline payload symbol 与 envelope `subject_id` 均为 canonical ID，子进程诊断日志使用并发安全 buffer。

---

## 1. 结论

### 1.1 可以并且应该删除 `MooxMessage`

最终只保留以下外层消息：

```protobuf
message EventMessage {
  string event_id = 1;
  string event_name = 2;
  uint32 event_version = 3;
  string space_id = 4;
  string subject_id = 5;
  google.protobuf.Timestamp occurred_at = 6;
  bytes payload = 7;
}
```

`EventMessage` 比 `Event` 更合适：`Event` 容易同时指事件类型、领域事件和 payload；`EventMessage` 明确表示“在 EventBus 上传输的外层消息”。

不再保留以下 `MooxMessage` 字段或等价字段：

- `protocol_version`：事件版本已经由 `event_name + event_version` 表达。
- `topic`：由事件注册表的 subject 模板和 `space_id/subject_id` 唯一渲染。
- `kind`：命令改为 `*.requested` 事件，快照改为 `*.reported` 事件，无需 EVENT/COMMAND/SNAPSHOT 枚举。
- `producer`：静态归属由事件注册表 `owner` 管理；真正属于业务的数据放入对应 payload。
- `published_at`：JetStream metadata 已记录服务端接收时间，业务只保留 `occurred_at`。
- `trace`：当前个人量化系统不建立跨消息分布式追踪契约。
- `sequence`：需要顺序的领域在 payload 中定义领域序号；传输层不提供一个含义模糊的全局序号。
- `content_type`、`message_type`：NATS Header 固定为 EventMessage 媒体类型，payload 类型由注册表确定。
- `attributes`：业务扩展必须结构化进入 payload，不能回到任意字符串 Map。

### 1.2 NATS 与 EventMessage 的唯一映射

每条消息必须满足：

```text
Nats-Msg-Id = EventMessage.event_id
Content-Type = application/vnd.moox.event+protobuf
NATS Subject = Registry.RenderSubject(event_name, event_version, space_id, subject_id)
NATS Data = deterministic protobuf(EventMessage)
```

`subject_id` 是业务聚合/分区标识，不是已编码的 NATS token。`packages/events` 统一调用 `jetstream.EncodeSubjectToken`，业务模块不得自行拼接或解析 subject。

### 1.3 “命令”和“快照”也使用 EventMessage

统一 EventMessage 不等于把语义抹平。事件名必须显式表达语义：

- 原命令 `cloudnode job item` 改为 `event.cloudnode.job.execution.requested`。
- 原命令 `trade reconciliation` 改为 `trade.reconciliation.requested`。
- 原快照 `host metrics` 改为 `metrics.host.reported`。
- 原快照 `service metrics` 改为 `metrics.snapshot.reported`。

消费者不再读取 `kind` 判断消息类型，而是绑定注册表生成的事件 family，并校验 `event_name + event_version`。

### 1.4 不做兼容和迁移

本项目未上线，执行时采用一次性替换：

- 不实现 `MooxMessage`/`EventMessage` 双解码。
- 不双写新旧 subject。
- 不保留类型别名、废弃字段、`reserved` 字段或兼容配置。
- 不迁移历史 JetStream 消息、旧 SQLite/Pebble 测试数据和旧 outbox 行。
- 开发和测试环境直接删除旧 Stream 数据、重建数据库和重新对账 topology。

允许实施过程先增加新 API、再逐模块删除旧 API，但任何可合入提交都不得同时发布两种信封。

## 2. 可靠性边界

隔壁审查中“删除 outbox/inbox/DLQ，只依赖 JetStream”的建议不采纳。它们与 JetStream 解决的问题不同：

- **Outbox 保留**：解决本地状态提交成功但事件发布失败的原子性缺口。
- **Inbox/幂等表保留**：解决消息重复投递后数据库副作用重复执行。
- **DLQ 保留**：隔离无法解码、违反契约或超过重试上限的毒消息。
- **Streamcalc checkpoint 保留**：恢复窗口聚合状态，而不是替代 JetStream offset。
- **Storage View subject lane 保留**：保障单 Dataset 顺序、跨 Dataset 并发和 Backfill 互斥。

这些机制不得宣传为“全局 exactly-once”。最终语义是：

```text
JetStream at-least-once
+ deterministic event_id
+ Nats-Msg-Id duplicate window
+ consumer-side idempotency
+ transactional outbox where local state and event must commit together
```

本次可以删除的只是无生产调用或由新单信封架构取代的能力：

- `MooxMessage` codec。
- `Client.Publish(*MooxMessage)`。
- 接受 `[]*MooxMessage` 的 `PublishBatch`；未来确有批量需求时在 `packages/events` 新建无顺序保证的批量 API。
- `PullConsumer.Fetch` 的 `MooxMessage` 解码分支。
- `FetchRaw`/`Fetch` 双入口，最终只保留返回原始 Delivery 的 `Fetch`。
- 没有生产方持久化和恢复调用的 delivery token。
- Archive/Factor 的临时 raw adapter 和已无调用的 `sleepContext`。
- `packages/jetstream/storage_event.go` 中属于业务事件层的 subject 解析。

以下能力仍有生产用途，不删除：

- `packages/jetstream/credentials.go`。
- `packages/jetstream/keyvalue.go`。
- `packages/jetstream/subject_token.go`。
- `modules/eventbus` 的 topology reconciliation。
- `modules/eventbus` 的只读管理 RPC；是否降级为 HTTP/NATS CLI 应单独审查，不与信封重构混做。

## 3. 目标分层

```text
modules/*
  ├─ construct typed protobuf payload
  ├─ choose events.EventType
  └─ call events.Publisher / events.DecodeDelivery
                         │
                         ▼
packages/events
  ├─ EventMessage
  ├─ events.yaml
  ├─ payload factories and validation
  ├─ subject rendering
  └─ deterministic encode/decode
                         │
                         ▼
packages/jetstream
  ├─ PublishRaw
  ├─ raw PullConsumer.Fetch
  ├─ Delivery ACK/NAK/TERM/Progress
  ├─ Runner
  ├─ credentials
  └─ KeyValue
                         │
                         ▼
NATS JetStream
```

依赖方向必须保持：

```text
modules -> packages/events -> packages/jetstream -> nats.go
modules -> packages/*pb
modules/eventbus -> packages/events (读取注册表)
packages/jetstream -X-> packages/events
packages/jetstream -X-> packages/messagepb
```

## 4. 最终事件目录

`packages/events/registry/events.yaml` 是唯一事件目录。最终至少包含：

| Event name | Payload | Stream | `subject_id` | Owner |
|---|---|---|---|---|
| `market.tick.received` | `trpc.moox.market.Tick` | `MOOX_MARKET` | symbol/instrument ID | collector |
| `market.kline.closed` | `trpc.moox.market.KlineClosed` | `MOOX_MARKET` | symbol/instrument ID | streamcalc |
| `event.storage.dataset.rows.upserted` | `trpc.moox.storage.event.DatasetRowsUpserted` | `MOOX_STORAGE` | dataset ID | storage |
| `trading.signal` | `trpc.moox.trading.TradingSignal` | `MOOX_TRADE` | symbol | factor/strategy |
| `metrics.host.reported` | `trpc.moox.hostagent.HostMetric` | `MOOX_METRICS` | agent ID | hostagent |
| `metrics.snapshot.reported` | `trpc.moox.metrics.MetricReport` | `MOOX_METRICS` | `service/instance` | report |
| `dlq.message.rejected` | `trpc.moox.dlq.RejectedMessage` | `MOOX_DLQ` | rejecting consumer ID | consumers |
| `event.cloudnode.job.execution.requested` | `trpc.moox.cloudjob.JobExecutionRequested` | `MOOX_CLOUDNODE_EXEC` | `code_package/job_type` | cloudnode |
| `trade.order.intent.created` | `trpc.moox.trade.event.OrderSnapshot` | `MOOX_TRADE` | order ID | trade |
| `trade.execution.slice.ready` | `trpc.moox.trade.event.OrderSnapshot` | `MOOX_TRADE` | order ID | trade |
| `trade.order.acknowledged` | `trpc.moox.trade.event.OrderSnapshot` | `MOOX_TRADE` | order ID | trade |
| `trade.order.submit.unknown` | `trpc.moox.trade.event.OrderSnapshot` | `MOOX_TRADE` | order ID | trade |
| `trade.order.state.changed` | `trpc.moox.trade.event.OrderSnapshot` | `MOOX_TRADE` | order ID | trade |
| `trade.fill.received` | `trpc.moox.trade.event.FillReceived` | `MOOX_TRADE` | fill ID | trade |
| `trade.reconciliation.requested` | `trpc.moox.trade.event.ReconciliationRequested` | `MOOX_TRADE` | reconciliation request ID | trade |
| `trade.rebalance.requested` | `trpc.moox.trade.event.RebalanceRequested` | `MOOX_TRADE` | rebalance run ID | trade |
| `trade.rebalance.completed` | `trpc.moox.trade.event.RebalanceCompleted` | `MOOX_TRADE` | rebalance run ID | trade |
| `strategy.output.accepted` | `trpc.moox.strategy.event.StrategyOutputAccepted` | `MOOX_STRATEGY` | binding ID | strategy |

所有 subject 采用同一形式：

```text
moox.<event_name tokens>.v<version>.<encoded-space>.<encoded-subject>
```

示例：

```yaml
- name: metrics.host.reported
  version: 1
  payload: trpc.moox.hostagent.HostMetric
  subject: moox.metrics.host.reported.v1.<space>.<subject>
  stream: MOOX_METRICS
  partition_key: subject_id
  owner: hostagent
```

### 4.1 Strategy 与 TradingSignal 的边界

当前 Strategy runtime 输出的是 `hold/rebalance + target weights`，不是天然的 BUY/SELL/OPEN/CLOSE 信号。不得把 `strategy.action.accepted` JSON 机械转换成 `TradingSignal`。

本次采用两个清晰契约：

- `strategy.output.accepted`：记录一次策略输出被原子提交，payload 只包含 run/binding/strategy、action、targets、data revision 和 trigger time。
- `trading.signal`：仅在 Factor/Strategy 已能明确给出 symbol、side 和 OPEN/CLOSE/INCREASE/DECREASE 时发布。

后续若要把目标仓位转换为 TradingSignal，必须先定义“当前仓位、目标仓位、正负仓方向、零仓开平”的独立映射规则；不能在本次信封重构里凭 `target_weight` 猜测。

## 5. Payload 结构调整

### 5.1 HostMetric

删除 `MooxMessage.producer` 后，监控业务仍需要 Agent 身份，因此身份进入结构化 payload：

```protobuf
message HostMetric {
  string agent_id = 1;
  string hostname = 2;
  string boot_id = 3;
  string agent_version = 4;
  HostSnapshot snapshot = 5;
}
```

校验：

```text
EventMessage.space_id == "mooxsys"
EventMessage.subject_id == HostMetric.agent_id
agent_id is UUID
snapshot != nil
```

### 5.2 MetricReport

保持 `MetricSnapshot` 只描述指标数据，新建报告 payload：

```protobuf
message MetricReport {
  string service_name = 1;
  string instance_id = 2;
  string node_id = 3;
  string boot_id = 4;
  string service_version = 5;
  uint64 sequence = 6;
  MetricSnapshot snapshot = 7;
}
```

校验：

```text
EventMessage.space_id == "mooxsys"
EventMessage.subject_id == service_name + "/" + instance_id
service_name != ""
instance_id != ""
snapshot != nil
```

### 5.3 RejectedMessage

因为未上线，直接替换现有字段，不保留旧名：

```protobuf
message RejectedMessage {
  string rejected_by = 1;
  string original_message_id = 2;
  string original_subject = 3;
  string original_content_type = 4;
  bytes original_data = 5;
  string reason = 6;
  uint64 delivery_count = 7;
}
```

`EventMessage.subject_id = rejected_by`。若原始数据能解出合法 `space_id`，DLQ 事件沿用该空间；否则使用 `mooxsys`。

### 5.4 CloudNode JobExecutionRequested

在 `packages/cloudjobpb` 新建事件边界 payload，CloudNode RPC 类型继续作为 RPC 类型并在边界转换，避免事件包依赖模块内部生成代码：

```protobuf
message JobExecutionRequested {
  string job_id = 1;
  string job_item_id = 2;
  string job_type = 3;
  string code_package_id = 4;
  google.protobuf.Struct params = 5;
  int32 priority = 6;
}
```

`subject_id = code_package_id + "/" + job_type`，继续支持按代码包和任务类型绑定精确 consumer。

### 5.5 Trade 事件

在 `packages/tradeeventpb` 新建共享 payload。`OrderSnapshot` 可被多个不同事件复用，因此 Registry 不再强制“一种 payload 只能属于一个 event”。

```protobuf
message OrderSnapshot {
  string order_id = 1;
  string client_order_id = 2;
  string account_id = 3;
  string channel_id = 4;
  string symbol = 5;
  string market_type = 6;
  string base_asset = 7;
  string quote_asset = 8;
  string side = 9;
  string quantity = 10;
  string price = 11;
  bool reduce_only = 12;
  string filled_quantity = 13;
  string state = 14;
  string exchange_order_id = 15;
  int64 version = 16;
}

message FillReceived {
  string fill_id = 1;
  string order_id = 2;
  string account_id = 3;
  string channel_id = 4;
  string symbol = 5;
  string side = 6;
  string quantity = 7;
  string price = 8;
  string fee = 9;
  string fee_currency = 10;
  int64 traded_at_ms = 11;
}

message ReconciliationRequested {
  string request_id = 1;
  string account_id = 2;
  string channel_id = 3;
}

message RebalanceRequested {
  string run_id = 1;
  string account_id = 2;
  string channel_id = 3;
  string market_snapshot_id = 4;
  string position_snapshot_id = 5;
  string rules_version = 6;
}

message RebalanceCompleted {
  string run_id = 1;
  string status = 2;
}
```

`space_id` 只存在于外层 EventMessage，不在这些 payload 中重复。

### 5.6 StrategyOutputAccepted

在 `packages/strategyeventpb` 新建：

```protobuf
message StrategyOutputAccepted {
  string run_id = 1;
  string binding_id = 2;
  string strategy_id = 3;
  string strategy_version = 4;
  string action = 5;
  repeated StrategyTarget targets = 6;
  string data_revision = 7;
  google.protobuf.Timestamp trigger_time = 8;
}

message StrategyTarget {
  string instrument_id = 1;
  string symbol = 2;
  string market_type = 3;
  string target_weight = 4;
  string reason = 5;
}
```

`next_state` 和 `debug_info` 是 Strategy 内部状态，不进入跨模块事件。

## 6. 文件地图

### Create

- `packages/cloudjobpb/go.mod`
- `packages/cloudjobpb/job_events.proto`
- `packages/cloudjobpb/job_events.pb.go`
- `packages/cloudjobpb/Makefile`
- `packages/tradeeventpb/go.mod`
- `packages/tradeeventpb/trade_events.proto`
- `packages/tradeeventpb/trade_events.pb.go`
- `packages/tradeeventpb/Makefile`
- `packages/strategyeventpb/go.mod`
- `packages/strategyeventpb/strategy_events.proto`
- `packages/strategyeventpb/strategy_events.pb.go`
- `packages/strategyeventpb/Makefile`
- `packages/events/architecture_test.go`
- `scripts/check/verify-event-contracts.sh`
- E2E tests listed in the tasks below.

### Modify

- `packages/events/proto/eventpb/event_message.proto`
- `packages/events/eventpb/event_message.pb.go`
- `packages/events/registry/events.yaml`
- `packages/events/registry.go`
- `packages/events/message.go`
- `packages/events/decode.go`
- `packages/events/publisher.go`
- `packages/events/events_test.go`
- `packages/events/go.mod`
- `packages/hostmetricpb/host_metric.proto`
- `packages/hostmetricpb/host_metric.pb.go`
- `packages/metricspb/metrics.proto`
- `packages/metricspb/metrics.pb.go`
- `packages/dlqpb/dlq_payload.proto`
- `packages/dlqpb/dlq_payload.pb.go`
- `packages/jetstream/{publisher,consumer,delivery,runner}.go`
- `modules/eventbus/config/app.yaml`
- `modules/eventbus/internal/config/{config_types,config_defaults,config_validation}.go`
- `modules/eventbus/internal/registry/registry.go`
- `modules/eventbus/proto/eventbus.proto`
- all producers/consumers currently importing `packages/messagepb`
- Trade/Strategy outbox schema and relay code
- `go.work` and affected `go.mod` files
- `docs/协议设计.md`
- `docs/架构总览.md`
- `docs/2026-07-23-event-contract-refactor-plan.md`

### Delete

- entire `packages/messagepb/`
- `packages/jetstream/codec.go`
- `packages/jetstream/token.go` after confirming no production rehydration caller
- `packages/jetstream/storage_event.go`
- corresponding obsolete tests
- old JSON/`wrapperspb.BytesValue` event wrappers
- old EventBus `topics`/`topic_families` config and `MessageKind` fields
- dead Strategy runtime probe event
- dead Strategy topics with no producer and no consumer
- Archive/Factor adapter types made unnecessary by the raw `Fetch`

## 7. 实施任务

### Task 0: 建立可复现基线并保护当前工作

**Files:**
- Create `outputs/moox-eventmessage-baseline.txt`.
- Modify no production code.

- [ ] 当前工作区有大量未提交修改。先确认这些修改已被用户提交或安全保留；不得 reset、checkout 或覆盖。
- [ ] 在包含当前事件重构成果的精确 SHA 上创建独立 worktree，例如 `feature/eventmessage-single-envelope`。
- [ ] 记录 `git status --short --branch`、`git rev-parse HEAD`、`go version`、`protoc --version` 和 `protoc-gen-go --version`。
- [ ] 记录所有 `MooxMessage`、`messagepb`、`PublishBatch`、`FetchRaw`、`wrapperspb.BytesValue` 和硬编码 `moox.*` 主题的生产代码位置。
- [ ] 按具体 Go module 运行现有测试并记录结果；不能从仓库根目录用一次 `go test ./...` 代替多模块验证。
- [ ] 将结果保存到 `outputs/moox-eventmessage-baseline.txt`，后续不得删除。

Suggested commands:

```bash
rg -n "MooxMessage|packages/messagepb|PublishBatch|FetchRaw|wrapperspb.BytesValue" \
  --glob '*.go' --glob '*.proto' --glob '*.yaml' .
rg -n '"moox\.[^"]+"' modules packages --glob '*.go'
```

**Commit:**

```text
docs(event): capture single-envelope refactor baseline
```

### Task 1: 锁定 EventMessage 单信封契约

**Files:**
- Modify `packages/events/proto/eventpb/event_message.proto`.
- Regenerate `packages/events/eventpb/event_message.pb.go`.
- Modify `packages/events/message.go`.
- Modify `packages/events/decode.go`.
- Modify `packages/events/publisher.go`.
- Modify `packages/events/events_test.go`.

- [ ] 先增加失败测试，锁定 EventMessage 只有七个字段，字段号和名称与本计划一致。
- [ ] 修正 proto 的 `go_package` 为实际导入路径 `github.com/mooyang-code/moox/packages/events/eventpb;eventpb`。
- [ ] 不添加 `kind`、`producer`、`published_at`、`trace`、`sequence`、`attributes` 或 `protocol_version`。
- [ ] 新增 `Registry.ValidateMessage(*eventpb.EventMessage)`，校验 metadata、时间、注册事件和 payload 类型。
- [ ] 新增 `events.DecodeDelivery(registry, *jetstream.Delivery)`，统一校验 NATS message ID、Content-Type、subject 和 typed payload。
- [ ] 新增 `Publisher.PublishMessage(ctx, *eventpb.EventMessage)`，供 outbox relay 发布已经持久化的 EventMessage。
- [ ] `Publisher.Publish` 和 `PublishMessage` 最终都只能调用 `jetstream.Client.PublishRaw`。
- [ ] deterministic marshal 后使用 `event_id` 作为 `Nats-Msg-Id`。
- [ ] 增加负向测试：未知事件、版本 0、空 space/subject、无 payload、错误 payload 类型、subject 不匹配、Header message ID 不匹配、错误 Content-Type。
- [ ] 运行 `go test ./...` from `packages/events`.

**Commit:**

```text
refactor(events): lock the single EventMessage contract
```

### Task 2: 扩展注册表并允许 payload 复用

**Files:**
- Modify `packages/events/registry/events.yaml`.
- Modify `packages/events/registry.go`.
- Modify `packages/events/events_test.go`.
- Create and generate the new `packages/*eventpb` modules.
- Modify `go.work`.

- [ ] 先写测试证明两个不同 event 可以复用 `OrderSnapshot` payload。
- [ ] 删除 `Registry.byPayload` 和未使用的 `EventForPayload`；event name/version 到 payload 的映射仍必须唯一。
- [ ] 为本计划第 4 节所有事件增加 `EventType` 和 YAML schema。
- [ ] 增加 HostMetric、MetricReport、RejectedMessage、CloudJob、Trade、Strategy payload factories。
- [ ] schema 校验继续要求 name/version/payload/subject/stream/partition_key/owner。
- [ ] 每个 subject 模板必须恰好包含一个 `<space>` 和一个 `<subject>`。
- [ ] 删除 YAML 中不存在生产者和消费者的 `strategy.group_target.ready`、`strategy.execution.requested` 等旧条目。
- [ ] 增加“registry 中每个 payload factory 均可实例化”的测试。
- [ ] 增加“所有 schemas 排序稳定”的测试，避免 EventBus 输出漂移。
- [ ] 分别运行新 protobuf module 与 `packages/events` 测试。

**Commit:**

```text
feat(events): register every EventBus contract
```

### Task 3: 锁定 JetStream 原始传输能力

**Files:**
- Modify `packages/jetstream/publisher.go`.
- Modify `packages/jetstream/consumer.go`.
- Modify `packages/jetstream/delivery.go`.
- Modify `packages/jetstream/runner.go`.
- Modify related tests.

- [ ] 先写 PublishRaw/FetchRaw/ack/nak/term/progress 测试，证明原始传输路径不依赖业务信封。
- [ ] 确认 Raw Delivery 已暴露 RawData、RawMessageID、Subject、ContentType、DeliveryCount、metadata 和 ACK 所需 NATS message。
- [ ] Runner 测试只依赖 raw Delivery，不在 Runner 中解码任何业务消息。
- [ ] 不再向 JetStream 包增加 EventMessage 或其他业务 codec。
- [ ] 保持 credentials、KeyValue 和 subject token 文件及测试。
- [ ] 本任务只锁定迁移所需的 raw 能力；旧 Publish/Fetch API 暂时保留，避免后续模块迁移前产生不可编译提交。
- [ ] 所有模块完成 Tasks 5-11 后，由 Task 12 一次性删除旧 API、重命名最终 Fetch 并删除 `messagepb` 依赖。
- [ ] 运行 `go test ./...` and `go test -race ./...` from `packages/jetstream`.

**Commit:**

```text
test(jetstream): lock raw transport primitives
```

### Task 4: 让 EventBus 从事件注册表派生契约

**Files:**
- Modify `modules/eventbus/config/app.yaml`.
- Modify `modules/eventbus/internal/config/config_types.go`.
- Modify `modules/eventbus/internal/config/config_defaults.go`.
- Modify `modules/eventbus/internal/config/config_validation.go`.
- Modify `modules/eventbus/internal/registry/registry.go`.
- Modify `modules/eventbus/proto/eventbus.proto`.
- Regenerate `modules/eventbus/proto/eventbusgen`.
- Update unit and E2E tests.

- [ ] 删除配置中的 `topics` 和 `topic_families`，避免和 `events.yaml` 双份维护。
- [ ] 删除 `kind`、`payload_content_type`、`payload_version` 配置字段。
- [ ] Stream/Consumer/KV、retention、max age、max bytes 等运行参数仍留在 EventBus 配置。
- [ ] 对账器遍历 `events.DefaultRegistry().Schemas()`，通过 `FamilyPattern` 得到每个事件 family。
- [ ] 启动校验必须证明每个事件引用的 Stream 存在，且 Stream subjects 覆盖该 family。
- [ ] 启动校验必须拒绝：缺 Stream、缺 family 覆盖、同一 family 被多个不兼容 Stream 覆盖、consumer filter 不属于目标 Stream。
- [ ] 将只读 RPC 的 `TopicInfo` 改为 `EventInfo`，字段为 event_name/version/subject_pattern/stream/payload_type/owner。
- [ ] 删除 eventbus proto 对 `packages/messagepb` 的 import。
- [ ] 保持只读 RPC，不新增 Publish RPC。
- [ ] 增加完整注册表 topology E2E。
- [ ] 运行 `go test ./...` from `modules/eventbus`.

**Commit:**

```text
refactor(eventbus): derive governed subjects from the event registry
```

### Task 5: 迁移 Market、Storage、Streamcalc 生产链路

**Files:**
- Modify Collector publishers under `modules/collector/internal/jobs/tick` and source adapters.
- Modify Streamcalc consumer/processor/runner files.
- Modify Storage DatasetRowsUpserted publishers and consumers.
- Modify Archive and Factor storage-event consumers.

- [x] Collector 只能通过 `events.Publisher.Publish(events.TickReceived, *marketpb.Tick, ...)` 发布 Tick。
- [ ] Streamcalc 使用 `events.DecodeDelivery`，只接受 `TickReceived`，处理后发布 `MarketKlineClosed`。
- [ ] Streamcalc 保存 checkpoint 成功后才允许 Runner ACK。
- [ ] Storage publishers 继续持久化完整 deterministic EventMessage 到 outbox，再由 `PublishMessage` 发布。
- [ ] DatasetRowsUpserted 继续校验 outer/payload/row key 的 space/dataset identity。
- [ ] Storage View 保持同 Dataset lane 内顺序、跨 Dataset 并行和 Backfill 独占。
- [ ] Archive、Factor 的 handler 先全部改为处理 raw Delivery + `events.DecodeDelivery`；为兼容旧 `Fetch` 保留的临时 adapter 统一在 Task 12 删除。
- [ ] Factor inbox、batch first/last received time、min/max data time 和 replay 行为保持不变。
- [x] 增加 Collector ingress -> EventPublisher 的真实 E2E，以及 Streamcalc -> Storage -> Archive/Factor 生产进程的嵌入式 NATS E2E。
- [ ] 增加进程重启后 checkpoint/outbox/inbox 去重 E2E。
- [ ] 分别运行 Collector、Streamcalc、Storage、Archive、Factor module tests。

**Commit:**

```text
refactor(streaming): run market and storage flows on EventMessage
```

### Task 6: 迁移 HostAgent 和通用指标报告

**Files:**
- Modify `packages/hostmetricpb/host_metric.proto` and generated code.
- Modify `packages/metricspb/metrics.proto` and generated code.
- Modify `modules/hostagent/internal/app/app.go`.
- Modify `packages/report/handler.go`.
- Modify `modules/monitor/internal/hostmetrics/hostmetrics.go`.
- Modify `modules/monitor/internal/metrics/consumer.go`.
- Modify `modules/monitor/internal/metrics/message_store.go`.
- Update tests and E2E.

- [ ] 先写测试证明删除 `producer` 后 Agent/Service 身份仍完整保存。
- [ ] HostAgent 构造 HostMetric identity fields，subject_id 使用 agent ID。
- [ ] `packages/report` 构造 MetricReport，subject_id 使用 `service_name/instance_id`。
- [ ] message ID 保持稳定可去重；sequence 进入 MetricReport payload，不进入 EventMessage。
- [ ] Monitor 使用 typed payload identity 做 SysDeploy authorizer 校验、catalog 更新和 Storage 写入。
- [ ] `MetricMessageStore.CommitIngest` 改接收 EventMessage + MetricReport 或一个明确的本地 ingest struct，不再依赖 MooxMessage。
- [ ] Host metric clock-skew、UUID、snapshot 内容校验保持。
- [ ] 普通 metrics/hostmetrics 消费统一使用 Runner；毒消息进入统一 DLQ publisher。
- [ ] 增加 HostAgent -> Monitor 和 Report -> Monitor E2E。
- [ ] 运行 `packages/report`、HostAgent、Monitor module tests。

**Commit:**

```text
refactor(metrics): carry producer identity in typed event payloads
```

### Task 7: 迁移 CloudNode Job 队列

**Files:**
- Create `packages/cloudjobpb`.
- Modify `modules/cloudnode/internal/jobqueue/jetstream_queue.go`.
- Modify `modules/cloudnode/internal/jobqueue/naming.go`.
- Modify `modules/cloudnode/internal/jobqueue/payload.go`.
- Modify CloudNode bootstrap/config/tests.

- [ ] RPC `cloudnodegen.JobItem` 在 publish 边界转换为 `cloudjobpb.JobExecutionRequested`。
- [ ] EventMessage `space_id` 来自 RPC item space；`subject_id` 由 code package + job type 生成。
- [ ] 删除旧 `moox.event.cloudnode.exec.v1.jobitem.s.*.pkg.*.type.*` 拼接。
- [ ] consumer 使用 Registry 渲染 exact subject，并通过 `events.DecodeDelivery` 解码。
- [ ] `JobItemMessage.SubmittedAt` 改取 EventMessage.occurred_at。
- [ ] 保持 ActiveKVBucket、attempt、lease、ack 和 job state 行为。
- [ ] 增加不同 package/type 的路由隔离测试。
- [ ] 增加 publish -> poll -> report -> ACK E2E。
- [ ] 运行 `go test ./...` from `modules/cloudnode`.

**Commit:**

```text
refactor(cloudnode): publish job requests as governed events
```

### Task 8: 将 Trade JSON/BytesValue 事件改为 typed EventMessage

**Files:**
- Create `packages/tradeeventpb`.
- Modify `modules/trade/schema/bus.sql`.
- Modify `modules/trade/internal/infra/store/store.go`.
- Modify `modules/trade/internal/infra/bus/relay.go`.
- Modify producers under `modules/trade/internal/application`.
- Modify consumers under `modules/trade/internal/bootstrap`.
- Update Trade tests.

- [ ] 重建 `t_trade_outbox`：删除 topic/trace/request 列，保留 event_id、event_data、claim/attempt/time 字段。
- [ ] 重建 `t_trade_inbox`：将 `c_topic` 改为 `c_event_name`，以 consumer + event_id 做唯一约束。
- [ ] producer 在业务事务前构造 typed payload，在同一事务中持久化完整 EventMessage bytes。
- [ ] relay 解出并校验 EventMessage，通过 `events.Publisher.PublishMessage` 发布。
- [ ] 删除 JSON marshal + `wrapperspb.BytesValue` 双包装。
- [ ] order lifecycle 的多个事件复用 `OrderSnapshot`。
- [ ] reconciliation、rebalance、fill 使用各自 typed payload，不再用 map、run JSON 或裸字符串。
- [ ] workers 通过注册表 family 绑定、Runner 执行、typed payload 分派。
- [ ] 保持交易 side effect 与 inbox 插入在同一个 DB 事务内。
- [ ] 保持 outbox 发布失败重试和 Nats-Msg-Id 去重。
- [ ] 增加 relay 发布成功但 mark/delete 失败，重启后重复发布而消费者不重复副作用的 E2E。
- [ ] 增加所有 Trade 事件的 producer/consumer contract tests。
- [ ] 运行 `go test ./...` and focused `go test -race` from `modules/trade`.

**Commit:**

```text
refactor(trade): replace JSON bus payloads with typed events
```

### Task 9: 迁移 Strategy outbox，保持语义真实

**Files:**
- Create `packages/strategyeventpb`.
- Modify `modules/strategy/internal/domain/types.go`.
- Modify `modules/strategy/internal/store/commit.go`.
- Modify `modules/strategy/internal/store/outbox.go`.
- Modify `modules/strategy/internal/bus/publisher.go`.
- Modify Strategy schema/tests.

- [ ] 将 `strategy.action.accepted` 重命名为 `strategy.output.accepted`。
- [ ] 在 commit transaction 中构造并持久化 `StrategyOutputAccepted` EventMessage。
- [ ] outbox 删除 topic 列，只保存 event_id/event_data 和 relay 状态。
- [ ] publisher 删除 JSON MooxMessage 构造，改用 `events.Publisher.PublishMessage`。
- [ ] 删除通过发布 `strategy.run.completed` 假事件实现的 runtime probe；readiness 使用 NATS connection/JetStream account readiness。
- [ ] 不把 target weights 猜成 TradingSignal。
- [ ] 若代码中已有明确的 direct signal output，单独通过 `events.TradingSignal` 发布；否则本任务不制造虚假生产入口。
- [ ] 保留 outbox pending/oldest age 健康指标。
- [ ] 增加原子 commit、relay retry、restart dedupe 和 typed payload E2E。
- [ ] 运行 `go test ./...` from `modules/strategy`.

**Commit:**

```text
refactor(strategy): publish typed strategy output events
```

### Task 10: 统一 TradingSignal 生产与 Trade 消费边界

**Files:**
- Modify `packages/events/trading.go`.
- Modify Factor/Strategy signal publisher boundary if a production signal source exists.
- Modify `modules/trade/internal/bootstrap/trading_signal_worker.go`.
- Modify Trade signal store/tests.

- [ ] 保持最终 TradingSignal 无 confidence、strength 字段。
- [ ] 保持 SignalAction 为 OPEN/CLOSE/INCREASE/DECREASE。
- [ ] signal_id、strategy_id、symbol、side、action、signal_time 必填。
- [ ] `EventMessage.subject_id == TradingSignal.symbol`。
- [ ] Trade 只持久化 recommendation，不因收到 TradingSignal 自动下单；账户、通道、数量和执行策略仍是独立显式决策。
- [ ] 如果 Factor/Strategy 当前没有合法生产数据，删除伪造测试生产入口以外的假接线，并在文档标记“契约和消费者就绪，生产策略需显式输出 signal”。
- [ ] 保留 embedded NATS contract E2E，证明合法 signal 可解码、重复 event_id 不重复入库、非法 signal TERM/DLQ。

**Commit:**

```text
refactor(trading): enforce the governed signal boundary
```

### Task 11: 统一 DLQ 为 EventMessage

**Files:**
- Modify `packages/dlqpb/dlq_payload.proto` and generated code.
- Add a shared DLQ publisher under `packages/events` or a narrowly named package.
- Modify Storage View, Monitor, Archive and other DLQ callers.
- Update tests.

- [ ] 先写一个共享 builder/publisher，输入 raw Delivery、rejecting consumer ID 和 reason。
- [ ] builder 尝试读取原始 EventMessage 的 space，仅用于选择 DLQ space；原消息即使损坏也必须可隔离。
- [ ] Event ID 使用确定性格式，至少包含原 event/message ID 和 rejecting consumer，避免不同消费者冲突。
- [ ] 原始 subject、Content-Type、raw bytes、delivery_count 全部进入 RejectedMessage payload。
- [ ] DLQ publish 失败必须 RETRY 原消息，不能 TERM 后丢失。
- [ ] DLQ publish 成功后 poison message TERM。
- [ ] 删除每个模块各自的 MooxMessage rejection builder。
- [ ] 增加 malformed envelope、payload validation failure、retry exhausted 三类测试。
- [ ] 增加 DLQ EventMessage 可由 registry 正确解码的 E2E。

**Commit:**

```text
refactor(events): centralize typed DLQ publication
```

### Task 12: 删除 messagepb 和全部历史路径

**Files:**
- Delete `packages/messagepb`.
- Modify all affected `go.mod`, `go.sum`, `go.work`.
- Delete obsolete code/tests/config.
- Create `scripts/check/verify-event-contracts.sh`.
- Create `packages/events/architecture_test.go`.

- [ ] 删除整个 `packages/messagepb` module。
- [ ] 删除所有 `replace/require` 和 generated proto imports。
- [ ] 删除 EventMessage/JetStream 业务信封中的 `MessageKind`、`Producer`、`TraceContext`、`ProtocolVersion` 残留；Python worker 握手和 CloudNode RPC 的独立 `ProtocolVersion` 不属于事件信封，继续保留。
- [ ] 删除 `application/x-protobuf; message=google.protobuf.BytesValue` 和 event payload JSON。
- [ ] 删除旧 topic constants 和直接 subject 拼接。
- [ ] 删除 `Client.Publish`、旧 `PublishBatch` 和 `packages/jetstream/codec.go`。
- [ ] 将 raw `FetchRaw` 重命名为最终唯一的 `Fetch`，同步 Runner interface 和所有调用方。
- [ ] 删除 `Delivery.Message`、legacy codec/adapter 和 `packages/jetstream/storage_event.go`。
- [ ] Task 0 确认无生产 rehydration 调用后，删除 persistent delivery token、AckToken/NakToken fallback 和 `packages/jetstream/token.go`。
- [ ] `packages/jetstream/go.mod` 删除 `packages/messagepb` 依赖。
- [ ] 添加 architecture gate，至少执行：

```bash
! rg -n "MooxMessage|packages/messagepb|messagepb|moox_message|messagepb\.MessageKind" \
  --glob '*.go' --glob '*.proto' --glob '*.yaml' --glob '!**/*_test.go' .
! rg -n "wrapperspb.BytesValue|google.protobuf.BytesValue" \
  --glob '*.go' --glob '*.proto' .
! rg -n '\.PublishRaw\(' modules
! rg -n 'Client\.Publish\(' modules
```

- [ ] gate 允许 `PublishRaw` 只存在于 `packages/events` 和 `packages/jetstream` 自身测试。
- [x] gate 校验生产模块出现的 EventType 全部在 `events.yaml` 注册，并拒绝 alias/变量绕过。
- [ ] gate 校验每个 registry schema 被 EventBus Stream 覆盖。
- [ ] 运行 `go work sync`，审查 go.sum 变更，禁止无关升级。

**Commit:**

```text
chore(event): remove the legacy MooxMessage stack
```

### Task 13: 全链路验证和故障恢复

**Files:**
- Add/update E2E tests under affected modules.
- Modify CI/module verification scripts.
- Update `outputs/` evidence.

- [ ] EventBus 启动并完成 registry reconciliation。
- [x] Collector Tick -> Streamcalc Kline -> Storage DatasetRowsUpserted：同一 embedded NATS 上由真实 TickCollector 产生 Tick，Streamcalc、KlineConsumer 和 Storage outbox 继续处理；同时验证 Collector payload symbol 与 envelope subject_id 一致。
- [x] DatasetRowsUpserted -> Storage View/Archive/Factor，验证生产 Archive/Factor 进程 durable ACK。
- [ ] HostAgent/Report -> Monitor，验证 producer identity 未丢失。
- [ ] CloudNode submit -> exact route poll -> report/ack。
- [ ] Trade outbox -> workers，验证 typed lifecycle events。
- [ ] TradingSignal -> Trade recommendation inbox，验证幂等。
- [ ] poison EventMessage -> DLQ -> original TERM。
- [ ] outbox publish 成功但本地完成标记失败 -> 重启 -> 重发 -> consumer 去重。
- [ ] consumer 处理成功但 ACK 失败 -> redelivery -> side effect 不重复。
- [ ] Streamcalc 保存 checkpoint 后进程退出 -> 重启恢复窗口。
- [ ] EventBus 无权限查询 ConsumerInfo 时 readiness 明确失败。
- [x] 在目标 Linux/CGO 环境运行 Storage E2E，不能以默认跳过代替验收。

Per-module commands:

```bash
cd packages/jetstream && go test -race ./...
cd packages/events && go test -race ./...
cd modules/eventbus && go test ./...
cd modules/collector && go test ./...
cd modules/streamcalc && go test ./...
cd modules/storage && go test ./...
cd modules/archive && go test ./...
cd modules/factor && go test ./...
cd modules/hostagent && go test ./...
cd packages/report && go test ./...
cd modules/monitor && go test ./...
cd modules/cloudnode && go test ./...
cd modules/strategy && go test ./...
cd modules/trade && go test -race ./...
```

- [ ] 保存每个 module 的命令、结果和 exact SHA；不以“静态检查通过”代替实际测试。

**Commit:**

```text
test(event): verify EventMessage flows and recovery
```

### Task 14: 更新架构文档

**Files:**
- Modify `docs/协议设计.md`.
- Modify `docs/架构总览.md`.
- Modify `docs/2026-07-23-event-contract-refactor-plan.md`.
- Modify relevant deployment/runbook docs.

- [ ] 文档中只出现 EventMessage 单信封。
- [ ] 画出 Collector -> Streamcalc -> Storage、Strategy/Factor -> Trade、Host/Service -> Monitor 三条主链路。
- [ ] 说明 event_name/version/space_id/subject_id 的职责。
- [ ] 说明 payload 必须是注册的结构化 Protobuf。
- [ ] 说明 subject 由 Registry 渲染，模块不得硬编码。
- [ ] 说明 retention 是 JetStream Stream 的数据保留策略，不是事件契约字段。
- [ ] 说明 outbox/inbox/DLQ/checkpoint 的边界和 at-least-once 语义。
- [ ] 删除 MooxMessage、kind、producer envelope、双信封和旧 topic 示例。

**Commit:**

```text
docs(event): document the EventMessage-only architecture
```

### Task 15: 独立 Agent 代码审查

**Files:**
- No planned production edits until findings are confirmed.

- [ ] 新起独立 Agent，不向其提供“已经正确”的结论，只提供目标计划、base SHA、head SHA 和测试证据。
- [ ] 要求逐项核查本计划，而不是只看 diff。
- [ ] 重点审查：事件语义是否真实、payload identity 是否完整、subject 是否唯一派生、outbox/inbox 原子性、DLQ 失败行为、ACK 时机、跨 Dataset 并发、Trade 副作用幂等。
- [ ] 对 Agent 的每条意见先在当前代码和测试中复核，不能机械照单全收。
- [ ] 修复有效问题后重新运行受影响 module tests 和全链路 gate。
- [ ] 第二次独立复核必须基于修复后的 exact SHA。

Final acceptance requires:

```text
zero messagepb imports
zero MooxMessage symbols
zero legacy subject publishers
zero JSON/BytesValue EventBus payload wrappers
all EventBus business messages decode as registered EventMessage
all module tests pass
critical race tests pass
all required E2E pass on the target environment
independent review has no unresolved P0/P1/P2 findings
```

## 8. 建议提交顺序

1. Baseline evidence.
2. EventMessage API and registry.
3. New typed payload packages.
4. EventBus registry-derived topology.
5. Market/Storage streaming flow.
6. Metrics flow.
7. CloudNode flow.
8. Trade flow.
9. Strategy/TradingSignal flow.
10. Shared DLQ.
11. JetStream legacy API deletion and `messagepb` deletion.
12. E2E, docs and independent review fixes.

实施中可以短暂保留旧 API 以维持编译，但禁止双写、禁止兼容解码，最终删除任务必须在合入前完成。

## 9. 合入与远端验收

- [ ] 从目标 feature branch 的最新远端 SHA rebase/merge，确认用户已有修改未丢失。
- [ ] `git diff --check` 通过。
- [ ] 所有模块验证基于最终 HEAD 重跑。
- [ ] 合并到 `feature/mooyang` 后再次执行关键 contract/E2E。
- [ ] push 到远端。
- [ ] 使用 `git ls-remote origin refs/heads/feature/mooyang` 验证远端 SHA 等于本地合并 SHA。
- [ ] 只有远端 ref 验证完成后才能报告“已合入并推送”。

## 10. 本次实施记录（2026-07-24）

本计划已在当前 `feature/mooyang` 工作区执行，采用新项目一次性替换策略，不保留兼容层：

- 已删除 `packages/messagepb`、`MooxMessage`、JetStream 业务编解码器、旧 `PublishBatch`/`FetchRaw`、持久化 Delivery token 和旧 Storage event helper。
- 已将 `packages/jetstream` 收敛为 raw transport，将 `packages/events` 作为唯一 `EventMessage` 注册表、Subject、Payload 编解码和发布/消费边界。
- 已迁移 Collector、Streamcalc、Storage、Archive、Factor、Strategy、Trade、CloudNode、HostAgent、Monitor、Report、EventBus 及相关 E2E/契约测试。
- 已将 `DatasetRowsUpserted` 和 DLQ payload 放入独立公共包；删除 Storage 的 `DatasetFieldsChanged` proto 及 generated code。
- 已删除 Trade 旧 subject 别名，并注册 `trade.order.intent.created`；EventBus topology 校验直接读取事件注册表。
- 已修复 EventBus proto Makefile，生成代码可重复生成且不依赖已删除的 message proto。
- 已更新架构、EventBus、主机监控和指标运维文档中的单信封说明。

验证记录：packages/events、packages/jetstream、packages/storagepb、packages/metricspb、packages/hostmetricpb、packages/report、modules/eventbus、archive、cloudnode、factor、monitor、storage、strategy、streamcalc、trade 的 `go test ./...` 均通过；packages/events、packages/jetstream、modules/trade 的 `go test -race ./...` 通过；Storage/EventBus proto Makefile 均可执行。Admin 全量测试仍有一个与本次事件重构无关的既有部署 seed 描述断言失败：`storage-primary` 描述期望值与当前 seed 不一致，未修改该无关行为。

独立 Agent 已对最终工作区进行静态代码审查；审查意见需逐条回到当前代码和测试复核后，才能作为收尾结论。本次尚未执行 commit/push，也未宣称远端合入。

### 追加修复与最终验证（2026-07-24）

根据独立审查复核并补齐以下问题：

- Storage 内部仍需要一个本地写入结果模型，因此将 `RowsUpserted` 放回
  `modules/storage/proto/rows.proto`；公共 EventBus 契约只使用
  `packages/storagepb.DatasetRowsUpserted`，不再删除 Storage 编译所需的内部类型。
- 根 `Makefile` 现在显式生成 `packages/dlqpb`、`packages/storagepb`、`packages/events`；
  `packages/events/Makefile` 固定使用仓库内 proto include，避免生成依赖临时目录。
- EventBus 的 ConsumerTemplate 校验现在必须命中 `packages/events` Registry 中的事件族，
  并增加缺失/错误 Stream 的负向测试。
- 独立复核发现 Stream 事件族校验不能只判断主题“有交集”，已改为完整覆盖判断；
  `*` 不再被错误地当作可以覆盖 `>`，并增加窄主题负向测试。
- Strategy、Trade、CloudNode、Storage 业务代码不再直接调用 `PublishRaw`；统一构造
  `events.Publisher` 并通过注册事件发布，raw transport 只保留在 `packages/events` 和
  `packages/jetstream` 边界内。
- 补齐当前架构文档中的旧 `messagepb`/`MooxMessage` 说明，统一改为 EventMessage、
  `packages/events`、`packages/storagepb` 和 `DatasetRowsUpserted`。

本轮验证结果：

- `go test ./...`：`packages/events`、`modules/eventbus`、`modules/storage`、
  `modules/cloudnode`、`modules/strategy`、`modules/trade` 全部通过。
- `GOWORK=off go test -mod=readonly ./...`：`packages/events` 通过。
- `go test -race ./...`：`packages/events`、`packages/jetstream`、`modules/trade` 通过。
- `make`：`packages/dlqpb`、`packages/storagepb`、`packages/events`、
  `modules/storage/proto`、`modules/eventbus/proto` 均可重复生成。
- 静态契约门禁和 `git diff --check` 通过；业务模块生产代码中无 `PublishRaw` 调用，
  无 `MooxMessage`/`messagepb` 依赖和旧事件主题发布。
- 首轮独立 Agent 审查发现的 P1 已修复，EventBus 受影响测试重新通过；第二轮独立 Agent
  已基于修复后的工作区完成只读复核，无 P0/P1/P2 阻塞问题。其提出的两个 P3 格式问题
  已通过 `gofmt` 处理，计划状态更新为最终验证完成。
- 基线和验证记录已保存到 [`outputs/moox-eventmessage-baseline.txt`](../../../outputs/moox-eventmessage-baseline.txt)。
  macOS 和 Linux 目标机均使用 `CGO_ENABLED=1` 运行 Storage E2E；远端产物已核验为
  Linux/amd64 ELF。压缩回传步骤因长时间无进展停止，未将不完整文件作为本地产物。
- 当前未执行 commit/push；远端合入仍是后续明确操作，不在本次任务范围内。
