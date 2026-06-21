# MooX Web 交互再设计 Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 重构管理台「数据资产」信息架构与「数据浏览」交互，降低用户理解成本，全程不暴露"主存"等底层协议词汇。

**Architecture:** 纯前端改动（Vue3 + Arco + Pinia + vue-router）。菜单改为「数据资产」一级父 + 三个二级分组（数据建模 / 查询视图 / 数据管理）；「数据列表」重写为「数据浏览」，用一次"选数据集/视图 + 选对象"替代四层 tab，按对象的 `data_kind` 自动推断查询接口；「数据同步」更名「数据导入」；顶部空间下拉新增"新建空间"。不改后端、proto、`web/src/api/**`。

**Tech Stack:** Vue 3.5、TypeScript、@arco-design/web-vue、Pinia、vue-router；轻量测试用 `node *.test.js`（见 `web/src/views/data/shared/metadata-utils.test.js` 约定），类型校验用 `pnpm vue-tsc --noEmit`。

**工作目录：** 所有命令在 `web/` 下执行（worktree：`feature/moox-web-space-workbench`）。

---

## File Structure

- 修改：`web/src/lang/modules/zhCN.ts`、`web/src/lang/modules/enUS.ts` — 菜单文案，新增/改名 key。
- 修改：`web/src/router/route.ts` — `/data/list`→`/data/browse`、`/data/sync`→`/data/import`。
- 修改：`web/src/mock/_data/system_menu.ts` — 三层菜单树。
- 迁移：`web/src/views/data/sync/` → `web/src/views/data/import/`。
- 重写：`web/src/views/data/list/index.vue` → 新建 `web/src/views/data/browse/index.vue`（删除旧 `list/`）。
- 复用：`web/src/views/data/shared/metadata-utils.ts` 的 `resolveViewRebuildKind`（已测）做类型推断，不新增重复逻辑。
- 修改：`web/src/layout/layout-head/index.vue` — 空间下拉新增"新建空间"入口与创建弹窗。

---

## Task 1: 菜单文案 i18n（含"数据来源"→"数据源"）

**Files:**
- Modify: `web/src/lang/modules/zhCN.ts:122-128`
- Modify: `web/src/lang/modules/enUS.ts:116-120`

- [ ] **Step 1: 修改 zhCN 数据资产相关文案，新增分组与浏览/导入 key**

将 `web/src/lang/modules/zhCN.ts` 中这一段：

```ts
    ["data-assets"]: "数据资产",
    ["data-sources"]: "数据来源",
    ["data-subjects"]: "数据对象",
    ["data-datasets"]: "数据集",
    ["data-fields"]: "字段管理",
    ["data-factors"]: "因子管理",
    ["data-views"]: "查询视图",
```

替换为：

```ts
    ["data-assets"]: "数据资产",
    ["data-modeling"]: "数据建模",
    ["data-mgmt"]: "数据管理",
    ["data-sources"]: "数据源",
    ["data-subjects"]: "数据对象",
    ["data-datasets"]: "数据集",
    ["data-fields"]: "字段管理",
    ["data-factors"]: "因子管理",
    ["data-views"]: "查询视图",
    ["data-browse"]: "数据浏览",
    ["data-import"]: "数据导入",
```

- [ ] **Step 2: 修改 enUS 对应文案**

将 `web/src/lang/modules/enUS.ts` 中这一段：

```ts
    ["data-assets"]: "data assets",
    ["data-sources"]: "data sources",
    ["data-subjects"]: "subjects",
    ["data-datasets"]: "datasets",
    ["data-fields"]: "fields",
```

替换为：

```ts
    ["data-assets"]: "data assets",
    ["data-modeling"]: "data modeling",
    ["data-mgmt"]: "data management",
    ["data-sources"]: "data sources",
    ["data-subjects"]: "subjects",
    ["data-datasets"]: "datasets",
    ["data-fields"]: "fields",
    ["data-browse"]: "data browse",
    ["data-import"]: "data import",
```

> 说明：旧 key `data-management`（zhCN="数据管理"）仍被其他历史路由使用，保留不动；二级分组改用独立 key `data-mgmt`，避免歧义。

- [ ] **Step 3: 类型校验**

Run: `pnpm vue-tsc --noEmit`
Expected: 通过，无新增错误。

- [ ] **Step 4: Commit**

```bash
git add web/src/lang/modules/zhCN.ts web/src/lang/modules/enUS.ts
git commit -m "feat(web): rename 数据来源→数据源 and add data IA i18n keys"
```

---

## Task 2: 路由路径重命名

**Files:**
- Modify: `web/src/router/route.ts:93-104`

- [ ] **Step 1: 重命名 /data/list 与 /data/sync 路由**

将 `web/src/router/route.ts` 中这一段：

