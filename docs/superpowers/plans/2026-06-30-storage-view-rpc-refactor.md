# Storage View RPC Refactor Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make `moox-storage` support both simple single-process deployment and flexible multi-machine deployment by splitting Access, Primary, and View responsibilities, while making View use RPC for Metadata and Access instead of opening other services' local stores.

**Architecture:** Storage is modeled as three independently deployable roles: `access`, `primary`, and `view`. `access` owns Metadata and Access APIs, writes to `primary`, and publishes row-change events. `view` owns only DuckDB/Bleve local view stores, consumes events through EventBus, reads facts through Access RPC, and reads/writes metadata through Metadata RPC.

**Tech Stack:** Go, tRPC-Go, YAML config, NATS, in-memory EventBus, SQLite metadata store, Pebble PrimaryStore, DuckDB time-series view store, Bleve record view index.

---

## Current Decisions

- Use `view` as the user-facing role name.
- Use `moox-storage-view` as the deployable service name.
- Move `modules/storage/internal/services/deriver` to `modules/storage/internal/services/view/builder`.
- Use `package builder` in the new `services/view/builder` directory.
- Keep existing `modules/storage/internal/services/view` for View projection, rebuild, query, and metadata-facing abstractions.
- Do a hard rename from `deriver` to `view` because this is a new project and no backward compatibility is required.
- Keep `eventbus.type: memory` for same-process `access + view` deployments.
- Require `eventbus.type: nats` when `access` and `view` are deployed as separate processes.
- In a standalone `view` process, do not open Metadata SQLite, Pebble, or Archive/Parquet stores.
- In a standalone `view` process, local stores are only DuckDB and Bleve.

## Target Runtime Topologies

### Single-process simple deployment

```yaml
storage:
  roles:
    - access
    - primary
    - view
  primary:
    service_name: ""
  eventbus:
    type: memory
  view:
    metadata_service_name: ""
    access_service_name: ""
```

Expected behavior:

```text
one process exposes Metadata + Access + PrimaryStore + DataView
one process opens metadata sqlite + pebble + duckdb + bleve
row-change events stay in memory
```

### Two-process deployment

```text
moox-storage-primary: roles=[primary]
moox-storage-access-view: roles=[access,view]
```

Expected behavior:

```yaml
# access-view process
storage:
  roles:
    - access
    - view
  primary:
    service_name: trpc.moox.storage.PrimaryStore
  eventbus:
    type: memory
```

### Three-process deployment

```text
moox-storage-access: roles=[access]
moox-storage-primary: roles=[primary]
moox-storage-view: roles=[view]
```

Expected behavior:

```yaml
# access process
storage:
  roles:
    - access
  primary:
    service_name: trpc.moox.storage.PrimaryStore
  eventbus:
    type: nats

# view process
storage:
  roles:
    - view
  eventbus:
    type: nats
  view:
    metadata_service_name: trpc.moox.storage.Metadata
    access_service_name: trpc.moox.storage.Access
```

---

## File Structure Plan

### New files

- `modules/storage/internal/services/view/metadata.go`
- Defines the narrow metadata interface needed by View query, rebuild, scheduler, and incremental builder.

- `modules/storage/internal/services/view/remote_metadata.go`
- Implements the View metadata interface through `pb.MetadataClientProxy`.

- `modules/storage/internal/services/view/service.go`
- Hosts `pb.DataViewService` implementation after extracting DataView query/rebuild methods out of `access.Service`.

- `modules/storage/internal/services/view/builder/service.go`
- Moved from `modules/storage/internal/services/deriver/service.go`.

- `modules/storage/internal/services/view/builder/options.go`
- Moved from `modules/storage/internal/services/deriver/options.go` and renamed to View Builder terminology.

- `modules/storage/internal/services/view/builder/time_series.go`
- Moved from `modules/storage/internal/services/deriver/time_series.go`.

- `modules/storage/internal/services/view/builder/record.go`
- Moved from `modules/storage/internal/services/deriver/record.go`.

- `modules/storage/internal/services/view/builder/batcher.go`
- Moved from `modules/storage/internal/services/deriver/batcher.go`.

- `modules/storage/internal/services/view/builder/access_reader.go`
- Moved from the current deriver Access reader implementation if that file exists in the old package.

- `modules/storage/internal/services/view/builder/event.go`
- Moved from the current deriver event handling implementation if that file exists in the old package.

### Modified files

- `modules/storage/cmd/moox-storage/main.go`
- Assemble role-specific runtimes without forcing the `view` role to create `access.Service`.

- `modules/storage/cmd/moox-storage/main_test.go`
- Replace `deriver` role tests with `view` role tests and add view-only remote dependency tests.

- `modules/storage/internal/config/loader.go`
- Rename `DeriverConfig` to `ViewConfig`, parse `storage.view`, and default roles to `access, view`.

- `modules/storage/internal/config/loader_test.go`
- Update config loader tests for `view.metadata_service_name`, `view.access_service_name`, and role normalization.

- `modules/storage/config/storage.yaml`
- Replace `roles: [access, deriver]` with `roles: [access, view]`.

