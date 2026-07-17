# Storage Primary And Shard Boundary Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Replace the ambiguous Storage Access and physical PrimaryStore naming with one public `storage` facade, a logical `PrimaryStore`, and strictly internal physical `DataShard` instances, while preserving a simple two-process default deployment.

**Architecture:** Admin Gateway owns the public `/api/admin/storage/{method}` namespace and dispatches each allowlisted method directly to Metadata, PrimaryStore, or DataView. PrimaryStore validates and routes authoritative-data operations to an embedded local DataShard by default or explicitly configured remote DataShards in the optional single-copy sharding topology. DataView remains an independent derived-query service. No compatibility aliases, replicated shards, automatic failover, leader election, rebalancing, or cross-shard transactions are introduced.

**Tech Stack:** Go 1.24 workspaces, tRPC-Go, Protocol Buffers, Pebble, SQLite, JetStream, Vue 3, TypeScript, Vitest, shell deployment scripts.

---

## Final Contract

| Responsibility | Final name | Exposure |
| --- | --- | --- |
| Browser and external API | `Storage`, `/api/admin/storage/{method}` | Public through Admin Gateway |
| Metadata backend | `Metadata` | Internal; reached through the facade |
| Authoritative-data application service | `PrimaryStore` | Internal tRPC and facade backend |
| Bounded authoritative scan | `PrimaryStoreScan` | Trusted internal callers only |
| Physical Pebble shard | `DataShard` | Strictly internal |
| Derived query service | `DataView` | Internal; reached through the facade |

The atomic rename is:

| Current | Final |
| --- | --- |
| logical `Access` | `PrimaryStore` |
| `AccessScan` | `PrimaryStoreScan` |
| physical `PrimaryStore` | `DataShard` |
| `PrimaryStoreNode/Route/Target/Key/Row` | `ShardNode/Route/Target/Key/Row` |
| logical role `access` | `primary` |
| physical role `primary` | `shard` |
| `storage-access` / `moox-storage-access` | `storage-primary` / `moox-storage-primary` |
| old physical `storage-primary` | optional `storage-shard` |

## Non-Negotiable Constraints

- Do not retain forwarding packages, type aliases, duplicate proto services, old config keys, or transitional service-deployment rows.
- Do not route DataView calls through PrimaryStore. The Gateway dispatches them directly to DataView.
- Do not publish `PrimaryStoreScan`, `DataShard`, ViewIndex, timer, or maintenance methods through the public facade.
- Keep the standard installation to `storage-primary` and `storage-view`; `storage-primary` embeds one local DataShard.
- Treat optional multi-machine DataShard placement as capacity sharding only. A shard failure makes that shard unavailable until manual recovery.
- Preserve unrelated working-tree changes. In particular, do not overwrite concurrent CLI release/setup edits while applying the targeted renames.

### Task 1: Lock The New Boundary With Failing Contract Tests

**Files:**
- Create: `scripts/test-storage-boundary-contract.sh`
- Modify: `Makefile`
- Modify: `modules/admin/internal/gateway/gateway_test.go`
- Modify: `modules/admin/internal/service/sysdeploy/defaults_test.go`
- Modify: `modules/storage/cmd/server/runtime_config_test.go`
- Modify: `web/src/api/storage/http.test.ts`

- [ ] **Step 1: Add an active-tree obsolete-name scan**

Create a read-only script that scans active Go, proto, YAML, shell, TypeScript, Vue, README, and architecture-document surfaces. It must fail on these obsolete identities:

```text
trpc.moox.storage.Access
trpc.moox.storage.AccessScan
WritePrimaryRows|ReadPrimaryRows|ScanPrimaryRows|DeletePrimaryRows
PrimaryStoreNode|PrimaryStoreRoute|PrimaryStoreTarget|PrimaryStoreKey|PrimaryStoreRow
primary.service_name
storage_access|storage-access|moox-storage-access
```

Exclude `docs/superpowers/specs`, `docs/superpowers/plans`, generated build output, `.git`, and dependency directories. Do not ban generic words such as `access`, and do not ban the final logical `PrimaryStore` name.

