# CloudNode Batch Change Naming Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [x]`) syntax for tracking.

**Goal:** Rename CloudNode batch management result semantics from generic `job` / `operation` / `submission` wording to `batch_change`, while keeping method names `BatchCreateNodes`, `BatchDeleteNodes`, and `BatchDeployNodes`.

**Architecture:** CloudNode control-plane batch management is distinct from collector `task_instance` and SCF async `work_item` execution. The RPC method names stay action-oriented and short; the result entity becomes `BatchChangeResult` with `batch_id` and `processed_count`. Frontend and CLI should expose `batch_change` wording to users and keep `job` reserved for real async execution work.

**Tech Stack:** Protocol Buffers under `modules/collect/proto`, Go services in `modules/cloudnode` and `modules/cli`, Vue/TypeScript admin UI under `web/src`, docs under `docs`.

---

## Naming Decisions

Use these terms consistently:

| Term | Meaning | Owns it |
|---|---|---|
| `task_rule` | Collector rule that describes what data should be collected | `modules/collector` |
| `task_instance` | Concrete collector business task generated from a rule and dataset subject | `modules/collector` |
| `batch_change` | CloudNode control-plane batch management change, such as create/delete/deploy nodes | `modules/cloudnode` |
| `work_item` | Async execution item leased by SCF runtime | `packages/cloudruntime` |
| `execution` | One runtime attempt/result for a `work_item` | `modules/cloudnode` |
| `invocation` | Synchronous cloud function call | `modules/cloudnode` |

Do not use `job`, `task`, `operation`, or `submission` for CloudNode node-management batch create/delete/deploy results.

Method names remain short:

```text
BatchCreateNodes
BatchDeleteNodes
BatchDeployNodes
```

Return/result names become:

```text
BatchChangeResult
batch_id
processed_count
message
```

---

## File Map

- Modify: `modules/collect/proto/collect_service.proto`
  - Rename the batch management response message to `BatchChangeResult`.
  - Rename response fields from `job_id` and `total_task_cnt` to `batch_id` and `processed_count`.
  - Keep RPC method names `BatchCreateNodes`, `BatchDeleteNodes`, and `BatchDeployNodes`.

- Regenerate: `modules/collect/proto/collectgen/`
  - Regenerate generated Go code after the proto change.

- Modify: `modules/cloudnode/internal/service/cloudnode/service.go`
  - Return `BatchChangeResult` with `batch_id` and `processed_count`.
  - Rename helper/local variables from operation/job wording to batch-change wording.

- Modify: `modules/cloudnode/internal/repository/catalog.go`
  - Only change if helper names or comments still mention management `job` semantics.

- Modify: `web/src/api/cloud-node.ts`
  - Expose `batch_id` and `processed_count` from batch management APIs.
  - Stop adapting backend `job_id` into `operation_id`.

- Modify: `web/src/utils/cloud-node-batch-change.ts`
  - Rename to `web/src/utils/cloud-node-batch-change.ts` if still needed.
  - Replace `submission` terminology with `batch_change` terminology.

- Modify: `web/src/views/collector/cloud-function/cloud-function.vue`
  - Replace user-facing and internal `operation` / `submission` wording for node batch management with `batch_change` wording.
  - Keep collector task and CloudNode execution terminology unchanged.

- Modify: `modules/cli/internal/adminclient/cloudnode.go`
  - Use `BatchID` and `ProcessedCount` in `BatchChangeResponse`, or rename the type to `BatchChangeResponse`.

- Modify: `modules/cli/cmd/collector.go`
  - Output JSON fields `create_batch_id`, `deploy_batch_id`, `create_processed_count`, and `deploy_processed_count`.

- Modify: docs under `docs/`
  - Update CloudNode naming docs and the audit document.

- Do not commit unless the user explicitly asks.

---

### Task 1: Update the CloudNode batch management proto

**Files:**
- Modify: `modules/collect/proto/collect_service.proto`
- Regenerate: `modules/collect/proto/collectgen/`

- [x] **Step 1: Update the response message name and fields**

In `modules/collect/proto/collect_service.proto`, replace the current management batch response with this shape:

```protobuf
message BatchChangeResult {
  RetInfo ret_info = 1;
  string batch_id = 2;
  int32 processed_count = 3;
  string message = 4;
}
```

Keep the request messages and RPC method names action-oriented:

```protobuf
rpc BatchCreateNodes(BatchCreateNodesReq) returns (BatchChangeResult);
rpc BatchDeleteNodes(BatchDeleteNodesReq) returns (BatchChangeResult);
rpc BatchDeployNodes(BatchDeployNodesReq) returns (BatchChangeResult);
```

- [x] **Step 2: Regenerate collect proto Go code**

Run only when the user has approved implementation/validation commands:

```bash
cd /Users/mooyang/Documents/go/src/github.com/mooyang-code/moox/modules/collect/proto
make all
```

