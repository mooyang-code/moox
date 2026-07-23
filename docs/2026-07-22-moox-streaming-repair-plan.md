# MooX Streaming、事件模块与 Storage View 并发实施计划

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 在现有 `Go + NATS JetStream` 基础上，建立统一、显式、可治理的事件模块，打通 `Collector -> NATS -> streamcalc -> Storage` 的实时流计算路径；同时修复消费可靠性、Storage View 全局队头阻塞和 Factor Runtime 边界问题。

**Architecture:** `modules/eventbus` 作为独立部署的 NATS/JetStream 基础设施服务；`packages/events` 作为所有业务事件的唯一契约和发布/消费入口；业务模块不得直接拼接 NATS Subject 或发布裸消息。实时链路采用 `Collector -> EventMessage -> NATS -> streamcalc -> Storage`，Storage 通过 Outbox 发布 `storage.rows.upserted`，下游 Factor、Strategy、View、Archive 再消费标准事件。Storage View 第一阶段按 `delivery.Subject` 建立有序 Lane，使不同 Dataset 并行、同一 Dataset 保序；暂不做同一 Dataset 内按标的并发，直到补齐 Row/Field revision 语义。

**Tech Stack:** Go 1.25、NATS JetStream、Pebble、DuckDB、Bleve、SQLite、Protobuf、Prometheus、现有 MooX 多模块 `go.work`。

---

## 设计决策与非目标

### 已确认的决策

1. `PublishBatch` 不额外拆分批次；保留现有 API，注释明确“结果按输入顺序返回，但不保证 JetStream 发布顺序”。当前 K 线批次通常由相互独立的标的组成，不依赖跨标的发布顺序。
2. Factor 是系统唯一允许依赖 Python Runtime 的生产模块；Go/NATS/Storage/Trade/Strategy 仍保持 Go 二进制部署。
3. Storage View 第一阶段使用 `Subject` 级别有序 Lane：不同 Dataset 并行，同一 Dataset 内保持消息顺序。
4. Storage View 第一阶段目标参数为 `MaxAckPending=8`、`FetchBatch=8`、`Workers=4`，参数必须可配置并通过压测调整。
5. 新项目不做历史兼容：直接用新的 `packages/events` 和 `EventMessage` 替换旧的通用 `MooxMessage`、`DatasetFieldsChanged` 语义，不保留旧主题别名。
6. 事件统一放在 `packages/events` 管理；`event_name`、`version`、`space`、`subject` 必须作为外层 `EventMessage` 字段显式提供，消费者不得依赖解析 NATS Subject 获取业务元数据。
7. `EventMessage` 只保留事件事实所需的核心字段；`protocol_version`、`topic`、`content_type`、`payload_type`、`published_at`、`producer`、`causation_id`、`correlation_id`、`attributes`、`partition_key` 等不进入核心消息体。
8. `retention` 是 EventBus JetStream Stream 的运行策略，表示消息可供重放/恢复的保留窗口，不是事件 Payload 或事件契约字段；ACK/NAK、`AckWait`、`MaxDeliver`、`MaxAckPending` 另行管理。
9. `modules/eventbus` 是独立部署服务，负责 NATS/JetStream、Stream、Durable Consumer、事件注册表校验、ACL/TLS 和拓扑健康检查，不承载 K 线聚合、Factor 或其他业务 Handler。

### 明确不做

- 不替换 NATS JetStream。
- 不把 Storage View 直接改成 `MaxAckPending=1000`。
- 不在当前阶段按 symbol/RowKey 拆分 Storage 事件。
- 不把 Factor 的实时 Durable Consumer 直接改成 `DeliverAll`。
- 不承诺 exactly-once；统一目标是 at-least-once transport + business idempotency。
- 不再使用一个泛化的 `DatasetFieldsChanged` 覆盖所有事件；每个业务事实必须有稳定的 `event_name`、版本和 Payload 类型。
- 不允许业务模块直接调用裸 `jetstream.Publish`、手写 NATS Subject 或让消费者通过 Subject 反推业务字段。
- 不把 `retention` 写入事件契约 YAML；它只出现在 EventBus Stream topology 配置中。
- 不在实时链路中让 Collector 同时直接写 Storage、又发布同一事实给 streamcalc，避免双写和重复计算。
- 不使用 `git reset`、`git checkout` 等方式覆盖已有未提交修改。

## 2026-07-23 事件模块与 streamcalc 设计落版

本节是对前述讨论的最终收敛，优先级高于本文此前针对旧 `MooxMessage`/`DatasetFieldsChanged` 的示例。由于 MooX 尚未上线，实施时直接按新契约改造，不设计历史消息兼容层。

### 1. EventMessage 外层契约

推荐在 `packages/events/proto/event_message.proto` 中定义：

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

字段规则：

| 字段 | 规则 |
|---|---|
| `event_id` | 全局幂等键；由事实生产者稳定生成，重试/Outbox 重放不得变化 |
| `event_name` | 事件逻辑名称，例如 `market.trade.received`，不使用模糊的 `changed` |
| `event_version` | Payload/语义版本；新项目直接演进版本，不承担 `protocol_version` 兼容层职责 |
| `space_id` | 业务空间，例如交易所、账户域或策略空间；由事件注册表声明是否必填 |
| `subject_id` | 事件主体，例如 `BTC-USDT`、Dataset ID；无主体事件可为空 |
| `occurred_at` | 事实发生时间，供 event-time 聚合、迟到处理和回放使用 |
| `payload` | 具体 Protobuf Payload；Dataset、频率、窗口、交易 ID 等业务字段放在 Payload 内 |

明确不放入核心消息体的内容：

- `protocol_version`：新项目不增加另一套协议版本；事件版本已经覆盖消息语义演进。
- `topic`：NATS Subject 是基础设施路由信息，不是重复的业务元数据。
- `content_type`、`payload_type`：由注册表和生成代码确定，不让生产者重复填写。
- `published_at`、`producer`：由 JetStream/Outbox/消息头和观测系统记录。
- `causation_id`、`correlation_id`、`attributes`：暂不进入核心契约；未来确实需要时放入受治理的 Header/Trace，不向 Payload 添加自由扩展字段。
- `partition_key`：由注册表根据 `subject_id` 或受控 Subject 模板推导。
- `source_sequence`、`revision`、`dataset_id`、`freq`：仅在对应业务 Payload 中定义，不能污染所有事件。

`packages/events` 初始目录：

```text
packages/events/
  proto/event_message.proto
  registry/events.yaml
  spec.go
  publisher.go
  consumer.go
  subject.go
  validation.go
```

它建立在现有 `packages/jetstream` 之上：`packages/jetstream` 只负责通用传输、Pull Consumer、ACK 和重试动作；`packages/events` 负责类型化事件注册、Subject 生成、Envelope 编解码、Payload 校验和业务模块可用的发布/消费 API。

业务模块只允许使用类似下面的入口：

```go
events.Publish(ctx, events.MarketTradeReceived, payload, events.PublishOptions{
  EventID:    eventID,
  OccurredAt: eventTime,
  SpaceID:    spaceID,
  SubjectID:  subjectID,
})
```

不允许业务代码直接写：

```go
jetstream.Publish(ctx, "moox.market.trade.received.v1.crypto.BTC-USDT", rawBytes)
```

### 2. Event Registry 与事件治理

注册表是所有事件的单一事实来源，至少管理：逻辑名称、版本、Payload 类型、NATS Subject 模板、Stream、分区/顺序键、Owner 和上下文必填规则。

建议文件为 `packages/events/registry/events.yaml`：

```yaml
version: 1
events:
  - name: market.trade.received
    version: 1
    payload: trpc.moox.market.TradeReceived
    subject: moox.market.trade.received.v1.<space>.<subject>
    stream: market
    partition_key: subject_id
    owner: collector
  - name: market.kline.closed
    version: 1
    payload: trpc.moox.market.KlineClosed
    subject: moox.market.kline.closed.v1.<space>.<subject>.<freq>
    stream: market
    partition_key: subject_id
    owner: streamcalc
  - name: storage.rows.upserted
    version: 1
    payload: trpc.moox.storage.RowsUpserted
    subject: moox.storage.rows.upserted.v1.<space>.<dataset>
    stream: storage
    partition_key: subject_id
    owner: storage
```

规则：

1. 每个事件必须注册，Payload 类型必须存在且可反射校验。
2. 事件版本、Payload 类型、Subject 版本和 Stream 映射必须一致。
3. 一个事件只能落到一个 Stream class；Stream 的 `retention`、容量和消费策略不写入事件契约。
4. Subject 只用于路由，运行时仍校验 Subject 与 `EventMessage` 的 `event_name/version/space_id/subject_id` 一致。
5. 业务模块的生产者、消费者、ACL 和文档由注册表校验或生成；CI 禁止出现未注册的裸 Subject。
6. 存储事件统一改名为 `storage.rows.upserted`；不保留 `DatasetFieldsChanged` 兼容别名。

### 3. Retention 与 EventBus 服务边界

`retention` 只表示 JetStream Stream 的消息保留窗口，例如 `market=168h`、`storage=72h`。它影响：

- Consumer 重启后能回放多久；
- streamcalc、Factor、Archive 的恢复和 Backfill 能否直接依赖事件总线；
- JetStream 磁盘占用和过期清理压力。

它不表示 Storage 已落盘数据的保留期，也不改变 ACK、NAK、`AckWait`、`MaxDeliver` 或 `MaxAckPending`。

因此应把配置拆开：

```yaml
streams:
  - name: market
    jetstream: MOOX_MARKET
    retention: 168h
    max_bytes: 0
  - name: storage
    jetstream: MOOX_STORAGE
    retention: 72h
    max_bytes: 0
```

`modules/eventbus` 是一个独立部署的服务：启动时加载 topology 和事件注册表，创建/校验 Stream、Durable Consumer、ACL/TLS 与健康检查；其他服务通过 NATS 连接使用它。它不包含业务事件 Handler，也不执行 K 线聚合。

### 4. 初始事件分类

只把已经发生的事实命名为 Event；请求执行某件事时，未来单独定义 `CommandMessage`，状态快照时单独定义 `SnapshotMessage`，不把三者混在 `EventMessage` 中。

```text
market.trade.received
market.quote.updated
market.kline.updated
market.kline.closed
storage.rows.upserted
storage.dataset.created
storage.dataset.subject_bound
factor.calculation.requested
factor.calculation.completed
strategy.signal.generated
trade.order.intent_created
trade.order.state_changed
trade.fill.received
dlq.event.rejected
```

### 5. Collector、streamcalc 与 Storage 的事实流

目标链路：

```text
Collector
  -> market.trade.received / market.kline.closed
  -> NATS JetStream
  -> streamcalc
  -> Storage WriteFields
  -> storage.rows.upserted
  -> Factor / Strategy / Storage View / Archive
```

当前 Binance Collector 仍存在直接调用 Storage RPC、只接收一-shot K 线并过滤 closed Kline 的路径；实施时必须明确区分：

- 实时路径：Collector 只发布标准市场事件，streamcalc 负责聚合并写 Storage，写成功后由 Storage Outbox 发布 `storage.rows.upserted`。
- 历史/回补路径：可以保留批量导入或直接 Storage 写入，但必须明确它是独立的 backfill path，不能与同一实时事实做双写。

### 6. Event 生命周期与幂等

```text
producer creates fact
  -> Registry validates
  -> Outbox persist
  -> JetStream publish
  -> Consumer decode
  -> Inbox/idempotency check
  -> handler updates state and downstream outbox
  -> ACK
```

ACK 只能发生在 Inbox/状态/下游 Outbox 的业务提交完成之后。重复 `event_id` 必须视为已处理并 ACK；临时错误使用 `InProgress`/NAK；Payload 不合法或不可恢复错误进入 DLQ/TERM。

`event_id` 的稳定生成规则：交易事件使用 `exchange + trade_id`；K 线事件使用 `source + subject + freq + window_start + revision`；Storage 事件使用 Storage Outbox ID。重试和 Outbox 重放不能重新生成 ID。

## 文件责任边界

| 文件/目录 | 责任 |
|---|---|
| `modules/eventbus` | 独立部署的 NATS/JetStream topology、Stream、Durable、ACL/TLS 和健康检查服务；不承载业务 Handler |
| `modules/eventbus/config/app.yaml` | EventBus Stream、Topic Family、Durable Consumer 的事实来源；`retention` 只在 Stream topology 中配置 |
| `packages/events` | EventMessage、事件注册表、Subject 生成、Payload 编解码/校验、类型化 Publish/Consume API |
| `packages/jetstream` | 通用 NATS/JetStream Publish、Consumer Bind、Delivery ACK/NAK/TERM 生命周期；不承载业务事件命名 |
| `modules/storage/internal/service/datanode/pebble` | Fact + Outbox 原子提交与 Storage 事件 Envelope |
| `modules/storage/internal/service/view` | Storage View 消费、Subject Lane、Live/Backfill 协调 |
| `modules/streamcalc` | 消费市场事件、按 event-time 聚合 K 线、维护窗口状态、写 Storage；不直接依赖 Python Runtime |
| `modules/factor` | Storage 事件实时触发、Factor batch window、Python Runtime |
| `modules/archive` | Storage 事件解码、Journal 幂等与 Parquet 归档 |
| `modules/monitor`、`modules/trade` | 消费 ACK/重试/DLQ 行为统一接入与可观测性 |
| `modules/eventbus/test`、各模块 `test` 目录 | 跨模块拓扑、重复消息、重启和恢复验证 |

## 执行阶段和依赖

为避免先在旧契约上扩展业务，实际执行顺序调整为：

```text
Phase A  基线与基础设施：Task 1 -> Task 9 -> Task 10
Phase B  事件迁移与实时入口：Task 11 -> Task 13
Phase C  流计算闭环：Task 12
Phase D  可靠性与并发治理：Task 2 -> Task 3 -> Task 4 -> Task 5 -> Task 6 -> Task 7 -> Task 8
Phase E  全链路验收：Task 14
```

Task 2-8 是此前已确认的 Storage/Consumer/Factor 可靠性工作；其中所有旧事件名、旧 Envelope 示例和旧 Topic 引用均以本次 `packages/events` 设计为准替换。Task 12 完成最小 K 线闭环后，再扩展 Trade/Tick 输入和更多聚合算子。

## 实施顺序

### Task 1: 建立当前契约基线与工作树保护

