# Storage 架构一致性与项目 CR 最终整改 Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 在不保留历史兼容层的前提下，修复 Storage 的消息一致性、Merge、分页、删除、覆盖范围、分片边界、Metadata 扩展性和运行可靠性，建立 Admin Gateway + Node Service Gateway 双网关链路，并完成 Storage、PrimaryStore、DataShard、DataView、ViewBuilder、ViewIndex 的最终命名、垂直代码组织与两进程部署收敛。

**Architecture:** PrimaryStore 是单点的事实数据编排服务，负责校验、路由和跨 DataShard 聚合；每个 DataShard 独立持有一个 Pebble 和本地连续的 Outbox Sequence，并在同一 Pebble Batch 中提交事实行和类型明确的 `TimeSeriesRowsCommitted` / `RecordRowsCommitted`。storage-view 按 Shard 有序消费已提交的完整事实快照，把发生变化的 Dataset 映射为该来源拥有的 View 列片段，再通过 ViewIndex 显式 `MERGE` 到本地已有完整 View 行；只有目标行缺失时才批量读取组成该行的全部来源并用 `REPLACE` 恢复，不在每次 A 更新时重复读取未变化的 B。浏览器经 Admin Gateway 进入 Node Service Gateway，服务间调用直接进入同一 Node Service Gateway；Archive 只归档历史写入数据，忽略 Delete，不参与在线删除。

**Tech Stack:** Go 1.25、tRPC-Go、Protocol Buffers、Pebble、SQLite、NATS JetStream、DuckDB、Bleve、Parquet、Vue 3、TypeScript、Vitest、Shell。

---

## 计划状态

本文件是 Storage 整改的最终执行计划，取代本文件之前的版本。若它与 `docs/superpowers/specs/2026-07-18-storage-primary-shard-boundary-design.md` 或更早的 Storage 计划冲突，以本计划为准；实施期间必须同步修订现行设计文档，不能保留互相矛盾的“当前架构”描述。

本计划不包含数据迁移和兼容逻辑。项目按全新部署处理，旧 Proto、旧表、旧角色、旧配置和旧服务名直接删除。

## 已完成基线与本计划边界

最初 CR 和后续讨论中有一部分已经在当前 `main` 落地。实施者不得重复造一套替代实现，但必须在最终验收中防止回归：

| 原问题或讨论项 | 当前基线 | 本计划处理 |
| --- | --- | --- |
| Storage 派生写失败先 ACK、错误被丢弃 | 尚未彻底解决 | Task 1、7、8 重做 ACK/NAK、Checkpoint 和可观测性 |
| Strategy 菜单已开放但构建、发布、部署缺失 | 已接入 Build/Release/Deploy，默认服务已注册 | Task 18 运行 Strategy 发布和 E2E 门禁 |
| Strategy 非法 RFC3339 被忽略 | 已使用 `parseStrictTimeRange` 严格返回参数错误 | Task 18 保留边界测试 |
| `go.work`、架构总览、Factor/Gateway 文档漂移 | 已增加架构文档契约并修正文档 | Task 18 在 Storage 重构后重新校验模块清单和描述 |
| Package Boundary、gofmt、ESLint、Prettier 未进入 CI | 已进入 `make verify`，ESLint 为零 Warning | Task 18 完整运行只读门禁 |
| Storage Main、Monitor Bootstrap 过大 | 已拆到约 220 行入口和多个职责文件 | Task 15 保持薄入口，不重新合并 |
| CloudNode、View Browse 前端维护热点 | 仍分别约 1970/1322 行 | Task 17 按工作流、状态和展示职责拆分 |
| 生产代码使用 `context.Background()` | 已清零，进程入口使用 tRPC Context，异步入口 Clone | Task 4、7、15 保持该规则 |
| Gateway 路由刷新、HostAgent 采样使用进程 Ticker | 已迁移官方 tRPC Timer；Gateway 不重复立即执行，HostAgent 使用 `startAtOnce=1` | Task 18 运行现有 Timer 契约测试 |
| 其他会话级/对象级 Timer | 按决定保留进程内 Timer | 本计划不强制迁移 Outbox、Debounce Flush、ACK Heartbeat、Listen-Key Keepalive 等非调度任务 |
| `retention` 命名和 Host Metrics 无界清理 | 已改为 `host_metrics_cleanup`，默认 48H、每数据集 10 批、每批 1000、超时、防重入 | Task 6、15、18 保留受信删除边界和运维文档 |
| Factor `Debouncer/Coalescer` 命名 | 已改为固定窗口 `EventBatcher` 和 `event_batch_window_ms` | Task 18 做旧名扫描；Storage View 采用相同清晰命名 |
| `packages/crypto` 与加密货币语义冲突 | 已改为 `packages/security` | Task 18 扫描旧包名并运行安全包测试 |

## 最终决策

| 主题 | 最终决定 |
| --- | --- |
| 浏览器入口 | Browser -> Admin Gateway `/api/admin/storage/{method}` -> Node Service Gateway -> Storage |
| 服务间入口 | Go Service -> Node Service Gateway 原生 tRPC Listener -> Storage |
| 网关职责 | Admin Gateway 负责用户会话、权限和 HTTP/BFF 适配；Node Service Gateway 负责 Service HMAC、tRPC 路由、限流和追踪 |
| 默认部署 | 仅 `storage-primary` 和 `storage-view` 两个进程 |
| Metadata | 独立代码服务，和 PrimaryStore 装配在 `storage-primary` 进程 |
| PrimaryStore | 单点逻辑事实服务，负责校验、路由、聚合，不直接拥有 Pebble |
| DataShard | 内部物理事实分片，固定 `shard_id`，独立部署或嵌入 `storage-primary` |
| 分片能力 | 只提供单副本容量分片；不实现副本、自动故障转移、迁移、再平衡和跨分片事务 |
| 公共消息 | 统一使用 `MooxMessage`；新增 `message_type`，代码和文档不再使用 Envelope 作为别名 |
| 事实提交消息 | 保留两种显式领域消息并改名为 `TimeSeriesRowsCommitted`、`RecordRowsCommitted`；行元素使用 `TimeSeriesRowWrite`、`RecordRowWrite`，不使用 RowsChanged、RowChange、Mutation、FactMutation、DataChange、DataChangeMsg |
| 消息 ID | 只使用 `MooxMessage.message_id`，Payload 不重复 |
| 消息顺序 | `MooxMessage.sequence` 是 DataShard 内连续序列；不增加全局 `write_version`，不使用雪花算法 |
| 行内版本 | 同一事实 Key 只属于一个 DataShard，内部版本为 `shard_id + last_sequence` |
| View 消费进度 | 每个 ViewIndex 记录各 DataShard 的 `last_applied_sequence` |
| 事实写入 | `WriteTimeSeriesRows` / `WriteRecordRows` 改为 `MergeTimeSeriesRows` / `MergeRecordRows`；未提供字段保留，行不存在则创建 |
| View 物化 | 已提交消息携带完整事实快照；稳态只映射本次变化来源拥有的列并在 ViewIndex 本地 MERGE，不重复回读未变化来源 |
| View 行并发 | 同一 `ViewRowKey` 串行处理，不比较不同 DataShard 的 Sequence |
| View 缺行恢复 | MERGE 目标不存在时整批不落盘、不推进 Checkpoint，并返回全部缺失 RowKey；ViewBuilder 批量读取这些行的所有来源后以 REPLACE 重试 |
| DuckDB/Bleve | 共用显式 MERGE/REPLACE/DELETE 协议；MERGE 都先读取本地完整行再合并，Bleve 不再直接用局部文档覆盖 |
| 共享包边界 | 删除含糊的 `internal/core` 总目录；真正跨服务复用的代码只保留顶层 `internal/rowkey`、`internal/typedvalue`、`internal/retinfo` |
| ViewIndex 写入模型 | 不创建独立 `viewrow` 包；`RowKey`、`RowWrite`、`BatchWrite` 作为 ViewIndex 写入契约，由 `internal/service/viewindex` 持有 |
| ViewIndex 写操作 | `RowWrite.operation` 只允许 `MERGE` / `REPLACE` / `DELETE`；MERGE 携带单一来源列片段，REPLACE 携带完整 View 行，DELETE 只携带 Key；统一通过 `ViewIndex.Apply` 提交 |
| ViewIndex 写入进度 | `BatchWrite.checkpoint_updates` 保存一个或多个带 Expected/Last Sequence 的 `ShardCheckpointUpdate`；可选 `IndexRangeUpdate` 只由进度协调逻辑产生 |
| View Schema Fence | 使用 `view_schema_hash` / `ViewSchemaHash`，明确区别于 Dataset `schema_hash`；它与 `view_version` 共同拒绝过期写入 |
| 错误与返回信息 | 错误码、类型化错误和 `pb.RetInfo` 转换统一放入 `internal/retinfo`；不拆分 `errorcode`、`rpcresult`，不保留 `response` 包 |
| Archive 范围 | 只处理配置白名单中的 TimeSeries Dataset；永久保留每个历史业务 Key 的最新完整状态 |
| Archive 删除 | Archive 忽略 Delete 并 ACK，不写 Tombstone，不删除 Parquet/COS |
| View 范围 | 使用成对的 `indexed_from` / `indexed_to`，删除 `active_coverage_start/end` |
| 行映射命名 | `projection.go` 改为 `row_mapper.go`；不使用 Replay |
| 路由变更 | Dataset 首次写入后锁定分片拓扑；无迁移不得改变 Hash Pool、节点或权重 |
| Runtime YAML | `storage-primary`、`storage-view` 各只读取一个 `trpc_go.yaml`；可选私网 `storage-shard` 也只读取自身 YAML |

## 明确不实现

- 不实现全局事实 Version、Snowflake Version、全局消息总序。
- 不实现 PrimaryStore 高可用、Leader Election 或多 PrimaryStore 并发写入。
- 不实现 DataShard 副本、自动 Failover、在线迁移或自动 Rebalance。
- 不实现跨 DataShard 事务；跨分片批次继续返回明确的部分成功结果。
- 不通过 PrimaryStore 转发 DataView 查询。
- 不允许浏览器绕过 Admin Gateway 直接访问 Node Service Gateway；Admin Gateway 不直接连接具体 Storage 进程。
- 不在 Admin Gateway 复制 Storage 业务逻辑；它只执行用户鉴权、静态方法白名单、HTTP/BFF 适配和向 Node Service Gateway 的受信 tRPC 调用。
- 不让 Archive 跟随在线删除，也不把 Archive 定义为“当前状态备份”。
- 不保留 Access、旧物理 PrimaryStore、通用 Device、旧 Runtime Role 或旧配置别名。
- 不为旧 SQLite/Pebble 数据增加迁移代码；开发和部署使用全新数据目录。

## 必须保持的正确性不变量

1. DataShard 只有在事实行和 Outbox 同一 Pebble Batch 提交成功后，写请求才成功。
2. `MooxMessage.sequence` 由 DataShard 在提交 Batch 内分配；重启后继续递增，不重复。
3. Outbox 只能删除从队首开始连续发布成功的前缀，不能跨过失败项。
4. ViewBuilder 只有在所有受影响的活动/构建中 ViewIndex 都写入成功后才 ACK。
5. 同一 Shard 的消息严格按 Sequence 处理；同一 ViewRowKey 不并发写入。
6. ViewIndex 只接受包含明确 MERGE/REPLACE/DELETE 的 `BatchWrite`；MERGE 不得创建缺列行，REPLACE 必须是完整 View 行，DuckDB 和 Bleve 的写入语义一致。
7. ViewIndex 数据写入成功后才能推进对应 Shard Checkpoint；崩溃最多造成重放，不得造成跳过。
8. Delete 必须从 PrimaryStore 传播到 DuckDB/Bleve；Archive 必须忽略 Delete。
9. Cursor 必须指向最后一条实际返回或明确消费的数据，不能因为底层预取跳过未返回行。
10. View 查询不得把超出 `indexed_from/indexed_to` 或尚未追平的数据伪装成完整成功。
11. DataShard 只接受发给自身 `shard_id` 的请求，并验证请求内所有 Space、Dataset、Key 一致。
12. Dataset 拓扑锁定后，任何会改变既有 Key 放置位置的 Metadata 变更都必须失败。
13. 事实 Merge 必须在 DataShard 的同一原子边界内读取旧行、合并提供字段、校验完整行，并把同一完整结果写入 Pebble 和 RowsCommitted MooxMessage。
14. TimeSeriesRowsCommitted 和 RecordRowsCommitted 共用每个 DataShard 的同一条 Sequence；ViewBuilder 必须通过一个逻辑 Shard Lane 和一个 Checkpoint 消费两类消息。
15. 稳态 View MERGE 只修改消息所属 Dataset 拥有的列；未变化来源的列必须从 ViewIndex 本地已有行保留，不能在每次事件中回读，也不能被局部文档覆盖。
16. MERGE 缺行恢复读取到的每个事实快照必须携带内部 `shard_id + last_sequence` 来源戳；ViewIndex 保存每个来源的最新戳并忽略更旧片段，防止恢复时读取到的新状态被随后到达的旧消息回滚。
17. 浏览器只能经过 Admin Gateway；Admin Gateway 和其他 Go 服务只能经过 Node Service Gateway 调用可路由的 Storage 服务，失败时不得绕过网关直连。

## 目标代码结构

```text
modules/storage/
  cmd/server/                         # 只负责装配、配置、健康和关闭
  internal/service/
    metadata/                         # Metadata RPC 与目录管理
      sqlite/                         # Metadata 唯一持久化实现
      cache/                          # 小型目录缓存
    primarystore/                     # 事实校验、路由、跨 Shard 聚合、内部扫描
      schema/                         # Merge 后完整行的 Schema Contract
      shardrouter/                    # Dataset 到 DataShard 的锁定路由
    datashard/                        # 物理读写、固定 Shard 身份
      pebble/                         # 事实行、Sequence 和 Outbox
      messagepublisher/               # MooxMessage 发布适配器
    dataview/                         # 对外派生查询
    viewbuilder/                      # RowsCommitted 消费、列片段映射、缺行恢复、物化
      eventconsumer/                  # JetStream 领域消息消费适配器
      rowmapper/                      # 多 DataShard 事实行到 ViewRow 的映射
    viewindex/                        # ViewIndex 生命周期和内部 RPC
      duckdb/                         # 结构化索引实现
      bleve/                          # 全文索引实现
  internal/rowkey/                    # 事实行 Key 规范化、解析、维度哈希和时间格式
  internal/typedvalue/                # TypedValue 转换、比较和 Null 基础语义
  internal/retinfo/                   # 错误码、类型化错误和 pb.RetInfo 转换
```

Storage 不再保留横向的 `internal/infra` 技术分层：有唯一生命周期所有者的实现必须和所属领域服务放在一起；真正跨模块复用的 JetStream、鉴权和协议能力继续使用 `packages/*`。Admin Gateway 是浏览器 BFF，Node Service Gateway 是唯一内部服务入口；Storage 模块内不再创建额外转发进程或 God Service。

| 身份 | 最终名称 | 部署位置 | 可见性 |
| --- | --- | --- | --- |
| 浏览器 Facade | `/api/admin/storage/{method}` | Admin Gateway | 用户会话入口，静态方法白名单，转发到 Node Service Gateway |
| 内部逻辑入口 | Storage tRPC Callee/Method | Node Service Gateway | 原生 tRPC、Service HMAC、按 `(callee, method)` 路由 |
| Metadata | `trpc.moox.storage.Metadata` | `storage-primary` | 仅由 Node Service Gateway 和进程内受信组件调用 |
| 事实编排 | `trpc.moox.storage.PrimaryStore` | `storage-primary` | 仅由 Node Service Gateway 路由，不直接公开物理地址 |
| 有界扫描 | `trpc.moox.storage.PrimaryStoreScan` | `storage-primary` | Service Gateway 特权路由，仅允许 ViewBuilder、Archive 和维护身份 |
| 物理分片 | `trpc.moox.storage.DataShard` | 嵌入或私网独立进程 | 嵌入时 LocalClient；独立时经所在节点 Service Gateway 特权路由，仅允许 PrimaryStore |
| 派生查询 | `trpc.moox.storage.DataView` | `storage-view` | 由 Node Service Gateway 按方法路由，不经过 PrimaryStore |
| 派生写入 | `trpc.moox.storage.ViewIndex` | `storage-view` | 仅进程内 ViewBuilder/维护调用 |

## 阶段门禁

| 阶段 | 任务 | 进入下一阶段前必须满足 |
| --- | --- | --- |
| A. 一致性协议 | Task 1-10 | Merge、乱序、分页、Delete、View 范围、Schema 和错误契约的回归测试全部通过 |
| B. 分片与 Metadata | Task 11-12 | Shard 身份、拓扑冻结、Metadata 真分页和缓存测试通过 |
| C. 结构收敛 | Task 13-15 | 最终包名、Proto、角色、配置和健康检查通过；旧名字扫描为零 |
| D. 对外与交付 | Task 16-18 | Gateway、前端热点、发布、文档、全量 Verify 和 E2E 全部通过 |

