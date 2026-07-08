# Admin Menu Reorganization Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Reorganize the MooX admin console so "数据采集" owns data collection, dataset, and view workflows, "因子计算" owns factor definition, binding, run, and result workflows, and "数据资产" remains a slim shared dictionary area.

**Architecture:** This is a frontend IA change plus a lightweight metadata-attribution change. Keep Storage schema and APIs intact by using existing `Dataset.attributes` and `View.attributes`, remove over-granular menu entries from the sidebar, add scenario-specific route wrappers, and preserve old `/data/*` URLs with redirects so bookmarks do not break.

**Tech Stack:** Vue 3, Vue Router, Arco Design Vue, TypeScript, Vite, pnpm, Go, tRPC, static system menu fixtures.

---

## Confirmed Product Decisions

- "因子元数据" is not a standalone menu item. Remove `数据资产 / 因子字典（元数据）` from visible navigation.
- `因子计算 / 计算定义` is renamed to `因子计算 / 因子定义`.
- "因子值视图" and "数据预览" are not exposed as two user-facing entries. Users need one `因子结果` entry that shows factor output values through result views.
- `数据资产` keeps only cross-cutting dictionaries: `数据源`, `数据对象`, `字段管理`.
- `数据采集` becomes the main place for `数据集合` and `数据视图`; definition and browsing are merged inside these pages rather than split into five sidebar entries.
- Storage metadata records dataset/view ownership and purpose. Use `attributes.owner_module`, `attributes.dataset_role`, and `attributes.view_role` first; do not add indexed schema columns in this iteration.

## Systems And Modules Affected

### System: Admin Console Frontend (`web/`)

Primary change area.

- Static sidebar menu: `web/src/api/modules/system/static-menu.ts`
- Router: `web/src/router/route.ts`
- Chinese menu labels: `web/src/lang/modules/zhCN.ts`
- English menu labels: `web/src/lang/modules/enUS.ts`
- Menu regression script: `web/scripts/check-menu-structure.mjs`
- Metadata attribution helper: `web/src/views/data/shared/module-attribution.ts`
- Data collection wrapper pages:
  - `web/src/views/collector/datasets/index.vue`
  - `web/src/views/collector/views/index.vue`
- Factor result wrapper page:
  - `web/src/views/factor/results/index.vue`
- Existing reusable pages:
  - `web/src/views/data/datasets/index.vue`
  - `web/src/views/data/browse/index.vue`
  - `web/src/views/data/views/index.vue`
  - `web/src/views/data/view-browse/index.vue`
- Factor run table style fix:
  - `web/src/views/factor/runs/index.vue`

### System: Admin Backend

No expected API or schema change. The admin backend serves auth, space context, and service proxy calls; this menu reorganization should keep using existing endpoints.

### System: Storage Service

No expected API or schema change. The implementation uses the existing `attributes` maps persisted in `c_attrs_json`; document the contract and keep existing metadata and view APIs:

- `ListDatasets`
- `ListViews`
- `ListDatasetColumns`
- `ListViewColumns`
- `QueryTimeSeriesRows`
- `SearchRecordRows`

### System: Factor Service

No expected API change. Factor metadata sync must stamp result datasets with ownership attributes so the admin console can reliably find factor-owned results:

- `ListBindings`
- `ListFactorRuns`
- `MetadataSync.createDataset` in `modules/factor/internal/registry/metadata_sync.go`
- `modules/factor/internal/registry/service_test.go`

### System: Collector Service

No expected API or schema change. Existing collector cloud node, package, rule, and task pages stay available under `数据采集`.

### System: Deployment

Deploy admin/web-host and factor. Storage schema, collector, and cloudnode do not need to be restarted for this change.

---

## Target Navigation

```text
首页
数据资产
  数据源
  数据对象
  字段管理
数据采集
  数据集合
  数据视图
  采集规则
  任务实例
  云节点
  代码包
因子计算
  因子定义
  计算绑定
  运行记录
  因子结果
交易管理
资源与运维
系统设置
```

Old visible entries removed from the sidebar:

- `数据资产 / 数据建模`
- `数据资产 / 数据管理`
- `数据资产 / 查询视图`
- `数据资产 / 因子字典（元数据）`
- standalone `数据概览`
- standalone `数据浏览`
- standalone `视图列表`
- standalone `视图浏览`

Old routes remain as hidden redirects:

- `/data/datasets` -> `/collector/datasets?tab=definitions`
- `/data/browse` -> `/collector/datasets?tab=browse`
- `/data/overview` -> `/collector/datasets`
- `/data/views` -> `/collector/views?tab=definitions`
- `/data/view-browse` -> `/collector/views?tab=browse`
- `/data/factors` -> `/factor/definitions`

---

## Storage Metadata Attribution Contract

Do not teach Storage about menu names. Storage only records ownership and purpose; the admin console translates those attributes into menu placement.

### Dataset Attributes

```text
owner_module = collector | factor | trade | manual | system
dataset_role = raw_collection | factor_result | import | analysis | manual
managed_by = collector_rule_id:<id> | factor_binding_id:<id> | manual | system
source_dataset_id = <dataset_id>
source_freq = <freq>
```

Required writes in this iteration:

- Datasets created from `数据采集 / 数据集合`: `owner_module=collector`, `dataset_role=raw_collection`.
- Factor result datasets created by factor metadata sync: `owner_module=factor`, `dataset_role=factor_result`, `managed_by=factor`, `source_dataset_id=<source_dataset>`, `source_freq=<freq>`.
- Legacy datasets with no `owner_module` remain visible in `数据采集 / 数据集合` to avoid hiding existing production data.

### View Attributes

```text
owner_module = collector | factor | manual | system
view_role = collection_browse | factor_result | analysis | manual
managed_by = collector_rule_id:<id> | factor_binding_id:<id> | manual | system
primary_dataset_role = raw_collection | factor_result | import | analysis | manual
```

Required writes in this iteration:

- Views created from `数据采集 / 数据视图`: `owner_module=collector`, `view_role=collection_browse`.
- Views created from factor result setup flows: `owner_module=factor`, `view_role=factor_result`.
- `因子结果` shows views with `view_role=factor_result` and also legacy views whose `primary_dataset_id` is a factor binding target dataset.

---

## Task 1: Strengthen Menu Structure Regression Test

**Files:**

- Modify: `web/scripts/check-menu-structure.mjs`

- [ ] **Step 1: Replace the current assertions with the target IA assertions**

Use this full script body:

```js
import fs from 'node:fs';
import path from 'node:path';
import { fileURLToPath } from 'node:url';

const scriptDir = path.dirname(fileURLToPath(import.meta.url));
const root = path.resolve(scriptDir, '..');
const staticMenu = fs.readFileSync(path.join(root, 'src/api/modules/system/static-menu.ts'), 'utf8');
const zhCN = fs.readFileSync(path.join(root, 'src/lang/modules/zhCN.ts'), 'utf8');

function assert(condition, message) {
  if (!condition) {
    throw new Error(message);
  }
}

function findDirectory(name) {
  const pattern = new RegExp(
    String.raw`directory\("([^"]+)",\s*"([^"]+)",\s*"([^"]+)",\s*"${name}",\s*"([^"]+)",\s*(\d+)`,
  );
  const match = staticMenu.match(pattern);
  assert(match, `directory ${name} not found`);
  return {
    id: match[1],
    parentId: match[2],
    path: match[3],
    title: match[4],
    sort: Number(match[5]),
  };
}

function findMenu(name) {
  const pattern = new RegExp(
    String.raw`menu\("([^"]+)",\s*"([^"]+)",\s*"([^"]+)",\s*"${name}",\s*"([^"]+)",\s*"([^"]+)",\s*(\d+)`,
  );
  const match = staticMenu.match(pattern);
  assert(match, `menu ${name} not found`);
  return {
    id: match[1],
    parentId: match[2],
    path: match[3],
    title: match[4],
    component: match[5],
    sort: Number(match[6]),
  };
}

function assertNotVisible(name) {
  assert(!staticMenu.includes(`"${name}", "${name}"`), `${name} must not be a visible static-menu entry`);
}

const dataAssets = findDirectory('data-assets');
const dataCollection = findDirectory('compute-collector');
const factorCompute = findDirectory('factor-compute');
const trading = findDirectory('trading');

assert(zhCN.includes('["compute-collector"]: "数据采集"'), 'compute-collector zh-CN label must be 数据采集');
assert(zhCN.includes('["factor-definitions"]: "因子定义"'), 'factor-definitions zh-CN label must be 因子定义');
assert(zhCN.includes('["collector-datasets"]: "数据集合"'), 'collector-datasets zh-CN label must be 数据集合');
assert(zhCN.includes('["collector-views"]: "数据视图"'), 'collector-views zh-CN label must be 数据视图');
assert(zhCN.includes('["factor-results"]: "因子结果"'), 'factor-results zh-CN label must be 因子结果');

assert(dataAssets.parentId === '0', 'data-assets must remain a root menu');
assert(dataAssets.path === '/data/sources', 'data-assets default path must be /data/sources');
assert(dataCollection.parentId === '0', 'compute-collector must be a root menu');
assert(dataCollection.path === '/collector/datasets', 'compute-collector default path must be /collector/datasets');
assert(factorCompute.parentId === '0', 'factor-compute must be a root menu');
assert(factorCompute.sort > dataCollection.sort, 'factor-compute must appear after data collection');
assert(factorCompute.sort < trading.sort, 'factor-compute must appear before trading');

const dataSources = findMenu('data-sources');
const dataSubjects = findMenu('data-subjects');
const dataFields = findMenu('data-fields');
assert(dataSources.parentId === dataAssets.id, 'data-sources must be under data-assets');
assert(dataSubjects.parentId === dataAssets.id, 'data-subjects must be under data-assets');
assert(dataFields.parentId === dataAssets.id, 'data-fields must be under data-assets');

const collectorDatasets = findMenu('collector-datasets');
const collectorViews = findMenu('collector-views');
const collectorRules = findMenu('collector-rules');
const collectorTasks = findMenu('collector-tasks');
const collectorCloudnodes = findMenu('collector-cloudnodes');
const collectorPackages = findMenu('collector-packages');
assert(collectorDatasets.parentId === dataCollection.id, 'collector-datasets must be under data collection');
assert(collectorViews.parentId === dataCollection.id, 'collector-views must be under data collection');
assert(collectorRules.parentId === dataCollection.id, 'collector-rules must be under data collection');
assert(collectorTasks.parentId === dataCollection.id, 'collector-tasks must be under data collection');
assert(collectorCloudnodes.parentId === dataCollection.id, 'collector-cloudnodes must be under data collection');
assert(collectorPackages.parentId === dataCollection.id, 'collector-packages must be under data collection');
assert(collectorDatasets.sort < collectorViews.sort, 'data collections must appear before data views');
assert(collectorViews.sort < collectorRules.sort, 'data views must appear before collection rules');

const factorDefinitions = findMenu('factor-definitions');
const factorBindings = findMenu('factor-bindings');
const factorRuns = findMenu('factor-runs');
const factorResults = findMenu('factor-results');
assert(factorDefinitions.parentId === factorCompute.id, 'factor-definitions must be under factor compute');
assert(factorBindings.parentId === factorCompute.id, 'factor-bindings must be under factor compute');
assert(factorRuns.parentId === factorCompute.id, 'factor-runs must be under factor compute');
assert(factorResults.parentId === factorCompute.id, 'factor-results must be under factor compute');
assert(factorRuns.sort < factorResults.sort, 'factor results must appear after run records');

assertNotVisible('data-modeling');
assertNotVisible('data-mgmt');
assertNotVisible('data-views');
assertNotVisible('data-factors');
assertNotVisible('data-overview');
assertNotVisible('data-browse');
assertNotVisible('data-view-list');
assertNotVisible('data-view-browse');

console.log('menu structure ok');
```

- [ ] **Step 2: Run the menu check and verify it fails before implementation**

Run:

```bash
cd web && pnpm check:menu
```

Expected before implementation: failure mentioning `collector-datasets` or `factor-definitions` label.

- [ ] **Step 3: Commit the failing guard if working with strict TDD**

```bash
git add web/scripts/check-menu-structure.mjs
git commit -m "test(admin): capture target menu organization"
```

If implementation is done in one batch, skip this intermediate commit and include the script in the final admin menu commit.

---

## Task 1A: Define Metadata Attribution Helpers

**Files:**

- Create: `web/src/views/data/shared/module-attribution.ts`

- [ ] **Step 1: Create the shared attribution helper**

Create `web/src/views/data/shared/module-attribution.ts`:

```ts
import type { Dataset, View } from '@/api/storage/types';

export const ownerModules = ['collector', 'factor', 'trade', 'manual', 'system'] as const;
export type OwnerModule = (typeof ownerModules)[number];

export const datasetRoles = ['raw_collection', 'factor_result', 'import', 'analysis', 'manual'] as const;
export type DatasetRole = (typeof datasetRoles)[number];

