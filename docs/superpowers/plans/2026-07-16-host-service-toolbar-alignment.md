# Host And Service Toolbar Alignment Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Align the default host and service list pages around one compact, create-first toolbar layout.

**Architecture:** Keep search and action state in the existing child components and merge only their template rows. Use local scoped styles plus matching `12px` parent content spacing; no shared component or state lifting is needed.

**Tech Stack:** Vue 3, Arco Design Vue, scoped SCSS/CSS, Vitest, Playwright, Vite, Go statik web host

---

### Task 1: Add A Failing Toolbar Layout Contract

**Files:**
- Create: `web/tests/host-service-toolbar-alignment-contract.test.ts`

- [ ] **Step 1: Assert the host control order and spacing**

Read `container/ssh-hosts/ssh-hosts.vue` and `ops/host-workbench/index.vue`, then assert:

```ts
expect(hosts).toContain('class="host-list-toolbar"');
expect(hosts).not.toContain('<!-- 筛选区域 -->');
expect(hosts).not.toContain('<!-- 操作按钮区域 -->');
expect(hosts.indexOf('新增主机')).toBeLessThan(hosts.indexOf('搜索主机名称或地址'));
expect(hosts.indexOf('搜索主机名称或地址')).toBeLessThan(hosts.indexOf('<span>查询</span>'));
expect(hosts.indexOf('<span>查询</span>')).toBeLessThan(hosts.indexOf('<span>批量删除</span>'));
expect(hosts).toMatch(/\.host-list-toolbar\s*\{[\s\S]*?margin-bottom:\s*12px;/);
expect(hostWorkbench).toMatch(/\.workbench-content\s*\{[\s\S]*?margin-top:\s*12px;/);
```

- [ ] **Step 2: Assert the gateway-node control order and spacing**

Read `ops/service-management/gateway-nodes.vue` and assert:

```ts
expect(gateway).toContain('class="toolbar"');
expect(gateway.indexOf('新增节点')).toBeLessThan(gateway.indexOf('placeholder="节点 ID"'));
expect(gateway.indexOf('placeholder="节点 ID"')).toBeLessThan(gateway.indexOf('placeholder="配置状态"'));
expect(gateway.indexOf('placeholder="配置状态"')).toBeLessThan(gateway.indexOf('>查询</a-button>'));
expect(gateway).toMatch(/\.toolbar\s*\{[\s\S]*?margin-bottom:\s*12px;/);
expect(gateway).not.toContain('justify-content: space-between');
```

- [ ] **Step 3: Run the contract and verify failure**

```bash
CI=true pnpm --dir web exec vitest run tests/host-service-toolbar-alignment-contract.test.ts
```

Expected: the suite fails because host controls remain split, gateway filters precede create, and spacing is not uniformly `12px`.

### Task 2: Merge And Align The Toolbars

**Files:**
- Modify: `web/src/views/container/ssh-hosts/ssh-hosts.vue`
- Modify: `web/src/views/ops/host-workbench/index.vue`
- Modify: `web/src/views/ops/service-management/gateway-nodes.vue`

- [ ] **Step 1: Merge the host list rows**

Replace the two top template blocks with:

```vue
<a-space class="host-list-toolbar" wrap>
  <a-button type="primary" status="success" @click="onAdd">
    <template #icon><icon-plus /></template>
    <span>新增主机</span>
  </a-button>
  <a-input
    v-model="keyword"
    placeholder="搜索主机名称或地址"
    allow-clear
    style="width: 280px"
    @press-enter="onSearch"
    @clear="onSearch"
  />
  <a-button type="primary" @click="onSearch">
    <template #icon><icon-search /></template>
    <span>查询</span>
  </a-button>
  <a-button type="primary" status="danger" :disabled="selectedKeys.length === 0" @click="batchDelete">
    <template #icon><icon-delete /></template>
    <span>批量删除</span>
  </a-button>
</a-space>
```

Add `.host-list-toolbar { margin-bottom: 12px; }`, remove the obsolete two-pixel Arco row spacing override, and set host `.workbench-content` to `margin-top: 12px`.

- [ ] **Step 2: Reorder and merge the gateway-node toolbar**

Use one wrapping Space in this order:

```vue
<a-space class="toolbar" wrap>
  <a-button type="primary" status="success" @click="openCreate">
    <template #icon><icon-plus /></template>
    新增节点
  </a-button>
  <a-input v-model="filters.node_id" allow-clear placeholder="节点 ID" @press-enter="reloadFirstPage" />
  <a-select v-model="filters.status" allow-clear placeholder="配置状态" class="status-filter" @change="reloadFirstPage">
    <a-option value="enabled">enabled</a-option>
    <a-option value="disabled">disabled</a-option>
  </a-select>
  <a-button @click="reloadFirstPage">查询</a-button>
</a-space>
```

Set `.toolbar` to `margin-bottom: 12px` without `justify-content: space-between`; keep `gap: 12px` and wrapping behavior.

- [ ] **Step 3: Run the contract and focused tests**

```bash
CI=true pnpm --dir web exec vitest run \
  tests/host-service-toolbar-alignment-contract.test.ts \
  src/views/ops/host-workbench/host-workbench-utils.test.ts \
  src/views/ops/service-management/gateway-nodes.test.ts
```

Expected: all selected files pass.

### Task 3: Verify, Publish, And Prove The Live Layout

**Files:**
- Modify: `web-host/internal/statik/statik.go` (generated)

- [ ] **Step 1: Run full frontend verification**

```bash
CI=true pnpm --dir web test:unit
CI=true pnpm --dir web build:prod
```

Expected: every frontend unit test passes and the production build succeeds.

- [ ] **Step 2: Verify desktop and narrow layouts in Playwright**

Open `/ops/hosts` and `/ops/services` with a signed mocked session. Assert create appears before search, controls share one toolbar row at desktop width, toolbar-to-table gaps are `12px`, and narrow layouts wrap without overlap. Capture screenshots.

- [ ] **Step 3: Commit and push implementation**

```bash
git add web/src web/tests/host-service-toolbar-alignment-contract.test.ts
git commit -m "style: align host and service toolbars"
git push origin HEAD:main
```

- [ ] **Step 4: Build and commit embedded assets**

```bash
make -C web-host statik
TARGET_GOOS=linux TARGET_GOARCH=amd64 ./scripts/build.sh web-host
git add web-host/internal/statik/statik.go
git commit -m "build: update embedded web assets"
git push origin HEAD:main
```

- [ ] **Step 5: Deploy and verify production**

Replace the production `moox-web-host`, restart it, and run its health check. Repeat Playwright checks against `https://106.53.107.122:9527`, compare local and remote binary SHA-256 values, fetch `origin/main`, and confirm a clean worktree with matching revisions.