在阶段 A 完成前，不得开始纯目录重命名；避免用大规模 Rename 掩盖一致性回归。

### Task 1: 锁定一致性红线和失败基线

**Files:**
- Create: `modules/storage/test/storage_consistency_contract_test.go`
- Create: `scripts/test-storage-consistency-contract.sh`

该 Contract Test 使用 `//go:build storage_consistency_contract`，只由专用脚本显式运行。在 Stage A 完成前不得接入默认 `make verify`，避免把后续各任务的中间提交永久置于红色；每个具体修复仍必须在所属包增加默认执行的单元/集成测试。

- [ ] **Step 1: 添加 Merge 物化失败测试**

分别调用 `MergeTimeSeriesRows` / `MergeRecordRows` 构造同一 Key 的两次事实写入：第一次提供完整列，第二次只提供一列。测试必须证明 PrimaryStore 最终行保留未修改列，`TimeSeriesRowsCommitted` / `RecordRowsCommitted` 和 Archive 收到的是 DataShard 合并后的完整事实快照。另建一个由 kline 与 factor 组成的 View，证明只更新 kline.close 时 ViewBuilder 不读取 factor，DuckDB/Bleve 都通过 MERGE 保留已有 momentum/volatility，不能把局部列当完整文档覆盖。

增加缺行恢复用例：先删除或损坏活动 ViewIndex 中的一行，再消费任一来源消息。第一次 Apply 必须返回全部缺失 RowKey，且 RowWrites 和 Checkpoint 均未提交；ViewBuilder 随后批量读取这些 RowKey 的全部来源，以 REPLACE 重试并恢复完整行。恢复读取到的来源状态若领先于待消费旧消息，后续旧消息必须被来源戳抑制，不能把列回滚。

- [ ] **Step 2: 添加 Outbox 非连续 ACK 测试**

覆盖 ACK 结果 `[nil, error, nil]`，当前实现会删除第 1、3 项。目标断言是只删除第 1 项，第 2、3 项留在 Outbox，下一次仍从第 2 项发布。

- [ ] **Step 3: 添加分页漏数测试**

写入 1001 条时序事实行，分别使用页大小 1、25、999，覆盖 ASC/DESC。逐页收集必须恰好得到 1001 个唯一 Key，不能遗漏或重复。

- [ ] **Step 4: 添加 Delete 传播测试**

写入事实行并等待 DuckDB/Bleve 可查，再删除事实行。目标断言：PrimaryStore、DuckDB、Bleve 均不可查；Archive 中原归档数据仍存在且没有 Delete 标记。

- [ ] **Step 5: 添加 View 范围与状态测试**

覆盖请求早于 `indexed_from`、晚于 `indexed_to`、View inactive、Dataset inactive、Checkpoint 落后 Shard Head。目标均不得返回普通成功。

- [ ] **Step 6: 记录当前失败状态**

脚本必须在模块目录执行 `go test -tags storage_consistency_contract -count=1 ./test -run StorageConsistencyContract`，不能通过 `|| true` 吞掉失败。

Run:

```bash
bash scripts/test-storage-consistency-contract.sh
```

Expected: 新增测试仅因当前事件、Outbox、分页、Delete 和范围语义不满足而失败；不得有编译错误或随机失败。

- [ ] **Step 7: Commit**

```bash
git add modules/storage/test/storage_consistency_contract_test.go scripts/test-storage-consistency-contract.sh
git commit -m "test(storage): lock consistency remediation contract"
```

### Task 2: 统一 MooxMessage 和 RowsCommitted 协议

**Files:**
- Modify: `packages/messagepb/moox_message.proto`
- Regenerate: `packages/messagepb/moox_message.pb.go`
- Rename/Rewrite: `modules/storage/proto/message.proto` -> `modules/storage/proto/rows_committed.proto`
- Modify: `modules/storage/proto/Makefile`
- Regenerate: `modules/storage/proto/storagegen/*`
- Modify: `packages/jetstream/codec.go`
- Create/Modify: `packages/jetstream/codec_test.go`
- Create: `packages/jetstream/subject_token.go`
- Create: `packages/jetstream/subject_token_test.go`
- Modify: `modules/storage/internal/core/eventbus/bus.go`
- Modify: `modules/storage/internal/core/eventbus/bus_test.go`
- Modify: `modules/storage/internal/infra/eventbus/producer_bus.go`
- Modify: `modules/storage/internal/infra/eventbus/producer_bus_test.go`
- Modify: `modules/storage/internal/bootstrap/eventbus/factory.go`
- Modify: `modules/storage/internal/bootstrap/eventbus/factory_test.go`
- Modify: `modules/cloudnode/internal/jobqueue/jetstream_queue.go`
- Modify: `modules/hostagent/internal/app/app.go`
- Modify: `modules/monitor/internal/hostmetrics/hostmetrics.go`
- Modify: `modules/monitor/internal/metrics/consumer.go`
- Modify: `modules/strategy/internal/bus/publisher.go`
- Modify: `modules/trade/internal/infra/bus/relay.go`
- Modify: `packages/report/handler.go`
- Modify: `modules/cloudnode/internal/jobqueue/jetstream_queue_test.go`
- Modify: `modules/hostagent/internal/app/app_test.go`
- Modify: `modules/monitor/internal/hostmetrics/hostmetrics_test.go`
- Modify: `modules/monitor/internal/metrics/consumer_test.go`
- Modify: `modules/strategy/internal/bus/publisher_test.go`
- Modify: `modules/trade/internal/infra/bus/relay_test.go`
- Modify: `packages/report/handler_test.go`
- Modify: `modules/archive/internal/consumer/decode.go`
- Modify: `modules/archive/internal/consumer/decode_test.go`
- Modify: `modules/archive/internal/consumer/handler.go`
- Modify: `modules/archive/internal/consumer/handler_test.go`
- Modify: `modules/archive/internal/consumer/runner.go`
- Modify: `modules/archive/internal/consumer/runner_test.go`
- Modify: `modules/archive/internal/journal/store.go`
- Modify: `modules/archive/test/archive_e2e_test.go`
- Modify: `modules/archive/config/app.yaml`
- Modify: `modules/eventbus/internal/config/config_defaults.go`
- Modify: `modules/eventbus/internal/config/config_test.go`
- Modify: `modules/eventbus/config/app.yaml`
- Modify: `modules/factor/internal/testkit/events.go`
- Modify: `modules/factor/internal/trigger/event_batcher.go`
- Modify: `modules/factor/internal/trigger/event_batcher_test.go`
- Modify: `modules/factor/internal/trigger/nats.go`
- Modify: `modules/factor/config/app.yaml`
- Modify: `modules/storage/internal/service/archive/events.go`
- Modify: `modules/storage/internal/service/archive/service_test.go`
- Modify: `modules/storage/test/view_derivation_reliability_test.go`
- Modify: `modules/storage/config/storage.yaml`
- Modify: `modules/storage/config/storage.access.yaml`
- Modify: `modules/storage/config/storage_view/trpc_go.yaml`
- Modify: `modules/storage/config/trpc_go.yaml`
- Modify: `modules/storage/config/trpc_go.access.yaml`

- Modify: `modules/storage/internal/service/primary/service.go`
- Modify: `modules/storage/internal/service/primary/outbox_relay.go`
- Modify: `modules/storage/internal/service/primary/outbox_relay_test.go`

- [ ] **Step 1: 为 MooxMessage 增加业务消息类型**

新增：

```proto
string message_type = 14;
```

字段职责固定为：`kind` 表示 EVENT/COMMAND/SNAPSHOT，`message_type` 表示 Payload Schema，`topic` 只负责路由，`content_type` 表示编码。

- [ ] **Step 2: 定义两个显式 Storage RowsCommitted 消息**

不删除 TimeSeries 和 Record 两种领域消息概念。旧名 `TimeSeriesRowsUpdated`、`RecordRowsUpdated` 容易让人误解为普通 RPC 成功通知，`RowsChanged` / `RowChange` 又只表达模糊的“发生变化”，因此最终命名为外层批次 `TimeSeriesRowsCommitted`、`RecordRowsCommitted`，内层动作 `TimeSeriesRowWrite`、`RecordRowWrite`。两类消息保持独立，使 Factor、Archive 可以只订阅 TimeSeries，避免用一个 `oneof DataChange` 隐藏不同的 Key 和范围语义：

```proto
enum RowWriteOperation {
  ROW_WRITE_OPERATION_UNSPECIFIED = 0;
  ROW_WRITE_OPERATION_MERGE = 1;
  ROW_WRITE_OPERATION_REPLACE = 2;
  ROW_WRITE_OPERATION_DELETE = 3;
}

message TimeSeriesRowWrite {
  RowWriteOperation operation = 1;
  TimeSeriesRow row = 2;
}

message TimeSeriesRowsCommitted {
  string shard_id = 1;
  string space_id = 2;
  string dataset_id = 3;
  repeated TimeSeriesRowWrite writes = 4;
}

message RecordRowWrite {
  RowWriteOperation operation = 1;
  RecordRow row = 2;
}

message RecordRowsCommitted {
  string shard_id = 1;
  string space_id = 2;
  string dataset_id = 3;
  repeated RecordRowWrite writes = 4;
}
```

DELETE 的 Row 只携带完整 Key；UPSERT 携带 DataShard 合并后的完整事实行。`Committed` 表示 Pebble 事实行与 Outbox 已在同一个 Sync Batch 中提交，不表示 View 已完成派生。Payload 不重复 `message_id`、`sequence`、`occurred_at`。

- [ ] **Step 3: 固定两类消息的 Message Type 和 Topic**

```text
TimeSeries:
  message_type: moox.storage.time_series.rows_committed.v1
  topic:        moox.storage.rows_committed.time_series.v1.<shard_token>
  content_type: application/x-protobuf; message=trpc.moox.storage.TimeSeriesRowsCommitted

Record:
  message_type: moox.storage.record.rows_committed.v1
  topic:        moox.storage.rows_committed.record.v1.<shard_token>
  content_type: application/x-protobuf; message=trpc.moox.storage.RecordRowsCommitted

Both:
  kind:         MESSAGE_KIND_EVENT
  sequence:     DataShard 本地统一 Sequence
```

`shard_token` 固定为 Shard ID UTF-8 字节的无 Padding 小写 Base32，确保它始终是单个 NATS Token；生产者和消费者共用 `packages/jetstream` 的 Encode/Decode Helper，禁止调用方直接拼接任意 Subject。两类消息进入同一个 Storage Change Stream；DataShard Outbox 按统一 Sequence 串行发布。ViewBuilder 使用同一个 Durable 和两个 Filter Subject 接收两类消息，再进入同一条按 Shard 排序的处理通道，不能为两类消息分别推进 Checkpoint。

- [ ] **Step 4: 强化 MooxMessage 校验并清除 Envelope 别名**

所有新 MooxMessage 的 `message_type`、`message_id`、`producer`、`topic` 和 `payload` 缺失时拒绝；Storage RowsCommitted 还必须拒绝 Sequence=0，并满足 Topic Shard 与 Payload `shard_id` 一致。一次性更新仓库内所有生产者，为每种 Payload 设置带命名空间和版本的 Message Type，不能只让 Storage 使用新字段。

代码和现行文档统一使用实际类型名 `MooxMessage`，同时完成以下无兼容重命名：`EnvelopePublisher` -> `MessagePublisher`、`PublishEnvelope` -> `PublishMessage`、`Envelope()` -> `Message()`、`RawEnvelope` -> `RawMessage`、`fixtureEnvelope` -> `fixtureMessage`。不得创建新的 `Envelope` 类型，也不得把它改叫 `MessageHeader`，因为外层对象同时拥有 Payload。

```go
type MessagePublisher interface {
    PublishMessage(context.Context, []byte) error
}
```

Outbox Relay 在 Task 4 固定为按 Sequence 逐条等待 ACK，因此删除批量 `PublishEnvelopes`/`PublishMessages` 接口，不保留两套发布语义。

- [ ] **Step 5: 原子更新所有 RowsCommitted 消费者**

Archive、Factor、ViewBuilder 和 EventBus Registry 一次性从 RowsUpdated 切换到两个 RowsCommitted Message Type。Factor 和 Archive 只订阅 TimeSeries Topic Family；ViewBuilder 用一个 Durable 同时过滤 TimeSeries 和 Record Topic Family；各消费者保留独立 Durable。Registry 允许这两个 Topic Family，Storage 生产者和所有消费者还必须校验 Topic 最后一段解码后等于 Payload `shard_id`。Factor 只从 UPSERT TimeSeries Row 提取触发 Key；Archive 归档完整 UPSERT 并忽略 DELETE；ViewBuilder 将两种外部消息归一为内部 `CommittedBatch` 后处理 UPSERT/DELETE。未知 `message_type` 不得尝试按 Topic 猜 Payload。旧的 Storage 内 Archive 虽会在 Task 14 删除，但在该任务前仍须保持编译和测试通过。

- [ ] **Step 6: 重新生成代码**

```bash
make -C packages/messagepb all
make -C modules/storage/proto clean
make -C modules/storage/proto all
gofmt -w packages/messagepb/moox_message.pb.go modules/storage/proto/storagegen/*.go
```

Expected: 生成代码只暴露 `TimeSeriesRowsCommitted`、`RecordRowsCommitted` 和各自的 RowWrite；旧 `message.pb.go`、RowsUpdated、RowsChanged、RowChange 类型消失，生成文件名为 `rows_committed.pb.go`。

- [ ] **Step 7: Run and commit**

```bash
(cd packages/messagepb && env GOCACHE=/tmp/moox-gocache go test -count=1 ./...)
(cd packages/jetstream && env GOCACHE=/tmp/moox-gocache go test -count=1 ./...)
(cd modules/storage && env GOCACHE=/tmp/moox-gocache go test -count=1 ./internal/core/eventbus ./internal/infra/eventbus ./internal/service/view/builder ./internal/service/archive ./test)
(cd modules/archive && env GOCACHE=/tmp/moox-gocache go test -count=1 ./...)
(cd modules/factor && env GOCACHE=/tmp/moox-gocache go test -count=1 ./internal/trigger)
(cd modules/eventbus && env GOCACHE=/tmp/moox-gocache go test -count=1 ./internal/config)
(cd modules/cloudnode && env GOCACHE=/tmp/moox-gocache go test -count=1 ./internal/jobqueue)
(cd modules/hostagent && env GOCACHE=/tmp/moox-gocache go test -count=1 ./internal/app)
(cd modules/monitor && env GOCACHE=/tmp/moox-gocache go test -count=1 ./internal/hostmetrics ./internal/metrics)
(cd modules/strategy && env GOCACHE=/tmp/moox-gocache go test -count=1 ./internal/bus)
(cd modules/trade && env GOCACHE=/tmp/moox-gocache go test -count=1 ./internal/infra/bus)
(cd packages/report && env GOCACHE=/tmp/moox-gocache go test -count=1 ./...)
git add packages/messagepb packages/jetstream packages/report \
  modules/storage/proto modules/storage/internal/core/eventbus modules/storage/internal/infra/eventbus modules/storage/internal/bootstrap/eventbus \
  modules/storage/internal/service/archive modules/storage/internal/service/view/builder modules/storage/test modules/storage/config \
  modules/archive modules/eventbus modules/factor \
  modules/cloudnode/internal/jobqueue modules/hostagent/internal/app modules/monitor/internal/hostmetrics modules/monitor/internal/metrics \
  modules/strategy/internal/bus modules/trade/internal/infra/bus
git commit -m "refactor(storage): define rows committed message contracts"
```

### Task 3: 让 DataShard 原子生成完整 RowsCommitted

**Files:**
- Modify: `modules/storage/proto/store.proto`
- Modify: `modules/storage/internal/infra/device/store.go`
- Modify: `modules/storage/internal/infra/device/pebble/store.go`
- Modify: `modules/storage/internal/infra/device/pebble/outbox.go`
- Modify: `modules/storage/internal/service/primary/service.go`
- Modify: `modules/storage/internal/service/primary/local.go`
- Modify: `modules/storage/internal/service/primary/remote.go`
- Modify: `modules/storage/internal/infra/device/pebble/store_test.go`
- Modify: `modules/storage/internal/infra/device/pebble/outbox_test.go`
- Modify: `modules/storage/internal/service/primary/service_test.go`
- Modify: `modules/storage/internal/service/primary/local_test.go`
- Modify: `modules/storage/internal/service/primary/remote_test.go`

- [ ] **Step 1: 移除调用方预编码 Outbox**

从物理写请求删除 `outbox_message`。PrimaryStore 只传规范化事实 Merge 请求、AuthInfo、预期 `shard_id` 和按 Dataset 缓存编译的只读 Schema Contract；最终 MooxMessage 必须由 DataShard 创建。Schema Contract 只包含列名、类型、Required 集合和 Schema Hash，DataShard 不查询 Metadata。

- [ ] **Step 2: 固定 DataShard 身份**

DataShard 构造函数必须接收非空 `ShardID`。服务端拒绝请求 Shard ID 与本机不一致、行内 Space/Dataset 不一致或 Key 数据类型不一致的批次。

