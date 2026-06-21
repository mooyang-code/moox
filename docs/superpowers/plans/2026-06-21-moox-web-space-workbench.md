# MooX Web Space Workbench Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Rework the MooX management console so Space becomes the global business context, Storage UI matches the new `modules/storage` architecture, and existing non-storage modules remain available under the selected Space.

**Architecture:** The browser keeps a single global `selectedSpaceId` in Pinia and exposes it through the top header selector, similar to the old project selector. Control owns platform-level Space, admin schema, users, permissions, collectors, cloud functions, SSH, monitoring, strategy, and trading-console state; Storage consumes `space_id` and owns only storage metadata. Control APIs and Storage APIs are reached through a small web-host gateway that rewrites short admin paths to the existing Control gateway or to storage tRPC HTTP paths. The left menu is organized by business domain, not by Space nesting; every domain page reads the current Space from the global store.

**Tech Stack:** Vue 3, TypeScript, Pinia, Vue Router, Arco Design Vue, Vite, Axios, Go `net/http` reverse proxy, Control HTTP gateway, tRPC HTTP services, moox-storage Metadata/Access/View APIs.

---

## Confirmed Decisions

- MooX Web is the whole management console, not a storage-only admin page.
- The project is not online yet; old management-console schemas, route names, API wrappers, and UI concepts may be rebuilt directly with no compatibility layer and no data migration path.
- Do not introduce `migrations/` for this stage. Maintain only the latest full SQL files because there is no production data to upgrade.
- Platform-level Space belongs to Control/Admin, not Storage. Storage may keep a Space metadata concept for validation and query scoping, but the top-header Space selector and cross-domain authorization come from Control/Admin.
- Unified final schema files are:
  - `/Users/mooyang/Documents/go/src/github.com/mooyang-code/moox/modules/control/schema/admin.sql`
  - `/Users/mooyang/Documents/go/src/github.com/mooyang-code/moox/modules/storage/schema/metadata.sql`
- Remove `/Users/mooyang/Documents/go/src/github.com/mooyang-code/moox/modules/storage/schema/admin_console.sql` from Storage ownership.
- Rename `/Users/mooyang/Documents/go/src/github.com/mooyang-code/moox/modules/storage/schema/storage_metadata.sql` to `/Users/mooyang/Documents/go/src/github.com/mooyang-code/moox/modules/storage/schema/metadata.sql`.
- Container, SSH, cloud function, strategy, and trading-account modules stay in the console and will be gradually aligned to Space.
- Space is the largest user-facing business isolation concept, such as `A股交易空间`, `美股交易空间`, `Crypto 交易空间`, or `生产交易空间`.
- Space selection belongs in the top header, not as a deep left-menu directory.
- Left menu shows functions inside the current Space and avoids `/space/:spaceId/...` depth unless a page truly needs shareable explicit URLs.
- Frontend may keep a hardcoded `app_key` during this stage, but it must live in one storage auth module, not scattered through API files.
- Users must not configure or see `Device`; it is a backend/bootstrap/ops resource. The console exposes `PrimaryStoreNode`, `PrimaryStoreRoute`, and `ArchiveFile`, but not `CreateDevice`, `UpdateDevice`, or a storage-device page.
- Storage data reads and writes are exposed as two user concepts:
  - TimeSeries: fixed `subject_id + freq`, versioned by `data_time`.
  - Record: `record_id`, versioned by `version`, with server-generated UTC version when omitted.
- View queries are exposed as two user concepts:
  - TimeSeries View query: `QueryTimeSeriesRows`, backed by DuckDB view results.
  - Record View search: `SearchRecordRows`, backed by Bleve view indexes.
- Data import must not auto-bind `DatasetSubject`; binding is an explicit metadata operation.

## Target Menu Shape

Top header:

```text
当前空间: [A股交易空间 v]    创建空间 / 空间设置入口
```

Left menu:

```text
首页

数据资产
  ├── 数据来源
  ├── 数据对象
  ├── 数据集
  ├── 字段管理
  ├── 因子管理
  ├── 查询视图
  ├── 数据概览
  ├── 数据列表
  └── 数据同步

计算与采集
  ├── 云函数
  ├── 功能包
  ├── 采集规则
  └── 任务实例

策略管理
  └── 策略列表

交易管理
  ├── 账户总览
  ├── 持仓详情
  └── 交易明细

资源与运维
  ├── 资源监控
  ├── 服务状态
  ├── 主机管理
  ├── SSH 终端
  ├── 会话管理
  └── 存储配置
       ├── 主存节点
       ├── 主存路由
       └── 归档文件

系统设置
  ├── 空间管理
  └── 用户权限
```

## Routing Model

Preferred flat routes:

```text
/home
/data/sources
/data/subjects
/data/datasets
/data/fields
/data/factors
/data/views
/data/overview
/data/list
/data/sync
/collector/functions
/collector/packages
/collector/rules
/collector/tasks
/strategy/list
/trading/accounts
/trading/positions
/trading/orders
/ops/resource-monitor
/ops/service-status
/ops/ssh-hosts
/ops/ssh-terminal
/ops/ssh-sessions
/ops/storage/nodes
/ops/storage/routes
/ops/storage/archive
/settings/spaces
/settings/permissions
```

The selected `space_id` comes from `spaceStore.selectedSpaceId`. The Space list and membership metadata are loaded from Control/Admin APIs, not from Storage MetadataService. API wrappers that call Space-scoped storage endpoints must inject this value into request bodies.

## Current Code Pivots

Old concepts to replace in Storage-related code:

| Old | New |
| --- | --- |
| `project`, `projectId`, `proj_id` | `space`, `spaceId`, `space_id` |
| `CreateDataSet`, `DataSet` | `CreateDataset`, `Dataset` |
| `object_id`, object route | `subject_id` or `record_id`, `PrimaryStoreRoute` |
| `StorageNode` | `PrimaryStoreNode` |
| `StorageDevice` UI | hidden backend `Device`, no user page |
| `FieldRoute` | deleted |
| `field_format_type` | `FieldValueType` |
| field directly binding dataset | `DatasetColumn` |
| `/gateway/*` | `/api/control/{service}/{method}` for Control/Admin and `/api/storage/{metadata|access|view}/{Method}` for Storage |
| `modules/storage/schema/admin_console.sql` | `modules/control/schema/admin.sql` |
| `modules/storage/schema/storage_metadata.sql` | `modules/storage/schema/metadata.sql` |

Schema policy:

- Control/Admin tables are maintained in `modules/control/schema/admin.sql`.
- Storage metadata tables are maintained in `modules/storage/schema/metadata.sql`.
- No migration files are required before the first production release.
- Do not use GORM `AutoMigrate` as the authoritative schema definition for management-console tables. It may be removed or kept only as a developer convenience after the SQL schema is applied and tested.

Non-storage modules keep their pages. Their routes move under the new menu, and their API calls receive Space context only after the corresponding backend accepts `space_id`.

## File Responsibility Map

Schema ownership:

- Create or rewrite: `/Users/mooyang/Documents/go/src/github.com/mooyang-code/moox/modules/control/schema/admin.sql`: full latest Control/Admin schema for users, tokens, platform Spaces, Space members, cloud accounts, cloud nodes, function packages, collector rules, async jobs, SSH hosts/sessions, host monitoring, and future strategy/trading admin state.
- Rename and rewrite: `/Users/mooyang/Documents/go/src/github.com/mooyang-code/moox/modules/storage/schema/storage_metadata.sql` -> `/Users/mooyang/Documents/go/src/github.com/mooyang-code/moox/modules/storage/schema/metadata.sql`: full latest Storage metadata schema only.
- Delete: `/Users/mooyang/Documents/go/src/github.com/mooyang-code/moox/modules/storage/schema/admin_console.sql`: admin-console tables do not belong to Storage.
- Modify: `/Users/mooyang/Documents/go/src/github.com/mooyang-code/moox/modules/control/internal/service/database/manager.go`: apply `admin.sql` at startup or through an explicit bootstrap command.
- Modify: `/Users/mooyang/Documents/go/src/github.com/mooyang-code/moox/modules/storage/internal/infra/metadata/sqlite/store.go`: default schema path uses `schema/metadata.sql`.
- Create: `/Users/mooyang/Documents/go/src/github.com/mooyang-code/moox/modules/control/tests/schema/admin_schema_test.go`: schema assertions for Control/Admin tables and `c_space_id` indexes.
- Rename or update: `/Users/mooyang/Documents/go/src/github.com/mooyang-code/moox/modules/storage/tests/schema/storage_metadata_schema_test.go` -> `/Users/mooyang/Documents/go/src/github.com/mooyang-code/moox/modules/storage/tests/schema/metadata_schema_test.go`: schema assertions target `schema/metadata.sql`.

Control/Admin backend API:

- Create: `/Users/mooyang/Documents/go/src/github.com/mooyang-code/moox/modules/control/internal/service/space/model.go`: GORM models for `t_spaces` and `t_space_members`.
- Create: `/Users/mooyang/Documents/go/src/github.com/mooyang-code/moox/modules/control/internal/service/space/dao.go`: explicit CRUD/query methods over `admin.sql` tables.
- Create: `/Users/mooyang/Documents/go/src/github.com/mooyang-code/moox/modules/control/internal/service/space/service.go`: validation, paging, and service-level methods.
- Create: `/Users/mooyang/Documents/go/src/github.com/mooyang-code/moox/modules/control/internal/service/space/gateway.go`: Control gateway handler with service id `space`.
- Create: `/Users/mooyang/Documents/go/src/github.com/mooyang-code/moox/modules/control/internal/service/space/service_test.go`: Space service tests against `admin.sql`.
- Modify: `/Users/mooyang/Documents/go/src/github.com/mooyang-code/moox/modules/control/internal/bootstrap/services.go`: construct `SpaceService`.
- Modify: `/Users/mooyang/Documents/go/src/github.com/mooyang-code/moox/modules/control/internal/bootstrap/trpc.go`: register Space gateway after gateway initialization.

Backend web host:

- `/Users/mooyang/Documents/go/src/github.com/mooyang-code/moox/web-host/main.go`: static file server plus admin gateway router.
- `/Users/mooyang/Documents/go/src/github.com/mooyang-code/moox/web-host/main_test.go`: proxy rewrite and static fallback tests.
- `/Users/mooyang/Documents/go/src/github.com/mooyang-code/moox/web-host/README.md`: gateway environment variables and local run examples.

