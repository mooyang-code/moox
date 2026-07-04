# Admin / CloudNode / Collector split audit

Date: 2026-07-02

## Goal

Track the remaining work for the split where:

1. The admin database is `data/admin.db`, not `data/moox.db`.
2. Admin no longer owns CloudNode, cloud account, cloud function package, or collector task business logic.
3. CloudNode and Collector run as independent services with their own schemas.
4. Old one-off migration and compatibility code is removed.
5. System data can be rebuilt from `examples`.

## Current evidence

### Admin DB and schema

Admin config points to `admin.db`:

```text
modules/admin/config/app.yaml
modules/admin/config/gateway.yaml
modules/admin/internal/config/app.go
modules/admin/internal/service/database/manager.go
```

Admin schema now owns only admin-plane tables:

```text
t_spaces
t_space_members
t_service_deployments
t_users
t_active_tokens
t_login_history
t_user_actions
t_ssh_host
t_ssh_session
t_host_monitor_history
t_secrets
```

Removed from admin schema during this audit:

```text
t_exchange_symbols
```

CloudNode and Collector tables are not in `modules/admin/schema/admin.sql`.

### Admin service boundary

`modules/admin/internal/bootstrap/trpc.go` registers admin-local services only:

```text
Auth
Gateway
SpaceMgr
Dns
Ssh
Monitor
SecretMgr
SysDeploy
```

It does not register `CollectMgr` or `CloudNodeMgr`.

Admin gateway resolves these service IDs from active `t_service_deployments`
records:

```text
collectmgr -> moox_collector -> trpc.moox.collect.CollectMgr at 127.0.0.1:11402
cloudnode  -> moox_cloudnode -> trpc.moox.collect.CloudNodeMgr at 127.0.0.1:11401
```

This matches the expected direction: admin is the API gateway for `/api/admin/*` and `/api/service/*`, while business logic is in independent services.

The gateway now forwards method names verbatim. Historical aliases such as `cloudnode/ReportHeartbeatInner -> ReportHeartbeat` have been removed so old callers fail visibly instead of being hidden by gateway-specific compatibility logic.

Unused gateway HTTP-client helper methods (`GetServiceConfig`, `GetServiceConfigByID`, `GetStorageServiceConfig`, `GetAuthServiceConfig`, `GetMetadataServiceConfig`, `HasService`) were removed. `GetServiceDetail` is the only gateway service resolver used by `forwardHTTP`.

### Independent service schemas

CloudNode schema lives under:

```text
modules/cloudnode/schema/cloudnode.sql
```

It owns:

```text
t_cloud_nodes
t_cloud_accounts
t_cloud_function_packages
t_cloud_async_jobs
t_cloud_job_attempts
t_cloud_invocations
t_cloud_invocation_results
```

Collector schema lives under:

```text
modules/collector/schema/collector.sql
```

It owns:

```text
t_collector_task_rules
t_collector_task_instances
t_collector_execution_logs
```

### Independent service entries

CloudNode service entry:

```text
modules/cloudnode/cmd/moox-cloudnode/main.go
```

Collector service entry:

```text
modules/collector/cmd/moox-collector/main.go
```

The root deploy script can deploy `admin`, `web-host`, `cloudnode`, `collector`, and storage independently.

### Dead code removed in this pass

Removed one-off deploy/migration compatibility code:

```text
scripts/deploy-moox-test.sh
scripts/deploy-moox.sh --keep-legacy-data / PURGE_LEGACY_DATA path
modules/admin/internal/service/auth/dao/user.go AutoMigrate
modules/admin/scripts/deploy.sh
web-host/scripts/deploy.sh
modules/cli/scripts/
modules/storage/scripts/
modules/collector/data-collector.md
modules/admin/release/
web-host/release/
modules/admin/bin/server
modules/cloudnode/moox-scf-deploy
modules/collector/scf-build/main
modules/collector/collector-scf-*.zip
modules/storage/moox-storage
modules/storage/data/metadata.sqlite
modules/storage/tests/
modules/storage/BUILD.md
modules/storage/DEPLOY.md
modules/storage/release/
modules/storage/var/
modules/storage/logs/
modules/trade/data/moox_trade.db
modules/trade/log/
modules/trade/scripts/e2e_trade_gateway.py
modules/cli/release/
bin/
release/
var/
docs/deadcode-scan-2026-06-27.md
docs/deadcode-cleanup-2026-06-27-exec.md
docs/soft-delete-refactor-2026-06-27.md
docs/采集与云节点拆分执行计划.md
docs/superpowers/plans/2026-06-29-service-deployments-scf-runtime.md
docs/superpowers/plans/2026-06-30-storage-view-rpc-refactor.md
openspec/changes/adopt-modules-monorepo/
modules/admin/plan.md
```

`modules/admin/Makefile` and `web-host/Makefile` now delegate `make deploy` to the repository-level `scripts/deploy-moox.sh` entrypoint.

Their old module-local release packaging logic was also removed; module `build` / `release` / `deploy` targets now proxy to repository-level scripts.

`scripts/deploy-moox.sh` no longer searches `web-host/release` as a web-host binary source.

Removed migration/static guard tests that were only protecting already-completed moves:

```text
modules/admin/internal/service/database/manager_test.go
modules/admin/internal/service/protocol_contract_test.go
modules/admin/internal/gateway/gateway_test.go
modules/admin/internal/service/dnsproxy/rpc/service_test.go
modules/admin/internal/service/monitor/rpc/service_test.go
modules/admin/internal/service/msghub/msghub_test.go
modules/admin/internal/service/space/service_test.go
modules/admin/proto/admingen/page_result_json_test.go
modules/storage/internal/services/access/legacy_consumer_static_test.go
modules/cli/cmd/collector_test.go
modules/cli/cmd/tencent_ops_test.go
modules/cli/internal/tencentcloud/lighthouse_test.go
modules/collector/internal/adminapi/url_test.go
modules/collector/internal/cloudfunction/handler_test.go
modules/collector/internal/collector/binance/kline_test.go
modules/collector/internal/collector/binance/storage_config_test.go
modules/collector/internal/collector/binance/symbol_test.go
modules/collector/internal/discovery/discovery_test.go
modules/collector/internal/event/example_test.go
modules/collector/internal/exchange/binance/client_test.go
modules/collector/internal/executor/executor_test.go
modules/collector/internal/scheduler/ratelimiter_test.go
modules/collector/internal/source/binance_test.go
modules/collector/pkg/packager/scf_test.go
modules/collector/pkg/storage/client_test.go
modules/cli/cmd/data_test.go
modules/cli/cmd/metadata_test.go
modules/cli/cmd/storage_import_test.go
modules/cli/internal/adminclient/client_test.go
modules/storage/cmd/moox-storage-bench/main_test.go
modules/storage/cmd/moox-storage/main_test.go
modules/storage/internal/bench/convert_test.go
modules/storage/internal/bench/kline_test.go

### 2026-07-04 continuation audit

Checked `examples/` and `scripts/` for old `data/moox.db`, admin-owned cloudnode/collector tables, and old `/api/admin` or `/api/service` path residues:

```text
examples/
scripts/
```

No `data/moox.db`, `t_cloud_*`, `t_collect_*`, or `t_collector_*` residues were found in `examples/`.

Checked admin/cloudnode/collector/frontend call boundaries:

```text
web/src/api/cloud-account.ts
web/src/api/cloud-node.ts
web/src/api/function-package.ts
modules/cloudnode/internal/service/cloudnode/service.go
```

Cloud account, cloud node, and function package frontend calls now go through `callControl('cloudnode', ...)`, which targets `/api/admin/cloudnode/*` and is handled by the independent `moox-cloudnode` service. Admin remains the gateway boundary rather than owning these business implementations.

Checked one-off migration/test residue:

```text
find modules scripts docs examples -iname '*migration*' -o -iname '*migrate*' -o -iname '*legacy*' -o -iname '*_test.go'
```

No project-owned migration files or `_test.go` files remained in the checked source tree.

Removed one additional dead helper:

```text
modules/admin/internal/service/sysdeploy/defaults.go deploymentWithStatus
```

### 2026-07-04 collector/cloudnode boundary cleanup

Checked `modules/admin` for old CloudNode, CloudAccount, SCF, CollectMgr, task rule, and task instance business implementation residues. The only `collect*` source path under admin is:

```text
modules/admin/internal/service/monitor/collector.go
```

This file is the admin monitor metric collector, not the independent Collector business service. CloudNode and Collector entries under admin are limited to deployment metadata, gateway comments, and docs.

Checked `modules/cloudnode` and `modules/collector` for old migration, legacy, compatibility, old API aliases, and project-owned `_test.go` residues. No such source files remained.

Removed additional dead residues:

```text
modules/collector/.claude/
modules/collector/internal/.claude/
modules/collector/internal/source/binance.go
```

The deleted `internal/source/binance.go` package was not imported by the current collector service path. Current task generation uses `internal/adapters`, `internal/planner`, `internal/storageclient`, and `internal/exchange`/`internal/collector` execution paths instead.

Kept these items because they are active dependencies rather than dead code:

```text
scripts/release.sh
scripts/check-module-boundaries.sh
trpc-log-cls related indirect Tencent CLS SDK dependencies
```

### 2026-07-04 runtime artifact and schema-boundary audit

Checked the repository for committed runtime data and generated release artifacts:

```text
*.db
*.sqlite
*.sqlite3
*.zip
*.tar.gz
bin/
release/
logs/
var/
```

No runtime database files or generated release artifacts were found in the working tree. The only matching directory names were frontend source directories:

```text
web/src/style/var
web/src/views/data
```

Checked admin/cloudnode/collector schema initialization boundaries:

```text
modules/admin/schema/admin.sql
modules/admin/schema/service_deployments_seed.sql
modules/cloudnode/schema/cloudnode.sql
modules/collector/schema/collector.sql
modules/admin/internal/service/database/manager.go
modules/cloudnode/internal/bootstrap/bootstrap.go
modules/collector/internal/control/bootstrap/bootstrap.go
```

Current ownership remains split as expected:

```text
admin     -> admin.sql + service_deployments_seed.sql
cloudnode -> cloudnode.sql
collector -> collector.sql
```

No `AutoMigrate`, migration file, or cross-module schema initialization was found for these modules.

Removed an old Collector SCF timer residue:

```text
modules/collector/configs/trpc_go.yaml trpc.collectexec.timer
```

The current collector runtime registers `heartbeatSchedule` and `dnsResolveSchedule`; no `collectExecSchedule` scheduler is registered. Task execution now goes through cloud function events / CloudNode job leases instead of the old local timer execution path.

### 2026-07-04 SCF package config cleanup

Removed one temporary generated coverage artifact:

```text
modules/storage/cover.out.tmp
```

Cleaned the SCF runtime example tRPC config:

```text
modules/collector/configs/example_trpc_go.yaml
```

The example now uses console logging instead of CLS logging placeholders. This avoids carrying cloud account placeholders into the generated SCF package.

Updated the SCF package builder:

```text
scripts/build-collector-scf-package.sh
```

The builder still copies `modules/collector/configs/` as the SCF runtime config tree, but it removes any local `configs/trpc_go.yaml` copied from that tree and writes the package `trpc_go.yaml` from `example_trpc_go.yaml`. This keeps developer-local ignored config out of SCF release packages while preserving the packaged runtime config path expected by tRPC.

### 2026-07-04 CLI SCF packager alignment

Checked the CLI package path:

```text
modules/cli/cmd/collector.go
modules/collector/pkg/packager/scf.go
```

`moox-cli collector function package` uses `modules/collector/pkg/packager/scf.go`, not the root shell script. The packager previously preferred `configs/trpc_go.yaml`, which is developer-local and ignored by Git.

Updated the packager so it now prefers:

```text
modules/collector/configs/example_trpc_go.yaml -> package trpc_go.yaml
```

If a custom config directory has no `example_trpc_go.yaml`, the packager still accepts an explicit `trpc_go.yaml`. This keeps the default repository path safe while preserving a deliberate custom-package path.

### 2026-07-03 CloudNode batch_id implementation

Implemented the CloudNode batch management naming decision:

```text
BatchCreateNodes
BatchDeleteNodes
BatchDeployNodes
```

The result entity is now `BatchChangeResult` and returns:

```text
batch_id
processed_count
message
```

Updated implementation surfaces:

```text
modules/collect/proto/collect_service.proto
modules/collect/proto/collectgen/
modules/cloudnode/internal/service/cloudnode/service.go
modules/cli/internal/adminclient/cloudnode.go
modules/cli/cmd/collector.go
web/src/api/cloud-node.ts
web/src/utils/cloud-node-batch-change.ts
web/src/views/collector/cloud-function/cloud-function.vue
docs/云节点管理.md
docs/云节点执行平台架构.md
docs/代码包管理.md
```

Reserved `job_id` for the existing SCF async execution queue (`SubmitJobs` / `PollJobs` / `ReportJobStatus`). Management batch create/delete/deploy no longer exposes `job_id`, `operation_id`, or `submission_id` at the API, CLI, or frontend progress-display boundary.

### 2026-07-04 CLI cloudnode operation naming cleanup

Checked CLI and helper entrypoints for old admin cloudfunction paths, old `data/moox.db`, old local collector timer paths, and old storage gateway paths:

```text
modules/cli/
scripts/
docs/
examples/
```

No old `/api/admin/cloudfunction`, `/api/service/storage`, `data/moox.db`, `collectExecSchedule`, or `trpc.collectexec.timer` residues were found in CLI or scripts.

Cleaned CLI naming for CloudNode batch management operations:

```text
modules/cli/internal/adminclient/cloudnode.go
modules/cli/cmd/collector.go
```

SUPERSEDED by 2026-07-03 batch_id cleanup: the CLI client no longer accepts management-batch `job_id` or `total_task_cnt` wire fields. The current CLI client reads `batch_id` and `processed_count`, and CLI JSON output uses:

```text
create_operation_id
deploy_operation_id
create_processed_count
deploy_processed_count
```

This keeps CLI semantics aligned with the current CloudNode catalog management behavior: batch node create/deploy operations are direct management operations, not old collector execution jobs.
modules/storage/internal/services/access/acceptance_test.go
modules/storage/proto/gen/page_result_json_test.go
modules/storage/tests/e2e/cli_import_test.go
modules/storage/tests/e2e/direct_storage_test.go
modules/storage/tests/e2e/e2e_test.go
modules/storage/tests/schema/metadata_schema_test.go
modules/factor/internal/service/service_test.go
modules/storage/internal/bootstrap/eventbus/factory_test.go
modules/storage/internal/config/loader_test.go
modules/storage/internal/core/eventbus/bus_test.go
modules/storage/internal/core/response/metadata_test.go
modules/storage/internal/core/router/resolver_test.go
modules/storage/internal/core/schema/validator_test.go
modules/storage/internal/infra/device/bleve/index_test.go
modules/storage/internal/infra/device/duckdb/view_store_internal_test.go
modules/storage/internal/infra/device/duckdb/view_store_nocgo_test.go
modules/storage/internal/infra/device/duckdb/view_store_quote_test.go
modules/storage/internal/infra/device/duckdb/view_store_test.go
modules/storage/internal/infra/device/factkey/key_test.go
modules/storage/internal/infra/device/parquet/archive_test.go
modules/storage/internal/infra/device/pebble/store_scan_internal_test.go
modules/storage/internal/infra/device/pebble/store_test.go
modules/storage/internal/infra/eventbus/producer_bus_test.go
modules/storage/internal/infra/metadata/cache/store_test.go
modules/storage/internal/infra/metadata/sqlite/store_internal_test.go
modules/storage/internal/infra/metadata/sqlite/store_test.go
modules/storage/internal/infra/transport/nats/producer_test.go
modules/storage/internal/infra/transport/registry_test.go
modules/storage/internal/services/access/register_data_subject_test.go
modules/storage/internal/services/access/service_test.go
modules/storage/internal/services/archive/events_test.go
modules/storage/internal/services/archive/service_test.go
modules/storage/internal/services/primary/local_internal_test.go
modules/storage/internal/services/primary/local_test.go
modules/storage/internal/services/view/builder/batcher_test.go
modules/storage/internal/services/view/builder/service_test.go
modules/storage/internal/services/view/naming_internal_test.go
modules/storage/internal/services/view/projection_test.go
modules/storage/internal/services/view/view_builder_test.go
modules/trade/internal/exchange/binance/binance_test.go
modules/trade/internal/exchange/binance/testhelpers_test.go
modules/trade/internal/exchange/okx/okx_test.go
modules/trade/internal/exchange/registry_test.go
modules/trade/internal/service/dao/dao_test.go
modules/trade/internal/service/memstore_test.go
modules/trade/internal/service/order_exec_test.go
modules/trade/internal/service/service_test.go
modules/admin/internal/service/secret/rpc/service_test.go
web-host/main_test.go
modules/admin/tests/
web/src/api/ret-info.test.js
web/src/router/route-output.test.js
web/src/utils/timeSeriesValidator.test.js
web/src/views/data/browse/browse-utils.test.js
web/src/views/data/shared/metadata-utils.test.js
web/src/views/data/view-browse/view-browse-utils.test.js
web/src/views/data/views/view-form-utils.test.js
scripts/test.sh
```

Repository and module Makefiles no longer expose `test` / `test-changed` targets after removing project-owned functional tests.

`modules/collector/Makefile`, `modules/storage/Makefile`, and `modules/cli/Makefile` were reduced to current build/release/deploy helpers and no longer expose test, e2e, coverage, perf, or integration targets.

## Follow-up alignment completed

### Collect/CloudNode protocol package

`modules/cloudnode` and `modules/collector` now import:

```text
github.com/mooyang-code/moox/modules/collect/proto/collectgen
```

The collect/cloudnode protocol source and generated package live outside admin:

```text
modules/collect/proto/collect_service.proto
modules/collect/proto/collectgen
```

`modules/admin/proto/admingen` no longer generates `collect_service`.

`scripts/build.sh proto` now regenerates `modules/collect/proto` between storage and admin, matching root `make proto`.

`scripts/release.sh` packages the current independently deployable services: `admin`, `web-host`, `cloudnode`, `collector`, `collector-scf`, `storage`, `factor`, `trade`, and `cli`. The old `account` package entry was removed because that module is not present in this repository.

Stale `modules/account` / `moox-account` and `modules/order` / `moox-order` references were removed from build scripts, architecture docs, and the `skills/moox` helper docs.

### SCF runtime deployment payload

SCF collector events now use `service_gateway_target` as the primary runtime callback target. Collector resolves the target from the active `service_gateway` row in `t_service_deployments`, and storage writes use the active storage tRPC targets.

### Legacy infra.local deployment helpers removed

The legacy `infra/infra.local.yaml` configuration chain has been removed. Runtime service deployment data is managed through `t_service_deployments` / SysDeploy.

Removed:

```text
infra/
pkg/infraconfig
scripts/infra-env.sh
```

Developer SSH helpers now resolve targets from explicit CLI arguments, `MOOX_DEV_SSH_TARGET`, `~/.moox-dev.env`, or interactive input.

### Full data reset has not been executed in this pass

The remote environment has already been reseeded with examples metadata and storage split is running, but this pass did not delete all remote data. A destructive reset should be explicit because it removes cloud accounts, SCF package metadata, collector tasks, and admin login/session data.

Reusable reference:

```text
.codex/skills/dev-helper/references/moox-remote-storage-split-reseed.md
```

## Frontend cloudnode management routing cleanup

The cloud function management page was aligned with the independent `moox-cloudnode` service boundary:

- `web/src/api/cloud-node.ts` keeps full per-node `BatchCreateNodes` payloads instead of collapsing a batch into the first node plus `count`.
- `web/src/utils/async-task.ts` no longer falls back to admin `asynctask/CreateAsyncJob` for `CREATE_NODE`, `DELETE_NODE`, or `DEPLOY_NODE`; these operations route to cloudnode batch APIs.
- `web/src/views/collector/cloud-function/cloud-function.vue` submits create/delete/deploy actions through `/api/admin/cloudnode/BatchCreateNodes`, `/api/admin/cloudnode/BatchDeleteNodes`, and `/api/admin/cloudnode/BatchDeployNodes` rather than old admin async-task endpoints.
- `web/scripts/verify-space-context.mjs` no longer references the deleted `cloud-function-async.vue` page, so local web checks do not pull the obsolete cloud function flow back into the active source set.

This keeps admin as a gateway for cloudnode management requests and avoids recreating cloudnode business execution inside admin.

Removed obsolete frontend cloud-function artifacts that still described or implemented the old admin async-task cloudnode flow:

- `web/src/views/collector/cloud-function/cloud-function-async.vue`
- `web/src/views/collector/cloud-function/README.md`

## Admin AsyncTask and unused admin-side services cleanup

Admin no longer exposes the old `asynctask` RPC service. Cloudnode management jobs are owned by `modules/cloudnode`, and collector planning is owned by `modules/collector`.

Removed or updated:

- Removed `modules/admin/internal/service/asynctask`.
- Removed `asynctask` from admin gateway config, default service deployments, and service deployment seed SQL.
- Removed admin app worker settings for async/node create/delete/deploy workers.
- Removed `AsyncTask` definitions from `modules/admin/proto/infra_service.proto` and regenerated `modules/admin/proto/admingen`.
- Removed obsolete docs: `docs/异步任务.md`, `modules/admin/alert.md`.
- Removed unused admin-local `modules/admin/internal/service/msghub`, which was not registered by admin bootstrap and still documented old asynctask-style organization.
- Frontend cloud function management no longer has a fallback path to admin async tasks for cloudnode operations.

Additional frontend cleanup:

- Replaced `web/src/utils/async-task.ts` with `web/src/utils/cloud-node-job.ts`; the new helper only preserves cloudnode job URL/status display state and does not call admin async-task APIs.
- Removed the stale `@/utils/async-task` import from `web/src/views/container/container-list/container-list.vue`.

## Collector SCF deploy helper DB coupling cleanup

`scripts/deploy-collector-scf-package.sh` no longer reads Tencent Cloud credentials directly from a remote `data/cloudnode/moox_cloudnode.db` / `t_cloud_accounts` table over SSH.

The helper now requires credentials through explicit `--secret-id` / `--secret-key` flags or `TENCENT_SECRET_ID` / `TENCENT_SECRET_KEY` environment variables. Cloud account records remain owned by the independent `moox-cloudnode` service instead of becoming an implicit shell-script database contract.

## Runtime SQLite artifact cleanup

Removed local SQLite runtime sidecar files under `modules/trade/data/` (`moox_trade.db-wal` and `moox_trade.db-shm`). Runtime data is ignored by `.gitignore` and should be recreated from module startup or examples/e2e data flows rather than preserved in source state.

## CLI direct database tooling cleanup

Removed the old root `moox-cli db` and `moox-cli import` commands, along with `modules/cli/internal/database/*`. These commands directly initialized, dropped, inspected, and imported YAML into SQLite databases such as `../data/admin.db`, bypassing the current module-owned schema and RPC/API boundaries.

The CLI config no longer exposes `metadata_database.storage_device`; active data operations should go through the admin, storage, collector, cloudnode, and trade service APIs rather than direct SQLite table mutation.

Follow-up cleanup removed the stale root help and README examples for `moox-cli db` / root `moox-cli import`, and removed the direct `modernc.org/sqlite` dependency plus stale checksum entries from `modules/cli/go.mod` / `modules/cli/go.sum`.

The remaining SQLite-only transitive dependency tail in `modules/cli` was also removed manually: `modernc.org/*`, `github.com/dustin/go-humanize`, `github.com/remyoudompheng/bigfft`, and `github.com/ncruces/go-strftime`. These were only needed by the deleted direct SQLite tooling, not by the active RPC/API CLI commands.

## Admin tRPC AsyncTask config cleanup

Removed the stale `trpc.moox.infra.AsyncTask` service block from `modules/admin/config/trpc_go.yaml`. Admin no longer registers or implements AsyncTask; cloudnode management jobs live in `modules/cloudnode`, and collector planning lives in `modules/collector`.

## Web-host embedded asset regeneration notes

`web-host/internal/statik/statik.go` is generated code and can contain stale frontend chunks until regenerated from the current `web/dist`. The web-host Makefile and README now make this explicit: `make build` only compiles with the currently embedded statik assets, while `make statik` regenerates assets from `../web/dist`. `scripts/deploy-moox.sh` now refreshes web assets by default whenever web-host is enabled; use `--reuse-web-assets` only when intentionally reusing the current embedded statik bundle.

## Collector API deployment row cleanup

Removed the stale `collector_api` / `service_api` default deployment row and the old `11001` admin README port note. Collector management is represented by the independently deployed `moox_collector` service (`trpc.moox.collect.CollectMgr`), so the separate `trpc.moox.api.stdhttp` row was an obsolete interface-level deployment entry.

The service deployment UI kind options now include `collector` and `cloudnode` directly instead of the old `service_api` category.

## Storage ViewBuilder naming cleanup

Replaced remaining old `Deriver` / `deriver` wording in storage comments and architecture docs with `ViewBuilder` / `view builder`, matching the current `modules/storage/internal/services/view/builder` package layout. The default NATS durable consumer base was renamed from `storage_deriver` to `storage_view_builder`; historical durable state does not need migration because runtime data can be rebuilt from examples/e2e flows.

## Web-host asset refresh default

Cloud function and cloud account frontend source now calls the independent `cloudnode` admin APIs, for example `/api/admin/cloudnode/ListCloudAccounts`. A stale deployed web-host bundle can still load obsolete chunks such as `async-task-*.js` if the Vue `dist` and statik bundle are not regenerated before compiling `moox-web-host`.

To avoid republishing old frontend assets, `scripts/deploy-moox.sh` now rebuilds Vue `dist`, regenerates `web-host/internal/statik`, and then builds `moox-web-host` by default whenever web-host is included. Use `--reuse-web-assets` only for an intentional fast rebuild with the currently embedded statik bundle.

## Frontend dangling test script cleanup

Project-owned test files have been removed from active source. The remaining frontend `test:api` and `test:unit` npm scripts pointed at deleted `*.test.js` files, so they were removed from `web/package.json` to avoid preserving dead test entrypoints.

`docs/大仓架构.md` was also updated so the repository architecture no longer recommends module-level `tests/` directories. Current policy is to rebuild runtime data from `examples/` and service flows rather than keeping one-off migration, schema-guard, functional, or unit test code in source.

## Admin cloud account storage config cleanup

Admin no longer owns cloud account credentials, cloud function packages, or COS upload settings. The old admin app config still carried package-storage fields (`cos_bucket`, `cos_region`, local package cache settings, and related environment overrides), which implied admin still managed cloud package storage.

Cleaned up admin-side storage config so it only keeps the local bootstrap `xdata_url` used to derive storage metadata defaults. COS bucket/region and cloud account credential settings now belong to the independent `moox-cloudnode` service and its `t_cloud_accounts` records.

Also renamed the admin default encryption key/comment from cloud-account wording to admin-local secrets/SSH credential wording. Admin still has a generic encryption key because it owns local SSH credentials and `t_secrets`; it no longer describes that key as encrypting cloud account credentials.

## Admin unused encryption config field cleanup

Removed the unused `security.encryption_key` YAML/config fields from admin app/gateway/auth config structs. Admin local secret encryption is implemented by `modules/admin/internal/common/crypto.GetEncryptionKey`, which reads `MOOX_ENCRYPTION_KEY` directly and falls back to a development default.

Keeping an unused `security.encryption_key` entry in YAML made it look like cloud/admin credential encryption was configurable through gateway config when it was not. Removing it keeps the active configuration surface aligned with runtime behavior.

## Admin secrets cloud category cleanup

Admin `t_secrets` is kept for admin-local credentials such as SSH, exchange, database, and system-token secrets. It no longer advertises a `cloud` / cloud-provider category, because cloud account credentials are owned by the independent `moox-cloudnode` service and persisted in `modules/cloudnode/schema/cloudnode.sql` (`t_cloud_accounts`).

Cleaned up the admin secrets schema comments, SecretMgr proto comments, generated comment text, and the settings secrets page so new admin secrets default to `ssh` rather than `cloud`. Existing old data is not migrated; runtime data can be deleted and rebuilt from examples/service flows.

## SSH hosts stale cloudnode UI cleanup

The SSH hosts page still exposed three stale UI actions: a placeholder batch deploy button, a cloud account management modal, and a function package management modal. The batch deploy handler only showed a "feature under development" message, and the cloud account / package modals were not connected to SSH host management.

Removed these dead entries from `web/src/views/container/ssh-hosts/ssh-hosts.vue` so cloud account and function package management remain under the collector/cloudnode management flow instead of appearing as unrelated shortcuts on the SSH host operations page.

## Stale container cloud-function page cleanup

Removed `web/src/views/container/container-list/container-list.vue`. The file was not referenced by the active router or mock menu, duplicated the cloud function node management UI, and still contained placeholder handlers such as "batch add/delete feature under development" and package-detail message-only actions.

The active cloudnode management page remains `web/src/views/collector/cloud-function/cloud-function.vue` and routes cloud account, cloud node, and package requests through `/api/admin/cloudnode/*`.

## Stale container API and file-management cleanup

Removed `web/src/api/modules/container.ts`, which was unreferenced and still targeted obsolete `/api/container/*` endpoints instead of the current admin gateway/API structure.

Removed `web/src/views/container/file-management/file-management.vue`, an unreferenced container file-management demo page with static container/file data and `/api/upload`. The active SSH file manager is `web/src/views/container/ssh-file-manager/ssh-file-manager.vue`, which is referenced by the SSH terminal flow and remains in place.

## Frontend template demo and mock cleanup

Removed unreferenced frontend template/demo pages and their mock API wrappers:

- `web/src/views/file/document-library/*`
- `web/src/views/table/common-table/*`
- `web/src/views/table/custom-table/*`
- `web/src/views/functions/routing-operation/*`
- `web/src/views/directive/{anti-shake,test-directive,throttle}/*`
- `web/src/api/modules/{file,table,test}/*`
- `web/src/mock/{file,table,test}/*`

These pages were not present in the active router or mock menu, and their API wrappers only targeted `/mock/*` endpoints. The related i18n menu keys were removed from `web/src/lang/modules/{zhCN,enUS}.ts` so deleted pages do not leave stale menu labels behind.

## 2026-07-02 cleanup: remove unimplemented strategy placeholder page

Removed the active but unimplemented strategy placeholder from the admin frontend:

- Dropped `/strategy/list` from the static route table.
- Dropped the `strategy` directory/menu seed from the frontend mock system menu.
- Removed `strategy` and `strategy-list` locale keys.
- Deleted `web/src/views/strategy/strategy-list/strategy-list.vue`, which only rendered a development placeholder.

Rationale: the page was not connected to an independently deployed backend service and did not provide a usable system capability. Keeping it in the active menu made the frontend carry dead navigation and stale chunks during the admin/cloudnode/collector split cleanup.

## 2026-07-02 cleanup: remove frontend test-only leftovers

Removed frontend-only test leftovers after the admin/cloudnode/collector split cleanup:

- Deleted `web/src/utils/test-network-error.ts`, a browser-console helper that only triggered fake `/test-*` requests to exercise network error messages.
- Removed the empty `web/src/mock/test` and `web/src/api/modules/test` directories left behind by previous template/demo API cleanup.

Rationale: these artifacts were development-time test/demo scaffolding and were not part of any independently deployed MooX service or runtime data rebuild flow.

## 2026-07-02 cleanup: remove unreachable frontend template pages

Removed frontend template/demo pages that were no longer exposed by the current static route table or system menu:

- Deleted `web/src/views/about`.
- Deleted `web/src/views/component`, including player, print, draggable, editor, newbie, icon selector, user center, fingerprint, barcode, qrcode, and pinyin demos.
- Deleted `web/src/views/disable-menu` and `web/src/views/hide-menu`.
- Deleted `web/src/views/i18n`.
- Deleted `web/src/views/link`.
- Deleted `web/src/views/multilevel`.
- Deleted `web/src/views/permission`.
- Deleted `web/src/views/test-router`.
- Deleted `web/src/utils/testRoute.ts`, an unused dynamic-route demo that referenced non-existent `multilevel-menu` view files.
- Removed the corresponding zh-CN/en-US locale keys.

Rationale: these pages came from the admin UI template and were unrelated to the MooX admin/cloudnode/collector split. Keeping them inflated the frontend source and could leave stale demo chunks in generated static assets.

## 2026-07-02 cleanup: remove frontend demo dependency leftovers

Removed dependency and component leftovers from the deleted frontend template/demo pages:

- Removed the unused dynamic external-library loader entries for WangEditor and xgplayer, and stopped prefetching those CDN scripts from `web/src/main.ts`.
- Removed unused demo dependencies from `web/package.json` and the importer section of `web/pnpm-lock.yaml`: `@fingerprintjs/fingerprintjs`, `@wangeditor/editor`, `@wangeditor/editor-for-vue`, `driver.js`, `fingerprintjs2`, `jsbarcode`, `pinyin-pro`, `print-js`, `qrcode`, `sortablejs`, `vuedraggable`, and `xgplayer`.
- Removed stale ambient declarations for template-only libraries from `web/src/vite-env.d.ts`.
- Removed stale global component declarations from `web/src/components.d.ts`.
- Deleted unused demo component directories: `web/src/components/barcode-draw`, `web/src/components/qrcode-draw`, and `web/src/components/pinyin-pro`.

Rationale: the removed libraries and components were only used by admin-template demo pages that are no longer reachable in MooX. VChart, lightweight-charts, `vue-color-kit`, and `vue-pick-colors` were intentionally kept because current MooX pages still use them.

## 2026-07-02 cleanup: remove unused frontend CDN prefetch helpers

Removed the last CDN-prefetch leftovers from the deleted frontend demo/template pages:

- Stopped importing `prefetchResource` in `web/src/main.ts`.
- Removed the idle-time prefetch for the unpkg `lightweight-charts` standalone script. Current MooX chart pages import `lightweight-charts` through the npm dependency, so the CDN script was not used by the runtime module graph.
- Removed unused `preloadResource` and `prefetchResource` helpers from `web/src/utils/dynamic-loader.ts`, leaving only the lazy-image helper still used by app bootstrap.

Rationale: frontend assets should come from the built module graph rather than stale admin-template CDN script hints. This keeps the generated web-host bundle aligned with current MooX source.

## 2026-07-02 cleanup: remove orphan frontend template components

Removed orphan frontend components left after deleting admin-template pages:

- Deleted `web/src/components/code-view`.
- Deleted `web/src/components/error-boundary`.
- Deleted `web/src/components/external-link-page`.
- Deleted `web/src/components/fill-page`.
- Deleted `web/src/components/internal-link-page`.
- Deleted `web/src/components/page-wrapper`.
- Deleted `web/src/components/select-icon`.
- Removed their stale global component declarations from `web/src/components.d.ts`.

Rationale: these components were no longer referenced by the current MooX layout, routes, or pages. `LangProvider` and `MainTransition` were intentionally kept because the active layout still imports them directly.

## 2026-07-02 cleanup: remove unused frontend mock endpoints

Removed frontend mock endpoints that belonged to the old admin UI template:

- Deleted `web/src/mock/data/index.ts`, which only served fake user/order/object-list data under `/mock/data/*`.
- Deleted `web/src/mock/user/index.ts`, which only served old `/mock/login` and `/mock/user/getUserInfo` responses.
- Removed the now-empty `web/src/mock/data` and `web/src/mock/user` directories.

Rationale: current MooX login/user info goes through the admin gateway APIs, and the data/object mock endpoints are not referenced by current pages. The remaining `mock/system` files are intentionally left for a separate cleanup because current menu/dict bootstrap still depends on the static system menu/data helpers.

## 2026-07-02 cleanup: remove mock system HTTP endpoints

Removed the remaining mock HTTP endpoints used for frontend menu/dictionary data:

- Added `web/src/api/modules/system/static-data.ts` for the small static dictionary data still needed by the admin shell.
- Changed `getDictAPI` in `web/src/api/modules/system/index.ts` to return the static dictionary directly instead of calling `/mock/system/getDict`.
- Deleted `web/src/mock/system/system.ts`, `web/src/mock/system/menu.ts`, and `web/src/mock/system/index.ts`.
- Deleted the old `web/src/mock/index.ts` production mock-server stub.
- Deleted `web/src/mock/_data/system_data.ts`, which contained old template role/account data plus the dictionary now moved next to the system API helper.
- Removed the empty `permissionData` export from `web/src/mock/_data/system_menu.ts`.
- Removed the already-commented vite mock-server injection block from `web/build/vite-plugin.ts`.

Rationale: current MooX frontend authenticates through admin gateway APIs and builds menu/dict data locally. Keeping mock HTTP endpoint files made the source look like it still supported the old admin-template mock server. The static menu helper remains for now because route/menu generation still depends on `web/src/mock/_data/system_menu.ts` and `web/src/mock/_utils.ts`; that can be renamed/migrated separately.

## 2026-07-02 cleanup: rename remaining frontend mock menu helpers

Moved the last remaining frontend `mock` helpers into the real system API module:

- Moved `web/src/mock/_data/system_menu.ts` to `web/src/api/modules/system/static-menu.ts`.
- Moved `web/src/mock/_utils.ts` to `web/src/api/modules/system/menu-utils.ts`.
- Updated `web/src/api/modules/system/index.ts` to import static menu data and menu utilities from the local system API module.
- Removed the now-empty `web/src/mock/_data`, `web/src/mock/file`, `web/src/mock/table`, and `web/src/mock` directories.

Rationale: these files no longer provided mock HTTP endpoints; they are local static menu helpers used by the current admin shell. Moving them out of `src/mock` makes the frontend source reflect the current no-mock-server architecture.

## 2026-07-02 cleanup: remove frontend mock tool dependencies

Removed mock-server tool leftovers after moving menu/dictionary helpers out of `src/mock`:

- Removed `mockjs` usage from `web/src/api/modules/system/menu-utils.ts`; the old mock response wrappers were no longer used by the static menu helper.
- Removed the stale commented `vite-plugin-mock` import from `web/build/vite-plugin.ts`.
- Removed the obsolete `VITE_APP_OPEN_MOCK` environment type from `web/src/typings/global.d.ts`.
- Removed the obsolete `mockjs` ambient declaration from `web/src/vite-env.d.ts`.
- Removed `mockjs` and `vite-plugin-mock` dependency declarations from `web/package.json` and the web lockfile importer section.

Rationale: MooX admin no longer has a frontend mock server. Menu and dictionary data are local static helpers under `web/src/api/modules/system`, while authentication and service data go through the admin gateway.

## 2026-07-02 cleanup: remove final frontend mock naming leftovers

Removed final source/config leftovers from the old frontend mock setup:

- Deleted `web/src/utils/mock-websocket.ts`, an unused SSH-terminal demo WebSocket simulator.
- Removed `VITE_APP_OPEN_MOCK=true` from `web/.env.development`; the frontend no longer wires `vite-plugin-mock` or `src/mock` endpoints.

Rationale: MooX admin should use the real admin gateway and SSH WebSocket endpoints. Keeping mock toggles or demo WebSocket code made local development configuration misleading after the mock server was removed.

## 2026-07-02 cleanup: remove frontend editor/demo dependency leftovers

Removed frontend dependency leftovers from deleted admin-template editor/demo pages:

- Cleared `web/build/optimize.ts` because all entries were for removed demo/editor libraries.
- Removed stale Vite manual chunk groups for `qrcode`, `jsbarcode`, `print-js`, CodeMirror, draggable/sortable/driver, fingerprint, and pinyin packages from `web/vite.config.ts`.
- Removed stale CodeMirror ambient declarations from `web/src/vite-env.d.ts`.
- Removed unused CodeMirror dependencies from `web/package.json` and the `web/pnpm-lock.yaml` importer section.
- Deleted `web/package-lock.json`; the web project enforces pnpm via `preinstall`, so the npm lockfile was a stale generated artifact and still referenced removed template dependencies.

Rationale: after removing the old template editor/demo pages, these packages were no longer referenced by MooX source. Keeping them in build split rules or lockfiles made the dependency graph look larger and less accurate than the current frontend actually needs.

## 2026-07-02 cleanup: remove stale frontend build optimization leftovers

Removed stale frontend build configuration that still referenced deleted template/editor dependencies:

- Removed `codemirror` from the custom `web/build-prod.js` manual chunk config.
- Removed the empty `optimizeDeps.include` import/config from `web/vite.config.ts`.
- Deleted `web/build/optimize.ts`, which only contained an empty pre-bundle include list after earlier template dependency cleanup.

Rationale: after removing admin-template editor/demo pages and their dependencies, keeping empty or stale build optimization files made the frontend build graph harder to reason about and could reintroduce references to removed packages.

## 2026-07-02 cleanup: remove old factor template page

Removed the unused old factor CRUD template page:

- Deleted `web/src/views/factor/factor-list/factor-list.vue`.
- Removed its empty parent directories under `web/src/views/factor`.
- Removed the stale `factor-list` locale keys from `web/src/lang/modules/zhCN.ts` and `web/src/lang/modules/enUS.ts`.

Rationale: the current MooX factor metadata page is `/data/factors` and uses `web/src/views/data/factors/index.vue`. The old `factor/factor-list` page was not reachable from the static route table or system menu and still used mock-style behavior such as “模拟提交成功”.

## 2026-07-02 cleanup: remove fake ops service status page

Removed the active but fake service-status operations page:

- Removed `/ops/service-status` from `web/src/router/route.ts`.
- Removed the `ops-service-status` menu entry from `web/src/api/modules/system/static-menu.ts`.
- Removed `ops-service-status` locale keys from `web/src/lang/modules/zhCN.ts` and `web/src/lang/modules/enUS.ts`.
- Deleted `web/src/views/container/service-status/service-status.vue`.

Rationale: the page displayed hard-coded container/service data and its refresh/start/stop/restart actions only modified frontend memory state. MooX should not expose fake operations controls as an active menu item after the admin/cloudnode/collector split; real deployment/service status should come from the service deployment and ops APIs.

## 2026-07-02 cleanup: cloudnode management API alignment check

Checked cloudnode and collector management routing after the admin/cloudnode/collector split:

- `web/src/api/cloud-account.ts`, `web/src/api/cloud-node.ts`, and `web/src/api/function-package.ts` call `callControl('cloudnode', ...)`, so frontend management requests go through `/api/admin/cloudnode/*`.
- `modules/admin/config/gateway.yaml` maps `cloudnode` to `trpc.moox.collect.CloudNodeMgr` at `127.0.0.1:11401`; admin only forwards and no longer owns the implementation.
- `modules/cloudnode` owns cloud accounts, cloud nodes, function packages, async jobs, and sync invocation state.
- `modules/collector` runtime callbacks use `/api/service/cloudnode/*` and `/api/service/collectmgr/*`, matching the backend encrypted service gateway.
- `modules/collect/proto/collectgen` is still active shared generated protocol code used by both `modules/cloudnode` and `modules/collector`; it is not a stale independently deployed service.

Removed `web/src/api/management-api-contract.ts`, an unreferenced old frontend contract file that only imported broad management/storage APIs and exposed a manual assertion helper. It had no runtime route or side effect.

Also removed the mount-time warning from `web/src/views/collector/cloud-function/cloud-function.vue` when the cloud account list is empty. An empty cloud account list is valid initial state; the page now keeps the creation guidance at the user action point instead of showing it as a load warning.

## 2026-07-02 cleanup: remove remaining local test script

Removed `skills/moox/scripts/test_tencent_lighthouse_firewall.py`, a `unittest` file for the Lighthouse firewall helper script. The runnable helper remains at `skills/moox/scripts/tencent_lighthouse_firewall.py` and is still referenced by the MooX skill instructions.

Rationale: the project policy for this cleanup is to remove functional/unit test code and rely on rebuildable examples/service flows for current verification. Keeping a standalone unit-test file under the MooX skill scripts made the repository still contain test code after source-level `*_test.go` cleanup.

## 2026-07-02 cleanup: remove local macOS metadata files

Removed `.DS_Store` files from the MooX workspace:

- `.DS_Store`
- `docs/.DS_Store`
- `modules/.DS_Store`
- `modules/admin/.DS_Store`
- `modules/admin/internal/.DS_Store`
- `modules/storage/.DS_Store`
- `web/.DS_Store`
- `web/src/views/settings/.DS_Store`

Rationale: these files are local macOS Finder metadata and are not source, schema, docs, examples, runtime configuration, or rebuildable product data.

Kept `.claude/settings.local.json` files untouched because they are local tool configuration rather than product dead code; deleting them could unexpectedly change the user's local agent/tool behavior.

## 2026-07-02 cleanup: align admin DB backup examples

Updated the SQLite backup examples in `docs/数据库管理.md` from `moox_backup.db` / `moox_backup.sql` to `admin_backup.db` / `admin_backup.sql`.

Rationale: admin now uses `admin.db`; backup examples should no longer carry old `moox` database naming that could be confused with the removed `data/moox.db` layout.

## 2026-07-02 cleanup: remove empty web-host npm lockfile

Removed `web-host/package-lock.json`.

Rationale: `web-host` is a Go static-file host with `go.mod` / `go.sum` and no `package.json`. The npm lockfile was empty (`packages: {}`) and not used by the current build or deployment flow.

## 2026-07-02 cleanup: remove fake frontend home dashboard

Replaced the old frontend home dashboard with a MooX workbench:

- Removed hard-coded total assets, strategy count, trade log, risk level, and revenue curve data from `web/src/views/home/home.vue`.
- Replaced the fake dashboard with real navigation cards for data assets, view browsing, collector rules, cloud nodes, service deployments, and storage topology.
- Deleted unused old template home components under `web/src/views/home/components/`.
- Removed the `lightweight-charts` dependency from `web/package.json` and the web pnpm lock importer/snapshot entries because it was only used by the deleted random candlestick demo component.

Rationale: the home page was reachable and displayed simulated finance/trading metrics that did not come from MooX services. Keeping those demo widgets made the admin look like it had strategy/trading state that does not exist in the current split architecture.

## 2026-07-02 cleanup: remove stale frontend test/verify entrypoints

Removed stale frontend package scripts and checks:

- Removed `build:test` from `web/package.json`; the frontend only has `.env.development` and `.env.production`, so `vite build --mode test` had no current environment config.
- Removed `verify:space-context` from `web/package.json`.
- Deleted `web/scripts/verify-space-context.mjs`.

Rationale: `verify-space-context.mjs` still referenced deleted pages such as `container-list`, `file-management`, `service-status`, and `strategy-list`. Keeping that script preserved an obsolete static test entrypoint after the related template/demo pages were removed.

Kept `verify:dev-proxy` and `verify:storage-gateway` because they check current architecture constraints: the frontend must call the gateway directly and storage control APIs must go through `/api/admin/{service}/{method}` rather than old proxy or `/api/service` paths.

## 2026-07-02 cleanup: remove old acceptance test script

Removed the old storage acceptance test entrypoint:

- Deleted `scripts/acceptance.sh`.
- Removed the `acceptance` target from the root `Makefile`.
- Updated `docs/大仓架构.md` so it no longer recommends `testdata/` or `scripts/test.sh`, and so the scripts list matches current build/deploy/helper scripts.

Rationale: `scripts/acceptance.sh` was a functional test script that depended on `~/Downloads` or old `sample-data` CSV paths. Current runtime data rebuilds should come from examples/e2e and service flows rather than preserving standalone acceptance tests.

## 2026-07-02 cleanup: remove small mock/test wording leftovers

Cleaned up wording that still suggested old test/mock paths:

- Changed the local system menu helper comment in `web/src/api/modules/system/index.ts` from "mock API response" wording to "axios-compatible local menu response" wording.
- Changed the SQLite logging example in `docs/数据库管理.md` from `test.db` to `admin_debug.db`.

Rationale: these were not runtime behavior changes, but leaving mock/test naming in current docs and source comments made the codebase look less aligned with the current no-mock-server, `admin.db` control-plane architecture.

## 2026-07-02 cleanup: remove Go mock dependency leftovers

Removed stale `github.com/golang/mock` references from module files after source-level unit tests were deleted:

- Removed indirect `github.com/golang/mock` requirements from generated proto modules:
  - `modules/collect/proto/collectgen/go.mod`
  - `modules/storage/proto/gen/go.mod`
  - `modules/admin/proto/admingen/go.mod`
  - `modules/trade/proto/tradegen/go.mod`
- Removed stale `github.com/golang/mock` checksum entries from module `go.sum` files where no source imports remain.

Rationale: the repository no longer contains Go unit tests or generated mock source that imports `gomock`. Keeping mock-only module references made the dependency graph look like test scaffolding was still part of the current source set.

## 2026-07-02 cleanup: remove remaining Go test-library dependency leftovers

Removed stale Go test-library references after confirming current Go source no longer imports them:

- Removed `github.com/stretchr/testify` requirements from module `go.mod` files.
- Removed stale `github.com/stretchr/testify`, `github.com/google/go-cmp`, and `go.uber.org/goleak` checksum entries from module `go.sum` files.

Rationale: these dependencies were left behind by deleted unit/functional tests and generated test scaffolding. The current source tree should not keep test-only libraries in active module metadata after the tests themselves have been removed.

## 2026-07-02 cleanup: remove downstream Go test dependency leftovers

Removed stale downstream test dependency references after confirming current Go source no longer imports them:

- `github.com/davecgh/go-spew`
- `github.com/pmezard/go-difflib`
- `github.com/stretchr/objx`
- `gopkg.in/check.v1`

Rationale: these packages were pulled in by deleted test libraries such as `testify`. Keeping them in module metadata after removing source-level tests made the dependency graph retain test-only baggage.

## 2026-07-02 cleanup: remove unused frontend YAML dependency

Removed the unused frontend direct YAML dependency:

- Removed `js-yaml` and `@types/js-yaml` from `web/package.json`.
- Removed their direct importer entries from `web/pnpm-lock.yaml`.
- Removed stale manual chunk references to `js-yaml` from `web/vite.config.ts` and `web/build-prod.js`.

Rationale: current frontend source no longer imports `js-yaml`; it only remained in hand-written bundle split configuration after template/demo cleanup. Keeping it as a direct dependency made the web dependency graph retain an unused utility library.

## 2026-07-02 cleanup: remove CLI workspace compatibility alias

Removed the old `--workspace` alias from `moox-cli data csv import` and `moox-cli data rows export`; these commands now expose only `--space` for the control-plane Space ID.

Also updated MooX skill references and example seed descriptions so business terminology uses `space`, `data source`, and `subject` instead of old `workspace`, `exchange`, and `instrument` wording.

Updated `modules/storage/config/metadata.seed.yaml` for the same business terminology alignment.

## 2026-07-02 cleanup: remove remaining compatibility wording

Removed small compatibility remnants:

- Removed the old single-value DNSProxy `ping_port` config field and fallback path; DNSProxy now only exposes `ping_ports`.
- Removed the frontend cloudnode status numeric fallback; cloudnode status is now treated as the current string status (`online`, `offline`, `timeout`, `abnormal`).
- Renamed storage documentation's `legacy_row_id` physical-key placeholder to `row_id`.

Rationale: the project does not need to preserve old config or status formats. Keeping compatibility fallbacks and `legacy_*` terminology made the source look like it still supported pre-split data/config shapes.

## 2026-07-02 cleanup: remove old table-shaped CLI metadata seed

Removed `modules/cli/config/metadata.yaml`.

Rationale: the file used old table-shaped keys such as `t_spaces` and `c_space_id`, while the current `moox-cli metadata import` path imports domain-oriented seed files from `examples/` through the Storage MetadataService. The file had no active references and could mislead users into thinking direct table-shaped seed YAML is still supported.

Kept `modules/cli/config/fields.yaml` because CSV import still uses it as the default column display-name configuration.

## 2026-07-02 cleanup: remove old collector table-shaped test seed

Removed `modules/cli/config/collector.yaml`.

Rationale: the file was explicitly labeled as collector data-type/form test data and used old table-shaped keys such as `t_collector_data_type_configs` and `c_data_type`. It had no active references. Collector configuration and task planning now live in the independent `modules/collector` service and examples/service flows, not in CLI-local table-shaped YAML test fixtures.

## 2026-07-02 cleanup: remove obsolete CLI message consumer

Removed the standalone CLI NATS message consumer:

- Deleted `modules/cli/cmd/message.go`.
- Deleted `modules/cli/internal/message/`.
- Removed `message` configuration from `modules/cli/internal/config/config.go` and `modules/cli/config/cli.yaml`.
- Removed `github.com/nats-io/nats.go`, `github.com/nats-io/nkeys`, and `github.com/nats-io/nuid` from `modules/cli/go.mod` / `modules/cli/go.sum`.
- Removed message queue docs and dependency notes from `modules/cli/README.md`.

Rationale: current NATS usage belongs to the storage eventbus implementation and service deployment configuration. A standalone `moox-cli msg consume` debug surface was not part of the current admin/cloudnode/collector/storage split and preserved an old client-side message consumption model.

## 2026-07-02 cleanup: remove obsolete CLI internal placeholders

Removed unused CLI internal placeholder directories:

- `modules/cli/internal/datadog/`
- `modules/cli/internal/healthcheck/`

Each directory only contained a README and had no source references.

Also updated `modules/cli/README.md` so it no longer advertises removed direct database operations or stale `database.go` / `internal/database` project structure.

Rationale: these placeholders were unrelated to the current module-owned service architecture and preserved old generic CLI scaffolding.

Rationale: MooX is a new project and does not need CLI compatibility aliases for old parameter names. Keeping `--workspace` as an alias preserved exactly the kind of compatibility surface the cleanup is meant to remove.

## 2026-07-02 追加清理：前端未引用静态资产

### 处理结论

确认删除一批来自 Vue/Vite 默认模板或旧管理台模板菜单的静态资产。这些文件不再被当前静态菜单、路由、页面组件或布局组件引用。

### 已删除文件

- `web/public/vite.svg`
- `web/src/assets/vue.svg`
- `web/src/assets/svgs/add-voucher.svg`
- `web/src/assets/svgs/classify.svg`
- `web/src/assets/svgs/data-analysis.svg`
- `web/src/assets/svgs/data-queries.svg`
- `web/src/assets/svgs/earth.svg`
- `web/src/assets/svgs/financial-statement.svg`
- `web/src/assets/svgs/firewall.svg`
- `web/src/assets/svgs/folder-close.svg`
- `web/src/assets/svgs/folder-open.svg`
- `web/src/assets/svgs/safety.svg`
- `web/src/assets/svgs/video.svg`
- `web/src/assets/svgs/wechat.svg`

### 保留说明

- `web/src/assets/img/my-image.jpg` 仍被 Header 通知、Header 用户区域、个人设置、个人信息页引用，暂不删除。
- 业务菜单仍在使用的 SVG 图标，例如 `home`、`folder-menu`、`functions`、`balance-inquiry`、`defend`、`set` 等，未纳入本次删除。

### 验证状态

- 已做静态文件名引用检查。
- 未运行前端构建、后端编译或自动化测试。

## 2026-07-02 追加检查：前端云节点管理接口

### 检查范围

- `web/src/api/admin/http.ts`
- `web/src/api/cloud-account.ts`
- `web/src/api/cloud-node.ts`
- `web/src/api/function-package.ts`
- `web/src/views/collector/cloud-function/cloud-function.vue`
- `web/src/views/collector/cloud-function/function-package-manage.vue`
- `web/src/views/collector/cloud-account/cloud-account-manage.vue`
- `modules/admin/config/gateway.yaml`
- `modules/admin/internal/gateway/forward.go`
- `modules/admin/internal/service/sysdeploy/service.go`
- `modules/cloudnode/internal/service/cloudnode/service.go`

### 结论

本地前端源码中的云账户、云节点、云函数代码包请求已经通过 `callControl('cloudnode', method, req)` 统一访问 `/api/admin/cloudnode/{method}`，没有发现继续请求旧 admin 内置云函数/云账户接口的代码。

SUPERSEDED by 2026-07-03 gateway target cleanup: admin 网关运行时只从 `t_service_deployments` active 记录解析 `cloudnode -> trpc.moox.collect.CloudNodeMgr` 的转发目标。部署记录缺失或非 active 时请求直接失败，不再回退到 `gateway.yaml`。

### 线上仍失败时的优先排查点

- 前端静态资源是否重新构建并发布，否则浏览器可能仍加载旧 bundle 和旧错误文案。
- `t_service_deployments` 中 `moox_cloudnode` 是否为 `active`，`host/port/rpc_address/gateway_path` 是否指向当前独立 cloudnode 进程。
- `moox-cloudnode` 进程是否在 `11401` 有协议 HTTP 端口正常监听。
- `/api/admin/cloudnode/ListCloudAccounts` 是否被登录态拦截，前端请求必须带 `Authorization` 或 `X-Access-Token`。

### 本轮代码处理

- 未发现需要把前端从旧接口迁移到新接口的代码改动点。
- 未运行前端构建、后端编译或自动化测试。

## 2026-07-02 追加清理：admin 迁出业务依赖与前端模板字典

### 处理结论

继续收敛 `modules/admin` 到“认证、基础运维、系统设置、网关转发”的边界，删除云节点/云函数/消息总线迁出后遗留的依赖和无调用工具。

### 已删除的 admin 依赖残留

- 从 `modules/admin/go.mod` / `modules/admin/go.sum` 删除 `github.com/tencentcloud/tencentcloud-sdk-go/tencentcloud/scf`。
- 从 `modules/admin/go.mod` / `modules/admin/go.sum` 删除 admin 侧不再使用的腾讯云 `common/kms` 与 `github.com/tencentyun/cos-go-sdk-v5` 残留。
- 从 `modules/admin/go.mod` / `modules/admin/go.sum` 删除 admin 侧不再使用的 NATS 依赖：`nats-server/v2`、`nats.go`、`jwt/v2`、`nkeys`、`nuid`。
- 从 `modules/admin/go.mod` / `modules/admin/go.sum` 删除未被 admin 代码引用的 `snowflake`、`concurrent-map/v2`、`xid`。
- 删除未被调用的 `modules/admin/internal/common/retry.go`，并移除 `github.com/avast/retry-go` 依赖。

这些依赖曾服务于云函数、云节点管理、消息总线或通用旧工具；当前相关能力已经迁移到独立 `moox-cloudnode`、`moox-collector` 或 storage 相关模块，admin 不应继续持有这些实现依赖。

### 前端模板字典清理

- 将 `web/src/api/modules/system/static-data.ts` 中来自旧模板的性别、岗位、状态、任务状态示例字典清空为稳定空数组契约。
- `getDictAPI()` 保持返回结构不变，避免影响全局 store 初始化；真实业务字典后续应由所属后台服务/API 提供。

### 保留说明

- admin 仍保留 Prometheus、SFTP、mux、Badger、GORM、JWT、localcache、tRPC 等依赖，因为当前认证、SSH、监控、网关、DNS、系统设置代码仍有直接引用。
- 未运行 `go mod tidy`、后端编译、前端构建或自动化测试。

## 2026-07-02 追加清理：运行期数据与前端一次性验证脚本

### 处理结论

继续落实“系统数据可删除并从 examples/e2e 重建”和“不保留功能/一次性验证脚本”的目标。

### 已删除内容

- 删除本地运行期 Badger 数据目录：`modules/admin/data/badger/`。
- 删除前端一次性验证脚本：
  - `web/scripts/verify-dev-proxy-targets.mjs`
  - `web/scripts/verify-storage-control-gateway.mjs`
- 删除对应 `web/package.json` 脚本入口：
  - `verify:dev-proxy`
  - `verify:storage-gateway`

### 兼容表述收敛

- `web/src/api/config.ts` 不再使用“兼容原有存储方式 / 兼容新格式”的表述。
- 删除了等价的 `code/data` 成功分支；非 `ret_info` 响应仍原样返回，由具体 API 调用方自行处理。

### 保留说明

- `collector` 模块的 `retry-go` 仍被 Binance 采集、心跳、任务状态上报等真实网络调用使用，本轮不删除。
- `docs/存储目标架构与元数据.md` 中的 `schema_migrations` 只出现在“不进入 storage 核心 schema / 删除或暂缓”的说明里，不是当前 schema 表定义残留，本轮不改。

### 验证状态

- 仅做静态扫描和文件清理。
- 未运行前端构建、后端编译、自动化测试、发布或 git。

## 2026-07-02 追加清理：旧协议注释与前端调试日志

### 当前扫描结论

- 未发现仓库内仍保留的运行期 SQLite/Badger 数据文件。
- 未发现项目自身仍保留的 `*_test.go`、`test/`、`tests/`、`verify*.mjs`、`migration/migrate` 文件。
- `collector` 模块中的 `retry-go` 是 Binance 采集、心跳和任务状态上报的真实网络重试依赖，不属于死依赖。

### 已收敛旧协议/兼容表述

- `modules/admin/proto/infra_service.proto` 与对应生成文件中的 `AppInfo` 注释不再声明“保留与旧协议一致”。
- `modules/admin/internal/service/ssh/rpc/service.go` 中 SFTP list 转换注释改为当前 RPC 响应结构转换，不再称为兼容性转换。
- `web/src/api/modules/system/index.ts` 中本地菜单响应注释不再使用兼容表述。

### 已删除前端调试残留

- 删除 `web/src/utils/crypto.ts` 中打印盐值、密钥材料、密文、加密结果、登录流程的调试日志。
- 删除 `web/src/store/modules/user-info.ts` 中打印 token、用户信息、后台响应和清理流程的调试日志。
- 删除登录页、路由守卫、菜单、路由状态、采集规则开关、云函数任务进度、代码包上传/下载中的普通 `console.log` 调试输出。
- 删除 `web/src/layout/components/Main/index.vue` 中针对旧 `CreateProject` / `StepForm` 组件的 keep-alive 排除与 DOM 残留清理逻辑。

### 保留说明

- 前端 `console.warn` / `console.error` 异常日志本轮保留，用于真实错误排查。
- `docs/存储目标架构与元数据.md` 中 `schema_migrations` 仅作为“不进入核心 schema”的设计说明保留。

### 验证状态

- 仅做静态扫描和文件清理。
- 未运行前端构建、后端编译、自动化测试、发布或 git。

## 2026-07-02 追加清理：前端旧模板菜单残留与 CLI direct 依赖

### 前端旧模板残留

- 删除 `web/src/router/index.ts` 中针对旧 `step-form` / `StepForm` 路由组件的缓存清理逻辑。当前 MooX 菜单/路由已不再使用该模板路由名。
- 删除 `web/src/layout/components/Menu/index.vue` 中 Ctrl+D 调试浮层和 `showDebugInfo` 状态。
- 删除 `web/src/lang/modules/zhCN.ts`、`web/src/lang/modules/enUS.ts` 中不在当前 `static-menu.ts` 使用的旧 admin-template 菜单翻译键，例如 project/create-project/common-project、second/third 菜单、Markdown、uigradients、vue/vite/github、旧 collector/factor/trading 页面键等。

### CLI 依赖边界收敛

- `modules/cli/go.mod` 中 `github.com/spf13/pflag` 没有被 CLI 源码直接 import，已从 direct require 挪到 indirect require。它仍作为 `cobra` 的传递依赖保留在依赖闭包中。

### 验证状态

- 仅做静态扫描和文件清理。
- 未运行前端构建、后端编译、自动化测试、发布或 git。

## 2026-07-02 追加清理：前端模板防调试、模板工具和 crypto-js 死依赖

### 前端模板防调试功能

- 删除 `web/src/utils/debug-prevention.ts`。
- 删除系统设置抽屉中的“防调试”开关。
- 删除 `theme-config` store 中的 `debugPrevention` 状态。

原因：该功能来自通用 admin 模板，会阻止右键/F12，并通过 `eval("debugger")` 循环触发断点。它不属于 MooX 管理台业务能力，也会影响开发和线上排障。

### 前端模板工具清理

- 删除未使用的图片懒加载工具 `web/src/utils/dynamic-loader.ts`，并移除 `main.ts` 中的 `setupLazyImages()` 调用。当前源码没有 `img[data-src]` 使用点。
- 删除未引用的 `web/src/utils/px2px.ts`。
- 删除未引用的全局 loading 工具 `web/src/utils/loading-page.ts`。
- 删除只服务该 loading 工具的样式 `web/src/style/model/loading-page.scss`，并从 `web/src/style/model/index.scss` 移除引用。

### 语言包与依赖清理

- 删除 `zhCN/enUS` 中未被源码引用的旧系统文案键：`not-power`、`project-address`、`switch-language-to-preview`、`please-enter-something`。
- 删除 `crypto.ts` 中不可达的 `CryptoJS` 降级分支。
- 从 `web/package.json` 和 `web/pnpm-lock.yaml` 删除 `crypto-js` 与 `@types/crypto-js`。当前非 WebCrypto 环境走 `node-forge` AES-GCM 降级实现。

### 验证状态

- 仅做静态扫描和文件清理。
- 未运行前端构建、后端编译、自动化测试、发布或 git。

## 2026-07-02 追加清理：admin/collector 无入口 Go 包

### admin 无入口包

- 删除 `modules/admin/internal/errors/errors.go`。

原因：该包没有任何 admin 源码入口引用，且包含 Node、Task、Package、Account 等迁移前业务错误码语义；当前 admin 使用各 RPC 服务自己的 `RetInfo` 返回，不再需要这套通用错误包。

### collector 无入口包

删除以下没有模块内或跨模块入口引用的旧抽象包：

- `modules/collector/internal/discovery/discovery.go`
- `modules/collector/internal/event/notifier.go`
- `modules/collector/internal/metrics/collector.go`
- `modules/collector/internal/scheduler/ratelimiter.go`
- `modules/collector/pkg/errors/errors.go`

这些包是旧 miner/通用框架抽象残留，未接入当前 `moox-collector` 控制面、SCF runtime、Binance 采集链路或 CLI 打包链路。

### 保留说明

- `modules/cloudnode/scf/runtime` 被 `modules/collector/internal/cloudruntime` 跨模块引用，保留。
- `modules/collector/pkg/packager` 被 `modules/cli/cmd/collector.go` 跨模块引用，保留。
- `modules/collector/go.mod` 中 `github.com/pkg/errors` 当前仅作为 indirect 依赖记录存在；本轮未手工改动间接依赖闭包。

### 验证状态

- 仅做静态 import 图扫描和文件清理。
- 未运行 `go mod tidy`、后端编译、前端构建、自动化测试、发布或 git。

## 2026-07-02 frontend utils dead-code cleanup

结论：本轮继续清理前端模板遗留工具代码，只保留仍被业务页面引用的工具能力。

处理结果：

- `web/src/utils/index.ts`：删除未被引用的通用工具导出 `findParentsTailRecursive`、`webDefaultLanguage`、`getTimestamp`、`appendFormData`，保留仍在使用的 `deepClone`、`arrayFlattened`、`isEmptyObject`。
- `web/src/utils/timeSeriesValidator.ts`：该时序频率校验工具无业务入口引用，删除整个文件。
- `web/src/utils/error-handler.ts`：仅保留对外导出的 `getErrorMessage`；`isError` 改为文件内私有辅助函数，删除未被外部使用的错误格式化/网络错误/鉴权错误/标准错误构造工具。
- `web/src/utils/cloud-node-job.ts`：云函数页面仍在引用，保留。

未执行：

- 未执行前端构建。
- 未执行 Go 构建。
- 未执行单元测试或端到端验证。
- 未执行 git 操作。

## 2026-07-02 CLI legacy SQL/YAML utility cleanup

结论：`modules/cli/internal/utils` 是旧数据库导入/表结构解析链路残留，当前 CLI 命令不再引用该包。

处理结果：

- 删除 `modules/cli/internal/utils/sql.go`：其中包含读取 SQL 文件、拆分 SQL 语句、解析 `CREATE TABLE` 表名/表结构的辅助函数，属于旧 `moox-cli db/import` 类能力的尾部残留。
- 删除 `modules/cli/internal/utils/yaml.go`：其中包含未被使用的通用 YAML 文件读取函数。
- 删除空目录 `modules/cli/internal/utils`。

保留说明：

- `modules/cli/cmd/data_remote.go` 与 `modules/cli/cmd/metadata.go` 仍直接使用 `gopkg.in/yaml.v3` 解析当前命令配置/示例数据，不属于本次删除范围。
- `modules/cli/internal/config/config.go` 仍使用 `gopkg.in/yaml.v2` 读取 CLI 配置，不属于旧数据库导入工具。

未执行：

- 未执行 CLI 构建。
- 未执行 Go 测试。
- 未执行 git 操作。

## 2026-07-02 storage test helper and empty frontend test route cleanup

结论：本轮继续清理测试/模板残留，不影响当前运行时链路。

处理结果：

- 删除 `modules/storage/internal/testutil/facts.go`：该文件只提供基于 `testing.T` 的 Pebble 测试辅助函数，当前仓库没有调用入口。
- 删除 `modules/storage/internal/testutil/values.go`：该文件只提供测试构造 `ColumnValue` 的辅助函数，当前仓库没有调用入口。
- 删除空目录 `modules/storage/internal/testutil`。
- 删除空目录 `web/src/views/directive/test-directive`：该目录已无页面文件和路由引用，仅为模板测试页空壳残留。

保留说明：

- `web/src/views/directive/anti-shake` 与 `web/src/views/directive/throttle` 本轮未处理；它们不是空目录，是否属于业务保留示例需要另行确认。

未执行：

- 未执行 Go 构建。
- 未执行前端构建。
- 未执行单元测试或端到端验证。
- 未执行 git 操作。

## 2026-07-02 frontend directive template residue cleanup

结论：本轮继续清理前端模板指令示例残留。

处理结果：

- 删除 `web/src/directives/modules/global/anti-shake.ts`：该全局防抖指令仅在指令安装表中注册，没有业务模板使用入口。
- 更新 `web/src/directives/index.ts`：移除 `antiShake` import 与注册项。
- 删除空目录 `web/src/views/directive/anti-shake`。
- 删除空目录 `web/src/views/directive/throttle`。
- 删除空目录 `web/src/views/directive`。

保留说明：

- `web/src/directives/modules/global/throttle.ts` 保留；当前错误页仍使用 `v-throttle` 防止重复点击返回。
- 权限相关指令 `hasPerm`、`hasRole` 保留，属于当前管理台权限控制能力。
- `custom` 指令本轮未处理，需单独确认是否仍有业务入口。

未执行：

- 未执行前端构建。
- 未执行单元测试或端到端验证。
- 未执行 git 操作。

## 2026-07-02 frontend custom directive residue cleanup

结论：`custom` 全局指令是前端模板遗留点击指令，当前管理台页面没有 `v-custom` 使用入口。

处理结果：

- 删除 `web/src/directives/modules/global/custom.ts`：该指令只包装点击事件并透传 `goodsId/event`，不属于当前管理台业务能力。
- 更新 `web/src/directives/index.ts`：移除 `custom` import 与注册项。
- 更新 `web/src/vite-env.d.ts`：删除 `@/directives/modules/custom` 的陈旧模块声明。

保留说明：

- `web/src/views/collector/collector-rules/collector-rules.vue` 中的 `custom-form-*` 是样式类名，不是 `custom` 指令引用。
- `throttle`、`hasPerm`、`hasRole` 仍为当前页面能力，继续保留。

未执行：

- 未执行前端构建。
- 未执行单元测试或端到端验证。
- 未执行 git 操作。

## 2026-07-02 frontend generated dist and SVG residue cleanup

结论：本轮检查到 `web/dist` 仍包含旧模板页面 chunk，但构建入口同时引用这些 chunk；手工删除单个构建产物会造成当前 `dist` 不一致，因此本轮不直接修改 `web/dist`，等待下一次正式前端构建刷新。

处理结果：

- 对比当前源码路由 `web/src/router/route.ts` 与菜单 `web/src/api/modules/system/static-menu.ts`，未删除任何仍被父组件引用的页面组件。
- 检查 `web/src/assets/svgs` 与当前源码引用，删除以下无引用旧模板 SVG：
- `web/src/assets/svgs/about.svg`
- `web/src/assets/svgs/directives.svg`
- `web/src/assets/svgs/github.svg`
- `web/src/assets/svgs/link.svg`
- `web/src/assets/svgs/more.svg`
- `web/src/assets/svgs/permission.svg`
- `web/src/assets/svgs/switch.svg`
- `web/src/assets/svgs/table.svg`

保留说明：

- `web/dist/static/js/anti-shake-*.js`、`test-directive-*.js`、`throttle-*.js` 等旧构建产物本轮未手工删除；它们应由下一次 `web` 构建整体刷新。
- `login/components/*`、`personal/user-settings/components/*`、`container/ssh-file-manager/*`、`collector/cloud-account/*` 虽非路由入口，但仍被父页面引用，继续保留。
- `snow`、`exit`、`lock-pwd`、`password`、`user`、`内容加载失败`、`数据时代`、`网络断开` 等 SVG 当前仍有源码引用，继续保留。

未执行：

- 未执行前端构建。
- 未执行单元测试或端到端验证。
- 未执行 git 操作。

## 2026-07-02 frontend empty dictionary chain cleanup

结论：前端仍保留了一条模板字典链路，但当前 `dictData` 已为空，业务页面没有调用 `dictFilter` / `setDictData`。该链路不再对应当前 admin 网关或后端接口能力。

处理结果：

- 删除 `web/src/api/modules/system/static-data.ts`：该文件只导出空 `dictData`。
- 删除 `web/src/store/modules/system.ts`：该 store 仅加载空字典并持久化 `dict`，无业务入口。
- 更新 `web/src/api/modules/system/index.ts`：移除 `dictData` import 与 `getDictAPI` 导出，只保留当前菜单获取逻辑 `getRoutersAPI`。
- 更新 `web/src/globals/index.ts`：移除未使用的 `dictFilter`，保留 `arcoMessage`。
- 更新 `web/src/auto-import.d.ts`：移除 `dictFilter` 的自动导入声明。

保留说明：

- `web/src/api/modules/system/static-menu.ts`、`menu-utils.ts` 与 `getRoutersAPI` 仍是当前管理台菜单来源，继续保留。
- `arcoMessage` 仍作为全局提示封装保留。

未执行：

- 未执行前端构建。
- 未执行单元测试或端到端验证。
- 未执行 git 操作。

## 2026-07-02 frontend global helper auto-import cleanup

结论：`web/src/globals` 只剩未被业务调用的模板全局提示封装；页面已直接从 `@arco-design/web-vue` 导入 `Message`，不再需要自定义 globals 自动导入目录。

处理结果：

- 删除 `web/src/globals/index.ts`：移除未使用的 `arcoMessage` 全局 helper。
- 删除空目录 `web/src/globals`。
- 更新 `web/build/vite-plugin.ts`：移除 AutoImport 的 `dirs: ["src/globals"]` 配置，避免构建时继续扫描已删除的全局 helper 目录。
- 更新 `web/src/auto-import.d.ts`：移除 `Message` 与 `arcoMessage` 的陈旧全局声明。其中 `Message` 并非 `globals/index.ts` 导出，原声明已不准确。

保留说明：

- 各页面直接导入的 `Message` 使用方式保留。
- Vue / Vue Router 的自动导入配置保留。
- Arco Resolver 的按需导入配置保留。

未执行：

- 未执行前端构建。
- 未执行单元测试或端到端验证。
- 未执行 git 操作。

## 2026-07-02 frontend mock lockfile residue cleanup

结论：前端源码和 `web/package.json` 已不再使用 `mockjs` / `vite-plugin-mock`，但 `web/pnpm-lock.yaml` 中仍残留旧 mock 依赖链的 lock 条目。

处理结果：

- 在 `web` 目录执行 `pnpm install --lockfile-only --ignore-scripts`，按当前 `package.json` 重写 `web/pnpm-lock.yaml`。
- 未恢复 mock 插件配置，未新增 mock 源码入口。

说明：

- 这一步只刷新 lockfile，没有安装后置脚本执行。
- pnpm 输出包含 supply-chain policy 检查过程；本轮不把它作为前端构建或测试验证结论。

未执行：

- 未执行前端构建。
- 未执行单元测试或端到端验证。
- 未执行 git 操作。

## 2026-07-02 frontend stale menu language key cleanup

结论：当前路由与静态菜单仍使用 `route-config` / `route-output` 动态装载链路，不能删除；但语言包中还残留旧模板父菜单 key `personal`。

处理结果：

- 更新 `web/src/lang/modules/zhCN.ts`：删除未被当前菜单/路由使用的 `menu.personal`。
- 更新 `web/src/lang/modules/enUS.ts`：删除未被当前菜单/路由使用的 `menu.personal`。

保留说明：

- `web/src/router/route-output.ts` 与 `web/src/store/modules/route-config.ts` 当前仍被路由守卫、布局菜单、标签页、登录流程使用，继续保留。
- `menu.userinfo` 与 `menu.user-settings` 当前仍用于个人信息和用户设置页面，继续保留。

未执行：

- 未执行前端构建。
- 未执行单元测试或端到端验证。
- 未执行 git 操作。

## 2026-07-02 frontend static notice demo cleanup

结论：右上角通知弹窗仍是前端模板静态演示数据，没有后台接口，也会固定显示红点，属于当前管理台无效功能残留。

处理结果：

- 删除 `web/src/layout/components/Header/components/Notice/index.vue`：移除静态 `notice/message/backlog` 示例数据和假通知列表。
- 删除空目录 `web/src/layout/components/Header/components/Notice`。
- 更新 `web/src/layout/components/Header/components/header-right/index.vue`：移除通知按钮、`Notice` import 和固定红点样式。
- 更新 `web/src/lang/modules/zhCN.ts`：删除 `system.notice`、`system.message`、`system.backlog`。
- 更新 `web/src/lang/modules/enUS.ts`：删除 `system.notice`、`system.message`、`system.backlog`。

保留说明：

- 右上角语言切换、暗色模式、全屏、系统设置、主题设置、用户菜单仍为当前管理台能力，继续保留。
- `cloud-function` 页面里的 `batchPlanNotice` 是业务提示状态，不属于本次通知模板组件。

未执行：

- 未执行前端构建。
- 未执行单元测试或端到端验证。
- 未执行 git 操作。

## 2026-07-02 frontend unused SvgAndIcon component cleanup

结论：`SvgAndIcon` 是旧模板菜单图标包装组件，当前菜单图标已经由 `layout/components/Menu/menu-item-icon.vue` 直接处理，源码中没有 `SvgAndIcon` 使用入口。

处理结果：

- 删除 `web/src/components/svg-and-icon/index.vue`。
- 删除空目录 `web/src/components/svg-and-icon`。
- 更新 `web/src/components.d.ts`：移除 `SvgAndIcon` 全局组件声明。

保留说明：

- `web/src/components/svg-icon/index.vue` 当前仍被菜单、Logo、错误页、登录页和右上角用户菜单使用，继续保留。
- `SpaceContextBar`、`LangProvider`、`MainTransition`、`VerifyCode` 当前仍有业务/布局使用入口，继续保留。

未执行：

- 未执行前端构建。
- 未执行单元测试或端到端验证。
- 未执行 git 操作。

## 2026-07-02 frontend globalProperties hook cleanup

结论：`useGlobalProperties` 是旧模板中通过 Vue `globalProperties` 间接调用 `$message` 的 helper。当前项目页面普遍直接从 `@arco-design/web-vue` 导入 `Message`，该 hook 只剩个人设置页的少量旧调用。

处理结果：

- 删除 `web/src/hooks/useGlobalProperties.ts`。
- 更新 `web/src/views/personal/user-settings/user-settings.vue`：改为直接导入并调用 `Message.info`。
- 更新 `web/src/views/personal/user-settings/components/basic-info.vue`：改为直接导入并调用 `Message.success`。
- 更新 `web/src/views/personal/user-settings/components/security-settings.vue`：改为直接导入并调用 `Message.success`。

保留说明：

- 个人设置页面本轮仅清理消息调用方式，未改动页面本身的资料展示/表单结构。
- Arco `Message` 的直接导入方式与当前登录页、数据页、采集页等用法保持一致。

未执行：

- 未执行前端构建。
- 未执行单元测试或端到端验证。
- 未执行 git 操作。

## 2026-07-02 frontend fake user settings page cleanup

结论：`/personal/user-settings` 仍是前端模板静态页面，展示固定用户资料、地区、实名认证和安全设置表单，提交只弹出本地消息，没有真实 admin API 或用户资料保存链路。继续保留入口会让用户误以为修改密码/安全设置已生效。

处理结果：

- 更新 `web/src/layout/components/Header/components/header-right/index.vue`：移除右上角用户菜单中的“修改密码”入口及跳转函数。
- 更新 `web/src/router/route.ts`：移除 `/personal/user-settings` 静态路由。
- 更新 `web/src/router/route-output.ts`：从静态路由名称白名单中移除 `user-settings`。
- 更新 `web/src/lang/modules/zhCN.ts`：删除 `system.change-password` 与 `menu.user-settings`。
- 更新 `web/src/lang/modules/enUS.ts`：删除 `system.change-password` 与 `menu.user-settings`。
- 删除 `web/src/assets/svgs/lock-pwd.svg`：该图标只服务已移除的修改密码入口。
- 删除 `web/src/views/personal/user-settings/user-settings.vue`。
- 删除 `web/src/views/personal/user-settings/components/accreditation.vue`。
- 删除 `web/src/views/personal/user-settings/components/basic-info.vue`。
- 删除 `web/src/views/personal/user-settings/components/security-settings.vue`。
- 删除空目录 `web/src/views/personal/user-settings/components` 与 `web/src/views/personal/user-settings`。

保留说明：

- `/personal/userinfo` 与右上角“个人信息”入口继续保留。
- 当前真实用户信息仍由 `user-info` store 与登录态接口负责，本轮未改变登录/登出链路。

未执行：

- 未执行前端构建。
- 未执行单元测试或端到端验证。
- 未执行 git 操作。

## 2026-07-02 frontend fake personal info page cleanup

结论：`/personal/userinfo` 是前端模板静态个人资料页，展示固定昵称、地址、邮箱、微信和个人介绍，没有读取当前登录用户信息，也没有 admin API 支撑。继续保留会让管理台出现不可信的假个人资料入口。

处理结果：

- 更新 `web/src/layout/components/Header/components/header-right/index.vue`：移除右上角用户菜单中的“个人信息”入口及跳转函数。
- 更新 `web/src/router/route.ts`：移除 `/personal/userinfo` 静态路由。
- 更新 `web/src/router/route-output.ts`：从静态路由名称白名单中移除 `userinfo`。
- 更新 `web/src/lang/modules/zhCN.ts`：删除 `system.personal-information` 与 `menu.userinfo`。
- 更新 `web/src/lang/modules/enUS.ts`：删除 `system.personal-information` 与 `menu.userinfo`。
- 删除 `web/src/views/personal/userinfo/userinfo.vue`。
- 删除空目录 `web/src/views/personal/userinfo` 与 `web/src/views/personal`。
- 删除 `web/src/assets/svgs/user.svg`：该图标只服务已移除的个人信息入口。

保留说明：

- 右上角用户昵称/头像展示仍来自 `user-info` store，继续保留。
- 退出登录入口仍保留。
- 本轮未修改登录态接口或用户信息 store。

未执行：

- 未执行前端构建。
- 未执行单元测试或端到端验证。
- 未执行 git 操作。

## 2026-07-02 collector unused source placeholder cleanup

结论：`modules/collector/configs/config.yaml` 仍保留 OKX、Twitter、Coindesk、Ethereum 等禁用 source 占位，但仓库中没有对应配置文件，也没有当前采集实现读取这些分类。SCF 包会复制该配置树，继续保留不存在路径会造成误导。

处理结果：

- 更新 `modules/collector/configs/config.yaml`：只保留当前存在且可读取的 `market.binance` source，删除指向不存在文件的 OKX、Twitter、Coindesk、Ethereum 占位项。
- 更新 `modules/collector/pkg/config/local_config.go`：`SourcesConfig` 收窄为当前实际使用的 `Market`，删除未读取的 `Social`、`News`、`Blockchain` 字段；默认配置只保留 Binance source。

保留说明：

- `modules/collector/configs` 目录本身仍被 SCF 打包脚本和 Binance 配置解析使用，不能删除。
- `modules/collector/configs/sources/market/binance.yaml` 继续保留。
- `modules/cli/cmd/storage_import.go` 与 `modules/cli/config/fields.yaml` 仍是当前数据导入/字段展示能力，未删除。

未执行：

- 未执行 Go 构建。
- 未执行单元测试或端到端验证。
- 未执行 git 操作。

## 2026-07-02 collector unsupported capability cleanup

结论：collector 当前只有 Binance 相关实现，但 SCF keepalive 和本地心跳仍上报 OKX/Huobi 能力，`CollectorTypeOKX` 与 `CollectorTypeHuobi` 也只是无实现常量。这会误导 cloudnode/admin 认为节点支持不存在的采集器。

处理结果：

- 更新 `modules/collector/pkg/model/types.go`：删除未实现的 `CollectorTypeOKX`、`CollectorTypeHuobi` 常量。
- 更新 `modules/collector/internal/cloudfunction/handler.go`：SCF keepalive 的 `Capabilities` 只上报 `binance`。
- 更新 `modules/collector/internal/heartbeat/heartbeat.go`：本地心跳节点信息的 `Capabilities` 只上报 `binance`。

保留说明：

- `data_source` 字段继续保留，作为采集规则/任务参数中的通用数据源标识。
- 未来接入 OKX/Huobi 时，应同时补齐 adapter/source/exchange 实现、配置文件、元数据 seed 和能力上报。

未执行：

- 未执行 Go 构建。
- 未执行单元测试或端到端验证。
- 未执行 git 操作。

## 2026-07-02 collector unused ticker/orderbook/trade cleanup

结论：collector 当前实际实现和节点能力只支持 Binance K 线/标的采集。`ticker`、`orderbook`、`trade` 仍以禁用配置、未使用模型和任务类型常量形式残留，但没有对应采集器、adapter 或写入链路。

处理结果：

- 更新 `modules/collector/pkg/model/types.go`：只保留当前支持的 `TaskTypeKLine`，删除未实现的 `ticker`、`orderbook`、`trade` 常量。
- 更新 `modules/collector/pkg/config/task_instance.go`：任务缓存注释收窄为当前支持 `kline`。
- 更新 `modules/collector/internal/collector/interface.go`：采集器接口注释收窄为当前支持 `kline`。
- 删除 `modules/collector/internal/model/market/ticker.go`。
- 删除 `modules/collector/internal/model/market/orderbook.go`。
- 重写 `modules/collector/configs/sources/market/binance.yaml`：只保留当前代码实际读取的 `api` 与 `storage.bindings`，删除未读取的 `auth`、`collectors.ticker`、`collectors.orderbook`、`collectors.trade`、`processing`、`monitoring`、`advanced` 等占位配置。

保留说明：

- K 线模型 `modules/collector/internal/model/market/kline.go` 仍被 Binance K 线采集器使用，继续保留。
- `data_source` 字段继续保留，用于采集规则/任务实例标识数据源。
- 未来接入 ticker/orderbook/trade 时，应补齐采集器实现、配置结构、元数据 seed、storage 写入和能力上报，再恢复相应类型。

未执行：

- 未执行 Go 构建。
- 未执行单元测试或端到端验证。
- 未执行 git 操作。

## 2026-07-02 追加清理：collector 旧任务调度模型收窄

### 已删除

- `modules/collector/pkg/model/types.go` 中旧的 `Task` / `TaskStats` 结构体。
- `modules/collector/pkg/model/types.go` 中仅为旧本地任务调度抽象服务的 `TaskType` / `TaskStatus` enum 及未使用常量。
- `modules/collector/pkg/model/types.go` 中未被 collector 当前链路引用的 `NodeStatus` enum。

### 判断依据

- 当前 collector 的任务实例执行链路使用 `modules/collect/proto/collectgen` 生成协议、`modules/collector/pkg/config/task_instance.go` 和 `internal/executor`，不再消费 `model.Task`。
- 心跳链路仍使用 `TaskSummary` / `NodeMetrics` / `HeartbeatPayload` / `LocalDNSReportItem`，因此这些结构保留。
- SCF 入口仍使用 `CloudFunctionEvent` / `Response` / `EventActionTask` / `EventActionKeepalive`，因此这些结构和 action 常量保留。

### 当前策略

- `TaskSummary` 仍作为心跳响应契约保留，但 `type` / `status` 字段改为普通字符串，避免公共 model 包继续暴露已废弃的旧任务调度 enum。
- 未执行构建或测试；如需确认编译结果，建议后续显式执行 collector/admin 相关构建。

## 2026-07-02 追加复核：云函数页面云账户接口

### 源码结论

- `web/src/api/cloud-account.ts` 的云账户接口已经统一通过 `callControl('cloudnode', ...)` 请求 `/api/admin/cloudnode/*`。
- `web/src/api/cloud-node.ts` 的云节点列表、详情、创建、删除、部署等接口已经统一请求 `/api/admin/cloudnode/*`。
- `web/src/api/function-package.ts` 的云函数代码包接口已经统一请求 `/api/admin/cloudnode/*`。
- SUPERSEDED by 2026-07-03 gateway target cleanup: `modules/admin/config/gateway.yaml` 不再维护 `cloudnode` 静态服务地址；admin 本身不注册 cloudnode 实现，只通过 `t_service_deployments.moox_cloudnode` active 记录转发。
- SUPERSEDED by 2026-07-03 gateway target cleanup: `modules/admin/internal/service/sysdeploy/service.go` / gateway resolver 不再提供 gateway yaml 静态地址回退；部署表无 active 可用记录时请求直接失败。

### 线上排查信号

- `http://106.53.107.122:11000/api/admin/health` 仍返回了本地源码中已经移除的 `asynctask` service id，说明线上 admin 配置或发布物可能不是当前代码状态。
- `cloudnode` 源码接口路径已经对齐；若页面仍提示 `ListCloudAccounts` 失败，优先检查线上 `moox-cloudnode` 进程、`11401` 监听状态、以及 `t_service_deployments.moox_cloudnode` 的 active 地址是否指向当前独立 cloudnode 进程。

### 未执行项

- 未执行构建、测试、发布或 git 操作。

## 2026-07-02 追加清理：云函数页面旧路由上下文判断

### 已修改

- `web/src/views/collector/cloud-function/cloud-function.vue` 不再通过旧路径 `/collector/cloud-function` / `/factor/cloud-function` 判断代码包类型、运行时和业务类型。
- 当前有效路由是 `/collector/functions`，页面语义是采集云节点管理，因此 `package_type` 固定为 `data_collector`，默认 runtime 固定为 `Go1`，`biz_type` 固定为 `data_collector`。
- 移除了该页面不再需要的 `useRoute` 依赖。

### 判断依据

- `web/src/router/route.ts` 当前只注册 `/collector/functions` 和 `/collector/packages`，未注册 `/collector/cloud-function` 或 `/factor/cloud-function`。
- 旧判断会在当前页面让 `currentPackageType` 返回 `undefined`，导致代码包列表和代码包选择不再按采集器包类型过滤。

### 未执行项

- 未执行构建、测试、发布或 git 操作。

## 2026-07-02 追加清理：删除空壳目录

### 已删除空目录

- `modules/admin/data`
- `modules/collector/configs/sources/blockchain`
- `modules/collector/configs/sources/news`
- `modules/collector/configs/sources/social`
- `web/src/api/modules/file`
- `web/src/api/modules/table`
- `web/src/views/container/container-list`
- `web/src/views/container/file-management`
- `web/src/views/container/service-status`
- `web/src/views/file/document-library/components`
- `web/src/views/functions/routing-operation`
- `web/src/views/home/components`
- `web/src/views/strategy/strategy-list`
- `web/src/views/table/common-table`
- `web/src/views/table/custom-table`

### 判断依据

- 这些目录已经没有文件内容，主要来自此前删除的模板/demo/mock 页面、旧 collector source 类型和旧 admin runtime 数据目录。
- 保留这些空目录会让源码树继续暗示不存在的旧功能仍在维护。

### 未执行项

- 未执行构建、测试、发布或 git 操作。

## 2026-07-02 追加清理：admin DB 文档路径对齐

### 已修改

- `docs/数据库管理.md` 的 SQLite 备份、导出、恢复示例从当前目录 `admin.db` 改为 `./data/admin.db`。

### 判断依据

- 当前 admin SQLite 配置统一使用 `./data/admin.db`，部署时映射到 `<deploy-dir>/data/admin.db`。
- 备份示例继续写 `admin.db` 容易让用户误以为数据库文件位于工作目录根部。

### 未执行项

- 未执行构建、测试、发布或 git 操作。

## 2026-07-02 追加清理：collector source 注释对齐

### 已修改

- `modules/collector/pkg/config/task_instance.go` 中 `DataSource` 注释从 `binance, okx 等` 改为当前内置 `binance`、未来可扩展。
- `modules/collector/internal/collector/interface.go` 中 `Source()` 注释同步改为当前内置 `binance`、未来可扩展。

### 判断依据

- 当前 collector 配置和能力上报只保留 Binance K 线采集，OKX/Huobi/Twitter/Coindesk/Ethereum 等旧占位源已经移除。
- 注释继续列举 OKX 会让后续接入者误以为已有可用实现或配置。

### 未执行项

- 未执行构建、测试、发布或 git 操作。

## 2026-07-02 追加清理：剩余空目录与 mock 注释措辞

### 已删除空目录

- `docs/superpowers`
- `modules/storage/data`
- `modules/trade/data`
- `web/src/views/file/document-library`
- `web/src/views/functions`
- `web/src/views/strategy`
- `web/src/views/table`

### 已修改

- `modules/trade/internal/service/service.go` 中 `WithExchangeFactory` 注释不再写“测试注入 mock”，改为“便于替换交易所适配实现”。

### 判断依据

- 这些目录没有文件内容，主要来自已删除的旧文档/模板页面或本地运行数据目录。
- `WithExchangeFactory` 是正常依赖注入扩展点，不是测试代码；去掉 mock/test 措辞可避免把测试语义误读为当前产品源码的一部分。

### 未执行项

- 未执行构建、测试、发布或 git 操作。

## 2026-07-02 追加复核：admin / cloudnode 边界

### 复核结论

- `modules/admin/schema/admin.sql` 中未包含 `t_cloud_*`、云账户、云函数包、云节点或 AsyncTask 表结构。
- `modules/admin/proto` 与 `modules/admin/internal/service` 中未发现 `CloudNodeMgr` / `CollectMgr` / `PackageMgr` / `AsyncTask` 的实现或协议定义。
- admin 侧仅在 `service_deployments_seed.sql` 与 `sysdeploy/defaults.go` 中保留 `moox_collector`、`moox_cloudnode` 的独立部署记录，供网关转发和服务发现使用。
- 云节点、云账户、云函数包、异步 job 和 invocation 的表结构集中在 `modules/cloudnode/schema/cloudnode.sql`。
- 采集与云节点协议集中在 `modules/collect/proto/collect_service.proto`，由 `modules/collector` / `modules/cloudnode` 独立服务实现。

### 判断依据

- 对 `modules/admin/schema`、`modules/admin/proto`、`modules/admin/internal/bootstrap`、`modules/admin/internal/service` 的云节点/云账户/AsyncTask/CollectMgr 关键词扫描只命中服务部署记录和“已拆分由网关转发”的注释。
- 对 `modules/cloudnode/schema` 与 `modules/collect/proto` 的扫描命中对应独立服务的表结构和协议定义，符合当前拆分边界。

### 未执行项

- 未执行构建、测试、发布或 git 操作。

## 2026-07-02 追加清理：测试/coverage 产物残留

### 复核结论

- 当前业务源码中未发现 Go `_test.go`、前端 `*.test.ts` / `*.spec.ts` / `*.test.vue` / `*.spec.vue` 文件。
- 当前构建脚本中未发现 `go test`、`pnpm test`、`vitest`、`jest`、`playwright`、`cypress`、`gomock`、`mockgen` 等测试入口或测试依赖引用。

### 已修改

- `Makefile` 的 `clean` 目标不再删除 `coverage`。
- `modules/storage/Makefile` 的 `clean` 目标不再删除 `coverage` / `cover.out` / `cover.out.tmp`。
- `modules/cli/Makefile` 的 `clean` 目标不再删除 `coverage`。

### 判断依据

- coverage/cover.out 已没有对应测试或覆盖率生成入口，继续保留清理项会暗示仓库仍维护旧测试/覆盖率流程。
- `.codex/skills` 与 `openspec` 目录属于开发流程资料，不作为产品功能单测代码删除。

### 未执行项

- 未执行构建、测试、发布或 git 操作。

## 2026-07-02 追加清理：前端架构文档结构对齐

### 已修改

- `docs/架构总览.md` 的 `web/src` 目录结构示例删除已不存在的 `views/strategy` 页面目录。
- 同一结构示例中的 admin/storage API 封装文件名从旧的 `callControl.ts` / `callMetadata.ts` / `callAccess.ts` / `callView.ts` 更新为当前实际的 `admin/http.ts`、`admin/{spaces,secret,sysdeploy}.ts` 与 `storage/{http,metadata,access,view}.ts`。
- 增补 `api/trade` 作为当前 Trade 网关封装目录。

### 判断依据

- 当前 `web/src/router/route.ts` 和 `web/src/api/modules/system/static-menu.ts` 已不再暴露策略占位页面。
- 当前 `web/src/api` 目录下不存在旧 `call*.ts` 文件，实际封装集中在 `admin/http.ts`、`storage/http.ts` 等文件。

### 未执行项

- 未执行构建、测试、发布或 git 操作。

## 2026-07-02 追加清理：MooX skill 脚本描述对齐

### 已修改

- `skills/moox/SKILL.md` 的 repository layout 中 `scripts` 描述不再提已删除的 `acceptance` 脚本。
- 新描述对齐当前脚本职责：root build/release/deploy、collector SCF package、storage helper、node_exporter operation scripts。

### 判断依据

- 当前 `scripts/` 目录不存在 `acceptance.sh` 或 acceptance 入口。
- 当前 `scripts/` 保留 `build.sh`、`release.sh`、`deploy-moox.sh`、`build-collector-scf-package.sh`、`deploy-collector-scf-package.sh`、`storage-start.sh`、`storage-stop.sh`、`package-skill.sh` 和 `node_exporter/`。

### 未执行项

- 未执行构建、测试、发布、git 或 skill subagent 验证。

## 2026-07-02 追加清理：MooX skill acceptance 流程残留

### 已修改

- `skills/moox/SKILL.md` 的 Common Commands 删除已不存在的 `make acceptance`。
- `skills/moox/SKILL.md` 删除旧的 CSV acceptance import 示例，不再指向 `~/Downloads/APT-USDT.csv` 这类本地一次性数据文件。
- `skills/moox/references/release.md` 删除 `CSV_DIR` / `STORAGE_URL` acceptance 变量说明，以及“部署后运行 CSV acceptance 脚本”的旧描述。
- release reference 改为：部署上传 binaries/docs/skills/build scripts/configs/examples，运行时数据可从 `examples/` 和服务流程重建。

### 判断依据

- 当前根 Makefile 不再有 `acceptance` target，`scripts/` 目录也不存在 `acceptance.sh`。
- 当前目标明确允许删除运行时数据，并从 examples/e2e 与服务流程重建数据，不应保留 standalone CSV acceptance 流程。

### 未执行项

- 未执行构建、测试、发布、git 或 skill subagent 验证。

## 2026-07-02 追加清理：MooX skill collector 描述对齐

### 已修改

- `skills/moox/SKILL.md` 中 `modules/collector` 描述不再指向已删除的 `discovery/source/scheduler` 旧组织方式。
- 新描述对齐当前 collector 边界：独立 CollectMgr 服务、planner、executor、cloudruntime、source adapters 和 SCF runtime entrypoints。

### 判断依据

- `modules/collector/internal/discovery` 与 `modules/collector/internal/scheduler` 已经删除。
- 当前 collector 目录中仍存在并使用 `internal/service/collectmgr`、`internal/planner`、`internal/executor`、`internal/cloudruntime`、`internal/source`、`internal/adapters` 等组织。

### 未执行项

- 未执行构建、测试、发布、git 或 skill subagent 验证。

## 2026-07-02 追加清理：大仓架构文档 coverage/test wording

### 已修改

- `docs/大仓架构.md` 的运行产物忽略示例删除 `/coverage/` 与 `/modules/*/coverage/`。
- 同一段将“测试样例”改为 `examples` 示例。

### 判断依据

- 当前仓库已经没有测试/coverage 入口，Makefile clean 目标也不再处理 coverage 产物。
- 运行时数据重建方向应以 `examples/` 和服务流程为准，而不是保留测试样例/coverage 口径。

### 未执行项

- 未执行构建、测试、发布或 git 操作。

## 2026-07-02 追加清理：CLI 防火墙命令旧迁移文案

### 已修改

- `modules/cli/cmd/tencent_ops_firewall_open.go` 的命令帮助不再提 `admin/cmd/open-lighthouse-firewall` 或“从本地 SQLite DB 解密”。
- 新描述明确云账户归属独立 `moox-cloudnode` 服务，CLI 通过 `/api/service/cloudnode/*` HMAC 后台接口从控制面获取凭证。

### 判断依据

- admin 已不再拥有云账户/云函数/云节点实现，也不应保留“本地 SQLite 解密云凭证”的旧迁移口径。
- 当前 CLI 命令行为已经通过控制面后台 API 获取云账户凭证，本轮仅清理帮助文本。

### 未执行项

- 未执行构建、测试、发布或 git 操作。

## 2026-07-02 追加清理：CLI import 示例一致性

### 已修改

- `modules/cli/README.md` 中 `storage import` 示例的 `--subject` 从 `AR-USDT` 改为 `ARB-USDT`，与示例文件 `ARB-USDT.csv` 保持一致。

### 判断依据

- 该示例是普通 CLI 历史数据导入用法，不属于已删除的 acceptance 脚本；本轮只修正示例笔误，避免后续用 examples/service flows 重建数据时复制出错误 subject。

### 未执行项

- 未执行构建、测试、发布或 git 操作。

## 2026-07-02 追加清理：CLI metadata import 示例转向 examples

### 已修改

- `modules/cli/cmd/metadata.go` 的 `metadata import` 帮助示例不再推荐 `../storage/config/metadata.seed.yaml`。
- 示例改为导入 `../../examples/platform-local.seed.yaml` 和 `../../examples/metadata-crypto.seed.yaml`，与当前从 `examples/` 重建运行数据的方向一致。

### 判断依据

- `examples/README.md` 已经将平台拓扑和业务空间 seed 作为当前推荐的元数据重建入口。
- `storage/config/metadata.seed.yaml` 属于模块内部配置样例，不应作为系统级数据重建的主入口展示。

### 未执行项

- 未执行构建、测试、发布或 git 操作。

## 2026-07-02 追加清理：CLI README metadata seed 示例

### 已修改

- `modules/cli/README.md` 的元数据初始化示例从 `../storage/config/metadata.seed.yaml` 改为 `../../examples/platform-local.seed.yaml`。
- dry-run 示例改为 `../../examples/metadata-crypto.seed.yaml`。

### 判断依据

- 当前系统级数据重建入口应集中在 `examples/`。
- `modules/cli/README.md` 已说明 CLI 通过 `MetadataService` 导入，不直接写 SQLite 表；本轮只修正示例 seed 来源。

### 未执行项

- 未执行构建、测试、发布或 git 操作。

## 2026-07-02 追加清理：collector 架构文档与 Binance seed 描述

### 已修改

- `examples/metadata-crypto.seed.yaml` 与 `modules/storage/config/metadata.seed.yaml` 中 crypto space 描述从 `Binance/OKX style` 改为 Binance-only 描述。
- `docs/index.md` 的采集能力说明不再写“已预留 OKX、Twitter、CoinDesk、Ethereum 等数据源扩展点”，改为当前内置 Binance，后续可扩展更多数据源。
- `docs/架构总览.md` 的 Collector 模块结构从旧的 registry/scheduler/pkg/binance 组织更新为当前独立 `moox-collector`、`moox-collector-scf`、`collectmgr`、`planner`、`executor`、`cloudruntime`、`adapters/binance`、`storageclient` 组织。
- `docs/架构总览.md` 的 Collector 接口说明从旧 `Collect()/Name()/Type()` 更新为当前 `Source()/DataType()/Collect()`，并说明新增数据源需要补齐实现、配置、seed 和能力上报。

### 判断依据

- 当前 collector 配置和能力上报只保留 Binance；OKX/Twitter/CoinDesk/Ethereum 占位 source 已移除。
- 当前 collector 目录中不存在旧 `internal/discovery`、`internal/scheduler` 或 `pkg/binance` 结构。

### 未执行项

- 未执行构建、测试、发布或 git 操作。

## 2026-07-02 追加清理：CLI 旧 data csv import 入口

### 已删除

- `modules/cli/cmd/data.go` 中旧的 `moox-cli data csv import` 命令、flags 和 CSV K 线解析 helper。
- `modules/cli/cmd/data_remote.go` 中 `data csv import` 独占的远端导入 helper：导入前自动创建 Space/DataSource/Subject/Dataset/PrimaryStoreRoute/Column 的逻辑。

### 保留

- `moox-cli data rows export` 暂时保留，仍作为通过 Storage Access 读取 TimeSeries 行并导出 JSON 的工具。
- `postStorage` / `postStorageRaw` / `checkStorageRetInfo` 等 HTTP 调用 helper 保留，因为 `metadata import` 与 `storage import` 仍在复用。

### 判断依据

- 当前 README 与 `storage` 命令都推荐 `moox-cli metadata import` 先导入 `examples/` seed，再用 `moox-cli storage import --format csv` 写入历史数据。
- 旧 `data csv import` 会在写数据前自动补齐元数据和本地主存路由，绕开 examples/service flows 的显式重建路径，容易重新形成一次性导入工具口径。

### 未执行项

- 未执行构建、测试、发布或 git 操作。

## 2026-07-02 追加清理：Workspace/Exchange/Instrument 旧协议术语

### 已修改

- `docs/大仓架构.md` 中 admin 职责从业务 `Workspace` 改为 `Space`。
- `skills/moox/references/protocol.md` 的新 API 推荐词表从旧 `Workspace` / `Exchange` / `Instrument` / `DataView` / `StorageRoute` 改为当前 `Space` / `DataSource` / `Subject` / `SubjectSymbol` / `Dataset` / `DatasetSubject` / `View` / `PrimaryStoreRoute` 等。
- 同一文件的禁用概念补充：不要把 `Workspace` 当业务域别名，不要把 `Exchange` 当公开 data-source 概念，不要把 `Instrument` 当公开 subject 概念。

### 判断依据

- 当前 public API 与文档已经统一使用 Space、DataSource、Subject、Dataset、View 等概念。
- Go Workspace 作为构建概念保留；本轮仅清理业务协议术语中的旧 Workspace。

### 未执行项

- 未执行构建、测试、发布、git 或 skill subagent 验证。

## 2026-07-02 追加修复：cloudnode 管理接口错误透传

前端云账户、云节点、云函数代码包请求已经统一通过 `callControl('cloudnode', ...)` 访问 `/api/admin/cloudnode/*`，没有发现继续调用旧 admin 内置云函数/云账户接口的活跃代码。

本次修复了错误展示链路：

- `web/src/api/admin/http.ts` 在缺少登录 token 时直接提示登录态失效，避免继续发起必然失败的管理接口请求。
- `web/src/api/admin/http.ts` 会优先透出网关或 RPC 返回的 `ret_info.msg` / `message` / `msg`，不再只显示 Axios 通用错误。
- `web/src/views/collector/cloud-account/cloud-account-manage.vue`、`web/src/views/collector/cloud-function/cloud-function.vue`、`web/src/views/collector/cloud-function/function-package-manage.vue` 不再把所有云账户加载失败都覆盖成“moox-cloudnode 未部署”。

这样如果线上页面仍提示加载云账户失败，可以直接区分是未登录、网关未配置、`moox-cloudnode` 未启动，还是 CloudNodeMgr 业务返回错误。

## 2026-07-02 追加清理：测试/迁移文字残留

继续按当前目标复核 `data/moox.db -> data/admin.db`、独立 cloudnode/collector 边界和死代码清理状态：

- 未发现活跃 `data/moox.db` 引用；admin 配置和文档示例均指向 `data/admin.db`。
- 未发现真实 `*_test.go`、`.test.ts`、`.spec.ts`、coverage 产物或迁移文件名残留。
- 未发现前端继续调用 `/api/admin/cloud-function`、旧 `PackageMgr`、旧 `AsyncTask` / `CreateAsyncJob` 的活跃代码。
- 未发现脚本绕过 `moox-cloudnode` 直接读取远端 `t_cloud_accounts` / cloudnode SQLite 的旧逻辑。

本次删除/改写了几处会误导维护者继续沉淀项目自有测试代码的文档残留：

- `docs/存储引擎架构.md`：将“极简单元测试”模式改为“本地单进程或临时验证”，并明确验证脚本不沉淀为项目自有单元测试。
- `modules/cli/README.md`：将“编写完整的单元测试和集成测试”改为通过 `examples/` 与运行期服务流程重建端到端数据。
- `web/README.md`：移除不存在的 `.env.test` 测试环境配置条目。

## 2026-07-02 追加修复：SecretMgr cloud 分类漏清

复核 admin 依赖与服务实现时发现 `modules/admin/internal/service/secret/rpc/service.go` 的 `validCategories` 仍允许 `cloud` 分类。schema、proto 注释和前端设置页已经只保留 `ssh`、`exchange`、`database`、`jwt`、`other`，因此这是 admin/cloudnode 拆分后的漏网残留。

已删除 SecretMgr RPC 后端校验中的 `cloud` 分类，避免通过 `/api/admin/secret/*` 继续创建 admin 本地云账户凭证。云账户、COS 凭证和云函数包上传凭证继续归独立 `moox-cloudnode` 服务与 `t_cloud_accounts` 表管理。

## 2026-07-02 追加清理：admin go.mod 旧 storage proto replace

复核 admin 模块依赖时发现 `modules/admin/go.mod` 仍保留：

```text
replace github.com/mooyang-code/moox/modules/storage/proto/gen => ../storage/proto/gen
```

当前 admin 源码没有 import `modules/storage/proto/gen`，也没有对应 require；storage、collector、cloudnode 请求均通过 admin 网关的 `serviceID -> address/path` 配置转发，不需要 admin 直接依赖 storage 生成协议包。

已删除该无用 replace，减少 admin 与 storage 协议包的静态耦合。`trpc.group/trpc-go/trpc-log-cls` 仍保留，因为 `modules/admin/cmd/moox-admin/main.go` 仍 blank import 该日志插件；它不是旧云函数/云节点业务实现依赖。

## 2026-07-02 追加清理：SSH RPC import 占位死代码

复核 active 页面和服务实现中的占位/假功能残留时，发现 `modules/admin/internal/service/ssh/rpc/service.go` 末尾仍有：

```go
// ensure fmt used (避免 import 未使用报错占位，后续若 extractClientIP 扩展可移除)
var _ = fmt.Sprintf
```

当前 `extractClientIP` 仅返回空字符串，`fmt` 没有实际业务用途。这属于为避免 import 未使用而保留的占位死代码。

已删除 `fmt` import 和 `var _ = fmt.Sprintf`。本次扫描还复核了旧 `async-task` / `cloud-function-async` / `CreateAsyncJob` / `data/moox.db` / `moox.db` / 测试与迁移关键词，未发现新的活跃代码残留；文档中保留的 `PackageMgr` 与 `schema_migrations` 命中均处于“已迁移/不维护/删除列表”语境。

## 2026-07-02 追加清理：collector 心跳旧本地任务管理器 TODO

复核脚本、技能和 collector 运行时代码时，没有发现新的 CSV acceptance、迁移脚本、测试文件、coverage 产物、`data/moox.db` 或直接读取远端 cloudnode SQLite 的活跃残留。

发现 `modules/collector/internal/heartbeat/heartbeat.go` 中 `getRunningTasks` / `getTaskStatistics` 仍保留“从任务管理器获取实际任务/统计”的 TODO。当前设计中云函数 runtime 只通过 cloudnode/collector 协议拉取任务并上报结果，任务状态与统计以 cloudnode/collector 为准，不应补回云函数本地任务管理器。

已将这两个 TODO 改为当前设计说明：runtime 不维护本地运行任务快照或本地任务统计，避免后续误按旧本地任务管理模型继续实现。

## 2026-07-02 追加清理：采集规则页未实现数据源选项

复核 collector/cloudnode 采集能力时确认：当前运行时上报和 collector 实现只包含 Binance。`modules/collector/pkg/model/types.go` 仅保留 `CollectorTypeBinance`，心跳与云函数 probe 也只上报 Binance 能力。

发现 `web/src/views/collector/collector-rules/collector-rules.vue` 的数据源兜底选项仍暴露 OKX、Huobi、Bybit 等未实现采集器。如果后端数据类型配置缺失、格式错误，或旧配置里带着历史预留数据源，管理台会允许用户创建不可执行的采集规则。

已在采集规则页增加当前支持数据源白名单，仅暴露 `binance`；从后端配置解析出的数据源也会过滤到该白名单。保留 `getDataSourceLabel` 中的历史标签只是为了展示旧记录时可读，不再作为可选能力暴露。

## Cleanup pass: root docs package lock

Root `package.json` is still retained because it is the VitePress docs entrypoint (`docs:dev`, `docs:build`, `docs:preview`).

Removed `package-lock.json` from the repository root because the root package declares pnpm as the package manager, so the npm lockfile was a stale generated artifact.

Updated `web/README.md` to document `pnpm-lock.yaml` instead of the already-removed `package-lock.json`.

Local `node_modules` directories were left untouched because they are workspace dependency caches rather than project source/dead code.

## Cleanup pass: frontend README script examples

Removed the stale `build:test` example from `web/README.md`.

Rationale: the actual frontend package no longer exposes `build:test`, and the project only keeps current development and production build modes in the documented startup snippet.

The `schema_migrations` references in `docs/存储目标架构与元数据.md` were retained because that document lists it under tables that should be deleted or kept out of the storage core schema.

## Cleanup pass: empty frontend directories

Removed empty frontend directories left after earlier source cleanup:

- `web/scripts`
- `web/src/views/file`

Retained the empty `openspec/changes` workspace directory because it belongs to the local OpenSpec workflow rather than application runtime/source code.

## Cleanup pass: cloudnode admin API routing

Scanned cloudnode/cloud account/cloud function references in frontend, modules, docs, and skills.

Conclusion:

- Frontend cloud account, cloud node, and package APIs call `callControl('cloudnode', ...)`, so they route through `/api/admin/cloudnode/*` instead of old admin-local implementations.
- Active admin-side cloudnode references are gateway/deployment/default-service descriptions, not business implementations.
- `modules/collect/proto` is retained. It is the active shared protocol module used by `modules/cloudnode` and `modules/collector`, even though cloudnode and collector are independently deployed services.

## Cleanup pass: frontend cloudnode runtime wrapper

Removed unused `nodeHeartbeat` from `web/src/api/cloud-node.ts`.

Rationale: cloudnode heartbeat belongs to the SCF/runtime `/api/service/cloudnode/*` path. The frontend wrapper called `ReportHeartbeat` through the admin control path and had no call sites, so keeping it blurred the management API/runtime API boundary.

The backend `ReportHeartbeat` protocol and SCF runtime callers are still active and retained.

## Cleanup pass: unused frontend cloudnode API wrappers

Removed unused frontend exports from `web/src/api/cloud-node.ts`:

- `getNodeDetail`
- `deleteNode`
- `updateNodeLoad`
- `updateNodeFunction`

Rationale: these wrappers had no frontend call sites. The current cloud function management page uses the list/update/batch-create/batch-deploy/batch-delete/list-region APIs, with create/delete/deploy operations submitted as cloudnode jobs.

## Cleanup pass: unused frontend cloud account/package API wrappers

Removed unused frontend exports:

- `getCloudAccountDetail` from `web/src/api/cloud-account.ts`
- `getCloudAccountsByProvider` from `web/src/api/cloud-account.ts`
- `getFunctionPackageOptions` from `web/src/api/function-package.ts`

Rationale: these wrappers had no frontend call sites. The remaining frontend cloud account/package APIs map to the active management pages.

Removed the now-unused `PackageOption` type from `web/src/api/function-package.ts` after deleting the unused package-options wrapper.

## Cleanup pass: unused frontend API wrappers

Removed frontend API wrappers that had no active call sites and do not require proto/generated-code changes:

- `NODE_STATUS`, `NODE_TYPE`, `getStatusText`, and `getNodeTypeText` from `web/src/api/cloud-node.ts`.
- `enableMonitor`, `disableMonitor`, `getMonitorStatus`, `testNodeExporter`, and the unused `TestResult` type from `web/src/api/modules/host-monitor.ts`.
- `execSSHCommand` from `web/src/api/modules/ssh.ts`.
- `getLoginSaltAPI` from `web/src/api/modules/user/index.ts`.

Rationale:

- Cloud node pages use page-local status text helpers and the active cloudnode job APIs.
- The resource monitor page only calls current/history metrics and byte formatting helpers.
- The SSH terminal/file pages do not call the one-off command execution wrapper.
- Secure login now goes through `secureLoginManager.login`; the old salt-fetch wrapper had no callers.

Kept proto/generated-backed RPCs such as `GetPackageOptions` for now even though the frontend wrapper was removed. Removing those safely requires regenerating and reviewing `modules/collect/proto/collectgen`, which was intentionally not done in this cleanup pass.

## Cleanup pass: migration/table-migration scan

Scanned active source, scripts, skills, and docs for migration/table-migration leftovers such as `schema_migrations`, `migration`, `migrate`, `ALTER TABLE`, and `CREATE TABLE IF NOT EXISTS`.

Conclusion:

- No active one-off migration/table-migration implementation was found in source or scripts.
- Remaining `CREATE TABLE IF NOT EXISTS` hits are current module-owned schema initialization files, including admin, cloudnode, collector, storage, and trade schemas.
- The remaining `schema_migrations` references in `docs/存储目标架构与元数据.md` are intentional documentation that the project is not maintaining `schema_migrations` and that this table should stay out of the storage core schema.

## Cleanup pass: stale compatibility/test/mock scan

Scanned active source, scripts, docs, and skills for old compatibility aliases, mock/test/spec files, and stale admin/cloudnode paths.

Findings:

- No project-owned mock/test/spec source files remain outside the OpenSpec workflow directory.
- Frontend API exports now have no obvious one-call-site dead wrappers. The remaining single-file export hits are internal helpers used in the same file: `getPackageDownloadURL` and `roleBase`.
- Search hits for `Deprecated` are protobuf generated-code descriptor comments and were not edited by hand.
- Search hits for `CloudNodeMgr` and `PackageMgr` are current architecture/protocol docs or active gateway/deployment metadata, not admin-local business implementations.

Remaining cleanup candidate for a later proto regeneration pass:

- Some RPCs still exist in proto/generated/service implementation after their frontend wrappers were removed, such as cloudnode package options and a few ops monitor/SSH one-off methods. Removing those safely should be done by editing the `.proto` sources and regenerating generated code together, then compiling affected modules.

## Cleanup pass: frontend dead TypeScript types

Removed frontend types that had no call sites or signature usage:

- `CreateNodeRequest` from `web/src/api/cloud-node.ts`. The management UI now creates nodes through batch cloudnode jobs rather than a single-node create request shape.
- `SftpListResult` from `web/src/api/modules/ssh.ts`. SFTP list responses are consumed directly by the file-manager page, while `SftpFileItem` remains active and imported by the page.

Backend methods corresponding to removed frontend wrappers were scanned. Several candidates are still tied to `.proto` service definitions and generated interfaces, including cloudnode single-node detail/delete/package-update, package options, SSH exec command, and monitor enable/disable/status/test methods. These should be cleaned in a dedicated proto regeneration pass rather than by manually editing generated files or removing only implementation methods.

## Cleanup pass: admin database path recheck

Rechecked active text references for `moox.db`, `data/moox`, `admin.db`, and `data/admin` outside this audit log.

Conclusion:

- No active `data/moox.db` / `moox.db` reference remains in source, scripts, docs, or skills.
- Admin references point to `./data/admin.db` or deployed `../data/admin.db`.
- Independently deployed modules keep their own module database files such as `moox_cloudnode.db`, `moox_collector.db`, and `moox_trade.db`, which matches the split-service storage boundary.

## Cleanup pass: Go test/mock deps and frontend tooling deps

Scanned Go module files and source imports for common test/mock dependencies such as `testify`, `gomock`, `go-sqlmock`, `goom`, `ginkgo`, and `gomega`.

Conclusion: no active Go module retains those common test/mock dependencies.

Scanned frontend package dependencies against source/config references. Tooling dependencies that do not appear in business source were retained when they are tied to project tooling:

- commitlint dependencies are backed by `web/commitlint.config.cjs` and `web/.husky/commit-msg`.
- stylelint dependencies are backed by `web/.stylelintrc.cjs` and lint-staged commands.
- `vue-tsc`, `terser`, `sass-embedded`, and TypeScript/ESLint dependencies are tied to build/lint configuration or Vite optional build paths.

No package dependency was removed in this pass.

## Cleanup pass: runtime data file scan

Scanned the repository for runtime database/storage artifacts including `.db`, `.sqlite`, `.sqlite3`, `.duckdb`, `.sst`, Pebble manifest files, and root/module runtime `data` directories.

Conclusion: no runtime data files or module-root runtime `data` directories remain in the repository. Frontend source paths under `web/src/views/data` are application pages and were not treated as runtime data.

## Cleanup pass: DeliverAll/DeliverNew naming recheck

Rechecked active source/docs for deprecated MooX `DeliverAll` / `DeliverNew` terminology.

Conclusion: no MooX business-level `DeliverAll` / `DeliverNew` concept remains. The only remaining `DeliverNew` text is `nats.DeliverNew()` in the NATS client integration, which is an upstream library option name and should not be renamed locally.

## Cleanup pass: SCF status callback documentation

Updated documentation and debug skill text to distinguish the two active status layers:

- CloudNode job runtime uses `/api/service/cloudnode/PollJobs` and `/api/service/cloudnode/ReportJobStatus`.
- Collector task-instance state still uses `/api/service/collectmgr/ReportTaskStatus`.

Files updated:

- `modules/collector/README.md`
- `docs/采集任务管理.md`
- `docs/云节点管理.md`
- `skills/debug/SKILL.md`
- `skills/debug/references/scf-e2e-debug.md`

Rationale: scans show `modules/collector/internal/cloudruntime` adapts CloudNode job polling to collector execution, while `modules/collector/internal/executor` still reports collector task-instance state through `ReportTaskStatus`. Therefore `ReportTaskStatus` is not dead code yet; it should not be removed unless collector task-instance state is redesigned in a later protocol pass.

## Cleanup pass: admin split-boundary scan

Scanned `modules/admin` and core docs for cloudnode/collector business remnants such as cloud node/package tables, CollectMgr/CloudNodeMgr implementations, task-rule/task-instance tables, and old admin-local package manager names.

Conclusion:

- No admin-local cloudnode/collector business table or service implementation was found.
- Remaining `modules/admin` hits are expected gateway/deployment references:
  - `modules/admin/config/gateway.yaml` routes `collectmgr` and `cloudnode` to independent services.
  - `modules/admin/schema/service_deployments_seed.sql` seeds `moox_collector` and `moox_cloudnode` deployment records.
  - `modules/admin/internal/service/sysdeploy/defaults.go` provides default service deployment metadata.
  - `modules/admin/internal/bootstrap/trpc.go` documents `/api/admin/cloudnode/*` and `/api/admin/collectmgr/*` forwarding.
- Docs already describe that collector/cloudnode schemas live under `modules/collector/schema` and `modules/cloudnode/schema`, not admin schema.

No code was deleted in this pass because the remaining references are part of the desired gateway/deployment boundary, not dead admin business logic.

## Cleanup pass: module-owned schema distribution scan

Scanned active SQL/table-definition references for `CREATE TABLE`, cloudnode tables, collector tables, admin deployment/auth/ops tables, and storage/trade schemas.

Conclusion:

- Admin schema owns only admin/base tables such as spaces, users, tokens, login history, actions, service deployments, SSH hosts/sessions, monitor history, and secrets.
- CloudNode tables are defined under `modules/cloudnode/schema/cloudnode.sql` and mapped by `modules/cloudnode/internal/repository` models.
- Collector task-rule/task-instance/execution-log tables are defined under `modules/collector/schema/collector.sql` and mapped by collector domain/repository models.
- Storage metadata tables live under `modules/storage/schema/metadata.sql`.
- Trade account/order tables live under `modules/trade/schema`.
- No scattered one-off table creation script for cloudnode/collector was found under admin, docs, scripts, skills, or examples.

No files were changed in this pass because the current schema distribution already matches the intended module boundaries.

## Cleanup pass: admin storage bootstrap config naming

Renamed the admin local bootstrap storage address setting from legacy `xdata_url` wording to `storage_access_url`:

- `modules/admin/config/app.yaml`
- `modules/admin/internal/config/app.go`

This setting remains only a local bootstrap default for deriving storage metadata access when SysDeploy has not injected runtime deployment addresses. Real service topology still comes from `t_service_deployments` / SysDeploy, not local config files.

## Cleanup pass: legacy xData naming recheck

Rechecked active source, configs, scripts, docs, skills, examples, and frontend source for `xdata_url`, `GetXDataURL`, `XDataURL`, and `xData/storage` after the admin storage bootstrap rename.

Conclusion:

- No active source/config/script reference to the old admin `xdata_url` setting remains.
- Remaining `xData` / `xData-mini` hits are historical/target-architecture documentation explaining where storage concepts came from. They are context, not runtime configuration or dead code.

## Cleanup pass: empty directory recheck

Rechecked empty directories outside dependency/build output folders.

Conclusion: the only remaining empty directory is `openspec/changes`, which belongs to the OpenSpec workflow workspace and is intentionally retained. No business/module empty shell directory remains to delete.

## Cleanup pass: build/release script stale module recheck

Rechecked root scripts and module Makefiles for stale module/package names such as `account-center`, `order-center`, `factor-calculator`, `data-collector`, `data-miner`, `xData-mini`, `moox-server`, `moox-account`, `moox-order`, `modules/account`, and `modules/order`.

Conclusion: no stale module/package names remain in active build, release, deploy, or Makefile entrypoints.

## Cleanup pass: service deployment category recheck

Rechecked source, docs, skills, scripts, examples, and frontend source for old `service_api` deployment category wording and interface-granularity service deployment remnants.

Conclusion:

- No `service_api` category remains in active code or docs.
- Current docs/skills consistently use `t_service_deployments` as the service deployment source of truth and state that records should represent independently deployable services rather than interface-level entries.
- Remaining Chinese comments about per-interface configuration are gateway rate-limit descriptions, not service deployment taxonomy, so they were retained.

## Cleanup pass: frontend route/menu/i18n recheck

Rechecked frontend route, static menu, and menu i18n files for stale deleted-page entries such as factor-list, strategy, file/table pages, routing-operation, service-status, and old cloud-function paths.

Conclusion:

- Active routes and static menu only reference current pages, including `/collector/functions` and `/collector/packages` for cloudnode management UI.
- Menu i18n keys in `web/src/lang/modules/zhCN.ts` and `web/src/lang/modules/enUS.ts` align with current static menu keys.
- No stale frontend route/menu/i18n entry was removed in this pass.

## Cleanup pass: frontend import path recheck

Scanned frontend route/view/api imports for references to removed view directories or old module paths.

Conclusion:

- No import references to deleted `factor-list`, strategy, file/table, or old cloud-function route directories were found.
- Current imports point to active views, shared metadata utilities, storage/admin/trade API wrappers, cloudnode management API wrappers, and retained child components.
- No frontend import-path cleanup was needed in this pass.

## Cleanup pass: test file recheck

Rechecked the repository for project-owned test files outside dependency/build outputs:

- `*_test.go`
- `*.test.ts` / `*.spec.ts`
- `*.test.js` / `*.spec.js`
- `*.test.tsx` / `*.spec.tsx`

Conclusion: no project-owned Go/TypeScript/JavaScript test files remain.

## Cleanup pass: SCF manual deploy helper wording

Rechecked SCF package/deploy helper references around runtime service address injection.

Findings:

- `scripts/build-collector-scf-package.sh` builds the SCF package and does not inject admin/storage topology.
- `scripts/deploy-collector-scf-package.sh` and `modules/cloudnode/cmd/moox-scf-deploy` are manual debug/deploy helpers that can invoke a keepalive event with command-line topology overrides.
- The formal release/build path does not package `moox-scf-deploy`; the normal control-plane flow should publish/probe SCF nodes through `moox-cloudnode`, with `service_deployments` coming from `t_service_deployments`.

Updated wording in:

- `modules/README.md`
- `modules/cloudnode/README.md`
- `docs/云节点管理.md`
- `scripts/deploy-collector-scf-package.sh`

Rationale: keep the manual helper available for debugging, but stop presenting it as a formal independently deployed module or the normal source of SCF runtime topology.

Also updated `docs/大仓架构.md` to label `deploy-collector-scf-package.sh` as a manual debug helper, while formal SCF publishing should go through `moox-cloudnode`.

## Cleanup pass: remove SCF package config overrides

Removed the SCF package build-time arbitrary config override path:

- `modules/collector/pkg/packager/scf.go`
  - Removed `BuildSCFPackageOptions.Overrides`.
  - Removed arbitrary dotted-key YAML override application from package config generation.
  - Package config patching now only stamps `system.version`.
- `modules/cli/cmd/collector.go`
  - Removed `collector function package --set dotted.path=value` and the corresponding publish/deploy package option.

Rationale: SCF runtime service topology must come from control-plane keepalive `service_deployments` sourced from `t_service_deployments`, not from build-time package config mutation. This also prevents reintroducing admin/storage address injection via the packager.

Retained `--env` and `--function-config` for cloudnode node creation because those values are submitted to the cloudnode management API as node/runtime metadata, not written into the SCF zip package config.

## Cleanup pass: remove manual SCF deploy helper

Removed the remaining manual SCF deploy/probe helper path:

- Deleted `scripts/deploy-collector-scf-package.sh`.
- Deleted `modules/cloudnode/cmd/moox-scf-deploy/main.go` and its empty command directory.
- Removed `moox-scf-deploy` / `deploy-collector-scf-package.sh` entries from `modules/cloudnode/README.md`, `docs/云节点管理.md`, and `docs/大仓架构.md`.

Rationale: this helper was not part of the formal build/release path and still allowed command-line gateway/storage topology overrides for keepalive probes. The intended flow is for `moox-cloudnode` to own SCF publishing/probing and for runtime topology to come from `t_service_deployments` via keepalive `service_deployments`, not from ad-hoc helper flags.

## Cleanup pass: post-delete SCF helper reference/dependency recheck

After removing `scripts/deploy-collector-scf-package.sh` and `modules/cloudnode/cmd/moox-scf-deploy`, rechecked active source/docs/scripts for:

- `moox-scf-deploy`
- `deploy-collector-scf-package`
- `MOOX_SERVICE_GATEWAY_URL`
- `MOOX_STORAGE_ACCESS_URL`
- `--gateway-url`
- `--storage-access-url`

Conclusion: no stale references remain outside this audit log.

Also reviewed `modules/cloudnode/go.mod` direct dependencies against current imports. Tencent SCF SDK dependencies are still used by `modules/cloudnode/internal/providers/tencent-scf`, and YAML/GORM/tRPC/protobuf dependencies are still imported by active cloudnode code. No cloudnode dependency was removed in this pass.

## Cleanup pass: debug skill SCF topology wording

Rechecked skills/docs for references to the removed manual SCF deploy helper and old build-time topology injection flow.

Conclusion:

- No references to the deleted `deploy-collector-scf-package.sh` or `moox-scf-deploy` remain outside this audit log.
- Updated `skills/debug/references/scf-e2e-debug.md` common-root-cause wording from stale package YAML/local endpoint injection to the current keepalive `service_deployments` model.

Rationale: SCF runtime topology should be diagnosed through control-plane keepalive payload delivery and application, not through package-time address injection.

## Cleanup pass: clarify collector function runtime config flags

Rechecked collector CLI package/publish docs after removing build-time SCF package config overrides.

Updated wording in:

- `modules/cli/cmd/collector.go`
- `skills/debug/references/scf-e2e-debug.md`

`--function-config` is now explicitly described as cloudnode node runtime config and not as a mutation of the SCF zip `config.yaml`. `--env` / `--function-config` remain valid because they are sent to cloudnode node creation metadata, while package-time arbitrary config mutation remains removed.

## Cleanup pass: remove shell SCF config.yaml patch

Removed the Python `config.yaml` patch from `scripts/build-collector-scf-package.sh`. The script still stamps the SCF binary version through Go `ldflags` and packages copied runtime configs as-is. Service topology must come from control-plane keepalive `service_deployments`, not from package-time config mutation.

## Cleanup pass: move SCF package version to binary runtime metadata

Removed the remaining SCF packager `config.yaml` mutation path from `modules/collector/pkg/packager/scf.go`. The packager now copies runtime config files as-is and no longer accepts a dead `BuildSCFPackageOptions.Version` field.

Added explicit `main.Version` support in `modules/collector/cmd/moox-collector-scf/main.go`, matching the existing `-ldflags -X main.Version=...` build path used by CLI/script package builds. `config.GetNodeInfo()` now prefers the runtime-injected version and falls back to `config.yaml` only when no runtime version is set.

## Cleanup pass: remove unused admin SSH command and monitor control execution paths

Frontend wrappers for one-off SSH command execution and monitor enable/disable/status/test APIs had already been removed. This pass removed the real backend execution paths while keeping minimal RPC stubs required by the currently generated admin proto interfaces.

Changed:
- Removed `ExecCommand` from the internal SSH service interface, implementation, and `SSHConn`.
- Deprecated the `ExecCommand` RPC implementation to return an error instead of executing remote commands.
- Removed monitor `EnableMonitor`, `DisableMonitor`, `IsMonitorEnabled`, and `TestNodeExporter` from the internal monitor service interface/implementation.
- Deprecated the corresponding monitor RPC implementations to return errors instead of invoking internal business logic.

Generated proto files were intentionally not hand-edited in this pass. A later proto regeneration pass can remove the deprecated RPC definitions and generated client/server methods completely.

## Cleanup pass: deprecate unused cloudnode package options implementation

Frontend package selectors now use `GetPackageList`; the old `GetPackageOptions` wrapper had already been removed from `web/src/api/function-package.ts`. This pass removed the real cloudnode-side option-building implementation and changed `GetPackageOptions` to return a deprecated error response.

Generated collect proto files were intentionally not hand-edited here. A later proto regeneration pass can remove `PackageOption`, `GetPackageOptionsReq`, `GetPackageOptionsRsp`, and the `CloudNodeMgr.GetPackageOptions` RPC from `modules/collect/proto` and generated outputs.

## Cleanup pass: remove explicit deprecated RPC stubs covered by generated unimplemented embeds

After confirming the service structs embed generated `Unimplemented...` implementations, explicit deprecated stubs were removed for:
- `cloudnode.Service.GetPackageOptions`
- `ssh/rpc.Service.ExecCommand`
- `monitor/rpc.Service.EnableMonitor`
- `monitor/rpc.Service.DisableMonitor`
- `monitor/rpc.Service.GetMonitorStatus`
- `monitor/rpc.Service.TestNodeExporter`

The generated unimplemented embeds now cover these obsolete RPCs until the proto regeneration pass removes the definitions entirely.

## Cleanup pass: remove obsolete RPCs from source proto and regenerate

Removed obsolete RPC definitions from source proto files and regenerated Go outputs with the project Makefile targets.

Source proto changes:
- `modules/admin/proto/ops_service.proto`: removed SSH `ExecCommand` request/response and RPC.
- `modules/admin/proto/ops_service.proto`: removed monitor `EnableMonitor`, `DisableMonitor`, `GetMonitorStatus`, and `TestNodeExporter` request/response/RPC definitions, plus the now-unused `TestResult` message.
- `modules/collect/proto/collect_service.proto`: removed `PackageOption`, `GetPackageOptionsReq`, `GetPackageOptionsRsp`, and `CloudNodeMgr.GetPackageOptions`.

Generated outputs were refreshed via:
- `make -C modules/admin/proto all`
- `make -C modules/collect/proto all`

This aligns the generated protocol layer with the previously removed frontend wrappers and backend business implementations.

## Cleanup pass: remove remaining schema_migrations wording from storage docs

The storage architecture doc already states that this pre-launch project does not maintain migration version tables. This pass removed the explicit old `schema_migrations` table name from the deleted-table examples to avoid suggesting that the system still owns or migrates that table.

## Audit pass: generated proto outputs did not reintroduce tests or mocks

After regenerating admin and collect proto outputs, no project-owned test or mock files were found under `modules`, `web`, `scripts`, or `examples`. The proto Makefiles still use `--mock=false`.

## Cleanup pass: post-proto reference and admin DB path audit

Post-proto cleanup scans found no active source references to the removed `GetPackageOptions`, SSH `ExecCommand`, or monitor control/test RPC types. The only package-options matches were CLI-internal collector packaging option names, unrelated to the removed cloudnode RPC.

Database path scan found no active `data/moox.db` or `moox.db` references outside the audit log. Admin defaults point to `data/admin.db`; cloudnode and collector use their own module databases.

Migration/test scans found no project-owned migration implementation, `AutoMigrate`, migration directories, unit test files, or generated mock files. Proto generation remains configured with `--mock=false`.

Removed the orphaned `startSSHDirectServer` deprecated comment from `modules/admin/internal/bootstrap/services.go`; the function itself had already been removed, and SSH raw endpoints are registered through the admin gateway rawhandler.

## Cleanup pass: remove monitor DAO methods left by deprecated control/test RPCs

After removing monitor enable/disable/status/test RPCs, the monitor DAO still kept helper methods used only by those old flows. This pass removed:
- `SSHHostDAO.SetMonitorEnabled`
- `SSHHostDAO.IsMonitorEnabled`
- `SSHHostDAO.GetHost`

The active monitor collector path still uses `ListMonitorHosts`, which reads `c_monitor_enabled` to decide which SSH hosts should be scraped.

## Cleanup pass: remove unused auth log query DAO methods

Admin auth still writes login history and user action records, so the tables and write paths remain active. This pass removed unused DAO read helpers that had no API/service callers:
- `UserDAO.GetLoginHistoryByUser`
- `UserDAO.GetUserActionsByUser`

## Cleanup pass: align monitor documentation after control/test RPC removal

Updated monitor-related docs after removing monitor enable/disable/status/test RPCs:
- `modules/admin/README.md` now describes Monitor as host metric collection/query rather than monitor configuration/control.
- `docs/监控配置.md` no longer documents the old Node Exporter test API; troubleshooting now points to direct Node Exporter `/metrics` access plus Monitor logs/UI.

## Cleanup pass: remove admin external schema override

Removed the `MOOX_CONTROL_ADMIN_SCHEMA_FILE` override path from `modules/admin/internal/service/database/manager.go`. Admin now always applies the embedded module-owned schema from `modules/admin/schema`, matching the current pre-launch reset/rebuild model and avoiding external one-off SQL overrides.

Also removed the now-unused `ApplySchema` method and made the remaining SQL application helper private to the database manager package.

## Cleanup pass: make module schema application helpers private

After removing the admin external schema override, the schema application helpers in the SQLite managers are only used internally during startup. This pass made those helpers private in:
- `modules/admin/internal/service/database/manager.go`
- `modules/cloudnode/internal/storage/database.go`
- `modules/collector/internal/control/storage/database.go`
- `modules/trade/internal/service/database/manager.go`

Each service still applies its embedded, module-owned schema at startup; no external schema override path remains in admin.

## Cleanup pass: remove unused CloudNode GetCloudAccount RPC

Frontend cloud-account management now uses `ListCloudAccounts` for table/edit data; the detail wrapper had already been removed. This pass removed the unused single-account detail RPC from cloudnode:
- Removed `GetCloudAccountReq` and `GetCloudAccountRsp` from `modules/collect/proto/collect_service.proto`.
- Removed `CloudNodeMgr.GetCloudAccount` from the collect proto service.
- Removed `cloudnode.Service.GetCloudAccount` implementation.
- Regenerated collect proto outputs with `make -C modules/collect/proto all`.

Cloud account create/update/delete/list and `GetCOSAccountInfo` remain active.

## Cleanup pass: remove unused single-node cloudnode RPCs

Frontend cloud-function management uses node list, update, batch create/delete/deploy, region list, and package APIs. The old single-node detail/delete/package-selection RPCs had no callers. This pass removed:
- `GetNodeDetailReq`, `GetNodeDetailRsp`, and `CloudNodeMgr.GetNodeDetail`
- `DeleteNodeReq`, `DeleteNodeRsp`, and `CloudNodeMgr.DeleteNode`
- `UpdateNodeFunctionReq`, `UpdateNodeFunctionRsp`, and `CloudNodeMgr.UpdateNodeFunction`
- `cloudnode.Service.GetNodeDetail`
- `cloudnode.Service.DeleteNode`
- `cloudnode.Service.UpdateNodeFunction`
- `CatalogRepository.SoftDeleteNode`

`InvokeFunction`, `ReportHeartbeat`, `BatchDeleteNodes`, and `BatchDeployNodes` remain active. Collect proto outputs were regenerated with `make -C modules/collect/proto all`.

## 2026-07-02 继续清理：移除旧 SCF 部署信息查询接口

### 结论

`GetSCFDeployInfo` 是旧 `scf-publish` 工具时代用于按节点查询 SCF 部署信息的接口。当前部署与发布链路已经改为云节点服务集中管理，未发现前端、CLI、脚本或文档中的有效调用方，因此删除该 RPC 及对应消息结构。

### 本次删除

- `modules/collect/proto/collect_service.proto`
  - 删除 `SCFDeployInfo`
  - 删除 `GetSCFDeployInfoReq`
  - 删除 `GetSCFDeployInfoRsp`
  - 删除 `CloudNodeMgr.GetSCFDeployInfo`
- `modules/cloudnode/internal/service/cloudnode/service.go`
  - 删除 `Service.GetSCFDeployInfo`
- 重新执行 `make -C modules/collect/proto all`，同步生成 `collectgen` 代码。

### 保留判断

以下接口仍有明确用途，本轮不删除：

- `BatchDeployNodes`：前端云函数页面和 CLI 发布链路仍在使用。
- `ListCloudRegions`：前端创建/部署云函数节点时仍需要地区列表。
- `GetCOSAccountInfo`：CLI 腾讯云运维辅助链路仍需要读取 COS 账号信息。
- `InvokeFunction`：云节点执行平台设计中仍用于 MooX 侧主动调用云函数，后续 factor 同步计算也可能复用。

## 2026-07-02 继续核对：云函数页面云账户接口

### 结论

当前源码中的云函数页面、代码包页面、云账户管理页面已经统一通过 `callControl('cloudnode', ...)` 请求独立 cloudnode 服务，URL 形态为 `/api/admin/cloudnode/{method}`，没有发现仍请求旧 `collector` 云账户接口或旧 admin 内置云账户接口的残留。

已确认的调用包括：

- `/api/admin/cloudnode/ListCloudAccounts`
- `/api/admin/cloudnode/CreateCloudAccount`
- `/api/admin/cloudnode/UpdateCloudAccount`
- `/api/admin/cloudnode/DeleteCloudAccount`
- `/api/admin/cloudnode/BatchCreateNodes`
- `/api/admin/cloudnode/BatchDeployNodes`
- `/api/admin/cloudnode/ListCloudRegions`

### 线上提示判断

当前 `web/src` 中已经不存在“加载云账户失败，请点击云账户管理按钮”这类旧提示文案。如果线上仍出现该文案，更可能是 web-host 仍在加载旧静态包或浏览器缓存中的旧 chunk，而不是当前源码还在请求旧接口。

### 本次补强

修复 `modules/cloudnode/internal/service/cloudnode/service.go` 中 `UpdateCloudAccount` 的更新语义：前端编辑云账户时不传 `secret_id` / `secret_key`，服务端会读取现有账号并保留原凭证，避免把密钥覆盖为空。

## 2026-07-02 继续清理：前端 mock 残留

### 结论

`web` 当前没有 `src/mock` 目录，也没有 `mockjs` / `vite-plugin-mock` 的源码引用。`web/package.json` importer 中也未声明 mock 相关依赖。

### 本次删除

- `web/eslint.config.js`
  - 删除 `"/src/mock/*"` ignore，避免未来误加 mock 代码时被 lint 配置绕过。
- `web/README.md`
  - 删除项目目录说明中的 `src/mock` 条目。

### 说明

`web/pnpm-lock.yaml` 中仍有历史 package/snapshot 条目，但 importer 未引用。锁文件应由包管理器重算时自然裁剪，本轮不手工编辑锁文件，避免破坏 lockfile 结构。

## 2026-07-02 继续清理：前端无效 crypto 分包项

### 结论

`web/src/utils/crypto.ts` 当前使用 `node-forge` 作为 HTTP 环境下的加密降级实现；未发现源码引用 `crypto-js`。`web/package.json` 也未声明 `crypto-js` 依赖。

### 本次删除

- `web/vite.config.ts`
  - 将 `manualChunks['utils-crypto']` 从 `['crypto-js', 'node-forge']` 调整为 `['node-forge']`。

这样避免 Vite/Rollup 分包配置继续引用不存在或未使用的历史依赖。

## 2026-07-02 继续核对：测试、迁移和数据库命名残留

### 数据库命名

当前项目未发现 `data/moox.db` / `moox.db` 的有效引用。Admin 默认库名继续为 `data/admin.db`。

### 测试与 mock

按文件名扫描未发现项目自有 Go 单测文件（`*_test.go`），也未发现 mock/mocks 文件或目录。

### 一次性迁移

按文件名与内容扫描未发现项目自有 migration / schema_migrations / AutoMigrate 代码残留；命中项仅为第三方 lockfile 包维护提示或文档中的架构说明。

### SSH 旧独立端口

未发现旧 `20180` SSH 独立 HTTP 服务实现残留；当前仅保留说明性注释，实际 WebSocket/SFTP 入口已通过 admin 网关 rawhandler 分派。

## 2026-07-02 继续清理：删除源码树中的本地 Claude 权限配置

### 结论

仓库中残留多份 `.claude/settings.local.json`，内容均为本地助手执行权限白名单，不属于 MooX 运行配置、构建配置或业务代码。其中 `modules/admin/.claude/settings.local.json` 还包含旧 `internal/service/cloudnode` 构建命令，容易误导后续维护。

### 本次删除

- `web/.claude/settings.local.json`
- `modules/admin/.claude/settings.local.json`
- `modules/admin/internal/.claude/settings.local.json`
- `modules/admin/internal/service/.claude/settings.local.json`
- `modules/collector/.claude/settings.local.json`
- `modules/collector/internal/.claude/settings.local.json`
- `modules/cli/.claude/settings.local.json`
- `modules/storage/.claude/settings.local.json`
- `modules/storage/internal/services/.claude/settings.local.json`

这些文件删除后不影响运行期服务、协议或构建逻辑。

## 2026-07-02 继续清理：移除 auth/gateway 未使用数据库配置

### 结论

Admin 的 SQLite 连接由 `modules/admin/config/app.yaml` 和 `database.Manager` 管理，认证服务初始化时直接复用已初始化的 `database.Manager`，不再从 `gateway.yaml` 读取独立数据库配置。

### 本次删除

- `modules/admin/internal/service/auth/config/config.go`
  - 删除未使用的 `Config.Database` 字段。
  - 删除未使用的 auth `DatabaseConfig` 结构。
- `modules/admin/config/gateway.yaml`
  - 删除顶部 `database` 配置段及 `dbname: ./data/admin.db`。
- `scripts/deploy-moox.sh`
  - 删除发布阶段对 `gateway.yaml` 中 `dbname` 的改写，只保留 Badger `data_dir` 改写。

### 归属说明

Admin 数据库路径继续只由 `modules/admin/config/app.yaml` 的 `database.path` 表达；当前默认值为 `./data/admin.db`。`gateway.yaml` 只保留网关、JWT、缓存、限流、服务转发等配置。

## 2026-07-02 继续清理：收窄 admin 主配置数据库边界

### 结论

Admin 当前只支持并使用 SQLite，数据库连接由 `modules/admin/config/app.yaml` 的 `database.path` 和连接池配置表达。MySQL/Postgres 风格字段、`DB_HOST` / `DB_USER` / `DB_NAME` 等环境变量，以及迁移前采集任务用的 `task_management` 配置，在 admin 中均没有运行期调用方。

### 本次删除

- `modules/admin/internal/config/app.go`
  - 删除 `AppConfig.TaskManagement` 和 `TaskManagementConfig`。
  - 删除 `DatabaseConfig.Type`、`Host`、`Port`、`User`、`Password`、`DBName`。
  - 删除 `DB_HOST`、`DB_PORT`、`DB_USER`、`DB_PASSWORD`、`DB_NAME` 环境变量覆盖逻辑。
  - `Validate` 改为直接校验 `database.path`，因为 admin 当前只使用 SQLite。
- `modules/admin/config/app.yaml`
  - 删除 `database.type: sqlite`。
  - 删除 `task_management` 配置段。
- `modules/admin/internal/service/database/manager.go`
  - 补充 `strings` import，匹配 `buildSQLiteDSN` 的实际依赖。
- `docs/数据库管理.md`
  - 删除 admin 数据库文档中的 MySQL/Postgres 配置与环境变量示例。
  - 明确 admin 当前数据库边界是 SQLite `data/admin.db`。

### 保留

- `database.path`
- `max_idle_conns`
- `max_open_conns`
- `conn_max_lifetime`
- `conn_max_idle_time`

这些字段仍被 admin `database.Manager` 消费。

## 2026-07-03 继续清理：删除 admin 无消费 ServiceConfig

### 结论

`modules/admin/internal/config/service.go` 只负责从 tRPC 全局配置中读取 `trpc.moox.gateway.stdhttp` 的端口并保存到全局 `ServiceConfig`。当前代码没有任何调用方读取 `GetGatewayPort` 或 `GetGlobalServiceConfig`，启动流程加载该配置没有实际运行价值。

### 本次删除

- `modules/admin/internal/config/service.go`
  - 删除 `ServiceConfig`、`LoadServiceConfig`、全局 service config、`GetGatewayPort`。
- `modules/admin/internal/bootstrap/config.go`
  - 删除 `Config.Service` 字段。
  - 删除启动阶段 `LoadServiceConfig` / `SetGlobalServiceConfig` 流程。

### 说明

SUPERSEDED by 2026-07-03 gateway target cleanup: admin 网关实际监听端口仍由 `modules/admin/config/trpc_go.yaml` 的 tRPC 服务配置决定；统一转发目标只由运行期 `t_service_deployments` active 记录解析，不再由 `gateway.yaml` 维护服务地址。

## 2026-07-03 继续清理：收窄 admin auth/gateway 配置

### 结论

当前 AuthService 只签发访问令牌，没有 `RefreshToken` RPC 或 refresh token 运行链路。`refresh_expired`、`TokenTypeRefresh`、预留的 `cache.db`，以及 gateway 配置结构中重复声明但不消费的 auth `security` 字段都属于历史/预留残留。

### 本次删除与调整

- `modules/admin/internal/service/auth/config/config.go`
  - 删除 `JWTConfig.RefreshExpired`。
  - 删除预留的 `CacheConfig.DB`。
- `modules/admin/internal/gateway/config.go`
  - 删除 gateway 侧未消费的 `Security` 字段和 `SecurityConfig`。
  - gateway 侧 `JWTConfig` 仅保留鉴权实际需要的 `secret_key`。
- `modules/admin/config/gateway.yaml`
  - 删除 `cache.db`。
  - 删除 `jwt.refresh_expired`。
- `modules/admin/internal/common/crypto/aes.go`
  - 删除未使用的 `TokenTypeRefresh`。
- `modules/admin/internal/service/auth/utils/token.go`
  - 删除未使用的 `TokenTypeRefresh` 导出别名。
- `modules/admin/internal/service/auth/model/user.go`
  - 删除未使用的 `TokenTypeRefresh` 字符串常量。
- `modules/admin/internal/service/auth/impl/init.go`
  - auth cache 默认目录从旧 `./data/cache` 对齐为当前配置默认值 `./data/badger`。
- `docs/架构总览.md`
  - AuthService 方法列表按当前 `infra_service.proto` 修正，删除不存在的 `RefreshToken`。
- `docs/认证鉴权.md`
  - 删除 refresh token 示例，改为说明当前只签发访问令牌。
  - 配置示例改为当前 `security.max_login_attempt` / `cache.data_dir: ./data/badger`。

### 保留

- `jwt.secret_key`：登录签发和网关鉴权都需要。
- `jwt.access_expired`：登录签发访问令牌需要。
- `security.salt_expired`、`security.max_login_attempt`、`security.lock_duration`：登录盐值和失败锁定逻辑需要。
- `cache.data_dir`：Badger 缓存目录需要。

## 2026-07-03 继续清理：删除 admin app Auth/Log 死配置

### 结论

Admin 主配置 `modules/admin/config/app.yaml` 中的 `auth` 和 `log` 配置已经没有运行期调用方。认证配置实际由 `modules/admin/config/gateway.yaml` 的 `jwt`、`security`、`cache` 段读取；日志路径由 tRPC 配置管理，`app.yaml` 的 `log.output_path` 只被旧 Validate 逻辑用于创建目录。

### 本次删除与调整

- `modules/admin/internal/config/app.go`
  - 删除 `AppConfig.Auth`、`AuthConfig`。
  - 删除 `AppConfig.Log`、`LogConfig`。
  - 删除 `JWT_SECRET` 环境变量覆盖逻辑。
  - 删除基于 `Log.OutputPath` 创建目录的 Validate 逻辑。
- `modules/admin/config/app.yaml`
  - 删除 `log` 配置段。
- `scripts/deploy-moox.sh`
  - 删除发布阶段对 admin `app.yaml` 中 `output_path` 的替换。
- `docs/认证鉴权.md`
  - 删除旧 `JWT_SECRET` / `MAX_LOGIN_ATTEMPTS` / `LOCK_DURATION` 环境变量示例，改为说明生产配置应修改 `gateway.yaml`。
  - 修正锁定时间配置路径为 `gateway.yaml` 的 `security.lock_duration`。
- `modules/admin/internal/common/crypto/aes.go`
  - 删除不再使用的默认 JWT secret 和 `MOOX_JWT_SECRET_KEY` 读取逻辑，仅保留 issuer 默认值。
- `modules/admin/internal/service/auth/utils/token.go`
  - 删除无调用方的 `ExtractUserIDFromToken` 和默认 JWT 配置别名。

### 保留

- `gateway.yaml` 的 `jwt.secret_key`、`jwt.access_expired`、`security.*`、`cache.data_dir`，这些由 AuthService 登录/盐值/锁定逻辑实际消费。
- `trpc_go.yaml` 的日志配置，仍是 admin 运行期日志输出的配置来源。

## 2026-07-03 继续清理：auth 工具层与模型常量死代码

### 结论

Auth 当前通过 tRPC metadata 传递用户上下文，并通过 `common/crypto` 实现 JWT 访问令牌。早期基于普通 `context.Value` 的 user helper、部分 token 薄包装、以及若干无调用模型常量已经没有运行期调用方。

### 本次删除与调整

- `modules/admin/internal/service/auth/utils/user.go`
  - 删除无调用方的 `UserContextKey` 和 `GetUserIDFromContext`。
- `modules/admin/internal/service/auth/utils/token.go`
  - 删除无调用方的 `GenerateToken`、`ParseToken` wrapper。
  - 删除无调用方的 `TokenType`、`JWTConfig`、`TokenTypeAccess` 导出别名。
  - 保留实际调用的 `UnifiedClaims`、`GenerateAccessToken`、`ValidateAccessToken`。
- `modules/admin/internal/service/auth/model/user.go`
  - 删除无调用方的 `UserRoleGuest/UserRoleUser/UserRoleAdmin/UserRoleSuperAdmin` 常量。
  - 删除无调用方的 `LoginResultFailed/LoginResultLocked` 常量。
  - 保留当前登录记录使用的 `LoginResultSuccess` 和用户操作 action 常量。
- `docs/认证鉴权.md`
  - 下游用户上下文 helper 从旧 `GetUserIDFromContext` 改为当前 `GetUserInfoFromCtx`。
  - token 校验函数名从旧 `ParseAccessToken` 改为当前 `ValidateAccessToken`。
  - 盐值示例从旧 `utils.GenerateRandomSalt(16)` 改为当前 `crypto.GenerateSalt()`。

## 2026-07-03 继续清理：common crypto 早期哈希校验残留

### 结论

`modules/admin/internal/common/crypto/aes.go` 中的 `ValidateHash` 是早期“密码 + salt + timestamp”哈希校验方案残留。当前登录和改密链路使用动态盐派生 AES-GCM 密钥，先解密客户端提交的密码，再用用户静态盐校验服务端存储的密码哈希，因此 `ValidateHash` 没有调用方。

### 本次删除

- `modules/admin/internal/common/crypto/aes.go`
  - 删除无调用方的 `ValidateHash`。

### 保留判断

以下函数仍有调用方，本轮明确保留：

- `AESEncrypt` / `AESDecrypt`：SecretMgr 和 SSH 凭据加解密仍使用。
- `ValidateEncryptedPassword`：登录和改密旧密码校验仍使用。
- `DecryptPassword`：改密新密码解密仍使用。
- JWT 访问令牌生成/校验函数：AuthService 登录和 admin 网关鉴权仍使用。

## 2026-07-03 继续清理：删除无状态 JWT 下未使用的 active token 表

### 结论

Admin 当前使用无状态 JWT：登录签发访问令牌，网关校验签名和 claims，不查询数据库中的 token 会话表。因此 `t_active_tokens`、`ActiveToken` 模型和相关 DAO 方法没有运行期调用方，属于早期 JWT 会话管理设计残留。

### 本次删除

- `modules/admin/schema/admin.sql`
  - 删除 `t_active_tokens` 表定义。
  - 删除 `idx_tokens_*` 索引。
  - 删除 `update_tokens_mtime` 触发器。
- `modules/admin/internal/service/auth/model/user.go`
  - 删除 `ActiveToken` 模型和 `TableName`。
- `modules/admin/internal/service/auth/dao/user.go`
  - 删除 `CreateToken`。
  - 删除 `GetTokenByJTI`。
  - 删除 `UpdateTokenLastUsed`。
  - 删除 `RevokeToken`。
  - 删除 `RevokeUserTokens`。
  - 删除 `CleanExpiredTokens`。
  - 删除 `CountActiveTokens`。

### 保留说明

当前访问令牌有效性由 JWT 签名、过期时间、token_type 和网关鉴权逻辑决定；主动撤销/会话表能力尚未落地为业务接口，因此不保留空表和未调用 DAO。

## 2026-07-03 继续清理：删除无写入口的 SSH monitor_enabled 字段

### 结论

`t_ssh_host.c_monitor_enabled` 只剩 schema 字段、索引和 monitor DAO 查询条件，没有前端、RPC、模型或 DAO 写入口。此前 monitor enable/disable 接口已删除后，该字段默认值为 0，会导致监控采集拿不到任何主机，属于迁移后残留开关。

### 本次删除与调整

- `modules/admin/schema/admin.sql`
  - 删除 `t_ssh_host.c_monitor_enabled` 字段。
  - 删除 `idx_ssh_host_monitor_enabled` 索引。
- `modules/admin/internal/service/monitor/dao/ssh_host.go`
  - `ListMonitorHosts` 不再筛选 `c_monitor_enabled = 1`。
  - `hostIDs` 为空时返回所有 SSH 主机；传入 `hostIDs` 时返回指定主机。

### 保留说明

监控是否执行由 monitor service 的采集入口和 SSH 主机列表决定；不再保留无写入口、无前端控制面的 per-host monitor 开关。

## 2026-07-03 继续清理：auth DAO 未调用方法

### 结论

Auth DAO 中仍有部分早期用户管理/统计接口残留，但当前 `infra_service.proto` 没有用户列表、按角色查询、删除用户、email 查询或数据库登录次数落库接口。登录失败次数当前通过 Badger `LoginAttempt` 临时状态管理，而不是写入 `t_users.c_login_attempts`。

### 本次删除

- `modules/admin/internal/service/auth/dao/user.go`
  - 删除未调用的 `GetUserByEmail`。
  - 删除未调用的 `IncrementLoginAttempts`。
  - 删除未调用的 `DeleteUser`。
  - 删除未调用的 `CountUsers`。
  - 删除未调用的 `GetUsersByRole`。
  - 删除未调用的 `UserDAO.Close`。

### 保留说明

保留的方法均有当前业务调用方，覆盖注册、登录、获取/更新用户信息、改密、登录历史、用户操作审计、登录盐值、改密盐值和登录失败临时状态。

## 2026-07-03 继续清理：auth 用户表登录锁定残留字段

- 删除 `t_users.c_login_attempts` 与 `t_users.c_locked_until`，这两个持久化字段已经不再承担登录失败锁定逻辑。
- 删除 `model.User.LoginAttempts` 与 `model.User.LockedUntil`，避免模型继续暴露无效字段。
- 删除 `UpdateUserLoginInfo` 中对上述两列的成功登录重置写入；登录失败次数和锁定状态继续由 Badger 中的 `LoginAttempt` 临时记录负责。
- 未删除 `LoginAttempt`、`SetLoginAttempt`、`GetLoginAttempt`、`DeleteLoginAttempt` 以及 `security.max_login_attempt` / `security.lock_duration`，这些仍是当前登录保护链路的一部分。

## 2026-07-03 继续清理：网关转发目标收敛到 t_service_deployments

- 删除 `modules/admin/config/gateway.yaml` 中的静态 `gateway.services` 地址映射，避免服务部署信息同时存在于 YAML 和 `t_service_deployments` 两个来源。
- 删除网关配置模型中的 `GatewayConfig.Services` 以及 YAML fallback 读取逻辑；`forwardHTTP` 只通过 SysDeploy 注入的 resolver 解析 active 部署记录。
- 缺失 active 部署记录时，网关直接返回“未在 t_service_deployments 中找到 active 部署记录”，不再回退到本地静态地址。
- 更新 admin README 与云节点文档，说明 `gateway.yaml` 只保留 JWT/CORS/限流/后台签名/免鉴权路径，服务地址统一通过服务部署表维护。

## 2026-07-03 继续清理：网关健康检查移除 gateway.yaml 服务列表

- 删除 `InitGatewayServices` 对 `cfg.GetAllServiceIDs()` 的残留调用，网关初始化日志改为说明转发目标来自 `t_service_deployments` active 记录。
- 删除 `/api/admin/health` 响应中的 `services` 字段，避免继续表达“服务列表来自 gateway.yaml”的旧语义。
- 保留健康检查基础状态与时间字段；服务部署信息应通过 SysDeploy 的 `ListServiceDeployments` / `ListActiveServiceDeployments` 查询。

## 2026-07-03 继续清理：删除 gateway 未接入的端口和超时配置

- 删除 `GatewayConfig.Port` 与 `GatewayConfig.Timeout`，这两个字段当前没有运行时读取路径。
- 删除 `modules/admin/config/gateway.yaml` 中的 `gateway.timeout`，避免误导使用者以为网关转发超时由该字段控制。
- 保留 `cache`、`jwt.access_expired`、`security.*`，这些仍由 auth 登录盐值、JWT 过期和登录失败锁定逻辑使用。

## 2026-07-03 继续清理：修正数据库文档中的缓存目录样例

- 将数据库管理文档中的认证缓存目录从旧的 `./data/cache` 修正为当前 `./data/badger`。
- 明确认证缓存配置位于 `modules/admin/config/gateway.yaml`，数据库配置继续由 `modules/admin/config/app.yaml` 管理。

## 2026-07-03 继续清理：admin 移除本地 storage 地址兜底

- 删除 `modules/admin/config/app.yaml` 中的 `storage.storage_access_url`，storage 部署地址统一由 `t_service_deployments` 维护。
- 删除 `AppConfig.Storage`、`StorageConfig`、`GetStorageAccessURL`、`GetMetadataURL` 和基于 `20201 -> 20200` 推导 metadata 地址的旧兜底函数。
- 删除 bootstrap 中读取服务部署后写入本地 runtime storage metadata URL 的逻辑；admin 网关转发 storage 目标时统一通过 SysDeploy resolver 解析 active 部署记录。

## 2026-07-03 继续清理：Badger 缓存目录兜底统一为 data/badger

- 将 `database.Manager.InitializeCache` 的空路径兜底从旧的 `./data/cache` 改为当前 `./data/badger`。
- 更新数据库管理文档中的缓存初始化示例，避免继续传播旧目录。

## 2026-07-03 继续清理：数据库文档初始化示例对齐当前 Manager API

- 删除数据库配置示例中的旧 `database.type` 字段，admin 当前只使用 SQLite。
- 将文档中的 `dbManager.Initialize("./data/admin.db")` 更新为当前 `dbManager.Initialize(&config.DatabaseConfig{Path: "./data/admin.db"})`。

## 2026-07-03 继续清理：删除 auth JWT 薄包装

- 删除 `modules/admin/internal/service/auth/utils/token.go`，该文件只重新导出 `common/crypto` 中的 JWT claims 和访问令牌函数。
- `gateway/authorize.go` 改为直接使用 `common/crypto.ValidateAccessToken` 与 `crypto.UnifiedClaims`。
- `auth/impl/login.go` 改为直接使用 `common/crypto.GenerateAccessToken`。
- `auth/utils` 继续保留上下文用户信息、安全 UserInfo 构造和输入格式校验等实际辅助函数。

## 2026-07-03 继续清理：collector 心跳不再解析 task_instances

- CloudNode `ReportHeartbeat` 当前只记录节点心跳并回显 `tasks_md5`，任务执行由 `PollJobs` / `ReportJobStatus` 维护。
- 删除 collector heartbeat 中解析 `task_instances` 并更新本地任务缓存的旧协议逻辑。
- 更新 SCF keepalive 注释，将链路说明从 `ProcessProbe -> ReportHeartbeat -> ExecuteDueTasks` 改为 `ProcessProbe -> ReportHeartbeat -> PollJobs`。

## 2026-07-03 继续清理：删除 collector 本地任务缓存定时执行链路

- 删除 `collectexec.timer` 注册，collector 不再通过本地内存任务缓存定时执行采集。
- 删除 `executor.ScheduledExecute` / `ExecuteDueTasks` / `executeDueTasksAt` / `shouldExecute`，保留 CloudNode job 使用的 `ExecuteTaskImmediately`。
- 删除 `pkg/config/task_instance.go` 和 `github.com/orcaman/concurrent-map/v2` 依赖。
- SCF keepalive 的 `RunningTasks` 不再读取旧本地任务缓存；任务租约状态由 CloudNode job 状态维护。
- 删除 `modules/collector/configs/example_trpc_go.yaml` 中的 `trpc.collectexec.timer` 示例配置。

## 2026-07-03 继续清理：心跳协议移除任务同步字段

- 删除 `ReportHeartbeatReq.tasks_md5`，SCF 不再通过心跳上报本地任务缓存摘要。
- 删除 `HeartbeatTaskInstance` 与 `ReportHeartbeatRsp.task_instances/tasks_md5/package_version`，心跳响应只保留 `ret_info`。
- `moox-cloudnode` 的 `ReportHeartbeat` 不再回显 `tasks_md5`。
- `moox-collector-scf` 心跳客户端只校验 `ret_info`，不再解析包版本或任务实例。
- 更新架构文档与 debug skill，明确 keepalive 只负责服务部署信息/节点在线，任务执行通过 CloudNode `PollJobs` job lease。

## 2026-07-03 继续清理：cloudnode 删除未实现的 node pool/deployment 预留表

- 删除 `t_cloud_node_pools` 与 `t_cloud_deployments`，这两张表没有当前 repository/service/API 使用路径。
- 删除 `cloudnode` 配置中的 `node_pool` 预留配置和对应 `NodePoolConfig`。
- 保留 `t_cloud_nodes.c_pool_id`、job/invocation 的 `deployment_id` 字段；它们作为筛选/版本标识仍被当前 PollJobs 和 InvokeSync 链路使用。
- 更新云节点执行平台文档中的目录示例，移除未落地的 node_pool/deployment repo 结构。
- 更新 CloudNode 管理台 API 列表，移除未实现的 `ListNodePools/ListDeployments/CreateDeployment/DisableDeployment` 等旧设计项。

## 2026-07-03 继续清理：删除 Binance 真实接口验证 CLI

- 删除 `modules/trade/cmd/binance-verify/main.go`。
- 该入口用于携带真实 Binance API key 对交易适配器逐接口发起只读验证请求，属于临时功能验证工具，不应作为模块源码长期沉淀。
- 仓库中未发现其他对 `binance-verify` 的引用。

## 2026-07-03 继续审计：云函数页面接口与生成物状态

本轮重点复查前端云函数/云账户页面是否仍请求旧 admin 内置接口。

结论：源码侧已经走独立 `cloudnode` 服务，不再调用旧 admin 内置云账户接口。

- `web/src/api/cloud-account.ts` 使用 `/api/admin/cloudnode/ListCloudAccounts`、`CreateCloudAccount`、`UpdateCloudAccount`、`DeleteCloudAccount`。
- `web/src/api/cloud-node.ts` 使用 `/api/admin/cloudnode/GetNodeList`、`BatchCreateNodes`、`BatchDeployNodes`、`BatchDeleteNodes`、`ListCloudRegions`。
- `web/src/api/function-package.ts` 使用 `/api/admin/cloudnode/UploadPackage`、`GetPackageList`、`GetPackageDetail`、`DeletePackage`、`GetPackageDownloadURL`。
- admin gateway resolver 已保留服务名映射：`cloudnode -> moox_cloudnode`、`collector/collectmgr -> moox_collector`，这属于服务部署名适配，不是针对单个接口的特殊逻辑。
- 旧接口名 `GetCloudAccount`、`GetPackageOptions`、`GetNodeDetail`、`UpdateNodeFunction`、`GetSCFDeployInfo` 等未发现源码调用残留。

远端与构建产物状态：

- 远端 web-host 当前 `cloud-account` chunk 已包含 `ListCloudAccounts`，说明不是旧 admin 接口调用问题。
- 远端 `cloud-function` chunk 的错误提示仍是旧文案，和当前源码不一致；这是静态资源未重新构建/发布导致的生成物滞后问题。
- `web/dist` 仍包含历史 chunk（例如旧 `cloud-function-async`），但它是生成物，当前源码路由没有入口到该页面；不要手工删单个 chunk，下一次正式前端构建会整体刷新。

补充复查：

- 仓库中未发现 `*_test.go`、前端 `*.spec.*` / `*.test.*` 残留。
- 未发现 `data/moox.db` / `moox.db` 旧数据库路径残留；admin 默认路径已经统一为 `data/admin.db`。
- 未发现旧 `tasks_md5`、`HeartbeatTaskInstance`、`collectexec.timer`、`ScheduledExecute` 等本地任务缓存/定时执行链路残留。

## 2026-07-03 继续清理：cloudnode 批量管理旧异步壳

本轮发现 `CloudNodeMgr.BatchCreateNodes`、`BatchDeleteNodes`、`BatchDeployNodes` 虽然已经迁移到独立 `modules/cloudnode`，但实现仍只是向通用 `t_cloud_async_jobs` 写入 `cloudnode.batch_*` 管理 job。这类 job 依赖云 runtime 拉取执行，不适合作为 cloudnode 自身 catalog 管理操作，容易出现“管理台显示已提交，但节点 catalog 未真正落表/更新”的旧异步壳问题。

处理结果：

- `BatchCreateNodes` 改为在 `moox-cloudnode` 服务内直接创建/更新 `t_cloud_nodes` catalog 记录。
- `BatchDeleteNodes` 改为在 `moox-cloudnode` 服务内直接软删 `t_cloud_nodes` 记录，并标记状态为 `deleted`。
- `BatchDeployNodes` 改为在 `moox-cloudnode` 服务内直接更新节点的 package/deployment 投影。
- `UpdateNode` 改为局部更新语义：只覆盖调用方显式传入的字段；心跳配置、tag、biz_type 等沿用 `metadata` 合并，不再因为 proto3 默认值覆盖已有节点字段。
- 批量创建节点 ID/函数名生成优先使用前端 metadata 中的全局 `index`，避免分 chunk 提交时使用 chunk 内序号导致重复。
- 前端云函数页面将旧 `createAsyncJob/queryAsyncJob` 命名替换为 `submitCloudNodeManagement/queryCloudNodeSubmission`，避免继续表达旧异步任务模型。
- 协议注释从“批量操作（提交异步任务）”调整为“cloudnode 独立服务内直接处理”。

涉及文件：

- `modules/cloudnode/internal/service/cloudnode/service.go`
- `modules/cloudnode/internal/repository/catalog.go`
- `modules/collect/proto/collect_service.proto`
- `web/src/api/cloud-node.ts`
- `web/src/views/collector/cloud-function/cloud-function.vue`

说明：本轮未重新生成 proto、未构建、未测试、未发布；如需发布，需要后续显式执行构建和部署流程。

## 2026-07-03 继续审计：admin 侧云节点/采集残留

本轮复查 `modules/admin`，未发现云节点、云函数、采集管理业务实现目录残留，也未发现 `t_cloud_*` / `t_collect_*` 表结构仍定义在 admin schema。

保留项均属于预期范围：

- `modules/admin/schema/service_deployments_seed.sql` 中保留 `moox_cloudnode`、`moox_collector` 的服务部署 seed，用于网关解析独立服务地址。
- `modules/admin/internal/service/sysdeploy/service.go` 中保留 `cloudnode -> moox_cloudnode`、`collectmgr -> moox_collector` 服务名映射，用于统一网关路由到独立部署服务。
- README / 注释中说明 CloudNodeMgr、CollectMgr 已迁移到独立模块，admin 仅转发。

结论：admin 侧符合“只做请求转发 + 基础管理能力”的目标，本轮未删除文件。

## 2026-07-03 继续清理：前端 cloudnode 管理轮询壳

在后端 `BatchCreateNodes` / `BatchDeleteNodes` / `BatchDeployNodes` 已改为 cloudnode 服务内直接处理后，前端不再需要旧的 URL `task_id` 恢复和轮询 manager。

处理结果：

- 删除 `web/src/utils/cloud-node-job.ts`。
- 新增 `web/src/utils/cloud-node-submission.ts`，只保留云节点管理提交状态展示所需的类型定义。
- `web/src/views/collector/cloud-function/cloud-function.vue` 移除 `CloudNodeJobManager`、URL `task_id` 写入/恢复、批量轮询定时器等旧异步壳。
- 页面展示语义从 `Job` 收敛为 `Submission/Operation`；后端协议字段 `job_id` 暂时保留，避免本轮扩大到协议兼容变更。

说明：本轮未构建、未测试、未发布。

## 2026-07-03 继续审计：collector/cloudnode/collect 旧执行链路残留

本轮复查 `modules/collector`、`modules/cloudnode`、`modules/collect`：

- 未发现 `ScheduledExecute`、`ExecuteDueTasks`、`collectexec.timer` 等旧本地定时执行链路残留。
- 未发现 `tasks_md5`、`HeartbeatTaskInstance` 等旧心跳下发任务缓存协议残留。
- 未发现 `*_test.go`、迁移/回填/临时脚本类文件残留。

结论：collector/cloudnode/collect 当前未发现上述旧链路死代码。

## 2026-07-03 继续清理：cloudnode 管理前端 operation/submission 语义

本轮继续收敛云节点管理页面的旧任务语义：

- `web/src/api/cloud-node.ts` 对外将批量管理接口返回包装为 `operation_id`、`processed_count`。
- API 包装层内部仍读取后端当前 proto 返回的 `job_id`、`total_task_cnt`，这是兼容当前 `BatchOperationRsp` 的边界适配，不再泄漏到页面调用层。
- `web/src/utils/cloud-node-submission.ts` 将展示类型改为 `SubmissionStatus`、`SubmissionStatusResponse`、`SubmissionDetailItem`。
- `web/src/views/collector/cloud-function/cloud-function.vue` 将展示字段改为 `submission_id`、`operation_type`、`submission_status`，页面文案从“任务类型/任务完成”收敛为“操作类型/提交完成”。

说明：通用云执行任务协议（`SubmitJobs` / `PollJobs` / `ReportJobStatus`）仍保留 `job_id`，该语义属于 SCF runtime 通用云任务，不属于本轮 cloudnode 管理旧异步壳。

## 2026-07-03 继续清理：cloudnode 管理操作命名与文档边界

本轮继续收敛 cloudnode 管理面与通用云执行 job 的边界：

- `web/src/views/collector/cloud-function/cloud-function.vue` 内部构造批量管理请求的局部命名从 `taskType/requestParams/tasks` 调整为 `operationType/requestPayload/operations`。
- `modules/cloudnode/README.md` 中默认数据库路径修正为 `./data/moox_cloudnode.db`，与 `modules/cloudnode/config/app.yaml` 保持一致。
- `docs/云节点执行平台架构.md` 和 `docs/云节点管理.md` 增加说明：`BatchCreateNodes`、`BatchDeleteNodes`、`BatchDeployNodes` 是管理面 catalog 操作，由 `moox-cloudnode` 直接维护节点记录/部署投影，不进入 `SubmitJobs/PollJobs/ReportJobStatus` 云执行 job 队列。
- 部署脚本和模块 Makefile 检查结果：`deploy-moox.sh` 已将 `cloudnode`、`collector` 作为独立发布组件处理；admin 模块 deploy 默认排除 cloudnode/collector，符合 admin 只部署网关和基础服务的定位。

说明：本段记录的是 2026-07-03 早期过渡状态，已被后续 `BatchChangeResult.batch_id/processed_count` 改造取代；`web/src/api/cloud-node.ts` 当前不再读取管理批量变更的 `BatchOperationRsp.job_id/total_task_cnt`。

## 2026-07-03 continuation cleanup: CloudNode wording and admin boundary recheck

Continued the active split/dead-code objective with a narrow source inspection focused on old database names, admin-owned CloudNode/collector residue, and one-off migration/test files.

Findings from the inspected current state:

- No active `data/moox.db` reference was found outside the historical audit log; admin defaults continue to use `./data/admin.db`.
- `modules/admin/schema` has no CloudNode or collector-owned table definitions; CloudNode and collector data remain module-owned.
- `modules/admin/internal/service` only references cloudnode through SysDeploy service-deployment metadata, not CloudNodeMgr business implementation.
- No project-owned `*_test.go`, `*migration*`, `*migrate*`, `*legacy*`, or `*mock*` file was found under active `modules`, `scripts`, `examples`, or `web/src` paths.

Cleanup applied:

- Updated the CloudNode proto section comment from batch operation wording to batch change wording.
- Updated default service-deployment descriptions for `moox_cloudnode` to use Chinese `异步执行队列和同步调用` wording instead of mixed `job` / `invocation` terminology.

No build, test, deploy, or git command was run in this pass.

## 2026-07-03 continuation cleanup: stale RPC tail and runtime artifact scan

Continued the active split/dead-code objective by checking previously noted stale RPC tails and rebuildable runtime artifacts.

Stale RPC tail check:

- Current `modules/collect/proto/collect_service.proto` no longer contains the previously noted dead `GetPackageOptions` RPC or related package-option messages.
- Current `modules/admin/proto/ops_service.proto` no longer contains the previously noted SSH one-off command RPC or monitor enable/disable/status/test RPCs.
- Current frontend/package API references to package options are collector packager CLI option structs, not the removed cloudnode RPC.

Runtime artifact check:

- No `.db`, `.sqlite`, `.sqlite3`, SQLite WAL/SHM, `.zip`, `.tar.gz`, `.log`, or `cover.out*` artifacts were found under the repository outside pruned dependency/build directories.
- The only `data` directory hit under the checked active paths was `web/src/views/data`, which is frontend source code rather than runtime data.
- Remaining `./data/moox_cloudnode.db`, `./data/moox_collector.db`, and `./data/moox_trade.db` references are module-owned default database paths, not the removed admin `data/moox.db` layout.

Implementation update:

- Regenerated `modules/collect/proto/collectgen` after the CloudNode batch-change proto comment cleanup so generated code stays aligned with proto source.

No build, test, deploy, or git command was run in this pass.

## 2026-07-03 continuation cleanup: active compatibility wording scan

Scanned active source, scripts, examples, and frontend source for compatibility/deprecation/test/migration wording while excluding generated code and dependency/build directories.

Cleanup applied:

- Removed the remaining active-source wording that described an old SSH standalone HTTP port from `modules/admin/internal/service/ssh/rpc/rawhandler.go`.
- Updated `modules/admin/internal/bootstrap/services.go` so the SSH comment describes the current `/api/admin/ssh/*` rawhandler entrypoint without mentioning the removed standalone port.

Not changed:

- `web/src/lang/index.ts` keeps `legacy: false`; this is a vue-i18n Composition API option, not legacy MooX compatibility logic.
- Go module `+incompatible` version suffixes are dependency metadata, not project compatibility code.
- `STORAGE_SCHEMA_FILE` remains in the storage deployment path because it points the independently deployed storage process at its module-owned schema file; it is not the removed admin external schema override path.

No build, test, deploy, or git command was run in this pass.

## 2026-07-03 continuation cleanup: frontend boundary and empty agent directories

Checked active frontend/API/admin gateway paths for old mock or pre-split CloudNode/CloudFunction routes.

Findings:

- No active `/mock/*` endpoint calls remain in `web/src` or active module code.
- No active old `/api/admin/cloud-function` or `/api/admin/cloudfunction` path remains.
- No active old `/api/service/storage` path remains; storage frontend calls continue through `/api/admin/{storage service}` and SCF/storage direct paths are handled outside the admin frontend.
- Cloud account and cloud function package frontend APIs call `callControl('cloudnode', ...)`, so admin remains a gateway boundary for those requests.

Cleanup applied:

- Removed empty admin agent metadata directories:
  - `modules/admin/.claude`
  - `modules/admin/internal/.claude`
  - `modules/admin/internal/service/.claude`

No build, test, deploy, or git command was run in this pass.

## 2026-07-03 continuation cleanup: deployment boundary and empty directory scan

Checked deployment packaging and admin gateway configuration for module-boundary residue.

Findings:

- `scripts/deploy-moox.sh` packages admin, cloudnode, collector, and storage schemas into their own service directories. CloudNode/collector schemas are not copied into the admin schema directory.
- Admin gateway forwarding continues to resolve `/api/admin/*` and `/api/service/*` targets from `t_service_deployments`.
- Admin code references cloudnode/collector only as deployment metadata or gateway target aliases; no admin-local CloudNodeMgr/CollectMgr business implementation was found in the checked paths.

Cleanup applied:

- Removed empty agent metadata directories:
  - `web/.claude`
  - `modules/cli/.claude`
  - `modules/storage/.claude`
  - `modules/storage/internal/services/.claude`
- Removed empty dead command directory:
  - `modules/trade/cmd/binance-verify`

Not changed:

- The empty `openspec/changes` workflow directory was left in place because it belongs to the OpenSpec workflow structure rather than runtime dead code.

No build, test, deploy, or git command was run in this pass.

## 2026-07-03 continuation cleanup: frontend unified response protocol

Checked admin service directories, admin proto/config/schema, frontend API wrappers, and deployment script references for old CloudNode/collector business ownership residue.

Findings:

- `modules/admin/internal/service` contains only current admin-local foundation services: auth, database, dnsproxy, monitor, secret, space, ssh, and sysdeploy.
- CloudNode/collector hits in admin code are service deployment metadata, gateway target aliases, or menu/API names, not admin-local business implementations.
- Collector rules and task instance pages already handle the unified `ret_info` response format from `/api/admin/collectmgr/*`.

Cleanup applied:

- Removed the old frontend axios compatibility branch that accepted `{ code, data }` collector-style responses from `web/src/api/index.ts`.
- The shared frontend axios client now treats `ret_info` as the single valid admin gateway response envelope.

No build, test, deploy, or git command was run in this pass.

## 2026-07-03 continuation cleanup: strict RetInfo success code

Checked frontend response handling after removing the old `{code,data}` collector response branch.

Findings:

- Backend proto defines `ErrorCode.SUCCESS = 0` in the shared `moox_common.proto` files.
- Active backend services return success through `RetInfo.Code = ErrorCode_SUCCESS`.
- Collector internal clients already treat `ret_info.code == 0` as the success condition.

Cleanup applied:

- Tightened `web/src/api/ret-info.ts` so `isRetInfoSuccess` only accepts numeric `0` as success.
- Updated the shared frontend axios comment in `web/src/api/index.ts` to describe the strict `ret_info.code == 0` success rule.

Rationale: this removes leftover frontend compatibility with HTTP-style `200`, string `'200'`, string `'0'`, and enum-name `'SUCCESS'` success codes. MooX now uses one admin gateway envelope: `ret_info.code = 0` for success.

No build, test, deploy, or git command was run in this pass.

## 2026-07-03 continuation cleanup: strict auth RetInfo code

Rechecked the frontend `ret_info` success/auth handling against the current proto and gateway behavior.

Findings:

- Shared proto defines `NO_AUTH = 2` and `NO_PERMISSION = 3`.
- Admin gateway authentication failure responses use `RetInfo.Code = ErrorCode_NO_AUTH`.
- `NO_PERMISSION` is an authorization failure and should not be treated as an expired login session.

Cleanup applied:

- Tightened `web/src/api/ret-info.ts` so `RetInfoCode` is numeric-only and `isAuthExpiredCode` only accepts `2` (`NO_AUTH`).
- Updated `web/src/api/index.ts` error handling to use `isAuthExpiredCode(...)` instead of hard-coding `ret_info.code === 3`.
- HTTP 401 handling remains separate in the HTTP error branch.

Rationale: this removes leftover string/token-name/HTTP-style business-code compatibility and fixes the old frontend behavior that cleared login state for `NO_PERMISSION`.

No build, test, deploy, or git command was run in this pass.

## 2026-07-03 continuation cleanup: remove legacy frontend admin axios client

Continued the frontend protocol cleanup by consolidating admin API calls onto `callControl`.

Cleanup applied:

- Migrated collector rule page requests from the old shared `service.post('/api/admin/collectmgr/...')` client to `callControl('collectmgr', ...)`.
- Migrated task instance list requests from the old shared client to `callControl('collectmgr', 'GetTaskInstanceList', ...)`.
- Migrated `getUserInfoAPI` from the old `@/api` axios instance to `callControl('auth', 'GetUserInfo', ...)`.
- Updated `callControl` so callers may pass an explicit token header. This keeps login-followed-by-GetUserInfo safe even before persisted local storage has caught up.
- Removed the old `web/src/api/index.ts` axios client, which had been carrying legacy response-envelope and auth handling code.
- Tightened `ControlResponse` so the admin client no longer models old `{code,message,msg,data}` wrappers; active admin gateway calls use top-level PB JSON with `ret_info`.

No build, test, deploy, or git command was run in this pass.

## 2026-07-03 continuation cleanup: remove legacy frontend config client

Continued consolidating frontend admin API calls onto the single `callControl` client.

Cleanup applied:

- Migrated host monitor RPC calls from `api.post('/monitor/...')` to `callControl('monitor', ...)`.
- Migrated SSH management RPC calls from `api.post('/ssh/...')` to `callControl('ssh', ...)`.
- Updated SSH/Monitor pages to consume the direct PB JSON response returned by `callControl` instead of Axios-style `response.data` wrappers.
- Kept SSH rawhandler URL helpers for SFTP upload/download and WebSocket terminal unchanged because they are intentionally non-PB raw HTTP/WebSocket entrypoints.
- Removed the now-unused `web/src/api/config.ts` generic axios client.
- Removed the remaining `response.data || response` compatibility fallback in `web/src/store/modules/user-info.ts` after `getUserInfoAPI` was migrated to `callControl`.

No build, test, deploy, or git command was run in this pass.

## 2026-07-03 continuation cleanup: require ret_info in frontend admin client

After removing the legacy frontend axios clients, tightened the remaining `callControl` client contract.

Evidence checked:

- Admin proto response messages under `modules/admin/proto` all include `ret_info` as their response status field.
- Collect/CloudNode proto response messages under `modules/collect/proto` all include `ret_info` as their response status field.
- Admin gateway forwarding returns PB JSON bodies unchanged for service responses, and gateway middleware errors also use the `{ret_info}` envelope.

Cleanup applied:

- Made `ControlResponse<T>.ret_info` required in `web/src/api/admin/types.ts`.
- Updated `web/src/api/admin/http.ts` so `callControl` throws `control response missing ret_info` when an admin/control response lacks `ret_info`.

Rationale: pages should not silently accept non-PB JSON, old wrappers, or malformed responses after the admin frontend has been consolidated onto the unified gateway protocol.

No build, test, deploy, or git command was run in this pass.

## 2026-07-03 continuation cleanup: remove duplicated frontend ret_info wrappers

Continued the frontend gateway-protocol cleanup after making `callControl` require `ret_info`.

Cleanup applied:

- Removed duplicated local `unwrap(ret_info)` helpers from:
  - `web/src/api/cloud-node.ts`
  - `web/src/api/cloud-account.ts`
  - `web/src/api/function-package.ts`
- Updated cloudnode/cloud-account/function-package wrappers to rely on `callControl` as the single place that validates `ret_info`.
- Changed `uploadFunctionPackage` to return the direct PB JSON business response instead of wrapping it as `{data: rsp}`.
- Updated the function-package upload page to consume the direct response and to use `Error.message` from `callControl` instead of old Axios `error.response.data` handling.
- Removed the stale `response` variable reference from `web/src/store/modules/user-info.ts` and removed its final `response.data || response`/manual `ret_info` checks after `getUserInfoAPI` moved to `callControl`.
- Tightened `getUserInfoAPI`'s response type from `unknown` user payload to `Record<string, any>` so the store can consume current PB JSON fields without reintroducing an Axios envelope.

No build, test, deploy, or git command was run in this pass.
### 2026-07-03 Frontend ret_info contract cleanup

Tightened the frontend gateway response contract so business code no longer treats `ret_info` as optional compatibility data.

Changes:

```text
web/src/api/admin/http.ts
web/src/api/storage/http.ts
web/src/api/trade/http.ts
web/src/api/admin/secret.ts
web/src/api/cloud-node.ts
web/src/api/cloud-account.ts
web/src/api/function-package.ts
web/src/api/modules/user/index.ts
web/src/api/modules/host-monitor.ts
web/src/api/modules/ssh.ts
```

The shared admin client now requires backend RPC responses to include `ret_info`; missing `ret_info` is treated as a protocol error instead of falling back to old `{code,msg,data}` wrapper formats. Storage and trade frontend clients also require `ret_info` in response typings. Secret update/delete/toggle calls expose empty business responses to callers because `callControl` centralizes `ret_info` validation.

No build, deployment, git operation, or runtime validation was run in this step.
### 2026-07-03 active split cleanup continuation

Continued the active admin/cloudnode/collector split cleanup with a narrow source audit.

Evidence checked:

```text
modules/admin/proto
modules/admin/schema
modules/admin/internal
modules/admin/config
modules/cloudnode
modules/collector
scripts
examples
```

Findings:

- `modules/admin/internal/service` still only contains admin-local foundation services: auth, database, dnsproxy, monitor, secret, space, ssh, and sysdeploy.
- No active `data/moox.db` reference was found outside the historical audit log; admin still uses `./data/admin.db`.
- No `_test.go`, migration, mock, fixture, or coverage artifact was found in the active repository scan after excluding build/dependency directories.
- CloudNode/collector runtime heartbeat remains active: SCF keepalive still uses `ProcessProbe -> ReportHeartbeat -> PollJobs`, so this path was not deleted.

Changes:

- Updated the Trade database manager comment to describe applying the embedded module schema, not a one-off table migration.
- Updated CloudNode async execution documentation and comments to describe the runtime queue as `job_item` while preserving the existing `SubmitJobs` / `PollJobs` / `ReportJobStatus` protocol names and `job_id` wire fields.
- Removed a duplicate stale `云执行 job 队列` sentence from `docs/云节点执行平台架构.md`.

No build, deployment, git operation, or runtime validation was run in this step.
### 2026-07-03 frontend and CLI split-boundary scan

Checked frontend, CLI, and scripts for old admin-owned cloud function / package / cloud account paths.

Searched areas:

```text
web/src
modules/cli
scripts
```

Result:

- No old `/api/admin/cloudfunction`, `/api/service/cloudfunction`, `PackageMgr`, or `data/moox.db` caller was found.
- Frontend cloud node, cloud account, and function package APIs call `/api/admin/cloudnode/*` through the shared admin gateway client.
- CLI cloudnode helpers call `/api/admin/cloudnode/*` or `/api/service/cloudnode/*`, matching the independent `moox-cloudnode` boundary.
- Remaining `cloud_account_id` hits are protocol fields, not admin-owned cloud account persistence.

No build, deployment, git operation, or runtime validation was run in this step.
### 2026-07-03 examples/e2e rebuild entrypoint

Checked `examples/` and `scripts/` for rebuild-data responsibilities and old direct table writes.

Result:

- `examples/` contains domain seed files for Storage metadata and platform topology.
- No old `data/moox.db`, `t_cloud_*`, `t_collect*`, `t_collector*`, old cloudfunction gateway path, or direct SQLite write path was found in `examples/`.
- Deployment scripts still rewrite module-owned database paths for deployed runtime directories, including `admin.db`, `moox_cloudnode.db`, and `moox_collector.db`; these are runtime deployment paths, not old source data.

Changes:

- Added `examples/e2e/README.md` as the current E2E data rebuild entrypoint.
- Updated `examples/README.md` to link to the E2E rebuild flow.
- The new E2E document explicitly avoids test code, migration scripts, and direct SQLite writes. It describes rebuilding data through module startup, `moox-cli metadata import`, admin/cloudnode service APIs, collector task generation, and storage view rebuild.

No build, deployment, git operation, or runtime validation was run in this step.
### 2026-07-03 deployment script split-boundary check

Reviewed the relevant `scripts/deploy-moox.sh` sections for independent cloudnode and collector deployment behavior.

Evidence:

- Deployment flags include `--no-cloudnode`, `--no-collector`, and `--reset-data`.
- Build logic builds `moox-cloudnode`, `moox-collector`, and `moox-collector-scf` as independent binaries when enabled.
- Staging copies independent configs and schemas:

```text
modules/cloudnode/config -> cloudnode/config
modules/cloudnode/schema/cloudnode.sql -> cloudnode/schema/cloudnode.sql
modules/collector/config -> collector/config
modules/collector/schema/collector.sql -> collector/schema/collector.sql
```

- Runtime config rewriting maps module databases to independent deployed paths:

```text
../data/cloudnode/moox_cloudnode.db
../data/collector/moox_collector.db
```

- Generated runtime scripts include independent `start_cloudnode`, `start_collector`, `stop.sh cloudnode`, `stop.sh collector`, and `status.sh` entries.
- `--reset-data` removes the deployed runtime `data` directory so module-owned schemas and examples/service flows can rebuild state.

No build, deployment, git operation, or runtime validation was run in this step.
### 2026-07-03 completion evidence matrix

Added a requirement-by-requirement completion matrix:

```text
docs/admin-cloudnode-collector-split-completion-matrix-2026-07-03.md
```

The matrix maps the five active cleanup objectives to current source/script evidence and explicitly lists remaining proof needed before the goal can be marked complete. Current evidence supports the source-level split and cleanup, but full completion still requires authorized build, deployment, reset-data, and E2E runtime verification.

No build, deployment, git operation, or runtime validation was run in this step.
### 2026-07-03 build and release entrypoint cleanup

Reviewed repository build/release entrypoints:

```text
Makefile
scripts/build.sh
scripts/check-module-boundaries.sh
scripts/release.sh
modules/*/Makefile
web-host/Makefile
```

Findings:

- `scripts/build.sh` builds `moox-cloudnode`, `moox-collector`, and `moox-collector-scf` as independent binaries.
- Module Makefiles proxy to repository-level build/deploy scripts and do not expose old e2e/test/coverage targets.
- `scripts/check-module-boundaries.sh` only allows cross-module imports through generated proto packages.
- `scripts/release.sh` already packaged cloudnode/collector binaries, configs, and schemas separately.
- `scripts/release.sh` did not copy `examples/`, while the current rebuild model and root README expect release packages to carry rebuildable examples seed data.

Change:

- Updated `scripts/release.sh` to include `examples/` in release packages.
- Updated `docs/admin-cloudnode-collector-split-completion-matrix-2026-07-03.md` so target 4 records release-package examples coverage.

No build, deployment, git operation, or runtime validation was run in this step.
### 2026-07-03 admin gateway forwarding boundary check

Reviewed the admin gateway implementation and config for old compatibility branches or hardcoded service routing.

Evidence:

```text
modules/admin/internal/gateway/resolver.go
modules/admin/internal/gateway/forward.go
modules/admin/internal/gateway/gateway.go
modules/admin/internal/gateway/init.go
modules/admin/internal/gateway/path.go
modules/admin/internal/gateway/rawhandler.go
modules/admin/config/gateway.yaml
```

Findings:

- `/api/admin/{service}/{method}` and `/api/service/{service}/{method}` share the same pure forwarding path.
- `forwardHTTP` resolves target `address` and `path` only through the runtime `t_service_deployments` resolver.
- Missing active deployment records fail visibly; gateway does not fall back to local YAML service addresses.
- No cloudnode/collectmgr/cloudfunction method alias or endpoint-specific compatibility branch was found in the inspected gateway forwarding path.
- SSH rawhandler remains a raw HTTP/multipart/websocket path and is unrelated to cloudnode/collector split.

Change:

- Updated `modules/admin/config/gateway.yaml` comments to remove stale PostgreSQL/MySQL database wording. Admin database configuration belongs to `app.yaml`; `gateway.yaml` now documents only gateway/auth/cache/JWT/service-auth/CORS/rate-limit responsibilities.
- Updated `modules/admin/internal/gateway/rawhandler.go` comment from stale `RPC dispatcher` wording to the current `forwardHTTP` pure forwarding path.
- Updated `docs/admin-cloudnode-collector-split-completion-matrix-2026-07-03.md` with gateway resolver evidence.

No build, deployment, git operation, or runtime validation was run in this step.
### 2026-07-03 cross-module runtime boundary cleanup

Found that `modules/collector/internal/cloudruntime` imported `github.com/mooyang-code/moox/modules/cloudnode/scf/runtime` directly. This contradicted the module boundary checker, which only permits cross-module imports through generated proto packages.

Change:

- Added root shared module `packages/cloudruntime`.
- Moved the generic CloudNode SCF job_item runtime and HMAC service-auth helper from `modules/cloudnode/scf/runtime` to `packages/cloudruntime`.
- Updated `modules/collector/internal/cloudruntime/poller.go` to import `github.com/mooyang-code/moox/packages/cloudruntime`.
- Updated `modules/collector/go.mod` and `go.work` for the new shared module.
- Removed the old `modules/cloudnode/scf/runtime` files.
- Updated architecture docs and the completion matrix so generic SCF runtime is no longer documented as cloudnode-private code.

No build, deployment, git operation, or runtime validation was run in this step.
### 2026-07-03 shared packages documentation cleanup

Followed up the CloudNode runtime move by updating current architecture docs.

Changes:

- Added `packages/cloudruntime/README.md` to define the shared package responsibilities and boundaries.
- Updated `docs/大仓架构.md` so root-level `packages/` is documented as an active shared-code location, not a future possibility.
- Updated the CloudNode batch-change plan glossary so `job_item` runtime ownership points to `packages/cloudruntime`.
- Updated the completion matrix with the root-level shared package boundary evidence.

Historical audit entries that described the earlier `modules/cloudnode/scf/runtime` state were left intact as history.

No build, deployment, git operation, or runtime validation was run in this step.
### 2026-07-03 collector cloudnode dependency follow-up

Followed up the shared runtime move with a narrow collector dependency scan.

Findings:

- `modules/collector` no longer imports `github.com/mooyang-code/moox/modules/cloudnode`.
- `modules/collector/go.mod` now depends on `github.com/mooyang-code/moox/packages/cloudruntime` instead of `modules/cloudnode`.
- Remaining `job_id` / `cloud_job_id` names are current wire/table field names and were intentionally not renamed in this pass.

Changes:

- Updated two collector error messages from `cloud jobs` to `cloud JobItems`.
- Clarified `docs/云节点执行平台架构.md` so the `modules/cloudnode/internal/providers/tencent-scf` import alias is explicitly for `modules/cloudnode` internal code only.
- Updated the completion matrix to state collector uses the root-level runtime package rather than the cloudnode module.

No build, deployment, git operation, or runtime validation was run in this step.
### 2026-07-03 repository architecture document alignment

Aligned the repository architecture overview with the current workspace layout after introducing `packages/cloudruntime`.

Changes:

- Added `modules/cloudnode` and `packages/cloudruntime` to the `go.work` module list in `docs/大仓架构.md`.
- Updated the repository tree to include `modules/cloudnode`, `modules/collect`, and `packages/cloudruntime`.
- Removed the stale `modules/account` tree entry.
- Updated the CloudNode responsibility wording from async `job` to async `job_item`.
- Added `packages/cloudruntime` to the module responsibility table as the root-level shared CloudNode job_item runtime package.

No build, deployment, git operation, or runtime validation was run in this step.
### 2026-07-03 packages boundary checker enhancement

Strengthened the module boundary checker after introducing `packages/cloudruntime`.

Change:

- `scripts/check-module-boundaries.sh` now also scans `packages/`.
- Root shared packages are forbidden from importing `github.com/mooyang-code/moox/modules/...`.
- Business modules may depend on stable root packages under `packages/...`, while cross-business-module imports remain limited to generated proto packages.
- Updated `docs/大仓架构.md` and the completion matrix with the new packages boundary rule.

No build, boundary-check command, deployment, git operation, or runtime validation was run in this step.
### 2026-07-03 release package source-script cleanup

Reviewed the relationship between binary release packages and source-only developer scripts.

Finding:

- `scripts/release.sh` produced a binary/config/docs/examples package, but also copied the whole source `scripts/` directory.
- Scripts such as `build.sh`, `release.sh`, `check-module-boundaries.sh`, and `build-collector-scf-package.sh` require source tree context and are misleading inside a binary release package.
- Runtime storage start/stop helpers are already copied into `storage/start.sh` and `storage/stop.sh`.

Change:

- Updated `scripts/release.sh` so binary release packages no longer copy the full `scripts/` directory.
- Updated `skills/moox/references/release.md` and `docs/大仓架构.md` to clarify that source-only developer scripts remain in the repository, not in binary release packages.
- Updated the completion matrix with this release packaging boundary.

No build, release command, deployment, git operation, or runtime validation was run in this step.

### 2026-07-03 continuation: gateway fallback wording superseded

Continued the active split/dead-code objective with a narrow current-state inspection focused on:

```text
data/moox.db
schema_migrations / migration / migrate
admin-owned CloudNodeMgr / CollectMgr implementation
old cloudfunction / PackageMgr / AsyncTask endpoints
gateway.yaml service-address fallback wording
```

Current findings:

- No active `data/moox.db` reference was found outside this historical audit log; admin still defaults to `./data/admin.db`.
- No project-owned migration, fixture, mock, coverage, or `_test.go` file was found under active `modules`, `scripts`, `examples`, `web/src`, or `skills/moox` paths.
- Admin source references CloudNode/Collector only through service-deployment metadata and gateway routing comments, not through admin-local CloudNodeMgr/CollectMgr business implementations.
- Current admin gateway code resolves service targets from `t_service_deployments` active records and returns an error when the record is missing; `gateway.yaml` no longer owns service addresses or fallback targets.

Cleanup performed:

- Marked older audit entries that described `gateway.yaml` service-address fallback as superseded by the 2026-07-03 `t_service_deployments`-only gateway target cleanup.
- Did not run build, release, deploy, git, or runtime validation commands.
### 2026-07-03 release package skills cleanup

Reviewed binary release packaging for repository skills after removing source-only scripts from release packages.

Finding:

- `scripts/release.sh` still copied the whole `skills/` directory.
- `skills/dev-helper` is explicitly an internal maintainer workflow with source-tree deployment scripts.
- `skills/debug` and `skills/moox` also reference source repository commands such as build/deploy scripts.
- Keeping skills in a binary release package would imply these source workflows are available in the runtime package, even though the source scripts are intentionally not copied.

Change:

- Updated `scripts/release.sh` so binary release packages no longer copy `skills/`.
- Updated release and architecture docs to clarify that repository skills stay in the source tree.
- Updated the completion matrix with this release packaging boundary.

No build, release command, deployment, git operation, or runtime validation was run in this step.
### 2026-07-03 release versus deploy wording cleanup

Clarified the difference between binary release archives and runnable deployment directories.

Findings:

- The root README described `deploy-moox.sh` output as a release package, which was easy to confuse with `make release`.
- `skills/moox/references/release.md` said deployment uploads docs, but `deploy-moox.sh` creates a runnable deployment directory with binaries, configs, schemas, examples, and runtime helper scripts.
- `docs/大仓架构.md` had the `packages/cloudruntime` responsibility row outside the markdown table.

Changes:

- Updated `README.md` to describe `make release` and `make deploy` separately.
- Updated `skills/moox/references/release.md` to distinguish binary archive contents from deploy directory contents.
- Updated `docs/大仓架构.md` wording and fixed the `packages/cloudruntime` table row.

No build, release command, deployment, git operation, or runtime validation was run in this step.
### 2026-07-03 stale module and job_item wording cleanup

Checked docs and repository skills for stale module and CloudNode async execution wording.

Changes:

- Updated `docs/大仓架构.md` so old account/order source systems are described as capabilities now folded into `modules/trade`, not as current standalone modules.
- Updated `docs/架构总览.md` from CloudNode `job` wording to `job_item` wording.
- Updated `skills/moox/SKILL.md` from account/order module wording to current cloudnode/trade module wording.
- Updated debug skill references from CloudNode job wording to CloudNode job_item wording.
- Replaced stale `create_job_id` debug wording with create-node `batch_id`, matching the current BatchCreateNodes result semantics.

No build, release command, deployment, git operation, or runtime validation was run in this step.

### 2026-07-03 continuation cleanup: remove service auth test helper

Removed `GenerateServiceAuthHeaderForTest` from `modules/admin/internal/gateway/service_auth.go`.

Rationale: the function was an active-source test helper with no caller in current source, scripts, examples, web code, or docs. Keeping it contradicted the current objective to remove feature/unit-test and one-off helper code from the production source tree.

Kept the real service-auth implementation paths:

```text
normalizeServiceAuthConfig
currentServiceAuthConfig
validateServiceAuthHeader
generateServiceAuthSignature
```

No build, release command, deployment, git operation, or runtime validation was run in this step.

### 2026-07-03 continuation cleanup: collector runtime name alignment

Continued the active split/dead-code objective by removing active old `data-collector` runtime identity defaults now that collection is owned by the independent `moox-collector` module.

Changed active defaults from old project/service names to `moox-collector` in:

```text
modules/cli/cmd/collector.go
modules/collector/pkg/config/local_config.go
modules/collector/pkg/logger/logger.go
modules/collector/pkg/httpclient/client.go
modules/collector/internal/dnsproxy/probe.go
modules/collector/configs/config.yaml
modules/collector/configs/sources/market/binance.yaml
```

Also updated admin-facing wording from old `MooX Server` single-service phrasing to `MooX Admin` in:

```text
modules/admin/config/app.yaml
docs/数据库管理.md
docs/认证鉴权.md
```

Notes:

- Kept `data_collector` package/biz type values unchanged in this pass because they are active protocol/filter values across schema, frontend, CLI, and cloudnode package metadata.
- Updated the completion matrix to record this active old-project-name cleanup.
- Did not run build, release, deploy, git, gofmt, or runtime validation commands.

### 2026-07-03 continuation cleanup: source-module wording alignment

Continued the active split/dead-code objective by checking current source and docs for stale single-repo/source-module wording such as:

```text
MooX Server
moox-server
multi-source-data-collector
data-collector
moox/server
modules/account
modules/order
```

Current findings:

- `data_collector` remains an active package/biz type value across schema, frontend, CLI, and cloudnode package metadata; it was intentionally not renamed in this pass.
- `cloud-function` / `cloudfunction` hits are current SCF page and collector SCF runtime package names, not old admin `/api/admin/cloudfunction` endpoints.
- The stale architecture wording was in `docs/大仓架构.md`, where old source modules were described as if they were still current independent Go modules.

Cleanup performed:

- Updated `docs/大仓架构.md` to describe `data-collector`, `moox/server`, `xData-mini/*`, and `data-miner` as pre-merge source modules, while the current repository is organized by `modules/*` and root-level `packages/*`.
- Did not run build, release, deploy, git, gofmt, or runtime validation commands.

### 2026-07-03 continuation cleanup: job_item wording in deployment metadata

Continued the active split/dead-code objective by checking current deployment defaults and architecture docs for stale CloudNode async execution wording.

Cleanup performed:

- Updated `modules/admin/schema/service_deployments_seed.sql` and `modules/admin/internal/service/sysdeploy/defaults.go` so the default `moox_cloudnode` deployment description says `异步 job_item 队列` instead of generic `异步执行队列`.
- Updated `docs/云节点执行平台架构.md` and `docs/采集任务管理.md` from CloudNode async `job` wording to CloudNode `job_item` wording.
- Updated the collector CloudNode client comment so it describes the returned value as a job_item id, while keeping the current protocol field names unchanged.

Notes:

- `SubmitJobs`, `PollJobs`, `ReportJobStatus`, and wire-level `job_id` remain current protocol names in this pass.
- Did not run build, release, deploy, git, gofmt, or runtime validation commands.

### 2026-07-03 continuation cleanup: admin SQLite boundary tightening

Continued the active split/dead-code objective by checking Admin database docs and implementation against the current `data/admin.db` boundary.

Cleanup performed:

- Removed unused `database.Manager.CreateInstance` from `modules/admin/internal/service/database/manager.go`; it had no caller and only returned the same `*gorm.DB`, so it was not a real independent connection path.
- Renamed the admin database override environment variable in `modules/admin/internal/config/app.go` from generic `DB_PATH` to `MOOX_ADMIN_DB_PATH`, avoiding old single-service/global database wording.
- Updated `docs/数据库管理.md` so Admin is documented as SQLite-only and no longer claims MySQL/PostgreSQL support or MySQL backup/restore commands.
- Updated the database troubleshooting text to recommend WAL/lower write concurrency/batching instead of switching Admin to MySQL.

Notes:

- Admin still defaults to `./data/admin.db` and applies the embedded module-owned admin schema.
- CloudNode, Collector, Trade, and Storage continue to own their own module databases/schemas.
- Did not run build, release, deploy, git, gofmt, or runtime validation commands.

### 2026-07-03 continuation cleanup: trade module deployment boundary wording

Continued the active split/dead-code objective by checking module database override and gateway-target wording outside Admin.

Cleanup performed:

- Renamed Trade database override from generic `DB_PATH` to `MOOX_TRADE_DB_PATH` in `modules/trade/internal/config/app.go`, matching the module-owned database boundary.
- Updated `modules/trade/README.md` so Trade admin API forwarding is documented as coming from `t_service_deployments` active deployment records, not `modules/admin/config/gateway.yaml` static service mappings.
- Documented Trade environment variables `MOOX_TRADE_DB_PATH` and `MOOX_ENCRYPTION_KEY` in the module README.

Notes:

- This aligns Trade with the same post-split deployment model as CloudNode and Collector: module-owned DB + service discovery through `t_service_deployments`.
- Did not run build, release, deploy, git, gofmt, or runtime validation commands.

### 2026-07-03 continuation cleanup: modules README gateway wording

Continued the active split/dead-code objective by checking repository module overview docs for stale gateway target and CloudNode async execution wording.

Cleanup performed:

- Updated `modules/README.md` so CloudNode is described as owning async `job_item` execution, not generic async jobs.
- Updated `modules/README.md` so admin gateway forwarding is documented as resolving targets from `t_service_deployments` active deployment records; `config/gateway.yaml` no longer owns service addresses.

Notes:

- Other `gateway.yaml` references remain valid when they refer to JWT, no-auth paths, service-auth signing, CORS, rate-limit, or cache configuration.
- Did not run build, release, deploy, git, gofmt, or runtime validation commands.

### 2026-07-03 continuation cleanup: admin auth debug and stale env hint

Continued the active split/dead-code objective by checking active Admin auth/config paths for old debug and stale environment-variable hints.

Cleanup performed:

- Removed the debug log that printed the decrypted plaintext password from `modules/admin/internal/common/crypto/aes.go`.
- Replaced the full `%+v` user object login log in `modules/admin/internal/service/auth/impl/login.go` with a narrow `user_id` / `username` log, avoiding password hash and salt leakage.
- Updated `modules/admin/config/gateway.yaml` JWT default placeholder from the stale `JWT_SECRET` env hint to a plain development placeholder.

Notes:

- The current JWT secret still comes from `gateway.yaml`; the old `JWT_SECRET` environment override is not part of the current config contract.
- Did not run build, release, deploy, git, gofmt, or runtime validation commands.

### 2026-07-03 continuation cleanup: module-specific encryption env vars

Continued the active split/dead-code objective by checking module encryption key overrides for old single-service/global environment variable usage.

Cleanup performed:

- Changed Admin sensitive-data encryption override from global `MOOX_ENCRYPTION_KEY` to `MOOX_ADMIN_ENCRYPTION_KEY` in `modules/admin/internal/common/crypto/key.go`.
- Changed Trade API credential encryption override from global `MOOX_ENCRYPTION_KEY` to `MOOX_TRADE_ENCRYPTION_KEY` in `modules/trade/internal/config/app.go`.
- Updated `modules/trade/README.md` and `modules/trade/config/app.yaml` to stop implying Trade must share Admin's encryption-key setting.

Notes:

- Each module can still be configured with the same literal key value if an operator intentionally wants shared decryption semantics, but the environment variable names are now module-scoped.
- Did not run build, release, deploy, git, gofmt, or runtime validation commands.

### 2026-07-03 continuation cleanup: cloudnode and collector DB env vars

Continued the active split/dead-code objective by aligning CloudNode and Collector database path overrides with the module-owned database boundary.

Cleanup performed:

- Added `MOOX_CLOUDNODE_DB_PATH` override support in `modules/cloudnode/internal/config/config.go`.
- Added `MOOX_COLLECTOR_DB_PATH` override support in `modules/collector/internal/control/config/config.go`.
- Documented both variables in `modules/cloudnode/README.md` and `modules/collector/README.md`.

Notes:

- Admin, CloudNode, Collector, and Trade now use module-scoped database path override names instead of a shared `DB_PATH` style global name.
- Did not run build, release, deploy, git, gofmt, or runtime validation commands.

### 2026-07-03 continuation audit: collector static dependency addresses remain

Continued the active split/dead-code objective by checking module README/config files and Collector control-plane service construction for service-deployment boundary alignment.

Finding:

- `modules/collector/config/app.yaml` still contains local static dependency addresses:

```text
cloudnode.address: 127.0.0.1:11401
storage.metadata_url: http://127.0.0.1:20200
storage.access_url: http://127.0.0.1:20201
```

- `modules/collector/internal/service/collectmgr/service.go` currently constructs clients directly from those config values:

```text
storageclient.NewDatasetSource(cfg.Storage.MetadataURL)
cloudnodeclient.New(cfg.CloudNode.Address)
```

Assessment:

- This is not old admin-owned CloudNode/Collector implementation code, but it is still a local static service-address dependency inside an independently deployed module.
- It should be handled in a follow-up collector service-discovery pass so Collector resolves CloudNode/Storage dependencies from the Admin SysDeploy / `t_service_deployments` source of truth, or receives the same active deployment payload through a clearly defined bootstrap contract.
- A safe implementation likely needs a small Collector dependency resolver/client design rather than a local string rename, because `/api/service/*` gateway calls require backend service-auth signing while direct CloudNode/Storage calls currently do not.

No code change was made for this finding in this pass. Did not run build, release, deploy, git, gofmt, or runtime validation commands.

### 2026-07-03 continuation cleanup: collector static dependency config marked local default

Followed up on the Collector static dependency address finding.

Cleanup performed:

- Updated `modules/collector/config/app.yaml` comments to mark `cloudnode.address`, `storage.metadata_url`, and `storage.access_url` as local development defaults.
- The comment now states production deployment should resolve dependency services through `t_service_deployments` / SysDeploy active deployment information.
- Updated the completion matrix to keep this as an explicit remaining implementation gap, not as completed work.

No runtime behavior was changed in this pass. Did not run build, release, deploy, git, gofmt, or runtime validation commands.

### 2026-07-03 continuation implementation: collector SysDeploy dependency discovery

Implemented the first Collector control-plane dependency-discovery pass for the service deployment boundary.

Changes:

- Added `modules/collector/internal/control/discovery/dependencies.go`.
- Collector can now call Admin `/api/service/sysdeploy/ListActiveServiceDeployments` with backend HMAC auth and read `deployment_map` from SysDeploy.
- Added `sysdeploy.admin_gateway_url` and `sysdeploy.service_auth` to `modules/collector/internal/control/config/config.go` and `modules/collector/config/app.yaml`.
- Added env overrides:

```text
MOOX_COLLECTOR_ADMIN_GATEWAY_URL
MOOX_SERVICE_AUTH_VERSION
MOOX_SERVICE_AUTH_ACCESS_KEY
MOOX_SERVICE_AUTH_SECRET_KEY
MOOX_SERVICE_AUTH_EXPIRE_SECONDS
```

- Updated `modules/collector/internal/service/collectmgr/service.go` so CollectMgr dependency clients are constructed from resolved dependencies:

```text
moox_cloudnode / cloudnode       -> CloudNode client
storage_metadata                 -> DatasetSource metadata URL
storage_access                   -> available resolved access URL for future use
```

Behavior:

- If `sysdeploy.admin_gateway_url` is empty, Collector keeps using local development defaults from `cloudnode.address` and `storage.*`.
- If SysDeploy lookup fails, Collector logs a warning and falls back to local defaults instead of blocking local development startup.

Notes:

- This removes the hard requirement that Collector dependency addresses come only from local static config, but the new path still needs build and runtime validation.
- Did not run build, release, deploy, git, gofmt, or runtime validation commands.

### 2026-07-03 continuation implementation: deploy enables collector SysDeploy discovery

Followed up on the Collector SysDeploy dependency discovery implementation by wiring deployment startup defaults.

Changes:

- Updated `scripts/deploy-moox.sh` to start `moox-collector` with environment variables for SysDeploy dependency discovery:

```text
MOOX_COLLECTOR_ADMIN_GATEWAY_URL=http://127.0.0.1:11000
MOOX_SERVICE_AUTH_VERSION=moox-auth-v1
MOOX_SERVICE_AUTH_ACCESS_KEY=moox-service
MOOX_SERVICE_AUTH_SECRET_KEY=moox-service-secret-change-me
MOOX_SERVICE_AUTH_EXPIRE_SECONDS=1800
```

- Values can still be overridden by the caller's environment.
- Updated `skills/moox/references/release.md` to document the collector discovery environment variables.
- Updated the completion matrix to note that deploy startup now enables this discovery path, while build/runtime validation is still missing.

Notes:

- This keeps local source-tree collector runs simple because `modules/collector/config/app.yaml` can still use local defaults when no admin gateway URL is configured.
- Did not run build, release, deploy, git, gofmt, or runtime validation commands.

## 2026-07-03 continuation cleanup: debug skill collector package wording

Continued the active admin/cloudnode/collector split cleanup by removing stale collector debug wording from project skills.

Updated:

```text
skills/debug/SKILL.md
skills/debug/references/scf-e2e-debug.md
```

Changes:

- Reworded old standalone `data-collector` repository references so the debug skill points maintainers to the current `modules/collector` implementation.
- Changed the SCF publish example package name from `data-collector` to `moox-collector`.
- Kept `package_type=data_collector` and `biz_type=data_collector` unchanged because those remain current protocol/filter values.

This avoids resurrecting old collector repository or package naming during future SCF publish and E2E debugging work.

## 2026-07-03 continuation cleanup: cloudnode frontend batch-change local naming

Continued the CloudNode management naming cleanup in the frontend page.

Updated:

```text
web/src/views/collector/cloud-function/cloud-function.vue
modules/cloudnode/internal/service/cloudnode/service.go
```

Changes:

- Renamed cloudnode management page local helpers and variables from `operation(s)` to `batchChange(s)` for node create/delete/deploy catalog changes.
- Kept backend request semantics unchanged; these are still CloudNode management `BatchCreateNodes`, `BatchDeleteNodes`, and `BatchDeployNodes` calls.
- Reworded the CloudNode service clock helper comment so it no longer references package-level tests as a reason for existing.

This further separates CloudNode management `batch_change` terminology from SCF runtime `job_item` / job execution terminology.

## 2026-07-03 continuation cleanup: current docs old-name wording

Continued reducing stale operational wording after the admin/cloudnode/collector split.

Updated:

```text
skills/debug/SKILL.md
docs/云节点管理.md
docs/代码包管理.md
docs/admin-cloudnode-collector-split-completion-matrix-2026-07-03.md
```

Changes:

- Reworded the debug skill so current operational guidance says old standalone collector repository paths generically instead of naming the pre-merge `data-collector` checkout as something to interact with.
- Reworded current code-package docs from old admin `PackageMgr` terminology to admin built-in package-management wording.
- Reworded the completion matrix old-path row so it no longer exposes `PackageMgr` as a current named object.
- Reworded the collector runtime identity row so current evidence points to `moox-collector` without repeating old runtime names in the active checklist.

Historical architecture notes can still mention pre-merge project names when explaining repository lineage; those are not operational instructions.

## 2026-07-03 continuation cleanup: examples e2e rebuild contract

Continued objective 4 by strengthening the examples-based data rebuild runbook.

Updated:

```text
examples/e2e/README.md
```

Changes:

- Added a runtime data ownership table for admin, cloudnode, collector, storage, and trade data after deleting local runtime files.
- Clarified that `examples/` only owns reusable storage metadata/platform seeds and does not maintain SQLite seed data for admin/cloudnode/collector/trade tables.
- Added the Binance spot 1m view seed import to the minimal crypto metadata rebuild path.
- Added a minimal end-to-end demo loop: admin service deployments, storage metadata import, cloud account recreation, collector SCF package/node deployment, collector rule generation, SCF job_item execution, storage write, and view browsing.
- Documented that failures should be debugged through service boundaries rather than manual SQLite writes.

No E2E commands were run in this continuation; runtime verification still requires explicit build/start/reset authorization.

## 2026-07-03 continuation audit: migration/test/dead-code residue scan

Continued objective 5 with a narrow current-state scan for one-off migration, fixture, mock, coverage, and project-owned unit/functional test residues.

Scanned active project paths:

```text
modules
scripts
examples
web/src
packages
```

Patterns checked at file-name level:

```text
*migration*
*migrate*
*fixture*
*mock*
*_test.go
*testdata*
*coverage*
```

Patterns checked at source-text level:

```text
schema_migrations
migration
migrate
fixture
mock
coverage
_test
testdata
单测
功能测试
迁移脚本
表迁移
数据迁移
```

Result:

- No active migration/migrate/mock/fixture/coverage/testdata file was found in the scanned project-owned paths.
- No project-owned `_test.go` file was found in the scanned paths.
- No `schema_migrations` table or migration table definition was found under module schemas/internal code, scripts, or examples.
- Remaining text hits are not dead test or migration code:
  - `examples/e2e/README.md` explicitly states that examples do not contain functional test code or migration scripts.
  - Proto Makefiles use `--mock=false` to avoid generating mock code.
  - DNS proxy and trading UI hits are runtime features for ping/test-connection behavior, not unit/functional test artifacts.

No files were deleted in this pass because the scan did not identify a confirmed dead artifact to remove. No build, test, deployment, git, or E2E command was run.

## 2026-07-03 continuation cleanup: admin monitor metrics scraper naming

Continued objective 1 and objective 5 by removing a misleading admin-local `collector` name that was unrelated to the independent `moox-collector` service.

Updated:

```text
modules/admin/internal/service/monitor/collector.go -> modules/admin/internal/service/monitor/scraper.go
modules/admin/internal/service/monitor/impl.go
```

Changes:

- Confirmed the file was admin monitor's Node Exporter HTTP metrics scraping logic, not old CollectMgr or cloud-function collection business logic.
- Renamed the monitor helper type from `Collector` to `MetricsScraper`.
- Renamed the constructor from `newCollector` to `newMetricsScraper`.
- Renamed the service field from `collector` to `metricsScraper`.
- Renamed the source file to `scraper.go` so admin-local monitor code no longer appears as a collector business implementation during boundary scans.

Admin still owns resource monitoring as an admin-local ops capability. Collector task rules, task instances, planner, and SCF collection execution remain owned by the independent `moox-collector` module.

No build, gofmt, test, deployment, git, or E2E command was run in this continuation.

## 2026-07-03 continuation audit: admin boundary references and collector job wording

Continued the admin/cloudnode/collector boundary cleanup with a focused scan of admin-side references.

Checked:

```text
modules/admin
README.md
docs/架构总览.md
docs/大仓架构.md
docs/云节点管理.md
docs/采集任务管理.md
docs/admin-cloudnode-collector-split-completion-matrix-2026-07-03.md
```

Findings:

- `modules/admin` references to `collector`, `collectmgr`, and `cloudnode` are service deployment seeds, gateway forwarding documentation, or SysDeploy resolver aliases.
- No admin schema or internal implementation reference to `t_cloud_*`, `t_collect*`, `cloud_account`, `function_package`, `task_rule`, `task_instance`, or `async_job` was found in the checked admin paths.
- The previous admin-local monitor `collector.go` false positive has already been renamed to `scraper.go` / `MetricsScraper`.

Cleanup performed:

```text
docs/采集任务管理.md
```

- Reworded `采集 job 下发到 SCF 云节点` to `采集 job_item 下发到 SCF 云节点`.
- Reworded `cloudnode job 协议` to `CloudNode job_item 协议`.

This keeps collector task-instance terminology separate from CloudNode runtime job_item terminology while preserving the current wire RPC names such as `SubmitJobs`, `PollJobs`, and `ReportJobStatus`.

No build, gofmt, test, deployment, git, or E2E command was run in this continuation.

## 2026-07-03 continuation audit: release and deploy boundary scan

Continued objectives 2 and 4 by checking release/deploy scripts for independent module packaging and examples-based rebuild support.

Checked:

```text
scripts/build.sh
scripts/release.sh
scripts/deploy-moox.sh
skills/moox/references/release.md
README.md
modules/admin/README.md
modules/cloudnode/README.md
modules/collector/README.md
```

Findings:

- `scripts/build.sh` builds `moox-admin`, `moox-cloudnode`, `moox-collector`, `moox-collector-scf`, `moox-storage`, `moox-factor`, `moox-trade`, and `moox-web-host` as independent binaries.
- `scripts/deploy-moox.sh` supports `--no-cloudnode` and `--no-collector`, stages cloudnode and collector configs/schemas separately, and starts them with independent runtime directories.
- Deployed SQLite paths remain module-owned: admin uses `../data/admin.db`, cloudnode uses `../data/cloudnode/moox_cloudnode.db`, and collector uses `../data/collector/moox_collector.db`.
- No script hit was found for old `data/moox.db`, old admin cloudfunction endpoints, old PackageMgr/AsyncTask/asynctask flow, old `data-collector` package name, direct `sqlite3` manipulation, or direct `t_cloud_accounts` access.
- `scripts/release.sh` copies `examples/` into binary release packages.
- `scripts/deploy-moox.sh` stages `examples/` and syncs/extracts it into deployment directories, so deleting runtime `data/` does not remove the seed/runbook material needed for rebuild.

Updated:

```text
docs/admin-cloudnode-collector-split-completion-matrix-2026-07-03.md
```

- Added explicit evidence that deploy directories carry `examples/` after local or remote deployment.

No build, release, deploy, test, git, or E2E command was run in this continuation.

## 2026-07-03 continuation audit: admin schema ownership scan

Continued objective 3 and objective 5 by reviewing admin-local schema tables for unused old business leftovers.

Checked:

```text
modules/admin/schema/admin.sql
modules/admin/internal/service
modules/admin/internal/gateway
web/src
docs/架构总览.md
docs/数据库管理.md
docs/admin-cloudnode-collector-split-completion-matrix-2026-07-03.md
```

Admin schema tables reviewed:

```text
t_spaces
t_space_members
t_service_deployments
t_users
t_login_history
t_user_actions
t_ssh_host
t_ssh_session
t_host_monitor_history
t_secrets
```

Findings:

- `t_spaces` / `t_space_members` belong to admin Space management.
- `t_service_deployments` belongs to SysDeploy and gateway target resolution.
- `t_users`, `t_login_history`, and `t_user_actions` belong to admin auth and audit logging.
- `t_ssh_host` and `t_ssh_session` belong to admin SSH host/session management.
- `t_host_monitor_history` belongs to admin resource monitoring.
- `t_secrets` belongs to admin-local secret management for non-cloudnode-owned credentials.
- No `t_cloud_*`, `t_collect*`, cloud account, cloud function package, task rule, task instance, or async work queue table is present in admin schema.

No admin schema table was deleted in this pass because the reviewed tables are still owned by current admin-local基础能力 rather than old cloudnode/collector business logic.

Updated:

```text
docs/admin-cloudnode-collector-split-completion-matrix-2026-07-03.md
```

- Added explicit evidence that current admin-local tables have valid ownership and are not旧 cloudnode/collector tables.

No build, gofmt, test, deployment, git, or E2E command was run in this continuation.

## 2026-07-03 continuation cleanup: collector schema runtime seed removal

Continued objectives 3, 4, and 5 by reviewing `modules/collector/schema/collector.sql` ownership and removing runtime seed data from the schema file.

Checked:

```text
modules/collector/schema/collector.sql
modules/collector/internal/domain
modules/collector/internal/repository
modules/collector/internal/service/collectmgr
docs/采集任务管理.md
docs/架构总览.md
docs/admin-cloudnode-collector-split-completion-matrix-2026-07-03.md
modules/collector/README.md
examples/e2e/README.md
```

Findings:

- `t_collector_task_rules` is used by `domain.TaskRule` and `TaskRuleRepository`.
- `t_collector_task_instances` is used by `domain.TaskInstance`, planner, collectmgr service, and `TaskInstanceRepository`.
- `t_collector_execution_logs` is used by `ExecutionLog` and `ReportTaskStatus` logging.
- These tables are current collector-owned tables, not admin-owned leftovers.

Cleanup performed:

- Removed the `INSERT OR IGNORE INTO t_collector_task_rules` default Binance spot K-line rule from `collector.sql`.

Rationale:

- The schema should create module-owned structure only.
- Runtime collector rules should be recreated through the management console or `/api/admin/collectmgr/*` service flow after data reset.
- This aligns with `examples/e2e/README.md`, which states that examples do not directly seed admin/cloudnode/collector/trade SQLite tables.

Updated:

```text
docs/admin-cloudnode-collector-split-completion-matrix-2026-07-03.md
```

- Recorded that collector schema no longer embeds default采集规则 runtime seed and that task rules are rebuilt via service APIs.

No build, gofmt, test, deployment, git, or E2E command was run in this continuation.

## 2026-07-03 continuation audit: cloudnode schema ownership scan

Continued objectives 2, 3, and 5 by reviewing `modules/cloudnode/schema/cloudnode.sql` ownership and runtime seed state.

Checked:

```text
modules/cloudnode/schema/cloudnode.sql
modules/cloudnode/internal/repository
modules/cloudnode/internal/service/cloudnode
docs/云节点管理.md
docs/架构总览.md
docs/admin-cloudnode-collector-split-completion-matrix-2026-07-03.md
modules/cloudnode/README.md
examples/e2e/README.md
```

CloudNode schema tables reviewed:

```text
t_cloud_nodes
t_cloud_accounts
t_cloud_function_packages
t_cloud_async_jobs
t_cloud_job_attempts
t_cloud_invocations
t_cloud_invocation_results
```

Findings:

- `t_cloud_nodes` is used by cloudnode catalog, heartbeat, node management, polling filters, and sync invocation node selection.
- `t_cloud_accounts` is used by cloud account CRUD, COS package upload, and SCF invocation credentials.
- `t_cloud_function_packages` is used by package upload/list/detail/delete and node deployment projection.
- `t_cloud_async_jobs` is used by `AsyncJobRepository` for CloudNode async job_item submit/poll/report.
- `t_cloud_job_attempts` is used by job_item lease attempts and status reporting.
- `t_cloud_invocations` and `t_cloud_invocation_results` are used by sync invocation summary/detail persistence.
- `cloudnode.sql` contains no `INSERT` runtime seed rows.
- No reviewed cloudnode table belongs in admin schema; cloud account data remains owned by the independent `moox-cloudnode` service.

Updated:

```text
docs/云节点管理.md
docs/admin-cloudnode-collector-split-completion-matrix-2026-07-03.md
```

- Completed the CloudNode table ownership list with `t_cloud_job_attempts` and `t_cloud_invocation_results`.
- Strengthened the completion matrix evidence for CloudNode table ownership.

Notes:

- Schema/table/field names still use `job` where the current wire protocol uses `SubmitJobs`, `PollJobs`, `ReportJobStatus`, and `job_id`. Documentation continues to describe the runtime entity as CloudNode `job_item` to avoid confusing it with collector `task_instance`.

No build, gofmt, test, deployment, git, or E2E command was run in this continuation.

## 2026-07-03 continuation cleanup: collectmgr task rule API completion

Continued objectives 3 and 4 after removing the default runtime task-rule seed from `modules/collector/schema/collector.sql`.

Problem found:

- The collect proto and frontend already referenced `CreateTaskRule`, `UpdateTaskRule`, `DisableTaskRule`, and `GetTaskRuleDetail`.
- The independent `moox-collector` service only implemented list/recalculate/status paths, so deleting the schema seed would leave no complete service/API path for rebuilding collector rules after data reset.
- The frontend was submitting task-rule fields as a flat object, while the current proto request shape is `{ rule: TaskRule }`, which could trigger proto JSON unknown-field errors.

Updated:

```text
modules/collector/internal/repository/task_rule_repo.go
modules/collector/internal/service/collectmgr/convert.go
modules/collector/internal/service/collectmgr/service.go
web/src/views/collector/collector-rules/collector-rules.vue
examples/e2e/README.md
docs/admin-cloudnode-collector-split-completion-matrix-2026-07-03.md
```

Changes:

- Added collector rule repository methods for create, get-by-rule-id, update-by-rule-id, and enabled flag updates.
- Implemented `GetTaskRuleDetail`, `CreateTaskRule`, `UpdateTaskRule`, and `DisableTaskRule` in the independent `moox-collector` service.
- Added minimal `GetDataTypeConfigs` and `GetDataTypeConfigWithFields` responses for the current Binance K-line rule form.
- Added PB-to-domain conversion for `TaskRule` and basic validation/defaulting of task-rule fields.
- Updated the frontend collector rule page to submit create/update payloads using the proto wrapper shape `{ rule: ... }`; enabled-toggle updates now submit `{ rule_id, rule }`.
- Documented the `CreateTaskRule` request shape and `RecalculateAllTaskInstances` step in `examples/e2e/README.md`.
- Recorded completion-matrix evidence that collector rules are rebuilt through collectmgr API rather than schema seed data.

Notes:

- Creating a rule persists the rule. Task instances are still generated by `RecalculateAllTaskInstances`, matching the current planner design.
- Proto/generated code was not changed in this continuation because the required RPCs and messages already exist in `modules/collect/proto/collectgen`.

No gofmt, build, frontend build, test, deployment, git, or E2E command was run in this continuation.

## 2026-07-03 continuation cleanup: collector rule form field config alignment

Continued the collectmgr rule-rebuild path cleanup after implementing task-rule CRUD in the independent collector service.

Updated:

```text
modules/collector/internal/service/collectmgr/service.go
web/src/views/collector/collector-rules/collector-rules.vue
```

Changes:

- Aligned the frontend `FieldConfig` interface with proto JSON by using `is_required` instead of the stale `required_flag` field name.
- Completed the minimal K-line field metadata returned by `GetDataTypeConfigWithFields` with the `objects` field, matching the page's current `COLLECT_PARAMS_FIELDS['kline']` behavior.
- Expanded the returned interval options to match the page's K-line interval option set.

Rationale:

- After removing collector schema runtime seed data, users must be able to recreate collector rules through the independent collectmgr API and management page.
- The form metadata and proto JSON shape should not preserve old field names that could make rule creation brittle after a data reset.

No gofmt, build, frontend build, test, deployment, git, or E2E command was run in this continuation.

## 2026-07-03 continuation cleanup: collectmgr task-rule request shape docs

Continued hardening the examples/API rebuild path after implementing collectmgr task-rule CRUD.

Checked current frontend, examples, docs, and CLI references for `CreateTaskRule` / `UpdateTaskRule` request-shape drift.

Findings:

- The active frontend call path in `web/src/views/collector/collector-rules/collector-rules.vue` now submits proto-shaped payloads:
  - create: `{ rule: ... }`
  - update: `{ rule_id: "...", rule: ... }`
- No additional active flat `CreateTaskRule` caller was found outside historical audit records.

Updated:

```text
docs/采集任务管理.md
```

- Documented the required proto JSON wrapper shape for create/update rule requests before admin forwards them to the independent `moox-collector` service.

No gofmt, build, frontend build, test, deployment, git, or E2E command was run in this continuation.

## 2026-07-03 continuation audit: collectmgr proto implementation boundary

Continued hardening the collector rebuild path by comparing `CollectMgr` proto methods with the independent `moox-collector` service implementation and active frontend calls.

Checked:

```text
modules/collect/proto/collect_service.proto
modules/collector/internal/service/collectmgr/service.go
web/src
examples
docs
modules/cli
```

Findings:

- The active management-console/E2E rebuild path calls rule list/create/update, data-type config, task-instance list, and `RecalculateAllTaskInstances`.
- Those required paths are now implemented by `modules/collector/internal/service/collectmgr`.
- Proto still contains manual task-instance CRUD/control RPCs such as `CreateTaskInstance`, `UpdateTaskInstance`, `DeleteTaskInstance`, `StartTaskInstance`, `StopTaskInstance`, and `InvalidateTaskInstance`, but no active frontend, CLI, examples, or current rebuild doc path calls them.
- Current design keeps `task_instance` as planner-generated data: users create task rules, then collector recalculates instances from storage dataset subjects.

Updated:

```text
docs/采集任务管理.md
```

- Documented that `task_instance` is not seed data and not manually maintained through the rebuild path; it is generated or updated by planner after `CreateTaskRule` + `RecalculateAllTaskInstances`.

No gofmt, build, frontend build, test, deployment, git, or E2E command was run in this continuation.

## 2026-07-03 continuation cleanup: remove unused manual task-instance RPCs

Continued objective 5 by removing unused manual task-instance CRUD/control protocol exposure from CollectMgr.

Rationale:

- Current collector rebuild design creates task rules through collectmgr and then generates task instances through planner via `RecalculateAllTaskInstances`.
- Active frontend, CLI, examples, and docs do not call manual task-instance cache/detail/create/update/delete/start/stop/invalidate RPCs.
- Keeping those RPCs exposed made task instances look like manually maintained seed/CRUD data, which conflicts with the examples-based reset/rebuild path.

Updated:

```text
modules/collect/proto/collect_service.proto
modules/collect/proto/collectgen/collect_service.pb.go
modules/collect/proto/collectgen/collect_service.trpc.go
docs/admin-cloudnode-collector-split-completion-matrix-2026-07-03.md
```

Removed proto messages and RPCs for:

```text
GetTaskInstanceListCache
GetTaskInstanceDetail
CreateTaskInstance
UpdateTaskInstance
DeleteTaskInstance
StartTaskInstance
StopTaskInstance
InvalidateTaskInstance
```

Kept current task-instance protocol paths:

```text
GetTaskInstanceList
ReportTaskStatus
```

Code generation run:

```bash
cd modules/collect/proto
make all
```

Result: command completed successfully and regenerated `collectgen`.

No Go build, frontend build, test, deployment, git, or E2E command was run in this continuation.

## 2026-07-03 continuation audit: removed task-instance RPC residue scan

Continued objective 5 after removing unused manual task-instance CRUD/control RPCs from CollectMgr.

Scanned active code and current docs outside the historical audit log for removed RPC names:

```text
GetTaskInstanceListCache
GetTaskInstanceDetail
CreateTaskInstance
UpdateTaskInstance
DeleteTaskInstance
StartTaskInstance
StopTaskInstance
InvalidateTaskInstance
```

Checked paths:

```text
modules
web/src
docs
examples
modules/cli
```

Result:

- No active reference to the removed RPC names remains outside the historical audit log.
- Remaining task-instance wording in current docs is intentional boundary documentation: task instances are planner-generated from task rules and dataset subjects, not manually seeded or maintained through CRUD APIs.

No gofmt, build, frontend build, test, deployment, git, or E2E command was run in this continuation.

## 2026-07-03 continuation cleanup: collector rule frontend type alignment

Continued hardening the collectmgr rule rebuild path by aligning the frontend task-rule type with the current PB/schema fields.

Updated:

```text
web/src/views/collector/collector-rules/collector-rules.vue
```

Changes:

- Added optional `id` to `TaskConfig`.
- Added `biz_type` to `TaskConfig`.

Rationale:

- `TaskRule` PB/schema includes `id` and `biz_type`.
- The rule-update path sends the selected record back to collectmgr as `{ rule_id, rule }`; keeping the frontend type complete reduces the chance of dropping collector-owned fields when rules are recreated after a data reset.

No frontend build, typecheck, gofmt, Go build, test, deployment, git, or E2E command was run in this continuation.

## 2026-07-03 continuation cleanup: gofmt changed Go files

Formatted the Go files touched by the recent admin monitor naming cleanup and collectmgr rule-rebuild implementation.

Formatted:

```text
modules/collector/internal/repository/task_rule_repo.go
modules/collector/internal/service/collectmgr/convert.go
modules/collector/internal/service/collectmgr/service.go
modules/admin/internal/service/monitor/impl.go
modules/admin/internal/service/monitor/scraper.go
```

Command run:

```bash
gofmt -w modules/collector/internal/repository/task_rule_repo.go modules/collector/internal/service/collectmgr/convert.go modules/collector/internal/service/collectmgr/service.go modules/admin/internal/service/monitor/impl.go modules/admin/internal/service/monitor/scraper.go
```

No Go build, frontend build, test, deployment, git, or E2E command was run in this continuation.
### 2026-07-03 CloudNodeMgr protocol exposure audit

Continued the admin/cloudnode/collector split objective by auditing the current CloudNodeMgr protocol surface against implementation and callers.

Evidence:

- `modules/collect/proto/collect_service.proto` and `modules/cloudnode/internal/service/cloudnode/service.go` currently expose and implement the same CloudNodeMgr RPC set.
- `BatchCreateNodes`、`BatchDeleteNodes`、`BatchDeployNodes` are active CloudNode control-plane `batch_change` APIs and return `batch_id`.
- `SubmitJobs`、`PollJobs`、`ReportJobStatus` remain active SCF runtime job_item APIs; these should not be removed as old admin logic.
- `GetCOSAccountInfo` is used by `modules/cli/cmd/tencent_ops_firewall_open.go` through `modules/cli/internal/adminclient/cloudnode.go`, so it is not dead code.
- `UpdateNode` has a frontend API wrapper in `web/src/api/cloud-node.ts`, so it is not currently a proven dead RPC.
- `InvokeSync` has no current frontend call but is part of the agreed CloudNode platform capability for synchronous fan-out/fan-in workloads such as future factor calculation, so it should not be removed in this cleanup pass.

Conclusion:

No CloudNodeMgr RPC was removed in this pass. The audit result is recorded to prevent accidentally deleting platform-owned CloudNode APIs while cleaning old admin-owned cloudfunction/collector code.

### 2026-07-03 Collector SCF runtime package rename

Renamed the collector SCF runtime entry package from:

```text
modules/collector/internal/cloudfunction
```

to:

```text
modules/collector/internal/scf
```

Rationale:

- The old `cloudfunction` package name was easy to confuse with removed admin-owned cloudfunction management APIs.
- The code remains collector-owned SCF runtime logic, not CloudNode platform provider logic.
- Runtime behavior was not changed; `moox-collector-scf` now imports `modules/collector/internal/scf` and still registers the Tencent SCF handler.

### 2026-07-03 Architecture docs interaction cleanup

Cleaned remaining outdated collector interaction wording in `docs/架构总览.md`.

Changed the old description:

```text
Collector -> Admin: heartbeat / get rules
Collector -> Storage: three REST API endpoints
```

to the current split architecture:

```text
Admin Gateway -> moox-collector control plane: /api/admin/collectmgr/*
moox-collector control plane -> moox-cloudnode: submit job_item
SCF runtime -> Admin Gateway: /api/service/cloudnode/* and /api/service/collectmgr/*
SCF runtime -> Storage Access: direct HTTP/tRPC JSON writes
```

Also updated the auth table so HMAC is described as the `/api/service/*` backend/SCF gateway mechanism rather than a collector-to-admin special path.

The audit header was also refreshed to remove stale `AsyncTask`, `t_cloud_node_pools`, and `t_cloud_deployments` current-evidence entries; current forwarding evidence now points to active `t_service_deployments` records instead of static gateway config.

### 2026-07-03 Admin database manager boundary audit

Audited `modules/admin/internal/service/database` to distinguish required admin infrastructure from old one-off database management or migration code.

Findings:

- `database.Manager` only opens the Admin SQLite database, applies embedded `modules/admin/schema/admin.sql`, initializes Badger cache, exposes `GetDB`/`GetCache`, and closes cache resources.
- No external database-management RPC, migration service, or old `data/moox.db` compatibility path is registered from this package.
- The package is still required by auth, space, monitor, sysdeploy, and bootstrap startup code, so it is not dead code.

Cleanup:

- Updated `docs/数据库管理.md` to remove stale `Initialize(&cfg.Database, schemaSQL)` / `adminschema.AllSQL()` examples from the removed schema override era.
- Documented the current behavior: `Initialize(&cfg.Database)` opens `data/admin.db` and automatically applies the embedded Admin schema. Other modules initialize their own databases and schemas outside admin.

### 2026-07-03 Module schema initialization boundary tightening

Tightened CloudNode and Collector database initialization so bootstrap no longer passes arbitrary schema SQL text into storage managers.

Changes:

- `modules/cloudnode/internal/storage.Manager.Initialize` now accepts only `*config.DatabaseConfig` and applies `modules/cloudnode/schema.AllSQL()` internally.
- `modules/collector/internal/control/storage.Manager.Initialize` now accepts only `*config.DatabaseConfig` and applies `modules/collector/schema.AllSQL()` internally.
- `modules/cloudnode/internal/bootstrap` and `modules/collector/internal/control/bootstrap` no longer import schema packages directly or pass `schemaSQL` to Initialize.

Rationale:

- Table definitions remain owned by each module's `schema/` package.
- Startup code can no longer inject arbitrary schema text, reducing the surface that looked like one-off schema override / migration-era code.
- Runtime behavior remains the same: each independent service opens its own SQLite database and applies its embedded module schema.

### 2026-07-03 Release/deploy embedded schema cleanup

Cleaned release and deploy staging scripts after Admin, CloudNode, and Collector schema initialization was tightened to embedded module schemas.

Changes:

- `scripts/release.sh` no longer creates or copies `admin/schema`, `cloudnode/schema`, or `collector/schema` into the release package.
- `scripts/deploy-moox.sh` no longer stages Admin, CloudNode, or Collector schema files for deployment.
- Storage schema packaging is intentionally retained because the storage init path still uses `STORAGE_SCHEMA_FILE=${ROOT}/storage/schema/metadata.sql` for metadata initialization.

Rationale:

- Admin, CloudNode, and Collector no longer need runtime schema files in deployment artifacts; each binary applies its embedded module schema on startup.
- Keeping those schema files in release/deploy packages made the package look dependent on one-off SQL initialization files and weakened the current service-owned schema boundary.

### 2026-07-03 README release package wording cleanup

Updated root `README.md` so release/deploy package descriptions match the current embedded-schema packaging model.

Changes:

- Replaced generic `各模块配置/schema` wording with `各模块配置、Storage schema`.
- Added explicit note that Admin, CloudNode, and Collector SQLite schemas are embedded in their binaries and are applied automatically at startup.
- Documented that the deployment package only keeps `storage/schema/metadata.sql` because Storage metadata initialization still uses that runtime schema file.

Rationale:

- Prevent users from expecting `admin/schema`, `cloudnode/schema`, or `collector/schema` directories in release/deploy artifacts after these schemas moved fully into service binaries.

### 2026-07-03 Runtime database artifact and manual restore audit

Audited repository runtime data artifacts and manual SQLite restore guidance.

Findings:

- No active `.db`, `.sqlite`, `.sqlite3`, WAL, or SHM runtime database files were found in the repository outside ignored/generated areas.
- `examples/` and active scripts do not directly write Admin, CloudNode, or Collector SQLite tables.
- `docs/数据库管理.md` still showed `sqlite3 .backup`, `.dump`, and SQL restore examples for `data/admin.db`, which could conflict with the current delete-and-rebuild contract.

Cleanup:

- Replaced the backup/restore example section with delete-and-rebuild-first guidance.
- Clarified that E2E/demo rebuilding should use module startup, examples metadata import, Admin/CloudNode/Collector APIs, and Storage rebuild flows.
- Clarified that manual database backups are only an operational fallback and should not be committed to the repository or used as the normal E2E rebuild path.

### 2026-07-03 Frontend mock dependency lock cleanup

Audited project-owned test/mock/fixture/coverage files and frontend mock dependencies.

Findings:

- No project-owned `_test.go`, `testdata`, `fixtures`, `mocks`, or coverage directories/files were found outside ignored generated/vendor areas.
- `web/package.json` no longer declares `mockjs` or `vite-plugin-mock`, and active frontend source/config does not import them.
- `web/pnpm-lock.yaml` still retained orphan lock entries for `mockjs`, `vite-plugin-mock`, and the `commander@14.0.0` dependency pulled only by `mockjs`.

Cleanup:

- Removed the orphan `mockjs`, `vite-plugin-mock`, and `commander@14.0.0` package/snapshot entries from `web/pnpm-lock.yaml`.

Rationale:

- The admin frontend now calls real gateway APIs and should not carry stale mock-server dependency locks from old development scaffolding.

### 2026-07-03 Admin schema boundary recheck

Rechecked Admin, CloudNode, and Collector schema SQL boundaries.

Findings:

- `modules/admin/schema/admin.sql` does not define CloudNode, cloud function package, collector task rule, collector task instance, or CloudNode async job_item business tables.
- `modules/admin/schema/service_deployments_seed.sql` contains `moox_cloudnode` and `moox_collector` only as service deployment discovery records for gateway forwarding.
- `modules/cloudnode/schema/cloudnode.sql` and `modules/collector/schema/collector.sql` contain table/index/trigger definitions only; no runtime seed `INSERT` rows were found.

Cleanup:

- Updated the header comment in `modules/admin/schema/admin.sql` from the stale auth-only wording to `MooX Admin 本地基础数据库表设计`, matching the current admin-local schema boundary.

### 2026-07-03 Runtime SQLite gitignore hardening

Audited repository ignore rules for runtime database artifacts.

Findings:

- Root `.gitignore` already ignored root and module runtime/build directories such as `/data/`, `/logs/`, `/var/`, `/release/`, `/modules/*/data/`, and `/modules/*/var/`.
- No active runtime SQLite files were found in the repository outside ignored dependency/release areas.
- SQLite sidecar files can still be created near configured database paths, so explicit file-pattern protection is useful.

Cleanup:

- Added root `.gitignore` entries for `*.db`, `*.db-shm`, `*.db-wal`, `*.sqlite`, and `*.sqlite3`.

Rationale:

- Schema SQL belongs in module `schema/` directories and is embedded or explicitly consumed by service startup.
- Runtime database files should be rebuildable from service startup plus examples/E2E flows and should not be committed.

### 2026-07-03 Admin proto boundary audit

Audited `modules/admin/proto` and generated admin registration references for old admin-owned CloudNode, cloudfunction, package-management, async-task, and collector protocols.

Findings:

- Current admin proto services are limited to `SpaceMgr`, `Auth`, `Dns`, `Ssh`, `Monitor`, `SecretMgr`, and `SysDeploy`.
- No `AsyncTask`, `PackageMgr`, CloudFunction, CollectMgr, or CloudNodeMgr service is defined in `modules/admin/proto`.
- Admin bootstrap registers only admin-local services plus raw SSH handlers; CloudNodeMgr and CollectMgr are reached through gateway forwarding via `t_service_deployments`.

Conclusion:

No admin proto file or generated admin service code was removed in this pass because no old admin-owned cloudnode/cloudfunction/collector protocol remains there.

### 2026-07-03 Admin import boundary hardening

Tightened module boundary checks to prevent Admin from re-accumulating downstream business logic.

Changes:

- `scripts/check-module-boundaries.sh` now treats `modules/admin` more strictly than ordinary business modules: Admin must not import any other `modules/*` package, including generated `proto/*` packages.
- `docs/大仓架构.md` documents this Admin-specific rule: downstream services are accessed through `t_service_deployments` and `/api/admin|service/{service}/{method}` gateway forwarding, not direct imports.

Rationale:

- Ordinary business services may share generated protocol packages when needed, such as `cloudnode` and `collector` sharing `modules/collect/proto`.
- Admin is the management gateway and should remain protocol-agnostic for downstream services to avoid rebuilding admin-owned CloudNode/Collector logic.

### 2026-07-03 Release 包运行态目录边界清理

清理 `scripts/release.sh` 中的 `storage/var/storage` 空目录创建，避免二进制归档包携带或暗示携带运行态数据目录。

保留 `scripts/deploy-moox.sh` 在部署目录中创建 `data`、`logs`、`run` 的逻辑；这些目录属于实际运行部署目录，不属于 release 归档内的源码/配置边界。

Storage 的 `./var/storage` 示例路径仍保留在配置和文档中，实际部署时由部署脚本改写到 `../data/storage`，由运行时或启动脚本创建。

### 2026-07-03 Admin 数据库旧路径复核

继续复核 `moox.db` / `data/moox.db` 与 `admin.db` 命名边界。

结果：

- 未发现活跃 `data/moox.db` / `moox.db` 引用；命中项仅存在于历史审计日志或目标说明语境中。
- Admin 配置、部署脚本、README 和数据库文档均指向 `./data/admin.db` 或部署目录的 `../data/admin.db`。
- `moox_cloudnode.db`、`moox_collector.db`、`moox_trade.db` 是 CloudNode、Collector、Trade 各自模块拥有的独立 SQLite 默认库名，不属于被废弃的 Admin 全局 `data/moox.db`。

### 2026-07-03 云函数 runtime 归属文档修正

修正 `docs/云节点执行平台架构.md` 中早期草案遗留的职责描述。

变更：

- 将“通用云函数 runtime 放到 cloudnode”修正为“SCF 通用运行时放到根级 `packages/cloudruntime`”。
- 从 CloudNode 目录示例中移除 `scf/runtime` 旧规划，避免 CloudNode 重新承载跨业务 runtime。
- 将 Collector 目录示例调整为当前 `cmd/moox-collector-scf`、`internal/scf`、`internal/cloudnodepoller` 结构，并明确采集协议由根级 `modules/collect/proto` 维护。

结论：CloudNode 继续负责云节点控制面和 provider；跨业务 runtime 属于 `packages/cloudruntime`；Collector 只实现采集业务入口、poller 适配和 workload。

### 2026-07-03 Collector CloudNode poller 命名收窄

将 `modules/collector/internal/cloudruntime` 重命名为 `modules/collector/internal/cloudnodepoller`。

原因：

- 该包不是通用 SCF runtime，而是 collector 侧把 CloudNode job_item 转换为采集 `TaskExecuteEvent` 的适配层。
- 根级 `packages/cloudruntime` 才是跨业务复用的通用 runtime。
- 收窄命名可以避免后续误以为通用 runtime 仍归 collector 或 cloudnode 私有。

同时更新 `modules/collector/internal/scf` 的 import 和架构文档中的目录示例。

### 2026-07-03 cloudruntime workspace 配置复核

复核根级共享 runtime 的 Go workspace 纳入情况。

结果：

- `go.work` 已包含 `./packages/cloudruntime`。
- `packages/cloudruntime/go.mod` 使用独立 module path `github.com/mooyang-code/moox/packages/cloudruntime`。
- `modules/collector/go.mod` 已 require 该 module，并通过本地 `replace ../../packages/cloudruntime` 对齐源码开发与 gowork 编译方式。

结论：`packages/cloudruntime` 作为根级共享库已经纳入当前多模块编译布局，无需额外调整。

### 2026-07-03 CloudNode 发布矩阵 schema 表述修正

修正完成度矩阵中 CloudNode 独立发布证据的旧表述。

变更：

- 将“复制 `moox-cloudnode` 二进制、cloudnode config/schema”改为“复制二进制和 cloudnode config”。
- 明确 CloudNode schema 已内嵌，不再随部署包复制。

原因：这与当前 `scripts/deploy-moox.sh` 和“Admin/CloudNode/Collector schema 由各自二进制内嵌”的发布边界保持一致。

### 2026-07-03 Trade schema 初始化文档修正

复核 Trade 模块 schema 初始化描述与当前实现。

结果：

- 当前 `modules/trade/internal/service/database.Manager.Initialize(&cfg.Database)` 会打开模块 SQLite 并读取 `modules/trade/schema.AllSQL()` 应用内嵌 schema。
- `modules/trade/internal/bootstrap` 只加载配置并传入数据库配置，不直接传 `schemaSQL` 文本。
- `modules/trade/DESIGN.md` 中“在 bootstrap 中调用 `schema.AllSQL()` 建表”的旧建议已改为 Manager 内部初始化 schema。

结论：Trade 文档与 Admin/CloudNode/Collector 的模块自主管理 schema 边界保持一致。

### 2026-07-03 旧 collector 项目标识复核

复核 `data-collector`、`data_collector`、`moox-collector` 等采集相关标识。

结果：

- `data-collector` 只出现在 `docs/大仓架构.md` 的历史大仓合并背景中，用于说明旧项目来源，不是运行态标识。
- `moox-collector` 是当前独立采集服务二进制、日志 component、User-Agent、默认函数包名和配置 app_id，符合当前命名。
- `data_collector` 仍是当前 CloudNode/SCF 包类型、采集规则 `biz_type`、前端云函数页面和 CLI 参数中的业务枚举值，不是旧 `data-collector` 项目名的残留。

结论：本轮不重命名 `data_collector`；若后续要改为更窄的 `collector` 或 `market_collector`，需要作为协议兼容/数据重建变更单独处理。

### 2026-07-03 旧云函数/包管理接口残留复核

复核旧云函数、旧包管理和旧异步任务接口残留。

扫描项包括：

- `GetSCFDeployInfo`
- `PackageMgr`
- `AsyncTask` / `CreateAsyncJob`
- `cloud-function-async`
- 旧 `/api/admin/cloudfunction`、`/api/admin/cloud-function`、`/api/service/cloudfunction`
- `GetPackageOptions`、`UpdateNodeFunction`、旧 `GetCloudAccount`、旧 `GetNodeDetail`

结果：

- 活跃源码、前端、脚本、examples 和当前文档中未发现上述旧接口调用残留。
- 命中的 `BuildSCFPackageOptions` 与 CLI `collectorPackageOptions` 是当前 collector SCF 本地打包参数结构，不是旧 Admin `PackageMgr` RPC。

结论：旧云函数/包管理 RPC 未重新出现；当前打包、上传、部署仍通过 collector CLI + cloudnode 独立服务路径。

### 2026-07-03 CLI 与 Collector 打包工具边界修正

发现 `modules/cli/cmd/collector.go` 直接 import `modules/collector/pkg/packager`，这不符合当前大仓边界规则：跨业务模块只能 import 对方 `proto/*`，稳定共享能力应放根级 `packages/*`，而 CLI 如果只是自己使用工具逻辑，应放在 CLI 内部。

处理：

- 将 `modules/collector/pkg/packager/scf.go` 移到 `modules/cli/internal/collectorpackager/scf.go`。
- 更新 `modules/cli/cmd/collector.go`，改为 import CLI 内部 `collectorpackager`。
- Collector 服务模块不再暴露仅供 CLI 使用的 `pkg/packager`。

结论：collector SCF 打包仍由 CLI 命令提供，但不再通过 CLI 直接 import collector 模块 `pkg` 实现，模块边界更清晰。

### 2026-07-03 Admin service deployment seed 内嵌复核

复核删库后 Admin 默认服务部署记录是否能随 schema 重建。

结果：

- `modules/admin/schema/schema.go` 通过 `go:embed` 同时内嵌 `admin.sql` 和 `service_deployments_seed.sql`。
- `schema.AdminSQL()` 返回两段 SQL 的拼接结果。
- `modules/admin/internal/service/database.Manager` 通过 `adminSchemaSQL()` 调用 `adminschema.AdminSQL()`，因此 Admin 启动应用 schema 时会同时应用默认 `t_service_deployments` seed。

结论：删掉 `data/admin.db` 后，Admin 本地基础表和默认服务部署记录具备源码层面的重建路径。

### 2026-07-03 旧全局环境变量残留复核

复核旧单体式配置环境变量残留。

结果：

- 未发现活跃 `DB_PATH`、`MOOX_ENCRYPTION_KEY` 或 `JWT_SECRET` 配置残留。
- Admin 数据库覆盖使用 `MOOX_ADMIN_DB_PATH`，CloudNode 使用 `MOOX_CLOUDNODE_DB_PATH`，Collector 使用 `MOOX_COLLECTOR_DB_PATH`，Trade 使用 `MOOX_TRADE_DB_PATH`。
- Admin 与 Trade 的敏感数据加解密密钥分别使用 `MOOX_ADMIN_ENCRYPTION_KEY` 和 `MOOX_TRADE_ENCRYPTION_KEY`。
- Collector 调用 `/api/service/*` 使用 `MOOX_SERVICE_AUTH_*` 后台服务签名配置，这是当前后台网关加解密/鉴权链路的一部分。

结论：数据库路径和密钥配置已经按模块收敛，没有继续保留旧全局 override 名称。

### 2026-07-03 Collector pkg 剩余包用途复核

迁移 CLI-only packager 后，继续复核 `modules/collector/pkg` 剩余包用途。

结果：

- `modules/collector/pkg/config`、`httpclient`、`logger`、`model`、`storage` 均只被 collector 自己的 `cmd` 或 `internal` 代码引用。
- 未发现其他模块继续 import `modules/collector/pkg/*`。
- `modules/collector/pkg/packager` 是本轮发现的唯一跨模块 CLI-only 暴露面，已移入 `modules/cli/internal/collectorpackager`。

结论：Collector 模块当前没有继续向其他业务模块暴露 `pkg` 实现包。

### 2026-07-03 CLI go.mod collector 依赖清理

迁移 CLI-only packager 后，继续清理 CLI 模块依赖残留。

变更：

- 从 `modules/cli/go.mod` 删除 `github.com/mooyang-code/moox/modules/collector` 的 `require`。
- 从 `modules/cli/go.mod` 删除对应 `replace ../collector`。
- 更新 `modules/cli/README.md`，将“采集打包可选依赖 `modules/collector`”改为“CLI 内部 `internal/collectorpackager` 生成 collector SCF zip，不直接依赖 collector 实现包”。

结论：CLI 不再通过 Go module 依赖 collector 模块；采集打包能力留在 CLI 内部实现，符合当前跨模块依赖边界。

### 2026-07-03 go.mod 跨模块边界检查增强

发现 `scripts/check-module-boundaries.sh` 只检查 Go 源码 import，没有检查 `go.mod` 的跨模块 `require` / `replace`，因此无法阻止“源码已清理但模块依赖仍挂着”的情况。

变更：

- 删除 `modules/cli/go.mod` 中未使用的 `replace github.com/mooyang-code/moox/modules/storage => ../storage`。
- 增强 `scripts/check-module-boundaries.sh`：现在会检查 `modules/*/go.mod` 中的跨模块依赖。
- 非 Admin 业务模块仍只允许依赖其他模块的 `proto/*` module。
- Admin 的 `go.mod` 不允许依赖任何其他业务模块，包括其他模块的 `proto/*`。
- 根级 `packages/*/go.mod` 不允许依赖 `modules/*` 业务模块。

结论：模块边界检查从“源码 import”扩展到“源码 import + go.mod 依赖声明”，能更早发现隐藏的跨模块耦合。

### 2026-07-03 Collector cloudnodepoller 文档残留修正

迁移 `modules/collector/internal/cloudruntime` 到 `modules/collector/internal/cloudnodepoller` 后，继续清理文档中的旧目录名。

变更：

- 更新 `docs/架构总览.md` 中 Collector 目录树，将 `cloudruntime/` 改为 `cloudnodepoller/`。
- 更新 `modules/collector/README.md` 中 Collector 目录说明，将该包描述为 CloudNode job_item 到采集任务的 poll/execute 适配。

结论：文档中的 Collector SCF 适配层命名与当前代码目录保持一致，避免和根级 `packages/cloudruntime` 混淆。

### 2026-07-03 Admin gateway 转发特殊逻辑复核

复核 `modules/admin/internal/gateway` 中是否存在针对 cloudnode、collectmgr、storage 或 trade 的 endpoint 级硬编码转发逻辑。

结果：

- `forwardHTTP` 只接收 URL 中的 `{service}/{method}`，通过 `ServiceDetailResolver` 查找目标 address/path，再组装 `/{gateway_path}/{method}` 透传。
- `gateway.yaml` 不维护下游服务地址，配置中也明确 `/api/admin/*` 与 `/api/service/*` 的转发目标来自 `t_service_deployments`。
- `sysdeploy.ResolveGatewayServiceDetail` 仍保留服务别名解析：`collectmgr` / `collector` 指向 `moox_collector`，`cloudnode` 指向 `moox_cloudnode`，`auth` 指向 `admin_auth`。

结论：

- Admin gateway 本身没有 endpoint 级特殊转发分支。
- 当前保留的别名映射属于服务名到独立部署记录的 resolver 逻辑，用于让外部 URL 段保持 `/api/admin/collectmgr/*`、`/api/admin/cloudnode/*`，同时让部署记录按独立服务 `moox_collector` / `moox_cloudnode` 管理。
- 如果后续希望彻底去掉硬编码别名，应新增数据化 alias 字段或调整 `t_service_deployments.c_service_name` 命名；这属于服务发现模型变更，不在本轮直接修改。

### 2026-07-03 Web dist 构建产物边界复核

复核前端构建产物与 web-host 嵌入资源边界。

结果：

- `web-host` 通过 `web-host/internal/statik/statik.go` 嵌入静态资源。
- `scripts/deploy-moox.sh` 在包含 web-host 且未显式 `--reuse-web-assets` 时，会执行 `npm run build:prod` 生成 `web/dist`，再用 `statik -src=../web/dist -dest=./internal` 刷新嵌入资源，最后构建 `moox-web-host`。
- `web/dist` 是前端构建产物，不应作为源码维护；本轮在 `.gitignore` 中显式加入 `/web/dist/`。
- 当前未手工删除 `web/dist` 或 `web-host/internal/statik/statik.go`，避免在未授权构建的情况下制造 web-host 静态资源不一致。

结论：前端死代码应通过源码删除后重新构建刷新，不手工编辑生成物；仓库忽略规则已补齐 `web/dist` 构建产物边界。

### 2026-07-03 测试/迁移/fixture 残留复核

继续复核项目自有测试代码、一次性迁移脚本和 mock/fixture 残留。

结果：

- 未发现项目自有 `*_test.go` 文件。
- 未发现活跃数据库 migration/migrate、fixture、coverage 或前端 mock 服务文件。
- `modules/*/proto/Makefile` 中的 `--mock=false` 是代码生成参数，表示不生成 mock，不属于 mock 残留。
- `modules/trade` 中的 `max_backups` 是日志滚动保留数量，不是数据库备份/恢复路径。

结论：本轮没有可删除的测试或一次性迁移代码；当前剩余命中均为合法配置或历史审计语境。

### 2026-07-03 Admin 转发边界复核

复核 `modules/admin`、前端 API、发布脚本和架构文档中的 cloudnode / collector 相关命中。

结果：

- `modules/admin` 中的 cloudnode / collector 命中只存在于服务部署 seed、`sysdeploy` 默认服务记录、README/Makefile 转发说明和 bootstrap 网关注释中。
- 未发现 Admin 内部重新实现 CloudNodeMgr、CollectMgr、PackageMgr、AsyncTask 或 cloudfunction 业务逻辑。
- 前端 cloud account、cloud node、function package、collector rule、task instance 调用通过 `callControl("cloudnode" | "collectmgr", ...)` 走 Admin Gateway 转发到独立服务。

结论：Admin 继续保持管理入口和网关职责；CloudNode 与 Collector 业务实现仍归独立模块。
### 2026-07-03 Admin service deployment seed 去 SQL 化

发现 Admin 默认服务部署记录存在两份来源：

- `modules/admin/schema/service_deployments_seed.sql`
- `modules/admin/internal/service/sysdeploy.DefaultDeployments()`

其中 `SysDeploy.SeedDefaults` 会在 Admin 启动创建 SysDeploy 服务时补齐缺失记录，并且只插入不存在的 service_name，不覆盖用户修改；这比把运行默认数据放进 schema SQL 更符合“schema 目录只放表定义”的边界。

变更：

- 删除 `modules/admin/schema/service_deployments_seed.sql`。
- `modules/admin/schema/schema.go` 只内嵌 `admin.sql` 表结构。
- 保留 `SysDeploy.SeedDefaults` 作为默认服务部署记录的唯一源码入口。
- 更新完成度矩阵：默认 `t_service_deployments` 记录随 SysDeploy 服务启动补齐，而不是随 schema SQL 写入。

结论：Admin schema 目录不再混入运行默认数据，默认服务部署记录仍可在删库后由 Admin 启动流程重建。

### 2026-07-03 模块 schema 数据 seed 复核

复核 `modules/*/schema/*.sql` 是否仍包含运行数据 seed。

结果：

- 未发现模块 schema SQL 中的运行数据 `INSERT` seed。
- 命中的 `UPDATE` 均位于 mtime 自动刷新触发器或外键动作语境，不是数据迁移或默认数据写入。
- Admin 默认服务部署记录已从 SQL seed 转移为 SysDeploy 启动补缺省记录。

结论：当前模块 schema 目录只保留表结构、索引、触发器和约束，不再混入运行态默认数据。

### 2026-07-03 examples/e2e 服务部署信息来源修正

同步删库重建文档中的默认服务部署信息来源。

变更：

- 将 `examples/e2e/README.md` 中“admin 默认 seed + 管理台调整”改为“SysDeploy 启动补齐默认部署记录，再通过管理台调整”。

原因：Admin schema 已不再内嵌 `service_deployments_seed.sql`，默认部署记录的源码入口是 `SysDeploy.SeedDefaults`。
### 2026-07-03 Collector 旧本地任务缓存语义清理

复核旧 collector 本地任务缓存、心跳下发任务和 `ExecuteDueTasks` 语义残留。

变更：

- 将 `modules/collector/internal/scf/handler.go` 中的 `executeDueTasksAfterHeartbeat` 重命名为 `pollJobItemsAfterHeartbeat`。
- 将 keepalive 后的日志从“任务执行调度”改为 “CloudNode job_item 拉取/执行”。
- 更新 `skills/debug/references/scf-e2e-debug.md`，移除 “collector local task cache” 和 create-node job 旧说法，改为 CloudNode `batch_change` / `job_item` 语义。
- 更新 `docs/架构总览.md`，将 collector `heartbeat/` 说明改为心跳与服务部署探测，将 `pkg/` 说明中的“打包工具”移除。

结论：SCF keepalive 后执行路径的命名与当前 CloudNode job_item 租约模型一致，不再暗示旧本地任务缓存或到期任务调度器。

### 2026-07-03 Admin schema 旧业务表残留复核

复核 `modules/admin/schema`、`modules/admin/internal/service` 和 `modules/admin/proto` 中是否仍残留 cloudnode、collector、trade、storage 的业务表结构。

结果：

- Admin schema 未发现 `t_cloud_*`、collector task rule/instance、trade order/account、storage dataset/subject/view 等业务表结构。
- `admin.sql` 中的 `c_collect_time` 属于 `t_host_monitor_history` 资源监控历史表，不是 collector 采集任务表。
- `sysdeploy` 中的 storage、collector、trade 命中是服务部署记录和 storage 拓扑变更提醒，不是下游业务实现。

结论：Admin schema 仍只包含 admin 本地基础表；下游业务表结构归各独立模块。

### 2026-07-03 迁移/回填/备份关键词复扫

复扫 active 源码和当前文档中的 `migration`、`migrate`、`schema_migrations`、`backfill`、`backup`、`restore`、`sqlite3` 等关键词。

结果：

- 未发现活跃数据库迁移脚本、schema migration 表、SQLite `.backup/.dump/restore` 操作说明或一次性回填脚本。
- Storage 文档中的“历史补仓/补偿”指 View rebuild 控制面能力，不是数据库迁移。
- Trade 中的“成交回填”是交易业务状态推进，不是一次性数据迁移。
- `max_backups` 是日志滚动配置，不是数据库备份路径。

结论：本轮没有发现需要删除的一次性迁移/备份恢复代码残留。
### 2026-07-03 孤儿测试库 go.sum 记录清理

复核 Go 测试库依赖残留。

结果：

- `github.com/frankban/quicktest` 只出现在 `modules/admin`、`modules/cloudnode`、`modules/collector` 的 `go.sum` 中。
- 未发现对应 `go.mod` require。
- 未发现源码 import 或使用。

变更：

- 删除上述三个模块 `go.sum` 中的 `quicktest` checksum 记录。

结论：删除项目自有测试代码后，残留的孤儿测试库 checksum 也已清理。

### 2026-07-03 Collector 孤儿 logger 包清理

复核 `modules/collector/pkg/logger` 的使用情况。

结果：

- 未发现 collector 或其他模块 import `modules/collector/pkg/logger`。
- 搜索命中均来自 `logger.go` 文件内部方法调用。
- 该包仍带旧 `DATA-COLLECTOR` 日志前缀，与当前 `moox-collector` 运行标识收敛方向不一致。

变更：

- 删除 `modules/collector/pkg/logger/logger.go`。
- 更新完成度矩阵，记录该孤儿包已经清理。

结论：Collector 不再保留未使用的旧 logger 包；当前日志链路继续使用各运行路径已有的 `log` / `slog` / `trpc log` 实现。

### 2026-07-03 Collector 公共 pkg 目录收敛到 internal

继续复核 `modules/collector/pkg` 中剩余实现包的模块边界。

结果：

- `pkg/config`、`pkg/model`、`pkg/httpclient`、`pkg/storage` 均只被 collector 自身 `cmd` 或 `internal` 代码引用。
- 未发现其他业务模块或根级 package import collector 的这些实现包。
- 这些包不是跨模块稳定 API，继续放在 `pkg` 会误导外部模块直接依赖 collector 实现细节。

变更：

- `modules/collector/pkg/config` 迁入 `modules/collector/internal/config`。
- `modules/collector/pkg/model` 迁入 `modules/collector/internal/model`。
- `modules/collector/pkg/httpclient` 迁入 `modules/collector/internal/httpclient`。
- `modules/collector/pkg/storage` 合并入已有 `modules/collector/internal/storageclient`。
- 删除空的 `modules/collector/pkg` 目录。
- 更新 collector README、架构总览和完成度矩阵，说明当前实现包均在 `internal`。

结论：Collector 模块不再暴露公共 `pkg` 实现包；跨模块复用仍应走协议、网关或根级 `packages/*`。

### 2026-07-03 Collector 旧空 cloudruntime 目录清理

迁移 CloudNode job_item runtime 后，复核 collector 内部旧目录。

结果：

- `modules/collector/internal/cloudruntime` 已为空目录。
- 实际跨业务 runtime 位于根级 `packages/cloudruntime`。
- Collector 私有适配层位于 `modules/collector/internal/cloudnodepoller`。

变更：

- 删除空目录 `modules/collector/internal/cloudruntime`。
- 更新完成度矩阵，记录该旧空目录已清理。

结论：Collector 目录结构与当前 runtime 分层一致，不再保留旧命名空壳。

### 2026-07-03 CloudNode 异步执行队列 job_item 命名收敛

根据云节点命名讨论，批量节点管理使用 `batch_change/batch_id`，云函数异步执行队列使用更具体的 `job_item/job_item_id`，不继续使用宽泛的 `job/job_id`。

变更：

- 更新 `modules/collect/proto/collect_service.proto`：
  - `CloudJob` -> `CloudJobItem`
  - `SubmitJobs` -> `SubmitJobItems`
  - `PollJobs` -> `PollJobItems`
  - `ReportJobStatus` -> `ReportJobItemStatus`
  - `job_id` -> `job_item_id`
  - `jobs` -> `job_items`
- 通过 `modules/collect/proto/Makefile` 重新生成 `modules/collect/proto/collectgen`。
- 更新 `modules/cloudnode/internal/service/cloudnode` 和 repository：
  - `AsyncJobRepository` -> `JobItemRepository`
  - `AsyncJob` -> `JobItem`
  - `JobAttempt` -> `JobItemAttempt`
- 更新 `modules/cloudnode/schema/cloudnode.sql`：
  - `t_cloud_async_jobs` -> `t_cloud_job_items`
  - `t_cloud_job_attempts` -> `t_cloud_job_item_attempts`
  - `c_job_id` -> `c_job_item_id`
- 更新 collector 提交端和运行端：
  - `SubmitCollectorJobs` -> `SubmitCollectorJobItems`
  - task instance / execution log 字段从 `cloud_job_id` 改为 `cloud_job_item_id`
  - `cloudnodepoller.PollAndExecuteJobs` -> `PollAndExecuteJobItems`
- 更新 `packages/cloudruntime`：
  - runtime lease 结构和 JSON 字段使用 `job_item_id` / `job_items`
  - 轮询和上报方法改为 `PollJobItems` / `ReportJobItemStatus`
- 更新云节点架构文档、cloudruntime README 和完成度矩阵。

结论：CloudNode 异步执行队列的协议、表结构、运行时和 collector 集成路径已统一为 `job_item` 语义。

### 2026-07-03 CloudNode job_item 文档与调试 skill 同步

同步上一条 CloudNode 异步执行队列重命名后的面向用户文档和调试指引。

变更：

- 更新 `docs/云节点管理.md`：
  - CloudNode 表归属改为 `t_cloud_job_items` / `t_cloud_job_item_attempts`。
  - 后台接口改为 `SubmitJobItems`、`PollJobItems`、`ReportJobItemStatus`。
- 更新 `docs/采集任务管理.md`：
  - 采集任务提交流程改为 `SubmitJobItems`。
  - 后台/SCF 路径改为 `PollJobItems` / `ReportJobItemStatus`。
- 更新 `examples/e2e/README.md`：
  - 删库重建链路和最小 E2E 流程改为新的 job_item RPC。
  - 删除“协议名暂保留 SubmitJobs/PollJobs/ReportJobStatus 和 job_id wire 字段”的过时说明。
- 更新 `skills/debug/SKILL.md` 和 `skills/debug/references/scf-e2e-debug.md`：
  - 调试检查项和日志过滤关键字改为新 RPC/类型名。

结果：

- 除历史审计/计划记录外，当前活跃源码和面向用户/运维文档不再暴露旧 `SubmitJobs`、`PollJobs`、`ReportJobStatus`、`CloudJob`、`job_id` 命名。
- `modules/storage/internal/services/access.waitForAsyncJobs` 是 Storage Access 内部异步写队列命名，和 CloudNode job_item 无关，本轮保留。

### 2026-07-03 CloudNode 旧私有 SCF runtime 空目录清理

复核 `modules/cloudnode` 当前目录结构时发现旧空目录：

```text
modules/cloudnode/scf/runtime
```

该目录已不承载任何代码。当前分层是：

- 通用 CloudNode SCF runtime 位于根级 `packages/cloudruntime`。
- collector 业务 SCF 入口位于 `modules/collector/internal/scf`。
- cloudnode 只负责云节点控制面、云厂商 provider、job_item 队列和同步 invocation。

变更：

- 删除空目录 `modules/cloudnode/scf/runtime` 及其父空目录 `modules/cloudnode/scf`。
- 更新 `docs/云节点执行平台架构.md` 中的 cloudnode 目录树，移除旧私有 proto/runtime 描述，改为当前 `collect/proto` 共享协议和 `job_item_repo.go`。
- 更新完成度矩阵，记录 cloudnode 旧私有 SCF runtime 空壳已清理。

结论：CloudNode 目录结构与当前“控制面 + provider，不私有承载 runtime”的边界一致。

### 2026-07-03 Admin/CloudNode/Collector 边界静态复核

继续复核拆分后的活跃路径，重点检查旧数据库名、admin 旧业务表/RPC、CloudNode job_item 旧命名和跨模块依赖表述。

结果：

- 活跃源码、脚本、examples 和当前用户文档中未发现 `data/moox.db` 或 `moox.db` 引用。
- `modules/admin/schema`、`modules/admin/internal/service`、`modules/admin/proto` 未发现云节点、云函数代码包、采集规则或采集任务实例业务表/RPC 实现。
- admin 命中的 `collectmgr` / `cloudnode` 仅位于 `sysdeploy` 默认服务部署记录、resolver 别名和网关注释，属于转发与服务发现边界。
- 活跃路径中未发现旧 CloudNode 异步执行队列命名 `SubmitJobs`、`PollJobs`、`ReportJobStatus`、`CloudJob`、`job_id`；Storage Access 内部 `waitForAsyncJobs` 是 storage 自身异步写队列，不属于 CloudNode job_item。
- 前端仍有 `collector/cloud-function` 页面路径和组件名，但未发现旧 `/api/admin/cloudfunction` 网关路径；当前是云函数节点管理 UI 命名，不是 admin 后端旧逻辑回流。

变更：

- 更新 `docs/云节点执行平台架构.md` 中的依赖边界说明：`collector` / `factor` 通过 CloudNodeMgr 协议或 `/api/service/cloudnode/*` 使用 `cloudnode`，不能直接 import `modules/cloudnode/internal/...`。

结论：当前静态证据继续支持 admin 只做网关/基础服务、CloudNode/Collector 独立承载业务能力的拆分边界；运行态完成仍需授权后构建、部署、删库重建和 E2E 验证。

### 2026-07-03 发布包 schema 与运行数据路径复核

复核 `scripts/release.sh`、`scripts/deploy-moox.sh` 和模块 README 中的 schema/运行数据路径说明。

结果：

- `scripts/release.sh` 只复制 `modules/storage/schema` 到发布包，不复制 admin/cloudnode/collector schema。
- `scripts/deploy-moox.sh` 只 staging `storage/schema/metadata.sql`，不 staging admin/cloudnode/collector schema。
- Admin、CloudNode、Collector schema 继续由各自二进制内嵌并在启动时自动应用。
- `scripts/deploy-moox.sh --reset-data` 会删除部署目录 `data`，符合“删运行数据后从 examples + 服务流程重建”的目标。

变更：

- 更新 `modules/cloudnode/README.md`，说明本地默认 SQLite 路径是 `./data/moox_cloudnode.db`，部署时会改写为 `../data/cloudnode/moox_cloudnode.db`。
- 更新 `modules/collector/README.md`，说明本地默认 SQLite 路径是 `./data/moox_collector.db`，部署时会改写为 `../data/collector/moox_collector.db`。

结论：发布/部署包仍只携带 Storage metadata 初始化所需 schema；CloudNode/Collector README 与部署脚本、E2E 删库重建文档的路径口径已对齐。

### 2026-07-03 前端云节点管理 API 路径复核

复核管理台云节点、云账户和云函数代码包页面的 API 调用路径。

结果：

- `web/src/api/cloud-node.ts` 使用 `callControl('cloudnode', ...)` 调用 `GetNodeList`、`BatchCreateNodes`、`BatchDeployNodes`、`BatchDeleteNodes` 等 CloudNodeMgr 方法。
- `web/src/api/cloud-account.ts` 使用 `callControl('cloudnode', ...)` 调用云账户 CRUD。
- `web/src/api/function-package.ts` 使用 `callControl('cloudnode', ...)` 调用 `UploadPackage`、`GetPackageList`、`GetPackageDetail`、`DeletePackage`、`GetPackageDownloadURL`。
- `callControl` 底层请求 `/api/admin/{service}/{method}`，所以上述请求均进入 `/api/admin/cloudnode/{Method}`。
- 未发现旧 `/api/admin/cloudfunction`、`/api/admin/cloud-function` 或 `/api/admin/scf` 路径。

说明：

- 前端仍存在 `collector/cloud-function` 页面目录和 `CloudFunction` UI 类型名；当前它们是页面/展示命名，不是旧 admin 后端接口路径或业务实现回流。本轮不做大规模 UI 目录重命名。

结论：管理台云节点相关请求已经通过 admin 网关转发到独立 `moox-cloudnode`，未发现旧 admin 内置云函数接口路径残留。

### 2026-07-03 CLI CloudNode/Collector 控制面调用复核

复核 CLI 中 collector SCF 包发布、节点批量变更和腾讯云运维辅助命令的控制面调用。

结果：

- `modules/cli/internal/adminclient/cloudnode.go` 通过 `/api/admin/cloudnode/*` 调用 `UploadPackage`、`BatchCreateNodes`、`BatchDeployNodes`、`ListCloudAccounts`、`GetCOSAccountInfo`。
- `adminclient.Client.postJSON` 在配置 `ServiceAuth` 时会把 `/api/admin/{service}/{method}` 改写为 `/api/service/{service}/{method}`，并附加 HMAC `Auth` 头。
- CLI batch 响应只解析 `batch_id`、`processed_count`、`message`，未发现 `operation_id`、`submission_id`、`job_id`、`total_task_cnt` 等旧字段。
- 未发现旧 `/api/admin/cloudfunction`、`PackageMgr` 或旧 CloudNode job RPC 路径。

变更：

- 将 CLI helper `newCollectorAdminClient` 重命名为 `newControlClient`。
- 将 `adminclient.Client` 注释从 collector function publishing 专用语义改为通用 MooX control HTTP API client。

结论：CLI 管理与后台辅助调用继续走 admin/service 网关和独立 `moox-cloudnode`，不再暴露旧 batch operation 或 admin 内置云函数专名。

### 2026-07-03 Collector task_instance 暴露 CloudNode job_item ID

复核 `job_item` 命名收敛后，发现 collector 数据库已保存 `c_cloud_job_item_id`，但 CollectMgr `TaskInstance` proto 和管理台任务实例页面未返回/展示该字段。

变更：

- 更新 `modules/collect/proto/collect_service.proto`：
  - `TaskInstance` 新增 `cloud_job_item_id = 18`。
- 通过 `modules/collect/proto/Makefile` 重新生成 `modules/collect/proto/collectgen`。
- 更新 `modules/collector/internal/service/collectmgr/convert.go`，将 `domain.TaskInstance.CloudJobItemID` 返回到 proto。
- 更新 `web/src/views/collector/task-instances/task-instances.vue`：
  - `TaskInstance` 类型新增 `CloudJobItemID`。
  - normalize 兼容 `CloudJobItemID` 和 `cloud_job_item_id`。
  - 列表和详情增加 `JobItem ID` 展示列。

结论：管理台任务实例现在可以直接关联 CloudNode job_item，方便排查 planner 生成、CloudNode 下发、SCF 执行和任务状态回写之间的链路。
