# Storage Page Tab Actions Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Move the three storage management page toolbars into the global route tab row so the empty toolbar band disappears without changing page behavior.

**Architecture:** Add a reusable `PageTabActions` component that teleports page-owned controls into a neutral mount point in the global `Tabs` component. The page keeps every callback and state value; the layout owns only placement. When route tabs are disabled, `PageTabActions` renders the same controls inline as a fallback.

**Tech Stack:** Vue 3.5 Teleport, Pinia, Arco Design Vue, TypeScript, Vitest, Vue Test Utils, Vite, Playwright browser verification, Go `statik` web-host packaging.

---

## File Map

- Create `web/src/components/page-tab-actions/index.vue`: place page-owned actions in the global tab row or inline fallback.
- Create `web/src/components/page-tab-actions/index.test.ts`: verify teleport, fallback, and runtime tab-setting changes.
- Modify `web/src/layout/components/Tabs/index.vue`: expose the mount point and reserve responsive space for it.
- Create `web/tests/storage-page-tab-actions-contract.test.ts`: lock the three storage pages to the shared action container and reject the obsolete toolbar row.
- Modify `web/src/views/ops/storage/nodes.vue`: move refresh and create controls into `PageTabActions`.
- Modify `web/src/views/ops/storage/routes.vue`: move refresh and create controls into `PageTabActions`.
- Modify `web/src/views/ops/storage/archive.vue`: move filter, debug switch, and refresh into `PageTabActions`; make the filter width responsive.
- Regenerate `web-host/internal/statik/statik.go`: embed the verified production frontend bundle.

### Task 1: Build the Reusable Page Action Portal

**Files:**
- Create: `web/src/components/page-tab-actions/index.vue`
- Create: `web/src/components/page-tab-actions/index.test.ts`

- [ ] **Step 1: Write the failing component tests**

Create `web/src/components/page-tab-actions/index.test.ts`:

```ts
import { enableAutoUnmount, mount } from '@vue/test-utils';
import { createPinia, setActivePinia } from 'pinia';
import { defineComponent, nextTick, shallowRef } from 'vue';
import { afterEach, beforeEach, describe, expect, it } from 'vitest';
import { useThemeConfig } from '@/store/modules/theme-config';
import PageTabActions from './index.vue';

enableAutoUnmount(afterEach);

describe('PageTabActions', () => {
  beforeEach(() => {
    document.body.innerHTML = '<div id="page-tab-actions"></div>';
    setActivePinia(createPinia());
  });

  it('teleports actions into the global tab row when route tabs are enabled', async () => {
    useThemeConfig().isTabs = true;
    const wrapper = mount(PageTabActions, {
      slots: { default: '<button data-test="action">新增节点</button>' },
    });
    await nextTick();

    expect(document.querySelector('#page-tab-actions [data-test="action"]')).not.toBeNull();
    expect(wrapper.find('[data-test="action"]').exists()).toBe(false);
  });

  it('renders actions inline when route tabs are disabled', () => {
    useThemeConfig().isTabs = false;
    const wrapper = mount(PageTabActions, {
      slots: { default: '<button data-test="action">新增节点</button>' },
    });

    expect(wrapper.find('[data-test="action"]').exists()).toBe(true);
    expect(document.querySelector('#page-tab-actions [data-test="action"]')).toBeNull();
  });

  it('moves actions when the route tab setting changes', async () => {
    const theme = useThemeConfig();
    theme.isTabs = true;
    const wrapper = mount(PageTabActions, {
      slots: { default: '<button data-test="action">新增节点</button>' },
    });
    await nextTick();

    theme.isTabs = false;
    await nextTick();

    expect(wrapper.find('[data-test="action"]').exists()).toBe(true);
    expect(document.querySelector('#page-tab-actions [data-test="action"]')).toBeNull();
  });

  it('removes actions when a kept-alive page is deactivated', async () => {
    useThemeConfig().isTabs = true;
    const StoragePage = defineComponent({
      components: { PageTabActions },
      template: '<PageTabActions><button data-test="storage-action">新增节点</button></PageTabActions>',
    });
    const OtherPage = defineComponent({ template: '<div>其他页面</div>' });
    const activePage = shallowRef(StoragePage);
    mount(defineComponent({
      setup: () => ({ activePage }),
      template: '<KeepAlive><component :is="activePage" /></KeepAlive>',
    }));
    await nextTick();

    expect(document.querySelector('#page-tab-actions [data-test="storage-action"]')).not.toBeNull();
    activePage.value = OtherPage;
    await nextTick();

    expect(document.querySelector('#page-tab-actions [data-test="storage-action"]')).toBeNull();
  });
});
```