**Files:**
- Read: `/Users/mooyang/Documents/go/src/github.com/mooyang-code/moox/go.work`
- Read: `/Users/mooyang/Documents/go/src/github.com/mooyang-code/moox/modules/eventbus/config/app.yaml`
- Read: `/Users/mooyang/Documents/go/src/github.com/mooyang-code/moox/modules/factor/config/app.yaml`
- Read: `/Users/mooyang/Documents/go/src/github.com/mooyang-code/moox/modules/archive/config/app.yaml`
- Record: `/Users/mooyang/Documents/Codex/2026-07-22/new-chat/outputs/moox-contract-baseline.txt`

- [ ] **Step 1: 记录基线 SHA 与工作树状态**

Run:

```bash
cd /Users/mooyang/Documents/go/src/github.com/mooyang-code/moox
git rev-parse HEAD
git status --short --branch
```

Expected: 记录当前 commit SHA；已有未提交修改保持原样，不纳入本次修复的回滚范围。

- [ ] **Step 2: 生成 active contract 搜索结果**

Run:

```bash
cd /Users/mooyang/Documents/go/src/github.com/mooyang-code/moox
rg -n 'rows_committed|fields_changed|application/protobuf|application/x-protobuf|storage_view|factor_calc|moox_archive_kline_v1' \
  modules packages scripts --glob '*.go' --glob '*.yaml' --glob '*.sh'
```

Expected: 将结果保存到基线文件，区分 active code/config/test 与历史设计文档；后续任务以该文件作为“无残留”的验证基准。

- [ ] **Step 3: 建立模块级测试基线**

Run:

```bash
cd /Users/mooyang/Documents/go/src/github.com/mooyang-code/moox/modules/eventbus && go test -count=1 ./...
cd /Users/mooyang/Documents/go/src/github.com/mooyang-code/moox/packages/jetstream && go test -count=1 ./...
cd /Users/mooyang/Documents/go/src/github.com/mooyang-code/moox/modules/storage && go test -count=1 ./...
cd /Users/mooyang/Documents/go/src/github.com/mooyang-code/moox/modules/factor && go test -count=1 ./...
cd /Users/mooyang/Documents/go/src/github.com/mooyang-code/moox/modules/archive && go test -count=1 ./...
```

Expected: 每个模块单独记录 PASS/FAIL；不把根目录 `go test ./...` 当作多模块完整验证。

- [ ] **Step 4: 保存基线，不修改业务代码**

将 SHA、工作树状态、active contract 搜索结果和各模块测试结果保存到
`/Users/mooyang/Documents/Codex/2026-07-22/new-chat/outputs/moox-contract-baseline.txt`。
该基线文件属于执行记录，不进入 MooX 业务仓库；执行期间不修改或覆盖已有未提交文件。

### Task 2: 将 Storage 通用变更事件替换为明确事实事件

**Files:**
- Modify: `/Users/mooyang/Documents/go/src/github.com/mooyang-code/moox/modules/storage/internal/service/datanode/pebble/event.go:36-46`
- Modify: `/Users/mooyang/Documents/go/src/github.com/mooyang-code/moox/modules/factor/config/app.yaml:20-22`
- Modify: `/Users/mooyang/Documents/go/src/github.com/mooyang-code/moox/modules/factor/internal/bootstrap/config.go:135-137,198-205`
- Modify: `/Users/mooyang/Documents/go/src/github.com/mooyang-code/moox/modules/archive/config/app.yaml:10-19`
- Modify: `/Users/mooyang/Documents/go/src/github.com/mooyang-code/moox/modules/archive/internal/config/config.go:90-95`
- Modify: `/Users/mooyang/Documents/go/src/github.com/mooyang-code/moox/modules/admin/cmd/cli/eventbus_credentials.go:273`
- Test: `/Users/mooyang/Documents/go/src/github.com/mooyang-code/moox/modules/storage/internal/service/datanode/pebble/event_test.go`
- Test: `/Users/mooyang/Documents/go/src/github.com/mooyang-code/moox/modules/archive/internal/bootstrap/app_test.go`

- [ ] **Step 1: 写失败测试，锁定 `storage.rows.upserted` 契约**

测试必须断言 DataNode Outbox 生成的 `EventMessage` 同时满足：

```go
if got := msg.GetEventName(); got != "storage.rows.upserted" {
	 t.Fatalf("event_name=%q", got)
}
if got := msg.GetEventVersion(); got != 1 {
	 t.Fatalf("event_version=%d", got)
}
if got := msg.GetSpaceId(); got != spaceID {
	 t.Fatalf("space_id=%q", got)
}
if got := msg.GetSubjectId(); got != datasetID {
	 t.Fatalf("subject_id=%q", got)
}
```

- [ ] **Step 2: 运行失败测试**

```bash
cd /Users/mooyang/Documents/go/src/github.com/mooyang-code/moox
go test ./modules/storage/internal/service/datanode/pebble -run TestBuildRowsUpsertedEventContract -count=1
```

Expected: 在迁移完成前因旧 `DatasetFieldsChanged`/`fields_changed` 契约不满足 Registry 校验而失败。

- [ ] **Step 3: 修改 DataNode Outbox EventMessage**

将 [event.go](/Users/mooyang/Documents/go/src/github.com/mooyang-code/moox/modules/storage/internal/service/datanode/pebble/event.go) 改为使用 `packages/events.StorageRowsUpserted`，外层字段为 `event_name=storage.rows.upserted`、`event_version=1`、`space_id` 和 `subject_id=dataset_id`。Subject 由 Registry 生成：

```text
moox.storage.rows.upserted.v1.<space-token>.<dataset-token>
```

Content-Type、MessageType 等传输描述不再由 DataNode 业务代码手写。

- [ ] **Step 4: 修改 Factor/Archive/Admin active 配置**

删除 active 配置中的：

```text
moox.storage.rows.upserted.v1.>
```

统一替换为 Registry 生成的：

```text
moox.storage.rows.upserted.v1.>
```

同步修复 Archive bootstrap 测试，使测试绑定真实 EventBus topology，而不是自行创建旧主题。

- [ ] **Step 5: 运行契约测试和残留搜索**

```bash
cd /Users/mooyang/Documents/go/src/github.com/mooyang-code/moox
go test ./modules/storage/internal/service/datanode/pebble ./modules/archive/internal/bootstrap -count=1
rg -n 'rows_committed|fields_changed|DatasetFieldsChanged|application/protobuf' modules packages scripts --glob '*.go' --glob '*.yaml' --glob '*.sh'
```

Expected: active code/config/test 不再出现旧主题、旧事件名或旧 Content-Type；历史设计文档可以保留，但不得被运行时代码读取。

- [ ] **Step 6: Commit**

```bash
git add modules/storage modules/factor modules/archive modules/admin
git commit -m "fix: align storage event contracts"
```

### Task 3: 让 EventBus topology 成为唯一 Consumer 配置来源

**Files:**
- Modify: `/Users/mooyang/Documents/go/src/github.com/mooyang-code/moox/packages/jetstream/consumer.go`
- Modify: `/Users/mooyang/Documents/go/src/github.com/mooyang-code/moox/modules/factor/internal/trigger/nats.go`
- Modify: `/Users/mooyang/Documents/go/src/github.com/mooyang-code/moox/modules/archive/internal/bootstrap/app.go`
- Modify: `/Users/mooyang/Documents/go/src/github.com/mooyang-code/moox/modules/storage/internal/service/view/consume.go`
- Modify: `/Users/mooyang/Documents/go/src/github.com/mooyang-code/moox/modules/eventbus/config/app.yaml`
- Test: `/Users/mooyang/Documents/go/src/github.com/mooyang-code/moox/packages/jetstream/consumer_test.go`
- Test: `/Users/mooyang/Documents/go/src/github.com/mooyang-code/moox/modules/eventbus/test/eventbus_e2e_test.go`

- [ ] **Step 1: 增加按 Durable 绑定的显式 API**

在 `packages/jetstream` 增加只读绑定路径，语义为：

```go
type ConsumerBindRef struct {
	Stream      string
	Durable     string
	FetchMaxWait time.Duration
}

func (c *Client) BindManagedPullConsumer(ctx context.Context, ref ConsumerBindRef) (*PullConsumer, error)
```

该 API 必须：

1. 读取 JetStream `ConsumerInfo`；
2. 拒绝不存在的 Durable；
3. 使用服务端真实的 FilterSubject、AckWait、MaxDeliver、MaxAckPending、DeliverPolicy；
4. 只允许客户端覆盖 FetchMaxWait；
5. 不创建、不更新、不删除 Consumer。

- [ ] **Step 2: 为 Managed Bind 写失败测试**

覆盖以下情况：

```text
Durable 不存在 -> ErrConsumerNotFound
FilterSubject 与客户端旧配置不同 -> 仍以服务端配置为准
客户端无权限查询 ConsumerInfo -> 启动失败并暴露原因
```

- [ ] **Step 3: Factor/Archive/View 改为只声明 Stream + Durable**

业务模块不再重复声明 `Subject`、`AckWait`、`MaxAckPending` 和 `DeliverPolicy`；这些值统一来自 EventBus topology。

模块配置只保留：

```text
stream
durable
fetch_batch
fetch_max_wait
业务处理超时
```

- [ ] **Step 4: 增加跨模块 topology E2E**

测试流程：

```text
启动临时 NATS JetStream
加载 modules/eventbus/config/app.yaml
执行 Registry.Reconcile
绑定 storage_view、factor_calc、moox_archive_kline_v1
发布一个 storage.rows.upserted EventMessage
三个 Consumer 都收到各自消息
重复发布同一个 MessageID
验证消费者业务侧只产生一次有效处理
```

- [ ] **Step 5: Commit**

```bash
git add packages/jetstream modules/eventbus modules/factor modules/archive modules/storage
git commit -m "refactor: bind consumers from eventbus topology"
```

### Task 4: 统一 Consumer ACK、重试和错误可观测性

**Files:**
- Create: `/Users/mooyang/Documents/go/src/github.com/mooyang-code/moox/packages/jetstream/runner.go`
- Test: `/Users/mooyang/Documents/go/src/github.com/mooyang-code/moox/packages/jetstream/runner_test.go`
- Modify: `/Users/mooyang/Documents/go/src/github.com/mooyang-code/moox/modules/factor/internal/trigger/nats.go`
- Modify: `/Users/mooyang/Documents/go/src/github.com/mooyang-code/moox/modules/archive/internal/consumer/runner.go`
- Modify: `/Users/mooyang/Documents/go/src/github.com/mooyang-code/moox/modules/monitor/internal/metrics/consumer.go`
- Modify: `/Users/mooyang/Documents/go/src/github.com/mooyang-code/moox/modules/trade/internal/bootstrap/kernel_workers.go`

- [x] **Step 1: 定义最小 Handler Outcome**

```go
type HandlerDecision uint8

const (
	DecisionAck HandlerDecision = iota + 1
	DecisionRetry
	DecisionTerm
)

type HandlerResult struct {
	Decision HandlerDecision
	Delay    time.Duration
	Err      error
}

type DeliveryHandler interface {
	Handle(context.Context, *Delivery) HandlerResult
}
```

- [x] **Step 2: 先写 Runner 行为测试**

必须覆盖：

```text
成功 -> Ack
临时错误 -> InProgress 或 Nak
永久错误 -> Term
Ack 失败 -> 记录错误并返回
Term 失败 -> 记录错误并返回
ctx 取消 -> 不启动新的 Delivery
```

- [x] **Step 3: 迁移 Monitor 和 Factor**

禁止继续使用：

```go
_ = c.HandleDelivery(ctx, d)
_ = delivery.Ack(ctx)
_ = delivery.Term(ctx)
```

所有 ACK/NAK/TERM 错误必须进入统一 counter，并影响 Runner 返回值或模块健康状态。

- [x] **Step 4: 迁移 Archive 和 Trade**

保留它们现有的 Journal/Inbox/交易状态幂等逻辑；Runner 只统一 Transport 层动作，不把业务去重逻辑搬进共享包。

- [ ] **Step 5: Commit**

```bash
git add packages/jetstream modules/factor modules/archive modules/monitor modules/trade
git commit -m "refactor: standardize consumer delivery outcomes"
```

### Task 5: 实现 Storage View Subject 级并发

**Files:**
- Modify: `/Users/mooyang/Documents/go/src/github.com/mooyang-code/moox/modules/storage/internal/service/view/service.go`
- Modify: `/Users/mooyang/Documents/go/src/github.com/mooyang-code/moox/modules/storage/internal/service/view/consume.go`
- Modify: `/Users/mooyang/Documents/go/src/github.com/mooyang-code/moox/modules/eventbus/config/app.yaml:160-168`
- Modify: `/Users/mooyang/Documents/go/src/github.com/mooyang-code/moox/modules/storage/config/storage.yaml:17-26`
- Test: `/Users/mooyang/Documents/go/src/github.com/mooyang-code/moox/modules/storage/internal/service/view/consume_test.go`
- Test: `/Users/mooyang/Documents/go/src/github.com/mooyang-code/moox/modules/storage/test/storage_view_concurrency_e2e_test.go`

- [ ] **Step 1: 增加可配置消费参数**

```go
type EventConsumerOptions struct {
	FetchBatch     int
	MaxWorkers     int
	MaxAckPending  int
	Ordering       string // subject
}
```

默认值：

```text
FetchBatch=8
MaxWorkers=4
MaxAckPending=8
Ordering=subject
```

- [ ] **Step 2: 把单一 liveGate 改成实时共享锁 + Backfill 独占锁**

实时 Delivery 获取读锁；Backfill 获取写锁。写锁等待期间禁止新实时任务进入，避免 Backfill 启动后仍不断增加实时工作量。

- [ ] **Step 3: 实现 Subject Lane Dispatcher**

Dispatcher 的核心约束：

```text
laneKey = delivery.Subject
同一个 laneKey 只有一个 active handler
不同 laneKey 可以同时处理
单条 Delivery 只有在对应 Handler 完成后才能 ACK
```

禁止以 `MessageID`、NATS Stream Sequence 或随机 worker 作为排序键。

- [ ] **Step 4: 处理错误和重试**

同一 Subject Lane 内：

1. 当前 Delivery 临时失败时保持 `InProgress`；
2. 不允许后续同 Subject Delivery 越过当前失败事件；
3. 永久错误才 `Term` 或进入 DLQ；
4. 不同 Subject Lane 的失败不能阻塞其他 Dataset。

- [ ] **Step 5: 增加并发正确性测试**

测试必须验证：

```text
Dataset A handler 延迟 500ms，Dataset B 仍能完成
同一 Dataset 的事件按投递顺序应用
同一 Dataset 失败时，后续事件不会越过它
多个 Dataset 并行时 View runtime/schema 不发生竞态
Backfill 等待所有实时任务完成后独占执行
Backfill 完成后实时事件继续消费
```