```ts
      {
        path: "/data/list",
        name: "data-list",
        component: () => import("@/views/data/list/index.vue"),
        meta: { title: "data-list" }
      },
      {
        path: "/data/sync",
        name: "data-sync",
        component: () => import("@/views/data/sync/index.vue"),
        meta: { title: "data-sync" }
      },
```

替换为：

```ts
      {
        path: "/data/browse",
        name: "data-browse",
        component: () => import("@/views/data/browse/index.vue"),
        meta: { title: "data-browse" }
      },
      {
        path: "/data/import",
        name: "data-import",
        component: () => import("@/views/data/import/index.vue"),
        meta: { title: "data-import" }
      },
```

> 注意：此时 `@/views/data/browse/index.vue` 与 `@/views/data/import/index.vue` 尚未创建，Task 4/5 完成后类型校验才会通过。本任务先不单独跑 vue-tsc。

- [ ] **Step 2: Commit**

```bash
git add web/src/router/route.ts
git commit -m "feat(web): rename data routes /data/list->/data/browse, /data/sync->/data/import"
```

---

## Task 3: 菜单树重构为三层（X 方案）

**Files:**
- Modify: `web/src/mock/_data/system_menu.ts:54-63`

- [ ] **Step 1: 替换「数据资产」菜单块为三层结构**

将 `web/src/mock/_data/system_menu.ts` 中这一段（id 02 及其直接子项 0201-0209）：

```ts
  directory("02", "0", "/data/overview", "data-assets", "data-assets", 2, { svgIcon: "folder-menu", icon: "" }),
  menu("0201", "02", "/data/sources", "data-sources", "data-sources", "data/sources/index", 1),
  menu("0202", "02", "/data/subjects", "data-subjects", "data-subjects", "data/subjects/index", 2),
  menu("0203", "02", "/data/datasets", "data-datasets", "data-datasets", "data/datasets/index", 3),
  menu("0204", "02", "/data/fields", "data-fields", "data-fields", "data/fields/index", 4),
  menu("0205", "02", "/data/factors", "data-factors", "data-factors", "data/factors/index", 5),
  menu("0206", "02", "/data/views", "data-views", "data-views", "data/views/index", 6),
  menu("0207", "02", "/data/overview", "data-overview", "data-overview", "data/overview/overview", 7),
  menu("0208", "02", "/data/list", "data-list", "data-list", "data/list/index", 8),
  menu("0209", "02", "/data/sync", "data-sync", "data-sync", "data/sync/index", 9),
```

替换为：

```ts
  directory("02", "0", "/data/overview", "data-assets", "data-assets", 2, { svgIcon: "folder-menu", icon: "" }),
  // 二级分组：数据建模
  directory("0210", "02", "/data/sources", "data-modeling", "data-modeling", 1),
  menu("021001", "0210", "/data/sources", "data-sources", "data-sources", "data/sources/index", 1),
  menu("021002", "0210", "/data/subjects", "data-subjects", "data-subjects", "data/subjects/index", 2),
  menu("021003", "0210", "/data/datasets", "data-datasets", "data-datasets", "data/datasets/index", 3),
  menu("021004", "0210", "/data/fields", "data-fields", "data-fields", "data/fields/index", 4),
  menu("021005", "0210", "/data/factors", "data-factors", "data-factors", "data/factors/index", 5),
  // 二级单页：查询视图（独立成组）
  menu("0220", "02", "/data/views", "data-views", "data-views", "data/views/index", 2),
  // 二级分组：数据管理
  directory("0230", "02", "/data/overview", "data-mgmt", "data-mgmt", 3),
  menu("023001", "0230", "/data/overview", "data-overview", "data-overview", "data/overview/overview", 1),
  menu("023002", "0230", "/data/browse", "data-browse", "data-browse", "data/browse/index", 2),
  menu("023003", "0230", "/data/import", "data-import", "data-import", "data/import/index", 3),
```

- [ ] **Step 2: Commit**

```bash
git add web/src/mock/_data/system_menu.ts
git commit -m "feat(web): restructure data-assets menu into 3 second-level groups"
```

---

## Task 4: 迁移「数据同步」→「数据导入」

**Files:**
- Move: `web/src/views/data/sync/` → `web/src/views/data/import/`
- Modify: `web/src/views/data/import/index.vue`（迁移后）

- [ ] **Step 1: git 移动目录**

Run:
```bash
git mv web/src/views/data/sync web/src/views/data/import
```
Expected: `web/src/views/data/import/index.vue` 存在，`web/src/views/data/sync` 不再存在。

- [ ] **Step 2: 更新组件名**

在 `web/src/views/data/import/index.vue` 将：

```ts
defineOptions({ name: 'DataSync' });
```

替换为：

```ts
defineOptions({ name: 'DataImport' });
```

