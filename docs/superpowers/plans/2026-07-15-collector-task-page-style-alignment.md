# Collector Task Page Style Alignment Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make `/#/collector/tasks` use the same list-page structure, toolbar controls, table presentation, and row-action style as `/#/collector/packages` without changing task behavior.

**Architecture:** Keep the existing task API and state flow in `task-instances.vue`, but replace its page-level DOM and scoped styles with the package page's established structure. Add a source contract script that protects the agreed visual structure and rejects the removed title, result card, selection state, and fixed table-body height.

**Tech Stack:** Vue 3, TypeScript, Arco Design Vue, pnpm, Vitest, Vite, Go `web-host` statik embedding.

---

## File Map

- Create `web/scripts/check-collector-task-style.mjs`: source-level contract for the aligned task page.
- Modify `web/package.json`: expose the contract as `check:collector-task-style`.
- Modify `web/src/views/collector/task-instances/task-instances.vue`: align structure, buttons, tags, table behavior, selection state, and scoped styles.
- Modify generated `web-host/internal/statik/statik.go`: embed the verified production frontend bundle.

### Task 1: Add the RED page-style contract

**Files:**
- Create: `web/scripts/check-collector-task-style.mjs`
- Modify: `web/package.json`

- [ ] **Step 1: Create the contract script**

Create `web/scripts/check-collector-task-style.mjs` with:

```js
import fs from 'node:fs';
import path from 'node:path';
import process from 'node:process';

const source = fs.readFileSync(
  path.join(process.cwd(), 'src/views/collector/task-instances/task-instances.vue'),
  'utf8',
);

const required = [
  'class="task-toolbar"',
  'class="task-filters"',
  ':bordered="{ cell: true }"',
  ':scroll="{ x: 1810 }"',
  'class="task-id-button"',
  '<icon-eye />',
  'type="primary"',
  'size="mini"',
];

const forbidden = [
  '<h2>任务实例</h2>',
  'class="task-result-pane"',
  ':row-selection=',
  ':selected-keys=',
  '@select=',
  '@select-all=',
  'selectedKeys',
  'const select =',
  'const selectAll =',
  'y: 500',
  '<a-tag bordered',
];

const missing = required.filter((token) => !source.includes(token));
const remaining = forbidden.filter((token) => source.includes(token));

if (missing.length || remaining.length) {
  console.error(
    `collector task style contract failed; missing: ${missing.join(', ') || 'none'}; remaining: ${remaining.join(', ') || 'none'}`,
  );
  process.exit(1);
}

console.log('collector task page style contract passed');
```

- [ ] **Step 2: Register the contract command**

Add this entry beside the other `check:*` scripts in `web/package.json`:

```json
"check:collector-task-style": "node scripts/check-collector-task-style.mjs"
```

- [ ] **Step 3: Run the contract and verify RED**

Run:

```bash
cd web
CI=true pnpm check:collector-task-style
```

Expected: exit code `1`, with missing tokens for `task-toolbar`, `task-filters`, the task-ID button, and view icon; old heading, result pane, row selection, and `y: 500` are reported as remaining.

- [ ] **Step 4: Commit the RED contract**

```bash
git add web/package.json web/scripts/check-collector-task-style.mjs
git commit -m "test(collector): define task page style contract"
```

### Task 2: Align the task page structure and controls

**Files:**
- Modify: `web/src/views/collector/task-instances/task-instances.vue`
- Test: `web/scripts/check-collector-task-style.mjs`

- [ ] **Step 1: Replace the page heading and query wrapper with the package-style toolbar**

Inside `.moox-inner`, remove `page-head` and `task-query-panel`. Use:

```vue
<div class="task-toolbar">
  <a-space wrap class="task-filters">
    <!-- keep all existing filter inputs, selects, switch, query, and reset controls -->
  </a-space>
</div>
```

Keep every existing `v-model`, placeholder, width, select option, switch label, and click handler unchanged.

- [ ] **Step 2: Put the table directly in the content area**

Remove the `task-result-pane` section wrapper and configure the existing table as:

```vue
<a-table
  row-key="TaskID"
  size="small"
  :data="instanceList"
  :bordered="{ cell: true }"
  :loading="loading"
  :scroll="{ x: 1810 }"
  :pagination="paginationConfig"
  @page-change="onPageChange"
  @page-size-change="onPageSizeChange"
>
```

Do not add a fixed vertical scroll height.

- [ ] **Step 3: Align task links, tags, and row actions**

Render the task ID as:

```vue
<a-button class="task-id-button" type="text" @click="onViewDetails(record)">
  {{ record.TaskID }}
</a-button>
```

Remove `bordered` from execution-status and validity tags. Render the operation as:

```vue
<a-button type="primary" size="mini" @click="onViewDetails(record)">
  <template #icon><icon-eye /></template>
  查看
</a-button>
```

- [ ] **Step 4: Remove unused selection state and handlers**

Delete:

```ts
const selectedKeys = ref<string[]>([]);

const select = (list: string[]) => {
  selectedKeys.value = list;
};

const selectAll = (state: boolean) => {
  selectedKeys.value = state ? instanceList.value.map(el => el.TaskID) : [];
};
```

