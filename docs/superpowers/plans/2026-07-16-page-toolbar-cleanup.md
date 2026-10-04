# Page Toolbar Cleanup Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Simplify the named MooX pages and remove refresh or reset controls that compete with create and query actions across the frontend.

**Architecture:** Apply explicit template and scoped-style edits to each affected Vue page, retaining existing load functions wherever lifecycle or mutation flows still use them. Add one repository-level Vitest source contract that documents the reviewed pages and prevents the removed toolbar patterns from returning.

**Tech Stack:** Vue 3, scoped CSS/SCSS, Arco Design Vue, Vitest, Playwright, Vite, Go statik web host

---

### Task 1: Add The Failing Page Cleanup Contract

**Files:**
- Create: `web/tests/page-toolbar-cleanup-contract.test.ts`

- [ ] **Step 1: Create source helpers and named-page assertions**

Create a Vitest suite that reads Vue files from `web/src/views` and asserts:

```ts
const hostMonitor = read('ops/host-workbench/host-monitor.vue');
expect(hostMonitor).toContain('<strong>资源状态</strong>');
expect(hostMonitor).not.toContain('主机资源状态');
expect(hostMonitor).not.toContain('lastRefreshAt');
expect(hostMonitor).not.toContain('formatAge');

const services = read('settings/service-deployments/index.vue');
expect(services).not.toContain('class="top-alert"');
expect(services).not.toContain('<icon-refresh />');
expect(services).toMatch(/\.filters\s*\{[\s\S]*?margin-bottom:\s*12px;/);

const serviceManagement = read('ops/service-management/index.vue');
expect(serviceManagement).toMatch(/\.management-content\s*\{[\s\S]*?margin-top:\s*12px;/);

const secrets = read('settings/secrets/index.vue');
expect(secrets).not.toContain('统一管理 admin 本地秘钥');
expect(secrets).not.toContain('<icon-refresh />');
expect(secrets.indexOf('<h2>秘钥管理</h2>')).toBeLessThan(secrets.indexOf('placeholder="搜索名称或描述"'));

const fields = read('data/fields/index.vue');
expect(fields).not.toContain('class="field-total"');
expect(fields).not.toContain('content="刷新"');
expect(fields).toContain('<template #icon><icon-search /></template>查询');

const sources = read('data/sources/index.vue');
expect(sources).not.toContain('<icon-refresh />');
```

- [ ] **Step 2: Add the reviewed toolbar inventory**

Use an explicit map of files and forbidden button markers:

```ts
const forbiddenToolbarMarkers = {
  'collector/cloud-node/cloud-node.vue': ['@click="reset"'],
  'collector/cloud-node/function-package-manage.vue': ['@click="resetSearch"'],
  'collector/collector-rules/collector-rules.vue': ['@click="reset"'],
  'collector/task-instances/task-instances.vue': ['@click="reset"'],
  'container/ssh-hosts/ssh-hosts.vue': ['@click="reset"'],
  'data/datasets/index.vue': ['<a-button :disabled="!selectedSpaceId" @click="load">'],
  'data/datasets/components/dataset-column-panel.vue': ['<a-button :disabled="!datasetId" @click="load">'],
  'data/datasets/components/dataset-subject-panel.vue': ['<a-button :disabled="!datasetId" @click="load">'],
  'data/factors/index.vue': ['<a-button :disabled="!selectedSpaceId" @click="load">'],
  'data/subjects/index.vue': ['<a-button :disabled="!selectedSpaceId" @click="load">'],
  'data/views/index.vue': ['<a-button :disabled="!selectedSpaceId" @click="load">'],
  'data/views/components/view-column-panel.vue': ['<a-button :disabled="!viewId" @click="load">'],
  'factor/bindings/index.vue': ['<a-button :disabled="!selectedSpaceId" @click="load">'],
  'factor/definitions/index.vue': ['<a-button @click="load">'],
  'ops/metric-monitor/index.vue': ['<a-button @click="refreshAll" :loading="loading">'],
  'ops/service-management/gateway-nodes.vue': ['aria-label="刷新节点状态"'],
  'ops/service-monitor/index.vue': ['<a-button @click="refreshAll" :loading="loading">'],
  'settings/secrets/index.vue': ['<a-button @click="load">'],
  'settings/service-deployments/index.vue': ['<a-button @click="load">'],
  'settings/spaces/index.vue': ['<a-button @click="load">'],
  'trading/account-overview/account-overview.vue': ['<a-button @click="loadAccounts">'],
};
```

Loop over the map and assert each marker is absent. Keep file-manager, host-monitor, balance-preview, position, chart, and detail refresh controls outside the inventory.

- [ ] **Step 3: Run the contract and verify it fails for the current UI**

```bash
CI=true pnpm --dir web exec vitest run tests/page-toolbar-cleanup-contract.test.ts
```

Expected: the new suite fails on the old host title, named-page copy and controls, and the reviewed toolbar markers.

### Task 2: Implement The Five Named Page Changes

