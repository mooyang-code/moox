# Storage CR 深度整改与一致性收敛 Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 修复 2026-07-18 Storage 执行计划落地后仍存在的消息重试、重建竞态、Payload 边界、Schema 校验、权威分页、Metadata 扩展性和旧协议残留问题，使事实写入、活动 View 增量、View 重建和查询新鲜度形成可证明、可恢复、可运维的闭环。

**Architecture:** DataShard 保留全局连续 Outbox Sequence，同时为每个 Dataset 原子维护 Source Head；活动 View 和重建 View 使用彼此独立的 JetStream Durable Consumer。活动链路按 Shard 有界串行，失败只阻塞同一 Shard 的后续 Sequence，并允许失败 Sequence 重试；重建链路先创建专用 Durable Consumer，再取得 Pebble 固定快照和 Source Head Vector，完成快照回填后重放 Head Vector 之后的事件，并通过 View 级切换屏障激活。PrimaryStore 只做请求预检、路由和聚合，DataShard 在事实与 Outbox 同一 Pebble Batch 提交前完成 Merge 后最终 Schema、大小和 Schema Fence 校验。ViewIndex 只保留原子 `Apply` 协议，Checkpoint 以 `{shard_id, dataset_id}` Source Lane 为单位。

**Tech Stack:** Go、tRPC-Go、Protocol Buffers、Pebble、SQLite、NATS JetStream、DuckDB、Bleve、Prometheus、Shell。

---

## 文档定位

本计划基于对当前 `main`（审查基线 `ca36e828b828cd93c31ac2990e47c6b7df859799`）的第二轮深度 CR。它不否定 [2026-07-18 原计划](./2026-07-18-storage-primary-shard-boundary.md) 已确立的正确方向，但替换原计划中尚未完成或已被代码事实证明不成立的实施设计。

实施时遵循以下优先级：

1. 本计划中的正确性不变量和最终架构决策。
2. `docs/superpowers/specs/2026-07-18-storage-primary-shard-boundary-design.md` 经本计划同步修订后的版本。
3. 2026-07-18 原计划仅作为历史背景和已完成工作索引。

这是全新项目，不保留旧数据、旧 Proto、旧 RPC、旧配置或旧类型的兼容层。每个重命名必须在所属 Task 内一次完成，禁止增加 Deprecated Alias、双写、双读或迁移分支。

## 审查结论

原计划总体方向正确，但还不能作为最终一致性设计直接继续实施。核心事实提交和 ViewIndex 原子 Apply 已经打下良好基础；活动消费失败模型、在线重建模型、Schema 最终校验位置和分页模型需要改写，而不是局部补丁。

### 应保留的设计

| 设计 | 结论 | 原因 |
| --- | --- | --- |
| DataShard 事实行与 Outbox 同一 Pebble Batch | 保留 | 建立了事实提交与事件发布的原子边界 |
| 每个 DataShard 单调连续 Sequence | 保留 | 足以保证 Outbox 发布顺序和同 Shard 重放，不需要全局总序 |
| Outbox 只删除连续成功前缀 | 保留 | 失败项后面的消息不能越过发布 |
| ViewIndex 行写入、Checkpoint、范围同一事务 Apply | 保留 | 数据与消费进度可以一起 CAS 提交 |
| `MERGE` / `REPLACE` / `DELETE` 显式语义 | 保留 | 稳态局部列更新、缺行恢复和删除边界清楚 |
| MERGE 缺行时整批失败并全源读取后 REPLACE | 保留 | 防止局部 View 行被当成完整行 |
| 每列来源保存 `shard_id + sequence` | 保留 | 可以拒绝恢复读到新值后又被旧事件回滚 |
| `indexed_from/indexed_to` 与事件新鲜度分离 | 保留 | 业务覆盖范围和消费进度不是同一概念 |
| 首次写入后冻结 Dataset 拓扑 | 保留并收紧 | 没有迁移协议时不能改变既有 Key 放置 |
| Admin Gateway + Node Service Gateway 双入口 | 保留 | 用户鉴权和服务路由职责分离合理 |

### 必须整改的 CR 发现

| 级别 | 当前事实 | 风险 | 本计划修复 |
| --- | --- | --- | --- |
| P1 | `viewbuilder.Service` 首次失败后把 Shard 永久放入 `blockedByShard`，相同 Sequence 重投也不能解除 | 单次瞬时错误可永久停止整个 Shard；Readiness 仍可能为真 | Task 3 改为可恢复的 Shard Scheduler |
| P1 | BUILDING/CATCHING_UP ViewIndex 被活动消费链路视为 Writable，空 Checkpoint 直接接收当前 Sequence | 当前 Sequence 通常不为 1，warming Apply 发生 Gap；活动写成功也会因 warming 失败而 NAK | Task 7-9 完全隔离活动与重建消费 |
| P1 | DataShard 接受 16 MiB Payload，而 JetStream 默认只允许 8 MiB | 事实已经提交，Outbox 首项却永远无法发布，后续消息全部被堵塞 | Task 4 统一最终 Payload 上限并在提交前拒绝 |
| P1 | Merge Validator 未覆盖空批次、重复 Key/列、跨 Space/Dataset、Oneof 类型、TIME/JSON、NaN/Inf、Required-after-merge 和大小边界 | 非法事实可进入 Pebble 或在派生阶段才失败 | Task 5 把最终校验移入 DataShard 原子边界 |
| P2 | Metadata List 多数先全表加载再内存分页；Cache 包含 ArchiveFile 等无界对象，每次 CRUD 全量 Refresh | 数据增长后内存、延迟和锁竞争失控；提交成功后 Cache 刷新失败会向调用方返回失败 | Task 11 实现 SQL 真分页和有界缓存 |
| P2 | PrimaryStore 跨 Target 查询先全量读取、排序，再在 10,000 行处失败；Cursor 只是 `targetIndex|innerCursor` | 大数据查询不可用，跨 Shard 页序不稳定 | Task 10 改为结构化 Keyset Cursor 和 K-way Merge |
| P2 | 通用 Device、PrimaryStoreNode/Route、调用方可传物理 Target、旧 `BatchWrite`/`Write`、`physical_bytes` 仍在活动协议 | 物理实现泄漏给调用方，存在绕过一致性入口和双写协议 | Task 2、12 删除旧面 |
| P2 | `retinfo` 和 PrimaryStore 通过错误字符串分类，RPC 直接暴露 `err.Error()` | 错误码不稳定，可能泄露内部细节 | Task 5 使用类型化错误和安全消息 |
| P2 | 缺少 Outbox Payload、队首年龄、重试和阻塞原因指标 | 发布停滞只能靠日志猜测 | Task 4、13 增加指标和受审计修复工具 |
| P2 | 现有 Reliability 测试第一次 Apply 实际因缺行失败，没有覆盖“持久化成功后返回错误”的重放 | 测试绿但没有验证最危险的崩溃窗口 | Task 3 修正测试模型 |

### 为什么现有门禁不足

审查时以下命令均通过：Storage 全量单测、核心 Race、DuckDB/Bleve Race、`test-storage-boundary-contract.sh`、`test-storage-consistency-contract.sh` 和 `make verify`。这只能说明当前门禁内部一致，不能证明上表问题不存在。

本计划要求先为每个问题写能在旧实现上稳定失败的测试，再修改实现；不得把现有绿灯当作跳过 TDD 的理由。

## 最终架构决策

### 1. 两层 Sequence，而不是一个 Checkpoint 承担两种职责

- `shard_sequence`：DataShard 每次事实提交生成的全局连续序列，只用于 Outbox 排序、JetStream 重放和同 Shard 执行顺序。
- `dataset_source_head`：同一 Pebble Batch 内把本次提交 Sequence 写到对应 Dataset Head，表示该 Dataset 在该 Shard 的最新事实事件。
- `view_source_checkpoint`：ViewIndex 按 `{shard_id, dataset_id}` 保存最后已应用的 Source Sequence，只为 View 实际依赖的 Dataset 更新。
- Source Sequence 可以跨过同 Shard 其他 Dataset 的 Sequence；CAS 只要求 `expected == stored && last > expected`，不要求 `last == expected + 1`。
- 全局 Shard Sequence 仍由消费 Scheduler 保证不越过失败消息。Source Checkpoint 不替代 JetStream 的有序交付。

最终协议形状：

```proto
message ViewIndexSourceCheckpointUpdate {
  string shard_id = 1;
  string dataset_id = 2;
  uint64 expected_last_applied_sequence = 3;
  uint64 last_applied_sequence = 4;
}

message DatasetShardHead {
  string shard_id = 1;
  string dataset_id = 2;
  uint64 last_committed_sequence = 3;
}
```

