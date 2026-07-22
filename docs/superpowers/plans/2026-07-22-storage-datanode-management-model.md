# Storage DataNode Management Model Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Replace the obsolete PrimaryStore node/route model with deployment-owned DataNodes, direct Dataset binding, revision-guarded activation/rebinding, a focused management UI, and a verified deployment to the configured 106 host.

**Architecture:** Metadata Schema v5 stores `Dataset.data_node_id -> DataNode.service_target` as the only routing path. Dataset creation is disabled and unlocked; a shared read-only activation checker validates signed DataNode readiness, while explicit activation performs a short revision-CAS transaction and permanently locks the binding. Runtime reads and writes resolve only from the in-memory Metadata Snapshot; deployment registers nodes and explicitly activates healthy datasets after Doctor reports healthy.

**Tech Stack:** Go 1.24, tRPC-Go, Protocol Buffers, SQLite, Pebble, Vue 3, TypeScript, Pinia, Arco Design, Vitest, Playwright, Bash, `moox-cli setup`, SSH deployment through ignored `custom.toml`.

---

## Delivery Rules

- Work only in `/Users/mooyang/Documents/go/src/github.com/mooyang-code/moox/.worktrees/storage-datanode-management` on branch `feature/storage-datanode-management`.
- Use test-driven development for every behavioral change: add one focused failing test, observe the intended failure, implement the smallest coherent change, and rerun the focused test.
- Regenerate all Proto outputs with `make proto`; never edit generated `*.pb.go`, `*_trpc.pb.go`, or `common_alias.go` files by hand.
- This is a clean break. Do not add aliases, redirects, migrations, dual reads, route fallbacks, or compatibility adapters for `PrimaryStoreNode`, `PrimaryStoreRoute`, Schema v4, or `/#/ops/storage/routes`.
- Never open, parse, print, source, or log `custom.toml`. The approved interaction is only through `./bin/moox-cli setup ... --file ./custom.toml`; `setup hosts` returns sanitized host metadata.
- Never put credentials, `AuthInfo`, SSH passwords, HMAC keys, cookies, or `custom.toml` contents in test artifacts, screenshots, logs, commits, or review messages.
- Every commit command below is run from the dedicated worktree. Before each commit, inspect `git diff --check` and `git status --short` and stage only the files named by that task.
- Completion requires two fresh independent review passes, exact-HEAD verification, browser and backend E2E, Linux amd64 release artifacts, SHA256 recording, deployment to `106.53.107.122`, and signed remote health verification.

## Final Contract

The implementation must converge on these public types; later tasks use these names exactly:

```proto
message DataNode {
  string node_id = 1;
  string name = 2;
  string service_target = 3;
  string status = 4;
  string created_at = 5;
  string updated_at = 6;
}

message DatasetSummary {
  string space_id = 1;
  string dataset_id = 2;
  string name = 3;
  DataKind data_kind = 4;
  string keep_duration = 5;
  string status = 6;
}

message DataNodeListItem {
  DataNode node = 1;
  repeated DatasetSummary datasets = 2;
}

message DatasetActivationCheck {
  string check_id = 1;
  bool ready = 2;
  string summary = 3;
}
```

`Dataset` retains fields 1-18 and adds:

```proto
bool binding_locked = 19;
uint64 revision = 20;
```

The Metadata RPC contract is:

```proto
rpc RegisterDataNode(RegisterDataNodeReq) returns (RegisterDataNodeRsp);
rpc UpdateDataNode(UpdateDataNodeReq) returns (UpdateDataNodeRsp);
rpc GetDataNode(GetDataNodeReq) returns (GetDataNodeRsp);
rpc ListDataNodes(ListDataNodesReq) returns (ListDataNodesRsp);
rpc DeleteDataNode(DeleteDataNodeReq) returns (DeleteDataNodeRsp);
rpc RebindDatasetDataNode(RebindDatasetDataNodeReq) returns (RebindDatasetDataNodeRsp);
rpc CheckDatasetActivation(CheckDatasetActivationReq) returns (CheckDatasetActivationRsp);
rpc ActivateDataset(ActivateDatasetReq) returns (ActivateDatasetRsp);
```

`RegisterDataNodeReq` contains `auth_info`, `node_id`, `service_target`, and `initial_name`. `UpdateDataNodeReq` contains `auth_info`, `node_id`, `name`, and `status`. Rebind and Activate contain `expected_revision`; ListDatasets adds `data_node_id`. `ListDataNodesRsp` contains `repeated DataNodeListItem items` and `page_result`, with no `dataset_count`.

Use these exact request fields and numbers:

```proto
message RegisterDataNodeReq { common.AuthInfo auth_info = 1; string node_id = 2; string service_target = 3; string initial_name = 4; }
message UpdateDataNodeReq { common.AuthInfo auth_info = 1; string node_id = 2; string name = 3; string status = 4; }
message GetDataNodeReq { common.AuthInfo auth_info = 1; string node_id = 2; }
message ListDataNodesReq { common.AuthInfo auth_info = 1; string status = 2; common.Page page = 3; }
message DeleteDataNodeReq { common.AuthInfo auth_info = 1; string node_id = 2; }
message RebindDatasetDataNodeReq { common.AuthInfo auth_info = 1; string space_id = 2; string dataset_id = 3; string data_node_id = 4; uint64 expected_revision = 5; }
message CheckDatasetActivationReq { common.AuthInfo auth_info = 1; string space_id = 2; string dataset_id = 3; }
message ActivateDatasetReq { common.AuthInfo auth_info = 1; string space_id = 2; string dataset_id = 3; uint64 expected_revision = 4; }
```

The shared common error enum removes route-specific values and assigns:

```proto
CONFLICT = 14;
ENGINE_CAPABILITY_UNSUPPORTED = 16;
DIMENSION_VALUE_INVALID = 17;
SUBJECT_NOT_IN_DATASET = 18;
```

`CONFLICT` represents stale Dataset revision or an incompatible current state. Constraint failures that the caller can correct use `INVALID_PARAM`; missing entities use `NOT_FOUND` or `DATASET_NOT_FOUND`; unexpected persistence/network failures use `INNER_ERR`.

## File Map

- `packages/commonpb/moox_common.proto`: remove route errors and define the generic conflict code.
- `modules/storage/proto/metadata.proto`: final DataNode, Dataset lifecycle, request/response, and service contract.
- `modules/storage/schema/metadata.sql`: Schema v5 tables and constraints.
- `modules/storage/internal/service/metadata/sqlite/crud_data_node.go`: DataNode registration, administration, list aggregation support, and constrained deletion.
- `modules/storage/internal/service/metadata/sqlite/crud_dataset.go`: Dataset create/update, monotonic revision, activation commit, and rebind CAS.
- `modules/storage/internal/service/catalog/activation.go`: shared, read-only readiness checker using signed `GetNodeState`.
- `modules/storage/internal/service/catalog/metadata_data_node.go`: DataNode RPC handlers.
- `modules/storage/internal/service/catalog/metadata_catalog.go`: Dataset lifecycle RPC handlers.
- `modules/storage/internal/service/metadata/cache/store.go`: final DataNode and Dataset snapshot model with no routes.
- `modules/storage/cmd/server/main.go`: snapshot-only direct resolver and target-aware DataNode proxy cache.
- `modules/storage/cmd/cli/main.go`: deployment-only registration and explicit activation orchestration.
- `modules/cli/internal/doctor/storage_activation.go`: read-only Doctor aggregation over activation checks.
- `modules/cli/internal/setup/deploy/deploy.go`: explicit storage-data reset and deploy readiness stages.
- `web/src/views/ops/storage/nodes.vue`: DataNode table, tags, tooltips, drawer, and admin operations.
- `web/src/views/data/datasets/index.vue`: DataNode selection, activation, revision display, and pre-activation rebind.
- `scripts/test-storage-datanode-management-contract.sh`: positive and zero-residual contract checks.
- `modules/storage/internal/service/e2e/datanode_management_test.go`: service and runtime lifecycle E2E.
- `web/tests/storage-datanode-management.e2e.spec.ts`: browser workflow E2E at desktop and mobile widths.

### Task 1: Lock the Proto contract and regenerate every consumer

**Files:**
- Modify: `packages/commonpb/moox_common.proto`
- Modify: `modules/storage/proto/metadata.proto`
- Create: `modules/storage/proto/metadata_proto_test.go`
- Generated: `packages/commonpb/*.pb.go`
- Generated: `modules/*/proto/*gen/common_alias.go`
- Generated: `modules/storage/proto/storagegen/*`

- [ ] **Step 1: Add failing descriptor tests for the clean-break contract**

Add table-driven descriptor assertions that require `DataNode`, `DataNodeListItem`, `DatasetSummary`, `Dataset.binding_locked=19`, `Dataset.revision=20`, the eight final RPCs, and `ListDatasetsReq.data_node_id`. Add negative assertions for every `PrimaryStoreNode`, `PrimaryStoreRoute`, route RPC, and `Device.node_id` symbol.

```go
func TestMetadataProtoDataNodeContract(t *testing.T) {
	file := pb.File_metadata_proto
	require.NotNil(t, file.Messages().ByName("DataNode"))
	require.NotNil(t, file.Messages().ByName("DataNodeListItem"))
	dataset := file.Messages().ByName("Dataset")
	require.Equal(t, protoreflect.FieldNumber(19), dataset.Fields().ByName("binding_locked").Number())
	require.Equal(t, protoreflect.FieldNumber(20), dataset.Fields().ByName("revision").Number())
	require.Nil(t, file.Messages().ByName("PrimaryStoreNode"))
	require.Nil(t, file.Messages().ByName("PrimaryStoreRoute"))
}
```