- [ ] **Step 3: 更新页面标题文案**

在 `web/src/views/data/import/index.vue` 将：

```html
        <h2>数据同步</h2>
```

替换为：

```html
        <h2>数据导入</h2>
```

- [ ] **Step 4: Commit**

```bash
git add web/src/views/data/import
git commit -m "feat(web): rename data sync page to data import"
```

---

## Task 5: 重写「数据浏览」页（核心）

把旧 `data/list/index.vue` 的四层 tab（主存/视图 × TimeSeries/Record）替换为「来源(数据集/视图) + 选对象」单入口，类型由对象的 `data_kind` 自动推断（复用已测的 `resolveViewRebuildKind`），界面不出现"主存"、不暴露接口名。

**Files:**
- Create: `web/src/views/data/browse/index.vue`
- Delete: `web/src/views/data/list/index.vue`（及空目录 `web/src/views/data/list/`）
- Reuse: `web/src/views/data/shared/metadata-utils.ts:113`（`resolveViewRebuildKind`）

- [ ] **Step 1: 给推断逻辑补一条浏览场景的断言（先让测试失败）**

在 `web/src/views/data/shared/metadata-utils.test.js` 末尾 `console.log('metadata utils tests passed');` 之前插入：

```js
// 数据浏览：数据集/视图均按 primary dataset 的 data_kind 推断查询类型
assert.equal(resolveViewRebuildKind(datasets, 'kline'), 'time_series'); // 时序数据集 → 时序读取
assert.equal(resolveViewRebuildKind(datasets, 'company_profile'), 'record'); // 记录数据集 → 记录读取
assert.equal(resolveViewRebuildKind(datasets, ''), 'missing'); // 未选对象
```

- [ ] **Step 2: 运行测试确认通过（逻辑已存在，验证复用正确）**

Run: `node web/src/views/data/shared/metadata-utils.test.js`
Expected: 输出 `metadata utils tests passed`，无 AssertionError。
（说明：本步骤是对"复用 `resolveViewRebuildKind` 做浏览推断"这一决策的回归保护；该函数已存在，故应直接通过。）

- [ ] **Step 3: 创建 `web/src/views/data/browse/index.vue`**

完整内容：