export const viewRoles = ['collection_browse', 'factor_result', 'analysis', 'manual'] as const;
export type ViewRole = (typeof viewRoles)[number];

export interface DatasetAttributionInput {
  ownerModule?: OwnerModule;
  datasetRole?: DatasetRole;
  managedBy?: string;
  sourceDatasetId?: string;
  sourceFreq?: string;
}

export interface ViewAttributionInput {
  ownerModule?: OwnerModule;
  viewRole?: ViewRole;
  managedBy?: string;
  primaryDatasetRole?: DatasetRole;
}

export interface AttributionFilter {
  ownerModules?: OwnerModule[];
  datasetRoles?: DatasetRole[];
  viewRoles?: ViewRole[];
  includeUnowned?: boolean;
}

function clean(value?: string) {
  return (value || '').trim();
}

function putIfPresent(target: Record<string, string>, key: string, value?: string) {
  const normalized = clean(value);
  if (normalized) {
    target[key] = normalized;
  }
}

function includes<T extends string>(values: readonly T[] | undefined, value: string) {
  return !values || values.length === 0 || values.includes(value as T);
}

export function mergeDatasetAttribution(
  attributes: Record<string, string> | undefined,
  input: DatasetAttributionInput,
) {
  const next = { ...(attributes || {}) };
  putIfPresent(next, 'owner_module', input.ownerModule);
  putIfPresent(next, 'dataset_role', input.datasetRole);
  putIfPresent(next, 'managed_by', input.managedBy);
  putIfPresent(next, 'source_dataset_id', input.sourceDatasetId);
  putIfPresent(next, 'source_freq', input.sourceFreq);
  return next;
}

export function mergeViewAttribution(
  attributes: Record<string, string> | undefined,
  input: ViewAttributionInput,
) {
  const next = { ...(attributes || {}) };
  putIfPresent(next, 'owner_module', input.ownerModule);
  putIfPresent(next, 'view_role', input.viewRole);
  putIfPresent(next, 'managed_by', input.managedBy);
  putIfPresent(next, 'primary_dataset_role', input.primaryDatasetRole);
  return next;
}

export function datasetMatchesAttribution(dataset: Dataset, filter: AttributionFilter) {
  const attrs = dataset.attributes || {};
  const owner = clean(attrs.owner_module);
  const role = clean(attrs.dataset_role);
  if (!owner && filter.includeUnowned) {
    return true;
  }
  return includes(filter.ownerModules, owner) && includes(filter.datasetRoles, role);
}