Also remove the table's `row-selection`, `selected-keys`, `select`, and `select-all` bindings.

- [ ] **Step 5: Replace old page styles with package-style layout rules**

Remove `.page-head`, `.task-query-panel`, `.task-filter-row`, and `.task-result-pane` rules. Add:

```css
.task-toolbar {
  display: flex;
  flex-wrap: wrap;
  gap: 8px;
  align-items: center;
  justify-content: space-between;
  margin-bottom: 12px;
}

.task-filters {
  flex: 1 1 auto;
}

.task-id-button {
  height: auto;
  max-width: 100%;
  padding: 0;
  color: #165dff;
}

.task-id-button :deep(.arco-btn-content) {
  max-width: 100%;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}
```

Keep `.ellipsis-text` and `.detail-json`. Remove `.params-preview` if it has no template usage.

- [ ] **Step 6: Run the contract and verify GREEN**

```bash
cd web
CI=true pnpm check:collector-task-style
```

Expected: `collector task page style contract passed`.

- [ ] **Step 7: Run type checking**

```bash
cd web
CI=true pnpm exec vue-tsc --noEmit
```

Expected: exit code `0` with no TypeScript diagnostics.

- [ ] **Step 8: Commit the aligned page**

```bash
git add web/src/views/collector/task-instances/task-instances.vue
git commit -m "style(collector): align task page with package list"
```

### Task 3: Verify the frontend and embed the production bundle

**Files:**
- Modify generated: `web-host/internal/statik/statik.go`

- [ ] **Step 1: Run focused and regression checks**

Run from `web`:

```bash
CI=true pnpm check:collector-task-style
CI=true pnpm check:detail-pages
CI=true pnpm test -- --run
CI=true pnpm exec vue-tsc --noEmit
```

Expected: all commands exit `0`; the current Vitest suite passes without regressions.

- [ ] **Step 2: Build the production frontend**

```bash
cd web
CI=true pnpm run build:prod
```

Expected: Vite completes successfully. Existing Browserslist age, Sass legacy API, `lottie-web` eval, and large-chunk warnings are non-blocking.

- [ ] **Step 3: Regenerate and test the embedded frontend**

```bash
cd web-host
make statik
go test -count=1 ./...
```

Expected: `web-host` tests pass and only `web-host/internal/statik/statik.go` changes.

- [ ] **Step 4: Build the Linux web-host binary**

```bash
TARGET_GOOS=linux TARGET_GOARCH=amd64 ./scripts/build/build.sh web-host
sha256sum bin/moox-web-host
```

Expected: `bin/moox-web-host` is produced for Linux AMD64 and a SHA-256 digest is printed.

- [ ] **Step 5: Commit the embedded bundle**

```bash
git add web-host/internal/statik/statik.go
git commit -m "build(web): embed aligned collector task page"
```

### Task 4: Deploy and perform browser acceptance

**Files:**
- No source changes expected.

- [ ] **Step 1: Upload and restart web-host**

```bash
scp -o BatchMode=yes bin/moox-web-host ubuntu@106.53.107.122:/tmp/moox-web-host.new
ssh -o BatchMode=yes ubuntu@106.53.107.122 '
  set -euo pipefail
  cd /home/ubuntu/moox/prod
  cp bin/moox-web-host "bin/moox-web-host.backup.$(date +%Y%m%d%H%M%S)"
  MOOX_WITH_WEB_HOST=1 ./stop.sh web-host
  install -m 0755 /tmp/moox-web-host.new bin/moox-web-host
  rm -f /tmp/moox-web-host.new
  MOOX_WITH_WEB_HOST=1 ./start.sh web-host
  ./healthcheck.sh web-host
  sha256sum bin/moox-web-host
'
```

Expected: the remote health check exits `0`, and the remote digest matches the local binary.

- [ ] **Step 2: Verify desktop layout in the browser**

Open:

```text
https://106.53.107.122:9527/?v=<commit>#/collector/tasks
```

Verify:

- no standalone `任务实例` heading;
- filter controls and buttons align with `/#/collector/packages`;
- no row-selection checkbox column;
- task ID is a text-style link button;
- status tags match the package page treatment;
- `查看` is a primary mini button with the view icon;
- the table is directly on the page without the old bordered result card;
- the page scrolls naturally and the table has no fixed `500px` body height.

- [ ] **Step 3: Verify interactions and narrow viewport behavior**

At desktop width, exercise query, reset, pagination, and one task-detail modal. At a narrow viewport, confirm the toolbar wraps without overlap and the table scrolls horizontally without compressing identifiers. Check browser console errors after both passes.

Expected: all interactions retain their previous behavior, no controls overlap, and there are no new console errors.

- [ ] **Step 4: Push and verify remote branch synchronization**

```bash
git push origin feature/frontend-service-host-workbench
git status --short --branch
git rev-parse HEAD
git rev-parse origin/feature/frontend-service-host-workbench
```

Expected: the worktree is clean, the branch is not ahead or behind, and both revisions are identical.