- [ ] **Step 2: Add Gateway facade expectations**

Add table-driven tests proving:

```text
/api/admin/storage/GetSpace               -> storage_metadata
/api/admin/storage/WriteTimeSeriesRows    -> storage_primary
/api/admin/storage/QueryTimeSeriesRows    -> storage_view
/api/admin/storage/ScanTimeSeriesRows     -> 404
/api/admin/storage/WriteShardRows         -> 404
/api/admin/storage/UnknownMethod          -> 404
```

Also prove direct public requests for `storage_metadata`, `storage_primary`, `storage_view`, and `storage_shard` are rejected.

- [ ] **Step 3: Add runtime and deployment expectations**

Tests must require logical role `primary`, physical role `shard`, an embedded DataShard when `shard.service_name` is empty, and internal-only SysDeploy rows. The default deployment must contain no Access service and no standalone DataShard service.

- [ ] **Step 4: Add frontend URL expectations**

Require every Storage browser request to use exactly:

```ts
`/api/admin/storage/${method}`
```

Reject any generated URL containing `storage_access`, `storage_metadata`, or `storage_view`.

- [ ] **Step 5: Run focused tests and record the expected red state**

```bash
(cd modules/admin && env GOCACHE=/tmp/moox-gocache go test -count=1 ./internal/gateway ./internal/service/sysdeploy)
(cd modules/storage && env GOCACHE=/tmp/moox-gocache go test -count=1 ./cmd/server)
pnpm --dir web exec vitest run src/api/storage/http.test.ts
bash scripts/test-storage-boundary-contract.sh
```

Expected: tests fail only because the old names and routing behavior still exist.

### Task 2: Replace The Storage Proto Vocabulary Atomically

**Files:**
- Rename: `modules/storage/proto/access.proto` -> `modules/storage/proto/primary_store.proto`
- Rename: `modules/storage/proto/store.proto` -> `modules/storage/proto/data_shard.proto`
- Modify: `modules/storage/proto/metadata.proto`
- Modify: `modules/storage/proto/Makefile`
- Regenerate: `modules/storage/proto/storagegen/*`

- [ ] **Step 1: Define the logical PrimaryStore services**

Rename `Access` to `PrimaryStore` without changing the authoritative row method semantics. Rename `AccessScan` to `PrimaryStoreScan`. Keep bounded scan RPCs out of the public method registry.

```proto
service PrimaryStore {
  rpc WriteTimeSeriesRows(WriteTimeSeriesRowsReq) returns (WriteTimeSeriesRowsRsp);
  rpc ReadTimeSeriesRows(ReadTimeSeriesRowsReq) returns (ReadTimeSeriesRowsRsp);
  rpc DeleteTimeSeriesRows(DeleteTimeSeriesRowsReq) returns (DeleteTimeSeriesRowsRsp);
  rpc WriteRecordRows(WriteRecordRowsReq) returns (WriteRecordRowsRsp);
  rpc ReadRecordRows(ReadRecordRowsReq) returns (ReadRecordRowsRsp);
}

service PrimaryStoreScan {
  rpc ScanTimeSeriesRows(ScanTimeSeriesRowsReq) returns (ScanTimeSeriesRowsRsp);
  rpc ScanRecordRows(ScanRecordRowsReq) returns (ScanRecordRowsRsp);
}
```

- [ ] **Step 2: Define the physical DataShard service**

Rename physical messages to `ShardKey`, `ShardRow`, and `ShardTarget`, and expose only internal physical operations:

```proto
service DataShard {
  rpc WriteShardRows(WriteShardRowsReq) returns (WriteShardRowsRsp);
  rpc ReadShardRows(ReadShardRowsReq) returns (ReadShardRowsRsp);
  rpc ScanShardRows(ScanShardRowsReq) returns (ScanShardRowsRsp);
  rpc DeleteShardRows(DeleteShardRowsReq) returns (DeleteShardRowsRsp);
}
```

- [ ] **Step 3: Rename topology metadata**

