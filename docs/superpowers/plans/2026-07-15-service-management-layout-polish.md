# Service Management Layout Polish Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Give service management the same title-level navigation and embedded-content spacing as the host workbench.

**Architecture:** Keep route state and dynamic child selection in `service-management/index.vue`. Replace only the page shell navigation with shared `PageTitleTabs`, then normalize the nested child shells through scoped deep selectors.

**Tech Stack:** Vue 3, TypeScript, Arco Design Vue, SCSS, Node contract checks, Vite, Go statik web host

---

### Task 1: Add A Failing Service Layout Contract

**Files:**
- Create: `web/scripts/check-service-management-contract.mjs`
- Modify: `web/package.json`

- [ ] Create the contract with these assertions:

```js
import fs from 'node:fs';
import path from 'node:path';
import process from 'node:process';

const source = fs.readFileSync(path.join(process.cwd(), 'src/views/ops/service-management/index.vue'), 'utf8');
const required = ['PageTitleTabs', 'aria-label="服务管理"', "label: '服务部署'", "label: '可用性监控'", "label: '应用指标'", 'management-content'];
const forbidden = ['<h2>服务管理</h2>', 'type="rounded"', '<a-tabs'];
const missing = required.filter((token) => !source.includes(token));
const remaining = forbidden.filter((token) => source.includes(token));
if (missing.length || remaining.length) {
  console.error(`service management layout contract failed; missing: ${missing.join(', ') || 'none'}; remaining: ${remaining.join(', ') || 'none'}`);
  process.exit(1);
}
console.log('service management frontend contract passed');
```

- [ ] Add this package script:

```json
"check:service-management": "node scripts/check-service-management-contract.mjs"
```
- [ ] Run `cd web && CI=true pnpm check:service-management` and verify it fails on the current layout.

### Task 2: Implement Shared Title Tabs And Embedded Spacing

**Files:**
- Modify: `web/src/views/ops/service-management/index.vue`
- Test: `web/scripts/check-service-management-contract.mjs`

- [ ] Import `PageTitleTabs` and define the three stable tab items:

```ts
import PageTitleTabs from '@/components/page-title-tabs/index.vue';

const tabs = [
  { key: 'deployments', label: '服务部署' },
  { key: 'availability', label: '可用性监控' },
  { key: 'metrics', label: '应用指标' },
] as const;
```

- [ ] Replace the page heading and rounded tabs with:

```vue
<PageTitleTabs :model-value="activeTab" :items="tabs" aria-label="服务管理" @change="onTabChange" />
```
- [ ] Keep the existing `keep-alive` dynamic component inside `.management-content`.
- [ ] Set `.management-content` to a 16px top margin and normalize nested shells:

```scss
.management-content {
  min-height: 0;
  flex: 1;
  margin-top: 16px;
  overflow: hidden;
}

.management-content :deep(.moox-page) {
  padding: 0;
  background: transparent;
}

.management-content :deep(.moox-page > .moox-inner) {
  padding: 0;
  border: 0;
  border-radius: 0;
  box-shadow: none;
}
```
- [ ] Run the service-management contract and `vue-tsc`; verify both pass.
- [ ] Commit with `style(ops): align service management layout`.

### Task 3: Verify, Embed, Deploy, And Inspect

**Files:**
- Modify: `web-host/internal/statik/statik.go`

- [ ] Run frontend unit tests, host-monitor contract, service-management contract, detail-page check, type checking, and production build.
- [ ] Regenerate statik assets, run `go test -count=1 ./...` in `web-host`, and build the Linux web-host binary.
- [ ] Commit embedded assets with `build(web): embed polished service management`.
- [ ] Deploy only web-host to `ubuntu@106.53.107.122` and verify signed health plus binary SHA-256 equality.
- [ ] Browser-check all three service tabs, route synchronization, nested layout, and console errors.
- [ ] Push the feature branch and verify the local and remote commit IDs match.