- [ ] **Step 2: Run the descriptor tests and observe the old contract**

Run: `go test ./modules/storage/proto -run 'TestMetadataProto(DataNode|CleanBreak)Contract' -count=1`

Expected: FAIL because `DataNode` and lifecycle fields/RPCs do not exist and old PrimaryStore messages still exist.

- [ ] **Step 3: Replace the Proto messages and RPCs**

Edit `metadata.proto` to match the Final Contract. Reserve removed field number `2` and name `node_id` in `Device`; remove all PrimaryStore node/route messages and RPCs. Use these lifecycle response shapes:

```proto
message CheckDatasetActivationRsp {
  common.RetInfo ret_info = 1;
  uint64 dataset_revision = 2;
  repeated DatasetActivationCheck checks = 3;
  bool ready = 4;
}

message ActivateDatasetRsp {
  common.RetInfo ret_info = 1;
  Dataset dataset = 2;
  repeated DatasetActivationCheck checks = 3;
}
```

Remove `ROUTE_NOT_FOUND` and `ROUTE_CROSS_DEVICE_UNSUPPORTED` from `moox_common.proto`; define `CONFLICT=14`. Keep unrelated numeric values stable.

- [ ] **Step 4: Regenerate and format generated contracts**

Run: `make proto`

Expected: PASS; generated Storage and common aliases contain `ErrorCode_CONFLICT` and no route-specific aliases.

- [ ] **Step 5: Run descriptor and generated-consumer compile tests**

Run: `go test ./modules/storage/proto ./packages/commonpb -count=1`

Expected: PASS for descriptor tests; consumer compilation may still fail where source code references removed symbols, which Task 4 removes.

- [ ] **Step 6: Commit the contract**

```bash
git add packages/commonpb modules/storage/proto modules/*/proto/*gen/common_alias.go
git commit -m "feat(storage): define DataNode metadata contract"
```

### Task 2: Replace Metadata Schema v4 with strict Schema v5

**Files:**
- Modify: `modules/storage/schema/metadata.sql`
- Modify: `modules/storage/schema/metadata_schema_version_test.go`
- Modify: `modules/storage/internal/service/metadata/sqlite/store.go`
- Modify: `modules/storage/internal/service/metadata/sqlite/store_test.go`

- [ ] **Step 1: Write failing Schema v5 contract tests**

Rename `TestMetadataSchemaV4Contract` to `TestMetadataSchemaV5Contract`. Require version `5`, `t_data_nodes`, Dataset foreign key/index/lock/revision constraints, and absence of `t_primary_store_nodes`, `t_primary_store_routes`, `t_dataset_topology_locks`, and `t_storage_devices.c_node_id`.

```go
require.Contains(t, schema, "INSERT INTO t_schema_version (c_version) VALUES (5)")
require.Contains(t, schema, "c_binding_locked INTEGER NOT NULL DEFAULT 0 CHECK (c_binding_locked IN (0, 1))")
require.Contains(t, schema, "c_revision INTEGER NOT NULL DEFAULT 1 CHECK (c_revision > 0)")
require.NotContains(t, schema, "t_primary_store_routes")
require.NotContains(t, schema, "t_dataset_topology_locks")
```

- [ ] **Step 2: Run the schema tests**

Run: `go test ./modules/storage/schema ./modules/storage/internal/service/metadata/sqlite -run 'Schema|Version' -count=1`

Expected: FAIL with version 4 and old table assertions.

- [ ] **Step 3: Write the final v5 DDL**

Define `t_data_nodes` before Dataset foreign-key use and change Dataset columns to:

```sql
c_data_node_id TEXT NOT NULL,
c_keep_duration TEXT NOT NULL,
c_binding_locked INTEGER NOT NULL DEFAULT 0 CHECK (c_binding_locked IN (0, 1)),
c_revision INTEGER NOT NULL DEFAULT 1 CHECK (c_revision > 0),
c_status TEXT NOT NULL DEFAULT 'disabled' CHECK (c_status IN ('active', 'disabled')),
FOREIGN KEY (c_data_node_id) REFERENCES t_data_nodes(c_node_id) ON DELETE RESTRICT
```

Create `idx_datasets_data_node_id`. Define DataNode status as `active|disabled`. Delete route/topology tables and the Device node foreign key/index. Change `metadataSchemaVersion` to `5`; keep strict equality so a v4 database fails with `metadata schema version mismatch` rather than migrating.

- [ ] **Step 4: Run schema creation and strict-version tests**

Run: `go test ./modules/storage/schema ./modules/storage/internal/service/metadata/sqlite -run 'Schema|Version' -count=1`

Expected: PASS, including a test proving an existing v4 DB is rejected.

- [ ] **Step 5: Commit Schema v5**

```bash
git add modules/storage/schema/metadata.sql modules/storage/schema/metadata_schema_version_test.go modules/storage/internal/service/metadata/sqlite/store.go modules/storage/internal/service/metadata/sqlite/store_test.go
git commit -m "feat(storage): replace metadata schema with v5"
```

### Task 3: Implement SQLite DataNode and Dataset lifecycle invariants

**Files:**
- Create: `modules/storage/internal/service/metadata/sqlite/crud_data_node.go`
- Create: `modules/storage/internal/service/metadata/sqlite/crud_data_node_test.go`
- Modify: `modules/storage/internal/service/metadata/sqlite/crud_dataset.go`
- Modify: `modules/storage/internal/service/metadata/sqlite/dataset_test.go`
- Modify: `modules/storage/internal/service/metadata/sqlite/crud_store.go`
- Delete: `modules/storage/internal/service/metadata/sqlite/topology.go`
- Modify: `modules/storage/internal/service/metadata/sqlite/crud_test.go`
- Create: `modules/storage/internal/service/metadata/sqlite/errors.go`

- [ ] **Step 1: Add typed invariant errors and failing DataNode tests**

Define exact sentinels used by catalog error mapping:

```go
var (
	ErrRevisionConflict = errors.New("dataset revision conflict")
	ErrBindingLocked = errors.New("dataset binding is locked")
	ErrDatasetMustBeDisabled = errors.New("dataset must be disabled")
	ErrDataNodeDisabled = errors.New("data node is disabled")
	ErrDataNodeReferenced = errors.New("data node still has datasets")
	ErrDataNodeMustBeDisabled = errors.New("data node must be disabled")
)
```

Test registration idempotency: the second registration updates only `service_target`, preserving admin-managed `name` and `status`. Test update accepts only `name/status`; deletion rejects active and referenced nodes; list returns deterministic `node_id` order with pagination.

- [ ] **Step 2: Run DataNode tests and observe missing methods**

Run: `go test ./modules/storage/internal/service/metadata/sqlite -run 'DataNode' -count=1`

Expected: FAIL because final DataNode CRUD does not exist.

- [ ] **Step 3: Implement transactional DataNode methods**

Implement these signatures with trimmed IDs/targets, strict status validation, and one transaction for delete preconditions plus delete:

```go
func (s *Store) RegisterDataNode(ctx context.Context, nodeID, serviceTarget, initialName string) (*pb.DataNode, error)
func (s *Store) UpdateDataNode(ctx context.Context, nodeID, name, status string) (*pb.DataNode, error)
func (s *Store) GetDataNode(ctx context.Context, nodeID string) (*pb.DataNode, error)
func (s *Store) ListDataNodes(ctx context.Context, page *pb.Page) ([]*pb.DataNode, *pb.PageResult, error)
func (s *Store) DeleteDataNode(ctx context.Context, nodeID string) error
```

Use `INSERT ... ON CONFLICT(c_node_id) DO UPDATE SET c_service_target=excluded.c_service_target,c_updated_at=excluded.c_updated_at`; do not overwrite name/status on conflict.

- [ ] **Step 4: Add failing Dataset state-machine tests**

Cover creation defaults, active-node requirement, generic update restrictions, revision increments, rebind CAS, first activation, idempotent active activation, locked-disabled reactivation, and no unlock transition. Every rejected mutation must preserve row contents and revision.

```go
created, err := store.CreateDataset(ctx, &pb.Dataset{DataNodeId: "node-a", KeepDuration: "4320h"})
require.NoError(t, err)
require.Equal(t, "disabled", created.Status)
require.False(t, created.BindingLocked)
require.Equal(t, uint64(1), created.Revision)
```

- [ ] **Step 5: Run Dataset lifecycle tests**

Run: `go test ./modules/storage/internal/service/metadata/sqlite -run 'Dataset(Create|Update|Rebind|Activate|Revision)' -count=1`

Expected: FAIL because current Upsert permits active creation and lacks CAS lifecycle methods.

- [ ] **Step 6: Implement Dataset mutation methods**

Replace positional list filters with:

```go
type DatasetQuery struct {
	SpaceID     string
	DataSourceID string
	DataNodeID  string
	DataNodeIDs []string
	Freq        string
	DataKind    pb.DataKind
	Page        *pb.Page
}
```

Creation ignores caller status/lock/revision and writes `disabled,false,1`; it requires an existing active DataNode. Generic update preserves `data_node_id` and `binding_locked`, permits `active -> disabled` or an unchanged status, rejects `disabled -> active`, and performs `c_revision=c_revision+1`. `DataNodeIDs` is an internal batch filter implemented with parameterized `IN` placeholders; public ListDatasets maps only its singular `data_node_id` into `DataNodeID`. Implement:

```go
func (s *Store) RebindDatasetDataNode(ctx context.Context, spaceID, datasetID, nodeID string, expectedRevision uint64) (*pb.Dataset, error)
func (s *Store) CommitDatasetActivation(ctx context.Context, spaceID, datasetID string, expectedRevision uint64) (*pb.Dataset, error)
```

Both methods use `BEGIN IMMEDIATE` semantics through the existing write transaction helper, reread state, compare `expectedRevision`, update once, increment revision, and return the committed row. Rebind requires `disabled && !binding_locked`, active target, and different node. Activation requires revision equality for either disabled state, accepts `disabled/unlocked` and `disabled/locked`, and rejects `active/unlocked`. `active/locked` returns the current row without comparing a stale request revision or incrementing revision, so retry after a lost activation response is idempotent.

- [ ] **Step 7: Remove topology and Device-node persistence**

Delete topology methods/tests. Remove PrimaryStore route/node sections from `crud_store.go`; retain ArchiveFile and Device CRUD but remove Device `node_id` scanning, filtering, and writes.

- [ ] **Step 8: Run SQLite tests**

Run: `go test ./modules/storage/internal/service/metadata/sqlite -count=1`

Expected: PASS with race-free deterministic state-machine tests.

- [ ] **Step 9: Commit persistence invariants**

```bash
git add modules/storage/internal/service/metadata/sqlite
git commit -m "feat(storage): enforce DataNode binding lifecycle"
```

### Task 4: Replace metadata interfaces, cache entries, and catalog DataNode RPCs

**Files:**
- Modify: `modules/storage/internal/service/metadata/store.go`
- Modify: `modules/storage/internal/service/metadata/cache/store.go`
- Modify: `modules/storage/internal/service/metadata/cache/store_test.go`
- Modify: `modules/storage/internal/service/catalog/service.go`
- Create: `modules/storage/internal/service/catalog/metadata_data_node.go`
- Create: `modules/storage/internal/service/catalog/metadata_data_node_test.go`
- Modify: `modules/storage/internal/service/catalog/metadata_catalog.go`
- Modify: `modules/storage/internal/service/catalog/metadata_infra.go`
- Modify: `modules/storage/internal/retinfo/metadata.go`
- Modify: `modules/storage/internal/retinfo/metadata_test.go`

- [ ] **Step 1: Write failing interface and error-mapping tests**

Require `RequestSnapshot.GetDataNode`, no route lookup, `ListDatasets(DatasetQuery)`, and stable mappings: revision conflict to `CONFLICT`; invariant failures to `INVALID_PARAM`; missing rows to `NOT_FOUND`/`DATASET_NOT_FOUND`.

- [ ] **Step 2: Run focused tests**

Run: `go test ./modules/storage/internal/service/metadata/... ./modules/storage/internal/retinfo -run 'DataNode|DatasetQuery|MetadataStoreCode' -count=1`

Expected: FAIL because interfaces and cache still expose PrimaryStore symbols.

- [ ] **Step 3: Replace Reader, Writer, and RequestSnapshot contracts**

Use final names only:

```go
type RequestSnapshot interface {
	GetDataset(spaceID, datasetID string) (*pb.Dataset, bool)
	GetDataNode(nodeID string) (*pb.DataNode, bool)
}
```

Add final DataNode and lifecycle methods to Reader/Writer. Delete route and topology methods. Change every ListDatasets implementation/call to `DatasetQuery`.

- [ ] **Step 4: Rewrite cache kinds and snapshot indexes**

Replace `kindPrimaryStoreNode` with `kindDataNode`; delete route kinds, route fetches, route indexes, and aliases. Preserve Dataset payloads including `binding_locked` and `revision`. Apply mutations atomically so a successful rebind/activation is visible on the next snapshot generation.

- [ ] **Step 5: Add failing ListDataNodes aggregation tests**

Seed two nodes and datasets across spaces. Assert one node query plus one Dataset query, all summaries attached, empty nodes contain `datasets: []`, no N+1 calls, and no count field.

- [ ] **Step 6: Implement final catalog DataNode handlers**

`RegisterDataNode` verifies service HMAC and calls only `RegisterDataNode`. Admin `UpdateDataNode` changes name/status only. `ListDataNodes` fetches the requested node page, performs one unpaginated Dataset query using `DatasetQuery.DataNodeIDs` for the returned node IDs, groups by `data_node_id`, and builds `DataNodeListItem` values. `DeleteDataNode` relies on transactional Store enforcement.

- [ ] **Step 7: Remove old catalog handlers and compile all Storage services**

Retain DataSource, Device, and ArchiveFile handlers in `metadata_infra.go`, but remove every PrimaryStore node/route handler and topology lock call.

Run: `go test ./modules/storage/internal/service/metadata/... ./modules/storage/internal/service/catalog ./modules/storage/internal/retinfo -count=1`

Expected: PASS.

- [ ] **Step 8: Commit metadata/cache convergence**

```bash
git add modules/storage/internal/service/metadata modules/storage/internal/service/catalog modules/storage/internal/retinfo
git commit -m "feat(storage): expose DataNode metadata services"
```

### Task 5: Build the shared read-only activation checker and explicit activation RPC

**Files:**
- Create: `modules/storage/internal/service/catalog/activation.go`
- Create: `modules/storage/internal/service/catalog/activation_test.go`
- Modify: `modules/storage/internal/service/catalog/service.go`
- Modify: `modules/storage/internal/service/catalog/metadata_catalog.go`
- Create: `modules/storage/internal/service/catalog/metadata_catalog_test.go`
- Modify: `modules/storage/internal/service/datanode/service_test.go`

- [ ] **Step 1: Write failing checker tests with a fake signed DataNode client**

Define an injected boundary:

```go
type NodeStateChecker interface {
	GetNodeState(context.Context, string, *pb.GetNodeStateReq) (*pb.GetNodeStateRsp, error)
}
```

Test ordered bounded checks with IDs `dataset_state`, `dataset_schema`, `keep_duration`, `data_node`, `service_target`, `data_node_readiness`, and `data_node_identity`. Assert `CheckDatasetActivation` never invokes a metadata writer. Cover timeout, malformed `ip://` target, disabled/missing node, non-READY response, signed response error, and returned node ID mismatch.

- [ ] **Step 2: Run checker tests**

Run: `go test ./modules/storage/internal/service/catalog -run 'DatasetActivation' -count=1`

Expected: FAIL because no checker exists.

- [ ] **Step 3: Implement the checker outside database transactions**

Create a checker with a default 3-second timeout and a maximum of seven results. Reuse existing Dataset schema/keep-duration validators. The checker stores the injected node-auth secret and builds the request exactly as follows before calling `service_target`:

```go
const appID = "storage-metadata"
req := &pb.GetNodeStateReq{
	NodeId: node.GetNodeId(),
	AuthInfo: &pb.AuthInfo{AppId: appID, AppKey: datanode.ServiceAuthKey(c.authSecret, appID)},
}
```

Redact transport details: summaries may contain check IDs, node IDs, and safe reason text, never secrets or full auth payloads.

- [ ] **Step 4: Add failing ActivateDataset CAS and idempotency tests**

Assert Activate runs the same checker itself, then calls `CommitDatasetActivation` with the request revision. Simulate a metadata mutation between network check and commit and require `CONFLICT` with no state change. Assert already `active+locked` returns success without changing revision, while `disabled+locked` rechecks readiness and increments revision when re-enabled.

- [ ] **Step 5: Implement Check and Activate handlers**

`CheckDatasetActivation` returns the observed Dataset revision and checks without writes. `ActivateDataset` reruns all checks and returns failed checks without mutation. For disabled datasets it rejects a request revision different from the observed revision and commits through the CAS Store method only when all checks are ready. For `active+locked`, it returns idempotent success even when the retry carries the pre-activation revision.

- [ ] **Step 6: Verify DataNode identity behavior**

Extend DataNode service tests to prove `GetNodeState` requires the correct signed service identity, rejects mismatched requested node ID, and returns exactly the configured node ID with status `READY`.

Run: `go test ./modules/storage/internal/service/catalog ./modules/storage/internal/service/datanode -run 'Activation|GetNodeState' -count=1`

Expected: PASS.

- [ ] **Step 7: Commit activation domain behavior**

```bash
git add modules/storage/internal/service/catalog modules/storage/internal/service/datanode/service_test.go
git commit -m "feat(storage): add revision guarded Dataset activation"
```

### Task 6: Make read/write routing snapshot-only and target-aware

**Files:**
- Modify: `modules/storage/cmd/server/main.go`
- Modify: `modules/storage/cmd/server/main_test.go`
- Modify: `modules/storage/internal/service/primarystore/metadata_validator.go`
- Modify: `modules/storage/internal/service/primarystore/metadata_validator_test.go`
- Modify: `modules/storage/internal/service/primarystore/service_test.go`

- [ ] **Step 1: Write failing resolver tests**

Test the exact resolution chain `active Dataset -> data_node_id -> active DataNode -> service_target`. Reject disabled Dataset, disabled/missing DataNode, empty/malformed target, and unknown Dataset. Assert no endpoint or attributes fallback. Test target refresh: changing `service_target` for the same node ID creates/replaces the proxy rather than reusing a stale proxy.

- [ ] **Step 2: Run resolver tests**

Run: `go test ./modules/storage/cmd/server ./modules/storage/internal/service/primarystore -run 'Resolver|Metadata|Proxy' -count=1`