- [ ] **Step 6: 运行 Storage View E2E**

```bash
cd /Users/mooyang/Documents/go/src/github.com/mooyang-code/moox
go test ./modules/storage/internal/service/view/... -count=1 -race
go test ./modules/storage/test -run TestStorageViewSubjectConcurrency -count=1
```

Expected: 同一 Subject 保序、不同 Subject 并发；`-race` 无数据竞争。

- [ ] **Step 7: Commit**

```bash
git add modules/storage modules/eventbus/config/app.yaml
git commit -m "feat: parallelize storage view by dataset subject"
```

### Task 6: 补齐 Storage View 和 Outbox 运行指标

**Files:**
- Modify: `/Users/mooyang/Documents/go/src/github.com/mooyang-code/moox/modules/storage/internal/observability/view_metrics.go`
- Modify: `/Users/mooyang/Documents/go/src/github.com/mooyang-code/moox/modules/storage/internal/service/view/consume.go`
- Modify: `/Users/mooyang/Documents/go/src/github.com/mooyang-code/moox/modules/storage/internal/service/datanode/outbox_relay.go`
- Modify: `/Users/mooyang/Documents/go/src/github.com/mooyang-code/moox/modules/eventbus/internal/health/server.go`
- Test: `/Users/mooyang/Documents/go/src/github.com/mooyang-code/moox/modules/storage/internal/observability/view_metrics_test.go`

- [ ] **Step 1: 增加低基数指标**

至少增加：

```text
moox_storage_view_consumer_lag_messages
moox_storage_view_oldest_pending_event_age_seconds
moox_storage_view_delivery_duration_seconds
moox_storage_view_ack_errors_total
moox_storage_view_in_progress_errors_total
moox_storage_view_lane_active
moox_storage_outbox_pending_entries
moox_storage_outbox_oldest_age_seconds
moox_storage_outbox_publish_errors_total
moox_storage_outbox_duplicate_publish_total
```

不要把完整 Subject、Symbol 或 MessageID 作为 Prometheus label；Subject 只允许使用受控的 Dataset 维度或聚合值。

- [ ] **Step 2: 增加健康状态规则**

健康检查必须区分：

```text
process alive
NATS connected
consumer bound
outbox draining
oldest pending within threshold
```

NATS 连接正常不应掩盖 Storage View backlog 或 Outbox 持续失败。

- [ ] **Step 3: Commit**

```bash
git add modules/storage modules/eventbus/internal/health
git commit -m "feat: expose streaming lag and delivery health"
```

### Task 7: 明确 Factor 实时、回放和 Python Runtime 契约

**Files:**
- Modify: `/Users/mooyang/Documents/go/src/github.com/mooyang-code/moox/modules/factor/config/app.yaml`
- Modify: `/Users/mooyang/Documents/go/src/github.com/mooyang-code/moox/modules/factor/internal/bootstrap/config.go`
- Modify: `/Users/mooyang/Documents/go/src/github.com/mooyang-code/moox/modules/factor/internal/trigger/nats.go`
- Modify: `/Users/mooyang/Documents/go/src/github.com/mooyang-code/moox/modules/factor/internal/trigger/event_batcher.go`
- Create: `/Users/mooyang/Documents/go/src/github.com/mooyang-code/moox/modules/factor/internal/trigger/replay.go`
- Test: `/Users/mooyang/Documents/go/src/github.com/mooyang-code/moox/modules/factor/internal/trigger/event_batcher_test.go`
- Test: `/Users/mooyang/Documents/go/src/github.com/mooyang-code/moox/modules/factor/test/factor_replay_e2e_test.go`

- [ ] **Step 1: 保持 Live Consumer 使用 `DeliverNew`**

实时消费不改为 `DeliverAll`，避免服务首次启动时重新执行全部历史事件。

- [x] **Step 2: 增加明确的 Replay/Rebuild 入口**

Replay 必须接收：

```text
space_id
dataset_id
start_time
end_time
factor_version
target_run_id
```

 Replay 使用独立的任务 ID 和幂等键，不复用实时 batch 的 processing-time deadline；`go run ./cmd/cli replay` 提供生产触发面，读取 JSONL ReplayEvent 并执行生成的任务。

- [ ] **Step 3: 区分 processing-time window 和 event-time bar**

`EventBatcher` 保留当前 2 秒收敛窗口，但任务必须同时记录：

```text
first_received_at
last_received_at
min_data_time
max_data_time
```

迟到数据必须有明确策略：重新计算、忽略或生成修正任务，不能由 `time.Now()` 隐式决定。

- [ ] **Step 4: 增加 Python Runtime readiness**

启动时检查：

```text
python_bin 可执行
factor 运行目录可读
Python worker 可启动并返回版本信息
```

检查失败时 Factor readiness 为 false，并给出可定位错误。

- [ ] **Step 5: Commit**

```bash
git add modules/factor
git commit -m "feat: separate factor live and replay execution"
```

### Task 8: PublishBatch 注释、幂等语义和最终验证

**Files:**
- Modify: `/Users/mooyang/Documents/go/src/github.com/mooyang-code/moox/packages/jetstream/publisher.go:83`
- Modify: `/Users/mooyang/Documents/go/src/github.com/mooyang-code/moox/packages/jetstream/client_test.go:234-270`
- Modify: `/Users/mooyang/Documents/go/src/github.com/mooyang-code/moox/docs/架构总览.md`
- Create: `/Users/mooyang/Documents/go/src/github.com/mooyang-code/moox/modules/eventbus/test/storage_consumers_e2e_test.go`

- [ ] **Step 1: 修正 PublishBatch 注释和测试名称**

测试名称从 `TestPublishBatchPreservesOrderAndPartialFailures` 调整为能表达真实语义的名称，例如：

```text
TestPublishBatchPreservesResultOrderAndPartialFailures
```

测试必须明确断言结果 slice 的 input index 对齐，不断言 PubAck 的 Stream Sequence 顺序。

- [ ] **Step 2: 增加 at-least-once 恢复测试**

测试流程：

```text
发布 MessageID=m1
模拟 publish 成功但 outbox delete 失败
重启 relay
再次发布 m1
验证 View/Archive/Trade 业务侧只产生一次最终效果
```

- [ ] **Step 3: 增加 EventBus topology 全链路测试**

验证：

```text
DataNode storage.rows.upserted 消息
storage_view consumer
factor_calc consumer
archive consumer
MessageID 去重
consumer 重启恢复
```

- [ ] **Step 4: 执行最终模块验证**

```bash
cd /Users/mooyang/Documents/go/src/github.com/mooyang-code/moox/modules/eventbus && go test -count=1 ./...
cd /Users/mooyang/Documents/go/src/github.com/mooyang-code/moox/packages/jetstream && go test -count=1 ./...
cd /Users/mooyang/Documents/go/src/github.com/mooyang-code/moox/modules/storage && go test -count=1 ./...
cd /Users/mooyang/Documents/go/src/github.com/mooyang-code/moox/modules/factor && go test -count=1 ./...
cd /Users/mooyang/Documents/go/src/github.com/mooyang-code/moox/modules/archive && go test -count=1 ./...
cd /Users/mooyang/Documents/go/src/github.com/mooyang-code/moox/modules/storage && go test -race -count=1 ./internal/service/view/...
```

- [ ] **Step 5: 执行 active contract 检查**

```bash
rg -n 'rows_committed|fields_changed|DatasetFieldsChanged|application/protobuf' modules packages scripts \
  --glob '*.go' --glob '*.yaml' --glob '*.sh'
```

Expected: 无 active runtime/config/test 残留；历史计划文档不参与运行时构建。

- [ ] **Step 6: 记录最终 SHA、测试范围和未完成项**

```bash
git rev-parse HEAD
git status --short --branch
```

最终报告必须区分：

```text
模块级测试结果
跨模块 E2E 结果
race 测试结果
工作树是否干净
提交 SHA
远程 CI 是否验证
```

### Task 9: 落地 `packages/events` 与 EventMessage

**Files:**
- Create: `/Users/mooyang/Documents/go/src/github.com/mooyang-code/moox/packages/events/proto/event_message.proto`
- Create: `/Users/mooyang/Documents/go/src/github.com/mooyang-code/moox/packages/events/registry/events.yaml`
- Create: `/Users/mooyang/Documents/go/src/github.com/mooyang-code/moox/packages/events/spec.go`
- Create: `/Users/mooyang/Documents/go/src/github.com/mooyang-code/moox/packages/events/publisher.go`
- Create: `/Users/mooyang/Documents/go/src/github.com/mooyang-code/moox/packages/events/consumer.go`
- Create: `/Users/mooyang/Documents/go/src/github.com/mooyang-code/moox/packages/events/subject.go`
- Create: `/Users/mooyang/Documents/go/src/github.com/mooyang-code/moox/packages/events/validation.go`
- Remove/Replace: `/Users/mooyang/Documents/go/src/github.com/mooyang-code/moox/packages/messagepb/moox_message.proto`
- Test: `/Users/mooyang/Documents/go/src/github.com/mooyang-code/moox/packages/events/*_test.go`

- [ ] **Step 1: 定义精简但显式的 EventMessage**

仅保留：

```text
event_id, event_name, event_version, space_id, subject_id, occurred_at, payload
```

`event_name`、`event_version`、`space_id`、`subject_id` 必须由消费者直接从外层字段读取；不能要求消费者解析 NATS Subject。`subject_id` 对无主体事件可为空，但是否必填由注册表控制。

- [ ] **Step 2: 实现强类型事件规格**

事件规格至少包含：

```go
type EventSpec struct {
    Name         string
    Version      uint32
    PayloadType  protoreflect.FullName
    Subject      SubjectTemplate
    Stream       string
    PartitionKey PartitionKey
    Owner        string
}
```

提供 `MarketTradeReceived`、`MarketKlineClosed`、`StorageRowsUpserted` 等强类型规格；生产者不再传入任意事件名或任意 Topic。

- [ ] **Step 3: 实现 Registry 加载和启动校验**

启动/测试时拒绝以下情况：事件重复注册、Payload 类型不存在、Subject 模板缺少必要上下文、事件版本与 Subject 版本不一致、Stream 未定义、分区键不可推导。

- [ ] **Step 4: 实现类型化 Publish/Consume API**

`packages/events` 调用 `packages/jetstream` 完成发布和消费，但业务模块只依赖事件 API。Publish 时自动完成：Payload 序列化、EventMessage 组装、Registry 校验、Subject 生成和 `event_id` 检查。

- [ ] **Step 5: 增加治理检查**

CI 必须检查：

```text
所有事件均存在 Registry
所有 Payload 均可反射解析
所有生产/消费模块使用注册事件
业务代码不存在裸 NATS Subject 和裸 jetstream.Publish
每个事件恰好映射一个 Stream
Subject 与 EventMessage 元数据一致
```

- [ ] **Step 6: Commit**

```bash
git add packages/events packages/messagepb
git commit -m "feat: add governed event message contracts"
```

### Task 10: 重构 EventBus topology 与独立部署边界

**Files:**
- Modify: `/Users/mooyang/Documents/go/src/github.com/mooyang-code/moox/modules/eventbus/cmd/server/main.go`
- Modify: `/Users/mooyang/Documents/go/src/github.com/mooyang-code/moox/modules/eventbus/internal/broker/server.go`
- Modify: `/Users/mooyang/Documents/go/src/github.com/mooyang-code/moox/modules/eventbus/config/app.yaml`
- Modify: `/Users/mooyang/Documents/go/src/github.com/mooyang-code/moox/modules/eventbus/internal/health/server.go`
- Test: `/Users/mooyang/Documents/go/src/github.com/mooyang-code/moox/modules/eventbus/test/*_test.go`

- [ ] **Step 1: 明确 EventBus 只做基础设施编排**

服务职责限定为：连接/管理 NATS、创建或校验 JetStream Stream、创建或校验 Durable Consumer、加载 Registry、校验拓扑、管理 ACL/TLS 和暴露健康状态。它不实现 `KlineAggregator`、Factor、Storage View 或业务重试 Handler。

- [ ] **Step 2: 把 retention 从事件契约拆到 Stream topology**

事件 Registry 不允许出现 `retention`。`modules/eventbus/config/app.yaml` 单独定义 `streams`，并记录 `retention`、`max_bytes` 等存储策略；Consumer 配置单独记录 `AckWait`、`MaxDeliver`、`MaxAckPending`、FilterSubject 和 DeliverPolicy。

- [ ] **Step 3: 以 Registry 生成/校验 Topic Family、Stream 和 ACL**

EventBus 启动时拒绝未知事件 Subject；发现 Stream/Consumer 与 Registry 不一致时失败而不是静默创建一套旁路配置。业务服务只通过 `stream + durable` 绑定托管 Consumer。

- [ ] **Step 4: 增加独立部署验收**

使用临时 NATS JetStream 启动 EventBus，加载 topology，验证 Stream、Durable、ACL/TLS、健康检查和未知事件拒绝。业务模块随后只连接 EventBus 暴露的 NATS 地址，不复制 topology 创建逻辑。

- [ ] **Step 5: Commit**

```bash
git add modules/eventbus
git commit -m "refactor: make eventbus topology authoritative"
```

### Task 11: 全量迁移事件命名和消息生产/消费

**Files:**
- Modify: `/Users/mooyang/Documents/go/src/github.com/mooyang-code/moox/modules/collector`
- Modify: `/Users/mooyang/Documents/go/src/github.com/mooyang-code/moox/modules/storage`
- Modify: `/Users/mooyang/Documents/go/src/github.com/mooyang-code/moox/modules/factor`
- Modify: `/Users/mooyang/Documents/go/src/github.com/mooyang-code/moox/modules/archive`
- Modify: `/Users/mooyang/Documents/go/src/github.com/mooyang-code/moox/modules/monitor`
- Modify: `/Users/mooyang/Documents/go/src/github.com/mooyang-code/moox/modules/trade`
- Test: `/Users/mooyang/Documents/go/src/github.com/mooyang-code/moox/modules/*/test`

- [ ] **Step 1: 建立初始事件目录**

首批事件至少包括：

```text
market.trade.received
market.quote.updated
market.kline.updated
market.kline.closed
storage.rows.upserted
storage.dataset.created
storage.dataset.subject_bound
factor.calculation.requested
factor.calculation.completed
strategy.signal.generated
trade.order.intent_created
trade.order.state_changed
trade.fill.received
dlq.event.rejected
```

- [ ] **Step 2: 替换旧 Storage 通用事件**

