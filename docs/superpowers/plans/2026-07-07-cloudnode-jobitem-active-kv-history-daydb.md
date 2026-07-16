# CloudNode JobItem Active KV History DayDB Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Replace `t_cloud_job_items` and `t_cloud_job_item_attempts` as online state tables with a 48-hour NATS JetStream KV active store, while keeping SQLite only as terminal JobItem history day databases.

**Architecture:** JetStream remains the execution queue source of truth for delivery, ACK, NAK, TERM, and redelivery. A JetStream KV bucket stores the current JobItem state for 48 hours with CAS updates; terminal states are best-effort persisted into `data/cloudnode/jobs/YYYYMMDD.db`. The management console continues to query only active data and does not add historical query UI in this change.

**Tech Stack:** Go, nats.go JetStream `KeyValue`, SQLite/GORM, existing CloudNode RPC service, existing embedded NATS runtime, existing `go test` suite.

---

## Scope And Decisions

- Active state uses a single TTL policy: every active KV entry expires 48 hours after its last update. Do not split terminal and non-terminal TTL.
- NATS KV means JetStream Key/Value buckets, not plain NATS pub/sub. It is backed by JetStream streams named `KV_<bucket>`, supports `Put`, `Get`, `Create`, `Update`, `Delete`, key listing, TTL, history, and revision-based CAS.
- Management console reads only active KV data. No historical JobItem UI is added.
- SQLite day DBs store terminal snapshots for local audit and short recent troubleshooting. SCF execution logs remain stdout logs and are expected to be collected by CLS.
- A tRPC framework timer runs once per day. On day `D`, it creates future day DBs `D+1` and `D+2`, then deletes the historical DBs for `D-2` and `D-3`.
- Old main-db JobItem tables are removed from the online schema. Cutover does not preserve in-flight legacy SQLite JobItems; any JetStream delivery without an active KV record is treated as an expired orphan and TERM'ed.

## File Structure

### New Files

- `modules/cloudnode/internal/jobstate/types.go`
  - Owns active JobItem state structs, status constants, error variables, and protobuf conversion helpers.
- `modules/cloudnode/internal/jobstate/key.go`
  - Encodes KV keys with URL-safe base64 segments: `job.<space>.<job_item_id>`.
- `modules/cloudnode/internal/jobstate/kv_store.go`
  - Implements `Store` using `nats.KeyValue`, TTL-friendly JSON values, and revision CAS.
- `modules/cloudnode/internal/jobstate/kv_store_test.go`
  - Covers create/dedup, CAS running transition, retryable/permanent reports, cancel directives, list filtering, and orphan behavior.
- `modules/cloudnode/internal/jobhistory/schema.go`
  - Contains day-db schema for `t_cloud_job_items` and `t_cloud_job_item_attempts`.
- `modules/cloudnode/internal/jobhistory/store.go`
  - Opens day DBs by finish day, upserts terminal snapshots, and prunes old day DBs.
- `modules/cloudnode/internal/jobhistory/store_test.go`
  - Verifies day DB creation, terminal upsert, attempts persistence, and retention cleanup.
- `modules/cloudnode/internal/jobhistory/maintenance.go`
  - Owns daily day-db preparation: create `D+1`/`D+2`, delete `D-2`/`D-3`.
- `modules/cloudnode/internal/jobhistory/maintenance_test.go`
  - Verifies the daily maintenance date math and file operations.
- `modules/cloudnode/internal/rpc/job_history_schedule.go`
  - tRPC timer handler entry for daily jobs DB maintenance.
- `modules/cloudnode/internal/rpc/job_history_schedule_test.go`
  - Verifies handler delegates to the configured history store.

### Modified Files

- `modules/cloudnode/internal/config/config.go`
  - Add active KV and history settings.
- `modules/cloudnode/config/app.yaml`
  - Add default `active_kv_bucket`, `active_ttl_hours: 48`, `history_dir`, `history_retention_days`.
- `modules/cloudnode/internal/jobqueue/jetstream_client.go`
  - Ensure the active KV bucket exists alongside the execution stream.
  - Remove or stop requiring the old projection stream for JobItem state.
- `modules/cloudnode/internal/bootstrap/bootstrap.go`
  - Wire `jobstate.Store` and `jobhistory.Store` into the RPC service.
  - Stop starting JobItem projection/reconcile workers for active state.
  - Register the daily tRPC timer handler for jobs day DB maintenance.
- `modules/cloudnode/internal/rpc/server.go`
  - Replace `projectionRepo`/`projector` JobItem state dependencies with `jobstate.Store` and `jobhistory.Store`.
- `modules/cloudnode/config/trpc_go.yaml`
  - Add `trpc.moox.cloudnode.jobhistory.timer`, scheduled once per day.
- `modules/cloudnode/go.mod`
  - Add `trpc.group/trpc-go/trpc-database/timer` for the tRPC timer plugin.
- `modules/cloudnode/go.sum`
  - Add timer dependency checksums after `go mod tidy`.
- `modules/cloudnode/internal/rpc/job_item.go`
  - Switch Submit/Poll/Report/Cancel/Get/List/ListAttempts to active KV and day history.
- `modules/cloudnode/internal/rpc/node.go`
  - Read heartbeat cancel directives from active KV instead of SQLite projection.
- `modules/cloudnode/schema/cloudnode.sql`
  - Drop legacy online tables `t_cloud_job_items` and `t_cloud_job_item_attempts`.
  - Remove their online CREATE statements, indexes, and triggers.
- `modules/cloudnode/schema/schema_test.go`
  - Assert legacy online JobItem tables are dropped and not recreated.
- `modules/cloudnode/cmd/cli/init_schema_test.go`
  - Assert init removes old online JobItem tables from the main cloudnode DB.
- `modules/cloudnode/internal/rpc/job_item_test.go`
  - Update RPC integration tests to use active KV and day history.
- `modules/cloudnode/internal/rpc/node_heartbeat_test.go`
  - Update cancel directive tests to use active KV.
- `modules/cloudnode/internal/projection/*`
  - Delete or shrink JobItem-specific projection/reconciler code; keep heartbeat buffering only if still useful for node catalog batching.
- `modules/cloudnode/internal/jobqueue/naming.go`
  - Remove projection event naming if no longer used.
- `modules/cloudnode/internal/jobqueue/*_test.go`
  - Update stream tests to assert execution stream and KV bucket, not projection stream.
- `modules/cloudnode/README.md`
  - Document active KV, 48h TTL, day DB history, and no historical console query.
- `docs/云节点执行平台架构.md`
  - Update the architecture diagram and lifecycle description.
- `docs/云节点管理.md`
  - Update storage responsibilities and table list.
- `scripts/deploy-moox.sh`
  - Ensure `data/cloudnode/jobs` exists in deployment runtime directories.

---

### Task 1: Add Active KV And History Config

**Files:**
- Modify: `modules/cloudnode/internal/config/config.go`
- Modify: `modules/cloudnode/config/app.yaml`
- Test: `modules/cloudnode/internal/config/config_test.go`

- [ ] **Step 1: Write failing config test**

Add a test that verifies default settings:

```go
func TestDefaultJobItemActiveKVAndHistoryConfig(t *testing.T) {
	cfg := Default()
	if cfg.JobItem.ActiveKVBucket != "MOOX_CLOUDNODE_JOB_ACTIVE" {
		t.Fatalf("ActiveKVBucket = %q", cfg.JobItem.ActiveKVBucket)
	}
	if cfg.JobItem.ActiveTTLHours != 48 {
		t.Fatalf("ActiveTTLHours = %d, want 48", cfg.JobItem.ActiveTTLHours)
	}
	if cfg.JobItem.HistoryDir != "../data/cloudnode/jobs" {
		t.Fatalf("HistoryDir = %q", cfg.JobItem.HistoryDir)
	}
	if cfg.JobItem.HistoryRetentionDays != 2 {
		t.Fatalf("HistoryRetentionDays = %d, want 2", cfg.JobItem.HistoryRetentionDays)
	}
}
```

- [ ] **Step 2: Run config test and verify it fails**

