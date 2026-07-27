# CloudNode 异步 SCF 发布 Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 将 CloudNode 当前同步创建、部署腾讯云 SCF 的接口改为可查询的异步 Job/Item 模型，并让管理前端和 `moox-cli` 统一使用“提交后立即返回 JobID、随后查询真实进度”的工作流。

**Architecture:** 只在 CloudNode 内实现专用的节点批量变更任务，不恢复旧版通用 AsyncTask 平台，也不引入 JetStream。Job 和 Item 原子写入 CloudNode SQLite；一个后台 Runner 每次从 SQLite 原子取得最多 `batch_size` 个 Item，使用 `trpc.GoAndWait` 并发执行整批，等待本批全部结束后再取下一批。前端每 2 秒查询一次任务状态，CLI 提供独立的 `submit` 和 `status` 命令。

**Tech Stack:** Go 1.25、tRPC-Go、Protocol Buffers、SQLite/GORM、Tencent Cloud SCF SDK、Cobra、Vue 3、TypeScript、Vitest。

---

## 1. 实施基线

本计划基于 `feature/mooyang` 当前提交 `68fdc981`。实施前必须重新确认 HEAD 和工作树；若已有相关修改，应吸收而不是覆盖。

当前行为：

- `InitPackageUpload -> COS PUT -> CompletePackageUpload` 已经由前端和 CLI 正确实现。
- `BatchCreateNodes`、`BatchDeployNodes` 在请求协程中逐项调用腾讯云，全部完成后才返回。
- 返回的 `batch_id` 只是即时生成的字符串，没有持久化记录，也没有查询接口。
- 前端收到同步响应后直接伪造 `progress=100`。
- `moox-cli collector function publish` 自己拆批并串行等待所有创建或部署请求完成。

旧版 `moox_old` 提供了正确的交互方向：

```text
CreateAsyncJob -> job_id -> QueryAsyncJob -> progress
```

但旧版的执行队列是 `MemoryTaskQueue`。Job/Task 虽然落库，服务重启后待执行消息不会恢复。本计划保留 Job/Item 状态模型和前端轮询方式，改用 SQLite 本身作为简单任务队列。

## 2. 已确认设计

### 2.1 范围

本次实现：

1. 异步批量创建 SCF 节点。
2. 异步批量部署代码包到已有 SCF 节点。
3. 查询 Job 汇总和所有 Item 结果。
4. CloudNode 重启后重新执行遗留的 `RUNNING` Item。
5. 前端真实进度轮询和刷新恢复。
6. CLI 发布任务提交和结果查询。
7. 真实 50 个 SCF 发布验收。

本次不实现：

- 通用 AsyncTask 平台或 Executor Registry。
- JetStream 发布任务队列。
- 多 CloudNode 实例协调、分布式锁或租约。
- 任务取消、单 Item 在线重试、自动重试失败 Job。
- Job 列表、归档、定期清理和分页。
- WebSocket、SSE 或服务端进度推送。
- exactly-once、Saga、DLQ。

### 2.2 包上传边界

代码包上传继续同步完成：

```text
InitPackageUpload
  -> 客户端使用预签名 URL PUT COS
  -> CompletePackageUpload
  -> package.status=available
  -> SubmitCreateNodes / SubmitDeployNodes
```

上传文件不经过 CloudNode 后台 Runner。提交节点任务前必须确认 Package 属于当前 Space 且状态为 `available`。

### 2.3 状态模型

Job 操作类型：

```text
CREATE_NODES
DEPLOY_NODES
```

Item 状态：

```text
PENDING -> RUNNING -> SUCCESS
                   -> FAILED
```

Job 状态由 Item 聚合：

| Item 聚合 | Job 状态 |
|---|---|
| 尚未开始 | `PENDING` |
| 存在 `PENDING` 或 `RUNNING` | `RUNNING` |
| 全部 `SUCCESS` | `SUCCESS` |
| 全部终态且全为 `FAILED` | `FAILED` |
| 全部终态且成功、失败并存 | `PARTIAL` |

`progress_percent` 使用：

```text
(success_count + failed_count) * 100 / total_count
```

### 2.4 并发和恢复

默认配置：

```yaml
node_batch:
  batch_size: 3
  poll_interval: 500ms
```

约束：

- `batch_size` 同时是一次从 SQLite 取得的最大 Item 数和本批 `trpc.GoAndWait` 的最大并发数，不增加第二个 concurrency 参数。
- Runner 原子取得一批 Item 并标记为 `RUNNING`，然后为本批每个 Item 构造一个 handler。
- Runner 必须等待 `trpc.GoAndWait` 返回后才能从 SQLite 取得下一批；一个慢 Item 会延迟下一批，这是为保持实现简单而接受的批次屏障。
- SQLite 使用条件更新领取整批 Item；不能先查询再无条件更新。
- CloudNode 启动时将所有 `RUNNING` Item 恢复为 `PENDING`。
- 创建和部署继续复用现有腾讯云状态查询及超时后 reconciliation，允许重启后重复执行并收敛。
- 一次最多只向 `trpc.GoAndWait` 提交 `batch_size` 个 handler，禁止一次启动全部 50 个任务，也禁止自行实现 semaphore、worker channel 或第二套 goroutine pool。

### 2.5 敏感参数

创建和部署 Item 的持久化 payload 可能包含 EventBus、Gateway、CLS 等运行时凭据。为支持重启恢复，第一版允许把完整 payload 保存到本机 CloudNode SQLite，但必须满足：

- CloudNode 数据库文件权限收紧为 `0600`。
- RPC 查询结果绝不返回 `request_json`、Environment 或 Config。
- 日志只记录 `space_id/job_id/item_id/operation/node_id`，不得打印 payload。
- Web 和 CLI 状态输出只展示节点身份、状态、结果摘要和错误。

## 3. 最终接口

删除当前没有真实查询语义的 `BatchChangeResult`，使用以下 RPC：

```proto
rpc SubmitCreateNodes(BatchCreateNodesReq) returns (SubmitNodeBatchRsp);
rpc SubmitDeployNodes(BatchDeployNodesReq) returns (SubmitNodeBatchRsp);
rpc GetNodeBatchChange(GetNodeBatchChangeReq) returns (GetNodeBatchChangeRsp);
rpc BatchDeleteNodes(BatchDeleteNodesReq) returns (BatchDeleteNodesRsp);
```

提交响应：

```json
{
  "ret_info": {"code": 0, "msg": "ok"},
  "job_id": "node-batch-...",
  "operation": "NODE_BATCH_OPERATION_CREATE_NODES",
  "total_count": 50
}
```