将 `DatasetFieldsChanged` 改为明确的 `storage.rows.upserted`，Payload 改为 `RowsUpserted`；不添加旧主题兼容别名，不让新的业务代码继续依赖 `fields_changed`、`rows_committed` 等旧命名。

- [ ] **Step 3: 清理所有直接发布路径**

Collector、Storage、Factor、Archive、Monitor、Trade 统一使用 `packages/events`。保留 `packages/jetstream` 仅作为底层传输包；任何业务模块出现裸 Topic、裸 Envelope 或自己填写 Content-Type 都应由编译检查/CI 检查拦截。

- [ ] **Step 4: 明确事件事实与命令边界**

已发生的行情、K 线、Storage 行更新、成交和订单状态变化使用 Event；要求执行计算、下单或回补时另建 Command，不把“请求”伪装成 Event。

- [ ] **Step 5: 增加事件链路契约测试**

每个首批事件至少测试：构造、Registry 校验、Subject 生成、外层字段读取、Payload 解码、错误事件拒绝和重复 `event_id` 幂等。

- [ ] **Step 6: Commit**

```bash
git add modules packages
git commit -m "refactor: migrate modules to governed events"
```

### Task 12: 实现 `modules/streamcalc` 实时流计算服务

**Files:**
- Create: `/Users/mooyang/Documents/go/src/github.com/mooyang-code/moox/modules/streamcalc/cmd/server/main.go`
- Create: `/Users/mooyang/Documents/go/src/github.com/mooyang-code/moox/modules/streamcalc/internal/bootstrap`
- Create: `/Users/mooyang/Documents/go/src/github.com/mooyang-code/moox/modules/streamcalc/internal/consumer`
- Create: `/Users/mooyang/Documents/go/src/github.com/mooyang-code/moox/modules/streamcalc/internal/aggregate`
- Create: `/Users/mooyang/Documents/go/src/github.com/mooyang-code/moox/modules/streamcalc/internal/state`
- Create: `/Users/mooyang/Documents/go/src/github.com/mooyang-code/moox/modules/streamcalc/internal/storage`
- Create: `/Users/mooyang/Documents/go/src/github.com/mooyang-code/moox/modules/streamcalc/config/app.yaml`
- Test: `/Users/mooyang/Documents/go/src/github.com/mooyang-code/moox/modules/streamcalc/internal/*/*_test.go`
- Test: `/Users/mooyang/Documents/go/src/github.com/mooyang-code/moox/modules/streamcalc/test/streamcalc_e2e_test.go`

- [ ] **Step 1: 定义服务边界**

`streamcalc` 是独立部署的 Go 服务，消费 `market.trade.received`、`market.quote.updated` 或 `market.kline.closed`，维护聚合状态并调用 Storage 写入接口；它不拥有 Collector 的行情连接，不承担 Storage View 投影，也不调用 Python Runtime。

- [ ] **Step 2: 选择事件输入和聚合粒度**

第一阶段建议从 `market.kline.closed` 做稳定的 K 线聚合闭环，再扩展到 Trade/Tick 聚合。配置至少包含：

```text
input_event
output_event
space_id
subject_id / symbol
source_frequency
target_frequency
allowed_lateness
window_close_policy
```

例如 `1m -> 5m`：按照 `occurred_at` 或 Payload 中的 event time 计算窗口，不使用收到时间替代市场事实时间。

- [ ] **Step 3: 设计聚合状态**

状态键至少为：

```text
space_id + subject_id + target_frequency + window_start
```

状态保存 `open/high/low/close/volume`、已接收输入的幂等集合/水位、最新 `revision` 和窗口状态。状态存储可以先使用本地 Pebble；必须预留 checkpoint/rebuild 接口，使服务重启可从 JetStream 重放或从 Storage 回补重建。

- [ ] **Step 4: 设计迟到、重复和修正**

同一输入 `event_id` 重复到达只应用一次。事件时间超过窗口关闭点但在 `allowed_lateness` 内，生成同一窗口的新 `revision`；超过允许迟到窗口，进入明确的 late-data/DLQ 指标和补算队列，不静默覆盖已发布 K 线。

- [ ] **Step 5: 原子写入 Storage 并发布下游事实**

聚合结果提交必须遵循：状态更新、Storage `WriteFields`、下游 Outbox 的一致性边界。不能在 Storage 成功前 ACK 输入事件，也不能先发布 `market.kline.closed` 再写 Storage 导致下游看到不存在的数据。推荐使用 streamcalc 本地状态 + Storage 幂等写入 + Outbox/Inbox 记录；具体跨存储事务在实现任务中锁定。

- [ ] **Step 6: 增加可观测性**

至少暴露：输入 lag、event-time lag、窗口状态数、关闭窗口数、修正次数、迟到事件数、重复事件数、Storage 写入失败数、当前 checkpoint 和 DLQ 数量。指标标签只能使用受控的 space/frequency，不使用完整 Symbol 或 event_id 作为高基数标签。

- [ ] **Step 7: Commit**

```bash
git add modules/streamcalc
git commit -m "feat: add nats driven stream calculation service"
```

### Task 13: 改造 Collector 实时入口，禁止双写

**Files:**
- Modify: `/Users/mooyang/Documents/go/src/github.com/mooyang-code/moox/modules/collector/internal/sources/interface.go`
- Modify: `/Users/mooyang/Documents/go/src/github.com/mooyang-code/moox/modules/collector/internal/sources/binance/kline.go`
- Modify: `/Users/mooyang/Documents/go/src/github.com/mooyang-code/moox/modules/collector/internal/sources/binance/storage_rpc.go`
- Modify: `/Users/mooyang/Documents/go/src/github.com/mooyang-code/moox/modules/collector/internal/bootstrap`
- Test: `/Users/mooyang/Documents/go/src/github.com/mooyang-code/moox/modules/collector/test/collector_event_e2e_test.go`

- [ ] **Step 1: 区分 Live 与 Historical/Backfill Source**

Collector Source 接口不能只表达“一次性返回 K 线”。实时 Source 必须能够产出事件流；历史 Source 继续支持批量读取，但通过明确的 backfill job 入口执行。

- [ ] **Step 2: Live Collector 只发布市场事件**

Binance 实时数据先构造 `market.trade.received`、`market.quote.updated` 或 `market.kline.updated/closed`，通过 `packages/events` 写入 NATS。实时 Collector 不再直接调用 Storage RPC。

- [ ] **Step 3: 明确 closed Kline 和 provisional Kline 语义**

未闭合 K 线使用 `market.kline.updated`；交易所确认窗口结束后使用 `market.kline.closed`。两者不能都伪装成同一个 Storage 更新事件，否则 streamcalc 无法判断是累积更新还是最终关闭。

- [ ] **Step 4: 验证没有实时双写**

E2E 注入一条行情事件，验证只有 streamcalc 写入 Storage，Collector 不产生第二次相同事实写入；重复 delivery 仍只产生一个最终 Storage 效果。

- [ ] **Step 5: Commit**

```bash
git add modules/collector
git commit -m "refactor: route live collector data through events"
```

### Task 14: 端到端验证事件链和恢复语义

**Files:**
- Create: `/Users/mooyang/Documents/go/src/github.com/mooyang-code/moox/modules/streamcalc/test/streaming_pipeline_e2e_test.go`
- Modify: `/Users/mooyang/Documents/go/src/github.com/mooyang-code/moox/modules/eventbus/test/eventbus_e2e_test.go`
- Modify: `/Users/mooyang/Documents/go/src/github.com/mooyang-code/moox/modules/storage/test/storage_view_concurrency_e2e_test.go`
- Modify: `/Users/mooyang/Documents/go/src/github.com/mooyang-code/moox/docs/架构总览.md`

- [ ] **Step 1: 验证事件拓扑**

```text
EventBus 启动并加载 Registry
Collector 发布 market.trade.received
streamcalc 收到并聚合
Storage 写入并发布 storage.rows.upserted
Factor/Strategy/View/Archive 各自收到标准事件
```

- [ ] **Step 2: 验证消息语义**

消费者直接读取 `event_name/version/space_id/subject_id`；删除或改变 NATS Subject 中的冗余上下文后，事件外层仍可正确处理。未知事件名、版本、Payload 或 Subject 映射必须被拒绝并进入 DLQ/健康告警。

- [ ] **Step 3: 验证重启和重复**

在 streamcalc、Storage View、Factor、Archive 处理过程中重启服务，验证 JetStream redelivery、Inbox 幂等、状态恢复、Outbox 重放和最终 ACK 行为；不把“无重复投递”误当作保证。

- [ ] **Step 4: 验证并发和顺序**

验证不同 Dataset 的 Storage View Lane 可以并发，同一 Subject 保序；streamcalc 同一聚合键的事件保序或按 revision 合并，其他 subject 不被单个热点键拖住。

- [ ] **Step 5: 运行最终命令并记录证据**

```bash
cd /Users/mooyang/Documents/go/src/github.com/mooyang-code/moox/modules/eventbus && go test -count=1 ./...
cd /Users/mooyang/Documents/go/src/github.com/mooyang-code/moox/packages/events && go test -count=1 ./...
cd /Users/mooyang/Documents/go/src/github.com/mooyang-code/moox/modules/streamcalc && go test -race -count=1 ./...
cd /Users/mooyang/Documents/go/src/github.com/mooyang-code/moox/modules/storage && go test -race -count=1 ./internal/service/view/...
```

同时运行 Registry lint、裸 Subject 搜索、模块 E2E，并记录测试结果、SHA、工作树状态和未完成项。

## 验收标准

### 必须满足

- `packages/events` 是所有业务事件的唯一 Publish/Consume 入口，Registry 是事件名称、版本、Payload、Subject、Stream 和顺序键的唯一事实来源。
- `EventMessage` 外层显式包含 `event_id`、`event_name`、`event_version`、`space_id`、`subject_id`、`occurred_at` 和 `payload`；消费者不需要解析 NATS Subject 获取这些信息。
- 核心 EventMessage 不包含 `protocol_version`、`topic`、`content_type`、`payload_type`、`published_at`、`producer`、`causation_id`、`correlation_id` 或自由扩展 `attributes`。
- `retention` 只存在于 EventBus Stream topology，不存在于 Event Registry 或 Payload；EventBus 服务可以独立启动、校验和暴露拓扑健康状态。
- `modules/eventbus` 不承载业务 Handler；`modules/streamcalc` 是独立 Go 服务，消费 NATS 事件并负责 K 线聚合。
- Collector 实时路径只发布市场事件，不与 streamcalc 对同一事实双写 Storage。
- Storage 事实事件统一使用 `storage.rows.upserted`；active runtime/config 和新契约测试不再依赖 `DatasetFieldsChanged`、`fields_changed` 或 `rows_committed`，旧通用 helper/legacy_storage 测试只作为非验收残留单独标记。
- streamcalc 能按 event-time 聚合目标 K 线，处理重复、迟到、窗口关闭和 revision，并在 Storage 写入完成后才 ACK 输入事件。
- Factor、Archive、Storage View 都能绑定 EventBus 当前声明的 Consumer。
- DataNode 发布的事件能被 Factor、Archive、Storage View 正确解码。
- 同一 Dataset Subject 内保持处理顺序。
- 不同 Dataset Subject 可以并行处理。
- 单个 Dataset 处理失败不会阻塞其他 Dataset。
- Backfill 与实时消费不会同时修改同一 View 边界。
- ACK、NAK、TERM、InProgress 失败均可观测。
- 重复发布不会产生重复业务效果。
- Factor readiness 能反映 Python Runtime 是否可用。

### 暂不作为验收条件

- 同一 Dataset 内不同 Symbol 的并发处理。
- exactly-once 发布。
- NATS 集群 HA。
- 删除 Python Runtime。
- 更换 Streaming Engine。

## 2026-07-23 实施状态

本轮已落地的执行项：

- [x] `packages/events` 提供精简的 `EventMessage`、Protobuf Payload、事件注册表、Subject 模板以及类型化 Publish/Consume API。
- [x] EventBus topology 增加 `market.kline.closed`、`storage.rows.upserted` 和 streamcalc Durable，并在启动校验中检查注册事件与 Topic Family/Stream 的映射。
- [x] Collector 实时 K 线只发布 `market.kline.closed`；历史采集仍走显式的 Storage 路径，避免实时双写。
- [x] streamcalc 实现 1m -> 目标周期的 event-time OHLCV 聚合、重复/迟到处理、checkpoint，以及 NATS JetStream E2E。
- [x] Storage DataNode Outbox 改为发布 `storage.rows.upserted`，Storage View、Factor、Archive 增加标准 EventMessage 解码入口。
- [x] Storage View 使用 Subject Lane、可配置 Fetch/Worker/AckPending，并让 ApplyViewIndex 与实时/回补 gate 共用协调机制。
- [x] 补齐 Event ID 与 NATS `Nats-Msg-Id` 校验、坏消息 TERM、streamcalc 写入失败后的 pending output 重试与 checkpoint 保存。
- [x] 保留 `PublishBatch` 输入结果顺序，但注释明确不承诺 JetStream 发布顺序。

仍需在最终验收阶段完成：

- [x] 已完成核心模块的 race/contract/E2E 回归：`packages/events`、`modules/streamcalc/test`、`modules/archive/test`、Storage DataNode/View；EventBus 的 `legacy_storage` 测试按新契约另行迁移。
- [x] Active runtime 已切换到标准 EventMessage；剩余旧通用消息辅助函数仅供既有单元测试/内部 Decoder API 使用。
- [ ] 为 watermark/timer 驱动的窗口关闭补充明确的生产策略；当前 V1 以 closed Kline 输入触发窗口关闭。
- [ ] 完成独立 Agent review 后的回归检查、提交、push 和远端 SHA 校验。
  当前工作树已完成本轮实现与本地验证，但尚未提交、push 或校验远端 SHA。

## 2026-07-23 CR 修复补充

针对当前分支的 CR 复核结果，已补充以下修复和验证：