export function viewMatchesAttribution(view: View, filter: AttributionFilter) {
  const attrs = view.attributes || {};
  const owner = clean(attrs.owner_module);
  const role = clean(attrs.view_role);
  if (!owner && filter.includeUnowned) {
    return true;
  }
  return includes(filter.ownerModules, owner) && includes(filter.viewRoles, role);
}
```

- [ ] **Step 2: Verify TypeScript can import the helper**

Run:

```bash
cd web && pnpm build:prod
```

Expected at this point: the helper itself has no TypeScript errors. Build may still fail later if route wrapper files from later tasks are not created yet.

---

## Task 2: Update Menu Labels

**Files:**

- Modify: `web/src/lang/modules/zhCN.ts`
- Modify: `web/src/lang/modules/enUS.ts`

- [ ] **Step 1: Update Chinese labels**

In `web/src/lang/modules/zhCN.ts`, keep old keys for hidden redirects, but change/add these menu entries:

```ts
["data-assets"]: "数据资产",
["data-sources"]: "数据源",
["data-subjects"]: "数据对象",
["data-fields"]: "字段管理",
["compute-collector"]: "数据采集",
["collector-datasets"]: "数据集合",
["collector-views"]: "数据视图",
["collector-rules"]: "采集规则",
["collector-tasks"]: "任务实例",
["collector-cloudnodes"]: "云节点",
["collector-functions"]: "云节点",
["collector-packages"]: "代码包",
["factor-compute"]: "因子计算",
["factor-definitions"]: "因子定义",
["factor-bindings"]: "计算绑定",
["factor-runs"]: "运行记录",
["factor-results"]: "因子结果",
```

Do not delete old keys such as `data-overview`, `data-browse`, `data-view-list`, and `data-view-browse`; hidden routes can still use them in tabs and redirects.

- [ ] **Step 2: Update English labels**

In `web/src/lang/modules/enUS.ts`, keep old keys for hidden redirects, but change/add these menu entries:

```ts
["data-assets"]: "data assets",
["data-sources"]: "data sources",
["data-subjects"]: "subjects",
["data-fields"]: "fields",
["compute-collector"]: "data collection",
["collector-datasets"]: "data collections",
["collector-views"]: "data views",
["collector-rules"]: "collection rules",
["collector-tasks"]: "task instances",
["collector-cloudnodes"]: "cloud nodes",
["collector-functions"]: "cloud nodes",
["collector-packages"]: "code packages",
["factor-compute"]: "factor compute",
["factor-definitions"]: "factor definitions",
["factor-bindings"]: "bindings",
["factor-runs"]: "runs",
["factor-results"]: "factor results",
```

- [ ] **Step 3: Run the menu check**

Run:

```bash
cd web && pnpm check:menu
```

Expected after this task only: still failing because static menu entries and routes are not moved yet.

---

## Task 3: Rebuild Static Sidebar Menu

**Files:**

- Modify: `web/src/api/modules/system/static-menu.ts`

- [ ] **Step 1: Replace the visible `systemMenu` data area with the target structure**

Keep helper functions unchanged. Replace only the `export const systemMenu = [...]` section with:

```ts
export const systemMenu = [
  menu("01", "0", "/home", "home", "home", "home/home", 1, { affix: true, svgIcon: "home", icon: "" }),

  directory("02", "0", "/data/sources", "data-assets", "data-assets", 2, { svgIcon: "folder-menu", icon: "" }),
  menu("0201", "02", "/data/sources", "data-sources", "data-sources", "data/sources/index", 1),
  menu("0202", "02", "/data/subjects", "data-subjects", "data-subjects", "data/subjects/index", 2),
  menu("0203", "02", "/data/fields", "data-fields", "data-fields", "data/fields/index", 3),

  directory("03", "0", "/collector/datasets", "compute-collector", "compute-collector", 3, { svgIcon: "functions", icon: "" }),
  menu("0305", "03", "/collector/datasets", "collector-datasets", "collector-datasets", "collector/datasets/index", 1),
  menu("0306", "03", "/collector/views", "collector-views", "collector-views", "collector/views/index", 2),
  menu("0303", "03", "/collector/rules", "collector-rules", "collector-rules", "collector/collector-rules/collector-rules", 3),
  menu("0304", "03", "/collector/tasks", "collector-tasks", "collector-tasks", "collector/task-instances/task-instances", 4),
  menu("0301", "03", "/collector/cloudnodes", "collector-cloudnodes", "collector-cloudnodes", "collector/cloud-node/cloud-node", 5),
  menu("0302", "03", "/collector/packages", "collector-packages", "collector-packages", "collector/cloud-node/function-package-manage", 6),

  directory("0240", "0", "/factor/definitions", "factor-compute", "factor-compute", 4, { svgIcon: "functions", icon: "" }),
  menu("024001", "0240", "/factor/definitions", "factor-definitions", "factor-definitions", "factor/definitions/index", 1),
  menu("024002", "0240", "/factor/bindings", "factor-bindings", "factor-bindings", "factor/bindings/index", 2),
  menu("024003", "0240", "/factor/runs", "factor-runs", "factor-runs", "factor/runs/index", 3),
  menu("024004", "0240", "/factor/results", "factor-results", "factor-results", "factor/results/index", 4),

  directory("05", "0", "/trading/accounts", "trading", "trading", 5, { svgIcon: "balance-inquiry", icon: "" }),
  menu("0501", "05", "/trading/accounts", "trading-accounts", "trading-accounts", "trading/account-overview/account-overview", 1),
  menu("0502", "05", "/trading/positions", "trading-positions", "trading-positions", "trading/position-detail/position-detail", 2),
  menu("0503", "05", "/trading/orders", "trading-orders", "trading-orders", "trading/trade-record/trade-record", 3),

  directory("06", "0", "/ops/resource-monitor", "ops", "ops", 6, { svgIcon: "defend", icon: "" }),
  menu("0601", "06", "/ops/resource-monitor", "ops-resource-monitor", "ops-resource-monitor", "container/resource-monitor/resource-monitor", 1),
  menu("0603", "06", "/ops/ssh-hosts", "ops-ssh-hosts", "ops-ssh-hosts", "container/ssh-hosts/ssh-hosts", 3),
  menu("0604", "06", "/ops/ssh-terminal", "ops-ssh-terminal", "ops-ssh-terminal", "container/ssh-terminal/ssh-terminal", 4, { keepAlive: false }),
  menu("0605", "06", "/ops/ssh-sessions", "ops-ssh-sessions", "ops-ssh-sessions", "container/ssh-sessions/ssh-sessions", 5),
  directory("0606", "06", "/ops/storage/nodes", "ops-storage", "ops-storage", 6),
  menu("060601", "0606", "/ops/storage/nodes", "ops-storage-nodes", "ops-storage-nodes", "ops/storage/nodes", 1),
  menu("060602", "0606", "/ops/storage/routes", "ops-storage-routes", "ops-storage-routes", "ops/storage/routes", 2),
  menu("060603", "0606", "/ops/storage/archive", "ops-storage-archive", "ops-storage-archive", "ops/storage/archive", 3),

  directory("07", "0", "/settings/spaces", "settings", "settings", 7, { svgIcon: "set", icon: "" }),
  menu("0701", "07", "/settings/spaces", "settings-spaces", "settings-spaces", "settings/spaces/index", 1),
  menu("0702", "07", "/settings/secrets", "settings-secrets", "settings-secrets", "settings/secrets/index", 2),
  menu("0703", "07", "/settings/service-deployments", "settings-service-deployments", "settings-service-deployments", "settings/service-deployments/index", 3)
];
```

- [ ] **Step 2: Run the menu check**

Run:

```bash
cd web && pnpm check:menu
```

Expected after Task 2 and Task 3: pass if route files have not been compiled yet; Vite build will still fail until wrapper pages and routes exist.

---

## Task 4: Add Scenario Routes And Preserve Old URLs

**Files:**

- Modify: `web/src/router/route.ts`

- [ ] **Step 1: Add new visible routes under `/collector` and `/factor`**

Inside the `children` array, add these route records near the existing collector and factor routes:

```ts
{
  path: "/collector/datasets",
  name: "collector-datasets",
  component: () => import("@/views/collector/datasets/index.vue"),
  meta: { title: "collector-datasets" }
},
{
  path: "/collector/views",
  name: "collector-views",
  component: () => import("@/views/collector/views/index.vue"),
  meta: { title: "collector-views" }
},
{
  path: "/factor/results",
  name: "factor-results",
  component: () => import("@/views/factor/results/index.vue"),
  meta: { title: "factor-results" }
},
```

- [ ] **Step 2: Convert old data modeling routes into hidden redirects**

Replace the existing `/data/datasets`, `/data/factors`, `/data/views`, `/data/view-browse`, `/data/overview`, and `/data/browse` route records with:

```ts
{
  path: "/data/datasets",
  redirect: { path: "/collector/datasets", query: { tab: "definitions" } },
  meta: { title: "collector-datasets", hide: true }
},
{
  path: "/data/factors",
  redirect: "/factor/definitions",
  meta: { title: "factor-definitions", hide: true }
},
{
  path: "/data/views",
  redirect: { path: "/collector/views", query: { tab: "definitions" } },
  meta: { title: "collector-views", hide: true }
},
{
  path: "/data/view-browse",
  redirect: { path: "/collector/views", query: { tab: "browse" } },
  meta: { title: "collector-views", hide: true }
},
{
  path: "/data/overview",
  redirect: "/collector/datasets",
  meta: { title: "collector-datasets", hide: true }
},
{
  path: "/data/list",
  redirect: { path: "/collector/datasets", query: { tab: "browse" } },
  meta: { title: "collector-datasets", hide: true }
},
{
  path: "/data/browse",
  redirect: { path: "/collector/datasets", query: { tab: "browse" } },
  meta: { title: "collector-datasets", hide: true }
},
```

- [ ] **Step 3: Keep dictionary routes unchanged**

Do not redirect these routes:

```ts
"/data/sources"
"/data/subjects"
"/data/fields"
```

They remain under `数据资产`.

---

## Task 4A: Make Data Pages Attribution-Aware

**Files:**

- Modify: `web/src/views/data/datasets/index.vue`
- Modify: `web/src/views/data/browse/index.vue`
- Modify: `web/src/views/data/views/index.vue`
- Modify: `web/src/views/data/view-browse/index.vue`

- [ ] **Step 1: Add attribution props to dataset definition page**

In `web/src/views/data/datasets/index.vue`, import the helper:

```ts
import {
  datasetMatchesAttribution,
  mergeDatasetAttribution,
  type DatasetRole,
  type OwnerModule,
} from '@/views/data/shared/module-attribution';
```

After `defineOptions({ name: 'DataDatasets' });`, add:

```ts
const props = withDefaults(defineProps<{
  ownerModule?: OwnerModule;
  datasetRole?: DatasetRole;
  filterOwnerModules?: OwnerModule[];
  filterDatasetRoles?: DatasetRole[];
  includeUnowned?: boolean;
  managedBy?: string;
}>(), {
  ownerModule: undefined,
  datasetRole: undefined,
  filterOwnerModules: undefined,
  filterDatasetRoles: undefined,
  includeUnowned: false,
  managedBy: undefined,
});
```

After `const rows = ref<Dataset[]>([]);`, add:

```ts
const visibleRows = computed(() =>
  rows.value.filter((item) =>
    datasetMatchesAttribution(item, {
      ownerModules: props.filterOwnerModules,
      datasetRoles: props.filterDatasetRoles,
      includeUnowned: props.includeUnowned,
    }),
  ),
);
```

Change the dataset table binding from:

```vue
:data="rows"
```

to:

```vue
:data="visibleRows"
```

Add `attributes: {}` to the initial `form` object and to `resetForm()`. In `submit()`, add attributes to the `payload`:

```ts
attributes: mergeDatasetAttribution(form.attributes, {
  ownerModule: props.ownerModule,
  datasetRole: props.datasetRole,
  managedBy: props.managedBy,
}),
```

- [ ] **Step 2: Add attribution filtering to dataset browse page**

In `web/src/views/data/browse/index.vue`, import:

```ts
import {
  datasetMatchesAttribution,
  type DatasetRole,
  type OwnerModule,
} from '@/views/data/shared/module-attribution';
```

After `defineOptions(...)`, add:

```ts
const props = withDefaults(defineProps<{
  datasetOwnerModules?: OwnerModule[];
  datasetRoles?: DatasetRole[];
  includeUnowned?: boolean;
}>(), {
  datasetOwnerModules: undefined,
  datasetRoles: undefined,
  includeUnowned: false,
});
```

After `const datasets = ref<Dataset[]>([]);`, add:

```ts
const visibleDatasets = computed(() =>
  datasets.value.filter((item) =>
    datasetMatchesAttribution(item, {
      ownerModules: props.datasetOwnerModules,
      datasetRoles: props.datasetRoles,
      includeUnowned: props.includeUnowned,
    }),
  ),
);
```

Replace UI and active-dataset logic that reads `datasets` for display/selection with `visibleDatasets`:

```vue
<a-empty v-if="visibleDatasets.length === 0" description="暂无数据集" />
<a-tab-pane v-for="dataset in visibleDatasets" :key="dataset.dataset_id" :title="datasetDisplayName(dataset)" />
```

```ts
const currentDataset = computed(() => visibleDatasets.value.find((item) => item.dataset_id === activeDatasetId.value));