Expected: FAIL because current resolver reads PrimaryStore attributes/endpoint and proxy cache keys only by node ID.

- [ ] **Step 3: Implement direct snapshot resolution**

Use this immutable cache key:

```go
type dataNodeProxyKey struct {
	NodeID        string
	ServiceTarget string
}
```

Resolve from one acquired snapshot and release it after both objects are read. Do not call SQLite or `LockDatasetBinding` anywhere in request handling. Remove route lookup, endpoint fallback, `legacyColumns`, and PrimaryStore naming from current source.

- [ ] **Step 4: Prove hot-path cache-only behavior**

Add a panic-on-access metadata Store fake behind an already-built Snapshot; successful read/write must not invoke it. Assert failed activation cannot make the Dataset writable and successful snapshot refresh can.

Run: `go test ./modules/storage/cmd/server ./modules/storage/internal/service/primarystore -count=1`

Expected: PASS.

- [ ] **Step 5: Commit direct runtime routing**

```bash
git add modules/storage/cmd/server modules/storage/internal/service/primarystore
git commit -m "refactor(storage): route Dataset requests directly to DataNode"
```

### Task 7: Register DataNodes from deployment and activate only after healthy self-check

**Files:**
- Modify: `modules/storage/cmd/cli/main.go`
- Create: `modules/storage/cmd/cli/main_test.go`
- Modify: `scripts/deploy-moox.sh`
- Modify: `scripts/test-deploy-moox-storage-profile.sh`
- Modify: `scripts/test-deploy-moox-storage-view.sh`
- Modify: `examples/service-deployments.seed.yaml`

- [ ] **Step 1: Write failing Storage CLI command tests**

Add `register-node` and `activate-datasets` command tests with fake Metadata clients. Registration must send `node_id`, `ip://127.0.0.1:20107`, initial name, and service HMAC. Activation lists disabled datasets, calls Check, skips failed checks, then calls Activate with the returned revision. Output is sanitized JSON containing Dataset IDs, check summaries, revisions, and status only.

- [ ] **Step 2: Run CLI tests**

Run: `go test ./modules/storage/cmd/cli -run 'RegisterNode|ActivateDatasets' -count=1`

Expected: FAIL because the commands do not exist.

- [ ] **Step 3: Implement deployment-only commands**

Add exact commands:

```text
moox-storage-cli register-node --metadata-target ip://127.0.0.1:20100 --node-id storage-node-0 --service-target ip://127.0.0.1:20107 --name 数据节点
moox-storage-cli activate-datasets --metadata-target ip://127.0.0.1:20100
```

Use app ID `storage-deployer` and `datanode.ServiceAuthKey` from `MOOX_STORAGE_NODE_AUTH_SECRET`. Exit nonzero when registration fails or any selected Dataset is not ready/conflicts. A run with zero disabled datasets succeeds.

- [ ] **Step 4: Add failing deploy ordering assertions**

Update shell contract tests to require this sequence: start DataNode, wait signed readiness, register DataNode, import/seed Metadata, run read-only Doctor bootstrap, require HEALTHY, explicitly activate datasets, then expose workload readiness. Assert ordinary `doctor bootstrap|diagnose` never invokes `activate-datasets`.

- [ ] **Step 5: Implement deployment orchestration**

In the storage profile, export the same generated node auth secret to primary, Metadata CLI, and node. Invoke `register-node` only after DataNode `GetNodeState` is ready. Invoke `activate-datasets` only in the explicit initialization path after `doctor bootstrap --format json` reports HEALTHY. Remove old route seed environment variables and route import branches.

- [ ] **Step 6: Restrict public gateway exposure**

Remove old PrimaryStore and route methods from `examples/service-deployments.seed.yaml`. Expose admin DataNode get/list/update/delete and Dataset check/activate/rebind methods, but omit `RegisterDataNode` so browser Admin cannot call it.

- [ ] **Step 7: Run deployment contract tests**

Run: `bash scripts/test-deploy-moox-storage-profile.sh && bash scripts/test-deploy-moox-storage-view.sh && go test ./modules/storage/cmd/cli -count=1`

Expected: PASS.

- [ ] **Step 8: Commit deployment registration and activation**

```bash
git add modules/storage/cmd/cli scripts/deploy-moox.sh scripts/test-deploy-moox-storage-profile.sh scripts/test-deploy-moox-storage-view.sh examples/service-deployments.seed.yaml
git commit -m "feat(storage): register and activate DataNodes during deployment"
```

### Task 8: Add read-only Doctor activation observations

**Files:**
- Create: `modules/cli/internal/doctor/storage_activation.go`
- Create: `modules/cli/internal/doctor/storage_activation_test.go`
- Modify: `modules/cli/internal/doctor/bootstrap.go`
- Modify: `modules/cli/internal/doctor/bootstrap_test.go`
- Modify: `modules/cli/internal/command/doctor.go`
- Modify: `modules/cli/internal/command/doctor_test.go`
- Modify: `scripts/test-doctor-e2e.sh`
- Modify: `docs/superpowers/specs/2026-07-22-storage-datanode-management-model-design.md`
- Modify: `docs/运维/MooX-Doctor运维.md`

- [ ] **Step 1: Clarify the approved Doctor boundary in the design**

State that fixed check `bootstrap.storage_dataset_activation` becomes active, while broader Storage functional-observability checks remain deferred. Doctor remains read-only; deployment/init owns the explicit mutation.

- [ ] **Step 2: Write failing read-only Doctor tests**

Inject:

```go
type StorageActivationClient interface {
	ListDatasets(context.Context, *pb.ListDatasetsReq) (*pb.ListDatasetsRsp, error)
	CheckDatasetActivation(context.Context, *pb.CheckDatasetActivationReq) (*pb.CheckDatasetActivationRsp, error)
}
```

Assert one fixed Check ID, bounded timeout, at most 16 observations plus an omitted-count summary, stable ordering by Space/Dataset, digest redaction, and zero calls to Activate. HEALTHY requires all disabled datasets ready; DEGRADED reports failed checks; unreachable Metadata reports UNKNOWN rather than mutating state.

- [ ] **Step 3: Run Doctor tests**

Run: `go test ./modules/cli/internal/doctor ./modules/cli/internal/command -run 'StorageDatasetActivation|DoctorBootstrap' -count=1`

Expected: FAIL because no Storage activation check is wired.

- [ ] **Step 4: Implement and wire the fixed check**

Add `bootstrap.storage_dataset_activation` to bootstrap specs. Build a signed Metadata client from existing CLI endpoint/auth configuration. The Runner lists disabled datasets and calls only Check. Keep diagnosis and recovery actions read-only; do not register an automatic fix action.

- [ ] **Step 5: Extend Doctor E2E**

Capture Dataset status/revision before and after `doctor bootstrap` and `doctor diagnose`; assert byte-for-byte equality. Assert the report includes activation observations. Then invoke the separate deployment activation command and assert status/revision change only there.

Run: `bash scripts/test-doctor-e2e.sh`

Expected: PASS.

- [ ] **Step 6: Commit Doctor integration**

```bash
git add modules/cli/internal/doctor modules/cli/internal/command/doctor.go modules/cli/internal/command/doctor_test.go scripts/test-doctor-e2e.sh docs/superpowers/specs/2026-07-22-storage-datanode-management-model-design.md docs/运维/MooX-Doctor运维.md
git commit -m "feat(doctor): report Dataset activation readiness"
```

### Task 9: Update Admin, Factor, and Monitor consumers of the removed route contract

**Files:**
- Modify: `modules/admin/internal/gateway/storage_bff.go`
- Modify: `modules/admin/internal/gateway/storage_bff_test.go`
- Modify: `modules/admin/internal/service/sysdeploy/defaults.go`
- Modify: `modules/admin/internal/service/sysdeploy/defaults_test.go`
- Modify: `modules/admin/internal/service/sysdeploy/service.go`
- Modify: `modules/admin/internal/service/sysdeploy/service_test.go`
- Delete: `modules/admin/internal/service/sysdeploy/storage_topology_checker.go`
- Delete: `modules/admin/internal/service/sysdeploy/storage_topology_checker_test.go`
- Modify: `modules/factor/cmd/cli/run_once.go`
- Modify: `modules/factor/cmd/cli/run_once_test.go`
- Modify: `modules/factor/internal/bootstrap/bootstrap.go`
- Modify: `modules/factor/internal/bootstrap/bootstrap_test.go`
- Modify: `modules/factor/internal/registry/metadata_sync.go`
- Modify: `modules/factor/internal/registry/service_test.go`
- Modify: `modules/monitor/internal/hostmetrics/storage_gate.go`
- Modify: `modules/monitor/internal/hostmetrics/storage_gate_test.go`
- Modify: `modules/monitor/internal/metrics/storage.go`
- Modify: `modules/monitor/internal/metrics/storage_test.go`

- [ ] **Step 1: Write failing Admin gateway allowlist tests**

Require DataNode get/list/update/delete plus Dataset check/activate/rebind to route to `storage-primary`. Require `RegisterDataNode` and every removed PrimaryStore node/route method to be absent from browser and public service allowlists.

- [ ] **Step 2: Run Admin focused tests**

Run: `go test ./modules/admin/internal/gateway ./modules/admin/internal/service/sysdeploy -run 'Storage.*Methods|StorageBFF' -count=1`

Expected: FAIL because defaults still list old node/route methods.

- [ ] **Step 3: Replace Admin mappings and remove obsolete topology warnings**

