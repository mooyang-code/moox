# Event System Local Simplification Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use `superpowers:subagent-driven-development` (recommended) or `superpowers:executing-plans` to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking. Use `superpowers:test-driven-development` for behavioral changes and `superpowers:verification-before-completion` before declaring completion.

**Goal:** 在不改变五事件契约、可靠性边界和 Storage View 顺序语义的前提下，收敛历史文档、统一 CloudNode 精确 Subject Consumer 入口、拆分 View 局部运行时、删除 Factor 未被执行链使用的 late-data 状态，并让单机默认部署不生成未使用的 EventBus TLS 凭据。

**Architecture:** `packages/events` 继续作为业务事件契约层：`NewConsumer` 绑定事件 family，新增 `NewSubjectConsumer` 通过 Registry identity 绑定一个精确 Subject；业务模块不得把裸 NATS Subject 传给契约层。Storage View 本轮只做行为保持的文件拆分，不迁移到无法保持按 Subject 有序、queued heartbeat 和 backfill gate 的通用 Runner。Factor 保留完整五维 bucket key、固定窗口和持久化 Inbox，只删除没有进入 scheduler/executor 的 late-data bookkeeping。EventBus 的十一类最小权限角色和远程 TLS 边界不变，默认 loopback 部署跳过凭据生成。

**Tech Stack:** Go 1.25, NATS JetStream, Protocol Buffers, tRPC-Go, Pebble/SQLite, Bash deployment tests, Go workspaces.

---

## 1. 已确认的边界

实施基线为：

```text
branch: feature/mooyang
commit: 5775dd6fc9530b9b5e0fcf39de1e76cd6708bd10
remote: origin/feature/mooyang
```

本计划必须保持：

1. `packages/events/registry.go` 仍只声明五个公共事件。
2. `EventMessage` 仍只有七个字段。
3. `modules/eventbus` 的业务拓扑仍只创建四个 Stream 和一个 KV，不创建业务 Consumer。
4. Storage View 同 Subject 串行、不同 Subject 并行；排队期间续 heartbeat；本地重试不能让同 Subject 后续消息越过；Backfill 使用写优先 gate。
5. Factor 的 bucket key 保持 `(space_id, source_dataset, target_dataset, subject_id, freq)`，窗口 deadline 从首条匹配事件开始且不滑动。
6. Factor 持久化 Inbox 保持 claim-before-ACK、scheduler 接受后 commit、失败或重启后 restore。
7. EventBus 十一类角色 ACL 不合并；远程 NATS 继续要求 TLS、CA 和认证。
8. Trade 当前 cancel/replace Saga、Storage/Strategy Outbox、Archive quarantine 不在本次范围内。

明确不做：

- 不新增 Event、Stream、KV、通用 DLQ、Schema Registry 或 exactly-once 协议。
- 不把 Storage View 直接迁到 `jetstream.Runner`。
- 不给 `jetstream.Runner` 增加 keyed scheduler、backfill gate 或 View 专用策略。
- 不把 Factor 改成只按 `symbol/freq` 分桶，也不改成每条事件重置 deadline 的滑动 debounce。
- 不合并 EventBus 角色，不关闭远程 TLS。
- 不修改历史 Git 提交；只在历史文档顶部增加状态标记。

## 2. 目标文件结构

### `packages/events`

```text
consumer.go
  ConsumerConfig
  SubjectConsumerConfig
  NewConsumer
  NewSubjectConsumer
  shared newConsumer

consumer_test.go
  family filter 与 exact filter 的 Registry 派生测试
  空 identity、未注册 Event 和不同 route 隔离测试
```

### `modules/storage/internal/service/view`

```text
consume.go                  Consumer options、StartEventConsumer、生命周期装配
delivery_policy.go          retry、ACK/NAK/TERM、永久错误分类
event_apply.go              EventMessage 解码、RowFieldUpsert 应用到 ViewIndex 和缺行恢复
subject_dispatcher.go       按 Subject 分组的有序队列和跨 Subject 并行调度
delivery_heartbeat.go       queued delivery 的 InProgress 生命周期
live_gate.go                live/backfill writer-priority gate

consume_test.go             options 与启动边界
delivery_policy_test.go     retry/TERM/ACK 行为
subject_dispatcher_test.go  同 Subject 顺序、跨 Subject 并行、queued heartbeat
live_gate_test.go           live/backfill 互斥
```

### `modules/factor/internal/trigger`

```text
event_batcher.go            固定窗口、完整 bucket key、Task 生成
event_batcher_inbox.go      Inbox claim/commit/restore
event_batcher_test.go       窗口、分桶和任务身份
event_batcher_inbox_test.go
replay.go                   离线 replay，保持独立 bucket
replay_test.go
```

### CloudNode、部署和文档

```text
modules/cloudnode/internal/jobqueue/jetstream_queue.go
modules/cloudnode/internal/jobqueue/queue.go
modules/cloudnode/internal/jobqueue/naming.go
modules/cloudnode/internal/config/config.go
modules/cloudnode/config/app.yaml
modules/cloudnode/internal/testfixture/runtime.go
modules/cloudnode/internal/bootstrap/bootstrap.go

scripts/deploy-moox.sh
scripts/test-deploy-moox-eventbus.sh

docs/2026-07-23-event-contract-refactor-plan.md
docs/superpowers/plans/2026-07-24-eventmessage-single-envelope-refactor.md
docs/协议设计.md
docs/架构总览.md
docs/存储层架构.md
docs/因子计算模块设计.md
docs/运维/MooX-EventBus运维.md
```

---

### Task 0: 固化计划、创建隔离工作树并记录基线

**Files:**
- Add: `docs/superpowers/plans/2026-07-25-event-system-local-simplification.md`

- [x] **Step 1: 核对远端精确基线**

Run:

```bash
git fetch origin
git rev-parse feature/mooyang
git ls-remote origin refs/heads/feature/mooyang
git status --short
```

Expected:

```text
local and remote SHA: 5775dd6fc9530b9b5e0fcf39de1e76cd6708bd10
worktree changes: none
```

- [x] **Step 2: 在用户确认后提交计划文档**

Run:

```bash
git add docs/superpowers/plans/2026-07-25-event-system-local-simplification.md
git commit -m "docs(events): plan local event system simplification"
PLAN_SHA=$(git rev-parse HEAD)
git merge-base --is-ancestor \
  5775dd6fc9530b9b5e0fcf39de1e76cd6708bd10 \
  "${PLAN_SHA}"
```

Expected: 计划提交成功，且代码基线 `5775dd6f` 是计划提交的祖先。

- [x] **Step 3: 创建实施工作树**

Run:

```bash
git worktree add .worktrees/event-system-local-simplification \
  -b refactor/event-system-local-simplification \
  "${PLAN_SHA}"
cd .worktrees/event-system-local-simplification
```

Expected: `git status --short --branch` 显示新分支且工作树干净。

- [x] **Step 4: 运行基线证明集**

Run:

```bash
./scripts/verify-event-contracts.sh
(cd modules/factor && go test ./internal/trigger ./internal/store)
(cd modules/cloudnode && go test ./internal/jobqueue)
bash scripts/test-deploy-moox-eventbus.sh
```

Expected: 四组命令全部 PASS。基线失败时停止实施，先把失败归因到基线或环境，不把修复混入本计划。

---

### Task 1: 把过期计划显式标记为历史

**Files:**
- Modify: `docs/2026-07-23-event-contract-refactor-plan.md:1`
- Modify: `docs/superpowers/plans/2026-07-24-eventmessage-single-envelope-refactor.md:1`
- Modify: `docs/架构总览.md:90`
- Modify: `scripts/test-docs-architecture.sh:13`

- [x] **Step 1: 写文档状态检查并确认当前失败**

Run:

```bash
head -n 8 docs/2026-07-23-event-contract-refactor-plan.md
head -n 8 docs/superpowers/plans/2026-07-24-eventmessage-single-envelope-refactor.md
```

Expected: 两份文档顶部都没有“历史计划，不是当前事实源”的标记。

- [x] **Step 2: 给 2026-07-23 计划增加固定状态块**

在一级标题后加入：

```markdown
> **状态：历史计划，禁止作为当前实现依据。**
> 本文保留 2026-07-23 当时的设计与审查记录，其中的 Tick/Streamcalc、
> YAML Registry、旧事件词表和 Consumer/DLQ 描述已经过期。当前运行契约以
> [协议设计](协议设计.md)、[架构总览](架构总览.md)和
> [Event System CR Remediation](superpowers/plans/2026-07-24-event-system-cr-remediation.md)
> 为准。
```

- [x] **Step 3: 给 single-envelope 计划增加固定状态块**

在一级标题后加入：

```markdown
> **状态：历史执行记录，禁止把“当前执行状态”当作当前架构。**
> 本文记录重构过程中的阶段性目标；其中的 Market Tick/Streamcalc、共享 DLQ、
> YAML Registry 和已删除事件不再存在。当前运行契约以
> [协议设计](../../协议设计.md)、[架构总览](../../架构总览.md)和
> [Event System CR Remediation](2026-07-24-event-system-cr-remediation.md)
> 为准。
```

- [x] **Step 4: 验证链接和状态块**

Run:

```bash
sed -n '1,14p' docs/2026-07-23-event-contract-refactor-plan.md
sed -n '1,14p' docs/superpowers/plans/2026-07-24-eventmessage-single-envelope-refactor.md
bash scripts/test-docs-architecture.sh
```

Expected: 两份文档首屏都显示历史状态，文档架构测试 PASS。

执行时发现 `go.work` 已有 44 个 module，而当前架构清单缺少
`packages/cloudjobpb`、`packages/storagepb`、`packages/tradeeventpb`，文档门禁仍固定为
41。已同步清单并把门禁计数改为 44；这是恢复既有文档契约所必需的基线修正。

- [x] **Step 5: 提交文档状态**

```bash
git add docs/2026-07-23-event-contract-refactor-plan.md \
  docs/superpowers/plans/2026-07-24-eventmessage-single-envelope-refactor.md
git commit -m "docs(events): mark transitional plans as historical"
```

---

### Task 2: 在事件契约层增加精确 Subject Consumer

**Files:**
- Modify: `packages/events/consumer.go`
- Create: `packages/events/consumer_test.go`

- [x] **Step 1: 为 exact Subject filter 写失败测试**

新增测试，直接验证 filter 由 Registry identity 渲染，而不是接受裸 Subject：

```go
func TestSubjectConsumerFilterUsesRegistryIdentity(t *testing.T) {
	registry, err := DefaultRegistry()
	if err != nil {
		t.Fatal(err)
	}
	cfg := SubjectConsumerConfig{
		ConsumerConfig: ConsumerConfig{
			Name:  "cloudnode-route",
			Event: CloudJobExecutionRequested,
		},
		SpaceID:   "crypto",
		SubjectID: "collector.pkg/collect.kline",
	}
	got, err := subjectConsumerFilter(registry, cfg)
	if err != nil {
		t.Fatal(err)
	}
	want, err := registry.RenderSubject(
		CloudJobExecutionRequested,
		"crypto",
		"collector.pkg/collect.kline",
	)
	if err != nil {
		t.Fatal(err)
	}
	if got != want || strings.Contains(got, ">") {
		t.Fatalf("filter = %q, want exact %q", got, want)
	}
}
```

同时增加：

```go
func TestSubjectConsumerFilterSeparatesRoutes(t *testing.T)
func TestSubjectConsumerFilterRejectsEmptySpaceID(t *testing.T)
func TestSubjectConsumerFilterRejectsEmptySubjectID(t *testing.T)
func TestSubjectConsumerFilterRejectsUnregisteredEvent(t *testing.T)
```

空 identity 必须返回包含 `space_id` 或 `subject_id` 的错误；不同 route 必须得到不同 Subject；未注册 Event 必须被 Registry 拒绝。