- `modules/storage/internal/services/access/service.go`
- Remove DataView-specific ownership from Access service where possible.

- `modules/storage/internal/services/access/query.go`
- Move DataView query methods to `modules/storage/internal/services/view/service.go`.

- `modules/storage/internal/services/view/view_builder.go`
- Replace dependency on `metadata.Store` with the new View metadata interface.

- `modules/storage/internal/services/view/schedule.go`
- Replace dependency on `metadata.Store` with the new View metadata interface.

- `modules/storage/README.md`
- Update role names, deployment examples, and EventBus rules.

- `docs/superpowers/plans/2026-06-30-storage-view-rpc-refactor.md`
- This plan.

### Deleted paths after migration

- `modules/storage/internal/services/deriver`

---

## Task 1: Rename storage config from `deriver` to `view`

**Files:**

- Modify: `modules/storage/internal/config/loader.go`
- Modify: `modules/storage/internal/config/loader_test.go`
- Modify: `modules/storage/config/storage.yaml`
- Modify: `modules/storage/README.md`

- [ ] **Step 1: Replace config struct names**

Change the storage config model from this shape:

```go
type StorageConfig struct {
    Roles   []string      `yaml:"roles"`
    Deriver DeriverConfig `yaml:"deriver"`
}

type DeriverConfig struct {
    AccessServiceName string `yaml:"access_service_name"`
    BatchSize         int    `yaml:"batch_size"`
    BatchWaitMS       int    `yaml:"batch_wait_ms"`
    MaxWorkers        int    `yaml:"max_workers"`
}
```

To this shape:

```go
type StorageConfig struct {
    Roles []string   `yaml:"roles"`
    View  ViewConfig `yaml:"view"`
}

type ViewConfig struct {
    MetadataServiceName string `yaml:"metadata_service_name"`
    AccessServiceName   string `yaml:"access_service_name"`
    BatchSize           int    `yaml:"batch_size"`
    BatchWaitMS         int    `yaml:"batch_wait_ms"`
    MaxWorkers          int    `yaml:"max_workers"`
}
```

- [ ] **Step 2: Update defaults**

Use these defaults in `ApplyDefaults` or the equivalent config normalization function:

```go
if len(c.Roles) == 0 {
    c.Roles = []string{"access", "view"}
}
if c.View.MetadataServiceName == "" {
    c.View.MetadataServiceName = "trpc.moox.storage.Metadata"
}
if c.View.AccessServiceName == "" {
    c.View.AccessServiceName = "trpc.moox.storage.Access"
}
if c.View.BatchSize <= 0 {
    c.View.BatchSize = 500
}
if c.View.BatchWaitMS <= 0 {
    c.View.BatchWaitMS = 200
}
if c.View.MaxWorkers <= 0 {
    c.View.MaxWorkers = 4
}
```

- [ ] **Step 3: Normalize roles**

Make `HasRole("view")` the only View role check used by production code.

```go
func (c StorageConfig) HasRole(role string) bool {
    role = strings.ToLower(strings.TrimSpace(role))
    for _, item := range c.Roles {
        if strings.ToLower(strings.TrimSpace(item)) == role {
            return true
        }
    }
    return false
}
```

- [ ] **Step 4: Update default YAML**

Change `modules/storage/config/storage.yaml` to this shape:

```yaml
storage:
  root: ./var/storage
  roles:
    - access
    - view
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
    type: memory
    nats_url: nats://127.0.0.1:4222
    stream_name: MOOX_STORAGE
    subject_prefix: moox.storage
    consumer_name: storage_view
    embedded:
      enabled: false
  view:
    metadata_service_name: trpc.moox.storage.Metadata
    access_service_name: trpc.moox.storage.Access
    batch_size: 500
    batch_wait_ms: 200
    max_workers: 4
```

- [ ] **Step 5: Update config tests**

Replace old assertions that mention `Deriver` with assertions that mention `View`.

```go
if !cfg.Storage.HasRole("view") {
    t.Fatalf("expected default view role")
}
if cfg.Storage.View.MetadataServiceName != "trpc.moox.storage.Metadata" {
    t.Fatalf("metadata service name = %q", cfg.Storage.View.MetadataServiceName)
}
if cfg.Storage.View.AccessServiceName != "trpc.moox.storage.Access" {
    t.Fatalf("access service name = %q", cfg.Storage.View.AccessServiceName)
}
```

- [ ] **Step 6: Run focused tests**

Run:

```bash
go test ./modules/storage/internal/config
```

Expected:

```text
ok github.com/mooyang-code/moox/modules/storage/internal/config
```

- [ ] **Step 7: Commit**

```bash
git add modules/storage/internal/config/loader.go modules/storage/internal/config/loader_test.go modules/storage/config/storage.yaml modules/storage/README.md
git commit -m "refactor(storage): rename deriver config to view"
```

---

## Task 2: Move `deriver` package to `services/view/builder`

**Files:**