Replace `PrimaryStoreNode` and `PrimaryStoreRoute` with `ShardNode` and `ShardRoute`. Rename fields only where they describe a physical shard. Keep logical PrimaryStore fields named for PrimaryStore.

- [ ] **Step 4: Regenerate code and delete obsolete generated files**

```bash
make -C modules/storage/proto clean
make -C modules/storage/proto
gofmt -w modules/storage/proto/storagegen
```

Expected: generated clients and servers expose `PrimaryStore`, `PrimaryStoreScan`, and `DataShard`; no generated Access or old physical PrimaryStore service remains.

- [ ] **Step 5: Commit the contract boundary**

```bash
git add modules/storage/proto
git commit -m "refactor(storage): rename primary and shard contracts"
```

### Task 3: Rename Physical Persistence To DataShard

**Files:**
- Rename: `modules/storage/internal/service/primary/` -> `modules/storage/internal/service/datashard/`
- Rename: `modules/storage/internal/core/router/` -> `modules/storage/internal/core/shardrouter/`
- Modify: `modules/storage/internal/core/metadata/store.go`
- Rename: `modules/storage/internal/infra/metadata/sqlite/crud_store.go` -> `modules/storage/internal/infra/metadata/sqlite/crud_shard.go`
- Rename: `modules/storage/internal/infra/metadata/sqlite/crud_store_test.go` -> `modules/storage/internal/infra/metadata/sqlite/crud_shard_test.go`
- Modify: `modules/storage/schema/metadata.sql`
- Modify: `modules/storage/internal/bootstrap/metadata/seed.go`
- Modify: `modules/storage/internal/bootstrap/metadata/seed_test.go`
- Modify: `modules/storage/config/metadata.seed.yaml`
- Modify: `examples/platform-local.seed.yaml`
- Modify: `examples/metadata-monitor-metrics-local-route.seed.yaml`
- Modify: `examples/metadata-monitor-host-local-route.seed.yaml`

- [ ] **Step 1: Rename implementation and tests before changing behavior**

Move local, remote, client, service, Outbox relay, and test files to package `datashard`. Rename service/client types and RPC implementations to `DataShard`. Preserve Pebble batching, Outbox atomicity, key encoding, pagination, and retry behavior.

- [ ] **Step 2: Rename the resolver around physical topology**

Package `shardrouter` must resolve `ShardNode`, `ShardRoute`, and `ShardTarget`. Preserve exact route precedence and weighted rendezvous behavior. Add or update tests for stable selection, disabled nodes, missing routes, and explicit local target selection.

- [ ] **Step 3: Rename physical metadata persistence**

Replace the schema objects atomically:

```text
t_primary_store_nodes  -> t_shard_nodes
t_primary_store_routes -> t_shard_routes
```

Rename associated indexes, triggers, foreign keys, DAO methods, scan helpers, and seed keys. Because this project requires no historical compatibility, do not add migration views or dual-read logic.

- [ ] **Step 4: Rename CLI metadata seed types and commands**

Update:

```text
modules/cli/internal/command/metadata_types.go
modules/cli/internal/command/metadata_implementation.go
modules/cli/internal/command/metadata_spaces.go
modules/cli/internal/command/*metadata*_test.go
```

Use `ShardNode` and `ShardRoute` in JSON/YAML and user-facing output. Reject old `primary_store_nodes` and `primary_store_routes` seed keys.

- [ ] **Step 5: Run physical-layer tests**

```bash
(cd modules/storage && env GOCACHE=/tmp/moox-gocache go test -count=1 ./internal/service/datashard/... ./internal/core/shardrouter/... ./internal/infra/metadata/sqlite/... ./internal/bootstrap/metadata/...)
(cd modules/cli && env GOCACHE=/tmp/moox-gocache go test -count=1 ./internal/command/...)
```

Expected: all tests pass, including Pebble/Outbox and routing tests.

### Task 4: Rename The Logical Service To PrimaryStore