查询响应：

```json
{
  "ret_info": {"code": 0, "msg": "ok"},
  "job": {
    "job_id": "node-batch-...",
    "operation": "NODE_BATCH_OPERATION_DEPLOY_NODES",
    "status": "NODE_BATCH_STATUS_RUNNING",
    "total_count": 50,
    "pending_count": 30,
    "running_count": 3,
    "success_count": 16,
    "failed_count": 1,
    "progress_percent": 34,
    "created_at": "...",
    "completed_at": ""
  },
  "items": [
    {
      "item_id": "node-batch-...-000",
      "node_id": "moox-collector-ap-guangzhou-000",
      "status": "NODE_BATCH_ITEM_STATUS_SUCCESS",
      "result_summary": "deployed package pkg-...",
      "error_message": "",
      "started_at": "...",
      "completed_at": "..."
    }
  ]
}
```

前端和 CLI 都通过既有 Gateway 路径调用：

```text
/api/admin/cloudnode/SubmitCreateNodes
/api/admin/cloudnode/SubmitDeployNodes
/api/admin/cloudnode/GetNodeBatchChange
```

后台服务签名调用继续由 adminclient 自动改写为 `/api/service/cloudnode/...`。

## 4. 文件结构

新增或调整后的职责边界：

```text
modules/cloudnode/proto/cloudnode.proto
  对外提交、查询协议和状态枚举

modules/cloudnode/schema/cloudnode.sql
  Job/Item 表及索引

modules/cloudnode/internal/store/models.go
  GORM Job/Item 模型

modules/cloudnode/internal/store/node_batch.go
  原子创建、领取、完成、恢复和查询

modules/cloudnode/internal/rpc/node.go
  单节点创建/部署业务函数，保留腾讯云调用逻辑

modules/cloudnode/internal/rpc/node_batch.go
  提交和查询 RPC

modules/cloudnode/internal/rpc/node_batch_runner.go
  SQLite 批量领取、trpc.GoAndWait 并发和 Item 执行分发

modules/cloudnode/internal/config/config.go
  Runner 批大小和轮询配置

modules/cloudnode/internal/bootstrap/bootstrap.go
  Runner 生命周期和启动恢复

modules/cli/internal/adminclient/cloudnode.go
  Submit/Get API 客户端

modules/cli/internal/command/collector.go
  publish submit/status 命令编排

web/src/api/cloud-node.ts
  Submit/Get 前端 API

web/src/views/collector/cloud-node/cloud-node-batch-service.ts
  请求转换和查询结果转换

web/src/views/collector/cloud-node/cloud-node-batch-poller.ts
  2 秒轮询、超时和停止逻辑

web/src/views/collector/cloud-node/cloud-node.vue
  进度 UI 和页面恢复
```

---

## Task 1: 定义异步节点批次协议

**Files:**

- Modify: `modules/cloudnode/proto/cloudnode.proto`
- Regenerate: `modules/cloudnode/proto/cloudnodegen/cloudnode.pb.go`
- Regenerate: `modules/cloudnode/proto/cloudnodegen/cloudnode.trpc.go`

- [x] **Step 1: 写协议契约测试或静态断言**

在现有 CloudNode Proto 使用方测试中先引用下列新符号，确保生成前编译失败：

```go
pb.NodeBatchOperation_NODE_BATCH_OPERATION_CREATE_NODES
pb.NodeBatchStatus_NODE_BATCH_STATUS_RUNNING
pb.NodeBatchItemStatus_NODE_BATCH_ITEM_STATUS_FAILED
&pb.SubmitNodeBatchRsp{}
&pb.GetNodeBatchChangeReq{}
&pb.GetNodeBatchChangeRsp{}
```

运行：

```bash
(cd modules/cloudnode && go test ./internal/rpc -run TestNodeBatchProtoContract -count=1)
```

Expected: FAIL，提示新类型尚不存在。

- [x] **Step 2: 增加状态枚举和消息**

在 `cloudnode.proto` 中增加：

```proto
enum NodeBatchOperation {
  NODE_BATCH_OPERATION_UNSPECIFIED = 0;
  NODE_BATCH_OPERATION_CREATE_NODES = 1;
  NODE_BATCH_OPERATION_DEPLOY_NODES = 2;
}

enum NodeBatchStatus {
  NODE_BATCH_STATUS_UNSPECIFIED = 0;
  NODE_BATCH_STATUS_PENDING = 1;
  NODE_BATCH_STATUS_RUNNING = 2;
  NODE_BATCH_STATUS_SUCCESS = 3;
  NODE_BATCH_STATUS_FAILED = 4;
  NODE_BATCH_STATUS_PARTIAL = 5;
}

enum NodeBatchItemStatus {
  NODE_BATCH_ITEM_STATUS_UNSPECIFIED = 0;
  NODE_BATCH_ITEM_STATUS_PENDING = 1;
  NODE_BATCH_ITEM_STATUS_RUNNING = 2;
  NODE_BATCH_ITEM_STATUS_SUCCESS = 3;
  NODE_BATCH_ITEM_STATUS_FAILED = 4;
}

message SubmitNodeBatchRsp {
  common.RetInfo ret_info = 1;
  string job_id = 2;
  NodeBatchOperation operation = 3;
  int32 total_count = 4;
}

message GetNodeBatchChangeReq {
  string job_id = 1;
}

message NodeBatchSummary {
  string job_id = 1;
  NodeBatchOperation operation = 2;
  NodeBatchStatus status = 3;
  int32 total_count = 4;
  int32 pending_count = 5;
  int32 running_count = 6;
  int32 success_count = 7;
  int32 failed_count = 8;
  int32 progress_percent = 9;
  string created_at = 10;
  string completed_at = 11;
}

message NodeBatchItemResult {
  string item_id = 1;
  string node_id = 2;
  NodeBatchItemStatus status = 3;
  string result_summary = 4;
  string error_message = 5;
  string started_at = 6;
  string completed_at = 7;
}

message GetNodeBatchChangeRsp {
  common.RetInfo ret_info = 1;
  NodeBatchSummary job = 2;
  repeated NodeBatchItemResult items = 3;
}

message BatchDeleteNodesRsp {
  common.RetInfo ret_info = 1;
  int32 processed_count = 2;
}
```

服务方法改为：