```vue
<template>
  <div class="data-browse-page">
    <div class="page-head">
      <h2>数据浏览</h2>
      <span>当前空间：{{ spaceStore.selectedSpace?.name || '未选择' }}</span>
    </div>

    <a-alert v-if="!selectedSpaceId" type="warning" show-icon>请先在顶部选择空间</a-alert>

    <template v-else>
      <section class="picker">
        <a-form layout="inline">
          <a-form-item label="数据来源">
            <a-radio-group v-model="source" type="button" @change="onSourceChange">
              <a-radio value="dataset">数据集</a-radio>
              <a-radio value="view">视图</a-radio>
            </a-radio-group>
          </a-form-item>
          <a-form-item :label="source === 'dataset' ? '选择数据集' : '选择视图'">
            <a-select
              v-model="objectId"
              allow-search
              :loading="metaLoading"
              :placeholder="source === 'dataset' ? '请选择数据集' : '请选择视图'"
              style="width: 280px"
              @change="onObjectChange"
            >
              <a-option v-for="opt in objectOptions" :key="opt.value" :value="opt.value">
                {{ opt.label }}
              </a-option>
            </a-select>
          </a-form-item>
          <a-button type="text" :loading="metaLoading" @click="loadMeta">
            <template #icon><icon-refresh /></template>
            刷新
          </a-button>
        </a-form>
        <div v-if="modeHint" class="mode-hint">{{ modeHint }}</div>
      </section>

      <section v-if="mode === 'time_series'" class="query-panel">
        <a-form :model="tsForm" layout="vertical">
          <a-row :gutter="12">
            <a-col :xs="24" :md="8" :lg="6">
              <a-form-item label="对象（subject_id）">
                <a-input v-model="tsForm.subject_id" placeholder="subject_id" />
              </a-form-item>
            </a-col>
            <a-col :xs="24" :md="8" :lg="4">
              <a-form-item label="周期">
                <a-input v-model="tsForm.freq" placeholder="1m" />
              </a-form-item>
            </a-col>
            <a-col :xs="24" :md="12" :lg="5">
              <a-form-item label="开始时间">
                <a-input v-model="tsForm.start_time" placeholder="2024-01-01T00:00:00Z" />
              </a-form-item>
            </a-col>
            <a-col :xs="24" :md="12" :lg="5">
              <a-form-item label="结束时间">
                <a-input v-model="tsForm.end_time" placeholder="2024-01-02T00:00:00Z" />
              </a-form-item>
            </a-col>
            <a-col :xs="24" :md="12" :lg="8">
              <a-form-item label="列名">
                <a-input v-model="tsForm.column_names" placeholder="close,volume" />
              </a-form-item>
            </a-col>
            <a-col :xs="12" :md="6" :lg="4">
              <a-form-item label="排序">
                <a-select v-model="tsForm.order">
                  <a-option value="SORT_ORDER_ASC">升序</a-option>
                  <a-option value="SORT_ORDER_DESC">降序</a-option>
                </a-select>
              </a-form-item>
            </a-col>
            <a-col :xs="12" :md="6" :lg="4">
              <a-form-item label="每页条数">
                <a-input-number v-model="tsForm.page_size" :min="1" :max="500" />
              </a-form-item>
            </a-col>
            <a-col :xs="24" :md="12" :lg="8">
              <a-form-item label="维度（高级，JSON）">
                <a-input v-model="tsForm.dimensions" placeholder='{"adjust":"none"}' />
              </a-form-item>
            </a-col>
          </a-row>
          <a-button type="primary" :loading="loading" @click="runQuery">
            <template #icon><icon-search /></template>
            查询
          </a-button>
        </a-form>
      </section>

      <section v-else-if="mode === 'record'" class="query-panel">
        <a-form :model="recForm" layout="vertical">
          <a-row :gutter="12">
            <a-col :xs="24" :md="8" :lg="6">
              <a-form-item :label="source === 'dataset' ? '记录 ID' : '记录 ID（可留空）'">
                <a-input v-model="recForm.record_id" placeholder="record_id" />
              </a-form-item>
            </a-col>
            <a-col :xs="24" :md="8" :lg="5">
              <a-form-item label="开始版本">
                <a-input v-model="recForm.start_version" placeholder="可留空" />
              </a-form-item>
            </a-col>
            <a-col :xs="24" :md="8" :lg="5">
              <a-form-item label="结束版本">
                <a-input v-model="recForm.end_version" placeholder="可留空" />
              </a-form-item>
            </a-col>
            <a-col :xs="24" :md="12" :lg="8">
              <a-form-item label="列名">
                <a-input v-model="recForm.column_names" placeholder="title,body" />
              </a-form-item>
            </a-col>
            <a-col :xs="12" :md="6" :lg="4">
              <a-form-item label="排序">
                <a-select v-model="recForm.order">
                  <a-option value="SORT_ORDER_ASC">升序</a-option>
                  <a-option value="SORT_ORDER_DESC">降序</a-option>
                </a-select>
              </a-form-item>
            </a-col>
            <a-col :xs="12" :md="6" :lg="4">
              <a-form-item label="每页条数">
                <a-input-number v-model="recForm.page_size" :min="1" :max="500" />
              </a-form-item>
            </a-col>
          </a-row>

          <a-collapse v-if="source === 'view'" :default-active-key="[]">
            <a-collapse-item key="adv" header="高级检索">
              <a-row :gutter="12">
                <a-col :xs="24" :md="12" :lg="8">
                  <a-form-item label="全文检索">
                    <a-input v-model="viewRecForm.text_query" placeholder="关键词" />
                  </a-form-item>
                </a-col>
                <a-col :xs="24" :lg="8">
                  <a-form-item label="过滤（JSON 数组）">
                    <a-textarea v-model="viewRecForm.filters" :auto-size="{ minRows: 3, maxRows: 6 }" />
                  </a-form-item>
                </a-col>
                <a-col :xs="24" :lg="8">
                  <a-form-item label="排序（JSON 数组）">
                    <a-textarea v-model="viewRecForm.sorts" :auto-size="{ minRows: 3, maxRows: 6 }" />
                  </a-form-item>
                </a-col>
              </a-row>
            </a-collapse-item>
          </a-collapse>

          <a-button type="primary" :loading="loading" @click="runQuery" style="margin-top: 12px">
            <template #icon><icon-search /></template>
            查询
          </a-button>
        </a-form>
      </section>

      <a-empty v-else-if="mode === 'missing'" description="无法识别该对象的数据类型" />
      <a-empty v-else description="请选择要浏览的数据集或视图" />

      <section class="result-panel">
        <div class="result-head">
          <strong>查询结果</strong>
          <span>{{ resultRows.length }} 行</span>
        </div>
        <a-table
          row-key="id"
          size="small"
          :bordered="{ cell: true }"
          :loading="loading"
          :data="resultRows"
          :pagination="false"
          :scroll="{ x: 'max-content', y: 420 }"
        >
          <template #columns>
            <a-table-column title="Key" data-index="key" :width="320" />
            <a-table-column title="版本" data-index="version" :width="210" />
            <a-table-column title="列数据" :width="520">
              <template #cell="{ record }">
                <pre>{{ record.columns }}</pre>
              </template>
            </a-table-column>
          </template>
        </a-table>
      </section>
    </template>
  </div>
</template>

<script setup lang="ts">
import { computed, reactive, ref } from 'vue';
import { Message } from '@arco-design/web-vue';
import { listDatasets, listViews } from '@/api/storage/metadata';
import { readRecordRows, readTimeSeriesRows } from '@/api/storage/access';
import { queryTimeSeriesRows, searchRecordRows } from '@/api/storage/view';
import type { Dataset, FilterExpr, RecordRow, SortOrder, SortSpec, TimeSeriesRow, View } from '@/api/storage/types';
import { resolveViewRebuildKind } from '@/views/data/shared/metadata-utils';
import { useSpaceStore } from '@/store/modules/space';

defineOptions({ name: 'DataBrowse' });

interface ResultRow {
  id: string;
  key: string;
  version: string;
  columns: string;
}

const spaceStore = useSpaceStore();
const selectedSpaceId = computed(() => spaceStore.selectedSpaceId);

const source = ref<'dataset' | 'view'>('dataset');
const objectId = ref('');
const datasets = ref<Dataset[]>([]);
const views = ref<View[]>([]);
const metaLoading = ref(false);
const loading = ref(false);
const resultRows = ref<ResultRow[]>([]);

const tsForm = reactive({
  subject_id: '',
  freq: '',
  start_time: '',
  end_time: '',
  dimensions: '{}',
  column_names: '',
  order: 'SORT_ORDER_ASC',
  page_size: 100,
});

const recForm = reactive({
  record_id: '',
  start_version: '',
  end_version: '',
  column_names: '',
  order: 'SORT_ORDER_ASC',
  page_size: 100,
});

const viewRecForm = reactive({
  text_query: '',
  filters: '[]',
  sorts: '[]',
});

const objectOptions = computed(() => {
  if (source.value === 'dataset') {
    return datasets.value.map((item) => ({
      value: item.dataset_id,
      label: `${item.name || item.dataset_id} (${item.dataset_id})`,
    }));
  }
  return views.value.map((item) => ({
    value: item.view_id,
    label: `${item.name || item.view_id} (${item.view_id})`,
  }));
});

const currentView = computed(() => views.value.find((item) => item.view_id === objectId.value));

const mode = computed<'none' | 'time_series' | 'record' | 'missing'>(() => {
  if (!objectId.value) return 'none';
  if (source.value === 'dataset') {
    return resolveViewRebuildKind(datasets.value, objectId.value);
  }
  const view = currentView.value;
  if (!view) return 'missing';
  return resolveViewRebuildKind(datasets.value, view.primary_dataset_id);
});

const modeHint = computed(() => {
  const subject = source.value === 'dataset' ? '该数据集' : '该视图';
  if (mode.value === 'time_series') return `${subject}为时序数据`;
  if (mode.value === 'record') return `${subject}为记录数据`;
  if (mode.value === 'missing') return '无法识别该对象的数据类型';
  return '';
});

function splitNames(value: string) {
  return value
    .split(/[,，\n]/)
    .map((item) => item.trim())
    .filter(Boolean);
}

function parseJsonObject(value: string, label: string) {
  if (!value.trim()) return {};
  const parsed = JSON.parse(value);
  if (!parsed || Array.isArray(parsed) || typeof parsed !== 'object') {
    throw new Error(`${label} 必须是 JSON 对象`);
  }
  return parsed as Record<string, string>;
}

function parseJsonArray<T>(value: string, label: string) {
  if (!value.trim()) return [];
  const parsed = JSON.parse(value);
  if (!Array.isArray(parsed)) {
    throw new Error(`${label} 必须是 JSON 数组`);
  }
  return parsed as T[];
}

function validateTime(value: string, label: string) {
  if (!value) return;
  if (Number.isNaN(Date.parse(value))) {
    throw new Error(`${label} 必须是 RFC3339/RFC3339Nano 时间`);
  }
}

function requireInput(value: string, label: string) {
  if (!value.trim()) throw new Error(`请填写 ${label}`);
}

function mapTimeSeries(rows: TimeSeriesRow[]): ResultRow[] {
  return rows.map((row, index) => ({
    id: `ts-${index}-${row.key?.data_time || ''}`,
    key: `${row.key?.dataset_id || ''}/${row.key?.subject_id || ''}/${row.key?.freq || ''}`,
    version: row.key?.data_time || '-',
    columns: JSON.stringify(row.columns || [], null, 2),
  }));
}

function mapRecord(rows: RecordRow[]): ResultRow[] {
  return rows.map((row, index) => ({
    id: `record-${index}-${row.key?.version || ''}`,
    key: `${row.key?.dataset_id || ''}/${row.key?.record_id || ''}`,
    version: row.key?.version || '-',
    columns: JSON.stringify(row.columns || [], null, 2),
  }));
}

async function loadMeta() {
  const space_id = spaceStore.selectedSpaceId;
  if (!space_id) return;
  metaLoading.value = true;
  try {
    const [dsRsp, viewRsp] = await Promise.all([
      listDatasets({ space_id, page: { page: 1, size: 200 } }),
      listViews({ space_id, page: { page: 1, size: 200 } }),
    ]);
    datasets.value = dsRsp.datasets || [];
    views.value = viewRsp.views || [];
  } catch (error) {
    Message.error(error instanceof Error ? error.message : '加载数据资产失败');
  } finally {
    metaLoading.value = false;
  }
}

function onSourceChange() {
  objectId.value = '';
  resultRows.value = [];
}

function onObjectChange() {
  resultRows.value = [];
}

async function runQuery() {
  if (mode.value === 'none') {
    Message.warning('请选择要浏览的数据集或视图');
    return;
  }
  if (mode.value === 'missing') {
    Message.error('无法识别该对象的数据类型');
    return;
  }
  loading.value = true;
  try {
    const space_id = spaceStore.requireSpaceId();

    if (source.value === 'dataset' && mode.value === 'time_series') {
      requireInput(tsForm.subject_id, '对象（subject_id）');
      requireInput(tsForm.freq, '周期');
      validateTime(tsForm.start_time, '开始时间');
      validateTime(tsForm.end_time, '结束时间');
      const rsp = await readTimeSeriesRows({
        keys: [{
          space_id,
          dataset_id: objectId.value,
          subject_id: tsForm.subject_id,
          freq: tsForm.freq,
          dimensions: parseJsonObject(tsForm.dimensions, '维度'),
        }],
        time_range: { start_time: tsForm.start_time, end_time: tsForm.end_time },
        order: tsForm.order as SortOrder,
        column_names: splitNames(tsForm.column_names),
        page: { page: 1, size: tsForm.page_size },
      });
      resultRows.value = mapTimeSeries(rsp.rows || []);
      return;
    }

    if (source.value === 'dataset' && mode.value === 'record') {
      requireInput(recForm.record_id, '记录 ID');
      const rsp = await readRecordRows({
        keys: [{ space_id, dataset_id: objectId.value, record_id: recForm.record_id }],
        version_range: { start_version: recForm.start_version, end_version: recForm.end_version },
        order: recForm.order as SortOrder,
        column_names: splitNames(recForm.column_names),
        page: { page: 1, size: recForm.page_size },
      });
      resultRows.value = mapRecord(rsp.rows || []);
      return;
    }

    const view = currentView.value;
    if (!view) {
      Message.error('无法识别该视图');
      return;
    }

    if (mode.value === 'time_series') {
      requireInput(tsForm.subject_id, '对象（subject_id）');
      requireInput(tsForm.freq, '周期');
      validateTime(tsForm.start_time, '开始时间');
      validateTime(tsForm.end_time, '结束时间');
      const rsp = await queryTimeSeriesRows({
        space_id,
        view_id: view.view_id,
        keys: [{
          space_id,
          dataset_id: view.primary_dataset_id,
          subject_id: tsForm.subject_id,
          freq: tsForm.freq,
          dimensions: parseJsonObject(tsForm.dimensions, '维度'),
        }],
        time_range: { start_time: tsForm.start_time, end_time: tsForm.end_time },
        column_names: splitNames(tsForm.column_names),
        page: { page: 1, size: tsForm.page_size },
      });
      resultRows.value = mapTimeSeries(rsp.rows || []);
      return;
    }

    const rsp = await searchRecordRows({
      space_id,
      view_id: view.view_id,
      keys: recForm.record_id
        ? [{ space_id, dataset_id: view.primary_dataset_id, record_id: recForm.record_id }]
        : [],
      text_query: viewRecForm.text_query,
      version_range: { start_version: recForm.start_version, end_version: recForm.end_version },
      filters: parseJsonArray<FilterExpr>(viewRecForm.filters, '过滤'),
      sorts: parseJsonArray<SortSpec>(viewRecForm.sorts, '排序'),
      column_names: splitNames(recForm.column_names),
      page: { page: 1, size: recForm.page_size },
    });
    resultRows.value = mapRecord(rsp.rows || []);
  } catch (error) {
    Message.error(error instanceof Error ? error.message : '查询失败');
  } finally {
    loading.value = false;
  }
}

onMounted(loadMeta);
watch(selectedSpaceId, () => {
  objectId.value = '';
  resultRows.value = [];
  loadMeta();
});
</script>

<style scoped>
.data-browse-page {
  padding: 20px;
}

.page-head,
.result-head {
  display: flex;
  align-items: center;
  justify-content: space-between;
  margin-bottom: 16px;
}

.page-head h2 {
  margin: 0 0 4px;
  font-size: 20px;
  font-weight: 600;
}

.page-head span,
.result-head span {
  color: var(--color-text-3);
}

.picker {
  padding: 8px 0 4px;
}

.mode-hint {
  margin-top: 8px;
  font-size: 13px;
  color: var(--color-text-3);
}

.query-panel,
.result-panel {
  padding: 16px 0;
}

pre {
  max-width: 520px;
  margin: 0;
  white-space: pre-wrap;
  word-break: break-word;
}
</style>
```