Frontend API foundation:

- `/Users/mooyang/Documents/go/src/github.com/mooyang-code/moox/web/src/api/control/http.ts`: Control/Admin Axios client and short admin API path helper.
- `/Users/mooyang/Documents/go/src/github.com/mooyang-code/moox/web/src/api/control/spaces.ts`: top-header Space list/create/update APIs.
- `/Users/mooyang/Documents/go/src/github.com/mooyang-code/moox/web/src/api/control/types.ts`: Control/Admin TypeScript types, including `Space` and `SpaceMember`.
- `/Users/mooyang/Documents/go/src/github.com/mooyang-code/moox/web/src/api/storage/auth.ts`: centralized development auth info.
- `/Users/mooyang/Documents/go/src/github.com/mooyang-code/moox/web/src/api/storage/http.ts`: Axios client and tRPC path helpers.
- `/Users/mooyang/Documents/go/src/github.com/mooyang-code/moox/web/src/api/storage/types.ts`: TypeScript model types that mirror current storage proto JSON.
- `/Users/mooyang/Documents/go/src/github.com/mooyang-code/moox/web/src/api/storage/metadata.ts`: MetadataService wrappers.
- `/Users/mooyang/Documents/go/src/github.com/mooyang-code/moox/web/src/api/storage/access.ts`: AccessService wrappers.
- `/Users/mooyang/Documents/go/src/github.com/mooyang-code/moox/web/src/api/storage/view.ts`: ViewService wrappers.
- `/Users/mooyang/Documents/go/src/github.com/mooyang-code/moox/web/src/api/config.ts`: keep only legacy/common API behavior that non-storage pages still use.
- `/Users/mooyang/Documents/go/src/github.com/mooyang-code/moox/web/src/api/index.ts`: keep or rename as non-storage client; no storage-specific compatibility remains here.

Space state and layout:

- `/Users/mooyang/Documents/go/src/github.com/mooyang-code/moox/web/src/store/modules/space.ts`: global selected Space store.
- `/Users/mooyang/Documents/go/src/github.com/mooyang-code/moox/web/src/layout/layout-head/index.vue`: top Space selector.
- `/Users/mooyang/Documents/go/src/github.com/mooyang-code/moox/web/src/layout/components/Menu/project-menu.vue`: remove project-specific behavior or replace with Space-aware menu helper.
- `/Users/mooyang/Documents/go/src/github.com/mooyang-code/moox/web/src/store/modules/project.ts`: migrate or delete after imports are moved.

Routes and menu:

- `/Users/mooyang/Documents/go/src/github.com/mooyang-code/moox/web/src/router/route.ts`: route tree.
- `/Users/mooyang/Documents/go/src/github.com/mooyang-code/moox/web/src/router/route-output.ts`: route auto-loader assumptions.
- `/Users/mooyang/Documents/go/src/github.com/mooyang-code/moox/web/src/mock/_data/system_menu.ts`: menu tree.
- `/Users/mooyang/Documents/go/src/github.com/mooyang-code/moox/web/src/store/modules/route-config.ts`: dynamic menu loading behavior.

Storage pages:

- Create: `/Users/mooyang/Documents/go/src/github.com/mooyang-code/moox/web/src/views/settings/spaces/index.vue`
- Create: `/Users/mooyang/Documents/go/src/github.com/mooyang-code/moox/web/src/views/data/sources/index.vue`
- Create: `/Users/mooyang/Documents/go/src/github.com/mooyang-code/moox/web/src/views/data/subjects/index.vue`
- Create: `/Users/mooyang/Documents/go/src/github.com/mooyang-code/moox/web/src/views/data/datasets/index.vue`
- Create: `/Users/mooyang/Documents/go/src/github.com/mooyang-code/moox/web/src/views/data/datasets/components/dataset-column-panel.vue`
- Create: `/Users/mooyang/Documents/go/src/github.com/mooyang-code/moox/web/src/views/data/datasets/components/dataset-subject-panel.vue`
- Create: `/Users/mooyang/Documents/go/src/github.com/mooyang-code/moox/web/src/views/data/fields/index.vue`
- Create: `/Users/mooyang/Documents/go/src/github.com/mooyang-code/moox/web/src/views/data/factors/index.vue`
- Create: `/Users/mooyang/Documents/go/src/github.com/mooyang-code/moox/web/src/views/data/views/index.vue`
- Create: `/Users/mooyang/Documents/go/src/github.com/mooyang-code/moox/web/src/views/data/views/components/view-column-panel.vue`
- Create: `/Users/mooyang/Documents/go/src/github.com/mooyang-code/moox/web/src/views/data/list/index.vue`
- Create: `/Users/mooyang/Documents/go/src/github.com/mooyang-code/moox/web/src/views/data/sync/index.vue`
- Modify or replace: `/Users/mooyang/Documents/go/src/github.com/mooyang-code/moox/web/src/views/data/overview/overview.vue`
- Create: `/Users/mooyang/Documents/go/src/github.com/mooyang-code/moox/web/src/views/ops/storage/nodes.vue`
- Create: `/Users/mooyang/Documents/go/src/github.com/mooyang-code/moox/web/src/views/ops/storage/routes.vue`
- Create: `/Users/mooyang/Documents/go/src/github.com/mooyang-code/moox/web/src/views/ops/storage/archive.vue`

Old Storage pages to remove after replacements are wired:

- `/Users/mooyang/Documents/go/src/github.com/mooyang-code/moox/web/src/views/project/create-project/create-project.vue`
- `/Users/mooyang/Documents/go/src/github.com/mooyang-code/moox/web/src/views/project/dataset/dataset.vue`
- `/Users/mooyang/Documents/go/src/github.com/mooyang-code/moox/web/src/views/project/field-management/field-management.vue`
- `/Users/mooyang/Documents/go/src/github.com/mooyang-code/moox/web/src/views/project/storage-config/storage-config.vue`
- `/Users/mooyang/Documents/go/src/github.com/mooyang-code/moox/web/src/views/project/storage-config/components/storage-device-config.vue`
- `/Users/mooyang/Documents/go/src/github.com/mooyang-code/moox/web/src/views/project/storage-config/components/field-route-config.vue`
- `/Users/mooyang/Documents/go/src/github.com/mooyang-code/moox/web/src/views/project/storage-config/components/object-route-config.vue`
- `/Users/mooyang/Documents/go/src/github.com/mooyang-code/moox/web/src/views/project/storage-config/components/storage-node-config.vue`
- `/Users/mooyang/Documents/go/src/github.com/mooyang-code/moox/web/src/views/data/object-list/object-list.vue`
- `/Users/mooyang/Documents/go/src/github.com/mooyang-code/moox/web/src/views/data/data-list/data-list.vue`
- `/Users/mooyang/Documents/go/src/github.com/mooyang-code/moox/web/src/views/data/sync/sync.vue`

Non-storage pages to keep:

- `/Users/mooyang/Documents/go/src/github.com/mooyang-code/moox/web/src/views/collector/**`
- `/Users/mooyang/Documents/go/src/github.com/mooyang-code/moox/web/src/views/container/**`
- `/Users/mooyang/Documents/go/src/github.com/mooyang-code/moox/web/src/views/strategy/**`
- `/Users/mooyang/Documents/go/src/github.com/mooyang-code/moox/web/src/views/trading/**`

## Task 1: Rebuild Control/Admin and Storage Schema

**Files:**

- Create or rewrite: `/Users/mooyang/Documents/go/src/github.com/mooyang-code/moox/modules/control/schema/admin.sql`
- Rename and rewrite: `/Users/mooyang/Documents/go/src/github.com/mooyang-code/moox/modules/storage/schema/storage_metadata.sql` -> `/Users/mooyang/Documents/go/src/github.com/mooyang-code/moox/modules/storage/schema/metadata.sql`
- Delete: `/Users/mooyang/Documents/go/src/github.com/mooyang-code/moox/modules/storage/schema/admin_console.sql`
- Modify: `/Users/mooyang/Documents/go/src/github.com/mooyang-code/moox/modules/control/internal/service/database/manager.go`
- Modify: `/Users/mooyang/Documents/go/src/github.com/mooyang-code/moox/modules/control/internal/service/auth/dao/user.go`
- Modify: `/Users/mooyang/Documents/go/src/github.com/mooyang-code/moox/modules/control/internal/service/auth/impl/init.go`
- Modify: `/Users/mooyang/Documents/go/src/github.com/mooyang-code/moox/modules/storage/internal/infra/metadata/sqlite/store.go`
- Modify: `/Users/mooyang/Documents/go/src/github.com/mooyang-code/moox/modules/storage/config/storage.yaml`
- Create: `/Users/mooyang/Documents/go/src/github.com/mooyang-code/moox/modules/control/tests/schema/admin_schema_test.go`
- Rename or update: `/Users/mooyang/Documents/go/src/github.com/mooyang-code/moox/modules/storage/tests/schema/storage_metadata_schema_test.go` -> `/Users/mooyang/Documents/go/src/github.com/mooyang-code/moox/modules/storage/tests/schema/metadata_schema_test.go`

- [ ] **Step 1: Create final Control/Admin schema**

Create `modules/control/schema/admin.sql` as the single latest full schema for the management console. It must include these platform-level tables:

```sql
CREATE TABLE IF NOT EXISTS t_spaces (
    c_id INTEGER NOT NULL PRIMARY KEY AUTOINCREMENT,
    c_space_id TEXT NOT NULL,
    c_name TEXT NOT NULL,
    c_description TEXT NOT NULL DEFAULT '',
    c_owner TEXT NOT NULL DEFAULT '',
    c_market TEXT NOT NULL DEFAULT '',
    c_timezone TEXT NOT NULL DEFAULT '',
    c_status TEXT NOT NULL DEFAULT 'active',
    c_attributes TEXT NOT NULL DEFAULT '{}',
    c_invalid INTEGER NOT NULL DEFAULT 0,
    c_ctime DATETIME DEFAULT CURRENT_TIMESTAMP,
    c_mtime DATETIME DEFAULT CURRENT_TIMESTAMP
);

CREATE UNIQUE INDEX IF NOT EXISTS idx_spaces_space_id_invalid
ON t_spaces(c_space_id, c_invalid);

CREATE TABLE IF NOT EXISTS t_space_members (
    c_id INTEGER NOT NULL PRIMARY KEY AUTOINCREMENT,
    c_space_id TEXT NOT NULL,
    c_user_id TEXT NOT NULL,
    c_role TEXT NOT NULL DEFAULT 'member',
    c_status TEXT NOT NULL DEFAULT 'active',
    c_attributes TEXT NOT NULL DEFAULT '{}',
    c_ctime DATETIME DEFAULT CURRENT_TIMESTAMP,
    c_mtime DATETIME DEFAULT CURRENT_TIMESTAMP
);

CREATE UNIQUE INDEX IF NOT EXISTS idx_space_members_space_user
ON t_space_members(c_space_id, c_user_id);
CREATE INDEX IF NOT EXISTS idx_space_members_user_id
ON t_space_members(c_user_id);
```