- [ ] **Step 3: 重构 Pebble 原子写接口**

Pebble 写入内部必须先按规范化 ShardKey 排序获取行锁，再持有单个 Sequence/Outbox 提交锁，按以下顺序完成；任何路径都按同一锁顺序释放，避免并发多行批次死锁：

```text
读取旧行
合并请求中提供的字段
使用 Schema Contract 校验合并后完整行
分配下一 Shard Sequence
按数据类型用完整行构造 TimeSeriesRowsCommitted 或 RecordRowsCommitted
构造 MooxMessage
校验编码后的 MooxMessage 不超过 EventBus MaxPayload
Batch.Set 事实行
Batch.Set Outbox
Batch.Set Sequence High Water
Batch.Commit(Sync)
```

任一步失败都不得留下部分事实行、Outbox 或 Sequence。下一 Sequence 只能从已提交的 High Water 推导；Payload 超限或 Commit 失败时不得只在内存中消耗一个 Sequence，重试可以复用尚未提交的候选值。

- [ ] **Step 4: 保存行的来源序列**

在内部 ShardRow 中保存 `last_sequence`，但不在公共 TimeSeriesRow/RecordRow API 暴露为业务 Version。一次 Batch 内的所有成功行使用同一个 Sequence。读取旧行、合并字段、校验、写事实行和写 Outbox 必须处于同一组行锁与 Pebble Sync Batch 边界，使权威行和消息中的完整行永远表示同一次提交。

供 ViewBuilder 缺行恢复和重建使用的特权读取/扫描结果必须另外返回 `SourceStamp{dataset_id, shard_id, last_sequence}`。它是内部派生一致性元数据，不是用户数据列，也不能进入 DataView 查询响应。RowsCommitted 的 UPSERT 直接使用 MooxMessage 的 Shard ID/Sequence 作为该事实快照的来源戳。

- [ ] **Step 5: 修正 Merge 的 Null 语义**

暂时保持“未携带列不变”，但不得继续把显式 Null 静默当作未携带；完整 Null/Unset 语义在 Task 10 一次性落地。

- [ ] **Step 6: 添加崩溃边界测试**

使用可注入失败点覆盖：合并后、Sequence 后、Outbox Set 后、Commit 前。每种失败都断言重开 Pebble 后事实行、Sequence 和 Outbox 同时旧或同时新。

- [ ] **Step 7: Run**

```bash
(cd modules/storage && env GOCACHE=/tmp/moox-gocache go test -count=1 ./internal/infra/device/pebble ./internal/service/primary)
```

Expected: 原子性、完整 UPSERT、来源戳、固定 Shard 身份测试通过。

- [ ] **Step 8: Commit**

```bash
git add modules/storage/proto modules/storage/internal/infra/device modules/storage/internal/service/primary
git commit -m "fix(storage): create shard changes atomically with facts"
```

### Task 4: 修复 Outbox 顺序、重试和可观测性

**Files:**
- Modify: `modules/storage/internal/service/primary/outbox_relay.go`
- Modify: `modules/storage/internal/service/primary/outbox_relay_test.go`
- Modify: `modules/storage/internal/infra/device/pebble/outbox.go`
- Modify: `modules/storage/internal/observability/view_metrics.go`
- Modify: `modules/storage/internal/observability/view_metrics_test.go`
- Create: `modules/storage/internal/observability/shard_metrics.go`
- Create: `modules/storage/internal/observability/shard_metrics_test.go`
- Modify: `modules/storage/cmd/server/health.go`

- [ ] **Step 1: 只删除连续成功前缀**

Relay 可以从 Outbox 分页读取一批，但必须按 Sequence 逐条同步等待 JetStream ACK；第一条 Publish/ACK 失败后立即停止本轮，不能继续发布更高 Sequence。仅删除已经连续确认成功的前缀。保留 `[nil, error, nil]` 单元测试用于锁定 Prefix Helper 的防御语义，但生产 Relay 不再主动制造这种乱序发布窗口。

- [ ] **Step 2: 保持每个 Shard 单 Relay**

同一个 Pebble/Shard 只允许一个 Relay 实例。Relay 必须按 Outbox Key 顺序读取，不并发发布同一 Shard 的不同消息；读取批大小只减少 Pebble 扫描次数，不改变逐条 Publish/ACK 顺序。

- [ ] **Step 3: 使用 tRPC Context**

进程生命周期使用 `trpc.BackgroundContext()`；进入异步 Relay 时使用 `trpc.CloneContext` 保留链路字段并切断上游 RPC Deadline，再增加 Relay 自己的停止 Context。

- [ ] **Step 4: 增加指标**

至少暴露：

```text
storage_shard_outbox_rows
storage_shard_outbox_bytes
storage_shard_outbox_oldest_seconds
storage_shard_outbox_publish_failures_total
storage_shard_outbox_last_published_sequence
storage_shard_outbox_last_publish_timestamp
```

- [ ] **Step 5: Readiness 严格失败**

Pebble 不可读、Outbox 超过配置行数/字节数/年龄、Relay 未启动或持续失败时，DataShard Readiness 返回失败并说明原因。

- [ ] **Step 6: Run and commit**

```bash
(cd modules/storage && env GOCACHE=/tmp/moox-gocache go test -race -count=1 ./internal/service/primary ./internal/infra/device/pebble ./internal/observability ./cmd/server)
git add modules/storage
git commit -m "fix(storage): preserve shard outbox ordering"
```

### Task 5: 修复权威读取分页漏数

**Files:**
- Modify: `modules/storage/internal/service/access/data.go`
- Modify: `modules/storage/internal/service/access/data_test.go`
- Modify: `modules/storage/internal/service/access/factreader.go`
- Modify: `modules/storage/internal/service/access/factreader_test.go`
- Modify: `modules/storage/internal/infra/device/pebble/store.go`
- Modify: `modules/storage/internal/infra/device/pebble/store_test.go`

- [ ] **Step 1: 禁止预取后丢弃**

`scanTimeSeriesSubject` 每次向底层请求的数量只能是“当前页面剩余容量”，不能固定取 1000 后只返回前 N 条。

- [ ] **Step 2: 统一 Cursor 定义**

Cursor 必须编码最后一条已经返回给调用方的规范化 ShardKey；ASC/DESC 都从其严格下一项继续。空页不能推进 Cursor。

- [ ] **Step 3: 跨 Target Cursor 保存独立位置**

跨 Shard Scan 的 Cursor 使用结构化 Proto/JSON 后再 Base64 编码，包含排序方向、Target ID 和每个 Target 的底层 Cursor；不得通过字符串拼接解析。

- [ ] **Step 4: 增加完整分页矩阵**

覆盖 1001 行、页面 1/25/999、ASC/DESC、单 Subject、跨 Subject、跨 Target、空 Target、最后一页和无效 Cursor。

- [ ] **Step 5: Run and commit**

```bash
(cd modules/storage && env GOCACHE=/tmp/moox-gocache go test -count=1 ./internal/service/access ./internal/infra/device/pebble)
git add modules/storage/internal/service/access modules/storage/internal/infra/device/pebble
git commit -m "fix(storage): make fact pagination lossless"
```

### Task 6: 明确 Delete 在线传播和 Archive 保留语义

**Files:**
- Modify: `modules/storage/proto/rows_committed.proto`
- Modify: `modules/storage/proto/store.proto`
- Modify: `modules/storage/internal/infra/device/pebble/store.go`
- Modify: `modules/storage/internal/service/access/data.go`
- Modify: `modules/storage/internal/service/view/builder/service.go`
- Modify: `modules/storage/internal/infra/device/duckdb/view_store_write.go`
- Modify: `modules/storage/internal/infra/device/bleve/index.go`
- Modify: `modules/archive/internal/consumer/decode.go`
- Modify: `modules/archive/internal/consumer/decode_test.go`

- [ ] **Step 1: 原子提交 Delete 和 RowsCommitted**

Pebble 删除事实 Key、分配 Sequence、按数据类型写入 DELETE `TimeSeriesRowsCommitted` 或 `RecordRowsCommitted` Outbox 必须在同一个 Sync Batch 中完成。不存在的 Key 删除仍发布一次幂等提交消息，以便清除可能残留的 View；Archive 始终忽略这类消息。

- [ ] **Step 2: ViewBuilder 传播删除**

DELETE 必须按 ViewRowKey 找到受影响 View。主 Dataset 删除映射为整行 DELETE；附属 Dataset 删除映射为 MERGE，把该来源拥有的列显式写成 Null，保留主 Dataset 和其他附属来源的列。若目标 View 行缺失，仍走 Task 7 的批量缺行恢复，不能创建只有 Null 片段的残缺行。

- [ ] **Step 3: 两个引擎实现同一删除接口**

DuckDB 按完整主键删除，Bleve 按稳定 Document ID 删除。删除成功后才推进 Shard Checkpoint。

- [ ] **Step 4: Archive 明确忽略 Delete**

Archive Decoder 对 DELETE 返回 Ignore/ACK，不追加 Journal，不写 Tombstone，不改 Parquet/COS。架构测试必须证明删除在线事实后归档行仍可读取。Archive 白名单外的 Dataset 明确忽略；白名单内每次完整 UPSERT 都进入 Journal，同一历史业务 Key 的新快照替换 Parquet/COS 中该 Key 的旧物化值，但不会删除其他历史时间点。Archive 不是 Merge 命令审计日志，不额外保存同一业务 Key 的每次修改；需要修订身份时由 Dataset 自身的 `revision` 业务列表达。默认 48H Host Metrics Cleanup 的 Dataset 必须与 Archive 白名单互斥；未来若要自动清理已归档事实，必须另行设计 Archive Completeness Checkpoint，本计划不得仅凭“已发布到 JetStream”删除可用于补档的事实。

- [ ] **Step 5: 收紧公开面**

把用于 48H Host Metrics 清理的删除能力移动到受信内部接口；Admin Gateway 的浏览器方法白名单和 Node Service Gateway 的普通服务路由都不公开任意范围 Delete。

- [ ] **Step 6: Run and commit**

```bash
(cd modules/storage && env GOCACHE=/tmp/moox-gocache CGO_ENABLED=1 go test -count=1 ./internal/infra/device/... ./internal/service/access ./internal/service/view/...)
(cd modules/archive && env GOCACHE=/tmp/moox-gocache go test -count=1 ./...)
git add modules/storage modules/archive
git commit -m "fix(storage): propagate online deletes to views"
```

### Task 7: 重写 ViewBuilder 为有界批量刷新流水线

**Files:**
- Rename: `modules/storage/internal/service/view/builder/batcher.go` -> `modules/storage/internal/service/view/builder/event_batcher.go`
- Rename: `modules/storage/internal/service/view/builder/batcher_test.go` -> `modules/storage/internal/service/view/builder/event_batcher_test.go`
- Rename: `modules/storage/internal/service/view/projection.go` -> `modules/storage/internal/service/view/builder/rowmapper/row_mapper.go`
- Rename: `modules/storage/internal/service/view/projection_test.go` -> `modules/storage/internal/service/view/builder/rowmapper/row_mapper_test.go`
- Modify: `modules/storage/internal/service/view/builder/service.go`
- Modify: `modules/storage/internal/service/view/builder/service_test.go`
- Modify: `modules/storage/internal/service/view/builder/time_series.go`
- Modify: `modules/storage/internal/service/view/builder/time_series_test.go`
- Modify: `modules/storage/internal/service/view/builder/record.go`
- Modify: `modules/storage/internal/service/view/builder/record_test.go`
- Modify: `modules/storage/internal/service/view/builder/options.go`
- Modify: `modules/storage/internal/service/view/builder/options_test.go`
- Create: `modules/storage/internal/core/viewindex/batch_write.go`
- Create: `modules/storage/internal/core/viewindex/batch_write_test.go`
- Modify: `modules/storage/internal/observability/view_metrics.go`
- Modify: `modules/storage/internal/observability/view_metrics_test.go`

- [ ] **Step 1: 使用最终命名**

批量合并组件命名为 `EventBatcher`；行组合组件命名为 `RowMapper`。将 `ProjectionReader` 改为 `SourceReader`，`ProjectionGrainKey` 改为 `ViewRowKey`，`ViewProjectionDatasets` 改为 `SourceDatasetIDs`。

- [ ] **Step 2: RowsCommitted 作为提交事实与刷新输入**

ViewBuilder 分别解码 `TimeSeriesRowsCommitted` 和 `RecordRowsCommitted`，归一为只在进程内部存在的 `CommittedBatch`，读取 Shard ID、MooxMessage Sequence、Operation 和事实 Key。UPSERT Payload 必须是 DataShard 合并后的完整事实快照，可供日志、Archive 和缺行恢复使用；正常增量刷新使用来源列片段 MERGE，不因每次来源变更而回读未变化的 Dataset。

```go
type CommittedBatch struct {
    MessageID string
    ShardID   string
    Sequence  uint64
    Writes    []CommittedWrite
}

type CommittedWrite struct {
    Operation     pb.RowWriteOperation
    TimeSeriesKey *pb.TimeSeriesKey
    RecordKey     *pb.RecordKey
}
```

每个 `CommittedWrite` 必须且只能设置一种 Key。EventConsumer 另外持有 JetStream Delivery/完成通知，不能把 ACK 生命周期塞进可批量合并的纯 `CommittedBatch`。

- [ ] **Step 3: 按 Shard 保序**

每个 `shard_id` 使用一个有界串行输入队列。Sequence 小于等于已处理值的重投可以幂等处理；不能跨过当前失败事件继续 ACK 后续事件。EventBatcher 只能合并从当前 Checkpoint 开始的连续 Sequence 前缀，并以该前缀最高 Sequence 提交 Checkpoint；遇到缺口必须暂停该 Shard、把相关 ViewIndex 标记为 `REBUILD_REQUIRED` 并告警，不能用“回读最新值”掩盖丢失消息。若 JetStream 已无法提供缺失 Sequence，必须通过 Task 9 的 Snapshot + Catch-up 重建到新的 Shard Head 后才能重置 Checkpoint、处理/ACK 已被重建覆盖的积压消息并恢复实时消费。

- [ ] **Step 4: 按 ViewRowKey 合并**

EventBatcher 在固定等待窗口内把相同 ViewRowKey 合并为一次刷新。不同 Dataset 但映射到同一个 ViewRowKey 的事件进入同一个串行 Lane，避免跨 Shard 并发覆盖。

- [ ] **Step 5: 优先复用 ViewIndex，缺行时才批量回读**

RowMapper 按每个来源 Dataset 生成该来源拥有的 View 列片段。对已存在的 View 行，ViewIndex 以显式 `MERGE` 保留其他 Dataset 已物化的列，因此 A 的变更不会触发对未变化 B 的读取。只有 ViewIndex 预检发现 MERGE 目标缺行时，ViewBuilder 才按 ViewRowKey 批量读取该行涉及的所有来源 Dataset，并以完整 `REPLACE` 重试；不能用局部列创建半行。重建、回填和缺行恢复统一使用 `REPLACE`。

按目标 Shard 对缺行恢复所需的事实 Key 分组，使用现有批量 Read RPC；一次读取结果复用于所有相关 View。配置必须包含并验证：

```yaml
batch_size: 500
batch_wait: 200ms
max_workers: 2
max_pending_keys: 10000
read_timeout: 10s
write_timeout: 10s
```

达到 Pending 上限时停止拉取 JetStream，不能继续无界缓存。

- [ ] **Step 6: 异步入口 Clone Context**

消息处理进入异步队列前使用 `trpc.CloneContext` 保留 Trace/日志字段，但每个批次使用自身 Timeout，不能继承上游 RPC 的短 Deadline。

- [ ] **Step 7: 生成完整 View 行**

`rowmapper` 属于 ViewBuilder，不是通用 Infra 或 Core 算法。正常 MERGE 只输出本次变更来源拥有的 View 列；缺行恢复、回填和重建才读取所有 ViewColumn 来源并输出完整列集合。缺少主数据集行返回“删除 View 行”，缺少非主来源列输出明确 Null。

不创建独立 `viewrow` 包。ViewBuilder 和 ViewIndex 之间统一使用 ViewIndex 所拥有的 `viewindex.RowKey`、`viewindex.RowWrite`、`viewindex.BatchWrite` 写入契约；本任务先在现有 `internal/core/viewindex` 中建立这些类型，Task 13 再随整个 ViewIndex 契约原子移动到 `internal/service/viewindex`。RowMapper 只负责生成稳定 Key 和完整列集合，ViewBuilder 再将其包装为 UPSERT/DELETE RowWrite；这些类型不得包含事实回读、Filter 或 RowMapper 流程。

- [ ] **Step 8: ACK/NAK 必须等待持久化结果**

JetStream Handler 为每条输入消息保留完成通知；EventBatcher 可以合并刷新，但必须把最终结果回传给所有被合并消息。只有受影响的全部 ViewIndex 数据和 Shard Checkpoint 成功持久化后才 ACK。事实回读、DuckDB、Bleve 或 Checkpoint 的临时错误返回 NAK/可重试错误并使用有界退避；协议损坏或未知 Message Type 记录结构化错误后进入明确的 Term/DLQ 策略，不能无限毒消息重试，也不能 `_ = err`。处理可能超过 Ack Wait 时使用单条消息生命周期内的 `InProgress` Heartbeat，并在 ACK/NAK/取消时停止；它不是调度任务，不迁移为 tRPC Timer。

