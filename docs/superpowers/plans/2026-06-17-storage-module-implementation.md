# moox Storage Module Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 将 `/Users/mooyang/Documents/go/src/github.com/mooyang-code/moox/modules/storage` 从临时的内存元数据和 JSONL 文件存储，补齐为基于 SQLite 元数据、StorageRoute、StorageNode、Pebble、DuckDB、Bleve 和 Parquet 的量化数据存储模块。

**Architecture:** `moox-storage` 接入层只处理协议、校验和路由；`StorageRoute` 把在线事实主存写入路由到 `StorageNode`；`StorageNode` 是 adapter 存储代理节点，包裹一组底层 `Device`。Pebble 是事实主存，DuckDB、Bleve 和 Parquet 都从 Pebble 变更事件异步派生，用户按 `DataSet` 写入/读取事实数据，按 `View` 查询物化结果。

**Tech Stack:** Go 1.24、tRPC-Go、Protocol Buffers、SQLite、Pebble、DuckDB、Bleve、Parquet、NATS、YAML、Makefile、pnpm/vue-tsc。

---

## File Structure

**Primary module root:**

- `/Users/mooyang/Documents/go/src/github.com/mooyang-code/moox/modules/storage`

**Reference source:**

- `/Users/mooyang/Documents/go/src/github.com/mooyang-code/xData-mini/storage`

**Existing design documents to keep aligned:**

- `/Users/mooyang/Documents/go/src/github.com/mooyang-code/moox/docs/storage-concepts-and-design-intent.md`
- `/Users/mooyang/Documents/go/src/github.com/mooyang-code/moox/docs/storage-target-architecture-and-metadata.md`
- `/Users/mooyang/Documents/go/src/github.com/mooyang-code/moox/docs/pb-protocol-redesign.md`
- `/Users/mooyang/Documents/go/src/github.com/mooyang-code/moox/schema/storage_metadata.sql`

**Create packages:**

- `/Users/mooyang/Documents/go/src/github.com/mooyang-code/moox/modules/storage/internal/services/metadata`
- `/Users/mooyang/Documents/go/src/github.com/mooyang-code/moox/modules/storage/internal/services/metadata/sqlite`
- `/Users/mooyang/Documents/go/src/github.com/mooyang-code/moox/modules/storage/internal/services/schema`
- `/Users/mooyang/Documents/go/src/github.com/mooyang-code/moox/modules/storage/internal/services/router`
- `/Users/mooyang/Documents/go/src/github.com/mooyang-code/moox/modules/storage/internal/services/adapter`
- `/Users/mooyang/Documents/go/src/github.com/mooyang-code/moox/modules/storage/internal/services/device`
- `/Users/mooyang/Documents/go/src/github.com/mooyang-code/moox/modules/storage/internal/services/device/pebble`
- `/Users/mooyang/Documents/go/src/github.com/mooyang-code/moox/modules/storage/internal/services/device/duckdb`
- `/Users/mooyang/Documents/go/src/github.com/mooyang-code/moox/modules/storage/internal/services/device/bleve`
- `/Users/mooyang/Documents/go/src/github.com/mooyang-code/moox/modules/storage/internal/services/device/parquet`
- `/Users/mooyang/Documents/go/src/github.com/mooyang-code/moox/modules/storage/internal/services/changefeed`
- `/Users/mooyang/Documents/go/src/github.com/mooyang-code/moox/modules/storage/internal/services/materializer`
- `/Users/mooyang/Documents/go/src/github.com/mooyang-code/moox/modules/storage/internal/services/archive`

**Keep as protocol handler package:**

- `/Users/mooyang/Documents/go/src/github.com/mooyang-code/moox/modules/storage/internal/services/storage`

**Repurpose package:**

- `/Users/mooyang/Documents/go/src/github.com/mooyang-code/moox/modules/storage/pkg/quantstore`

`pkg/quantstore` will keep reusable response helpers and typed value helpers. It will stop being the JSONL physical store.

**Remove during implementation:**

- JSONL fact files produced by the current `pkg/quantstore.Store`
- CSV cold storage behavior in storage core
- in-memory metadata maps as the durable source of truth

**External dependencies to add to `/Users/mooyang/Documents/go/src/github.com/mooyang-code/moox/modules/storage/go.mod`:**

- `github.com/cockroachdb/pebble`
- `github.com/blevesearch/bleve/v2`
- `github.com/marcboeker/go-duckdb/v2`
- `github.com/parquet-go/parquet-go`
- `modernc.org/sqlite`

---

## Implementation Tasks

### Task 0: Baseline And Guardrails

**Files:**

- Read: `/Users/mooyang/Documents/go/src/github.com/mooyang-code/moox/modules/storage/go.mod`
- Read: `/Users/mooyang/Documents/go/src/github.com/mooyang-code/moox/modules/storage/internal/services/storage/service.go`
- Read: `/Users/mooyang/Documents/go/src/github.com/mooyang-code/moox/modules/storage/pkg/quantstore/store.go`
- Modify: `/Users/mooyang/Documents/go/src/github.com/mooyang-code/moox/modules/storage/internal/services/protocol_contract_test.go`
- Modify: `/Users/mooyang/Documents/go/src/github.com/mooyang-code/moox/modules/storage/internal/services/storage_metadata_schema_test.go`

- [ ] **Step 0.1: Record current worktree**

  Run:

  ```bash
  cd /Users/mooyang/Documents/go/src/github.com/mooyang-code/moox
  git status --short
  ```

  Expected: output is recorded in the implementation log. Existing user changes are not reverted.

- [ ] **Step 0.2: Run baseline tests**

  Run:

  ```bash
  cd /Users/mooyang/Documents/go/src/github.com/mooyang-code/moox
  go test ./modules/storage/...
  go test ./modules/storage/proto/gen/...
  pnpm --dir web exec vue-tsc --noEmit
  ```

  Expected: current baseline result is recorded. If one command fails before implementation starts, keep the failure text and do not hide it.

- [ ] **Step 0.3: Add a contract test that forbids JSONL as storage core**

  Add this assertion to `TestStorageProtocolUsesCanonicalSurface` or a new storage architecture contract test:

  ```go
  requireNoProjectSourceContains(t, root, "facts/")
  requireNoProjectSourceContains(t, root, ".jsonl")
  requireNoProjectSourceContains(t, root, "CSVImportOptions")
  ```

  Keep generated files excluded the same way current contract helpers exclude generated-only artifacts.

- [ ] **Step 0.4: Run the contract test and confirm RED**

  Run:

  ```bash
  cd /Users/mooyang/Documents/go/src/github.com/mooyang-code/moox
  go test ./modules/storage/internal/services -run TestStorageProtocolUsesCanonicalSurface -count=1
  ```

  Expected: FAIL because `pkg/quantstore/store.go` still contains JSONL path logic and CSV import helpers.

- [ ] **Step 0.5: Commit the RED test**

  Run:

  ```bash
  cd /Users/mooyang/Documents/go/src/github.com/mooyang-code/moox
  git add modules/storage/internal/services/protocol_contract_test.go
  git commit -m "test: forbid jsonl storage core"
  ```

  Expected: commit succeeds, or the implementer records that commit is delayed because unrelated user changes share the same file.

---

### Task 1: Metadata Store Backed By SQLite

**Files:**

- Create: `/Users/mooyang/Documents/go/src/github.com/mooyang-code/moox/modules/storage/internal/services/metadata/store.go`
- Create: `/Users/mooyang/Documents/go/src/github.com/mooyang-code/moox/modules/storage/internal/services/metadata/sqlite/store.go`
- Create: `/Users/mooyang/Documents/go/src/github.com/mooyang-code/moox/modules/storage/internal/services/metadata/sqlite/store_test.go`
- Modify: `/Users/mooyang/Documents/go/src/github.com/mooyang-code/moox/modules/storage/internal/services/storage/service.go`
- Use schema: `/Users/mooyang/Documents/go/src/github.com/mooyang-code/moox/schema/storage_metadata.sql`

- [ ] **Step 1.1: Write failing SQLite schema load test**

  Create `metadata/sqlite/store_test.go` with this test:

  ```go
  package sqlite_test

  import (
  	"context"
  	"path/filepath"
  	"testing"

  	"github.com/mooyang-code/moox/modules/storage/internal/services/metadata/sqlite"
  	"github.com/stretchr/testify/require"
  )

  func TestStoreInitializesStorageMetadataSchema(t *testing.T) {
  	ctx := context.Background()
  	dbPath := filepath.Join(t.TempDir(), "storage_metadata.db")
  	schemaPath := "/Users/mooyang/Documents/go/src/github.com/mooyang-code/moox/schema/storage_metadata.sql"

  	store, err := sqlite.Open(ctx, sqlite.Options{Path: dbPath, SchemaPath: schemaPath})
  	require.NoError(t, err)
  	defer store.Close()

  	require.NoError(t, store.InitSchema(ctx))

  	tables, err := store.TableNames(ctx)
  	require.NoError(t, err)
  	require.Contains(t, tables, "t_spaces")
  	require.Contains(t, tables, "t_storage_nodes")
  	require.Contains(t, tables, "t_storage_devices")
  	require.Contains(t, tables, "t_storage_routes")
  }
  ```

- [ ] **Step 1.2: Run test and confirm RED**

  Run:

  ```bash
  cd /Users/mooyang/Documents/go/src/github.com/mooyang-code/moox
  go test ./modules/storage/internal/services/metadata/sqlite -run TestStoreInitializesStorageMetadataSchema -count=1
  ```

  Expected: FAIL because package and `sqlite.Open` do not exist.