Expected generated files to change under:

```text
modules/collect/proto/collectgen/
```

- [x] **Step 3: Search for old generated response type references**

Run only when implementation validation is allowed:

```bash
cd /Users/mooyang/Documents/go/src/github.com/mooyang-code/moox
rg -n 'BatchOperationRsp|job_id|total_task_cnt|operation_id|submission_id' modules web/src docs --glob '!**/node_modules/**' --glob '!**/dist/**'
```

Expected after implementation:

```text
No management batch create/delete/deploy code path should expose job_id, total_task_cnt, operation_id, or submission_id.
Real CloudNode async execution code may still use job/work-item terminology until the separate work_item rename is planned.
```

---

### Task 2: Update CloudNode service return semantics

**Files:**
- Modify: `modules/cloudnode/internal/service/cloudnode/service.go`
- Modify if needed: `modules/cloudnode/internal/repository/catalog.go`

- [x] **Step 1: Change batch methods to return BatchChangeResult**

Update these methods to return `*pb.BatchChangeResult`:

```go
func (s *Service) BatchCreateNodes(ctx context.Context, req *pb.BatchCreateNodesReq) (*pb.BatchChangeResult, error)
func (s *Service) BatchDeleteNodes(ctx context.Context, req *pb.BatchDeleteNodesReq) (*pb.BatchChangeResult, error)
func (s *Service) BatchDeployNodes(ctx context.Context, req *pb.BatchDeployNodesReq) (*pb.BatchChangeResult, error)
```

- [x] **Step 2: Use batch_id and processed_count in responses**

For direct catalog-management changes, response construction should follow this shape:

```go
return &pb.BatchChangeResult{
    RetInfo:        retOK(),
    BatchId:  newDirectBatchID("batch_create_nodes"),
    ProcessedCount: int32(len(req.GetNodes())),
    Message:        fmt.Sprintf("created %d cloud nodes", len(req.GetNodes())),
}, nil
```

Use action-specific prefixes:

```text
batch_create_nodes
batch_delete_nodes
batch_deploy_nodes
```

- [x] **Step 3: Rename helper/local variables**

Rename management helper names away from operation/job wording:

```go
func newDirectBatchID(action string) string {
    return fmt.Sprintf("%s-%d", action, time.Now().UnixNano())
}
```

Do not rename real async CloudNode job queue types in this task. The `work_item` rename is separate.

- [x] **Step 4: Format changed Go files**

Run after editing Go files:

```bash
cd /Users/mooyang/Documents/go/src/github.com/mooyang-code/moox
gofmt -w modules/cloudnode/internal/service/cloudnode/service.go modules/cloudnode/internal/repository/catalog.go
```

Expected: command exits successfully with no stdout.

---

### Task 3: Update frontend API and page terminology

**Files:**
- Modify: `web/src/api/cloud-node.ts`
- Modify: `web/src/utils/cloud-node-batch-change.ts`
- Modify: `web/src/views/collector/cloud-function/cloud-function.vue`

- [x] **Step 1: Update API response types**

In `web/src/api/cloud-node.ts`, expose batch change fields:

```ts
export interface BatchChangeResult {
  batch_id: string;
  processed_count: number;
  message?: string;
}
```

Update these functions to return `Promise<BatchChangeResult>`:

```ts
export const batchCreateNodes = async (data: BatchCreateNodesRequest): Promise<BatchChangeResult> => { ... }
export const batchDeployNodes = async (data: BatchDeployNodesRequest): Promise<BatchChangeResult> => { ... }
export const batchDeleteNodes = async (data: BatchDeleteNodesRequest): Promise<BatchChangeResult> => { ... }
```

The response unwrap should read:

```ts
const rsp = unwrap<BatchChangeResult>(
  await callControl<RequestShape, BatchChangeResult>('cloudnode', 'BatchCreateNodes', payload)
);
return {
  batch_id: rsp.batch_id ?? '',
  processed_count: rsp.processed_count ?? 0,
  message: rsp.message,
};
```

- [x] **Step 2: Rename frontend utility if present**

Update `web/src/utils/cloud-node-batch-change.ts` names like:

```text
SubmissionStatus -> BatchChangeStatus
submission_id -> batch_id
submission_status -> batch_change_status
submissionProcessing -> batchChangeProcessing
```

- [x] **Step 3: Update cloud function page state names**

In `web/src/views/collector/cloud-function/cloud-function.vue`, update management-batch wording:

```text
currentSubmissionStatus -> currentBatchChangeStatus
batchSubmissionStatuses -> batchChangeStatuses
submissionProcessing -> batchChangeProcessing
submissionCompleteHandled -> batchChangeCompleteHandled
getOperationTypeText -> getBatchChangeTypeText
```

User-facing strings should use:

```text
云节点批量变更处理中
批次ID
批量创建节点完成
批量部署节点完成
批量删除节点完成
```