- [x] **Step 2: 运行测试并确认失败**

Run:

```bash
(cd packages/events && go test ./... -run 'TestSubjectConsumerFilter')
```

Expected: FAIL，原因是 `SubjectConsumerConfig` 和 `subjectConsumerFilter` 尚不存在。

- [x] **Step 3: 增加显式配置类型和构造函数**

在 `packages/events/consumer.go` 增加：

```go
type SubjectConsumerConfig struct {
	ConsumerConfig
	SpaceID   string
	SubjectID string
}

func NewSubjectConsumer(
	ctx context.Context,
	client *jetstream.Client,
	registry *Registry,
	cfg SubjectConsumerConfig,
) (*Consumer, error) {
	filter, err := subjectConsumerFilter(registry, cfg)
	if err != nil {
		return nil, err
	}
	return newConsumer(ctx, client, registry, cfg.ConsumerConfig, filter)
}

func subjectConsumerFilter(registry *Registry, cfg SubjectConsumerConfig) (string, error) {
	if strings.TrimSpace(cfg.SpaceID) == "" {
		return "", fmt.Errorf("event subject consumer space_id is required")
	}
	if strings.TrimSpace(cfg.SubjectID) == "" {
		return "", fmt.Errorf("event subject consumer subject_id is required")
	}
	if err := registry.Validate(); err != nil {
		return "", err
	}
	return registry.RenderSubject(cfg.Event, cfg.SpaceID, cfg.SubjectID)
}
```

把现有 `NewConsumer` 收敛为 family filter 加共享构造函数：

```go
func NewConsumer(
	ctx context.Context,
	client *jetstream.Client,
	registry *Registry,
	cfg ConsumerConfig,
) (*Consumer, error) {
	if err := registry.Validate(); err != nil {
		return nil, err
	}
	filter, err := registry.FamilyPattern(cfg.Event)
	if err != nil {
		return nil, err
	}
	return newConsumer(ctx, client, registry, cfg, filter)
}

func newConsumer(
	ctx context.Context,
	client *jetstream.Client,
	registry *Registry,
	cfg ConsumerConfig,
	filter string,
) (*Consumer, error) {
	if client == nil {
		return nil, fmt.Errorf("event consumer client is nil")
	}
	consumer, err := client.NewConsumer(ctx, jetstream.ConsumerConfig{
		Stream: cfg.Event.Stream(), Durable: cfg.Name, FilterSubject: filter,
		AckWait: cfg.AckWait, MaxDeliver: cfg.MaxDeliver,
		MaxAckPending: cfg.MaxAckPending, FetchMaxWait: cfg.FetchMaxWait,
		DeliverPolicy: cfg.DeliverPolicy,
		DeliverDecodeErrors: cfg.DeliverDecodeErrors,
	})
	if err != nil {
		return nil, err
	}
	return &Consumer{consumer: consumer, registry: registry}, nil
}
```

Import `strings`。不增加 `FilterSubject string` 配置，不允许业务模块绕过 Registry。

- [x] **Step 4: 验证 family 与 exact 两种语义**

Run:

```bash
(cd packages/events && gofmt -w consumer.go consumer_test.go)
(cd packages/events && go test ./...)
```

Expected: PASS；既有 `NewConsumer` 行为不变，新增 exact filter 测试通过。

- [x] **Step 5: 提交契约层 API**

```bash
git add packages/events/consumer.go packages/events/consumer_test.go
git commit -m "feat(events): add registry-owned subject consumer"
```

---

### Task 3: CloudNode 改用 `NewSubjectConsumer`

**Files:**
- Modify: `modules/cloudnode/internal/jobqueue/jetstream_queue.go`
- Modify: `modules/cloudnode/internal/jobqueue/jetstream_queue_test.go`
- Modify: `modules/cloudnode/internal/jobqueue/queue.go`
- Modify: `modules/cloudnode/internal/jobqueue/naming.go`
- Modify: `modules/cloudnode/internal/jobqueue/naming_test.go`
- Modify: `modules/cloudnode/internal/config/config.go`
- Modify: `modules/cloudnode/config/app.yaml`
- Modify: `modules/cloudnode/internal/bootstrap/bootstrap.go`
- Modify: `modules/cloudnode/internal/testfixture/runtime.go`
- Modify: `modules/cloudnode/internal/rpc/job_item_test.go`
- Modify: `scripts/verify-event-contracts.sh`

- [x] **Step 1: 把 route config 测试改成事件层配置**

`consumerConfigForRoute` 的目标返回值改成：

```go
events.SubjectConsumerConfig{
	ConsumerConfig: events.ConsumerConfig{
		Name:          ConsumerName(spaceID, codePackageID, jobType),
		Event:         events.CloudJobExecutionRequested,
		AckWait:       cfg.AckWait,
		MaxDeliver:    cfg.MaxDeliver,
		MaxAckPending: cfg.DefaultMaxBatch,
		FetchMaxWait:  cfg.FetchMaxWait,
	},
	SpaceID:   spaceID,
	SubjectID: codePackageID + "/" + jobType,
}
```

测试断言：

```go
require.Equal(t, events.CloudJobExecutionRequested, got.Event)
require.Equal(t, "crypto", got.SpaceID)
require.Equal(t, "moox-collector_v202607142250/collect.kline", got.SubjectID)
require.Equal(t, ConsumerName(
	"crypto",
	"moox-collector_v202607142250",
	"collect.kline",
), got.Name)
```

- [x] **Step 2: 增加架构门禁并确认失败**

在 `scripts/verify-event-contracts.sh` 增加：

```bash
reject '\.NewConsumer\(' \
  "CloudNode bypasses the Registry-owned event Consumer API" \
  --glob '*.go' --glob '!**/*_test.go' modules/cloudnode
```

Run:

```bash
./scripts/verify-event-contracts.sh
```

Expected: FAIL，并定位到 `modules/cloudnode/internal/jobqueue/jetstream_queue.go` 的底层 `q.client.NewConsumer`。