### 2. 活动消费和重建消费必须隔离失败域

- 活动 Durable Consumer 只写当前 Active ViewIndex。
- 每次 Build 创建独立、可恢复、名称稳定的 Durable Consumer，只写本次 warming ViewIndex。
- 活动消息不能因为 warming Index 失败而 NAK；warming 失败只把 Build 标记为 FAILED。
- 活动处理在写入前解析当前 Active Index，切换期间通过 View 级 Activation Barrier 阻止旧 Active Index 吞掉新 Index 尚未追上的事件。
- Build Consumer 使用独立 Scheduler，不占用活动 Shard Lane；它采用 `DeliverNew + AckExplicit`，创建后先不拉取，直到 Snapshot 基线提交。
- MOOX_STORAGE Stream 使用 `InterestPolicy + DiscardNew`：消息由所有匹配的 Durable Consumer ACK 后释放；容量耗尽时让 Publish 失败并回压 DataShard Outbox，不能用 `LimitsPolicy + DiscardOld` 淘汰 Build 尚未消费的历史消息。
- EventBus Stream 的 `max_age` 和 Consumer 生命周期必须覆盖配置允许的最长 Build 时间并留出安全余量；无法证明保留窗口足够时拒绝启动 Build。

### 3. 重建必须基于存储快照和事件屏障

业务时间 `snapshot_end` 不是存储一致性快照。最终流程为：

1. 持久化 Build 记录及 Durable Consumer 名称。
2. 创建或确认 Build Durable Consumer 已存在。
3. 对每个来源 DataShard 创建 Pebble Snapshot，取得 `snapshot_id + DatasetShardHead Vector`。
4. 从固定 Snapshot 回填 warming Index；游标只与该 Snapshot 绑定。
5. 回填完成后原子设置基线 Source Checkpoint 和业务范围。
6. Build Consumer 丢弃并 ACK `sequence <= snapshot head` 的来源事件，应用其后的事件。
7. 进入 View Activation Barrier，固定新的 Source Head Vector，追平后 CAS 切换 Active Index。
8. 切换成功后释放活动消费，删除 Build Consumer 和 Snapshot。

若 Snapshot 在回填完成前过期或丢失，必须放弃本次 Build，重新 Prepare 新 Index 和新 Build；禁止拿旧 Cursor 读取新 Snapshot。

### 4. Schema 最终校验属于 DataShard

PrimaryStore 可做便宜的早期拒绝，但不能成为最终正确性边界。`MergeRowsReq` 必须携带结构化 `DatasetSchemaFence`；DataShard 在读取旧行、合并新值后，验证 Schema Fence、完整行 Required、TypedValue、重复字段和序列化大小，全部通过后才写事实行、Dataset Head 和 Outbox。

```proto
message DatasetSchemaFence {
  uint64 schema_version = 1;
  string schema_hash = 2;
  repeated DatasetColumnConstraint columns = 3;
}
```

DataShard 持久化每个 Dataset 已接受的 `schema_version + schema_hash`，拒绝旧版本或同版本不同 Hash。公共写请求限制为 1,000 行、4 MiB、单行 1 MiB；DataShard 再使用与 JetStream Publisher 完全相同的最终 Payload 上限做提交前校验。公共写入不做隐式二分重试，避免一个原子批次静默变成部分提交。

### 5. 分页必须从存储层开始有界

- Metadata 无界集合使用 `(sort_key, id)` Keyset Pagination；只有经过明确上限约束的小目录允许 OFFSET。
- DataShard Scan Cursor 表示该 Target 最后一条实际返回的 Key，而不是底层预取位置。
- PrimaryStore Cursor 使用 Base64URL 编码的确定性 Proto，包含查询指纹、排序方向和每个 Target 的最后返回 Key。
- PrimaryStore 每页从各 Target 有界取数并用 Heap 做 K-way Merge；未返回的预取行不能推进对应 Target Cursor。

### 6. 运维逃生口不能破坏静默一致性

- Outbox 提供 inspect、retry 和 audited force-skip。
- force-skip 默认禁用，必须同时提供 Shard、Sequence、Message ID、原因和显式确认参数。
- force-skip 必须在删除队首 Outbox 项的同一 Pebble Batch 中写入不可变 `DeliveryGap` 和本地 `REPAIR_REQUIRED` 标记；即使 Metadata 更新失败，DataShard Readiness 也不能恢复绿色。
- DeliveryGap 记录受影响 Dataset 和所有强一致下游消费者。只有各消费者提交可验证的回填证据后才能 Resolve；View 重建不能代替 Archive 回填。
- 在线 DELETE 只删除当前事实和 View；Archive 继续保留历史。合规 PURGE 涉及 Parquet/COS Manifest 重写和审计，不在本计划伪装实现，必须另立设计和执行计划。

## 正确性不变量

1. 事实行、Shard Sequence、Dataset Head 和 Outbox Message 必须在同一 Pebble Batch 中提交。
2. DataShard 必须在提交前证明最终序列化消息不超过 Publisher 的实际 Max Payload。
3. Outbox 只能删除连续成功前缀；重试不得跳过失败 Sequence。
4. 同一 Shard 的活动消费不越过失败 Sequence；失败 Sequence 成功重投后 Lane 自动恢复。
5. 一个 Shard 失败不得阻塞其他 Shard；Scheduler 队列和 Worker 数必须有上限。
6. 活动 Consumer 的 ACK 只取决于当前 Active Index 的持久化结果，不取决于 warming Build。
7. warming Build 只能由本 Build 的 Durable Consumer 和 Snapshot Backfill 写入。
8. ViewIndex 行、Source Checkpoint、View Fence 和可选范围更新通过一个 Apply 原子提交。
9. Source Checkpoint 只为 View 实际依赖的 `{shard_id, dataset_id}` 更新，不为无关 Dataset 制造空 Apply。
10. MERGE 只改来源 Dataset 拥有的列；缺行时整批不写、不推进 Checkpoint。
11. REPLACE 必须携带完整 View 行；DELETE 只携带 RowKey。
12. Build Consumer 必须先于 Snapshot Head 获取而存在，确保回填期间事件不会丢失。
13. Snapshot Cursor 只能继续读取原 Snapshot；Snapshot 丢失必须从新 Build 重来。
14. 激活时 warming Source Checkpoint 必须达到 Barrier Head Vector，且切换使用 Metadata CAS。
15. Schema Fence、Merge 后完整行和 Payload 大小必须由 DataShard 最终验证。
16. RPC 错误分类只依赖类型化错误，不解析字符串，不向客户端暴露内部路径或底层错误。
17. Cursor 只推进到实际返回的最后一行；ASC/DESC、跨 Shard 和重复排序键均不能漏数或重数。
18. Cache 更新失败不能把已提交的 SQLite 写伪装成失败；失效后必须回源 SQL。
19. Dataset 首次写入后不允许改变放置规则；需要新拓扑时创建新 Dataset 并显式搬运和切换。
20. `physical_bytes` 只属于运维指标，不进入 ViewIndex 一致性协议。

## 范围外事项

- 不实现 PrimaryStore HA、DataShard 副本、自动 Failover、在线 Rebalance 或跨 Shard 事务。
- 不实现跨 DataShard 的全局一致 Snapshot；Build 使用每个来源 Shard 的 Snapshot Head Vector。
- 不实现旧数据迁移、旧 Proto Alias、旧 RPC Adapter 或旧配置兼容。
- 不实现 Archive 合规 PURGE；另立包含 Manifest、对象删除、保留期和审计证明的设计。
- 不以拆分前端大文件、调整 UI 或重写网关作为本轮 Storage 一致性整改的前置任务；仅同步受协议变化影响的调用方。

## 实施顺序

| 阶段 | Tasks | 退出条件 |
| --- | --- | --- |
| A. 协议与活动链路止血 | 1-5 | 旧写入口清零；瞬时失败可恢复；非法或超限事实不提交 |
| B. Source Lane 与可证明重建 | 6-9 | 活动/重建失败域隔离；Snapshot + Catch-up + Barrier E2E 通过 |
| C. 读取和 Metadata 扩展性 | 10-11 | 大于 10,000 行分页稳定；Metadata 不再全表分页和全量缓存 |
| D. 边界、运维和收口 | 12-13 | Device/物理 Target 清零；故障可观测、可审计修复；两轮 Review 和全量门禁通过 |

### Task 1: 建立隔离工作树和审查基线