**Files:**
- Rename: `modules/storage/internal/service/access/` -> `modules/storage/internal/service/primarystore/`
- Modify: `modules/storage/cmd/server/main.go`
- Modify: `modules/storage/cmd/server/view_runtime.go`
- Modify: `modules/storage/cmd/server/runtime_config.go`
- Modify: `modules/storage/cmd/server/*_test.go`
- Create: `modules/storage/config/storage_primary/trpc_go.yaml`
- Modify: `modules/storage/config/storage_view/trpc_go.yaml`
- Delete: `modules/storage/config/storage.access.yaml`
- Delete: `modules/storage/config/trpc_go.access.yaml`
- Modify or delete after consolidation: `modules/storage/config/storage.yaml`
- Modify or delete after consolidation: `modules/storage/config/trpc_go.yaml`

- [ ] **Step 1: Move the logical service package**

Rename package `access` to `primarystore`, including time-series, record, validation, fact-reader, metadata CRUD integration, scan, host cleanup, and tests. The logical service must call `DataShard` through a narrow client interface and must not import Pebble infrastructure directly.

- [ ] **Step 2: Make runtime roles unambiguous**

Use only:

```text
primary  logical PrimaryStore plus optional embedded DataShard
shard    standalone internal DataShard for advanced manual deployment
view     DataView, ViewBuilder, and ViewIndex runtime
```

Rename helper functions so their subject is explicit, for example `shouldCreatePrimaryStoreService` and `shouldCreateDataShardService`. Delete parsing support for roles `access` and old physical `primary` semantics.

- [ ] **Step 3: Implement default embedded-shard selection**

Configuration must use:

```yaml
storage:
  roles: [primary]
  shard:
    service_name: ""
```

An empty `shard.service_name` creates one embedded DataShard backed by the configured local Pebble directory. A non-empty value creates only a remote DataShard client. Never start both accidentally.

- [ ] **Step 4: Give each deployed process one runtime YAML**

`storage-primary` reads only `storage_primary/trpc_go.yaml`; `storage-view` reads only `storage_view/trpc_go.yaml`. Keep component options inside the tRPC plugin/config sections instead of requiring a second `storage.yaml` at runtime.

- [ ] **Step 5: Update all internal callers**

Update imports and client names in ViewBuilder, Archive, Collector, CloudNode, CLI, tests, mocks, and config. ViewBuilder and Archive may call trusted `PrimaryStore` or `PrimaryStoreScan`; no caller except PrimaryStore may call DataShard.

- [ ] **Step 6: Run Storage module tests**

```bash
(cd modules/storage && env GOCACHE=/tmp/moox-gocache go test -count=1 ./...)
```

Expected: all Storage tests pass with only final names and roles.

- [ ] **Step 7: Commit the runtime rename**

```bash
git add modules/storage modules/cli/internal/command examples
git commit -m "refactor(storage): separate primary store from data shards"
```

### Task 5: Add The Single Public Storage Facade

**Files:**
- Create: `modules/admin/internal/gateway/storage_facade.go`
- Create: `modules/admin/internal/gateway/storage_facade_test.go`
- Modify: `modules/admin/internal/gateway/gateway.go`
- Modify: `modules/admin/internal/gateway/forward.go`
- Modify: `modules/admin/internal/gateway/gateway_test.go`
- Modify: `modules/admin/internal/gateway/forward_test.go`

- [ ] **Step 1: Define one fail-closed method registry**

Represent each public method with its internal backend and rate-limit class:

```go
type storageBackend string

const (
	storageMetadataBackend storageBackend = "storage_metadata"
	storagePrimaryBackend  storageBackend = "storage_primary"
	storageViewBackend     storageBackend = "storage_view"
)

type storageMethodRoute struct {
	Backend storageBackend
	Class   requestClass
}
```

List every intentionally public Metadata, PrimaryStore, and DataView method explicitly. Missing methods fail closed. Do not derive public exposure from proto reflection or deployment-table contents.

- [ ] **Step 2: Resolve the facade before deployment lookup**

When `serviceID == "storage"`, map the method to its internal backend before resolving `t_service_deployments`. For every other public service, preserve existing Gateway behavior. Retain the original public path for authentication, authorization, audit fields, and client-visible errors.

