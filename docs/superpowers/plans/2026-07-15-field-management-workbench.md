# Field Management Workbench Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Complete the field-group metadata backend, expose the enhanced RPCs through the existing admin gateway, import the initial quantitative metadata, and replace the field page with the approved directory-and-table workbench.

**Architecture:** Extend the Metadata protobuf contract and the metadata store with a typed field query, aggregate counts, transactional batch updates, and restricted empty-group deletion. The admin gateway remains a generic `/api/admin/{service}/{method}` proxy; gateway registration is proven by the `storage_metadata` deployment plus generated Metadata service registration and forwarding tests. The Vue page owns requests and URL state while focused child components render the group tree, table, batch bar, dialogs, and editor drawer.

**Tech Stack:** Go 1.24+, SQLite, protobuf/tRPC-Go, Vue 3, TypeScript, Arco Design Vue, Vitest, Playwright.

---

### Task 1: Lock the Metadata RPC contract with failing tests

**Files:**
- Modify: `modules/storage/proto/metadata.proto`
- Regenerate: `modules/storage/proto/storagegen/metadata.pb.go`
- Regenerate: `modules/storage/proto/storagegen/metadata.trpc.go`
- Modify: `modules/storage/internal/service/access/metadata_catalog_test.go`

- [ ] **Step 1: Add tests for enhanced field queries and mutations**

Add service tests that request keyword/status filters, recursive group selection, field counts, transactional batch updates, and empty-group deletion. Assert invalid sort values, invalid batch targets, non-empty group deletion, and cross-Space field IDs return non-success `ret_info`.

- [ ] **Step 2: Run the service test and verify it fails**

Run:

```bash
GOWORK=/tmp/moox-field-test.work go test -count=1 ./internal/service/access
```

Expected: FAIL because the new request fields and RPC methods do not exist.

- [ ] **Step 3: Extend the protobuf contract**

Add `status`, `keyword`, `include_descendants`, `ungrouped_only`, `sort_by`, and `sort_order` to `ListFieldsReq`; add `field_counts`, `total_field_count`, and `ungrouped_field_count` to `ListFieldGroupsRsp`; add `BatchUpdateFields` and `DeleteFieldGroup` request/response messages and service methods exactly as defined by the approved design.

- [ ] **Step 4: Regenerate protobuf and tRPC code**

Run:

```bash
make -C modules/storage proto
```

Expected: generated `metadata.pb.go` and `metadata.trpc.go` include both new methods in the server interface, handler table, unimplemented server, and client proxy.

- [ ] **Step 5: Commit the contract**

```bash
git add modules/storage/proto/metadata.proto modules/storage/proto/storagegen/metadata.pb.go modules/storage/proto/storagegen/metadata.trpc.go modules/storage/internal/service/access/metadata_catalog_test.go
git commit -m "feat(storage): extend field metadata contract"
```

### Task 2: Implement field queries, counts, batch updates, and deletion

**Files:**
- Modify: `modules/storage/internal/core/metadata/store.go`
- Modify: `modules/storage/internal/infra/metadata/sqlite/crud_dataset.go`
- Modify: `modules/storage/internal/infra/metadata/sqlite/crud_field_group.go`
- Modify: `modules/storage/internal/infra/metadata/sqlite/crud_test.go`
- Modify: `modules/storage/internal/infra/metadata/cache/store.go`
- Modify: `modules/storage/internal/infra/metadata/cache/store_test.go`
- Modify: `modules/storage/internal/service/access/metadata_catalog.go`
- Modify: `modules/storage/internal/service/access/metadata_space_view_test.go`

- [ ] **Step 1: Introduce a typed query boundary**

Define a `FieldQuery` in the core metadata package containing Space, group, value type, status, keyword, recursion, ungrouped, sort, and page fields. Change `ListFields` to receive this query so parameters do not continue growing positionally.

- [ ] **Step 2: Write SQLite tests first**

Cover case-insensitive keyword matching across ID/name/description, status and value type combinations, recursive root-group results, exact child-group results, stable pagination, allowed sort modes, count semantics, batch rollback, and deletion restrictions.

- [ ] **Step 3: Run SQLite tests and verify failure**

```bash
GOWORK=/tmp/moox-field-test.work go test -count=1 ./internal/infra/metadata/sqlite
```

Expected: FAIL on missing query/count/mutation methods.

- [ ] **Step 4: Implement safe SQL queries**

Build SQL arguments separately from SQL text and select sort clauses from a fixed Go map. Use one recursive CTE for `include_descendants`; compute root counts including descendants and child counts directly. Never interpolate request values into SQL.

- [ ] **Step 5: Implement transactional mutations**

Validate a maximum of 100 distinct field IDs, target status (`active` or `inactive`), target group existence, and all field ownership before starting the update. Execute a single transaction and return `updated_count`; delete only groups with no children and no fields.

- [ ] **Step 6: Wire service and cache layers**

