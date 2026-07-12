# CloudNode JetStream JobItem Queue Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Replace CloudNode's high-concurrency SQLite JobItem lease queue with a NATS JetStream execution queue while keeping SQLite as the management-console query projection and control-plane store.

**Architecture:** SCF runtimes continue to talk only to CloudNode/Admin Gateway. JetStream becomes the execution queue and retry/ack source of truth; SQLite keeps task plans, queryable JobItem projection rows, attempts, cancellation state, node metadata, code packages, and console-facing lists. CloudNode owns all JetStream interaction, batches projection writes, and exposes the same RPC surface with a cleaner internal split between queue facts and query facts.

**Tech Stack:** Go 1.24, tRPC-Go, NATS JetStream (`github.com/nats-io/nats.go` and optional embedded `nats-server`), SQLite/GORM, existing CloudNode/Collector protobuf APIs, Vue management console query APIs.

---

## References

- NATS JetStream stores messages for replay and durable processing: https://docs.nats.io/nats-concepts/jetstream
- NATS JetStream consumers support explicit ack, nak, in-progress ack, durable state, pull consumers, and batching: https://docs.nats.io/nats-concepts/jetstream/consumers
- NATS recommends pull consumers for new projects when flow control and error handling matter: https://docs.nats.io/using-nats/developer/develop_jetstream/consumers

## Non-Negotiable Decisions

- SCF never connects to NATS or SQLite directly.
- `storage` keeps using `moox.storage.*` subjects; CloudNode uses only `moox.cloudnode.*` subjects.
- JetStream is the execution queue. SQLite is the control/query projection and management database.
- Management-console cancel/pause/retry writes SQLite first. CloudNode checks SQLite before returning a JobItem to SCF and through heartbeat/progress directives.
- New project, no compatibility requirement: old SQLite queue leasing behavior can be replaced in one controlled refactor.

## Stream And Subject Naming

Use two CloudNode-specific streams. Do not reuse `MOOX_STORAGE` or any `moox.storage.*` subject.

```text
Stream: MOOX_CLOUDNODE_EXEC
Subjects:
  moox.cloudnode.exec.v1.jobitem.s.<space_token>.pkg.<package_token>.type.<job_type_token>

Stream: MOOX_CLOUDNODE_PROJECTION
Subjects:
  moox.cloudnode.projection.v1.jobitem.submitted
  moox.cloudnode.projection.v1.jobitem.running
  moox.cloudnode.projection.v1.jobitem.reported
  moox.cloudnode.projection.v1.jobitem.canceled
  moox.cloudnode.projection.v1.node.heartbeat
```

Token rules:

- `space_token`, `package_token`, and `job_type_token` are routing tokens only.
- Payload always carries the original `space_id`, `code_package_id`, and `job_type`.
- Token format is lowercase `[a-z0-9_-]`; any other character is replaced with `_`.
- If the sanitized token is empty or longer than 64 characters, use `x` + first 16 hex chars of SHA-256.
- Example:

```text
space_id: crypto
code_package_id: moox-collector_dev
job_type: collect.kline
subject: moox.cloudnode.exec.v1.jobitem.s.crypto.pkg.moox-collector_dev.type.collect_kline
```

Recommended stream config for personal deployment:

```text
MOOX_CLOUDNODE_EXEC:
  retention: WorkQueuePolicy
  storage: FileStorage
  replicas: 1
  max_age: 72h
  discard: old

MOOX_CLOUDNODE_PROJECTION:
  retention: LimitsPolicy
  storage: FileStorage
  replicas: 1
  max_age: 168h
  discard: old
```

CloudNode embedded NATS default:

```yaml
jetstream:
  enabled: true
  nats_url: nats://127.0.0.1:4223
  subject_prefix: moox.cloudnode
  exec_stream: MOOX_CLOUDNODE_EXEC
  projection_stream: MOOX_CLOUDNODE_PROJECTION
  embedded:
    enabled: true
    host: 127.0.0.1
    port: 4223
    store_dir: ../data/cloudnode/nats
```

The default port is `4223` to avoid colliding with storage examples that use `4222`.

---

## Target File Structure