- Create: `modules/storage/internal/services/view/builder/*.go`
- Modify: `modules/storage/cmd/moox-storage/main.go`
- Modify: `modules/storage/cmd/moox-storage/main_test.go`
- Delete: `modules/storage/internal/services/deriver`

- [ ] **Step 1: Move files**

Run:

```bash
mkdir -p modules/storage/internal/services/view/builder
git mv modules/storage/internal/services/deriver/*.go modules/storage/internal/services/view/builder/
rmdir modules/storage/internal/services/deriver
```

Expected:

```text
modules/storage/internal/services/view/builder contains the old deriver files
modules/storage/internal/services/deriver no longer exists
```

- [ ] **Step 2: Rename package declarations**

In every moved file, replace:

```go
package deriver
```

With:

```go
package builder
```

- [ ] **Step 3: Rename user-facing comments**

Replace comments like this:

```go
// Service consumes storage row-change events and updates derived view stores.
```

With:

```go
// Service consumes storage row-change events and updates materialized view stores.
```

- [ ] **Step 4: Update imports**

Replace old imports:

```go
"github.com/mooyang-code/moox/modules/storage/internal/services/deriver"
```

With:

```go
viewbuilder "github.com/mooyang-code/moox/modules/storage/internal/services/view/builder"
```

- [ ] **Step 5: Update runtime type references**

Replace code like this:

```go
var accessReader deriver.AccessReader
```

With:

```go
var accessReader viewbuilder.AccessReader
```

Replace code like this:

```go
service := deriver.NewService(deriver.Options{
    Events: opts.Events,
})
```

With:

```go
service := viewbuilder.NewService(viewbuilder.Options{
    Events: opts.Events,
})
```

- [ ] **Step 6: Run package tests**

Run:

```bash
go test ./modules/storage/internal/services/view/builder
```

Expected:

```text
ok github.com/mooyang-code/moox/modules/storage/internal/services/view/builder
```

- [ ] **Step 7: Commit**

```bash
git add modules/storage/internal/services/view/builder modules/storage/cmd/moox-storage/main.go modules/storage/cmd/moox-storage/main_test.go
git add -u modules/storage/internal/services/deriver
git commit -m "refactor(storage): move deriver service under view builder"
```

---

## Task 3: Define View metadata interface and remote Metadata RPC adapter

**Files:**

- Create: `modules/storage/internal/services/view/metadata.go`
- Create: `modules/storage/internal/services/view/remote_metadata.go`
- Modify: `modules/storage/internal/services/view/view_builder.go`
- Modify: `modules/storage/internal/services/view/schedule.go`
- Modify: `modules/storage/internal/services/view/builder/options.go`
- Modify: `modules/storage/internal/services/view/builder/service.go`
- Modify: `modules/storage/internal/services/view/builder/record.go`
- Modify: `modules/storage/internal/services/view/builder/time_series.go`

- [ ] **Step 1: Add a narrow View metadata interface**

Create `modules/storage/internal/services/view/metadata.go`:

```go
package view

import (
    "context"

    pb "github.com/mooyang-code/moox/modules/storage/proto/gen"
)

// Metadata defines the metadata operations required by View query, rebuild,
// scheduling, and incremental materialization.
type Metadata interface {
    GetView(ctx context.Context, spaceID string, viewID string) (*pb.View, error)
    ListViews(ctx context.Context, spaceID string, datasetID string, status string, page *pb.Page) ([]*pb.View, *pb.PageResult, error)
    ListViewsByDataset(ctx context.Context, spaceID string, datasetID string) ([]*pb.View, error)
    ListViewColumns(ctx context.Context, spaceID string, viewID string, page *pb.Page) ([]*pb.ViewColumn, *pb.PageResult, error)
    ListSpaces(ctx context.Context, owner string, page *pb.Page) ([]*pb.Space, *pb.PageResult, error)
    GetDataset(ctx context.Context, spaceID string, datasetID string) (*pb.Dataset, error)
    UpsertView(ctx context.Context, item *pb.View) (*pb.View, error)
    BeginViewBuild(ctx context.Context, spaceID string, viewID string, targetVersion uint64, resultName string) (*pb.View, error)
    CompleteViewBuild(ctx context.Context, spaceID string, viewID string, targetVersion uint64, resultName string) error
    FailViewBuild(ctx context.Context, spaceID string, viewID string, targetVersion uint64, resultName string, buildErr error) error
}
```

- [ ] **Step 2: Replace `metadata.Store` in View builder options**

Change `modules/storage/internal/services/view/view_builder.go` from:

```go
type BuilderOptions struct {
    Metadata metadata.Store
}
```

To:

```go
type BuilderOptions struct {
    Metadata Metadata
}
```

Update the `Builder` field from:

```go
metadata metadata.Store
```

To:

```go
metadata Metadata
```

- [ ] **Step 3: Replace builder package metadata dependency**

In `modules/storage/internal/services/view/builder/options.go`, replace:

```go
Metadata       metadata.Store
MetadataReader metadata.Reader
```

With:

```go
Metadata view.Metadata
```

Then update `Service` fields in `service.go` from:

```go
metadata       metadata.Store
metadataReader metadata.Reader
```

To:

```go
metadata view.Metadata
```

- [ ] **Step 4: Update incremental builder calls**

Replace code like this:

```go
views, err := s.metadataReader.ListViewsByDataset(ctx, key.spaceID, key.datasetID)
columns, _, err := s.metadataReader.ListViewColumns(ctx, item.GetSpaceId(), item.GetViewId(), &pb.Page{Size: 10000})
```

With:

```go
views, err := s.metadata.ListViewsByDataset(ctx, key.spaceID, key.datasetID)
columns, _, err := s.metadata.ListViewColumns(ctx, item.GetSpaceId(), item.GetViewId(), &pb.Page{Size: 10000})
```

- [ ] **Step 5: Add remote Metadata adapter**

Create `modules/storage/internal/services/view/remote_metadata.go`:

```go
package view

import (
    "context"
    "fmt"

    pb "github.com/mooyang-code/moox/modules/storage/proto/gen"
    "trpc.group/trpc-go/trpc-go/client"
)

// RemoteMetadata implements Metadata by calling the Metadata tRPC service.
type RemoteMetadata struct {
    proxy pb.MetadataClientProxy
}

func NewRemoteMetadata(serviceName string, opts ...client.Option) *RemoteMetadata {
    if serviceName != "" {
        opts = append([]client.Option{client.WithServiceName(serviceName)}, opts...)
    }
    return &RemoteMetadata{proxy: pb.NewMetadataClientProxy(opts...)}
}

func retInfoErr(ret *pb.RetInfo) error {
    if ret == nil || ret.GetCode() == 0 {
        return nil
    }
    return fmt.Errorf("metadata rpc failed: code=%d msg=%s", ret.GetCode(), ret.GetMsg())
}
```

- [ ] **Step 6: Implement required remote methods**

Implement these methods on `RemoteMetadata` using `pb.MetadataClientProxy`:

```go
func (m *RemoteMetadata) GetView(ctx context.Context, spaceID string, viewID string) (*pb.View, error)
func (m *RemoteMetadata) ListViews(ctx context.Context, spaceID string, datasetID string, status string, page *pb.Page) ([]*pb.View, *pb.PageResult, error)
func (m *RemoteMetadata) ListViewsByDataset(ctx context.Context, spaceID string, datasetID string) ([]*pb.View, error)
func (m *RemoteMetadata) ListViewColumns(ctx context.Context, spaceID string, viewID string, page *pb.Page) ([]*pb.ViewColumn, *pb.PageResult, error)
func (m *RemoteMetadata) ListSpaces(ctx context.Context, owner string, page *pb.Page) ([]*pb.Space, *pb.PageResult, error)
func (m *RemoteMetadata) GetDataset(ctx context.Context, spaceID string, datasetID string) (*pb.Dataset, error)
func (m *RemoteMetadata) UpsertView(ctx context.Context, item *pb.View) (*pb.View, error)
func (m *RemoteMetadata) BeginViewBuild(ctx context.Context, spaceID string, viewID string, targetVersion uint64, resultName string) (*pb.View, error)
func (m *RemoteMetadata) CompleteViewBuild(ctx context.Context, spaceID string, viewID string, targetVersion uint64, resultName string) error
func (m *RemoteMetadata) FailViewBuild(ctx context.Context, spaceID string, viewID string, targetVersion uint64, resultName string, buildErr error) error
```

Use this pattern for list-style methods:

```go
rsp, err := m.proxy.ListViews(ctx, &pb.ListViewsReq{
    SpaceId:   spaceID,
    DatasetId: datasetID,
    Status:    status,
    Page:      page,
})
if err != nil {
    return nil, nil, err
}
if err := retInfoErr(rsp.GetRetInfo()); err != nil {
    return nil, nil, err
}
return rsp.GetViews(), rsp.GetPageResult(), nil
```

Use this pattern for state updates:

```go
rsp, err := m.proxy.CompleteViewBuild(ctx, &pb.CompleteViewBuildReq{
    SpaceId:       spaceID,
    ViewId:        viewID,
    TargetVersion: targetVersion,
    ResultName:    resultName,
})
if err != nil {
    return err
}
return retInfoErr(rsp.GetRetInfo())
```

- [ ] **Step 7: Run focused tests**

Run:

```bash
go test ./modules/storage/internal/services/view ./modules/storage/internal/services/view/builder
```

Expected:

```text
ok github.com/mooyang-code/moox/modules/storage/internal/services/view
ok github.com/mooyang-code/moox/modules/storage/internal/services/view/builder
```

- [ ] **Step 8: Commit**

```bash
git add modules/storage/internal/services/view modules/storage/internal/services/view/builder
git commit -m "feat(storage): add rpc metadata adapter for view"
```

---

## Task 4: Extract DataView service out of Access service

**Files:**

- Create: `modules/storage/internal/services/view/service.go`
- Modify: `modules/storage/internal/services/access/service.go`
- Modify: `modules/storage/internal/services/access/query.go`
- Modify: `modules/storage/internal/services/view/view_builder.go`
- Modify: `modules/storage/cmd/moox-storage/main.go`