- [x] Trade、CloudNode、Monitor HostMetrics 的 ACK/NAK/TERM 错误不再静默丢弃，错误带消息身份写入日志。
- [x] Storage View metrics 增加 `consumer_bound`，Storage View/DataNode readiness 分别反映 Consumer 绑定和 Outbox 最老 Pending 年龄阈值。
- [x] Factor Batcher 记录 `first_received_at`、`last_received_at`、`min_data_time`、`max_data_time`，明确迟到策略为 `recompute`。
- [x] Factor 增加六参数 ReplayRange 契约和 `go run ./cmd/cli replay` 生产触发入口，使用独立 `trigger_type`、`factor_version`、`target_run_id` 和显式时间边界；`factor_version` 必须解析到 `.versions/factor/<name>/<version>/module.py`，Replay task 由 SQLite ledger 持久化去重；Python worker 启动时完成路径检查、hello warmup 和版本信息暴露。
- [x] Replay 按 `subject_id/freq/data_time` 生成独立历史任务，保留 binding 的自定义 `target_dataset`；running ledger 具备 15 分钟租约回收，避免进程崩溃后永久阻塞。
- [x] Trade、Monitor HostMetrics 已迁移到共享 JetStream Runner；CloudNode 保留其 Active KV Poll 流程，但 Term/NAK 失败会向 Poll 层返回。
- [x] 增加 EventBus Registry reconcile + 三 Durable topology/MessageID 去重 E2E、Storage View 独立 Dataset Lane E2E，以及 Outbox publish 成功但 delete 失败后关闭并重新打开 Pebble Store 的 Relay 恢复测试。
- [x] Outbox 后台 relay 增加可注入错误上报器并默认记录 flush 错误；Storage View 在非 timeout 的 Fetch 错误时撤销 `consumer_bound`，恢复 Fetch 后重新置位。
- [x] 更新 `docs/架构总览.md`、`docs/存储层架构.md`、`docs/协议设计.md`，并保存外部执行基线 `outputs/moox-contract-baseline.txt`。

本轮验证仍区分普通 Go 模块和 CGO-only E2E；默认 shell 的 `CGO_ENABLED=0`，但本轮已额外用 `CGO_ENABLED=1` 执行 Storage View DuckDB 独立 Dataset Lane E2E 并通过。后续正式验收仍需在目标构建环境重复执行并记录环境信息。

说明：本补充区的 `[x]` 表示实现或局部验证已完成；正文中各阶段的 Commit、push、远端 SHA 和完整跨模块 Replay E2E 仍须按最终验收项单独完成，不能据此宣称已合入远端。

## 回滚策略

1. 新项目不做旧事件兼容回滚；契约切换前先在临时 NATS 环境完成 Registry、Stream、Durable 和全链路 E2E 验证。
2. 部署顺序为 EventBus topology -> `packages/events` 依赖的业务服务 -> Collector/streamcalc -> Factor/Archive/View 等下游消费者。
3. Subject Lane 并发通过配置开关控制；出现数据一致性问题时恢复 `MaxWorkers=1`、`FetchBatch=1`、`MaxAckPending=1`。
4. 回滚期间不得删除 Durable Consumer 或清理 JetStream 数据；保留 Outbox 和事件，以便按新版本从起点重放。
5. 发生数据不一致时使用 Replay/Rebuild 重新物化 View 或 streamcalc 状态，不通过跳过消息解决 backlog；涉及 K 线修正时使用 `revision` 和补算任务。

## 推荐提交拆分

```text
fix: align storage event contracts
feat: add governed event message contracts
refactor: make eventbus topology authoritative
refactor: migrate modules to governed events
feat: add nats driven stream calculation service
refactor: route live collector data through events
refactor: bind consumers from eventbus topology
refactor: standardize consumer delivery outcomes
feat: parallelize storage view by dataset subject
feat: expose streaming lag and delivery health
feat: separate factor live and replay execution
test: verify streaming recovery and idempotency
```
actor 是系统唯一允许依赖 Python Runtime 的生产模块；Go/NATS/Storage/Trade/Strategy 仍保持 Go 二进制部署。
3. Storage View 第一阶段使用 `Subject` 级别有序 Lane：不同 Dataset 并行，同一 Dataset 内保持消息顺序。
4. Storage View 第一阶段目标参数为 `MaxAckPending=8`、`FetchBatch=8`、`Workers=4`，参数必须可配置并通过压测调整。
5. 新项目不做历史兼容：直接用新的 `packages/events` 和 `EventMessage` 替换旧的通用 `MooxMessage`、`DatasetFieldsChanged` 语义，不保留旧主题别名。
6. 事件统一放在 `packages/events` 管理；`event_name`、`version`、`space`、`subject` 必须作为外层 `EventMessage` 字段显式提供，消费者不得依赖解析 NATS Subject 获取业务元数据。
7. `EventMessage` 只保留事件事实所需的核心字段；`protocol_version`、`topic`、`content_type`、`payload_type`、`published_at`、`producer`、`causation_id`、`correlation_id`、`attributes`、`partition_key` 等不进入核心消息体。
8. `retention` 是 EventBus JetStream Stream 的运行策略，表示消息可供重放/恢复的保留窗口，不是事件 Payload 或事件契约字段；ACK/NAK、`AckWait`、`MaxDeliver`、`MaxAckPending` 另行管理。
9. `modules/eventbus` 是独立部署服务，负责 NATS/JetStream、Stream、Durable Consumer、事件注册表校验、ACL/TLS 和拓扑健康检查，不承载 K 线聚合、Factor 或其他业务 Handler。

### 明确不做

- 不替换 NATS JetStream。
- 不把 Storage View 直接改成 `MaxAckPending=1000`。
- 不在当前阶段按 symbol/RowKey 拆分 Storage 事件。
- 不把 Factor 的实时 Durable Consumer 直接改成 `DeliverAll`。
- 不承诺 exactly-once；统一目标是 at-least-once transport + business idempotency。
- 不再使用一个泛化的 `DatasetFieldsChanged` 覆盖所有事件；每个业务事实必须有稳定的 `event_name`、版本和 Payload 类型。
- 不允许业务模块直接调用裸 `jetstream.Publish`、手写 NATS Subject 或让消费者通过 Subject 反推业务字段。
- 不把 `retention` 写入事件契约 YAML；它只出现在 EventBus Stream topology 配置中。
- 不在实时链路中让 Collector 同时直接写 Storage、又发布同一事实给 streamcalc，避免双写和重复计算。
- 不使用 `git reset`、`git checkout` 等方式覆盖已有未提交修改。

## 2026-07-23 事件模块与 streamcalc 设计落版

本节是对前述讨论的最终收敛，优先级高于本文此前针对旧 `MooxMessage`/`DatasetFieldsChanged` 的示例。由于 MooX 尚未上线，实施时直接按新契约改造，不设计历史消息兼容层。

### 1. EventMessage 外层契约

推荐在 `packages/events/proto/event_message.proto` 中定义：

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

字段规则：

| 字段 | 规则 |
|---|---|
| `event_id` | 全局幂等键；由事实生产者稳定生成，重试/Outbox 重放不得变化 |
| `event_name` | 事件逻辑名称，例如 `market.trade.received`，不使用模糊的 `changed` |
| `event_version` | Payload/语义版本；新项目直接演进版本，不承担 `protocol_version` 兼容层职责 |
| `space_id` | 业务空间，例如交易所、账户域或策略空间；由事件注册表声明是否必填 |
| `subject_id` | 事件主体，例如 `BTC-USDT`、Dataset ID；无主体事件可为空 |
| `occurred_at` | 事实发生时间，供 event-time 聚合、迟到处理和回放使用 |
| `payload` | 具体 Protobuf Payload；Dataset、频率、窗口、交易 ID 等业务字段放在 Payload 内 |

明确不放入核心消息体的内容：

- `protocol_version`：新项目不增加另一套协议版本；事件版本已经覆盖消息语义演进。
- `topic`：NATS Subject 是基础设施路由信息，不是重复的业务元数据。
- `content_type`、`payload_type`：由注册表和生成代码确定，不让生产者重复填写。
- `published_at`、`producer`：由 JetStream/Outbox/消息头和观测系统记录。
- `causation_id`、`correlation_id`、`attributes`：暂不进入核心契约；未来确实需要时放入受治理的 Header/Trace，不向 Payload 添加自由扩展字段。
- `partition_key`：由注册表根据 `subject_id` 或受控 Subject 模板推导。
- `source_sequence`、`revision`、`dataset_id`、`freq`：仅在对应业务 Payload 中定义，不能污染所有事件。

`packages/events` 初始目录：

```text
packages/events/
  proto/event_message.proto
  registry/events.yaml
  spec.go
  publisher.go
  consumer.go
  subject.go
  validation.go
```

它建立在现有 `packages/jetstream` 之上：`packages/jetstream` 只负责通用传输、Pull Consumer、ACK 和重试动作；`packages/events` 负责类型化事件注册、Subject 生成、Envelope 编解码、Payload 校验和业务模块可用的发布/消费 API。

业务模块只允许使用类似下面的入口：

```go
events.Publish(ctx, events.MarketTradeReceived, payload, events.PublishOptions{
  EventID:    eventID,
  OccurredAt: eventTime,
  SpaceID:    spaceID,
  SubjectID:  subjectID,
})
```

不允许业务代码直接写：

```go
jetstream.Publish(ctx, "moox.market.trade.received.v1.crypto.BTC-USDT", rawBytes)
```

### 2. Event Registry 与事件治理

注册表是所有事件的单一事实来源，至少管理：逻辑名称、版本、Payload 类型、NATS Subject 模板、Stream、分区/顺序键、Owner 和上下文必填规则。

建议文件为 `packages/events/registry/events.yaml`：

```yaml
version: 1
events:
  - name: market.trade.received
    version: 1
    payload: trpc.moox.market.TradeReceived
    subject: moox.market.trade.received.v1.<space>.<subject>
    stream: market
    partition_key: subject_id
    owner: collector
  - name: market.kline.closed
    version: 1
    payload: trpc.moox.market.KlineClosed
    subject: moox.market.kline.closed.v1.<space>.<subject>.<freq>
    stream: market
    partition_key: subject_id
    owner: streamcalc
  - name: storage.rows.upserted
    version: 1
    payload: trpc.moox.storage.RowsUpserted
    subject: moox.storage.rows.upserted.v1.<space>.<dataset>
    stream: storage
    partition_key: subject_id
    owner: storage
```

规则：

1. 每个事件必须注册，Payload 类型必须存在且可反射校验。
2. 事件版本、Payload 类型、Subject 版本和 Stream 映射必须一致。
3. 一个事件只能落到一个 Stream class；Stream 的 `retention`、容量和消费策略不写入事件契约。
4. Subject 只用于路由，运行时仍校验 Subject 与 `EventMessage` 的 `event_name/version/space_id/subject_id` 一致。
5. 业务模块的生产者、消费者、ACL 和文档由注册表校验或生成；CI 禁止出现未注册的裸 Subject。
6. 存储事件统一改名为 `storage.rows.upserted`；不保留 `DatasetFieldsChanged` 兼容别名。

### 3. Retention 与 EventBus 服务边界

`retention` 只表示 JetStream Stream 的消息保留窗口，例如 `market=168h`、`storage=72h`。它影响：

- Consumer 重启后能回放多久；
- streamcalc、Factor、Archive 的恢复和 Backfill 能否直接依赖事件总线；
- JetStream 磁盘占用和过期清理压力。

它不表示 Storage 已落盘数据的保留期，也不改变 ACK、NAK、`AckWait`、`MaxDeliver` 或 `MaxAckPending`。

因此应把配置拆开：

```yaml
streams:
  - name: market
    jetstream: MOOX_MARKET
    retention: 168h
    max_bytes: 0
  - name: storage
    jetstream: MOOX_STORAGE
    retention: 72h
    max_bytes: 0
```

`modules/eventbus` 是一个独立部署的服务：启动时加载 topology 和事件注册表，创建/校验 Stream、Durable Consumer、ACL/TLS 与健康检查；其他服务通过 NATS 连接使用它。它不包含业务事件 Handler，也不执行 K 线聚合。

### 4. 初始事件分类

只把已经发生的事实命名为 Event；请求执行某件事时，未来单独定义 `CommandMessage`，状态快照时单独定义 `SnapshotMessage`，不把三者混在 `EventMessage` 中。

```text
market.trade.received
market.quote.updated
market.kline.updated
market.kline.closed
storage.rows.upserted
storage.dataset.created
storage.dataset.subject_bound
factor.calculation.requested
factor.calculation.completed
strategy.signal.generated
trade.order.intent_created
trade.order.state_changed
trade.fill.received
dlq.event.rejected
```

### 5. Collector、streamcalc 与 Storage 的事实流

目标链路：

```text
Collector
  -> market.trade.received / market.kline.closed
  -> NATS JetStream
  -> streamcalc
  -> Storage WriteFields
  -> storage.rows.upserted
  -> Factor / Strategy / Storage View / Archive
```

当前 Binance Collector 仍存在直接调用 Storage RPC、只接收一-shot K 线并过滤 closed Kline 的路径；实施时必须明确区分：

- 实时路径：Collector 只发布标准市场事件，streamcalc 负责聚合并写 Storage，写成功后由 Storage Outbox 发布 `storage.rows.upserted`。
- 历史/回补路径：可以保留批量导入或直接 Storage 写入，但必须明确它是独立的 backfill path，不能与同一实时事实做双写。

### 6. Event 生命周期与幂等

```text
producer creates fact
  -> Registry validates
  -> Outbox persist
  -> JetStream publish
  -> Consumer decode
  -> Inbox/idempotency check
  -> handler updates state and downstream outbox
  -> ACK
```

ACK 只能发生在 Inbox/状态/下游 Outbox 的业务提交完成之后。重复 `event_id` 必须视为已处理并 ACK；临时错误使用 `InProgress`/NAK；Payload 不合法或不可恢复错误进入 DLQ/TERM。

`event_id` 的稳定生成规则：交易事件使用 `exchange + trade_id`；K 线事件使用 `source + subject + freq + window_start + revision`；Storage 事件使用 Storage Outbox ID。重试和 Outbox 重放不能重新生成 ID。

## 文件责任边界

| 文件/目录 | 责任 |
|---|---|
| `modules/eventbus` | 独立部署的 NATS/JetStream topology、Stream、Durable、ACL/TLS 和健康检查服务；不承载业务 Handler |
| `modules/eventbus/config/app.yaml` | EventBus Stream、Topic Family、Durable Consumer 的事实来源；`retention` 只在 Stream topology 中配置 |
| `packages/events` | EventMessage、事件注册表、Subject 生成、Payload 编解码/校验、类型化 Publish/Consume API |
| `packages/jetstream` | 通用 NATS/JetStream Publish、Consumer Bind、Delivery ACK/NAK/TERM 生命周期；不承载业务事件命名 |
| `modules/storage/internal/service/datanode/pebble` | Fact + Outbox 原子提交与 Storage 事件 Envelope |
| `modules/storage/internal/service/view` | Storage View 消费、Subject Lane、Live/Backfill 协调 |
| `modules/streamcalc` | 消费市场事件、按 event-time 聚合 K 线、维护窗口状态、写 Storage；不直接依赖 Python Runtime |
| `modules/factor` | Storage 事件实时触发、Factor batch window、Python Runtime |
| `modules/archive` | Storage 事件解码、Journal 幂等与 Parquet 归档 |
| `modules/monitor`、`modules/trade` | 消费 ACK/重试/DLQ 行为统一接入与可观测性 |
| `modules/eventbus/test`、各模块 `test` 目录 | 跨模块拓扑、重复消息、重启和恢复验证 |

