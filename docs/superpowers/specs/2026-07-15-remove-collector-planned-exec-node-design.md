# Remove Collector Planned Execution Node Design

## Goal

Set the task-instance list default page size to 20 and remove the unused planned execution node field from the frontend, control API, Collector domain/store, SQLite schema, existing production database, and documentation.

## Current State

- `/#/collector/tasks` defaults to 10 rows per page and exposes planned-node filtering, a planned-node table column, and a planned-node detail field.
- `planned_exec_node` is field 12 in both `TaskInstance` and `TaskInstanceFilter`.
- Collector persists `c_planned_exec_node` and includes it in the execution index with `c_last_exec_status`.
- The production Collector database currently contains 454 task instances and zero non-empty `c_planned_exec_node` values.

## Frontend

- Change the initial pagination page size from 10 to 20.
- Keep page-size selection enabled so operators can choose a different size after loading the page.
- Remove the planned-node input from the toolbar.
- Remove the planned-node table column and detail-modal row.
- Remove the field from the TypeScript task model, normalization, form state, reset logic, and request construction.
- Reduce the table's configured horizontal width by the removed column width so the remaining columns do not leave unused space.
- Preserve the last execution node filter, column, and detail field.

## Protocol And RPC

- Remove `planned_exec_node` from `TaskInstance` and `TaskInstanceFilter`.
- Reserve field number 12 and name `planned_exec_node` in both messages. Older clients may still send the unknown field, but the new server ignores it and the field number cannot be reused accidentally.
- Regenerate Collector protobuf bindings with the repository's existing `trpc-open` workflow.
- Remove planned-node response conversion and request-filter mapping from the RPC layer.
- Preserve all other protobuf field numbers and wire behavior.

## Domain And Store

- Remove `PlannedExecNode` from `domain.TaskInstance` and `store.TaskInstanceFilter`.
- Remove `c_planned_exec_node` from the upsert update list and query filter construction.
- Preserve task identity, CloudNode job association, last execution node/status updates, pagination, and all other filters.

## Schema And Migration

- Remove `c_planned_exec_node` from the canonical `CREATE TABLE` definition.
- Keep `idx_collector_instances_exec`, but redefine it on `c_last_exec_status` only.
- Add an idempotent Collector schema migration that runs before the canonical schema SQL:
  1. Inspect `PRAGMA table_info(t_collector_task_instances)`.
  2. If the table or column is absent, do nothing.
  3. If the column exists, begin a transaction, drop the old execution index, and execute `ALTER TABLE t_collector_task_instances DROP COLUMN c_planned_exec_node`.
  4. Commit, then let the canonical schema create the new status-only execution index.
- Migration tests must start from an old-schema table containing a task row and prove that the row survives, the planned-node column is absent, and the new index contains only `c_last_exec_status`.
- Fresh-schema tests must prove the removed column is never created.

## Documentation

- Update the Collector task-instance field inventory in `docs/云节点执行平台架构.md` so it no longer lists `planned_exec_node`.

## Deployment

- Build both Collector server/CLI binaries and the embedded `web-host` Linux binary.
- Before running the migration, stop Collector and create a timestamped backup of `data/collector/moox_collector.db` together with the existing binary backups.
- Install the new Collector server/CLI and restart Collector through the production start script. Schema initialization performs the idempotent migration before service startup.
- Install and restart `web-host` after the Collector API is healthy.
- Verify the production database with `PRAGMA table_info` and `PRAGMA index_info`.

## Verification

- TDD coverage for the frontend contract, default page size, protobuf removal/reservation, domain/store removal, old-schema migration, fresh schema, and RPC list behavior.
- Run all Collector module tests, frontend tests, Vue type checking, production frontend build, `web-host` tests, and Linux builds.
- Browser acceptance must prove 20 rows render by default, the planned-node filter/column/detail field are absent, last execution node remains, pagination still allows changing page size, and there are no new console errors.
- API/runtime acceptance must prove task-instance responses no longer expose `planned_exec_node`.
- After implementation and initial verification, start a fresh independent Agent to review the code and repeat targeted verification. Resolve every material finding before final acceptance.

## Rollback

- Keep the pre-migration SQLite backup and previous Collector/web-host binaries.
- If migration or runtime verification fails, stop the affected services, restore the database and binaries from the same timestamped backup set, restart, and rerun health checks.

## Non-Goals

- No change to scheduling or CloudNode assignment behavior.
- No fixed page size; 20 is the default while the selector remains available.
- No reuse of protobuf field number 12.