- [ ] **Step 1.3: Define metadata store interfaces**

  Create `metadata/store.go`:

  ```go
  package metadata

  import (
  	"context"

  	pb "github.com/mooyang-code/moox/modules/storage/proto/gen"
  )

  type Store interface {
  	Close() error
  	InitSchema(ctx context.Context) error
  	TableNames(ctx context.Context) ([]string, error)

  	UpsertSpace(ctx context.Context, space *pb.Space) (*pb.Space, error)
  	GetSpace(ctx context.Context, spaceID string) (*pb.Space, error)
  	ListSpaces(ctx context.Context, owner string, page *pb.Page) ([]*pb.Space, *pb.PageResult, error)

  	UpsertDataSource(ctx context.Context, item *pb.DataSource) (*pb.DataSource, error)
  	GetDataSource(ctx context.Context, spaceID string, dataSourceID string) (*pb.DataSource, error)

  	UpsertSubject(ctx context.Context, item *pb.Subject) (*pb.Subject, error)
  	GetSubject(ctx context.Context, spaceID string, subjectID string) (*pb.Subject, error)
  	UpsertSubjectSymbol(ctx context.Context, item *pb.SubjectSymbol) (*pb.SubjectSymbol, error)

  	UpsertDataSet(ctx context.Context, item *pb.DataSet) (*pb.DataSet, error)
  	GetDataSet(ctx context.Context, spaceID string, datasetID string) (*pb.DataSet, error)
  	BindDataSetSubject(ctx context.Context, item *pb.DataSetSubject) (*pb.DataSetSubject, error)
  	ListDataSetSubjects(ctx context.Context, spaceID string, datasetID string) ([]*pb.DataSetSubject, error)

  	UpsertField(ctx context.Context, item *pb.Field) (*pb.Field, error)
  	GetField(ctx context.Context, spaceID string, fieldID string) (*pb.Field, error)
  	UpsertFactor(ctx context.Context, item *pb.Factor) (*pb.Factor, error)
  	GetFactor(ctx context.Context, spaceID string, factorID string) (*pb.Factor, error)
  	UpsertDataSetColumn(ctx context.Context, item *pb.DataSetColumn) (*pb.DataSetColumn, error)
  	ListDataSetColumns(ctx context.Context, spaceID string, datasetID string, textIndexedOnly bool, page *pb.Page) ([]*pb.DataSetColumn, *pb.PageResult, error)

  	UpsertView(ctx context.Context, item *pb.View) (*pb.View, error)
  	GetView(ctx context.Context, spaceID string, viewID string) (*pb.View, error)
  	ListViews(ctx context.Context, spaceID string, datasetID string, status string, page *pb.Page) ([]*pb.View, *pb.PageResult, error)
  	UpsertViewColumn(ctx context.Context, item *pb.ViewColumn) (*pb.ViewColumn, error)
  	ListViewColumns(ctx context.Context, spaceID string, viewID string, page *pb.Page) ([]*pb.ViewColumn, *pb.PageResult, error)

  	UpsertStorageNode(ctx context.Context, item *pb.StorageNode) (*pb.StorageNode, error)
  	GetStorageNode(ctx context.Context, nodeID string) (*pb.StorageNode, error)
  	ListStorageNodes(ctx context.Context, page *pb.Page) ([]*pb.StorageNode, *pb.PageResult, error)
  	UpsertDevice(ctx context.Context, item *pb.Device) (*pb.Device, error)
  	GetDevice(ctx context.Context, deviceID string) (*pb.Device, error)
  	ListDevices(ctx context.Context, nodeID string, engine string, page *pb.Page) ([]*pb.Device, *pb.PageResult, error)
  	UpsertStorageRoute(ctx context.Context, item *pb.StorageRoute) (*pb.StorageRoute, error)
  	GetStorageRoute(ctx context.Context, spaceID string, routeID string) (*pb.StorageRoute, error)
  	ListStorageRoutes(ctx context.Context, spaceID string, datasetID string, subjectID string, nodeID string, page *pb.Page) ([]*pb.StorageRoute, *pb.PageResult, error)
  	RegisterArchiveFile(ctx context.Context, item *pb.ArchiveFile) (*pb.ArchiveFile, error)
  	ListArchiveFiles(ctx context.Context, spaceID string, datasetID string, page *pb.Page) ([]*pb.ArchiveFile, *pb.PageResult, error)
  }
  ```

- [ ] **Step 1.4: Implement SQLite Open and schema init**

  Create `metadata/sqlite/store.go`:

  ```go
  package sqlite

  import (
  	"context"
  	"database/sql"
  	"fmt"
  	"os"

  	_ "modernc.org/sqlite"
  )

  type Options struct {
  	Path       string
  	SchemaPath string
  }

  type Store struct {
  	db         *sql.DB
  	schemaPath string
  }

  func Open(ctx context.Context, opts Options) (*Store, error) {
  	if opts.Path == "" {
  		return nil, fmt.Errorf("metadata sqlite path is required")
  	}
  	if opts.SchemaPath == "" {
  		return nil, fmt.Errorf("metadata schema path is required")
  	}
  	db, err := sql.Open("sqlite", opts.Path)
  	if err != nil {
  		return nil, err
  	}
  	if err := db.PingContext(ctx); err != nil {
  		_ = db.Close()
  		return nil, err
  	}
  	return &Store{db: db, schemaPath: opts.SchemaPath}, nil
  }

  func (s *Store) Close() error {
  	if s == nil || s.db == nil {
  		return nil
  	}
  	return s.db.Close()
  }

  func (s *Store) InitSchema(ctx context.Context) error {
  	schemaSQL, err := os.ReadFile(s.schemaPath)
  	if err != nil {
  		return err
  	}
  	_, err := s.db.ExecContext(ctx, schemaSQL)
  	return err
  }

  func (s *Store) TableNames(ctx context.Context) ([]string, error) {
  	rows, err := s.db.QueryContext(ctx, `SELECT name FROM sqlite_master WHERE type='table' ORDER BY name`)
  	if err != nil {
  		return nil, err
  	}
  	defer rows.Close()

  	var names []string
  	for rows.Next() {
  		var name string
  		if err := rows.Scan(&name); err != nil {
  			return nil, err
  		}
  		names = append(names, name)
  	}
  	return names, rows.Err()
  }
  ```

- [ ] **Step 1.5: Run test and confirm GREEN**

  Run:

  ```bash
  cd /Users/mooyang/Documents/go/src/github.com/mooyang-code/moox
  go test ./modules/storage/internal/services/metadata/sqlite -run TestStoreInitializesStorageMetadataSchema -count=1
  ```

  Expected: PASS.

- [ ] **Step 1.6: Implement CRUD in small groups**

  Implement SQLite methods in this order:

  ```text
  Space
  DataSource / Subject / SubjectSymbol
  DataSet / DataSetSubject / DataSetColumn
  Field / Factor
  View / ViewColumn
  StorageNode / Device / StorageRoute / ArchiveFile
  ```

  For each group, first add a test using one create/get/list round trip, then write the smallest SQL implementation that passes.

- [ ] **Step 1.7: Commit metadata store**

  Run:

  ```bash
  cd /Users/mooyang/Documents/go/src/github.com/mooyang-code/moox
  git add modules/storage/internal/services/metadata modules/storage/go.mod modules/storage/go.sum
  git commit -m "feat(storage): add sqlite metadata store"
  ```

---

### Task 2: Thin Protocol Service And Dependency Injection

**Files:**

- Modify: `/Users/mooyang/Documents/go/src/github.com/mooyang-code/moox/modules/storage/internal/services/storage/service.go`
- Modify: `/Users/mooyang/Documents/go/src/github.com/mooyang-code/moox/modules/storage/internal/services/storage/service_test.go`
- Modify: `/Users/mooyang/Documents/go/src/github.com/mooyang-code/moox/modules/storage/cmd/moox-storage/main.go`
- Create: `/Users/mooyang/Documents/go/src/github.com/mooyang-code/moox/modules/storage/internal/services/storage/options.go`

- [ ] **Step 2.1: Write failing test for persistent metadata**

  Add this test to `service_test.go`:

  ```go
  func TestServicePersistsStorageNodeMetadataAcrossRestart(t *testing.T) {
  	ctx := context.Background()
  	root := t.TempDir()

  	svc := NewService(root)
  	createRsp, err := svc.CreateStorageNode(ctx, &pb.CreateStorageNodeReq{
  		Node: &pb.StorageNode{Name: "adapter-1", Endpoint: "127.0.0.1:19001"},
  	})
  	require.NoError(t, err)
  	require.Equal(t, pb.ErrorCode_SUCCESS, createRsp.GetRetInfo().GetCode())
  	nodeID := createRsp.GetNode().GetNodeId()

  	restarted := NewService(root)
  	getRsp, err := restarted.GetStorageNode(ctx, &pb.GetStorageNodeReq{NodeId: nodeID})
  	require.NoError(t, err)
  	require.Equal(t, pb.ErrorCode_SUCCESS, getRsp.GetRetInfo().GetCode())
  	require.Equal(t, nodeID, getRsp.GetNode().GetNodeId())
  }
  ```

- [ ] **Step 2.2: Run test and confirm RED**

  Run:

  ```bash
  cd /Users/mooyang/Documents/go/src/github.com/mooyang-code/moox
  go test ./modules/storage/internal/services/storage -run TestServicePersistsStorageNodeMetadataAcrossRestart -count=1
  ```

  Expected: FAIL because current service stores metadata in memory.

- [ ] **Step 2.3: Introduce service options**

  Create `options.go`:

  ```go
  package storage

  import "github.com/mooyang-code/moox/modules/storage/internal/services/metadata"

  type Options struct {
  	Root     string
  	Metadata metadata.Store
  }
  ```

- [ ] **Step 2.4: Add constructor with explicit dependencies**

  Add:

  ```go
  func NewServiceWithOptions(opts Options) *Service {
  	return &Service{
  		store:    quantstore.New(opts.Root),
  		metadata: opts.Metadata,
  	}
  }
  ```

  Keep `NewService(root string)` as a convenience constructor that opens SQLite under:

  ```text
  {root}/metadata/storage_metadata.db
  ```