Then include the existing admin-console domains as final-state tables, not migrated legacy tables:

```text
t_users
t_active_tokens
t_login_history
t_user_actions
t_cloud_accounts
t_cloud_nodes
t_function_packages
t_collector_data_type_configs
t_collector_field_configs
t_collector_task_rules
t_collector_task_instances
t_async_jobs
t_async_job_tasks
t_node_task_snapshot
t_exchange_symbols
t_ssh_host
t_ssh_session
t_host_monitor_history
```

- [ ] **Step 2: Add `c_space_id` to Space-scoped admin tables**

In `admin.sql`, add `c_space_id TEXT NOT NULL DEFAULT ''` and useful indexes to these tables:

```text
t_cloud_accounts
t_cloud_nodes
t_function_packages
t_collector_task_rules
t_collector_task_instances
t_async_jobs
t_async_job_tasks
t_node_task_snapshot
t_exchange_symbols
t_ssh_host
t_ssh_session
t_host_monitor_history
```

Do not add `c_space_id` to these global identity/audit tables:

```text
t_users
t_active_tokens
t_login_history
t_user_actions
```

- [ ] **Step 3: Remove admin schema from Storage**

Delete `modules/storage/schema/admin_console.sql`. No Control/Admin table may be maintained under `modules/storage/schema`.

- [ ] **Step 4: Rename Storage metadata schema**

Rename `modules/storage/schema/storage_metadata.sql` to `modules/storage/schema/metadata.sql`. Update all code, config, tests, and docs that refer to the old filename.

The default storage schema path must become:

```text
modules/storage/schema/metadata.sql
```

- [ ] **Step 5: Stop treating GORM AutoMigrate as authoritative schema**

Replace `auth` startup comments and behavior so `admin.sql` is authoritative. If `AutoMigrate` remains temporarily, it must be documented as a developer-only fallback after `admin.sql` has been applied, not the source of truth.

- [ ] **Step 6: Apply Control/Admin schema from the database manager**

Add a schema application helper in `modules/control/internal/service/database/manager.go`:

```go
func (dm *Manager) ApplySchema(schemaPath string) error {
	raw, err := os.ReadFile(schemaPath)
	if err != nil {
		return fmt.Errorf("read schema %s: %w", schemaPath, err)
	}
	if err := dm.db.Exec(string(raw)).Error; err != nil {
		return fmt.Errorf("apply schema %s: %w", schemaPath, err)
	}
	return nil
}
```

Call it with `modules/control/schema/admin.sql` during control startup or from an explicit bootstrap path.

- [ ] **Step 7: Add schema tests**

Create `modules/control/tests/schema/admin_schema_test.go` that opens `modules/control/schema/admin.sql` as text and asserts:

```text
contains CREATE TABLE IF NOT EXISTS t_spaces
contains CREATE TABLE IF NOT EXISTS t_space_members
contains c_space_id in all Space-scoped admin tables
does not contain CREATE TABLE for storage metadata tables such as t_datasets or t_views
does not contain migration directory references
```

Update the storage schema test to read `modules/storage/schema/metadata.sql` and assert:

```text
contains t_spaces or storage-side Space metadata only if Storage still needs it
contains t_datasets, t_views, t_fields, t_dataset_columns
does not contain t_users, t_cloud_nodes, t_ssh_host, or t_collector_task_rules
```

- [ ] **Step 8: Verify schema tests**

Run:

```bash
cd /Users/mooyang/Documents/go/src/github.com/mooyang-code/moox/modules/control
GOWORK=off go test ./...

cd /Users/mooyang/Documents/go/src/github.com/mooyang-code/moox/modules/storage
GOWORK=off go test ./tests/schema/...
```

Expected: PASS.

## Task 2: Implement Control/Admin Space API

**Files:**

- Create: `/Users/mooyang/Documents/go/src/github.com/mooyang-code/moox/modules/control/internal/service/space/model.go`
- Create: `/Users/mooyang/Documents/go/src/github.com/mooyang-code/moox/modules/control/internal/service/space/dao.go`
- Create: `/Users/mooyang/Documents/go/src/github.com/mooyang-code/moox/modules/control/internal/service/space/service.go`
- Create: `/Users/mooyang/Documents/go/src/github.com/mooyang-code/moox/modules/control/internal/service/space/gateway.go`
- Create: `/Users/mooyang/Documents/go/src/github.com/mooyang-code/moox/modules/control/internal/service/space/service_test.go`
- Modify: `/Users/mooyang/Documents/go/src/github.com/mooyang-code/moox/modules/control/internal/bootstrap/services.go`
- Modify: `/Users/mooyang/Documents/go/src/github.com/mooyang-code/moox/modules/control/internal/bootstrap/trpc.go`

- [ ] **Step 1: Create Space models**

Create `model.go` with GORM models that match `admin.sql`:

```go
package space

import "time"

type Space struct {
	ID          int64     `gorm:"column:c_id;primaryKey;autoIncrement" json:"id"`
	SpaceID     string    `gorm:"column:c_space_id;not null;uniqueIndex:idx_spaces_space_id_invalid" json:"space_id"`
	Name        string    `gorm:"column:c_name;not null" json:"name"`
	Description string    `gorm:"column:c_description;not null;default:''" json:"description"`
	Owner       string    `gorm:"column:c_owner;not null;default:''" json:"owner"`
	Market      string    `gorm:"column:c_market;not null;default:''" json:"market"`
	Timezone    string    `gorm:"column:c_timezone;not null;default:''" json:"timezone"`
	Status      string    `gorm:"column:c_status;not null;default:'active'" json:"status"`
	Attributes  string    `gorm:"column:c_attributes;not null;default:'{}'" json:"attributes"`
	Invalid     int       `gorm:"column:c_invalid;not null;default:0;uniqueIndex:idx_spaces_space_id_invalid" json:"-"`
	CreatedAt   time.Time `gorm:"column:c_ctime;default:CURRENT_TIMESTAMP" json:"created_at"`
	UpdatedAt   time.Time `gorm:"column:c_mtime;default:CURRENT_TIMESTAMP" json:"updated_at"`
}

func (Space) TableName() string { return "t_spaces" }

type SpaceMember struct {
	ID         int64     `gorm:"column:c_id;primaryKey;autoIncrement" json:"id"`
	SpaceID    string    `gorm:"column:c_space_id;not null;uniqueIndex:idx_space_members_space_user" json:"space_id"`
	UserID     string    `gorm:"column:c_user_id;not null;uniqueIndex:idx_space_members_space_user" json:"user_id"`
	Role       string    `gorm:"column:c_role;not null;default:'member'" json:"role"`
	Status     string    `gorm:"column:c_status;not null;default:'active'" json:"status"`
	Attributes string    `gorm:"column:c_attributes;not null;default:'{}'" json:"attributes"`
	CreatedAt  time.Time `gorm:"column:c_ctime;default:CURRENT_TIMESTAMP" json:"created_at"`
	UpdatedAt  time.Time `gorm:"column:c_mtime;default:CURRENT_TIMESTAMP" json:"updated_at"`
}

func (SpaceMember) TableName() string { return "t_space_members" }
```

- [ ] **Step 2: Create DAO**

Create `dao.go` with explicit SQL/GORM operations. Do not use `AutoMigrate` here; `admin.sql` is authoritative.

```go
package space

import (
	"context"
	"fmt"
	"time"

	"gorm.io/gorm"
)

type DAO struct {
	db *gorm.DB
}

func NewDAO(db *gorm.DB) *DAO { return &DAO{db: db} }

func (d *DAO) CreateSpace(ctx context.Context, item *Space) error {
	now := time.Now()
	item.CreatedAt = now
	item.UpdatedAt = now
	if item.Status == "" {
		item.Status = "active"
	}
	if item.Attributes == "" {
		item.Attributes = "{}"
	}
	return d.db.WithContext(ctx).Create(item).Error
}

func (d *DAO) UpdateSpace(ctx context.Context, item *Space) error {
	if item.SpaceID == "" {
		return fmt.Errorf("space_id is required")
	}
	updates := map[string]interface{}{
		"c_name":        item.Name,
		"c_description": item.Description,
		"c_owner":       item.Owner,
		"c_market":      item.Market,
		"c_timezone":    item.Timezone,
		"c_status":      item.Status,
		"c_attributes":  item.Attributes,
		"c_mtime":       time.Now(),
	}
	return d.db.WithContext(ctx).Model(&Space{}).
		Where("c_space_id = ? AND c_invalid = 0", item.SpaceID).
		Updates(updates).Error
}

func (d *DAO) ListSpaces(ctx context.Context, owner string, status string, offset int, limit int) ([]Space, int64, error) {
	query := d.db.WithContext(ctx).Model(&Space{}).Where("c_invalid = 0")
	if owner != "" {
		query = query.Where("c_owner = ?", owner)
	}
	if status != "" {
		query = query.Where("c_status = ?", status)
	}
	var total int64
	if err := query.Count(&total).Error; err != nil {
		return nil, 0, err
	}
	var rows []Space
	if err := query.Order("c_mtime DESC").Offset(offset).Limit(limit).Find(&rows).Error; err != nil {
		return nil, 0, err
	}
	return rows, total, nil
}

func (d *DAO) ListSpaceMembers(ctx context.Context, spaceID string, offset int, limit int) ([]SpaceMember, int64, error) {
	query := d.db.WithContext(ctx).Model(&SpaceMember{}).Where("c_space_id = ?", spaceID)
	var total int64
	if err := query.Count(&total).Error; err != nil {
		return nil, 0, err
	}
	var rows []SpaceMember
	if err := query.Order("c_mtime DESC").Offset(offset).Limit(limit).Find(&rows).Error; err != nil {
		return nil, 0, err
	}
	return rows, total, nil
}
```

