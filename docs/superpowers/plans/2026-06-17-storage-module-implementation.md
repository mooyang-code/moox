# moox Storage Module Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use `superpowers:subagent-driven-development` (recommended) or `superpowers:executing-plans` to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 将 `/Users/mooyang/Documents/go/src/github.com/mooyang-code/moox/modules/storage` 落地为新的量化数据存储模块：所有数据访问先进入 Access Service，Access 按语义转发到 PrimaryStore、Search、View 和 Archive 等可独立部署的内部服务。

**Architecture:** Access Service 是唯一公开数据访问入口，负责 PB 协议、元数据校验、PrimaryRoute 解析和请求转发。PrimaryStore Service 使用 Pebble 承载在线事实主存，并可按 Subject 水平切分。Search Service 使用 Bleve 承载集中式搜索索引。View Service 使用 DuckDB 承载异步物化结果。Archive Service 使用 Parquet 承载事实冷备。用户不感知底层设备细节。

**Tech Stack:** Go 1.24、tRPC-Go、Protocol Buffers、SQLite、Pebble、DuckDB、Bleve、Parquet、NATS、YAML、Makefile、shell scripts。

---

## 0. 执行约束

- 项目未上线，不做旧接口兼容。
- 删除旧 JSONL/CSV 主存路径，不保留 RocksDB。
- CSV 只作为验收输入文件，不作为存储引擎；冷备使用 Parquet。
- 不提供用户删除数据能力。
- 不实现临时组合查询；不存在的组合直接返回 `VIEW_NOT_FOUND`。
- 写入接口不返回行变更、旧值、写入数量。
- 执行前先提交并 push 当前基线，后续每个阶段小步提交。
- 当前工作区可能已有未提交实现变更，执行时必须先 `git status --short`，不要回滚用户或其他任务的改动。

## 1. 参考文件

**设计文档：**

- `/Users/mooyang/Documents/go/src/github.com/mooyang-code/moox/docs/storage-concepts-and-design-intent.md`
- `/Users/mooyang/Documents/go/src/github.com/mooyang-code/moox/docs/storage-target-architecture-and-metadata.md`
- `/Users/mooyang/Documents/go/src/github.com/mooyang-code/moox/docs/pb-protocol-redesign.md`

**元数据表：**

- `/Users/mooyang/Documents/go/src/github.com/mooyang-code/moox/modules/storage/schema/storage_metadata.sql`
- `/Users/mooyang/Documents/go/src/github.com/mooyang-code/moox/modules/storage/schema/admin_console.sql`

**协议：**

- `/Users/mooyang/Documents/go/src/github.com/mooyang-code/moox/modules/storage/proto/common.proto`
- `/Users/mooyang/Documents/go/src/github.com/mooyang-code/moox/modules/storage/proto/metadata.proto`
- `/Users/mooyang/Documents/go/src/github.com/mooyang-code/moox/modules/storage/proto/data.proto`
- `/Users/mooyang/Documents/go/src/github.com/mooyang-code/moox/modules/storage/proto/query.proto`
- `/Users/mooyang/Documents/go/src/github.com/mooyang-code/moox/modules/storage/proto/primary.proto`
- `/Users/mooyang/Documents/go/src/github.com/mooyang-code/moox/modules/storage/proto/message.proto`

**旧 xData 参考：**

- `/Users/mooyang/Documents/go/src/github.com/mooyang-code/xData-mini/storage`

## 2. 目标文件结构

```text
/Users/mooyang/Documents/go/src/github.com/mooyang-code/moox/modules/storage
├── cmd/moox-storage
├── config
├── internal
│   ├── config
│   ├── core
│   │   ├── eventbus
│   │   ├── metadata
│   │   ├── router
│   │   └── schema
│   ├── infra
│   │   ├── device
│   │   │   ├── bleve
│   │   │   ├── duckdb
│   │   │   ├── parquet
│   │   │   └── pebble
│   │   ├── eventbus
│   │   ├── metadata
│   │   │   └── sqlite
│   │   └── transport
│   │       └── nats
│   └── services
│       ├── access
│       ├── primary
│       ├── search
│       ├── view
│       └── archive
└── proto
```

职责边界：

- `services/access`: 唯一公开数据访问入口，组合元数据、校验器、PrimaryRoute、PrimaryStore/Search/View/Archive client 和查询转发。
- `services/primary`: PrimaryStore Service，负责 Pebble 在线事实主存读写，可多实例部署。
- `services/search`: Search Service，根据主存变更维护 Bleve 集中式索引，并执行 `SearchRows`。
- `services/view`: View Service，根据 View 元数据构建 DuckDB 物化结果，并执行 `QueryView`。
- `services/archive`: Archive Service，从 Pebble 事实数据归档 Parquet 并登记 `ArchiveFile`。
- `core/metadata`: 元数据存储接口。
- `core/schema`: 写入契约校验，校验 DataSet、DataSetColumn、类型、必填列。
- `core/router`: 根据 PrimaryRoute 把在线事实主存路由到 PrimaryNode。当前协议和表结构仍使用 StorageRoute/StorageNode 旧名。
- `core/eventbus`: 写入 Pebble 后发布 `DataRowsChangedEvent` 等 storage 领域事件。
- `infra/metadata/sqlite`: `modules/storage/schema/storage_metadata.sql` 的 SQLite 持久化实现。
- `infra/device/pebble`: 在线事实主存。
- `infra/device/duckdb`: View 物化结果存储和查询。
- `infra/device/bleve`: 文本索引。
- `infra/device/parquet`: 从 Pebble 事实归档生成 Parquet。
- `infra/eventbus`: 把底层 transport producer 适配为业务 `eventbus.Bus`。
- `infra/transport`: NATS 等底层消息传输实现，供 eventbus 的具体实现复用。
- `internal/config`: moox-storage 运行配置加载。

## 3. 核心概念验收

实现必须满足：