- [ ] **Step 1: Create View service type**

Create `modules/storage/internal/services/view/service.go`:

```go
package view

import (
    deviceduckdb "github.com/mooyang-code/moox/modules/storage/internal/infra/device/duckdb"
    "github.com/mooyang-code/moox/modules/storage/internal/services/search"
    pb "github.com/mooyang-code/moox/modules/storage/proto/gen"
)

var _ pb.DataViewService = (*Service)(nil)

type Service struct {
    metadata Metadata
    views    *deviceduckdb.ViewStore
    search   *search.Service
    builder  *Builder
}

type ServiceOptions struct {
    Metadata Metadata
    Views    *deviceduckdb.ViewStore
    Search   *search.Service
    Builder  *Builder
}

func NewService(opts ServiceOptions) *Service {
    return &Service{
        metadata: opts.Metadata,
        views:    opts.Views,
        search:   opts.Search,
        builder:  opts.Builder,
    }
}
```

- [ ] **Step 2: Move DataView query methods**

Move these methods from `modules/storage/internal/services/access/query.go` to `modules/storage/internal/services/view/service.go` and update receivers from `*access.Service` to `*view.Service`:

```go
func (s *Service) QueryTimeSeriesRows(ctx context.Context, req *pb.QueryTimeSeriesRowsReq) (*pb.QueryTimeSeriesRowsRsp, error)
func (s *Service) SearchRecordRows(ctx context.Context, req *pb.SearchRecordRowsReq) (*pb.SearchRecordRowsRsp, error)
func (s *Service) RebuildTimeSeriesView(ctx context.Context, req *pb.RebuildTimeSeriesViewReq) (*pb.RebuildTimeSeriesViewRsp, error)
func (s *Service) RebuildRecordView(ctx context.Context, req *pb.RebuildRecordViewReq) (*pb.RebuildRecordViewRsp, error)
```

- [ ] **Step 3: Replace internal field access**

In moved methods, replace Access-service field references like this:

```go
s.metadataReader
s.viewStore()
s.search
```

With View-service dependencies like this:

```go
s.metadata
s.views
s.search
```

- [ ] **Step 4: Keep Access service focused**

After moving DataView methods, keep Access service implementing only:

```go
var _ pb.MetadataService = (*Service)(nil)
var _ pb.AccessService = (*Service)(nil)
```

Remove this assertion from Access service:

```go
var _ pb.DataViewService = (*Service)(nil)
```

- [ ] **Step 5: Run service tests**

Run:

```bash
go test ./modules/storage/internal/services/access ./modules/storage/internal/services/view
```

Expected:

```text
ok github.com/mooyang-code/moox/modules/storage/internal/services/access
ok github.com/mooyang-code/moox/modules/storage/internal/services/view
```

- [ ] **Step 6: Commit**

```bash
git add modules/storage/internal/services/access modules/storage/internal/services/view modules/storage/cmd/moox-storage/main.go
git commit -m "refactor(storage): extract data view service from access"
```

---

## Task 5: Refactor `moox-storage` role assembly

**Files:**

- Modify: `modules/storage/cmd/moox-storage/main.go`
- Modify: `modules/storage/cmd/moox-storage/main_test.go`

- [ ] **Step 1: Define role creation rules**

Update helper functions to use these rules:

```go
func shouldCreateAccessService(storage storageconfig.StorageConfig) bool {
    return storage.HasRole("access")
}

func shouldCreateViewService(storage storageconfig.StorageConfig) bool {
    return storage.HasRole("view")
}

func shouldCreatePrimaryService(storage storageconfig.StorageConfig) bool {
    return storage.HasRole("primary") || (storage.HasRole("access") && storage.Primary.ServiceName == "")
}
```

- [ ] **Step 2: Create Access only for access role**

Change main startup so `access.NewServiceWithOptions` runs only when `storage.HasRole("access")` is true.

```go
var accessService *access.Service
if storage.HasRole("access") {
    accessService = access.NewServiceWithOptions(access.Options{
        Root:               storage.Root,
        MetadataPath:       storage.Metadata.Path,
        PebblePath:         storage.Devices.PebblePath,
        DuckDBPath:         storage.Devices.DuckDBPath,
        BlevePath:          storage.Devices.BlevePath,
        ParquetPath:        storage.Devices.ParquetPath,
        PrimaryServiceName: storage.Primary.ServiceName,
        Events:             events,
    })
    pb.RegisterMetadataService(s.Service("trpc.moox.storage.Metadata"), accessService)
    pb.RegisterAccessService(s.Service("trpc.moox.storage.Access"), accessService)
}
```

- [ ] **Step 3: Create View runtime without Access service**

When `storage.HasRole("view")` is true, open only DuckDB and Bleve local stores plus local or remote metadata.