- [ ] **Step 3: Create service layer**

Create `service.go`:

```go
package space

import (
	"context"
	"fmt"

	"github.com/mooyang-code/moox/modules/control/internal/service/database"
)

type PageReq struct {
	Page int `json:"page"`
	Size int `json:"size"`
}

type PageResult struct {
	Page    int   `json:"page"`
	Size    int   `json:"size"`
	Total   int64 `json:"total"`
	HasMore bool  `json:"has_more"`
}

type Service interface {
	CreateSpace(ctx context.Context, item *Space) (*Space, error)
	UpdateSpace(ctx context.Context, item *Space) (*Space, error)
	ListSpaces(ctx context.Context, owner string, status string, page PageReq) ([]Space, PageResult, error)
	ListSpaceMembers(ctx context.Context, spaceID string, page PageReq) ([]SpaceMember, PageResult, error)
}

type service struct {
	dao *DAO
}

func NewService(dbManager *database.Manager) Service {
	return &service{dao: NewDAO(dbManager.GetDB())}
}

func normalizePage(page PageReq) (PageReq, int, int) {
	if page.Page <= 0 {
		page.Page = 1
	}
	if page.Size <= 0 || page.Size > 200 {
		page.Size = 20
	}
	offset := (page.Page - 1) * page.Size
	return page, offset, page.Size
}

func makePageResult(page PageReq, total int64) PageResult {
	return PageResult{Page: page.Page, Size: page.Size, Total: total, HasMore: int64(page.Page*page.Size) < total}
}

func (s *service) CreateSpace(ctx context.Context, item *Space) (*Space, error) {
	if item == nil || item.SpaceID == "" || item.Name == "" {
		return nil, fmt.Errorf("space_id and name are required")
	}
	if err := s.dao.CreateSpace(ctx, item); err != nil {
		return nil, err
	}
	return item, nil
}

func (s *service) UpdateSpace(ctx context.Context, item *Space) (*Space, error) {
	if item == nil || item.SpaceID == "" {
		return nil, fmt.Errorf("space_id is required")
	}
	if err := s.dao.UpdateSpace(ctx, item); err != nil {
		return nil, err
	}
	return item, nil
}

func (s *service) ListSpaces(ctx context.Context, owner string, status string, page PageReq) ([]Space, PageResult, error) {
	page, offset, limit := normalizePage(page)
	rows, total, err := s.dao.ListSpaces(ctx, owner, status, offset, limit)
	return rows, makePageResult(page, total), err
}

func (s *service) ListSpaceMembers(ctx context.Context, spaceID string, page PageReq) ([]SpaceMember, PageResult, error) {
	page, offset, limit := normalizePage(page)
	rows, total, err := s.dao.ListSpaceMembers(ctx, spaceID, offset, limit)
	return rows, makePageResult(page, total), err
}
```

- [ ] **Step 4: Create gateway handler**

Create `gateway.go` with `ServiceID() == "space"` so web-host can call `/api/control/space/{method}`:

```go
package space

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/mooyang-code/moox/modules/control/internal/gateway"
	"trpc.group/trpc-go/trpc-go/log"
)

type gatewayHandler struct {
	service Service
}

func RegisterGateway(service Service) {
	handler := &gatewayHandler{service: service}
	gateway.GetGatewayHandleInstance().Register(handler)
	log.Infof("[Space Gateway] registered service: %s", handler.ServiceID())
}

func (h *gatewayHandler) ServiceID() string { return "space" }

func (h *gatewayHandler) ForwardRequest(ctx context.Context, method string, headers map[string]string, body []byte) ([]byte, error) {
	switch method {
	case "CreateSpace":
		var req struct{ Space Space `json:"space"` }
		if err := json.Unmarshal(body, &req); err != nil { return nil, err }
		space, err := h.service.CreateSpace(ctx, &req.Space)
		return encodeControlResponse(map[string]interface{}{"space": space}, err)
	case "UpdateSpace":
		var req struct{ Space Space `json:"space"` }
		if err := json.Unmarshal(body, &req); err != nil { return nil, err }
		space, err := h.service.UpdateSpace(ctx, &req.Space)
		return encodeControlResponse(map[string]interface{}{"space": space}, err)
	case "ListSpaces":
		var req struct {
			Owner  string  `json:"owner"`
			Status string  `json:"status"`
			Page   PageReq `json:"page"`
		}
		if err := json.Unmarshal(body, &req); err != nil { return nil, err }
		spaces, page, err := h.service.ListSpaces(ctx, req.Owner, req.Status, req.Page)
		return encodeControlResponse(map[string]interface{}{"spaces": spaces, "page_result": page}, err)
	case "ListSpaceMembers":
		var req struct {
			SpaceID string  `json:"space_id"`
			Page    PageReq `json:"page"`
		}
		if err := json.Unmarshal(body, &req); err != nil { return nil, err }
		members, page, err := h.service.ListSpaceMembers(ctx, req.SpaceID, req.Page)
		return encodeControlResponse(map[string]interface{}{"members": members, "page_result": page}, err)
	default:
		return nil, fmt.Errorf("unsupported space method: %s", method)
	}
}

func encodeControlResponse(data map[string]interface{}, err error) ([]byte, error) {
	if err != nil {
		return json.Marshal(map[string]interface{}{"code": 1, "message": err.Error()})
	}
	data["code"] = 0
	data["message"] = "success"
	return json.Marshal(data)
}
```

- [ ] **Step 5: Wire service into bootstrap**

Modify `Services` in `bootstrap/services.go`:

```go
SpaceService space.Service
```

In `createCoreServices`, after DB initialization:

```go
log.Info("[Bootstrap] 正在创建 Space 服务...")
spaceService := space.NewService(dbManager)
```

Add it to the returned `Services`:

```go
SpaceService: spaceService,
```

Modify `bootstrap/trpc.go` imports and registration:

```go
spacegateway "github.com/mooyang-code/moox/modules/control/internal/service/space"

spacegateway.RegisterGateway(services.SpaceService)
```

Place the registration after `gateway.InitGatewayServices(s)` and before the web-host/manual verification step tries `/gateway/space/ListSpaces`.

- [ ] **Step 6: Add Space service tests**

Create `service_test.go` with an in-memory SQLite DB that applies `modules/control/schema/admin.sql`, then asserts:

```text
CreateSpace creates one row in t_spaces
ListSpaces returns the created Space and page_result.total=1
UpdateSpace changes name, owner, market, timezone, and status
ListSpaceMembers returns members inserted into t_space_members
```

Use the real `NewService` path or the DAO directly; do not use GORM AutoMigrate in this test.

- [ ] **Step 7: Verify Control Space service**

Run:

```bash
cd /Users/mooyang/Documents/go/src/github.com/mooyang-code/moox/modules/control
GOWORK=off go test ./internal/service/space ./internal/bootstrap
```

Expected: PASS.

## Task 3: Add web-host Control and Storage Gateway

**Files:**

- Modify: `/Users/mooyang/Documents/go/src/github.com/mooyang-code/moox/web-host/main.go`
- Create: `/Users/mooyang/Documents/go/src/github.com/mooyang-code/moox/web-host/main_test.go`
- Modify: `/Users/mooyang/Documents/go/src/github.com/mooyang-code/moox/web-host/README.md`

- [ ] **Step 1: Extract gateway configuration**

Add a small config struct in `main.go`:

```go
type gatewayConfig struct {
	ListenAddr  string
	ControlURL  string
	MetadataURL string
	AccessURL   string
	ViewURL     string
}

func loadGatewayConfig() gatewayConfig {
	return gatewayConfig{
		ListenAddr:  envOr("MOOX_WEB_HOST_ADDR", ":19527"),
		ControlURL:  strings.TrimRight(envOr("MOOX_CONTROL_GATEWAY_URL", "http://127.0.0.1:20103"), "/"),
		MetadataURL: strings.TrimRight(envOr("MOOX_STORAGE_METADATA_URL", "http://127.0.0.1:19101"), "/"),
		AccessURL:   strings.TrimRight(envOr("MOOX_STORAGE_ACCESS_URL", "http://127.0.0.1:19104"), "/"),
		ViewURL:     strings.TrimRight(envOr("MOOX_STORAGE_VIEW_URL", "http://127.0.0.1:19105"), "/"),
	}
}

func envOr(key string, fallback string) string {
	if value := strings.TrimSpace(os.Getenv(key)); value != "" {
		return value
	}
	return fallback
}
```

- [ ] **Step 2: Write the proxy routing tests**

Create `main_test.go` with table tests that verify Control and Storage path rewriting:

```go
func TestControlGatewayRewrite(t *testing.T) {
	cases := []struct {
		path       string
		wantPath   string
		wantService string
		wantMethod  string
	}{
		{"/api/control/space/ListSpaces", "/gateway/space/ListSpaces", "space", "ListSpaces"},
		{"/api/control/cloudnode/ListNodes", "/gateway/cloudnode/ListNodes", "cloudnode", "ListNodes"},
	}
	for _, tc := range cases {
		got, ok := resolveControlGatewayTarget(tc.path)
		if !ok {
			t.Fatalf("%s was not recognized as control gateway path", tc.path)
		}
		if got.Path != tc.wantPath || got.Service != tc.wantService || got.Method != tc.wantMethod {
			t.Fatalf("%s => %+v, want path=%s service=%s method=%s", tc.path, got, tc.wantPath, tc.wantService, tc.wantMethod)
		}
	}
}

func TestStorageGatewayRewrite(t *testing.T) {
	cases := []struct {
		path       string
		wantBase   string
		wantRPC    string
		wantMethod string
	}{
		{"/api/storage/metadata/ListDatasets", "metadata", "/trpc.storage.metadata.MetadataService/ListDatasets", "ListDatasets"},
		{"/api/storage/access/ReadTimeSeriesRows", "access", "/trpc.storage.access.AccessService/ReadTimeSeriesRows", "ReadTimeSeriesRows"},
		{"/api/storage/view/QueryTimeSeriesRows", "view", "/trpc.storage.view.ViewService/QueryTimeSeriesRows", "QueryTimeSeriesRows"},
	}
	for _, tc := range cases {
		got, ok := resolveStorageGatewayTarget(tc.path)
		if !ok {
			t.Fatalf("%s was not recognized as storage gateway path", tc.path)
		}
		if got.Base != tc.wantBase || got.Path != tc.wantRPC || got.Method != tc.wantMethod {
			t.Fatalf("%s => %+v, want base=%s path=%s method=%s", tc.path, got, tc.wantBase, tc.wantRPC, tc.wantMethod)
		}
	}
}
```