- [x] **Step 3: 让 Queue 持有 Registry 和事件 Consumer**

`JetStreamQueue` 改为：

```go
type JetStreamQueue struct {
	rt         *Runtime
	client     *jetstream.Client
	registry   *events.Registry
	publisher  *events.Publisher
	cfg        QueueConfig
	mu         sync.Mutex
	inflight   map[string]*jetstream.Delivery
	consumers  map[string]*events.Consumer
	fetchLock  map[string]*sync.Mutex
	fetchStart map[string]uint64
}
```

`NewJetStreamQueue` 只读取一次默认 Registry，并用同一实例创建 Publisher 和 Consumer：

```go
registry, registryErr := events.DefaultRegistry()
var publisher *events.Publisher
if client != nil && registryErr == nil {
	publisher, _ = events.NewPublisher(client, registry)
}
```

初始化 `consumers: make(map[string]*events.Consumer)`。

- [x] **Step 4: 用精确 Subject API 创建 route Consumer**

`ensureConsumer` 改成：

```go
func (q *JetStreamQueue) ensureConsumer(
	spaceID, codePackageID, jobType string,
) (*events.Consumer, error) {
	key := routeConsumerKey(spaceID, codePackageID, jobType)
	if consumer := q.consumers[key]; consumer != nil {
		return consumer, nil
	}
	if q.client == nil || q.registry == nil {
		return nil, errors.New("cloudnode event consumer is unavailable")
	}
	consumer, err := events.NewSubjectConsumer(
		trpc.BackgroundContext(),
		q.client,
		q.registry,
		consumerConfigForRoute(q.cfg, spaceID, codePackageID, jobType),
	)
	if err != nil {
		return nil, err
	}
	q.consumers[key] = consumer
	return consumer, nil
}
```

`Fetch` 继续调用 `consumer.Fetch`，因此 Delivery、ACK、NAK、TERM 和 inflight token 行为不变。

- [x] **Step 5: 删除 CloudNode 重复的业务拓扑配置**

从 `QueueConfig` 删除：

```go
Naming     NamingConfig
ExecStream string
```

从 CloudNode `JetStreamConfig` 和 `config/app.yaml` 删除：

```yaml
subject_prefix: moox.cloudnode
exec_stream: MOOX_CLOUDNODE_EXEC
```

删除以下仅用于重复派生业务拓扑的符号：

```go
DefaultSubjectPrefix
DefaultExecStream
NamingConfig
ValidateNamingConfig
SubjectToken
ExecFilterSubject
ExecStreamSubject
subjectPrefix
qExecStream
```

`naming.go` 最终只保留稳定的 `ConsumerName` 和所需 imports。Bootstrap 日志从可配置 stream 改为：

```go
log.InfoContextf(
	ctx,
	"cloudnode JetStream 已启用: event=%s active_kv=%s nats_url=%s",
	events.CloudJobExecutionRequested.Name(),
	cfg.JobItem.ActiveKVBucket,
	cfg.JetStream.NATSURL,
)
```

测试 fixture 创建 Stream 时使用：

```go
registry, err := events.DefaultRegistry()
if err != nil {
	return nil, err
}
family, err := registry.FamilyPattern(events.CloudJobExecutionRequested)
if err != nil {
	return nil, err
}
_, err = js.AddStream(&nats.StreamConfig{
	Name:     events.CloudJobExecutionRequested.Stream(),
	Subjects: []string{family},
	Storage:  nats.FileStorage,
	Retention: nats.WorkQueuePolicy,
})
```

- [x] **Step 6: 更新 CloudNode 测试中的 Subject 期望**

测试需要精确 Subject 时统一调用：

```go
func mustCloudJobSubject(
	t *testing.T,
	spaceID, codePackageID, jobType string,
) string {
	t.Helper()
	registry, err := events.DefaultRegistry()
	require.NoError(t, err)
	subject, err := registry.RenderSubject(
		events.CloudJobExecutionRequested,
		spaceID,
		codePackageID+"/"+jobType,
	)
	require.NoError(t, err)
	return subject
}
```

不要在生产包重新增加一个返回空字符串的 subject helper。

- [x] **Step 7: 运行 CloudNode、契约和 race 测试**

Run:

```bash
(cd modules/cloudnode && gofmt -w internal/jobqueue internal/config internal/bootstrap internal/testfixture internal/rpc)
(cd modules/cloudnode && go test ./internal/jobqueue ./internal/rpc ./internal/bootstrap)
(cd modules/cloudnode && go test -race ./internal/jobqueue)
./scripts/verify-event-contracts.sh
```

Expected: 全部 PASS；生产 CloudNode 不再直接调用底层 `NewConsumer`，route 隔离测试仍证明不同 jobType 使用不同精确 Subject。

- [x] **Step 8: 提交 CloudNode 迁移**

```bash
git add packages/events \
  modules/cloudnode/internal/jobqueue \
  modules/cloudnode/internal/config/config.go \
  modules/cloudnode/config/app.yaml \
  modules/cloudnode/internal/bootstrap/bootstrap.go \
  modules/cloudnode/internal/testfixture/runtime.go \
  modules/cloudnode/internal/rpc/job_item_test.go \
  scripts/verify-event-contracts.sh
git commit -m "refactor(cloudnode): use exact event subject consumer"
```

---

### Task 4: 行为保持地拆分 Storage View Consumer

**Files:**
- Modify: `modules/storage/internal/service/view/consume.go`
- Modify: `modules/storage/internal/service/view/consume_test.go`
- Create: `modules/storage/internal/service/view/delivery_policy.go`
- Create: `modules/storage/internal/service/view/delivery_policy_test.go`
- Create: `modules/storage/internal/service/view/event_apply.go`
- Create: `modules/storage/internal/service/view/subject_dispatcher.go`
- Create: `modules/storage/internal/service/view/subject_dispatcher_test.go`
- Create: `modules/storage/internal/service/view/delivery_heartbeat.go`
- Create: `modules/storage/internal/service/view/live_gate.go`
- Create: `modules/storage/internal/service/view/live_gate_test.go`