- [ ] **Step 2.5: Move metadata methods from maps to metadata.Store**

  Update `CreateStorageNode`, `GetStorageNode`, `ListStorageNodes`, `CreateDevice`, `CreateStorageRoute` and the rest of MetadataService to call `s.metadata`.

  Example:

  ```go
  func (s *Service) CreateStorageNode(ctx context.Context, req *pb.CreateStorageNodeReq) (*pb.CreateStorageNodeRsp, error) {
  	node := req.GetNode()
  	if node == nil || (node.GetNodeId() == "" && node.GetName() == "") {
  		return &pb.CreateStorageNodeRsp{RetInfo: quantstore.Error(pb.ErrorCode_INVALID_PARAM, errors.New("node_id or name is required"))}, nil
  	}
  	if node.NodeId == "" {
  		node.NodeId = defaultID(node.GetName(), "node")
  	}
  	created, err := s.metadata.UpsertStorageNode(ctx, node)
  	if err != nil {
  		return &pb.CreateStorageNodeRsp{RetInfo: quantstore.Error(pb.ErrorCode_INVALID_PARAM, err)}, nil
  	}
  	return &pb.CreateStorageNodeRsp{RetInfo: quantstore.Success("success"), Node: created}, nil
  }
  ```

- [ ] **Step 2.6: Run persistence test and full storage tests**

  Run:

  ```bash
  cd /Users/mooyang/Documents/go/src/github.com/mooyang-code/moox
  go test ./modules/storage/internal/services/storage -run TestServicePersistsStorageNodeMetadataAcrossRestart -count=1
  go test ./modules/storage/...
  ```

  Expected: both commands PASS.

- [ ] **Step 2.7: Commit service metadata wiring**

  Run:

  ```bash
  cd /Users/mooyang/Documents/go/src/github.com/mooyang-code/moox
  git add modules/storage/internal/services/storage modules/storage/cmd/moox-storage
  git commit -m "feat(storage): persist metadata service state"
  ```

---

### Task 3: Schema Validator For WriteRows

**Files:**

- Create: `/Users/mooyang/Documents/go/src/github.com/mooyang-code/moox/modules/storage/internal/services/schema/validator.go`
- Create: `/Users/mooyang/Documents/go/src/github.com/mooyang-code/moox/modules/storage/internal/services/schema/validator_test.go`
- Modify: `/Users/mooyang/Documents/go/src/github.com/mooyang-code/moox/modules/storage/internal/services/storage/data.go`

- [ ] **Step 3.1: Write failing validator test**

  Create `validator_test.go`:

  ```go
  package schema_test

  import (
  	"context"
  	"fmt"
  	"testing"

  	"github.com/mooyang-code/moox/modules/storage/internal/services/schema"
  	"github.com/mooyang-code/moox/modules/storage/pkg/quantstore"
  	pb "github.com/mooyang-code/moox/modules/storage/proto/gen"
  	"github.com/stretchr/testify/require"
  )

  func TestValidatorRejectsUnknownColumn(t *testing.T) {
  	ctx := context.Background()
  	meta := &fakeValidatorMetadata{
  		dataset: &pb.DataSet{SpaceId: "crypto", DatasetId: "binance_spot_kline", Status: "active"},
  		columns: []*pb.DataSetColumn{{
  			SpaceId:    "crypto",
  			DatasetId:  "binance_spot_kline",
  			ColumnName: "close",
  			ValueType:  pb.FieldValueType_FIELD_VALUE_TYPE_DOUBLE,
  			Status:     "active",
  		}},
  	}
  	validator := schema.NewValidator(meta)

  	err := validator.ValidateWriteRows(ctx, []*pb.DataRow{{
  		Key: &pb.DataKey{
  			Scope: &pb.DataScope{
  				SpaceId:   "crypto",
  				DatasetId: "binance_spot_kline",
  				SubjectId: "APT-USDT",
  				Freq:      "1m",
  			},
  			DataTime: "2026-06-15T00:00:00+08:00",
  		},
  		Columns: []*pb.ColumnValue{
  			quantstore.DoubleValue("unknown_close", 9.9),
  		},
  	}})

  	require.ErrorContains(t, err, "column unknown_close is not registered")
  }

  type fakeValidatorMetadata struct {
  	dataset *pb.DataSet
  	columns []*pb.DataSetColumn
  }

  func (f *fakeValidatorMetadata) GetDataSet(ctx context.Context, spaceID string, datasetID string) (*pb.DataSet, error) {
  	if f.dataset.GetSpaceId() == spaceID && f.dataset.GetDatasetId() == datasetID {
  		return f.dataset, nil
  	}
  	return nil, fmt.Errorf("dataset not found")
  }

  func (f *fakeValidatorMetadata) ListDataSetColumns(ctx context.Context, spaceID string, datasetID string, textIndexedOnly bool, page *pb.Page) ([]*pb.DataSetColumn, *pb.PageResult, error) {
  	return f.columns, &pb.PageResult{Total: uint64(len(f.columns))}, nil
  }
  ```

- [ ] **Step 3.2: Run test and confirm RED**

  Run:

  ```bash
  cd /Users/mooyang/Documents/go/src/github.com/mooyang-code/moox
  go test ./modules/storage/internal/services/schema -run TestValidatorRejectsUnknownColumn -count=1
  ```

  Expected: FAIL because validator package does not exist.

- [ ] **Step 3.3: Implement validator**

  Create:

  ```go
  package schema

  import (
  	"context"
  	"fmt"

  	pb "github.com/mooyang-code/moox/modules/storage/proto/gen"
  )

  type MetadataReader interface {
  	GetDataSet(ctx context.Context, spaceID string, datasetID string) (*pb.DataSet, error)
  	ListDataSetColumns(ctx context.Context, spaceID string, datasetID string, textIndexedOnly bool, page *pb.Page) ([]*pb.DataSetColumn, *pb.PageResult, error)
  }

  type Validator struct {
  	metadata MetadataReader
  }

  func NewValidator(store MetadataReader) *Validator {
  	return &Validator{metadata: store}
  }

  func (v *Validator) ValidateWriteRows(ctx context.Context, rows []*pb.DataRow) error {
  	for _, row := range rows {
  		if row.GetKey() == nil || row.GetKey().GetScope() == nil {
  			return fmt.Errorf("data key and scope are required")
  		}
  		scope := row.GetKey().GetScope()
  		if scope.GetSpaceId() == "" || scope.GetDatasetId() == "" {
  			return fmt.Errorf("space_id and dataset_id are required")
  		}
  		dataset, err := v.metadata.GetDataSet(ctx, scope.GetSpaceId(), scope.GetDatasetId())
  		if err != nil {
  			return err
  		}
  		if dataset.GetStatus() != "" && dataset.GetStatus() != "active" {
  			return fmt.Errorf("dataset %s is not active", scope.GetDatasetId())
  		}
  		allowed, _, err := v.metadata.ListDataSetColumns(ctx, scope.GetSpaceId(), scope.GetDatasetId(), false, nil)
  		if err != nil {
  			return err
  		}
  		allowedColumns := make(map[string]*pb.DataSetColumn, len(allowed))
  		for _, column := range allowed {
  			allowedColumns[column.GetColumnName()] = column
  		}
  		for _, value := range row.GetColumns() {
  			if allowedColumns[value.GetColumnName()] == nil {
  				return fmt.Errorf("column %s is not registered in dataset %s", value.GetColumnName(), scope.GetDatasetId())
  			}
  		}
  	}
  	return nil
  }
  ```

- [ ] **Step 3.4: Wire validator into WriteRows**

  In `storage/data.go`, call validator before routing or writing:

  ```go
  if err := s.validator.ValidateWriteRows(ctx, req.GetRows()); err != nil {
  	return &pb.WriteRowsRsp{RetInfo: quantstore.Error(pb.ErrorCode_INVALID_PARAM, err)}, nil
  }
  ```

- [ ] **Step 3.5: Run tests**

  Run:

  ```bash
  cd /Users/mooyang/Documents/go/src/github.com/mooyang-code/moox
  go test ./modules/storage/internal/services/schema -count=1
  go test ./modules/storage/internal/services/storage -run TestService -count=1
  go test ./modules/storage/...
  ```

  Expected: PASS.

- [ ] **Step 3.6: Commit validator**

  Run:

  ```bash
  cd /Users/mooyang/Documents/go/src/github.com/mooyang-code/moox
  git add modules/storage/internal/services/schema modules/storage/internal/services/storage
  git commit -m "feat(storage): validate dataset write rows"
  ```

---

### Task 4: StorageRoute Resolver And Adapter Client

**Files:**

- Create: `/Users/mooyang/Documents/go/src/github.com/mooyang-code/moox/modules/storage/internal/services/router/resolver.go`
- Create: `/Users/mooyang/Documents/go/src/github.com/mooyang-code/moox/modules/storage/internal/services/router/resolver_test.go`
- Create: `/Users/mooyang/Documents/go/src/github.com/mooyang-code/moox/modules/storage/internal/services/adapter/client.go`
- Modify: `/Users/mooyang/Documents/go/src/github.com/mooyang-code/moox/modules/storage/internal/services/storage/data.go`

