# CloudNode JobItem Runtime Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Implement the simplified CloudNode JobItem execution model, and make CloudRuntime dispatch business handlers by `job_type`.

**Architecture:** Collector creates Jobs and splits them into JobItems. CloudNode stores, dispatches, recovers, and records JobItems in SQLite. CloudRuntime polls JobItems, finds the registered handler by `job_type`, lets the handler write business data to Storage, and reports only execution summary and errors back to CloudNode.

**Tech Stack:** Protocol Buffers, tRPC-Go, GORM, SQLite, `packages/commonpb`, `packages/cloudruntime`, Vue/TypeScript admin UI.

---

## Scope

This plan intentionally optimizes for a personal project: simple code, local development, and maintainability. It does not optimize for high availability or multi-node CloudNode scheduling.

Keep:

- SQLite as the only JobItem queue backend.
- Single CloudNode scheduler instance.
- `poll -> execute -> report` runtime flow.
- `attempt_no` and `recover_at` for crash recovery.
- `InvokeSync` only as an operations/debug path, not as collector's formal task lifecycle.

Remove from the async execution path:

- `RenewLease`
- `LEASED`
- `lease_timeout_ms`
- `owner_service`
- `owner_ref`
- `idempotency_key`
- `payload_schema_version`
- `routing labels`
- `protocol_version`
- `max_inflight`
- Directive/control instruction handling

## Target Terms

| Term | Meaning |
|---|---|
| `job` | One collector business job generated from a rule |
| `job_id` | ID for a collector Job |
| `job_item` | Smallest executable unit dispatched by CloudNode |
| `job_item_id` | Unique ID and idempotency key for a JobItem |
| `job_type` | CloudRuntime handler key, for example `collect.kline` |
| `code_package_id` | Code package required to execute this JobItem |
| `attempt` | One execution try for a JobItem |
| `attempt_no` | Try number returned by Poll and required by Report |
| `recover_at` | Server-side recovery time for a RUNNING JobItem |

## Target Code Structure

```text
modules/cloudnode/
  proto/cloudnode.proto
  schema/cloudnode.sql
  internal/
    rpc/
      job_item.go
      node.go
      account.go
      package.go
      invocation.go
      server.go
      convert.go
    service/
      job_item.go
      package.go
    repository/
      models.go
      job_item.go
      node.go
      account.go
      package.go
      invocation.go

packages/cloudruntime/
  runtime.go
  registry.go
  handler.go
  client.go
  runtime_test.go

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
```

## Target Proto Shape

Use `common.Page` and `common.PageResult` for list pagination. Do not introduce `page_size` or `page_token`.

```protobuf
enum JobItemStatus {
  JOB_ITEM_STATUS_UNSPECIFIED = 0;
  JOB_ITEM_STATUS_PENDING = 1;
  JOB_ITEM_STATUS_RUNNING = 2;
  JOB_ITEM_STATUS_SUCCESS = 3;
  JOB_ITEM_STATUS_FAILED = 4;
  JOB_ITEM_STATUS_CANCELED = 5;
}

enum JobItemAckStatus {
  JOB_ITEM_ACK_STATUS_UNSPECIFIED = 0;
  JOB_ITEM_ACK_STATUS_CREATED = 1;
  JOB_ITEM_ACK_STATUS_DEDUPLICATED = 2;
  JOB_ITEM_ACK_STATUS_REJECTED = 3;
}

enum JobItemReportStatus {
  JOB_ITEM_REPORT_STATUS_UNSPECIFIED = 0;
  JOB_ITEM_REPORT_STATUS_SUCCESS = 1;
  JOB_ITEM_REPORT_STATUS_FAILED = 2;
}

enum JobItemErrorKind {
  JOB_ITEM_ERROR_KIND_UNSPECIFIED = 0;
  JOB_ITEM_ERROR_KIND_RETRYABLE = 1;
  JOB_ITEM_ERROR_KIND_PERMANENT = 2;
}

enum JobItemAttemptStatus {
  JOB_ITEM_ATTEMPT_STATUS_UNSPECIFIED = 0;
  JOB_ITEM_ATTEMPT_STATUS_RUNNING = 1;
  JOB_ITEM_ATTEMPT_STATUS_SUCCESS = 2;
  JOB_ITEM_ATTEMPT_STATUS_FAILED = 3;
  JOB_ITEM_ATTEMPT_STATUS_LOST = 4;
}
```