- [x] **Step 1: 先冻结五项并发和可靠性契约**

把现有测试移动到目标测试文件；把测试名中的 `Lane` 改成 `SubjectDispatcher`，断言和测试体不改：

```text
delivery_policy_test.go
  TestProcessDeliveryUsesClientRetryCountWhenDeliveryCountDoesNotChange
  TestProcessDeliveryTermsAfterRetryExhaustion

subject_dispatcher_test.go
  TestDeliveryHeartbeatIntervalUsesAckWait
  TestSubjectDispatcherDifferentSubjectsRunInParallel
  TestSubjectDispatcherPreservesSubjectOrder
  TestSubjectDispatcherRetryKeepsSubjectBlockedButOtherSubjectRuns
  TestSubjectDispatcherStartsHeartbeatWhenDeliveryIsQueuedAndStopsItOnClose

live_gate_test.go
  TestBackfillWriterWaitsForLiveAndBlocksNewLive
```

`consume_test.go` 只保留：

```text
TestEventConsumerOptionsDefaults
TestEventConsumerOptionsRejectsNegativeRetryAttempts
```

- [x] **Step 2: 只移动测试并验证仍通过**

Run:

```bash
(cd modules/storage && gofmt -w internal/service/view/*_test.go)
(cd modules/storage && CGO_ENABLED=1 go test ./internal/service/view)
```

Expected: PASS。此时生产 `consume.go` 尚未拆分。

- [x] **Step 3: 移动 delivery policy**

把以下符号原样移动到 `delivery_policy.go`：

```text
processDelivery
firstHeartbeat
processDeliveryWithPolicy
processDeliveryWithApply
deliveryActions
processDeliveryWithApplyAndActions
sleepDeliveryRetry
permanentDeliveryError
isPermanentDeliveryError
```

不得改变：

- Active apply 成功后 ACK 失败不能再次调用 apply。
- 临时错误在客户端循环重试并持续 `InProgress`。
- 重试耗尽和永久错误执行 TERM。
- ACK/TERM/InProgress 错误仍进入现有 reporter/metrics。

- [x] **Step 4: 移动事件应用逻辑**

把以下符号原样移动到 `event_apply.go`：

```text
applyDelivery
applyDatasetEvent
applyEventToIndex
hasMissingRows
recoverMissingRows
withDataset
appendMatchingField
eventWrites
viewColumnSource
viewColumnDataset
```

不得修改 protobuf 解码、Space/Dataset identity、Active/New 写入、缺行恢复或 checkpoint 行为。

- [x] **Step 5: 移动 Subject 调度、heartbeat 和 live gate**

把原来的 `lane` 命名改为“Subject 队列/调度器”，避免把它误解为 NATS 自带分区：

```text
subject_dispatcher.go
  laneHandler                  -> subjectDeliveryHandler
  laneMetricsHooks             -> subjectDispatcherMetricsHooks
  subjectLane                  -> subjectQueue
  laneDelivery                 -> queuedDelivery
  subjectLaneDispatcher        -> subjectDispatcher
  newSubjectLaneDispatcher     -> newSubjectDispatcher
  Dispatch/worker/next/finish/enqueue/Close

delivery_heartbeat.go
  deliveryHeartbeat
  newDeliveryHeartbeat
  deliveryHeartbeatInterval
  report/err/stop

live_gate.go
  Service.initLiveGate
  Service.acquireBackfill
  Service.acquireLiveDelivery
  Service.releaseLiveDelivery
  Service.releaseBackfill
  Service.releaseLiveGate
  liveLeaseGate
  newLiveLeaseGate
  acquireRead/releaseRead/acquireWrite/releaseWrite/signalLocked
```

最终 `consume.go` 只保留 `EventConsumerOptions.withDefaults` 和 `StartEventConsumer`。

- [x] **Step 6: 验证没有行为变化和竞态**

Run:

```bash
(cd modules/storage && gofmt -w internal/service/view)
(cd modules/storage && CGO_ENABLED=1 go test ./internal/service/view)
(cd modules/storage && CGO_ENABLED=1 go test -race ./internal/service/view)
./scripts/verify-event-contracts.sh
```

Expected: 全部 PASS；五项冻结契约不变；`consume.go` 不再包含事件应用、Subject 调度、heartbeat 或 gate 的类型实现。

- [x] **Step 7: 提交纯结构拆分**

```bash
git add modules/storage/internal/service/view
git commit -m "refactor(storage): split view consumer responsibilities"
```

该提交不得混入 Runner API 或配置默认值变化。

---

### Task 5: 删除 Factor 未使用的 late-data 状态

**Files:**
- Modify: `modules/factor/internal/trigger/event_batcher.go`
- Modify: `modules/factor/internal/trigger/event_batcher_test.go`
- Modify: `modules/factor/internal/trigger/replay_test.go`
- Create: `modules/factor/internal/trigger/event_batcher_inbox.go`
- Create: `modules/factor/internal/trigger/event_batcher_inbox_test.go`

- [x] **Step 1: 增加完整 key 和固定窗口回归测试**

保留并强化以下断言：

```go
func TestEventBatcherKeepsCompleteScopeSeparate(t *testing.T)
```

测试构造相同 `subject/freq`、不同 `space/source/target` 的 binding/event，断言分别生成任务，且每个任务携带正确：

```text
SpaceID
SourceDataset
TargetDataset
SubjectID
Freq
FactorIDs
```

保留：

```text
TestEventBatcherUsesFixedWindowFromFirstEvent
TestEventBatcherSplitsTasksByTargetDataset
TestDurableEventBatcherClaimsDuplicateMessageOnlyOnce
TestDurableEventBatcherPersistsAndReplaysBeforeFlush
TestDurableEventBatcherRestoresPendingWhenCommitFails
TestDurableEventBatcherSkipsProcessedRedelivery
```

