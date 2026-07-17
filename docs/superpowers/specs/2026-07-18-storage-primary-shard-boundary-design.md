# Storage Primary And Shard Boundary Design

## Context

MooX Storage currently exposes separate `storage_access`, `storage_view`, and
`storage_metadata` identities through the Admin Gateway. The `Access` service
validates fact rows, resolves routes, groups requests by target, calls
the current physical `PrimaryStore`, and merges reads. That physical service
owns Pebble and may run inside the Access process or on a remote machine.

The current names obscure those responsibilities. `Access` is too generic, and
reusing `PrimaryStore` for both the logical and physical layers would be
ambiguous. Public callers also need to understand internal service boundaries
that should remain an implementation detail.

This design gives Storage one public identity and names its internal layers by
responsibility:

```text
Storage      public API facade
PrimaryStore authoritative data validation, routing, and aggregation
DataShard    internal single-copy physical shard backed by Pebble
DataView     derived DuckDB and Bleve queries
Archive      long-term fact archive
```

## Goals

- Expose one logical `storage` API through the Gateway.
- Keep PrimaryStore and DataView as separate internal services and failure
  domains.
- Rename `Access` to `PrimaryStore` across active source, configuration,
  deployment, tests, and current documentation.
- Rename the current physical `PrimaryStore` to `DataShard` and make it
  strictly internal.
- Preserve optional horizontal partitioning across multiple machines without
  adding replication, leader election, or automatic failover.
- Make standard single-host deployment simple: `storage-primary` embeds one
  local DataShard, while `storage-view` remains a separate process.
- State the single-copy storage and manual recovery limits plainly in user
  documentation.

## Non-Goals

- Replication, quorum writes, leader election, or automatic failover.
- Automatic node removal, route reassignment, shard migration, or rebalancing.
- Cross-shard transactions or rollback of already committed shard writes.
- Hiding partial-success semantics for batches that span shards.
- Turning PrimaryStore into a proxy for DataView.
- Adding a standalone Storage facade process.
- Preserving old Access names or old physical PrimaryStore symbols through
  aliases, forwarding services, compatibility packages, or deprecated
  configuration keys.

## Target Architecture

```text
External client
    |
    | /api/admin/storage/{method}
    v
Admin Gateway
    |-- metadata methods ----------> Metadata
    |-- authoritative row methods -> PrimaryStore
    `-- derived query methods -----> DataView

Trusted internal services
    |-- Collector / SCF ----------> PrimaryStore
    |-- ViewBuilder / Archive ----> PrimaryStore / PrimaryStoreScan
    `-- PrimaryStore --------------> DataShard nodes

PrimaryStore
    |-- local mode ---------------> embedded DataShard -> Pebble
    `-- partitioned mode
          |-----------------------> DataShard A -> Pebble A
          |-----------------------> DataShard B -> Pebble B
          `-----------------------> DataShard C -> Pebble C
```

The Gateway is the public facade. It authenticates, authorizes, limits, audits,
and dispatches requests. It does not interpret rows, merge results, or perform
business queries. PrimaryStore remains the only component that understands
authoritative-data validation and shard routing. DataView queries its own
derived indexes directly and never pass through PrimaryStore.

## Public API Boundary

Public HTTP callers use one service identifier:

```text
/api/admin/storage/{method}
```

Examples:

```text
/api/admin/storage/WriteTimeSeriesRows
/api/admin/storage/ReadRecordRows
/api/admin/storage/QueryTimeSeriesRows
/api/admin/storage/SearchRecordRows
```

The Gateway owns a static, reviewed method registry. Each entry maps one public
method to exactly one internal service category: Metadata, PrimaryStore, or
DataView. Unknown and duplicate methods fail closed. The registry must not
include `PrimaryStoreScan`, `DataShard`, ViewIndex, or maintenance methods.

The public facade does not introduce a new network hop through PrimaryStore for
DataView requests. Gateway dispatches DataView methods directly to
`storage-view`.

Trusted service-to-service callers may call `trpc.moox.storage.PrimaryStore`
directly on the private network. This direct route is an internal contract, not
a public Gateway route.

## PrimaryStore

PrimaryStore replaces Access as the authoritative-data application service. It
continues to:

- validate TimeSeries and Record rows against metadata;
- resolve Dataset and object keys to a shard;
- convert public row models to internal shard row models;
- group batch writes by shard target;
- call local or remote DataShard services;
- merge, order, and page reads that span targets;
- create deterministic row-update events and preserve the Pebble/Outbox atomic
  write boundary;