```protobuf
message JobItem {
  string space_id = 1;
  string job_id = 2;
  string job_item_id = 3;
  string job_type = 4;
  string code_package_id = 5;
  google.protobuf.Struct params = 6;
  int32 priority = 7;
}

message SubmitJobItemsReq {
  repeated JobItem items = 1;
}

message JobItemAck {
  string job_item_id = 1;
  JobItemAckStatus status = 2;
  string reject_reason = 3;
}

message SubmitJobItemsRsp {
  common.RetInfo ret_info = 1;
  repeated JobItemAck acks = 2;
  int32 created = 3;
  int32 deduplicated = 4;
  int32 rejected = 5;
}
```

```protobuf
message PollJobItemsReq {
  string space_id = 1;
  string node_id = 2;
  repeated string supported_job_types = 3;
  int32 limit = 4;
}

message PolledJobItem {
  string space_id = 1;
  string job_id = 2;
  string job_item_id = 3;
  string job_type = 4;
  string code_package_id = 5;
  google.protobuf.Struct params = 6;
  int32 attempt_no = 7;
}

message PollJobItemsRsp {
  common.RetInfo ret_info = 1;
  repeated PolledJobItem items = 2;
  google.protobuf.Timestamp poll_time = 3;
}
```

```protobuf
message ReportJobItemStatusReq {
  string space_id = 1;
  string node_id = 2;
  string job_item_id = 3;
  int32 attempt_no = 4;
  JobItemReportStatus status = 5;
  JobItemErrorKind error_kind = 6;
  string error_code = 7;
  string error_message = 8;
  google.protobuf.Struct result_summary = 9;
  int64 duration_ms = 10;
}

message ReportJobItemStatusRsp {
  common.RetInfo ret_info = 1;
}
```

```protobuf
message CancelJobItemReq {
  string space_id = 1;
  string job_item_id = 2;
}

message CancelJobItemRsp {
  common.RetInfo ret_info = 1;
}

message GetJobItemReq {
  string space_id = 1;
  string job_item_id = 2;
}

message JobItemDetail {
  string space_id = 1;
  string job_id = 2;
  string job_item_id = 3;
  string job_type = 4;
  string code_package_id = 5;
  google.protobuf.Struct params = 6;
  int32 priority = 7;
  JobItemStatus status = 8;
  string running_node = 9;
  int32 attempt_no = 10;
  google.protobuf.Timestamp recover_at = 11;
  google.protobuf.Struct result_summary = 12;
  JobItemErrorKind last_error_kind = 13;
  string last_error_code = 14;
  string last_error_message = 15;
  google.protobuf.Timestamp create_time = 16;
  google.protobuf.Timestamp start_time = 17;
  google.protobuf.Timestamp finish_time = 18;
}

message GetJobItemRsp {
  common.RetInfo ret_info = 1;
  JobItemDetail item = 2;
}

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

message JobItemAttempt {
  int32 attempt_no = 1;
  string node_id = 2;
  JobItemAttemptStatus status = 3;
  JobItemErrorKind error_kind = 4;
  string error_code = 5;
  string error_message = 6;
  google.protobuf.Struct result_summary = 7;
  google.protobuf.Timestamp started_at = 8;
  google.protobuf.Timestamp finished_at = 9;
}

message ListJobItemAttemptsReq {
  string space_id = 1;
  string job_item_id = 2;
}

message ListJobItemAttemptsRsp {
  common.RetInfo ret_info = 1;
  repeated JobItemAttempt attempts = 2;
}
```

Add these methods to `CloudNodeMgr`:

```protobuf
rpc SubmitJobItems(SubmitJobItemsReq) returns (SubmitJobItemsRsp);
rpc PollJobItems(PollJobItemsReq) returns (PollJobItemsRsp);
rpc ReportJobItemStatus(ReportJobItemStatusReq) returns (ReportJobItemStatusRsp);
rpc CancelJobItem(CancelJobItemReq) returns (CancelJobItemRsp);
rpc GetJobItem(GetJobItemReq) returns (GetJobItemRsp);
rpc ListJobItems(ListJobItemsReq) returns (ListJobItemsRsp);
rpc ListJobItemAttempts(ListJobItemAttemptsReq) returns (ListJobItemAttemptsRsp);
```

