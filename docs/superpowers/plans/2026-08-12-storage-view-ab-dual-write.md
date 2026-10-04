# Storage View A/B 在线重建简化修复执行计划

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 修复 Storage View 历史重建阻塞实时写入、rows/marker 乱序、DuckDB 批量写语义分叉和失败构建反复重试问题，并用简单的 A 权威、B 延迟收敛模型完成平滑切换。

**Architecture:** Active A 在整个重建期间继续承担查询和实时写入；Inactive B 先挂载为 `next`，随后历史回填与实时双写并行。回填只补 B 中缺失的字段，实时写保持字段级 last-write-wins；回填完成后获取当前 View 的短时互斥锁，等待在途双写结束并原子切换到 B。B 发生任何写入或回填错误都直接废弃，保留 A 继续服务，稍后完整重建。

**Tech Stack:** Go、tRPC-Go、JetStream、DuckDB、SQLite Metadata、protobuf、Go race detector。

---

> 状态：核心实现与最新 codeCR 回归完成，已发布 Linux storage-view 并完成线上健康检查；本轮补充了显式零列投影的 Dataset 事件映射与物理 revision/schema hash fail-closed 校验。真实 A/B 长回填、切换后的完整数据校验和跨进程故障注入仍需继续验证。

> **当前实现门禁：** 启动恢复、激活竞态、旧文件删除、首启 ACK、B 写失败和文件超限重试的本地回归门禁已通过。Metadata 激活成功但响应异常时不会删除已被指向的 B；首次构建尚无 Active 时，未恢复完成的 View 不会 ACK 行事件；显式零列 View 仍会建立 Dataset 事件映射，不会因没有投影列而永久 pending。剩余未勾选项仅是需要真实部署/跨进程执行的集成验收，不代表本地实现尚未完成。

## 1. 已确认的设计边界

### 1.1 一致性目标

本计划保证的是“对外 Active View 正确”和“切换时 B 已收敛”，不是构建期间 A、B 每一时刻逐字节相同。

```text
Active A 持续读写
    -> 短暂锁住当前 View，挂载 B 为 next
    -> 释放锁
    -> 历史数据 A -> B 回填
    -> 新事件继续写 A，并同时写 B
    -> 回填完成
    -> 短暂锁住当前 View，等待在途双写结束
    -> Metadata CAS 激活 B，并切换 runtime.active
    -> 释放锁
    -> 固定 grace 后删除旧 A 文件
```

必须保持以下不变量：

1. 构建期间所有查询只读取 A，不读取半成品 B。
2. B 挂载前完成的事件已经进入 A，由历史回填带入 B。
3. B 挂载后的事件同时写 A、B。
4. 回填不能覆盖 B 中已经存在的非空实时值。
5. 同一 Dataset 的 rows、period marker、sync point 严格串行。
6. B 只要发生一次无法完成的写入，就禁止激活并废弃本轮 B。
7. 切换时必须排空当前 View 的在途双写，但不等待整个 durable consumer 清空。

不需要持久化 watermark 的依据是两个短临界区：挂载 B 时，`runtime.mu` 会等待挂载前已经开始的 A 写入完成；此后所有尚未开始的事件都会看到 `next=B` 并执行双写。切换时再次持有 `runtime.mu`，切换前已开始的双写全部完成，尚未开始的事件则在锁释放后只写新的 Active B。历史回填使用“只补缺失字段”语义，因此它与实时双写无论谁先获得 per-index gate，都不会覆盖已经写入 B 的非空实时值。

### 1.2 明确接受的简化

- 接受已经显式清空为 `NULL` 的因子值在回填时被历史值重新补回。
- 接受 B 在构建期间比 A 延迟，不要求提供 B 的中间查询能力。
- 接受进程重启后丢弃未激活的 B 并从头重建，不实现回填 cursor 恢复。
- 首次创建一个此前没有 Active 的新 View 时，不承诺枚举已经被 durable consumer
  ACK 的历史 Primary 行；首建从后续事件开始收敛。需要完整历史时，按当前个人量化
  部署约定删除并重建相关数据/视图，而不是把一个无法 range-scan 的 Primary 偷换成
  “已完整回填”的 B。
- 旧 A 使用固定 60 秒 grace 后删除，不增加查询 reader refcount。
- 失败的文件超限重建使用固定 30 分钟重试间隔，不实现 failure fingerprint 或指数退避状态机。
- Active/READY 物理索引恢复必须同时校验 Metadata 的 active revision/schema hash；同名
  但内容不一致的旧文件直接 fail-closed，不能先挂载再等实时写暴露冲突。
- `ReplaceViewColumns` 的空列集合是合法的显式零列投影，Metadata 必须持久化
  `moox.columns_explicit=true`，不能在重建时回退成 Primary 的默认全量列。
- 显式零列 View 仍属于其 `DatasetIds` 的事件消费者；虽然不写字段，rows、period
  marker 和 sync point 仍必须完成同 Dataset 队列的 ACK/顺序处理。

### 1.3 本次明确不做

