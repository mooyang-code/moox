# MooX Greenfield Dead-Code Cleanup Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Remove MooX compatibility-only protocols, schema upgrades, runtime fallbacks, and proven dead code so the current greenfield design is the only supported contract.

**Architecture:** Work from contracts inward. Add a repository guard first, simplify Protobuf and canonical schemas, migrate live callers off compatibility adapters, then remove unreachable code confirmed across the whole Go workspace and frontend route graph. Regenerate all generated code and verify every touched module before an independent review and exact-SHA push.

**Tech Stack:** Go 1.25 workspace, tRPC-Go, Protobuf, SQLite/GORM, NATS JetStream, Python 3 worker protocol, Vue 3/TypeScript/Vitest, Bash.

---

## File Map

- `scripts/test-greenfield-contract.sh`: repository policy check for protocol reservations and explicitly retired compatibility symbols.
- `Makefile`: exposes the policy check and includes it in `verify-pr`.
- `modules/{admin,collector,storage}/proto/*.proto`: compact greenfield wire contracts.
- `modules/{admin,collector,storage}/proto/*gen`: regenerated Go and tRPC bindings.
- `modules/{admin,collector,factor,monitor,trade}/schema/*.sql`: canonical fresh-install DDL without retired-table cleanup.
- `modules/{admin,collector,monitor,trade}/internal/**`: removes database and request compatibility paths.
- `packages/{jetstream,pyruntime,security}/**`: removes shared compatibility APIs and aliases.
- `modules/{cloudnode,factor,storage,archive,collector,monitor,cli}/**`: migrates live callers to current contracts.
- `web/src/**`: removes old value normalization and hidden route aliases.
- module-local `*_test.go`, Python tests, and Vitest files: assert only the surviving contracts.
- active README and architecture files: describe current commands and APIs only.

## Task 1: Add a Failing Greenfield Contract Gate

**Files:**
- Create: `scripts/test-greenfield-contract.sh`
- Modify: `Makefile`
- Test: `scripts/test-greenfield-contract.sh`

- [ ] **Step 1: Add the policy script**

Create an executable script with this contract:

```bash
#!/usr/bin/env bash
set -euo pipefail

cd "$(git rev-parse --show-toplevel)"

failures=0
check_absent() {
  local label=$1
  shift
  if matches=$(rg -n "$@" 2>/dev/null); then
    printf '%s\n%s\n' "$label" "$matches" >&2
    failures=1
  fi
}

check_absent "protocol reserved declarations are forbidden" \
  '^[[:space:]]*reserved([[:space:]]|$)' \
  --glob '*.proto' --glob '*.thrift' modules packages
check_absent "deprecated protocol fields are forbidden" \
  '\[[[:space:]]*deprecated[[:space:]]*=[[:space:]]*true[[:space:]]*\]' \
  --glob '*.proto' --glob '*.thrift' modules packages
check_absent "retired compatibility symbols remain" \
  'migrateTaskInstanceSchema|DropLegacyHostSampleTables|EnsureMetricRuleStateColumns|LegacyKVEntry|LEGACY_PACKAGE_TYPE|legacyKey\(|releaseLiveGate|FRAME_READY[[:space:]]*=' \
  --glob '*.go' --glob '*.py' --glob '*.ts' --glob '!**/*_test.go' modules packages web
check_absent "retired Collector callback fields remain" \
  'ServerIP|ServerPort|server_ip|server_port' \
  --glob '*.go' modules/collector
check_absent "retired schema cleanup remains" \
  'DROP TABLE IF EXISTS (t_user_actions|t_collector_execution_logs|t_factor_runs)' \
  --glob '*.sql' modules

exit "$failures"
```

- [ ] **Step 2: Wire the gate into repository verification**

Add `test-greenfield-contract` to `.PHONY`, add:

```make
test-greenfield-contract:
	bash scripts/test-greenfield-contract.sh
```

and make `verify-pr` depend on `test-greenfield-contract`.

- [ ] **Step 3: Run the gate and verify RED**

Run:

```bash
bash scripts/test-greenfield-contract.sh
```

Expected: non-zero exit with current `reserved` declarations and compatibility symbols listed. A syntax error or missing-file error is not the expected failure.

## Task 2: Simplify and Regenerate Protocol Contracts