## Task 1: Proto Contract

**Files:**

- Modify: `modules/cloudnode/proto/cloudnode.proto`
- Modify: `modules/cloudnode/internal/rpc/proto_contract_test.go`
- Regenerate: `modules/cloudnode/proto/cloudnodegen/`
- Modify after generation: `packages/cloudruntime/runtime.go`
- Modify after generation: `modules/collector/internal/taskpublisher/client.go`
- Modify after generation: `modules/collector/internal/taskrunner/poller.go`

- [ ] **Step 1: Add a failing contract test**

Add assertions that the async execution protocol exposes JobItem names and no removed fields:

```go
func TestJobItemProtoContract(t *testing.T) {
    desc := (&pb.JobItem{}).ProtoReflect().Descriptor()
    mustHaveFields(t, desc, "space_id", "job_id", "job_item_id", "job_type", "code_package_id", "params", "priority")
    mustNotHaveFields(t, desc, "owner_service", "owner_ref", "idempotency_key", "payload_schema_version", "deployment_id", "lease_timeout_ms")

    listDesc := (&pb.ListJobItemsReq{}).ProtoReflect().Descriptor()
    mustHaveFields(t, listDesc, "space_id", "job_id", "job_type", "status", "page")
    mustNotHaveFields(t, listDesc, "page_size", "page_token")
}
```

Run:

```bash
cd /Users/mooyang/Documents/go/src/github.com/mooyang-code/moox/modules/cloudnode
go test ./internal/rpc -run TestJobItemProtoContract -count=1
```

Expected: fail because `JobItem` does not exist yet.

- [ ] **Step 2: Update `cloudnode.proto`**

Replace the current async execution section with the target JobItem protocol above. Keep node, account, package, heartbeat, and `InvokeSync` protocol sections.

- [ ] **Step 3: Regenerate proto**

Run:

```bash
cd /Users/mooyang/Documents/go/src/github.com/mooyang-code/moox/modules/cloudnode/proto
make all
```

Expected: `cloudnode.pb.go` and related generated files compile with new JobItem messages.

- [ ] **Step 4: Re-run proto tests**

Run:

```bash
cd /Users/mooyang/Documents/go/src/github.com/mooyang-code/moox/modules/cloudnode
go test ./internal/rpc -run TestJobItemProtoContract -count=1
```

Expected: pass.

## Task 2: SQLite Schema and Models

**Files:**

- Modify: `modules/cloudnode/schema/cloudnode.sql`
- Modify: `modules/cloudnode/internal/repository/models.go`
- Modify: `modules/cloudnode/internal/storage/migrate.go` if table names are referenced explicitly
- Test: `modules/cloudnode/internal/repository/job_item_test.go`

- [ ] **Step 1: Rename queue tables conceptually**

Because this is a new project, prefer rebuilding the local SQLite schema instead of keeping compatibility migrations. Target tables:

```text
t_cloud_job_items
t_cloud_job_item_attempts
```

Keep invocation tables separate:

```text
t_cloud_invocations
t_cloud_invocation_results
```

- [ ] **Step 2: Define `t_cloud_job_items` fields**

Use these columns:

```sql
c_id INTEGER PRIMARY KEY AUTOINCREMENT,
c_space_id TEXT NOT NULL,
c_job_id TEXT NOT NULL,
c_job_item_id TEXT NOT NULL,
c_job_type TEXT NOT NULL,
c_code_package_id TEXT NOT NULL,
c_params TEXT NOT NULL,
c_priority INTEGER NOT NULL DEFAULT 0,
c_status TEXT NOT NULL,
c_running_node TEXT,
c_attempt_no INTEGER NOT NULL DEFAULT 0,
c_recover_at DATETIME,
c_result_summary TEXT,
c_last_error_kind TEXT,
c_last_error_code TEXT,
c_last_error_message TEXT,
c_ctime DATETIME NOT NULL,
c_mtime DATETIME NOT NULL,
c_start_time DATETIME,
c_finish_time DATETIME
```

Add indexes:

```sql
CREATE UNIQUE INDEX idx_cloud_job_items_space_item ON t_cloud_job_items(c_space_id, c_job_item_id);
CREATE INDEX idx_cloud_job_items_poll ON t_cloud_job_items(c_space_id, c_status, c_priority, c_ctime);
CREATE INDEX idx_cloud_job_items_recover ON t_cloud_job_items(c_space_id, c_status, c_recover_at);
CREATE INDEX idx_cloud_job_items_job ON t_cloud_job_items(c_space_id, c_job_id);
```

- [ ] **Step 3: Define `t_cloud_job_item_attempts` fields**

Use these columns:

```sql
c_id INTEGER PRIMARY KEY AUTOINCREMENT,
c_space_id TEXT NOT NULL,
c_job_item_id TEXT NOT NULL,
c_attempt_no INTEGER NOT NULL,
c_node_id TEXT NOT NULL,
c_status TEXT NOT NULL,
c_error_kind TEXT,
c_error_code TEXT,
c_error_message TEXT,
c_result_summary TEXT,
c_started_at DATETIME NOT NULL,
c_finished_at DATETIME,
c_ctime DATETIME NOT NULL,
c_mtime DATETIME NOT NULL
```

Add indexes:

```sql
CREATE UNIQUE INDEX idx_cloud_job_item_attempts_unique ON t_cloud_job_item_attempts(c_space_id, c_job_item_id, c_attempt_no);
CREATE INDEX idx_cloud_job_item_attempts_item ON t_cloud_job_item_attempts(c_space_id, c_job_item_id);
```

- [ ] **Step 4: Update GORM models**

Define `JobItem` / `JobItemAttempt` GORM models. Keep status constants close to the repository implementation:

```go
const (
    JobItemStatusPending  = "pending"
    JobItemStatusRunning  = "running"
    JobItemStatusSuccess  = "success"
    JobItemStatusFailed   = "failed"
    JobItemStatusCanceled = "canceled"

    JobItemAttemptRunning = "running"
    JobItemAttemptSuccess = "success"
    JobItemAttemptFailed  = "failed"
    JobItemAttemptLost    = "lost"
)
```

- [ ] **Step 5: Run schema/model tests**

Run:

```bash
cd /Users/mooyang/Documents/go/src/github.com/mooyang-code/moox/modules/cloudnode
go test ./internal/repository -run 'Test.*JobItem' -count=1
```

Expected initially: fail until repository code is updated in Task 3.

## Task 3: CloudNode JobItem Repository and Service

**Files:**

- Create: `modules/cloudnode/internal/service/job_item.go`
- Modify: `modules/cloudnode/internal/repository/job_item.go`
- Modify: `modules/cloudnode/internal/repository/job_item_test.go`

- [ ] **Step 1: Define repository API at the service boundary**

The service should depend on this shape:

```go
type JobItemStore interface {
    Submit(ctx context.Context, items []*pb.JobItem) ([]*pb.JobItemAck, error)
    Poll(ctx context.Context, req *pb.PollJobItemsReq) ([]*pb.PolledJobItem, error)
    Report(ctx context.Context, req *pb.ReportJobItemStatusReq) error
    Cancel(ctx context.Context, req *pb.CancelJobItemReq) error
    Get(ctx context.Context, req *pb.GetJobItemReq) (*pb.JobItemDetail, error)
    List(ctx context.Context, req *pb.ListJobItemsReq) ([]*pb.JobItemDetail, *commonpb.PageResult, error)
    ListAttempts(ctx context.Context, req *pb.ListJobItemAttemptsReq) ([]*pb.JobItemAttempt, error)
}
```

- [ ] **Step 2: Implement Submit semantics**

Rules:

```text
space_id required
job_id required
job_item_id required
job_type required
code_package_id required
params may be empty Struct but must not be nil after normalization
UNIQUE(space_id, job_item_id)
duplicate returns ACK_STATUS_DEDUPLICATED
invalid item returns ACK_STATUS_REJECTED for that item
```

- [ ] **Step 3: Implement Poll recovery and dispatch**

Poll transaction order:

```text
1. Validate space_id, node_id, supported_job_types.
2. Load node by (space_id, node_id).
3. Mark expired RUNNING items as PENDING and mark their current attempt LOST.
4. Query PENDING items where:
   - space_id matches
   - job_type in supported_job_types
   - code_package_id matches node.package_id
5. Order by priority DESC, ctime ASC.
6. For each selected item:
   - status = RUNNING
   - running_node = node_id
   - attempt_no = attempt_no + 1
   - recover_at = now + configured recover_after
   - start_time = now
   - create JobItemAttempt(status=RUNNING)
```

