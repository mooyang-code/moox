# Remove Collector Planned Execution Node Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Default the task-instance page to 20 rows and remove the unused planned execution node from UI, protocol, RPC, domain, store, SQLite schema, production data, and documentation.

**Architecture:** Remove the field at every compile-time boundary, reserve its protobuf identity, and add a pre-schema idempotent SQLite migration for existing databases. Deploy Collector first so the API and database converge, then deploy the embedded frontend and verify the resulting runtime with database, API, and browser evidence.

**Tech Stack:** Vue 3, TypeScript, Arco Design Vue, Go 1.24, protobuf/tRPC, GORM, SQLite, pnpm, Vitest, Vite, embedded Go `web-host`.

---

## File Map

- Create `scripts/check-collector-planned-node-removal.mjs`: repository-wide removal contract.
- Modify `web/src/views/collector/task-instances/task-instances.vue`: default 20 rows and remove planned-node UI/state/request mapping.
- Modify `web/scripts/check-collector-task-style.mjs`: preserve the task list layout while requiring the new width and default size.
- Modify `modules/collector/proto/collector.proto`: remove and reserve field 12 in two messages.
- Regenerate `modules/collector/proto/collectorgen/collector.pb.go`: generated protocol binding without planned-node accessors.
- Regenerate `modules/collector/proto/collectorgen/collector.trpc.go`: generator output, if changed.
- Modify `modules/collector/internal/domain/task_instance.go`: remove persistence field.
- Modify `modules/collector/internal/store/task_instance.go`: remove filter and upsert column.
- Modify `modules/collector/internal/rpc/convert.go`: remove response mapping.
- Modify `modules/collector/internal/rpc/service.go`: remove request-filter mapping.
- Modify `modules/collector/schema/collector.sql`: remove column and redefine execution index.
- Modify `modules/collector/schema/schema_test.go`: fresh-schema contract.
- Modify `modules/collector/internal/store/database.go`: invoke pre-schema migration.
- Create `modules/collector/internal/store/schema_migration.go`: idempotent existing-database migration.
- Modify `modules/collector/internal/store/database_test.go`: old-schema data-preservation and index migration test.
- Modify `modules/collector/internal/rpc/convert_test.go`: protocol conversion remains correct without planned node.
- Modify `docs/云节点执行平台架构.md`: remove obsolete field inventory entry.
- Modify generated `web-host/internal/statik/statik.go`: embed the final frontend bundle.

### Task 1: Add RED removal and pagination contracts

**Files:**
- Create: `scripts/check-collector-planned-node-removal.mjs`
- Modify: `web/scripts/check-collector-task-style.mjs`
- Modify: `modules/collector/schema/schema_test.go`

- [ ] **Step 1: Create the repository-wide removal contract**

Create `scripts/check-collector-planned-node-removal.mjs`:

```js
import fs from 'node:fs';
import path from 'node:path';
import process from 'node:process';

const root = process.cwd();
const read = (file) => fs.readFileSync(path.join(root, file), 'utf8');
const trackedWithoutProtoReservation = [
  'web/src/views/collector/task-instances/task-instances.vue',
  'modules/collector/proto/collectorgen/collector.pb.go',
  'modules/collector/internal/domain/task_instance.go',
  'modules/collector/internal/store/task_instance.go',
  'modules/collector/internal/rpc/convert.go',
  'modules/collector/internal/rpc/service.go',
  'modules/collector/schema/collector.sql',
  'docs/云节点执行平台架构.md',
];
const joined = trackedWithoutProtoReservation.map(read).join('\n');
const web = read(trackedWithoutProtoReservation[0]);
const proto = read('modules/collector/proto/collector.proto');
const schema = read('modules/collector/schema/collector.sql');

const forbidden = ['planned_exec_node', 'PlannedExecNode', 'c_planned_exec_node', '计划节点'];
const remaining = forbidden.filter((token) => joined.includes(token));
const requirements = [
  [web.includes('pageSize: 20'), 'frontend default page size 20'],
  [web.includes(':scroll="{ x: 1650 }"'), 'reduced table scroll width'],
  [(proto.match(/reserved 12;/g) || []).length === 2, 'reserved field number 12 twice'],
  [(proto.match(/reserved "planned_exec_node";/g) || []).length === 2, 'reserved field name twice'],
  [!proto.includes('string planned_exec_node = 12;'), 'removed protobuf field declaration'],
  [schema.includes('idx_collector_instances_exec ON t_collector_task_instances(c_last_exec_status)'), 'status-only execution index'],
];
const missing = requirements.filter(([ok]) => !ok).map(([, label]) => label);

if (remaining.length || missing.length) {
  console.error(`collector planned-node removal failed; remaining: ${remaining.join(', ') || 'none'}; missing: ${missing.join(', ') || 'none'}`);
  process.exit(1);
}

console.log('collector planned-node removal contract passed');
```