- [ ] **Step 2: Run the focused test and verify failure**

Run:

```bash
pnpm --dir web test:unit -- src/components/page-tab-actions/index.test.ts
```

Expected: FAIL because `web/src/components/page-tab-actions/index.vue` does not exist.

- [ ] **Step 3: Implement the minimal portal component**

Create `web/src/components/page-tab-actions/index.vue`:

```vue
<template>
  <Teleport v-if="isTabs" defer to="#page-tab-actions">
    <div class="page-tab-actions page-tab-actions--teleported">
      <slot />
    </div>
  </Teleport>
  <div v-else class="page-tab-actions page-tab-actions--inline">
    <slot />
  </div>
</template>

<script setup lang="ts">
import { storeToRefs } from 'pinia';
import { useThemeConfig } from '@/store/modules/theme-config';

defineOptions({ name: 'PageTabActions' });

const { isTabs } = storeToRefs(useThemeConfig());
</script>

<style scoped>
.page-tab-actions {
  display: flex;
  align-items: center;
  min-width: 0;
}

.page-tab-actions--inline {
  justify-content: flex-end;
  margin-bottom: 14px;
}
</style>
```

- [ ] **Step 4: Run the focused test and verify success**

Run:

```bash
pnpm --dir web test:unit -- src/components/page-tab-actions/index.test.ts
```

Expected: 4 tests PASS.

- [ ] **Step 5: Commit the portal component**

```bash
git add web/src/components/page-tab-actions/index.vue web/src/components/page-tab-actions/index.test.ts
git commit -m "feat(web): add page tab action portal"
```

### Task 2: Add the Global Tab Row Mount Point

**Files:**
- Modify: `web/src/layout/components/Tabs/index.vue`

- [ ] **Step 1: Add the neutral mount point between route tabs and global controls**

Change the end of the `Tabs` template from:

```vue
    </a-tabs>
    <div class="tabs_setting">
```

to:

```vue
    </a-tabs>
    <div id="page-tab-actions" class="tabs_page_actions" aria-label="当前页面操作"></div>
    <div class="tabs_setting">
```

- [ ] **Step 2: Add stable responsive sizing and a scope divider**

Add these rules inside the existing scoped style:

```scss
.tabs {
  // Keep the existing declarations.

  .tabs_page_actions {
    display: flex;
    flex: 0 0 auto;
    align-items: center;
    min-width: 0;

    &:not(:empty) {
      padding-left: 12px;
      margin-left: 12px;
      border-left: $border-1 solid $color-border-2;
    }
  }
}

:deep(.arco-tabs) {
  flex: 1 1 auto;
  min-width: 0;
}
```

Keep `tabs_setting` after the page action mount point. Do not remove the existing global refresh or tab management controls.

- [ ] **Step 3: Run the portal tests against the real target selector**

Run:

```bash
pnpm --dir web test:unit -- src/components/page-tab-actions/index.test.ts
```

Expected: 4 tests PASS.

- [ ] **Step 4: Run type checking through the development build**

Run:

```bash
pnpm --dir web build:dev
```

Expected: `vue-tsc` and Vite finish successfully.

- [ ] **Step 5: Commit the layout mount point**

```bash
git add web/src/layout/components/Tabs/index.vue
git commit -m "feat(web): host page actions in route tabs"
```

### Task 3: Move All Storage Page Controls

**Files:**
- Create: `web/tests/storage-page-tab-actions-contract.test.ts`
- Modify: `web/src/views/ops/storage/nodes.vue`
- Modify: `web/src/views/ops/storage/routes.vue`
- Modify: `web/src/views/ops/storage/archive.vue`

- [ ] **Step 1: Write the failing storage page contract test**

Create `web/tests/storage-page-tab-actions-contract.test.ts`:

```ts
import { readFileSync } from 'node:fs';
import { resolve } from 'node:path';
import { describe, expect, it } from 'vitest';

const root = resolve(__dirname, '..');
const page = (name: string) => readFileSync(resolve(root, `src/views/ops/storage/${name}.vue`), 'utf8');

describe('storage page tab actions', () => {
  it.each(['nodes', 'routes', 'archive'])('%s uses the shared tab action container', (name) => {
    const source = page(name);
    expect(source).toContain("import PageTabActions from '@/components/page-tab-actions/index.vue'");
    expect(source).toContain('<PageTabActions>');
    expect(source).not.toContain('class="page-toolbar"');
  });

  it('keeps every page action available after moving the controls', () => {
    expect(page('nodes')).toMatch(/刷新节点列表[\s\S]*新增节点/);
    expect(page('routes')).toMatch(/刷新路由列表[\s\S]*新增路由/);
    expect(page('archive')).toMatch(/datasetFilter[\s\S]*debugMode[\s\S]*刷新归档列表/);
  });
});
```

- [ ] **Step 2: Run the contract test and verify failure**

Run:

```bash
pnpm --dir web test:unit -- tests/storage-page-tab-actions-contract.test.ts
```

Expected: FAIL because the three pages still use `page-toolbar`.

- [ ] **Step 3: Move the node page actions**

In `web/src/views/ops/storage/nodes.vue`, replace the `page-toolbar` wrapper with:

```vue
    <PageTabActions>
      <a-space>
        <a-tooltip content="刷新节点列表">
          <a-button aria-label="刷新节点列表" @click="load">
            <template #icon><icon-refresh /></template>
          </a-button>
        </a-tooltip>
        <a-button type="primary" @click="openCreate">
          <template #icon><icon-plus /></template>
          新增节点
        </a-button>
      </a-space>
    </PageTabActions>
```

Add the import:

```ts
import PageTabActions from '@/components/page-tab-actions/index.vue';
```

Delete the obsolete `.page-toolbar` style block. Keep `.topology-alert { margin-bottom: 14px; }`.

- [ ] **Step 4: Move the route page actions**

In `web/src/views/ops/storage/routes.vue`, replace the `page-toolbar` wrapper with:

```vue
    <PageTabActions>
      <a-space>
        <a-tooltip content="刷新路由列表">
          <a-button aria-label="刷新路由列表" :disabled="!selectedSpaceId" @click="load">
            <template #icon><icon-refresh /></template>
          </a-button>
        </a-tooltip>
        <a-button type="primary" :disabled="!selectedSpaceId" @click="openCreate">
          <template #icon><icon-plus /></template>
          新增路由
        </a-button>
      </a-space>
    </PageTabActions>
```

Add the import:

```ts
import PageTabActions from '@/components/page-tab-actions/index.vue';
```

Delete the obsolete `.page-toolbar` style block.

- [ ] **Step 5: Move the archive page actions and make the filter responsive**

In `web/src/views/ops/storage/archive.vue`, replace the `page-toolbar` wrapper with:

```vue
    <PageTabActions>
      <a-space>
        <a-input v-model="datasetFilter" class="archive-dataset-filter" allow-clear placeholder="dataset_id" />
        <a-switch v-model="debugMode" size="small">
          <template #checked>调试</template>
          <template #unchecked>调试</template>
        </a-switch>
        <a-tooltip content="刷新归档列表">
          <a-button aria-label="刷新归档列表" :disabled="!selectedSpaceId" @click="load">
            <template #icon><icon-refresh /></template>
          </a-button>
        </a-tooltip>
      </a-space>
    </PageTabActions>
```

Add the import:

```ts
import PageTabActions from '@/components/page-tab-actions/index.vue';
```

Replace the obsolete `.page-toolbar` style with:

```css
.archive-dataset-filter {
  width: 180px;
}

@media (max-width: 960px) {
  .archive-dataset-filter {
    width: 128px;
  }
}
```

- [ ] **Step 6: Run the component and contract tests**

Run:

```bash
pnpm --dir web test:unit -- src/components/page-tab-actions/index.test.ts tests/storage-page-tab-actions-contract.test.ts
```

Expected: 8 tests PASS: 4 portal tests, 3 parameterized page tests, and 1 explicit action-preservation test.

- [ ] **Step 7: Commit the storage page migration**

```bash
git add web/tests/storage-page-tab-actions-contract.test.ts web/src/views/ops/storage/nodes.vue web/src/views/ops/storage/routes.vue web/src/views/ops/storage/archive.vue
git commit -m "refactor(web): compact storage page actions"
```

### Task 4: Verify Layout, Behavior, and Production Build

**Files:**
- No source files expected.

- [ ] **Step 1: Run the complete frontend test suite fresh**

Run:

```bash
CI=true pnpm --dir web test:unit
```

Expected: all Vitest suites PASS with no unhandled errors.

- [ ] **Step 2: Build the production frontend**

Run:

```bash
CI=true pnpm --dir web build:prod
```

Expected: `vue-tsc` and the production Vite build finish successfully.

- [ ] **Step 3: Start a local preview server**

Run:

```bash
pnpm --dir web exec vite preview --host 127.0.0.1 --port 4173
```

Expected: Vite reports `http://127.0.0.1:4173/`. Keep the process running for browser checks.

- [ ] **Step 4: Verify the three pages at desktop width**

Use Playwright or the persistent browser at `1440x900`. Open these routes after signing in:

```text
http://127.0.0.1:4173/#/ops/storage/nodes
http://127.0.0.1:4173/#/ops/storage/routes
http://127.0.0.1:4173/#/ops/storage/archive
```

For every page, verify:

- The page actions appear between route tabs and global tab controls.
- The old empty toolbar row is absent.
- The alert or table begins directly below the content padding.
- Refresh, create, filter, and debug interactions retain their previous behavior.
- No label, input, switch, or button overlaps another element.

- [ ] **Step 5: Verify responsive and fallback layouts**

Repeat at `800x700` and `390x844`. Confirm the route tab list shrinks or scrolls, action controls remain visible, and the page does not gain a second tab-row line. Turn off the global tab bar in system settings and confirm the same page actions render inline above the alert or table.

- [ ] **Step 6: Inspect browser console and layout geometry**

Confirm the console has no Teleport target warnings. Use `getBoundingClientRect()` for the tab row, page action container, and first content element; verify the rectangles do not overlap and the removed toolbar does not reserve height.

### Task 5: Review, Package, Publish, and Prove the Live UI

**Files:**
- Regenerate: `web-host/internal/statik/statik.go`

- [ ] **Step 1: Review the final diff for scope and lifecycle risks**

Run:

```bash
git diff origin/main...HEAD -- web/src/components/page-tab-actions web/src/layout/components/Tabs web/src/views/ops/storage web/tests/storage-page-tab-actions-contract.test.ts
```

Check that inactive `keep-alive` pages cannot leave teleported controls behind, the inline fallback remains functional, global tab controls are unchanged, and no unrelated page was modified.

- [ ] **Step 2: Regenerate embedded web assets**

Run:

```bash
make -C web-host statik
```

Expected: `web-host/internal/statik/statik.go` contains the current `web/dist` bundle.

- [ ] **Step 3: Build the Linux web-host binary**

Run:

```bash
TARGET_GOOS=linux TARGET_GOARCH=amd64 ./scripts/build/build.sh web-host
```

Expected: `bin/moox-web-host` is an x86-64 Linux executable.

- [ ] **Step 4: Commit the embedded frontend**

```bash
git add web-host/internal/statik/statik.go
git commit -m "build(web-host): embed compact storage UI"
```

- [ ] **Step 5: Push the completed commits**

Run:

```bash
git push origin HEAD:main
```

Expected: the remote `main` branch advances to `HEAD`. If the push is rejected, fetch and integrate the new remote commits without discarding local or user changes, rerun the proving tests, and push again.

- [ ] **Step 6: Upload and restart web-host**

Using the configured remote SSH credentials, upload `bin/moox-web-host` to a temporary path on `ubuntu@106.53.107.122`. In `/home/ubuntu/moox/prod`, run:

```bash
./stop.sh web-host
install -m 0755 /tmp/moox-web-host.storage-tab-actions bin/moox-web-host
./start.sh web-host
./healthcheck.sh web-host
```

Expected: web-host restarts and the signed health check passes.

- [ ] **Step 7: Verify the deployed browser surface**

Open the live routes at desktop and narrow widths:

```text
https://106.53.107.122:9527/#/ops/storage/nodes
https://106.53.107.122:9527/#/ops/storage/routes
https://106.53.107.122:9527/#/ops/storage/archive
```

Confirm the old empty toolbar band is absent on all three pages, actions remain usable, content does not overlap, and browser console/API requests show no new failures.

- [ ] **Step 8: Prove repository synchronization**

Run:

```bash
git fetch origin
test "$(git rev-parse HEAD)" = "$(git rev-parse origin/main)"
git status --short --branch
```

Expected: `HEAD` equals `origin/main`; only pre-existing unrelated user changes may remain in the original working tree.