- [ ] **Step 3: Implement gateway matching**

Use one helper so both tests and handler share the same rule:

```go
type gatewayTarget struct {
	Base    string
	Service string
	Path    string
	Method  string
}

func resolveControlGatewayTarget(path string) (gatewayTarget, bool) {
	const prefix = "/api/control/"
	if !strings.HasPrefix(path, prefix) {
		return gatewayTarget{}, false
	}
	rest := strings.TrimPrefix(path, prefix)
	parts := strings.Split(rest, "/")
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return gatewayTarget{}, false
	}
	return gatewayTarget{
		Base:    "control",
		Service: parts[0],
		Path:    "/gateway/" + parts[0] + "/" + parts[1],
		Method:  parts[1],
	}, true
}

func resolveStorageGatewayTarget(path string) (gatewayTarget, bool) {
	const prefix = "/api/storage/"
	if !strings.HasPrefix(path, prefix) {
		return gatewayTarget{}, false
	}
	rest := strings.TrimPrefix(path, prefix)
	parts := strings.Split(rest, "/")
	if len(parts) != 2 || parts[1] == "" {
		return gatewayTarget{}, false
	}
	switch parts[0] {
	case "metadata":
		return gatewayTarget{Base: "metadata", Path: "/trpc.storage.metadata.MetadataService/" + parts[1], Method: parts[1]}, true
	case "access":
		return gatewayTarget{Base: "access", Path: "/trpc.storage.access.AccessService/" + parts[1], Method: parts[1]}, true
	case "view":
		return gatewayTarget{Base: "view", Path: "/trpc.storage.view.ViewService/" + parts[1], Method: parts[1]}, true
	default:
		return gatewayTarget{}, false
	}
}
```

- [ ] **Step 4: Replace the single `/gateway` proxy**

In the HTTP handler:

- route `/api/control/*` through `resolveControlGatewayTarget` to `ControlURL`;
- route `/api/storage/*` through `resolveStorageGatewayTarget` to Metadata, Access, or View URLs;
- do not expose the old `/gateway/*` path from the browser as the primary frontend contract;
- do not forward storage calls to the old `20103` Control gateway.

The Control route is intentionally generic because current Control already has a service gateway shaped as `/gateway/{service}/{method}`. Add a `space` service there in the Control module when implementing Space CRUD.

- [ ] **Step 5: Remove unused gzip response wrapper**

Delete `gzipResponseWriter` if it remains unused after the proxy refactor.

- [ ] **Step 6: Verify web-host**

Run:

```bash
cd /Users/mooyang/Documents/go/src/github.com/mooyang-code/moox/web-host
GOWORK=off go test ./...
```

Expected: PASS.

## Task 4: Build the Control and Storage API Foundation

**Files:**

- Create: `/Users/mooyang/Documents/go/src/github.com/mooyang-code/moox/web/src/api/control/http.ts`
- Create: `/Users/mooyang/Documents/go/src/github.com/mooyang-code/moox/web/src/api/control/types.ts`
- Create: `/Users/mooyang/Documents/go/src/github.com/mooyang-code/moox/web/src/api/control/spaces.ts`
- Create: `/Users/mooyang/Documents/go/src/github.com/mooyang-code/moox/web/src/api/storage/auth.ts`
- Create: `/Users/mooyang/Documents/go/src/github.com/mooyang-code/moox/web/src/api/storage/http.ts`
- Create: `/Users/mooyang/Documents/go/src/github.com/mooyang-code/moox/web/src/api/storage/types.ts`
- Create: `/Users/mooyang/Documents/go/src/github.com/mooyang-code/moox/web/src/api/storage/metadata.ts`
- Create: `/Users/mooyang/Documents/go/src/github.com/mooyang-code/moox/web/src/api/storage/access.ts`
- Create: `/Users/mooyang/Documents/go/src/github.com/mooyang-code/moox/web/src/api/storage/view.ts`
- Modify: `/Users/mooyang/Documents/go/src/github.com/mooyang-code/moox/web/vite.config.ts`

- [ ] **Step 1: Add Control/Admin HTTP helper**

Create `web/src/api/control/types.ts`:

```ts
export interface ControlResponse<T> {
  code?: number | string;
  message?: string;
  msg?: string;
  data?: T;
}

export interface PageReq {
  page?: number;
  size?: number;
}

export interface PageResult {
  page: number;
  size: number;
  total: number | string;
  has_more?: boolean;
}

export interface Space {
  space_id: string;
  name: string;
  description?: string;
  owner?: string;
  market?: string;
  timezone?: string;
  status: string;
  attributes?: Record<string, string>;
  created_at?: string;
  updated_at?: string;
}

export interface SpaceMember {
  space_id: string;
  user_id: string;
  role: string;
  status: string;
}
```

Create `web/src/api/control/http.ts`:

```ts
import axios from 'axios';
import { Message } from '@arco-design/web-vue';
import type { ControlResponse } from './types';

const controlClient = axios.create({
  baseURL: '',
  timeout: 30000,
  headers: { 'Content-Type': 'application/json' },
});

function assertControlSuccess<T>(rsp: ControlResponse<T>): T {
  const code = rsp.code ?? 0;
  if (code !== 0 && code !== '0' && code !== 'SUCCESS') {
    throw new Error(rsp.message || rsp.msg || `control request failed: ${code}`);
  }
  return (rsp.data ?? rsp) as T;
}

export async function callControl<TReq extends object, TRsp>(
  service: string,
  method: string,
  req: TReq,
): Promise<TRsp> {
  const rsp = await controlClient.post<ControlResponse<TRsp>>(`/api/control/${service}/${method}`, req);
  return assertControlSuccess<TRsp>(rsp.data);
}

controlClient.interceptors.response.use(
  (rsp) => rsp,
  (error) => {
    Message.error(error?.message || 'Control 请求失败');
    return Promise.reject(error);
  },
);
```

- [ ] **Step 2: Add Control/Admin Space API wrapper**

Create `web/src/api/control/spaces.ts`:

```ts
import { callControl } from './http';
import type { PageReq, PageResult, Space, SpaceMember } from './types';

export interface ListSpacesReq {
  owner?: string;
  status?: string;
  page?: PageReq;
}

export interface ListSpacesRsp {
  spaces: Space[];
  page_result?: PageResult;
}

export function listSpaces(req: ListSpacesReq = {}) {
  return callControl<ListSpacesReq, ListSpacesRsp>('space', 'ListSpaces', req);
}

export function createSpace(space: Space) {
  return callControl<{ space: Space }, { space: Space }>('space', 'CreateSpace', { space });
}

export function updateSpace(space: Space) {
  return callControl<{ space: Space }, { space: Space }>('space', 'UpdateSpace', { space });
}

export function listSpaceMembers(req: { space_id: string; page?: PageReq }) {
  return callControl<typeof req, { members: SpaceMember[]; page_result?: PageResult }>('space', 'ListSpaceMembers', req);
}
```

- [ ] **Step 3: Add centralized Storage development auth**

Create `auth.ts`:

```ts
export interface StorageAuthInfo {
  app_id: string;
  app_key: string;
  operator: string;
  request_id: string;
}

export function getStorageAuthInfo(): StorageAuthInfo {
  return {
    app_id: 'moox_frontend',
    app_key: '2521e0d21b6be0347b72bca93904a0dd',
    operator: 'moox_web',
    request_id: `web-${Date.now()}-${Math.random().toString(16).slice(2)}`,
  };
}
```

- [ ] **Step 4: Add Storage HTTP helper**

Create `http.ts`:

```ts
import axios from 'axios';
import { Message } from '@arco-design/web-vue';
import { getStorageAuthInfo } from './auth';
import type { RetInfo } from './types';

const storageClient = axios.create({
  baseURL: '',
  timeout: 30000,
  headers: { 'Content-Type': 'application/json' },
});

function assertSuccess(retInfo?: RetInfo) {
  if (!retInfo) throw new Error('storage response missing ret_info');
  if (retInfo.code !== 'SUCCESS' && retInfo.code !== 0) {
    throw new Error(retInfo.msg || `storage request failed: ${retInfo.code}`);
  }
}

async function callStorage<TReq extends object, TRsp extends { ret_info?: RetInfo }>(
  group: 'metadata' | 'access' | 'view',
  method: string,
  req: TReq,
): Promise<TRsp> {
  const rsp = await storageClient.post<TRsp>(`/api/storage/${group}/${method}`, {
    auth_info: getStorageAuthInfo(),
    ...req,
  });
  assertSuccess(rsp.data.ret_info);
  return rsp.data;
}

export const callMetadata = <TReq extends object, TRsp extends { ret_info?: RetInfo }>(
  method: string,
  req: TReq,
) => callStorage<TReq, TRsp>('metadata', method, req);

export const callAccess = <TReq extends object, TRsp extends { ret_info?: RetInfo }>(
  method: string,
  req: TReq,
) => callStorage<TReq, TRsp>('access', method, req);

export const callView = <TReq extends object, TRsp extends { ret_info?: RetInfo }>(
  method: string,
  req: TReq,
) => callStorage<TReq, TRsp>('view', method, req);

storageClient.interceptors.response.use(
  (rsp) => rsp,
  (error) => {
    Message.error(error?.message || 'Storage 请求失败');
    return Promise.reject(error);
  },
);
```

- [ ] **Step 5: Add Storage proto JSON types**

Create `web/src/api/storage/types.ts` with the exact user-facing storage models:

```ts
export type DataKind =
  | 'DATA_KIND_RECORD'
  | 'DATA_KIND_TIME_SERIES'
  | 'DATA_KIND_SNAPSHOT'
  | 'DATA_KIND_EVENT'
  | 'DATA_KIND_DOCUMENT'
  | 'DATA_KIND_TABLE';

export type FieldValueType =
  | 'FIELD_VALUE_TYPE_STRING'
  | 'FIELD_VALUE_TYPE_INT'
  | 'FIELD_VALUE_TYPE_DOUBLE'
  | 'FIELD_VALUE_TYPE_BOOL'
  | 'FIELD_VALUE_TYPE_TIME'
  | 'FIELD_VALUE_TYPE_JSON'
  | 'FIELD_VALUE_TYPE_BYTES';

export type DatasetColumnOriginType =
  | 'DATASET_COLUMN_ORIGIN_TYPE_FIELD'
  | 'DATASET_COLUMN_ORIGIN_TYPE_FACTOR'
  | 'DATASET_COLUMN_ORIGIN_TYPE_SYSTEM';

export type ColumnOriginType =
  | 'COLUMN_ORIGIN_TYPE_DATASET_COLUMN'
  | 'COLUMN_ORIGIN_TYPE_SYSTEM'
  | 'COLUMN_ORIGIN_TYPE_EXPRESSION';

export interface RetInfo { code: number | string; msg: string; }
export interface Page { page?: number; size?: number; cursor?: string; }
export interface PageResult { page: number; size: number; total: string | number; has_more: boolean; next_cursor: string; }
export interface TimeRange { start_time?: string; end_time?: string; }
export interface VersionRange { start_version?: string; end_version?: string; }
```

Then add the remaining storage interfaces in the same file: `DataSource`, `Subject`, `SubjectSymbol`, `Dataset`, `DatasetSubject`, `Field`, `Factor`, `DatasetColumn`, `View`, `ViewColumn`, `PrimaryStoreNode`, `PrimaryStoreRoute`, `ArchiveFile`, `ColumnValue`, `TimeSeriesKey`, `TimeSeriesRow`, `RecordKey`, and `RecordRow`.

- [ ] **Step 6: Add Storage metadata wrappers**

Create `metadata.ts` with one function per Storage MetadataService method used by the UI:

```ts
export async function listDatasets(params: { space_id: string; page?: Page }) {
  const rsp = await callMetadata<typeof params, { ret_info: RetInfo; datasets: Dataset[]; page_result: PageResult }>('ListDatasets', params);
  return rsp;
}

export async function createDataset(dataset: Dataset) {
  const rsp = await callMetadata<{ dataset: Dataset }, { ret_info: RetInfo; dataset: Dataset }>('CreateDataset', { dataset });
  return rsp.dataset;
}
```

Include wrappers for `Create/Update/Get/List` or `Upsert/List` of: DataSource, Subject, SubjectSymbol, Dataset, DatasetSubject, Field, Factor, DatasetColumn, View, ViewColumn, PrimaryStoreNode, PrimaryStoreRoute, and ArchiveFile. Do not add Control/Admin `Space` wrappers here. Do not add `CreateDevice`, `UpdateDevice`, `GetDevice`, or `ListDevices` wrappers for UI use.

- [ ] **Step 7: Add access and view wrappers**

Create `access.ts` for `WriteTimeSeriesRows`, `ReadTimeSeriesRows`, `WriteRecordRows`, and `ReadRecordRows`.

Create `view.ts` for `QueryTimeSeriesRows`, `SearchRecordRows`, `RebuildTimeSeriesView`, and `RebuildRecordView`.

- [ ] **Step 8: Update Vite development proxy**

In `vite.config.ts`, replace the old browser-facing `/gateway` usage with Control and Storage rewrite rules:

```ts
proxy: {
  '/api/control': {
    target: 'http://127.0.0.1:20103',
    changeOrigin: true,
    rewrite: (path) => path.replace(/^\/api\/control\/([^/]+)\/([^/]+)$/, '/gateway/$1/$2'),
  },
  '/api/storage/metadata': {
    target: 'http://127.0.0.1:19101',
    changeOrigin: true,
    rewrite: (path) => path.replace(/^\/api\/storage\/metadata\/(.+)$/, '/trpc.storage.metadata.MetadataService/$1'),
  },
  '/api/storage/access': {
    target: 'http://127.0.0.1:19104',
    changeOrigin: true,
    rewrite: (path) => path.replace(/^\/api\/storage\/access\/(.+)$/, '/trpc.storage.access.AccessService/$1'),
  },
  '/api/storage/view': {
    target: 'http://127.0.0.1:19105',
    changeOrigin: true,
    rewrite: (path) => path.replace(/^\/api\/storage\/view\/(.+)$/, '/trpc.storage.view.ViewService/$1'),
  },
}
```

- [ ] **Step 9: Verify type checking**

Run:

```bash
cd /Users/mooyang/Documents/go/src/github.com/mooyang-code/moox/web
pnpm build:prod
```

Expected: `vue-tsc` and Vite build complete without type errors.

## Task 5: Add Global Space Store and Header Selector

**Files:**

- Create: `/Users/mooyang/Documents/go/src/github.com/mooyang-code/moox/web/src/store/modules/space.ts`
- Modify: `/Users/mooyang/Documents/go/src/github.com/mooyang-code/moox/web/src/layout/layout-head/index.vue`
- Modify: `/Users/mooyang/Documents/go/src/github.com/mooyang-code/moox/web/src/views/home/home.vue`
- Modify: `/Users/mooyang/Documents/go/src/github.com/mooyang-code/moox/web/src/store/index.ts`

- [ ] **Step 1: Add `spaceStore`**

Create a Pinia store:

```ts
import { computed, ref } from 'vue';
import { defineStore } from 'pinia';
import { listSpaces } from '@/api/control/spaces';
import type { Space } from '@/api/control/types';

export const useSpaceStore = defineStore(
  'spaceStore',
  () => {
    const spaces = ref<Space[]>([]);
    const selectedSpaceId = ref<string>('');
    const loading = ref(false);

    const selectedSpace = computed(() => spaces.value.find((item) => item.space_id === selectedSpaceId.value));

    async function loadSpaces() {
      loading.value = true;
      try {
        const rsp = await listSpaces({ page: { page: 1, size: 200 } });
        spaces.value = rsp.spaces || [];
        if (!selectedSpaceId.value && spaces.value.length > 0) {
          selectedSpaceId.value = spaces.value[0].space_id;
        }
      } finally {
        loading.value = false;
      }
    }

    function setSelectedSpace(spaceId: string) {
      selectedSpaceId.value = spaceId;
    }

    function requireSpaceId() {
      if (!selectedSpaceId.value) throw new Error('请先选择空间');
      return selectedSpaceId.value;
    }

    return { spaces, selectedSpaceId, selectedSpace, loading, loadSpaces, setSelectedSpace, requireSpaceId };
  },
  { persist: true },
);
```

- [ ] **Step 2: Replace old project selector in header**

In `layout-head/index.vue`, add an Arco select bound to `spaceStore.selectedSpaceId`. It must show `Space.name` and use `Space.space_id` as value. Add a small command button linking to `/settings/spaces`.

- [ ] **Step 3: Load spaces on app entry**

When layout mounts, call `spaceStore.loadSpaces()`. If no Space exists, show an empty-state message with a link to `/settings/spaces`.

- [ ] **Step 4: Keep non-storage pages usable**

The header selector must not block container, SSH, cloud-function, strategy, or trading pages from rendering. Pages that call storage APIs must call `spaceStore.requireSpaceId()` before sending requests.

- [ ] **Step 5: Verify**

Run:

```bash
cd /Users/mooyang/Documents/go/src/github.com/mooyang-code/moox/web
pnpm build:prod
```

Expected: PASS.

## Task 6: Reorganize Routes and Menu

**Files:**

- Modify: `/Users/mooyang/Documents/go/src/github.com/mooyang-code/moox/web/src/router/route.ts`
- Modify: `/Users/mooyang/Documents/go/src/github.com/mooyang-code/moox/web/src/mock/_data/system_menu.ts`
- Modify: `/Users/mooyang/Documents/go/src/github.com/mooyang-code/moox/web/src/store/modules/route-config.ts`
- Modify: `/Users/mooyang/Documents/go/src/github.com/mooyang-code/moox/web/src/router/route-output.ts`

- [ ] **Step 1: Replace Storage-related routes**

Remove old route entries under:

```text
/project/:projectId/*
/data-management/:projectId/*
```

Add flat routes:

```ts
{
  path: '/data/sources',
  name: 'data-sources',
  component: () => import('@/views/data/sources/index.vue'),
  meta: { title: '数据来源' },
},
{
  path: '/data/subjects',
  name: 'data-subjects',
  component: () => import('@/views/data/subjects/index.vue'),
  meta: { title: '数据对象' },
},
{
  path: '/data/datasets',
  name: 'data-datasets',
  component: () => import('@/views/data/datasets/index.vue'),
  meta: { title: '数据集' },
}
```

Add the remaining routes from the routing model section.

- [ ] **Step 2: Preserve non-storage routes**

Keep existing collector, container, strategy, and trading route components, but move their menu grouping to the new domain names. Do not delete these pages in this task.

- [ ] **Step 3: Update menu data**

Rewrite `system_menu.ts` to match the target menu shape. Use stable route names from `route.ts`; do not reference removed project routes.

- [ ] **Step 4: Verify residual route references**

Run:

```bash
cd /Users/mooyang/Documents/go/src/github.com/mooyang-code/moox
rg -n "projectId|proj_id|/project/|/data-management/|create-project|field-management|storage-config" web/src/router web/src/mock web/src/layout web/src/store
```

Expected: no matches in route/menu/layout/store files except comments that explicitly document removed legacy names.

## Task 7: Rebuild Space and Storage Metadata Pages

**Files:**

- Create: `/Users/mooyang/Documents/go/src/github.com/mooyang-code/moox/web/src/views/settings/spaces/index.vue`
- Create: `/Users/mooyang/Documents/go/src/github.com/mooyang-code/moox/web/src/views/data/sources/index.vue`
- Create: `/Users/mooyang/Documents/go/src/github.com/mooyang-code/moox/web/src/views/data/subjects/index.vue`
- Create: `/Users/mooyang/Documents/go/src/github.com/mooyang-code/moox/web/src/views/data/datasets/index.vue`
- Create: `/Users/mooyang/Documents/go/src/github.com/mooyang-code/moox/web/src/views/data/datasets/components/dataset-column-panel.vue`
- Create: `/Users/mooyang/Documents/go/src/github.com/mooyang-code/moox/web/src/views/data/datasets/components/dataset-subject-panel.vue`
- Create: `/Users/mooyang/Documents/go/src/github.com/mooyang-code/moox/web/src/views/data/fields/index.vue`
- Create: `/Users/mooyang/Documents/go/src/github.com/mooyang-code/moox/web/src/views/data/factors/index.vue`
- Create: `/Users/mooyang/Documents/go/src/github.com/mooyang-code/moox/web/src/views/data/views/index.vue`
- Create: `/Users/mooyang/Documents/go/src/github.com/mooyang-code/moox/web/src/views/data/views/components/view-column-panel.vue`