增加固定低基数指标和结构化日志：`storage_view_messages_pending`、`storage_view_refresh_failures_total{stage}`、`storage_view_message_redeliveries_total`、`storage_view_shard_checkpoint_lag{shard_id}`、`storage_view_last_success_timestamp`。测试必须证明 Handler 入队后不会提前 ACK，写入失败会重投，成功重放保持幂等。

- [ ] **Step 9: Run and commit**

```bash
(cd modules/storage && env GOCACHE=/tmp/moox-gocache go test -race -count=1 ./internal/core/viewindex ./internal/service/view/builder/... ./internal/service/viewindex ./internal/observability)
git add modules/storage/internal/core/viewindex modules/storage/internal/service/view modules/storage/internal/service/viewindex
git commit -m "refactor(storage): batch and serialize view refreshes"
```

### Task 8: 统一 DuckDB/Bleve BatchWrite、范围和 Checkpoint

**Files:**
- Modify: `modules/storage/internal/core/viewindex/engine.go`
- Modify: `modules/storage/internal/core/viewindex/batch_write.go`
- Modify: `modules/storage/internal/infra/device/duckdb/view_store_write.go`
- Modify: `modules/storage/internal/infra/device/duckdb/view_store_schema.go`
- Modify: `modules/storage/internal/infra/device/duckdb/view_store_lifecycle.go`
- Modify: `modules/storage/internal/infra/device/duckdb/index_manager.go`
- Modify: `modules/storage/internal/infra/device/bleve/index.go`
- Modify: `modules/storage/internal/service/viewindex/service.go`
- Modify: `modules/storage/internal/service/viewindex/client.go`
- Modify: `modules/storage/internal/service/view/builder/options.go`
- Modify: `modules/storage/internal/service/view/builder/time_series.go`
- Modify: `modules/storage/internal/service/view/builder/record.go`
- Modify: `modules/storage/internal/service/view/maintenance.go`
- Modify: `modules/storage/internal/service/view/search/service.go`
- Modify: `modules/storage/proto/view_index.proto`
- Modify: `modules/storage/proto/metadata.proto`
- Regenerate: `modules/storage/proto/storagegen/*`
- Modify: `modules/storage/schema/metadata.sql`
- Modify: `modules/storage/internal/infra/metadata/sqlite/crud_view.go`
- Modify: `modules/storage/internal/infra/metadata/sqlite/crud_view_index.go`
- Modify: `modules/storage/internal/core/viewindex/engine_test.go`
- Modify: `modules/storage/internal/core/viewindex/batch_write_test.go`
- Modify: `modules/storage/internal/infra/device/duckdb/view_store_test.go`
- Modify: `modules/storage/internal/infra/device/duckdb/index_manager_test.go`
- Modify: `modules/storage/internal/infra/device/bleve/index_test.go`
- Modify: `modules/storage/internal/service/viewindex/service_test.go`

- [ ] **Step 1: 定义 BatchWrite 写入契约**

```go
type RowWriteOperation uint8

const (
    RowWriteOperationUnspecified RowWriteOperation = iota
    RowWriteOperationUpsert
    RowWriteOperationDelete
)

type RowKey struct {
    TimeSeriesKey *pb.TimeSeriesKey
    RecordKey     *pb.RecordKey
}

type RowWrite struct {
    Operation  RowWriteOperation
    Key        RowKey
    Columns    []*pb.ColumnValue
    Attributes map[string]string
}

type ShardCheckpointUpdate struct {
    ShardID                     string
    ExpectedLastAppliedSequence uint64
    LastAppliedSequence         uint64
}

type IndexRangeUpdate struct {
    IndexedFrom *string
    IndexedTo   *string
}

type BatchWrite struct {
    RowWrites         []RowWrite
    CheckpointUpdates []ShardCheckpointUpdate
    ViewVersion       uint64
    ViewSchemaHash    string
    IndexRangeUpdate  *IndexRangeUpdate
}
```

每个 `RowKey` 必须且只能设置 TimeSeries/Record 中的一种。`RowWrite.operation` 的零值必须拒绝。`MERGE` 只携带本次来源拥有的列并保留现有 View 行的其他来源列，目标行不存在时返回全部缺失 RowKey；`REPLACE` 必须携带完整 View 行，用于重建、回填和缺行恢复；`DELETE` 只携带 Key 并拒绝非空 Columns/Attributes。同一 BatchWrite 中同一 RowKey 只能出现一次，不增加全局 `write_version`。允许 RowWrites 为空的 Checkpoint-only 或 Range-only Apply，但 RowWrites、CheckpointUpdates、IndexRangeUpdate 三者不能同时为空。

Proto 使用 `ViewIndexRowWriteOperation`、`ViewIndexRowKey`、`ViewIndexRowWrite`、`ViewIndexShardCheckpointUpdate`、`ViewIndexRangeUpdate`、`ViewIndexBatchWrite`；枚举固定为 `UNSPECIFIED=0`、`MERGE=1`、`REPLACE=2`、`DELETE=3`。`IndexRangeUpdate.indexed_from/indexed_to` 在 Proto 中使用 `optional string`，保留“不修改”和“设置新值”的区别。

将 `ViewIndexEngine.Write` 和内部 `WriteViewIndex` RPC 原子改为 `Apply` / `ApplyViewIndex`，请求体使用 `ViewIndexBatchWrite`；不保留旧方法 Alias。`BatchWrite` 是一次完整索引应用命令，不是 EventBatcher 的内存聚合批次。

- [ ] **Step 2: 明确 View Schema Fence**

所有 ViewIndex 协议和状态中的通用 `schema_hash` 改为 `view_schema_hash` / `ViewSchemaHash`：包括 `ViewIndexSchema`、`ViewIndexBatchWrite`、`ViewIndexStats`、`ViewIndexBuild`，以及 View 上的 `active_schema_hash -> active_view_schema_hash`。Dataset 自身的 `schema_hash` 保持不变。

`ViewSchemaHash` 是物化 View 结构指纹，由 Space ID、View ID、索引引擎和有序 ViewColumn 的名称、来源、类型、顺序稳定计算；它不包含行数据，也不代替 `ViewVersion`。ViewIndex Apply 必须同时校验 ViewVersion 和 ViewSchemaHash，任一不匹配都拒绝过期 Builder 写入。

- [ ] **Step 3: DuckDB 改为完整 Replace**

保留“读取旧 `row_json`”作为 MERGE 的明确实现，而不是隐式 Patch 语义。DuckDB 在一个事务中预检 MERGE 目标、合并来源列，或应用完整 REPLACE/DELETE，同时提交 Checkpoint 和可选 IndexRangeUpdate。MERGE 缺行时整批不落盘、不推进 Checkpoint，由 ViewBuilder 批量回读所有来源后以 REPLACE 重试。

- [ ] **Step 4: Bleve 保持完整 Document Replace**

Bleve Batch 必须与 DuckDB 采用相同语义：MERGE 先加载已存完整文档再合并来源列，REPLACE 写入完整文档，DELETE 删除整行，并与内部状态文档同一 Batch 提交。状态文档保存 View Version、View Schema Hash、Index Range 及每个 Shard 的 Last Applied Sequence。

- [ ] **Step 5: 明确 Checkpoint 来源和提交顺序**

DataShard 是 Shard ID 和 Sequence 的唯一权威来源；ViewBuilder 从已验证的 MooxMessage 复制它们，并把处理前的持久化位置写入 `ShardCheckpointUpdate.expected_last_applied_sequence`、当前连续成功前缀的最高 Sequence 写入 `last_applied_sequence`，不得自行生成 Sequence。普通实时 Apply 通常只提交一个 Checkpoint Update；重建完成可以一次提交多个相关 Shard 的 Checkpoint Update。

ViewIndex 必须拒绝重复 Shard ID、`Last <= Expected` 和 CAS 不匹配。只有 BatchWrite 中所有 Checkpoint Update 的“当前持久化值都等于 Expected”时，才允许应用全部 RowWrites 并推进到各自 Last；批量合并连续消息时 Last 可以跨越多个 Sequence，因此不能仅凭数值跳跃判错。若所有当前值都已经大于等于各自 Last，整次 BatchWrite 视为已被后续提交覆盖，必须跳过全部 RowWrites 并按幂等成功处理；一部分待推进、一部分已覆盖，或当前值落在 Expected 与 Last 之间/低于 Expected 时统一返回 Checkpoint Conflict，不能选择性重放无法归属到单个 Shard 的 RowWrites。

单个 ViewIndex 内的 RowWrites、CheckpointUpdates 和可选 IndexRangeUpdate 必须作为一个原子提交；DuckDB 使用同一事务，Bleve 使用同一 Batch 写入数据与内部状态文档，不提供非原子降级实现。活动槽与构建槽之间不要求跨引擎事务，但 ViewBuilder 只有在所有目标成功后才 ACK；部分目标成功时通过重投让已成功目标幂等跳过、失败目标继续 Apply。崩溃重放必须保持 Entry Count、内容和持久化进度不变。

- [ ] **Step 6: 统一 Stat**

`ViewIndexStats` 返回：

```text
indexed_from
indexed_to
shard_checkpoints[]
entry_count
view_version
view_schema_hash
updated_at
physical_bytes
```

- [ ] **Step 7: 跨引擎契约测试**

用同一组测试验证 DuckDB/Bleve：UNSPECIFIED 拒绝、UPSERT 完整替换、DELETE 不允许行内容、重复 RowKey 拒绝、空 Apply 拒绝、Checkpoint-only/Range-only Apply、多 Sequence 连续前缀、幂等重放、Checkpoint CAS 冲突、多 Shard Checkpoint、Checkpoint 不超前、受控范围更新、View Version/View Schema Hash 不匹配拒绝。

- [ ] **Step 8: Run and commit**

```bash
make proto
(cd modules/storage && env GOCACHE=/tmp/moox-gocache CGO_ENABLED=1 go test -race -count=1 ./internal/core/viewindex ./internal/infra/device/duckdb ./internal/infra/device/bleve ./internal/service/view/... ./internal/service/viewindex)
git add modules/storage
git commit -m "fix(storage): unify view index materialization semantics"
```

### Task 9: 用 indexed_from/indexed_to 建立严格查询范围

**Files:**
- Modify: `modules/storage/proto/metadata.proto`
- Modify: `modules/storage/proto/view.proto`
- Modify: `modules/storage/proto/view_index.proto`
- Modify: `modules/storage/proto/store.proto`
- Modify: `modules/storage/internal/service/primary/service.go`
- Modify: `modules/storage/internal/service/primary/local.go`
- Modify: `modules/storage/internal/service/primary/remote.go`
- Modify: `modules/storage/internal/service/view/maintenance.go`
- Modify: `modules/storage/internal/service/view/service.go`
- Modify: `modules/storage/internal/infra/metadata/sqlite/crud_view.go`
- Modify: `modules/storage/internal/infra/metadata/sqlite/crud_view_index.go`
- Modify: `modules/storage/internal/service/primary/service_test.go`
- Modify: `modules/storage/internal/service/primary/local_test.go`
- Modify: `modules/storage/internal/service/primary/remote_test.go`
- Modify: `modules/storage/internal/service/view/maintenance_test.go`
- Modify: `modules/storage/internal/service/view/service_test.go`
- Modify: `modules/storage/internal/service/viewindex/service_test.go`
- Modify: `modules/storage/internal/infra/metadata/sqlite/crud_test.go`

- [ ] **Step 1: 删除旧 Coverage 字段**

删除 `active_coverage_start/end`。定义 `ViewIndexState`，包含 `index_id`、可空 `index_range`、`shard_checkpoints`、`view_version`、`view_schema_hash`；未完成首次构建时整个 Index Range 为空，不使用两个空字符串伪装有效范围。

- [ ] **Step 2: 定义范围语义**

`indexed_from/indexed_to` 是闭区间，值统一为归一化后的 UTC RFC3339Nano（`2006-01-02T15:04:05.000000000Z`）。TimeSeries View 使用 `TimeSeriesKey.data_time` 的值域；Record View 使用 `RecordKey.version` 的值域，Task 10 必须保证 Record Version 可规范化为同一时间格式。

它们表示当前索引经过 Backfill 和 Catch-up 后承诺完整服务的业务时间/版本范围，不是 Shard Sequence、消息发布时间、View Version、系统当前时间的简单复制，也不是 DuckDB/Bleve 中实际存在行或当前 BatchWrite 的最小/最大时间。区间内没有返回行表示“确认没有数据”，不能表示“尚未构建”。

- [ ] **Step 3: 构建和增量更新范围**

Backfill 捕获归一化 `snapshot_end`，计算 `indexed_from = snapshot_end - retention_window`，扫描该闭区间并 Catch-up。只有 ViewBuilder 已追平构建时观察到的所有相关 Shard Head 后，Maintenance/重建进度协调逻辑才能通过 `IndexRangeUpdate` 设置或推进 `indexed_to`。普通实时 BatchWrite 默认 `IndexRangeUpdate=nil`，不能根据本批行时间自行扩大范围；定时维护只有在所有 Shard Checkpoint 追平当前 Head 时才能推进安静时段的 `indexed_to`。

View 物理清理或 48H Host Metrics 清理产生的 DELETE 全部 Apply 成功后，才能通过 `IndexRangeUpdate` 推进 `indexed_from` 到新的清理边界。两个边界只能向前推进且必须满足 `indexed_from <= indexed_to`；先推进范围再删除、删除失败仍推进范围、单个 Shard 未追平却推进 `indexed_to`，均视为正确性错误。

新增内部 `GetShardState` RPC，返回固定 Shard ID、`last_committed_sequence` 和 `last_committed_at`。PrimaryStore 只向 ViewBuilder/Maintenance 聚合暴露相关 Dataset 的 Shard Head；该 RPC 不进入 Gateway。Sequence=0 的空 Shard 视为已追平，使用 Backfill Snapshot End 作为范围上界。

- [ ] **Step 4: 查询严格校验**

DataView 查询前校验 View/Dataset active、ActiveIndex 存在、Schema 就绪、请求范围包含于 Index Range、Checkpoint 未落后已观察 Shard Head。任一不满足返回明确 `VIEW_NOT_READY` 或新增的 `VIEW_RANGE_NOT_AVAILABLE`，不得返回普通成功和部分行。

- [ ] **Step 5: 响应返回服务范围**

TimeSeries/Record 查询响应增加 `served_range` 和 `complete`。成功响应必须 `complete=true`；错误响应携带当前可服务范围，便于 UI 和调用方展示。

- [ ] **Step 6: 防止重建竞态**

构建开始记录各 Shard Head，Backfill 后回放/刷新到这些 Head；切换前再次读取 Head 并追平。不能仅依赖 `data_time` 的重叠窗口判断完成。

- [ ] **Step 7: Run and commit**

```bash
(cd modules/storage && env GOCACHE=/tmp/moox-gocache CGO_ENABLED=1 go test -count=1 ./internal/service/view/... ./internal/service/viewindex ./internal/infra/metadata/sqlite)
git add modules/storage
git commit -m "fix(storage): enforce materialized view ranges"
```

### Task 10: 落实 Schema、Merge 和错误契约

**Files:**
- Modify: `modules/storage/proto/access.proto`
- Modify: `modules/storage/proto/common.proto`
- Modify: `modules/storage/proto/metadata.proto`
- Modify: `modules/storage/internal/core/schema/validator.go`
- Modify: `modules/storage/internal/core/schema/validator_test.go`
- Modify: `modules/storage/internal/service/access/validate.go`
- Modify: `modules/storage/internal/service/access/validate_test.go`
- Modify: `modules/storage/internal/service/access/errors.go`
- Rename: `modules/storage/internal/core/response/` -> `modules/storage/internal/retinfo/`
- Modify: `modules/storage/internal/retinfo/retinfo.go`
- Modify: `modules/storage/internal/retinfo/retinfo_test.go`
- Modify: `modules/storage/internal/service/primary/remote.go`
- Modify: `modules/storage/internal/service/primary/remote_test.go`
- Modify: `modules/storage/internal/bootstrap/metadata/seed.go`
- Modify: `modules/storage/internal/bootstrap/metadata/seed_test.go`
- Modify: `modules/storage/config/metadata.seed.yaml`
- Modify: `modules/storage/internal/infra/metadata/sqlite/crud_dataset.go`
- Modify: `modules/storage/internal/infra/metadata/sqlite/crud_test.go`
- Modify: `modules/storage/schema/metadata.sql`
- Modify: `modules/cli/internal/command/storage_import.go`
- Modify: `modules/collector/internal/sources/binance/storage_rpc.go`
- Modify: `modules/factor/internal/storageio/client.go`
- Modify: `modules/factor/internal/storageio/writeback.go`
- Modify: `modules/monitor/internal/hostmetrics/storage_writer.go`
- Modify: `modules/monitor/internal/metrics/storage.go`
- Modify: `modules/storage/cmd/bench/main_scenarios.go`
- Modify: `web/src/api/storage/types.ts`
- Modify: `web/src/views/data/fields/components/FieldEditorDrawer.vue`
- Modify: `web/src/views/data/datasets/components/dataset-column-panel.vue`
- Modify: `web/src/views/data/fields/field-workbench.test.ts`
- Modify: `web/src/views/data/metadata-catalog.test.ts`
- Modify: `Makefile`