- 不新增持久化 stream watermark。
- 不新增 `activation_fence_mode`、`backfill_write_mode` 等功能开关。
- 不增加复杂的 View repair CLI。
- 不增加新的 Metadata 表或通用任务调度系统。
- 正常运行路径不直接修改 `storage_metadata.db`；若发布前检查发现历史元数据快照与规范列不一致，必须先停相关服务、做带时间戳备份，再执行一次性离线修复并复核，不能在在线请求路径中静默改库。
- 不解决显式 `NULL` 值被回填复活的问题。
- 不用增加 DuckDB 内存、线程数掩盖顺序和锁问题。

> 运维边界：同一 Dataset 的永久 pending delivery 仍会占用 JetStream 的全局
> `max_ack_pending` 槽位；Dataset 队列键保证已拉取消息不越序，但不提供跨 broker
> consumer 的配额隔离。生产配置应让 `max_ack_pending` 明显大于 `fetch_batch` 和
> `max_workers`，并通过 poison-message 运维处理避免单一 Dataset 长期占满全局窗口。

### 1.4 术语约定

- **Dataset 队列键**：同一 Dataset 的事件路由到同一条串行队列的标识，格式为
  `space_id + "\\x00" + dataset_id`。它只用于保证 rows、周期 Marker 和 SyncPoint 的处理顺序，
  不表示独立的服务进程或线程。全文统一使用“Dataset 队列键”这个名称。
- **重建检查**：按固定周期检查 View 是否需要 A/B 重建（版本变化、覆盖范围变化或文件超过大小上限）。
  它不是通用的数据库维护任务，也不负责逐行删除事实数据。
- **超限重建重试间隔**：如果一次重建只是因为文件超过 `max_view_file_bytes` 失败，
  在下一次完整重建前等待 30 分钟。这个等待只抑制重复的全量重建尝试；Active View 仍继续读写，
  不会暂停实时数据处理。若 desired revision 发生变化，则立即允许重新构建。

## 2. 当前问题与修复对应关系

| 当前问题 | 根因 | 本计划修复 |
|---|---|---|
| A/B 回填期间 View 最新时间停滞 | `BackfillViewWithReader` 长时间持有 Service 级 `liveGate` writer | 删除全局回填锁，只在挂载和切换时短暂锁当前 View |
| B 回填与实时写互相覆盖 | 同一 index 没有可取消的写入串行化，普通 UPSERT 会覆盖字段 | 增加 per-index write gate，Backfill 只填空，LiveWrite 只更新请求中出现的字段 |
| rows 未写完 marker 已先执行 | dispatcher 以完整 NATS subject 选择串行队列 | Dataset 队列 key 改为 `space_id + dataset_id` |
| Primary 与 View 重复 RowKey 结果不同 | DuckDB 单条多行 UPSERT 对同 key 可能 first-write-wins | SQL 前折叠重复 RowKey，按字段输入顺序实现 last-write-wins |
| 切换等待所有 Dataset 的 backlog | `acquireActivationFence` 等全 durable `NumPending==0` | 只获取当前 View 的 `runtime.mu`，等待当前 View 在途写完成 |
| 构建失败后每分钟重建 | Active 文件仍超过大小限制，`shouldRetryFailedBuild` 总为 true | 使用现有 build `updated_at`，30 分钟后再尝试文件超限重建 |
| 正常 build CAS 被报成 `INNER_ERR` | `ErrViewIndexBuildConflict` 未映射 | 映射为 `CONFLICT`，reconciler 重新读取状态 |
| 启动时先进行长回填，consumer 尚未启动 | `StartReconciler` 在 EventBus consumer 前同步执行 | 先恢复 Active 映射，再启动 consumer，最后异步启动重建检查 |

## 3. 文件职责

### EventBus 顺序

- Modify: `modules/storage/internal/service/view/eventconsumer/subject_dispatcher.go`（按 Dataset 队列键路由，文件名沿用）
- Modify: `modules/storage/internal/service/view/eventconsumer/subject_dispatcher_test.go`
- Modify: `modules/storage/internal/service/view/eventconsumer/consumer.go`
- Modify: `modules/storage/internal/service/view/eventconsumer/consumer_test.go`
- Modify: `modules/storage/config/storage_view/trpc_go.yaml`

### DuckDB 写入语义

- Modify: `modules/storage/internal/service/viewindex/duckdb/index_manager.go`
- Modify: `modules/storage/internal/service/viewindex/duckdb/index_manager_test.go`
- Create: `modules/storage/internal/service/view/index_write_gate.go`
- Create: `modules/storage/internal/service/view/index_write_gate_test.go`

### A/B 生命周期

- Modify: `modules/storage/internal/service/view/build.go`
- Modify: `modules/storage/internal/service/view/event_apply.go`
- Modify: `modules/storage/internal/service/view/reconcile.go`
- Modify: `modules/storage/internal/service/view/reconcile_test.go`
- Modify: `modules/storage/internal/service/view/service.go`
- Modify: `modules/storage/internal/service/view/service_test.go`
- Delete after call sites migrate: `modules/storage/internal/service/view/live_gate.go`
- Modify: `modules/storage/cmd/server/main.go`

### Metadata 和文档