## 执行阶段和依赖

为避免先在旧契约上扩展业务，实际执行顺序调整为：

```text
Phase A  基线与基础设施：Task 1 -> Task 9 -> Task 10
Phase B  事件迁移与实时入口：Task 11 -> Task 13
Phase C  流计算闭环：Task 12
Phase D  可靠性与并发治理：Task 2 -> Task 3 -> Task 4 -> Task 5 -> Task 6 -> Task 7 -> Task 8
Phase E  全链路验收：Task 14
```

Task 2-8 是此前已确认的 Storage/Consumer/Factor 可靠性工作；其中所有旧事件名、旧 Envelope 示例和旧 Topic 引用均以本次 `packages/events` 设计为准替换。Task 12 完成最小 K 线闭环后，再扩展 Trade/Tick 输入和更多聚合算子。

## 实施顺序

### Task 1: 建立当前契约基线与工作树保护

**Files:**
- Read: `/Users/mooyang/Documents/go/src/github.com/mooyang-code/moox/go.work`
- Read: `/Users/mooyang/Documents/go/src/github.com/mooyang-code/moox/modules/eventbus/config/app.yaml`
- Read: `/Users/mooyang/Documents/go/src/github.com/mooyang-code/moox/modules/factor/config/app.yaml`
- Read: `/Users/mooyang/Documents/go/src/github.com/mooyang-code/moox/modules/archive/config/app.yaml`
- Record: `/Users/mooyang/Documents/Codex/2026-07-22/new-chat/outputs/moox-contract-baseline.txt`

- [ ] **Step 1: 记录基线 SHA 与工作树状态**

Run:

```bash
cd /Users/mooyang/Documents/go/src/github.com/mooyang-code/moox
git rev-parse HEAD
git status --short --branch
```

Expected: 记录当前 commit SHA；已有未提交修改保持原样，不纳入本次修复的回滚范围。

- [ ] **Step 2: 生成 active contract 搜索结果**

Run:

```bash
cd /Users/mooyang/Documents/go/src/github.com/mooyang-code/moox
rg -n 'rows_committed|fields_changed|application/protobuf|application/x-protobuf|storage_view|factor_calc|moox_archive_kline_v1' \
  modules packages scripts --glob '*.go' --glob '*.yaml' --glob '*.sh'
```

Expected: 将结果保存到基线文件，区分 active code/config/test 与历史设计文档；后续任务以该文件作为“无残留”的验证基准。

- [ ] **Step 3: 建立模块级测试基线**

Run:

```bash
cd /Users/mooyang/Documents/go/src/github.com/mooyang-code/moox/modules/eventbus && go test -count=1 ./...
cd /Users/mooyang/Documents/go/src/github.com/mooyang-code/moox/packages/jetstream && go test -count=1 ./...
cd /Users/mooyang/Documents/go/src/github.com/mooyang-code/moox/modules/storage && go test -count=1 ./...
cd /Users/mooyang/Documents/go/src/github.com/mooyang-code/moox/modules/factor && go test -count=1 ./...
cd /Users/mooyang/Documents/go/src/github.com/mooyang-code/moox/modules/archive && go test -count=1 ./...
```

Expected: 每个模块单独记录 PASS/FAIL；不把根目录 `go test ./...` 当作多模块完整验证。

- [ ] **Step 4: 保存基线，不修改业务代码**

将 SHA、工作树状态、active contract 搜索结果和各模块测试结果保存到
`/Users/mooyang/Documents/Codex/2026-07-22/new-chat/outputs/moox-contract-baseline.txt`。
该基线文件属于执行记录，不进入 MooX 业务仓库；执行期间不修改或覆盖已有未提交文件。

### Task 2: 将 Storage 通用变更事件替换为明确事实事件

**Files:**
- Modify: `/Users/mooyang/Documents/go/src/github.com/mooyang-code/moox/modules/storage/internal/service/datanode/pebble/event.go:36-46`
- Modify: `/Users/mooyang/Documents/go/src/github.com/mooyang-code/moox/modules/factor/config/app.yaml:20-22`
- Modify: `/Users/mooyang/Documents/go/src/github.com/mooyang-code/moox/modules/factor/internal/bootstrap/config.go:135-137,198-205`
- Modify: `/Users/mooyang/Documents/go/src/github.com/mooyang-code/moox/modules/archive/config/app.yaml:10-19`
- Modify: `/Users/mooyang/Documents/go/src/github.com/mooyang-code/moox/modules/archive/internal/config/config.go:90-95`
- Modify: `/Users/mooyang/Documents/go/src/github.com/mooyang-code/moox/modules/admin/cmd/cli/eventbus_credentials.go:273`
- Test: `/Users/mooyang/Documents/go/src/github.com/mooyang-code/moox/modules/storage/internal/service/datanode/pebble/event_test.go`
- Test: `/Users/mooyang/Documents/go/src/github.com/mooyang-code/moox/modules/archive/internal/bootstrap/app_test.go`

- [ ] **Step 1: 写失败测试，锁定 `storage.rows.upserted` 契约**

测试必须断言 DataNode Outbox 生成的 `EventMessage` 同时满足：

```go
if got := msg.GetEventName(); got != "storage.rows.upserted" {
	 t.Fatalf("event_name=%q", got)
}
if got := msg.GetEventVersion(); got != 1 {
	 t.Fatalf("event_version=%d", got)
}
if got := msg.GetSpaceId(); got != spaceID {
	 t.Fatalf("space_id=%q", got)
}
if got := msg.GetSubjectId(); got != datasetID {
	 t.Fatalf("subject_id=%q", got)
}
```

- [ ] **Step 2: 运行失败测试**

```bash
cd /Users/mooyang/Documents/go/src/github.com/mooyang-code/moox
go test ./modules/storage/internal/service/datanode/pebble -run TestBuildRowsUpsertedEventContract -count=1
```

Expected: 在迁移完成前因旧 `DatasetFieldsChanged`/`fields_changed` 契约不满足 Registry 校验而失败。

- [ ] **Step 3: 修改 DataNode Outbox EventMessage**

将 [event.go](/Users/mooyang/Documents/go/src/github.com/mooyang-code/moox/modules/storage/internal/service/datanode/pebble/event.go) 改为使用 `packages/events.StorageRowsUpserted`，外层字段为 `event_name=storage.rows.upserted`、`event_version=1`、`space_id` 和 `subject_id=dataset_id`。Subject 由 Registry 生成：

```text
moox.storage.rows.upserted.v1.<space-token>.<dataset-token>
```

Content-Type、MessageType 等传输描述不再由 DataNode 业务代码手写。

- [ ] **Step 4: 修改 Factor/Archive/Admin active 配置**

删除 active 配置中的：

```text
moox.storage.rows.upserted.v1.>
```

统一替换为 Registry 生成的：

```text
moox.storage.rows.upserted.v1.>
```

同步修复 Archive bootstrap 测试，使测试绑定真实 EventBus topology，而不是自行创建旧主题。

- [ ] **Step 5: 运行契约测试和残留搜索**

```bash
cd /Users/mooyang/Documents/go/src/github.com/mooyang-code/moox
go test ./modules/storage/internal/service/datanode/pebble ./modules/archive/internal/bootstrap -count=1
rg -n 'rows_committed|fields_changed|DatasetFieldsChanged|application/protobuf' modules packages scripts --glob '*.go' --glob '*.yaml' --glob '*.sh'
```

Expected: active code/config/test 不再出现旧主题、旧事件名或旧 Content-Type；历史设计文档可以保留，但不得被运行时代码读取。

- [ ] **Step 6: Commit**

```bash
git add modules/storage modules/factor modules/archive modules/admin
git commit -m "fix: align storage event contracts"
```

### Task 3: 让 EventBus topology 成为唯一 Consumer 配置来源

**Files:**
- Modify: `/Users/mooyang/Documents/go/src/github.com/mooyang-code/moox/packages/jetstream/consumer.go`
- Modify: `/Users/mooyang/Documents/go/src/github.com/mooyang-code/moox/modules/factor/internal/trigger/nats.go`
- Modify: `/Users/mooyang/Documents/go/src/github.com/mooyang-code/moox/modules/archive/internal/bootstrap/app.go`
- Modify: `/Users/mooyang/Documents/go/src/github.com/mooyang-code/moox/modules/storage/internal/service/view/consume.go`
- Modify: `/Users/mooyang/Documents/go/src/github.com/mooyang-code/moox/modules/eventbus/config/app.yaml`
- Test: `/Users/mooyang/Documents/go/src/github.com/mooyang-code/moox/packages/jetstream/consumer_test.go`
- Test: `/Users/mooyang/Documents/go/src/github.com/mooyang-code/moox/modules/eventbus/test/eventbus_e2e_test.go`

- [ ] **Step 1: 增加按 Durable 绑定的显式 API**

在 `packages/jetstream` 增加只读绑定路径，语义为：

```go
type ConsumerBindRef struct {
	Stream      string
	Durable     string
	FetchMaxWait time.Duration
}

func (c *Client) BindManagedPullConsumer(ctx context.Context, ref ConsumerBindRef) (*PullConsumer, error)
```

该 API 必须：

1. 读取 JetStream `ConsumerInfo`；
2. 拒绝不存在的 Durable；
3. 使用服务端真实的 FilterSubject、AckWait、MaxDeliver、MaxAckPending、DeliverPolicy；
4. 只允许客户端覆盖 FetchMaxWait；
5. 不创建、不更新、不删除 Consumer。

- [ ] **Step 2: 为 Managed Bind 写失败测试**

覆盖以下情况：

```text
Durable 不存在 -> ErrConsumerNotFound
FilterSubject 与客户端旧配置不同 -> 仍以服务端配置为准
客户端无权限查询 ConsumerInfo -> 启动失败并暴露原因
```

- [ ] **Step 3: Factor/Archive/View 改为只声明 Stream + Durable**

业务模块不再重复声明 `Subject`、`AckWait`、`MaxAckPending` 和 `DeliverPolicy`；这些值统一来自 EventBus topology。

模块配置只保留：

```text
stream
durable
fetch_batch
fetch_max_wait
业务处理超时
```

- [ ] **Step 4: 增加跨模块 topology E2E**

测试流程：

```text
启动临时 NATS JetStream
加载 modules/eventbus/config/app.yaml
执行 Registry.Reconcile
绑定 storage_view、factor_calc、moox_archive_kline_v1
发布一个 storage.rows.upserted EventMessage
三个 Consumer 都收到各自消息
重复发布同一个 MessageID
验证消费者业务侧只产生一次有效处理
```

- [ ] **Step 5: Commit**

```bash
git add packages/jetstream modules/eventbus modules/factor modules/archive modules/storage
git commit -m "refactor: bind consumers from eventbus topology"
```

### Task 4: 统一 Consumer ACK、重试和错误可观测性

**Files:**
- Create: `/Users/mooyang/Documents/go/src/github.com/mooyang-code/moox/packages/jetstream/runner.go`
- Test: `/Users/mooyang/Documents/go/src/github.com/mooyang-code/moox/packages/jetstream/runner_test.go`
- Modify: `/Users/mooyang/Documents/go/src/github.com/mooyang-code/moox/modules/factor/internal/trigger/nats.go`
- Modify: `/Users/mooyang/Documents/go/src/github.com/mooyang-code/moox/modules/archive/internal/consumer/runner.go`
- Modify: `/Users/mooyang/Documents/go/src/github.com/mooyang-code/moox/modules/monitor/internal/metrics/consumer.go`
- Modify: `/Users/mooyang/Documents/go/src/github.com/mooyang-code/moox/modules/trade/internal/bootstrap/kernel_workers.go`

- [x] **Step 1: 定义最小 Handler Outcome**

```go
type HandlerDecision uint8

const (
	DecisionAck HandlerDecision = iota + 1
	DecisionRetry
	DecisionTerm
)

type HandlerResult struct {
	Decision HandlerDecision
	Delay    time.Duration
	Err      error
}

type DeliveryHandler interface {
	Handle(context.Context, *Delivery) HandlerResult
}
```

- [x] **Step 2: 先写 Runner 行为测试**

必须覆盖：

```text
成功 -> Ack
临时错误 -> InProgress 或 Nak
永久错误 -> Term
Ack 失败 -> 记录错误并返回
Term 失败 -> 记录错误并返回
ctx 取消 -> 不启动新的 Delivery
```

- [x] **Step 3: 迁移 Monitor 和 Factor**

禁止继续使用：

```go
_ = c.HandleDelivery(ctx, d)
_ = delivery.Ack(ctx)
_ = delivery.Term(ctx)
```

所有 ACK/NAK/TERM 错误必须进入统一 counter，并影响 Runner 返回值或模块健康状态。

- [x] **Step 4: 迁移 Archive 和 Trade**

保留它们现有的 Journal/Inbox/交易状态幂等逻辑；Runner 只统一 Transport 层动作，不把业务去重逻辑搬进共享包。

- [ ] **Step 5: Commit**

```bash
git add packages/jetstream modules/factor modules/archive modules/monitor modules/trade
git commit -m "refactor: standardize consumer delivery outcomes"
```

### Task 5: 实现 Storage View Subject 级并发

**Files:**
- Modify: `/Users/mooyang/Documents/go/src/github.com/mooyang-code/moox/modules/storage/internal/service/view/service.go`
- Modify: `/Users/mooyang/Documents/go/src/github.com/mooyang-code/moox/modules/storage/internal/service/view/consume.go`
- Modify: `/Users/mooyang/Documents/go/src/github.com/mooyang-code/moox/modules/eventbus/config/app.yaml:160-168`
- Modify: `/Users/mooyang/Documents/go/src/github.com/mooyang-code/moox/modules/storage/config/storage.yaml:17-26`
- Test: `/Users/mooyang/Documents/go/src/github.com/mooyang-code/moox/modules/storage/internal/service/view/consume_test.go`
- Test: `/Users/mooyang/Documents/go/src/github.com/mooyang-code/moox/modules/storage/test/storage_view_concurrency_e2e_test.go`

- [ ] **Step 1: 增加可配置消费参数**