- [x] **Step 2: 把 late-data 测试改成普通历史 bar 调度测试**

删除 `TestEventBatcherMarksDataOlderThanClosedWindowAsLateRecompute`，替换为：

```go
func TestEventBatcherSchedulesOlderBarWithoutExtraLateState(t *testing.T) {
	start := time.Date(2026, 7, 6, 9, 15, 0, 0, time.UTC)
	batcher := NewEventBatcher(
		time.Second,
		[]domain.FactorBinding{
			binding("bias", "binance_spot_kline", domain.SubjectModeAll, "[]"),
		},
	)
	batcher.Ingest(
		event("crypto", "binance_spot_kline", "BTC-USDT", "1m", start),
		start,
	)
	if tasks := batcher.Flush(start.Add(time.Second)); len(tasks) != 1 {
		t.Fatalf("first tasks=%+v", tasks)
	}
	older := start.Add(-time.Minute)
	batcher.Ingest(
		event("crypto", "binance_spot_kline", "BTC-USDT", "1m", older),
		start.Add(2*time.Second),
	)
	tasks := batcher.Flush(start.Add(3 * time.Second))
	if len(tasks) != 1 || !tasks[0].BarTime.Equal(older) {
		t.Fatalf("older-bar tasks=%+v", tasks)
	}
}
```

该测试确认迟到修订仍触发幂等重算，但不再维护第二套 closed-window 分类。

- [x] **Step 3: 运行新测试并确认旧类型仍存在**

Run:

```bash
(cd modules/factor && go test ./internal/trigger)
rg -n 'LateData|LateDataPolicy|closedBucket|MinDataTime|MaxDataTime' \
  modules/factor/internal/trigger
```

Expected: 测试 PASS，但 `rg` 仍命中待删除实现。

- [x] **Step 4: 删除没有进入 scheduler/executor 的字段和状态**

从 `Task` 删除：

```go
MinDataTime    time.Time
MaxDataTime    time.Time
LateDataPolicy string
LateData       bool
```

删除：

```go
LateDataPolicyRecompute
closedBucketRetention
maxClosedBuckets
EventBatcher.closed
closedBucket
pruneClosedLocked
```

从 ingest/flush 删除：

```text
closed bucket lookup
late bool
MinDataTime/MaxDataTime 初始化和更新
FlushPending 写 closed map
```

保留：

```text
BarTime = bucket 内最大 data_time
deadline = 第一条匹配事件 processingAt + window
完整 bucketKey
FactorIDs 去重和稳定排序
PendingEventIDs 去重和稳定排序
```

- [x] **Step 5: 调整 replay 测试**

`TestReplayRangeCarriesExplicitIdentityAndTimeSemantics` 继续断言：

```text
TriggerType == replay
FactorVersion
TargetRunID
BarTime
FirstReceivedAt
LastReceivedAt
```

删除 `MinDataTime`、`MaxDataTime` 和 `LateDataPolicy` 断言。Replay 仍使用独立 batcher，不得 flush live bucket。

- [x] **Step 6: 把 Inbox 持久化方法移动到独立文件**

移动到 `event_batcher_inbox.go`：

```text
IngestMessage
FlushPending
CommitPending
RestorePending
Replay
orderedMessageIDs
```

`Flush` 仍留在 `event_batcher.go`，调用 `FlushPending`。把 Inbox 测试移动到 `event_batcher_inbox_test.go`，测试体不改。

- [x] **Step 7: 验证 Factor 行为和竞态**

Run:

```bash
(cd modules/factor && gofmt -w internal/trigger)
(cd modules/factor && go test ./internal/trigger ./internal/bootstrap ./internal/scheduler)
(cd modules/factor && go test -race ./internal/trigger)
rg -n 'LateData|LateDataPolicy|closedBucket|MinDataTime|MaxDataTime' \
  modules/factor/internal/trigger
```

Expected: 测试和 race PASS；最后一个 `rg` 无输出。

- [x] **Step 8: 提交 Factor 收敛**

```bash
git add modules/factor/internal/trigger
git commit -m "refactor(factor): simplify fixed-window event batching"
```

---

### Task 6: 单机默认部署跳过 EventBus TLS 凭据生成

**Files:**
- Modify: `scripts/deploy-moox.sh`
- Modify: `scripts/test-deploy-moox-eventbus.sh`
- Modify: `docs/运维/MooX-EventBus运维.md`

- [x] **Step 1: 写静态部署契约检查并确认失败**

在 `scripts/test-deploy-moox-eventbus.sh` 增加：

```bash
grep -Fq \
  '[[ "${WITH_EVENTBUS}" == "1" && "${MOOX_EVENTBUS_ENABLE_TLS:-0}" == "1" && -x "${ROOT}/bin/moox-admin-cli" ]]' \
  "${ROOT}/scripts/deploy-moox.sh"
grep -Fq 'missing EventBus TLS credential' "${ROOT}/scripts/deploy-moox.sh"
```

Run:

```bash
bash scripts/test-deploy-moox-eventbus.sh
```

Expected: FAIL，因为当前 `start_admin` 仅按 `WITH_EVENTBUS` 判断。

- [x] **Step 2: 只在 TLS profile 下 provision/export**

把生成脚本中 `start_admin` 的条件改为单行条件，供部署契约测试精确检查：

```bash
if [[ "${WITH_EVENTBUS}" == "1" && "${MOOX_EVENTBUS_ENABLE_TLS:-0}" == "1" && -x "${ROOT}/bin/moox-admin-cli" ]]; then
  mkdir -p "${HOME}/.config/moox/eventbus"
  "${ROOT}/bin/moox-admin-cli" eventbus-credentials ensure \
    --db-path "${ROOT}/data/admin.db" \
    --encryption-key-file "${encryption_key_file}" \
    --public-ip "${MOOX_EVENTBUS_PUBLIC_IP:-}" \
    >>"${ROOT}/logs/admin/stdout.log" 2>&1 ||
    { echo "EventBus credential provisioning failed" >&2; exit 1; }
  "${ROOT}/bin/moox-admin-cli" eventbus-credentials export \
    --db-path "${ROOT}/data/admin.db" \
    --encryption-key-file "${encryption_key_file}" \
    --public-ip "${MOOX_EVENTBUS_PUBLIC_IP:-}" \
    --output-dir "${HOME}/.config/moox/eventbus" \
    >>"${ROOT}/logs/admin/stdout.log" 2>&1 ||
    { echo "EventBus credential export failed" >&2; exit 1; }
fi
```