> 说明：`onMounted`/`watch`/`ref`/`computed`/`reactive` 在本项目已由 `unplugin-auto-import` 自动注入（参考同目录其他页面无显式 import 即可使用 `ref`）。若类型校验报 `onMounted is not defined`，则在 `import { computed, reactive, ref } from 'vue';` 中补充 `onMounted, watch`。

- [ ] **Step 4: 删除旧页面**

Run:
```bash
git rm web/src/views/data/list/index.vue
```
Expected: `web/src/views/data/list/` 目录被清空/删除。

- [ ] **Step 5: 类型校验**

Run: `pnpm vue-tsc --noEmit`
Expected: 通过；若报自动导入相关错误，按 Step 3 说明补 `onMounted, watch` 后重跑。

- [ ] **Step 6: 运行轻量单测**

Run: `node web/src/views/data/shared/metadata-utils.test.js`
Expected: `metadata utils tests passed`。

- [ ] **Step 7: Commit**

```bash
git add web/src/views/data/browse web/src/views/data/shared/metadata-utils.test.js
git rm -r --cached web/src/views/data/list 2>/dev/null; git add -A web/src/views/data/list
git commit -m "feat(web): rewrite data list into data browse with auto type inference"
```

---

## Task 6: 顶部空间下拉新增「新建空间」

