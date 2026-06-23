# Storage Roles And Deriver Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Split `moox-storage` into configurable `access`, `primary`, and `deriver` runtime roles, make memory event consumption asynchronous, default event delivery to NATS, and batch DuckDB/Bleve derivation work.

**Architecture:** Keep one `moox-storage` binary. `access` writes PrimaryStore and publishes row-change events. `primary` owns Pebble. `deriver` subscribes to events, batches keys, reads current rows through Access, and writes DuckDB or Bleve.

**Tech Stack:** Go, tRPC-Go, NATS JetStream, Pebble, DuckDB, Bleve, existing storage proto/gen packages.

---

## File Structure

- Modify `modules/storage/internal/config/loader.go`: add role and deriver config defaults.
- Modify `modules/storage/internal/config/loader_test.go`: lock default roles, NATS defaults, and deriver batch defaults.
- Modify `modules/storage/config/storage.yaml`: set default roles and NATS business config.
- Modify `modules/storage/internal/core/eventbus/bus.go`: make `MemoryBus` queue handlers asynchronously.
- Modify `modules/storage/internal/core/eventbus/bus_test.go`: verify async behavior and close/wait behavior.
- Modify `modules/storage/internal/infra/eventbus/producer_bus.go`: keep subject split and derive subscription names per event type.
- Modify `modules/storage/internal/infra/transport/message.go`: carry ack/nak functions for batch failure redelivery.
- Modify `modules/storage/internal/infra/transport/nats/producer.go`: support explicit durable names per subscription and keep manual ack.
- Create `modules/storage/internal/services/deriver/options.go`: deriver dependencies and batch options.
- Create `modules/storage/internal/services/deriver/batcher.go`: generic key batcher used by both event types.
- Create `modules/storage/internal/services/deriver/service.go`: event subscription lifecycle, workers, and close.
- Create `modules/storage/internal/services/deriver/time_series.go`: TimeSeries event handling and DuckDB writes.
- Create `modules/storage/internal/services/deriver/record.go`: Record event handling and Bleve writes.
- Create `modules/storage/internal/services/deriver/access_reader.go`: local or remote AccessService row reader.
- Create `modules/storage/internal/services/deriver/view_projection.go`: move projection helpers from Access.
- Create `modules/storage/internal/services/deriver/view_dirty.go`: move dirty tracking from Access.
- Modify `modules/storage/internal/services/access/service.go`: remove event consumers and keep only user-facing Access/View APIs.
- Modify `modules/storage/internal/services/access/data.go`: publish events only.
- Modify `modules/storage/internal/services/access/query.go`: use shared projection helpers and keep rebuild dirty drain wired through exported deriver helpers.
- Modify `modules/storage/cmd/moox-storage/main.go`: start services by role.
- Modify `modules/storage/cmd/moox-storage/main_test.go`: cover role registration and default NATS startup errors.
- Modify `modules/storage/README.md` and `modules/storage/docs/architecture.md`: document roles, NATS default, and deriver batch settings.

---

### Task 1: Add Runtime Roles And Deriver Config

**Files:**
- Modify: `modules/storage/internal/config/loader.go`
- Modify: `modules/storage/internal/config/loader_test.go`
- Modify: `modules/storage/config/storage.yaml`

- [ ] **Step 1: Write failing config tests**

Append these tests to `modules/storage/internal/config/loader_test.go`:

```go
func TestStorageRuntimeConfigDefaultsRolesEventBusAndDeriver(t *testing.T) {
	t.Parallel()

	var cfg RuntimeConfig
	cfg.ApplyDefaults()

	if got := strings.Join(cfg.Storage.Roles, ","); got != "access,deriver" {
		t.Fatalf("roles = %q, want access,deriver", got)
	}
	if cfg.Storage.EventBus.Type != "nats" {
		t.Fatalf("eventbus type = %q, want nats", cfg.Storage.EventBus.Type)
	}
	if cfg.Storage.EventBus.NATSURL != "nats://127.0.0.1:4222" {
		t.Fatalf("nats_url = %q", cfg.Storage.EventBus.NATSURL)
	}
	if cfg.Storage.Deriver.AccessServiceName != "trpc.storage.access.AccessService" {
		t.Fatalf("access_service_name = %q", cfg.Storage.Deriver.AccessServiceName)
	}
	if cfg.Storage.Deriver.BatchSize != 500 {
		t.Fatalf("batch_size = %d", cfg.Storage.Deriver.BatchSize)
	}
	if cfg.Storage.Deriver.BatchWaitMS != 200 {
		t.Fatalf("batch_wait_ms = %d", cfg.Storage.Deriver.BatchWaitMS)
	}
	if cfg.Storage.Deriver.MaxWorkers != 4 {
		t.Fatalf("max_workers = %d", cfg.Storage.Deriver.MaxWorkers)
	}
}

func TestStorageRuntimeConfigHasRole(t *testing.T) {
	t.Parallel()

	cfg := StorageConfig{Roles: []string{"access", "primary"}}

	if !cfg.HasRole("access") || !cfg.HasRole("primary") {
		t.Fatalf("expected access and primary roles")
	}
	if cfg.HasRole("deriver") {
		t.Fatalf("did not expect deriver role")
	}
}
```