Run:

```bash
go test ./modules/cloudnode/internal/config -run TestDefaultJobItemActiveKVAndHistoryConfig -count=1
```

Expected: compile failure or assertion failure because the new fields do not exist yet.

- [ ] **Step 3: Add config fields**

Extend `JobItemConfig`:

```go
type JobItemConfig struct {
	DefaultLimit         int    `yaml:"default_limit"`
	MaxLimit             int    `yaml:"max_limit"`
	RecoverAfterMillis   int64  `yaml:"recover_after_millis"`
	DefaultMaxAttempts   int    `yaml:"default_max_attempts"`
	ActiveKVBucket       string `yaml:"active_kv_bucket"`
	ActiveTTLHours       int    `yaml:"active_ttl_hours"`
	HistoryDir           string `yaml:"history_dir"`
	HistoryRetentionDays int    `yaml:"history_retention_days"`
}
```

Set defaults:

```go
JobItem: JobItemConfig{
	DefaultLimit:         10,
	MaxLimit:             100,
	RecoverAfterMillis:   int64(10 * time.Minute / time.Millisecond),
	DefaultMaxAttempts:   3,
	ActiveKVBucket:       "MOOX_CLOUDNODE_JOB_ACTIVE",
	ActiveTTLHours:       48,
	HistoryDir:           "../data/cloudnode/jobs",
	HistoryRetentionDays: 2,
},
```

Update `modules/cloudnode/config/app.yaml`:

```yaml
job_item:
  default_limit: 10
  max_limit: 100
  recover_after_millis: 600000
  default_max_attempts: 3
  active_kv_bucket: MOOX_CLOUDNODE_JOB_ACTIVE
  active_ttl_hours: 48
  history_dir: ../data/cloudnode/jobs
  history_retention_days: 2
```

- [ ] **Step 4: Run config test and verify it passes**

Run:

```bash
go test ./modules/cloudnode/internal/config -run TestDefaultJobItemActiveKVAndHistoryConfig -count=1
```

Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add modules/cloudnode/internal/config/config.go modules/cloudnode/internal/config/config_test.go modules/cloudnode/config/app.yaml
git commit -m "feat(cloudnode): add job item active kv config"
```

---

### Task 2: Ensure JetStream KV Bucket

**Files:**
- Modify: `modules/cloudnode/internal/jobqueue/jetstream_client.go`
- Modify: `modules/cloudnode/internal/jobqueue/jetstream_client_test.go`

- [ ] **Step 1: Write failing KV bucket test**

Add a test beside the existing stream tests:

```go
func TestEnsureStreamsCreatesActiveKVBucket(t *testing.T) {
	ctx := context.Background()
	rt := startTestRuntime(t, ctx)
	cfg := config.Default().JetStream
	jobCfg := config.Default().JobItem
	if err := rt.EnsureStreams(cfg, jobCfg); err != nil {
		t.Fatalf("EnsureStreams() error = %v", err)
	}
	kv, err := rt.JetStream().KeyValue(jobCfg.ActiveKVBucket)
	if err != nil {
		t.Fatalf("active kv bucket missing: %v", err)
	}
	status, err := kv.Status()
	if err != nil {
		t.Fatalf("kv status: %v", err)
	}
	if got, want := status.TTL(), 48*time.Hour; got != want {
		t.Fatalf("ttl = %v, want %v", got, want)
	}
}
```

If `nats.KeyValueStatus` in the current client version does not expose `TTL()`, assert through the backing stream:

```go
info, err := rt.JetStream().StreamInfo("KV_" + jobCfg.ActiveKVBucket)
if err != nil {
	t.Fatalf("kv backing stream missing: %v", err)
}
if got, want := info.Config.MaxAge, 48*time.Hour; got != want {
	t.Fatalf("MaxAge = %v, want %v", got, want)
}
```

- [ ] **Step 2: Run test and verify it fails**

Run:

```bash
go test ./modules/cloudnode/internal/jobqueue -run TestEnsureStreamsCreatesActiveKVBucket -count=1
```

Expected: compile failure because `EnsureStreams` still only accepts `JetStreamConfig`, or runtime failure because the KV bucket is not created.

- [ ] **Step 3: Update EnsureStreams signature**

Change:

```go
func (r *Runtime) EnsureStreams(cfg config.JetStreamConfig) error
```

to:

```go
func (r *Runtime) EnsureStreams(cfg config.JetStreamConfig, jobCfg config.JobItemConfig) error
```

Inside it, after ensuring the execution stream, ensure the KV bucket:

```go
ttl := time.Duration(jobCfg.ActiveTTLHours) * time.Hour
if ttl <= 0 {
	ttl = 48 * time.Hour
}
bucket := strings.TrimSpace(jobCfg.ActiveKVBucket)
if bucket == "" {
	bucket = "MOOX_CLOUDNODE_JOB_ACTIVE"
}
if _, err := r.js.KeyValue(bucket); err != nil {
	if errors.Is(err, nats.ErrBucketNotFound) || strings.Contains(err.Error(), "bucket not found") {
		_, addErr := r.js.CreateKeyValue(&nats.KeyValueConfig{
			Bucket:  bucket,
			Storage: nats.FileStorage,
			History: 1,
			TTL:     ttl,
		})
		if addErr != nil {
			return fmt.Errorf("create active job kv bucket: %w", addErr)
		}
	} else {
		return fmt.Errorf("open active job kv bucket: %w", err)
	}
}
```

Keep the execution stream. Stop creating the old projection stream in this task only if no remaining tests depend on it; otherwise remove it in Task 9.

- [ ] **Step 4: Update all call sites**

Update calls in:

```go
rt.EnsureStreams(cfg.JetStream, cfg.JobItem)
```

and tests that call `EnsureStreams`.

- [ ] **Step 5: Run jobqueue tests**

Run:

```bash
go test ./modules/cloudnode/internal/jobqueue -count=1
```

Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add modules/cloudnode/internal/jobqueue/jetstream_client.go modules/cloudnode/internal/jobqueue/jetstream_client_test.go
git commit -m "feat(cloudnode): ensure job item active kv bucket"
```

---

### Task 3: Create JobItem State Types And KV Key Encoding

**Files:**
- Create: `modules/cloudnode/internal/jobstate/types.go`
- Create: `modules/cloudnode/internal/jobstate/key.go`
- Create: `modules/cloudnode/internal/jobstate/key_test.go`

- [ ] **Step 1: Write key encoding tests**

```go
func TestJobKeyRoundTrip(t *testing.T) {
	key := JobKey("crypto", "collector:kline:BTC/USDT:1m")
	if !strings.HasPrefix(key, "job.") {
		t.Fatalf("key = %q, want job prefix", key)
	}
	spaceID, jobItemID, ok := ParseJobKey(key)
	if !ok {
		t.Fatalf("ParseJobKey(%q) failed", key)
	}
	if spaceID != "crypto" || jobItemID != "collector:kline:BTC/USDT:1m" {
		t.Fatalf("decoded = %q %q", spaceID, jobItemID)
	}
}

func TestSpacePrefix(t *testing.T) {
	prefix := SpacePrefix("crypto")
	if !strings.HasPrefix(prefix, "job.") || !strings.HasSuffix(prefix, ".") {
		t.Fatalf("prefix = %q", prefix)
	}
}
```

- [ ] **Step 2: Run key tests and verify they fail**

Run:

```bash
go test ./modules/cloudnode/internal/jobstate -run 'TestJobKey|TestSpacePrefix' -count=1
```

Expected: package does not exist.

- [ ] **Step 3: Add key helpers**

`key.go`:

```go
package jobstate

import (
	"encoding/base64"
	"strings"
)

func encodeSegment(value string) string {
	return base64.RawURLEncoding.EncodeToString([]byte(value))
}

func decodeSegment(value string) (string, bool) {
	raw, err := base64.RawURLEncoding.DecodeString(value)
	if err != nil {
		return "", false
	}
	return string(raw), true
}

func JobKey(spaceID, jobItemID string) string {
	return "job." + encodeSegment(strings.TrimSpace(spaceID)) + "." + encodeSegment(strings.TrimSpace(jobItemID))
}

func SpacePrefix(spaceID string) string {
	return "job." + encodeSegment(strings.TrimSpace(spaceID)) + "."
}

func ParseJobKey(key string) (spaceID string, jobItemID string, ok bool) {
	parts := strings.Split(key, ".")
	if len(parts) != 3 || parts[0] != "job" {
		return "", "", false
	}
	spaceID, ok = decodeSegment(parts[1])
	if !ok {
		return "", "", false
	}
	jobItemID, ok = decodeSegment(parts[2])
	if !ok {
		return "", "", false
	}
	return spaceID, jobItemID, true
}
```

- [ ] **Step 4: Add state types**

`types.go`:

```go
package jobstate

import (
	"errors"
	"time"

	pb "github.com/mooyang-code/moox/modules/cloudnode/proto/cloudnodegen"
	"google.golang.org/protobuf/types/known/structpb"
	"google.golang.org/protobuf/types/known/timestamppb"
)

const (
	StatusPending       = "pending"
	StatusRunning       = "running"
	StatusSuccess       = "success"
	StatusFailed        = "failed"
	StatusCanceled      = "canceled"
	StatusEnqueueFailed = "enqueue_failed"

	AttemptRunning  = "running"
	AttemptSuccess  = "success"
	AttemptFailed   = "failed"
	AttemptLost     = "lost"
	AttemptCanceled = "canceled"

	ErrorRetryable = "retryable"
	ErrorPermanent = "permanent"
)

var (
	ErrConflict     = errors.New("job item state conflict")
	ErrStaleAttempt = errors.New("stale job item attempt")
	ErrInactive     = errors.New("job item is not running")
	ErrInvalid      = errors.New("invalid job item")
	ErrNotFound     = errors.New("job item not found")
)

type QueueMeta struct {
	Subject    string `json:"subject,omitempty"`
	Stream     string `json:"stream,omitempty"`
	StreamSeq  uint64 `json:"stream_seq,omitempty"`
	AckSubject string `json:"ack_subject,omitempty"`
}

type Attempt struct {
	AttemptNo     int            `json:"attempt_no"`
	NodeID        string         `json:"node_id"`
	Status        string         `json:"status"`
	ErrorKind     string         `json:"error_kind,omitempty"`
	ErrorCode     string         `json:"error_code,omitempty"`
	ErrorMessage  string         `json:"error_message,omitempty"`
	ResultSummary map[string]any `json:"result_summary,omitempty"`
	StartedAt     time.Time      `json:"started_at"`
	FinishedAt    *time.Time     `json:"finished_at,omitempty"`
}

type State struct {
	SchemaVersion    int            `json:"schema_version"`
	SpaceID          string         `json:"space_id"`
	JobID            string         `json:"job_id"`
	JobItemID        string         `json:"job_item_id"`
	JobType          string         `json:"job_type"`
	CodePackageID    string         `json:"code_package_id"`
	Params           map[string]any `json:"params,omitempty"`
	Priority         int32          `json:"priority"`
	Status           string         `json:"status"`
	RunningNode      string         `json:"running_node,omitempty"`
	AttemptNo        int            `json:"attempt_no"`
	RecoverAt        *time.Time     `json:"recover_at,omitempty"`
	Queue            QueueMeta      `json:"queue,omitempty"`
	ResultSummary    map[string]any `json:"result_summary,omitempty"`
	LastErrorKind    string         `json:"last_error_kind,omitempty"`
	LastErrorCode    string         `json:"last_error_code,omitempty"`
	LastErrorMessage string         `json:"last_error_message,omitempty"`
	CancelReason     string         `json:"cancel_reason,omitempty"`
	HistorySynced    bool           `json:"history_synced,omitempty"`
	StartedAt        *time.Time     `json:"started_at,omitempty"`
	FinishedAt       *time.Time     `json:"finished_at,omitempty"`
	CreatedAt        time.Time      `json:"created_at"`
	UpdatedAt        time.Time      `json:"updated_at"`
	Attempts         []Attempt      `json:"attempts,omitempty"`
}

type CreateResult struct {
	JobItemID    string
	Status       pb.JobItemAckStatus
	RejectReason string
	Created      bool
	Deduplicated bool
}

type RunningRequest struct {
	SpaceID    string
	JobItemID  string
	NodeID     string
	AckSubject string
	StreamSeq  uint64
}

type RunningState struct {
	AttemptNo  int
	AckSubject string
	RecoverAt  time.Time
}

type ReportEvent struct {
	SpaceID       string
	JobItemID     string
	NodeID        string
	AttemptNo     int32
	Status        string
	ErrorKind     string
	ErrorCode     string
	ErrorMessage  string
	ResultSummary map[string]any
	DurationMS    int64
	Time          time.Time
}
```

Include helper methods in the same file:

```go
func (s State) IsTerminal() bool {
	return s.Status == StatusSuccess || s.Status == StatusFailed || s.Status == StatusCanceled
}

func (s State) ToDetail() *pb.JobItemDetail {
	params, _ := structpb.NewStruct(s.Params)
	result, _ := structpb.NewStruct(s.ResultSummary)
	return &pb.JobItemDetail{
		SpaceId:          s.SpaceID,
		JobId:            s.JobID,
		JobItemId:        s.JobItemID,
		JobType:          s.JobType,
		CodePackageId:    s.CodePackageID,
		Params:           params,
		Priority:         s.Priority,
		Status:           statusToPB(s.Status),
		RunningNode:      s.RunningNode,
		AttemptNo:        int32(s.AttemptNo),
		ResultSummary:    result,
		LastErrorKind:    errorKindToPB(s.LastErrorKind),
		LastErrorCode:    s.LastErrorCode,
		LastErrorMessage: s.LastErrorMessage,
		RecoverAt:        timePtrToPB(s.RecoverAt),
		StartTime:        timePtrToPB(s.StartedAt),
		FinishTime:       timePtrToPB(s.FinishedAt),
		CreateTime:       timestamppb.New(s.CreatedAt),
	}
}
```

Implement `statusToPB`, `errorKindToPB`, and `timePtrToPB` with the existing projection mapping semantics.

- [ ] **Step 5: Run jobstate tests**

Run:

```bash
go test ./modules/cloudnode/internal/jobstate -count=1
```

Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add modules/cloudnode/internal/jobstate
git commit -m "feat(cloudnode): add job item state model"
```

---

### Task 4: Implement NATS KV Active Store

**Files:**
- Create: `modules/cloudnode/internal/jobstate/kv_store.go`
- Create: `modules/cloudnode/internal/jobstate/kv_store_test.go`

- [ ] **Step 1: Write failing create/dedup test**

```go
func TestKVStoreCreatePendingDeduplicates(t *testing.T) {
	ctx := context.Background()
	store := newTestKVStore(t, 48*time.Hour)
	item := testJobItem("crypto", "ji-1")

	first, err := store.CreatePending(ctx, item, QueueMeta{})
	if err != nil {
		t.Fatalf("CreatePending first error = %v", err)
	}
	if !first.Created || first.Status != pb.JobItemAckStatus_JOB_ITEM_ACK_STATUS_CREATED {
		t.Fatalf("first = %+v", first)
	}

	second, err := store.CreatePending(ctx, item, QueueMeta{})
	if err != nil {
		t.Fatalf("CreatePending second error = %v", err)
	}
	if !second.Deduplicated || second.Status != pb.JobItemAckStatus_JOB_ITEM_ACK_STATUS_DEDUPLICATED {
		t.Fatalf("second = %+v", second)
	}
}
```

- [ ] **Step 2: Write failing state transition test**

```go
func TestKVStoreRunningAndTerminalReport(t *testing.T) {
	ctx := context.Background()
	store := newTestKVStore(t, 48*time.Hour)
	_, err := store.CreatePending(ctx, testJobItem("crypto", "ji-2"), QueueMeta{Subject: "sub"})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.MarkPublished(ctx, "crypto", "ji-2", QueueMeta{Subject: "sub", Stream: "EXEC", StreamSeq: 42}); err != nil {
		t.Fatal(err)
	}
	ok, running, err := store.TryMarkRunning(ctx, RunningRequest{
		SpaceID: "crypto", JobItemID: "ji-2", NodeID: "node-1", AckSubject: "ack", StreamSeq: 42,
	})
	if err != nil || !ok {
		t.Fatalf("TryMarkRunning ok=%v state=%+v err=%v", ok, running, err)
	}
	if running.AttemptNo != 1 {
		t.Fatalf("attempt = %d, want 1", running.AttemptNo)
	}
	updated, err := store.MarkReported(ctx, ReportEvent{
		SpaceID: "crypto", JobItemID: "ji-2", NodeID: "node-1", AttemptNo: 1,
		Status: StatusSuccess, ResultSummary: map[string]any{"rows": float64(3)}, Time: time.Now().UTC(),
	})
	if err != nil {
		t.Fatalf("MarkReported error = %v", err)
	}
	if updated.Status != StatusSuccess || !updated.IsTerminal() || len(updated.Attempts) != 1 {
		t.Fatalf("updated = %+v", updated)
	}
}
```

- [ ] **Step 3: Run tests and verify they fail**

Run:

```bash
go test ./modules/cloudnode/internal/jobstate -run 'TestKVStore' -count=1
```

Expected: compile failure because `KVStore` is not implemented.

- [ ] **Step 4: Define Store interface**

In `kv_store.go`:

```go
type Store interface {
	CreatePending(ctx context.Context, item *pb.JobItem, meta QueueMeta) (*CreateResult, error)
	MarkPublished(ctx context.Context, spaceID, jobItemID string, meta QueueMeta) error
	MarkEnqueueFailed(ctx context.Context, spaceID, jobItemID string, message string) error
	Get(ctx context.Context, spaceID, jobItemID string) (*State, error)
	TryMarkRunning(ctx context.Context, req RunningRequest) (bool, RunningState, error)
	MarkCanceled(ctx context.Context, spaceID, jobItemID, reason string) error
	ClearCancelDirective(ctx context.Context, spaceID, jobItemID string, attemptNo int32) error
	MarkReported(ctx context.Context, event ReportEvent) (*State, error)
	List(ctx context.Context, req *pb.ListJobItemsReq) ([]*pb.JobItemDetail, *pb.PageResult, error)
	ListAttempts(ctx context.Context, req *pb.ListJobItemAttemptsReq) ([]*pb.JobItemAttempt, error)
	ListCancelDirectives(ctx context.Context, spaceID, nodeID string, limit int) ([]*pb.ControlDirective, error)
}
```

- [ ] **Step 5: Implement KVStore JSON and CAS loop**

Use:

```go
type KVStore struct {
	kv                   nats.KeyValue
	clock                Clock
	recoverAfterMillis    int64
	defaultMaxAttempts    int
	maxCASRetries         int
}
```

Implement `withStateCAS(ctx, key, fn)`:

```go
func (s *KVStore) withStateCAS(ctx context.Context, key string, mutate func(State) (State, bool, error)) (State, bool, error) {
	retries := s.maxCASRetries
	if retries <= 0 {
		retries = 5
	}
	for i := 0; i < retries; i++ {
		entry, err := s.kv.Get(key, nats.Context(ctx))
		if err != nil {
			return State{}, false, mapKVError(err)
		}
		state, err := decodeState(entry.Value())
		if err != nil {
			return State{}, false, err
		}
		next, changed, err := mutate(state)
		if err != nil || !changed {
			return next, changed, err
		}
		next.UpdatedAt = s.now()
		raw, err := encodeState(next)
		if err != nil {
			return State{}, false, err
		}
		if _, err := s.kv.Update(key, raw, entry.Revision(), nats.Context(ctx)); err == nil {
			return next, true, nil
		}
	}
	return State{}, false, ErrConflict
}
```

Use `Create` for `CreatePending` so idempotency is atomic. Treat `nats.ErrKeyExists` as `DEDUPLICATED`.

- [ ] **Step 6: Implement transition rules**

Rules:

- `TryMarkRunning`
  - `pending` or `enqueue_failed`: mark `running`, increment `attempt_no`, set `running_node`, `ack_subject`, `stream_seq`, `recover_at`, append running attempt.
  - `running` with expired `recover_at`: mark prior attempt `lost`, then mark new running attempt.
  - terminal/canceled: return `ok=false`.
  - attempts exceeding `default_max_attempts`: mark failed with `MAX_ATTEMPTS_EXCEEDED`, return `ok=false`.
- `MarkReported`
  - Require current state `running`.
  - Require matching `node_id` and `attempt_no`.
  - success: state `success`, attempt `success`, set `finished_at`.
  - failed retryable with attempts left: state `pending`, attempt `failed`, clear running node and ack subject.
  - failed permanent or max attempts reached: state `failed`, attempt `failed`, set last error fields.
  - canceled: state `canceled`, attempt `canceled`.
- `MarkCanceled`
  - `pending`, `running`, `enqueue_failed`: mark `canceled`, set `cancel_reason`.
  - terminal: no-op success.
- `ClearCancelDirective`
  - Clear `cancel_reason` only when `attempt_no` matches.

- [ ] **Step 7: Implement active list**

`List` can scan current keys because active TTL is only 48 hours:

```go
keys, err := s.kv.Keys(nats.Context(ctx))
```

Filter keys by `SpacePrefix(req.GetSpaceId())`, decode states, filter status/job_type/job_id, sort by `UpdatedAt` descending, then page using `req.Page`.

- [ ] **Step 8: Implement attempt list and cancel directives**

`ListAttempts` reads one state and converts `Attempts`.

`ListCancelDirectives` scans active states where:

```go
state.Status == StatusCanceled &&
state.RunningNode == nodeID &&
state.AttemptNo > 0 &&
state.CancelReason != ""
```

Return at most `limit` directives.

- [ ] **Step 9: Run jobstate tests**

Run:

```bash
go test ./modules/cloudnode/internal/jobstate -count=1
```

Expected: PASS.

- [ ] **Step 10: Commit**

```bash
git add modules/cloudnode/internal/jobstate
git commit -m "feat(cloudnode): add nats kv job item state store"
```

---

### Task 5: Implement SQLite Day History Store

**Files:**
- Create: `modules/cloudnode/internal/jobhistory/schema.go`
- Create: `modules/cloudnode/internal/jobhistory/store.go`
- Create: `modules/cloudnode/internal/jobhistory/store_test.go`

- [ ] **Step 1: Write failing day DB test**

```go
func TestStoreWritesTerminalStateToDayDB(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	store := NewStore(StoreOptions{Dir: dir, RetentionDays: 2})
	finished := time.Date(2026, 7, 7, 12, 0, 0, 0, time.UTC)
	state := jobstate.State{
		SpaceID: "crypto", JobID: "job-1", JobItemID: "ji-1", JobType: "collector.kline",
		CodePackageID: "pkg-1", Status: jobstate.StatusSuccess,
		ResultSummary: map[string]any{"rows": float64(3)},
		CreatedAt: finished.Add(-time.Minute), UpdatedAt: finished, FinishedAt: &finished,
		Attempts: []jobstate.Attempt{{
			AttemptNo: 1, NodeID: "node-1", Status: jobstate.AttemptSuccess,
			StartedAt: finished.Add(-time.Second), FinishedAt: &finished,
		}},
	}
	if err := store.WriteTerminal(ctx, state); err != nil {
		t.Fatalf("WriteTerminal() error = %v", err)
	}
	dbPath := filepath.Join(dir, "20260707.db")
	if _, err := os.Stat(dbPath); err != nil {
		t.Fatalf("day db missing: %v", err)
	}
}
```

- [ ] **Step 2: Run test and verify it fails**

Run:

```bash
go test ./modules/cloudnode/internal/jobhistory -run TestStoreWritesTerminalStateToDayDB -count=1
```

Expected: package does not exist.

- [ ] **Step 3: Add day DB schema**

Use the legacy table names only inside day DB files:

```go
const SchemaSQL = `
CREATE TABLE IF NOT EXISTS t_cloud_job_items (
    c_space_id TEXT NOT NULL DEFAULT '',
    c_job_id TEXT NOT NULL DEFAULT '',
    c_job_item_id TEXT NOT NULL,
    c_job_type TEXT NOT NULL DEFAULT '',
    c_code_package_id TEXT NOT NULL DEFAULT '',
    c_params TEXT NOT NULL DEFAULT '{}',
    c_priority INTEGER NOT NULL DEFAULT 0,
    c_status TEXT NOT NULL DEFAULT '',
    c_running_node TEXT NOT NULL DEFAULT '',
    c_attempt_no INTEGER NOT NULL DEFAULT 0,
    c_result_summary TEXT NOT NULL DEFAULT '{}',
    c_last_error_kind TEXT NOT NULL DEFAULT '',
    c_last_error_code TEXT NOT NULL DEFAULT '',
    c_last_error_message TEXT NOT NULL DEFAULT '',
    c_cancel_reason TEXT NOT NULL DEFAULT '',
    c_start_time DATETIME,
    c_finish_time DATETIME,
    c_ctime DATETIME,
    c_mtime DATETIME,
    PRIMARY KEY (c_space_id, c_job_item_id)
);
CREATE TABLE IF NOT EXISTS t_cloud_job_item_attempts (
    c_space_id TEXT NOT NULL DEFAULT '',
    c_job_item_id TEXT NOT NULL,
    c_attempt_no INTEGER NOT NULL DEFAULT 0,
    c_node_id TEXT NOT NULL DEFAULT '',
    c_status TEXT NOT NULL DEFAULT '',
    c_error_kind TEXT NOT NULL DEFAULT '',
    c_error_code TEXT NOT NULL DEFAULT '',
    c_error_message TEXT NOT NULL DEFAULT '',
    c_result_summary TEXT NOT NULL DEFAULT '{}',
    c_started_at DATETIME,
    c_finished_at DATETIME,
    c_ctime DATETIME,
    c_mtime DATETIME,
    PRIMARY KEY (c_space_id, c_job_item_id, c_attempt_no)
);
`
```

- [ ] **Step 4: Implement Store**

`store.go` responsibilities:

- `NewStore(StoreOptions{Dir, RetentionDays})`
- `WriteTerminal(ctx, state jobstate.State) error`
- `openDayDB(ctx context.Context, day time.Time) (*gorm.DB, error)` as an unexported helper used by both terminal writes and daily maintenance.
- Open/create `YYYYMMDD.db` based on `state.FinishedAt` if present, otherwise `state.UpdatedAt`.
- Apply schema once per DB path.
- Upsert the item row.
- Upsert all attempt rows.
- `Prune(ctx, now)` removes day DB files older than `RetentionDays`.

Use GORM with `sqlite.Open(path)` to match the module's existing SQLite style. Keep the store independent from the main cloudnode DB.

- [ ] **Step 5: Run history tests**

Run:

```bash
go test ./modules/cloudnode/internal/jobhistory -count=1
```

Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add modules/cloudnode/internal/jobhistory
git commit -m "feat(cloudnode): write terminal job item day history"
```