**Files:**
- Read: `docs/superpowers/plans/2026-07-19-storage-consistency-review-remediation.md`
- Read: `docs/superpowers/specs/2026-07-18-storage-primary-shard-boundary-design.md`
- Read: `modules/storage/test/view_derivation_reliability_test.go`
- Read: `scripts/test-storage-boundary-contract.sh`
- Read: `scripts/test-storage-consistency-contract.sh`

- [ ] **Step 1: 从最新 main 创建独立 Worktree**

```bash
git fetch origin
git worktree add ../moox-storage-consistency -b codex/storage-consistency origin/main
cd ../moox-storage-consistency
git status --short --branch
```

Expected: 新工作区干净，分支基于最新 `origin/main`。不得在本计划文档提交所在工作区直接实施大改。

- [ ] **Step 2: 记录当前绿灯基线**

```bash
(cd modules/storage && env GOCACHE=/tmp/moox-gocache go test -count=1 ./...)
bash scripts/test-storage-boundary-contract.sh
bash scripts/test-storage-consistency-contract.sh
make verify
```

Expected: 命令通过。将输出保存在实施会话记录中，但不据此关闭任何 CR 发现。

- [ ] **Step 3: 建立实现检查表**

将本计划所有 Checkbox 作为唯一完成清单。每个后续 Task 必须遵循：先写失败测试、确认失败原因、实现最小正确改动、运行窄测试、运行 Storage 全量测试、提交。

### Task 2: 删除 ViewIndex 双写协议并完成 Proto 文件最终命名

**Files:**
- Rename: `modules/storage/proto/store.proto` -> `modules/storage/proto/data_shard.proto`
- Rename: `modules/storage/proto/view.proto` -> `modules/storage/proto/data_view.proto`
- Modify: `modules/storage/proto/view_index.proto`
- Modify: `modules/storage/proto/Makefile`
- Regenerate: `modules/storage/proto/storagegen/*`
- Modify: `modules/storage/internal/service/viewindex/engine.go`
- Modify: `modules/storage/internal/service/viewindex/client.go`
- Modify: `modules/storage/internal/service/viewindex/duckdb/view_store_write.go`
- Modify: `modules/storage/internal/service/viewindex/bleve/index.go`
- Modify: `modules/storage/internal/service/viewindex/engine_test.go`
- Modify: `modules/storage/internal/service/viewindex/client_test.go`
- Modify: `modules/storage/internal/service/viewindex/batch_write_test.go`
- Modify: `modules/storage/internal/service/viewindex/duckdb/view_store_test.go`
- Modify: `modules/storage/internal/service/viewindex/bleve/index_test.go`
- Modify: `docs/协议设计.md`
- Modify: `docs/架构总览.md`
- Modify: `scripts/test-storage-boundary-contract.sh`

- [ ] **Step 1: 先让边界测试拒绝旧协议**

在 `scripts/test-storage-boundary-contract.sh` 增加活动源码扫描，拒绝：

```text
store.proto
view.proto
ViewIndexStats.physical_bytes
viewindex.BatchWrite
Client.Write(
Engine.Write(
```

Expected: 在旧代码上脚本失败，并准确列出命中位置；注释中的“禁止旧名”不计为活动引用。

- [ ] **Step 2: 一次完成 Proto 文件重命名**

更新 `PROTO_FILES`、Proto import、生成文件源名、文档和构建脚本。删除旧生成文件，不保留同内容的两套生成代码。

```bash
make -C modules/storage/proto clean all
rg -n '(^|[^[:alnum:]_])(store|view)\.proto([^[:alnum:]_]|$)' modules/storage docs --glob '!docs/superpowers/**'
```

Expected: 生成成功；活动代码和现行文档零命中。

- [ ] **Step 3: ViewIndex 只保留一个写入口**

删除旧 `BatchWrite`、`Client.Write` 和 DuckDB/Bleve 直接 `Write` 路径。所有调用方构造 `ViewIndexApplyBatch` 并调用 `Apply`。禁止以 Wrapper 保留旧行为。

- [ ] **Step 4: 从一致性协议删除 physical_bytes**

删除 Proto/Go `ViewIndexStats.PhysicalBytes`。磁盘容量在 Task 13 通过 Prometheus 和文件系统采样提供。

- [ ] **Step 5: 运行和提交**

```bash
(cd modules/storage/proto/storagegen && env GOCACHE=/tmp/moox-gocache go test -count=1 ./...)
(cd modules/storage && env GOCACHE=/tmp/moox-gocache go test -count=1 ./internal/service/viewindex/...)
bash scripts/test-storage-boundary-contract.sh
git diff --check
git add --all
git commit -m "refactor(storage): finalize view index protocol"
```

### Task 3: 用可恢复的 Shard Scheduler 替换永久 Poison

**Files:**
- Create: `modules/storage/internal/service/viewbuilder/shard_scheduler.go`
- Create: `modules/storage/internal/service/viewbuilder/shard_scheduler_test.go`
- Modify: `modules/storage/internal/service/viewbuilder/service.go`
- Modify: `modules/storage/internal/service/viewbuilder/options.go`
- Modify: `modules/storage/internal/service/viewbuilder/service_test.go`
- Modify: `modules/storage/internal/service/viewbuilder/eventconsumer/producer_bus.go`
- Modify: `modules/storage/internal/service/viewbuilder/eventconsumer/producer_bus_test.go`
- Modify: `modules/storage/test/view_derivation_reliability_test.go`
- Modify: `modules/storage/cmd/server/health.go`

- [ ] **Step 1: 写出旧实现会失败的 Scheduler 测试**

覆盖以下行为：

1. Shard A 的 Sequence N 第一次返回瞬时错误，N 重投成功后 N+1 可以继续。
2. N 失败期间 N+1 返回类型化 `ErrShardWaitingForRetry`，不得执行 Apply。
3. Shard A 阻塞时 Shard B 正常执行。
4. 同 Shard 同 Sequence 并发投递只能有一个执行者，其他调用等待同一结果。
5. 队列达到上限时返回可重试过载错误，不创建无界 Goroutine。
6. 连续失败达到阈值后 Readiness 失败；成功重投后恢复。

核心 API 固定为同步完成语义：

```go
type ShardScheduler interface {
	Execute(ctx context.Context, shardID string, sequence uint64, apply func(context.Context) error) error
	Health() ShardSchedulerHealth
	Close(context.Context) error
}
```

- [ ] **Step 2: 修正“持久化成功后返回错误”的可靠性测试**

先用 `REPLACE` 创建完整目标行，再让第一次增量 Apply 在底层提交成功后注入返回错误。第二次投递必须命中幂等 Checkpoint，不增加行数、不回滚列值，并最终 ACK。禁止继续用“第一次因缺行失败”冒充该场景。

- [ ] **Step 3: 删除永久 blockedByShard**

Scheduler 只记录当前失败 Sequence、同 Sequence 的执行状态、连续失败次数和最近错误。成功应用相同 Sequence 后立即清除失败态。持久 Checkpoint 是重启后的事实源；内存状态不能成为永久熔断记录。

删除 `normalizeSubscriberOptions` 把 `MaxInFlight` 强制改成 1 的全局串行。Transport 可以按配置接收多个在途消息，但所有执行必须先进入有界 Shard Scheduler；不得用提高 JetStream 并发绕开同 Shard 顺序。

- [ ] **Step 4: ACK/NAK 等待真实持久化结果**

Event Consumer Handler 同步等待 Scheduler 和 ViewIndex Apply 完成：成功才 ACK；任何可重试错误都 NAK；Context Cancel 按 JetStream 关闭语义处理，不得先 ACK 后异步写。

- [ ] **Step 5: 收紧 Readiness**

Readiness 至少检查 Consumer 运行状态、Scheduler 过载、持续失败阈值和最后成功时间。单次瞬时错误不立即摘流，但超过配置阈值必须失败。

- [ ] **Step 6: 运行和提交**

```bash
(cd modules/storage && env GOCACHE=/tmp/moox-gocache go test -race -count=1 ./internal/service/viewbuilder/... ./test -run 'Shard|Retry|PostWrite')
git diff --check
git add --all
git commit -m "fix(storage): make shard retries recoverable"
```

### Task 4: 统一 Payload、Outbox 背压和发布可观测性