**Files:**
- Modify: `modules/collector/proto/collector.proto`
- Modify: `modules/storage/proto/common.proto`
- Modify: `modules/storage/proto/view_index.proto`
- Modify: `modules/storage/proto/metadata.proto`
- Modify: `modules/storage/proto/metadata_proto_test.go`
- Modify: `modules/admin/proto/sysdeploy_service.proto`
- Modify: `modules/admin/proto/secret_service.proto`
- Modify: `modules/admin/proto/admingen/security_test.go`
- Regenerate: `modules/collector/proto/collectorgen/**`
- Regenerate: `modules/storage/proto/storagegen/**`
- Regenerate: `modules/admin/proto/admingen/**`

- [ ] **Step 1: Change descriptor tests to the greenfield tags**

Update reflection tests before editing protocol sources. The assertions must require:

```go
assertFields(t, file.Messages().ByName("Device"), map[string]protoreflect.FieldNumber{
	"device_id": 1, "name": 2, "engine": 3, "endpoint": 4,
	"config_json": 5, "status": 6, "created_at": 7, "updated_at": 8,
})
assertFields(t, file.Messages().ByName("ListDevicesReq"), map[string]protoreflect.FieldNumber{
	"auth_info": 1, "engine": 2, "page": 3,
})
```

Remove all test expectations for reserved ranges or reserved names. Rename the secret test so it asserts the current contiguous `RevealedSecret` tags, beginning with `secret_id = 1`.

- [ ] **Step 2: Run descriptor tests and verify RED**

Run:

```bash
(cd modules/storage && go test ./proto -run 'TestMetadataProtoCleanBreakContract' -count=1)
(cd modules/admin && go test ./proto/admingen -run 'TestRevealedSecret' -count=1)
```

Expected: failures report old field numbers or reserved ranges.

- [ ] **Step 3: Delete every reservation and compact field numbers**

Apply these exact tag changes:

```protobuf
// collector TaskInstance
string last_exec_node = 12;
TaskInstanceStatus last_exec_status = 13;
string last_exec_time = 14;
google.protobuf.Struct result = 15;
bool is_deleted = 16;
string create_time = 17;
string modify_time = 18;
string cloud_job_item_id = 19;

// collector TaskInstanceFilter
string last_exec_node = 12;
bool include_deleted = 13;
common.Page page = 14;

// storage ViewIndexStats
string updated_at = 6;
uint64 free_disk_bytes = 7;
uint64 view_version = 8;
string indexed_from = 9;
string indexed_to = 10;
```

Also compact `Device`, `ListDevicesReq`, the three SysDeploy request messages, and `RevealedSecret`. Remove the trailing reservations from `FilterSpec` and `DataKind`.

Keep `DatasetColumn.required`, `is_unique`, and `aliases` because current import and UI flows use them. Remove their `[deprecated = true]` annotations and replace the compatibility comments with their current semantics.

- [ ] **Step 4: Regenerate affected bindings**

Run:

```bash
make -C modules/collector/proto all
make -C modules/storage proto
make -C modules/admin/proto all
```

Expected: exit 0 and generated descriptors contain the new tags.

- [ ] **Step 5: Run protocol and consumer tests**

Run:

```bash
(cd modules/collector && go test ./proto/... ./internal/rpc/... -count=1)
(cd modules/storage && go test ./proto ./internal/service/metadata/... -count=1)
(cd modules/admin && go test ./proto/admingen ./internal/service/sysdeploy/... -count=1)
```

Expected: PASS.

## Task 3: Remove Old-Database Upgrade and Cleanup Paths

**Files:**
- Delete: `modules/collector/internal/store/schema_migration.go`
- Modify: `modules/collector/internal/store/database.go`
- Modify: `modules/collector/internal/store/database_test.go`
- Modify: `modules/collector/cmd/cli/init_schema_test.go`
- Delete: `modules/monitor/internal/store/migrations.go`
- Delete: `modules/monitor/cmd/cli/cleanup_host_samples.go`
- Modify: `modules/monitor/cmd/cli/main.go`
- Modify: `modules/monitor/cmd/cli/init_schema_test.go`
- Modify: `modules/monitor/internal/bootstrap/bootstrap.go`
- Modify: `modules/admin/internal/service/sysdeploy/dao.go`
- Modify: `modules/admin/internal/service/sysdeploy/{defaults_test.go,acceptance_test.go}`
- Modify: `modules/admin/test/admin_host_monitor_cleanup_e2e_test.go`
- Modify: `modules/trade/internal/infra/store/store.go`
- Modify: `modules/{admin,collector,factor}/schema/*.sql`
- Modify: `modules/{admin,collector,factor}/schema/schema_test.go`