- [ ] **Step 4: Implement Report semantics**

Rules:

```text
Report accepts only SUCCESS or FAILED.
Report must match space_id, job_item_id, node_id, attempt_no.
If attempt_no mismatches, return stale-attempt conflict.
SUCCESS sets JobItem SUCCESS and attempt SUCCESS.
FAILED + RETRYABLE sets JobItem PENDING when attempts remain.
FAILED + PERMANENT sets JobItem FAILED.
FAILED after max attempts sets JobItem FAILED.
```

- [ ] **Step 5: Implement Cancel semantics**

Rules:

```text
Cancel only PENDING.
RUNNING returns conflict.
SUCCESS/FAILED/CANCELED return conflict or no-op consistently; prefer conflict for clarity.
```

- [ ] **Step 6: Implement query semantics**

`GetJobItem`, `ListJobItems`, and `ListJobItemAttempts` must return `google.protobuf.Timestamp` fields and use `common.PageResult`.

- [ ] **Step 7: Repository tests**

Add table-driven tests:

```text
TestSubmitJobItemsDeduplicatesBySpaceAndJobItemID
TestPollJobItemsDispatchesByPackageAndJobType
TestPollJobItemsRecoversExpiredRunningAttemptAsLost
TestReportRejectsStaleAttemptNumber
TestReportRetryableFailureReturnsToPending
TestReportPermanentFailureMarksFailed
TestCancelJobItemOnlyAllowsPending
TestListJobItemsUsesCommonPageResult
```

Run:

```bash
cd /Users/mooyang/Documents/go/src/github.com/mooyang-code/moox/modules/cloudnode
go test ./internal/repository -count=1
```

Expected: pass.

## Task 4: CloudNode RPC Wiring

**Files:**

- Create/Modify: `modules/cloudnode/internal/rpc/job_item.go`
- Modify: `modules/cloudnode/internal/rpc/server.go`
- Modify: `modules/cloudnode/internal/rpc/common.go`
- Test: `modules/cloudnode/internal/rpc/job_item_test.go`

- [ ] **Step 1: Add RPC methods**

Implement:

```go
func (s *Service) SubmitJobItems(ctx context.Context, req *pb.SubmitJobItemsReq) (*pb.SubmitJobItemsRsp, error)
func (s *Service) PollJobItems(ctx context.Context, req *pb.PollJobItemsReq) (*pb.PollJobItemsRsp, error)
func (s *Service) ReportJobItemStatus(ctx context.Context, req *pb.ReportJobItemStatusReq) (*pb.ReportJobItemStatusRsp, error)
func (s *Service) CancelJobItem(ctx context.Context, req *pb.CancelJobItemReq) (*pb.CancelJobItemRsp, error)
func (s *Service) GetJobItem(ctx context.Context, req *pb.GetJobItemReq) (*pb.GetJobItemRsp, error)
func (s *Service) ListJobItems(ctx context.Context, req *pb.ListJobItemsReq) (*pb.ListJobItemsRsp, error)
func (s *Service) ListJobItemAttempts(ctx context.Context, req *pb.ListJobItemAttemptsReq) (*pb.ListJobItemAttemptsRsp, error)
```

- [ ] **Step 2: Preserve execution-plane space_id rule**

All service-plane JobItem RPCs use explicit `space_id` from request body. Do not read JobItem `space_id` from ctx.

- [ ] **Step 3: Convert repository errors to RetInfo**

Map:

```text
invalid param -> common.INVALID_PARAM
not found -> common.NOT_FOUND
stale attempt / cancel conflict -> common.INVALID_PARAM with message prefix "conflict:"
internal DB error -> common.INNER_ERR
```

- [ ] **Step 4: RPC tests**

Run:

```bash
cd /Users/mooyang/Documents/go/src/github.com/mooyang-code/moox/modules/cloudnode
go test ./internal/rpc -count=1
```

Expected: pass.

## Task 5: CloudRuntime Registry and Client

**Files:**