---

### Task 6: Add Daily tRPC Timer For Jobs Day DB Maintenance

**Files:**
- Create: `modules/cloudnode/internal/jobhistory/maintenance.go`
- Create: `modules/cloudnode/internal/jobhistory/maintenance_test.go`
- Create: `modules/cloudnode/internal/rpc/job_history_schedule.go`
- Create: `modules/cloudnode/internal/rpc/job_history_schedule_test.go`
- Modify: `modules/cloudnode/internal/bootstrap/bootstrap.go`
- Modify: `modules/cloudnode/config/trpc_go.yaml`
- Modify: `modules/cloudnode/go.mod`
- Modify: `modules/cloudnode/go.sum`

- [ ] **Step 1: Write failing history maintenance test**

Create `modules/cloudnode/internal/jobhistory/maintenance_test.go`:

```go
func TestMaintainDailyCreatesFutureTwoDaysAndDeletesPastTwoDays(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	store := NewStore(StoreOptions{Dir: dir, RetentionDays: 2})

	for _, name := range []string{"20260704.db", "20260705.db", "20260706.db", "20260707.db"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("legacy"), 0o644); err != nil {
			t.Fatalf("seed %s: %v", name, err)
		}
	}

	now := time.Date(2026, 7, 7, 8, 0, 0, 0, time.UTC)
	if err := store.MaintainDaily(ctx, now); err != nil {
		t.Fatalf("MaintainDaily() error = %v", err)
	}

	for _, name := range []string{"20260708.db", "20260709.db"} {
		if _, err := os.Stat(filepath.Join(dir, name)); err != nil {
			t.Fatalf("future db %s was not created: %v", name, err)
		}
	}
	for _, name := range []string{"20260704.db", "20260705.db"} {
		if _, err := os.Stat(filepath.Join(dir, name)); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("old db %s still exists or stat failed with unexpected error: %v", name, err)
		}
	}
	for _, name := range []string{"20260706.db", "20260707.db"} {
		if _, err := os.Stat(filepath.Join(dir, name)); err != nil {
			t.Fatalf("recent db %s should remain: %v", name, err)
		}
	}
}
```

- [ ] **Step 2: Run maintenance test and verify it fails**

Run:

```bash
go test ./modules/cloudnode/internal/jobhistory -run TestMaintainDailyCreatesFutureTwoDaysAndDeletesPastTwoDays -count=1
```

Expected: compile failure because `MaintainDaily` does not exist.

- [ ] **Step 3: Implement `EnsureDayDB` and `MaintainDaily`**

Create `maintenance.go`:

```go
package jobhistory

import (
	"context"
	"os"
	"path/filepath"
	"time"
)

func (s *Store) EnsureDayDB(ctx context.Context, day time.Time) error {
	db, err := s.openDayDB(ctx, day)
	if err != nil {
		return err
	}
	sqlDB, err := db.DB()
	if err != nil {
		return err
	}
	return sqlDB.Close()
}

func (s *Store) MaintainDaily(ctx context.Context, now time.Time) error {
	day := utcDay(now)
	for _, offset := range []int{1, 2} {
		if err := s.EnsureDayDB(ctx, day.AddDate(0, 0, offset)); err != nil {
			return err
		}
	}
	for _, offset := range []int{-2, -3} {
		path := filepath.Join(s.dir, day.AddDate(0, 0, offset).Format("20060102")+".db")
		if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
			return err
		}
	}
	return nil
}

func utcDay(t time.Time) time.Time {
	y, m, d := t.UTC().Date()
	return time.Date(y, m, d, 0, 0, 0, 0, time.UTC)
}
```

`EnsureDayDB` must call `openDayDB`, so it creates the schema in the DB instead of only touching an empty file.

- [ ] **Step 4: Run maintenance test and verify it passes**

Run:

```bash
go test ./modules/cloudnode/internal/jobhistory -run TestMaintainDailyCreatesFutureTwoDaysAndDeletesPastTwoDays -count=1
```

Expected: PASS.

- [ ] **Step 5: Write failing tRPC handler test**

Create `modules/cloudnode/internal/rpc/job_history_schedule_test.go`:

```go
type fakeHistoryMaintainer struct {
	called bool
	now    time.Time
	err    error
}

func (f *fakeHistoryMaintainer) MaintainDaily(ctx context.Context, now time.Time) error {
	f.called = true
	f.now = now
	return f.err
}

func TestHandleJobHistoryScheduleCallsConfiguredMaintainer(t *testing.T) {
	fake := &fakeHistoryMaintainer{}
	restore := setDefaultJobHistoryMaintainerForTest(fake, func() time.Time {
		return time.Date(2026, 7, 7, 8, 0, 0, 0, time.UTC)
	})
	defer restore()

	if err := HandleJobHistorySchedule(context.Background(), ""); err != nil {
		t.Fatalf("HandleJobHistorySchedule() error = %v", err)
	}
	if !fake.called {
		t.Fatal("maintainer was not called")
	}
	if fake.now.Format("20060102") != "20260707" {
		t.Fatalf("now = %s", fake.now.Format(time.RFC3339))
	}
}
```