Update both the BFF map and SysDeploy JSON constant from one shared expected method list in tests. Keep deployment-only registration off the browser surface. Delete `storageTopologyWarnings` and its files; deployment now refreshes `service_target` automatically, so CRUD responses must not warn users to synchronize a removed Endpoint/topology model.

- [ ] **Step 4: Write failing Factor metadata-sync tests**

Remove route-copy expectations. Require target Dataset creation to copy `data_node_id` and `keep_duration` from the source Dataset, remain disabled while columns/subjects are synchronized, then call Check and Activate with the returned revision. A failed readiness check leaves the Dataset disabled and returns an actionable error; retry of an already active/locked target is success.

- [ ] **Step 5: Implement route-free Factor bootstrap**

Delete `primaryStoreRouteLister`, `primaryStoreRouteCreator`, `copyPrimaryStoreRoutes`, `createTargetRoute`, `targetRouteID`, and adapter methods. Extend the Factor Metadata client boundary with `CheckDatasetActivation` and `ActivateDataset`. Activate only after Dataset, columns, and subject bindings are complete.

- [ ] **Step 6: Write failing Monitor storage-gate tests**

Replace wildcard-route fixtures with Dataset/DataNode fixtures. Require each monitored Dataset to be active and locked, have a nonempty `data_node_id`, and resolve to an active DataNode. Keep column/schema validation unchanged. Missing/disabled DataNode must fail readiness; no route RPC may be called.

- [ ] **Step 7: Implement route-free Monitor validation**

Replace `ListPrimaryStoreRoutes` in both host metrics and service metrics boundaries with `GetDataNode`. Validate the node referenced by each Dataset and rely on activation lock instead of wildcard route shape.

- [ ] **Step 8: Run all consumer tests**

```bash
go test ./modules/admin/internal/gateway ./modules/admin/internal/service/sysdeploy -count=1
go test ./modules/factor/... -count=1
go test ./modules/monitor/... -count=1
```

Expected: PASS with no removed Proto symbol in these modules.

- [ ] **Step 9: Commit consumer convergence**

```bash
git add modules/admin/internal/gateway modules/admin/internal/service/sysdeploy modules/factor modules/monitor
git commit -m "refactor(storage): remove route assumptions from consumers"
```

### Task 10: Remove route/node seeds and update metadata import tooling

**Files:**
- Modify: `modules/storage/internal/bootstrap/metadata/seed.go`
- Create: `modules/storage/internal/bootstrap/metadata/seed_test.go`
- Modify: `modules/cli/internal/command/metadata_types.go`
- Modify: `modules/cli/internal/command/metadata_implementation.go`
- Modify: `modules/cli/internal/command/metadata_spaces.go`
- Modify: `modules/cli/internal/command/metadata_test.go`
- Modify: `modules/cli/internal/command/metadata_quant_seed_test.go`
- Modify: `modules/storage/config/metadata.seed.yaml`
- Modify: `examples/metadata-monitor-metrics.seed.yaml`
- Delete: `examples/metadata-monitor-metrics-local-route.seed.yaml`
- Modify: `examples/metadata-monitor-host.seed.yaml`
- Delete: `examples/metadata-monitor-host-local-route.seed.yaml`
- Modify: `examples/metadata-quant-initial.seed.yaml`
- Modify: `examples/platform-local.seed.yaml`
- Modify: `scripts/release.sh`
- Modify: `scripts/test-deploy-moox-eventbus.sh`

- [ ] **Step 1: Write failing seed/import clean-break tests**

Require seed structs and summaries to have no PrimaryStore nodes/routes or Device node ID. Require every Dataset to contain nonempty `data_node_id` and `keep_duration`, and seed import to force `disabled` regardless of an input status. Registration is deployment-owned and is not part of metadata seed import.

- [ ] **Step 2: Run seed and generic CLI tests**

Run: `go test ./modules/storage/internal/bootstrap/metadata ./modules/cli/internal/command -run 'Seed|MetadataApply|MetadataImport' -count=1`

Expected: FAIL because old node/route sections and active Dataset seeds remain.

- [ ] **Step 3: Simplify seed/import types and summaries**

Delete `PrimaryStoreNodes`, `PrimaryStoreRoutes`, route apply calls, topology locks, and Device-node linkage. Update Space selection to copy only final logical resources and direct Dataset fields. Reject a Dataset missing final binding/retention fields before any RPC. Import Datasets as disabled and let deployment activation own state transition.

- [ ] **Step 4: Rewrite all current seed fixtures**

Remove global PrimaryStore node/route sections and obsolete Pebble/DuckDB/Bleve Device rows. Set every Dataset status to `disabled`. Keep direct `data_node_id: storage-node-0` and valid `keep_duration`. Delete local-route overlay files completely.

- [ ] **Step 5: Update release and EventBus checks**

Remove local-route validation and route environment assumptions. Replace them with assertions that monitor Datasets bind directly to `storage-node-0` and begin disabled.

- [ ] **Step 6: Run seed dry-runs and release contracts**

```bash
go test ./modules/storage/internal/bootstrap/metadata ./modules/cli/internal/command -count=1
go run ./modules/cli/cmd/moox-cli metadata apply --file ./examples/metadata-quant-initial.seed.yaml --dry-run
bash scripts/test-deploy-moox-eventbus.sh
```

Expected: PASS; dry-run output contains no route/node seed counts and no active Dataset creation.

- [ ] **Step 7: Commit seed cleanup**

```bash
git add modules/storage/internal/bootstrap/metadata modules/cli/internal/command/metadata_types.go modules/cli/internal/command/metadata_implementation.go modules/cli/internal/command/metadata_spaces.go modules/cli/internal/command/metadata_test.go modules/cli/internal/command/metadata_quant_seed_test.go modules/storage/config examples scripts/release.sh scripts/test-deploy-moox-eventbus.sh
git commit -m "refactor(storage): remove route based metadata seeds"
```

### Task 11: Implement frontend API types and DataNode management page

**Files:**
- Modify: `web/src/api/storage/types.ts`
- Modify: `web/src/api/storage/metadata.ts`
- Create: `web/src/api/storage/metadata.test.ts`
- Modify: `web/src/views/ops/storage/index.vue`
- Modify: `web/src/views/ops/storage/nodes.vue`
- Delete: `web/src/views/ops/storage/routes.vue`
- Modify: `web/src/router/route.ts`
- Modify: `web/src/views/ops/storage/storage-management.test.ts`
- Modify: `web/src/views/data/overview/overview.vue`
- Modify: `web/tests/page-layout-standard-contract.test.ts`
- Modify: `web/tests/storage-page-tab-actions-contract.test.ts`

- [ ] **Step 1: Write failing API and tab tests**

Define TypeScript `DataNode`, `DatasetSummary`, `DataNodeListItem`, and activation types matching Proto JSON names. Test API method names and payloads. Test that `tab=nodes` and unknown values render nodes, no routes tab/page/menu exists, and `/ops/storage/routes` is absent rather than redirected.

- [ ] **Step 2: Run focused Vitest tests**

Run: `CI=true pnpm --dir web exec vitest run src/api/storage/metadata.test.ts src/views/ops/storage/storage-management.test.ts tests/storage-page-tab-actions-contract.test.ts`

Expected: FAIL because PrimaryStore types and the route tab still exist.

- [ ] **Step 3: Replace frontend API contracts**

Add Dataset `data_node_id`, `keep_duration`, `binding_locked`, and `revision`. Implement `registerDataNode` only in no browser module: the browser API exports get/list/update/delete DataNode and check/activate/rebind Dataset. Add `data_node_id` to list filters. Remove all PrimaryStore route APIs and types.

Remove the route count/request from Data Overview rather than replacing it with a redundant binding count. Update the page-layout contract’s expected component list after deleting `routes.vue`.

- [ ] **Step 4: Write failing DataNode component tests**

Mount the page with two list items. Assert no create button; node ID/target are read-only; name/status operations send only owned fields; all Dataset names render as wrapping clickable Arco Tags; Tooltip text contains Space and Dataset ID; empty nodes show `-`; detail drawer uses list payload without another listDatasets request; delete is disabled or explained until node is disabled and empty.

- [ ] **Step 5: Implement the DataNode page**

Use Lucide/Arco icons already installed for view, edit, enable/disable, delete, and info actions. Every unfamiliar icon button gets a Tooltip and `aria-label`. Put a focusable Info icon beside the title with exactly these concepts: deployment-owned identity/target, direct Dataset binding with no route layer, immutable binding after first activation, and disabled-empty delete rule. Limit Dataset column width, use `display:flex;flex-wrap:wrap;gap:4px`, fix operation-column dimensions, and keep cards out of the page layout.

- [ ] **Step 6: Implement Dataset-tag deep links**

On tag click, call `router.push({path:"/data/datasets",query:{space_id:summary.space_id,dataset_id:summary.dataset_id}})`. The Dataset page task consumes both values; do not invent a route compatibility alias.

- [ ] **Step 7: Run component tests and type checking**

```bash
CI=true pnpm --dir web exec vitest run src/api/storage/metadata.test.ts src/views/ops/storage/storage-management.test.ts tests/storage-page-tab-actions-contract.test.ts
pnpm --dir web run typecheck
```

Expected: PASS.

- [ ] **Step 8: Commit DataNode UI**

```bash
git add web/src/api/storage web/src/views/ops/storage web/src/views/data/overview/overview.vue web/src/router/route.ts web/tests/page-layout-standard-contract.test.ts web/tests/storage-page-tab-actions-contract.test.ts
git commit -m "feat(web): replace storage routes with DataNode management"
```