- provide bounded scans to ViewBuilder and maintenance code through the
  internal `PrimaryStoreScan` service.

PrimaryStore does not own DuckDB, Bleve, or DataView query APIs. It does not
forward DataView calls.

The standard deployment names are:

```text
role:       primary
process:    storage-primary
binary:     moox-storage-primary
Go package: internal/service/primarystore
service:    trpc.moox.storage.PrimaryStore
scan:       trpc.moox.storage.PrimaryStoreScan
```

## DataShard

DataShard replaces the current physical PrimaryStore service. It owns one local
Pebble database and implements internal row write, read, scan, delete, and
Outbox operations. It trusts the new logical PrimaryStore to supply a resolved
`ShardTarget` but still validates target shape and Outbox ownership before
committing data.

DataShard is strictly internal:

- it has no Admin Gateway method mapping;
- it is absent from public service deployment defaults;
- its listener binds loopback in embedded mode or an explicitly configured
  private address in remote mode;
- firewall and deployment documentation permit only PrimaryStore and controlled
  maintenance access;
- its protobuf messages do not appear in public API documentation;
- no browser or ordinary external client calls it directly.

The remote deployment names are:

```text
role:       shard
process:    storage-shard
binary:     moox-storage-shard
Go package: internal/service/datashard
service:    trpc.moox.storage.DataShard
```

## Naming Migration

The rename is atomic and leaves no compatibility layer.

| Current | Target |
| --- | --- |
| `Access` | `PrimaryStore` |
| `AccessScan` | `PrimaryStoreScan` |
| `access` role | `primary` role |
| `storage-access` | `storage-primary` |
| `moox-storage-access` | `moox-storage-primary` |
| `internal/service/access` | `internal/service/primarystore` |
| `access_service_name` | `primary_store_service_name` |
| `access_scan_service_name` | `primary_store_scan_service_name` |
| old physical `PrimaryStore` | `DataShard` |
| old physical `primary` role | `shard` role |
| old physical `storage-primary` | `storage-shard` |
| `internal/service/primary` | `internal/service/datashard` |
| `PrimaryStoreNode` | `ShardNode` |
| `PrimaryStoreRoute` | `ShardRoute` |
| `PrimaryStoreTarget` | `ShardTarget` |
| `PrimaryStoreKey` | `ShardKey` |
| `PrimaryStoreRow` | `ShardRow` |
| `WritePrimaryRows` | `WriteShardRows` |
| `ReadPrimaryRows` | `ReadShardRows` |
| `ScanPrimaryRows` | `ScanShardRows` |
| `DeletePrimaryRows` | `DeleteShardRows` |
| `primary.service_name` | `shard.service_name` |
| public `storage_access` | public `storage` facade |
| `storage_access_trpc` | internal `storage_primary_trpc` |
| old physical `storage_primary_trpc` | removed from public defaults |

Generated protobuf files, CLI metadata seed types, schema column names, metrics,
health identities, deployment scripts, and active documentation follow the new
terminology. Historical plans under `docs/superpowers/plans/` remain historical
records and are excluded from old-name acceptance scans. This design's context
and migration table are the only current-documentation exceptions because they
must identify the symbols being replaced.

## Single-Host Deployment

The default personal deployment remains two processes:

```text
storage-primary  roles: [primary], shard.service_name: ""
storage-view     roles: [view]
```

An empty `shard.service_name` makes `storage-primary` create an embedded local
DataShard and own the configured Pebble directory. This keeps the operational
surface as small as it is today. `storage-primary` and `storage-view` each read
one `trpc_go.yaml` from their own deployment directory.

Only users who need more disk capacity deploy separate `storage-shard`
processes and configure PrimaryStore with a non-empty DataShard service name.

## Optional Multi-Machine Partitioning

Multi-machine mode is capacity partitioning, not high availability. Metadata
contains:

- `ShardNode`: one DataShard instance and private endpoint;
- `Device`: the active Pebble device on that node;
- `ShardRoute`: Dataset/object matching, priority, hash rule, and target node;
- `ShardTarget`: the resolved execution target passed to DataShard.

PrimaryStore preserves the existing route precedence and weighted rendezvous
selection. It groups writes by ShardTarget and caches one tRPC proxy per private
endpoint. Dataset scans enumerate all active targets.

The user-facing contract must state:

- each shard has one online copy;
- a failed DataShard makes its shard unavailable until the node returns or the
  operator restores data;
- adding, removing, or reweighting nodes does not migrate existing rows;
- changing a hash pool may change key placement, so operators must migrate data
  manually before activating the new route set;
- a request spanning shards may partially succeed and does not provide a global
  transaction;
- Pebble backups and Archive/Parquet/COS remain the operator's data-protection
  mechanisms.

The standard deployment command does not pretend to automate this topology.
Current documentation gives an explicit advanced procedure and marks it as
manual operation.

## Data Flows

### Authoritative Write

```text
Storage facade
  -> PrimaryStore validates rows
  -> ShardRouter resolves each key
  -> PrimaryStore groups rows by ShardTarget
  -> DataShard writes rows and Outbox message in one Pebble batch
  -> Outbox relay publishes the update event
```

PrimaryStore returns partial written keys when an earlier shard committed and a
later shard failed. It does not retry non-idempotent public writes at the
Gateway.

### Authoritative Read

```text
Storage facade
  -> PrimaryStore resolves keys or dataset targets
  -> DataShard reads the selected Pebble shard
  -> PrimaryStore merges, orders, and pages cross-shard results
```

### Derived Query

```text
Storage facade
  -> DataView
  -> active ViewIndex
  -> DuckDB or Bleve
```

This path does not traverse PrimaryStore. ViewBuilder separately consumes row
events and uses PrimaryStore/PrimaryStoreScan to reload authoritative rows.

## Security And Service Governance

The Gateway applies one public Storage policy for authentication, Space
authorization, rate limits, request-size limits, and audit records. Audit data
records the public Storage method and selected internal backend without
exposing credentials or row payloads.

PrimaryStore, PrimaryStoreScan, DataShard, and ViewIndex use private service
identities. Gateway and SysDeploy validation reject public mappings for
`PrimaryStoreScan`, DataShard, and ViewIndex. Health and metrics endpoints
remain separate operational surfaces and require the existing health
authentication.

Resource policies differ by method category. Writes, bounded authoritative
reads, View queries, and internal scans must not share one undifferentiated
limit merely because they use one public Storage path.

## Error Handling

- Gateway authentication, authorization, routing, and request-limit failures
  use the common Gateway error contract.
- Backend business responses preserve their existing `ret_info` semantics.
- Unknown public Storage methods fail before backend selection.
- Missing, inactive, or invalid shard routes return route errors from
  PrimaryStore.
- Shard transport failures identify the affected internal node in logs and
  metrics but do not disclose private endpoints to public callers.
- DataView unavailability does not make authoritative PrimaryStore reads and
  writes unavailable, and PrimaryStore unavailability does not stop an already
  healthy DataView from serving its current derived index.

## Documentation

Update the active Storage architecture, deployment guide, Storage README,
custom setup guide, service deployment examples, and metadata seed examples.
Documentation must distinguish:

- one public Storage facade from internal runtime services;
- authoritative PrimaryStore data from derived DataView indexes;
- logical PrimaryStore routing from physical DataShard persistence;
- standard embedded single-host mode from manual multi-machine partitioning;
- backups and Archive from unsupported replication and failover.

## Verification

Acceptance requires:

1. The public frontend and HTTP clients use only `/api/admin/storage/{method}`.
2. The Gateway method registry maps every public Storage method to exactly one
   internal backend and excludes internal RPCs.
3. `PrimaryStore` and `DataView` remain independent backends; View requests do
   not pass through PrimaryStore.
4. DataShard and PrimaryStoreScan cannot be reached through public Gateway
   routes.
5. Standard deployment starts `storage-primary` and `storage-view`, embeds a
   local DataShard, and gives each process exactly one runtime YAML file.
6. A focused multi-shard test proves deterministic routing, target grouping,
   endpoint-specific proxy selection, cross-shard reads, and documented partial
   success behavior.
7. Active source, configuration, schemas, scripts, generated code, tests, and
   current documentation contain no obsolete Access symbols or old physical
   PrimaryStore symbols such as `WritePrimaryRows`, `PrimaryStoreNode`, and
   `primary.service_name`, except this design's migration references and
   historical plans excluded by the acceptance scan.
8. All affected Go modules, frontend tests, deployment contracts, documentation
   builds, package boundaries, formatting checks, and `make verify` pass.
9. The worktree is clean after commit and push, and `HEAD` equals
   `origin/main`.