- [ ] **Step 3: Keep internal identities non-addressable**

Direct browser routes for `storage_metadata`, `storage_primary`, `storage_view`, `storage_primary_trpc`, `storage_view_trpc`, and any `storage_shard` identity must fail. Internal backend resolution is a Gateway implementation detail, not a public alias.

- [ ] **Step 4: Apply method-specific request limits**

Classify metadata CRUD, authoritative writes, authoritative reads, and derived scans separately using the existing limiter mechanism. Tests must prove a caller cannot evade a stricter method limit by changing the backend service ID.

- [ ] **Step 5: Run Gateway tests**

```bash
(cd modules/admin && env GOCACHE=/tmp/moox-gocache go test -count=1 ./internal/gateway)
```

Expected: allowlisted facade methods route correctly; internal and unknown methods return 404.

### Task 6: Make SysDeploy Describe Only The New Topology

**Files:**
- Modify: `modules/admin/internal/service/sysdeploy/defaults.go`
- Modify: `modules/admin/internal/service/sysdeploy/defaults_test.go`
- Modify: `modules/admin/internal/service/sysdeploy/dao.go`
- Modify: `modules/admin/internal/service/sysdeploy/acceptance_test.go`
- Modify: `modules/admin/internal/service/sysdeploy/storage_topology_checker.go`
- Modify: `modules/admin/internal/service/sysdeploy/storage_topology_checker_test.go`
- Modify: `examples/service-deployments.seed.yaml`

- [ ] **Step 1: Replace default deployment identities**

Seed internal backend rows for:

```text
storage_metadata       HTTP Metadata backend
storage_primary        HTTP PrimaryStore backend
storage_view           HTTP DataView backend
storage_primary_trpc   trusted logical PrimaryStore tRPC
storage_view_trpc      trusted DataView tRPC
```

All must be internal and `gateway_enabled=false`; the public `storage` facade is code-owned and must not be represented as a directly forwarded deployment row.

- [ ] **Step 2: Remove obsolete seeded rows**

Before applying defaults, delete `storage_access`, `storage_access_trpc`, and the old physical meaning of `storage_primary_trpc`. Then seed the final logical `storage_primary_trpc` row. Do not leave disabled tombstones or aliases.

- [ ] **Step 3: Keep DataShard topology out of SysDeploy**

DataShard endpoints belong only to `ShardNode` metadata. The default topology checker must validate that no DataShard route is public and that standard deployment uses an embedded local shard.

- [ ] **Step 4: Run SysDeploy tests**

```bash
(cd modules/admin && env GOCACHE=/tmp/moox-gocache go test -count=1 ./internal/service/sysdeploy)
```

Expected: default rows are internal, obsolete rows are removed, and topology acceptance passes.

### Task 7: Route The Entire Frontend Through Storage

**Files:**
- Modify: `web/src/api/storage/http.ts`
- Rename: `web/src/api/storage/access.ts` -> `web/src/api/storage/primary-store.ts`
- Modify: `web/src/api/storage/metadata.ts`
- Modify: all imports under `web/src/` that reference Storage Access APIs
- Modify: `web/src/views/ops/storage/nodes.vue`
- Modify: `web/src/views/ops/storage/routes.vue`
- Modify: related Vitest files under `web/src/api/storage/` and `web/src/views/ops/storage/`

- [ ] **Step 1: Collapse public URL construction**

Keep domain-specific TypeScript modules for type safety, but make metadata, authoritative data, and view queries call the same helper:

```ts
async function callStorage<TReq extends object, TRsp extends { ret_info: RetInfo }>(
  method: string,
  req: TReq
): Promise<TRsp> {
  const rsp = await storageClient.post<TRsp>(`/api/admin/storage/${method}`, {
    auth_info: getStorageAuthInfo(),
    ...req
  });
  assertSuccess(rsp.data.ret_info);
  return rsp.data;
}
```

Export `callMetadata`, `callPrimaryStore`, and `callView` as typed wrappers over this one helper. Remove the group-to-service-ID map and the `callAccess` export.