**Files:**
- Modify: `modules/storage/internal/config/loader.go`
- Modify: `modules/storage/config/storage.yaml`
- Modify: `packages/jetstream/config.go`
- Modify: `modules/storage/internal/service/datashard/local.go`
- Modify: `modules/storage/internal/service/datashard/pebble/outbox.go`
- Modify: `modules/storage/internal/service/datashard/outbox_relay.go`
- Create: `modules/storage/internal/service/datashard/shard_metrics.go`
- Modify: `modules/storage/internal/service/datashard/local_test.go`
- Modify: `modules/storage/internal/service/datashard/outbox_relay_test.go`
- Modify: `modules/storage/internal/service/datashard/pebble/outbox_test.go`

- [ ] **Step 1: 写 Payload 不可发布回归测试**

配置 Publisher Max Payload 为 8 MiB，构造最终 `MooxMessage` 超过该值但小于旧 16 MiB 的 Merge。断言：

- 返回类型化 `BATCH_TOO_LARGE`。
- 事实行、Shard Head、Dataset Head 和 Outbox 均未变化。
- 后续小消息可以正常提交和发布。

- [ ] **Step 2: 只保留一个 Max Payload 配置源**

`storage.eventbus.max_payload_bytes` 是 Publisher 和 DataShard Validator 的共同输入；删除 Pebble 层硬编码 `16 << 20`。配置缺失使用 JetStream 包定义的同一默认值，配置冲突启动失败。

- [ ] **Step 3: 在 Pebble Commit 前验证最终消息**

先完成 Merge 和最终消息编码，再比较实际字节数。只有校验通过才创建提交 Batch。不得“先写事实，再让 Relay 发现发不出去”。

- [ ] **Step 4: 所有写路径执行 Outbox 背压**

移除 `len(message) > 0` 之类的旁路条件。正常由 DataShard 内部生成消息的 Merge/Delete 也必须检查 Entry Count、Bytes 和 Oldest Age 阈值。

- [ ] **Step 5: 增加 Shard 级指标**

至少暴露：

```text
storage_outbox_entries{shard_id}
storage_outbox_bytes{shard_id}
storage_outbox_oldest_age_seconds{shard_id}
storage_outbox_publish_failures_total{shard_id,reason}
storage_outbox_head_sequence{shard_id}
storage_outbox_last_published_sequence{shard_id}
storage_datashard_payload_rejections_total{shard_id}
```

- [ ] **Step 6: 运行和提交**

```bash
(cd modules/storage && env GOCACHE=/tmp/moox-gocache go test -race -count=1 ./internal/service/datashard/... ./internal/config/...)
git diff --check
git add --all
git commit -m "fix(storage): align outbox payload boundaries"
```

### Task 5: 把严格 Merge、Schema Fence 和类型化错误落到 DataShard

**Files:**
- Modify: `packages/commonpb/moox_common.proto`
- Modify: `modules/storage/proto/metadata.proto`
- Modify: `modules/storage/proto/data_shard.proto`
- Regenerate: `packages/commonpb/*`, `modules/storage/proto/storagegen/*`
- Modify: `modules/storage/internal/typedvalue/factvalue.go`
- Modify: `modules/storage/internal/typedvalue/factvalue_test.go`
- Create: `modules/storage/internal/service/datashard/schema_validator.go`
- Create: `modules/storage/internal/service/datashard/schema_validator_test.go`
- Modify: `modules/storage/internal/service/datashard/pebble/committed.go`
- Modify: `modules/storage/internal/service/primarystore/schema/validator.go`
- Modify: `modules/storage/internal/service/primarystore/schema/validator_test.go`
- Modify: `modules/storage/internal/service/primarystore/data.go`
- Modify: `modules/storage/internal/service/primarystore/data_test.go`
- Modify: `modules/storage/internal/retinfo/retinfo.go`
- Modify: `modules/storage/internal/retinfo/metadata.go`
- Modify: `modules/storage/internal/retinfo/retinfo_test.go`
- Modify: `modules/storage/internal/retinfo/metadata_test.go`

- [ ] **Step 1: 增加完整非法输入矩阵**

Table Test 覆盖：空批次、重复 RowKey、重复列、跨 Space、跨 Dataset、未知列、Value Oneof 与声明类型不符、非法 TIME、非法 JSON、NaN/Inf、错误 List 元素、Null/Remove/Required 冲突、Merge 后缺 Required、单行超限、批次超限、旧 Schema Version、同 Version 不同 Hash。

每个 Case 都断言事实、Heads 和 Outbox 未提交。

- [ ] **Step 2: 定义结构化 Schema Fence**

Dataset 增加单调 `schema_version`，Schema 变更在 Metadata 事务中同时更新 Version 和 Hash。PrimaryStore 根据 Metadata 构造 `DatasetSchemaFence`，DataShard 不再只接收一个无法解释的字符串 Hash。

DataShard 必须以规范化 Column Constraint 重新计算 Hash，并验证它等于 Fence 中的 `schema_hash`；不能信任调用方提供但内容不匹配的 Version/Hash/Constraint 组合。

- [ ] **Step 3: DataShard 执行最终 Merge 校验**

处理顺序固定为：验证请求同一 Dataset -> 验证 Fence -> 读取旧行 -> 应用 Set/Null/Remove -> 验证完整 TypedValue 和 Required -> 编码 RowsCommitted -> 验证最终 Payload -> 在一个 Batch 中提交事实、Heads 和 Outbox。

- [ ] **Step 4: PrimaryStore 只做 Preflight**

公共边界为每批最多 1,000 行、请求 4 MiB、单行 1 MiB。Preflight 失败直接返回；DataShard 返回 `BATCH_TOO_LARGE` 时原样返回，不做隐式拆批和部分提交。

- [ ] **Step 5: 使用类型化错误**

定义稳定错误类型或 Sentinel，集中映射到 `pb.RetInfo`。删除 `strings.Contains(err.Error(), ...)` 和直接把底层 `err.Error()` 返回客户端的逻辑；日志保留完整 Cause，客户端只得到稳定安全消息。

- [ ] **Step 6: 运行和提交**

```bash
make -C packages/commonpb clean all
make -C modules/storage/proto clean all
(cd packages/commonpb && env GOCACHE=/tmp/moox-gocache go test -count=1 ./...)
(cd modules/storage/proto/storagegen && env GOCACHE=/tmp/moox-gocache go test -count=1 ./...)
(cd modules/storage && env GOCACHE=/tmp/moox-gocache go test -race -count=1 ./internal/typedvalue ./internal/retinfo ./internal/service/datashard/... ./internal/service/primarystore/...)
git diff --check
git add --all
git commit -m "fix(storage): enforce schema at shard commit"
```

### Task 6: 增加 Dataset Source Head 和 View Source Checkpoint

**Files:**
- Modify: `modules/storage/proto/data_shard.proto`
- Modify: `modules/storage/proto/view_index.proto`
- Regenerate: `modules/storage/proto/storagegen/*`
- Modify: `modules/storage/internal/service/datashard/pebble/key.go`
- Modify: `modules/storage/internal/service/datashard/pebble/committed.go`
- Modify: `modules/storage/internal/service/datashard/service.go`
- Modify: `modules/storage/internal/service/viewindex/batch_write.go`
- Modify: `modules/storage/internal/service/viewindex/duckdb/view_store_apply.go`
- Modify: `modules/storage/internal/service/viewindex/bleve/index.go`
- Modify: `modules/storage/internal/service/viewbuilder/checkpoint.go`
- Modify: `modules/storage/internal/service/viewbuilder/apply.go`
- Modify: `modules/storage/internal/service/datashard/pebble/store_test.go`
- Modify: `modules/storage/internal/service/datashard/service_test.go`
- Modify: `modules/storage/internal/service/viewindex/batch_write_test.go`
- Modify: `modules/storage/internal/service/viewindex/duckdb/view_store_test.go`
- Modify: `modules/storage/internal/service/viewindex/bleve/index_test.go`
- Create: `modules/storage/internal/service/viewbuilder/checkpoint_test.go`
- Modify: `modules/storage/internal/service/viewbuilder/apply_test.go`

- [ ] **Step 1: 先写 Source Lane 行为测试**

在同一 Shard 交错写 A1(seq=10)、B1(seq=11)、A2(seq=12)。依赖 A 的 View 允许 Checkpoint 从 10 跳到 12，不为 B1 做空 Apply；依赖 B 的 View 独立停在 11。旧 `shard_id` 单维 Checkpoint 实现应失败。

- [ ] **Step 2: 原子保存 Dataset Head**

每次事实提交在同一 Pebble Batch 中更新：

```text
shard/head -> sequence
dataset/head/{dataset_id} -> sequence
```

新增批量 `GetDatasetShardHeads`，请求必须携带期望 Shard ID 和 Dataset ID 列表。

- [ ] **Step 3: ViewIndex Checkpoint 改为 Source Lane**