Forward the typed query and new writer methods through cache. In Access service, validate incompatible `group_id`/`ungrouped_only` and sort parameters before reaching SQLite, and return project-standard `ret_info` errors.

- [ ] **Step 7: Run storage tests**

```bash
GOWORK=/tmp/moox-field-test.work go test -count=1 ./internal/infra/metadata/sqlite ./internal/infra/metadata/cache ./internal/service/access
```

Expected: PASS.

- [ ] **Step 8: Commit backend behavior**

```bash
git add modules/storage/internal/core/metadata modules/storage/internal/infra/metadata modules/storage/internal/service/access
git commit -m "feat(storage): add field governance operations"
```

### Task 3: Finish schema and YAML import delivery

**Files:**
- Modify: `modules/storage/schema/metadata.sql`
- Modify: `modules/storage/internal/bootstrap/metadata/seed.go`
- Modify: `modules/storage/internal/bootstrap/metadata/schema_test.go`
- Modify: `modules/storage/internal/bootstrap/metadata/quant_seed_test.go`
- Modify: `modules/cli/internal/command/metadata_types.go`
- Modify: `modules/cli/internal/command/metadata_implementation.go`
- Modify: `modules/cli/internal/command/metadata_test.go`
- Create: `examples/metadata-quant-initial.seed.yaml`
- Create: `modules/storage/internal/service/access/quant_seed_e2e_test.go`
- Create: `docs/量化初始元数据设计.md`

- [ ] **Step 1: Verify schema invariants**

Test that fields require an existing group, parent groups cannot be self-referential, and application validation rejects a third level. Keep foreign keys restrictive so groups cannot disappear under fields.

- [ ] **Step 2: Verify importer ordering and compatibility**

Test that `field_groups` calls precede `fields`, undefined groups fail before RPC execution, and legacy seeds with missing `group_id` receive a generated `general` group.

- [ ] **Step 3: Verify the quantitative seed end to end**

Load `examples/metadata-quant-initial.seed.yaml` into an in-memory metadata store and assert representative Spaces, record/time_series datasets, groups, fields, dataset columns, and financial metric representation.

- [ ] **Step 4: Run import tests**

```bash
GOWORK=/tmp/moox-field-test.work go test -count=1 ./internal/bootstrap/metadata ./internal/service/access
GOWORK=/tmp/moox-field-test.work go test -count=1 ./internal/command
```

Expected: PASS.

- [ ] **Step 5: Dry-run the real YAML**

Build the CLI and run:

```bash
go run ./cmd/moox-cli metadata import --file ../../examples/metadata-quant-initial.seed.yaml --dry-run
```

Expected: every resource validates and the command exits zero without network writes.

- [ ] **Step 6: Commit schema, importer, seed, and metadata documentation**

```bash
git add modules/storage/schema modules/storage/internal/bootstrap/metadata modules/storage/internal/service/access/quant_seed_e2e_test.go modules/cli/internal/command/metadata_*.go examples/metadata-quant-initial.seed.yaml docs/量化初始元数据设计.md
git commit -m "feat(metadata): add quantitative initial catalog seed"
```

### Task 4: Prove admin-gateway registration

**Files:**
- Modify: `modules/admin/internal/gateway/gateway_test.go`
- Modify: `modules/admin/internal/service/sysdeploy/defaults_test.go`
- Verify: `modules/admin/internal/service/sysdeploy/defaults.go`
- Verify: `modules/storage/proto/storagegen/metadata.trpc.go`
- Verify: `web/src/api/storage/http.ts`

- [ ] **Step 1: Add gateway route coverage**

Add table-driven tests proving `/api/admin/storage_metadata/ListFields`, `/BatchUpdateFields`, and `/DeleteFieldGroup` parse as service `storage_metadata` with the exact method name and enter the generic forwarding path.

- [ ] **Step 2: Assert deployment registration**

Keep a test that `storage_metadata` resolves to port `20200` and service path `trpc.moox.storage.Metadata`. Assert generated Metadata registration contains both new method handlers.

- [ ] **Step 3: Run gateway tests**

```bash
GOWORK=/tmp/moox-field-test.work go test -count=1 ./internal/gateway ./internal/service/sysdeploy
```

Expected: PASS.

- [ ] **Step 4: Commit gateway proof**

```bash
git add modules/admin/internal/gateway/gateway_test.go modules/admin/internal/service/sysdeploy/defaults_test.go
git commit -m "test(admin): cover field metadata gateway routes"
```

### Task 5: Add typed frontend API and state helpers

**Files:**
- Modify: `web/src/api/storage/types.ts`
- Modify: `web/src/api/storage/metadata.ts`
- Create: `web/src/views/data/fields/field-workbench.ts`
- Create: `web/src/views/data/fields/field-workbench.test.ts`

- [ ] **Step 1: Write helper tests**

Test query normalization, URL serialization/restoration, invalid group fallback, group tree construction, recursive count labels, field paths, dirty-form comparison, and stale request rejection.

- [ ] **Step 2: Run Vitest and verify failure**