- Modify: `modules/storage/internal/config/loader.go`
- Modify: `modules/storage/internal/config/loader_test.go`
- Modify: `modules/storage/config/storage.yaml`
- Modify: `modules/storage/config/storage.primary.yaml`
- Modify: `modules/storage/config/storage_view/trpc_go.yaml`
- Modify: `modules/storage/internal/retinfo/metadata.go`
- Modify: `modules/storage/internal/retinfo/metadata_test.go`
- Modify: `modules/storage/internal/service/metadata/sqlite/crud_view_index_test.go`
- Modify: `modules/storage/README.md`
- Modify: `docs/因子视图驱动计算设计.md`
- Modify: `docs/运维/数据保留与磁盘空间.md`

## 4. Task 1：恢复 Dataset 级事件顺序

**目的：** 在扩大 consumer 并发时，仍保证同一 Dataset 的 rows 不会被 marker 或 sync point 越过。

- [x] **Step 1：先写失败测试，证明按完整 subject 选择队列会乱序**

在 `dataset_queue_dispatcher_test.go` 增加测试：阻塞 `DatasetRowsUpserted`，随后提交相同 `space_id + dataset_id` 的 `DatasetPeriodCollected`，断言 marker handler 在 row 释放前没有开始。

同时增加不同 Dataset 的并行测试：阻塞 Dataset A 的 row，Dataset B 的 row 必须能够完成。

Run:

```bash
cd modules/storage
go test ./internal/service/view/eventconsumer -run 'TestDatasetQueueDispatcher' -count=1
```

Expected: FAIL，当前实现会把 rows 和 marker 放进不同的 `delivery.Subject` 队列。

- [x] **Step 2：将 dispatcher 的 key 来源改为 Dataset 队列 key**

为 dispatcher 注入 key 函数，而不是直接使用 `delivery.Subject`：

```go
type deliveryQueueKey func(*jetstream.Delivery) (string, error)

func datasetQueueKey(registry *events.Registry, delivery *jetstream.Delivery) (string, error) {
	message, _, err := events.DecodeRaw(
		registry,
		delivery.RawData,
		delivery.Subject,
		delivery.RawMessageID,
		delivery.ContentType,
	)
	if err != nil {
		return "", err
	}
	return message.GetSpaceId() + "\x00" + message.GetSubjectId(), nil
}
```

将 dispatcher/queue 的内部命名统一为 `datasetQueueDispatcher`/`datasetQueue`（对外行为不变）。
`Dispatch` 在进入队列前计算 Dataset 队列键；解码失败时仍保留原始 delivery 的错误处理路径，
不能因为无法计算队列键而绕过重试、告警和顺序保护。

- [x] **Step 3：锁定顺序和并行行为**

增加以下测试：

- 同 Dataset：rows -> period marker -> sync point 的开始和完成顺序一致。
- 同 Dataset row 第一次失败并重试时，后续 marker 不开始。
- 不同 Dataset 可被不同 worker 并行执行。
- 关闭 dispatcher 时，队列中的 delivery heartbeat 被停止。

- [x] **Step 4：保留最终 8 路并发，但给旧版本部署安全值**

最终新代码可保留：

```yaml
eventbus:
  max_ack_pending: 8
view:
  fetch_batch: 8
  max_workers: 8
  ordering: dataset
```

部署新代码前，旧版本必须临时使用 `1/1/1`；只有 Dataset 串行队列测试通过后才恢复 `8/8/8`。

同步修改 `eventconsumer.Config.withDefaults`：默认和唯一允许的 ordering 是 `dataset`。旧的 `subject` 值必须返回配置错误，避免生产环境误以为已经具备 Dataset 顺序保证。

- [x] **Step 5：运行测试并提交**

```bash
cd modules/storage
go test -race ./internal/service/view/eventconsumer -count=1
git add internal/service/view/eventconsumer config/storage_view/trpc_go.yaml
git commit -m "fix(storage): serialize view events by dataset"
```

Expected: PASS。当前工作区已运行该包的 race 回归；提交动作留给集成验收完成后的统一提交。

## 5. Task 2：统一 DuckDB 的字段级写入语义

**目的：** 让 Primary 与 View 对重复 RowKey、部分字段 patch 和 Backfill/LiveWrite 竞争得到确定结果。

- [x] **Step 1：增加四组失败测试**

在 `index_manager_test.go` 增加：

1. 同一 batch 两个相同 RowKey：`close=10` 后 `close=20`，最终必须是 `20`。
2. 同一 RowKey 先写 `close` 再写 `volume`，最终两个字段都保留。
3. LiveWrite 显式提交 `close=NULL`，最终必须为 NULL。
4. B 先收到 LiveWrite `close=20`，再收到 Backfill `close=10`，最终必须是 `20`。

Run:

```bash
cd modules/storage
CGO_ENABLED=1 go test ./internal/service/viewindex/duckdb -run 'TestWrite.*(Duplicate|Partial|Null|Backfill)' -count=1
```

Expected: 至少重复 RowKey和部分字段测试 FAIL。

- [x] **Step 2：在生成 SQL 前折叠同批重复 RowKey**