把 `ViewIndexShardCheckpointUpdate` 替换为 `ViewIndexSourceCheckpointUpdate`。DuckDB/Bleve 的 CAS 规则为：当前值必须等于 Expected，Last 必须大于 Expected；删除 `Last == Expected + 1` 假设。

- [ ] **Step 4: ViewBuilder 只更新依赖项**

收到 Dataset A 事件，只解析依赖 A 的 View 并更新其 Source Checkpoint。删除遍历全 Space View 并提交 Checkpoint-only Batch 的路径。

- [ ] **Step 5: 运行和提交**

```bash
make -C modules/storage/proto clean all
(cd modules/storage/proto/storagegen && env GOCACHE=/tmp/moox-gocache go test -count=1 ./...)
(cd modules/storage && env GOCACHE=/tmp/moox-gocache go test -race -count=1 ./internal/service/datashard/... ./internal/service/viewindex/... ./internal/service/viewbuilder/...)
git diff --check
git add --all
git commit -m "feat(storage): track dataset source checkpoints"
```

### Task 7: 为每次 Build 创建独立 Durable Consumer

**Files:**
- Modify: `modules/storage/proto/metadata.proto`
- Regenerate: `modules/storage/proto/storagegen/*`
- Create: `modules/storage/internal/service/viewbuilder/eventconsumer/build_consumer.go`
- Create: `modules/storage/internal/service/viewbuilder/eventconsumer/build_consumer_test.go`
- Create: `modules/storage/internal/service/viewbuilder/build_registry.go`
- Create: `modules/storage/internal/service/viewbuilder/build_registry_test.go`
- Modify: `packages/jetstream/consumer.go`
- Modify: `packages/jetstream/consumer_test.go`
- Modify: `modules/eventbus/internal/config/config_types.go`
- Modify: `modules/eventbus/internal/config/config_defaults.go`
- Modify: `modules/eventbus/internal/config/config_validation.go`
- Modify: `modules/eventbus/internal/config/config_test.go`
- Modify: `modules/eventbus/internal/registry/registry.go`
- Modify: `modules/eventbus/internal/registry/registry_test.go`
- Modify: `modules/eventbus/config/app.yaml`
- Modify: `modules/factor/internal/trigger/nats.go`
- Modify: `modules/factor/internal/trigger/nats_test.go`
- Modify: `modules/storage/internal/service/viewbuilder/service.go`
- Modify: `modules/storage/internal/service/viewbuilder/apply.go`
- Modify: `modules/storage/internal/config/loader.go`
- Modify: `modules/storage/internal/config/loader_test.go`
- Modify: `modules/storage/config/storage.yaml`
- Modify: `modules/storage/config/storage.primary.yaml`
- Modify: `modules/storage/cmd/server/view_runtime.go`

- [ ] **Step 1: 写活动和 warming 失败隔离测试**

同一个事件写 Active Index 成功、写 warming Index 失败时，活动 Consumer 必须 ACK，Active Source Checkpoint 前进；Build 记录变成 FAILED，warming Checkpoint 不前进。旧实现应因共享写入集合而 NAK。

- [ ] **Step 2: 持久化 Build Consumer 身份**

`ViewIndexBuild` 增加 `build_id`、`durable_name`、`consumer_created_at`、`last_error_code`。Durable Name 由稳定、可重建的 ID 组成，不使用随机进程内名称：

```text
storage-view-build-{view_id}-{build_id}
```

扩展 JetStream Consumer Config 支持多个 `FilterSubjects`。每个 Build 只订阅其来源 Shard 的两类 RowsCommitted Subject，不用全局 `moox.storage.>` Consumer；Durable Name 必须经过 Token 校验并限制长度。

- [ ] **Step 3: 活动路径只解析 Active Index**

删除 `BuildIndexWritable(BUILDING/CATCHING_UP)` 参与活动写入的逻辑。warming Index 只接受 Build Consumer 和 Snapshot Backfill 的 Apply。

- [ ] **Step 4: Build Consumer 可恢复**

进程重启后从 Metadata 枚举非终态 Build，Ensure Durable Consumer 存在并从 JetStream 已保存位置继续。创建和删除都必须幂等。

PREPARING/BUILDING 阶段只创建 Durable 并让消息留在 JetStream，不启动 Apply Loop；只有 Snapshot Backfill 基线 Checkpoint 已原子提交后才开始拉取并进入 CATCHING_UP，避免未建立基线时交错写 warming Index。

MOOX_STORAGE Stream 改为可配置且受契约测试保护的 `InterestPolicy + DiscardNew`；匹配 Consumer 全部 ACK 后消息才能释放，达到 Max Bytes/Msgs 时新 Publish 留在 DataShard Outbox 重试，不能删除旧 Pending。配置校验还必须证明 `max_build_duration + safety_margin <= stream_max_age`。若 Build 超过受保证的保留窗口或 Consumer Pending 出现不可解释的缺口，立即 FAILED，禁止带缺口激活。

- [ ] **Step 5: 审计 MOOX_STORAGE 的所有 Consumer**

Active View、Build 和 Archive 使用无限重试及健康告警。Factor 对不可解析消息 `Term`，正常消息持久接收后 ACK；其 Consumer 不得因有限 `MaxDeliver` 留下永远无法再投递、却持续占用 Interest 的消息。契约测试必须证明：Build 未 ACK 时容量压力拒绝新 Publish 而不淘汰 Pending；Build 删除后，其他匹配 Consumer 全部 ACK 的消息可以释放空间。

- [ ] **Step 6: 运行和提交**

```bash
make -C modules/storage/proto clean all
(cd modules/storage/proto/storagegen && env GOCACHE=/tmp/moox-gocache go test -count=1 ./...)
(cd packages/jetstream && env GOCACHE=/tmp/moox-gocache go test -race -count=1 ./...)
(cd modules/eventbus && env GOCACHE=/tmp/moox-gocache go test -race -count=1 ./...)
(cd modules/factor && env GOCACHE=/tmp/moox-gocache go test -race -count=1 ./internal/trigger/...)
(cd modules/storage && env GOCACHE=/tmp/moox-gocache go test -race -count=1 ./internal/service/viewbuilder/... ./cmd/server/...)
git diff --check
git add --all
git commit -m "feat(storage): isolate view build consumers"
```

### Task 8: 为 DataShard 增加有界 Pebble Snapshot

**Files:**
- Modify: `modules/storage/proto/data_shard.proto`
- Regenerate: `modules/storage/proto/storagegen/*`
- Create: `modules/storage/internal/service/datashard/pebble/snapshot.go`
- Create: `modules/storage/internal/service/datashard/pebble/snapshot_test.go`
- Create: `modules/storage/internal/service/datashard/snapshot_registry.go`
- Create: `modules/storage/internal/service/datashard/snapshot_registry_test.go`
- Modify: `modules/storage/internal/service/datashard/service.go`
- Modify: `modules/storage/internal/service/datashard/local.go`
- Modify: `modules/storage/internal/config/loader.go`
- Modify: `modules/storage/config/storage.yaml`

- [ ] **Step 1: 定义 Snapshot RPC**

```proto
rpc BeginShardSnapshot(BeginShardSnapshotReq) returns (BeginShardSnapshotRsp);
rpc EndShardSnapshot(EndShardSnapshotReq) returns (EndShardSnapshotRsp);

message BeginShardSnapshotRsp {
  RetInfo ret_info = 1;
  string snapshot_id = 2;
  string shard_id = 3;
  uint64 shard_head = 4;
  repeated DatasetShardHead dataset_heads = 5;
  int64 expires_at_unix_ms = 6;
}
```

`ScanRowsReq` 增加可选 `snapshot_id`。指定后所有 Page 必须从同一个 Pebble Snapshot 读取。

- [ ] **Step 2: 写并发快照测试**

Begin 后继续 Merge/Delete，Snapshot 多页扫描必须只看到 Begin 时的行集合和值；普通 Scan 看到最新值。End 后继续使用该 ID 返回稳定 `SNAPSHOT_NOT_FOUND`。

- [ ] **Step 3: 实现有界 Registry**

使用 `db.NewSnapshot()`；Registry 配置最大并发 Snapshot、TTL 和最大总存活时间。Begin 超限 Fail Closed，End 幂等，进程关闭释放全部 Snapshot。不得把 Pebble Snapshot 指针写入 Metadata。

- [ ] **Step 4: Snapshot 返回同一读点的 Heads**

在创建 Snapshot 后从该 Snapshot 读取 Shard Head 和请求 Dataset Heads，保证返回的 Head Vector 与扫描数据属于同一 Pebble Sequence。