```proto
rpc SubmitCreateNodes(BatchCreateNodesReq) returns (SubmitNodeBatchRsp);
rpc SubmitDeployNodes(BatchDeployNodesReq) returns (SubmitNodeBatchRsp);
rpc GetNodeBatchChange(GetNodeBatchChangeReq) returns (GetNodeBatchChangeRsp);
rpc BatchDeleteNodes(BatchDeleteNodesReq) returns (BatchDeleteNodesRsp);
```

直接删除 `BatchChangeResult` 以及旧的 `BatchCreateNodes`、`BatchDeployNodes` RPC，不保留兼容入口。

- [x] **Step 3: 生成并验证 Proto**

运行：

```bash
make -C modules/cloudnode/proto all
make proto
git diff --check
```

Expected:

- 生成代码包含三个新 RPC。
- 全仓没有二次生成差异。
- 当前 Go 使用方因尚未迁移旧方法而编译失败，这是本 Task 的预期中间状态。

- [x] **Step 4: 提交协议**

```bash
git add modules/cloudnode/proto
git commit -m "feat(cloudnode): define asynchronous node batch API"
```

## Task 2: 增加 Job/Item SQLite Schema 和原子 Store

**Files:**

- Modify: `modules/cloudnode/schema/cloudnode.sql`
- Modify: `modules/cloudnode/schema/schema_test.go`
- Modify: `modules/cloudnode/internal/store/models.go`
- Create: `modules/cloudnode/internal/store/node_batch.go`
- Create: `modules/cloudnode/internal/store/node_batch_test.go`
- Modify: `modules/cloudnode/internal/store/database.go`
- Modify: `modules/cloudnode/internal/store/database_test.go`

- [x] **Step 1: 写 Schema 和权限失败测试**

新增断言：

```go
func TestSchemaContainsNodeBatchTablesAndIndexes(t *testing.T)
func TestOpenRestrictsDatabaseFileToOwner(t *testing.T)
```

Schema 测试必须检查：

```text
t_cloud_node_batches
t_cloud_node_batch_items
idx_cloud_node_batches_space_job
idx_cloud_node_batch_items_job_index
idx_cloud_node_batch_items_status_id
```

数据库权限测试创建临时 DB，断言 `os.Stat(path).Mode().Perm() == 0600`。

- [x] **Step 2: 增加两张表**

使用以下最小字段：

```sql
CREATE TABLE IF NOT EXISTS t_cloud_node_batches (
    c_id INTEGER NOT NULL PRIMARY KEY AUTOINCREMENT,
    c_space_id TEXT NOT NULL,
    c_job_id TEXT NOT NULL,
    c_operation TEXT NOT NULL,
    c_status TEXT NOT NULL DEFAULT 'pending',
    c_total_count INTEGER NOT NULL,
    c_completed_at DATETIME,
    c_ctime DATETIME DEFAULT CURRENT_TIMESTAMP,
    c_mtime DATETIME DEFAULT CURRENT_TIMESTAMP
);

CREATE UNIQUE INDEX IF NOT EXISTS idx_cloud_node_batches_space_job
ON t_cloud_node_batches (c_space_id, c_job_id);

CREATE TABLE IF NOT EXISTS t_cloud_node_batch_items (
    c_id INTEGER NOT NULL PRIMARY KEY AUTOINCREMENT,
    c_space_id TEXT NOT NULL,
    c_job_id TEXT NOT NULL,
    c_item_id TEXT NOT NULL,
    c_item_index INTEGER NOT NULL,
    c_node_id TEXT NOT NULL DEFAULT '',
    c_status TEXT NOT NULL DEFAULT 'pending',
    c_request_json TEXT NOT NULL,
    c_result_summary TEXT NOT NULL DEFAULT '',
    c_error_message TEXT NOT NULL DEFAULT '',
    c_started_at DATETIME,
    c_completed_at DATETIME,
    c_ctime DATETIME DEFAULT CURRENT_TIMESTAMP,
    c_mtime DATETIME DEFAULT CURRENT_TIMESTAMP,
    FOREIGN KEY(c_space_id, c_job_id)
      REFERENCES t_cloud_node_batches(c_space_id, c_job_id)
      ON DELETE CASCADE
);

CREATE UNIQUE INDEX IF NOT EXISTS idx_cloud_node_batch_items_job_index
ON t_cloud_node_batch_items (c_space_id, c_job_id, c_item_index);

CREATE INDEX IF NOT EXISTS idx_cloud_node_batch_items_status_id
ON t_cloud_node_batch_items (c_status, c_id);
```

为复合外键补充父表唯一索引即可，不增加独立外键 ID。

- [x] **Step 3: 增加 Store 类型和 API**

定义：

```go
const (
    NodeBatchPending = "pending"
    NodeBatchRunning = "running"
    NodeBatchSuccess = "success"
    NodeBatchFailed  = "failed"
    NodeBatchPartial = "partial"
)

type NodeBatchCreate struct {
    SpaceID   string
    JobID     string
    Operation string
    Items     []NodeBatchItemCreate
}

type NodeBatchAggregate struct {
    Job          NodeBatch
    Items        []NodeBatchItem
    PendingCount int
    RunningCount int
    SuccessCount int
    FailedCount  int
}
```

Repository 方法：

```go
func (r *CatalogRepository) CreateNodeBatch(
    ctx context.Context,
    input NodeBatchCreate,
) error

func (r *CatalogRepository) TakePendingNodeBatchItems(
    ctx context.Context,
    limit int,
) ([]NodeBatchItem, error)

func (r *CatalogRepository) CompleteNodeBatchItem(
    ctx context.Context,
    spaceID, jobID, itemID, resultSummary string,
    executeErr error,
) error

func (r *CatalogRepository) RequeueInterruptedNodeBatchItems(
    ctx context.Context,
) (int64, error)

func (r *CatalogRepository) GetNodeBatch(
    ctx context.Context,
    spaceID, jobID string,
) (*NodeBatchAggregate, error)
```

`CreateNodeBatch` 必须在一个 GORM transaction 内写 Job 和全部 Item。

`TakePendingNodeBatchItems` 必须：

1. 校验 `limit > 0`。
2. 按 `c_id ASC` 查最多 `limit` 个最早的 `pending` Item。
3. 在同一事务中使用 `WHERE c_id IN ? AND c_status='pending'` 条件更新整批。
4. 只有 `RowsAffected == len(selectedItems)` 才返回该批 Item。
5. 同一事务内把本批涉及的所有 Job 状态更新为 `running`。
6. 返回结果保持 `c_id ASC`，让测试和日志具有稳定顺序。

若条件更新数量不一致，必须回滚本次事务并重新领取，不能返回部分已领取批次。