新增内部函数，保持 RowKey 第一次出现的位置，但字段值按输入顺序以后值覆盖前值：

```go
func collapseRowWrites(writes []viewindex.RowWrite) []viewindex.RowWrite
```

规则：

- RowKey 使用完整 `subject_id + freq + data_time + series_tag`。
- 相同字段后出现的值覆盖先出现的值，包括显式 NULL。
- 不同字段合并到同一个 RowWrite。
- 不改变不同 RowKey 的稳定顺序。

- [x] **Step 3：按字段集合分组生成 UPSERT**

当前 SQL 对所有 schema columns 都执行更新，会把本次 patch 未携带的字段写成 NULL。改为：

```text
keys + 本组实际出现的字段
    -> INSERT
    -> ON CONFLICT 只更新本组实际出现的字段
```

LiveWrite：

```sql
close = excluded.close
```

Backfill：

```sql
close = COALESCE(view_rows.close, excluded.close)
```

同一 batch 内按排序后的字段名集合分组，每组仍限制最多 256 rows。

- [x] **Step 4：增加可取消的 per-index write gate**

在 View Service 的 `index_write_gate.go` 创建基于 channel token 的 gate：

```go
type indexWriteGate struct {
	token chan struct{}
}

func (g *indexWriteGate) lock(ctx context.Context) (func(), error) {
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-g.token:
		return func() { g.token <- struct{}{} }, nil
	}
}
```

`view.Service` 按 index ID 保存 gate；`PrepareViewIndex`、`ApplyViewIndex`、`applyEventToIndex`、Backfill 的直接 `engine.Write`、`removeFailedBuild/RemoveViewIndex` 都必须经过同一个 gate。这样 DuckDB 和 Bleve 使用同一条生产写入规则，同一 index 互斥、不同 index 互不阻塞。

gate 在 `Service` 生命周期内保持稳定，不随文件删除而删除，避免等待旧 gate 的 goroutine 在同名 index 重建后越过新 gate。A/B index ID 数量受 View 数量约束，不需要增加 gate 回收状态机。

- [x] **Step 5：将 Backfill 读批次和写批次分开**

保留 10,000 行历史读取页，但每次交给 DuckDB 的事务最多 256 行：

```go
const (
	viewBackfillReadBatchSize  = 10000
	viewBackfillWriteBatchSize = 256
)
```

这样实时 B 写入可以在两个回填事务之间获得 per-index gate，不会等待一个 10,000 行大事务完成。

- [x] **Step 6：运行测试并提交**

```bash
cd modules/storage
CGO_ENABLED=1 go test -race ./internal/service/viewindex/duckdb -count=1
go test -race ./internal/service/view -count=1
git add internal/service/viewindex/duckdb internal/service/view/index_write_gate.go internal/service/view/index_write_gate_test.go internal/service/view/build.go
git commit -m "fix(storage): preserve view patch ordering in duckdb"
```

Expected: PASS。当前工作区已运行 DuckDB/View race 回归；提交动作留给集成验收完成后的统一提交。

## 6. Task 3：实现 A 权威、B 延迟收敛的在线重建

**目的：** 回填期间不再阻塞 A 和其他 View，并且不需要持久化 watermark。

- [x] **Step 1：增加在线重建失败测试**

在 `service_test.go` 和 `reconcile_test.go` 增加：

- B 已挂载且 Backfill 阻塞时，新 row 最终同时写 A、B。
- View A1/B1 回填时，另一个 View A2 的实时 row 能立即完成。
- Backfill 旧值和 LiveWrite 新值竞争，B 最终保留新值。
- B live write 失败后永远不能进入 active，A 继续接收后续事件。
- Backfill 完成后，切换等待当前 View 正在执行的双写结束。

Run:

```bash
cd modules/storage
go test ./internal/service/view -run 'Test.*(DualWrite|Backfill|Replacement|Switch)' -count=1
```

Expected: FAIL，当前全局 `liveGate` 会阻塞实时 delivery 或等待整个 durable backlog。

- [x] **Step 2：删除长时间持有的 Service 级 live gate**

从以下路径移除 `acquireBackfill/releaseBackfill`：

- `BackfillViewWithReader`
- `ApplyViewIndex` 的 Backfill/LiveWrite 分支
- `acquireActivationFence`

迁移完成后删除 `live_gate.go` 以及 `Service.liveGateOnce/liveGate` 字段。

View 级生命周期直接复用已有 `viewRuntime.mu`：

- `applyDatasetEvent` 在写 A/B 期间持有它。
- 挂载 B 时短暂持有它。
- 激活 B 时持有它直到 Metadata CAS 和 runtime pointer 更新完成。

- [x] **Step 3：确保 B 在回填开始前已经可接收实时写**

`PrepareViewIndex` 成功后，在返回前完成：

```text
runtime.next = B
runtime.status = building
indexView[B] = current View
byData[dataset] includes B
```

这些修改必须在当前 View 的 `runtime.mu` 下完成。完成挂载后才允许 `BackfillViewWithReader` 开始扫描 A。