- [ ] **Step 2: Run config tests and verify RED**

Run:

```bash
GOWORK=off go test ./internal/config
```

Expected: FAIL because `StorageConfig.Roles`, `StorageConfig.Deriver`, and `HasRole` do not exist.

- [ ] **Step 3: Implement config fields and defaults**

Update `modules/storage/internal/config/loader.go` with:

```go
type StorageConfig struct {
	Root     string          `yaml:"root"`
	Roles    []string        `yaml:"roles"`
	Metadata StorageMetadata `yaml:"metadata"`
	Devices  StorageDevices  `yaml:"devices"`
	Primary  StoragePrimary  `yaml:"primary"`
	EventBus StorageEventBus `yaml:"eventbus"`
	Deriver  StorageDeriver  `yaml:"deriver"`
}

type StorageDeriver struct {
	AccessServiceName string `yaml:"access_service_name"`
	BatchSize         int    `yaml:"batch_size"`
	BatchWaitMS       int    `yaml:"batch_wait_ms"`
	MaxWorkers        int    `yaml:"max_workers"`
}

func (c *StorageConfig) HasRole(role string) bool {
	role = strings.ToLower(strings.TrimSpace(role))
	for _, item := range c.Roles {
		if strings.ToLower(strings.TrimSpace(item)) == role {
			return true
		}
	}
	return false
}
```

Add `strings` to imports. Update `ApplyDefaults`:

```go
if len(c.Roles) == 0 {
	c.Roles = []string{"access", "deriver"}
}
if c.EventBus.Type == "" {
	c.EventBus.Type = "nats"
}
if c.EventBus.NATSURL == "" {
	c.EventBus.NATSURL = "nats://127.0.0.1:4222"
}
if c.EventBus.SubjectPrefix == "" {
	c.EventBus.SubjectPrefix = "moox.storage"
}
if c.EventBus.Type == "nats" && c.EventBus.StreamName == "" {
	c.EventBus.StreamName = "MOOX_STORAGE"
}
if c.EventBus.Type == "nats" && c.EventBus.ConsumerName == "" {
	c.EventBus.ConsumerName = "storage_deriver"
}
if c.Deriver.AccessServiceName == "" && c.EventBus.Type == "nats" {
	c.Deriver.AccessServiceName = "trpc.storage.access.AccessService"
}
if c.Deriver.BatchSize <= 0 {
	c.Deriver.BatchSize = 500
}
if c.Deriver.BatchWaitMS <= 0 {
	c.Deriver.BatchWaitMS = 200
}
if c.Deriver.MaxWorkers <= 0 {
	c.Deriver.MaxWorkers = 4
}
```

- [ ] **Step 4: Update default storage config**

Change `modules/storage/config/storage.yaml` to:

```yaml
storage:
  root: ./var/storage
  roles:
    - access
    - deriver
  metadata:
    path: ./var/storage/metadata/storage_metadata.db
  devices:
    pebble_path: ./var/storage/pebble
    duckdb_path: ./var/storage/duckdb/views.duckdb
    bleve_path: ./var/storage/bleve
    parquet_path: ./var/storage/archive
  primary:
    service_name: ""
  eventbus:
    type: nats
    nats_url: nats://127.0.0.1:4222
    stream_name: MOOX_STORAGE
    subject_prefix: moox.storage
    consumer_name: storage_deriver
  deriver:
    access_service_name: trpc.storage.access.AccessService
    batch_size: 500
    batch_wait_ms: 200
    max_workers: 4
```

- [ ] **Step 5: Run config tests and commit**

Run:

```bash
GOWORK=off go test ./internal/config
git diff --check
```

Expected: PASS.

Commit:

```bash
git add modules/storage/internal/config/loader.go modules/storage/internal/config/loader_test.go modules/storage/config/storage.yaml
git commit -m "feat(storage): add runtime roles config"
```

---

### Task 2: Make MemoryBus Asynchronous

**Files:**
- Modify: `modules/storage/internal/core/eventbus/bus.go`
- Modify: `modules/storage/internal/core/eventbus/bus_test.go`

- [ ] **Step 1: Replace MemoryBus tests with async expectations**

Update `TestMemoryBusRowsChangedSubscriptionCanClose` so it waits instead of expecting immediate handler execution:

```go
func TestMemoryBusRowsChangedSubscriptionCanClose(t *testing.T) {
	ctx := context.Background()
	bus := eventbus.NewMemoryBus()
	var called atomic.Int32
	sub, err := bus.SubscribeTimeSeriesRowsChanged(ctx, func(ctx context.Context, event *pb.TimeSeriesRowsChangedEvent) error {
		called.Add(1)
		return nil
	})
	require.NoError(t, err)

	err = bus.PublishTimeSeriesRowsChanged(ctx, &pb.TimeSeriesRowsChangedEvent{
		EventId: "evt-1",
		Keys:    []*pb.TimeSeriesKey{{SpaceId: "crypto", DatasetId: "kline", SubjectId: "APT-USDT", Freq: "1m"}},
	})
	require.NoError(t, err)
	require.Eventually(t, func() bool { return called.Load() == 1 }, time.Second, 10*time.Millisecond)

	require.NoError(t, sub.Close())
	err = bus.PublishTimeSeriesRowsChanged(ctx, &pb.TimeSeriesRowsChangedEvent{
		EventId: "evt-2",
		Keys:    []*pb.TimeSeriesKey{{SpaceId: "crypto", DatasetId: "kline", SubjectId: "APT-USDT", Freq: "1m"}},
	})
	require.NoError(t, err)
	require.NoError(t, bus.Wait(context.Background()))
	require.Equal(t, int32(1), called.Load())
}
```

