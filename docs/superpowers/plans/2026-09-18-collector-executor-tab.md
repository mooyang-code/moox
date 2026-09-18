# 采集任务执行器子 Tab Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 将云节点管理并入采集任务工作台的“执行器”子 tab，同时保留旧云节点路由兼容并完成生产部署。

**Architecture:** 复用现有 `cloud-node.vue`，只扩展采集任务工作台的 tab 状态和动态组件映射；静态菜单删除独立入口，旧路由改为重定向，首页入口统一指向新 tab。后端接口、云节点业务组件和数据流保持不变。

**Tech Stack:** Vue 3 `<script setup>`、Vue Router、Vitest、Playwright、Vite、Go statik Web Host。

---

### Task 1: Lock the navigation contract with a failing test

**Files:**
- Modify: `web/src/views/collector/task-management/task-management.test.ts`
- Test: `web/src/views/collector/task-management/task-management.test.ts`

- [ ] **Step 1: Add assertions for the executor tab, route compatibility, menu removal, and home target.**

  Extend the existing task-management test to assert:

  ```ts
  expect(positions).toEqual([...positions].sort((left, right) => left - right));
  expect(normalized).toContain('executors:{label:"执行器"');
  expect(normalized).toContain('query:{tab:"executors"}');
  expect(normalizedMenu).not.toContain('menu("0301"');
  expect(normalizedRoutes).toContain('path:"/collector/cloudnodes"');
  expect(normalizedRoutes).toContain('path:"/collector/rules",query:{...to.query,tab:"executors"}');
  expect(home).toContain('path:"/collector/rules?tab=executors"');
  ```

- [ ] **Step 2: Run the focused test and confirm it fails because the executor tab and redirect do not exist yet.**

  Run:

  ```bash
  pnpm exec vitest run --config vitest.config.ts src/views/collector/task-management/task-management.test.ts
  ```

  Expected: failure in the new executor/navigation assertions.

### Task 2: Implement the unified task-management navigation

**Files:**
- Modify: `web/src/views/collector/task-management/index.vue`
- Modify: `web/src/api/modules/system/static-menu.ts`
- Modify: `web/src/router/route.ts`
- Modify: `web/src/views/home/home.vue`

- [ ] **Step 1: Add the executor component and tab state.**

  In `task-management/index.vue`, import `CloudNode` from `@/views/collector/cloud-node/cloud-node.vue`, extend `CollectorTaskTab` with `"executors"`, append `{ key: "executors", label: "执行器" }`, map `executors` to `CloudNode`, normalize the new query value, and preserve it in `onTabChange`:

  ```ts
  type CollectorTaskTab = "rules" | "instances" | "executors";

  const tabs = [
    { key: "rules", label: "采集规则" },
    { key: "instances", label: "任务实例" },
    { key: "executors", label: "执行器" }
  ] as const;

  function normalizeTab(value: unknown): CollectorTaskTab {
    return value === "instances" || value === "executors" ? value : "rules";
  }

  function onTabChange(value: string | number) {
    const tab = normalizeTab(value);
    activeTab.value = tab;
    void router.replace({ path: "/collector/rules", query: tab === "rules" ? {} : { tab } });
  }
  ```

- [ ] **Step 2: Remove the standalone menu item and redirect the old route.**

  Delete menu `0301` from `static-menu.ts`. Replace the direct cloud-node route in `route.ts` with:

  ```ts
  {
    path: "/collector/cloudnodes",
    name: "collector-cloudnodes",
    redirect: (to: RedirectLocation) => ({
      path: "/collector/rules",
      query: { ...to.query, tab: "executors" }
    }),
    meta: { title: "collector-cloudnodes" }
  }
  ```

- [ ] **Step 3: Update the home node metric link.**

  Change the node metric card path in `home.vue` to `/collector/rules?tab=executors`; keep the visible metric label “云节点”.

- [ ] **Step 4: Run the focused Vitest and confirm it passes.**

  Run the same command from Task 1. Expected: all task-management tests pass.

### Task 3: Add browser coverage for the merged tab and legacy redirect

**Files:**
- Modify: `web/tests/data-collection-navigation.spec.ts`

- [ ] **Step 1: Add an E2E test for the executor tab and legacy URL.**

  Add a test that opens `/collector/rules?tab=executors`, checks the selected tab “执行器” and the “云节点” heading, then opens `/collector/cloudnodes` and checks the same final state. The existing gateway fallback responses are sufficient because the test only verifies navigation and the page heading.

- [ ] **Step 2: Run the collection navigation Playwright suite.**

  Run:

  ```bash
  pnpm exec playwright test tests/data-collection-navigation.spec.ts
  ```

  Expected: all tests pass, including the new executor/legacy-route test.

### Task 4: Build, deploy, and verify

**Files:**
- Modify: `docs/superpowers/specs/2026-09-18-collector-executor-tab-design.md`
- Modify: `docs/superpowers/plans/2026-09-18-collector-executor-tab.md`

- [ ] **Step 1: Run final frontend checks.**

  Run:

  ```bash
  pnpm exec vitest run --config vitest.config.ts src/views/collector/task-management/task-management.test.ts
  pnpm exec playwright test tests/data-collection-navigation.spec.ts
  pnpm build:prod
  ```

- [ ] **Step 2: Embed the production bundle and compile Web Host.**

  From repository root run:

  ```bash
  go run github.com/rakyll/statik@v0.1.7 -src=./web/dist -dest=./web-host/internal
  TARGET_GOOS=linux TARGET_GOARCH=amd64 ./scripts/build/build.sh web-host
  ```

- [ ] **Step 3: Commit the source, tests, and docs, then push `feature/mooyang`.**

  Stage only the task-management feature files and docs; preserve unrelated dirty files. Use:

  ```bash
  git commit -m "feat(web): merge cloud nodes into collection tasks"
  git push origin feature/mooyang
  ```

- [ ] **Step 4: Deploy and verify the remote Web Host.**

  Upload `bin/moox-web-host` to the configured remote host, verify local/remote SHA-256 equality, keep the previous binary as a rollback copy, restart `web-host`, check health HTTP 200, and use the production browser page to verify the “执行器” tab and legacy URL redirect.