- [ ] **Step 1: Implement Space management**

`settings/spaces/index.vue` must support:

- list spaces via `web/src/api/control/spaces.ts:listSpaces`;
- create space via `web/src/api/control/spaces.ts:createSpace`;
- update name, description, owner, market, timezone, and status via `web/src/api/control/spaces.ts:updateSpace`;
- set current Space through `spaceStore.setSelectedSpace`.

Required table columns:

```text
space_id, name, owner, status, updated_at, operation
```

This page must not call Storage MetadataService `ListSpaces`, `CreateSpace`, or `UpdateSpace`. If Storage still keeps internal Space metadata for validation, synchronize it through backend bootstrap or an explicit admin operation outside the top-header selector flow.

- [ ] **Step 2: Implement DataSource page**

`data/sources/index.vue` must use `spaceStore.requireSpaceId()` and support:

- `ListDataSources(space_id)`;
- `CreateDataSource`;
- `UpdateDataSource`;
- fields: `data_source_id`, `name`, `kind`, `market`, `timezone`, `config_json`, `status`.

- [ ] **Step 3: Implement Subject page**

`data/subjects/index.vue` must support `Subject` and `SubjectSymbol`:

- main table: `subject_id`, `subject_type`, `name`, `market`, `currency`, `timezone`, `status`;
- detail drawer: list and upsert symbols with `data_source_id` and `external_symbol`.

- [ ] **Step 4: Implement Dataset page**

`data/datasets/index.vue` must support:

- `ListDatasets(space_id)`;
- `CreateDataset`;
- `UpdateDataset`;
- fields: `dataset_id`, `data_source_id`, `name`, `description`, `data_kind`, `freqs`, `status`;
- `freqs` edited as tag input and serialized as `string[]`.

The page must include tabs or drawers for `DatasetColumn` and `DatasetSubject`.

- [ ] **Step 5: Implement DatasetColumn panel**

`dataset-column-panel.vue` must support:

- `ListDatasetColumns(space_id, dataset_id)`;
- `UpsertDatasetColumn`;
- field/factor/system origin selection;
- fields: `column_name`, `origin_type`, `origin_id`, `value_type`, `required`, `is_unique`, `aliases`, `status`.

- [ ] **Step 6: Implement DatasetSubject panel**

`dataset-subject-panel.vue` must support explicit binding only:

- list bindings via `ListDatasetSubjects`;
- bind through `BindDatasetSubject`;
- fields: `subject_id`, `subject_role`, `effective_start_time`, `effective_end_time`, `status`.

No data write or import page may call `BindDatasetSubject` automatically.

- [ ] **Step 7: Implement Field page**

`data/fields/index.vue` must support:

- `ListFields`;
- `CreateField`;
- `UpdateField`;
- fields: `field_id`, `name`, `description`, `value_type`, `unit`, `validation_rule_json`, `write_example`, `status`.

Do not include `dataset_ids`, `table_type`, `parent_field_id`, `value_lib_id`, or `field_format_type`.

- [ ] **Step 8: Implement Factor page**

`data/factors/index.vue` must support:

- `ListFactors`;
- `CreateFactor`;
- `UpdateFactor`;
- fields: `factor_id`, `name`, `description`, `algorithm`, `params_json`, `value_type`, `status`.

- [ ] **Step 9: Implement View page**

`data/views/index.vue` must support:

- `ListViews`;
- `CreateView`;
- `UpdateView`;
- `UpsertViewColumn`;
- `ListViewColumns`;
- `RebuildTimeSeriesView`;
- `RebuildRecordView`.

Main table columns:

```text
view_id, name, engine, primary_dataset_id, view_version, active_view_version, build_status, updated_at, operation
```

The rebuild action must choose TimeSeries or Record rebuild based on the primary dataset `data_kind`.

- [ ] **Step 10: Verify page build**

Run:

```bash
cd /Users/mooyang/Documents/go/src/github.com/mooyang-code/moox/web
pnpm build:prod
```

Expected: PASS.

## Task 8: Rebuild Data Operation Pages

**Files:**

- Modify: `/Users/mooyang/Documents/go/src/github.com/mooyang-code/moox/web/src/views/data/overview/overview.vue`
- Create: `/Users/mooyang/Documents/go/src/github.com/mooyang-code/moox/web/src/views/data/list/index.vue`
- Create: `/Users/mooyang/Documents/go/src/github.com/mooyang-code/moox/web/src/views/data/sync/index.vue`
- Modify: `/Users/mooyang/Documents/go/src/github.com/mooyang-code/moox/web/src/utils/timeSeriesValidator.ts`

- [ ] **Step 1: Rework overview**

Overview should show Space-scoped counts by calling metadata list APIs:

```text
DataSource count
Subject count
Dataset count
Field count
Factor count
View count
PrimaryStoreRoute count
ArchiveFile count
```

Use `page.size=1` if the service returns `page_result.total`; otherwise fetch a bounded `size=200` page and display the returned length with a `200+` indicator when `has_more=true`.

- [ ] **Step 2: Implement data list page**

`data/list/index.vue` must provide two tabs:

- Source of truth:
  - TimeSeries read through `ReadTimeSeriesRows`;
  - Record read through `ReadRecordRows`.
- View query:
  - TimeSeries view through `QueryTimeSeriesRows`;
  - Record view through `SearchRecordRows`.

The page must require selected Space and Dataset/View input before querying.

- [ ] **Step 3: Implement TimeSeries query form**

Fields:

```text
dataset_id
subject_id
freq
dimensions JSON
start_time
end_time
column_names
order
page.size
```

Validate time fields as RFC3339/RFC3339Nano before calling storage.

- [ ] **Step 4: Implement Record query form**

Fields:

```text
dataset_id
record_id
start_version
end_version
column_names
order
page.size
```

For View search, add:

```text
view_id
text_query
filters JSON
sorts JSON
```

- [ ] **Step 5: Implement data sync/import page**

`data/sync/index.vue` should provide:

- local file selector;
- `format` selector with initial values `csv` and `auto`;
- dataset selector;
- subject/freq/time-column fields for TimeSeries import;
- record-id/version-column fields for Record import;
- dry-run button that checks headers against `DatasetColumn`;
- import button that writes rows through `WriteTimeSeriesRows` when `Dataset.data_kind` is TimeSeries;
- import button that writes rows through `WriteRecordRows` when `Dataset.data_kind` is Record.

This page must not create Subject, Dataset, Field, or DatasetSubject metadata implicitly. It may show precise error messages with links to the metadata pages.

- [ ] **Step 6: Update time series validator**

Change legacy frequency parsing from `"1m+5m+1H"` to `string[]`, while keeping a small helper that can parse pasted strings into arrays for form convenience.

- [ ] **Step 7: Verify**

Run:

```bash
cd /Users/mooyang/Documents/go/src/github.com/mooyang-code/moox/web
pnpm build:prod
```

Expected: PASS.

## Task 9: Rebuild Storage Operations Pages Without Device UI

**Files:**

- Create: `/Users/mooyang/Documents/go/src/github.com/mooyang-code/moox/web/src/views/ops/storage/nodes.vue`
- Create: `/Users/mooyang/Documents/go/src/github.com/mooyang-code/moox/web/src/views/ops/storage/routes.vue`
- Create: `/Users/mooyang/Documents/go/src/github.com/mooyang-code/moox/web/src/views/ops/storage/archive.vue`
- Delete after replacement: `/Users/mooyang/Documents/go/src/github.com/mooyang-code/moox/web/src/views/project/storage-config/components/storage-device-config.vue`
- Delete after replacement: `/Users/mooyang/Documents/go/src/github.com/mooyang-code/moox/web/src/views/project/storage-config/components/field-route-config.vue`

- [ ] **Step 1: Implement PrimaryStoreNode page**

Fields:

```text
node_id
name
endpoint
weight
status
config_json
```

Use `CreatePrimaryStoreNode`, `UpdatePrimaryStoreNode`, and `ListPrimaryStoreNodes`.

- [ ] **Step 2: Implement PrimaryStoreRoute page**

Fields:

```text
space_id from selected Space
route_id
dataset_id
subject_id
subject_pattern
hash_rule
node_id
priority
status
```

Use `CreatePrimaryStoreRoute`, `UpdatePrimaryStoreRoute`, and `ListPrimaryStoreRoutes`.

- [ ] **Step 3: Implement ArchiveFile page as read-mostly**

Fields shown:

```text
archive_file_id
dataset_id
partition_key
file_uri
file_format
min_time
max_time
row_count
content_hash
columns
status
created_at
updated_at
```

Use `ListArchiveFiles`. Do not expose `device_id` as a configuration field; if it appears in API response, put it behind a collapsed technical details section only when the user enables debug mode.

- [ ] **Step 4: Remove Device page and links**

Search and remove UI references:

```bash
cd /Users/mooyang/Documents/go/src/github.com/mooyang-code/moox
rg -n "StorageDevice|storage-device|device_type|CreateDevice|UpdateDevice|ListDevices|存储设备" web/src
```

Expected after cleanup: no user-facing page, route, menu, or form for Device.

- [ ] **Step 5: Verify**

Run:

```bash
cd /Users/mooyang/Documents/go/src/github.com/mooyang-code/moox/web
pnpm build:prod
```

Expected: PASS.

## Task 10: Preserve and Space-Prepare Non-Storage Modules

**Files:**

- Modify: `/Users/mooyang/Documents/go/src/github.com/mooyang-code/moox/web/src/views/collector/**`
- Modify: `/Users/mooyang/Documents/go/src/github.com/mooyang-code/moox/web/src/views/container/**`
- Modify: `/Users/mooyang/Documents/go/src/github.com/mooyang-code/moox/web/src/views/strategy/**`
- Modify: `/Users/mooyang/Documents/go/src/github.com/mooyang-code/moox/web/src/views/trading/**`
- Modify: `/Users/mooyang/Documents/go/src/github.com/mooyang-code/moox/web/src/api/cloud-account.ts`
- Modify: `/Users/mooyang/Documents/go/src/github.com/mooyang-code/moox/web/src/api/cloud-node.ts`
- Modify: `/Users/mooyang/Documents/go/src/github.com/mooyang-code/moox/web/src/api/function-package.ts`
- Modify: `/Users/mooyang/Documents/go/src/github.com/mooyang-code/moox/web/src/api/modules/container.ts`