- `Space` 是业务命名空间；本文所有全局 ID 均限定在 Space 内。
- `DataSource` 表示数据来源，交易所只是其中一种。
- `Subject` 是 Space 内业务对象，不直接归属 DataSource。
- `SubjectSymbol` 表示 Subject 在某个 DataSource 下的外部代码。
- `DataSet` 是可写事实数据集，并且只绑定一个 DataSource。
- `DataSetSubject` 是 DataSet 的对象池。
- `Field` 和 `Factor` 是 Space 内字典，进入 DataSet 时统一成为 `DataSetColumn`。
- `DataSetColumn.text_indexed` 控制是否同步到 Bleve。
- `View` 是查询入口和物化结果定义；创建时确定 `primary_dataset_id`。
- `QueryView` 只查询已有 View 的 `active_result`；没有可用结果返回 `VIEW_NOT_FOUND`。
- PrimaryRoute 只记录在线事实主存到 PrimaryNode 的水平切分；当前协议和表结构仍使用 StorageRoute/StorageNode 旧名。
- PrimaryNode 是 PrimaryStore Service 节点；Device 是底层实际存储组件。
- SearchRows 查询 Search Service 的集中式索引，不走 PrimaryRoute。
- QueryView 查询 View Service 的物化结果，不走 PrimaryRoute。

---

## Task 0: 基线提交与执行护栏

**Files:**

- Modify: `/Users/mooyang/Documents/go/src/github.com/mooyang-code/moox/docs/superpowers/plans/2026-06-17-storage-module-implementation.md`
- Read: `/Users/mooyang/Documents/go/src/github.com/mooyang-code/moox/modules/storage/go.mod`
- Read: `/Users/mooyang/Documents/go/src/github.com/mooyang-code/moox/go.work`

- [ ] **Step 0.1: 记录当前工作区**

  Run:

  ```bash
  cd /Users/mooyang/Documents/go/src/github.com/mooyang-code/moox
  git status --short
  ```

  Expected: 输出被记录到执行日志；不回滚任何未确认改动。

- [ ] **Step 0.2: 先提交并 push 执行前基线**

  Run:

  ```bash
  cd /Users/mooyang/Documents/go/src/github.com/mooyang-code/moox
  git add docs/superpowers/plans/2026-06-17-storage-module-implementation.md
  git commit -m "docs(storage): regenerate implementation plan"
  git push
  ```

  Expected: 文档基线已推送。若工作区还有实现文件未提交，先和用户确认是否一并提交；不要把不相关改动混进文档提交。

- [ ] **Step 0.3: 跑当前基线测试**

  Run:

  ```bash
  cd /Users/mooyang/Documents/go/src/github.com/mooyang-code/moox
  go test ./modules/storage/...
  go test ./modules/storage/proto/gen/...
  ```

  Expected: 记录当前通过或失败状态。若失败，保留失败输出，后续任务必须修复 storage 模块失败。

- [ ] **Step 0.4: 固定 Go workspace 版本**

  Run:

  ```bash
  cd /Users/mooyang/Documents/go/src/github.com/mooyang-code/moox
  go work edit -go=1.24.0
  head -n 5 go.work
  ```

  Expected: `go.work` 顶部显示 `go 1.24.0`。

---

## Task 1: 协议与元数据契约测试

**Files:**

- Modify: `/Users/mooyang/Documents/go/src/github.com/mooyang-code/moox/modules/storage/internal/services/protocol_contract_test.go`
- Modify: `/Users/mooyang/Documents/go/src/github.com/mooyang-code/moox/modules/storage/internal/services/storage_metadata_schema_test.go`

- [ ] **Step 1.1: 增加禁止旧概念的契约测试**

  在 `protocol_contract_test.go` 中确保源码不再出现这些旧路径和旧概念：

  ```go
  requireNoProjectSourceContains(t, root, ".jsonl")
  requireNoProjectSourceContains(t, root, "CSVImportOptions")
  requireNoProjectSourceContains(t, root, "RocksDB")
  requireNoProjectSourceContains(t, root, "StorageEntity")
  requireNoProjectSourceContains(t, root, "object_id")
  requireNoProjectSourceContains(t, root, "DataView")
  ```

- [ ] **Step 1.2: 增加 schema 表名契约测试**

  在 `storage_metadata_schema_test.go` 中校验必须存在：

  ```go
  require.Contains(t, schema, "CREATE TABLE IF NOT EXISTS t_spaces")
  require.Contains(t, schema, "CREATE TABLE IF NOT EXISTS t_data_sources")
  require.Contains(t, schema, "CREATE TABLE IF NOT EXISTS t_subjects")
  require.Contains(t, schema, "CREATE TABLE IF NOT EXISTS t_subject_symbols")
  require.Contains(t, schema, "CREATE TABLE IF NOT EXISTS t_datasets")
  require.Contains(t, schema, "CREATE TABLE IF NOT EXISTS t_dataset_subjects")
  require.Contains(t, schema, "CREATE TABLE IF NOT EXISTS t_dataset_columns")
  require.Contains(t, schema, "CREATE TABLE IF NOT EXISTS t_views")
  require.Contains(t, schema, "CREATE TABLE IF NOT EXISTS t_view_columns")
  require.Contains(t, schema, "CREATE TABLE IF NOT EXISTS t_storage_nodes")
  require.Contains(t, schema, "CREATE TABLE IF NOT EXISTS t_storage_devices")
  require.Contains(t, schema, "CREATE TABLE IF NOT EXISTS t_storage_routes")
  require.Contains(t, schema, "CREATE TABLE IF NOT EXISTS t_archive_files")
  ```

- [ ] **Step 1.3: 跑契约测试**

  Run:

  ```bash
  cd /Users/mooyang/Documents/go/src/github.com/mooyang-code/moox
  go test ./modules/storage/internal/services -run 'Test.*Contract|Test.*Schema' -count=1
  ```

  Expected: 初次可能失败；完成旧代码清理后必须 PASS。