### Task 12: Implement Dataset creation, activation, and rebind UI

**Files:**
- Modify: `web/src/views/data/datasets/index.vue`
- Create: `web/src/views/data/datasets/dataset-lifecycle.test.ts`
- Modify: `web/src/store/modules/space.ts`
- Modify: `web/src/store/space-store-contract.ts`

- [ ] **Step 1: Write failing Dataset lifecycle component tests**

Cover: active DataNode selector and keep-duration required on create; payload forced disabled; DataNode and lock/revision read-only on edit; Check modal before Activate; failed check prevents Activate; successful Activate uses returned revision; rebind visible only for `disabled && !binding_locked`; rebind sends selected node plus expected revision; locked datasets explain immutability; query deep link selects its Space and opens the matching Dataset management drawer.

- [ ] **Step 2: Run lifecycle tests**

Run: `CI=true pnpm --dir web exec vitest run src/views/data/datasets/dataset-lifecycle.test.ts`

Expected: FAIL because lifecycle controls do not exist.

- [ ] **Step 3: Implement active DataNode selection and safe create/edit**

Load all DataNode pages, flatten `item.node`, and retain active nodes. Create requires node plus retention and sends `status:"disabled"`. Generic edit omits `data_node_id`, `binding_locked`, `revision`, and active status. Display current node, revision, and binding status as read-only descriptions.

- [ ] **Step 4: Implement explicit activation and rebind dialogs**

Activation always calls Check, renders every check with ready/fail status, and enables confirmation only when `ready=true`. Confirmation calls Activate with `dataset_revision`. Rebind excludes the current node and calls Rebind with the row revision. Backend errors remain authoritative and are shown verbatim after safe normalization.

- [ ] **Step 5: Implement cross-Space deep-link focus**

Use existing `spaceStore.setSelectedSpace(route.query.space_id)` only after verifying the Space exists in `spaceStore.spaces`. After rows load, locate `dataset_id`, set it active, and open the existing management drawer. Remove consumed query parameters with `router.replace` so refresh does not reopen unexpectedly.

- [ ] **Step 6: Add the Dataset title Info tooltip**

Use a focusable Info icon beside the title. Explain mandatory DataNode binding, permanent lock after first activation, rebind only before activation, and no data migration. Verify Tooltip does not overlap title/actions at 390px.

- [ ] **Step 7: Run tests, typecheck, and production build**

```bash
CI=true pnpm --dir web exec vitest run src/views/data/datasets/dataset-lifecycle.test.ts src/store/space-store-contract.ts
pnpm --dir web run typecheck
pnpm --dir web run build:prod
```

Expected: PASS; production bundle has no route-page chunk.

- [ ] **Step 8: Commit Dataset lifecycle UI**

```bash
git add web/src/views/data/datasets web/src/store/modules/space.ts web/src/store/space-store-contract.ts
git commit -m "feat(web): manage Dataset activation and DataNode binding"
```

### Task 13: Add zero-residual contracts and update current documentation

**Files:**
- Create: `scripts/test-storage-datanode-management-contract.sh`
- Modify: `Makefile`
- Modify: `README.md`
- Modify: `modules/storage/README.md`
- Modify: `skills/moox/references/protocol.md`
- Modify: `examples/e2e/README.md`
- Modify: `docs/主机监控架构设计.md`
- Modify: `docs/云节点执行平台架构.md`
- Modify: `docs/内置市场行情采集架构.md`
- Modify: `docs/协议设计.md`
- Modify: `docs/因子计算模块设计.md`
- Modify: `docs/存储层架构.md`
- Modify: `docs/存储目标架构与元数据.md`
- Modify: `docs/数据库管理.md`
- Modify: `docs/存储服务架构与部署.md`
- Modify: `docs/架构总览.md`
- Modify: `docs/行情数据归档模块设计.md`
- Modify: `docs/量化金融数据概念.md`
- Modify: `docs/运维/MooX-EventBus运维.md`
- Modify: `docs/运维/MooX指标监控.md`
- Modify: `docs/运维/数据保留与磁盘空间.md`
- Modify: `docs/superpowers/specs/2026-07-19-storage-dataset-node-simplification-design.md`

- [ ] **Step 1: Write the failing zero-residual contract**

The script must require final Proto/SQL/UI/runtime symbols and reject case-insensitive current-code occurrences of:

```text
PrimaryStoreNode
primary_store_node
PrimaryStoreRoute
primary_store_route
t_dataset_topology_locks
attributes.service_target
metadata-monitor-.*-local-route
/#/ops/storage/routes
```

Scan source, generated output, schemas, seeds, examples, scripts, skills, and current docs. Exclude `.git`, `.worktrees`, binary/build output, historical files under `docs/superpowers/plans` and `docs/superpowers/specs`, dated code-review reports, and dated performance reports. Separately reject route fields `subject_pattern`, `hash_rule`, route `priority`, and DataNode `weight/config_json/endpoint` only in storage metadata contexts so unrelated domain uses are not false positives.

- [ ] **Step 2: Run the contract and capture residuals**

Run: `bash scripts/test-storage-datanode-management-contract.sh`

Expected: FAIL and print every remaining current symbol with file and line.

- [ ] **Step 3: Update current documentation and remove residual source**

Document Schema v5, direct binding, deployment registration, read-only Doctor check, explicit activation, immutable post-activation binding, DataNode UI, and no migration. Mark the 2026-07-19 design as superseded specifically for node/route management. Remove all current old-name prose rather than describing compatibility.

- [ ] **Step 4: Wire the contract into repository verification**

Add `test-storage-datanode-management-contract` to `Makefile` and include it in `verify-pr`/`verify` after Proto generation checks.

- [ ] **Step 5: Run contract and documentation checks**

```bash
bash scripts/test-storage-datanode-management-contract.sh
git diff --check
```

Expected: PASS with `storage DataNode management contract: ok` and no whitespace errors.

- [ ] **Step 6: Commit cleanup and docs**

```bash
git add scripts/test-storage-datanode-management-contract.sh Makefile README.md modules/storage/README.md skills/moox/references/protocol.md examples/e2e/README.md docs/主机监控架构设计.md docs/云节点执行平台架构.md docs/内置市场行情采集架构.md docs/协议设计.md docs/因子计算模块设计.md docs/存储层架构.md docs/存储目标架构与元数据.md docs/数据库管理.md docs/存储服务架构与部署.md docs/架构总览.md docs/行情数据归档模块设计.md docs/量化金融数据概念.md docs/运维/MooX-EventBus运维.md docs/运维/MooX指标监控.md docs/运维/数据保留与磁盘空间.md docs/superpowers/specs/2026-07-19-storage-dataset-node-simplification-design.md
git commit -m "docs(storage): document direct DataNode binding model"
```

### Task 14: Add backend lifecycle E2E and test every Proto consumer

**Files:**
- Create: `modules/storage/internal/service/e2e/datanode_management_test.go`
- Create: `scripts/e2e/storage-datanode-management.sh`
- Modify: `Makefile`

- [ ] **Step 1: Write the failing backend E2E**

Start real Schema v5 SQLite, Metadata cache/catalog, primary service, and two in-process DataNode services. Exercise this exact sequence:

```text
register node-a and node-b
create Dataset on node-a -> disabled, unlocked, revision 1
write rejected while disabled
Check returns ready without changing row
mutate metadata -> stale Activate returns CONFLICT
Check again + Activate -> active, locked, revision increments
write/read succeeds through node-a using Snapshot only
disable Dataset -> rebind rejected because locked
create second Dataset -> rebind node-a to node-b succeeds while unlocked
delete active/referenced node rejected
disable empty node and delete succeeds
```

Use a Store wrapper that counts/panics on runtime SQLite access after snapshot publication.

- [ ] **Step 2: Run E2E and observe missing integration**

Run: `CGO_ENABLED=1 go test ./modules/storage/internal/service/e2e -run TestDataNodeManagementLifecycle -count=1 -v`

Expected: FAIL until all service wiring is complete.

- [ ] **Step 3: Finish integration wiring without weakening assertions**

Wire real services through public request types and signed auth helpers. Do not call Store lifecycle methods directly except test setup. Publish cache snapshots through the production refresh path.

- [ ] **Step 4: Add the shell E2E entrypoint**

The script runs Proto generation diff check, contract test, focused Go E2E, and Doctor E2E. Add `e2e-storage-datanode-management` to `Makefile`.

- [ ] **Step 5: Test all generated-contract consumers**

```bash
go test ./modules/storage/... -count=1
go test ./modules/admin/... -count=1
go test ./modules/gateway/... -count=1
go test ./modules/eventbus/... -count=1
go test ./modules/archive/... -count=1
go test ./modules/collector/... -count=1
go test ./modules/factor/... -count=1
go test ./modules/strategy/... -count=1
go test ./modules/trade/... -count=1
go test ./modules/cloudnode/... -count=1
go test ./modules/monitor/... -count=1
go test ./modules/cli/... -count=1
go test ./packages/commonpb -count=1
```

Expected: PASS. Root `go test ./...` is not evidence in this multi-module workspace; every listed module must pass independently.

- [ ] **Step 6: Run the complete backend E2E entrypoint**

Run: `bash scripts/e2e/storage-datanode-management.sh`

Expected: PASS with lifecycle, Doctor read-only, and zero-residual summaries.

- [ ] **Step 7: Commit backend E2E**