```go
type EventConsumerOptions struct {
	FetchBatch     int
	MaxWorkers     int
	MaxAckPending  int
	Ordering       string // subject
}
```

默认值：

```text
FetchBatch=8
MaxWorkers=4
MaxAckPending=8
Ordering=subject
```

- [ ] **Step 2: 把单一 liveGate 改成实时共享锁 + Backfill 独占锁**

实时 Delivery 获取读锁；Backfill 获取写锁。写锁等待期间禁止新实时任务进入，避免 Backfill 启动后仍不断增加实时工作量。

- [ ] **Step 3: 实现 Subject Lane Dispatcher**

Dispatcher 的核心约束：

```text
laneKey = delivery.Subject
同一个 laneKey 只有一个 active handler
不同 laneKey 可以同时处理
单条 Delivery 只有在对应 Handler 完成后才能 ACK
```

禁止以 `MessageID`、NATS Stream Sequence 或随机 worker 作为排序键。

- [ ] **Step 4: 处理错误和重试**

同一 Subject Lane 内：

1. 当前 Delivery 临时失败时保持 `InProgress`；
2. 不允许后续同 Subject Delivery 越过当前失败事件；
3. 永久错误才 `Term` 或进入 DLQ；
4. 不同 Subject Lane 的失败不能阻塞其他 Dataset。

- [ ] **Step 5: 增加并发正确性测试**

测试必须验证：

```text
Dataset A handler 延迟 500ms，Dataset B 仍能完成
同一 Dataset 的事件按投递顺序应用
同一 Dataset 失败时，后续事件不会越过它
多个 Dataset 并行时 View runtime/schema 不发生竞态
Backfill 等待所有实时任务完成后独占执行
Backfill 完成后实时事件继续消费
```

- [ ] **Step 6: 运行 Storage View E2E**

```bash
cd /Users/mooyang/Documents/go/src/github.com/mooyang-code/moox
go test ./modules/storage/internal/service/view/... -count=1 -race
go test ./modules/storage/test -run TestStorageViewSubjectConcurrency -count=1
```

Expected: 同一 Subject 保序、不同 Subject 并发；`-race` 无数据竞争。

- [ ] **Step 7: Commit**

```bash
git add modules/storage modules/eventbus/config/app.yaml
git commit -m "feat: parallelize storage view by dataset subject"
```

### Task 6: 补齐 Storage View 和 Outbox 运行指标

**Files:**
- Modify: `/Users/mooyang/Documents/go/src/github.com/mooyang-code/moox/modules/storage/internal/observability/view_metrics.go`
- Modify: `/Users/mooyang/Documents/go/src/github.com/mooyang-code/moox/modules/storage/internal/service/view/consume.go`
- Modify: `/Users/mooyang/Documents/go/src/github.com/mooyang-code/moox/modules/storage/internal/service/datanode/outbox_relay.go`
- Modify: `/Users/mooyang/Documents/go/src/github.com/mooyang-code/moox/modules/eventbus/internal/health/server.go`
- Test: `/Users/mooyang/Documents/go/src/github.com/mooyang-code/moox/modules/storage/internal/observability/view_metrics_test.go`

- [ ] **Step 1: 增加低基数指标**

至少增加：

```text
moox_storage_view_consumer_lag_messages
moox_storage_view_oldest_pending_event_age_seconds
moox_storage_view_delivery_duration_seconds
moox_storage_view_ack_errors_total
moox_storage_view_in_progress_errors_total
moox_storage_view_lane_active
moox_storage_outbox_pending_entries
moox_storage_outbox_oldest_age_seconds
moox_storage_outbox_publish_errors_total
moox_storage_outbox_duplicate_publish_total
```

不要把完整 Subject、Symbol 或 MessageID 作为 Prometheus label；Subject 只允许使用受控的 Dataset 维度或聚合值。

- [ ] **Step 2: 增加健康状态规则**

健康检查必须区分：

```text
process alive
NATS connected
consumer bound
outbox draining
oldest pending within threshold
```

NATS 连接正常不应掩盖 Storage View backlog 或 Outbox 持续失败。

- [ ] **Step 3: Commit**

```bash
git add modules/storage modules/eventbus/internal/health
git commit -m "feat: expose streaming lag and delivery health"
```

### Task 7: 明确 Factor 实时、回放和 Python Runtime 契约

**Files:**
- Modify: `/Users/mooyang/Documents/go/src/github.com/mooyang-code/moox/modules/factor/config/app.yaml`
- Modify: `/Users/mooyang/Documents/go/src/github.com/mooyang-code/moox/modules/factor/internal/bootstrap/config.go`
- Modify: `/Users/mooyang/Documents/go/src/github.com/mooyang-code/moox/modules/factor/internal/trigger/nats.go`
- Modify: `/Users/mooyang/Documents/go/src/github.com/mooyang-code/moox/modules/factor/internal/trigger/event_batcher.go`
- Create: `/Users/mooyang/Documents/go/src/github.com/mooyang-code/moox/modules/factor/internal/trigger/replay.go`
- Test: `/Users/mooyang/Documents/go/src/github.com/mooyang-code/moox/modules/factor/internal/trigger/event_batcher_test.go`
- Test: `/Users/mooyang/Documents/go/src/github.com/mooyang-code/moox/modules/factor/test/factor_replay_e2e_test.go`

- [ ] **Step 1: 保持 Live Consumer 使用 `DeliverNew`**

实时消费不改为 `DeliverAll`，避免服务首次启动时重新执行全部历史事件。

- [x] **Step 2: 增加明确的 Replay/Rebuild 入口**

Replay 必须接收：

```text
space_id
dataset_id
start_time
end_time
factor_version
target_run_id
```

Replay 使用独立的任务 ID 和幂等键，不复用实时 batch 的 processing-time deadline。

- [ ] **Step 3: 区分 processing-time window 和 event-time bar**

`EventBatcher` 保留当前 2 秒收敛窗口，但任务必须同时记录：

```text
first_received_at
last_received_at
min_data_time
max_data_time
```

迟到数据必须有明确策略：重新计算、忽略或生成修正任务，不能由 `time.Now()` 隐式决定。

- [ ] **Step 4: 增加 Python Runtime readiness**

启动时检查：

```text
python_bin 可执行
factor 运行目录可读
Python worker 可启动并返回版本信息
```

检查失败时 Factor readiness 为 false，并给出可定位错误。

- [ ] **Step 5: Commit**

```bash
git add modules/factor
git commit -m "feat: separate factor live and replay execution"
```

### Task 8: PublishBatch 注释、幂等语义和最终验证

**Files:**
- Modify: `/Users/mooyang/Documents/go/src/github.com/mooyang-code/moox/packages/jetstream/publisher.go:83`
- Modify: `/Users/mooyang/Documents/go/src/github.com/mooyang-code/moox/packages/jetstream/client_test.go:234-270`
- Modify: `/Users/mooyang/Documents/go/src/github.com/mooyang-code/moox/docs/架构总览.md`
- Create: `/Users/mooyang/Documents/go/src/github.com/mooyang-code/moox/modules/eventbus/test/storage_consumers_e2e_test.go`

- [ ] **Step 1: 修正 PublishBatch 注释和测试名称**

测试名称从 `TestPublishBatchPreservesOrderAndPartialFailures` 调整为能表达真实语义的名称，例如：

```text
TestPublishBatchPreservesResultOrderAndPartialFailures
```

测试必须明确断言结果 slice 的 input index 对齐，不断言 PubAck 的 Stream Sequence 顺序。

- [ ] **Step 2: 增加 at-least-once 恢复测试**

测试流程：

```text
发布 MessageID=m1
模拟 publish 成功但 outbox delete 失败
重启 relay
再次发布 m1
验证 View/Archive/Trade 业务侧只产生一次最终效果
```

- [ ] **Step 3: 增加 EventBus topology 全链路测试**

验证：

```text
DataNode storage.rows.upserted 消息
storage_view consumer
factor_calc consumer
archive consumer
MessageID 去重
consumer 重启恢复
```

- [ ] **Step 4: 执行最终模块验证**

```bash
cd /Users/mooyang/Documents/go/src/github.com/mooyang-code/moox/modules/eventbus && go test -count=1 ./...
cd /Users/mooyang/Documents/go/src/github.com/mooyang-code/moox/packages/jetstream && go test -count=1 ./...
cd /Users/mooyang/Documents/go/src/github.com/mooyang-code/moox/modules/storage && go test -count=1 ./...
cd /Users/mooyang/Documents/go/src/github.com/mooyang-code/moox/modules/factor && go test -count=1 ./...
cd /Users/mooyang/Documents/go/src/github.com/mooyang-code/moox/modules/archive && go test -count=1 ./...
cd /Users/mooyang/Documents/go/src/github.com/mooyang-code/moox/modules/storage && go test -race -count=1 ./internal/service/view/...
```

- [ ] **Step 5: 执行 active contract 检查**

```bash
rg -n 'rows_committed|fields_changed|DatasetFieldsChanged|application/protobuf' modules packages scripts \
  --glob '*.go' --glob '*.yaml' --glob '*.sh'
```

Expected: 无 active runtime/config/test 残留；历史计划文档不参与运行时构建。

- [ ] **Step 6: 记录最终 SHA、测试范围和未完成项**

```bash
git rev-parse HEAD
git status --short --branch
```

最终报告必须区分：

```text
模块级测试结果
跨模块 E2E 结果
race 测试结果
工作树是否干净
提交 SHA
远程 CI 是否验证
```

### Task 9: 落地 `packages/events` 与 EventMessage

**Files:**
- Create: `/Users/mooyang/Documents/go/src/github.com/mooyang-code/moox/packages/events/proto/event_message.proto`
- Create: `/Users/mooyang/Documents/go/src/github.com/mooyang-code/moox/packages/events/registry/events.yaml`
- Create: `/Users/mooyang/Documents/go/src/github.com/mooyang-code/moox/packages/events/spec.go`
- Create: `/Users/mooyang/Documents/go/src/github.com/mooyang-code/moox/packages/events/publisher.go`
- Create: `/Users/mooyang/Documents/go/src/github.com/mooyang-code/moox/packages/events/consumer.go`
- Create: `/Users/mooyang/Documents/go/src/github.com/mooyang-code/moox/packages/events/subject.go`
- Create: `/Users/mooyang/Documents/go/src/github.com/mooyang-code/moox/packages/events/validation.go`
- Remove/Replace: `/Users/mooyang/Documents/go/src/github.com/mooyang-code/moox/packages/messagepb/moox_message.proto`
- Test: `/Users/mooyang/Documents/go/src/github.com/mooyang-code/moox/packages/events/*_test.go`

- [ ] **Step 1: 定义精简但显式的 EventMessage**

仅保留：

```text
event_id, event_name, event_version, space_id, subject_id, occurred_at, payload
```

`event_name`、`event_version`、`space_id`、`subject_id` 必须由消费者直接从外层字段读取；不能要求消费者解析 NATS Subject。`subject_id` 对无主体事件可为空，但是否必填由注册表控制。

- [ ] **Step 2: 实现强类型事件规格**

事件规格至少包含：

```go
type EventSpec struct {
    Name         string
    Version      uint32
    PayloadType  protoreflect.FullName
    Subject      SubjectTemplate
    Stream       string
    PartitionKey PartitionKey
    Owner        string
}
```

提供 `MarketTradeReceived`、`MarketKlineClosed`、`StorageRowsUpserted` 等强类型规格；生产者不再传入任意事件名或任意 Topic。

- [ ] **Step 3: 实现 Registry 加载和启动校验**

启动/测试时拒绝以下情况：事件重复注册、Payload 类型不存在、Subject 模板缺少必要上下文、事件版本与 Subject 版本不一致、Stream 未定义、分区键不可推导。

- [ ] **Step 4: 实现类型化 Publish/Consume API**

`packages/events` 调用 `packages/jetstream` 完成发布和消费，但业务模块只依赖事件 API。Publish 时自动完成：Payload 序列化、EventMessage 组装、Registry 校验、Subject 生成和 `event_id` 检查。

- [ ] **Step 5: 增加治理检查**

CI 必须检查：

```text
所有事件均存在 Registry
所有 Payload 均可反射解析
所有生产/消费模块使用注册事件
业务代码不存在裸 NATS Subject 和裸 jetstream.Publish
每个事件恰好映射一个 Stream
Subject 与 EventMessage 元数据一致
```

- [ ] **Step 6: Commit**

```bash
git add packages/events packages/messagepb
git commit -m "feat: add governed event message contracts"
```

### Task 10: 重构 EventBus topology 与独立部署边界

**Files:**
- Modify: `/Users/mooyang/Documents/go/src/github.com/mooyang-code/moox/modules/eventbus/cmd/server/main.go`
- Modify: `/Users/mooyang/Documents/go/src/github.com/mooyang-code/moox/modules/eventbus/internal/broker/server.go`
- Modify: `/Users/mooyang/Documents/go/src/github.com/mooyang-code/moox/modules/eventbus/config/app.yaml`
- Modify: `/Users/mooyang/Documents/go/src/github.com/mooyang-code/moox/modules/eventbus/internal/health/server.go`
- Test: `/Users/mooyang/Documents/go/src/github.com/mooyang-code/moox/modules/eventbus/test/*_test.go`

- [ ] **Step 1: 明确 EventBus 只做基础设施编排**

服务职责限定为：连接/管理 NATS、创建或校验 JetStream Stream、创建或校验 Durable Consumer、加载 Registry、校验拓扑、管理 ACL/TLS 和暴露健康状态。它不实现 `KlineAggregator`、Factor、Storage View 或业务重试 Handler。

- [ ] **Step 2: 把 retention 从事件契约拆到 Stream topology**

事件 Registry 不允许出现 `retention`。`modules/eventbus/config/app.yaml` 单独定义 `streams`，并记录 `retention`、`max_bytes` 等存储策略；Consumer 配置单独记录 `AckWait`、`MaxDeliver`、`MaxAckPending`、FilterSubject 和 DeliverPolicy。

- [ ] **Step 3: 以 Registry 生成/校验 Topic Family、Stream 和 ACL**

EventBus 启动时拒绝未知事件 Subject；发现 Stream/Consumer 与 Registry 不一致时失败而不是静默创建一套旁路配置。业务服务只通过 `stream + durable` 绑定托管 Consumer。

- [ ] **Step 4: 增加独立部署验收**

使用临时 NATS JetStream 启动 EventBus，加载 topology，验证 Stream、Durable、ACL/TLS、健康检查和未知事件拒绝。业务模块随后只连接 EventBus 暴露的 NATS 地址，不复制 topology 创建逻辑。