```text
modules/cloudnode/internal/config/
  config.go                         # add jetstream/backend config

modules/cloudnode/internal/bootstrap/
  bootstrap.go                      # start/close embedded JetStream and projection workers

modules/cloudnode/internal/jobqueue/
  naming.go                         # stream and subject naming, token sanitizer
  payload.go                        # JobItem execution and projection event payloads
  queue.go                          # execution queue interfaces
  jetstream_client.go               # NATS connection, stream/consumer setup, ack helpers
  jetstream_queue.go                # Publish/Fetch/Ack/Nak/Term implementation
  embedded.go                       # optional embedded NATS JetStream runtime
  naming_test.go
  jetstream_queue_test.go

modules/cloudnode/internal/projection/
  repository.go                     # SQLite JobItem projection and attempts
  projector.go                      # durable projection consumer and batch writer
  heartbeat_buffer.go               # latest-wins node heartbeat batcher
  repository_test.go
  projector_test.go
  heartbeat_buffer_test.go

modules/cloudnode/internal/rpc/
  server.go                         # wire queue + projection + heartbeat buffer
  job_item.go                       # orchestrate Submit/Poll/Report/Cancel through new components
  node.go                           # ReportHeartbeat enqueues heartbeat projection instead of direct write
  job_item_test.go
  node_test.go

modules/cloudnode/proto/
  cloudnode.proto                   # add canceled report status and heartbeat directives
  cloudnodegen/                     # regenerated Go code

modules/cloudnode/schema/
  cloudnode.sql                     # projection columns for queue metadata and enqueue state

modules/collector/internal/reporter/
  heartbeat.go                      # parse heartbeat directives

modules/collector/internal/serverless/
  handler.go                        # stop long-running work when directive says CANCEL

examples/e2e/
  cloudnode_jetstream_queue.md      # documented manual e2e flow
```

---

### Task 1: Add CloudNode JetStream Config And Naming Registry

**Files:**
- Modify: `modules/cloudnode/internal/config/config.go`
- Modify: `modules/cloudnode/config/app.yaml`
- Create: `modules/cloudnode/internal/jobqueue/naming.go`
- Test: `modules/cloudnode/internal/jobqueue/naming_test.go`

- [ ] **Step 1: Write failing naming tests**

Create `modules/cloudnode/internal/jobqueue/naming_test.go` with tests for stream names, subject prefixes, token sanitization, and storage-prefix isolation:

```go
func TestExecSubjectUsesCloudNodePrefix(t *testing.T) {
	cfg := NamingConfig{SubjectPrefix: "moox.cloudnode"}
	got := ExecSubject(cfg, "crypto", "moox-collector_dev", "collect.kline")
	want := "moox.cloudnode.exec.v1.jobitem.s.crypto.pkg.moox-collector_dev.type.collect_kline"
	if got != want {
		t.Fatalf("ExecSubject() = %q, want %q", got, want)
	}
	if strings.HasPrefix(got, "moox.storage.") {
		t.Fatalf("exec subject must not use storage prefix: %s", got)
	}
}

func TestTokenFallsBackToHashWhenTooLong(t *testing.T) {
	raw := strings.Repeat("A", 80)
	got := SubjectToken(raw)
	if !strings.HasPrefix(got, "x") || len(got) != 17 {
		t.Fatalf("SubjectToken(%d chars) = %q, want x + 16 hex chars", len(raw), got)
	}
}
```

- [ ] **Step 2: Run test and verify red**

Run:

```bash
go test ./modules/cloudnode/internal/jobqueue -run TestExecSubjectUsesCloudNodePrefix
```

Expected: fail because `modules/cloudnode/internal/jobqueue` does not exist.

- [ ] **Step 3: Implement naming and config structs**

Create `modules/cloudnode/internal/jobqueue/naming.go` with:

```go
type NamingConfig struct {
	SubjectPrefix string
}

const (
	DefaultSubjectPrefix     = "moox.cloudnode"
	DefaultExecStream        = "MOOX_CLOUDNODE_EXEC"
	DefaultProjectionStream  = "MOOX_CLOUDNODE_PROJECTION"
)
```

Implement `SubjectToken`, `ExecSubject`, `ProjectionSubject`, and `ExecFilterSubject`.

Modify `modules/cloudnode/internal/config/config.go`:

```go
type QueueConfig struct {
	Backend string `yaml:"backend"` // sqlite or jetstream
}

type JetStreamConfig struct {
	Enabled          bool                   `yaml:"enabled"`
	NATSURL          string                 `yaml:"nats_url"`
	SubjectPrefix    string                 `yaml:"subject_prefix"`
	ExecStream       string                 `yaml:"exec_stream"`
	ProjectionStream string                 `yaml:"projection_stream"`
	Embedded         EmbeddedJetStreamConfig `yaml:"embedded"`
	AckWaitMillis    int64                  `yaml:"ack_wait_millis"`
	MaxDeliver       int                    `yaml:"max_deliver"`
	FetchMaxWaitMs   int64                  `yaml:"fetch_max_wait_ms"`
}

type EmbeddedJetStreamConfig struct {
	Enabled          bool   `yaml:"enabled"`
	Host             string `yaml:"host"`
	Port             int    `yaml:"port"`
	StoreDir         string `yaml:"store_dir"`
	StartupTimeoutMS int64  `yaml:"startup_timeout_ms"`
}
```