- [ ] **Step 6: Implement tRPC timer handler**

Create `modules/cloudnode/internal/rpc/job_history_schedule.go`:

```go
package rpc

import (
	"context"
	"errors"
	"sync"
	"time"

	"trpc.group/trpc-go/trpc-go/log"
)

type jobHistoryMaintainer interface {
	MaintainDaily(context.Context, time.Time) error
}

var defaultJobHistoryMaintenance = struct {
	sync.RWMutex
	maintainer jobHistoryMaintainer
	now        func() time.Time
}{
	now: time.Now,
}

func SetDefaultJobHistoryMaintainer(maintainer jobHistoryMaintainer) {
	defaultJobHistoryMaintenance.Lock()
	defer defaultJobHistoryMaintenance.Unlock()
	defaultJobHistoryMaintenance.maintainer = maintainer
}

func setDefaultJobHistoryMaintainerForTest(maintainer jobHistoryMaintainer, now func() time.Time) func() {
	defaultJobHistoryMaintenance.Lock()
	oldMaintainer := defaultJobHistoryMaintenance.maintainer
	oldNow := defaultJobHistoryMaintenance.now
	defaultJobHistoryMaintenance.maintainer = maintainer
	defaultJobHistoryMaintenance.now = now
	defaultJobHistoryMaintenance.Unlock()
	return func() {
		defaultJobHistoryMaintenance.Lock()
		defaultJobHistoryMaintenance.maintainer = oldMaintainer
		defaultJobHistoryMaintenance.now = oldNow
		defaultJobHistoryMaintenance.Unlock()
	}
}

func HandleJobHistorySchedule(ctx context.Context, rawParams string) error {
	defaultJobHistoryMaintenance.RLock()
	maintainer := defaultJobHistoryMaintenance.maintainer
	now := defaultJobHistoryMaintenance.now
	defaultJobHistoryMaintenance.RUnlock()
	if maintainer == nil {
		return errors.New("job history maintainer is not configured")
	}
	if now == nil {
		now = time.Now
	}
	runAt := now().UTC()
	log.InfoContextf(ctx, "cloudnode job history schedule triggered params=%s date=%s", rawParams, runAt.Format("20060102"))
	return maintainer.MaintainDaily(ctx, runAt)
}
```

- [ ] **Step 7: Register timer in bootstrap**

Add the timer dependency:

```bash
go get trpc.group/trpc-go/trpc-database/timer@v1.0.0
```

In `modules/cloudnode/internal/bootstrap/bootstrap.go`, import:

```go
import "trpc.group/trpc-go/trpc-database/timer"
```

Register after the history store is created:

```go
cloudnoderpc.SetDefaultJobHistoryMaintainer(historyStore)
registerJobHistorySchedule(s)
```

Add:

```go
func registerJobHistorySchedule(s *server.Server) {
	timer.RegisterScheduler("cloudnodeJobHistorySchedule", &timer.DefaultScheduler{})
	service := s.Service("trpc.moox.cloudnode.jobhistory.timer")
	if service == nil {
		log.Warn("cloudnode job history timer service is not configured, skip register")
		return
	}
	timer.RegisterHandlerService(service, cloudnoderpc.HandleJobHistorySchedule)
}
```

- [ ] **Step 8: Add tRPC timer service config**

Update `modules/cloudnode/config/trpc_go.yaml`:

```yaml
    - name: trpc.moox.cloudnode.jobhistory.timer
      port: 11406
      network: "0 5 0 * * *?scheduler=cloudnodeJobHistorySchedule&disable=0&params="
      protocol: timer
      timeout: 60000
```

This runs daily at `00:05` according to the tRPC timer cron format used by the repo.

- [ ] **Step 9: Run handler and history tests**

Run:

```bash
go test ./modules/cloudnode/internal/jobhistory ./modules/cloudnode/internal/rpc -run 'TestMaintainDaily|TestHandleJobHistorySchedule' -count=1
```

Expected: PASS.

- [ ] **Step 10: Commit**

```bash
git add modules/cloudnode/internal/jobhistory modules/cloudnode/internal/rpc/job_history_schedule.go modules/cloudnode/internal/rpc/job_history_schedule_test.go modules/cloudnode/internal/bootstrap/bootstrap.go modules/cloudnode/config/trpc_go.yaml modules/cloudnode/go.mod modules/cloudnode/go.sum
git commit -m "feat(cloudnode): maintain job history day dbs on timer"
```

---

### Task 7: Wire Active KV Store Into CloudNode Bootstrap

**Files:**
- Modify: `modules/cloudnode/internal/bootstrap/bootstrap.go`
- Modify: `modules/cloudnode/internal/rpc/server.go`
- Modify: `modules/cloudnode/internal/bootstrap/bootstrap_test.go` if present, otherwise add focused tests in `modules/cloudnode/internal/rpc/job_item_test.go`

- [ ] **Step 1: Write failing wiring test**

Add an RPC-level test helper that constructs service with `jobstate.Store` and `jobhistory.Store`, then asserts `SubmitJobItems` uses KV without writing main DB JobItem rows.

```go
func TestServiceUsesKVStoreForSubmit(t *testing.T) {
	ctx := context.Background()
	db := openTestDB(t)
	rt := startTestRuntime(t, ctx)
	requireNoError(t, rt.EnsureStreams(config.Default().JetStream, config.Default().JobItem))
	kv, err := rt.JetStream().KeyValue(config.Default().JobItem.ActiveKVBucket)
	requireNoError(t, err)
	stateStore := jobstate.NewKVStore(kv, jobstate.Options{DefaultMaxAttempts: 3, RecoverAfterMillis: 600000})
	history := jobhistory.NewStore(jobhistory.StoreOptions{Dir: t.TempDir(), RetentionDays: 2})
	svc := &Service{jobState: stateStore, history: history, executionQueue: jobqueue.NewJetStreamQueue(rt, testQueueConfig())}

	rsp, err := svc.SubmitJobItems(ctx, &pb.SubmitJobItemsReq{Items: []*pb.JobItem{testPBJobItem("crypto", "ji-kv")}})
	requireNoError(t, err)
	if rsp.GetCreated() != 1 {
		t.Fatalf("created = %d", rsp.GetCreated())
	}
	if db.Migrator().HasTable("t_cloud_job_items") {
		var count int64
		db.Table("t_cloud_job_items").Count(&count)
		if count != 0 {
			t.Fatalf("main db job item rows = %d, want 0", count)
		}
	}
}
```

- [ ] **Step 2: Run test and verify it fails**

Run:

```bash
go test ./modules/cloudnode/internal/rpc -run TestServiceUsesKVStoreForSubmit -count=1
```

Expected: compile failure because service fields do not exist.

- [ ] **Step 3: Replace service dependencies**

In `server.go`, replace JobItem-specific fields:

```go
jobState jobstate.Store
history  *jobhistory.Store
```

Keep `heartbeatSink` and `catalog`.

Add options:

```go
func WithJobStateStore(store jobstate.Store) Option
func WithJobHistoryStore(store *jobhistory.Store) Option
```

Update `retFromError` to map `jobstate.ErrConflict`, `jobstate.ErrStaleAttempt`, `jobstate.ErrInactive`, and `jobstate.ErrNotFound`.

- [ ] **Step 4: Update bootstrap**

In JetStream-enabled startup:

```go
kv, err := rt.JetStream().KeyValue(cfg.JobItem.ActiveKVBucket)
if err != nil {
	return nil, fmt.Errorf("open active job kv: %w", err)
}
stateStore := jobstate.NewKVStore(kv, jobstate.Options{
	RecoverAfterMillis: cfg.JobItem.RecoverAfterMillis,
	DefaultMaxAttempts: cfg.JobItem.DefaultMaxAttempts,
})
historyStore := jobhistory.NewStore(jobhistory.StoreOptions{
	Dir:           cfg.JobItem.HistoryDir,
	RetentionDays: cfg.JobItem.HistoryRetentionDays,
})
```