- [ ] **Step 2: Extend the frontend style contract**

In `web/scripts/check-collector-task-style.mjs`, replace the old scroll-width requirement with `:scroll="{ x: 1650 }"`, require `pageSize: 20`, and forbid `plannedExecNode`, `PlannedExecNode`, `planned_exec_node`, and `计划节点`.

- [ ] **Step 3: Strengthen the fresh-schema test**

Add to `TestAllSQL_ShouldReturnNonEmptySchema` in `modules/collector/schema/schema_test.go`:

```go
assert.NotContains(t, sql, "c_planned_exec_node")
assert.Contains(t, sql, "idx_collector_instances_exec ON t_collector_task_instances(c_last_exec_status)")
```

- [ ] **Step 4: Run contracts and verify RED**

```bash
node scripts/check-collector-planned-node-removal.mjs
cd web && CI=true pnpm check:collector-task-style
cd ../modules/collector && go test -count=1 ./schema -run TestAllSQL_ShouldReturnNonEmptySchema -v
```

Expected: all three checks fail because the old field and 10-row/1810-width behavior still exist.

- [ ] **Step 5: Commit RED contracts**

```bash
git add scripts/check-collector-planned-node-removal.mjs web/scripts/check-collector-task-style.mjs modules/collector/schema/schema_test.go
git commit -m "test(collector): define planned node removal contract"
```

### Task 2: Remove planned node from UI and protocol

**Files:**
- Modify: `web/src/views/collector/task-instances/task-instances.vue`
- Modify: `modules/collector/proto/collector.proto`
- Regenerate: `modules/collector/proto/collectorgen/collector.pb.go`
- Regenerate if changed: `modules/collector/proto/collectorgen/collector.trpc.go`

- [ ] **Step 1: Update the task-instance page**

In `task-instances.vue`:

- remove the `计划节点` input;
- remove the `计划节点` table column;
- remove the detail description item;
- delete `PlannedExecNode` from `TaskInstance`;
- delete `plannedExecNode` from the form initializer and reset object;
- delete planned-node normalization and request mapping;
- change `pagination.pageSize` from `10` to `20`;
- change table scroll width from `1810` to `1650`.

Do not change the last execution node field or filter.

- [ ] **Step 2: Reserve the removed protobuf identity**

Change both protobuf messages to reserve field 12:

```proto
message TaskInstance {
  reserved 12;
  reserved "planned_exec_node";
  // existing fields 1-11 and 13-20 remain unchanged
}

message TaskInstanceFilter {
  reserved 12;
  reserved "planned_exec_node";
  // existing fields 1-11 and 13-15 remain unchanged
}
```

- [ ] **Step 3: Regenerate protobuf output**

```bash
make -C modules/collector/proto all
```

Expected: generated `collector.pb.go` no longer contains `PlannedExecNode` or `GetPlannedExecNode`; generated descriptors contain the reserved ranges/names.

- [ ] **Step 4: Run the focused frontend and protocol checks**

```bash
cd web
CI=true pnpm check:collector-task-style
CI=true pnpm exec vue-tsc --noEmit
cd ..
rg -n "PlannedExecNode|planned_exec_node" modules/collector/proto/collectorgen modules/collector/proto/collector.proto
```

Expected: frontend checks pass; `rg` reports only the two reserved-name declarations in `collector.proto` and no generated field/accessor.

- [ ] **Step 5: Commit UI and protocol changes**

```bash
git add web/src/views/collector/task-instances/task-instances.vue modules/collector/proto/collector.proto modules/collector/proto/collectorgen
git commit -m "refactor(collector): remove planned node from UI and protocol"
```

### Task 3: Remove planned node from Collector runtime code

**Files:**
- Modify: `modules/collector/internal/domain/task_instance.go`
- Modify: `modules/collector/internal/store/task_instance.go`
- Modify: `modules/collector/internal/rpc/convert.go`
- Modify: `modules/collector/internal/rpc/service.go`
- Modify: `modules/collector/internal/rpc/convert_test.go`

- [ ] **Step 1: Remove the domain and repository fields**

Delete `PlannedExecNode` from `domain.TaskInstance` and `store.TaskInstanceFilter`. Remove `c_planned_exec_node` from `UpsertMany` assignment columns and delete the planned-node branch from `applyFilter`.

