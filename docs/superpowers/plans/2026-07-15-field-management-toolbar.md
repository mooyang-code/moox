# Field Management Toolbar Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Remove the redundant field-management heading and place search, filters, count, refresh, and create actions in one responsive toolbar.

**Architecture:** Keep all existing field-management state and event handlers unchanged. Restructure only the `web/src/views/data/fields/index.vue` template and scoped CSS, then protect the desktop layout contract with the existing Playwright workflow.

**Tech Stack:** Vue 3 SFC, Arco Design Vue, scoped CSS, Playwright, pnpm/Vite

---

### Task 1: Define the toolbar layout contract

**Files:**
- Modify: `web/tests/field-management-workbench.e2e.spec.ts`

- [ ] **Step 1: Replace the obsolete heading assertion and add single-row assertions**

In the desktop workflow, replace the heading check with assertions that the title is absent and the search and action controls share the same vertical center:

```ts
await expect(page.getByRole('heading', { name: '字段管理' })).toHaveCount(0);
const searchBox = page.getByPlaceholder('搜索字段 ID、中文名或描述');
const createButton = page.getByRole('button', { name: '新建字段' });
const [searchBounds, createBounds] = await Promise.all([searchBox.boundingBox(), createButton.boundingBox()]);
expect(Math.abs((searchBounds?.y || 0) + (searchBounds?.height || 0) / 2 - ((createBounds?.y || 0) + (createBounds?.height || 0) / 2))).toBeLessThanOrEqual(2);
```

- [ ] **Step 2: Run the focused E2E test and confirm it fails**

Run:

```bash
pnpm exec playwright test web/tests/field-management-workbench.e2e.spec.ts --grep "approved desktop"
```

Expected: failure because the `字段管理` heading still exists.

- [ ] **Step 3: Commit the failing contract**

```bash
git add web/tests/field-management-workbench.e2e.spec.ts
git commit -m "test(web): define compact field toolbar layout"
```

### Task 2: Implement and verify the compact toolbar

**Files:**
- Modify: `web/src/views/data/fields/index.vue`
- Test: `web/tests/field-management-workbench.e2e.spec.ts`

- [ ] **Step 1: Replace the header and separate filter row with one toolbar**

Use this structure inside `fields-inner`, before the no-space alert:

```vue
<div class="toolbar">
  <div class="toolbar-main">
    <a-button class="mobile-group-trigger" @click="mobileGroupVisible = true">
      <template #icon><icon-menu /></template>字段组
    </a-button>
    <a-input-search v-model="searchKeyword" class="keyword-input" allow-clear placeholder="搜索字段 ID、中文名或描述" @input="scheduleSearch" @search="commitSearch" />
    <a-select v-model="state.valueType" class="filter-select" allow-clear placeholder="值类型" @change="changeFilter">
      <a-option v-for="item in fieldValueTypeOptions" :key="item.value" :value="item.value">{{ item.label }}</a-option>
    </a-select>
    <a-select v-model="state.status" class="filter-select" allow-clear placeholder="状态" @change="changeFilter">
      <a-option v-for="item in statusOptions" :key="item.value" :value="item.value">{{ item.label }}</a-option>
    </a-select>
  </div>
  <div class="toolbar-actions">
    <span v-if="selectedSpaceId" class="field-total">{{ totalFieldCount }} 个字段</span>
    <a-tooltip content="刷新"><a-button :disabled="!selectedSpaceId" @click="loadAll"><icon-refresh /></a-button></a-tooltip>
    <a-button type="primary" :disabled="!selectedSpaceId || !groups.length" @click="openCreateField">
      <template #icon><icon-plus /></template>新建字段
    </a-button>
  </div>
</div>
```

Remove the old `page-head` block and remove the old toolbar from the `v-else` template.

- [ ] **Step 2: Add stable desktop and responsive sizing**

Replace `page-head` styles and update toolbar styles with:

```css
.toolbar { display: flex; min-height: 48px; margin-bottom: 8px; align-items: center; justify-content: space-between; gap: 16px; }
.toolbar-main { display: flex; min-width: 0; align-items: center; gap: 8px; }
.toolbar-actions { display: flex; flex: 0 0 auto; align-items: center; gap: 8px; white-space: nowrap; }
.field-total { color: var(--color-text-3); font-size: 13px; }
.keyword-input { width: 360px; min-width: 220px; flex: 1 1 360px; }
.filter-select { width: 140px; flex: 0 0 140px; }
```

At `max-width: 900px`, allow the toolbar and main group to wrap while keeping actions aligned to the right. At `max-width: 560px`, make the main and action groups full width and keep the search input flexible.

- [ ] **Step 3: Run unit and E2E tests**

Run:

```bash
pnpm test:unit -- web/src/views/data/fields/field-workbench.test.ts
pnpm exec playwright test web/tests/field-management-workbench.e2e.spec.ts
```

Expected: all field workbench unit tests and all three Playwright scenarios pass.

- [ ] **Step 4: Run the production build**

Run:

```bash
CI=true pnpm build:prod
```

Expected: `vue-tsc` and Vite complete successfully.

- [ ] **Step 5: Inspect desktop and mobile screenshots**

Confirm the Playwright screenshots show no page title, a single desktop toolbar row, and non-overlapping wrapped controls at 390px.

- [ ] **Step 6: Commit the implementation**

```bash
git add web/src/views/data/fields/index.vue web/tests/field-management-workbench.e2e.spec.ts
git commit -m "style(web): compact field management toolbar"
```