- [ ] **Step 1: 严格 TypedValue 校验**

校验声明 `value_type` 与 oneof 分支一致；TIME 必须 RFC3339Nano 并归一到 UTC；JSON 必须可解析；Double 拒绝 NaN/Inf；Bytes、String、JSON 必须受单值字节上限约束。`list_value` 只允许用于支持列表的查询 Filter，不允许作为事实 `ColumnValue` 落库。

在 `common.proto` 导入 `google/protobuf/struct.proto`，并在 `TypedValue.oneof` 增加 `google.protobuf.NullValue null_value = 9`；`value_type` 仍表示 Schema 类型。Nil `TypedValue` 继续视为非法输入，不能同时承担 Null 和“未携带”两种语义。写入时空 RecordKey Version 由 PrimaryStore 在分片前统一生成一个 UTC RFC3339Nano 并回传；非空 Version 必须可归一化为 RFC3339Nano，确保 Record View 的范围和保留窗口可以确定比较。

- [ ] **Step 2: 拒绝模糊批次**

拒绝空写入、Nil Row/Key、重复 Key、重复列、未知列、跨 Space/Dataset 批次、超过最大行数或总字节数的请求。默认公共请求上限为 1000 行/4MiB、单行编码后 1MiB；DataShard 还必须在 Commit 前验证完整 RowsChanged MooxMessage 不超过 EventBus 配置的 8MiB MaxPayload。若合并旧值后批次超限，返回类型化 `BATCH_TOO_LARGE`；PrimaryStore 只可对尚未提交的该 Shard Batch 做有界二分重试，单行仍超限则明确失败，不能提交事实后才发现事件发不出去。

- [ ] **Step 3: 把事实写入明确命名为 Merge 并定义三态**

无兼容地把 `WriteTimeSeriesRows` / `WriteRecordRows` 及其 Req/Rsp 改为 `MergeTimeSeriesRows` / `MergeRecordRows`。同步修改 CLI、Collector、Factor、Monitor、Bench、Web 和测试调用方。Merge 的契约固定为“提供的字段合并到现有行，未提供字段保持不变，目标行不存在时创建”；本计划不增加 `write_mode`，也不增加没有实际调用场景的 Replace RPC。

```text
列未出现                 -> 保留旧值
TypedValue.null_value    -> 保存显式 Null
removed_column_names     -> 删除已存单元格
removed_attribute_names  -> 删除 Attribute
```

在 `TimeSeriesRow` 和 `RecordRow` 增加 `repeated string removed_column_names = 4` 与 `repeated string removed_attribute_names = 5`。服务端拒绝同一个列/属性同时出现在 Set 和 Remove 集合中；RowsChanged 的完整 UPSERT 快照不得携带 Remove 集合。

Required 列在合并后的完整行上校验，不能被 Null 或 Remove。

- [ ] **Step 4: 删除虚假 Schema 能力**

实现 `required`；删除当前无法在分布式 Pebble 中正确保证的 `is_unique` 和未执行的 `validation_rule_json`，同步清理 Proto、SQL、Seed、UI 和文档。Dataset Schema 每次修改必须生成新的 `schema_hash`；已有事实数据时禁止改变现有列类型、删除列或新增 Required 列，除非先执行显式清空流程。新增可选列允许继续写入，但所有引用该 Dataset 的 ViewIndex 必须进入 Rebuild Required，不能让旧 Schema 索引继续显示 Ready。

- [ ] **Step 5: 将错误码和 RetInfo 收敛到单包**

使用单一 `internal/retinfo` 包定义 `Code`、带 `Code/Message/Cause` 的类型化错误，以及 `OK()`、`NewError()`、`FromError()`。`FromError()` 是内部错误到 `pb.RetInfo` 的唯一转换入口；未知错误统一返回不泄漏 SQL、文件路径和网络细节的 `INNER_ERR`。不再拆分 `errorcode`、`rpcresult`，也不保留 `response`、`errorinfo` 等平行包。

删除 `MetadataStoreCode` 和所有错误字符串解析。SQLite/Pebble/网络错误必须在所属服务边界转换为明确的 `retinfo.Code`；远程响应 `ret_info=nil` 视为协议错误，Pebble/网络错误不得映射为 `INVALID_PARAM`。`retinfo` 不负责日志、数据库判断和业务规则。

- [ ] **Step 6: Run and commit**

```bash
(cd modules/storage && env GOCACHE=/tmp/moox-gocache go test -count=1 ./internal/core/schema ./internal/retinfo ./internal/service/access ./internal/service/primary ./internal/infra/metadata/sqlite)
bash scripts/test-storage-consistency-contract.sh
pnpm --dir web exec vitest run src/views/data/fields/field-workbench.test.ts src/views/data/metadata-catalog.test.ts
pnpm --dir web exec eslint . --max-warnings=0
pnpm --dir web exec prettier --check .
git add modules/storage web/src
git add Makefile scripts/test-storage-consistency-contract.sh
git commit -m "fix(storage): enforce fact schema and error contracts"
```

Expected: Stage A 的全部 Contract Subtest 通过；此时把 `test-storage-consistency-contract` 加入 `make verify`，之后不得再作为可选测试。

### Task 11: 建立严格 DataShard 边界并冻结拓扑

**Files:**
- Modify: `modules/storage/proto/metadata.proto`
- Modify: `modules/storage/internal/core/router/resolver.go`
- Modify: `modules/storage/internal/core/router/resolver_test.go`
- Modify: `modules/storage/internal/service/access/metadata_infra.go`
- Modify: `modules/storage/internal/infra/metadata/sqlite/crud_store.go`
- Modify: `modules/storage/schema/metadata.sql`
- Modify: `modules/storage/config/metadata.seed.yaml`
- Modify: `modules/storage/internal/bootstrap/metadata/seed.go`
- Modify: `modules/storage/internal/bootstrap/metadata/seed_test.go`
- Modify: `modules/storage/internal/service/access/metadata_infra_test.go`

- [ ] **Step 1: 显式建模 Shard**

将 `PrimaryStoreNode/Route` 改为 `ShardNode/ShardRoute`。ShardNode 直接描述 `shard_id`、所在 Node Service Gateway ID/Target、Pebble 所有权和状态，不保存 DataShard 物理 Listener，不再通过通用 Device 间接寻找第一个 Pebble。

- [ ] **Step 2: 请求绑定 Endpoint 和 Shard**

PrimaryStore 根据 Route 选择已绑定 Shard Client；嵌入 Shard 使用 LocalClient，独立 Shard 使用目标节点 Service Gateway 的特权 tRPC Route。物理请求只携带期望 `shard_id`，DataShard 必须与启动配置比对。删除调用方可指定 DataShard 物理地址、`device_table`、任意 Node ID 或 Engine 的能力。

- [ ] **Step 3: 首次写入锁定拓扑**

Metadata 保存 Dataset 的 `topology_hash` 和 `topology_locked_at`。PrimaryStore 在 Dataset 第一次写入尝试前先锁定当前 Route/Node 集合；即使随后物理写失败也保留锁，避免跨 SQLite/Pebble 伪造分布式原子性。后续修改节点、权重、Hash Rule、Pool 或优先级时，只要会改变已锁定 Dataset 的放置就返回冲突。

- [ ] **Step 4: 禁止静默降级**

未知 Hash Rule、重复/重叠 Route、空候选、Inactive Shard 都返回类型化错误。节点失效不得自动把 Key 路由到其他 Shard。

- [ ] **Step 5: 明确清空流程**

只有显式删除 Dataset 全部事实数据并重置拓扑锁，或未来独立迁移工具完成后，才能应用新拓扑。本计划不实现迁移工具。

- [ ] **Step 6: Run and commit**

```bash
(cd modules/storage && env GOCACHE=/tmp/moox-gocache go test -race -count=1 ./internal/core/router ./internal/service/access ./internal/infra/metadata/sqlite)
git add modules/storage examples
git commit -m "fix(storage): lock dataset shard placement"
```

### Task 12: 修复 Metadata 真分页、缓存和时间戳

**Files:**
- Modify: `modules/storage/internal/infra/metadata/sqlite/crud_helpers.go`
- Modify: `modules/storage/internal/infra/metadata/sqlite/crud_dataset.go`
- Modify: `modules/storage/internal/infra/metadata/sqlite/crud_field_group.go`
- Modify: `modules/storage/internal/infra/metadata/sqlite/crud_space.go`
- Modify: `modules/storage/internal/infra/metadata/sqlite/crud_store.go`
- Modify: `modules/storage/internal/infra/metadata/sqlite/crud_subject.go`
- Modify: `modules/storage/internal/infra/metadata/sqlite/crud_view.go`
- Modify: `modules/storage/internal/infra/metadata/sqlite/crud_view_index.go`
- Modify: `modules/storage/internal/infra/metadata/cache/store.go`
- Modify: `modules/storage/internal/infra/metadata/cache/store_test.go`
- Modify: `modules/storage/internal/infra/metadata/sqlite/store.go`
- Modify: `modules/storage/schema/metadata.sql`
- Modify: `modules/storage/internal/infra/metadata/sqlite/crud_test.go`
- Modify: `modules/storage/internal/infra/metadata/sqlite/store_test.go`
- Modify: `modules/storage/internal/service/access/service_test.go`

- [ ] **Step 1: SQL 层完成真实分页**

所有 List RPC 使用确定性 ORDER BY、LIMIT、OFFSET 或 Cursor，并在确实请求精确总数时执行 COUNT。禁止全表读入后在 Go 中切片。

- [ ] **Step 2: 拆分缓存边界**

缓存只保留 Space、Dataset、Field、View、ShardNode、ShardRoute 等小型路由/Schema 目录。`ArchiveFile`、构建历史和持续增长的运行记录直接分页查 SQLite，不进入全量 Snapshot。

- [ ] **Step 3: 使用目标化失效**

CRUD 成功后只更新/失效受影响实体和二级索引。禁止每次写入同步刷新全部 Metadata；缓存刷新失败不能把已经提交的 SQLite 事务伪装成未提交。

- [ ] **Step 4: Snapshot 使用单个读事务**

确实需要一致性目录快照时，在同一个 SQLite Read Transaction 中读取全部小型实体，完成后一次原子替换缓存指针，不能发布混合版本。

- [ ] **Step 5: SQL 时间戳为唯一事实源**

`created_at/updated_at` 从 `c_ctime/c_mtime` 扫描到 Proto；`c_attrs_json` 只保存扩展 Attributes，不能再复制完整 Proto 和时间戳。

- [ ] **Step 6: 性能回归**

构造至少 10,000 ArchiveFile 和 2,000 Dataset/View 元数据，断言页面查询只扫描目标页，缓存初始化不读取 ArchiveFile，连续 CRUD 不触发全量刷新。

- [ ] **Step 7: Run and commit**

```bash
(cd modules/storage && env GOCACHE=/tmp/moox-gocache go test -race -count=1 ./internal/infra/metadata/sqlite ./internal/infra/metadata/cache ./internal/service/access)
git add modules/storage
git commit -m "perf(storage): bound metadata queries and cache"
```

### Task 13: 原子完成服务、Proto 和包结构重命名

**Files:**
- Rename: `modules/storage/proto/access.proto` -> `modules/storage/proto/primary_store.proto`
- Rename: `modules/storage/proto/store.proto` -> `modules/storage/proto/data_shard.proto`
- Rename: `modules/storage/proto/view.proto` -> `modules/storage/proto/data_view.proto`
- Split: `modules/storage/proto/metadata.proto` into focused metadata Proto files
- Rename: `modules/storage/internal/service/access/` -> split `metadata/` and `primarystore/`
- Rename: `modules/storage/internal/service/primary/` -> `datashard/`
- Rename: `modules/storage/internal/service/view/` -> `dataview/` plus `viewbuilder/`
- Rename: `modules/storage/internal/service/view/builder/access_reader.go` -> `modules/storage/internal/service/viewbuilder/source_reader.go`
- Move: `modules/storage/internal/service/view/remote_metadata.go` -> `modules/storage/internal/service/dataview/remote_metadata.go`
- Consolidate: `modules/storage/internal/service/viewindex/` as final `viewindex/`
- Rename: `modules/storage/internal/core/factkey/` -> `modules/storage/internal/rowkey/`
- Split: `modules/storage/internal/core/factvalue/` -> `modules/storage/internal/typedvalue/` plus owner-local helpers
- Keep: `modules/storage/internal/retinfo/` as the single error-code and RPC RetInfo package established by Task 10
- Move: `modules/storage/internal/core/schema/` -> `modules/storage/internal/service/primarystore/schema/`
- Move: `modules/storage/internal/core/router/` -> `modules/storage/internal/service/primarystore/shardrouter/`
- Move: `modules/storage/internal/core/metadata/store.go` -> `modules/storage/internal/service/metadata/contracts.go`
- Move: `modules/storage/internal/core/viewindex/engine.go` -> `modules/storage/internal/service/viewindex/engine.go`
- Move: `modules/storage/internal/core/viewindex/engine_test.go` -> `modules/storage/internal/service/viewindex/engine_test.go`
- Move: `modules/storage/internal/core/viewindex/path.go` -> `modules/storage/internal/service/viewindex/path.go`
- Move: `modules/storage/internal/core/viewindex/path_test.go` -> `modules/storage/internal/service/viewindex/path_test.go`
- Move: `modules/storage/internal/core/viewindex/batch_write.go` -> `modules/storage/internal/service/viewindex/batch_write.go`
- Move: `modules/storage/internal/core/viewindex/batch_write_test.go` -> `modules/storage/internal/service/viewindex/batch_write_test.go`
- Delete: `modules/storage/internal/core/eventbus/` after replacing it with DataShard `MessagePublisher`, ViewBuilder `EventConsumer`, and owner-local test fakes
- Delete: `modules/storage/internal/core/` after all remaining packages have moved to their final owners
- Move: `modules/storage/internal/infra/metadata/sqlite/` -> `modules/storage/internal/service/metadata/sqlite/`
- Move: `modules/storage/internal/infra/metadata/cache/` -> `modules/storage/internal/service/metadata/cache/`
- Move: `modules/storage/internal/infra/device/pebble/` -> `modules/storage/internal/service/datashard/pebble/`
- Move: `modules/storage/internal/infra/device/duckdb/` -> `modules/storage/internal/service/viewindex/duckdb/`
- Move: `modules/storage/internal/infra/device/bleve/` -> `modules/storage/internal/service/viewindex/bleve/`
- Move: `modules/storage/internal/infra/eventbus/` -> `modules/storage/internal/service/datashard/messagepublisher/`
- Move: Storage RowsChanged consumer adapter -> `modules/storage/internal/service/viewbuilder/eventconsumer/`
- Move: `modules/storage/internal/bootstrap/eventbus/` assembly -> focused files under `modules/storage/cmd/server/`
- Regenerate: `modules/storage/proto/storagegen/*`
- Modify: `modules/storage/cmd/server/main.go`
- Modify: `modules/storage/cmd/server/main_test.go`
- Modify: `modules/storage/cmd/server/runtime_config.go`
- Modify: `modules/storage/cmd/server/health.go`
- Modify: `modules/storage/cmd/server/view_runtime.go`
- Modify: `modules/storage/cmd/server/host_metrics_cleanup_timer.go`
- Modify: `modules/storage/cmd/server/host_metrics_cleanup_timer_test.go`
- Modify: `modules/storage/cmd/server/plugin_config_test.go`
- Modify: `modules/archive/internal/consumer/decode.go`
- Modify: `modules/cli/internal/command/metadata_implementation.go`
- Modify: `modules/cli/internal/command/metadata_quant_seed_test.go`
- Modify: `modules/cli/internal/command/metadata_spaces.go`
- Modify: `modules/cli/internal/command/metadata_test.go`
- Modify: `modules/cli/internal/command/metadata_types.go`
- Modify: `modules/admin/internal/gateway/gateway.go`
- Modify: `modules/admin/internal/gateway/forward.go`
- Modify: `web/src/api/storage/access.ts`
- Modify: `web/src/api/storage/auth.ts`
- Modify: `web/src/api/storage/http.ts`
- Modify: `web/src/api/storage/metadata.ts`
- Modify: `web/src/api/storage/types.ts`
- Modify: `web/src/api/storage/view.ts`
- Modify: `scripts/storage-start.sh`
- Modify: `scripts/storage-stop.sh`
- Modify: `examples/service-deployments.seed.yaml`
- Delete: `modules/storage/proto/storagegen/access.pb.go`
- Delete: `modules/storage/proto/storagegen/access.trpc.go`
- Delete: `modules/storage/proto/storagegen/store.pb.go`
- Delete: `modules/storage/proto/storagegen/store.trpc.go`
- Delete: `modules/storage/proto/storagegen/view.pb.go`
- Delete: `modules/storage/proto/storagegen/view.trpc.go`
- Delete: `modules/storage/proto/storagegen/message.pb.go`
- Delete: `modules/storage/proto/storagegen/metadata.pb.go`
- Delete: `modules/storage/proto/storagegen/metadata.trpc.go`
- Delete: old Proto files and old package directories after callers compile against the final names