Do not rename collector task-instance UI labels in this task.

---

### Task 4: Update CLI batch-change output

**Files:**
- Modify: `modules/cli/internal/adminclient/cloudnode.go`
- Modify: `modules/cli/cmd/collector.go`

- [x] **Step 1: Update admin client response type**

Use this type:

```go
type BatchChangeResponse struct {
    BatchID string
    ProcessedCount int
    Message string
}
```

Parse backend JSON fields:

```go
var resp struct {
    RetInfo        *retInfo `json:"ret_info"`
    BatchID  string   `json:"batch_id"`
    ProcessedCount int      `json:"processed_count"`
    Message        string   `json:"message"`
}
```

If `BatchID` is empty, return:

```go
return nil, fmt.Errorf("%s: empty batch_id", method)
```

- [x] **Step 2: Update CLI JSON summary fields**

In `modules/cli/cmd/collector.go`, use:

```go
type collectorPublishSummary struct {
    ZipPath                    string `json:"zip_path"`
    PackageID                  string `json:"package_id,omitempty"`
    CreateBatchID        string `json:"create_batch_id,omitempty"`
    DeployBatchID        string `json:"deploy_batch_id,omitempty"`
    CreateProcessedCount       int    `json:"create_processed_count,omitempty"`
    DeployProcessedCount       int    `json:"deploy_processed_count,omitempty"`
}
```

Assign values from the client response:

```go
summary.CreateBatchID = createResp.BatchID
summary.CreateProcessedCount = createResp.ProcessedCount
summary.DeployBatchID = deployResp.BatchID
summary.DeployProcessedCount = deployResp.ProcessedCount
```

- [x] **Step 3: Format changed CLI files**

Run after editing Go files:

```bash
cd /Users/mooyang/Documents/go/src/github.com/mooyang-code/moox
gofmt -w modules/cli/internal/adminclient/cloudnode.go modules/cli/cmd/collector.go
```

Expected: command exits successfully with no stdout.

---

### Task 5: Update docs and audit trail

**Files:**
- Modify: `docs/云节点管理.md`
- Modify: `docs/云节点执行平台架构.md`
- Modify: `docs/代码包管理.md`
- Modify: `docs/admin-cloudnode-collector-split-audit-2026-07-02.md`

- [x] **Step 1: Add a naming glossary**

Add or update a glossary with this content:

```text
task_rule      = 采集规则
task_instance  = 采集任务实例
batch_change   = 云节点批量管理变更
work_item      = SCF 异步执行待处理工作项
execution      = work_item 的一次执行记录
invocation     = 同步云函数调用
```

- [x] **Step 2: Document batch management methods**

Document that these methods are control-plane batch changes:

```text
BatchCreateNodes -> batch_id
BatchDeleteNodes -> batch_id
BatchDeployNodes -> batch_id
```

Also document that these are not collector task instances and not SCF work items.

- [x] **Step 3: Append audit record**

Append to `docs/admin-cloudnode-collector-split-audit-2026-07-02.md`:

```markdown
### YYYY-MM-DD CloudNode batch_change naming cleanup

Decided CloudNode node-management batch create/delete/deploy results use `batch_change` terminology.

Kept method names short:

```text
BatchCreateNodes
BatchDeleteNodes
BatchDeployNodes
```

Changed result semantics from `job_id` / `operation_id` / `submission_id` to:

```text
batch_id
processed_count
```

Reserved `task_instance` for collector business tasks and `work_item` for SCF async execution items.
```

---

### Task 6: Search cleanup and validation commands

**Files:**
- Inspect only; no direct file ownership.

- [x] **Step 1: Search for old management-batch names**

Run only after implementation edits:

```bash
cd /Users/mooyang/Documents/go/src/github.com/mooyang-code/moox
rg -n 'operation_id|submission_id|create_operation_id|deploy_operation_id|create_job_id|deploy_job_id|total_task_cnt|job_id' modules web/src docs --glob '!**/node_modules/**' --glob '!**/dist/**'
```

Expected:

```text
No hits for CloudNode node-management batch create/delete/deploy UI, CLI, or API paths.
Hits are acceptable only for real CloudNode async execution queue records until the separate work_item rename is implemented.
```

- [ ] **Step 2: Run targeted builds only when user authorizes validation**

Recommended commands after implementation approval:

```bash
cd /Users/mooyang/Documents/go/src/github.com/mooyang-code/moox
./scripts/build.sh proto
./scripts/build.sh cloudnode
./scripts/build.sh cli
```

Expected:

```text
Each command exits with status 0.
```

Frontend build command, only if user authorizes frontend validation:

```bash
cd /Users/mooyang/Documents/go/src/github.com/mooyang-code/moox/web
npm run build
```

Expected:

```text
Build exits with status 0 and emits updated web assets.
```

Do not commit or push unless the user explicitly asks.