Default values:

```go
Queue: QueueConfig{Backend: "jetstream"}
JetStream: JetStreamConfig{
	Enabled: true,
	NATSURL: "nats://127.0.0.1:4223",
	SubjectPrefix: "moox.cloudnode",
	ExecStream: "MOOX_CLOUDNODE_EXEC",
	ProjectionStream: "MOOX_CLOUDNODE_PROJECTION",
	AckWaitMillis: int64(2 * time.Minute / time.Millisecond),
	MaxDeliver: 3,
	FetchMaxWaitMs: 500,
	Embedded: EmbeddedJetStreamConfig{
		Enabled: true,
		Host: "127.0.0.1",
		Port: 4223,
		StoreDir: "../data/cloudnode/nats",
		StartupTimeoutMS: 10000,
	},
}
```

- [ ] **Step 4: Verify green**

Run:

```bash
go test ./modules/cloudnode/internal/jobqueue ./modules/cloudnode/internal/config
```

Expected: pass.

### Task 2: Add JetStream Runtime And Stream Setup

**Files:**
- Create: `modules/cloudnode/internal/jobqueue/embedded.go`
- Create: `modules/cloudnode/internal/jobqueue/jetstream_client.go`
- Modify: `modules/cloudnode/go.mod`
- Test: `modules/cloudnode/internal/jobqueue/jetstream_client_test.go`

- [ ] **Step 1: Write failing embedded runtime test**

Create a test that starts an embedded JetStream server on a random free port, ensures both streams exist, and closes cleanly.

Expected stream subjects:

```text
MOOX_CLOUDNODE_EXEC -> moox.cloudnode.exec.v1.>
MOOX_CLOUDNODE_PROJECTION -> moox.cloudnode.projection.v1.>
```

- [ ] **Step 2: Run test and verify red**

Run:

```bash
go test ./modules/cloudnode/internal/jobqueue -run TestEmbeddedJetStreamCreatesCloudNodeStreams
```

Expected: fail because embedded runtime and client setup do not exist.

- [ ] **Step 3: Implement embedded runtime and stream setup**

Use direct dependencies in `modules/cloudnode/go.mod`:

```text
github.com/nats-io/nats.go
github.com/nats-io/nats-server/v2
```

Create:

```go
type Runtime struct {
	server *server.Server
	nc     *nats.Conn
	js     nats.JetStreamContext
}

func StartEmbedded(ctx context.Context, cfg config.EmbeddedJetStreamConfig) (*Runtime, error)
func Connect(ctx context.Context, cfg config.JetStreamConfig) (*Runtime, error)
func (r *Runtime) Close() error
func (r *Runtime) EnsureStreams(cfg config.JetStreamConfig) error
```

Stream config:

```go
nats.StreamConfig{
	Name: cfg.ExecStream,
	Subjects: []string{prefix + ".exec.v1.>"},
	Retention: nats.WorkQueuePolicy,
	Storage: nats.FileStorage,
	Replicas: 1,
	MaxAge: 72 * time.Hour,
}
```

Projection stream:

```go
nats.StreamConfig{
	Name: cfg.ProjectionStream,
	Subjects: []string{prefix + ".projection.v1.>"},
	Retention: nats.LimitsPolicy,
	Storage: nats.FileStorage,
	Replicas: 1,
	MaxAge: 168 * time.Hour,
}
```

- [ ] **Step 4: Verify green**

Run:

```bash
go test ./modules/cloudnode/internal/jobqueue -run TestEmbeddedJetStreamCreatesCloudNodeStreams
```

Expected: pass.

### Task 3: Split SQLite Projection From Queue Leasing

**Files:**
- Create: `modules/cloudnode/internal/projection/repository.go`
- Modify: `modules/cloudnode/schema/cloudnode.sql`
- Modify: `modules/cloudnode/internal/store/job_item.go`
- Test: `modules/cloudnode/internal/projection/repository_test.go`

- [ ] **Step 1: Write failing projection tests**

Tests must cover:

- creating a pending projection row
- marking a row running only if current status is pending
- marking a row canceled
- listing rows for management console
- listing attempts
- idempotent duplicate submit

Use an in-memory SQLite DB and the existing schema.

- [ ] **Step 2: Run test and verify red**

Run:

```bash
go test ./modules/cloudnode/internal/projection -run TestProjectionRepository
```

Expected: fail because `projection` package does not exist.

- [ ] **Step 3: Add projection schema columns**

Modify `t_cloud_job_items` in `modules/cloudnode/schema/cloudnode.sql`:

```sql
c_queue_subject TEXT NOT NULL DEFAULT '',
c_queue_msg_id TEXT NOT NULL DEFAULT '',
c_stream_seq INTEGER NOT NULL DEFAULT 0,
c_ack_subject TEXT NOT NULL DEFAULT '',
c_enqueue_status TEXT NOT NULL DEFAULT 'queued',
c_control_version INTEGER NOT NULL DEFAULT 0,
c_cancel_reason TEXT NOT NULL DEFAULT '',
```

Add indexes:

```sql
CREATE INDEX IF NOT EXISTS idx_cloud_job_items_enqueue ON t_cloud_job_items(c_space_id, c_enqueue_status, c_status, c_ctime);
CREATE INDEX IF NOT EXISTS idx_cloud_job_items_running ON t_cloud_job_items(c_space_id, c_status, c_running_node, c_recover_at);
```

Add `enqueue_failed` to repository constants and proto enum in a later task.

- [ ] **Step 4: Implement projection repository**

Create methods:

```go
type Repository struct { db *gorm.DB }

func (r *Repository) CreatePending(ctx context.Context, item *pb.JobItem, queueMeta QueueMeta) (*CreateResult, error)
func (r *Repository) MarkPublished(ctx context.Context, spaceID, jobItemID string, meta QueueMeta) error
func (r *Repository) MarkEnqueueFailed(ctx context.Context, spaceID, jobItemID string, message string) error
func (r *Repository) TryMarkRunning(ctx context.Context, req RunningRequest) (bool, error)
func (r *Repository) MarkReportedBatch(ctx context.Context, reports []ReportEvent) error
func (r *Repository) MarkCanceled(ctx context.Context, spaceID, jobItemID, reason string) error
func (r *Repository) Get(ctx context.Context, req *pb.GetJobItemReq) (*pb.JobItemDetail, error)
func (r *Repository) List(ctx context.Context, req *pb.ListJobItemsReq) ([]*pb.JobItemDetail, *pb.PageResult, error)
func (r *Repository) ListAttempts(ctx context.Context, req *pb.ListJobItemAttemptsReq) ([]*pb.JobItemAttempt, error)
```

- [ ] **Step 5: Verify green**

Run:

```bash
go test ./modules/cloudnode/internal/projection
```

Expected: pass.

### Task 4: Add JetStream Execution Queue

**Files:**
- Create: `modules/cloudnode/internal/jobqueue/payload.go`
- Create: `modules/cloudnode/internal/jobqueue/queue.go`
- Create: `modules/cloudnode/internal/jobqueue/jetstream_queue.go`
- Test: `modules/cloudnode/internal/jobqueue/jetstream_queue_test.go`

- [ ] **Step 1: Write failing queue tests**

Test with embedded JetStream:

- publish uses `Nats-Msg-Id = space_id + ":" + job_item_id`
- duplicate publish deduplicates
- fetch by `space_id + code_package_id + job_type`
- ack removes message from work queue
- nak redelivers message

- [ ] **Step 2: Run test and verify red**

Run:

```bash
go test ./modules/cloudnode/internal/jobqueue -run TestJetStreamQueue
```

Expected: fail because execution queue does not exist.

- [ ] **Step 3: Define queue payloads**

Create payloads:

```go
type JobItemMessage struct {
	SpaceID       string         `json:"space_id"`
	JobID         string         `json:"job_id"`
	JobItemID     string         `json:"job_item_id"`
	JobType       string         `json:"job_type"`
	CodePackageID string         `json:"code_package_id"`
	Params        map[string]any `json:"params"`
	Priority      int32          `json:"priority"`
	SubmittedAt   time.Time      `json:"submitted_at"`
}

type Delivery struct {
	Message    JobItemMessage
	AttemptNo  int
	AckSubject string
	StreamSeq  uint64
	ConsumerSeq uint64
}
```

Define interface:

```go
type ExecutionQueue interface {
	Publish(ctx context.Context, item *pb.JobItem) (*PublishResult, error)
	Fetch(ctx context.Context, req FetchRequest) ([]Delivery, error)
	Ack(ctx context.Context, ackSubject string) error
	Nak(ctx context.Context, ackSubject string, delay time.Duration) error
	Term(ctx context.Context, ackSubject string) error
	InProgress(ctx context.Context, ackSubject string) error
	Close() error
}
```

- [ ] **Step 4: Implement JetStream queue**

Consumer naming:

```text
durable = cn_exec_<space_token>_<package_token>_<job_type_token>
filter  = moox.cloudnode.exec.v1.jobitem.s.<space_token>.pkg.<package_token>.type.<job_type_token>
```

Poll with multiple `supported_job_types` fetches from each type-specific durable consumer until `limit` is filled.

Ack helpers publish JetStream ack protocol messages to `ack_subject`:

```text
+ACK  success
-NAK  retry
+TERM permanent failure or canceled
+WPI  in progress
```

Keep these helpers behind `ExecutionQueue` so RPC code never depends on ack protocol strings.

- [ ] **Step 5: Verify green**

Run:

```bash
go test ./modules/cloudnode/internal/jobqueue
```

Expected: pass.

### Task 5: Add Durable Projection Event Worker

**Files:**
- Create: `modules/cloudnode/internal/projection/projector.go`
- Create: `modules/cloudnode/internal/jobqueue/projection_events.go`
- Test: `modules/cloudnode/internal/projection/projector_test.go`

- [ ] **Step 1: Write failing projector tests**

Test that `jobitem.reported` events are consumed in batches and update SQLite attempts/results idempotently.

Batch policy:

```text
max_batch_size: 100
max_wait: 500ms
```

- [ ] **Step 2: Run test and verify red**

Run:

```bash
go test ./modules/cloudnode/internal/projection -run TestProjectorBatchesReportedEvents
```

Expected: fail because projector does not exist.

- [ ] **Step 3: Define projection events**

Create event types:

```go
type JobItemSubmittedEvent struct { SpaceID, JobID, JobItemID, JobType, CodePackageID string; Priority int32; Time time.Time }
type JobItemRunningEvent struct { SpaceID, JobItemID, NodeID, AckSubject string; AttemptNo int32; StreamSeq uint64; Time time.Time }
type JobItemReportedEvent struct { SpaceID, JobItemID, NodeID string; AttemptNo int32; Status string; ErrorKind string; ErrorCode string; ErrorMessage string; ResultSummary map[string]any; DurationMS int64; Time time.Time }
type JobItemCanceledEvent struct { SpaceID, JobItemID, Reason string; Time time.Time }
type NodeHeartbeatEvent struct { SpaceID, NodeID, NodeType, RunningVersion string; SupportedWorkloads []string; Metadata map[string]any; Time time.Time }
```

- [ ] **Step 4: Implement projector**

Projection worker:

- pull consumes `moox.cloudnode.projection.v1.jobitem.reported`
- batches by size or wait
- calls `Repository.MarkReportedBatch`
- ack only after SQLite write succeeds
- nak with delay when SQLite write fails

- [ ] **Step 5: Verify green**

Run:

```bash
go test ./modules/cloudnode/internal/projection
```

Expected: pass.

### Task 6: Add Latest-Wins Heartbeat Batcher

**Files:**
- Create: `modules/cloudnode/internal/projection/heartbeat_buffer.go`
- Modify: `modules/cloudnode/internal/rpc/node.go`
- Test: `modules/cloudnode/internal/projection/heartbeat_buffer_test.go`
- Test: `modules/cloudnode/internal/rpc/node_test.go`

- [ ] **Step 1: Write failing heartbeat tests**

Tests:

- multiple heartbeats for same `space_id + node_id` collapse to one latest record
- `ReportHeartbeat` returns success without waiting for SQLite
- flush writes `last_heartbeat_at` and supported workloads

- [ ] **Step 2: Run test and verify red**

Run:

```bash
go test ./modules/cloudnode/internal/projection ./modules/cloudnode/internal/rpc -run 'TestHeartbeat|TestReportHeartbeat'
```

Expected: fail because heartbeat buffer does not exist.

- [ ] **Step 3: Implement latest-wins buffer**

Interface:

```go
type HeartbeatSink interface {
	Enqueue(req *pb.ReportHeartbeatReq) error
	Flush(ctx context.Context) error
	Close(ctx context.Context) error
}
```

Runtime behavior:

```text
buffer size: 2048 node keys
flush interval: 1s
flush batch: all current keys
overflow policy: replace same key, reject new key with warning when full
```

- [ ] **Step 4: Verify green**

Run:

```bash
go test ./modules/cloudnode/internal/projection ./modules/cloudnode/internal/rpc -run 'TestHeartbeat|TestReportHeartbeat'
```

Expected: pass.

### Task 7: Orchestrate Submit/Poll/Report/Cancel Through JetStream

**Files:**
- Modify: `modules/cloudnode/internal/rpc/server.go`
- Modify: `modules/cloudnode/internal/rpc/job_item.go`
- Modify: `modules/cloudnode/internal/rpc/node.go`
- Test: `modules/cloudnode/internal/rpc/job_item_test.go`

- [ ] **Step 1: Write failing RPC orchestration tests**

Tests:

- `SubmitJobItems` creates projection row and publishes execution message
- `PollJobItems` fetches JetStream message, checks SQLite status, marks running, returns item
- canceled projection row is acked/termed and never returned to SCF
- `ReportJobItemStatus(SUCCESS)` publishes reported event and ack's queue message
- `ReportJobItemStatus(FAILED retryable)` nak's queue message and writes reported event
- `CancelJobItem` marks SQLite canceled and causes later heartbeat directive to include cancel

- [ ] **Step 2: Run test and verify red**

Run:

```bash
go test ./modules/cloudnode/internal/rpc -run 'TestSubmitJobItems|TestPollJobItems|TestReportJobItemStatus|TestCancelJobItem'
```

Expected: fail because service still uses old SQLite `QueueStore` for all lifecycle operations.

- [ ] **Step 3: Wire new components**

`Service` fields:

```go
executionQueue jobqueue.ExecutionQueue
projectionRepo *projection.Repository
projector      *projection.Projector
heartbeatSink  projection.HeartbeatSink
```

`SubmitJobItems` flow:

```text
validate item
insert pending projection row
publish to JetStream exec subject
mark projection row published with stream sequence and queue subject
publish projection submitted event
return CREATED / DEDUPLICATED / REJECTED per item
```

`PollJobItems` flow:

```text
load cloud node by node_id
fetch messages for node package and supported job types
for each delivery:
  if SQLite status is canceled/failed/success -> term/ack delivery and skip
  if SQLite status is pending -> mark running and return item
  if SQLite mark running fails -> nak with short delay and skip
```

`ReportJobItemStatus` flow:

```text
validate node_id/job_item_id/attempt_no
load running projection row
publish reported projection event
success/canceled/permanent failed -> term or ack delivery
retryable failed -> nak delivery with delay
return OK after JetStream command succeeds
```

`CancelJobItem` flow:

```text
mark SQLite projection canceled
publish projection canceled event
return OK
```

- [ ] **Step 4: Verify green**

Run:

```bash
go test ./modules/cloudnode/internal/rpc
```

Expected: pass.

### Task 8: Update Proto For Canceled Reports And Directives

**Files:**
- Modify: `modules/cloudnode/proto/cloudnode.proto`
- Regenerate: `modules/cloudnode/proto/cloudnodegen/*`
- Modify: `modules/collector/internal/reporter/heartbeat.go`
- Modify: `modules/collector/internal/serverless/handler.go`
- Test: `modules/cloudnode/internal/rpc/proto_contract_test.go`
- Test: `modules/collector/internal/serverless/handler_test.go`

- [ ] **Step 1: Write failing proto contract tests**

Expected protocol additions:

```protobuf
enum JobItemStatus {
  JOB_ITEM_STATUS_ENQUEUE_FAILED = 6;
}

enum JobItemReportStatus {
  JOB_ITEM_REPORT_STATUS_CANCELED = 3;
}

enum ControlDirectiveType {
  CONTROL_DIRECTIVE_UNSPECIFIED = 0;
  CONTROL_DIRECTIVE_CONTINUE = 1;
  CONTROL_DIRECTIVE_CANCEL = 2;
  CONTROL_DIRECTIVE_PAUSE_AFTER_CURRENT = 3;
  CONTROL_DIRECTIVE_REDUCE_RATE = 4;
}

message ControlDirective {
  ControlDirectiveType type = 1;
  string job_item_id = 2;
  int32 attempt_no = 3;
  string reason = 4;
}

message ReportHeartbeatRsp {
  common.RetInfo ret_info = 1;
  repeated ControlDirective directives = 2;
}
```

- [ ] **Step 2: Run test and verify red**