No browser bundle may contain public `storage_metadata`, `storage_primary`, `storage_access`, or `storage_view` route construction.

- [ ] **Step 2: Rename authoritative client concepts**

Rename Access API exports to PrimaryStore language. Rename topology types and visible labels from Primary Store node/route to Data Shard node/route. Do not rename the `access` concept where it means authorization or network access.

- [ ] **Step 3: Surface the operational limitation honestly**

The shard topology page must state concisely that each DataShard is single-copy and requires manual recovery; it must not claim replication or automatic failover.

- [ ] **Step 4: Run frontend verification**

```bash
pnpm --dir web exec vitest run
pnpm --dir web exec eslint . --max-warnings=0
pnpm --dir web exec prettier --check .
pnpm --dir web build
```

Expected: all commands pass and production assets contain only `/api/admin/storage/` for Storage browser APIs.

### Task 8: Simplify Build, Release, Deployment, And CLI Status

**Files:**
- Modify: `scripts/build.sh`
- Modify: `scripts/release.sh`
- Modify: `scripts/deploy-moox.sh`
- Modify: `scripts/test-deploy-moox-storage-view.sh`
- Modify: `scripts/test-deploy-moox-storage-profile.sh`
- Modify: `modules/cli/internal/setup/deploy/deploy.go`
- Modify: relevant assertions in `modules/cli/internal/setup/deploy/deploy_test.go`
- Modify: `modules/cli/internal/setup/client/client.go`
- Modify: related CLI setup/client tests

- [ ] **Step 1: Publish only standard Storage binaries**

The normal release contains:

```text
moox-storage-primary
moox-storage-view
```

Do not include `moox-storage-access`. Do not publish a standalone `moox-storage-shard` in the standard personal deployment profile.

- [ ] **Step 2: Deploy two processes and two YAML files**

Install exactly:

```text
storage-primary/config/trpc_go.yaml
storage-view/config/trpc_go.yaml
```

Start PrimaryStore before DataView and stop them in reverse order. Preserve distinct health endpoints and log files. Ensure restart, status, uninstall, and partial-failure cleanup all use final names.

- [ ] **Step 3: Rename CLI readiness and status fields**

Replace `StorageAccessReady` and `storage-access` output with `StoragePrimaryReady` and `storage-primary`. Update client-side service lists and health aggregation. Work around concurrent edits rather than reverting unrelated tests or release behavior.

- [ ] **Step 4: Keep advanced DataShard deployment manual**

The generic `moox-storage` build may still run role `shard` when an operator deliberately supplies a private config, but standard scripts must neither launch it nor suggest HA. Validate that its listener is private and absent from Gateway/SysDeploy defaults.

- [ ] **Step 5: Run deployment and CLI tests**

```bash
bash scripts/test-deploy-moox-storage-view.sh
bash scripts/test-deploy-moox-storage-profile.sh
(cd modules/cli && env GOCACHE=/tmp/moox-gocache go test -count=1 ./internal/setup/...)
```

Expected: scripts validate the two-process default, and CLI status reports PrimaryStore and DataView readiness.

### Task 9: Publish The Storage Lifecycle And Failure Model

**Files:**
- Create: `docs/存储层架构.md`
- Delete: `docs/存储引擎架构.md`
- Delete: `docs/存储服务架构与部署.md`
- Modify: `docs/架构总览.md`
- Modify: `docs/存储概念与设计意图.md`
- Modify: `docs/存储目标架构与元数据.md`
- Modify: `modules/storage/README.md`
- Modify: `modules/cli/README.md`
- Modify: `skills/moox/references/custom-setup.md`
- Modify: `skills/moox/references/binary-release.md`
- Modify: `skills/moox/references/release.md`
- Modify: current examples and runbooks returned by the obsolete-name scan

- [ ] **Step 1: Establish one authoritative Storage architecture document**

Create `docs/存储层架构.md` by reconciling current implementation facts from
the two existing Storage documents, then delete
`docs/存储引擎架构.md` and `docs/存储服务架构与部署.md`. Do not leave redirect
stubs, because this repository does not preserve obsolete documentation paths.