- [ ] **Step 4.1: Write failing route resolver test**

  Create `router/resolver_test.go`:

  ```go
  package router_test

  import (
  	"context"
  	"testing"

  	"github.com/mooyang-code/moox/modules/storage/internal/services/router"
  	pb "github.com/mooyang-code/moox/modules/storage/proto/gen"
  	"github.com/stretchr/testify/require"
  )

  func TestResolverSelectsExactSubjectRouteBeforeWildcard(t *testing.T) {
  	ctx := context.Background()
  	meta := &fakeRouteMetadata{routes: []*pb.StorageRoute{
  		{SpaceId: "crypto", RouteId: "wildcard", DatasetId: "kline", SubjectPattern: "*", NodeId: "node-a", Priority: 100, Status: "active"},
  		{SpaceId: "crypto", RouteId: "apt", DatasetId: "kline", SubjectId: "APT-USDT", NodeId: "node-b", Priority: 10, Status: "active"},
  	}}
  	resolver := router.NewResolver(meta)

  	ref, err := resolver.Resolve(ctx, &pb.DataScope{SpaceId: "crypto", DatasetId: "kline", SubjectId: "APT-USDT"})
  	require.NoError(t, err)
  	require.Equal(t, "node-b", ref.GetNodeId())
  }

  type fakeRouteMetadata struct {
  	routes []*pb.StorageRoute
  }

  func (f *fakeRouteMetadata) ListStorageRoutes(ctx context.Context, spaceID string, datasetID string, subjectID string, nodeID string, page *pb.Page) ([]*pb.StorageRoute, *pb.PageResult, error) {
  	var out []*pb.StorageRoute
  	for _, route := range f.routes {
  		if route.GetSpaceId() == spaceID && route.GetDatasetId() == datasetID {
  			out = append(out, route)
  		}
  	}
  	return out, &pb.PageResult{Total: uint64(len(out))}, nil
  }
  ```

- [ ] **Step 4.2: Run test and confirm RED**

  Run:

  ```bash
  cd /Users/mooyang/Documents/go/src/github.com/mooyang-code/moox
  go test ./modules/storage/internal/services/router -run TestResolverSelectsExactSubjectRouteBeforeWildcard -count=1
  ```

  Expected: FAIL because router package does not exist.

- [ ] **Step 4.3: Implement resolver**

  Create:

  ```go
  package router

  import (
  	"context"
  	"fmt"
  	"sort"

  	pb "github.com/mooyang-code/moox/modules/storage/proto/gen"
  )

  type RouteReader interface {
  	ListStorageRoutes(ctx context.Context, spaceID string, datasetID string, subjectID string, nodeID string, page *pb.Page) ([]*pb.StorageRoute, *pb.PageResult, error)
  }

  type Resolver struct {
  	metadata RouteReader
  }

  func NewResolver(store RouteReader) *Resolver {
  	return &Resolver{metadata: store}
  }

  func (r *Resolver) Resolve(ctx context.Context, scope *pb.DataScope) (*pb.DeviceRef, error) {
  	if scope == nil {
  		return nil, fmt.Errorf("data scope is required")
  	}
  	routes, _, err := r.metadata.ListStorageRoutes(ctx, scope.GetSpaceId(), scope.GetDatasetId(), scope.GetSubjectId(), "", nil)
  	if err != nil {
  		return nil, err
  	}
  	var candidates []*pb.StorageRoute
  	for _, route := range routes {
  		if route.GetStatus() != "" && route.GetStatus() != "active" {
  			continue
  		}
  		if route.GetSubjectId() != "" && route.GetSubjectId() != scope.GetSubjectId() {
  			continue
  		}
  		if route.GetSubjectPattern() != "" && route.GetSubjectPattern() != "*" && route.GetSubjectPattern() != scope.GetSubjectId() {
  			continue
  		}
  		candidates = append(candidates, route)
  	}
  	if len(candidates) == 0 {
  		return nil, fmt.Errorf("storage route not found for %s/%s/%s", scope.GetSpaceId(), scope.GetDatasetId(), scope.GetSubjectId())
  	}
  	sort.SliceStable(candidates, func(i, j int) bool {
  		if candidates[i].GetPriority() == candidates[j].GetPriority() {
  			return candidates[i].GetRouteId() < candidates[j].GetRouteId()
  		}
  		return candidates[i].GetPriority() < candidates[j].GetPriority()
  	})
  	chosen := candidates[0]
  	return &pb.DeviceRef{
  		SpaceId:   scope.GetSpaceId(),
  		NodeId:    chosen.GetNodeId(),
  		Engine:    "pebble",
  		DatasetId: scope.GetDatasetId(),
  	}, nil
  }
  ```

- [ ] **Step 4.4: Define adapter client**

  Create `adapter/client.go`:

  ```go
  package adapter

  import (
  	"context"

  	pb "github.com/mooyang-code/moox/modules/storage/proto/gen"
  )

  type Client interface {
  	WriteRows(ctx context.Context, device *pb.DeviceRef, rows []*pb.DataRow, mode pb.WriteMode) error
  	ReadRows(ctx context.Context, device *pb.DeviceRef, req *pb.ReadRowsReq) ([]*pb.DataRow, *pb.PageResult, error)
  }
  ```

- [ ] **Step 4.5: Wire WriteRows through resolver and adapter client**

  Replace direct `s.store.WriteRows` in `data.go` with:

  ```go
  groups, err := groupRowsByDevice(ctx, s.router, req.GetRows())
  if err != nil {
  	return &pb.WriteRowsRsp{RetInfo: quantstore.Error(pb.ErrorCode_ROUTE_NOT_FOUND, err)}, nil
  }
  for _, group := range groups {
  	if err := s.adapter.WriteRows(ctx, group.device, group.rows, mode); err != nil {
  		return &pb.WriteRowsRsp{RetInfo: quantstore.Error(pb.ErrorCode_INNER_ERROR, err)}, nil
  	}
  }
  ```

- [ ] **Step 4.6: Run tests and commit**

  Run:

  ```bash
  cd /Users/mooyang/Documents/go/src/github.com/mooyang-code/moox
  go test ./modules/storage/internal/services/router -count=1
  go test ./modules/storage/...
  git add modules/storage/internal/services/router modules/storage/internal/services/adapter modules/storage/internal/services/storage
  git commit -m "feat(storage): route writes to storage nodes"
  ```

---

### Task 5: Pebble Online Fact Store

**Files:**

- Create: `/Users/mooyang/Documents/go/src/github.com/mooyang-code/moox/modules/storage/internal/services/device/store.go`
- Create: `/Users/mooyang/Documents/go/src/github.com/mooyang-code/moox/modules/storage/internal/services/device/pebble/store.go`
- Create: `/Users/mooyang/Documents/go/src/github.com/mooyang-code/moox/modules/storage/internal/services/device/pebble/key.go`
- Create: `/Users/mooyang/Documents/go/src/github.com/mooyang-code/moox/modules/storage/internal/services/device/pebble/store_test.go`
- Modify: `/Users/mooyang/Documents/go/src/github.com/mooyang-code/moox/modules/storage/pkg/quantstore/store.go`

- [ ] **Step 5.1: Write failing Pebble range read test**

  Create `device/pebble/store_test.go`:

  ```go
  package pebble_test

  import (
  	"context"
  	"testing"

  	"github.com/mooyang-code/moox/modules/storage/internal/services/device/pebble"
  	"github.com/mooyang-code/moox/modules/storage/pkg/quantstore"
  	pb "github.com/mooyang-code/moox/modules/storage/proto/gen"
  	"github.com/stretchr/testify/require"
  )

  func TestStoreWritesAndReadsRowsByTimeRange(t *testing.T) {
  	ctx := context.Background()
  	store, err := pebble.Open(pebble.Options{Path: t.TempDir()})
  	require.NoError(t, err)
  	defer store.Close()

  	scope := &pb.DataScope{SpaceId: "crypto", DatasetId: "kline", SubjectId: "APT-USDT", Freq: "1m"}
  	rows := []*pb.DataRow{
  		{Key: &pb.DataKey{Scope: scope, DataTime: "2026-06-15T00:00:00+08:00"}, Columns: []*pb.ColumnValue{quantstore.DoubleValue("close", 8.1)}},
  		{Key: &pb.DataKey{Scope: scope, DataTime: "2026-06-15T00:01:00+08:00"}, Columns: []*pb.ColumnValue{quantstore.DoubleValue("close", 8.2)}},
  	}

  	require.NoError(t, store.WriteRows(ctx, rows, pb.WriteMode_WRITE_MODE_UPSERT))
  	got, page, err := store.ReadRows(ctx, scope, pb.ReadMode_READ_MODE_RANGE, &pb.TimeRange{
  		StartTime: "2026-06-15T00:00:00+08:00",
  		EndTime:   "2026-06-15T00:01:00+08:00",
  	}, "", nil, []string{"close"}, nil)
  	require.NoError(t, err)
  	require.Len(t, got, 2)
  	require.Equal(t, uint64(2), page.GetTotal())
  }
  ```

- [ ] **Step 5.2: Run test and confirm RED**

  Run:

  ```bash
  cd /Users/mooyang/Documents/go/src/github.com/mooyang-code/moox
  go test ./modules/storage/internal/services/device/pebble -run TestStoreWritesAndReadsRowsByTimeRange -count=1
  ```

  Expected: FAIL because Pebble store does not exist.

- [ ] **Step 5.3: Define device fact store interface**

  Create `device/store.go`:

  ```go
  package device

  import (
  	"context"

  	pb "github.com/mooyang-code/moox/modules/storage/proto/gen"
  )

  type FactStore interface {
  	Close() error
  	WriteRows(ctx context.Context, rows []*pb.DataRow, mode pb.WriteMode) error
  	ReadRows(ctx context.Context, scope *pb.DataScope, mode pb.ReadMode, timeRange *pb.TimeRange, snapshotTime string, rowIDs []string, columnNames []string, page *pb.Page) ([]*pb.DataRow, *pb.PageResult, error)
  }
  ```