if (!visibleDatasets.value.length) {
  activeDatasetId.value = '';
  return;
}
if (!activeDatasetId.value || !visibleDatasets.value.some((item) => item.dataset_id === activeDatasetId.value)) {
  activeDatasetId.value = visibleDatasets.value[0].dataset_id;
}
```

- [ ] **Step 3: Add attribution props to view definition page**

In `web/src/views/data/views/index.vue`, import:

```ts
import {
  mergeViewAttribution,
  viewMatchesAttribution,
  type DatasetRole,
  type OwnerModule,
  type ViewRole,
} from '@/views/data/shared/module-attribution';
```

After `defineOptions({ name: "DataViews" });`, add:

```ts
const props = withDefaults(defineProps<{
  ownerModule?: OwnerModule;
  viewRole?: ViewRole;
  filterOwnerModules?: OwnerModule[];
  filterViewRoles?: ViewRole[];
  includeUnowned?: boolean;
  managedBy?: string;
}>(), {
  ownerModule: undefined,
  viewRole: undefined,
  filterOwnerModules: undefined,
  filterViewRoles: undefined,
  includeUnowned: false,
  managedBy: undefined,
});
```

After `const rows = ref<View[]>([]);`, add:

```ts
const visibleRows = computed(() =>
  rows.value.filter((item) =>
    viewMatchesAttribution(item, {
      ownerModules: props.filterOwnerModules,
      viewRoles: props.filterViewRoles,
      includeUnowned: props.includeUnowned,
    }),
  ),
);
```

Change the view table binding from `:data="rows"` to `:data="visibleRows"`.

In `submit()`, add attributes to the `payload`:

```ts
attributes: mergeViewAttribution(form.attributes, {
  ownerModule: props.ownerModule,
  viewRole: props.viewRole,
  managedBy: props.managedBy,
  primaryDatasetRole: primaryDataset.value?.attributes?.dataset_role as DatasetRole | undefined,
}),
```

- [ ] **Step 4: Add attribution filtering to view browse page**

In `web/src/views/data/view-browse/index.vue`, add these attribution props:

```ts
viewOwnerModules?: OwnerModule[];
viewRoles?: ViewRole[];
includeUnowned?: boolean;
```

Import and use `viewMatchesAttribution`. The `visibleViews` computed must include a view if either condition is true:

```ts
const visibleViews = computed(() => {
  const allowedPrimaryDatasetIds = props.allowedPrimaryDatasetIds || [];
  const allowedPrimary = new Set(allowedPrimaryDatasetIds.filter(Boolean));
  return views.value.filter((view) => {
    const matchedByDataset = allowedPrimary.size > 0 && allowedPrimary.has(view.primary_dataset_id);
    const matchedByAttrs = viewMatchesAttribution(view, {
      ownerModules: props.viewOwnerModules,
      viewRoles: props.viewRoles,
      includeUnowned: props.includeUnowned,
    });
    if (allowedPrimary.size > 0 && (props.viewOwnerModules?.length || props.viewRoles?.length)) {
      return matchedByDataset || matchedByAttrs;
    }
    if (allowedPrimary.size > 0) {
      return matchedByDataset;
    }
    return matchedByAttrs;
  });
});
```

- [ ] **Step 5: Build after attribution-aware page changes**

Run:

```bash
cd web && pnpm build:prod
```

Expected: TypeScript succeeds with the new props and helper imports.

---

## Task 5: Create Data Collection Workbench Pages

**Files:**

- Create: `web/src/views/collector/datasets/index.vue`
- Create: `web/src/views/collector/views/index.vue`

- [ ] **Step 1: Create `数据集合` wrapper**

Create `web/src/views/collector/datasets/index.vue`:

```vue
<template>
  <div class="collector-workbench">
    <a-tabs v-model:active-key="activeTab" type="rounded" size="medium" @change="syncRoute">
      <a-tab-pane key="definitions" title="集合定义">
        <DatasetDefinitions
          owner-module="collector"
          dataset-role="raw_collection"
          managed-by="manual"
          :filter-owner-modules="['collector']"
          :filter-dataset-roles="['raw_collection', 'import']"
          :include-unowned="true"
        />
      </a-tab-pane>
      <a-tab-pane key="browse" title="查看数据">
        <DatasetBrowse
          :dataset-owner-modules="['collector']"
          :dataset-roles="['raw_collection', 'import']"
          :include-unowned="true"
        />
      </a-tab-pane>
    </a-tabs>
  </div>