- Modify: `packages/cloudruntime/runtime.go`
- Create: `packages/cloudruntime/handler.go`
- Create: `packages/cloudruntime/registry.go`
- Create: `packages/cloudruntime/client.go`
- Modify: `packages/cloudruntime/runtime_test.go`

- [ ] **Step 1: Define runtime types**

Target public API:

```go
type JobItem struct {
    SpaceID       string
    JobID         string
    JobItemID     string
    JobType       string
    CodePackageID string
    Params        map[string]any
    AttemptNo     int
}

type Result struct {
    Summary map[string]any
}

type ErrorKind string

const (
    ErrorKindRetryable ErrorKind = "retryable"
    ErrorKindPermanent ErrorKind = "permanent"
)

type Handler interface {
    Execute(ctx context.Context, item JobItem) (Result, error)
}
```

Provide helper error wrappers:

```go
func Retryable(err error, code string) error
func Permanent(err error, code string) error
```

- [ ] **Step 2: Add registry**

Target API:

```go
func Register(jobType string, handler Handler)
func Run(ctx context.Context, cfg Config) error
```

Rules:

```text
Register rejects empty job_type.
Register rejects nil handler.
Duplicate job_type panics during startup.
Run polls once per invocation and reports each item.
Missing handler reports FAILED + PERMANENT + error_code=HANDLER_NOT_FOUND.
```

- [ ] **Step 3: Update CloudNode client calls**

Replace:

```text
poll RPC -> PollJobItems
report RPC -> ReportJobItemStatus
async item id -> job_item_id
workload_type -> job_type
deployment_id -> code_package_id
payload -> params
```

- [ ] **Step 4: Runtime tests**

Add tests:

```text
TestRegisterRejectsDuplicateJobType
TestRunPollsJobItemsAndDispatchesRegisteredHandler
TestRunReportsPermanentFailureWhenHandlerMissing
TestRunReportsRetryableErrorKind
TestRunReportsAttemptNo
```

Run:

```bash
cd /Users/mooyang/Documents/go/src/github.com/mooyang-code/moox/packages/cloudruntime
go test ./... -count=1
```

Expected: pass.

## Task 6: Collector Jobs Split

**Files:**

- Create: `modules/collector/internal/jobs/registry.go`
- Create: `modules/collector/internal/jobs/kline/params.go`
- Create: `modules/collector/internal/jobs/kline/planner.go`
- Create: `modules/collector/internal/jobs/kline/handler.go`
- Create: `modules/collector/internal/jobs/kline/result.go`
- Create: `modules/collector/internal/jobs/symbol/params.go`
- Create: `modules/collector/internal/jobs/symbol/planner.go`
- Create: `modules/collector/internal/jobs/symbol/handler.go`
- Create: `modules/collector/internal/jobs/symbol/result.go`
- Modify: `modules/collector/internal/planner/`
- Modify: `modules/collector/internal/taskpublisher/client.go`
- Modify: `modules/collector/internal/taskrunner/poller.go`

- [ ] **Step 1: Introduce job type constants**

Use:

```go
const (
    JobTypeCollectKline  = "collect.kline"
    JobTypeCollectSymbol = "collect.symbol"
)
```

- [ ] **Step 2: Move K-line planning into `jobs/kline`**

`jobs/kline/planner.go` owns:

```text
TaskRule -> []JobItem
dataset subjects expansion
interval expansion
job_id generation
job_item_id generation
```

`job_item_id` must be stable for the same logical unit. Use deterministic fields such as:

```text
space_id
rule_id
job_type
exchange
market
symbol
interval
time bucket or planned execution key
```

- [ ] **Step 3: Move symbol planning into `jobs/symbol`**

`jobs/symbol/planner.go` owns symbol full-list JobItem creation. It must not require `dataset_subjects` as input.

- [ ] **Step 4: Update taskpublisher**

Submit `SubmitJobItemsReq` with:

```text
space_id
job_id
job_item_id
job_type
code_package_id
params
priority
```

Remove use of:

```text
owner_service
owner_ref
deployment_id
payload_schema_version
idempotency_key
```

- [ ] **Step 5: Update taskrunner**

Register:

```go
cloudruntime.Register(jobs.JobTypeCollectKline, kline.Handler(...))
cloudruntime.Register(jobs.JobTypeCollectSymbol, symbol.Handler(...))
return cloudruntime.Run(ctx, cfg)
```