- [ ] **Step 5.4: Implement Pebble key encoding**

  Create `key.go`:

  ```go
  package pebble

  import (
  	"crypto/sha1"
  	"encoding/hex"
  	"sort"
  	"strings"

  	pb "github.com/mooyang-code/moox/modules/storage/proto/gen"
  )

  func factKey(row *pb.DataRow) []byte {
  	key := row.GetKey()
  	scope := key.GetScope()
  	return []byte(strings.Join([]string{
  		"fact",
  		escape(scope.GetSpaceId()),
  		escape(scope.GetDatasetId()),
  		escape(scope.GetSubjectId()),
  		escape(scope.GetFreq()),
  		dimensionHash(scope.GetDimensions()),
  		escape(key.GetDataTime()),
  		escape(key.GetRowId()),
  	}, "/"))
  }

  func factPrefix(scope *pb.DataScope) []byte {
  	return []byte(strings.Join([]string{
  		"fact",
  		escape(scope.GetSpaceId()),
  		escape(scope.GetDatasetId()),
  		escape(scope.GetSubjectId()),
  		escape(scope.GetFreq()),
  		dimensionHash(scope.GetDimensions()),
  	}, "/") + "/")
  }

  func dimensionHash(dimensions map[string]string) string {
  	if len(dimensions) == 0 {
  		return "_"
  	}
  	keys := make([]string, 0, len(dimensions))
  	for key := range dimensions {
  		keys = append(keys, key)
  	}
  	sort.Strings(keys)
  	var b strings.Builder
  	for _, key := range keys {
  		b.WriteString(key)
  		b.WriteByte('=')
  		b.WriteString(dimensions[key])
  		b.WriteByte(';')
  	}
  	sum := sha1.Sum([]byte(b.String()))
  	return hex.EncodeToString(sum[:])
  }

  func escape(value string) string {
  	value = strings.ReplaceAll(value, "/", "_")
  	if value == "" {
  		return "_"
  	}
  	return value
  }
  ```

- [ ] **Step 5.5: Implement Pebble write/read**

  Implement `store.go` with `github.com/cockroachdb/pebble`, using `proto.Marshal` and `proto.Unmarshal` for `pb.DataRow`. `WriteRows` writes a Pebble batch. `ReadRows` scans `factPrefix(scope)`, filters by `timeRange`, `snapshotTime`, and `rowIDs`, then applies column projection and pagination.

- [ ] **Step 5.6: Remove JSONL core**

  Replace `pkg/quantstore.Store` JSONL implementation with either:

  ```go
  type Store = device.FactStore
  ```

  or remove its direct use from service code. Keep helper functions:

  ```go
  Success
  Error
  StringValue
  DoubleValue
  IntValue
  ```

- [ ] **Step 5.7: Run tests and commit**

  Run:

  ```bash
  cd /Users/mooyang/Documents/go/src/github.com/mooyang-code/moox
  go test ./modules/storage/internal/services/device/pebble -count=1
  go test ./modules/storage/pkg/quantstore -count=1
  go test ./modules/storage/...
  git add modules/storage/internal/services/device modules/storage/pkg/quantstore modules/storage/go.mod modules/storage/go.sum
  git commit -m "feat(storage): use pebble as online fact store"
  ```

---

### Task 6: Local Adapter Backed By Devices

**Files:**

- Create: `/Users/mooyang/Documents/go/src/github.com/mooyang-code/moox/modules/storage/internal/services/adapter/local.go`
- Create: `/Users/mooyang/Documents/go/src/github.com/mooyang-code/moox/modules/storage/internal/services/adapter/local_test.go`
- Modify: `/Users/mooyang/Documents/go/src/github.com/mooyang-code/moox/modules/storage/internal/services/storage/adapter.go`
- Modify: `/Users/mooyang/Documents/go/src/github.com/mooyang-code/moox/modules/storage/internal/services/storage/data.go`

- [ ] **Step 6.1: Write failing local adapter test**

  Create `adapter/local_test.go`:

  ```go
  package adapter_test

  import (
  	"context"
  	"testing"

  	"github.com/mooyang-code/moox/modules/storage/internal/services/adapter"
  	"github.com/mooyang-code/moox/modules/storage/internal/services/device/pebble"
  	"github.com/mooyang-code/moox/modules/storage/pkg/quantstore"
  	pb "github.com/mooyang-code/moox/modules/storage/proto/gen"
  	"github.com/stretchr/testify/require"
  )

  func TestLocalAdapterWritesToPebbleDevice(t *testing.T) {
  	ctx := context.Background()
  	facts, err := pebble.Open(pebble.Options{Path: t.TempDir()})
  	require.NoError(t, err)
  	defer facts.Close()

  	client := adapter.NewLocal(adapter.LocalOptions{
  		Pebble: facts,
  	})

  	scope := &pb.DataScope{SpaceId: "crypto", DatasetId: "kline", SubjectId: "APT-USDT", Freq: "1m"}
  	row := &pb.DataRow{
  		Key:     &pb.DataKey{Scope: scope, DataTime: "2026-06-15T00:00:00+08:00"},
  		Columns: []*pb.ColumnValue{quantstore.DoubleValue("close", 8.1)},
  	}
  	err = client.WriteRows(ctx, &pb.DeviceRef{SpaceId: "crypto", NodeId: "local", Engine: "pebble", DatasetId: "kline"}, []*pb.DataRow{row}, pb.WriteMode_WRITE_MODE_UPSERT)
  	require.NoError(t, err)
  }
  ```

- [ ] **Step 6.2: Run test and confirm RED**

  Run:

  ```bash
  cd /Users/mooyang/Documents/go/src/github.com/mooyang-code/moox
  go test ./modules/storage/internal/services/adapter -run TestLocalAdapterWritesToPebbleDevice -count=1
  ```

  Expected: FAIL because `adapter.NewLocal` does not exist.

- [ ] **Step 6.3: Implement local adapter**

  Create:

  ```go
  package adapter

  import (
  	"context"
  	"fmt"

  	"github.com/mooyang-code/moox/modules/storage/internal/services/device"
  	pb "github.com/mooyang-code/moox/modules/storage/proto/gen"
  )

  type LocalOptions struct {
  	Pebble device.FactStore
  }

  type Local struct {
  	pebble device.FactStore
  }

  func NewLocal(opts LocalOptions) *Local {
  	return &Local{pebble: opts.Pebble}
  }

  func (l *Local) WriteRows(ctx context.Context, ref *pb.DeviceRef, rows []*pb.DataRow, mode pb.WriteMode) error {
  	switch ref.GetEngine() {
  	case "pebble", "":
  		return l.pebble.WriteRows(ctx, rows, mode)
  	default:
  		return fmt.Errorf("unsupported write engine %s", ref.GetEngine())
  	}
  }

  func (l *Local) ReadRows(ctx context.Context, ref *pb.DeviceRef, req *pb.ReadRowsReq) ([]*pb.DataRow, *pb.PageResult, error) {
  	switch ref.GetEngine() {
  	case "pebble", "":
  		return l.pebble.ReadRows(ctx, req.GetScope(), req.GetReadMode(), req.GetTimeRange(), req.GetSnapshotTime(), req.GetRowIds(), req.GetColumnNames(), req.GetPage())
  	default:
  		return nil, nil, fmt.Errorf("unsupported read engine %s", ref.GetEngine())
  	}
  }
  ```

- [ ] **Step 6.4: Update AdapterService handler**

  `WriteDeviceRows` and `ReadDeviceRows` should call local adapter/device instead of direct JSONL store.

- [ ] **Step 6.5: Run tests and commit**

  Run:

  ```bash
  cd /Users/mooyang/Documents/go/src/github.com/mooyang-code/moox
  go test ./modules/storage/internal/services/adapter -count=1
  go test ./modules/storage/...
  git add modules/storage/internal/services/adapter modules/storage/internal/services/storage
  git commit -m "feat(storage): add local adapter device dispatch"
  ```

---

### Task 7: Changefeed And DataRowsChangedEvent

**Files:**

- Create: `/Users/mooyang/Documents/go/src/github.com/mooyang-code/moox/modules/storage/internal/services/changefeed/publisher.go`
- Create: `/Users/mooyang/Documents/go/src/github.com/mooyang-code/moox/modules/storage/internal/services/changefeed/publisher_test.go`
- Modify: `/Users/mooyang/Documents/go/src/github.com/mooyang-code/moox/modules/storage/internal/services/storage/data.go`
- Use existing: `/Users/mooyang/Documents/go/src/github.com/mooyang-code/moox/modules/storage/internal/services/messager/publisher`

- [ ] **Step 7.1: Write failing event publisher test**

  Create:

  ```go
  package changefeed_test

  import (
  	"context"
  	"testing"

  	"github.com/mooyang-code/moox/modules/storage/internal/services/changefeed"
  	pb "github.com/mooyang-code/moox/modules/storage/proto/gen"
  	"github.com/stretchr/testify/require"
  )

  func TestMemoryPublisherRecordsRowsChangedEvent(t *testing.T) {
  	ctx := context.Background()
  	publisher := changefeed.NewMemoryPublisher()

  	err := publisher.PublishRowsChanged(ctx, &pb.DataRowsChangedEvent{
  		Scope:     &pb.DataScope{SpaceId: "crypto", DatasetId: "kline", SubjectId: "APT-USDT"},
  		EventTime: "2026-06-15T00:00:00+08:00",
  		Rows:      []*pb.DataRow{{Key: &pb.DataKey{DataTime: "2026-06-15T00:00:00+08:00"}}},
  	})

  	require.NoError(t, err)
  	require.Len(t, publisher.Events(), 1)
  }
  ```

- [ ] **Step 7.2: Run test and confirm RED**

  Run:

  ```bash
  cd /Users/mooyang/Documents/go/src/github.com/mooyang-code/moox
  go test ./modules/storage/internal/services/changefeed -run TestMemoryPublisherRecordsRowsChangedEvent -count=1
  ```

  Expected: FAIL because package does not exist.