Pass both into RPC service. Stop constructing `projection.NewRepository` for JobItems and stop starting `projection.Projector` or `projection.NewEnqueueReconciler`.

- [ ] **Step 5: Run bootstrap/rpc compile tests**

Run:

```bash
go test ./modules/cloudnode/internal/bootstrap ./modules/cloudnode/internal/rpc -count=1
```

Expected: remaining failures point to RPC methods still using projection. Fix in Task 8.

- [ ] **Step 6: Commit**

```bash
git add modules/cloudnode/internal/bootstrap modules/cloudnode/internal/rpc/server.go
git commit -m "refactor(cloudnode): wire job item kv state store"
```

---

### Task 8: Refactor JobItem RPC Flow To Active KV And Day History

**Files:**
- Modify: `modules/cloudnode/internal/rpc/job_item.go`
- Modify: `modules/cloudnode/internal/rpc/node.go`
- Modify: `modules/cloudnode/internal/rpc/job_item_test.go`
- Modify: `modules/cloudnode/internal/rpc/node_heartbeat_test.go`

- [ ] **Step 1: Update SubmitJobItems tests**

Expected behavior:

- `SubmitJobItems` creates active KV state first.
- It publishes to JetStream.
- It stores queue metadata in active KV.
- Duplicate submit returns `DEDUPLICATED`.
- Publish failure marks active state `enqueue_failed`.

- [ ] **Step 2: Update PollJobItems tests**

Expected behavior:

- Poll fetches from JetStream.
- If active KV is missing for a delivery, CloudNode TERM's the message and logs `cloudnode_job_orphan`.
- If active KV status is terminal/canceled, CloudNode TERM's the message.
- If active KV is pending, CAS marks it running and returns the JobItem to SCF.

- [ ] **Step 3: Update ReportJobItemStatus tests**

Expected behavior:

- Matching success report updates active KV to `success`, writes one terminal snapshot to day DB, and ACKs JetStream.
- Retryable failure with attempts left updates active KV back to `pending` and NAKs JetStream.
- Permanent failure updates active KV to `failed`, writes day DB, and TERM's JetStream.
- Stale attempt returns `INVALID_PARAM` and does not ACK/NAK/TERM the current delivery.

- [ ] **Step 4: Update Cancel and heartbeat tests**

Expected behavior:

- `CancelJobItem` marks active KV `canceled`.
- `ReportHeartbeat` returns cancel directive from active KV for the running node.
- `ReportJobItemStatus` for a canceled matching attempt TERM's JetStream and clears the cancel directive.

- [ ] **Step 5: Implement SubmitJobItems with jobState**

Replace projection usage:

```go
result, err := s.jobState.CreatePending(ctx, item, jobstate.QueueMeta{})
```

After publish:

```go
err := s.jobState.MarkPublished(ctx, item.GetSpaceId(), item.GetJobItemId(), jobstate.QueueMeta{
	Subject: pub.Subject,
	Stream: pub.Stream,
	StreamSeq: pub.Sequence,
})
```

On publish failure:

```go
_ = s.jobState.MarkEnqueueFailed(ctx, item.GetSpaceId(), item.GetJobItemId(), err.Error())
```

- [ ] **Step 6: Implement PollJobItems with jobState**

Use:

```go
state, err := s.jobState.Get(ctx, delivery.Message.SpaceID, delivery.Message.JobItemID)
if errors.Is(err, jobstate.ErrNotFound) {
	log.WarnContextf(ctx, "cloudnode_job_orphan space_id=%s job_item_id=%s stream_seq=%d action=term",
		delivery.Message.SpaceID, delivery.Message.JobItemID, delivery.StreamSeq)
	_ = s.executionQueue.Term(ctx, delivery.AckSubject)
	continue
}
if state.IsTerminal() || state.Status == jobstate.StatusCanceled {
	_ = s.executionQueue.Term(ctx, delivery.AckSubject)
	continue
}
ok, running, err := s.jobState.TryMarkRunning(ctx, jobstate.RunningRequest{...})
```

- [ ] **Step 7: Implement ReportJobItemStatus with history write**

Convert request to `jobstate.ReportEvent`. Call:

```go
updated, err := s.jobState.MarkReported(ctx, event)
```

If `updated.IsTerminal()`:

```go
if err := s.history.WriteTerminal(ctx, *updated); err != nil {
	log.WarnContextf(ctx, "cloudnode_job_history_write_failed space_id=%s job_item_id=%s err=%v",
		updated.SpaceID, updated.JobItemID, err)
} else {
	_ = s.jobState.MarkHistorySynced(ctx, updated.SpaceID, updated.JobItemID)
}
```

Add `MarkHistorySynced` to `jobstate.Store` if the implementation needs to avoid repeated writes. Repeated day DB upserts must remain safe.

Then ACK/NAK/TERM using the active state's `AckSubject` captured before update.

- [ ] **Step 8: Implement Get/List/ListAttempts with jobState**

Replace projection calls:

```go
state, err := s.jobState.Get(ctx, req.GetSpaceId(), req.GetJobItemId())
return &pb.GetJobItemRsp{RetInfo: retOK(), Item: state.ToDetail()}, nil
```

`ListJobItems` and `ListJobItemAttempts` delegate to `jobState`.

- [ ] **Step 9: Implement heartbeat directives with jobState**

In `node.go`:

```go
if s.jobState == nil {
	return nil, nil
}
return s.jobState.ListCancelDirectives(ctx, spaceID, nodeID, 20)
```

- [ ] **Step 10: Run RPC tests**

Run:

```bash
go test ./modules/cloudnode/internal/rpc -count=1
```

Expected: PASS.

- [ ] **Step 11: Commit**

```bash
git add modules/cloudnode/internal/rpc
git commit -m "refactor(cloudnode): serve job item rpc from active kv"
```

---

### Task 9: Remove SQLite Projection/Reconciler JobItem Path

**Files:**
- Modify/Delete: `modules/cloudnode/internal/projection/repository.go`
- Delete: `modules/cloudnode/internal/projection/projector.go`
- Delete: `modules/cloudnode/internal/projection/reconciler.go`
- Modify/Delete tests under `modules/cloudnode/internal/projection`
- Modify: `modules/cloudnode/internal/jobqueue/naming.go`
- Modify: `modules/cloudnode/internal/jobqueue/naming_test.go`
- Modify: `modules/cloudnode/internal/jobqueue/jetstream_client_test.go`
- Modify: `modules/cloudnode/README.md`

- [ ] **Step 1: Remove projector/reconciler tests**

Delete tests that only validate JobItem SQLite projection stream behavior:

```text
modules/cloudnode/internal/projection/projector_test.go
```

Keep heartbeat buffer files if they are still used for node catalog heartbeat batching:

```text
modules/cloudnode/internal/projection/heartbeat_buffer.go
modules/cloudnode/internal/projection/heartbeat_buffer_test.go
```

- [ ] **Step 2: Remove projection stream creation**

In `jobqueue/jetstream_client.go`, remove `ProjectionStream` ensure logic if no other code publishes projection events.

Update `JetStreamConfig` and YAML only after confirming no reference remains:

```bash
rg -n "ProjectionStream|projection_stream|ProjectionSubject|ProjectionEvent" modules/cloudnode
```

Expected after cleanup: only docs or no references.

- [ ] **Step 3: Update jobqueue tests**

`TestEnsureStreams...` should assert:

- execution stream exists
- active KV bucket exists
- no assertion for `MOOX_CLOUDNODE_PROJECTION`

- [ ] **Step 4: Run projection/jobqueue tests**

Run:

```bash
go test ./modules/cloudnode/internal/projection ./modules/cloudnode/internal/jobqueue -count=1
```

Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add modules/cloudnode/internal/projection modules/cloudnode/internal/jobqueue modules/cloudnode/internal/config/config.go modules/cloudnode/config/app.yaml
git commit -m "refactor(cloudnode): remove job item sqlite projection stream"
```

---

### Task 10: Remove Legacy Online Tables From Main CloudNode Schema

**Files:**
- Modify: `modules/cloudnode/schema/cloudnode.sql`
- Modify: `modules/cloudnode/schema/schema_test.go`
- Modify: `modules/cloudnode/cmd/cli/init_schema_test.go`

- [ ] **Step 1: Write schema test**

Add assertions:

```go
func TestCloudNodeSchemaDropsLegacyJobItemTables(t *testing.T) {
	sql := AllSQL()
	for _, table := range []string{"t_cloud_job_items", "t_cloud_job_item_attempts"} {
		if !strings.Contains(sql, "DROP TABLE IF EXISTS "+table) {
			t.Fatalf("schema must drop %s", table)
		}
		if strings.Contains(sql, "CREATE TABLE IF NOT EXISTS "+table) {
			t.Fatalf("schema must not recreate online table %s", table)
		}
	}
}
```

- [ ] **Step 2: Run schema test and verify it fails**

Run:

```bash
go test ./modules/cloudnode/schema -run TestCloudNodeSchemaDropsLegacyJobItemTables -count=1
```

Expected: fail because old schema still creates the tables.

- [ ] **Step 3: Update schema**

At top of `cloudnode.sql`:

```sql
DROP TABLE IF EXISTS t_cloud_job_item_attempts;
DROP TABLE IF EXISTS t_cloud_job_items;
```

Remove online CREATE statements, indexes, and triggers for both tables.

- [ ] **Step 4: Update CLI init test**

Create a temp DB with legacy tables, run `moox-cloudnode-cli init`, assert:

```sql
SELECT name FROM sqlite_master WHERE type='table' AND name IN ('t_cloud_job_items', 't_cloud_job_item_attempts');
```

returns no rows.

- [ ] **Step 5: Run schema and CLI tests**

Run:

```bash
go test ./modules/cloudnode/schema ./modules/cloudnode/cmd/cli -count=1
```

Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add modules/cloudnode/schema modules/cloudnode/cmd/cli
git commit -m "refactor(cloudnode): drop online job item sqlite tables"
```

---

### Task 11: Update Deployment Runtime Directories

**Files:**
- Modify: `scripts/deploy-moox.sh`
- Test: `scripts/test-deploy-moox-preserve-disabled.sh` if affected

- [ ] **Step 1: Add runtime directory**

In generated `start.sh`, ensure:

```bash
mkdir -p "${ROOT}/data/cloudnode/jobs"
```

The existing command already creates `data/cloudnode`; extend it to include `data/cloudnode/jobs`.

- [ ] **Step 2: Verify deploy package keeps history dir**

Run:

```bash
scripts/deploy-moox.sh --target localhost --dir /tmp/moox-jobstate-plan --goos "$(go env GOOS)" --goarch "$(go env GOARCH)" --no-storage --no-start
test -d /tmp/moox-jobstate-plan/data/cloudnode/jobs
```

Expected: directory exists.

- [ ] **Step 3: Commit**

```bash
git add scripts/deploy-moox.sh
git commit -m "chore(deploy): create cloudnode job history directory"
```

---

### Task 12: Update Documentation

**Files:**
- Modify: `modules/cloudnode/README.md`
- Modify: `docs/云节点执行平台架构.md`
- Modify: `docs/云节点管理.md`

- [ ] **Step 1: Update storage description**

Document:

```text
JobItem 执行队列事实源：JetStream WorkQueue stream。
JobItem active 状态：JetStream KV bucket MOOX_CLOUDNODE_JOB_ACTIVE，统一 TTL 48 小时。
JobItem 历史：终态快照写入 data/cloudnode/jobs/YYYYMMDD.db，仅本地排障使用，管理台当前不查询历史。
SCF 执行日志：通过 stdout 上报，由 CLS 采集。
```

- [ ] **Step 2: Update table list**

Remove `t_cloud_job_items` and `t_cloud_job_item_attempts` from the main cloudnode DB table list. Mention they exist only inside day DB files.

- [ ] **Step 3: Run doc grep check**

Run:

```bash
rg -n "SQLite 查询投影|t_cloud_job_items|t_cloud_job_item_attempts|projection stream|MOOX_CLOUDNODE_PROJECTION" docs modules/cloudnode/README.md
```

Expected:

- no stale statement saying active state lives in main SQLite
- references to `t_cloud_job_items` and `t_cloud_job_item_attempts` explicitly say day DB only

- [ ] **Step 4: Commit**

```bash
git add modules/cloudnode/README.md docs/云节点执行平台架构.md docs/云节点管理.md
git commit -m "docs(cloudnode): document job item active kv storage"
```

---

### Task 13: Full Verification And Remote Rollout

**Files:**
- No source changes expected.

- [ ] **Step 1: Run focused cloudnode tests**

```bash
go test ./modules/cloudnode/... -count=1
```

Expected: PASS.

- [ ] **Step 2: Run related module tests**

```bash
go test ./modules/collector/... ./packages/cloudruntime ./modules/cloudnode/... -count=1
```

Expected: PASS.

- [ ] **Step 3: Run schema grep**

```bash
rg -n "CREATE TABLE IF NOT EXISTS t_cloud_job_items|CREATE TABLE IF NOT EXISTS t_cloud_job_item_attempts" modules/cloudnode/schema
```

Expected: no output.

- [ ] **Step 4: Run local smoke with embedded NATS**

Start local package or use integration test to verify:

- `SubmitJobItems` returns `CREATED`
- `PollJobItems` returns one item
- `ReportJobItemStatus` success returns `ok`
- active KV has a key under `MOOX_CLOUDNODE_JOB_ACTIVE`
- `data/cloudnode/jobs/YYYYMMDD.db` contains terminal row
- main cloudnode DB does not contain online JobItem tables

- [ ] **Step 5: Deploy to remote**

After user approval:

```bash
scripts/deploy-moox.sh --target ubuntu@106.53.107.122 --dir /home/ubuntu/moox/prod --goos linux --goarch amd64 --no-storage
```

- [ ] **Step 6: Remote verification**

```bash
ssh ubuntu@106.53.107.122 '/home/ubuntu/moox/prod/status.sh'
curl -sS --max-time 10 http://106.53.107.122:11000/api/admin/health
ssh ubuntu@106.53.107.122 "sqlite3 /home/ubuntu/moox/prod/data/cloudnode/moox_cloudnode.db \"SELECT name FROM sqlite_master WHERE type='table' AND name IN ('t_cloud_job_items','t_cloud_job_item_attempts');\""
```

Expected:

- all services running
- health returns `{"status":"ok",...}`
- SQLite query returns no rows

- [ ] **Step 7: Verify active KV exists remotely**

If the `nats` CLI is installed on the remote:

```bash
ssh ubuntu@106.53.107.122 'nats --server nats://127.0.0.1:4223 kv info MOOX_CLOUDNODE_JOB_ACTIVE'
```

Expected: bucket exists, TTL is 48h, history is 1.

If `nats` CLI is not installed, add a temporary admin/debug CLI command only in a follow-up plan, not in this implementation.

- [ ] **Step 8: Commit final verification note if docs changed during verification**

Only commit if verification updates docs or scripts:

```bash
git status --short
```

Expected: clean working tree.

---

## Self-Review

- Spec coverage: Covers unified 48-hour active TTL, NATS KV active store, SQLite day DB history, daily tRPC timer maintenance for future day DB creation and `D-2`/`D-3` cleanup, no management-console historical query, orphan handling, SCF/CLS logging responsibility, schema cleanup, tests, docs, deployment.
- Placeholder scan: No placeholder tokens or vague edge-condition steps remain.
- Type consistency: Plan consistently uses `jobstate.State`, `jobstate.Store`, `jobhistory.Store`, `QueueMeta`, `RunningRequest`, `ReportEvent`, and existing CloudNode protobuf names.

## Open Confirmation

This plan intentionally does not migrate old in-flight SQLite JobItems. During cutover, stale JetStream messages without active KV state are TERM'ed and future collector schedules recreate needed work. Confirm this is acceptable before implementation; if not, add a bounded one-time migration task before Task 10.