- [ ] **Step 5: 运行和提交**

```bash
make -C modules/storage/proto clean all
(cd modules/storage/proto/storagegen && env GOCACHE=/tmp/moox-gocache go test -count=1 ./...)
(cd modules/storage && env GOCACHE=/tmp/moox-gocache go test -race -count=1 ./internal/service/datashard/...)
git diff --check
git add --all
git commit -m "feat(storage): add shard snapshots for rebuilds"
```

### Task 9: 把 View 重建改为 Snapshot + Catch-up + Activation Barrier

**Files:**
- Move/Rewrite: `modules/storage/internal/service/dataview/build_cursor.go` -> `modules/storage/internal/service/viewbuilder/rebuild_cursor.go`
- Move/Rewrite: build ownership from `modules/storage/internal/service/dataview/maintenance.go` to `modules/storage/internal/service/viewbuilder/rebuild_manager.go`
- Move/Rewrite: build scheduling from `modules/storage/internal/service/dataview/schedule.go` to `modules/storage/internal/service/viewbuilder/rebuild_schedule.go`
- Create: `modules/storage/internal/service/viewbuilder/rebuild_manager_test.go`
- Create: `modules/storage/internal/service/viewbuilder/activation_barrier.go`
- Create: `modules/storage/internal/service/viewbuilder/activation_barrier_test.go`
- Modify: `modules/storage/internal/service/viewbuilder/source_reader.go`
- Modify: `modules/storage/internal/service/primarystore/factreader.go`
- Modify: `modules/storage/internal/service/metadata/sqlite/crud_view_index.go`
- Modify: `modules/storage/proto/metadata.proto`
- Modify: `modules/storage/test/view_index_switch_test.go`

- [ ] **Step 1: 固定 Build 状态机**

只允许以下单向转换：

```text
PREPARING -> BUILDING -> CATCHING_UP -> READY_TO_ACTIVATE -> ACTIVE
     |           |            |                |
     +-----------+------------+----------------+-> FAILED
```

每次转换使用 `build_id + expected_state` CAS。FAILED Build 不得原地复活；重试创建新 Build 和新 Index。

- [ ] **Step 2: Durable Consumer 先于 Snapshot**

PREPARING 顺序不可交换：先 Ensure Build Consumer，再 Begin 每个来源 Shard Snapshot，最后在一个 Metadata 事务中保存 Snapshot IDs、Head Vector 和 BUILDING 状态。

- [ ] **Step 3: 从固定 Snapshot 批量 REPLACE**

Cursor 必须包含 `build_id + snapshot_id + source + last_returned_key`。每批 ViewIndex REPLACE 成功后才保存 Cursor。所有来源完成后，用空 RowWrite 的原子 Apply 设置 Snapshot Source Checkpoint 和 `indexed_from/indexed_to` 基线。

- [ ] **Step 4: Catch-up 只应用 Snapshot Head 之后的事件**

Build Consumer 对每个来源：`sequence <= snapshot_head` 直接 ACK；更大 Sequence 使用正常 MERGE/DELETE 和 Source Checkpoint CAS。Build 失败只更新 Build 状态，不影响 Active Consumer。

- [ ] **Step 5: 实现 View 级 Activation Barrier**

Barrier 必须与活动 Handler 共用 View ID 锁：

1. 阻止该 View 新的 Active Apply，并等待在途 Apply 完成。
2. 从各 DataShard 读取固定 Barrier Head Vector。
3. 让 Build Consumer 追平该 Vector。
4. Metadata CAS `old_active_index_id -> new_active_index_id`。
5. 释放锁；等待中的活动事件重新解析 Active Index 并写新 Index。

不得用“JetStream 当前看起来没有 Pending”代替 Source Head Vector。

- [ ] **Step 6: 明确失败清理**

- Snapshot 回填完成前丢失：FAILED，释放 Consumer/Snapshot/Index，创建新 Build。
- Catch-up 失败：保留 Durable 位置和错误供诊断，可由明确 Retry 操作创建新执行 Attempt，但不改 Build ID 和已提交 Checkpoint。
- Activation CAS 失败：重新读取 Metadata；若不是本 Index 已激活，则 FAILED，不覆盖其他 Build。
- 成功激活：删除 Build Consumer，End Snapshot，旧 Index 进入带宽限速的延迟清理队列。

- [ ] **Step 7: E2E 覆盖持续写入重建**

在 Backfill、Catch-up 和 Barrier 三个阶段持续写入两个 Dataset，最终断言新 Active Index 行值等于 PrimaryStore 当前事实，Source Checkpoint 达到或超过 Barrier Head，无重复、无漏更新；注入 warming 失败时旧 Active 查询持续更新。

- [ ] **Step 8: 运行和提交**

```bash
(cd modules/storage && env GOCACHE=/tmp/moox-gocache go test -race -count=1 ./internal/service/viewbuilder/... ./internal/service/dataview/... ./test -run 'Build|Snapshot|CatchUp|Switch|Barrier')
git diff --check
git add --all
git commit -m "feat(storage): rebuild views behind sequence barriers"
```

### Task 10: 重写 PrimaryStore 跨 Target 权威分页

**Files:**
- Modify: `modules/storage/proto/data_shard.proto`
- Create: `modules/storage/internal/service/primarystore/cursor.go`
- Create: `modules/storage/internal/service/primarystore/cursor_test.go`
- Create: `modules/storage/internal/service/primarystore/merge_page.go`
- Create: `modules/storage/internal/service/primarystore/merge_page_test.go`
- Modify: `modules/storage/internal/service/primarystore/data.go`
- Modify: `modules/storage/internal/service/datashard/pebble/committed.go`
- Modify: `modules/storage/internal/service/primarystore/data_test.go`

- [ ] **Step 1: 定义不可混用的结构化 Cursor**

内部 Proto 至少包含：版本、查询指纹、ASC/DESC、Page Kind、每个 `shard_id` 的最后实际返回 Key。编码使用 Deterministic Proto + Base64URL；解码必须限制总长度、Target 数量和字段长度。

- [ ] **Step 2: Cursor 绑定查询**

查询指纹覆盖 Space、Dataset、Key Prefix、时间/版本范围、排序字段、方向和投影。用旧 Cursor 改变任一查询条件必须返回 `INVALID_CURSOR`。

- [ ] **Step 3: 实现有界 K-way Merge**

每个 Target 从其最后返回 Key 之后有界读取，使用 Heap 按完整稳定排序键合并。只有某行实际进入响应时才推进其 Target Cursor；底层预取但未返回的行下页必须重新可见。

- [ ] **Step 4: 删除 scanAllPrimaryRows 和 10,000 行上限**

公共请求的内存复杂度保持 `O(shard_count * page_size)`，不得先收集全部行再排序。Page Size 设硬上限并在入口验证。

- [ ] **Step 5: 完整分页矩阵**

使用至少 20,001 行、3 Shard、重复业务排序值，覆盖 Page Size 1/25/999，ASC/DESC、空 Shard、单 Shard、跨页写入和非法 Cursor。静态数据场景必须完整遍历且无重复；并发写场景只承诺 Keyset 语义，不宣称 Snapshot Isolation。

- [ ] **Step 6: 运行和提交**

```bash
(cd modules/storage && env GOCACHE=/tmp/moox-gocache go test -race -count=1 ./internal/service/primarystore/... ./internal/service/datashard/...)
git diff --check
git add --all
git commit -m "fix(storage): stream authoritative shard pages"
```

### Task 11: 实现 Metadata 真分页、有界 Cache 和提交后失效

**Files:**
- Modify: `modules/storage/internal/service/metadata/store.go`
- Modify: `modules/storage/internal/service/metadata/sqlite/crud_helpers.go`
- Modify: `modules/storage/internal/service/metadata/sqlite/crud_store.go`
- Modify: `modules/storage/internal/service/metadata/sqlite/crud_space.go`
- Modify: `modules/storage/internal/service/metadata/sqlite/crud_dataset.go`
- Modify: `modules/storage/internal/service/metadata/sqlite/crud_view.go`
- Modify: `modules/storage/internal/service/metadata/sqlite/crud_view_index.go`
- Modify: `modules/storage/internal/service/metadata/cache/store.go`
- Modify: `modules/storage/internal/service/primarystore/service.go`
- Modify: `modules/storage/internal/service/primarystore/metadata_catalog.go`
- Modify: `modules/storage/internal/service/primarystore/metadata_infra.go`
- Modify: `modules/storage/internal/service/primarystore/metadata_space_view.go`
- Modify: `modules/storage/internal/service/primarystore/metadata_view_index.go`
- Modify: `modules/storage/internal/bootstrap/metadata/schema.go`
- Modify: `modules/storage/internal/service/metadata/sqlite/crud_test.go`
- Modify: `modules/storage/internal/service/metadata/sqlite/store_test.go`
- Modify: `modules/storage/internal/service/metadata/cache/store_test.go`
- Modify: `modules/storage/internal/service/primarystore/metadata_catalog_test.go`
- Modify: `modules/storage/internal/service/primarystore/metadata_infra_test.go`
- Modify: `modules/storage/internal/service/primarystore/metadata_space_view_test.go`
- Modify: `modules/storage/internal/service/primarystore/metadata_view_index_test.go`