```go
viewMetadata := metadataForViewRuntime(storage, accessService)
viewStore, err := openViewStore(storage)
if err != nil {
    return err
}
searchService := search.NewService(search.Options{
    Root:      storage.Root,
    BlevePath: storage.Devices.BlevePath,
    Metadata:  viewMetadata,
})
viewBuilder := view.NewBuilder(view.BuilderOptions{
    Metadata: viewMetadata,
    Views:    viewStore,
    Search:   searchService,
    Reader:   viewFactReader,
})
viewService := view.NewService(view.ServiceOptions{
    Metadata: viewMetadata,
    Views:    viewStore,
    Search:   searchService,
    Builder:  viewBuilder,
})
pb.RegisterDataViewService(s.Service("trpc.moox.storage.DataView"), viewService)
```

- [ ] **Step 4: Select Metadata dependency**

Add helper logic like this:

```go
func metadataForViewRuntime(storage storageconfig.StorageConfig, accessService *access.Service) view.Metadata {
    if accessService != nil {
        return accessService.MetadataStore()
    }
    return view.NewRemoteMetadata(storage.View.MetadataServiceName)
}
```

Expected behavior:

```text
roles=[access,view] uses local metadata store
roles=[view] uses Metadata RPC
```

- [ ] **Step 5: Select Access fact reader**

Use local Access reader only when Access is in the same process.

```go
func accessReaderForViewRuntime(storage storageconfig.StorageConfig, accessService *access.Service) viewbuilder.AccessReader {
    var local viewbuilder.AccessReader
    serviceName := storage.View.AccessServiceName
    if accessService != nil {
        local = accessService
        serviceName = ""
    }
    return viewbuilder.NewAccessReader(local, serviceName)
}
```

Expected behavior:

```text
roles=[access,view] reads facts locally
roles=[view] reads facts through trpc.moox.storage.Access
```

- [ ] **Step 6: Enforce EventBus rule**

Add startup validation:

```go
func validateViewDeployment(storage storageconfig.StorageConfig) error {
    if storage.HasRole("view") && !storage.HasRole("access") && strings.EqualFold(storage.EventBus.Type, "memory") {
        return errors.New("storage view role requires non-memory eventbus when access role is not in the same process")
    }
    return nil
}
```

- [ ] **Step 7: Update role tests**

Add these tests to `modules/storage/cmd/moox-storage/main_test.go`:

```go
func TestViewOnlyUsesRemoteMetadataAndAccess(t *testing.T) {
    cfg := storageconfig.StorageConfig{
        Roles: []string{"view"},
        View: storageconfig.ViewConfig{
            MetadataServiceName: "trpc.moox.storage.Metadata",
            AccessServiceName:   "trpc.moox.storage.Access",
        },
        EventBus: storageconfig.EventBusConfig{Type: "nats"},
    }
    if shouldCreateAccessService(cfg) {
        t.Fatalf("view-only role must not create access service")
    }
    if !shouldCreateViewService(cfg) {
        t.Fatalf("view-only role must create view service")
    }
}

func TestViewOnlyRejectsMemoryEventBus(t *testing.T) {
    cfg := storageconfig.StorageConfig{
        Roles:    []string{"view"},
        EventBus: storageconfig.EventBusConfig{Type: "memory"},
    }
    if err := validateViewDeployment(cfg); err == nil {
        t.Fatalf("expected view-only memory eventbus to be rejected")
    }
}
```

- [ ] **Step 8: Run startup tests**

Run:

```bash
go test ./modules/storage/cmd/moox-storage
```

Expected:

```text
ok github.com/mooyang-code/moox/modules/storage/cmd/moox-storage
```

- [ ] **Step 9: Commit**

```bash
git add modules/storage/cmd/moox-storage/main.go modules/storage/cmd/moox-storage/main_test.go
git commit -m "feat(storage): support standalone view role"
```

---

## Task 6: Update View Builder to use RPC dependencies in standalone mode

**Files:**

- Modify: `modules/storage/internal/services/view/builder/options.go`
- Modify: `modules/storage/internal/services/view/builder/service.go`
- Modify: `modules/storage/internal/services/view/builder/record.go`
- Modify: `modules/storage/internal/services/view/builder/time_series.go`
- Modify: `modules/storage/internal/services/view/builder/service_test.go`

- [ ] **Step 1: Update builder options**

Use this shape:

```go
type Options struct {
    Events     eventbus.Bus
    Reader     AccessReader
    Metadata   view.Metadata
    Views      TimeSeriesViewWriter
    Search     RecordViewIndexer
    BatchSize  int
    BatchWait  time.Duration
    MaxWorkers int
}
```

- [ ] **Step 2: Update builder start validation**

Use this validation:

```go
if s.reader == nil {
    return errors.New("view builder requires access reader")
}
if s.metadata == nil {
    return errors.New("view builder requires metadata client")
}
if s.views == nil {
    return errors.New("view builder requires time-series view writer")
}
if s.search == nil {
    return errors.New("view builder requires record view indexer")
}
```

- [ ] **Step 3: Update tests to use fake View metadata**

Replace old fake metadata embedding:

```go
type fakeDeriverMetadata struct {
    metadata.Store
}
```