```bash
git add modules/storage/internal/service/e2e scripts/e2e/storage-datanode-management.sh Makefile
git commit -m "test(storage): cover DataNode lifecycle end to end"
```

### Task 15: Add browser E2E and responsive visual verification

**Files:**
- Create: `web/tests/storage-datanode-management.e2e.spec.ts`
- Modify: `web/playwright.config.ts`
- Artifact only: `artifacts/storage-datanode-desktop.png`
- Artifact only: `artifacts/storage-datanode-mobile.png`
- Artifact only: `artifacts/storage-dataset-activation.png`

- [ ] **Step 1: Write Playwright network fixtures and workflow tests**

Mock signed gateway responses by method name, not component internals. Cover nodes list/tags, tag Tooltip hover and keyboard focus, detail drawer with no second Dataset request, admin-owned edit payload, delete error, unknown tab fallback, Dataset create payload, check-before-activate, revision forwarding, unlocked rebind, and locked rebind absence.

- [ ] **Step 2: Run browser E2E and observe failures**

Run: `pnpm --dir web exec playwright test tests/storage-datanode-management.e2e.spec.ts --project=chromium`

Expected: FAIL until selectors and flows match the final UI.

- [ ] **Step 3: Stabilize semantic selectors**

Use roles, accessible names, table text, and `data-testid` only where no semantic role is available. Do not use generated Arco class names. Ensure the Info icon is keyboard focusable and Tooltip text is discoverable through accessibility APIs.

- [ ] **Step 4: Capture desktop and mobile screenshots**

Run the spec at `1440x900` and `390x844`; save the three named artifacts. Verify in assertions that tags wrap without changing operation-column width, no text/button overlap exists, dialogs fit the viewport, title Info Tooltip stays on-screen, and the next page region remains reachable.

- [ ] **Step 5: Inspect screenshots and canvas pixels**

Use `view_image` on all three artifacts. Reject blank images, clipped longest Dataset names, overlapping service targets/actions, off-screen dialogs, or tooltip occlusion; fix CSS and rerun until clean.

- [ ] **Step 6: Run full frontend verification**

```bash
CI=true pnpm --dir web exec vitest run
pnpm --dir web run typecheck
pnpm --dir web run build:prod
pnpm --dir web exec playwright test tests/storage-datanode-management.e2e.spec.ts --project=chromium
```

Expected: PASS.

- [ ] **Step 7: Commit browser E2E; do not commit screenshots unless repository policy already tracks E2E artifacts**

```bash
git add web/tests/storage-datanode-management.e2e.spec.ts web/playwright.config.ts web/src
git commit -m "test(web): cover DataNode management workflows"
```

### Task 16: Add safe Schema v5 reset and remote verification commands

**Files:**
- Modify: `modules/cli/internal/command/setup.go`
- Modify: `modules/cli/internal/command/setup_test.go`
- Create: `modules/cli/internal/command/setup_storage.go`
- Create: `modules/cli/internal/command/setup_storage_test.go`
- Modify: `modules/cli/internal/setup/deploy/deploy.go`
- Modify: `modules/cli/internal/setup/deploy/deploy_test.go`
- Create: `web/tests/storage-datanode-management.remote.e2e.spec.ts`
- Create: `web/tests/remote-auth-global-setup.ts`
- Modify: `web/playwright.config.ts`
- Modify: `modules/cli/README.md`
- Modify: `docs/superpowers/specs/2026-07-16-custom-setup-config-design.md`

- [ ] **Step 1: Write failing reset-safety tests**

Add `--reset-storage-data` default false. Test it is passed into `setupdeploy.Options.ResetStorageData`. In deploy tests, default install copies prior `data/`; reset install does not copy storage data but still preserves `secrets/`; rollback restores the previous deployment if readiness fails.

- [ ] **Step 2: Run setup/deploy tests**

Run: `go test ./modules/cli/internal/command ./modules/cli/internal/setup/deploy -run 'SetupDeployStorage|Storage.*Reset' -count=1`

Expected: FAIL because the flag and option do not exist.

- [ ] **Step 3: Implement explicit reset without direct secret/config access**

Add `ResetStorageData bool` to deploy Options and pass one positional `0|1` value to the constant remote install script. When `1`, skip copying `$HOME/moox/storage/data`; preserve `$HOME/moox/storage/secrets`. The JSON result includes `reset_storage_data:true` but no filesystem paths or secrets.

- [ ] **Step 4: Write failing signed verification and E2E command tests**

Add command tests for `setup verify-storage`, `setup e2e-storage`, and `setup browser-e2e-storage`. All three load `custom.toml` only through the existing immutable Snapshot loader and require an explicit host. `verify-storage` is read-only and returns component health, exact commit, binary hashes, Schema version, signed DataNode identity/status, node count, Dataset count, and `route_rpc_registered:false`. `e2e-storage` uses a caller-supplied namespace, creates and cleans an isolated Space/DataSource/Dataset, and reports lifecycle assertions without row values. `browser-e2e-storage` launches the named Playwright remote spec, sends login material over the child process stdin, disables trace/video, and never writes credentials or browser storage state.

- [ ] **Step 5: Run command tests and observe missing commands**

Run: `go test ./modules/cli/internal/command -run 'Setup(VerifyStorage|E2EStorage|BrowserE2EStorage)' -count=1`

Expected: FAIL because the three commands do not exist.

- [ ] **Step 6: Implement sanitized remote verification boundaries**

Use the existing setup SSH transport and loopback forwarder. Signed Storage requests derive service auth from secrets held in the immutable Snapshot/deployed secret channel and never serialize AuthInfo. The lifecycle command performs `defer` cleanup for rows, Dataset, DataSource, and Space and reports cleanup status even after assertion failure. The browser command accepts `--repo-root`, invokes `pnpm --dir web exec playwright test tests/storage-datanode-management.remote.e2e.spec.ts --project=chromium`, and supplies base URL, username, and password through stdin to a global setup helper that retains them only in memory.

- [ ] **Step 7: Document the destructive and credential boundaries**

Document that reset is allowed only for explicitly confirmed pre-production Schema replacement. `custom.toml` remains read-only and only the setup CLI may load it. Verification output is sanitized; browser E2E must disable trace/video and must not persist session state.

- [ ] **Step 8: Run setup tests**

Run: `go test ./modules/cli/internal/command ./modules/cli/internal/setup/deploy -count=1`

Expected: PASS.

- [ ] **Step 9: Commit reset and verification support**

```bash
git add modules/cli/internal/command/setup.go modules/cli/internal/command/setup_test.go modules/cli/internal/command/setup_storage.go modules/cli/internal/command/setup_storage_test.go modules/cli/internal/setup/deploy modules/cli/README.md docs/superpowers/specs/2026-07-16-custom-setup-config-design.md web/tests/storage-datanode-management.remote.e2e.spec.ts web/tests/remote-auth-global-setup.ts web/playwright.config.ts
git commit -m "feat(setup): verify clean Storage deployments"
```

### Task 17: Run two independent review passes and close every finding

**Files:**
- Review: all changes from `39acc3d0..HEAD`

- [ ] **Step 1: Freeze an implementation candidate and record evidence**

Run:

```bash
git status --short
git diff --check
git log --oneline 39acc3d0..HEAD
git rev-parse HEAD
```

Expected: clean worktree, no diff-check errors, and a recorded candidate SHA.

- [ ] **Step 2: Start a fresh independent Agent for review pass 1**

The reviewer must not be an implementation Agent. Ask it to inspect `39acc3d0..HEAD` against both the design and this plan, prioritizing correctness, races, authorization, snapshot consistency, activation/rebind state transitions, generated consumers, frontend accessibility, E2E realism, and deploy safety. Require findings first with exact file/line references; no implementation edits by the reviewer.

- [ ] **Step 3: Validate and fix every pass-1 finding**

Use `superpowers:receiving-code-review`: reproduce each claim, reject unsupported findings with evidence, and add a regression test before each valid fix. Run focused tests, then commit:

```bash
git add -A
git commit -m "fix(storage): address independent review findings"
```

The command’s file list is the exact set reported and modified during this pass; verify it with `git diff --name-only HEAD^` before committing.

- [ ] **Step 4: Start a second fresh independent Agent from the new HEAD**

Give reviewer 2 the same design/plan and the new exact SHA, but do not provide reviewer 1’s conclusions. Require a from-scratch review of the full range, not only the fix commit.

- [ ] **Step 5: Close pass-2 findings and rerun affected suites**

For every valid finding, add a regression test, fix, run the affected module/browser suite, and commit with `fix(storage): close final review findings`. Repeat a fresh review if any severity-1 or severity-2 finding remains. Exit this task only when a fresh reviewer reports no actionable findings.

- [ ] **Step 6: Record review provenance**

Save reviewer task IDs, reviewed SHAs, findings disposition, and test commands in the final delivery note; do not commit conversational transcripts or secrets.

### Task 18: Verify exact committed HEAD and build Linux amd64 release artifacts

**Files:**
- Verify: committed repository state
- Artifact only: `release/`
- Artifact only: `artifacts/storage-datanode-release-sha256.txt`

- [ ] **Step 1: Run zero-residual and generated-code checks on clean HEAD**

```bash
test -z "$(git status --porcelain)"
VERIFY_SHA="$(git rev-parse HEAD)"
make proto
test -z "$(git status --porcelain)"
bash scripts/test-storage-datanode-management-contract.sh
```

Expected: clean before and after generation; contract PASS.