当前 SQLite 强制单连接，因此事务中不得嵌套调用另一个需要新连接的 Store 方法。

`CompleteNodeBatchItem` 必须在同一事务中：

1. 把 Item 更新为 `success` 或 `failed`。
2. 使用 SQL 重新统计该 Job 的四类 Item 数量，不使用进程内计数器。
3. 根据统计结果更新 Job 状态。
4. 全部 Item 到达终态时写入 `c_completed_at`。

`RequeueInterruptedNodeBatchItems` 把非正常中断遗留的 `running` Item 恢复为 `pending`，清空其开始时间，并将受影响且未终态的 Job 恢复为 `pending`。正常启动时调用一次；本批 `trpc.GoAndWait` 出现 panic recovery 或状态落库错误时，也只能在整批 handler 全部退出后调用。它不改变已终态 Item。名称保留 `Interrupted`，明确这些 Item 并非正常运行中，而是因进程退出或批次基础设施错误而中断。

- [x] **Step 4: 覆盖 Store 正确性**

新增：

```go
func TestCreateNodeBatchIsAtomic(t *testing.T)
func TestTakePendingNodeBatchItemsMarksWholeBatchRunning(t *testing.T)
func TestTakePendingNodeBatchItemsReturnsStableNonOverlappingBatches(t *testing.T)
func TestCompleteNodeBatchItemBuildsSuccessStatus(t *testing.T)
func TestCompleteNodeBatchItemBuildsPartialStatus(t *testing.T)
func TestRequeueInterruptedNodeBatchItemsReturnsRunningItemsToPending(t *testing.T)
func TestGetNodeBatchIsSpaceScoped(t *testing.T)
```

`TestCreateNodeBatchIsAtomic` 使用重复 `item_index` 触发约束失败，断言 Job 和 Item 都没有留下。

分批领取测试创建 10 个 Item，以 `limit=3` 连续领取，断言批大小为 `3/3/3/1`、每批内部顺序稳定，并且 10 个 ItemID 各出现一次。

- [x] **Step 5: 收紧 DB 权限**

`store.Open` 在成功打开数据库后执行：

```go
if err := os.Chmod(dbPath, 0o600); err != nil {
    return nil, fmt.Errorf("restrict cloudnode database permissions: %w", err)
}
```

不改变现有 SQLite `journal_mode` 和 `synchronous` 参数。

- [x] **Step 6: 运行测试并提交**

```bash
(cd modules/cloudnode && go test ./schema ./internal/store -count=1)
git diff --check
git add modules/cloudnode/schema modules/cloudnode/internal/store
git commit -m "feat(cloudnode): persist asynchronous node batches"
```

## Task 3: 提取可复用的单节点创建和部署执行函数

**Files:**

- Modify: `modules/cloudnode/internal/rpc/node.go`
- Modify: `modules/cloudnode/internal/rpc/node_test.go`
- Create: `modules/cloudnode/internal/rpc/node_item_test.go`

- [x] **Step 1: 为单 Item 执行写测试**

新增：

```go
func TestExecuteCreateNodeItemCreatesSCFAndCatalogNode(t *testing.T)
func TestExecuteDeployNodeItemUpdatesCodeAndCatalogPackage(t *testing.T)
func TestExecuteDeployNodeItemRejectsMissingNode(t *testing.T)
func TestExecuteDeployNodeItemRejectsUnavailablePackage(t *testing.T)
func TestExecuteDeployNodeItemReconcilesAcceptedTencentTimeout(t *testing.T)
```

复用现有 fake `scfProvisioner`，不要重新实现第二套腾讯云 mock。

- [x] **Step 2: 提取内部函数**

从当前同步循环中提取：

```go
func (s *Service) executeCreateNodeItem(
    ctx context.Context,
    spaceID string,
    item *pb.NodeCreateItem,
    index int,
) (string, error)

func (s *Service) executeDeployNodeItem(
    ctx context.Context,
    spaceID string,
    item *pb.NodeDeployItem,
) (string, error)
```

返回值是可公开展示的短摘要，例如：

```text
created function moox-collector-ap-guangzhou-000
deployed package pkg-123 to moox-collector-ap-guangzhou-000
```

函数继续负责：

- 参数校验。
- 确保 JobType 对应的执行队列存在。
- 调用腾讯云 SCF。
- 等待或 reconciliation。
- 更新 CloudNode catalog。

函数不得写 Job/Item 状态；状态统一由 Runner 的批内 handler 处理。

- [x] **Step 3: 删除旧同步批次实现**

删除：

```go
func (s *Service) BatchCreateNodes(...)
func (s *Service) BatchDeployNodes(...)
func directBatchID(...)
```

`BatchDeleteNodes` 保持同步，但改为返回 `BatchDeleteNodesRsp`，不再生成虚假 BatchID。

- [x] **Step 4: 运行测试并提交**

```bash
(cd modules/cloudnode && go test ./internal/rpc -run 'TestExecute(Create|Deploy)NodeItem|TestBatchDeleteNodes' -count=1)
git add modules/cloudnode/internal/rpc
git commit -m "refactor(cloudnode): isolate single node SCF operations"
```

## Task 4: 实现提交、查询 RPC

**Files:**

- Create: `modules/cloudnode/internal/rpc/node_batch.go`
- Create: `modules/cloudnode/internal/rpc/node_batch_test.go`
- Modify: `modules/cloudnode/internal/rpc/server.go`
- Modify: `modules/cloudnode/internal/rpc/common.go`

- [x] **Step 1: 写 RPC 失败测试**

覆盖：

```go
func TestSubmitCreateNodesReturnsBeforeTencentCall(t *testing.T)
func TestSubmitDeployNodesReturnsBeforeTencentCall(t *testing.T)
func TestSubmitCreateNodesRejectsEmptyItems(t *testing.T)
func TestSubmitCreateNodesRejectsMoreThanOneHundredItems(t *testing.T)
func TestSubmitDeployNodesPreflightsEveryNodeAndPackage(t *testing.T)
func TestGetNodeBatchChangeReturnsRealAggregate(t *testing.T)
func TestGetNodeBatchChangeDoesNotExposeRequestPayload(t *testing.T)
func TestGetNodeBatchChangeRejectsOtherSpace(t *testing.T)
```

“立即返回”测试给 fake Tencent client 设置阻塞 barrier，提交 RPC 必须在 barrier 未释放时返回 JobID，且 fake client 调用次数仍为 0。

- [x] **Step 2: 创建稳定 JobID 和 ItemID**