- [ ] **Step 7.3: Implement publisher abstraction**

  Create:

  ```go
  package changefeed

  import (
  	"context"
  	"sync"

  	pb "github.com/mooyang-code/moox/modules/storage/proto/gen"
  )

  type Publisher interface {
  	PublishRowsChanged(ctx context.Context, event *pb.DataRowsChangedEvent) error
  }

  type MemoryPublisher struct {
  	mu     sync.Mutex
  	events []*pb.DataRowsChangedEvent
  }

  func NewMemoryPublisher() *MemoryPublisher {
  	return &MemoryPublisher{}
  }

  func (p *MemoryPublisher) PublishRowsChanged(ctx context.Context, event *pb.DataRowsChangedEvent) error {
  	_ = ctx
  	p.mu.Lock()
  	defer p.mu.Unlock()
  	p.events = append(p.events, event)
  	return nil
  }

  func (p *MemoryPublisher) Events() []*pb.DataRowsChangedEvent {
  	p.mu.Lock()
  	defer p.mu.Unlock()
  	out := make([]*pb.DataRowsChangedEvent, len(p.events))
  	copy(out, p.events)
  	return out
  }
  ```

- [ ] **Step 7.4: Publish event after successful Pebble write**

  In `WriteRows`, after all adapter writes succeed, group rows by scope and publish `DataRowsChangedEvent`.

- [ ] **Step 7.5: Run tests and commit**

  Run:

  ```bash
  cd /Users/mooyang/Documents/go/src/github.com/mooyang-code/moox
  go test ./modules/storage/internal/services/changefeed -count=1
  go test ./modules/storage/...
  git add modules/storage/internal/services/changefeed modules/storage/internal/services/storage
  git commit -m "feat(storage): publish fact row change events"
  ```

---

### Task 8: Bleve SearchRows Derived Index

**Files:**

- Create: `/Users/mooyang/Documents/go/src/github.com/mooyang-code/moox/modules/storage/internal/services/device/bleve/index.go`
- Create: `/Users/mooyang/Documents/go/src/github.com/mooyang-code/moox/modules/storage/internal/services/device/bleve/index_test.go`
- Modify: `/Users/mooyang/Documents/go/src/github.com/mooyang-code/moox/modules/storage/internal/services/storage/query.go`

- [ ] **Step 8.1: Write failing Bleve search test**

  Create:

  ```go
  package bleve_test

  import (
  	"context"
  	"testing"

  	"github.com/mooyang-code/moox/modules/storage/internal/services/device/bleve"
  	"github.com/mooyang-code/moox/modules/storage/pkg/quantstore"
  	pb "github.com/mooyang-code/moox/modules/storage/proto/gen"
  	"github.com/stretchr/testify/require"
  )

  func TestIndexSearchesOnlyIndexedTextColumns(t *testing.T) {
  	ctx := context.Background()
  	index, err := bleve.Open(bleve.Options{Path: t.TempDir()})
  	require.NoError(t, err)
  	defer index.Close()

  	row := &pb.DataRow{
  		Key: &pb.DataKey{Scope: &pb.DataScope{SpaceId: "crypto", DatasetId: "news", SubjectId: "coindesk"}, DataTime: "2026-06-15T00:00:00+08:00"},
  		Columns: []*pb.ColumnValue{
  			quantstore.StringValue("title", "APT market rallies"),
  			quantstore.StringValue("internal_note", "do not index this"),
  		},
  	}

  	err = index.IndexRows(ctx, []*pb.DataRow{row}, map[string]bool{"title": true})
  	require.NoError(t, err)

  	got, _, err := index.SearchRows(ctx, bleve.SearchRequest{SpaceID: "crypto", DatasetID: "news", TextQuery: "rallies"})
  	require.NoError(t, err)
  	require.Len(t, got, 1)

  	got, _, err = index.SearchRows(ctx, bleve.SearchRequest{SpaceID: "crypto", DatasetID: "news", TextQuery: "internal"})
  	require.NoError(t, err)
  	require.Len(t, got, 0)
  }
  ```

- [ ] **Step 8.2: Run test and confirm RED**

  Run:

  ```bash
  cd /Users/mooyang/Documents/go/src/github.com/mooyang-code/moox
  go test ./modules/storage/internal/services/device/bleve -run TestIndexSearchesOnlyIndexedTextColumns -count=1
  ```

  Expected: FAIL because Bleve device package does not exist.

- [ ] **Step 8.3: Implement Bleve index**

  Implement `Open`, `IndexRows`, and `SearchRows`. Document IDs should be:

  ```text
  {space_id}/{dataset_id}/{subject_id}/{freq}/{data_time}/{row_id}
  ```

  Store full `DataRow` as JSON in a field named `_row_json`, and index only columns from `textIndexedColumns`.

- [ ] **Step 8.4: Wire SearchRows**

  `SearchRows` should:

  ```text
  if text_query is non-empty:
    query Bleve first
    apply structured filters
    apply column projection
  else:
    read Pebble rows by DataSet/time/subject
    apply structured filters
    apply sort/page
  ```

- [ ] **Step 8.5: Run tests and commit**

  Run:

  ```bash
  cd /Users/mooyang/Documents/go/src/github.com/mooyang-code/moox
  go test ./modules/storage/internal/services/device/bleve -count=1
  go test ./modules/storage/internal/services/storage -run TestSearchRows -count=1
  go test ./modules/storage/...
  git add modules/storage/internal/services/device/bleve modules/storage/internal/services/storage/query.go modules/storage/go.mod modules/storage/go.sum
  git commit -m "feat(storage): search rows with bleve index"
  ```

---

### Task 9: DuckDB View Materialization And QueryView

**Files:**

- Create: `/Users/mooyang/Documents/go/src/github.com/mooyang-code/moox/modules/storage/internal/services/device/duckdb/view_store.go`
- Create: `/Users/mooyang/Documents/go/src/github.com/mooyang-code/moox/modules/storage/internal/services/device/duckdb/view_store_test.go`
- Create: `/Users/mooyang/Documents/go/src/github.com/mooyang-code/moox/modules/storage/internal/services/materializer/view_builder.go`
- Create: `/Users/mooyang/Documents/go/src/github.com/mooyang-code/moox/modules/storage/internal/services/materializer/view_builder_test.go`
- Modify: `/Users/mooyang/Documents/go/src/github.com/mooyang-code/moox/modules/storage/internal/services/storage/query.go`

- [ ] **Step 9.1: Write failing View not found test**

  Add to storage query tests:

  ```go
  func TestQueryViewReturnsViewNotFoundWhenViewHasNoActiveResult(t *testing.T) {
  	ctx := context.Background()
  	svc := NewService(t.TempDir())

  	spaceRsp, err := svc.CreateSpace(ctx, &pb.CreateSpaceReq{Space: &pb.Space{SpaceId: "crypto", Name: "crypto"}})
  	require.NoError(t, err)
  	require.Equal(t, pb.ErrorCode_SUCCESS, spaceRsp.GetRetInfo().GetCode())

  	sourceRsp, err := svc.CreateDataSource(ctx, &pb.CreateDataSourceReq{DataSource: &pb.DataSource{SpaceId: "crypto", DataSourceId: "binance", Name: "Binance", Kind: "exchange"}})
  	require.NoError(t, err)
  	require.Equal(t, pb.ErrorCode_SUCCESS, sourceRsp.GetRetInfo().GetCode())

  	datasetRsp, err := svc.CreateDataSet(ctx, &pb.CreateDataSetReq{Dataset: &pb.DataSet{SpaceId: "crypto", DatasetId: "kline", DataSourceId: "binance", Name: "K线", DataKind: pb.DataKind_DATA_KIND_TIME_SERIES, Freqs: []string{"1m"}, Status: "active"}})
  	require.NoError(t, err)
  	require.Equal(t, pb.ErrorCode_SUCCESS, datasetRsp.GetRetInfo().GetCode())

  	_, err := svc.CreateView(ctx, &pb.CreateViewReq{View: &pb.View{
  		SpaceId:          "crypto",
  		ViewId:           "kline_view",
  		Name:             "K线视图",
  		PrimaryDatasetId: "kline",
  		DatasetIds:       []string{"kline"},
  		QueryWindow:      "30d",
  		Status:           "active",
  	}})
  	require.NoError(t, err)

  	rsp, err := svc.QueryView(ctx, &pb.QueryViewReq{SpaceId: "crypto", ViewId: "kline_view"})
  	require.NoError(t, err)
  	require.Equal(t, pb.ErrorCode_VIEW_NOT_FOUND, rsp.GetRetInfo().GetCode())
  }
  ```

- [ ] **Step 9.2: Run test and confirm RED**

  Run:

  ```bash
  cd /Users/mooyang/Documents/go/src/github.com/mooyang-code/moox
  go test ./modules/storage/internal/services/storage -run TestQueryViewReturnsViewNotFoundWhenViewHasNoActiveResult -count=1
  ```

  Expected: FAIL because current `QueryView` reads primary dataset directly.

- [ ] **Step 9.3: Implement DuckDB ViewStore**

  `ViewStore` should provide:

  ```go
  type ViewStore interface {
  	CreateResultTable(ctx context.Context, tableName string, columns []*pb.ViewColumn) error
  	InsertRows(ctx context.Context, tableName string, rows []*pb.QueryViewRow) error
  	QueryView(ctx context.Context, tableName string, req *pb.QueryViewReq) ([]*pb.QueryViewColumn, []*pb.QueryViewRow, *pb.PageResult, error)
  }
  ```

  The DuckDB table name must be internal, for example:

  ```text
  view_result_{space_id}_{view_id}_{unix_nano}
  ```

  Do not expose this table name in `QueryViewRsp`.

- [ ] **Step 9.4: Implement ViewBuilder**

  `ViewBuilder` should:

  ```text
  read View metadata
  read ViewColumn metadata
  compute build window from View.query_window
  scan Pebble facts for primary_dataset_id subjects
  join additional dataset columns by grain_keys
  write a new DuckDB result table
  update t_views.c_active_result
  set build_status = active
  ```

- [ ] **Step 9.5: Change QueryView**

  `QueryView` should:

  ```text
  load View by space_id + view_id
  if view missing or active_result empty: return VIEW_NOT_FOUND
  call DuckDB ViewStore.QueryView(active_result, req)
  return columns + rows + page_result
  ```