</template>

<script setup lang="ts">
import { computed, ref, watch } from 'vue';
import { useRoute, useRouter } from 'vue-router';
import DatasetDefinitions from '@/views/data/datasets/index.vue';
import DatasetBrowse from '@/views/data/browse/index.vue';

defineOptions({ name: 'CollectorDatasets' });

const route = useRoute();
const router = useRouter();
const activeTab = ref(tabFromRoute());

const normalizedQuery = computed(() => String(route.query.tab || ''));

function tabFromRoute() {
  return route.query.tab === 'browse' ? 'browse' : 'definitions';
}

function syncRoute(key: string | number) {
  const tab = key === 'browse' ? 'browse' : 'definitions';
  router.replace({ path: '/collector/datasets', query: tab === 'browse' ? { tab } : {} });
}

watch(normalizedQuery, () => {
  activeTab.value = tabFromRoute();
});
</script>

<style scoped>
.collector-workbench {
  min-height: 0;
}

.collector-workbench :deep(.arco-tabs-content) {
  padding-top: 0;
}
</style>
```

- [ ] **Step 2: Create `数据视图` wrapper**

Create `web/src/views/collector/views/index.vue`:

```vue
<template>
  <div class="collector-workbench">
    <a-tabs v-model:active-key="activeTab" type="rounded" size="medium" @change="syncRoute">
      <a-tab-pane key="definitions" title="视图定义">
        <ViewDefinitions
          owner-module="collector"
          view-role="collection_browse"
          managed-by="manual"
          :filter-owner-modules="['collector']"
          :filter-view-roles="['collection_browse', 'analysis']"
          :include-unowned="true"
        />
      </a-tab-pane>
      <a-tab-pane key="browse" title="查看数据">
        <ViewBrowse
          page-title="数据视图"
          empty-description="暂无数据视图"
          :view-owner-modules="['collector']"
          :view-roles="['collection_browse', 'analysis']"
          :include-unowned="true"
        />
      </a-tab-pane>
    </a-tabs>
  </div>
</template>

<script setup lang="ts">
import { computed, ref, watch } from 'vue';
import { useRoute, useRouter } from 'vue-router';
import ViewDefinitions from '@/views/data/views/index.vue';
import ViewBrowse from '@/views/data/view-browse/index.vue';

defineOptions({ name: 'CollectorViews' });

const route = useRoute();
const router = useRouter();
const activeTab = ref(tabFromRoute());

const normalizedQuery = computed(() => String(route.query.tab || ''));

function tabFromRoute() {
  return route.query.tab === 'browse' ? 'browse' : 'definitions';
}

function syncRoute(key: string | number) {
  const tab = key === 'browse' ? 'browse' : 'definitions';
  router.replace({ path: '/collector/views', query: tab === 'browse' ? { tab } : {} });
}

watch(normalizedQuery, () => {
  activeTab.value = tabFromRoute();
});
</script>

<style scoped>
.collector-workbench {
  min-height: 0;
}

.collector-workbench :deep(.arco-tabs-content) {
  padding-top: 0;
}
</style>
```

- [ ] **Step 3: Verify route wrappers compile**

Run:

```bash
cd web && pnpm build:prod
```

Expected after this task and before Task 6: build may still fail because `factor/results/index.vue` does not exist yet. No TypeScript errors should mention the two collector wrapper files.

---

## Task 6: Add Unified Factor Results Page

**Files:**

- Create: `web/src/views/factor/results/index.vue`
- Modify: `web/src/views/data/view-browse/index.vue`

- [ ] **Step 1: Make view browsing reusable for factor result filtering**

In `web/src/views/data/view-browse/index.vue`, import attribution helpers:

```ts
import {
  viewMatchesAttribution,
  type OwnerModule,
  type ViewRole,
} from '@/views/data/shared/module-attribution';
```

Then add props near the top of `<script setup>`:

```ts
const props = withDefaults(defineProps<{
  pageTitle?: string;
  emptyDescription?: string;
  allowedPrimaryDatasetIds?: string[];
  viewOwnerModules?: OwnerModule[];
  viewRoles?: ViewRole[];
  includeUnowned?: boolean;
}>(), {
  pageTitle: '视图数据浏览',
  emptyDescription: '暂无查询视图',
  allowedPrimaryDatasetIds: undefined,
  viewOwnerModules: undefined,
  viewRoles: undefined,
  includeUnowned: false,
});
```

Add this computed value after `const datasets = ref<Dataset[]>([]);` and before `const activeView = computed(...)`:

```ts
const visibleViews = computed(() => {
  const allowedPrimaryDatasetIds = props.allowedPrimaryDatasetIds || [];
  const allowedPrimary = new Set(allowedPrimaryDatasetIds.filter(Boolean));
  return views.value.filter((view) => {
    const matchedByDataset = allowedPrimary.size > 0 && allowedPrimary.has(view.primary_dataset_id);
    const matchedByAttrs = viewMatchesAttribution(view, {
      ownerModules: props.viewOwnerModules,
      viewRoles: props.viewRoles,
      includeUnowned: props.includeUnowned,
    });
    if (allowedPrimary.size > 0 && (props.viewOwnerModules?.length || props.viewRoles?.length)) {
      return matchedByDataset || matchedByAttrs;
    }
    if (allowedPrimary.size > 0) {
      return matchedByDataset;
    }
    return matchedByAttrs;
  });
});
```

Update existing uses that choose or render available views:

```vue
<h2>{{ props.pageTitle }}</h2>
<a-empty v-if="visibleViews.length === 0" :description="props.emptyDescription" />
<a-tab-pane v-for="view in visibleViews" :key="view.view_id" :title="viewDisplayName(view)" />
```

Update active view lookup:

```ts
const activeView = computed(() => visibleViews.value.find((item) => item.view_id === activeViewId.value));
```

Update active-view reset logic inside the existing loading flow:

```ts
if (!visibleViews.value.length) {
  activeViewId.value = '';
  return;
}
if (!activeViewId.value || !visibleViews.value.some((item) => item.view_id === activeViewId.value)) {
  activeViewId.value = visibleViews.value[0].view_id;
}
```

- [ ] **Step 2: Create `因子结果` wrapper**

Create `web/src/views/factor/results/index.vue`:

```vue
<template>
  <ViewBrowse
    page-title="因子结果"
    empty-description="暂无因子结果视图，请先确认因子绑定的目标数据集已创建对应视图"
    :allowed-primary-dataset-ids="targetDatasetIds"
    :view-owner-modules="['factor']"
    :view-roles="['factor_result']"
  />