使用：

```go
jobID := "node-batch-" + uuid.NewString()
itemID := fmt.Sprintf("%s-%03d", jobID, index)
```

不允许继续使用时间戳形式的 `directBatchID`。

- [x] **Step 3: 结构化序列化请求**

使用 `protojson.Marshal` 保存 `NodeCreateItem` 或 `NodeDeployItem`，Runner 使用 `protojson.Unmarshal`。禁止手写 JSON 字符串拼接。

提交前完成所有确定性预检：

- Item 数量为 `1..100`。
- CREATE：CloudAccount、Region、Package 必填。CloudAccount 是个人量化平台级配置，可被各 Space 复用；Package 必须属于当前 Space。
- DEPLOY：Node、Package 必填且属于当前 Space，Package 状态为 `available`。
- 同一 Job 中 NodeID 不得重复。

预检成功后调用一次 `CreateNodeBatch`，返回 `job_id`，不调用腾讯云。

- [x] **Step 4: 查询时只返回脱敏结果**

`GetNodeBatchChange`：

- 必须使用 context 中的 SpaceID。
- 找不到当前 Space Job 返回 `NOT_FOUND`。
- Item 按 `item_index ASC` 返回。
- `node_id` 对 CREATE 可从预检后生成的稳定函数名获得；不要把整个 request 解码后回传。
- 不返回 Environment、Config、Metadata 或 request JSON。

- [x] **Step 5: 运行测试并提交**

```bash
(cd modules/cloudnode && go test ./internal/rpc -run 'TestSubmit|TestGetNodeBatch' -count=1)
git add modules/cloudnode/internal/rpc
git commit -m "feat(cloudnode): submit and query node batches"
```

## Task 5: 实现 GoAndWait 批内并发 Runner 和启动恢复

**Files:**

- Create: `modules/cloudnode/internal/rpc/node_batch_runner.go`
- Create: `modules/cloudnode/internal/rpc/node_batch_runner_test.go`
- Modify: `modules/cloudnode/internal/rpc/server.go`
- Modify: `modules/cloudnode/internal/config/config.go`
- Modify: `modules/cloudnode/internal/config/config_test.go`
- Modify: `modules/cloudnode/config/app.yaml`
- Modify: `modules/cloudnode/internal/bootstrap/bootstrap.go`
- Modify: `modules/cloudnode/internal/bootstrap/bootstrap_test.go`

- [x] **Step 1: 写 Runner 行为测试**

新增：

```go
func TestNodeBatchRunnerRunsTakenBatchWithTRPCGoAndWait(t *testing.T)
func TestNodeBatchRunnerDoesNotTakeNextBatchUntilCurrentBatchFinishes(t *testing.T)
func TestNodeBatchRunnerCompletesOtherItemsAfterOneFailure(t *testing.T)
func TestNodeBatchRunnerStopsWithRuntimeContext(t *testing.T)
func TestNodeBatchRunnerRequeuesInterruptedItemsAtStartup(t *testing.T)
func TestNodeBatchRunnerNeverLogsRequestPayload(t *testing.T)
```

批内并发测试创建 7 个 Item，配置 `batch_size=3`。fake executor 使用 atomic 和 barrier 记录 `active`、`maxActive` 以及 Store 的领取次数，断言：

```go
maxActive == 3
successCount == 7
takeBatchSizes == []int{3, 3, 1}
```

`TestNodeBatchRunnerDoesNotTakeNextBatchUntilCurrentBatchFinishes` 阻塞第一批中的一个 handler，在释放 barrier 前断言 Store 只发生一次 `TakePendingNodeBatchItems`；释放并等待第一批全部结束后，第二次领取才允许发生。

- [x] **Step 2: 增加配置**

```go
type NodeBatchConfig struct {
    BatchSize    int           `yaml:"batch_size"`
    PollInterval time.Duration `yaml:"poll_interval"`
}
```

默认：

```go
NodeBatch: NodeBatchConfig{
    BatchSize:    3,
    PollInterval: 500 * time.Millisecond,
}
```

校验：

```text
batch_size: 1..10
poll_interval: 100ms..10s
```

不增加 `worker_count`、`concurrency` 或按 Job、Region、Provider 的第二层并发配置。

- [x] **Step 3: 启动 Runner**

增加：

```go
func (s *Service) StartNodeBatchRunner(
    ctx context.Context,
    batchSize int,
    pollInterval time.Duration,
) error
```

启动顺序：

1. 调用一次 `RequeueInterruptedNodeBatchItems`。
2. 启动一个后台 Runner 循环。
3. 每轮调用 `TakePendingNodeBatchItems(ctx, batchSize)`。
4. 无任务时等待 `pollInterval`，必须支持 `ctx.Done()`。
5. 为本批每个 Item 构造一个 `func() error` handler。
6. 每个 handler 使用独立的 `scfOperationTimeout`，根据 Job operation 解码并调用 `executeCreateNodeItem` 或 `executeDeployNodeItem`。
7. 每个 handler 无论业务执行成功失败都调用 `CompleteNodeBatchItem`；业务失败落为 `FAILED` 后 handler 返回 `nil`，不能影响同批其他 Item。
8. 使用 `trpc.GoAndWait(handlers...)` 并发执行并等待整批结束。
9. 只有 `trpc.GoAndWait` 返回后才能开始下一次 `TakePendingNodeBatchItems`。

`trpc.GoAndWait` 返回的错误只表示 panic recovery 或状态落库等基础设施错误。出现此类错误时，在本批全部 handler 退出后调用 `RequeueInterruptedNodeBatchItems`，让没有成功落终态的 `RUNNING` Item 回到 `PENDING`。Runner 不自动重新执行已经明确标记为 `FAILED` 的业务 Item。

- [x] **Step 4: 接入 Bootstrap 生命周期**

在 `cloudnoderpc.New` 完成、注册服务前后均可启动，但必须满足：

- Schema 已应用。
- credential resolver 和 SCF client factory 已注入。
- 使用服务进程 runtime context，而不是某个 RPC request context。
- 初始化失败直接阻止 CloudNode 启动。

- [x] **Step 5: 运行 Race 测试并提交**

```bash
(cd modules/cloudnode && go test -race ./internal/store ./internal/rpc ./internal/bootstrap -count=1)
git add modules/cloudnode/internal/config modules/cloudnode/config modules/cloudnode/internal/bootstrap modules/cloudnode/internal/rpc
git commit -m "feat(cloudnode): execute node batches with trpc concurrency"
```

## Task 6: 将前端改为真实提交和轮询