Use this exact responsibility split:

| Document | Owns | Must not own |
| --- | --- | --- |
| `docs/架构总览.md` | Storage's place in the whole system, public facade, major internal boundaries, link to the detailed document | ports, complete role matrices, config examples, operational procedures |
| `docs/存储层架构.md` | service architecture, data/event flows, engines, roles, deployment, cleanup/archive, failure model, observability | exhaustive field semantics and SQL table definitions |
| `docs/存储概念与设计意图.md` | Space, Dataset, Subject, View, fact-row semantics | runtime and deployment instructions |
| `docs/存储目标架构与元数据.md` | metadata entities, relationships, routing model | process topology and release procedures |

- [ ] **Step 2: Write the complete Storage architecture**

`docs/存储层架构.md` must contain these sections in order:

```text
1. 文档定位与适用范围
2. 架构目标与明确不支持的能力
3. Storage 在 MooX 中的位置
4. 对外 Storage Facade 与方法路由
5. Metadata、PrimaryStore、PrimaryStoreScan、DataShard、DataView、ViewIndex、Archive 职责
6. 事实写入、权威读取、派生查询、事件物化与归档数据流
7. SQLite、Pebble、DuckDB、Bleve、Parquet、JetStream 的所有权
8. Runtime Roles 与进程装配
9. 默认单机部署：storage-primary + storage-view
10. 可选多机单副本 DataShard 分片
11. 后台 Timer、历史数据自动清理、归档与磁盘增长控制
12. 一致性、部分成功、重试、重建与故障恢复
13. 安全边界、内部服务暴露和鉴权
14. 健康检查、日志、指标与运维检查
15. 配置、端口、数据目录和启动/停止顺序
16. 测试与架构约束
```

The document must describe current supported behavior, not mix implemented
behavior with an unlabeled target architecture. Unsupported capabilities belong
in an explicit limitation section.

- [ ] **Step 3: Document the public and internal request paths**

Show these three paths explicitly:

```text
Browser -> Admin Gateway /api/admin/storage/* -> Metadata | PrimaryStore | DataView
PrimaryStore -> embedded or remote DataShard -> Pebble + Outbox
ViewBuilder -> PrimaryStore/PrimaryStoreScan -> DuckDB/Bleve -> DataView
```

State that DataView calls do not traverse PrimaryStore.

- [ ] **Step 4: Document the standard deployment**

Describe `storage-primary` with embedded DataShard and separate `storage-view`, each with one `trpc_go.yaml`. Provide start order, health endpoints, data directories, cleanup policies, backup expectations, and disk-growth controls.

- [ ] **Step 5: Document optional single-copy sharding without overselling it**

State plainly:

- multiple DataShard machines distribute storage capacity;
- there is no replica, leader election, automatic failover, migration, rebalancing, or cross-shard transaction;
- a failed shard is unavailable until manual repair or restore;
- operators must back up authoritative Pebble data and archived data.

- [ ] **Step 6: Synchronize the system architecture overview**

Replace the detailed, stale Storage implementation block in
`docs/架构总览.md` with a concise system-level summary. It must show:

```text
External caller -> Admin Gateway storage facade
                     |-> Metadata
                     |-> PrimaryStore -> DataShard -> Pebble
                     `-> DataView -> ViewIndex -> DuckDB/Bleve