不修改 `eventBusRoles`、`eventBusKeys`、`usersYAML`、CA 生成、rotate 或远程配置校验。

- [x] **Step 3: TLS profile 缺少文件时 fail closed**

把 `start_eventbus` 中当前的：

```bash
if [[ "${MOOX_EVENTBUS_ENABLE_TLS:-0}" == "1" && -f "${credential_dir}/users.yaml" ]]; then
```

改为先检查全部必需文件：

```bash
if [[ "${MOOX_EVENTBUS_ENABLE_TLS:-0}" == "1" ]]; then
  for required in \
    users.yaml internal-admin.yaml ca.pem server.pem server-key.pem; do
    if [[ ! -r "${credential_dir}/${required}" ]]; then
      echo "missing EventBus TLS credential: ${credential_dir}/${required}" >&2
      exit 1
    fi
  done
  perl -0pi -e 's#enabled:\s*false\n    username:#enabled: true\n    username:#; s#users_file:\s*""#users_file: "'"${credential_dir}"'/users.yaml"#; s#enabled:\s*false\n    cert_file:#enabled: true\n    cert_file:#; s#cert_file:\s*""#cert_file: "'"${credential_dir}"'/server.pem"#; s#key_file:\s*""#key_file: "'"${credential_dir}"'/server-key.pem"#; s#ca_file:\s*""#ca_file: "'"${credential_dir}"'/ca.pem"#' \
    "${ROOT}/eventbus/config/app.yaml"
  perl -0pi -e 's#credential_file:\s*""#credential_file: "'"${credential_dir}"'/internal-admin.yaml"#; s#tls_ca_file:\s*""#tls_ca_file: "'"${credential_dir}"'/ca.pem"#' \
    "${ROOT}/eventbus/config/app.yaml"
fi
```

两个 `perl` 命令保留现有完整替换表达式，不修改生成配置的 auth/TLS 字段。这样显式执行 `./start.sh eventbus` 时不会因为凭据尚未生成而静默降级到明文。

- [x] **Step 4: 更新运维边界**

在 `docs/运维/MooX-EventBus运维.md` 的“容量与安全”写清：

```text
默认单机 loopback profile 使用 nats://127.0.0.1，不启用 Broker auth/TLS，
也不生成 EventBus role 文件。设置 MOOX_EVENTBUS_ENABLE_TLS=1 时，部署流程才
ensure/export 十一类最小权限凭据和私有 CA。非 loopback NATS URL 仍必须使用
TLS、CA 和认证；不得通过合并角色绕过 ACL。
```

- [x] **Step 5: 运行部署和 Admin 凭据测试**

Run:

```bash
bash -n scripts/deploy-moox.sh
bash scripts/test-deploy-moox-eventbus.sh
bash scripts/test-deploy-moox-storage-profile.sh
(cd modules/admin && go test ./cmd/cli -run EventBus)
```

Expected: 全部 PASS；TLS storage profile 仍写入 credential file；Admin 测试仍验证十一类角色和 ACL。

- [x] **Step 6: 提交部署收敛**

```bash
git add scripts/deploy-moox.sh \
  scripts/test-deploy-moox-eventbus.sh \
  docs/运维/MooX-EventBus运维.md
git commit -m "refactor(deploy): provision eventbus credentials only for tls"
```

---

### Task 7: 同步当前架构文档

**Files:**
- Modify: `docs/协议设计.md`
- Modify: `docs/架构总览.md`
- Modify: `docs/存储层架构.md`
- Modify: `docs/因子计算模块设计.md`

- [ ] **Step 1: 更新 Consumer API 说明**

`docs/协议设计.md` 和 `docs/架构总览.md` 明确：

```text
NewConsumer：按 Event family 创建业务方自有 Consumer。
NewSubjectConsumer：按 Event + space_id + subject_id，通过 Registry 渲染精确
Subject；用于 CloudNode route Consumer。
业务模块不得把裸 FilterSubject 传入 packages/events。
```

- [ ] **Step 2: 记录 View 没有直接迁 Runner 的原因**

`docs/存储层架构.md` 的 View 部分增加：

```text
View 的 Subject dispatcher 是 ViewBuilder 的有界执行策略，不是第二套 EventBus
拓扑所有者。它通过每个 Subject 的有序队列保留同 Dataset 顺序、跨 Dataset 并行、queued heartbeat 和
Backfill 写优先 gate。通用 Runner 当前为全局串行，不能在不改变这些语义的
情况下直接替换该策略。
```

- [ ] **Step 3: 把 Factor “去抖”改成准确的固定窗口合并**

把文档 key 改为：

```text
bucket[(space_id, source_dataset, target_dataset, subject_id, freq)]
```

把算法改为：

```text
第一条匹配事件建立 deadline = processing_time + event_batch_window；
后续同 bucket 事件只更新 BarTime=max(data_time)、FactorIDs 和 PendingEventIDs，
不延后 deadline。窗口到期后生成一个 scheduler Task。
```

删除 `bucket.max_data_time` 之外的 late/closed-window 分类描述。明确迟到修订仍按其 `BarTime` 生成普通幂等任务，对账和 `writeback_bars` 继续负责业务修正。

- [ ] **Step 4: 验证当前文档没有错误入口**

Run:

```bash
rg -n 'NewSubjectConsumer|NewConsumer|固定窗口|target_dataset|queued heartbeat' \
  docs/协议设计.md docs/架构总览.md docs/存储层架构.md docs/因子计算模块设计.md
rg -n 'bucket\\[\\(space, dataset, subject, freq\\)\\]|LateDataPolicy|closedBucket' \
  docs/协议设计.md docs/架构总览.md docs/存储层架构.md docs/因子计算模块设计.md
bash scripts/test-docs-architecture.sh
```