**Files:**

- Modify: `web/src/api/cloud-node.ts`
- Modify: `web/src/utils/cloud-node-batch-change.ts`
- Modify: `web/src/views/collector/cloud-node/cloud-node-batch-service.ts`
- Create: `web/src/views/collector/cloud-node/cloud-node-batch-poller.ts`
- Create: `web/src/views/collector/cloud-node/cloud-node-batch-poller.test.ts`
- Modify: `web/src/views/collector/cloud-node/cloud-node-model.ts`
- Modify: `web/src/views/collector/cloud-node/cloud-node-model.test.ts`
- Modify: `web/src/views/collector/cloud-node/cloud-node.vue`
- Modify: `web/tests/cloud-node-workflows.spec.ts`

- [x] **Step 1: 写 API 和轮询失败测试**

覆盖：

```ts
it("submits create nodes and returns the backend job id")
it("submits deployments and returns the backend job id")
it("polls immediately and then every two seconds")
it("stops on success, failure, or partial status")
it("keeps polling after a transient query error")
it("stops locally after thirty minutes without failing the backend job")
it("restores polling from job_id in the route")
it("stops timers when the component is disposed")
```

使用 Vitest fake timers：

```ts
vi.useFakeTimers();
await poller.start("node-batch-1");
await vi.advanceTimersByTimeAsync(2000);
expect(query).toHaveBeenCalledTimes(2);
```

- [x] **Step 2: 更新 API 类型**

增加：

```ts
export type NodeBatchStatus =
  | "NODE_BATCH_STATUS_PENDING"
  | "NODE_BATCH_STATUS_RUNNING"
  | "NODE_BATCH_STATUS_SUCCESS"
  | "NODE_BATCH_STATUS_FAILED"
  | "NODE_BATCH_STATUS_PARTIAL";

export interface SubmitNodeBatchResponse {
  job_id: string;
  operation: string;
  total_count: number;
}

export interface GetNodeBatchChangeResponse {
  job: NodeBatchSummary;
  items: NodeBatchItemResult[];
}
```

API：

```ts
submitCreateNodes(data)
submitDeployNodes(data)
getNodeBatchChange(jobId)
```

删除 `batchCreateNodes`、`batchDeployNodes` 和依赖虚假 `batch_id` 的返回类型。

批量删除改为读取 `processed_count`，直接刷新列表，不展示异步 Job 进度。

- [x] **Step 3: 实现独立 Poller**

`cloud-node-batch-poller.ts` 只负责：

```ts
start(jobId, callbacks)
stop()
dispose()
```

规则：

- `start` 立即查询一次。
- 默认每 2000ms 查询。
- `SUCCESS/FAILED/PARTIAL` 停止。
- 查询网络失败保留当前状态并继续。
- 30 分钟后停止自动查询，回调 `onPollingTimeout(jobId)`。
- timeout 不把 Job 映射为失败。
- 同一 Poller 同时只允许一个 Job；新 `start` 先停止旧 timer。

- [x] **Step 4: 删除前端伪完成逻辑**

删除：

```ts
makeCompletedBatchChangeStatus(...)
completeCloudNodeBatchChange(...)
```

提交后：

1. 保存 `job_id` 到路由 query。
2. `batchChangeProcessing=true`。
3. 启动 Poller。
4. 每次查询使用后端计数更新进度条。
5. 终态时刷新节点列表。

用户关闭进度展示时：

- 调用 Poller `stop()`。
- 清理本地显示状态和 URL。
- 解除页面操作禁用。
- 不调用后端取消。

页面卸载调用 `dispose()`。

- [x] **Step 5: 去掉客户端 100 条拆批**

后端单 Job 限制为 100 个 Item，前端一次创建最多也限制 100。删除 `chunkTasks` 和多 Job 聚合状态，确保一次用户操作只产生一个 `job_id`，刷新后可以可靠恢复。

- [x] **Step 6: 运行前端检查并提交**

```bash
(cd web && npm test -- --run cloud-node)
(cd web && npm run lint:eslint:check)
(cd web && npm run build:prod)
git diff --check
git add web/src/api/cloud-node.ts web/src/utils/cloud-node-batch-change.ts \
  web/src/views/collector/cloud-node web/tests/cloud-node-workflows.spec.ts
git commit -m "feat(web): poll real cloud node batch progress"
```

## Task 7: 为 moox-cli 增加 publish submit/status

**Files:**

- Modify: `modules/cli/internal/adminclient/cloudnode.go`
- Modify: `modules/cli/internal/adminclient/client_test.go`
- Modify: `modules/cli/internal/command/collector.go`
- Modify: `modules/cli/internal/command/collector_test.go`

- [x] **Step 1: 写命令结构失败测试**

覆盖：

```go
func TestCollectorPublishSubmitCommandExists(t *testing.T)
func TestCollectorPublishStatusCommandExists(t *testing.T)
func TestPublishSubmitReturnsAfterJobSubmission(t *testing.T)
func TestPublishStatusPrintsJobAndItems(t *testing.T)
func TestPublishSubmitCreateFleetUsesOneJob(t *testing.T)
func TestPublishSubmitDeployFleetUsesOneJob(t *testing.T)
func TestPublishSubmitRejectsPartialFleetBeforeUpload(t *testing.T)
```

命令帮助必须形成：

```text
moox-cli collector function publish submit
moox-cli collector function publish status --job-id <id>
```

- [x] **Step 2: 替换 adminclient 方法**

删除：

```go
BatchCreateNodes(...)
BatchDeployNodes(...)
parseBatchChangeResponse(...)
```

增加：

```go
func (c *Client) SubmitCreateNodes(
    ctx context.Context,
    nodes []NodeCreateItem,
) (*SubmitNodeBatchResponse, error)

func (c *Client) SubmitDeployNodes(
    ctx context.Context,
    deployments []NodeDeployItem,
) (*SubmitNodeBatchResponse, error)

func (c *Client) GetNodeBatchChange(
    ctx context.Context,
    jobID string,
) (*NodeBatchChangeResponse, error)
```

状态查询请求：

```go
map[string]any{"job_id": jobID}
```

必须验证 `ret_info`、非空 JobID 和非空 Job。

- [x] **Step 3: 重构 Cobra 命令**

将当前叶子命令 `publish` 改为父命令：

```text
collectorFunctionPublishCmd
  collectorFunctionPublishSubmitCmd
  collectorFunctionPublishStatusCmd
```

`submit` 保留当前打包、CLS 解析、COS 上传、fleet 识别和环境构建逻辑，但将最后一步改为：