- [ ] **Step 1: Change schema tests to assert canonical creation only**

Delete tests that create retired tables or columns and expect startup to drop or migrate them. Keep tests that load each complete schema into an empty SQLite database. Add assertions that canonical SQL does not contain:

```go
for _, forbidden := range []string{
	"DROP TABLE IF EXISTS t_user_actions",
	"DROP TABLE IF EXISTS t_collector_execution_logs",
	"DROP TABLE IF EXISTS t_factor_runs",
	"ALTER TABLE t_collector_task_instances",
} {
	if strings.Contains(sql, forbidden) {
		t.Fatalf("canonical schema contains upgrade statement %q", forbidden)
	}
}
```

- [ ] **Step 2: Run schema tests and verify RED**

Run:

```bash
(cd modules/admin && go test ./schema ./internal/service/sysdeploy ./test -count=1)
(cd modules/collector && go test ./schema ./internal/store ./cmd/cli -count=1)
(cd modules/factor && go test ./schema -count=1)
(cd modules/monitor && go test ./internal/store ./cmd/cli -count=1)
(cd modules/trade && go test ./internal/infra/store -count=1)
```

Expected: failures identify the upgrade and cleanup paths that still exist.

- [ ] **Step 3: Delete schema upgrades**

Make `collector.Store.ApplySchema` execute only validated canonical SQL:

```go
func (s *Store) ApplySchema(sql string) error {
	if s == nil || s.db == nil {
		return fmt.Errorf("collector database is not open")
	}
	if strings.TrimSpace(sql) == "" {
		return fmt.Errorf("collector schema sql is empty")
	}
	return s.db.Exec(sql).Error
}
```

Remove Monitor's metric-column upgrade, legacy-table cleanup command, and their bootstrap calls. Remove Trade's `HasColumn`/`ALTER TABLE` block. Remove the three retired-table `DROP TABLE` statements from canonical SQL.

- [ ] **Step 4: Make Admin seeding current-state only**

Delete `retireLegacyAdminMonitor`, `retireSplitViewDeployments`, deletion of old gateway rows, and `migrateUnifiedStorageViewHealth`. `SeedDefaults` must create missing current rows and update the current row's defaults without recognizing retired service names or old health URLs.

Keep a simple current-default merge:

```go
next, changed := mergeDefaultExtraConfig(row.ExtraConfig, item.ExtraConfig)
if changed {
	updates["c_extra_config"] = next
}
```

Delete tests whose setup consists of seeding an obsolete row. Preserve tests for idempotent current defaults.

- [ ] **Step 5: Run schema tests and verify GREEN**

Repeat the commands from Step 2.

Expected: PASS.

- [ ] **Step 6: Commit protocol and schema cleanup**

Run:

```bash
git add Makefile scripts/test-greenfield-contract.sh \
  modules/admin modules/collector modules/factor modules/monitor modules/storage/proto modules/trade
git diff --cached --check
git commit -m "refactor(protocol): remove compatibility contracts"
```

Do not stage the pre-existing EventBus firewall files with this commit.

## Task 4: Remove Shared Runtime Compatibility APIs

**Files:**
- Modify: `packages/security/crypto.go`
- Modify: `packages/security/crypto_test.go`
- Modify: `packages/jetstream/keyvalue.go`
- Modify: `packages/jetstream/keyvalue_test.go`
- Modify: `modules/cloudnode/internal/jobqueue/jetstream_client.go`
- Modify: `modules/cloudnode/internal/jobstate/kv_store.go`
- Modify: `modules/cloudnode/internal/jobstate/kv_store_test.go`
- Modify: `modules/cloudnode/internal/bootstrap/bootstrap.go`
- Modify: `packages/pyruntime/python/moox_pyruntime/protocol.py`
- Modify: `packages/pyruntime/python/tests/test_protocol.py`
- Modify: `modules/factor/pyworker/codec.py`
- Modify: `modules/factor/pyworker/worker.py`
- Modify: `modules/factor/pyworker/test_worker.py`

- [ ] **Step 1: Replace compatibility tests with current-only rejection tests**