Add imports:

```go
import (
	"sync/atomic"
	"time"
)
```

Add a new test:

```go
func TestMemoryBusPublishDoesNotRunHandlerInline(t *testing.T) {
	ctx := context.Background()
	bus := eventbus.NewMemoryBus()
	block := make(chan struct{})
	started := make(chan struct{})
	_, err := bus.SubscribeRecordRowsChanged(ctx, func(ctx context.Context, event *pb.RecordRowsChangedEvent) error {
		close(started)
		<-block
		return nil
	})
	require.NoError(t, err)

	err = bus.PublishRecordRowsChanged(ctx, &pb.RecordRowsChangedEvent{
		EventId: "evt-1",
		Keys:    []*pb.RecordKey{{SpaceId: "crypto", DatasetId: "symbols", RecordId: "BTC-USDT"}},
	})
	require.NoError(t, err)

	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("handler did not start")
	}
	close(block)
	require.NoError(t, bus.Wait(context.Background()))
}
```

- [ ] **Step 2: Run eventbus tests and verify RED**

Run:

```bash
GOWORK=off go test ./internal/core/eventbus
```

Expected: FAIL because `MemoryBus.Wait` does not exist.

- [ ] **Step 3: Implement async MemoryBus**

Modify `MemoryBus` in `bus.go`:

```go
type MemoryBus struct {
	mu                 sync.Mutex
	timeSeriesEvents   []*pb.TimeSeriesRowsChangedEvent
	recordEvents       []*pb.RecordRowsChangedEvent
	nextID             uint64
	timeSeriesHandlers map[uint64]TimeSeriesRowsChangedHandler
	recordHandlers     map[uint64]RecordRowsChangedHandler
	wg                 sync.WaitGroup
	closed             bool
}
```

In each `Publish*`, copy handlers under lock, append event, then start each handler in a goroutine:

```go
b.wg.Add(len(handlers))
for _, handler := range handlers {
	handler := handler
	event := event
	go func() {
		defer b.wg.Done()
		_ = handler(context.WithoutCancel(ctx), event)
	}()
}
return nil
```

Add:

```go
func (b *MemoryBus) Wait(ctx context.Context) error {
	done := make(chan struct{})
	go func() {
		b.wg.Wait()
		close(done)
	}()
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (b *MemoryBus) Close() error {
	return b.Wait(context.Background())
}
```

- [ ] **Step 4: Run eventbus tests and commit**

Run:

```bash
GOWORK=off go test ./internal/core/eventbus
git diff --check
```

Expected: PASS.

Commit:

```bash
git add modules/storage/internal/core/eventbus/bus.go modules/storage/internal/core/eventbus/bus_test.go
git commit -m "feat(storage): make memory eventbus asynchronous"
```

---

### Task 3: Add Deriver Batcher

**Files:**
- Create: `modules/storage/internal/services/deriver/options.go`
- Create: `modules/storage/internal/services/deriver/batcher.go`
- Create: `modules/storage/internal/services/deriver/batcher_test.go`

- [ ] **Step 1: Write failing batcher tests**

Create `modules/storage/internal/services/deriver/batcher_test.go`:

```go
package deriver

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestBatcherFlushesBySize(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	b := newBatcher[string](BatchOptions{BatchSize: 3, BatchWait: time.Hour})
	out := make(chan []string, 1)
	go b.run(ctx, out)

	require.NoError(t, b.add(ctx, "a"))
	require.NoError(t, b.add(ctx, "b"))
	require.NoError(t, b.add(ctx, "c"))

	require.Equal(t, []string{"a", "b", "c"}, <-out)
}

func TestBatcherFlushesByWait(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	b := newBatcher[string](BatchOptions{BatchSize: 10, BatchWait: 10 * time.Millisecond})
	out := make(chan []string, 1)
	go b.run(ctx, out)

	require.NoError(t, b.add(ctx, "a"))

	select {
	case got := <-out:
		require.Equal(t, []string{"a"}, got)
	case <-time.After(time.Second):
		t.Fatal("batcher did not flush by wait")
	}
}
```

- [ ] **Step 2: Run deriver tests and verify RED**

Run:

```bash
GOWORK=off go test ./internal/services/deriver
```

Expected: FAIL because the package and batcher do not exist.

- [ ] **Step 3: Implement options and batcher**

Create `options.go`:

```go
package deriver

import "time"

type Options struct {
	BatchSize   int
	BatchWait   time.Duration
	MaxWorkers  int
}

type BatchOptions struct {
	BatchSize int
	BatchWait time.Duration
}

func normalizeBatchOptions(opts BatchOptions) BatchOptions {
	if opts.BatchSize <= 0 {
		opts.BatchSize = 500
	}
	if opts.BatchWait <= 0 {
		opts.BatchWait = 200 * time.Millisecond
	}
	return opts
}
```

Create `batcher.go`:

```go
package deriver

import (
	"context"
	"time"
)

type batcher[T any] struct {
	opts BatchOptions
	in   chan T
}

func newBatcher[T any](opts BatchOptions) *batcher[T] {
	opts = normalizeBatchOptions(opts)
	return &batcher[T]{opts: opts, in: make(chan T, opts.BatchSize*2)}
}

func (b *batcher[T]) add(ctx context.Context, item T) error {
	select {
	case b.in <- item:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (b *batcher[T]) run(ctx context.Context, out chan<- []T) {
	var batch []T
	timer := time.NewTimer(b.opts.BatchWait)
	defer timer.Stop()
	resetTimer := func() {
		if !timer.Stop() {
			select {
			case <-timer.C:
			default:
			}
		}
		timer.Reset(b.opts.BatchWait)
	}
	flush := func() {
		if len(batch) == 0 {
			return
		}
		copied := make([]T, len(batch))
		copy(copied, batch)
		batch = batch[:0]
		out <- copied
		resetTimer()
	}
	for {
		select {
		case <-ctx.Done():
			flush()
			return
		case item := <-b.in:
			batch = append(batch, item)
			if len(batch) >= b.opts.BatchSize {
				flush()
			}
		case <-timer.C:
			flush()
			resetTimer()
		}
	}
}
```

- [ ] **Step 4: Run deriver tests and commit**

Run:

```bash
GOWORK=off go test ./internal/services/deriver
git diff --check
```

Expected: PASS.

Commit:

```bash
git add modules/storage/internal/services/deriver
git commit -m "feat(storage): add deriver batcher"
```

---

### Task 4: Extract Deriver Reader And Projection Helpers

**Files:**
- Create: `modules/storage/internal/services/deriver/access_reader.go`
- Create: `modules/storage/internal/services/deriver/view_projection.go`
- Create: `modules/storage/internal/services/deriver/view_projection_test.go`
- Modify: `modules/storage/internal/services/access/view_projection.go`

- [ ] **Step 1: Write projection tests in deriver package**

Create `modules/storage/internal/services/deriver/view_projection_test.go` with this minimal test:

```go
package deriver

import (
	"testing"

	pb "github.com/mooyang-code/moox/modules/storage/proto/gen"
	"github.com/stretchr/testify/require"
)

func TestProjectColumnsForTimeSeriesViewUsesQualifiedOrigin(t *testing.T) {
	columns := []*pb.ViewColumn{{
		ColumnName: "swap.close",
		OriginType: pb.ColumnOriginType_COLUMN_ORIGIN_TYPE_DATASET_COLUMN,
		OriginId: "swap_kline.close",
		ValueType: pb.FieldValueType_FIELD_VALUE_TYPE_DOUBLE,
	}}
	rows := map[string]*pb.TimeSeriesRow{
		"swap_kline": {
			Key: &pb.TimeSeriesKey{DatasetId: "swap_kline"},
			Columns: []*pb.ColumnValue{{
				ColumnName: "close",
				ValueType: pb.FieldValueType_FIELD_VALUE_TYPE_DOUBLE,
				Value: &pb.TypedValue{Value: &pb.TypedValue_DoubleValue{DoubleValue: 661.87}},
			}},
		},
	}

	got := projectColumnsForView("swap_kline", columns, rows)

	require.Len(t, got, 1)
	require.Equal(t, "swap.close", got[0].GetColumnName())
	require.Equal(t, 661.87, got[0].GetValue().GetDoubleValue())
}
```

- [ ] **Step 2: Run deriver tests and verify RED**

Run:

```bash
GOWORK=off go test ./internal/services/deriver
```

Expected: FAIL because projection helpers are not in deriver package.

- [ ] **Step 3: Move projection helpers**

Move these functions from `access/view_projection.go` into `deriver/view_projection.go`:

```go
projectColumnsForView
viewProjectionDatasets
viewColumnOriginDataset
viewColumnSourceName
projectRecordColumnsForView
isProjectableTimeSeriesView
isProjectableRecordView
timeSeriesProjectionGrainKey
recordProjectionGrainKey
cloneStringMap
```

Keep methods that need `*Service` row reads in deriver by introducing:

```go
type FactReader interface {
	ReadTimeSeriesRows(ctx context.Context, req *pb.ReadTimeSeriesRowsReq) (*pb.ReadTimeSeriesRowsRsp, error)
	ReadRecordRows(ctx context.Context, req *pb.ReadRecordRowsReq) (*pb.ReadRecordRowsRsp, error)
}
```

Add `access_reader.go`:

```go
package deriver

import (
	"context"
	"errors"

	pb "github.com/mooyang-code/moox/modules/storage/proto/gen"
	"trpc.group/trpc-go/trpc-go/client"
)

type accessReader struct {
	local       FactReader
	remote      pb.AccessServiceClientProxy
	serviceName string
}

func NewAccessReader(local FactReader, serviceName string) FactReader {
	if serviceName == "" {
		return &accessReader{local: local}
	}
	return &accessReader{
		local: local,
		remote: pb.NewAccessServiceClientProxy(client.WithServiceName(serviceName)),
		serviceName: serviceName,
	}
}

func (r *accessReader) ReadTimeSeriesRows(ctx context.Context, req *pb.ReadTimeSeriesRowsReq) (*pb.ReadTimeSeriesRowsRsp, error) {
	if r.remote != nil {
		return r.remote.ReadTimeSeriesRows(ctx, req)
	}
	if r.local == nil {
		return nil, errors.New("local access reader is required")
	}
	return r.local.ReadTimeSeriesRows(ctx, req)
}

func (r *accessReader) ReadRecordRows(ctx context.Context, req *pb.ReadRecordRowsReq) (*pb.ReadRecordRowsRsp, error) {
	if r.remote != nil {
		return r.remote.ReadRecordRows(ctx, req)
	}
	if r.local == nil {
		return nil, errors.New("local access reader is required")
	}
	return r.local.ReadRecordRows(ctx, req)
}
```

- [ ] **Step 4: Keep Access compiling**

In `access/view_projection.go`, keep thin wrappers or update imports so Access rebuild logic can call deriver projection helpers. The simplest form is:

```go
import deriversvc "github.com/mooyang-code/moox/modules/storage/internal/services/deriver"
```

Then replace calls:

```go
deriversvc.ProjectRecordColumnsForView(...)
```

Export only the helpers needed by Access rebuild code.

- [ ] **Step 5: Run tests and commit**

Run:

```bash
GOWORK=off go test ./internal/services/deriver ./internal/services/access
git diff --check
```

Expected: PASS.

Commit:

```bash
git add modules/storage/internal/services/deriver modules/storage/internal/services/access/view_projection.go
git commit -m "refactor(storage): share view projection helpers"
```

---

### Task 5: Move Event Consumers Into Deriver Service

**Files:**
- Create: `modules/storage/internal/services/deriver/service.go`
- Create: `modules/storage/internal/services/deriver/time_series.go`
- Create: `modules/storage/internal/services/deriver/record.go`
- Create: `modules/storage/internal/services/deriver/view_dirty.go`
- Create: `modules/storage/internal/services/deriver/service_test.go`
- Modify: `modules/storage/internal/services/access/service.go`
- Modify: `modules/storage/internal/services/access/data.go`
- Modify: `modules/storage/internal/services/access/query.go`

- [ ] **Step 1: Write failing deriver service test**

Create `modules/storage/internal/services/deriver/service_test.go` with a fake subscriber and fake writer:

```go
package deriver

import (
	"context"
	"testing"
	"time"

	coreeventbus "github.com/mooyang-code/moox/modules/storage/internal/core/eventbus"
	pb "github.com/mooyang-code/moox/modules/storage/proto/gen"
	"github.com/stretchr/testify/require"
)

func TestServiceSubscribesToRowsChangedEvents(t *testing.T) {
	ctx := context.Background()
	events := coreeventbus.NewMemoryBus()
	svc := NewService(Options{
		Events: events,
		Reader: fakeFactReader{},
		BatchSize: 2,
		BatchWait: 10 * time.Millisecond,
		MaxWorkers: 1,
	})

	require.NoError(t, svc.Start(ctx))
	require.NoError(t, events.PublishRecordRowsChanged(ctx, &pb.RecordRowsChangedEvent{
		EventId: "evt-1",
		Keys: []*pb.RecordKey{{SpaceId: "crypto", DatasetId: "symbols", RecordId: "BTC-USDT"}},
	}))
	require.NoError(t, events.Wait(ctx))
	require.NoError(t, svc.Close())
}

type fakeFactReader struct{}

func (fakeFactReader) ReadTimeSeriesRows(ctx context.Context, req *pb.ReadTimeSeriesRowsReq) (*pb.ReadTimeSeriesRowsRsp, error) {
	return &pb.ReadTimeSeriesRowsRsp{RetInfo: &pb.RetInfo{Code: pb.ErrorCode_SUCCESS}}, nil
}

func (fakeFactReader) ReadRecordRows(ctx context.Context, req *pb.ReadRecordRowsReq) (*pb.ReadRecordRowsRsp, error) {
	return &pb.ReadRecordRowsRsp{RetInfo: &pb.RetInfo{Code: pb.ErrorCode_SUCCESS}}, nil
}
```

- [ ] **Step 2: Run deriver tests and verify RED**

Run:

```bash
GOWORK=off go test ./internal/services/deriver
```

Expected: FAIL because `NewService`, `Start`, and `Close` are not implemented.

- [ ] **Step 3: Implement deriver lifecycle**

Create `service.go`:

```go
package deriver

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/mooyang-code/moox/modules/storage/internal/core/eventbus"
	"github.com/mooyang-code/moox/modules/storage/internal/core/metadata"
	deviceduckdb "github.com/mooyang-code/moox/modules/storage/internal/infra/device/duckdb"
	"github.com/mooyang-code/moox/modules/storage/internal/services/search"
	pb "github.com/mooyang-code/moox/modules/storage/proto/gen"
)

type Service struct {
	events eventbus.Bus
	reader FactReader
	metadata metadata.Store
	metadataReader metadata.Reader
	views *deviceduckdb.ViewStore
	search *search.Service
	batch BatchOptions
	maxWorkers int
	cancel context.CancelFunc
	wg sync.WaitGroup
	timeSeriesSub eventbus.Subscription
	recordSub eventbus.Subscription
	timeSeriesBatcher *batcher[*pb.TimeSeriesKey]
	recordBatcher *batcher[*pb.RecordKey]
}

func NewService(opts Options) *Service {
	maxWorkers := opts.MaxWorkers
	if maxWorkers <= 0 {
		maxWorkers = 4
	}
	return &Service{
		events: opts.Events,
		reader: opts.Reader,
		metadata: opts.Metadata,
		metadataReader: opts.MetadataReader,
		views: opts.Views,
		search: opts.Search,
		batch: normalizeBatchOptions(BatchOptions{BatchSize: opts.BatchSize, BatchWait: opts.BatchWait}),
		maxWorkers: maxWorkers,
	}
}

func (s *Service) Start(ctx context.Context) error {
	subscriber, ok := s.events.(eventbus.Subscriber)
	if !ok {
		return errors.New("deriver requires subscribable eventbus")
	}
	runCtx, cancel := context.WithCancel(ctx)
	s.cancel = cancel
	tsSub, err := subscriber.SubscribeTimeSeriesRowsChanged(ctx, s.enqueueTimeSeries)
	if err != nil {
		return err
	}
	recSub, err := subscriber.SubscribeRecordRowsChanged(ctx, s.enqueueRecord)
	if err != nil {
		_ = tsSub.Close()
		return err
	}
	s.timeSeriesSub = tsSub
	s.recordSub = recSub
	s.timeSeriesBatcher = newBatcher[*pb.TimeSeriesKey](s.batch)
	s.recordBatcher = newBatcher[*pb.RecordKey](s.batch)
	s.startWorkers(runCtx)
	return nil
}

func (s *Service) Close() error {
	if s.cancel != nil {
		s.cancel()
	}
	if s.timeSeriesSub != nil {
		_ = s.timeSeriesSub.Close()
	}
	if s.recordSub != nil {
		_ = s.recordSub.Close()
	}
	s.wg.Wait()
	return nil
}
```

Add missing fields to `Options`:

```go
Events eventbus.Bus
Reader FactReader
Metadata metadata.Store
MetadataReader metadata.Reader
Views *deviceduckdb.ViewStore
Search *search.Service
BatchSize int
BatchWait time.Duration
MaxWorkers int
```

- [ ] **Step 4: Move handlers from Access**

Move these methods from `access/data.go` into `deriver/time_series.go` and `deriver/record.go`, replacing `s` dependencies with deriver fields:

```go
handleTimeSeriesRowsChangedForView -> enqueueTimeSeries + processTimeSeriesBatch
handleRecordRowsChangedForSearch -> enqueueRecord + processRecordBatch
currentTimeSeriesRows
currentRecordRows
markViewPending
```

The `enqueue*` methods only add keys to batchers:

```go
func (s *Service) enqueueRecord(ctx context.Context, event *pb.RecordRowsChangedEvent) error {
	for _, key := range event.GetKeys() {
		if key != nil {
			if err := s.recordBatcher.add(ctx, key); err != nil {
				return err
			}
		}
	}
	return nil
}
```

The batch processor reads and writes in one call:

```go
func (s *Service) processRecordBatch(ctx context.Context, keys []*pb.RecordKey) error {
	rows, err := s.currentRecordRows(ctx, keys)
	if err != nil {
		return err
	}
	if len(rows) == 0 {
		return nil
	}
	return s.indexRecordRows(ctx, rows)
}
```

- [ ] **Step 5: Remove Access event consumers**

Delete from `access.Service`:

```go
indexCond
indexJobs
recordRowsChangedSub
timeSeriesRowsChangedSub
StartEventConsumers
runSearchIndexWorker
handleTimeSeriesRowsChangedForView
handleRecordRowsChangedForSearch
indexRecordRowsFromAccess
```

Keep `WaitForIndex` as a no-op if tests still call it:

```go
func (s *Service) WaitForIndex() {}
```

- [ ] **Step 6: Run package tests and commit**

Run:

```bash
GOWORK=off go test ./internal/services/deriver ./internal/services/access
git diff --check
```

Expected: PASS.

Commit:

```bash
git add modules/storage/internal/services/deriver modules/storage/internal/services/access
git commit -m "refactor(storage): move row derivation consumers"
```

---

### Task 6: Wire Roles In moox-storage Main

**Files:**
- Modify: `modules/storage/cmd/moox-storage/main.go`
- Modify: `modules/storage/cmd/moox-storage/main_test.go`

- [ ] **Step 1: Write role tests**

Add tests to `main_test.go`:

```go
func TestStorageRolesDefaultToAccessAndDeriver(t *testing.T) {
	cfg := storageconfig.RuntimeConfig{}
	cfg.ApplyDefaults()
	if !cfg.Storage.HasRole("access") || !cfg.Storage.HasRole("deriver") {
		t.Fatalf("default roles = %v", cfg.Storage.Roles)
	}
	if cfg.Storage.HasRole("primary") {
		t.Fatalf("primary must be explicit in default roles")
	}
}

func TestRoleEnabledIsCaseInsensitive(t *testing.T) {
	cfg := storageconfig.StorageConfig{Roles: []string{"Access", "DERIVER"}}
	if !cfg.HasRole("access") || !cfg.HasRole("deriver") {
		t.Fatalf("case insensitive role lookup failed")
	}
}
```

- [ ] **Step 2: Run main tests and verify RED if imports are missing**

Run:

```bash
GOWORK=off go test ./cmd/moox-storage
```

Expected: PASS if Task 1 already added `HasRole`; otherwise FAIL.

- [ ] **Step 3: Gate service startup by roles**

In `main.go`, replace unconditional startup with role checks:

```go
cfg, hasStorageConfig := loadStorageConfig(storageConfigPathFromArgs(os.Args, configPathFromArgs(os.Args)))
opts := storageOptions()

var storageService *storagesvc.Service
var primaryService *primarysvc.Service

if !hasStorageConfig || cfg.Storage.HasRole("access") || cfg.Storage.HasRole("deriver") {
	storageService = storagesvc.NewServiceWithOptions(opts)
}
if !hasStorageConfig || cfg.Storage.HasRole("primary") {
	primaryService = primarysvc.NewService(primarysvc.Options{Root: opts.Root, PebblePath: opts.PebblePath})
}
```

Register services only when their role exists:

```go
if storageService != nil && cfg.Storage.HasRole("access") {
	pb.RegisterAccessServiceService(s, storageService)
	pb.RegisterMetadataServiceService(s, storageService)
	pb.RegisterViewServiceService(s, storageService)
}
if primaryService != nil && cfg.Storage.HasRole("primary") {
	pb.RegisterPrimaryStoreServiceService(s, primaryService)
}
```

Start deriver only when role exists:

```go
if storageService != nil && cfg.Storage.HasRole("deriver") {
	deriverService, err := newDeriverService(trpc.BackgroundContext(), cfg, storageService, opts)
	if err != nil {
		log.Errorf("初始化 Deriver 失败: %v", err)
		os.Exit(1)
	}
	if err := deriverService.Start(trpc.BackgroundContext()); err != nil {
		log.Errorf("启动 Deriver 失败: %v", err)
		os.Exit(1)
	}
	defer deriverService.Close()
}
```

- [ ] **Step 4: Implement `newDeriverService` helper**

In `main.go`, add:

```go
func newDeriverService(ctx context.Context, cfg storageconfig.RuntimeConfig, localAccess *storagesvc.Service, opts storagesvc.Options) (*deriver.Service, error) {
	views, err := localAccess.ViewStore()
	if err != nil {
		return nil, err
	}
	reader := deriver.NewAccessReader(localAccess, cfg.Storage.Deriver.AccessServiceName)
	if cfg.Storage.EventBus.Type == "memory" {
		reader = deriver.NewAccessReader(localAccess, "")
	}
	return deriver.NewService(deriver.Options{
		Events: opts.Events,
		Reader: reader,
		Metadata: localAccess.MetadataStore(),
		MetadataReader: localAccess.MetadataReader(),
		Views: views,
		Search: localAccess.SearchService(),
		BatchSize: cfg.Storage.Deriver.BatchSize,
		BatchWait: time.Duration(cfg.Storage.Deriver.BatchWaitMS) * time.Millisecond,
		MaxWorkers: cfg.Storage.Deriver.MaxWorkers,
	}), nil
}
```

Expose these narrow getters on `access.Service`:

```go
func (s *Service) ViewStore() (*deviceduckdb.ViewStore, error) { return s.viewStore() }
func (s *Service) MetadataStore() metadata.Store { return s.metadata }
func (s *Service) MetadataReader() metadata.Reader { return s.metadataReader }
func (s *Service) SearchService() *search.Service { return s.search }
```

- [ ] **Step 5: Run main tests and commit**

Run:

```bash
GOWORK=off go test ./cmd/moox-storage ./internal/services/access ./internal/services/deriver
git diff --check
```

Expected: PASS.

Commit:

```bash
git add modules/storage/cmd/moox-storage/main.go modules/storage/cmd/moox-storage/main_test.go modules/storage/internal/services/access modules/storage/internal/services/deriver
git commit -m "feat(storage): start services by runtime role"
```

---

### Task 7: NATS Subscription Names And Ack Semantics

**Files:**
- Modify: `modules/storage/internal/infra/transport/message.go`
- Modify: `modules/storage/internal/infra/transport/nats/producer.go`
- Modify: `modules/storage/internal/infra/eventbus/producer_bus.go`
- Modify: `modules/storage/internal/infra/eventbus/producer_bus_test.go`

- [ ] **Step 1: Write failing subject and durable tests**

Add to `producer_bus_test.go`:

```go
func TestSubscriberBusUsesSeparateSubjectsForRowsChanged(t *testing.T) {
	ctx := context.Background()
	pubsub := &fakePubSub{}
	bus := infraeventbus.NewSubscriberBus(pubsub, "moox.storage")

	_, err := bus.SubscribeTimeSeriesRowsChanged(ctx, func(ctx context.Context, event *pb.TimeSeriesRowsChangedEvent) error { return nil })
	require.NoError(t, err)
	_, err = bus.SubscribeRecordRowsChanged(ctx, func(ctx context.Context, event *pb.RecordRowsChangedEvent) error { return nil })
	require.NoError(t, err)

	require.Contains(t, pubsub.subjects, "moox.storage.time_series.rows_changed.v1")
	require.Contains(t, pubsub.subjects, "moox.storage.record.rows_changed.v1")
}
```

- [ ] **Step 2: Run eventbus tests**

Run:

```bash
GOWORK=off go test ./internal/infra/eventbus ./internal/infra/transport/nats
```

Expected: PASS for subject split; if fake transport does not capture both subjects, update fake before implementation.

- [ ] **Step 3: Add ack hooks to transport message**

Update `transport.Message`:

```go
type Message struct {
	Subject string
	Data    []byte
	ID      string
	Time    time.Time
	Ack     func() error
	Nak     func() error
}
```

Update NATS `Subscribe` to set hooks and only auto-ack when handler succeeds:

```go
event := &transport.Message{
	Subject: msg.Subject,
	Data: msg.Data,
	Time: time.Now(),
	Ack: msg.Ack,
	Nak: msg.Nak,
}
```

Keep current handler contract: return nil means ack; error means nak.

- [ ] **Step 4: Use distinct durable names per subject**

In `producer_bus.go`, when subscribing, pass subject-specific durable names through the transport layer. If the transport interface cannot carry a per-call durable name, add:

```go
type SubscribeOptions struct {
	ConsumerName string
}
```

and update `Subscriber.Subscribe` to:

```go
Subscribe(ctx context.Context, subject string, handler MessageHandler, opts ...SubscribeOption) (Subscription, error)
```

Use names:

```go
storage_deriver_time_series
storage_deriver_record
```

- [ ] **Step 5: Run transport tests and commit**

Run:

```bash
GOWORK=off go test ./internal/infra/eventbus ./internal/infra/transport/...
git diff --check
```

Expected: PASS.

Commit:

```bash
git add modules/storage/internal/infra/eventbus modules/storage/internal/infra/transport
git commit -m "feat(storage): split nats deriver subscriptions"
```

---

### Task 8: Documentation And End-To-End Verification

**Files:**
- Modify: `modules/storage/README.md`
- Modify: `modules/storage/docs/architecture.md`
- Modify: `modules/storage/tests/e2e/config.go`
- Modify: `modules/storage/tests/README.md`

- [ ] **Step 1: Update storage docs**

In `modules/storage/README.md` and `modules/storage/docs/architecture.md`, add this deployment summary:

```markdown
### Runtime Roles

`moox-storage` uses one binary and three runtime roles.

- `access` accepts user reads and writes, resolves routes, writes PrimaryStore, and publishes row-change events.
- `primary` owns Pebble and serves PrimaryStore RPCs.
- `deriver` consumes row-change events, batches keys, reads current rows through AccessService, and writes DuckDB or Bleve.

The default event bus is NATS. Use `eventbus.type=memory` only for single-process development and tests. Memory mode is asynchronous and does not run derivation inside the write request.
```

Add batch config:

```yaml
storage:
  deriver:
    access_service_name: trpc.storage.access.AccessService
    batch_size: 500
    batch_wait_ms: 200
    max_workers: 4
```

- [ ] **Step 2: Update e2e config to explicit memory single-process roles**

In `modules/storage/tests/e2e/config.go`, ensure generated test config contains:

```yaml
storage:
  roles:
    - access
    - primary
    - deriver
  eventbus:
    type: memory
  deriver:
    access_service_name: ""
    batch_size: 100
    batch_wait_ms: 50
    max_workers: 1
```

- [ ] **Step 3: Run focused tests**

Run:

```bash
GOWORK=off go test ./internal/config ./internal/core/eventbus ./internal/infra/eventbus ./internal/infra/transport/... ./internal/services/deriver ./internal/services/access ./cmd/moox-storage
```

Expected: PASS.

- [ ] **Step 4: Run storage test suite**

Run:

```bash
CGO_ENABLED=1 GOWORK=off go test ./...
```

Expected: PASS. If DuckDB tests fail because CGO is not available, record the exact failure and run:

```bash
GOWORK=off go test ./internal/config ./internal/core/eventbus ./internal/infra/eventbus ./internal/infra/transport/... ./internal/services/deriver ./internal/services/access ./cmd/moox-storage
```

- [ ] **Step 5: Commit docs and e2e config**

Commit:

```bash
git add modules/storage/README.md modules/storage/docs/architecture.md modules/storage/tests/e2e/config.go modules/storage/tests/README.md
git commit -m "docs(storage): document runtime roles and deriver batching"
```
