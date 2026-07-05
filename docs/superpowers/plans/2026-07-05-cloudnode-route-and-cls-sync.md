# CloudNode Route and CLS Sync Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Rename the collector cloud node frontend route away from legacy `functions` naming and automatically surface Tencent SCF CLS topic IDs after node creation.

**Architecture:** Keep the user-facing concept as "cloud node". Preserve `/collector/functions` as a redirect for old links, while making `/collector/cloudnodes` the canonical route. Extend the Tencent SCF provider metadata response to include CLS logset/topic IDs and persist them in node metadata after SCF creation or discovery.

**Tech Stack:** Vue 3 + Vite + Arco Design, tRPC-Go, Tencent Cloud SCF Go SDK, SQLite/GORM repository models.

---

### Task 1: Sync Tencent SCF CLS Metadata

**Files:**
- Modify: `modules/cloudnode/internal/providers/tencent-scf/client.go`
- Modify: `modules/cloudnode/internal/rpc/node.go`
- Test: `modules/cloudnode/internal/rpc/node_scf_test.go`

- [ ] **Step 1: Write failing tests**

Add/extend tests so `ensureSCFFunction` persists `cls_logset_id` and `cls_topic_id` into node metadata when Tencent SCF returns them from `GetFunction`.

- [ ] **Step 2: Run test to verify it fails**

Run:

```bash
go test ./modules/cloudnode/internal/rpc -run TestEnsureSCFFunction
```

Expected: failure because `FunctionInfo` does not expose CLS fields and `ensureSCFFunction` does not persist returned CLS IDs.

- [ ] **Step 3: Implement minimal code**

Add `ClsLogsetID` and `ClsTopicID` to provider `FunctionInfo`, populate them from `GetFunction`, and merge returned values into `node.Metadata` after existing-function discovery and newly-created-function readback.

- [ ] **Step 4: Verify**

Run:

```bash
go test ./modules/cloudnode/internal/rpc ./modules/cloudnode/internal/providers/tencent-scf
```

### Task 2: Rename Frontend Cloud Node Route

**Files:**
- Modify: `web/src/router/route.ts`
- Modify: `web/src/api/modules/system/static-menu.ts`
- Modify: `web/src/lang/modules/zhCN.ts`
- Modify: `web/src/lang/modules/enUS.ts`
- Move: `web/src/views/collector/cloud-function/cloud-function.vue` to `web/src/views/collector/cloud-node/cloud-node.vue`
- Move: `web/src/views/collector/cloud-function/function-package-manage.vue` to `web/src/views/collector/cloud-node/function-package-manage.vue`

- [ ] **Step 1: Make `/collector/cloudnodes` canonical**

Add a canonical route named `collector-cloudnodes` and redirect legacy `/collector/functions` to it.

- [ ] **Step 2: Update static menu and i18n keys**

Change sidebar menu paths and keys from `collector-functions` to `collector-cloudnodes`, while keeping old i18n keys as aliases for compatibility.

- [ ] **Step 3: Move component files**

Move the cloud node page and package management components under `collector/cloud-node/` so the collector frontend directory no longer uses the legacy `cloud-function` grouping name.

- [ ] **Step 4: Verify**

Run:

```bash
pnpm -C web build:prod
```

Expected: production build succeeds.