Run:

```bash
go test ./modules/cloudnode/internal/rpc -run TestCloudNodeProtoContract
```

Expected: fail because proto fields/enums do not exist.

- [ ] **Step 3: Modify proto and regenerate**

Run the repository's existing proto generation command. If the repo has no wrapper, run the same `protoc` command used by current generated files and document it in `modules/cloudnode/README.md`.

- [ ] **Step 4: Update collector runtime**

Collector runtime behavior:

- parse `ReportHeartbeatRsp.directives`
- if directive is `CANCEL` for current running job item and attempt, stop long-running work
- K-line short jobs may ignore late cancel when no matching running job exists

- [ ] **Step 5: Verify green**

Run:

```bash
go test ./modules/cloudnode/... ./modules/collector/internal/reporter ./modules/collector/internal/serverless
```

Expected: pass.

### Task 9: Remove SQLite Queue Leasing From Hot Path

**Files:**
- Modify: `modules/cloudnode/internal/store/job_item.go`
- Modify: `modules/cloudnode/internal/store/job_item_test.go`
- Modify: `modules/cloudnode/internal/rpc/server.go`
- Test: `modules/cloudnode/internal/store/job_item_test.go`

- [ ] **Step 1: Write failing guard test**

Add a test or compile-time assertion that RPC service does not call old `JobItemRepository.Poll` / `Report` for JetStream backend.

- [ ] **Step 2: Run test and verify red**

Run:

```bash
go test ./modules/cloudnode/internal/store ./modules/cloudnode/internal/rpc
```

Expected: fail until service is wired to `jobqueue.ExecutionQueue` and `projection.Repository`.

- [ ] **Step 3: Retain only projection-safe repository behavior**

Keep old SQLite methods only for:

- `Get`
- `List`
- `ListAttempts`
- projection helper methods moved to `modules/cloudnode/internal/projection`

Remove or stop using SQLite lease logic:

- recover expired running inside each poll
- pending select + running update loop
- synchronous report transaction as the main queue source

- [ ] **Step 4: Verify green**

Run:

```bash
go test ./modules/cloudnode/...
```

Expected: pass.

### Task 10: Add Queue Recovery And Reconciliation Workers

**Files:**
- Create: `modules/cloudnode/internal/jobqueue/reconciler.go`
- Test: `modules/cloudnode/internal/jobqueue/reconciler_test.go`

- [ ] **Step 1: Write failing reconciliation tests**

Cases:

- projection row is `pending` with `enqueue_failed`, republish to JetStream
- projection row is `running` but JetStream redelivers after ack wait, next poll can mark a new attempt
- duplicate reported event is ignored if attempt already terminal

- [ ] **Step 2: Run test and verify red**

Run:

```bash
go test ./modules/cloudnode/internal/jobqueue ./modules/cloudnode/internal/projection -run 'TestReconcile|TestDuplicate'
```

Expected: fail because reconciliation workers do not exist.

- [ ] **Step 3: Implement workers**

Workers:

```text
enqueue reconciler:
  every 10s, find enqueue_failed or unpublished pending projection rows
  publish to JetStream using Nats-Msg-Id
  mark published on success

running reconciler:
  every 30s, find running rows past recover_at
  mark attempt lost only when no fresh heartbeat/progress exists
  let JetStream redelivery create the next attempt
```

- [ ] **Step 4: Verify green**

Run:

```bash
go test ./modules/cloudnode/internal/jobqueue ./modules/cloudnode/internal/projection
```

Expected: pass.

### Task 11: Update Deployment And Reset Flow

**Files:**
- Modify: `scripts/deploy-moox.sh`
- Modify: `modules/cloudnode/config/app.yaml`
- Modify: `modules/cloudnode/README.md`
- Create: `examples/e2e/cloudnode_jetstream_queue.md`

- [ ] **Step 1: Write deployment checks**

Add documented checks:

```bash
ssh ubuntu@106.53.107.122 'ss -ltnp | grep 4223'
ssh ubuntu@106.53.107.122 '/home/ubuntu/moox/prod/status.sh'
curl -sS http://106.53.107.122:11000/api/admin/health
```

- [ ] **Step 2: Update deploy script**

Deploy script behavior:

- include `../data/cloudnode/nats` directory
- preserve NATS data unless `--reset-data` is passed
- stop cloudnode before replacing embedded NATS data
- start cloudnode before admin/web-host health probes

- [ ] **Step 3: Document reset for new-project deployments**

Document safe reset:

```bash
/home/ubuntu/moox/prod/stop.sh cloudnode
rm -rf /home/ubuntu/moox/prod/data/cloudnode/moox_cloudnode.db
rm -rf /home/ubuntu/moox/prod/data/cloudnode/nats
/home/ubuntu/moox/prod/start.sh cloudnode
```

This is allowed only when the user explicitly accepts rebuilding CloudNode queue and projection state.

- [ ] **Step 4: Verify deployment locally**

Run:

```bash
./scripts/deploy-moox.sh --target localhost --dir /tmp/moox-jetstream-plan --no-storage --no-collector --no-start
```

Expected: package includes `bin/moox-cloudnode`, `cloudnode/config/app.yaml`, and an empty `data/cloudnode` directory can be created on start.

### Task 12: End-To-End Validation

**Files:**
- Create: `examples/e2e/cloudnode_jetstream_queue.md`
- Optional Create: `examples/e2e/cloudnode_jetstream_queue.sh`

- [ ] **Step 1: Document local e2e**

Flow:

```text
start cloudnode with embedded JetStream
submit 3 JobItems
poll as fake SCF node
report 1 success, 1 retryable failure, cancel 1 pending item
poll again and verify retry redelivery
list JobItems and attempts from SQLite projection
```

- [ ] **Step 2: Add scriptable checks**

Checks:

```bash
go test ./modules/cloudnode/...
pnpm -C web build:prod
go test ./web-host/...
```

Remote checks after deployment:

```bash
ssh ubuntu@106.53.107.122 '/home/ubuntu/moox/prod/status.sh'
curl -sS --max-time 10 http://106.53.107.122:11000/api/admin/health
curl -sS -I --max-time 10 http://106.53.107.122:9527/ | head
```

- [ ] **Step 3: Verify management-console query path**

Use admin login token or browser session to verify:

```text
/#/collector/tasks shows pending/running/success/failed/canceled JobItems from SQLite projection.
/#/collector/cloudnodes shows last heartbeat without SQLite lock storms.
```

### Task 13: Final Cleanup And Observability

**Files:**
- Modify: `modules/cloudnode/internal/rpc/job_item.go`
- Modify: `modules/cloudnode/internal/projection/projector.go`
- Modify: `modules/cloudnode/README.md`
- Modify: `docs/云节点执行平台架构.md`

- [ ] **Step 1: Add metrics/log fields**

Every queue path log includes:

```text
space_id
job_item_id
job_type
code_package_id
node_id
attempt_no
stream
subject
consumer
stream_seq
```

Counters:

```text
cloudnode.jobitem.submit.created
cloudnode.jobitem.submit.deduplicated
cloudnode.jobitem.poll.delivered
cloudnode.jobitem.poll.filtered_canceled
cloudnode.jobitem.report.success
cloudnode.jobitem.report.retryable_failed
cloudnode.jobitem.report.permanent_failed
cloudnode.projector.batch.size
cloudnode.heartbeat.flush.size
```

- [ ] **Step 2: Update architecture docs**

Document the new truth model:

```text
JetStream: execution queue truth
SQLite: control/query projection truth
CloudNode: sole JetStream owner
SCF: RPC-only runtime
```

- [ ] **Step 3: Run final verification**

Run:

```bash
go test ./modules/cloudnode/...
go test ./modules/collector/internal/reporter ./modules/collector/internal/serverless
pnpm -C web build:prod
go test ./web-host/...
```

Expected: all commands exit 0. Existing frontend warnings about Browserslist/Sass/lottie/chunk size may remain.

---

## Implementation Order

1. Naming and config.
2. JetStream runtime.
3. SQLite projection repository.
4. JetStream execution queue.
5. Projection and heartbeat batch workers.
6. RPC orchestration.
7. Proto/directive update.
8. Remove SQLite queue leasing from hot path.
9. Reconciliation workers.
10. Deployment and e2e docs.

This order keeps each step testable and prevents a half-migrated state where both SQLite and JetStream believe they own the execution queue.

## Self-Review

- Subject names are CloudNode-specific and cannot collide with storage data-change subjects.
- SCF does not import NATS concepts or connect to NATS.
- SQLite remains available for management-console query, cancel, retry, and scheduled adjustment workflows.
- JetStream handles high-concurrency execution queue pressure.
- Cancellation is eventually observed through Poll/Heartbeat/Progress, with SQLite as control fact.
- The plan includes tests for naming, queue behavior, projection batch writes, heartbeat batching, RPC orchestration, deployment, and e2e validation.