- [ ] **Step 1: Keep non-storage pages mounted**

Routes for container, SSH, cloud function, strategy, and trading pages must continue to resolve after menu refactor.

- [ ] **Step 2: Add optional Space awareness at page boundary**

For each page, read `spaceStore.selectedSpaceId` and display current Space in the page toolbar. The new `admin.sql` already gives Space-scoped admin tables a `c_space_id` column, but each service endpoint should receive `space_id` only after its request contract and query filter are updated.

- [ ] **Step 3: Add API wrapper helpers for future Space injection**

In non-storage API modules, centralize request shaping:

```ts
function withOptionalSpace<T extends Record<string, unknown>>(payload: T, spaceId: string) {
  return spaceId ? { ...payload, space_id: spaceId } : payload;
}
```

Use this helper only on endpoints whose backend already supports `space_id` and filters by `c_space_id`. For strict or not-yet-updated endpoints, show Space in the UI but keep the request unchanged.

- [ ] **Step 4: Verify no accidental page deletion**

Run:

```bash
cd /Users/mooyang/Documents/go/src/github.com/mooyang-code/moox
rg -n "cloud-function|ssh-terminal|strategy-list|account-overview|position-detail|trade-record" web/src/router web/src/mock/_data/system_menu.ts
```

Expected: all listed modules still have route or menu references.

## Task 11: Remove Legacy Storage UI and API Code

**Files:**

- Delete: `/Users/mooyang/Documents/go/src/github.com/mooyang-code/moox/web/src/api/project.ts`
- Delete or repurpose: `/Users/mooyang/Documents/go/src/github.com/mooyang-code/moox/web/src/api/dataset.ts`
- Delete or repurpose: `/Users/mooyang/Documents/go/src/github.com/mooyang-code/moox/web/src/api/field.ts`
- Delete or repurpose: `/Users/mooyang/Documents/go/src/github.com/mooyang-code/moox/web/src/api/storage-config.ts`
- Delete: `/Users/mooyang/Documents/go/src/github.com/mooyang-code/moox/web/src/typings/field-format.ts`
- Delete: `/Users/mooyang/Documents/go/src/github.com/mooyang-code/moox/web/src/typings/field-format.d.ts`
- Delete old Storage pages listed in the file responsibility map after route migration.

- [ ] **Step 1: Remove old API files after all imports are migrated**

Run:

```bash
cd /Users/mooyang/Documents/go/src/github.com/mooyang-code/moox
rg -n "@/api/(project|dataset|field|storage-config)|api/project|api/dataset|api/field|api/storage-config" web/src
```

Expected before deletion: only files being actively migrated. Expected after deletion: no matches.

- [ ] **Step 2: Remove old type files after imports are migrated**

Run:

```bash
cd /Users/mooyang/Documents/go/src/github.com/mooyang-code/moox
rg -n "field-format|field_format_type|primary_format|secondary_format|FieldFormat" web/src
```

Expected after cleanup: no matches.

- [ ] **Step 3: Remove old route/page references**

Run:

```bash
cd /Users/mooyang/Documents/go/src/github.com/mooyang-code/moox
rg -n "projectId|proj_id|CreateDataSet|UpdateDataSet|ObjectRoute|FieldRoute|object-route|field-route|storage-device-config" web/src
```

Expected after cleanup: no matches in active source.

- [ ] **Step 4: Verify build**

Run:

```bash
cd /Users/mooyang/Documents/go/src/github.com/mooyang-code/moox/web
pnpm build:prod
```

Expected: PASS.

## Task 12: End-to-End Manual Verification

**Files:**

- Modify: `/Users/mooyang/Documents/go/src/github.com/mooyang-code/moox/web/README.md`
- Modify: `/Users/mooyang/Documents/go/src/github.com/mooyang-code/moox/web-host/README.md`

- [ ] **Step 1: Start Control service**

Run from the Control module:

```bash
cd /Users/mooyang/Documents/go/src/github.com/mooyang-code/moox/modules/control
go run ./cmd/moox-server
```

Expected: the Control HTTP gateway is listening on `20103`, and `/gateway/space/ListSpaces` is reachable after the Space service is implemented.

- [ ] **Step 2: Start Storage service**

Run from storage module:

```bash
cd /Users/mooyang/Documents/go/src/github.com/mooyang-code/moox/modules/storage
./bin/moox-storage -storage-conf=config/storage.yaml -trpc-conf=config/trpc_go.yaml
```

Expected: HTTP ports `19101`, `19104`, and `19105` are listening.

- [ ] **Step 3: Start frontend in dev mode**

Run:

```bash
cd /Users/mooyang/Documents/go/src/github.com/mooyang-code/moox/web
pnpm dev
```

Expected: Vite dev server starts and proxies `/api/control/*` plus `/api/storage/*`.

- [ ] **Step 4: Verify Space flow**

In browser:

```text
打开管理台
进入 系统设置 > 空间管理
创建 A股交易空间
顶部选择 A股交易空间
刷新页面
确认顶部仍显示 A股交易空间
```

- [ ] **Step 5: Verify metadata flow**

In browser:

```text
创建 DataSource
创建 Subject
创建 Dataset
创建 Field
添加 DatasetColumn
显式绑定 DatasetSubject
创建 View
添加 ViewColumn
触发 RebuildTimeSeriesView 或 RebuildRecordView
```

Expected: Space CRUD goes through Control/Admin APIs; Storage metadata pages use selected `space_id`; no page asks for numeric `projectId`.

- [ ] **Step 6: Verify data flow**

In browser:

```text
进入 数据同步
选择 CSV 文件
执行 dry-run
确认字段名和类型校验通过
执行导入
进入 数据列表
用 ReadTimeSeriesRows 或 ReadRecordRows 查询主存
用 QueryTimeSeriesRows 或 SearchRecordRows 查询 View
```

Expected: import does not create DatasetSubject; query pages return rows after data exists.

- [ ] **Step 7: Verify non-storage modules**

Open:

```text
计算与采集 > 云函数
容器与主机 > SSH 终端
策略管理 > 策略列表
交易管理 > 账户总览
```

Expected: pages still route and render under the selected Space header.

## Task 13: Final Static Checks

**Files:**

- Check: `/Users/mooyang/Documents/go/src/github.com/mooyang-code/moox/modules/control/schema/**`
- Check: `/Users/mooyang/Documents/go/src/github.com/mooyang-code/moox/modules/storage/schema/**`
- Check: `/Users/mooyang/Documents/go/src/github.com/mooyang-code/moox/web/src/**`
- Check: `/Users/mooyang/Documents/go/src/github.com/mooyang-code/moox/web-host/**`

- [ ] **Step 1: Run Control and Storage backend tests**

```bash
cd /Users/mooyang/Documents/go/src/github.com/mooyang-code/moox/modules/control
GOWORK=off go test ./internal/service/space ./tests/schema/...

cd /Users/mooyang/Documents/go/src/github.com/mooyang-code/moox/modules/storage
GOWORK=off go test ./tests/schema/...
```

Expected: PASS.

- [ ] **Step 2: Run web-host tests**

```bash
cd /Users/mooyang/Documents/go/src/github.com/mooyang-code/moox/web-host
GOWORK=off go test ./...
```

Expected: PASS.

- [ ] **Step 3: Run frontend build**

```bash
cd /Users/mooyang/Documents/go/src/github.com/mooyang-code/moox/web
pnpm build:prod
```

Expected: PASS.

- [ ] **Step 4: Run residual scans**

```bash
cd /Users/mooyang/Documents/go/src/github.com/mooyang-code/moox
rg -n "proj_id|projectId|CreateDataSet|UpdateDataSet|field_format_type|ObjectRoute|FieldRoute|StorageDevice|device_type" web/src web-host
```

Expected: no active Storage UI references. Non-storage comments may mention historical words only when they are not executable route or API paths.

Control gateway references are allowed only inside web-host/Vite gateway rewrite code:

```bash
cd /Users/mooyang/Documents/go/src/github.com/mooyang-code/moox
rg -n "/gateway" web/src web-host
```

Expected: matches, if any, are limited to `/api/control/{service}/{method}` rewrite code and gateway tests. Browser-facing application API files must call `/api/control/*` or `/api/storage/*`, not `/gateway/*` directly.

- [ ] **Step 5: Verify schema ownership naming**

```bash
cd /Users/mooyang/Documents/go/src/github.com/mooyang-code/moox
test -f modules/control/schema/admin.sql
test -f modules/storage/schema/metadata.sql
test ! -f modules/storage/schema/admin_console.sql
test ! -f modules/storage/schema/storage_metadata.sql
test ! -d modules/control/migrations
test ! -d modules/storage/migrations
rg -n "storage_metadata\\.sql|admin_console\\.sql|modules/storage/schema/admin|modules/.*/migrations" modules/control modules/storage web web-host docs/superpowers/plans/2026-06-21-moox-web-space-workbench.md
```

Expected: the `test` commands pass. The final `rg` command may only match this checklist and historical docs that explicitly explain removed names; active code/config must not reference old schema paths.

- [ ] **Step 6: Inspect final git diff**

```bash
cd /Users/mooyang/Documents/go/src/github.com/mooyang-code/moox
git status --short
git diff --stat -- modules/control modules/storage web web-host docs/superpowers/plans/2026-06-21-moox-web-space-workbench.md
```

Expected: changes are limited to management console, web-host gateway, Control/Admin schema, Storage metadata schema, and this plan unless a later task explicitly expands backend Space isolation.

## Self-Review Checklist

- Space is a global top-header context, not a left-menu nesting level.
- Control/Admin owns platform-level Space and `modules/control/schema/admin.sql`.
- Storage owns only storage metadata and uses `modules/storage/schema/metadata.sql`.
- The plan has no compatibility layer, no data migration path, and no `migrations/` directory for this pre-release rebuild.
- `modules/storage/schema/admin_console.sql` and `modules/storage/schema/storage_metadata.sql` are removed or renamed in the plan.
- The plan preserves container, SSH, cloud function, strategy, and trading modules.
- The plan keeps hardcoded frontend `app_key` only in one centralized file.
- The plan removes user-facing Device configuration.
- Storage UI maps to current `modules/storage/proto/{metadata,access,view}.proto`.
- Data import does not auto-bind DatasetSubject.
- tRPC HTTP paths use service-qualified paths through web-host or Vite proxy.
- Verification covers web-host tests, frontend build, residual scans, and manual end-to-end flows.