</template>

<script setup lang="ts">
import { computed, onMounted, ref, watch } from 'vue';
import { listFactorBindings } from '@/api/factor';
import type { FactorBinding } from '@/api/factor/types';
import { useSpaceStore } from '@/store/modules/space';
import ViewBrowse from '@/views/data/view-browse/index.vue';

defineOptions({ name: 'FactorResults' });

const spaceStore = useSpaceStore();
const selectedSpaceId = computed(() => spaceStore.selectedSpaceId);
const bindings = ref<FactorBinding[]>([]);

const targetDatasetIds = computed(() => {
  return Array.from(new Set(bindings.value.map((item) => item.target_dataset).filter(Boolean)));
});

async function loadBindings() {
  if (!selectedSpaceId.value) {
    bindings.value = [];
    return;
  }
  const rsp = await listFactorBindings({
    space_id: selectedSpaceId.value,
    page: { page: 1, size: 1000 },
  });
  bindings.value = rsp.bindings || [];
}

watch(selectedSpaceId, loadBindings);
onMounted(loadBindings);
</script>
```

- [ ] **Step 3: Verify the factor result page builds**

Run:

```bash
cd web && pnpm build:prod
```

Expected after this task: build should pass unless an existing unrelated TypeScript issue is present.

---

## Task 6A: Stamp Factor Result Dataset Attribution

**Files:**

- Modify: `modules/factor/internal/registry/metadata_sync.go`
- Modify: `modules/factor/internal/registry/service_test.go`

- [ ] **Step 1: Add a regression assertion for factor dataset ownership**

In `modules/factor/internal/registry/service_test.go`, inside `TestMetadataSyncOrderAndPayload`, after the existing dataset name assertion:

```go
if got := client.datasetReqs[0].GetDataset().GetName(); got == "" || got == "因子结果" || len([]rune(got)) > 10 || !strings.Contains(got, "因子") {
	t.Fatalf("dataset name = %q", got)
}
```

add:

```go
datasetAttrs := client.datasetReqs[0].GetDataset().GetAttributes()
if datasetAttrs["owner_module"] != "factor" {
	t.Fatalf("owner_module = %q, want factor", datasetAttrs["owner_module"])
}
if datasetAttrs["dataset_role"] != "factor_result" {
	t.Fatalf("dataset_role = %q, want factor_result", datasetAttrs["dataset_role"])
}
if datasetAttrs["managed_by"] != "factor" {
	t.Fatalf("managed_by = %q, want factor", datasetAttrs["managed_by"])
}
if datasetAttrs["source_dataset_id"] != "binance_spot_kline" {
	t.Fatalf("source_dataset_id = %q, want binance_spot_kline", datasetAttrs["source_dataset_id"])
}
if datasetAttrs["source_freq"] != "1m" {
	t.Fatalf("source_freq = %q, want 1m", datasetAttrs["source_freq"])
}
```

- [ ] **Step 2: Run the focused test and verify it fails**

Run:

```bash
cd modules/factor && go test ./internal/registry -run TestMetadataSyncOrderAndPayload -count=1
```

Expected before implementation: fail with `owner_module = "", want factor`.

- [ ] **Step 3: Stamp attributes when factor creates result datasets**

In `modules/factor/internal/registry/metadata_sync.go`, update the `storagepb.Dataset` literal in `createDataset` to include:

```go
Attributes: factorResultDatasetAttributes(sourceDataset, freq),
```

The full `Dataset` literal should contain:

```go
Dataset: &storagepb.Dataset{
	SpaceId:      spaceID,
	DatasetId:    datasetID,
	DataSourceId: dataSourceID,
	Name:         datasetDisplayName(datasetID),
	Description:  fmt.Sprintf("Factor result dataset for %s", sourceDataset),
	DataKind:     storagepb.DataKind_DATA_KIND_TIME_SERIES,
	Freqs:        []string{freq},
	Status:       "active",
	Attributes:   factorResultDatasetAttributes(sourceDataset, freq),
},
```

Add this helper near `datasetDisplayName`:

```go
func factorResultDatasetAttributes(sourceDataset string, freq string) map[string]string {
	return map[string]string{
		"owner_module":      "factor",
		"dataset_role":      "factor_result",
		"managed_by":        "factor",
		"source_dataset_id": strings.TrimSpace(sourceDataset),
		"source_freq":       strings.TrimSpace(freq),
	}
}
```

- [ ] **Step 4: Run the focused factor registry test**

Run:

```bash
cd modules/factor && go test ./internal/registry -run TestMetadataSyncOrderAndPayload -count=1
```

Expected: pass.

- [ ] **Step 5: Run all factor tests**

Run:

```bash
cd modules/factor && go test ./...
```

Expected: pass.

---

## Task 7: Fix Factor Run Table Scrolling

**Files:**

- Modify: `web/src/views/factor/runs/index.vue`

- [ ] **Step 1: Add a bounded table shell**

Wrap the existing `<a-table ...>` in the template:

```vue
<div class="runs-table-shell">
  <a-table
    row-key="run_id"
    size="small"
    :bordered="{ cell: true }"
    :loading="loading"
    :data="rows"
    :pagination="pagination"
    :scroll="tableScroll"
    @page-change="onPageChange"
    @page-size-change="onPageSizeChange"
  >
    <template #columns>
      <!-- keep existing columns unchanged -->
    </template>
  </a-table>
</div>
```

- [ ] **Step 2: Replace inline table scroll with a computed value**

In the `<script setup>` area, after `const factorOptions = computed(() => factors.value);`, add:

```ts
const tableScroll = computed(() => ({
  x: 'max-content',
  y: 'calc(100vh - 360px)',
}));
```

- [ ] **Step 3: Add stable page and table styles**

In the scoped style block, replace:

```css
.factor-page {
  padding: 20px;
}
```

with:

```css
.factor-page {
  display: flex;
  flex-direction: column;
  min-height: 0;
  height: 100%;
  padding: 20px;
  overflow: hidden;
}
```

Then add:

```css
.runs-table-shell {
  flex: 1;
  min-height: 0;
  overflow: hidden;
}