**Files:**
- Modify: `web/src/layout/layout-head/index.vue`

- [ ] **Step 1: 新增导入 createSpace**

将：

```ts
import { useSpaceStore } from "@/store/modules/space";
```

替换为：

```ts
import { useSpaceStore } from "@/store/modules/space";
import { createSpace } from "@/api/control/spaces";
```

- [ ] **Step 2: 空间选择器旁增加"新建"按钮**

将模板中：

```html
          <a-button class="space-setting-button" type="text" size="small" @click="goSpaceSettings">
            <template #icon><icon-settings /></template>
          </a-button>
```

替换为：

```html
          <a-button class="space-setting-button" type="text" size="small" title="新建空间" @click="openCreate">
            <template #icon><icon-plus /></template>
          </a-button>
          <a-button class="space-setting-button" type="text" size="small" title="空间管理" @click="goSpaceSettings">
            <template #icon><icon-settings /></template>
          </a-button>
```

- [ ] **Step 3: 增加新建空间弹窗**

将模板中：

```html
      <Main />
      <Footer v-if="isFooter" />
    </div>
```

替换为：

```html
      <Main />
      <Footer v-if="isFooter" />

      <a-modal
        v-model:visible="createVisible"
        title="新建空间"
        :ok-loading="creating"
        @ok="submitCreate"
        @cancel="resetCreate"
      >
        <a-form :model="createForm" layout="vertical">
          <a-form-item label="空间 ID" required>
            <a-input v-model="createForm.space_id" placeholder="如 hk_stock" />
          </a-form-item>
          <a-form-item label="名称" required>
            <a-input v-model="createForm.name" placeholder="空间名称" />
          </a-form-item>
          <a-form-item label="描述">
            <a-input v-model="createForm.description" />
          </a-form-item>
          <a-form-item label="负责人">
            <a-input v-model="createForm.owner" />
          </a-form-item>
          <a-form-item label="市场">
            <a-input v-model="createForm.market" placeholder="如 HK / US / CN" />
          </a-form-item>
          <a-form-item label="时区">
            <a-input v-model="createForm.timezone" placeholder="如 Asia/Shanghai" />
          </a-form-item>
        </a-form>
      </a-modal>
    </div>
```