- [ ] **Step 6: Collector tests**

Add or update tests:

```text
TestKlinePlannerBuildsStableJobItems
TestSymbolPlannerDoesNotRequireDatasetSubjects
TestTaskPublisherSubmitsJobItems
TestTaskRunnerRegistersCollectorHandlers
```

Run:

```bash
cd /Users/mooyang/Documents/go/src/github.com/mooyang-code/moox/modules/collector
go test ./... -count=1
```

Expected: pass.

## Task 7: Web, CLI, and API Naming

**Files:**

- Modify: `web/src/api/cloud-node.ts`
- Modify: `web/src/views/collector/cloud-function/cloud-function.vue`
- Modify: `web/src/views/collector/task-instances/task-instances.vue`
- Modify: `modules/cli/internal/adminclient/cloudnode.go`
- Modify: `modules/cli/cmd/collector.go`

- [ ] **Step 1: Rename API types**

Frontend and CLI should expose:

```text
job_id
job_item_id
job_type
code_package_id
attempt_no
result_summary
```

Do not expose:

```text
legacy async item id
owner_service
owner_ref
deployment_id for JobItem routing
lease_deadline
```

- [ ] **Step 2: Keep code package management wording separate**

Cloud function package pages continue using:

```text
package_id
package_version
```

JobItem routing uses:

```text
code_package_id
```

- [ ] **Step 3: Type-check web**

Run:

```bash
cd /Users/mooyang/Documents/go/src/github.com/mooyang-code/moox
pnpm --dir web exec vue-tsc --noEmit
```

Expected: no new errors from CloudNode/JobItem API changes. If unrelated pre-existing errors remain, record them with file paths.

## Task 8: Documentation and Cleanup

**Files:**

- Modify: `docs/云节点管理.md`
- Modify: `docs/采集任务管理.md`
- Modify: `packages/cloudruntime/README.md`
- Modify: `docs/superpowers/plans/2026-07-04-cloudnode-job-item-runtime-simplification.md`
- Optional: update audit/completion matrix docs if they are kept as current-state references

- [ ] **Step 1: Update active docs**

Ensure active docs use JobItem target terminology and explain:

```text
Job / JobItem
job_type handler registration
code_package_id routing
attempt_no stale result protection
SQLite single-instance boundary
common.Page pagination
```

- [ ] **Step 2: Search for old async execution wording**

Run:

```bash
cd /Users/mooyang/Documents/go/src/github.com/mooyang-code/moox
rg -n 'legacy async item|owner_service|owner_ref|lease_timeout_ms|payload_schema_version' docs packages modules web/src --glob '!**/node_modules/**'
```

Expected: legacy platform fields remain only in historical audit docs or migration notes. Active code should use JobItem terms after all tasks complete.

## Task 9: Full Verification

Run these commands:

```bash
cd /Users/mooyang/Documents/go/src/github.com/mooyang-code/moox/modules/cloudnode
go test ./... -count=1
```

```bash
cd /Users/mooyang/Documents/go/src/github.com/mooyang-code/moox/packages/cloudruntime
go test ./... -count=1
```

```bash
cd /Users/mooyang/Documents/go/src/github.com/mooyang-code/moox/modules/collector
go test ./... -count=1
```

```bash
cd /Users/mooyang/Documents/go/src/github.com/mooyang-code/moox
git diff --check
```

Expected:

```text
cloudnode tests pass
cloudruntime tests pass
collector tests pass
git diff --check has no whitespace errors
```

## Acceptance Checklist

- [ ] `cloudnode.proto` exposes JobItem protocol and uses `common.Page` / `common.PageResult`.
- [ ] CloudRuntime has handler registration by `job_type`.
- [ ] Runtime execution path is only `poll -> execute -> report`.
- [ ] Collector has concrete job directories for K-line and symbol collection.
- [ ] JobItem idempotency uses `(space_id, job_item_id)`.
- [ ] `attempt_no` rejects stale reports.
- [ ] Expired RUNNING JobItems recover through `recover_at`.
- [ ] CloudNode stores execution summary only; business data goes to Storage.
- [ ] SQLite remains the only queue backend.
- [ ] `InvokeSync` remains outside collector's formal task lifecycle.