- [ ] **Step 2: Remove RPC request and response mapping**

Delete `PlannedExecNode: instance.PlannedExecNode` from `toPBInstance` and delete `repoFilter.PlannedExecNode = filter.GetPlannedExecNode()` from `GetTaskInstanceList`.

- [ ] **Step 3: Strengthen the converter regression test**

In `TestToPBInstance_ShouldMapStatus`, marshal the response to JSON and prove the removed field is absent:

```go
encoded, err := protojson.Marshal(instance)
require.NoError(t, err)
assert.NotContains(t, string(encoded), "plannedExecNode")
```

Add `google.golang.org/protobuf/encoding/protojson` and `require` imports if not already present.

- [ ] **Step 4: Run Collector compile and RPC tests**

```bash
cd modules/collector
go test -count=1 ./internal/domain ./internal/store ./internal/rpc
```

Expected: all packages compile and tests pass without planned-node generated getters or struct fields.

- [ ] **Step 5: Commit runtime removal**

```bash
git add modules/collector/internal/domain/task_instance.go modules/collector/internal/store/task_instance.go modules/collector/internal/rpc/convert.go modules/collector/internal/rpc/service.go modules/collector/internal/rpc/convert_test.go
git commit -m "refactor(collector): remove planned node runtime mapping"
```

### Task 4: Add and verify the SQLite migration

**Files:**
- Modify: `modules/collector/schema/collector.sql`
- Create: `modules/collector/internal/store/schema_migration.go`
- Modify: `modules/collector/internal/store/database.go`
- Modify: `modules/collector/internal/store/database_test.go`

- [ ] **Step 1: Write the old-schema migration test**

Add `TestApplySchemaDropsPlannedExecNodeWithoutLosingTasks` to `database_test.go`. The test must create the complete legacy task-instance table shape (all canonical columns plus `c_planned_exec_node`) so the remaining canonical indexes and triggers can be applied after migration:

```go
mgr, err := Open(&Options{Path: filepath.Join(t.TempDir(), "collector.db")})
require.NoError(t, err)
t.Cleanup(func() { _ = mgr.Close() })

require.NoError(t, mgr.db.Exec(`
CREATE TABLE t_collector_task_instances (
  c_id INTEGER NOT NULL PRIMARY KEY AUTOINCREMENT,
  c_space_id TEXT NOT NULL DEFAULT '',
  c_task_id TEXT NOT NULL,
  c_cloud_job_item_id TEXT NOT NULL DEFAULT '',
  c_rule_id TEXT NOT NULL,
  c_exchange TEXT NOT NULL DEFAULT '',
  c_market TEXT NOT NULL DEFAULT '',
  c_data_type TEXT NOT NULL DEFAULT '',
  c_dataset_id TEXT NOT NULL DEFAULT '',
  c_subject_id TEXT NOT NULL DEFAULT '',
  c_symbol TEXT NOT NULL DEFAULT '',
  c_interval TEXT NOT NULL DEFAULT 'default',
  c_planned_exec_node TEXT NOT NULL DEFAULT '',
  c_last_exec_node TEXT NOT NULL DEFAULT '',
  c_last_exec_status INTEGER NOT NULL DEFAULT 1,
  c_task_params TEXT NOT NULL DEFAULT '{}',
  c_last_exec_time DATETIME,
  c_result TEXT NOT NULL DEFAULT '{}',
  c_is_deleted INTEGER NOT NULL DEFAULT 0,
  c_ctime DATETIME DEFAULT CURRENT_TIMESTAMP,
  c_mtime DATETIME DEFAULT CURRENT_TIMESTAMP
);
CREATE INDEX idx_collector_instances_exec
  ON t_collector_task_instances(c_planned_exec_node, c_last_exec_status);
INSERT INTO t_collector_task_instances(c_task_id, c_rule_id, c_planned_exec_node, c_last_exec_status)
VALUES ('task-1', 'rule-1', '', 3);
`).Error)

require.NoError(t, mgr.ApplySchema(schema.AllSQL()))
```

Then query `PRAGMA table_info(t_collector_task_instances)`, assert `c_planned_exec_node` is absent, assert `task-1` still exists with status 3, and query `PRAGMA index_info(idx_collector_instances_exec)` to assert the sole indexed column is `c_last_exec_status`.

- [ ] **Step 2: Run the migration test and verify RED**

```bash
cd modules/collector
go test -count=1 ./internal/store -run TestApplySchemaDropsPlannedExecNodeWithoutLosingTasks -v
```

Expected: FAIL because no migration removes the old column/index.