Expected: 第一条命令命中更新后的当前契约；第二条命令无输出；文档架构测试 PASS。

- [ ] **Step 5: 提交当前文档**

```bash
git add docs/协议设计.md docs/架构总览.md \
  docs/存储层架构.md docs/因子计算模块设计.md
git commit -m "docs(events): describe bounded consumer simplification"
```

---

### Task 8: 跨模块验收和独立复核

**Files:**
- Modify only the plan checkbox state after every command has current-run evidence.

- [ ] **Step 1: 格式和静态检查**

Run:

```bash
gofmt -w $(git diff --name-only \
  5775dd6fc9530b9b5e0fcf39de1e76cd6708bd10...HEAD \
  -- '*.go')
git diff --check
bash -n scripts/deploy-moox.sh scripts/verify-event-contracts.sh
```

Expected: 无输出或 PASS。

- [ ] **Step 2: 事件契约证明**

Run:

```bash
./scripts/verify-event-contracts.sh
(cd packages/events && go test -race ./...)
(cd packages/jetstream && go test -race ./...)
(cd modules/eventbus && go test -race ./...)
```

Expected: 全部 PASS。

- [ ] **Step 3: 变更模块证明**

Run:

```bash
(cd modules/cloudnode && go test ./...)
(cd modules/cloudnode && go test -race ./internal/jobqueue)
(cd modules/factor && go test ./...)
(cd modules/factor && go test -race ./internal/trigger)
(cd modules/storage && CGO_ENABLED=1 go test ./internal/service/view/...)
(cd modules/storage && CGO_ENABLED=1 go test -race ./internal/service/view)
(cd modules/admin && go test ./cmd/cli -run EventBus)
```

Expected: 全部 PASS；race 无报告。

- [ ] **Step 4: 部署和文档证明**

Run:

```bash
bash scripts/test-deploy-moox-eventbus.sh
bash scripts/test-deploy-moox-storage-profile.sh
bash scripts/test-deploy-moox-storage-view.sh
bash scripts/test-docs-architecture.sh
```

Expected: 全部 PASS。

- [ ] **Step 5: 工作区级证明**

Run:

```bash
./scripts/test-go-workspace.sh
make verify-pr
```

Expected: 全部 PASS。MooX 是多 module workspace，不用仓库根目录的裸 `go test ./...` 代替这两个入口。

- [ ] **Step 6: 审查范围和禁止项**

Run:

```bash
git diff \
  5775dd6fc9530b9b5e0fcf39de1e76cd6708bd10...HEAD \
  --stat
rg -n 'NewJetstream|NewJetStream\\(' packages/events modules/cloudnode
rg -n '\\.NewConsumer\\(' modules/cloudnode --glob '*.go' --glob '!**/*_test.go'
rg -n 'LateData|LateDataPolicy|closedBucket|MinDataTime|MaxDataTime' \
  modules/factor/internal/trigger
rg -n 'subject_prefix|exec_stream' modules/cloudnode
```

Expected:

```text
NewJetstream/NewJetStream: no matches
CloudNode low-level .NewConsumer: no matches
Factor removed late-data state: no matches
CloudNode duplicate topology config: no matches
```

人工复核 diff，确认没有：

- 新 Event、Stream、KV、DLQ 或 Saga 框架。
- Storage View retry/order/heartbeat/gate 行为修改。
- Factor bucket key 降维或滑动 debounce。
- EventBus ACL 角色合并或远程 TLS 放宽。
- 与本计划无关的格式化、生成代码或依赖升级。

- [ ] **Step 7: 最终提交**

仅当验收步骤产生必要的测试/文档修正时创建最终提交：

```bash
git add scripts docs packages/events modules/cloudnode modules/storage modules/factor
git commit -m "test(events): verify local simplification boundaries"
```

若 Step 1-6 后工作树已经干净，不创建空提交。

- [ ] **Step 8: 推送和远端证明**

Run:

```bash
git status --short
git push -u origin refactor/event-system-local-simplification
git rev-parse HEAD
git ls-remote origin refs/heads/refactor/event-system-local-simplification
```

Expected: 工作树干净，本地 HEAD 与远端 ref 完全一致。

---

## 3. 提交边界

计划提交顺序：

```text
1. docs(events): plan local event system simplification
2. docs(events): mark transitional plans as historical
3. feat(events): add registry-owned subject consumer
4. refactor(cloudnode): use exact event subject consumer
5. refactor(storage): split view consumer responsibilities
6. refactor(factor): simplify fixed-window event batching
7. refactor(deploy): provision eventbus credentials only for tls
8. docs(events): describe bounded consumer simplification
9. test(events): verify local simplification boundaries
```

每个提交必须独立编译和通过对应模块测试。Task 4 是纯结构提交；Task 5 是 Factor 局部行为收敛；两者不得合并，便于回滚和定位。

## 4. 完成标准

只有同时满足以下条件才可以宣告完成：

- 两份过期计划首屏明确标记为历史。
- `packages/events` 同时提供 family `NewConsumer` 和 exact `NewSubjectConsumer`。
- CloudNode 精确 route Consumer 只通过 Registry identity 创建，不再携带可配置业务 stream/subject prefix。
- Storage View 的五项顺序/heartbeat/backfill 契约测试通过，`consume.go` 已按职责拆分。
- Factor 保留完整 key、固定窗口和持久化 Inbox，late-data closed map 与未使用字段已删除。
- loopback 默认部署不 ensure/export EventBus 凭据；TLS profile 仍生成十一类最小权限凭据和私有 CA。
- `verify-event-contracts`、相关模块 race、部署脚本、workspace 测试和 `make verify-pr` 全部通过。
- 最终工作树干净；如已推送，则本地 HEAD 与远端 ref 一致。