- [ ] **Step 9.6: Run tests and commit**

  Run:

  ```bash
  cd /Users/mooyang/Documents/go/src/github.com/mooyang-code/moox
  go test ./modules/storage/internal/services/device/duckdb -count=1
  go test ./modules/storage/internal/services/materializer -count=1
  go test ./modules/storage/internal/services/storage -run TestQueryView -count=1
  go test ./modules/storage/...
  git add modules/storage/internal/services/device/duckdb modules/storage/internal/services/materializer modules/storage/internal/services/storage/query.go modules/storage/go.mod modules/storage/go.sum
  git commit -m "feat(storage): query materialized duckdb views"
  ```

---

### Task 10: Parquet Fact Archive From Pebble

**Files:**

- Create: `/Users/mooyang/Documents/go/src/github.com/mooyang-code/moox/modules/storage/internal/services/device/parquet/archive.go`
- Create: `/Users/mooyang/Documents/go/src/github.com/mooyang-code/moox/modules/storage/internal/services/device/parquet/archive_test.go`
- Create: `/Users/mooyang/Documents/go/src/github.com/mooyang-code/moox/modules/storage/internal/services/archive/service.go`
- Create: `/Users/mooyang/Documents/go/src/github.com/mooyang-code/moox/modules/storage/internal/services/archive/service_test.go`

- [ ] **Step 10.1: Write failing Parquet archive test**

  Create:

  ```go
  package parquet_test

  import (
  	"context"
  	"path/filepath"
  	"testing"

  	parquetdevice "github.com/mooyang-code/moox/modules/storage/internal/services/device/parquet"
  	"github.com/mooyang-code/moox/modules/storage/pkg/quantstore"
  	pb "github.com/mooyang-code/moox/modules/storage/proto/gen"
  	"github.com/stretchr/testify/require"
  )

  func TestArchiveWritesLongFactParquet(t *testing.T) {
  	ctx := context.Background()
  	path := filepath.Join(t.TempDir(), "facts.parquet")
  	writer := parquetdevice.NewWriter()

  	err := writer.WriteFacts(ctx, path, []*pb.DataRow{{
  		Key: &pb.DataKey{
  			Scope:   &pb.DataScope{SpaceId: "crypto", DatasetId: "kline", SubjectId: "APT-USDT", Freq: "1m"},
  			DataTime: "2026-06-15T00:00:00+08:00",
  		},
  		Columns: []*pb.ColumnValue{quantstore.DoubleValue("close", 8.1)},
  	}})

  	require.NoError(t, err)
  	require.FileExists(t, path)
  }
  ```

- [ ] **Step 10.2: Run test and confirm RED**

  Run:

  ```bash
  cd /Users/mooyang/Documents/go/src/github.com/mooyang-code/moox
  go test ./modules/storage/internal/services/device/parquet -run TestArchiveWritesLongFactParquet -count=1
  ```

  Expected: FAIL because package does not exist.

- [ ] **Step 10.3: Implement long fact archive schema**

  The Parquet row struct should be:

  ```go
  type FactArchiveRow struct {
  	SpaceID        string `parquet:"space_id"`
  	DatasetID      string `parquet:"dataset_id"`
  	SubjectID      string `parquet:"subject_id"`
  	Freq           string `parquet:"freq"`
  	DimensionsJSON string `parquet:"dimensions_json"`
  	DataTime       string `parquet:"data_time"`
  	RowID          string `parquet:"row_id"`
  	ColumnName     string `parquet:"column_name"`
  	ValueType      string `parquet:"value_type"`
  	ValueJSON      string `parquet:"value_json"`
  	AttributesJSON string `parquet:"attributes_json"`
  }
  ```

  One `DataRow` with N columns writes N Parquet rows.

- [ ] **Step 10.4: Implement archive service**

  `archive.Service` should:

  ```text
  read rows from Pebble by DataScope and TimeRange
  write Parquet file under archive root
  compute content hash
  register t_archive_files via metadata store
  ```

- [ ] **Step 10.5: Run tests and commit**

  Run:

  ```bash
  cd /Users/mooyang/Documents/go/src/github.com/mooyang-code/moox
  go test ./modules/storage/internal/services/device/parquet -count=1
  go test ./modules/storage/internal/services/archive -count=1
  go test ./modules/storage/...
  git add modules/storage/internal/services/device/parquet modules/storage/internal/services/archive modules/storage/go.mod modules/storage/go.sum
  git commit -m "feat(storage): archive pebble facts to parquet"
  ```

---

### Task 11: Configuration And Bootstrap

**Files:**

- Modify: `/Users/mooyang/Documents/go/src/github.com/mooyang-code/moox/modules/storage/internal/services/common/config/loader.go`
- Modify: `/Users/mooyang/Documents/go/src/github.com/mooyang-code/moox/modules/storage/internal/services/common/config/loader_test.go`
- Modify: `/Users/mooyang/Documents/go/src/github.com/mooyang-code/moox/modules/storage/cmd/moox-storage/main.go`
- Modify: `/Users/mooyang/Documents/go/src/github.com/mooyang-code/moox/modules/storage/BUILD.md`
- Modify: `/Users/mooyang/Documents/go/src/github.com/mooyang-code/moox/modules/storage/DEPLOY.md`

- [ ] **Step 11.1: Write failing config test**

  Add:

  ```go
  func TestLoadStorageRuntimePaths(t *testing.T) {
  	path := filepath.Join(t.TempDir(), "storage.yaml")
  	require.NoError(t, os.WriteFile(path, []byte(`
  storage:
    root: /tmp/moox-storage
    metadata_path: /tmp/moox-storage/metadata/storage_metadata.db
    pebble_path: /tmp/moox-storage/pebble
    duckdb_path: /tmp/moox-storage/duckdb/moox.duckdb
    bleve_path: /tmp/moox-storage/bleve
    parquet_path: /tmp/moox-storage/archive
    node_id: adapter-1
  `), 0o644))

  	cfg, err := config.Load(path)
  	require.NoError(t, err)
  	require.Equal(t, "adapter-1", cfg.Storage.NodeID)
  	require.Equal(t, "/tmp/moox-storage/pebble", cfg.Storage.PebblePath)
  }
  ```

- [ ] **Step 11.2: Run test and confirm RED**

  Run:

  ```bash
  cd /Users/mooyang/Documents/go/src/github.com/mooyang-code/moox
  go test ./modules/storage/internal/services/common/config -run TestLoadStorageRuntimePaths -count=1
  ```

  Expected: FAIL because config fields do not exist.

- [ ] **Step 11.3: Add config struct**

  Add:

  ```go
  type StorageConfig struct {
  	Root         string `yaml:"root"`
  	MetadataPath string `yaml:"metadata_path"`
  	PebblePath   string `yaml:"pebble_path"`
  	DuckDBPath   string `yaml:"duckdb_path"`
  	BlevePath    string `yaml:"bleve_path"`
  	ParquetPath  string `yaml:"parquet_path"`
  	NodeID       string `yaml:"node_id"`
  }
  ```

- [ ] **Step 11.4: Wire main**

  `cmd/moox-storage/main.go` should:

  ```text
  load config
  open SQLite metadata
  init schema
  open Pebble
  open local adapter
  create storage.Service with explicit dependencies
  register MetadataService/DataService/QueryService/AdapterService
  serve tRPC
  ```

- [ ] **Step 11.5: Run tests and commit**

  Run:

  ```bash
  cd /Users/mooyang/Documents/go/src/github.com/mooyang-code/moox
  go test ./modules/storage/internal/services/common/config -count=1
  go test ./modules/storage/...
  git add modules/storage/internal/services/common/config modules/storage/cmd/moox-storage modules/storage/BUILD.md modules/storage/DEPLOY.md
  git commit -m "feat(storage): bootstrap storage runtime devices"
  ```

---

### Task 12: Acceptance Data Import And Query Verification

**Files:**

- Create: `/Users/mooyang/Documents/go/src/github.com/mooyang-code/moox/modules/storage/internal/services/storage/acceptance_test.go`
- Modify: `/Users/mooyang/Documents/go/src/github.com/mooyang-code/moox/scripts/acceptance.sh`
- Local data: `/Users/mooyang/Downloads/APT-USDT.csv`
- Local data: `/Users/mooyang/Downloads/AR-USDT.csv`