- [ ] **Step 1: 先写大集合性能契约测试**

插入至少 20,001 条 ArchiveFile、ViewIndexBuild 或审计记录，分页遍历并断言 SQL 每次只返回 Page Size 上限附近的行；测试不得通过公开 `queryMessages + pageItems` 帮助函数间接分页。

- [ ] **Step 2: 无界集合统一 Keyset Pagination**

使用稳定 `(created_at, id)` 或领域排序键加 ID。小目录即使使用 OFFSET 也必须在 Schema/Service 层有明确数量上限；否则同样改 Keyset。

- [ ] **Step 3: Cache 只保存有界目录**

Cache Snapshot 排除 ArchiveFile、Build History、Audit 和其他随运行增长的对象。`c_attrs_json` 只保存用户 Attributes，不再序列化完整 Proto。

- [ ] **Step 4: 提交后只失效目标 Key**

SQLite Commit 成功后更新或失效对应 Cache Entry。Cache 更新失败记录指标并把 Entry 标为 Stale，后续读取回源 SQL；不得向调用方返回“写失败”。删除每次 CRUD 后全量 `refreshMetadataCache`。

- [ ] **Step 5: Snapshot 在单个只读事务中构造**

需要一致目录 Snapshot 时，所有表读取共用同一个 SQLite Read Transaction；不得跨多次独立查询拼出时间点不一致的 Snapshot。

- [ ] **Step 6: SQL 是时间戳事实源**

Create/Update 返回 SQL 实际写入的 `created_at/updated_at`。Cache 不生成替代时间戳。

- [ ] **Step 7: 运行和提交**

```bash
(cd modules/storage && env GOCACHE=/tmp/moox-gocache go test -race -count=1 ./internal/service/metadata/... ./internal/service/primarystore/...)
git diff --check
git add --all
git commit -m "fix(storage): bound metadata paging and cache"
```

### Task 12: 删除通用 Device 和调用方物理路由能力

**Files:**
- Modify: `modules/storage/proto/metadata.proto`
- Modify: `modules/storage/proto/data_shard.proto`
- Regenerate: `modules/storage/proto/storagegen/*`
- Modify: `modules/storage/internal/service/primarystore/shardrouter/resolver.go`
- Modify: `modules/storage/internal/service/metadata/sqlite/topology.go`
- Modify: `modules/storage/internal/bootstrap/metadata/schema.go`
- Modify: `modules/storage/internal/bootstrap/metadata/seed.go`
- Modify: `modules/storage/internal/config/loader.go`
- Modify: `modules/storage/config/storage.yaml`
- Modify: `modules/storage/config/storage.primary.yaml`
- Modify: `modules/storage/config/metadata.seed.yaml`
- Modify: `modules/storage/internal/config/loader_test.go`
- Modify: `modules/storage/cmd/server/runtime_config.go`
- Modify: `modules/storage/cmd/server/runtime_config_test.go`
- Modify: `modules/storage/cmd/server/view_runtime.go`
- Modify: `modules/cli/internal/command/metadata_implementation.go`
- Modify: `modules/cli/internal/command/metadata_test.go`
- Modify: `web/src/api/storage/metadata.ts`
- Modify: `web/src/api/storage/types.ts`
- Modify: `web/src/views/ops/storage/nodes.vue`
- Modify: `web/src/views/ops/storage/routes.vue`
- Modify: `examples/metadata-monitor-host-local-route.seed.yaml`
- Modify: `examples/metadata-monitor-metrics-local-route.seed.yaml`
- Modify: `examples/metadata-quant-initial.seed.yaml`
- Modify: `examples/platform-local.seed.yaml`
- Modify: `scripts/test-storage-boundary-contract.sh`
- Modify: `modules/storage/internal/service/primarystore/shardrouter/resolver_test.go`
- Modify: `modules/storage/internal/service/metadata/sqlite/crud_test.go`

- [ ] **Step 1: 先扩充旧拓扑扫描**

活动协议和实现必须拒绝：Proto/Go 类型 `Device`、`PrimaryStoreNode`、`PrimaryStoreRoute`，以及调用方提供 `engine/table/device_id/node_endpoint` 的 `ShardTarget`。扫描必须匹配声明和类型引用，不能误伤 `host_device` 等普通业务字段。Expected: 旧代码失败。

- [ ] **Step 2: 最终模型只保留 ShardNode/ShardRoute**

- `ShardNode` 描述受控 DataShard Endpoint 和固定 Shard ID。
- `ShardRoute` 描述 Dataset 到 Shard Pool 的放置规则和拓扑版本。
- 公共调用方只提供 Space、Dataset、RowKey 和可选 Expected Shard ID；物理 Endpoint 由 PrimaryStore 解析，DataShard 再验证自身 Shard ID。

- [ ] **Step 3: 冻结拓扑并明确变更路径**

首次事实提交同一事务标记 Dataset `topology_locked_at` 和 `topology_version`。锁定后拒绝节点、权重、Hash Pool 或 Shard 数变化。本项目不提供危险 Unlock RPC；新拓扑使用新 Dataset，显式回填、校验和切换调用方后再停用旧 Dataset。

- [ ] **Step 4: 删除通用 Device CRUD/表/UI**

彻底删除 Device Proto、SQLite 表、Cache、Service、Seed 和前端表单。需要表达 Archive/COS/DuckDB 等资源时使用所属领域的明确配置，不再复用 Device God Model。

- [ ] **Step 5: 一次更新所有调用方**

Admin、Gateway、CLI、Bench、Examples 和 Docs 使用最终 Shard 类型；不保留 JSON/YAML 旧字段别名。

- [ ] **Step 6: 运行和提交**

```bash
make -C modules/storage/proto clean all
(cd modules/storage/proto/storagegen && env GOCACHE=/tmp/moox-gocache go test -count=1 ./...)
(cd modules/storage && env GOCACHE=/tmp/moox-gocache go test -race -count=1 ./...)
(cd modules/admin && env GOCACHE=/tmp/moox-gocache go test -count=1 ./...)
(cd modules/cli && env GOCACHE=/tmp/moox-gocache go test -count=1 ./...)
pnpm --dir web exec vitest run
bash scripts/test-storage-boundary-contract.sh
git diff --check
git add --all
git commit -m "refactor(storage): make shard topology explicit"
```

### Task 13: 运维闭环、文档、E2E 和最终 Review

**Files:**
- Create: `modules/storage/cmd/cli/outbox.go`
- Create: `modules/storage/cmd/cli/outbox_test.go`
- Modify: `modules/storage/cmd/cli/main.go`
- Modify: `modules/storage/proto/data_shard.proto`
- Regenerate: `modules/storage/proto/storagegen/*`
- Modify: `modules/storage/internal/service/datashard/service.go`
- Modify: `modules/storage/internal/service/datashard/pebble/outbox.go`
- Modify: `modules/storage/internal/service/datashard/pebble/outbox_test.go`
- Modify: `modules/storage/cmd/server/health.go`
- Modify: `modules/storage/internal/service/datashard/shard_metrics.go`
- Modify: `modules/storage/internal/service/viewbuilder/view_metrics.go`
- Modify: `modules/storage/test/storage_consistency_contract_test.go`
- Modify: `modules/storage/test/view_derivation_reliability_test.go`
- Create: `modules/storage/test/view_rebuild_barrier_test.go`
- Modify: `scripts/test-storage-boundary-contract.sh`
- Modify: `scripts/test-storage-consistency-contract.sh`
- Modify: `docs/存储层架构.md`
- Modify: `docs/存储引擎架构.md`
- Modify: `docs/superpowers/specs/2026-07-18-storage-primary-shard-boundary-design.md`
- Modify: `modules/storage/README.md`

- [ ] **Step 1: 实现受审计 Outbox 修复命令**