在 `viewRuntime` 保存本轮 `buildCancel context.CancelFunc`。B live write 失败时先将 runtime 状态置为 `failing` 并取消 Backfill；`removeFailedBuild` 再通过同一个 per-index gate 等待当前 B 事务结束后关闭和删除文件。禁止 Backfill goroutine在 B 删除后继续写入同名文件。

- [x] **Step 4：固定 active/next 写入规则**

`applyDatasetEvent` 使用以下简单规则：

```text
有 Active A：
  A 写失败                 -> 返回错误，不 ACK
  A 成功且没有 B           -> 成功
  A 成功且 B 成功          -> 成功
  A 成功但 B 失败          -> 标记 runtime=failing，取消 Backfill，持久化 FailViewIndexBuild
  FailViewIndexBuild 成功   -> 摘除并删除 B，ACK
  FailViewIndexBuild 失败   -> 保留 runtime=failing，返回错误，不 ACK

首次构建没有 Active：
  B 未 READY/active        -> 保持 delivery pending
  B 写失败                 -> 返回错误并废弃 B
```

将 `failRuntimeBuild` 改为返回 error，不能再只写日志后继续激活一个无法证明完整的 B。处于 `failing` 的 runtime 收到 redelivery 时只重试持久化失败状态，不再把 B 恢复为可激活状态；进程重启后由 `RestoreActiveViews` 将 Metadata 中仍未激活的 B 废弃。

- [x] **Step 5：用当前 View 的短锁完成切换**

删除对全 durable `NumPending/NumAckPending` 的等待。激活流程改为：

```go
runtime.mu.Lock()
defer runtime.mu.Unlock()

// 此时该 View 的在途 A/B 双写已经完成，新写会等待 runtime.mu。
// 再确认 next、build ID、status 和 schema revision。
// 调用 Metadata.ActivateViewIndex CAS。
// 成功后原子更新 runtime.active/runtime.next。
```

锁释放后，排队事件读取到的新 active 是 B，只写 B。正常切换后使用现有 60 秒 grace 删除旧 A DuckDB 文件。

- [x] **Step 6：锁定允许的 NULL 复活行为**

增加一个明确测试：LiveWrite 将 B 的字段清为 NULL，随后 Backfill 补入 A 的旧非空值，断言旧值被补回。测试名称和注释必须说明这是个人量化系统接受的简化边界，防止后续误当回归。

- [x] **Step 7：运行测试并提交**

```bash
cd modules/storage
CGO_ENABLED=1 go test -race ./internal/service/view ./internal/service/viewindex/duckdb -count=1
git add internal/service/view cmd/server/main.go
git commit -m "fix(storage): rebuild views without blocking active writes"
```

Expected: PASS。当前工作区已覆盖 A/B 双写、切换、失败和响应回读测试；提交动作留给集成验收完成后的统一提交。

## 7. Task 4：简化启动恢复、文件超限重建和 CAS 语义

**目的：** 重启时先恢复可读 A，未完成 B 直接废弃；失败或仍超限的文件重建不会每分钟反复消耗资源。

- [x] **Step 1：增加启动顺序测试**

在 `reconcile_test.go` 增加一个可阻塞 Backfill 的 fake：

1. Metadata 返回可用 Active A 和未完成 B。
2. 启动 Storage View。
3. 断言 A 已 Attach、consumer 已启动。
4. 断言未完成 B 被标记失败并删除。
5. 断言新的文件超限重建发生在 consumer 启动之后。

- [x] **Step 2：拆分 Active 恢复和重建检查**

新增明确入口：

```go
func (s *Service) RestoreActiveViews(ctx context.Context, opts ReconcilerOptions) error
```

它只负责：

- 分页读取 active View metadata。
- 先依据 Metadata 的 `View.Engine` 选择实际引擎并检查当前物理 A，再建立 index 到 engine 的运行时映射并 `AttachActiveView`；不能用尚未建立映射的 `engineFor(activeID)` 作为恢复入口。
- 发现未激活的 PREPARING/BUILDING/CATCHING_UP/READY B 时，标记失败并删除 B。
- 若 Metadata 已经指向某个 build 的 index，该 index 视为权威；不能因读取到旧的 READY build 记录而删除它。
- 不 Claim 新 build，不执行历史回填。

`cmd/server/main.go` 启动顺序固定为：

```text
RestoreActiveViews
    -> connect EventBus
    -> StartEventConsumer
    -> StartReconciler（后台立即检查，函数不等待完整 Backfill；首个 B 直接由 Dataset 事件补写，不能等待 consumer backlog 清零）
    -> Serve
```

启动阶段如果某条事件暂时找不到本地 View 映射，事件处理器会先按 Dataset
队列键查询 Metadata：未被任何 active View 管理的 Dataset 直接 ACK；已被管理但
尚未恢复映射的 Dataset 保持 pending，避免首轮长回填把无关 Dataset 的
`MaxAckPending` 槽位占满。DuckDB 启动只清理带有 `.duckdb.prepare-` 前缀的
中间文件；正式 `.duckdb` 文件交由 Metadata 恢复流程判定，不能因为启动时的
文件校验失败就盲目隔离可能仍是权威 Active A 的文件。

