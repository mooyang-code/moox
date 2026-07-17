# Frontend Hotspot Refactor Plan

> **Worker requirement:** Add unit/component/browser characterization before extraction. Preserve request payloads, route semantics, text, and interaction behavior.

**Goal:** Split CloudNode and View Browse into page coordinators, domain composables, and focused components, with desktop/mobile Playwright proof.

---

### Task 1: Characterize CloudNode behavior

**Files:**
- Create focused tests under `web/src/views/cloud-node/**/__tests__/`
- Modify: `web/tests/page-layout-standard-contract.test.ts`

- [ ] Lock list/filter/pagination/account/region requests, selection persistence, create/edit/deploy/delete confirmation, batch status, error messages, detail opening, tab/route coordination, and emitted payloads.
- [ ] Replace parent-file-only layout assertions with a composed parent+child contract.
- [ ] Run focused RED/characterization tests using the exact Vitest config from `web/package.json`.

### Task 2: Split CloudNode by responsibility

**Files:**
- Reduce: `web/src/views/cloud-node/cloud-node.vue`
- Create: `components/cloud-node-table.vue`, `cloud-node-editor.vue`, `cloud-node-detail.vue`
- Create: `composables/use-cloud-nodes.ts`, `use-cloud-node-actions.ts`
- Reuse types from `web/src/api/cloud-node.ts`

- [ ] Put list/filter/page/options state in `use-cloud-nodes`; mutations, confirmations, deployment, deletion, editing, and batch state in `use-cloud-node-actions`.
- [ ] Keep page layout, tabs, route coordination, and account/package page coordination in `cloud-node.vue`.
- [ ] Do not duplicate a local `CloudNode` model. Preserve API payload shape exactly.
- [ ] Run focused tests after each extraction and full Web unit/build after completion.

### Task 3: Characterize View Browse behavior

**Files:**
- Extend: `web/src/views/storage/view-browse/view-browse-utils.test.ts`
- Create: `use-view-query.test.ts`, `use-view-columns.test.ts`, focused component tests

- [ ] Lock `QueryTimeSeriesRows` and `SearchRecordRows` request structures, view switching, filters, pagination, sorting, column order/labels/widths, row detail, empty/error/loading states, and K-line open/update/close lifecycle.

### Task 4: Split View Browse including K-line ownership

**Files:**
- Reduce: `web/src/views/storage/view-browse/index.vue`
- Create components: toolbar, column selector, filter editor, result table, row detail, `view-kline-dialog.vue`
- Create composables: `use-view-query.ts`, `use-view-columns.ts`, `use-kline-chart.ts`

- [ ] Keep only props, visible-view selection, page layout, and child coordination in `index.vue`.
- [ ] Keep query/paging/sort/error state in `use-view-query`, columns in `use-view-columns`, and chart construction/update/disposal in `use-kline-chart`.
- [ ] Do not move the existing chart lifecycle into the result table or inflate `view-browse-utils.ts` with stateful behavior.

### Task 5: Desktop and mobile browser acceptance

**Files:**
- Create/extend: `web/tests/cloud-node.spec.ts`, `web/tests/view-browse.spec.ts`
- Modify: `web/playwright.config.ts` to include Desktop Chrome 1440x900 and Mobile Chrome 390x844

- [ ] Test primary flows with deterministic API fixtures, request-shape assertions, table/detail/dialog interactions, and no console/request errors.
- [ ] Assert no horizontal page overflow, primary controls visible, text not clipped, overlays usable, and no overlap/blank main region at both viewports.
- [ ] Run:

```bash
pnpm --dir web test
pnpm --dir web build:prod
pnpm --dir web exec playwright test tests/strategy-console.spec.ts tests/cloud-node.spec.ts tests/view-browse.spec.ts
```

### Task 6: Commit and task review

- [ ] Commit each page independently and request fresh task review for payload/behavior regressions, state ownership, accessibility, and responsive screenshots.

```bash
git commit -m "refactor(web): split cloud node workbench responsibilities"
git commit -m "refactor(web): split view browse query responsibilities"
```