.runs-table-shell :deep(.arco-table-container) {
  min-height: 0;
}
```

- [ ] **Step 4: Verify the page visually**

Run the web app:

```bash
cd web && pnpm dev -- --host 0.0.0.0
```

Open:

```text
http://localhost:5173/#/factor/runs
```

Expected:

- The page body does not create a broken double-scroll layout.
- The run table body scrolls when rows exceed viewport height.
- Pagination remains reachable.
- Horizontal scroll still works for wide columns.

---

## Task 8: Build, Browser Verification, And Code Review

**Files:**

- No new files.
- Review all files changed in Tasks 1-7, including the factor metadata sync attribution change.

- [ ] **Step 1: Run static menu check**

```bash
cd web && pnpm check:menu
```

Expected:

```text
menu structure ok
```

- [ ] **Step 2: Run production build**

```bash
cd web && pnpm build:prod
```

Expected:

- `vue-tsc` passes.
- Vite production build completes.

- [ ] **Step 2A: Run factor tests**

```bash
cd modules/factor && go test ./...
```

Expected: pass.

- [ ] **Step 3: Manually verify visible navigation**

Open the local dev server and verify these routes:

```text
/#/data/sources
/#/data/subjects
/#/data/fields
/#/collector/datasets
/#/collector/datasets?tab=browse
/#/collector/views
/#/collector/views?tab=browse
/#/factor/definitions
/#/factor/bindings
/#/factor/runs
/#/factor/results
```

Expected:

- `数据资产` contains only `数据源`, `数据对象`, `字段管理`.
- `数据采集` contains `数据集合`, `数据视图`, `采集规则`, `任务实例`, `云节点`, `代码包`.
- `因子计算` contains `因子定义`, `计算绑定`, `运行记录`, `因子结果`.
- No visible menu shows `因子字典（元数据）`, `计算定义`, `数据概览`, `数据浏览`, `视图列表`, or `视图浏览`.
- New datasets created from `数据采集 / 数据集合` include `attributes.owner_module=collector` and `attributes.dataset_role=raw_collection`.
- Factor-created result datasets include `attributes.owner_module=factor` and `attributes.dataset_role=factor_result`.

- [ ] **Step 4: Manually verify redirect compatibility**

Open these old URLs:

```text
/#/data/datasets
/#/data/browse
/#/data/overview
/#/data/views
/#/data/view-browse
/#/data/factors
```

Expected:

- Each old URL redirects to the new area.
- No 404 page appears.

- [ ] **Step 5: Start a fresh code review agent**

Ask a fresh agent to review:

```text
Review the admin menu reorganization changes. Focus on visible menu IA, route compatibility, i18n labels, dataset/view attribution attributes, wrapper page correctness, factor result filtering, factor metadata sync, and factor run table scroll behavior. Do not make code changes; report findings with file and line references.
```

Expected:

- Either no blocking findings, or every blocking finding is fixed and re-reviewed.

---

## Task 9: Commit And Deploy After Approval

**Files:**

- Commit all implementation files from Tasks 1-8.

- [ ] **Step 1: Inspect changed files**

```bash
git status --short
git diff -- web/scripts/check-menu-structure.mjs web/src/api/modules/system/static-menu.ts web/src/router/route.ts web/src/lang/modules/zhCN.ts web/src/lang/modules/enUS.ts web/src/views/data/shared/module-attribution.ts web/src/views/data/datasets/index.vue web/src/views/data/browse/index.vue web/src/views/data/views/index.vue web/src/views/data/view-browse/index.vue web/src/views/collector/datasets/index.vue web/src/views/collector/views/index.vue web/src/views/factor/results/index.vue web/src/views/factor/runs/index.vue modules/factor/internal/registry/metadata_sync.go modules/factor/internal/registry/service_test.go
```

Expected:

- Only admin frontend, menu test, and factor metadata attribution changes are present for this menu reorganization.

- [ ] **Step 2: Commit**

```bash
git add web/scripts/check-menu-structure.mjs web/src/api/modules/system/static-menu.ts web/src/router/route.ts web/src/lang/modules/zhCN.ts web/src/lang/modules/enUS.ts web/src/views/data/shared/module-attribution.ts web/src/views/data/datasets/index.vue web/src/views/data/browse/index.vue web/src/views/data/views/index.vue web/src/views/data/view-browse/index.vue web/src/views/collector/datasets/index.vue web/src/views/collector/views/index.vue web/src/views/factor/results/index.vue web/src/views/factor/runs/index.vue modules/factor/internal/registry/metadata_sync.go modules/factor/internal/registry/service_test.go
git commit -m "feat(admin): reorganize data and factor assets"
```

- [ ] **Step 3: Deploy admin and factor to `106.53.107.122`**

Because this plan changes factor metadata sync but not storage schema, collector, or cloudnode, deploy admin/web-host and factor:

```bash
scripts/deploy-moox.sh --target ubuntu@106.53.107.122 --dir /home/ubuntu/moox/prod --goos linux --goarch amd64 --no-storage --no-cloudnode --no-collector --build-web-assets
```

- [ ] **Step 4: Verify remote admin**

Open:

```text
http://106.53.107.122:9527/#/login
```

Login:

```text
admin / 123456
```

Then verify:

```text
http://106.53.107.122:9527/#/collector/datasets
http://106.53.107.122:9527/#/collector/views
http://106.53.107.122:9527/#/factor/definitions
http://106.53.107.122:9527/#/factor/runs
http://106.53.107.122:9527/#/factor/results
```

Expected:

- Login succeeds.
- New menu structure is visible.
- Factor run table scroll issue is fixed on the remote page.
- Factor results page renders and lists factor result views when factor binding target datasets have matching views.

---

## Self-Review Checklist

- Spec coverage: menu rename, factor metadata removal, dataset/view page merge, dataset/view module attribution, factor result unification, factor run scroll fix, code review, and deployment are all represented by tasks.
- Backend impact: no Storage schema or API changes are required. Factor metadata sync changes only write additional `Dataset.attributes` keys.
- Route safety: old `/data/*` URLs redirect instead of disappearing.
- Placeholder scan: no implementation task depends on an unnamed file, unlisted API, or unspecified command.
- Verification: menu script, production build, browser checks, fresh-agent review, and remote verification are included.