```go
if len(fleetNodes) == 0 {
    return client.SubmitCreateNodes(ctx, createItems)
}
return client.SubmitDeployNodes(ctx, deployments)
```

一次提交全部 50 个 Item，不再由 CLI 拆批。

删除参数：

```text
--create-batch-size
--deploy-batch-size
```

保留：

```text
--node-count
--function-name-prefix
--cloud-account-id
--region
--zip
```

- [x] **Step 4: 定义 JSON 输出**

`publish submit`：

```json
{
  "zip_path": "...",
  "package_id": "...",
  "fleet_mode": "created",
  "job_id": "node-batch-...",
  "operation": "create_nodes",
  "total_count": 50
}
```

`publish status` 原样输出脱敏后的 Job 和 Item：

```json
{
  "job": {...},
  "items": [...]
}
```

查询成功即退出 0，包括 Job 自身为 `FAILED/PARTIAL`；脚本按 JSON 状态判断业务结果。第一版不增加 `--watch`、`--retry-failed` 或 `--cancel`。

- [x] **Step 5: 处理现有单节点 deploy 命令**

保留 `collector function deploy` 的入口，但把它改为提交一个 `DEPLOY_NODES` Job 并立即输出 `job_id`。帮助信息明确使用：

```text
moox-cli collector function publish status --job-id <id>
```

不得保留任何同步调用 `SubmitDeployNodes` 后等待腾讯云完成的旁路。

- [x] **Step 6: 运行 CLI 测试并提交**

```bash
(cd modules/cli && go test ./internal/adminclient ./internal/command -count=1)
(cd modules/cli && go run ./cmd/moox-cli collector function publish --help)
(cd modules/cli && go run ./cmd/moox-cli collector function publish submit --help)
(cd modules/cli && go run ./cmd/moox-cli collector function publish status --help)
git add modules/cli/internal/adminclient modules/cli/internal/command
git commit -m "feat(cli): submit and query SCF publish jobs"
```

## Task 8: 更新真实 SCF E2E 和文档

**Files:**

- Modify: `examples/e2e/run-real-symbol-kline-scf.sh`
- Modify: `examples/e2e/test-run-real-symbol-kline-scf.sh`
- Modify: `examples/e2e/collector-symbol-kline.mjs`
- Modify: `examples/e2e/collector-symbol-kline.test.mjs`
- Modify: `docs/云节点管理.md`
- Modify: `docs/superpowers/plans/2026-07-27-collector-symbol-kline-real-scf-e2e.md` only to mark the superseded synchronous invocation command
- Modify: `scripts/test-deploy-moox-control-profile.sh`

- [x] **Step 1: 更新 shell contract 测试**

新的真实发布脚本必须：

```text
1. package/upload
2. publish submit
3. 解析 job_id
4. 每 2 秒调用 publish status
5. SUCCESS 后继续 Symbol/Kline E2E
6. FAILED/PARTIAL 时输出失败 Item 并退出非 0
7. 30 分钟仍未终态时退出超时，但保留 job_id
```

测试断言脚本不再出现：

```text
--deploy-batch-size
--create-batch-size
```

并必须出现：

```text
collector function publish submit
collector function publish status
```

- [x] **Step 2: 更新 E2E 输入**

`collector-symbol-kline.mjs` 不再读取旧同步 `publish_summary.deploy_processed_count`，改为读取终态状态文件：

```json
{
  "job": {
    "status": "NODE_BATCH_STATUS_SUCCESS",
    "total_count": 50,
    "success_count": 50,
    "failed_count": 0
  }
}
```

E2E 必须断言：

- `total_count == 50`
- `success_count == 50`
- `failed_count == 0`
- CloudNode catalog 中 50 个节点均绑定目标 PackageID。
- 腾讯云侧查询到 50 个目标函数。
- 后续 Keepalive、JetStream、Binance TLS、Storage 写入验证继续执行。

- [x] **Step 3: 更新运维文档**

`docs/云节点管理.md` 写清：

```bash
moox-cli collector function publish submit ...
moox-cli collector function publish status --job-id ...
```

并说明：

- 上传 COS 完成后才创建异步 Job。
- CLI 退出不影响后台发布。
- CloudNode 重启会重新执行运行中的 Item。
- 失败 Item 不自动重试；重新执行 `publish submit` 时已有目标版本节点会被跳过。

- [x] **Step 4: 锁定部署包配置**

`scripts/deploy-moox.sh` 已复制整个 `modules/cloudnode/config`，不需要修改部署脚本。在 `scripts/test-deploy-moox-control-profile.sh` 中增加断言，确认打包后的：

```text
cloudnode/config/app.yaml
```

包含：

```yaml
node_batch:
  batch_size: 3
  poll_interval: 500ms
```

- [x] **Step 5: 运行本地 E2E contract**

```bash
node --test examples/e2e/collector-symbol-kline.test.mjs
bash examples/e2e/test-run-real-symbol-kline-scf.sh
bash scripts/test-deploy-moox-control-profile.sh
git diff --check
```

- [x] **Step 6: 提交 E2E 和文档**

```bash
git add examples/e2e docs/云节点管理.md scripts
git commit -m "test(cloudnode): cover asynchronous SCF publishing"
```

## Task 9: 全量验证、真实部署和独立审查

**Files:**

- No planned source changes; only fix defects found by verification or review.

- [x] **Step 1: 确认生成代码干净**

```bash
make proto
git diff --exit-code -- modules/cloudnode/proto
```

Expected: PASS，没有未提交生成差异。

- [x] **Step 2: 运行模块测试和 Race**

```bash
(cd modules/cloudnode && go test ./... -count=1)
(cd modules/cloudnode && go test -race ./internal/store ./internal/rpc ./internal/bootstrap -count=1)
(cd modules/cli && go test ./... -count=1)
(cd web && npm test)
(cd web && npm run lint:eslint:check)
(cd web && npm run build:prod)
```

- [x] **Step 3: 运行仓库门禁**

```bash
./scripts/test-go-workspace.sh
make verify-pr
git diff --check
```

- [x] **Step 4: 部署到真实控制机**

使用现有 `custom.toml`/部署配置定位目标机，按当前发布脚本部署至少：

```text
moox-cloudnode
moox-admin / gateway（仅路由或服务清单发生变化时）
moox-cli
web-host
```

部署后验证：

```text
/readyz
CloudNode SQLite 新表存在
node_batch.batch_size=3 生效
Gateway 可调用 SubmitCreateNodes/SubmitDeployNodes/GetNodeBatchChange
```