- [ ] **Step 5: Commit**

```bash
git add modules/eventbus
git commit -m "refactor: make eventbus topology authoritative"
```

### Task 11: 全量迁移事件命名和消息生产/消费

**Files:**
- Modify: `/Users/mooyang/Documents/go/src/github.com/mooyang-code/moox/modules/collector`
- Modify: `/Users/mooyang/Documents/go/src/github.com/mooyang-code/moox/modules/storage`
- Modify: `/Users/mooyang/Documents/go/src/github.com/mooyang-code/moox/modules/factor`
- Modify: `/Users/mooyang/Documents/go/src/github.com/mooyang-code/moox/modules/archive`
- Modify: `/Users/mooyang/Documents/go/src/github.com/mooyang-code/moox/modules/monitor`
- Modify: `/Users/mooyang/Documents/go/src/github.com/mooyang-code/moox/modules/trade`
- Test: `/Users/mooyang/Documents/go/src/github.com/mooyang-code/moox/modules/*/test`

- [ ] **Step 1: 建立初始事件目录**

首批事件至少包括：

```text
market.trade.received
market.quote.updated
market.kline.updated
market.kline.closed
storage.rows.upserted
storage.dataset.created
storage.dataset.subject_bound
factor.calculation.requested
factor.calculation.completed
strategy.signal.generated
trade.order.intent_created
trade.order.state_changed
trade.fill.received
dlq.event.rejected
```

- [ ] **Step 2: 替换旧 Storage 通用事件**

将 `DatasetFieldsChanged` 改为明确的 `storage.rows.upserted`，Payload 改为 `RowsUpserted`；不添加旧主题兼容别名，不让新的业务代码继续依赖 `fields_changed`、`rows_committed` 等旧命名。

- [ ] **Step 3: 清理所有直接发布路径**

Collector、Storage、Factor、Archive、Monitor、Trade 统一使用 `packages/events`。保留 `packages/jetstream` 仅作为底层传输包；任何业务模块出现裸 Topic、裸 Envelope 或自己填写 Content-Type 都应由编译检查/CI 检查拦截。

- [ ] **Step 4: 明确事件事实与命令边界**

已发生的行情、K 线、Storage 行更新、成交和订单状态变化使用 Event；要求执行计算、下单或回补时另建 Command，不把“请求”伪装成 Event。

- [ ] **Step 5: 增加事件链路契约测试**

每个首批事件至少测试：构造、Registry 校验、Subject 生成、外层字段读取、Payload 解码、错误事件拒绝和重复 `event_id` 幂等。

- [ ] **Step 6: Commit**

```bash
git add modules packages
git commit -m "refactor: migrate modules to governed events"
```

### Task 12: 实现 `modules/streamcalc` 实时流计算服务

**Files:**
- Create: `/Users/mooyang/Documents/go/src/github.com/mooyang-code/moox/modules/streamcalc/cmd/server/main.go`
- Create: `/Users/mooyang/Documents/go/src/github.com/mooyang-code/moox/modules/streamcalc/internal/bootstrap`
- Create: `/Users/mooyang/Documents/go/src/github.com/mooyang-code/moox/modules/streamcalc/internal/consumer`
- Create: `/Users/mooyang/Documents/go/src/github.com/mooyang-code/moox/modules/streamcalc/internal/aggregate`
- Create: `/Users/mooyang/Documents/go/src/github.com/mooyang-code/moox/modules/streamcalc/internal/state`
- Create: `/Users/mooyang/Documents/go/src/github.com/mooyang-code/moox/modules/streamcalc/internal/storage`
- Create: `/Users/mooyang/Documents/go/src/github.com/mooyang-code/moox/modules/streamcalc/config/app.yaml`
- Test: `/Users/mooyang/Documents/go/src/github.com/mooyang-code/moox/modules/streamcalc/internal/*/*_test.go`
- Test: `/Users/mooyang/Documents/go/src/github.com/mooyang-code/moox/modules/streamcalc/test/streamcalc_e2e_test.go`

- [ ] **Step 1: 定义服务边界**

`streamcalc` 是独立部署的 Go 服务，消费 `market.trade.received`、`market.quote.updated` 或 `market.kline.closed`，维护聚合状态并调用 Storage 写入接口；它不拥有 Collector 的行情连接，不承担 Storage View 投影，也不调用 Python Runtime。

- [ ] **Step 2: 选择事件输入和聚合粒度**

第一阶段建议从 `market.kline.closed` 做稳定的 K 线聚合闭环，再扩展到 Trade/Tick 聚合。配置至少包含：

```text
input_event
output_event
space_id
subject_id / symbol
source_frequency
target_frequency
allowed_lateness
window_close_policy
```

例如 `1m -> 5m`：按照 `occurred_at` 或 Payload 中的 event time 计算窗口，不使用收到时间替代市场事实时间。

- [ ] **Step 3: 设计聚合状态**

状态键至少为：

```text
space_id + subject_id + target_frequency + window_start
```

状态保存 `open/high/low/close/volume`、已接收输入的幂等集合/水位、最新 `revision` 和窗口状态。状态存储可以先使用本地 Pebble；必须预留 checkpoint/rebuild 接口，使服务重启可从 JetStream 重放或从 Storage 回补重建。

- [ ] **Step 4: 设计迟到、重复和修正**

同一输入 `event_id` 重复到达只应用一次。事件时间超过窗口关闭点但在 `allowed_lateness` 内，生成同一窗口的新 `revision`；超过允许迟到窗口，进入明确的 late-data/DLQ 指标和补算队列，不静默覆盖已发布 K 线。

- [ ] **Step 5: 原子写入 Storage 并发布下游事实**

聚合结果提交必须遵循：状态更新、Storage `WriteFields`、下游 Outbox 的一致性边界。不能在 Storage 成功前 ACK 输入事件，也不能先发布 `market.kline.closed` 再写 Storage 导致下游看到不存在的数据。推荐使用 streamcalc 本地状态 + Storage 幂等写入 + Outbox/Inbox 记录；具体跨存储事务在实现任务中锁定。

- [ ] **Step 6: 增加可观测性**

至少暴露：输入 lag、event-time lag、窗口状态数、关闭窗口数、修正次数、迟到事件数、重复事件数、Storage 写入失败数、当前 checkpoint 和 DLQ 数量。指标标签只能使用受控的 space/frequency，不使用完整 Symbol 或 event_id 作为高基数标签。

- [ ] **Step 7: Commit**

```bash
git add modules/streamcalc
git commit -m "feat: add nats driven stream calculation service"
```

### Task 13: 改造 Collector 实时入口，禁止双写

**Files:**
- Modify: `/Users/mooyang/Documents/go/src/github.com/mooyang-code/moox/modules/collector/internal/sources/interface.go`
- Modify: `/Users/mooyang/Documents/go/src/github.com/mooyang-code/moox/modules/collector/internal/sources/binance/kline.go`
- Modify: `/Users/mooyang/Documents/go/src/github.com/mooyang-code/moox/modules/collector/internal/sources/binance/storage_rpc.go`
- Modify: `/Users/mooyang/Documents/go/src/github.com/mooyang-code/moox/modules/collector/internal/bootstrap`
- Test: `/Users/mooyang/Documents/go/src/github.com/mooyang-code/moox/modules/collector/test/collector_event_e2e_test.go`

- [ ] **Step 1: 区分 Live 与 Historical/Backfill Source**

Collector Source 接口不能只表达“一次性返回 K 线”。实时 Source 必须能够产出事件流；历史 Source 继续支持批量读取，但通过明确的 backfill job 入口执行。

- [ ] **Step 2: Live Collector 只发布市场事件**

Binance 实时数据先构造 `market.trade.received`、`market.quote.updated` 或 `market.kline.updated/closed`，通过 `packages/events` 写入 NATS。实时 Collector 不再直接调用 Storage RPC。

- [ ] **Step 3: 明确 closed Kline 和 provisional Kline 语义**

未闭合 K 线使用 `market.kline.updated`；交易所确认窗口结束后使用 `market.kline.closed`。两者不能都伪装成同一个 Storage 更新事件，否则 streamcalc 无法判断是累积更新还是最终关闭。

- [ ] **Step 4: 验证没有实时双写**

E2E 注入一条行情事件，验证只有 streamcalc 写入 Storage，Collector 不产生第二次相同事实写入；重复 delivery 仍只产生一个最终 Storage 效果。

- [ ] **Step 5: Commit**

```bash
git add modules/collector
git commit -m "refactor: route live collector data through events"
```

### Task 14: 端到端验证事件链和恢复语义

**Files:**
- Create: `/Users/mooyang/Documents/go/src/github.com/mooyang-code/moox/modules/streamcalc/test/streaming_pipeline_e2e_test.go`
- Modify: `/Users/mooyang/Documents/go/src/github.com/mooyang-code/moox/modules/eventbus/test/eventbus_e2e_test.go`
- Modify: `/Users/mooyang/Documents/go/src/github.com/mooyang-code/moox/modules/storage/test/storage_view_concurrency_e2e_test.go`
- Modify: `/Users/mooyang/Documents/go/src/github.com/mooyang-code/moox/docs/架构总览.md`

- [ ] **Step 1: 验证事件拓扑**

```text
EventBus 启动并加载 Registry
Collector 发布 market.trade.received
streamcalc 收到并聚合
Storage 写入并发布 storage.rows.upserted
Factor/Strategy/View/Archive 各自收到标准事件
```

- [ ] **Step 2: 验证消息语义**

消费者直接读取 `event_name/version/space_id/subject_id`；删除或改变 NATS Subject 中的冗余上下文后，事件外层仍可正确处理。未知事件名、版本、Payload 或 Subject 映射必须被拒绝并进入 DLQ/健康告警。

- [ ] **Step 3: 验证重启和重复**

在 streamcalc、Storage View、Factor、Archive 处理过程中重启服务，验证 JetStream redelivery、Inbox 幂等、状态恢复、Outbox 重放和最终 ACK 行为；不把“无重复投递”误当作保证。

- [ ] **Step 4: 验证并发和顺序**

验证不同 Dataset 的 Storage View Lane 可以并发，同一 Subject 保序；streamcalc 同一聚合键的事件保序或按 revision 合并，其他 subject 不被单个热点键拖住。

- [ ] **Step 5: 运行最终命令并记录证据**

```bash
cd /Users/mooyang/Documents/go/src/github.com/mooyang-code/moox/modules/eventbus && go test -count=1 ./...
cd /Users/mooyang/Documents/go/src/github.com/mooyang-code/moox/packages/events && go test -count=1 ./...
cd /Users/mooyang/Documents/go/src/github.com/mooyang-code/moox/modules/streamcalc && go test -race -count=1 ./...
cd /Users/mooyang/Documents/go/src/github.com/mooyang-code/moox/modules/storage && go test -race -count=1 ./internal/service/view/...
```

同时运行 Registry lint、裸 Subject 搜索、模块 E2E，并记录测试结果、SHA、工作树状态和未完成项。

## 验收标准

### 必须满足

- `packages/events` 是所有业务事件的唯一 Publish/Consume 入口，Registry 是事件名称、版本、Payload、Subject、Stream 和顺序键的唯一事实来源。
- `EventMessage` 外层显式包含 `event_id`、`event_name`、`event_version`、`space_id`、`subject_id`、`occurred_at` 和 `payload`；消费者不需要解析 NATS Subject 获取这些信息。
- 核心 EventMessage 不包含 `protocol_version`、`topic`、`content_type`、`payload_type`、`published_at`、`producer`、`causation_id`、`correlation_id` 或自由扩展 `attributes`。
- `retention` 只存在于 EventBus Stream topology，不存在于 Event Registry 或 Payload；EventBus 服务可以独立启动、校验和暴露拓扑健康状态。
- `modules/eventbus` 不承载业务 Handler；`modules/streamcalc` 是独立 Go 服务，消费 NATS 事件并负责 K 线聚合。
- Collector 实时路径只发布市场事件，不与 streamcalc 对同一事实双写 Storage。
- Storage 事实事件统一使用 `storage.rows.upserted`；active runtime/config 和新契约测试不再依赖 `DatasetFieldsChanged`、`fields_changed` 或 `rows_committed`，旧通用 helper/legacy_storage 测试只作为非验收残留单独标记。
- streamcalc 能按 event-time 聚合目标 K 线，处理重复、迟到、窗口关闭和 revision，并在 Storage 写入完成后才 ACK 输入事件。
- Factor、Archive、Storage View 都能绑定 EventBus 当前声明的 Consumer。
- DataNode 发布的事件能被 Factor、Archive、Storage View 正确解码。
- 同一 Dataset Subject 内保持处理顺序。
- 不同 Dataset Subject 可以并行处理。
- 单个 Dataset 处理失败不会阻塞其他 Dataset。
- Backfill 与实时消费不会同时修改同一 View 边界。
- ACK、NAK、TERM、InProgress 失败均可观测。
- 重复发布不会产生重复业务效果。
- Factor readiness 能反映 Python Runtime 是否可用。

### 暂不作为验收条件

- 同一 Dataset 内不同 Symbol 的并发处理。
- exactly-once 发布。
- NATS 集群 HA。
- 删除 Python Runtime。
- 更换 Streaming Engine。

## 回滚策略

1. 新项目不做旧事件兼容回滚；契约切换前先在临时 NATS 环境完成 Registry、Stream、Durable 和全链路 E2E 验证。
2. 部署顺序为 EventBus topology -> `packages/events` 依赖的业务服务 -> Collector/streamcalc -> Factor/Archive/View 等下游消费者。
3. Subject Lane 并发通过配置开关控制；出现数据一致性问题时恢复 `MaxWorkers=1`、`FetchBatch=1`、`MaxAckPending=1`。
4. 回滚期间不得删除 Durable Consumer 或清理 JetStream 数据；保留 Outbox 和事件，以便按新版本从起点重放。
5. 发生数据不一致时使用 Replay/Rebuild 重新物化 View 或 streamcalc 状态，不通过跳过消息解决 backlog；涉及 K 线修正时使用 `revision` 和补算任务。

## 推荐提交拆分

```text
fix: align storage event contracts
feat: add governed event message contracts
refactor: make eventbus topology authoritative
refactor: migrate modules to governed events
feat: add nats driven stream calculation service
refactor: route live collector data through events
refactor: bind consumers from eventbus topology
refactor: standardize consumer delivery outcomes
feat: parallelize storage view by dataset subject
feat: expose streaming lag and delivery health
feat: separate factor live and replay execution
test: verify streaming recovery and idempotency
```