With:

```go
type fakeViewMetadata struct {
    views   map[string][]*pb.View
    columns map[string][]*pb.ViewColumn
}
```

Implement the methods used by builder tests:

```go
func (m *fakeViewMetadata) ListViewsByDataset(_ context.Context, spaceID string, datasetID string) ([]*pb.View, error) {
    return m.views[spaceID+"/"+datasetID], nil
}

func (m *fakeViewMetadata) ListViewColumns(_ context.Context, spaceID string, viewID string, _ *pb.Page) ([]*pb.ViewColumn, *pb.PageResult, error) {
    return m.columns[spaceID+"/"+viewID], &pb.PageResult{}, nil
}

func (m *fakeViewMetadata) UpsertView(_ context.Context, item *pb.View) (*pb.View, error) {
    return item, nil
}
```

- [ ] **Step 4: Run builder tests**

Run:

```bash
go test ./modules/storage/internal/services/view/builder
```

Expected:

```text
ok github.com/mooyang-code/moox/modules/storage/internal/services/view/builder
```

- [ ] **Step 5: Commit**

```bash
git add modules/storage/internal/services/view/builder
git commit -m "refactor(storage): make view builder use view metadata interface"
```

---

## Task 7: Update service deployment model docs and seed data expectations

**Files:**

- Modify: `modules/storage/README.md`
- Modify: service deployment seed or migration files if they exist under `modules/admin`, `modules/moox`, `modules/*/schema`, or project migrations.

- [ ] **Step 1: Document process-level service records**

Use process records instead of interface-level records:

```text
moox-admin
moox-web-host
moox-storage-access
moox-storage-primary
moox-storage-view
moox-collector-scf
moox-factor
moox-trade
```

- [ ] **Step 2: Document monolith service record**

For simple deployments, store one process record:

```json
{
  "service_name": "moox-storage-monolith",
  "service_kind": "storage",
  "roles": ["access", "primary", "view"],
  "endpoints": {
    "metadata_http": "http://106.53.107.122:20200",
    "metadata_trpc": "trpc://106.53.107.122:20100",
    "primary_trpc": "trpc://106.53.107.122:20101",
    "access_http": "http://106.53.107.122:20201",
    "access_trpc": "trpc://106.53.107.122:20102",
    "view_http": "http://106.53.107.122:20202",
    "view_trpc": "trpc://106.53.107.122:20103"
  }
}
```

- [ ] **Step 3: Document three-process service records**

Use these records for multi-machine deployment:

```json
{
  "service_name": "moox-storage-access",
  "service_kind": "storage_access",
  "roles": ["access"],
  "endpoints": {
    "metadata_http": "http://106.53.107.122:20200",
    "metadata_trpc": "trpc://106.53.107.122:20100",
    "access_http": "http://106.53.107.122:20201",
    "access_trpc": "trpc://106.53.107.122:20102"
  }
}
```

```json
{
  "service_name": "moox-storage-primary",
  "service_kind": "storage_primary",
  "roles": ["primary"],
  "endpoints": {
    "primary_trpc": "trpc://106.53.107.122:20101"
  }
}
```

```json
{
  "service_name": "moox-storage-view",
  "service_kind": "storage_view",
  "roles": ["view"],
  "endpoints": {
    "view_http": "http://106.53.107.122:20202",
    "view_trpc": "trpc://106.53.107.122:20103"
  }
}
```

- [ ] **Step 4: Document SCF dependency selection**

SCF should consume these endpoints from service deployment data:

```text
Metadata RPC or HTTP: from moox-storage-access endpoints.metadata_http or endpoints.metadata_trpc
Access RPC or HTTP: from moox-storage-access endpoints.access_http or endpoints.access_trpc
Storage direct write: prefer Access endpoint unless SCF intentionally writes directly to storage Access tRPC
```

- [ ] **Step 5: Commit**

```bash
git add modules/storage/README.md
git add modules/admin modules/moox modules/*/schema 2>/dev/null || true
git commit -m "docs(storage): document process-level storage deployments"
```

---

## Task 8: Update docs and deployment examples

**Files:**

- Modify: `modules/storage/README.md`
- Modify: `docs` files that mention `deriver` after searching with `rg -n "deriver|Deriver"`.

- [ ] **Step 1: Replace terminology**

Replace user-facing text:

```text
deriver
```

With:

```text
view
```

Replace internal implementation text:

```text
deriver service
```

With:

```text
view builder service
```

- [ ] **Step 2: Add EventBus deployment rule**

Add this wording to storage docs:

```markdown
When `access` and `view` run in the same process, `eventbus.type: memory` is supported.
When `access` and `view` run in separate processes, use `eventbus.type: nats` and configure the same `nats_url`, `stream_name`, and `subject_prefix` for both processes.
```

- [ ] **Step 3: Add local store ownership table**

Add this table:

```markdown
| Process | Local stores |
|---|---|
| moox-storage-access | metadata SQLite, archive/parquet when archive is enabled |
| moox-storage-primary | Pebble |
| moox-storage-view | DuckDB, Bleve |
```