- [ ] **Step 1.4: 提交**

  Run:

  ```bash
  git add modules/storage/internal/services/protocol_contract_test.go modules/storage/internal/services/storage_metadata_schema_test.go
  git commit -m "test(storage): lock storage architecture contracts"
  ```

---

## Task 2: SQLite 元数据存储

**Files:**

- Create/Modify: `/Users/mooyang/Documents/go/src/github.com/mooyang-code/moox/modules/storage/internal/core/metadata/store.go`
- Create/Modify: `/Users/mooyang/Documents/go/src/github.com/mooyang-code/moox/modules/storage/internal/infra/metadata/sqlite/store.go`
- Create/Modify: `/Users/mooyang/Documents/go/src/github.com/mooyang-code/moox/modules/storage/internal/infra/metadata/sqlite/crud.go`
- Create/Modify: `/Users/mooyang/Documents/go/src/github.com/mooyang-code/moox/modules/storage/internal/infra/metadata/sqlite/store_test.go`
- Modify: `/Users/mooyang/Documents/go/src/github.com/mooyang-code/moox/modules/storage/go.mod`

- [ ] **Step 2.1: 写 schema 初始化测试**

  Test:

  ```go
  func TestStoreInitializesStorageMetadataSchema(t *testing.T) {
      ctx := context.Background()
      dbPath := filepath.Join(t.TempDir(), "storage_metadata.db")
      schemaPath := "/Users/mooyang/Documents/go/src/github.com/mooyang-code/moox/modules/storage/schema/storage_metadata.sql"

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

- [ ] **Step 2.2: 写核心 CRUD 测试**

  必须覆盖：

  ```text
  Space
  DataSource
  Subject
  SubjectSymbol
  DataSet
  DataSetSubject
  Field
  Factor
  DataSetColumn
  View
  ViewColumn
  PrimaryNode
  Device
  PrimaryRoute
  ArchiveFile
  ```

  每类测试至少验证 `Upsert`、`Get`、关键 `List` 过滤条件。

- [ ] **Step 2.3: 实现 metadata.Store 接口**

  Store 接口必须包含 storage 服务需要的所有方法：

  ```go
  type Store interface {
      Close() error
      InitSchema(ctx context.Context) error
      TableNames(ctx context.Context) ([]string, error)

      UpsertSpace(ctx context.Context, item *pb.Space) (*pb.Space, error)
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

      UpsertPrimaryNode(ctx context.Context, item *pb.PrimaryNode) (*pb.PrimaryNode, error)
      GetPrimaryNode(ctx context.Context, nodeID string) (*pb.PrimaryNode, error)
      UpsertDevice(ctx context.Context, item *pb.Device) (*pb.Device, error)
      ListDevices(ctx context.Context, nodeID string, engine string, page *pb.Page) ([]*pb.Device, *pb.PageResult, error)
      UpsertPrimaryRoute(ctx context.Context, item *pb.PrimaryRoute) (*pb.PrimaryRoute, error)
      ListPrimaryRoutes(ctx context.Context, spaceID string, datasetID string, subjectID string, nodeID string, page *pb.Page) ([]*pb.PrimaryRoute, *pb.PageResult, error)

      RegisterArchiveFile(ctx context.Context, item *pb.ArchiveFile) (*pb.ArchiveFile, error)
      ListArchiveFiles(ctx context.Context, spaceID string, datasetID string, page *pb.Page) ([]*pb.ArchiveFile, *pb.PageResult, error)
  }
  ```

- [ ] **Step 2.4: 跑元数据测试**

  Run:

  ```bash
  go test ./modules/storage/internal/core/metadata/... -count=1
  ```

  Expected: PASS。

- [ ] **Step 2.5: 提交**

  Run:

  ```bash
  git add modules/storage/internal/core/metadata modules/storage/go.mod modules/storage/go.sum
  git commit -m "feat(storage): add sqlite metadata store"
  ```

---

## Task 3: 写入契约校验

**Files:**

- Create/Modify: `/Users/mooyang/Documents/go/src/github.com/mooyang-code/moox/modules/storage/internal/core/schema/validator.go`
- Create/Modify: `/Users/mooyang/Documents/go/src/github.com/mooyang-code/moox/modules/storage/internal/core/schema/validator_test.go`
- Modify: `/Users/mooyang/Documents/go/src/github.com/mooyang-code/moox/modules/storage/internal/services/access/data.go`

- [ ] **Step 3.1: 写失败测试**

  覆盖这些场景：

  ```text
  DataSet 不存在 -> DATASET_NOT_FOUND
  写入列未登记 -> INVALID_ARGUMENT
  写入列类型不匹配 -> INVALID_ARGUMENT
  required 列缺失 -> INVALID_ARGUMENT
  正确行 -> PASS
  ```

- [ ] **Step 3.2: 实现 Validator**

  Validator 只依赖 `metadata.Store`，不访问物理设备。校验规则：

  - `space_id`、`dataset_id` 必填。
  - `dataset_id` 必须存在且状态可用。
  - `DataRow.columns[*].column_name` 必须存在于 `DataSetColumn`。
  - `ColumnValue.value_type` 必须匹配 `DataSetColumn.value_type`。
  - `required=true` 的列必须存在。

- [ ] **Step 3.3: 接入 WriteRows**

  `WriteRows` 顺序：

  ```text
  validate request
  validate schema
  resolve route
  write through PrimaryStore
  publish change event
  return ret_info only
  ```

- [ ] **Step 3.4: 跑测试并提交**

  Run:

  ```bash
  go test ./modules/storage/internal/core/schema ./modules/storage/internal/services/access -run 'Test.*WriteRows|Test.*Validator' -count=1
  git add modules/storage/internal/core/schema modules/storage/internal/services/access
  git commit -m "feat(storage): validate dataset write contracts"
  ```

---

## Task 4: Pebble 在线事实主存

**Files:**

- Create/Modify: `/Users/mooyang/Documents/go/src/github.com/mooyang-code/moox/modules/storage/internal/infra/device/store.go`
- Create/Modify: `/Users/mooyang/Documents/go/src/github.com/mooyang-code/moox/modules/storage/internal/infra/device/pebble/key.go`
- Create/Modify: `/Users/mooyang/Documents/go/src/github.com/mooyang-code/moox/modules/storage/internal/infra/device/pebble/store.go`
- Create/Modify: `/Users/mooyang/Documents/go/src/github.com/mooyang-code/moox/modules/storage/internal/infra/device/pebble/store_test.go`
- Create/Modify: `/Users/mooyang/Documents/go/src/github.com/mooyang-code/moox/modules/storage/internal/services/primary/local.go`
- Modify: `/Users/mooyang/Documents/go/src/github.com/mooyang-code/moox/modules/storage/go.mod`

- [ ] **Step 4.1: 写 Pebble 主存测试**

  必须覆盖：

  ```text
  WriteRows 后 ReadRows RANGE 可读取
  POINT 按 row_id 读取
  LATEST_BEFORE 返回截面前最新行
  同 scope+data_time 覆盖写入保持幂等
  dimensions 参与 key 前缀
  ```

- [ ] **Step 4.2: 设计 key**

  Pebble key 格式必须稳定、可前缀扫描：

  ```text
  fact/{space_id}/{dataset_id}/{subject_id}/{freq}/{dimensions_hash}/{data_time}/{row_id}
  ```

  `dimensions_hash` 由 dimensions 按 key 排序后生成，避免 map 顺序影响。

- [ ] **Step 4.3: 删除 JSONL 主存**

  在线主存不再写 `facts/*.jsonl`，也不再暴露公开存储 helper 包。主存访问收敛到 `services/primary`，底层通过 Pebble device 实现。

- [ ] **Step 4.4: 跑测试并提交**

  Run:

  ```bash
  go test ./modules/storage/internal/infra/device/pebble ./modules/storage/internal/services/primary -count=1
  go test ./modules/storage/internal/services -run TestStorageProtocolUsesCanonicalSurface -count=1
  git add modules/storage/internal/infra/device modules/storage/internal/services/primary modules/storage/go.mod modules/storage/go.sum
  git commit -m "feat(storage): use pebble as fact store"
  ```

---

## Task 5: PrimaryRoute 与 PrimaryStore 写入链路

**Files:**

- Create/Modify: `/Users/mooyang/Documents/go/src/github.com/mooyang-code/moox/modules/storage/internal/core/router/resolver.go`
- Create/Modify: `/Users/mooyang/Documents/go/src/github.com/mooyang-code/moox/modules/storage/internal/core/router/resolver_test.go`
- Create/Modify: `/Users/mooyang/Documents/go/src/github.com/mooyang-code/moox/modules/storage/internal/services/primary/client.go`
- Create/Modify: `/Users/mooyang/Documents/go/src/github.com/mooyang-code/moox/modules/storage/internal/services/primary/service.go`
- Create/Modify: `/Users/mooyang/Documents/go/src/github.com/mooyang-code/moox/modules/storage/internal/services/primary/service_test.go`
- Modify: `/Users/mooyang/Documents/go/src/github.com/mooyang-code/moox/modules/storage/internal/services/access/data.go`

- [ ] **Step 5.1: 写路由测试**

  覆盖：

  ```text
  精确 subject_id 路由优先
  subject_pattern 次优先
  dataset 默认路由兜底
  priority 数值越小越优先
  找不到路由 -> ROUTE_NOT_FOUND
  ```

- [ ] **Step 5.2: 实现 Resolver**

  PrimaryRoute 元数据只绑定到 PrimaryNode。当前协议和表结构仍使用 StorageRoute/StorageNode 旧名。Resolver 先按 subject 精确、subject pattern、dataset 默认路由选择 PrimaryNode，再生成内部路由结果。

- [ ] **Step 5.3: 实现 PrimaryStore client**

  接口：

  ```go
  type Client interface {
      WriteRows(ctx context.Context, target *PrimaryTarget, rows []*pb.DataRow, mode pb.WriteMode) error
      ReadRows(ctx context.Context, target *PrimaryTarget, req *pb.ReadRowsReq) ([]*pb.DataRow, *pb.PageResult, error)
  }
  ```

- [ ] **Step 5.4: 实现本地 PrimaryStore**

  单进程模式用于本地测试和个人部署：

  ```text
  PrimaryNode -> 找到 node 下 engine=pebble 的 Device -> 写入 Pebble
  ```

- [ ] **Step 5.5: WriteRows 接入 PrimaryStore**

  `storage.WriteRows` 按 PrimaryRoute 分组，分别调用 PrimaryStore。成功后不返回 affected/change，只返回 `ret_info`。

- [ ] **Step 5.6: 跑测试并提交**

  Run:

  ```bash
  go test ./modules/storage/internal/core/router ./modules/storage/internal/services/primary ./modules/storage/internal/services/access -run 'Test.*Route|Test.*Primary|Test.*WriteRows' -count=1
  git add modules/storage/internal/core/router modules/storage/internal/services/primary modules/storage/internal/services/access
  git commit -m "feat(storage): route writes through primary store"
  ```

---

## Task 6: EventBus 事件发布

**Files:**

- Create/Modify: `/Users/mooyang/Documents/go/src/github.com/mooyang-code/moox/modules/storage/internal/core/eventbus/bus.go`
- Create/Modify: `/Users/mooyang/Documents/go/src/github.com/mooyang-code/moox/modules/storage/internal/core/eventbus/bus_test.go`
- Create/Modify: `/Users/mooyang/Documents/go/src/github.com/mooyang-code/moox/modules/storage/internal/infra/transport/producer.go`
- Create/Modify: `/Users/mooyang/Documents/go/src/github.com/mooyang-code/moox/modules/storage/internal/infra/transport/nats/producer.go`
- Modify: `/Users/mooyang/Documents/go/src/github.com/mooyang-code/moox/modules/storage/internal/services/access/data.go`
- Check: `/Users/mooyang/Documents/go/src/github.com/mooyang-code/moox/modules/storage/proto/message.proto`

- [ ] **Step 6.1: 写事件测试**

  Test:

  ```text
  WriteRows 成功后发布 DataRowsChangedEvent
  event.scope 与写入 scope 一致
  event.rows 与写入 rows 一致
  event_time 非空
  ```

- [ ] **Step 6.2: 实现 EventBus 抽象**

  上层使用 `eventbus.Bus`，底层消息传输使用 `transport.Producer`。单元测试使用 `eventbus.MemoryBus`。

- [ ] **Step 6.3: WriteRows 成功后发布事件**

  顺序必须是：

  ```text
  Pebble 写入成功 -> 发布事件 -> 返回成功
  ```

  若发布失败，当前阶段返回失败，避免派生层漏数据。

- [ ] **Step 6.4: 跑测试并提交**

  Run:

  ```bash
  go test ./modules/storage/internal/core/eventbus ./modules/storage/internal/infra/transport ./modules/storage/internal/services/access -run 'Test.*Event|Test.*WriteRows' -count=1
  git add modules/storage/internal/core/eventbus modules/storage/internal/infra/transport modules/storage/internal/services/access
  git commit -m "feat(storage): publish fact row change events"
  ```

---

## Task 7: Bleve 文本索引

**Files:**

- Create/Modify: `/Users/mooyang/Documents/go/src/github.com/mooyang-code/moox/modules/storage/internal/infra/device/bleve/index.go`
- Create/Modify: `/Users/mooyang/Documents/go/src/github.com/mooyang-code/moox/modules/storage/internal/infra/device/bleve/index_test.go`
- Modify: `/Users/mooyang/Documents/go/src/github.com/mooyang-code/moox/modules/storage/internal/services/access/data.go`
- Modify: `/Users/mooyang/Documents/go/src/github.com/mooyang-code/moox/modules/storage/internal/services/access/query.go`
- Modify: `/Users/mooyang/Documents/go/src/github.com/mooyang-code/moox/modules/storage/go.mod`

- [ ] **Step 7.1: 写 Bleve 测试**

  覆盖：

  ```text
  text_indexed=true 的列被索引
  text_indexed=false 的列不进入全文索引
  text_query 可召回 DataRow
  text_query 为空时支持结构化过滤
  ```

- [ ] **Step 7.2: 实现 Index**

  文档 ID 使用事实主键：

  ```text
  {space_id}/{dataset_id}/{subject_id}/{freq}/{dimensions_hash}/{data_time}/{row_id}
  ```

  文档内容包含：

  ```text
  space_id
  dataset_id
  subject_id
  freq
  data_time
  row_id
  indexed text columns
  ```

- [ ] **Step 7.3: WriteRows 后同步索引**

  仅同步 `DataSetColumn.text_indexed=true` 的列，避免无关字段拖慢 Bleve。

- [ ] **Step 7.4: 实现 SearchRows**

  `SearchRows` 支持：

  ```text
  text_query
  subject_ids
  time_range
  filters
  sorts
  column_names
  page
  ```

  搜索结果返回 `data.DataRow`，维度仍是 DataSet。

- [ ] **Step 7.5: 跑测试并提交**

  Run:

  ```bash
  go test ./modules/storage/internal/infra/device/bleve ./modules/storage/internal/services/access -run 'Test.*Search|Test.*Bleve' -count=1
  git add modules/storage/internal/infra/device/bleve modules/storage/internal/services/access modules/storage/go.mod modules/storage/go.sum
  git commit -m "feat(storage): search rows with bleve index"
  ```

---

## Task 8: DuckDB View 物化结果查询

**Files:**

- Create/Modify: `/Users/mooyang/Documents/go/src/github.com/mooyang-code/moox/modules/storage/internal/infra/device/duckdb/view_store.go`
- Create/Modify: `/Users/mooyang/Documents/go/src/github.com/mooyang-code/moox/modules/storage/internal/infra/device/duckdb/view_store_fallback.go`
- Create/Modify: `/Users/mooyang/Documents/go/src/github.com/mooyang-code/moox/modules/storage/internal/infra/device/duckdb/view_store_test.go`
- Modify: `/Users/mooyang/Documents/go/src/github.com/mooyang-code/moox/modules/storage/internal/services/access/query.go`
- Modify: `/Users/mooyang/Documents/go/src/github.com/mooyang-code/moox/modules/storage/internal/services/access/service.go`
- Modify: `/Users/mooyang/Documents/go/src/github.com/mooyang-code/moox/modules/storage/internal/services/access/service_test.go`
- Modify: `/Users/mooyang/Documents/go/src/github.com/mooyang-code/moox/modules/storage/go.mod`

- [ ] **Step 8.1: 写 View 未物化测试**

  `View.active_result` 为空时：

  ```text
  QueryView -> VIEW_NOT_FOUND
  ```

- [ ] **Step 8.2: 写 DuckDB ViewStore 测试**

  覆盖：

  ```text
  CreateResultTable 创建结果表
  InsertRows 写入 QueryViewRow
  QueryView 支持 subject_ids
  QueryView 支持 time_range
  QueryView 支持 column_names 投影
  QueryView 支持 filters/sorts/page
  ```

- [ ] **Step 8.3: 实现 ViewStore**

  `ViewStore` 对外方法：

  ```go
  type ViewStore struct {}

  func Open(ctx context.Context, path string) (*ViewStore, error)
  func (s *ViewStore) Close() error
  func (s *ViewStore) CreateResultTable(ctx context.Context, resultName string, columns []*pb.QueryViewColumn) error
  func (s *ViewStore) InsertRows(ctx context.Context, resultName string, rows []*pb.QueryViewRow) error
  func (s *ViewStore) QueryView(ctx context.Context, resultName string, req *pb.QueryViewReq) ([]*pb.QueryViewColumn, []*pb.QueryViewRow, *pb.PageResult, error)
  ```

  `view_store.go` 使用真实 DuckDB driver；`view_store_fallback.go` 在 `!cgo` 下提供内存 fallback，保证默认 `go test ./modules/storage/...` 可跑。

- [ ] **Step 8.4: QueryView 只查 active_result**

  `storage.QueryView` 逻辑：

  ```text
  GetView(space_id, view_id)
  active_result 为空 -> VIEW_NOT_FOUND
  调 DuckDB ViewStore.QueryView(active_result, req)
  返回 QueryViewRsp
  ```

  不再从 Pebble 临时拼 View。

- [ ] **Step 8.5: 跑测试并提交**

  Run:

  ```bash
  go test ./modules/storage/internal/infra/device/duckdb -count=1
  CGO_ENABLED=1 go test ./modules/storage/internal/infra/device/duckdb -count=1
  go test ./modules/storage/internal/services/access -run TestQueryView -count=1
  git add modules/storage/internal/infra/device/duckdb modules/storage/internal/services/access modules/storage/go.mod modules/storage/go.sum
  git commit -m "feat(storage): query viewbuilder duckdb views"
  ```

---

## Task 9: View 物化构建器

**Files:**

- Create/Modify: `/Users/mooyang/Documents/go/src/github.com/mooyang-code/moox/modules/storage/internal/services/view/view_builder.go`
- Create/Modify: `/Users/mooyang/Documents/go/src/github.com/mooyang-code/moox/modules/storage/internal/services/view/view_builder_test.go`
- Modify: `/Users/mooyang/Documents/go/src/github.com/mooyang-code/moox/modules/storage/internal/services/access/service.go`

- [ ] **Step 9.1: 写构建器测试**

  测试流程：

  ```text
  准备 Space/DataSet/DataSetColumn/View/ViewColumn
  写入 Pebble facts
  Build(space_id, view_id)
  生成新的 resultName
  写入 DuckDB
  更新 View.active_result
  QueryView 能读到结果
  ```

- [ ] **Step 9.2: 实现 Build 起点时间**

  `query_window` 表示视图保留和回扫窗口。新建 View 表时，应按：

  ```text
  build_start_time = now - view.query_window
  ```

  `ViewColumn.online_time` 只表示列上线时间，用于解释列可见性；不改变整体回扫窗口。

- [ ] **Step 9.3: 实现宽表构建**

  构建规则：

  - 以 `primary_dataset_id` 的 `DataSetSubject` 为行域。
  - 从 Pebble 读取主 DataSet 事实行。
  - 附属 DataSet 只提供列，按 `subject_id`、`data_time`、`freq` 等 `grain_keys` 关联。
  - 附属 DataSet 缺失列填空。
  - 构建新结果表，成功后原子更新 `View.active_result`。

- [ ] **Step 9.4: 实现后台任务入口**

  当前阶段至少提供可调用的 Go 方法；若已有 worker 框架，则注册：

  ```text
  view.BuildView(space_id, view_id)
  view.RebuildPendingViews()
  ```

- [ ] **Step 9.5: 跑测试并提交**

  Run:

  ```bash
  go test ./modules/storage/internal/services/view ./modules/storage/internal/services/access -run 'Test.*ViewBuilder|TestQueryView' -count=1
  git add modules/storage/internal/services/view modules/storage/internal/services/access
  git commit -m "feat(storage): build views from pebble facts"
  ```

---

## Task 10: Parquet 冷备归档

**Files:**

- Create/Modify: `/Users/mooyang/Documents/go/src/github.com/mooyang-code/moox/modules/storage/internal/infra/device/parquet/archive.go`
- Create/Modify: `/Users/mooyang/Documents/go/src/github.com/mooyang-code/moox/modules/storage/internal/infra/device/parquet/archive_test.go`
- Create/Modify: `/Users/mooyang/Documents/go/src/github.com/mooyang-code/moox/modules/storage/internal/services/archive/service.go`
- Create/Modify: `/Users/mooyang/Documents/go/src/github.com/mooyang-code/moox/modules/storage/internal/services/archive/service_test.go`
- Modify: `/Users/mooyang/Documents/go/src/github.com/mooyang-code/moox/modules/storage/go.mod`

- [ ] **Step 10.1: 写 Parquet writer 测试**

  覆盖：

  ```text
  DataRow 被展开为 Parquet fact rows
  每个 ColumnValue 一条 long fact 记录
  文件可被 parquet-go 重新读取
  columns 列表包含写入字段
  ```

- [ ] **Step 10.2: 实现事实归档格式**

  Parquet long fact schema：

  ```text
  space_id
  dataset_id
  subject_id
  freq
  dimensions_json
  data_time
  row_id
  column_name
  value_type
  string_value
  int_value
  double_value
  bool_value
  time_value
  json_value
  bytes_value
  attributes_json
  ```

- [ ] **Step 10.3: 实现 archive.Service**

  归档路径只允许：

  ```text
  Pebble facts -> Parquet archive -> t_archive_files
  ```

  不允许从 DuckDB 宽表归档，避免不同宽表 schema 版本污染事实冷备。

- [ ] **Step 10.4: 跑测试并提交**

  Run:

  ```bash
  go test ./modules/storage/internal/infra/device/parquet ./modules/storage/internal/services/archive -count=1
  git add modules/storage/internal/infra/device/parquet modules/storage/internal/services/archive modules/storage/go.mod modules/storage/go.sum
  git commit -m "feat(storage): archive pebble facts to parquet"
  ```

---

## Task 11: 配置、启动与脚本

**Files:**

- Modify: `/Users/mooyang/Documents/go/src/github.com/mooyang-code/moox/modules/storage/internal/config/loader.go`
- Modify: `/Users/mooyang/Documents/go/src/github.com/mooyang-code/moox/modules/storage/internal/config/loader_test.go`
- Modify: `/Users/mooyang/Documents/go/src/github.com/mooyang-code/moox/modules/storage/cmd/moox-storage/main.go`
- Modify: `/Users/mooyang/Documents/go/src/github.com/mooyang-code/moox/modules/storage/config/trpc_go.yaml`
- Modify: `/Users/mooyang/Documents/go/src/github.com/mooyang-code/moox/scripts/build.sh`
- Modify: `/Users/mooyang/Documents/go/src/github.com/mooyang-code/moox/scripts/deploy.sh`
- Modify: `/Users/mooyang/Documents/go/src/github.com/mooyang-code/moox/scripts/storage-start.sh`
- Modify: `/Users/mooyang/Documents/go/src/github.com/mooyang-code/moox/scripts/storage-stop.sh`

- [ ] **Step 11.1: 写配置测试**

  配置必须能表达：

  ```yaml
  storage:
    root: ./var/storage
    metadata:
      path: ./var/storage/metadata/storage_metadata.db
    devices:
      pebble_path: ./var/storage/pebble
      duckdb_path: ./var/storage/duckdb/views.duckdb
      bleve_path: ./var/storage/bleve
      parquet_path: ./var/storage/archive
    eventbus:
      type: memory
      nats_url: ""
  ```

- [ ] **Step 11.2: main.go 接入显式配置**

  `moox-storage` 启动时：

  ```text
  读取配置
  初始化 SQLite metadata
  初始化本地 PrimaryStore 和设备目录
  注册 DataService/QueryService/MetadataService/PrimaryStoreService
  ```

- [ ] **Step 11.3: 脚本统一到 scripts**

  根目录 `scripts` 负责构建、发布、启动、停止。模块内旧脚本可保留为薄包装，但实际逻辑集中在根 `scripts`。

- [ ] **Step 11.4: 跑测试并提交**

  Run:

  ```bash
  go test ./modules/storage/internal/config ./modules/storage/cmd/moox-storage/... -count=1
  bash scripts/build.sh storage
  git add modules/storage/internal/config modules/storage/cmd/moox-storage modules/storage/config scripts
  git commit -m "feat(storage): wire storage configuration and scripts"
  ```

---

## Task 12: 本地端到端验收

**Files:**

- Create/Modify: `/Users/mooyang/Documents/go/src/github.com/mooyang-code/moox/modules/storage/internal/services/access/acceptance_test.go`
- Create/Modify: `/Users/mooyang/Documents/go/src/github.com/mooyang-code/moox/scripts/acceptance.sh`

- [ ] **Step 12.1: 写 Go 端到端测试**

  流程必须覆盖：

  ```text
  创建 Space
  创建 DataSource
  创建 Subject 和 SubjectSymbol
  创建 DataSet，绑定 Subject
  创建 Field 和 DataSetColumn
  创建 PrimaryNode、Device、PrimaryRoute
  WriteRows 写入 K 线
  ReadRows 读回 K 线
  SearchRows 搜索 text_indexed 列
  创建 View 和 ViewColumn
  ViewBuilder 构建 View
  QueryView 读回物化结果
  Archive 生成 Parquet 并登记 ArchiveFile
  ```

- [ ] **Step 12.2: 验收脚本支持本地 CSV 输入**

  `scripts/acceptance.sh` 支持参数：

  ```bash
  bash scripts/acceptance.sh \
    --storage-url http://127.0.0.1:8000 \
    --space crypto_acceptance \
    --csv /Users/mooyang/Downloads/APT-USDT.csv \
    --csv /Users/mooyang/Downloads/AR-USDT.csv \
    --output /Users/mooyang/Downloads/moox-storage-acceptance.json
  ```

  CSV 只作为输入源。脚本要把数据写入 storage，再从 storage 读回并输出到本地下载目录，供人工检查。

- [ ] **Step 12.3: 跑本地验收**

  Run:

  ```bash
  go test ./modules/storage/internal/services/access -run TestStorageAcceptance -count=1
  bash scripts/acceptance.sh \
    --local \
    --space crypto_acceptance \
    --csv /Users/mooyang/Downloads/APT-USDT.csv \
    --csv /Users/mooyang/Downloads/AR-USDT.csv \
    --output /Users/mooyang/Downloads/moox-storage-acceptance.json
  ```

  Expected:

  ```text
  PASS
  /Users/mooyang/Downloads/moox-storage-acceptance.json exists
  output contains APT-USDT and AR-USDT rows read from moox-storage
  ```

- [ ] **Step 12.4: 提交**

  Run:

  ```bash
  git add modules/storage/internal/services/access/acceptance_test.go scripts/acceptance.sh
  git commit -m "test(storage): add end-to-end acceptance flow"
  ```

---

## Task 13: 远端部署验收

**Files:**

- Modify: `/Users/mooyang/Documents/go/src/github.com/mooyang-code/moox/scripts/deploy.sh`
- Modify: `/Users/mooyang/Documents/go/src/github.com/mooyang-code/moox/scripts/storage-start.sh`
- Modify: `/Users/mooyang/Documents/go/src/github.com/mooyang-code/moox/scripts/storage-stop.sh`
- Check: `/Users/mooyang/Documents/go/src/github.com/mooyang-code/moox/modules/storage/DEPLOY.md`

- [ ] **Step 13.1: 停止旧 xdata-storage**

  Run:

  ```bash
  ssh ubuntu@43.132.204.177 'pkill -f xdata-storage || true'
  ssh ubuntu@43.132.204.177 'pgrep -af "xdata-storage|moox-storage" || true'
  ```

  Expected: 不再运行旧 `xdata-storage`。若已有 `moox-storage`，记录 PID 后重启。

- [ ] **Step 13.2: 发布到统一目录**

  远端路径：

  ```text
  ~/moox/storage
  ```

  目录结构：

  ```text
  ~/moox/storage/bin/moox-storage
  ~/moox/storage/config/trpc_go.yaml
  ~/moox/storage/data/metadata
  ~/moox/storage/data/pebble
  ~/moox/storage/data/duckdb
  ~/moox/storage/data/bleve
  ~/moox/storage/data/archive
  ~/moox/storage/logs/moox-storage.log
  ```

- [ ] **Step 13.3: 启动远端 moox-storage**

  Run:

  ```bash
  ssh ubuntu@43.132.204.177 'mkdir -p ~/moox/storage/logs'
  bash scripts/deploy.sh storage ubuntu@43.132.204.177:~/moox/storage
  ssh ubuntu@43.132.204.177 'cd ~/moox/storage && ./bin/moox-storage --config ./config/trpc_go.yaml > ./logs/moox-storage.log 2>&1 &'
  ssh ubuntu@43.132.204.177 'tail -n 80 ~/moox/storage/logs/moox-storage.log'
  ```

  Expected: 日志无 panic，服务监听端口可用。

- [ ] **Step 13.4: 用下载目录 CSV 做远端验收**

  Run:

  ```bash
  bash scripts/acceptance.sh \
    --storage-url http://43.132.204.177:8000 \
    --space crypto_acceptance \
    --csv /Users/mooyang/Downloads/APT-USDT.csv \
    --csv /Users/mooyang/Downloads/AR-USDT.csv \
    --output /Users/mooyang/Downloads/moox-storage-remote-acceptance.json
  ```

  Expected:

  ```text
  /Users/mooyang/Downloads/moox-storage-remote-acceptance.json exists
  output contains rows for APT-USDT and AR-USDT
  remote ~/moox/storage/logs/moox-storage.log has no write/query errors
  ```

- [ ] **Step 13.5: 提交部署脚本变更**

  Run:

  ```bash
  git add scripts/deploy.sh scripts/storage-start.sh scripts/storage-stop.sh modules/storage/DEPLOY.md
  git commit -m "chore(storage): deploy moox storage under unified path"
  ```

---

## Task 14: 全量回归、文档同步与最终 push

**Files:**

- Modify if needed: `/Users/mooyang/Documents/go/src/github.com/mooyang-code/moox/docs/storage-concepts-and-design-intent.md`
- Modify if needed: `/Users/mooyang/Documents/go/src/github.com/mooyang-code/moox/docs/storage-target-architecture-and-metadata.md`
- Modify if needed: `/Users/mooyang/Documents/go/src/github.com/mooyang-code/moox/docs/pb-protocol-redesign.md`
- Modify if needed: `/Users/mooyang/Documents/go/src/github.com/mooyang-code/moox/modules/storage/BUILD.md`
- Modify if needed: `/Users/mooyang/Documents/go/src/github.com/mooyang-code/moox/modules/storage/DEPLOY.md`

- [ ] **Step 14.1: 全量测试**

  Run:

  ```bash
  cd /Users/mooyang/Documents/go/src/github.com/mooyang-code/moox
  go work edit -go=1.24.0
  go test ./modules/storage/...
  go test ./modules/storage/proto/gen/...
  go test ./modules/cli/...
  go test ./modules/collector/...
  go test ./modules/control/...
  bash scripts/build.sh
  ```

  Expected: 所有命令 PASS。若 web 构建仍在项目约束内，补充：

  ```bash
  pnpm --dir web exec vue-tsc --noEmit
  ```

- [ ] **Step 14.2: 检查旧概念清理**

  Run:

  ```bash
  rg -n "RocksDB|StorageEntity|object_id|DataView|\\.jsonl|CSVImportOptions" \
    /Users/mooyang/Documents/go/src/github.com/mooyang-code/moox/modules/storage \
    /Users/mooyang/Documents/go/src/github.com/mooyang-code/moox/docs \
    /Users/mooyang/Documents/go/src/github.com/mooyang-code/moox/modules/storage/schema
  ```

  Expected: 无命中，或只命中文档中明确说明“已废弃”的历史说明。storage 实现代码不得命中。

- [ ] **Step 14.3: 文档同步**

  确保文档说明：

  - `Access` 是唯一公开数据访问入口。
  - `PrimaryNode` 是 PrimaryStore 节点；当前协议和表结构仍使用 StorageNode 旧名时，要注明目标语义。
  - `Device` 是底层具体存储组件。
  - `SearchRows` 查询 Search Service 的集中式索引，不走 PrimaryRoute。
  - `View` 不暴露底层物理表细节。
  - `QueryView` 只查询 `active_result`。
  - Parquet 冷备只从 Pebble 事实归档。
  - DataSet 只绑定一个 DataSource。

- [ ] **Step 14.4: 最终提交**

  Run:

  ```bash
  git status --short
  git add docs modules/storage schema scripts
  git commit -m "feat(storage): implement quant storage module"
  ```

  Expected: 若所有改动已在前面小提交完成，这一步可以没有新 commit。

- [ ] **Step 14.5: push**

  Run:

  ```bash
  git push
  git status --short
  ```

  Expected: push 成功，工作区干净或只剩用户明确保留的本地文件。

---

## Definition Of Done

- `go test ./modules/storage/...` PASS。
- `go test ./modules/storage/proto/gen/...` PASS。
- `go work` 保持 `go 1.24.0`。
- storage 源码不再依赖 JSONL/CSV 主存、RocksDB、旧 `StorageEntity`、旧 `DataView`、旧 `object_id`。
- SQLite 元数据覆盖所有当前表。
- 写入链路按 `DataSet` 校验并通过 `PrimaryRoute -> PrimaryStore -> Pebble` 写入。
- `ReadRows` 能按 DataSet 读回 Pebble facts。
- `SearchRows` 能按 DataSet 做 Bleve 全文和结构化搜索。
- `QueryView` 只读取已有 View 的物化结果；未构建返回 `VIEW_NOT_FOUND`。
- ViewBuilder 能从 Pebble 构建 DuckDB View 物化结果。
- Parquet 归档只从 Pebble facts 产生，并登记 `t_archive_files`。
- 本地下载目录的 `APT-USDT.csv`、`AR-USDT.csv` 可作为验收输入写入 storage，并能从 storage 读回到 `/Users/mooyang/Downloads/moox-storage-acceptance.json`。
- 远端 `43.132.204.177` 使用 `~/moox/storage` 运行新的 `moox-storage`，旧 `xdata-storage` 已停止。
- 远端日志位于 `~/moox/storage/logs/moox-storage.log`。