本次采用简单边界：Active View 的 `PrimaryDatasetId` 不允许在原 View 上直接
切换。若发现 desired primary 与当前 active primary 不一致，重建会 fail closed，
保留 A 并要求创建/切换到新的 View；这样避免仅扫描旧 primary 无法覆盖新 primary
历史行而激活一个不完整的 B。

同样，已有 Active View 的 `Engine` 不允许原地从 DuckDB/已有引擎切换到另一引擎。
Metadata 在 Upsert 阶段直接拒绝该变更；需要新建 View。这样重启恢复不会把一个
健康但由旧引擎创建的 A 误判为缺失。

- [x] **Step 3：把 View 重建检查配置改成直观名称**

删除旧版本宽泛的 `storage.view.maintenance` 配置块。本项目不需要兼容旧配置，直接改为“重建检查”配置：

```yaml
storage:
  view:
    rebuild_check_interval: 1m
    max_view_file_bytes: 1073741824
```

Go 类型只保留实际使用的字段：

```go
type StorageView struct {
	MetadataServiceName     string           `yaml:"metadata_service_name"`
	PrimaryStoreServiceName string           `yaml:"primary_store_service_name"`
	IndexServiceName        string           `yaml:"index_service_name"`
	BatchSize               int              `yaml:"batch_size"`
	BatchWaitMS             int              `yaml:"batch_wait_ms"`
	FetchBatch              int              `yaml:"fetch_batch"`
	MaxWorkers              int              `yaml:"max_workers"`
	Ordering                string           `yaml:"ordering"`
	RebuildCheckInterval    string           `yaml:"rebuild_check_interval"`
	MaxViewFileBytes        int64            `yaml:"max_view_file_bytes"`
	StorageRPC              StorageRPCConfig `yaml:"storage_rpc"`
}
```

删除不再使用的 `StorageViewMaintenance`、`StorageTimeSeriesMaintenance`、`StorageRecordMaintenance` 及其旧默认值。保留期继续由 Dataset/View metadata 的 `keep_duration` 负责，不在 Storage 进程配置中重复配置。

函数和字段同步改名：

| 原名称 | 新名称 |
|---|---|
| `storageViewMaintenanceSettings` | `storageViewRebuildSettings` |
| 旧检查间隔字段 | `rebuildCheckInterval` |
| `MaxPhysicalBytes` | `MaxViewFileBytes` |
| 旧超限判定字段 | `sizeLimitExceeded` |
| 旧超限重建函数 | `needsSizeLimitRebuild` |

配置校验要求：`rebuild_check_interval >= 30s`，`max_view_file_bytes > 0`。不增加单独的 enabled 开关，Storage View 启动后始终执行文件大小检查。

- [x] **Step 4：使用现有 build 时间实现超限重建重试间隔**

新增：代码使用 `sizeLimitRebuildRetryInterval`，中文说明统一称为“超限重建重试间隔”。

```go
const sizeLimitRebuildRetryInterval = 30 * time.Minute
```

作用和规则：

- `desired_revision != active_revision`：立即允许新构建。
- 仅因 `physical_bytes >= max_view_file_bytes` 触发的文件超限重建失败：从 build `updated_at` 起等待 30 分钟。
- 这不是暂停 View 的“冷却模式”，而是防止同一个超限问题每分钟重复启动完整回填，
  避免持续消耗 CPU、磁盘 IO 和临时文件空间。
- 30 分钟重试间隔结束后允许重新完整构建。
- 重启后仍从 Metadata 的 `updated_at` 判断，不增加新表。

测试必须覆盖失败后 1 分钟不重建、30 分钟后重建、desired revision 变化立即重建。

- [x] **Step 5：把 build CAS 映射为 CONFLICT**

在 `retinfo.MetadataStoreCode` 增加：

```go
case errors.Is(err, sqlite.ErrViewIndexBuildConflict):
	return pb.ErrorCode_CONFLICT
```

如果包依赖不允许 `retinfo` 直接导入 sqlite，则将错误移到 metadata store 公共包并保持 `errors.Is` typed error；禁止字符串匹配。

Reconciler 收到 `CONFLICT` 后结束本轮当前 View 处理，下一轮重新读取 Metadata，不调用 `FailViewIndexBuild`。

- [x] **Step 6：测试并提交**

```bash
cd modules/storage
go test -race ./internal/config ./internal/service/view ./internal/service/metadata/sqlite ./internal/retinfo -count=1
git add internal/config config/storage.yaml config/storage.primary.yaml config/storage_view/trpc_go.yaml internal/service/view internal/service/metadata/sqlite internal/retinfo cmd/server/main.go
git commit -m "fix(storage): simplify view rebuild recovery"
```

Expected: PASS。当前配置、View、SQLite metadata、retinfo 和 server focused race 回归均通过；提交动作留给集成验收完成后的统一提交。

## 8. Task 5：集成测试和正式环境验证

**目的：** 证明不是“测试绿但线上仍停在旧 A”。

- [x] **Step 1：增加最小真实 DuckDB 在线重建集成测试**