- [ ] **Step 12.1: Write failing end-to-end acceptance test**

  Add:

  ```go
  func TestAcceptanceWriteAndReadKlineRows(t *testing.T) {
  	ctx := context.Background()
  	svc := NewService(t.TempDir())
  	createAcceptanceMetadata(t, ctx, svc)

  	rows := []*pb.DataRow{{
  		Key: &pb.DataKey{
  			Scope:   &pb.DataScope{SpaceId: "crypto", DatasetId: "binance_spot_kline", SubjectId: "APT-USDT", Freq: "1m"},
  			DataTime: "2026-06-15T00:00:00+08:00",
  		},
  		Columns: []*pb.ColumnValue{
  			quantstore.DoubleValue("open", 8.0),
  			quantstore.DoubleValue("close", 8.1),
  		},
  	}}

  	writeRsp, err := svc.WriteRows(ctx, &pb.WriteRowsReq{WriteMode: pb.WriteMode_WRITE_MODE_UPSERT, Rows: rows})
  	require.NoError(t, err)
  	require.Equal(t, pb.ErrorCode_SUCCESS, writeRsp.GetRetInfo().GetCode())

  	readRsp, err := svc.ReadRows(ctx, &pb.ReadRowsReq{
  		Scope: &pb.DataScope{SpaceId: "crypto", DatasetId: "binance_spot_kline", SubjectId: "APT-USDT", Freq: "1m"},
  		TimeRange: &pb.TimeRange{
  			StartTime: "2026-06-15T00:00:00+08:00",
  			EndTime:   "2026-06-15T00:00:00+08:00",
  		},
  		ColumnNames: []string{"open", "close"},
  	})
  	require.NoError(t, err)
  	require.Equal(t, pb.ErrorCode_SUCCESS, readRsp.GetRetInfo().GetCode())
  	require.Len(t, readRsp.GetRows(), 1)
  }

  func createAcceptanceMetadata(t *testing.T, ctx context.Context, svc *Service) {
  	t.Helper()

  	spaceRsp, err := svc.CreateSpace(ctx, &pb.CreateSpaceReq{Space: &pb.Space{SpaceId: "crypto", Name: "crypto", Status: "active"}})
  	require.NoError(t, err)
  	require.Equal(t, pb.ErrorCode_SUCCESS, spaceRsp.GetRetInfo().GetCode())

  	sourceRsp, err := svc.CreateDataSource(ctx, &pb.CreateDataSourceReq{DataSource: &pb.DataSource{SpaceId: "crypto", DataSourceId: "binance", Name: "Binance", Kind: "exchange", Status: "active"}})
  	require.NoError(t, err)
  	require.Equal(t, pb.ErrorCode_SUCCESS, sourceRsp.GetRetInfo().GetCode())

  	subjectRsp, err := svc.UpsertSubject(ctx, &pb.UpsertSubjectReq{Subject: &pb.Subject{SpaceId: "crypto", SubjectId: "APT-USDT", SubjectType: "crypto_pair", Name: "APT-USDT", Status: "active"}})
  	require.NoError(t, err)
  	require.Equal(t, pb.ErrorCode_SUCCESS, subjectRsp.GetRetInfo().GetCode())

  	datasetRsp, err := svc.CreateDataSet(ctx, &pb.CreateDataSetReq{Dataset: &pb.DataSet{SpaceId: "crypto", DatasetId: "binance_spot_kline", DataSourceId: "binance", Name: "Binance 现货K线", DataKind: pb.DataKind_DATA_KIND_TIME_SERIES, Freqs: []string{"1m"}, Status: "active"}})
  	require.NoError(t, err)
  	require.Equal(t, pb.ErrorCode_SUCCESS, datasetRsp.GetRetInfo().GetCode())

  	bindRsp, err := svc.BindDataSetSubject(ctx, &pb.BindDataSetSubjectReq{Subject: &pb.DataSetSubject{SpaceId: "crypto", DatasetId: "binance_spot_kline", SubjectId: "APT-USDT", Status: "active"}})
  	require.NoError(t, err)
  	require.Equal(t, pb.ErrorCode_SUCCESS, bindRsp.GetRetInfo().GetCode())

  	for _, columnName := range []string{"open", "close"} {
  		fieldRsp, err := svc.CreateField(ctx, &pb.CreateFieldReq{Field: &pb.Field{SpaceId: "crypto", FieldId: columnName, Name: columnName, ValueType: pb.FieldValueType_FIELD_VALUE_TYPE_DOUBLE, Status: "active"}})
  		require.NoError(t, err)
  		require.Equal(t, pb.ErrorCode_SUCCESS, fieldRsp.GetRetInfo().GetCode())

  		columnRsp, err := svc.UpsertDataSetColumn(ctx, &pb.UpsertDataSetColumnReq{Column: &pb.DataSetColumn{SpaceId: "crypto", DatasetId: "binance_spot_kline", ColumnName: columnName, OriginType: pb.ColumnOriginType_COLUMN_ORIGIN_TYPE_FIELD, OriginId: fieldRsp.GetField().GetFieldId(), ValueType: pb.FieldValueType_FIELD_VALUE_TYPE_DOUBLE, Status: "active"}})
  		require.NoError(t, err)
  		require.Equal(t, pb.ErrorCode_SUCCESS, columnRsp.GetRetInfo().GetCode())
  	}

  	nodeRsp, err := svc.CreateStorageNode(ctx, &pb.CreateStorageNodeReq{Node: &pb.StorageNode{NodeId: "adapter-1", Name: "adapter-1", Endpoint: "local", Status: "active"}})
  	require.NoError(t, err)
  	require.Equal(t, pb.ErrorCode_SUCCESS, nodeRsp.GetRetInfo().GetCode())

  	routeRsp, err := svc.CreateStorageRoute(ctx, &pb.CreateStorageRouteReq{StorageRoute: &pb.StorageRoute{SpaceId: "crypto", RouteId: "route-binance-spot-kline", DatasetId: "binance_spot_kline", SubjectPattern: "*", NodeId: nodeRsp.GetNode().GetNodeId(), Priority: 100, Status: "active"}})
  	require.NoError(t, err)
  	require.Equal(t, pb.ErrorCode_SUCCESS, routeRsp.GetRetInfo().GetCode())
  }
  ```

- [ ] **Step 12.2: Run test and confirm RED**

  Run:

  ```bash
  cd /Users/mooyang/Documents/go/src/github.com/mooyang-code/moox
  go test ./modules/storage/internal/services/storage -run TestAcceptanceWriteAndReadKlineRows -count=1
  ```

  Expected: FAIL until metadata, routing, adapter, and Pebble are wired together.

- [ ] **Step 12.3: Implement CSV acceptance loader outside storage core**

  Put CSV parsing in test/helper or CLI import path, not in `pkg/quantstore` physical store. The loader should:

  ```text
  read CSV header
  map time/open/high/low/close/volume columns
  create DataRow values
  call DataService.WriteRows
  ```

- [ ] **Step 12.4: Run local acceptance**

  Run:

  ```bash
  cd /Users/mooyang/Documents/go/src/github.com/mooyang-code/moox
  go test ./modules/storage/internal/services/storage -run TestAcceptanceWriteAndReadKlineRows -count=1
  go test ./modules/storage/...
  ```

  Expected: PASS.

- [ ] **Step 12.5: Prepare remote acceptance script**

  Update `scripts/acceptance.sh` to:

  ```text
  build moox-storage
  deploy under ubuntu@43.132.204.177:~/moox/storage
  start moox-storage with config copied from local storage config
  import /Users/mooyang/Downloads/APT-USDT.csv and /Users/mooyang/Downloads/AR-USDT.csv
  query back APT-USDT and AR-USDT rows
  write query result to /Users/mooyang/Downloads/moox-storage-acceptance-result.json
  ```

- [ ] **Step 12.6: Commit acceptance**

  Run:

  ```bash
  cd /Users/mooyang/Documents/go/src/github.com/mooyang-code/moox
  git add modules/storage/internal/services/storage/acceptance_test.go scripts/acceptance.sh
  git commit -m "test(storage): add kline acceptance flow"
  ```

---

### Task 13: Final Cleanup And Verification

**Files:**

- Modify: `/Users/mooyang/Documents/go/src/github.com/mooyang-code/moox/docs/storage-concepts-and-design-intent.md`
- Modify: `/Users/mooyang/Documents/go/src/github.com/mooyang-code/moox/docs/storage-target-architecture-and-metadata.md`
- Modify: `/Users/mooyang/Documents/go/src/github.com/mooyang-code/moox/docs/pb-protocol-redesign.md`
- Modify: `/Users/mooyang/Documents/go/src/github.com/mooyang-code/moox/modules/storage/BUILD.md`
- Modify: `/Users/mooyang/Documents/go/src/github.com/mooyang-code/moox/modules/storage/DEPLOY.md`

- [ ] **Step 13.1: Remove obsolete implementation references**

  Run:

  ```bash
  cd /Users/mooyang/Documents/go/src/github.com/mooyang-code/moox
  rg -n "jsonl|CSVImportOptions|RocksDB|rocksdb|StorageEntity|storage_entity|entity_id|t_storage_entities|c_entity_id" modules/storage docs schema scripts
  ```

  Expected: no result except historical plan files that are intentionally not part of active implementation.

- [ ] **Step 13.2: Run full storage verification**

  Run:

  ```bash
  cd /Users/mooyang/Documents/go/src/github.com/mooyang-code/moox
  go test ./modules/storage/...
  go test ./modules/storage/proto/gen/...
  pnpm --dir web exec vue-tsc --noEmit
  ```

  Expected: all commands exit 0.

- [ ] **Step 13.3: Run focused acceptance**

  Run:

  ```bash
  cd /Users/mooyang/Documents/go/src/github.com/mooyang-code/moox
  go test ./modules/storage/internal/services/storage -run TestAcceptanceWriteAndReadKlineRows -count=1
  ```

  Expected: PASS.

- [ ] **Step 13.4: Commit final cleanup**

  Run:

  ```bash
  cd /Users/mooyang/Documents/go/src/github.com/mooyang-code/moox
  git add docs modules/storage modules/cli/config scripts web
  git commit -m "chore(storage): finalize storage module implementation"
  ```

---

## Self-Review Checklist

- [ ] Every active storage concept uses `StorageNode/node_id`, not `StorageEntity/entity_id`.
- [ ] `StorageRoute` routes only to `StorageNode`, never directly to a `Device`.
- [ ] Pebble is the only online fact主存.
- [ ] DuckDB, Bleve and Parquet are derived from Pebble changes.
- [ ] Parquet archive reads facts from Pebble, not from DuckDB materialized View results.
- [ ] `WriteRows` returns only `ret_info`; it does not return row changes or previous rows.
- [ ] `DeleteRows` and physical delete APIs are not reintroduced.
- [ ] `QueryView` returns `VIEW_NOT_FOUND` when no existing View materialization exists.
- [ ] `SearchRows` remains DataSet-scoped and supports text query plus structured filters.
- [ ] CSV parsing exists only as import or acceptance helper, not as storage cold engine.
- [ ] `go test ./modules/storage/...` passes.
- [ ] `go test ./modules/storage/proto/gen/...` passes.
- [ ] `pnpm --dir web exec vue-tsc --noEmit` passes.

---

## Execution Handoff

Plan complete and saved to `docs/superpowers/plans/2026-06-17-storage-module-implementation.md`. Two execution options:

1. **Subagent-Driven (recommended)** - dispatch a fresh subagent per task, review between tasks, fast iteration.
2. **Inline Execution** - execute tasks in this session using executing-plans, batch execution with checkpoints.