- [ ] **Step 1: 拆分 God Service**

`metadata` 只实现 Metadata RPC 和目录用例，并拥有 SQLite/Cache；`primarystore` 只实现事实校验、路由、聚合和内部 Scan，并拥有 Schema/ShardRouter；`datashard` 只实现物理 Pebble/Outbox，并拥有 MessagePublisher。三个服务通过窄接口装配，不互相读取具体实现字段。

- [ ] **Step 2: 整理 View 边界**

`dataview` 只查询活动索引；`viewbuilder` 拥有 EventConsumer 和 RowMapper，只消费 RowsCommitted、按需回读和物化；`viewindex` 拥有 DuckDB/Bleve 文件、内部 RPC 及其写入契约。ViewBuilder 生成 `viewindex.RowWrite` 并通过 `viewindex.BatchWrite` 调用 ViewIndex Apply；ViewIndex 负责校验 `RowKey`、MERGE/REPLACE/DELETE、Shard Checkpoint、View Version/View Schema Hash 和可选 Index Range Update，不创建独立 `viewrow` 包。DataView 和 ViewIndex 继续是独立包，在同一个 `storage-view` 进程中通过窄的 Typed LocalClient 接口调用，不读取对方具体字段，也不绕本机网络做 HTTP/tRPC 回环。

- [ ] **Step 3: 原子替换服务名**

```text
Access             -> PrimaryStore
AccessScan         -> PrimaryStoreScan
physical PrimaryStore -> DataShard
WriteTimeSeriesRows -> MergeTimeSeriesRows
WriteRecordRows     -> MergeRecordRows
WritePrimaryRows    -> MergeRows
ReadPrimaryRows     -> ReadRows
DeletePrimaryRows   -> DeleteRows
WriteViewIndex      -> ApplyViewIndex
ViewIndexBatch      -> ViewIndexBatchWrite
ViewIndex SchemaHash -> ViewSchemaHash
storage-access     -> storage-primary
role access        -> role primary
old role primary   -> role shard
```

不添加 Alias、Forwarding Package、Deprecated Service 或旧配置兼容分支。

- [ ] **Step 4: 删除 Core 总目录并完成垂直归属**

不使用 `core`、`common`、`shared` 之类的总括目录。`factkey` 实际表达行身份规范化，因此直接移动为顶层 `internal/rowkey`；其职责固定为 TimeSeries/Record Key 构造解析、维度排序转义哈希和 UTC 时间格式。`factvalue` 中 TypedValue String/Numeric/Compare 移到顶层 `internal/typedvalue`，无生产调用的 `TimeInRange`/`ParseTime` 及其孤立测试直接删除，简单 `StringSet` 放回唯一调用方。错误码和 RPC 返回信息只使用 Task 10 建立的顶层 `internal/retinfo`。禁止创建含糊的 `key`、`value`、`response` 通用包，也不得创建 `errorcode`、`rpcresult` 平行包。

完成 Pebble -> DataShard、DuckDB/Bleve 与 RowKey/RowWrite/BatchWrite -> ViewIndex、SQLite/Cache -> Metadata、Schema/ShardRouter -> PrimaryStore、RowMapper/EventConsumer -> ViewBuilder 的物理移动。删除组合 Publisher/Subscriber 的 Storage `Bus` 和运行时 MemoryBus；DataShard 只依赖窄 `MessagePublisher`，ViewBuilder 只依赖窄 `EventConsumer`，测试在各所有者包内使用 Fake。只有通用 JetStream 编解码、发布和鉴权留在 `packages/jetstream`。移动完成后，活动 Storage 代码不得存在 `internal/core`、`internal/infra` 或独立 `viewrow` 目录。

- [ ] **Step 5: 拆分超大 Metadata Proto**

创建 `catalog.proto`、`view_metadata.proto`、`shard_metadata.proto`、`archive_registry.proto` 和 `metadata_service.proto`；仍然只暴露一个 Metadata Service，不增加微服务。

- [ ] **Step 6: 更新所有模块调用方**

同步 Collector、CloudNode、Archive、CLI、Admin、Web、测试、Mock、Seed、脚本和文档中的生成类型及服务名。

- [ ] **Step 7: 运行旧名扫描**

```bash
rg -n 'trpc\.moox\.storage\.(Access|AccessScan)|Write(TimeSeries|Record)Rows|WritePrimaryRows|ReadPrimaryRows|WriteViewIndex|ViewIndexBatch\b|active_schema_hash|PrimaryStore(Node|Route|Target|Key|Row)|storage[_-]access|moox-storage-access|ProjectionReader|active_coverage_|factkey|factvalue|DataChange|Envelope(Publisher)?|PublishEnvelope|RawEnvelope' \
  modules packages scripts web/src examples skills/moox docs \
  --glob '!superpowers/**' --glob '!代码审查报告-*.md'
test ! -d modules/storage/internal/core
test ! -d modules/storage/internal/viewrow
test ! -d modules/storage/internal/response
test ! -d modules/storage/internal/errorcode
test ! -d modules/storage/internal/rpcresult
```

Expected: 活动代码和当前文档无匹配；历史 Git 记录不需要修改。

- [ ] **Step 8: Run and commit**

```bash
make proto
(cd modules/storage && env GOCACHE=/tmp/moox-gocache CGO_ENABLED=1 go test -count=1 ./...)
(cd modules/archive && env GOCACHE=/tmp/moox-gocache go test -count=1 ./...)
git add --all
git commit -m "refactor(storage): establish final service boundaries"
```

### Task 14: 删除通用 Device、死代码和兼容分支

**Files:**
- Delete: `modules/storage/internal/service/archive/`
- Delete: `modules/storage/internal/infra/device/parquet/`
- Delete: `modules/storage/internal/infra/device/store.go`
- Delete: `modules/storage/internal/infra/device/` after engine moves leave it empty
- Delete: `modules/storage/internal/infra/metadata/` after Metadata moves leave it empty
- Delete: `modules/storage/internal/infra/eventbus/` after adapters move leave it empty
- Delete: `modules/storage/internal/infra/` after all live owners move and dead implementations are removed
- Verify absent: `modules/storage/internal/core/` after Task 13 moved all owner-specific and shared packages
- Delete: `modules/storage/core`
- Modify: `modules/storage/proto/shard_metadata.proto`
- Modify: `modules/storage/proto/archive_registry.proto`
- Regenerate: `modules/storage/proto/storagegen/*`
- Modify: `modules/storage/schema/metadata.sql`
- Modify: `modules/storage/internal/service/metadata/contracts.go`
- Modify: `modules/storage/internal/service/metadata/cache/store.go`
- Modify: `modules/storage/internal/service/metadata/cache/store_test.go`
- Modify: `modules/storage/internal/service/metadata/sqlite/crud_store.go`
- Modify: `modules/storage/internal/service/metadata/infrastructure.go`
- Modify: `modules/storage/internal/service/metadata/infrastructure_test.go`
- Modify: `modules/storage/config/metadata.seed.yaml`
- Modify: `modules/cli/internal/command/metadata_implementation.go`
- Modify: `modules/cli/internal/command/metadata_types.go`
- Modify: `modules/cli/internal/command/metadata_test.go`
- Modify: `web/src/api/storage/types.ts`
- Modify: `modules/archive/config/app.yaml`
- Modify: `modules/archive/internal/config/config.go`
- Modify: `modules/archive/internal/config/config_test.go`
- Modify: `modules/archive/internal/bootstrap/app.go`
- Modify: `modules/archive/internal/registry/client.go`
- Modify: `modules/archive/cmd/cli/main_test.go`
- Modify: `modules/storage/internal/config/loader.go`
- Modify: `modules/storage/internal/service/metadata/sqlite/store.go`
- Modify: `modules/storage/cmd/server/runtime_config.go`
- Modify: `modules/storage/cmd/server/runtime_config_test.go`
- Modify: `modules/storage/cmd/server/main.go`
- Modify: `modules/storage/cmd/server/main_test.go`

- [ ] **Step 1: 删除 Storage 内旧 Archive**

独立 `modules/archive` 是唯一 Archive 实现。删除 Storage 内未注册的 Archive Service、Parquet Device 和所有只服务于旧实现的 Timer/配置。

- [ ] **Step 2: 删除通用 Device**

Pebble 归属 DataShard；DuckDB/Bleve 归属 storage-view 本地 ViewIndex；Archive 使用 `ArchiveStore`/`archive_store_id`。删除试图统一 Pebble、DuckDB、Bleve、Parquet 的 Device 类型和路由，并断言 Storage 活动树中不存在 `internal/infra` 目录。

- [ ] **Step 3: 删除兼容代码**

删除 `Embedded` 旧配置、Schema V2 兼容、`all/access/primary/view_builder/view_query/view_index/archive` 等旧 Role 解析，只保留最终 `primary/shard/view` 装配语义。

- [ ] **Step 4: 删除空目录、旧共享包和无引用 Symbol**

运行 `rg`、`go list` 和 `go vet` 确认没有死包、无引用配置键、旧 SQL 表和旧 Seed。删除迁空后的 `internal/core`，并断言最终只存在顶层 `internal/rowkey`、`internal/typedvalue`、`internal/retinfo` 三个跨服务小包；不得存在独立 `viewrow`、`response`、`errorcode`、`rpcresult` 包。

- [ ] **Step 5: Run and commit**

```bash
make -C modules/storage/proto clean
make -C modules/storage/proto all
(cd modules/storage && env GOCACHE=/tmp/moox-gocache go test -count=1 ./...)
(cd modules/archive && env GOCACHE=/tmp/moox-gocache go test -count=1 ./...)
bash scripts/check-package-boundaries.sh
git add --all
git commit -m "refactor(storage): remove obsolete device abstractions"
```

### Task 15: 收紧配置、Context、Timer、健康和启动失败语义

**Files:**
- Modify: `modules/storage/cmd/server/runtime_config.go`
- Modify: `modules/storage/cmd/server/main.go`
- Modify: `modules/storage/cmd/server/view_runtime.go`
- Modify: `modules/storage/cmd/server/health.go`
- Modify: `modules/storage/internal/service/metadata/service.go`
- Modify: `modules/storage/internal/service/primarystore/service.go`
- Modify: `modules/storage/internal/service/datashard/service.go`
- Modify: `modules/storage/internal/service/dataview/service.go`
- Modify: `modules/storage/internal/service/viewbuilder/service.go`
- Modify: `modules/storage/internal/service/viewindex/service.go`
- Modify: `modules/storage/internal/service/datashard/pebble/store.go`
- Modify: `modules/storage/internal/service/datashard/pebble/store_test.go`
- Modify: `modules/storage/internal/service/viewindex/bleve/index.go`
- Modify: `modules/storage/internal/service/viewindex/bleve/index_test.go`
- Modify: `modules/storage/internal/service/viewbuilder/schedule.go`
- Modify: `modules/storage/internal/service/viewbuilder/schedule_test.go`
- Modify: `packages/timerjob/job.go`
- Modify: `packages/timerjob/job_test.go`
- Create: `modules/storage/config/storage_primary/trpc_go.yaml`
- Modify: `modules/storage/config/storage_view/trpc_go.yaml`
- Create: `modules/storage/config/storage_shard/trpc_go.yaml`
- Delete: `modules/storage/config/storage.yaml`
- Delete: `modules/storage/config/storage.access.yaml`
- Delete: `modules/storage/config/trpc_go.yaml`
- Delete: `modules/storage/config/trpc_go.access.yaml`

- [ ] **Step 1: 配置严格失败**

缺失配置、未知 YAML 字段、无效 Role、空 Shard ID、不安全路径、无效持续时间、重复服务名均使进程启动失败。禁止 Warning 后套默认值继续运行。

- [ ] **Step 2: 构造函数统一返回 Error**

Metadata、Pebble、JetStream、DuckDB、Bleve、Relay、Timer 初始化失败必须向 Main 返回 Error；删除 Panic 和静默 Nil 降级。

- [ ] **Step 3: 每进程只读取一个 YAML**

`storage-primary` 只读取 `storage_primary/trpc_go.yaml`，装配 Metadata、PrimaryStore 和默认嵌入 DataShard；`storage-view` 只读取 `storage_view/trpc_go.yaml`，装配 DataView、ViewBuilder、ViewIndex；可选 `moox-storage-shard` 只读取 `storage_shard/trpc_go.yaml`，只装配固定身份 DataShard。禁止一个进程扫描或合并多个 YAML。

- [ ] **Step 4: Context 可取消**

Pebble Scan/Read/Write 和 Bleve Index/Search 循环定期检查 `ctx.Err()`。进程入口使用 `trpc.BackgroundContext()`；异步队列、Relay、Timer 使用 `trpc.CloneContext` 和自己的 Timeout。

- [ ] **Step 5: View Timer 返回真实错误**

删除包级 Default Maintenance。注册 Timer 时用闭包捕获 Manager；参数错误、未初始化、维护失败都返回 Error。使用 `packages/timerjob` 的 Timeout、防重入和立即执行能力。

- [ ] **Step 6: 深化 Readiness**

`storage-primary` 检查 SQLite 事务、Pebble Probe、Shard Identity、Outbox Backlog 和 Relay；`storage-view` 检查 Metadata、JetStream Consumer、DuckDB/Bleve 打开状态、Checkpoint Lag 和磁盘空间。

- [ ] **Step 7: Run and commit**

```bash
(cd modules/storage && env GOCACHE=/tmp/moox-gocache CGO_ENABLED=1 go test -race -count=1 ./cmd/server ./internal/...)
git add modules/storage
git commit -m "fix(storage): fail closed on runtime health"
```

### Task 16: 建立双网关 Storage 链路并收敛发布和前端

**Files:**
- Create: `modules/admin/internal/gateway/storage_bff.go`
- Create: `modules/admin/internal/gateway/storage_bff_test.go`
- Create: `modules/admin/internal/gateway/storage_gateway_client.go`
- Create: `modules/admin/internal/gateway/storage_gateway_client_test.go`
- Modify: `modules/admin/internal/gateway/gateway.go`
- Modify: `modules/admin/internal/gateway/gateway_test.go`
- Modify: `modules/admin/internal/gateway/forward.go`
- Modify: `modules/admin/internal/gateway/forward_test.go`
- Modify: `packages/gatewayauth/auth.go`
- Modify: `packages/gatewayauth/auth_test.go`
- Modify: `packages/gatewayauth/client.go`
- Modify: `packages/gatewayauth/client_test.go`
- Create: `packages/gatewayauth/trpc.go`
- Create: `packages/gatewayauth/trpc_test.go`
- Modify: `packages/gatewayproxy/route.go`
- Modify: `packages/gatewayproxy/route_test.go`
- Modify: `packages/gatewayproxy/table.go`
- Modify: `packages/gatewayproxy/table_test.go`
- Create: `packages/gatewayproxy/trpc_frame.go`
- Create: `packages/gatewayproxy/trpc_frame_test.go`
- Create: `modules/gateway/internal/router/trpc_router.go`
- Create: `modules/gateway/internal/router/trpc_router_test.go`
- Modify: `modules/gateway/internal/router/router.go`
- Modify: `modules/gateway/internal/bootstrap/bootstrap.go`
- Modify: `modules/gateway/internal/bootstrap/bootstrap_test.go`
- Modify: `modules/gateway/internal/config/config.go`
- Modify: `modules/gateway/internal/config/config_test.go`
- Modify: `modules/gateway/config/app.yaml`
- Modify: `modules/gateway/config/trpc_go.yaml`
- Modify: `modules/gateway/test/e2e_test.go`
- Modify: `modules/gateway/README.md`
- Modify: `modules/collector/config/app.yaml`
- Modify: `modules/collector/internal/sources/binance/storage_config.go`
- Modify: `modules/collector/internal/sources/binance/storage_rpc.go`
- Modify: `modules/factor/config/app.yaml`
- Modify: `modules/factor/internal/storageio/client.go`
- Modify: `modules/factor/internal/registry/metadata_sync.go`
- Modify: `modules/monitor/config/app.yaml`
- Modify: `modules/monitor/internal/config/config.go`
- Modify: `modules/monitor/internal/metrics/storage.go`
- Modify: `modules/monitor/internal/hostmetrics/storage_reader.go`
- Modify: `modules/monitor/internal/hostmetrics/storage_writer.go`
- Modify: `modules/archive/config/app.yaml`
- Modify: `modules/archive/internal/config/config.go`
- Modify: `modules/archive/internal/backfill/backfill.go`
- Modify: `modules/archive/internal/registry/client.go`
- Modify: `modules/storage/internal/service/viewbuilder/source_reader.go`
- Modify: `modules/storage/internal/service/dataview/remote_metadata.go`
- Modify: `modules/admin/internal/service/sysdeploy/defaults.go`
- Modify: `modules/admin/internal/service/sysdeploy/defaults_test.go`
- Modify: `modules/admin/internal/service/sysdeploy/dao.go`
- Modify: `modules/admin/internal/service/sysdeploy/acceptance_test.go`
- Modify: `modules/admin/internal/service/sysdeploy/storage_topology_checker.go`
- Modify: `modules/admin/internal/service/sysdeploy/storage_topology_checker_test.go`
- Create: `web/src/api/storage/http.test.ts`
- Rename: `web/src/api/storage/access.ts` -> `web/src/api/storage/primary-store.ts`
- Modify: `web/src/api/storage/http.ts`
- Modify: `web/src/api/storage/metadata.ts`
- Modify: `web/src/api/storage/types.ts`
- Modify: `web/src/api/storage/view.ts`
- Modify: `web/src/views/ops/storage/nodes.vue`
- Modify: `web/src/views/ops/storage/routes.vue`
- Modify: `web/src/views/ops/storage/storage-management.test.ts`
- Modify: `scripts/build.sh`
- Modify: `scripts/release.sh`
- Modify: `scripts/deploy-moox.sh`
- Modify: `scripts/test-release-contract.sh`
- Modify: `scripts/test-deploy-moox-storage-profile.sh`
- Modify: `scripts/test-deploy-moox-storage-view.sh`
- Modify: `modules/cli/internal/setup/deploy/deploy.go`
- Modify: `modules/cli/internal/setup/deploy/deploy_test.go`
- Modify: `modules/cli/internal/setup/deploy/service.go`
- Modify: `modules/cli/internal/setup/deploy/service_test.go`
- Modify: `examples/service-deployments.seed.yaml`

