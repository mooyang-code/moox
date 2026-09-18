# Unify Data Browse View Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Replace every collector “查看数据” page with the historical ViewBrowse + KlineModal interaction while keeping real Storage data queries.

**Architecture:** Keep the existing data-management route and secondary tabs. Change only the collector dataset workbench’s browse branch to render `ViewBrowse`; use its existing attribution filters, View queries, and KlineModal. Add a source contract test to prevent the old DatasetBrowse branch from returning.

**Tech Stack:** Vue 3 `<script setup>`, TypeScript, Arco Design Vue, Vitest, Playwright, Vite.

---

### Task 1: Add the failing interaction contract

**Files:**
- Modify: `web/src/views/collector/data-management/data-management.test.ts`

- [ ] **Step 1: Add a test that requires ViewBrowse and KlineModal wiring**

Add a test that reads `../datasets/index.vue`, normalizes whitespace, and asserts the browse branch contains `ViewBrowse`, `viewOwnerModules`, `viewRoles`, `includeUnowned`, and `KlineModal`; assert it no longer contains `DatasetBrowse`.

- [ ] **Step 2: Run the focused test and verify it fails**

Run:

```bash
pnpm exec vitest run --config vitest.config.ts src/views/collector/data-management/data-management.test.ts
```

Expected: the new assertion fails because the current source still imports and renders `DatasetBrowse`.

### Task 2: Switch the browse branch to the historical ViewBrowse workflow

**Files:**
- Modify: `web/src/views/collector/datasets/index.vue`

- [ ] **Step 1: Replace the browse component import and branch**

Import `ViewBrowse` from `@/views/data/view-browse/index.vue`. Render it in the `v-else` branch with:

```vue
<ViewBrowse
  :view-owner-modules="['collector']"
  :view-roles="['collection_browse']"
  :include-unowned="true"
  empty-description="暂无可浏览的采集视图"
>
```

Keep the existing `page-title` slot containing the collector secondary tabs and close the component normally. Remove the `DatasetBrowse` import and usage.

- [ ] **Step 2: Run the focused test and verify it passes**

Run the same Vitest command from Task 1. Expected: all data-management contract tests pass.

### Task 3: Run regression verification

**Files:**
- No additional source files.

- [ ] **Step 1: Run ViewBrowse workflow tests**

Run:

```bash
pnpm exec vitest run --config vitest.config.ts tests/storage-view-browse.spec.ts src/views/collector/data-management/data-management.test.ts
```

Expected: all tests pass, including the existing KlineModal open/close contract.

- [ ] **Step 2: Run navigation regression tests**

Run:

```bash
pnpm exec playwright test tests/data-collection-navigation.spec.ts
```

Expected: all navigation and tab-layout tests pass.

- [ ] **Step 3: Build the production frontend**

Run:

```bash
pnpm build:prod
```

Expected: `vue-tsc` and Vite finish with exit code 0.

- [ ] **Step 4: Deploy and verify the page**

Regenerate `web-host` static assets, build the Linux web-host binary, replace only remote `web-host`, and verify its health endpoint. Reload the production data-management route and confirm a View tab, real rows, the `K线` button, and the modal chart entry are visible.

### Task 4: Commit the focused change

**Files:**
- Modify: `web/src/views/collector/datasets/index.vue`
- Modify: `web/src/views/collector/data-management/data-management.test.ts`

- [ ] **Step 1: Review the diff and commit**

Run `git diff --check`, stage only the two focused frontend files, and commit with:

```bash
git commit -m "fix(web): restore view-based data browsing"
```