For encryption, keep only the SHA-256-derived AES key contract. A payload encrypted with the removed key normalization must fail:

```go
func TestDecryptRejectsNonCanonicalKeyDerivation(t *testing.T) {
	payload := encryptWithTestKey(t, legacyTestKey("0123456789abcdef"), "old")
	if _, err := Decrypt(payload, "0123456789abcdef"); err == nil {
		t.Fatal("Decrypt accepted a noncanonical key derivation")
	}
}
```

For Python, import and assert only `TYPE_*` constants. Add a worker test that removes `moox_pyruntime` from `PYTHONPATH` and expects startup to fail rather than use a local frame implementation.

- [ ] **Step 2: Run focused tests and verify RED**

Run:

```bash
(cd packages/security && go test ./... -count=1)
(cd packages/pyruntime && python3 -m pytest python/tests -q)
(cd modules/factor && python3 -m pytest pyworker -q)
```

Expected: the new rejection tests fail while compatibility fallbacks remain.

- [ ] **Step 3: Delete crypto and Python fallbacks**

Make `Decrypt` return the canonical decryption error directly:

```go
plaintext, err := decryptWithKey(data, deriveKey(secret))
if err != nil {
	return "", fmt.Errorf("decrypt ciphertext: %w", err)
}
return plaintext, nil
```

Delete `legacyKey`, `FRAME_*` aliases in the shared Python package, Factor's local framing implementation and `ImportError` fallback, and the Arrow-to-JSON exception fallback. Factor must import canonical `TYPE_*`, `read_frame`, and `write_frame` from `moox_pyruntime`.

- [ ] **Step 4: Move CloudNode to the current context-aware KV API**

Delete `LegacyKVEntry`, `KeyValue`, `keyValueAdapter`, `Client.KeyValue`, and `Client.CreateKeyValue`. Change CloudNode's runtime method to:

```go
func (r *Runtime) BindKV(ctx context.Context, bucket string) (jetstream.KVStore, error) {
	if r == nil || r.client == nil {
		return nil, fmt.Errorf("jetstream runtime is not initialized")
	}
	return r.client.BindKV(ctx, bucket)
}
```

Change `jobstate.KVStore.kv` and `NewKVStore` to `jetstream.KVStore`. Pass `ctx` to `Create`, `Get`, `Update`, and `Keys`, and update the in-memory test store to implement the current interface.

- [ ] **Step 5: Run shared and CloudNode tests**

Run:

```bash
(cd packages/jetstream && go test ./... -count=1)
(cd packages/security && go test ./... -count=1)
(cd packages/pyruntime && go test ./... -count=1 && python3 -m pytest python/tests -q)
(cd modules/cloudnode && go test ./internal/jobqueue ./internal/jobstate ./internal/bootstrap -count=1)
(cd modules/factor && python3 -m pytest pyworker -q)
```

Expected: PASS.

## Task 5: Remove Module-Level Compatibility Wrappers and Payload Fields

**Files:**
- Modify: `modules/collector/internal/model/types.go`
- Modify: `modules/collector/internal/reporter/heartbeat.go`
- Modify: `modules/collector/internal/serverless/handler.go`
- Modify: `modules/collector/cmd/scf/main.go`
- Modify: Collector tests referencing `server_ip` or `server_port`
- Modify: `modules/factor/internal/scheduler/service.go`
- Modify: `modules/factor/internal/rpc/recalc.go`
- Modify: Factor scheduler and E2E tests
- Modify: `modules/factor/internal/trigger/event_batcher.go`
- Modify: Factor trigger tests
- Modify: `modules/monitor/internal/hostmetrics/hostmetrics.go`
- Modify: `modules/monitor/internal/bootstrap/bootstrap.go`
- Modify: `modules/monitor/internal/rpc/host.go`
- Modify: Monitor host-metric tests
- Modify: `modules/admin/internal/service/auth/impl/user.go`
- Modify: `modules/admin/internal/service/auth/impl/user_test.go`

- [ ] **Step 1: Change tests to require the current APIs**

Collector events must serialize `service_gateway_target` and never
`server_ip`/`server_port`. Factor callers must handle `EnqueueChecked` and
`FlushPending` errors. Monitor must construct host stores only with
`NewStoreWithWriterReader` and must not call no-op `EnsureSchema`. Admin
`GetUserInfo` must reject a request body token when authenticated context is
missing.