```bash
CI=true pnpm test:unit -- src/views/data/fields/field-workbench.test.ts
```

Expected: FAIL because helpers do not exist.

- [ ] **Step 3: Add API types and functions**

Type the enhanced list request/response and add `batchUpdateFields` and `deleteFieldGroup`, all through `callMetadata` so they use `/api/admin/storage_metadata/{method}`.

- [ ] **Step 4: Implement pure state helpers**

Keep router conversion, tree/path building, and request sequencing outside Vue components so they can be unit tested without rendering Arco components.

- [ ] **Step 5: Run helper tests and commit**

```bash
CI=true pnpm test:unit -- src/views/data/fields/field-workbench.test.ts
git add web/src/api/storage web/src/views/data/fields/field-workbench.ts web/src/views/data/fields/field-workbench.test.ts
git commit -m "feat(web): add field workbench API state"
```

### Task 6: Build the approved field-management workbench

**Files:**
- Modify: `web/src/views/data/fields/index.vue`
- Create: `web/src/views/data/fields/components/FieldGroupTree.vue`
- Create: `web/src/views/data/fields/components/FieldTable.vue`
- Create: `web/src/views/data/fields/components/FieldBatchBar.vue`
- Create: `web/src/views/data/fields/components/FieldEditorDrawer.vue`
- Create: `web/src/views/data/fields/components/FieldGroupDialog.vue`

- [ ] **Step 1: Implement page orchestration**

Load groups/counts and fields independently, synchronize approved query keys with the hash route, debounce keyword searches by 300ms, discard stale responses, and clear all state when Space changes.

- [ ] **Step 2: Implement the two-level directory**

Render all fields, conditional ungrouped fields, expandable root groups, child groups, counts, node menus, and the bottom create action in a stable `240px` desktop column.

- [ ] **Step 3: Implement the dense field table and batch bar**

Render Chinese name with field ID as the secondary line, server sorting, page-size choices, current-page selection, icon actions with tooltips, and batch move/enable/disable confirmations.

- [ ] **Step 4: Replace the field modal with a drawer**

Use a `560px` editor drawer with grouped form sections, readonly ID on edit, JSON validation, fixed footer, save loading state, and dirty-close confirmation.

- [ ] **Step 5: Complete field-group management**

Keep a focused dialog for create/edit, prevent third-level choices in the UI, expose delete separately, and surface non-empty-group errors without clearing the form or selection.

- [ ] **Step 6: Add responsive and state styling**

At widths below `900px`, move the group directory into a side drawer, retain table horizontal scrolling, use `min(560px, 100vw)` for the editor, and provide loading, empty, no-result, and retry states.

- [ ] **Step 7: Run frontend tests and production build**

```bash
CI=true pnpm test:unit
CI=true pnpm build:prod
```

Expected: PASS with no TypeScript or Vite errors.

- [ ] **Step 8: Commit the workbench**

```bash
git add web/src/views/data/fields web/src/api/storage
git commit -m "feat(web): redesign field management workbench"
```

### Task 7: Integrated verification, independent review, and final acceptance

**Files:**
- Create: `docs/superpowers/verification/2026-07-15-field-management-workbench.md`
- Modify: only files required by review findings

- [ ] **Step 1: Run fresh backend verification**

Run storage, CLI, and admin gateway packages with `-count=1`. Record exact commands and results.

- [ ] **Step 2: Run fresh frontend verification**

Run all Vitest tests and production build under `CI=true`. Record results.

- [ ] **Step 3: Exercise the real gateway path**

Start storage and admin gateway or use the project's deployed test environment. Through `/api/admin/storage_metadata/*`, list fields, batch-move a disposable field, restore it, and verify empty-group deletion restrictions.

- [ ] **Step 4: Import the seed into an empty test metadata database**

Run `moox-cli metadata import --if-not-exists`, then query representative Space/group/dataset/field records through the gateway. Never import into an unknown production database.

- [ ] **Step 5: Perform browser acceptance**

Use Playwright at desktop and mobile viewports. Verify search, filters, group navigation, URL restoration, row edit drawer, dirty-close guard, batch operations, error states, and no overlapping text or controls. Capture screenshots.

- [ ] **Step 6: Dispatch a fresh review agent**

Give the reviewer the design, this plan, and the exact implementation diff. Ask for correctness, regression, security, data integrity, gateway registration, UX, and missing-test findings, ordered by severity with file/line references.

- [ ] **Step 7: Fix and reverify every accepted finding**

For each finding, reproduce it first, add or update a test, implement the smallest correct fix, then rerun the affected suite and the final verification set.

- [ ] **Step 8: Write verification evidence and commit**

```bash
git add docs/superpowers/verification/2026-07-15-field-management-workbench.md
git commit -m "docs: verify field management workbench"
```

The work is complete only when the gateway and browser workflows succeed, the seed is proven against an empty database, the independent reviewer has no unresolved findings, and the final fresh test/build run is green.
