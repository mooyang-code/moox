# Host Workbench Layout Polish Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Align the host workbench with the data-view page hierarchy and remove horizontal scrolling from the three host-detail tables.

**Architecture:** Reuse the shared `PageTitleTabs` component for page-level navigation, leaving route state in the existing host-workbench container. Keep detail rendering inside `HostMonitorDetail`, but replace fixed-width scrolling tables with fixed-layout responsive tables and explicit column sizing.

**Tech Stack:** Vue 3, TypeScript, Arco Design Vue, SCSS, Vitest, Vite, Go statik web host

---

### Task 1: Lock The Layout Contract

**Files:**
- Modify: `web/scripts/check-host-monitor-contract.mjs`

- [ ] **Step 1: Add failing title-tab assertions**

Add required tokens for the shared title tabs and forbidden tokens for the old page header and rounded tabs:

```js
const required = [
  // existing tokens
  'PageTitleTabs',
  'aria-label="主机工作台"',
];

const removedLayoutTokens = [
  '<h2>主机工作台</h2>',
  'type="rounded"',
  'min-width:460px',
  'overflow:auto',
];
```

- [ ] **Step 2: Run the contract and verify RED**

Run: `cd web && CI=true pnpm check:host-monitor`

Expected: FAIL listing the missing `PageTitleTabs` contract and remaining old layout tokens.

- [ ] **Step 3: Commit the failing contract with the implementation in Task 2**

Do not create a red-only commit; keep the working tree ready for the minimal implementation.

### Task 2: Replace Rounded Tabs With Shared Page Title Tabs

**Files:**
- Modify: `web/src/views/ops/host-workbench/index.vue`
- Modify: `web/scripts/check-host-monitor-contract.mjs`

- [ ] **Step 1: Replace the page heading and Arco tabs**

Import and render the shared component, then conditionally render the existing tab contents:

```vue
<PageTitleTabs
  :model-value="activeTab"
  :items="tabs"
  aria-label="主机工作台"
  @change="onTabChange"
/>

<SshHosts
  v-if="activeTab === 'hosts'"
  embedded
  :monitor-by-host-id="monitorByHostId"
  :monitor-only-hosts="monitorOnlyHosts"
  @connect="openTerminal"
  @file-manage="openFileManager"
/>
<HostMonitor v-else />
```

Define the stable tab items beside `HostTab`:

```ts
const tabs = [
  { key: 'hosts', label: '主机列表' },
  { key: 'monitor', label: '主机监控' },
] as const;
```

Remove the standalone page heading, description, `a-tabs`, and obsolete header CSS. Add a `workbench-content` wrapper with `margin-top: 16px` so the content rhythm matches data-view pages.

- [ ] **Step 2: Run the contract and type checker**

Run: `cd web && CI=true pnpm check:host-monitor`

Expected: PASS with `host monitor frontend contract passed` after Task 3 removes the remaining table tokens; until then, only table-layout assertions may fail.

Run: `cd web && CI=true pnpm exec vue-tsc --noEmit`

Expected: exit 0.

### Task 3: Fit Detail Tables Without Horizontal Scrolling

**Files:**
- Modify: `web/src/views/ops/host-workbench/host-monitor-detail.vue`
- Modify: `web/scripts/check-host-monitor-contract.mjs`

- [ ] **Step 1: Add table-specific classes and value titles**

Give each table a class and expose truncated cell content through native titles:

```vue
<table class="detail-table filesystem-table">...</table>
<table class="detail-table disk-table">...</table>
<table class="detail-table network-table">...</table>
```

Apply `:title` to device, rate, mountpoint, filesystem type, and interface cells whose values may be truncated.

- [ ] **Step 2: Replace scroll CSS with fixed layout**

Use this responsive behavior:

```scss
.table-scroll {
  max-height: 300px;
  overflow-y: auto;
  overflow-x: hidden;
  border-top: 1px solid var(--color-border-2);
}

.detail-table {
  width: 100%;
  table-layout: fixed;
  border-collapse: collapse;
  font-size: 11px;
}

th,
td {
  padding: 9px 5px;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}
```

Set table-specific column proportions with `th:nth-child(...)` rules so identifiers receive more width than numeric values. Keep the existing breakpoint that stacks `.tables-grid` into one column below 1180px.

- [ ] **Step 3: Run GREEN contract checks**

Run: `cd web && CI=true pnpm check:host-monitor`

Expected: `host monitor frontend contract passed`.

Run: `cd web && CI=true pnpm check:detail-pages`

Expected: `detail page style ok`.

- [ ] **Step 4: Commit the UI implementation**

```bash
git add web/src/views/ops/host-workbench/index.vue \
  web/src/views/ops/host-workbench/host-monitor-detail.vue \
  web/scripts/check-host-monitor-contract.mjs
git commit -m "style(ops): align host workbench layout"
```

### Task 4: Verify, Embed, Deploy, And Inspect

**Files:**
- Modify: `web-host/internal/statik/statik.go`

- [ ] **Step 1: Run the full frontend verification**

Run: `cd web && CI=true pnpm test -- --run`

Expected: all test files and tests pass.

Run: `cd web && CI=true pnpm exec vue-tsc --noEmit`

Expected: exit 0.

Run: `cd web && CI=true pnpm run build:prod`

Expected: Vite production build succeeds; existing dependency-age and chunk-size warnings are acceptable.

- [ ] **Step 2: Regenerate embedded assets and verify web-host**

Run: `cd web-host && make statik`

Run: `cd web-host && go test -count=1 ./...`

Expected: all web-host packages pass.

Run: `TARGET_GOOS=linux TARGET_GOARCH=amd64 ./scripts/build.sh web-host`

Expected: `bin/moox-web-host` is produced.

- [ ] **Step 3: Commit embedded assets**

```bash
git add web-host/internal/statik/statik.go
git commit -m "build(web): embed polished host workbench"
```

- [ ] **Step 4: Deploy only web-host**

Copy the Linux binary to `ubuntu@106.53.107.122`, replace `/home/ubuntu/moox/prod/bin/moox-web-host` atomically with a timestamped backup, and restart only `web-host` through the production scripts with `MOOX_WITH_WEB_HOST=1`.

Expected: signed `./healthcheck.sh web-host` succeeds and the remote SHA-256 equals the local binary.

- [ ] **Step 5: Browser verification**

Open `https://106.53.107.122:9527/#/ops/hosts?tab=monitor` and verify:

- Title-level tabs match `/#/collector/views`.
- The standalone title and rounded tabs are gone.
- The selected-host detail uses the page's standard horizontal margins.
- `scrollWidth === clientWidth` for filesystem, disk I/O, and network wrappers at the desktop viewport.
- History chart and live host data remain visible.
- Browser console error list is empty.

- [ ] **Step 6: Push and audit**

Push `feature/frontend-service-host-workbench`, then verify `HEAD` equals `origin/feature/frontend-service-host-workbench` and the worktree is clean.