- [ ] **Step 1: 把路由表提升为方法级路由**

`gatewayproxy.Table` 从只按 `service_id` 解析改为按 `(service_id, method)` 和 `(callee, method)` 解析，并为每条方法路由增加非空 `allowed_callers`。同一个逻辑 `storage` 允许存在多个 Route，但 `allowed_methods` 必须非空且互不重叠；空方法表、空 Caller 表、重复方法、同一 Key 指向多个 Upstream 必须使整个路由快照校验失败。HTTP 路由使用 `(service_id, method)`，原生 tRPC 路由使用请求头中的 `(callee, func)`，两种协议共享同一份不可变 Snapshot、超时、Body 上限、方法白名单和 Caller ACL。

SysDeploy 的服务部署 ExtraConfig 增加必填 `gateway_callers` 字符串数组，并原样编译为 Route `allowed_callers`；`*` 只允许出现在明确标记为所有已认证服务可调用的普通路由，Storage 特权路由禁止 `*`。Route Hash 的规范化输入必须包含排序去重后的 Methods 和 Callers，ACL 变更必须触发 Gateway 刷新。

- [ ] **Step 2: Node Service Gateway 增加原生 tRPC Unary Listener**

在现有 `/api/service/{service}/{method}` HTTP Listener 之外增加原生 tRPC TCP Listener。使用 tRPC-Go 官方 Frame/Codec 解码固定头和 `RequestProtocol`，只读取 Callee、Func、序列化方式、Request ID、Timeout 和认证元数据；业务 Payload 保持原始字节，不反序列化 Storage Proto。路由成功后只替换网络目标，保留 Callee/Func、Protobuf 或 JSON 序列化标记和响应错误语义。第一阶段只支持 Unary Request/Response；Streaming、Oneway、未知协议、超限 Frame 和方法白名单外调用必须明确拒绝。Frame 测试必须覆盖 TCP 分片/粘包、同连接连续及并发请求、乱序响应的 Request ID 对应、上游半关闭、Deadline 取消、响应超限和客户端提前断开；任何路径都必须释放上游连接和 Timeout。

Service HMAC 扩展到 tRPC Metadata，签名输入固定包含 Key ID、Caller、Target Gateway Node、Callee、Func、Timestamp、Nonce 和 Payload SHA-256；复用现有 Nonce Store 防重放。Gateway 的 `auth.credentials_file` 在启动时 Fail Closed 加载 `key_id -> caller + secret` 映射，Caller 必须由服务端根据 Key ID 得出，不能相信请求自报身份；凭证文件和单服务 Key File 都必须是 0600，暂不做热加载。Gateway 删除调用方伪造的内部身份字段，再向上游写入已验证 Caller、Trace 和 Gateway 身份。HTTP 与 tRPC Auth 共用凭证注册表、时钟偏差和审计指标，但不得混用用户登录令牌。

```yaml
version: 1
credentials:
  - key_id: admin-gateway
    caller: admin-gateway
    secret_file: secrets/admin-gateway.key
  - key_id: storage-view
    caller: storage-view
    secret_file: secrets/storage-view.key
```

配置拒绝未知字段、重复 Key ID、重复 Caller、空 Secret 和不安全文件权限。路由 ACL 只保存 Caller，不保存 Key ID 或 Secret。

- [ ] **Step 3: Admin Gateway 只做浏览器 BFF**

浏览器只访问 `/api/admin/storage/{method}`。Admin Gateway 校验用户登录态和 Storage 静态方法白名单，然后以固定 Caller `admin-gateway` 使用 Service HMAC 调用 Node Service Gateway 的原生 tRPC Listener，不得从 SysDeploy 解析并直连 Metadata、PrimaryStore 或 DataView 地址。BFF 的静态表只描述 `method -> callee`：事实方法到 PrimaryStore，Metadata 方法到 Metadata，查询方法到 DataView；PrimaryStoreScan、DataShard、ViewIndex、Timer、Maintenance 和任意范围 Delete 不得列入，Service Gateway 的 Caller ACL 还必须再次拒绝 `admin-gateway` 调用这些特权方法。

Admin BFF 不复制 Storage 业务类型或校验逻辑：将浏览器 JSON 作为 tRPC JSON Serialization Payload 发送，Node Gateway 原样转发，由目标 tRPC Handler 反序列化为生成的 Req；响应也以 JSON Serialization 原样返回浏览器。测试必须使用真实生成的 Storage Handler 证明 JSON/RetInfo 往返，而不是只断言 Mock 被调用。

- [ ] **Step 4: 服务间调用统一经过 Node Service Gateway**

Collector、Factor、Monitor、Archive、ViewBuilder 等服务继续使用生成的 tRPC Client 和 Protobuf Payload，但 Client Target 配置为 Node Service Gateway，原始 Callee/Func 用于方法级路由；调用方不得保存 Storage 物理地址。统一把各模块的 `access_target` / `metadata_target` 改为一份 `storage_rpc.gateway_target`、`gateway_node_id`、`key_id`、`hmac_key_file` 配置，不保留旧字段。部署工具为 `admin-gateway`、`collector`、`factor`、`monitor`、`archive`、`storage-view`、`storage-primary` 生成或引用独立 Key ID/Secret；禁止让所有服务共享一个可冒充任意 Caller 的密钥。

Metadata、PrimaryStore、DataView 是普通受信服务路由；PrimaryStoreScan 是只允许 `storage-view`、`archive` 和明确维护身份的特权路由。DataShard 嵌入时走 LocalClient；独立部署时注册在所在节点 Gateway，只允许 `storage-primary` Caller，PrimaryStore 保存目标 Gateway 而不是 DataShard 物理地址。ViewIndex 始终进程内调用，不注册任何 Gateway Route。所有 Storage 物理 Listener 只绑定 Loopback，远端调用先到目标节点 Gateway。

浏览器直接调用 `/api/service/storage/...` 必须因缺少 Service HMAC 返回 401；已登录用户令牌不能替代 Service HMAC。Admin Gateway 到 Node Gateway 失败时保留明确的 tRPC Code/RetInfo，不得降级为直连 Storage。

- [ ] **Step 5: 前端统一 Storage URL**

所有 Storage API 使用同一个 Helper 构造 `/api/admin/storage/` URL；删除 `access.ts` 和内部 Service ID 选择逻辑。页面术语统一为 PrimaryStore、DataShard、DataView。

- [ ] **Step 6: 标准发布只有两个 Storage 进程**

Build `all`、Release、Deploy 和 CLI Status 只默认处理 `moox-storage-primary` 和 `moox-storage-view`。默认部署同时为 Node Service Gateway 生成 Metadata、PrimaryStore、DataView 的普通方法级路由，以及带 Caller ACL 的 PrimaryStoreScan 特权路由；不得生成 ViewIndex 路由。`build.sh storage-shard` 可显式构建 `moox-storage-shard`，供高级私网手工部署；它不进入 `all`、Release 归档或默认部署清单，只能在目标节点生成限制为 `storage-primary` Caller 的 DataShard 特权路由，不得注册浏览器或普通服务路由。

- [ ] **Step 7: 明示单副本限制**

Shard 管理页面和部署文档必须说明：无副本、无自动故障转移、无自动迁移；Shard 不可用时相关数据不可用，需人工修复或恢复。

- [ ] **Step 8: 双协议和双网关 E2E**

使用真实 tRPC Storage 测试服务覆盖两条完整链路：`Browser JSON -> Admin Gateway -> tRPC Node Gateway -> Storage`，以及 `Generated tRPC Client -> Node Gateway -> Storage`。同时断言方法级分流到 Metadata/PrimaryStore/DataView、用户令牌不能访问 Service Gateway、Service HMAC 重放被拒绝、未知方法被拒绝、后端 tRPC Code 原样返回、路由刷新后两个协议同时切换且失败刷新继续使用最后一份合法快照。

- [ ] **Step 9: Run and commit**

```bash
(cd modules/admin && env GOCACHE=/tmp/moox-gocache go test -count=1 ./internal/gateway ./internal/service/sysdeploy)
(cd modules/gateway && env GOCACHE=/tmp/moox-gocache go test -race -count=1 ./internal/... ./test)
(cd packages/gatewayauth && env GOCACHE=/tmp/moox-gocache go test -count=1 ./...)
(cd packages/gatewayproxy && env GOCACHE=/tmp/moox-gocache go test -count=1 ./...)
(cd modules/collector && env GOCACHE=/tmp/moox-gocache go test -count=1 ./internal/sources/binance)
(cd modules/factor && env GOCACHE=/tmp/moox-gocache go test -count=1 ./internal/storageio ./internal/registry)
(cd modules/monitor && env GOCACHE=/tmp/moox-gocache go test -count=1 ./internal/config ./internal/metrics ./internal/hostmetrics)
(cd modules/archive && env GOCACHE=/tmp/moox-gocache go test -count=1 ./internal/config ./internal/backfill ./internal/registry)
(cd modules/cli && env GOCACHE=/tmp/moox-gocache go test -count=1 ./internal/setup/...)
pnpm --dir web exec vitest run src/api/storage
bash scripts/test-deploy-moox-storage-profile.sh
bash scripts/test-deploy-moox-storage-view.sh
git add modules/admin modules/gateway modules/cli packages/gatewayauth packages/gatewayproxy web scripts examples
git commit -m "feat(gateway): route storage through admin and service gateways"
```

### Task 17: 按职责拆分剩余前端维护热点

**Files:**
- Modify: `web/src/views/collector/cloud-node/cloud-node.vue`
- Modify: `web/src/views/collector/cloud-node/cloud-node.scss`
- Modify: `web/src/views/collector/cloud-node/cloud-node-model.ts`
- Modify: `web/src/views/collector/cloud-node/cloud-node-model.test.ts`
- Modify: `web/src/views/collector/cloud-node/cloud-node-batch-service.ts`
- Create: `web/src/views/collector/cloud-node/components/cloud-node-table.vue`
- Create: `web/src/views/collector/cloud-node/components/cloud-node-batch-add-dialog.vue`
- Create: `web/src/views/collector/cloud-node/components/cloud-node-batch-plan-dialog.vue`
- Create: `web/src/views/collector/cloud-node/components/cloud-node-deploy-dialog.vue`
- Create: `web/src/views/collector/cloud-node/components/cloud-node-detail-drawer.vue`
- Create: `web/src/views/collector/cloud-node/composables/use-cloud-node-list.ts`
- Create: `web/src/views/collector/cloud-node/composables/use-cloud-node-list.test.ts`
- Create: `web/src/views/collector/cloud-node/composables/use-cloud-node-batch-add.ts`
- Create: `web/src/views/collector/cloud-node/composables/use-cloud-node-batch-add.test.ts`
- Create: `web/src/views/collector/cloud-node/composables/use-cloud-node-deploy.ts`
- Create: `web/src/views/collector/cloud-node/composables/use-cloud-node-deploy.test.ts`
- Modify: `web/src/views/data/view-browse/index.vue`
- Modify: `web/src/views/data/view-browse/view-browse-utils.ts`
- Create: `web/src/views/data/view-browse/components/view-query-controls.vue`
- Create: `web/src/views/data/view-browse/components/view-result-table.vue`
- Create: `web/src/views/data/view-browse/components/view-row-detail-drawer.vue`
- Create: `web/src/views/data/view-browse/composables/use-view-catalog.ts`
- Create: `web/src/views/data/view-browse/composables/use-view-catalog.test.ts`
- Create: `web/src/views/data/view-browse/composables/use-view-query.ts`
- Create: `web/src/views/data/view-browse/composables/use-view-query.test.ts`
- Create: `web/src/views/data/view-browse/composables/use-view-kline.ts`
- Create: `web/src/views/data/view-browse/composables/use-view-kline.test.ts`
- Create: `web/tests/cloud-node-workflows.spec.ts`
- Create: `web/tests/storage-view-browse.spec.ts`

- [ ] **Step 1: 先锁定现有行为**

为 CloudNode 锁定列表筛选、分页、批量创建规划、批量变更状态、单个/批量部署、编辑、删除、包详情和进度；为 View Browse 锁定 View/Dataset 目录、Filter/Sort、TimeSeries/Record 分页、详情和 Kline。测试先覆盖当前可见行为、API Payload 和错误状态，避免拆分过程中“页面能打开但工作流缺失”。

- [ ] **Step 2: 拆分 CloudNode 工作流状态**

`use-cloud-node-list` 只管理查询、分页、选择和基础 CRUD；`use-cloud-node-batch-add` 只管理地区容量、规划和批次提交；`use-cloud-node-deploy` 只管理包分页、单个/批量部署和下载进度。三个 Composable 通过显式输入/输出协作，不共享可变模块级单例，也不互相调用私有函数。

- [ ] **Step 3: 拆分 CloudNode 展示组件**

Table、Batch Add、Batch Plan、Deploy 和 Detail 各自成为受控组件。展示组件只接收 Props、发出 Emits，不直接调用 API。根 `cloud-node.vue` 只负责编排三个工作流和弹层可见性；现有 CloudAccount/FunctionPackage 管理子页面保持独立，不复制其逻辑。

- [ ] **Step 4: 拆分 View Browse 数据工作流**

`use-view-catalog` 管理 View/Dataset/Column/Field/Factor 上下文；`use-view-query` 管理 Filter、Sort、Cursor/Page、TimeSeries/Record 查询和完整性错误；`use-view-kline` 管理 Kline 条件和数据加载。Storage Task 9 新增的 `served_range/complete` 必须在 Query Composable 中统一判断，不能由多个组件各自解释。

- [ ] **Step 5: 拆分 View Browse 展示组件**

Query Controls、Result Table、Row Detail 成为无 API 副作用的组件；现有 `kline-modal.vue` 保持 Kline 展示所有权。根 `index.vue` 只负责页面级选择、布局和组件编排。不得新增 Card 套 Card，也不得因抽取组件改变工具栏、表格密度或响应式布局。

- [ ] **Step 6: 用职责而非行数验收**

验收标准是每个 Composable 只有一个业务变化原因、组件没有跨域 API 调用、根页面没有大段网络/批处理算法，而不是机械追求固定行数。禁止把原代码原样搬进一个 `useEverything` 或 `utils.ts`。共享纯函数继续放在现有 Model/Utils，并有独立单测。

- [ ] **Step 7: 视觉和交互回归**

使用 Playwright 在 1440x900 和 390x844 验证两个页面：无文字溢出、工具栏和表格不重叠、Dialog/Drawer 可滚动、移动端操作可达、加载/空/错误状态稳定。对关键列表和弹层保存截图，并检查浏览器 Console 无 Error。

- [ ] **Step 8: Run and commit**

```bash
pnpm --dir web exec vitest run src/views/collector/cloud-node src/views/data/view-browse
pnpm --dir web exec playwright test tests/cloud-node-workflows.spec.ts tests/storage-view-browse.spec.ts
pnpm --dir web exec eslint . --max-warnings=0
pnpm --dir web exec prettier --check .
pnpm --dir web build
git add web/src/views/collector/cloud-node web/src/views/data/view-browse web/tests
git commit -m "refactor(web): split storage and cloud node workflows"
```

### Task 18: 同步架构文档、门禁和端到端验收