- [ ] **Step 2: Run focused tests and verify RED**

Run:

```bash
(cd modules/collector && go test ./internal/reporter ./internal/serverless ./cmd/scf -count=1)
(cd modules/factor && go test ./internal/scheduler ./internal/rpc ./internal/trigger ./test -count=1)
(cd modules/monitor && go test ./internal/hostmetrics ./internal/bootstrap ./internal/rpc -count=1)
(cd modules/admin && go test ./internal/service/auth/impl -count=1)
```

Expected: failures identify the old wrappers and fields.

- [ ] **Step 3: Delete Collector callback compatibility**

Remove `ServerIP` and `ServerPort` from `CloudFunctionEvent` and related probe
models. Keep only `ServiceGatewayTarget`; missing target follows one clear
error or warning path. Delete parsing that derives the removed fields and
remove `SERVER_IP`/`SERVER_PORT` environment fallback from `cmd/scf`.

- [ ] **Step 4: Delete Factor wrappers**

Delete `Service.Enqueue` and change every call to:

```go
if err := service.EnqueueChecked(ctx, task); err != nil {
	return err
}
```

Delete `EventBatcher.Flush`; tests and replay code call
`FlushPending(context.Background(), now)` and assert the returned error.

- [ ] **Step 5: Delete Monitor no-op constructors and Admin token fallback**

Keep one Monitor constructor:

```go
func NewStore(writer SnapshotWriter, reader HistoryReader) *Store {
	return &Store{writer: writer, reader: reader}
}
```

Delete `EnsureSchema` and its callers. In Admin, `getUserInfoCaller` reads only
the authenticated context; remove JWT parsing of `req.access_token`.

- [ ] **Step 6: Run focused tests and verify GREEN**

Repeat Step 2.

Expected: PASS.

## Task 6: Remove Storage Write and Row-Projection Compatibility

**Files:**
- Modify: `modules/storage/proto/rows.proto`
- Modify: `modules/storage/proto/primary_store.proto`
- Modify: `modules/storage/proto/metadata_proto_test.go`
- Regenerate: `modules/storage/proto/storagegen/**`
- Delete: `modules/storage/internal/service/primarystore/merge.go`
- Modify: `modules/storage/internal/service/primarystore/rows_read.go`
- Modify: `modules/storage/internal/service/primarystore/service_test.go`
- Modify: `modules/collector/internal/sources/binance/storage_rpc.go`
- Modify: `modules/factor/internal/storageio/{client.go,writeback.go,dataframe.go}`
- Modify: `modules/monitor/internal/{hostmetrics/storage_writer.go,metrics/storage.go}`
- Modify: `modules/cli/internal/command/storage_import.go`
- Modify: `modules/archive/internal/backfill/backfill.go`
- Modify: affected tests in the same packages

- [ ] **Step 1: Add a failing canonical Storage protocol assertion**

Extend the Storage reflection test:

```go
primary := storagepb.File_primary_store_proto.Services().ByName("PrimaryStore")
for _, removed := range []protoreflect.Name{"MergeTimeSeriesRows", "MergeRecordRows"} {
	if primary.Methods().ByName(removed) != nil {
		t.Fatalf("compatibility RPC %q remains", removed)
	}
}
for _, name := range []protoreflect.Name{"TimeSeriesRow", "RecordRow"} {
	if storagepb.File_rows_proto.Messages().ByName(name).Fields().ByName("columns") != nil {
		t.Fatalf("%s.columns remains", name)
	}
}
```

- [ ] **Step 2: Run the protocol test and verify RED**

Run:

```bash
(cd modules/storage && go test ./proto -run 'TestStorageRuntimeOnlySupportsFieldUpserts' -count=1)
```

Expected: failure lists the merge RPCs or `columns`.

- [ ] **Step 3: Make field upserts the only write contract**

Delete both `Merge*` RPCs and their request/response messages. Remove
`TimeSeriesRow.columns` and `RecordRow.columns`; compact `attributes` to field
3. Keep `ReadTimeSeriesRows` and `ReadRecordRows` as current range-query
contracts because they provide real behavior not covered by point
`ReadFields`.

Regenerate Storage bindings:

```bash
make -C modules/storage proto
```

- [ ] **Step 4: Migrate every writer to `PrimaryUpsertFieldsReq`**

Convert each row into a canonical `RowFieldUpsert`:

```go
write := &storagepb.RowFieldUpsert{
	Key: &storagepb.RowKey{
		SpaceId: row.GetKey().GetSpaceId(),
		DatasetId: row.GetKey().GetDatasetId(),
		Kind: &storagepb.RowKey_TimeSeries{TimeSeries: &storagepb.TimeSeriesRowKey{
			SubjectId: row.GetKey().GetSubjectId(),
			Freq: row.GetKey().GetFreq(),
			DataTime: row.GetKey().GetDataTime(),
		}},
	},
	Fields: row.GetFields(),
	Operation: storagepb.RowFieldOperation_ROW_FIELD_OPERATION_UPSERT,
}
```

Collector, Factor, Monitor, and CLI writers call `UpsertFields`. Remove
string-written-key conversion helpers.

- [ ] **Step 5: Remove read projections**

`ReadTimeSeriesRows` and `ReadRecordRows` return `fields` unchanged. Delete
`columnsFromFields`. Update Archive and Factor readers to build their local
column maps from `FieldValue.field_id` and `TypedValue`.

- [ ] **Step 6: Run cross-module Storage tests**

Run:

```bash
(cd modules/storage && CGO_ENABLED=1 go test ./proto ./internal/service/primarystore/... ./internal/service/view/... -count=1)
(cd modules/collector && go test ./internal/sources/binance/... -count=1)
(cd modules/factor && go test ./internal/storageio/... -count=1)
(cd modules/monitor && go test ./internal/hostmetrics/... ./internal/metrics/... -count=1)
(cd modules/archive && go test ./internal/backfill/... -count=1)
(cd modules/cli && go test ./internal/command/... -count=1)
```

Expected: PASS.

## Task 7: Remove Frontend Compatibility Values and Hidden Route Aliases

**Files:**
- Modify: `web/src/api/function-package.ts`
- Modify: `web/src/views/collector/cloud-node/cloud-node-model.ts`
- Modify: `web/src/views/collector/cloud-node/cloud-node-model.test.ts`
- Modify: `web/src/router/route.ts`
- Modify: route contract tests under `web/src/views/**`
- Modify: `web/scripts/check-menu-structure.mjs`
- Modify: `web/scripts/check-metric-monitor.mjs`

- [ ] **Step 1: Change tests to accept only canonical enum values and routes**

`resolvePackageType` accepts positive numbers and canonical
`PACKAGE_TYPE_*` strings only. Node status labels accept canonical enum names
or numeric enum values, not lowercase old strings. Hidden routes such as
`/ops/service-monitor` and `/ops/metric-monitor` must be absent rather than
redirected.

- [ ] **Step 2: Run frontend tests and verify RED**

Run:

```bash
pnpm --dir web test -- --run \
  src/views/collector/cloud-node/cloud-node-model.test.ts \
  src/views/collector/data-management/data-management.test.ts \
  src/views/ops/storage/storage-management.test.ts
node web/scripts/check-menu-structure.mjs
node web/scripts/check-metric-monitor.mjs
```

Expected: failures identify accepted old values or retained alias routes.

- [ ] **Step 3: Delete compatibility maps and routes**

Delete `LEGACY_PACKAGE_TYPE`, lowercase status/package mappings, and hidden
redirect-only routes. Keep the current `/collector/data-management` route
because it is the active unified workbench, not an alias. Remove only routes
whose sole purpose is forwarding an old URL.

- [ ] **Step 4: Run frontend verification**

Run:

```bash
pnpm --dir web test
pnpm --dir web run lint:eslint:check
pnpm --dir web run lint:prettier:check
pnpm --dir web run build:prod
```

Expected: PASS.

## Task 8: Remove Proven Unreachable Go Code

**Files:**
- Delete or modify the exact files reported by `deadcode` in application modules.
- Modify tests only when they test a removed unreachable helper rather than an entry point.

- [ ] **Step 1: Capture the workspace dead-code report**

Run each non-generated workspace module:

```bash
go work edit -json | jq -r '.Use[].DiskPath' |
  rg -v '/proto/[^/]+gen$' |
  while read -r module; do
    printf '### %s\n' "$module"
    (cd "$module" && deadcode -test ./...)
  done
```

Expected baseline includes the already confirmed application-module
candidates:

```text
modules/admin: HTTPRouter.handleHealthCheck
modules/archive: mainContext, registry.NewClient
modules/cli: loadGlobalConfig
modules/collector: NewSignedRequest, ReportTaskStatusAsync,
  KlineCollector.reportKlines, KlineCollector.sendTimeSeriesRowsWithRetry,
  RecentTrade.ToTrade
modules/eventbus: Registry.Config, Registry.ValidateTopic
modules/factor: NewRuntimeExecutor, RuntimeExecutor.Execute, NewWorkerPool,
  storageio.NewClient
modules/monitor: Consumer.handleDelivery, sortedSeries, MarshalSnapshot,
  snapshotTime, metricRuleToJSON, sortConditions
modules/storage: LoadConfigStrict, isNamespaceKey, sameRowPrefix,
  decodeProcessedEventTimestamp, buildLeaseTTL, pageItems, stringSet,
  containsString, waitForLiveIdle, processDelivery, firstHeartbeat,
  releaseLiveGate, DuckDBPath, BlevePath, bleve.OpenExisting
modules/strategy: Relay.LastError, Relay.now, Runtime.LastError, NewObserve,
  Observe.Submit, Observe.Inspect, Paper.Positions, Paper.Inspect,
  Service.Results, Snapshot.Revision, Snapshot.Cutoff
modules/trade: WithOperator, operatorFromContext, TraceFromContext
web-host: newHTTPHandler
```

- [ ] **Step 2: Prove each candidate has no dynamic entry point**

For each reported symbol, search the full workspace, generated service
descriptors, command registration, build tags, and config constructors. A
symbol may be deleted only when its declaration is the only production
reference. Package-module reports are not enough by themselves because
another Go module may import an exported symbol.

Run:

```bash
rg -n 'handleHealthCheck|mainContext|NewSignedRequest|ReportTaskStatusAsync|reportKlines|sendTimeSeriesRowsWithRetry|ToTrade|LoadConfigStrict|releaseLiveGate|WithOperator|TraceFromContext|newHTTPHandler' \
  modules packages web-host --glob '*.go'
```

Expected: declarations plus tests only for confirmed deletions.

- [ ] **Step 3: Delete confirmed application dead code**

Delete the candidates that pass Step 2. Delete whole files when every
production declaration in the file is unreachable. Remove associated tests
that exercise only the deleted helper.

- [ ] **Step 4: Audit shared-package reports across the whole workspace**

Cross-check reports from `packages/cloudprovider`, `packages/cloudruntime`,
`packages/doctor`, `packages/events`, `packages/gatewayauth`,
`packages/healthz`, `packages/jetstream`, `packages/pyruntime`, and
`packages/report`. Keep exports with any live cross-module caller. Remove
unexported unreachable helpers and exports with no caller anywhere.

- [ ] **Step 5: Run `deadcode`, compilation, and static analysis again**

Run:

```bash
./scripts/test-go-workspace.sh
staticcheck ./...
```

Run `staticcheck ./...` separately from every path returned by
`go work edit -json`; record any module for which the root command is not
valid. Re-run the `deadcode` loop. Expected: no confirmed application dead
code remains; any remaining library export has a documented cross-module or
dynamic entry point.

## Task 9: Finish Active Documentation, Dependencies, and Policy Gate

**Files:**
- Modify: `modules/storage/README.md`
- Modify: other active README or architecture files that name removed APIs
- Modify: affected `go.mod`/`go.sum` files after deleting whole dependencies
- Modify: `scripts/test-greenfield-contract.sh`

- [ ] **Step 1: Remove stale active documentation**

Delete the obsolete `go build -tags legacy_storage ./...` command and any
active documentation that instructs callers to use removed merge RPCs,
callback fields, cleanup commands, or hidden routes. Do not edit historical
files under `docs/superpowers/`.

- [ ] **Step 2: Tidy only touched Go modules**

Run `go mod tidy` in each module whose imports changed. Inspect every
`go.mod`/`go.sum` diff and keep only dependency changes caused by this cleanup.

- [ ] **Step 3: Run the greenfield gate and verify GREEN**

Run:

```bash
bash scripts/test-greenfield-contract.sh
```

Expected: exit 0 with no output.

- [ ] **Step 4: Format and check the full diff**

Run:

```bash
gofmt -w $(git diff --name-only --diff-filter=ACM -- '*.go')
pnpm --dir web run lint:prettier
git diff --check
```

Expected: exit 0.