- [ ] **Step 4: 增加弹窗状态与提交逻辑**

将脚本中：

```ts
const goSpaceSettings = () => {
  router.push("/settings/spaces");
};
```

替换为：

```ts
const goSpaceSettings = () => {
  router.push("/settings/spaces");
};

const createVisible = ref(false);
const creating = ref(false);
const createForm = reactive({
  space_id: "",
  name: "",
  description: "",
  owner: "",
  market: "",
  timezone: ""
});

const resetCreate = () => {
  createForm.space_id = "";
  createForm.name = "";
  createForm.description = "";
  createForm.owner = "";
  createForm.market = "";
  createForm.timezone = "";
};

const openCreate = () => {
  resetCreate();
  createVisible.value = true;
};

const submitCreate = async () => {
  if (!createForm.space_id.trim() || !createForm.name.trim()) {
    Message.warning("请填写空间 ID 和名称");
    return;
  }
  creating.value = true;
  try {
    await createSpace({ ...createForm, status: "active" });
    await spaceStore.loadSpaces();
    spaceStore.setSelectedSpace(createForm.space_id.trim());
    Message.success("空间创建成功");
    createVisible.value = false;
    resetCreate();
  } catch (error) {
    Message.error(error instanceof Error ? error.message : "创建空间失败");
  } finally {
    creating.value = false;
  }
};
```