- [ ] **Step 4: Run documentation grep**

Run:

```bash
rg -n "deriver|Deriver" modules/storage docs
```

Expected:

```text
No user-facing deriver references remain outside changelog or migration notes.
```

- [ ] **Step 5: Commit**

```bash
git add modules/storage/README.md docs
git commit -m "docs(storage): rename deriver role to view"
```

---

## Task 9: End-to-end validation matrix

**Files:**

- No source files are required for this task unless a test exposes a defect.

- [ ] **Step 1: Run storage unit tests**

Run:

```bash
go test ./modules/storage/...
```

Expected:

```text
all storage packages pass
```

- [ ] **Step 2: Validate single-process startup**

Use config:

```yaml
storage:
  roles:
    - access
    - primary
    - view
  eventbus:
    type: memory
  primary:
    service_name: ""
```

Run the normal `moox-storage` binary startup command used by the project.

Expected:

```text
Metadata service registered
Access service registered
PrimaryStore service registered
DataView service registered
view builder started with memory eventbus
```

- [ ] **Step 3: Validate three-process config rules**

Use config:

```yaml
storage:
  roles:
    - view
  eventbus:
    type: memory
```

Expected:

```text
startup fails with "storage view role requires non-memory eventbus when access role is not in the same process"
```

- [ ] **Step 4: Validate view-only NATS startup**

Use config:

```yaml
storage:
  roles:
    - view
  eventbus:
    type: nats
    nats_url: nats://127.0.0.1:4222
  view:
    metadata_service_name: trpc.moox.storage.Metadata
    access_service_name: trpc.moox.storage.Access
```

Expected:

```text
DataView service registered
view builder started with NATS eventbus
metadata sqlite is not opened by this process
pebble is not opened by this process
```

- [ ] **Step 5: Validate Kline path**

Run one SCF Kline collection flow or equivalent local write flow.

Expected:

```text
SCF writes Kline rows through Storage Access
Access publishes rows_changed event
View builder consumes event
View query returns latest Kline rows from DuckDB
```

- [ ] **Step 6: Commit validation fixes**

If validation requires code changes, commit them with a narrow message:

```bash
git add <changed-files>
git commit -m "fix(storage): stabilize view rpc deployment"
```

---

## Task 10: Remote rollout plan

**Files:**

- Modify deployment scripts or service config files used by the project if they reference `deriver`.

- [ ] **Step 1: Roll out simple single-process config first**

Use this config on the current remote machine:

```yaml
storage:
  roles:
    - access
    - primary
    - view
  eventbus:
    type: memory
  primary:
    service_name: ""
```

Expected:

```text
current behavior is preserved
management UI can query binance_spot_kline
SCF writes continue to succeed
```

- [ ] **Step 2: Add process-level deployment data**

Record current single-process storage as:

```text
service_name=moox-storage-monolith
roles=access,primary,view
```

Expected endpoints:

```text
metadata_http=http://106.53.107.122:20200
metadata_trpc=trpc://106.53.107.122:20100
primary_trpc=trpc://106.53.107.122:20101
access_http=http://106.53.107.122:20201
access_trpc=trpc://106.53.107.122:20102
view_http=http://106.53.107.122:20202
view_trpc=trpc://106.53.107.122:20103
```

- [ ] **Step 3: Prepare multi-process config templates**

Create config examples for:

```text
storage-access.yaml
storage-primary.yaml
storage-view.yaml
```

Expected:

```text
operators can split storage by changing config files, without code changes
```

- [ ] **Step 4: Commit deployment docs**

```bash
git add docs modules/storage/README.md
git commit -m "docs(storage): add storage deployment rollout plan"
```

---

## Acceptance Criteria

- `roles: [view]` starts a View process without creating `access.Service`.
- `roles: [view]` does not open metadata SQLite.
- `roles: [view]` does not open Pebble.
- `roles: [view]` owns DuckDB and Bleve local stores.
- `roles: [access]` owns Metadata and Access APIs.
- `roles: [primary]` owns PrimaryStore and Pebble.
- `roles: [access,primary,view]` supports single-process deployment with `eventbus.type: memory`.
- Split `access` and `view` deployment requires `eventbus.type: nats`.
- User-facing config and docs use `view`, not `deriver`.
- Internal event-consuming package is located at `modules/storage/internal/services/view/builder`.
- SCF can continue writing Kline rows through Storage Access.
- DataView can query latest Kline rows after View Builder consumes row-change events.

## Implementation Notes

- Do not introduce a separate `metadata` process in this change.
- Do not split `view-builder` and `view-query` into separate services in this change.
- Do not keep `deriver` as a compatibility alias unless a deployment script fails during rollout.
- Prefer narrow interfaces over passing `metadata.Store` into View code.
- Prefer process-level deployment records over interface-level deployment records.
- Keep tRPC service names stable: `trpc.moox.storage.Metadata`, `trpc.moox.storage.Access`, `trpc.moox.storage.PrimaryStore`, and `trpc.moox.storage.DataView`.