- [ ] **Step 3: Update the canonical schema**

Remove the `c_planned_exec_node` column from `collector.sql` and redefine:

```sql
CREATE INDEX IF NOT EXISTS idx_collector_instances_exec
  ON t_collector_task_instances(c_last_exec_status);
```

- [ ] **Step 4: Implement the idempotent pre-schema migration**

Create `schema_migration.go`:

```go
package store

import (
  "fmt"

  "gorm.io/gorm"
)

const plannedExecNodeColumn = "c_planned_exec_node"

func (s *Store) migrateTaskInstanceSchema() error {
  var count int64
  if err := s.db.Raw(`
SELECT COUNT(*)
FROM pragma_table_info('t_collector_task_instances')
WHERE name = ?
`, plannedExecNodeColumn).Scan(&count).Error; err != nil {
    return fmt.Errorf("inspect collector task instance schema: %w", err)
  }
  if count == 0 {
    return nil
  }
  if err := s.db.Transaction(func(tx *gorm.DB) error {
    if err := tx.Exec("DROP INDEX IF EXISTS idx_collector_instances_exec").Error; err != nil {
      return fmt.Errorf("drop collector execution index: %w", err)
    }
    if err := tx.Exec("ALTER TABLE t_collector_task_instances DROP COLUMN c_planned_exec_node").Error; err != nil {
      return fmt.Errorf("drop planned execution node column: %w", err)
    }
    return nil
  }); err != nil {
    return err
  }
  return nil
}
```

In `Store.ApplySchema`, call `migrateTaskInstanceSchema()` before `s.db.Exec(sql)` and wrap any migration error with context.

- [ ] **Step 5: Verify migration and repeatability GREEN**

Run twice against tests by adding a second `require.NoError(t, mgr.ApplySchema(schema.AllSQL()))` assertion, then execute:

```bash
cd modules/collector
go test -count=1 ./internal/store ./schema -v
```

Expected: old data survives, the removed column stays absent, and the status-only index remains correct after repeated schema application.

- [ ] **Step 6: Commit schema and migration**

```bash
git add modules/collector/schema/collector.sql modules/collector/schema/schema_test.go modules/collector/internal/store/database.go modules/collector/internal/store/database_test.go modules/collector/internal/store/schema_migration.go
git commit -m "feat(collector): migrate away planned execution node"
```

### Task 5: Update docs and run complete local verification

**Files:**
- Modify: `docs/云节点执行平台架构.md`
- Test: `scripts/check-collector-planned-node-removal.mjs`

- [ ] **Step 1: Remove the obsolete documentation field**

Update the task-instance field inventory so it lists `last_exec_node` and the remaining fields without `planned_exec_node`.

- [ ] **Step 2: Run the repository-wide removal contract**

```bash
node scripts/check-collector-planned-node-removal.mjs
```

Expected: `collector planned-node removal contract passed`.

- [ ] **Step 3: Run the full Collector module suite**

```bash
cd modules/collector
go test -count=1 ./...
```

Expected: every Collector package and E2E test passes.

- [ ] **Step 4: Run frontend verification**

```bash
cd web
CI=true pnpm check:collector-task-style
CI=true pnpm check:detail-pages
CI=true pnpm test -- --run
CI=true pnpm exec vue-tsc --noEmit
CI=true pnpm run build:prod
```

Expected: contracts pass, all current Vitest files pass, type checking is clean, and Vite builds successfully.

- [ ] **Step 5: Embed and test the frontend**

```bash
cd web-host
make statik
go test -count=1 ./...
```

Expected: only `web-host/internal/statik/statik.go` changes and web-host tests pass.

- [ ] **Step 6: Build Linux deployment binaries**

```bash
TARGET_GOOS=linux TARGET_GOARCH=amd64 ./scripts/build.sh collector
TARGET_GOOS=linux TARGET_GOARCH=amd64 ./scripts/build.sh web-host
sha256sum bin/moox-collector bin/moox-collector-cli bin/moox-web-host
```

Expected: all three Linux AMD64 binaries are produced with digests.

- [ ] **Step 7: Commit docs and embedded bundle**

```bash
git add docs/云节点执行平台架构.md web-host/internal/statik/statik.go
git commit -m "build(web): embed collector planned node removal"
```

### Task 6: Deploy Collector migration and frontend

**Files:**
- No source changes expected.

- [ ] **Step 1: Upload all binaries**

```bash
scp -o BatchMode=yes bin/moox-collector bin/moox-collector-cli bin/moox-web-host ubuntu@106.53.107.122:/tmp/
```