- [ ] **Step 5: Commit runtime and dead-code cleanup**

Run:

```bash
git add modules packages web web-host scripts Makefile
git diff --cached --check
git commit -m "refactor: remove obsolete runtime paths"
```

Exclude unrelated pre-existing user edits only if they are still being
modified concurrently.

## Task 10: Run the Full Proving Set

**Files:**
- No source changes unless verification reveals a defect.

- [ ] **Step 1: Verify generated sources**

Run:

```bash
make proto
git status --short
```

Expected: generation creates no new diff beyond the intended committed
protocol changes.

- [ ] **Step 2: Run focused race and cross-module tests**

Run:

```bash
(cd modules/storage && CGO_ENABLED=1 go test -race ./internal/service/primarystore/... ./internal/service/view/... -count=1)
(cd modules/factor && go test -race ./internal/scheduler ./internal/trigger ./internal/storageio -count=1)
(cd modules/monitor && go test -race ./internal/hostmetrics ./internal/metrics -count=1)
(cd modules/cloudnode && go test -race ./internal/jobstate ./internal/jobqueue -count=1)
```

Expected: PASS.

- [ ] **Step 3: Run repository verification**

Run:

```bash
make verify-pr
make check-boundaries
make test
make check-format
make check-lint
make test-quality-gates
make test-docs-architecture
```

Expected: PASS.

- [ ] **Step 4: Verify existing user changes**

The initial worktree also contained `AGENTS.md`,
`modules/cli/internal/command/setup.go`, and Tencent Lighthouse changes.
Re-read their current diff. If still uncommitted, run:

```bash
(cd packages/cloudprovider && go test ./tencent -count=1)
(cd modules/cli && go test ./internal/command -count=1)
```

Commit them in a separate conventional commit only after their tests pass, so
the session ends with no uncommitted file.

## Task 11: Start a New Agent for Independent Code Review

**Files:**
- Review the full diff from `8dd575cb` through current `HEAD`.
- Modify only files required by confirmed findings.

- [ ] **Step 1: Dispatch the required fresh review agent**

Give the new agent this bounded review prompt:

```text
Independently review the completed MooX greenfield dead-code cleanup from
8dd575cb to current HEAD. Do not modify files. Verify protocol tag compaction,
generated-code freshness, canonical fresh-only schemas, removal of runtime
compatibility paths, Storage caller migrations, dynamic Go entry points,
frontend route/value removals, tests, and unrelated user changes. Report
findings first, ordered by severity, with exact file and line references.
Confirm every claimed dead symbol against the complete multi-module workspace.
```

- [ ] **Step 2: Verify the agent's findings locally**

Do not accept the review summary on trust. Reproduce every finding against the
current worktree, current generated descriptors, and failing tests.

- [ ] **Step 3: Fix every confirmed finding and rerun affected tests**

Use a failing regression test before each behavioral fix. Rerun the smallest
proving set for the changed surface, followed by `make verify-pr`.

- [ ] **Step 4: Commit review fixes**

If fixes exist:

```bash
git add -A
git diff --cached --check
git commit -m "fix: address greenfield cleanup review"
```

If the review has no confirmed finding, do not create an empty commit.

## Task 12: Final Audit, Commit, Push, and Exact-SHA Proof

**Files:**
- All files changed, added, or deleted during the session.

- [ ] **Step 1: Audit every completion criterion**

Run:

```bash
bash scripts/test-greenfield-contract.sh
rg -n '^[[:space:]]*reserved([[:space:]]|$)' \
  --glob '*.proto' --glob '*.thrift' modules packages
rg -n '\[[[:space:]]*deprecated[[:space:]]*=[[:space:]]*true' \
  --glob '*.proto' --glob '*.thrift' modules packages
git diff --check
git status --short
```

Expected: both `rg` commands return no matches, diff check passes, and no
uncommitted session file remains after the final commit.

- [ ] **Step 2: Push the feature branch**

Run:

```bash
git push origin feature/mooyang
```

Expected: success.

- [ ] **Step 3: Verify local and remote SHA equality**

Run:

```bash
local_sha=$(git rev-parse HEAD)
remote_sha=$(git ls-remote --heads origin feature/mooyang | awk '{print $1}')
test "$local_sha" = "$remote_sha"
git status --short --branch
```

Expected: the SHA comparison exits 0 and the worktree is clean.