为保持实现简单，本轮先用一个真实 DuckDB View 完成最小闭环；不同 View/Dataset
的并行与 Dataset 队列顺序由 `view/eventconsumer` 的并发测试覆盖，不把多个 View
耦合进同一个长时测试。集成测试覆盖：

1. A 已有历史数据。
2. 挂载 B 并阻塞第一批 Backfill。
3. 同时投递实时 row、period marker、sync point。
4. 查询期间始终读取 A。
5. 释放 Backfill 后 B 激活。
6. 切换后 B 同时包含历史 row 和实时 row。
7. marker 只能在对应 rows 应用成功后完成。
8. 切换后旧 A 进入 60 秒 grace，新 B 文件仍可查询。

实现位置：
`modules/storage/internal/service/e2e/view_ab_dual_write_e2e_test.go`，测试名
`TestViewABDualWriteKeepsLiveValueAcrossBackfill`。它实际 Prepare/Write/Backfill/
Switch/Query DuckDB，断言历史行和回填期间的实时新值都在 B 中，且实时值不会被旧
回填覆盖。60 秒定时删除和跨进程重启仍保留在正式环境验收项中，不在单元测试中
等待真实墙钟时间。

实现边界：同一 View 的旧 A 在 grace 期间继续承担读写；inactive 槽在 grace 期间不得复用。
`keep_duration` 变化只更新 metadata 保留窗口，不触发 A/B 回填；扩大历史窗口不承诺恢复已经从 A/Primary 清理的数据，避免用旧 A 的有限范围生成不完整 B。

- [x] **Step 2：增加失败与重启回归测试**

本地回归覆盖启动恢复、Activate 响应丢失、未激活 build 清理、首建无映射时
Dataset 事件保持 pending、B 写失败不激活等阶段；测试不伪装成真正的进程 kill，
跨进程 kill/restart 仍列为正式环境门禁。覆盖以下状态边界：

- B 刚 Prepare，尚未回填。
- B 回填一半。
- B 回填完成但 Metadata 尚未 Activate。
- Metadata 已 Activate，但客户端收到超时/响应丢失。

前三种情况允许删除 B 并重建；最后一种必须以 Metadata 中已经激活的 B 为权威，不得删除 B。

- [x] **Step 3：执行模块验证**

```bash
cd modules/storage
CGO_ENABLED=1 go test -race ./internal/service/view/... ./internal/service/viewindex/duckdb ./internal/service/metadata/sqlite ./internal/retinfo -count=1
go vet ./internal/service/view/... ./internal/service/viewindex/duckdb ./internal/service/metadata/sqlite ./internal/retinfo
```

Expected: 全部 PASS。已在本地执行 View、DuckDB、配置、SQLite metadata、retinfo 和 server 的 race 回归。

- [ ] **Step 4：执行工作区合同验证（合同已基本通过，仍有基线失败）**

```bash
cd /Users/mooyang/Documents/go/src/github.com/mooyang-code/moox
make test-script-contracts
./scripts/test/contract/test-go-workspace.sh
```

当前 `make test-script-contracts` 已通过部署、Storage、Gateway、Factor、DNS 等
合同及各模块 Go test/vet，但最后的 greenfield 合同因现有
`modules/factor/proto/factor.proto` 中仍存在 3 个 `deprecated = true` 字段而失败：

```text
deprecated protocol declarations and markers are forbidden
./modules/factor/proto/factor.proto:36
./modules/factor/proto/factor.proto:37
./modules/factor/proto/factor.proto:108
```

这不是本次 Storage View A/B 改动引入的失败，未在本计划中删除 Factor 兼容字段。
`./scripts/test/contract/test-go-workspace.sh` 已完整通过，包含所有模块的 go test/go vet；该命令
与工作区合同总入口是两个独立门禁，不能用 workspace 通过替代 greenfield 合同通过。

本轮再次运行合同时，Storage/Linux、部署合同及前置模块均通过；执行到 Archive
时有一个既有时间敏感测试 `TestWriteDirtyReturnsWhenAllWorkersFailBeforeJobsExhausted`
偶发超过 400ms 门槛而失败。该测试单独 `-count=5` 通过，故未将这次环境抖动归因于
Storage View A/B 改动；合同总命令仍不能标记为全绿。

最新一次 `./scripts/test/contract/test-go-workspace.sh` 已完整通过，包含 Storage、Factor、Gateway、
Trade、Collector 及所有 packages 的 go test/go vet；工作区合同总入口仍保留上述
既有 baseline/时间敏感失败记录。

- [x] **Step 5：更新文档并提交**

文档只描述最终简单模型：A 权威、B 延迟、失败丢弃、NULL 可复活、固定重试间隔、固定 grace。删除旧文档中依赖全局 backlog fence 或持久化 watermark 的描述。

```bash
git add modules/storage/README.md docs/因子视图驱动计算设计.md docs/运维/数据保留与磁盘空间.md
git commit -m "docs(storage): document simplified view rebuild lifecycle"
```

文档内容已按“Active A 权威、B 延迟收敛、失败丢弃、固定 grace、允许 NULL
复活”的简化模型更新；提交动作留给集成验收完成后的统一提交。

- [x] **Step 6：正式环境发布前保护**