- [ ] **Step 2: Stop Collector and create a consistent rollback set**

On the server, create one timestamp and use it for binary and database backups:

```bash
cd /home/ubuntu/moox/prod
stamp=$(date +%Y%m%d%H%M%S)
./stop.sh collector
cp bin/moox-collector "bin/moox-collector.backup.${stamp}"
cp bin/moox-collector-cli "bin/moox-collector-cli.backup.${stamp}"
sqlite3 data/collector/moox_collector.db ".backup 'backup/moox_collector.db.before-planned-node-removal.${stamp}'"
```

- [ ] **Step 3: Install and start Collector**

```bash
install -m 0755 /tmp/moox-collector bin/moox-collector
install -m 0755 /tmp/moox-collector-cli bin/moox-collector-cli
rm -f /tmp/moox-collector /tmp/moox-collector-cli
./start.sh collector
./healthcheck.sh collector
```

Expected: schema initialization succeeds, Collector starts, and signed health check exits `0`.

- [ ] **Step 4: Verify the production migration**

```bash
sqlite3 data/collector/moox_collector.db "
SELECT COUNT(*) FROM t_collector_task_instances;
SELECT name FROM pragma_table_info('t_collector_task_instances') WHERE name='c_planned_exec_node';
SELECT name FROM pragma_index_info('idx_collector_instances_exec');
"
```

Expected: task count remains 454 or higher, the column query returns no rows, and the index query returns only `c_last_exec_status`.

- [ ] **Step 5: Install and start web-host**

```bash
cp bin/moox-web-host "bin/moox-web-host.backup.${stamp}"
MOOX_WITH_WEB_HOST=1 ./stop.sh web-host
install -m 0755 /tmp/moox-web-host bin/moox-web-host
rm -f /tmp/moox-web-host
MOOX_WITH_WEB_HOST=1 ./start.sh web-host
./healthcheck.sh web-host
sha256sum bin/moox-collector bin/moox-collector-cli bin/moox-web-host
```

Expected: both service health checks pass and remote hashes match local hashes.

- [ ] **Step 6: Browser and API acceptance**

Open `https://106.53.107.122:9527/?v=<commit>#/collector/tasks` and verify:

- exactly 20 body rows are rendered on the first page;
- pagination displays `20 条/页` and still exposes the selector;
- planned-node filter, column, and detail item are absent;
- last execution node remains visible;
- query, reset, page change, page-size change, and detail opening work;
- serialized task data and rendered DOM do not contain `planned_exec_node`;
- no new console errors appear at desktop or 720px viewport.

### Task 7: Independent Agent review and remediation

**Files:**
- Review all commits since `0e1a17df` and the deployed runtime.

- [ ] **Step 1: Start a fresh review Agent**

Spawn a new Agent after implementation and initial verification with this bounded task:

```text
Independently review and verify the planned_exec_node removal on the current feature branch. Inspect the diff from 0e1a17df, protobuf compatibility, SQLite migration idempotency/data preservation, frontend default pagination, generated code, tests, production DB/API/browser evidence, and rollback safety. Report findings first with severity and file/line references. Run targeted verification yourself; do not modify files.
```

- [ ] **Step 2: Address every material finding**

For each correctness, migration, compatibility, test, or UI finding, reproduce it, add or strengthen a failing test, implement the minimal fix, and rerun the affected suite. Commit fixes separately with a descriptive message.

- [ ] **Step 3: Ask the same fresh Agent to re-review fixes**

Send the Agent the remediation commit IDs and request a final targeted verification. The review is complete only when it reports no remaining material findings and lists the commands/runtime evidence it checked.

### Task 8: Final completion audit and push

**Files:**
- No source changes expected.

- [ ] **Step 1: Run final removal and test evidence**

```bash
node scripts/check-collector-planned-node-removal.mjs
cd modules/collector && go test -count=1 ./...
cd ../../web && CI=true pnpm check:collector-task-style && CI=true pnpm test -- --run && CI=true pnpm exec vue-tsc --noEmit
cd ../web-host && go test -count=1 ./...
```

Expected: every command exits `0` after review remediation.

- [ ] **Step 2: Recheck production truth**

Confirm Collector and web-host signed health, production database column/index state, first-page 20-row browser state, absence of the field in API/rendered data, and matching local/remote hashes.

- [ ] **Step 3: Push and prove synchronization**

```bash
git push origin feature/frontend-service-host-workbench
git status --short --branch
git rev-parse HEAD
git rev-parse origin/feature/frontend-service-host-workbench
```

Expected: the worktree is clean, the branch is neither ahead nor behind, and both revisions are identical.