**Files:**
- Create/Finalize: `docs/存储层架构.md`
- Modify: `docs/架构总览.md`
- Modify: `docs/存储引擎架构.md`
- Modify: `docs/存储服务架构与部署.md`
- Modify: `docs/存储概念与设计意图.md`
- Modify: `docs/存储目标架构与元数据.md`
- Modify: `docs/协议设计.md`
- Modify: `docs/节点服务网关架构.md`
- Modify: `docs/量化金融数据概念.md`
- Modify: `docs/内置市场行情采集架构.md`
- Modify: `docs/采集任务管理.md`
- Modify: `docs/云节点执行平台架构.md`
- Modify: `docs/主机监控架构设计.md`
- Modify: `docs/运维/数据保留与磁盘空间.md`
- Modify: `docs/行情数据归档模块设计.md`
- Modify/Finalize: `docs/superpowers/specs/2026-07-18-storage-primary-shard-boundary-design.md`
- Modify: `README.md`
- Modify: `modules/storage/README.md`
- Modify: `modules/archive/README.md`
- Modify: `modules/cli/README.md`
- Modify: `modules/collector/README.md`
- Modify: `modules/factor/README.md`
- Modify: `modules/strategy/internal/rpc/frontend_service_test.go`
- Modify: `scripts/test-deploy-moox-strategy.sh`
- Modify: `scripts/test-deploy-moox-strategy-e2e.sh`
- Modify: `scripts/test-docs-architecture.sh`
- Modify: `scripts/test-quality-gates.sh`
- Modify: `scripts/test-storage-boundary-contract.sh`
- Modify: `scripts/test-storage-consistency-contract.sh`
- Modify: `scripts/check-package-boundaries.sh`
- Modify: `Makefile`

- [ ] **Step 1: 文档写清最终数据流**

`docs/存储层架构.md` 必须覆盖：

```text
Browser -> Admin Gateway /api/admin/storage/{method} -> Node Service Gateway tRPC -> Metadata | PrimaryStore | DataView
Go Service -> Node Service Gateway tRPC -> Metadata | PrimaryStore | DataView
PrimaryStore -> DataShard -> Pebble + RowsChanged MooxMessage Outbox
TimeSeriesRowsChanged | RecordRowsChanged -> ViewBuilder -> ChangeBatch -> batch reread -> RowMapper -> BatchWrite -> ViewIndex.Apply -> DuckDB/Bleve
TimeSeriesRowsChanged UPSERT -> Archive Journal/Parquet/COS
RowsChanged DELETE -> View 删除；Archive Ignore/ACK
```

- [ ] **Step 2: 文档写清一致性和限制**

说明 Shard Sequence、ViewIndex Checkpoint、`indexed_from/indexed_to`、Merge 三态、Outbox 连续前缀、View 完整行替换、部分成功、重建流程、Archive 非当前状态备份，以及无 HA/Failover/Migration/Rebalance。明确 `rowkey`、`typedvalue`、`retinfo`、`MooxMessage`、两种 RowsChanged 消息的职责；说明 `RowKey`、`RowWrite`、`BatchWrite` 是 ViewIndex Apply 契约而不是独立 `viewrow` 包，解释 UPSERT/DELETE、Checkpoint 来源、`ViewSchemaHash` Fence 和 `IndexRangeUpdate` 的受控推进规则。明确 `indexed_from/indexed_to` 使用 UTC RFC3339Nano，TimeSeries 对应 `data_time`、Record 对应时间化 `version`，不是实际行 Min/Max。并说明 Pebble/Outbox、DuckDB/Bleve、SQLite/Cache、RowMapper/EventConsumer 分别由哪个领域服务拥有。

单独画出双网关信任边界：浏览器只能持用户令牌并进入 Admin Gateway；Admin Gateway 使用 Service HMAC 通过 Node Service Gateway 调用 Storage；服务间生成客户端直接通过 Node Service Gateway；任何调用方都不得保存或使用 Metadata、PrimaryStore、DataView 的物理地址。

- [ ] **Step 3: 文档写清磁盘治理**

保留现有 48H Host Metrics 自动清理、每次最多 10 批、每批 1000 条、超时和防重入；说明 Pebble Compaction、DuckDB/Bleve A/B 重建、Archive 永久保留对磁盘的不同影响。

- [ ] **Step 4: 添加架构扫描门禁**

脚本必须拒绝活动源码和现行文档中的旧 Access/PrimaryStore 物理命名、Mutation/DataChange/Projection、`active_coverage_*`、ViewIndex 通用 `schema_hash` / `active_schema_hash`、旧的精确符号 `ViewIndexBatch` / `WriteViewIndex`、`factkey`、`factvalue`、Envelope 别名、通用 Device、Storage `internal/core` / `internal/infra`、独立 `viewrow`、`response`、`errorcode`、`rpcresult` 包、旧 Runtime Role、旧配置和过时文档链接；扫描 `ViewIndexBatch` 时必须使用词边界，不能误伤新类型 `ViewIndexBatchWrite`。同时验证 `docs/架构总览.md` 链接 `docs/存储层架构.md`。Package Boundary 必须断言 Pebble 只能被 DataShard 导入、DuckDB/Bleve 及 `RowKey`/`RowWrite`/`BatchWrite` 只能被 ViewIndex 持有、SQLite/Cache 只能被 Metadata 导入、RowMapper/EventConsumer 只能属于 ViewBuilder；`retinfo` 不得包含日志、数据库错误识别和业务规则。`docs/superpowers/` 执行历史和带日期的历史审查报告可以保留原文，但不得被现行架构文档引用为当前事实源。

- [ ] **Step 5: 固化最初 CR 和后续讨论的已完成项**

在现有契约上增加或保留以下断言：Strategy 位于 `build.sh all`、Release 和默认 Deploy，SysDeploy 默认 Active/Gateway Enabled，前端入口所调用服务可达；`ListStrategyRuns`、`GetStrategyPerformance` 对非法 RFC3339、From>To 返回参数错误；架构总览覆盖 `go.work` 全部 Module；`make verify` 必须包含 Package Boundary、gofmt、Prettier、零 Warning ESLint；生产 Go 文件无 `context.Background()`；Gateway Route Refresh 和 HostAgent Sample 继续使用官方 tRPC Timer；Node Service Gateway 的 HTTP/tRPC 两种协议共享方法级路由且浏览器不能绕过 Admin Gateway；Factor 继续使用 `EventBatcher`；Host Metrics Cleanup 继续使用 48H、有界 10 批、每批 1000、超时、防重入；活动树中不存在 `packages/crypto`。

```bash
test -z "$(rg -l 'context\.Background\(\)' --glob '*.go' --glob '!**/*_test.go' --glob '!**/*pb.go' .)"
test -z "$(rg -l 'packages/crypto|package crypto' --glob '!docs/superpowers/**' .)"
test ! -d modules/storage/internal/core
test ! -d modules/storage/internal/viewrow
test ! -d modules/storage/internal/response
test ! -d modules/storage/internal/errorcode
test ! -d modules/storage/internal/rpcresult
(cd modules/storage && env GOCACHE=/tmp/moox-gocache go test -count=1 ./internal/rowkey ./internal/typedvalue ./internal/retinfo ./internal/service/viewindex)
(cd modules/strategy && env GOCACHE=/tmp/moox-gocache go test -count=1 ./internal/rpc)
(cd modules/gateway && env GOCACHE=/tmp/moox-gocache go test -count=1 ./internal/bootstrap)
(cd modules/hostagent && env GOCACHE=/tmp/moox-gocache go test -count=1 ./internal/app)
(cd modules/factor && env GOCACHE=/tmp/moox-gocache go test -count=1 ./internal/trigger)
(cd packages/security && env GOCACHE=/tmp/moox-gocache go test -count=1 ./...)
bash scripts/test-deploy-moox-strategy.sh
bash scripts/test-deploy-moox-strategy-e2e.sh
bash scripts/test-docs-architecture.sh
bash scripts/test-quality-gates.sh
```

- [ ] **Step 6: 完整 Storage E2E**

在全新临时目录启动 Embedded JetStream、Admin Gateway、Node Service Gateway、`storage-primary` 和 `storage-view`，为每个测试 Caller 使用独立 HMAC Key，验收：

```text
首次 Merge 创建 -> 再次局部 Merge -> PrimaryStore 完整行
TimeSeriesRowsChanged/RecordRowsChanged 共用的 Shard Sequence 连续且 Payload 完整
BatchWrite UPSERT/DELETE、Checkpoint CAS、ViewVersion/ViewSchemaHash Fence 全部生效
DuckDB/Bleve 查询结果一致
乱序 ACK 不删除非连续 Outbox
1001 行分页无漏数
Delete 后 View 消失而 Archive 保留
重启后 Outbox、Checkpoint 和 Sequence 恢复
超出 indexed_from/indexed_to 查询明确失败
indexed_from/indexed_to 对 TimeSeries data_time 与 Record version 使用 UTC RFC3339Nano，普通实时写入不能自行推进
错误 shard_id 和拓扑变更明确失败
Browser -> Admin Gateway -> tRPC Node Gateway -> Storage 成功
Generated tRPC Client -> Node Gateway -> Storage 成功
浏览器直连 Service Gateway、HMAC 重放和未授权方法明确失败
```

- [ ] **Step 7: 全量验证**

```bash
(cd modules/storage && env GOCACHE=/tmp/moox-gocache CGO_ENABLED=1 go test -race -count=1 ./...)
(cd modules/archive && env GOCACHE=/tmp/moox-gocache go test -race -count=1 ./...)
(cd modules/admin && env GOCACHE=/tmp/moox-gocache go test -race -count=1 ./...)
(cd modules/gateway && env GOCACHE=/tmp/moox-gocache go test -race -count=1 ./...)
(cd modules/cli && env GOCACHE=/tmp/moox-gocache go test -race -count=1 ./...)
(cd packages/gatewayauth && env GOCACHE=/tmp/moox-gocache go test -race -count=1 ./...)
(cd packages/gatewayproxy && env GOCACHE=/tmp/moox-gocache go test -race -count=1 ./...)
(cd modules/storage && env GOCACHE=/tmp/moox-gocache go vet ./...)
bash scripts/check-package-boundaries.sh
bash scripts/test-storage-boundary-contract.sh
bash scripts/test-storage-consistency-contract.sh
pnpm --dir web exec vitest run
pnpm --dir web exec eslint . --max-warnings=0
pnpm --dir web exec prettier --check .
pnpm --dir web build
make verify
```

Expected: 所有命令通过，命令不得修改 tracked files。

- [ ] **Step 8: 最终 Review**

新起独立 Review Agent，只提供最终设计、计划、Diff 和测试结果，要求按 P0/P1/P2 检查数据一致性、Outbox、分页、Delete、View Checkpoint、Shard 边界、配置和文档，不允许只做风格 Review。修复所有确认问题后重新运行 Step 7。

- [ ] **Step 9: Commit and push**

```bash
git status --short
git diff --check
git add --all
git commit -m "refactor(storage): complete architecture remediation"
git push
git status --short --branch
```

Expected: Push 成功，`main` 与 `origin/main` 同步，工作区干净。

## 问题到任务映射

| 审查问题 | 负责任务 |
| --- | --- |
| 局部 Merge 消息被当完整行 | Task 1、2、3、7、8 |
| Outbox 乱序和派生回退 | Task 1、3、4、7、8 |
| 分页跳过 26-1000 | Task 1、5 |
| Delete 不传播 | Task 1、2、3、6 |
| View 范围未校验 | Task 1、8、9 |
| DataShard 不是严格边界 | Task 3、11、13 |
| 动态加权路由无迁移 | Task 11、17 |
| 类型、Schema、Merge 校验不足 | Task 10 |
| 配置失败后静默默认 | Task 15 |
| Pebble/Bleve 忽略 Context | Task 15 |
| View Timer 吞错和全局实例 | Task 15 |
| Metadata 假分页和 O(N²) Cache | Task 12 |
| 通用 Device 模型误导 | Task 11、14 |
| Access God Service | Task 13 |
| Metadata 时间戳漂移 | Task 12 |
| 死 Archive/Parquet、空文件、兼容分支 | Task 14 |
| 命名、双网关边界、两进程部署 | Task 13、15、16 |
| Strategy 发布入口和非法时间参数回归 | Task 18 |
| `go.work`/README/架构总览漂移 | Task 18 |
| Package Boundary、gofmt、ESLint、Prettier 门禁 | Task 18 |
| CloudNode、View Browse 维护热点 | Task 17 |
| tRPC Context、Timer、Host Metrics Cleanup、Security 包命名回归 | Task 15、18 |
| 文档漂移和缺少运维信心 | Task 18 |
| `factkey`/`factvalue` 语义含糊 | Task 13、14、18 |
| `internal/core` 总目录缺少明确所有权 | Task 13、14、18 |
| `viewrow` 小包与 `rowkey` 边界不清 | Task 7、8、13、18 |
| ViewIndex `Batch`、`SchemaHash`、Shard 进度和范围更新语义不清 | Task 7、8、9、13、18 |
| `response` 混合错误分类和 RPC 返回构造 | Task 10、13、18 |
| Storage Infra 与领域生命周期分离 | Task 13、14、18 |
| 浏览器与服务间调用缺少双网关边界 | Task 16、18 |
| RowsUpdated 删除概念且通用 DataChange 隐藏领域差异 | Task 2、3、6、7 |
| Envelope 与实际 MooxMessage 命名重复 | Task 2、13、18 |
| Patch 命名不直观且完整行生成边界不清 | Task 1、3、10、18 |

## 最终验收清单

- [ ] 浏览器只通过 Admin Gateway `/api/admin/storage/{method}` 访问 Storage；Admin Gateway 再通过 Node Service Gateway 原生 tRPC 转发。
- [ ] 服务间生成客户端统一通过 Node Service Gateway 调用 Metadata、PrimaryStore、DataView，不保存其物理地址。
- [ ] PrimaryStoreScan、独立 DataShard 只进入带 Caller ACL 的特权 Service Gateway 路由；ViewIndex 不进入任何 Gateway 路由。
- [ ] 默认只部署 `storage-primary` 和 `storage-view`，每个进程只读取一个 YAML；可选私网 DataShard 也使用独立单 YAML。
- [ ] 每个 DataShard 有固定 Shard ID、独立 Pebble、连续 Sequence 和单 Relay。
- [ ] 不存在全局 `write_version`、Snowflake Version 或跨 Shard 总序假设。
- [ ] TimeSeriesRowsCommitted/RecordRowsCommitted 由 DataShard 原子生成；MooxMessage 只保存一份消息 ID、类型和 Sequence。
- [ ] RowsCommitted 的 UPSERT 携带完整提交后事实行；ViewBuilder 正常增量使用 ViewIndex MERGE，只有缺行、重建和回填才批量读取全部来源并 REPLACE。
- [ ] Outbox 永不跨过失败项删除，重放不会增加 DuckDB/Bleve Entry Count。
- [ ] DuckDB/Bleve 通过 ViewIndex 拥有的 `RowKey`、`RowWrite`、`BatchWrite` 接收明确 MERGE/REPLACE/DELETE，并拥有一致的 Apply/Checkpoint/IndexRangeUpdate 语义；不存在独立 `viewrow` 包。
- [ ] ViewIndex 使用 `ViewVersion + ViewSchemaHash` Fence；Dataset `schema_hash` 与 View `view_schema_hash` 命名和职责明确分离。
- [ ] Archive 忽略 Delete，不写 Tombstone，不删除历史文件。
- [ ] `indexed_from/indexed_to` 使用 UTC RFC3339Nano，分别对应 TimeSeries `data_time` / Record `version`；它们与带 CAS 的 Shard Checkpoint 能解释查询范围和新鲜度。
- [ ] 1001 行、1/25/999 页面、ASC/DESC 全部无遗漏和重复。
- [ ] Dataset 写入后不能改变 Key 放置；Shard 故障不会自动重路由。
- [ ] `MergeTimeSeriesRows` / `MergeRecordRows`、Null、Remove、Required、批次大小和类型错误全部有确定契约。
- [ ] Metadata 使用 SQL 真分页、小型目录缓存和 SQL 时间戳事实源。
- [ ] 配置、构造、Timer、健康和 Context 全部 Fail Closed。
- [ ] 旧 Access、旧物理 PrimaryStore、Mutation、Projection、Coverage、Device 和兼容角色从活动树清零。
- [ ] `factkey`、`factvalue`、Envelope 别名、DataChange 以及 Storage `internal/core` / `internal/infra` 从活动树清零；最终只使用顶层 `rowkey`、`typedvalue`、`retinfo`、MooxMessage 和垂直领域目录。
- [ ] 错误码、类型化错误和 `pb.RetInfo` 转换统一由 `internal/retinfo` 提供；不存在 `response`、`errorcode`、`rpcresult` 平行包，也不存在错误字符串分类。
- [ ] Strategy 构建、发布、默认部署、入口可达性和严格时间参数契约继续通过。
- [ ] Gateway/HostAgent tRPC Timer、Factor EventBatcher、48H Host Metrics Cleanup 和 `packages/security` 无回归。
- [ ] CloudNode、View Browse 已按工作流与展示职责拆分，桌面和移动端核心流程通过。
- [ ] `go.work`、根 README、模块 README 和架构总览保持一致。
- [ ] Storage、Archive、Admin、CLI、Web、Docs、Race、Vet 和 `make verify` 全部通过。