> `reactive` 与 `ref` 在本文件已可用（同文件已直接使用 `ref`/`watch`/`onMounted` 而无显式 import，由 auto-import 提供）。若类型校验报未定义，则在文件顶部补 `import { reactive, ref } from "vue";`。

- [ ] **Step 5: 类型校验**

Run: `pnpm vue-tsc --noEmit`
Expected: 通过。

- [ ] **Step 6: Commit**

```bash
git add web/src/layout/layout-head/index.vue
git commit -m "feat(web): add create-space entry to top space switcher"
```

---

## Task 7: 全量校验与残留清理

**Files:**
- 无新增；做交叉检查。

- [ ] **Step 1: 确认无遗留旧路由/旧标识引用**

Run: `rg -n "data/list|data/sync|data-list|data-sync|DataList|DataSync" web/src`
Expected: 仅 `web/src/mock/data/index.ts` 中的 mock 接口 URL（`/mock/data/data-list` 等）可能命中——这是与菜单无关的历史 mock 数据，**本次不处理**；除此之外应无命中。若 `route.ts`、`system_menu.ts`、`lang/*`、`views/*` 仍命中，回到对应任务修正。

- [ ] **Step 2: 确认"主存"未在数据浏览页出现**

Run: `rg -n "主存" web/src/views/data/browse`
Expected: 无命中。

- [ ] **Step 3: 轻量单测**

Run: `pnpm test:unit`
Expected: `metadata utils tests passed` 等全部通过。

- [ ] **Step 4: 类型校验 + 生产构建**

Run: `pnpm build:prod`
Expected: `vue-tsc` 与 `vite build` 均成功，无错误。

- [ ] **Step 5: 手动冒烟（dev）**

Run: `pnpm dev`（后台启动），登录 `e2eadmin / Pass1234`，验证：
- 「数据资产」展开仅见 数据建模 / 查询视图 / 数据管理 三组，且「数据源」文案正确。
- 「数据浏览」切换 数据集/视图、选对象后表单按类型自动切换，可查到数据；界面无"主存"字样。
- 顶部下拉旁「＋」可新建空间并自动切换。

- [ ] **Step 6: Commit（如有清理改动）**

```bash
git add -A
git commit -m "chore(web): verify data IA redesign, no leftover legacy refs"
```

---

## Self-Review

**1. Spec coverage（对照 `docs/superpowers/specs/2026-06-21-moox-web-interaction-redesign-design.md`）**
- §3 信息架构（X 三层、数据源改名、查询视图独立）→ Task 1/3。
- §4.1 数据浏览重构（来源/对象 + 自动推断 + 分类型表单 + 不暴露主存）→ Task 5（+ Task 7 Step 2 校验）。
- §4.2 顶部新建空间 → Task 6。
- §4.3 其余页与数据导入改名 → Task 4（导入）；其余页仅菜单归类（Task 3），不重写，符合预期。
- §URL 重命名 → Task 2。
- §7 验收标准 → Task 7。

**2. Placeholder scan**：无 TBD/TODO；浏览页给出完整文件内容；i18n/route/menu 均为精确替换。

**3. Type/命名一致性**：
- `resolveViewRebuildKind(datasets, datasetId)` 返回 `'time_series'|'record'|'missing'`，浏览页 `mode` 复用同一返回值集合并扩展 `'none'`（仅前端态），一致。
- 路由 name/title/i18n key 三处一致：`data-browse`、`data-import`、`data-modeling`、`data-mgmt`。
- API 调用形参（`readTimeSeriesRows`/`readRecordRows`/`queryTimeSeriesRows`/`searchRecordRows`）沿用旧 `list/index.vue` 已验证的字段结构，未改 `web/src/api/**`。

**4. 风险点**：auto-import 对 `onMounted/watch/reactive` 的可用性已在各步以"若报错则补 import"兜底；`a-modal @ok` 关闭行为以手动置 `createVisible=false` 兜底。

---

## Execution Handoff

Plan complete. 两种执行方式：
1. **Subagent-Driven（推荐）**：每个 Task 派发独立子代理，任务间评审。
2. **Inline Execution**：本会话内按 Task 批量执行，带检查点评审。