- [ ] **Step 2: Run all focused and repository verification suites**

```bash
bash scripts/e2e/storage-datanode-management.sh
CI=true pnpm --dir web exec vitest run
pnpm --dir web run typecheck
pnpm --dir web run build:prod
pnpm --dir web exec playwright test tests/storage-datanode-management.e2e.spec.ts --project=chromium
make verify-pr
```

Expected: PASS. If `make verify-pr` fails outside this change, diagnose and either fix a real regression or record exact unrelated evidence; do not claim closure from focused tests alone.

- [ ] **Step 3: Build exact-SHA Linux amd64 binaries**

```bash
VERSION="${VERIFY_SHA}" GIT_COMMIT="${VERIFY_SHA}" ./scripts/build-storage-linux.sh
VERSION="${VERIFY_SHA}" TARGET_GOOS=linux TARGET_GOARCH=amd64 ./scripts/build.sh cli
VERSION="${VERIFY_SHA}" TARGET_GOOS=linux TARGET_GOARCH=amd64 ./scripts/release.sh
```

Expected: `bin/moox-storage-primary`, `bin/moox-storage-view`, `bin/moox-storage-node`, `bin/moox-storage-cli`, `bin/moox-cli`, and release archive(s) are Linux amd64 and embed the exact commit.

- [ ] **Step 4: Record artifact hashes and architecture**

```bash
file bin/moox-storage-primary bin/moox-storage-view bin/moox-storage-node bin/moox-storage-cli bin/moox-cli
find release -type f -maxdepth 2 -print0 | sort -z | xargs -0 shasum -a 256 > artifacts/storage-datanode-release-sha256.txt
shasum -a 256 bin/moox-storage-primary bin/moox-storage-view bin/moox-storage-node bin/moox-storage-cli bin/moox-cli >> artifacts/storage-datanode-release-sha256.txt
```

Expected: every executable reports Linux x86-64/amd64 and every hash has 64 hexadecimal characters.

### Task 19: Deploy to the configured 106 host and run signed remote E2E

**Files:**
- Read only through CLI: repository-root `custom.toml`
- Artifact only: `artifacts/storage-datanode-106-deploy.json`
- Artifact only: `artifacts/storage-datanode-106-health.json`
- Artifact only: `artifacts/storage-datanode-106-e2e.json`

- [ ] **Step 1: Build the current `moox-cli` and validate the immutable setup file**

```bash
./scripts/build.sh cli
./bin/moox-cli setup validate --file ./custom.toml > artifacts/storage-datanode-106-validate.json
./bin/moox-cli setup status --file ./custom.toml > artifacts/storage-datanode-106-status.json
```

Expected: sanitized JSON, validation success, and setup state `completed`. Do not use `cat`, `rg`, `sed`, shell `source`, TOML parsers, or editor tools on `custom.toml`.

- [ ] **Step 2: Select the 106 host only from sanitized CLI output**

```bash
./bin/moox-cli setup hosts --file ./custom.toml > artifacts/storage-datanode-106-hosts.json
MOOX_106_HOST="$(jq -er '.hosts[] | select(.address == "106.53.107.122") | .name' artifacts/storage-datanode-106-hosts.json)"
test "$(jq -r --arg name "$MOOX_106_HOST" '[.hosts[] | select(.name == $name and .address == "106.53.107.122")] | length' artifacts/storage-datanode-106-hosts.json)" = "1"
```

Expected: exactly one sanitized host matches; no credential fields exist in the JSON.

- [ ] **Step 3: Perform the explicitly approved pre-production reset deployment**

```bash
./bin/moox-cli setup deploy-storage --file ./custom.toml --host "$MOOX_106_HOST" --reset-storage-data > artifacts/storage-datanode-106-deploy.json
```

Expected: JSON reports selected host, `status:"ready"`, and `reset_storage_data:true`. The command packages the exact checkout, deploys through setup SSH handling, rebuilds Schema v5, registers DataNode, runs read-only Doctor, explicitly activates ready Datasets, and updates Storage placement. Never call raw `ssh` with credentials extracted from the file.

- [ ] **Step 4: Verify signed health and identity through supported clients**

Use the signed setup verification command added in Task 16. It obtains credentials internally and emits sanitized JSON. Verify storage-primary, storage-view, and storage-node are ready; DataNode `GetNodeState` returns `READY` and `storage-node-0`; Metadata Schema is v5; ListDataNodes returns the node and all Dataset summaries; no route endpoint is registered.

```bash
./bin/moox-cli setup verify-storage --file ./custom.toml --host "$MOOX_106_HOST" > artifacts/storage-datanode-106-health.json
jq -e '.schema_version == 5 and .data_node.node_id == "storage-node-0" and .data_node.status == "READY" and (.route_rpc_registered | not)' artifacts/storage-datanode-106-health.json
```

Expected: PASS with only component names, IDs, counts, versions, hashes, and statuses in the JSON.

- [ ] **Step 5: Run remote management lifecycle E2E**

Run the Task 16 lifecycle command through its internally managed SSH tunnel. It lists DataNode summaries, creates a disabled temporary Dataset, checks activation, activates with revision, writes/reads one row, verifies binding lock, rejects rebind, deletes temporary resources, and confirms node deletion remains constrained.

```bash
./bin/moox-cli setup e2e-storage --file ./custom.toml --host "$MOOX_106_HOST" --namespace "codex-${VERIFY_SHA:0:12}" > artifacts/storage-datanode-106-e2e.json
jq -e '.status == "passed" and .cleanup == "completed"' artifacts/storage-datanode-106-e2e.json
```

Expected: PASS; output contains only IDs, revisions, check IDs, statuses, and assertion results.

- [ ] **Step 6: Run browser E2E against the deployed Admin UI**

Run the Task 16 browser command. It loads login material inside the setup process, streams it to Playwright through stdin, disables trace/video, and persists no browser state.

```bash
./bin/moox-cli setup browser-e2e-storage --file ./custom.toml --host "$MOOX_106_HOST" --repo-root . > artifacts/storage-datanode-106-browser.json
jq -e '.status == "passed" and .desktop == "passed" and .mobile == "passed"' artifacts/storage-datanode-106-browser.json
```

Expected: PASS for `/#/ops/storage/nodes?tab=nodes`, unknown-tab fallback, all Dataset Tags, Info hover/focus, detail drawer, Dataset activation UI, and absence of routes UI at desktop and 390px widths.

- [ ] **Step 7: Compare deployed provenance**

Require remote version output to equal `VERIFY_SHA`; compare remote binary SHA256 values with `artifacts/storage-datanode-release-sha256.txt`. A healthy process built from another SHA is a failed deployment.

- [ ] **Step 8: Preserve evidence and clean temporary E2E data**

Delete only the temporary E2E Dataset/rows through supported APIs. Keep deployment, health, E2E, commit SHA, and SHA256 evidence. Scan artifacts for strings matching password/key/secret/auth/token fields and delete/redact unsafe artifacts before any commit or final response.

### Task 20: Push the reviewed, deployed exact SHA

**Files:**
- Git metadata only

- [ ] **Step 1: Confirm final tree and deployed SHA**

```bash
git status --short
FINAL_SHA="$(git rev-parse HEAD)"
test "$FINAL_SHA" = "$VERIFY_SHA"
```

Expected: clean worktree and exact equality with the reviewed, tested, built, and deployed SHA. If deployment fixes created commits, repeat Tasks 17-19 from the new SHA.

- [ ] **Step 2: Push the feature branch**

```bash
git push -u origin feature/storage-datanode-management
git fetch origin feature/storage-datanode-management
test "$(git rev-parse HEAD)" = "$(git rev-parse origin/feature/storage-datanode-management)"
```

Expected: local and remote branch SHAs match exactly.

- [ ] **Step 3: Produce the delivery record**

Report final commit SHA, remote branch SHA, both independent review task IDs and reviewed SHAs, test/E2E commands, Linux amd64 artifact hashes, selected sanitized 106 host name/address, deployment result, signed health result, browser result, and any residual risk. Do not report completion if any required gate is missing.

## Acceptance Checklist

- [ ] No active code, Proto, generated output, schema, seed, current docs, CLI, or UI contains the old PrimaryStore node/route model.
- [ ] Schema v5 has final DataNode fields, mandatory Dataset binding, binding lock, revision, and restrictive foreign key; no migration exists.
- [ ] DataNode registration is deployment-only; browser administration changes only name/status.
- [ ] ListDataNodes returns every Dataset summary per node and no `dataset_count`; implementation is two-query, not N+1.
- [ ] Dataset creation is disabled/unlocked/revision 1 and requires active DataNode plus retention.
- [ ] Check is read-only; Activate reruns checks and uses revision CAS; first activation permanently locks binding.
- [ ] Rebind is atomic and only permitted for disabled, never-activated Datasets.
- [ ] Runtime read/write uses only one Metadata Snapshot and direct DataNode target; first write never locks or queries SQLite.
- [ ] Doctor remains read-only; deployment explicitly activates only after healthy self-check.
- [ ] DataNode page shows all Dataset names as wrapping clickable Tags and provides accessible Info tooltips; route UI is absent.
- [ ] Backend, Doctor, browser, contract, consumer, and exact-HEAD verification suites pass.
- [ ] Two fresh independent Agent review passes are complete with no actionable findings.
- [ ] Linux amd64 artifacts and hashes correspond to the exact deployed commit.
- [ ] `custom.toml` was accessed only through setup CLI and 106 deployment/health/E2E evidence is sanitized and complete.