旧 binary 尚未替换前，将 Storage View consumer 临时降为：

```yaml
eventbus:
  max_ack_pending: 1
view:
  fetch_batch: 1
  max_workers: 1
```

本次正式环境发布前已记录以下基线：

- Primary 最新 `data_time`。
- `dataset_binance_spot_kline_1m` Active View 最新 `data_time`。
- `binance_spot_kline_1m_factor` 最新 `data_time`。
- active/next index ID、build state、DuckDB 文件大小。
- JetStream pending/ack-pending。

- [ ] **Step 7：正式环境验收（已发布并健康，业务追平/故障门禁未闭环）**

新 binary 已发布到正式 Storage View（本轮只替换 `storage-view`，不重启
Storage Primary）。本轮最终发布 SHA-256 为
`5ae0c7e7bdd738fee977fb8716b4a5adee2748a737cef129800e50c37d0adda3`，进程 PID
`3471798`，`healthcheck.sh storage-view` 返回 `running ... ready`。当前配置恢复为
`8/8/8`，继续观察至少一个完整的文件大小检查周期：

- A 在整个回填期间持续接收最新 K 线。
- B 的历史回填可以落后，但 realtime row 持续进入 B。
- marker 不早于相同 Dataset rows 完成。
- B 切换后最新时间不倒退，历史数据仍可查询。
- 旧 A DuckDB 文件在 60 秒 grace 后删除。
- 文件超限重建失败后 30 分钟内不会重复启动完整回填。
- 一个 View 构建不影响另一个 View 的实时更新时间。

当前线上验证记录（2026-08-12）：Storage Primary 保持运行，Storage View 已发布
构建 SHA-256 为
`5ae0c7e7bdd738fee977fb8716b4a5adee2748a737cef129800e50c37d0adda3`，进程 PID
`3471798`，`healthcheck.sh storage-view` 返回 `running ... ready`。发布前已对
`storage_metadata.db` 做带时间戳备份并修复一次历史 desired/active revision 不一致。
线上 `view_crypto_spot_kline_1m` 当前 Metadata 为 active/desired revision `9/9`、
active slot `B`，且该 View 当前没有未完成 `t_view_index_builds` 记录；发布后
tRPC/HTTP 服务均监听成功，健康脚本通过。当前远端仍需
继续核对采集器、Primary watermark 与 View 的逐分钟进度；不能只用进程 ready
宣称最新 K 线已完全追平。完整 B 回填尾部、60 秒 grace 删除旧 A、故障注入及
跨进程重启场景仍未完成，不能把本次线上结果写成最终端到端通过。

已补充并执行生产只读业务 smoke：`modules/storage/cmd/view-smoke/main.go` 通过
Storage Primary 与 DataView HTTP 接口读取同一 `BTC-USDT/1m` 时间窗口。最近一次
（2026-08-12 23:20 CST）结果为 Primary 最新 `2026-08-11T15:25:00Z`，View 最新同为
`2026-08-11T15:25:00Z`，View 返回 5 行且无鉴权/协议错误；这证明当前线上 View
查询链路可用并与 Primary 的最新行对齐，但不替代 A/B 故障注入和长回填验收。该结果
同时表明上游 Primary 本身仍停在该时间点，不能据此把采集链路声明为实时追平。

本轮又以 `CGO_ENABLED=1 go test -race ./internal/service/e2e -count=1` 重跑
Storage E2E，5 个用例全部通过；其中包含 A/B 回填期间实时写入、独立 Dataset 队列
并行消费、View 查询及回填后的数据校验。该本地 E2E 仍不等价于正式环境故障注入。

## 9. 完成标准

只有以下条件全部成立，才能宣布完成：

1. 同 Dataset rows、period marker、sync point 在 8 worker 下仍保持顺序。
2. DuckDB 与 Primary 对重复 RowKey 和部分字段 patch 的最终结果一致。
3. A/B 回填期间 Active A 不停止更新，其他 View 不受全局锁影响。
4. B 挂载后实时事件双写；B 写失败后该 B 永不激活。
5. 切换只等待当前 View 的在途写，不等待全 durable backlog。
6. 重启后 Active A/B 能按 Metadata 恢复，未完成 B 可直接废弃重建。
7. 失败的文件超限重建使用固定 30 分钟重试间隔。
8. 正式环境 View 最新时间重新与 Primary 对齐，旧 A 文件按 grace 删除。
9. 文档明确记录 NULL 值可能被历史回填复活这一接受边界。
10. 显式零列 View 的 Dataset 事件仍可 ACK，且不会阻塞同 Dataset marker/sync point。

## 10. 回滚原则

- 新 B 出现任何不确定状态时，不切换，保留 A。
- 已完成 Metadata 激活的 B 不能因客户端超时被当作失败删除；下一轮先重新读取 Metadata。
- 回滚 binary 前将 consumer 并发降回 `1/1/1`。
- 不删除 JetStream durable，不跳过未处理 rows/marker。
- 不直接修改 SQLite 修复 build 状态。
- 如果无法证明 B 完整，删除 B 文件和 build 记录后重新完整构建，而不是修补半成品。