- [x] **Step 5: 执行真实 50 SCF 发布**

记录：

```text
package_id
job_id
提交时间
第一次 RUNNING 时间
终态时间
50 个 Item 结果
腾讯云函数数量
CloudNode catalog PackageID
```

验收标准：

- submit HTTP 在 5 秒内返回；COS 上传时间不计入该指标。
- 任意时刻腾讯云控制面调用并发不超过 3。
- CLI 进程退出后 Job 继续推进。
- 测试中途重启一次 CloudNode，遗留 `RUNNING` Item 恢复并最终收敛。
- 最终 50/50 成功；若腾讯云真实错误导致失败，必须保留错误 Item 证据，修复后重新提交并验证。

- [x] **Step 6: 执行完整 Symbol/Kline E2E**

运行更新后的真实脚本，验证：

```text
50 SCF 在线
Symbol 全市场数据写入
Kline JobItem 投递 JetStream
SCF Fetch 批内并发
Binance TLS 成功
Storage 写入成功
CLS 中可查询生命周期日志
```

- [x] **Step 7: 使用 codeCR 独立审查**

审查必须覆盖：

- Job/Item 创建是否原子。
- `TakePendingNodeBatchItems` 是否可能跨批次重复领取 Item。
- 第一批未完成时是否错误领取了下一批。
- 重启恢复是否泄露 request payload。
- Space 隔离。
- 腾讯云超时后的 reconciliation。
- 前端 timer 泄漏、无限轮询和关闭后禁用状态。
- CLI 是否仍存在同步发布旁路。
- 真实 E2E 是否验证 50 个函数而不是只验证 catalog。

所有结论必须附文件、符号或行号。修复发现后重新运行受影响测试。

- [x] **Step 8: 最终提交、推送和远端确认**

```bash
git status --short
git log --oneline --decorate -12
git push origin feature/mooyang
git rev-parse HEAD
git ls-remote origin refs/heads/feature/mooyang
```

最终要求：

- 工作树干净。
- 本地 HEAD 与远端 `feature/mooyang` SHA 一致。
- 最终说明中区分本地测试、真实腾讯云验证和远端分支状态。

## 5. 验收矩阵

| 场景 | 期望结果 | 证明方式 |
|---|---|---|
| 提交 50 个 CREATE | 立即返回一个 JobID | RPC/CLI 测试 |
| 提交 50 个 DEPLOY | 立即返回一个 JobID | RPC/CLI 测试 |
| Runner 批内并发 | 每批最大 3，批大小为 `3/3/...` | atomic barrier 测试、真实日志 |
| 批次屏障 | 当前批全部完成后才取下一批 | barrier + Store 调用次数测试 |
| 单 Item 失败 | 同批其他 Item 继续 | Runner 测试 |
| 部分成功 | Job=`PARTIAL` | Store/RPC 测试 |
| 查询其他 Space Job | `NOT_FOUND` | RPC 测试 |
| 查询结果脱敏 | 无 Environment/Config | RPC/前端测试 |
| CloudNode 重启 | RUNNING 恢复为 PENDING | 重启 E2E |
| 前端刷新 | 通过 URL JobID 恢复 | Vitest |
| 前端关闭进度 | 停止本地轮询并解除禁用 | Vitest |
| CLI 退出 | 后台 Job 继续 | 真实 E2E |
| 50 SCF 最终发布 | 50/50 且 PackageID 正确 | 腾讯云 + catalog |

## 6. 明确删除项

实现结束后，全仓搜索必须没有生产引用：

```text
directBatchID
BatchChangeResult
makeCompletedBatchChangeStatus
create-batch-size
deploy-batch-size
```

允许旧计划文档作为历史记录提到旧名称，但当前用户文档、CLI help、脚本和生产代码不得继续使用旧同步语义。

## 7. 实施顺序摘要

```text
Task 1  Proto 异步提交/查询契约
  ↓
Task 2  SQLite Job/Item 和原子领取
  ↓
Task 3  单节点创建/部署函数提取
  ↓
Task 4  提交和查询 RPC
  ↓
Task 5  GoAndWait 批内并发 Runner 与重启恢复
  ↓
Task 6  前端真实轮询
  ↓
Task 7  CLI publish submit/status
  ↓
Task 8  E2E 和运维文档
  ↓
Task 9  全量门禁、真实 50 SCF、codeCR、推送
```

每个 Task 完成后独立提交。不得在协议、Store、Runner 尚未通过测试时先修改真实部署脚本。

## 8. 实施验收记录（2026-07-28）

真实 Tencent SCF 发布使用 `moox-cli collector function publish submit/status`
完成，未使用同步批量接口：

```text
implementation_commit: 0fc0c523038868e671e67f200298f221db538e91
space_id: crypto
package_id: moox-collector-e2e_20260727T233125Z_32de3f34-39a4-4a2d-b59f-6296b0fdaf69
package_version: 20260727T233125Z
artifact_sha256: 11b224155bb63be1834fac93ad9dc90497bff70678dc5ba002fe9896b0a632f3
publish_job_id: node-batch-4c254a15-2003-4839-a0f3-8a7a11beb785
operation: NODE_BATCH_OPERATION_DEPLOY_NODES
terminal_status: NODE_BATCH_STATUS_SUCCESS
item_result: success=50 failed=0
fleet_result: online=50 package_match=50
```

验证过程中实际重启过 CloudNode，重启后 Runner 正常启动；遗留 `RUNNING` Item
回退 `PENDING` 由 Store/Runner 重启测试覆盖。随后重新提交的 50 Item 发布 Job
收敛为 50/50 SUCCESS。最终 Runner 使用
`node_batch.batch_size=3` 和 `trpc.GoAndWait`，本批完成后才领取下一批。
发布 Item 仅在腾讯云函数最终回到 `Active` 后才写入 catalog 并标记成功；
CLI 在访问控制面或上传 COS 前拒绝超过 100 个节点的批次。

本地验证：

```text
(modules/cloudnode) go test ./... -count=1                         PASS
(modules/cloudnode) go test -race ./internal/store ./internal/rpc
  ./internal/bootstrap ./test -count=1                            PASS
(modules/cli) go test ./... -count=1                              PASS
(web) pnpm test                                                   39 files / 124 tests PASS
(web) pnpm lint:eslint:check                                      PASS
(web) pnpm lint:prettier:check                                    PASS
(web) pnpm build:prod                                             PASS
./scripts/test-go-workspace.sh                                    PASS
```