```

Keep the fact-source versus derived-view distinction, the standard two-process
deployment, and the lack of automatic shard failover. Link prominently to
`存储层架构.md` for all details.

- [ ] **Step 7: Update concepts, metadata, commands, and examples**

Every active example must use `storage-primary`, `moox-storage-primary`, role `primary`, role `shard` only for advanced private deployment, and `shard_nodes` / `shard_routes` metadata.

Update every active link to either deleted Storage document so it points to
`docs/存储层架构.md` or the narrower concepts/metadata reference. Use the final
PrimaryStore and DataShard vocabulary in the two retained reference documents.

- [ ] **Step 8: Add documentation ownership checks**

Extend `scripts/test-storage-boundary-contract.sh` to fail when:

```text
docs/存储引擎架构.md exists
docs/存储服务架构与部署.md exists
docs/存储层架构.md is missing
an active file links to either deleted path
docs/架构总览.md does not link to 存储层架构.md
```

Run:

```bash
bash scripts/test-storage-boundary-contract.sh
pnpm docs:build
```

Expected: the ownership checks pass, all internal Markdown links resolve, and
the documentation site builds successfully.

### Task 10: Enforce The Boundary And Deliver Cleanly

**Files:**
- Modify: `Makefile`
- Modify: `scripts/test-storage-boundary-contract.sh`
- Modify: only files found by final verification

- [ ] **Step 1: Wire the contract scan into the repository gate**

Add `scripts/test-storage-boundary-contract.sh` to `make verify-custom-setup` or the closest existing always-run architecture gate. It must be read-only and deterministic.

- [ ] **Step 2: Run targeted boundary verification**

```bash
bash scripts/test-storage-boundary-contract.sh
rg -n 'trpc\.moox\.storage\.(Access|AccessScan)|WritePrimaryRows|ReadPrimaryRows|ScanPrimaryRows|DeletePrimaryRows|PrimaryStore(Node|Route|Target|Key|Row)|primary\.service_name|storage[_-]access|moox-storage-access' \
  modules packages scripts web/src examples docs/*.md skills/moox
```

Expected: the script passes; the manual scan returns no active obsolete identities. Historical dated specs and plans are intentionally outside the scan.

- [ ] **Step 3: Run complete backend verification**

```bash
(cd modules/storage && env GOCACHE=/tmp/moox-gocache go test -race -count=1 ./...)
(cd modules/admin && env GOCACHE=/tmp/moox-gocache go test -race -count=1 ./...)
(cd modules/cli && env GOCACHE=/tmp/moox-gocache go test -race -count=1 ./...)
make verify-custom-setup
make verify
```

Expected: every command passes without modifying tracked files.

- [ ] **Step 4: Run complete frontend verification**

```bash
pnpm --dir web exec vitest run
pnpm --dir web exec eslint . --max-warnings=0
pnpm --dir web exec prettier --check .
pnpm --dir web build
```

Expected: tests, lint, formatting, type checking, and production build pass.

- [ ] **Step 5: Inspect the final diff and repository state**

```bash
git status --short
git diff --check
git diff --stat
```

Confirm that every change belongs to this refactor or a consciously preserved concurrent edit, generated files match their protos, deleted old files have no forwarding replacements, and no runtime session remains active.

- [ ] **Step 6: Commit and push all session changes**

```bash
git add --all
git commit -m "refactor(storage): establish primary and data shard boundaries"
git push
git status --short
```

Expected: push succeeds and the final status is clean.

## Acceptance Checklist

- [ ] External callers use only `/api/admin/storage/{method}`.
- [ ] Gateway dispatches directly to Metadata, PrimaryStore, or DataView from a static allowlist.
- [ ] PrimaryStore is the sole logical owner of authoritative validation, routing, and aggregation.
- [ ] DataShard is the sole physical Pebble/Outbox owner and is never publicly routable.
- [ ] PrimaryStoreScan is internal only.
- [ ] Standard deployment starts only `storage-primary` and `storage-view` and reads one YAML per process.
- [ ] `storage-primary` embeds one local DataShard by default.
- [ ] Optional remote DataShards are documented as single-copy capacity sharding with manual recovery.
- [ ] `docs/存储层架构.md` is the only detailed root Storage architecture document.
- [ ] `docs/架构总览.md` contains a concise Storage summary and links to `docs/存储层架构.md`.
- [ ] The two superseded Storage documents are deleted and no active link references them.
- [ ] Old Access and old physical PrimaryStore identities are absent from active source, config, generated code, scripts, tests, examples, and current docs.
- [ ] All focused tests, `make verify-custom-setup`, `make verify`, frontend checks, and deployment-script tests pass.
