# Cloud Node Toolbar Layout Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Rebuild the cloud node page header as a standalone title followed by separate action and filter toolbars, without changing any existing behavior.

**Architecture:** Keep all work inside the existing cloud node Vue view and its layout contract test. Reorder the existing controls into two Arco `a-space` toolbars, then use scoped CSS to enforce left alignment, wrapping, `8px` vertical rhythm, bounded width, and mobile scrolling.

**Tech Stack:** Vue 3, TypeScript, Arco Design Vue, scoped CSS, Vitest, Vite, Playwright browser verification

---

## File Structure

- Modify `web/src/views/collector/cloud-node/cloud-node.vue`: reorganize the title, action controls, filter controls, and responsive styles.
- Modify `web/tests/page-layout-standard-contract.test.ts`: encode the selected B layout and spacing rules as source-level layout contracts.
- Regenerate `web-host/internal/statik/statik.go`: embed the verified production frontend for deployment.

### Task 1: Add the B-layout contract

**Files:**
- Modify: `web/tests/page-layout-standard-contract.test.ts`

- [ ] **Step 1: Replace the old single-toolbar assertions with the selected layout contract**

In the `normalizes special list workbenches` test, replace the cloud-node assertions with:

```ts
expect(cloudNodes).toContain('<h2>云节点</h2>');
expect(cloudNodes).toContain('class="cloud-node-action-bar"');
expect(cloudNodes).toContain('class="cloud-node-filter-bar"');
expect(cloudNodes).not.toContain('class="cloud-node-toolbar"');
expect(cloudNodes).not.toContain('.moox-inner .a-row');

const titleIndex = cloudNodes.indexOf('<h2>云节点</h2>');
const actionIndex = cloudNodes.indexOf('class="cloud-node-action-bar"');
const filterIndex = cloudNodes.indexOf('class="cloud-node-filter-bar"');
const tableIndex = cloudNodes.indexOf('<a-table');
expect(titleIndex).toBeLessThan(actionIndex);
expect(actionIndex).toBeLessThan(filterIndex);
expect(filterIndex).toBeLessThan(tableIndex);

expect(cloudNodes.indexOf('批量新增')).toBeLessThan(filterIndex);
expect(cloudNodes.indexOf('批量部署')).toBeLessThan(filterIndex);
expect(cloudNodes.indexOf('批量删除')).toBeLessThan(filterIndex);
expect(cloudNodes.indexOf('云账户管理')).toBeLessThan(filterIndex);
expect(cloudNodes.indexOf('代码包版本')).toBeLessThan(filterIndex);
expect(cloudNodes.indexOf('placeholder="请选择云账户"')).toBeGreaterThan(filterIndex);
expect(cloudNodes.indexOf('placeholder="请输入节点ID"')).toBeGreaterThan(filterIndex);

expectMargin(cloudNodes, '.page-head', 'margin-bottom', 8);
expectMargin(cloudNodes, '.cloud-node-action-bar', 'margin-bottom', 8);
expectMargin(cloudNodes, '.cloud-node-filter-bar', 'margin-bottom', 8);
expect(cloudNodes).toMatch(/\.moox-page\s*\{[\s\S]*?overflow-y:\s*auto;/);
```

- [ ] **Step 2: Run the focused contract test and confirm it fails**

Run:

```bash
CI=true pnpm --dir web exec vitest run --config vitest.config.ts tests/page-layout-standard-contract.test.ts
```

Expected: FAIL because `cloud-node-action-bar` and `cloud-node-filter-bar` do not exist yet.

### Task 2: Split the cloud-node header into three rows

**Files:**
- Modify: `web/src/views/collector/cloud-node/cloud-node.vue`
- Test: `web/tests/page-layout-standard-contract.test.ts`

- [ ] **Step 1: Make the title a standalone row**

Replace the current opening page-head structure with:

```vue
<div class="page-head">
  <h2>云节点</h2>
</div>
```

The title container must close before either toolbar starts.

- [ ] **Step 2: Add the fixed action toolbar below the title**

Move the five existing action buttons, preserving their attributes, events, icons, labels, and order, into:

```vue
<a-space class="cloud-node-action-bar" wrap>
  <a-button type="primary" status="success" :disabled="batchChangeProcessing" @click="onBatchAdd">
    <template #icon><icon-plus-circle /></template>
    <span>批量新增</span>
  </a-button>
  <a-button type="primary" status="warning" :disabled="batchChangeProcessing" @click="batchDeploy">
    <template #icon><icon-upload /></template>
    <span>批量部署</span>
  </a-button>
  <a-button type="primary" status="danger" :disabled="batchChangeProcessing" @click="batchDelete">
    <template #icon><icon-delete /></template>
    <span>批量删除</span>
  </a-button>
  <a-button type="outline" @click="onCloudAccountManage">
    <template #icon><icon-settings /></template>
    <span>云账户管理</span>
  </a-button>
  <a-button type="outline" @click="onFunctionPackageManage">
    <template #icon><icon-code /></template>
    <span>代码包版本</span>
  </a-button>
</a-space>
```

Do not change `batchChangeProcessing`, `onBatchAdd`, `batchDeploy`, `batchDelete`, `onCloudAccountManage`, or `onFunctionPackageManage`.

- [ ] **Step 3: Add the filter toolbar below the action toolbar**

Move the existing cloud account, node ID, region, node type, and status fields plus the blue query button into:

```vue
<a-space class="cloud-node-filter-bar" wrap>
  <a-select v-model="form.cloudAccountId" placeholder="请选择云账户" style="width: 200px" allow-clear>
    <a-option v-for="account in cloudAccountOptions" :key="account.account_id" :value="account.account_id">
      {{ account.account_name }} ({{ getProviderName(account.provider) }})
    </a-option>
  </a-select>
  <a-input v-model="form.nodeId" placeholder="请输入节点ID" allow-clear />
  <a-select v-model="form.region" placeholder="地区" style="width: 200px" allow-clear>
    <a-option v-for="region in regionOptions" :key="region.code" :value="region.code">
      {{ region.name }}
      <a-tag v-if="region.tag" size="small" :color="region.tag === '国内' ? 'blue' : 'orange'" style="margin-left: 4px">
        {{ region.tag }}
      </a-tag>
    </a-option>
  </a-select>
  <a-select v-model="form.nodeType" placeholder="节点类型" style="width: 180px" allow-clear>
    <a-option value="scf-event">云函数（事件型）</a-option>
    <a-option value="scf-web">云函数（Web型）</a-option>
    <a-option value="server">服务器</a-option>
  </a-select>
  <a-select v-model="form.status" placeholder="节点状态" style="width: 120px" allow-clear>
    <a-option value="online">在线</a-option>
    <a-option value="offline">离线</a-option>
  </a-select>
  <a-button type="primary" @click="search">
    <template #icon><icon-search /></template>
    <span>查询</span>
  </a-button>
</a-space>
```

Preserve every `v-model`, option list, placeholder, width, clear behavior, and `search` handler.

- [ ] **Step 4: Replace the combined-toolbar CSS**

Replace the current `.page-head`, `.cloud-node-toolbar`, and related mobile rules with:

```css
.page-head {
  margin-bottom: 8px;
}

.page-head h2 {
  margin: 0;
  font-size: 20px;
  font-weight: 600;
}

.cloud-node-action-bar,
.cloud-node-filter-bar {
  display: flex;
  width: 100%;
  max-width: 100%;
  min-width: 0;
  justify-content: flex-start;
  margin-bottom: 8px;
}
```

Remove the obsolete `width: calc(100% - 72px)`, `flex: 1`, right alignment, and `.page-head` mobile column rules. Keep the existing inline-size containment and vertical scrolling safeguards unchanged.

- [ ] **Step 5: Run the focused test and confirm it passes**

Run:

```bash
CI=true pnpm --dir web exec vitest run --config vitest.config.ts tests/page-layout-standard-contract.test.ts
```

Expected: 1 test file passes with all 4 tests green.

- [ ] **Step 6: Commit the source and contract changes**

```bash
git add web/src/views/collector/cloud-node/cloud-node.vue web/tests/page-layout-standard-contract.test.ts
git commit -m "style: separate cloud node toolbars"
```

### Task 3: Verify, embed, deploy, and prove the result

**Files:**
- Modify: `web-host/internal/statik/statik.go`

- [ ] **Step 1: Run the complete frontend unit suite**

Run:

```bash
CI=true pnpm --dir web test:unit
```

Expected: all test files and tests pass.

- [ ] **Step 2: Build the production frontend**

Run:

```bash
CI=true pnpm --dir web build:prod
```

Expected: `vue-tsc` and Vite complete successfully; existing Browserslist, Sass legacy API, and chunk-size warnings are acceptable.

- [ ] **Step 3: Verify the layout in desktop and mobile browsers**

Start the preview server:

```bash
pnpm --dir web exec vite preview --host 127.0.0.1 --port 4173
```

Open `/#/collector/cloudnodes` with the existing authenticated API-mock browser harness at `1440x900` and `390x844`. Confirm:

- title, action toolbar, filter toolbar, and table appear in that order;
- both toolbars are left aligned and use `8px` vertical gaps;
- desktop controls fit or wrap within the content width;
- mobile controls wrap without horizontal overflow;
- the page remains vertically scrollable at `390x500`.

- [ ] **Step 4: Regenerate embedded assets and build the Linux binary**

```bash
make -C web-host statik
env TARGET_GOOS=linux TARGET_GOARCH=amd64 ./scripts/build.sh web-host
```

Expected: `web-host/internal/statik/statik.go` changes and `bin/moox-web-host` is produced.

- [ ] **Step 5: Commit the embedded frontend and push both commits**

```bash
git add web-host/internal/statik/statik.go
git commit -m "build: update embedded web assets"
git push origin HEAD:main
```

Expected: remote `main` advances to the embedded-assets commit.

- [ ] **Step 6: Deploy only web-host and run health checks**

Upload and replace the binary using the deploy password already available as `MOOX_DEPLOY_PASSWORD`:

```bash
env "SSHPASS=${MOOX_DEPLOY_PASSWORD}" sshpass -e scp -o StrictHostKeyChecking=no \
  bin/moox-web-host ubuntu@106.53.107.122:/tmp/moox-web-host.new
env "SSHPASS=${MOOX_DEPLOY_PASSWORD}" sshpass -e ssh -o StrictHostKeyChecking=no \
  ubuntu@106.53.107.122 \
  'cd /home/ubuntu/moox/prod && ./stop.sh web-host && cp /tmp/moox-web-host.new bin/moox-web-host && chmod +x bin/moox-web-host && ./start.sh web-host'
```

Then run:

```bash
cd /home/ubuntu/moox/prod
./status.sh web-host
./healthcheck.sh web-host
```

Expected: web-host reports `running` and `ready`.

- [ ] **Step 7: Verify the deployed page and repository state**

Check `https://106.53.107.122:9527/#/collector/cloudnodes` at desktop and mobile widths using the authenticated API-mock browser harness. Compare local and remote binary SHA-256 values, then run:

```bash
git fetch origin main
git status --short
git rev-parse HEAD
git rev-parse origin/main
```

Expected: the page matches the approved B layout, hashes match, the working tree is clean, and `HEAD` equals `origin/main`.