通过只允许运维身份访问的 DataShard Repair RPC 提供只读 `inspect`、安全 `retry` 和默认禁用的 `force-skip`；CLI 不直接打开在线 Pebble。force-skip 必须校验 Shard/Sequence/Message ID，要求 `--reason` 与 `--confirm-data-loss`，并在删除 Outbox 项的同一 Pebble Batch 写入不可变 DeliveryGap 与 `REPAIR_REQUIRED`。Resolver 只有收到每个注册消费者的回填证据后才清除本地标记。

- [ ] **Step 2: 健康和指标形成闭环**

Readiness 覆盖 Outbox 超限/队首年龄、Scheduler 持续失败、Build Consumer 停滞、Snapshot 泄漏和 `REBUILD_REQUIRED`。增加 Build Lag、Source Checkpoint Lag、Snapshot Count/Age、Activation Failure 指标。

- [ ] **Step 3: 同步架构文档**

文档必须给出：

- 一个 Shard 内交错 Dataset Sequence 和 Source Checkpoint 示例。
- Active Consumer 与 Build Consumer 的失败域图。
- Durable-before-Snapshot、Backfill、Catch-up、Barrier、CAS Switch 时序。
- Schema Preflight 与 DataShard Final Validation 的边界。
- Keyset/K-way Merge Cursor 语义。
- force-skip 为什么会触发强制重建。
- 在线 DELETE 不等于 Archive PURGE，PURGE 为独立后续设计。

- [ ] **Step 4: 强化静态门禁**

两个 Contract Script 至少扫描：旧 Proto 文件、Device/PrimaryStoreRoute、旧 ViewIndex Write/BatchWrite、`ViewIndexStats.physical_bytes`、`blockedByShard`、BUILDING 进入 Active Writable、错误字符串分类、硬编码 16 MiB、全表 `queryMessages + pageItems`、活动 `scanAllPrimaryRows`。运维配置 `max_physical_bytes` 和 Prometheus 磁盘指标不属于禁止项。

- [ ] **Step 5: 运行 Storage E2E 场景**

必须使用真实 Pebble、JetStream、SQLite、DuckDB 和 Bleve，覆盖：

1. 活动 Apply 瞬时失败后同 Sequence 重投恢复。
2. Shard A 阻塞时 Shard B 继续。
3. 持久化成功但 ACK 前失败，重放幂等。
4. 超 Payload 和非法 TypedValue 在事实提交前拒绝。
5. 两 Dataset 持续写入期间重建并经 Barrier 切换。
6. warming 失败不影响旧 Active 查询和 ACK。
7. Snapshot 过期导致新 Build，不复用旧 Cursor。
8. 20,001 行跨 Shard ASC/DESC 分页无遗漏重复。
9. 20,001 条 Metadata 无界记录分页且 Cache 有界。
10. 非 Archive Dataset force-skip 后 Readiness 失败，View 重建提交证据后恢复；Archive 白名单 Dataset 还必须提交 Archive 回填证据。

- [ ] **Step 6: 第一轮独立 Review**

只提供最终设计、本计划、Diff 和测试证据，请 Reviewer 按 P0/P1/P2 检查事实提交、ACK/NAK、Scheduler、Snapshot、Build Consumer、Activation Barrier、Schema Fence、分页和 Metadata。修复全部确认问题并重跑相关测试。

- [ ] **Step 7: 第二轮独立 Review**

更换 Reviewer，从故障注入和恢复角度重新检查：进程重启、重复投递、Snapshot 失效、JetStream 停机、SQLite Commit 后 Cache 失败、Activation CAS 冲突、Outbox Poison。不得复用第一轮结论代替审查。

- [ ] **Step 8: 全量验证**

```bash
make -C modules/storage/proto clean all
(cd modules/storage/proto/storagegen && env GOCACHE=/tmp/moox-gocache go test -count=1 ./...)
(cd modules/storage && env GOCACHE=/tmp/moox-gocache go test -race -count=1 ./...)
(cd modules/storage && env GOCACHE=/tmp/moox-gocache go vet ./...)
(cd modules/admin && env GOCACHE=/tmp/moox-gocache go test -count=1 ./...)
(cd modules/gateway && env GOCACHE=/tmp/moox-gocache go test -count=1 ./...)
bash scripts/check-package-boundaries.sh
bash scripts/test-storage-boundary-contract.sh
bash scripts/test-storage-consistency-contract.sh
make verify
git diff --check
git status --short
```

Expected: 全部通过；命令不产生未提交的生成文件或格式变化。

- [ ] **Step 9: 最终提交、推送和同步证明**

```bash
git add --all
git commit -m "refactor(storage): complete consistency remediation"
git push -u origin codex/storage-consistency
git status --short --branch
git rev-parse HEAD
git rev-parse origin/codex/storage-consistency
```

Expected: 工作区干净，本地 HEAD 与远程分支一致。合并到 `main` 后再次运行 `make verify` 并证明 `main == origin/main`。

## 原计划到本计划映射

| 2026-07-18 原计划 | 本轮结论 | 新责任任务 |
| --- | --- | --- |
| Task 2-4 事实消息与 Outbox | 核心原子提交保留；Payload、背压和指标补齐 | Task 4-6、13 |
| Task 5 权威分页 | 原实现仍是全量扫描和简单 Target Cursor，必须重写 | Task 10 |
| Task 7 ViewBuilder 有序 ACK | 不能使用永久 Poison；必须按 Shard 可恢复 | Task 3 |
| Task 8 ViewIndex Apply/Checkpoint | Apply 原子性保留；Checkpoint 改为 Source Lane，删除双写入口 | Task 2、6 |
| Task 9 View 重建与范围 | 原 BUILDING Writable 设计推翻，改为独立 Consumer + Snapshot + Barrier | Task 7-9 |
| Task 10 Schema/Merge/Error | PrimaryStore Validator 不足，最终校验下沉 DataShard | Task 5 |
| Task 11 Shard 边界/拓扑锁 | 锁定方向保留；删除 Device 和调用方物理 Target | Task 12 |
| Task 12 Metadata | 当前基本未达成，按真分页、有界 Cache 重做 | Task 11 |
| Task 13-14 命名和兼容清理 | 仍有旧 Proto、Device、BatchWrite、physical_bytes | Task 2、12 |
| Task 15 健康与启动语义 | Readiness 需要纳入 Scheduler、Build、Outbox 和强制重建状态 | Task 3、4、13 |
| Task 18 门禁和 E2E | 当前门禁未覆盖本轮发现，必须补充故障注入 | Task 13 |

## 最终验收清单

- [ ] 相同 Shard Sequence 可在失败后重投恢复，不存在永久 `blockedByShard`。
- [ ] 不同 Shard 有界并行，单 Shard 失败不造成全局停顿。
- [ ] 活动 Consumer 只写 Active Index，warming 失败不影响活动 ACK。
- [ ] 每个 Build 拥有持久化 Durable Consumer，进程重启可恢复。
- [ ] Build Consumer 先创建，随后才获取 Pebble Snapshot 和 Head Vector。
- [ ] Snapshot 多页读取固定；Snapshot 丢失不复用旧 Cursor。
- [ ] Build 经 Backfill、Catch-up、Activation Barrier 后才切换。
- [ ] DataShard 原子提交事实、Shard Head、Dataset Head 和 Outbox。
- [ ] DataShard 使用 Publisher 实际 Payload 上限，超限时零提交。
- [ ] Merge 后 Required、TypedValue、Schema Fence 和大小全部在 DataShard 验证。
- [ ] ViewIndex 只存在 `Apply`，不存在 BatchWrite/Write 旁路。
- [ ] View Checkpoint 以 `{shard_id, dataset_id}` 为单位，不为空间内无关 View 做空 Apply。
- [ ] 20,001 行跨 Target ASC/DESC 分页无遗漏、重复或全量内存排序。
- [ ] Metadata 无界集合 SQL 真分页，不进入全量 Cache。
- [ ] SQLite Commit 后 Cache 失败不向调用方伪报写失败。
- [ ] 通用 Device、PrimaryStoreNode/Route 和调用方物理 ShardTarget 从活动树清零。
- [ ] RPC 错误不解析字符串，不暴露内部底层错误。
- [ ] Outbox 可 inspect/retry；force-skip 原子写 DeliveryGap 和 REPAIR_REQUIRED，全部下游回填后才可 Resolve。
- [ ] 文档明确在线 DELETE 与 Archive PURGE 不同，本计划不伪装合规删除。
- [ ] 两轮独立 Review 的确认问题全部修复。
- [ ] Storage Race/Vet/E2E、Contract Scripts 和 `make verify` 全部通过。