**Files:**
- Modify: `web/src/views/ops/host-workbench/host-monitor.vue`
- Modify: `web/src/views/ops/service-management/index.vue`
- Modify: `web/src/views/settings/service-deployments/index.vue`
- Modify: `web/src/views/settings/secrets/index.vue`
- Modify: `web/src/views/data/fields/index.vue`
- Modify: `web/src/views/data/sources/index.vue`

- [ ] **Step 1: Simplify the host monitor status heading**

Replace the refresh-status block with:

```vue
<div class="refresh-status">
  <strong>资源状态</strong>
</div>
```

Delete `lastRefreshAt`, its assignment in `refreshData`, `formatAge`, and the CSS for `.refresh-status span`. Keep manual and automatic refresh controls.

- [ ] **Step 2: Simplify and tighten service instances**

Remove the `@click="load"` refresh button and the static `top-alert`. Change `.filters` to `margin-bottom: 12px` and the outer service-management `.management-content` to `margin-top: 12px`. Retain `reportControlError(error)` so failed requests use transient Tips.

- [ ] **Step 3: Move secret search into the title row**

Use this header structure:

```vue
<div class="page-head">
  <h2>秘钥管理</h2>
  <a-space>
    <a-input-search
      v-model="filters.keyword"
      placeholder="搜索名称或描述"
      style="width: 240px"
      allow-clear
      @search="onSearch"
    />
    <a-button type="primary" status="success" @click="openCreate">
      <template #icon><icon-plus /></template>
      新增秘钥
    </a-button>
  </a-space>
</div>
```

Leave only category and status selects in `.filter-bar` and remove subtitle-specific styles if they become unused.

- [ ] **Step 4: Add explicit field query controls**

Remove `.field-total` and the refresh tooltip/button. Add after the filter selects:

```vue
<a-button type="primary" :disabled="!selectedSpaceId" @click="commitSearch">
  <template #icon><icon-search /></template>查询
</a-button>
```

Keep `@search="commitSearch"` on the keyword input and existing filter-change behavior.

- [ ] **Step 5: Remove the data-source refresh action**

Delete the button containing `<icon-refresh />` beside `新增来源`. Keep `load()` for mount, pagination, search, and mutation reloads.

### Task 3: Apply The Repository-Wide Toolbar Rule

**Files:**
- Modify the 21 files listed in Task 1's reviewed toolbar inventory as applicable.

- [ ] **Step 1: Remove reset controls from query toolbars**

Delete the reset buttons from collector rules, task instances, cloud nodes, function package management, and SSH hosts. Remove `reset`, `resetSearch`, or equivalent functions only when `rg` confirms the template button was their final caller.

- [ ] **Step 2: Remove refresh controls from create toolbars**

Delete the reviewed refresh buttons from data views, datasets, factors, subjects, metadata panels, factor definitions and bindings, settings spaces, service monitor, metric monitor, gateway nodes, and trading account overview. Preserve their load and refresh functions when lifecycle hooks, timers, filters, mutation success paths, or other operational controls still call them.

- [ ] **Step 3: Verify no reviewed marker remains**

```bash
CI=true pnpm --dir web exec vitest run tests/page-toolbar-cleanup-contract.test.ts
```

Expected: the new contract passes.

- [ ] **Step 4: Run focused existing page contracts**

```bash
CI=true pnpm --dir web exec vitest run \
  src/views/ops/host-workbench/host-workbench-utils.test.ts \
  src/views/ops/service-management/gateway-nodes.test.ts \
  src/views/data/fields/field-workbench.test.ts \
  src/views/collector/data-management/data-management.test.ts
```

Expected: all selected test files pass.

### Task 4: Verify, Publish, And Prove The Live UI

**Files:**
- Modify: `web-host/internal/statik/statik.go` (generated)

- [ ] **Step 1: Run complete frontend verification**

```bash
CI=true pnpm --dir web test:unit
CI=true pnpm --dir web build:prod
```

Expected: all frontend unit tests pass and the production build succeeds.

- [ ] **Step 2: Run Playwright layout checks**

Verify the five named routes with a signed mocked session. Assert the removed copy and controls are absent, field query is visible, secret search shares the title row, and service filter-to-table spacing is `12px`. Capture desktop screenshots.

- [ ] **Step 3: Commit implementation and push main**

```bash
git add web/src web/tests
git commit -m "style: simplify page toolbars and notices"
git push origin HEAD:main
```

- [ ] **Step 4: Build and commit embedded assets**

```bash
make -C web-host statik
TARGET_GOOS=linux TARGET_GOARCH=amd64 ./scripts/build/build.sh web-host
git add web-host/internal/statik/statik.go
git commit -m "build: update embedded web assets"
git push origin HEAD:main
```

- [ ] **Step 5: Deploy and verify production**

Replace `/home/ubuntu/moox/prod/bin/moox-web-host`, restart it, and run `./healthcheck.sh web-host`. Repeat the Playwright checks against `https://106.53.107.122:9527`, compare local and remote binary SHA-256 values, fetch `origin/main`, and verify a clean worktree with `HEAD == origin/main`.
