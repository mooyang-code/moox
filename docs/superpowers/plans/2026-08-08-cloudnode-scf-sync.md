# CloudNode SCF 云节点同步实施计划

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 在云节点页面增加安全、可预览、可筛除且幂等的腾讯云 SCF 同步能力，从已配置云账户的全部受支持地域和命名空间发现函数，并把用户确认的 MooX 函数恢复到 CloudNode 本地目录。

**Architecture:** CloudNode 后端拥有云凭据并提供两个明确 RPC：`PreviewSCFFunctions` 只读扫描云端，`ImportSCFFunctions` 对用户保留的函数重新读取云端真值后批量 Upsert。本地页面使用单弹窗工作台完成账号选择、全地域扫描、筛除和确认；完整 Environment、SecretID、SecretKey、CLS 密钥及服务 HMAC 均不返回浏览器或写入节点 Metadata。

**Tech Stack:** Go 1.25、tRPC-Go/Protobuf、Tencent Cloud SCF Go SDK v1.1.0、SQLite/GORM、Vue 3、Arco Design、TypeScript、Vitest、Playwright。

---

## Confirmed Product Decisions

- 使用单弹窗工作台，不做多步向导。
- 多云账户时必须由用户选择；只有一个账户时默认选中。
- 无云账户时不复制账户表单，显示“去配置云账户”，复用现有云账户管理。
- 选定账户后扫描系统支持的完整 SCF 地域目录，而不是只扫描当前页面已有的四个地域。当前代码中的权威支持目录包含 17 个地域：北京 `ap-beijing`、成都 `ap-chengdu`、重庆 `ap-chongqing`、广州 `ap-guangzhou`、南京 `ap-nanjing`、上海 `ap-shanghai`、上海金融 `ap-shanghai-fsi`、深圳金融 `ap-shenzhen-fsi`、中国香港 `ap-hongkong`、多伦多 `na-toronto`、硅谷 `na-siliconvalley`、法兰克福 `eu-frankfurt`、新加坡 `ap-singapore`、曼谷 `ap-bangkok`、雅加达 `ap-jakarta`、东京 `ap-tokyo`、首尔 `ap-seoul`。
- 每个地域先分页列出 Namespace，再分页列出函数；某个地域失败不隐藏其他地域结果。
- 所有 SCF 函数都可见；只有明确识别为当前 Space 的 MooX 函数可勾选导入。
- 非 MooX、跨 Space、非 Active、缺少 `MOOX_CODE_PACKAGE_ID`、身份冲突的函数只展示原因，不允许导入。
- 用户可以取消勾选或移除任意可导入项，再确认导入。
- 导入请求只携带 `region + namespace + function_name` 引用。后端不得信任浏览器回传的 package、Space、状态、Environment 或 Metadata。
- 节点身份继续使用 `node_id = function_name`。如果同一 Space 扫描到跨地域/Namespace 的同名函数，全部冲突项标为不可导入，避免覆盖错误节点。
- 当前线上云账户、节点和代码包表均为空。上线同步功能前必须通过现有“云账户管理”恢复账户配置；不得把密钥写入默认初始化 Bundle。

## Region Catalog Boundary

当前 `modules/cli/internal/setup/config/config.go` 已有 17 项 `supportedSCFRegion` 校验目录，而 `modules/cloudnode/internal/rpc/account.go` 的 `ListCloudRegions` 只有广州、上海、中国香港、新加坡四项；两者已经漂移。实施时建立一个共享的、带代码/中文名/国内外标签的 Tencent SCF region catalog，Setup 配置校验和 CloudNode `ListCloudRegions` 都复用它。同步按钮只调用后端返回的完整目录，不在 Vue 中复制地域列表。

这里的“全部地域”指 MooX 当前支持目录中的全部地域，而不是只扫描当前部署配置，也不是把腾讯云 API 未来动态增加的所有地域自动纳入范围。这 17 项是当前 MooX 的支持地域边界，不代表腾讯云未来永远不会增加新地域。今后新增地域只改共享目录、目录测试和展示标签，不改同步算法。当前 `custom.toml` 中启用的 `ap-beijing`、`ap-chengdu`、`ap-guangzhou`、`ap-shanghai`、`ap-singapore`、`ap-tokyo` 只是本次部署配置，不应被误当成全部支持地域。

## RPC Contract

Add the following source-level contract to `modules/cloudnode/proto/cloudnode.proto`:

```proto
enum SCFFunctionImportState {
  SCF_FUNCTION_IMPORT_STATE_UNSPECIFIED = 0;
  SCF_FUNCTION_IMPORT_STATE_NEW = 1;
  SCF_FUNCTION_IMPORT_STATE_EXISTING = 2;
  SCF_FUNCTION_IMPORT_STATE_DELETED = 3;
  SCF_FUNCTION_IMPORT_STATE_BLOCKED = 4;
}

message SCFFunctionRef {
  string region = 1;
  string namespace = 2;
  string function_name = 3;
}

message SCFFunctionCandidate {
  SCFFunctionRef function = 1;
  string status = 2;
  string runtime = 3;
  string function_type = 4;
  string package_id = 5;
  string node_id = 6;
  string trigger_type = 7;
  string biz_type = 8;
  SCFFunctionImportState import_state = 9;
  bool importable = 10;
  string reason = 11;
}

message SCFRegionScanError {
  string region = 1;
  string message = 2;
}

message PreviewSCFFunctionsReq { string account_id = 1; }
message PreviewSCFFunctionsRsp {
  common.RetInfo ret_info = 1;
  repeated SCFFunctionCandidate functions = 2;
  repeated SCFRegionScanError region_errors = 3;
}

message ImportSCFFunctionsReq {
  string account_id = 1;
  repeated SCFFunctionRef functions = 2;
}

message SCFFunctionImportResult {
  SCFFunctionRef function = 1;
  string node_id = 2;
  string action = 3;
  string error_message = 4;
}

message ImportSCFFunctionsRsp {
  common.RetInfo ret_info = 1;
  repeated SCFFunctionImportResult results = 2;
  int32 created = 3;
  int32 restored = 4;
  int32 unchanged = 5;
  int32 failed = 6;
}
```

The service adds:

```proto
rpc PreviewSCFFunctions(PreviewSCFFunctionsReq) returns (PreviewSCFFunctionsRsp);
rpc ImportSCFFunctions(ImportSCFFunctionsReq) returns (ImportSCFFunctionsRsp);
```

No response message may contain the complete SCF Environment.

## Import Mapping

| CloudNode field | Server-owned source |
| --- | --- |
| `space_id` | `spacecontext.MustFromContext(ctx)` and verified `MOOX_SPACE_ID` |
| `node_id` | exact remote `function_name`, after collision check |
| `cloud_account_id` | verified request account |
| `provider` | constant `tencent-scf` |
| `region`, `namespace`, `function_name` | remote function reference |
| `node_type` | remote SCF Type: `Event -> scf-event`, `HTTP -> scf-web` |
| `trigger_type` | recognized `moox-market-fetch-timer -> timer`; otherwise Event becomes `invoke` |
| `package_id` | remote `MOOX_CODE_PACKAGE_ID` |
| `package_version` | local package lookup when present; empty when package catalog was reset |
| `deployment_id` | empty; Tencent does not return MooX deployment ID |
| `metadata.deployment_ready` | `true` only after Active status and package marker verification |
| `metadata.biz_type` | `market_fetcher` only for recognized MooX fetcher prefix or Timer marker |
| other Metadata | whitelisted runtime, memory, timeout, import source/time; never credentials or full Environment |

## File Map

| Path | Responsibility |
| --- | --- |
| `modules/cloudnode/proto/cloudnode.proto` | Preview/import API source contract |
| `modules/cloudnode/proto/cloudnodegen/*` | Regenerated protobuf and tRPC bindings |
| `modules/cloudnode/internal/providers/tencentscf/discovery.go` | Paginated Namespace and function discovery |
| `modules/cloudnode/internal/providers/tencentscf/discovery_test.go` | Pagination, nil response, and SDK error tests |
| `packages/cloudprovider/tencent/scf_regions.go` | Shared complete SCF region catalog and labels |
| `packages/cloudprovider/tencent/scf_regions_test.go` | Catalog completeness, uniqueness, and lookup tests |
| `modules/cli/internal/setup/config/config.go` | Consume the shared catalog for setup validation |
| `modules/cloudnode/internal/providers/tencentscf/client.go` | Shared SCF client construction and detail conversion |
| `modules/cloudnode/internal/rpc/server.go` | Discovery-capable provider interface |
| `modules/cloudnode/internal/rpc/scf_sync.go` | Preview, eligibility, secure import, and result classification |
| `modules/cloudnode/internal/rpc/scf_sync_test.go` | Real SQLite plus fake provider RPC tests |
| `modules/cloudnode/internal/store/node.go` | Include-deleted identity lookup and transactional batch Upsert |
| `modules/cloudnode/internal/store/node_test.go` | New/existing/deleted/collision persistence behavior |
| `modules/cloudnode/test/scf_sync_e2e_test.go` | Preview-to-import workflow against real SQLite and fake SCF inventory |
| `web/src/api/cloud-node.ts` | Typed preview/import methods |
| `web/src/api/cloud-node.test.ts` | Exact service/method/body tests |
| `web/src/views/collector/cloud-node/components/sync-scf-dialog.vue` | Single-dialog sync workbench |
| `web/src/views/collector/cloud-node/composables/use-scf-sync.ts` | Dialog state, selection, filtering, and summaries |
| `web/src/views/collector/cloud-node/composables/use-scf-sync.test.ts` | Selection and rescan state tests |
| `web/src/views/collector/cloud-node/cloud-node.vue` | Toolbar entrypoint and post-import refresh |
| `web/tests/cloud-node-workflows.spec.ts` | Page integration contract |
| `web/tests/cloud-node-scf-sync.spec.ts` | Browser workflow with mocked APIs |

---

### Task 1: Add The Preview And Import Proto Contract

**Files:**
- Modify: `modules/cloudnode/proto/cloudnode.proto`
- Regenerate: `modules/cloudnode/proto/cloudnodegen/cloudnode.pb.go`
- Regenerate: `modules/cloudnode/proto/cloudnodegen/cloudnode.trpc.go`

- [ ] **Step 1: Write a failing generated-contract test**

Extend `modules/cloudnode/proto/cloudnodegen/node_batch_contract_test.go` or add `scf_sync_contract_test.go` to instantiate both requests and assert the two RPC methods exist on the generated service interface.

- [ ] **Step 2: Run and verify RED**

```bash
go test -count=1 ./modules/cloudnode/proto/cloudnodegen -run SCFSyncContract
```

Expected: compile failure because the messages and methods are absent.

- [ ] **Step 3: Add the exact contract above and regenerate**

```bash
make -C modules/cloudnode/proto clean all
```

Never hand-edit generated descriptors.

- [ ] **Step 4: Run generated-module tests**

```bash
go test -count=1 ./modules/cloudnode/proto/cloudnodegen
```

Expected: PASS.

- [ ] **Step 5: Commit the API contract**

```bash
git add modules/cloudnode/proto/cloudnode.proto modules/cloudnode/proto/cloudnodegen
git commit -m "feat(cloudnode): add scf sync contract"
```

---

### Task 2: Centralize The Complete Region Catalog And Implement Paginated Discovery

**Files:**
- Create: `packages/cloudprovider/tencent/scf_regions.go`
- Create: `packages/cloudprovider/tencent/scf_regions_test.go`
- Modify: `modules/cli/internal/setup/config/config.go`
- Modify: `modules/cloudnode/internal/rpc/account.go`
- Modify: `modules/cloudnode/go.mod`
- Create: `modules/cloudnode/internal/providers/tencentscf/discovery.go`
- Create: `modules/cloudnode/internal/providers/tencentscf/discovery_test.go`
- Modify: `modules/cloudnode/internal/providers/tencentscf/client.go`

- [ ] **Step 1: Write failing catalog and pagination tests**

Assert that the shared catalog has exactly the 17 codes listed in `Region Catalog Boundary`, that codes and labels are unique, and that both Setup validation and CloudNode `ListCloudRegions` expose the same set. The test must fail against the current four-entry CloudNode catalog.

```go
got := tencent.SCFRegions()
require.Len(t, got, 17)
assert.Contains(t, codes(got), "ap-beijing")
assert.Contains(t, codes(got), "ap-tokyo")
```

Use a narrow internal SDK interface with `ListNamespacesWithContext` and `ListFunctionsWithContext`. Cover multiple Namespace pages, multiple function pages, empty final pages, nil entries, duplicate function references, and Tencent errors preserving region/Namespace context.

```go
type DiscoveryFunction struct {
    FunctionRef
    Status string
    Runtime string
    Type string
}

func (c *Client) ListFunctionInventory(ctx context.Context, region string) ([]DiscoveryFunction, error)
```

- [ ] **Step 2: Run and verify RED**

```bash
go test -count=1 ./packages/cloudprovider/tencent ./modules/cloudnode/internal/rpc ./modules/cloudnode/internal/providers/tencentscf -run 'Test(SCFRegion|List(FunctionInventory|Namespaces|Functions))'
```

Expected: FAIL because the shared catalog and discovery are absent, and `ListCloudRegions` still exposes only four entries.

- [ ] **Step 3: Implement the shared catalog and reuse it everywhere**

Define one immutable ordered catalog with `Code`, `Name`, and `Tag` fields for all 17 entries. Change `supportedSCFRegion` to perform a catalog lookup instead of maintaining a second switch. Change `ListCloudRegions` to map the same catalog to Proto responses while preserving existing node quota fields. Add the `packages/cloudprovider` dependency to CloudNode using the repository's workspace replacement pattern.

- [ ] **Step 4: Implement complete Namespace/function pagination**

Use explicit `Limit=100` and advance `Offset` by the number returned. Stop only when returned count is less than the requested limit or the response total is reached. Sort the final inventory by Namespace then FunctionName for stable UI output.

- [ ] **Step 5: Run catalog, RPC, and provider tests**

```bash
go test -race -count=1 ./packages/cloudprovider/tencent ./modules/cli/internal/setup/config ./modules/cloudnode/internal/rpc ./modules/cloudnode/internal/providers/tencentscf
```

Expected: PASS, with `ListCloudRegions` returning all 17 catalog entries.

- [ ] **Step 6: Commit the catalog and provider discovery**

```bash
git add packages/cloudprovider/tencent/scf_regions.go packages/cloudprovider/tencent/scf_regions_test.go modules/cli/internal/setup/config/config.go modules/cloudnode/internal/rpc/account.go modules/cloudnode/go.mod modules/cloudnode/go.sum modules/cloudnode/internal/providers/tencentscf/discovery.go modules/cloudnode/internal/providers/tencentscf/discovery_test.go modules/cloudnode/internal/providers/tencentscf/client.go
git commit -m "feat(cloudnode): share complete scf region catalog"
```

---

### Task 3: Add Include-Deleted Lookup And Transactional Import Persistence

**Files:**
- Modify: `modules/cloudnode/internal/store/node.go`
- Modify: `modules/cloudnode/internal/store/node_test.go`

- [ ] **Step 1: Write failing store tests**

Cover new node, active existing node, soft-deleted restore, two references with the same Node ID, and transactional rollback on a database error.

```go
states, err := repo.GetNodeImportStates(ctx, "crypto_market", []string{"fetcher-a"})
require.NoError(t, err)
assert.Equal(t, NodeImportStateDeleted, states["fetcher-a"])
```

- [ ] **Step 2: Run and verify RED**

```bash
go test -count=1 ./modules/cloudnode/internal/store -run 'Test(NodeImport|BatchUpsertSyncedNodes)'
```

Expected: FAIL because include-deleted classification and batch persistence are absent.

- [ ] **Step 3: Implement focused repository methods**

Add:

```go
func (r *CatalogRepository) GetNodeImportStates(ctx context.Context, spaceID string, nodeIDs []string) (map[string]NodeImportState, error)
func (r *CatalogRepository) BatchUpsertSyncedNodes(ctx context.Context, nodes []CloudNode) error
```

`BatchUpsertSyncedNodes` must use one GORM transaction and the existing `(c_space_id,c_node_id)` conflict key. Updates must set `c_is_deleted=false` while preserving original `c_ctime`.

- [ ] **Step 4: Run store tests**

```bash
go test -race -count=1 ./modules/cloudnode/internal/store
```

Expected: PASS.

- [ ] **Step 5: Commit persistence support**

```bash
git add modules/cloudnode/internal/store/node.go modules/cloudnode/internal/store/node_test.go
git commit -m "feat(cloudnode): persist synced scf nodes"
```

---

### Task 4: Implement Secure Preview Eligibility

**Files:**
- Create: `modules/cloudnode/internal/rpc/scf_sync.go`
- Create: `modules/cloudnode/internal/rpc/scf_sync_test.go`
- Modify: `modules/cloudnode/internal/rpc/server.go`

- [ ] **Step 1: Write failing preview tests**

Use a fake SCF provider and real temporary SQLite. Cover missing Space context, missing/deleted account, unsupported provider, credential resolution failure, every entry in the 17-region catalog, per-region failure, Namespace pagination, Active and inactive functions, current/cross-Space functions, missing package marker, non-MooX visibility, soft-deleted classification, and duplicate Node ID collision.

- [ ] **Step 2: Run and verify RED**

```bash
go test -count=1 ./modules/cloudnode/internal/rpc -run 'TestPreviewSCFFunctions'
```

Expected: FAIL because the RPC does not exist.

- [ ] **Step 3: Implement bounded all-region preview**

Resolve the selected account once. Scan `tencent.SCFRegions()` through the `ListCloudRegions` catalog with concurrency `2`; this currently means all 17 supported regions, not four hardcoded regions. Within each region, keep provider pagination sequential. Fetch function detail with bounded concurrency `4` to inspect only the whitelisted identity keys. Return successful regions even when another region fails.

Eligibility requires:

```text
Status == Active
MOOX_SPACE_ID == current Space
MOOX_CODE_PACKAGE_ID is non-empty
remote Type is Event or HTTP
node_id has no cross-reference collision
```

Recognize `biz_type=market_fetcher` from the established `moox-fetcher-` function prefix or the exact `moox-market-fetch-timer` trigger. Never return Environment maps.

- [ ] **Step 4: Run preview tests with race detection**

```bash
go test -race -count=1 ./modules/cloudnode/internal/rpc -run 'TestPreviewSCFFunctions'
```

Expected: PASS with deterministic candidate ordering and no race.

- [ ] **Step 5: Commit preview behavior**

```bash
git add modules/cloudnode/internal/rpc/scf_sync.go modules/cloudnode/internal/rpc/scf_sync_test.go modules/cloudnode/internal/rpc/server.go
git commit -m "feat(cloudnode): preview importable scf nodes"
```

---

### Task 5: Implement Revalidated Idempotent Import

**Files:**
- Modify: `modules/cloudnode/internal/rpc/scf_sync.go`
- Modify: `modules/cloudnode/internal/rpc/scf_sync_test.go`

- [ ] **Step 1: Write failing import tests**

Cover empty selection, duplicate refs, forged package/Space fields being impossible by contract, cloud function deleted after preview, function changed to another Space after preview, inactive function, missing package marker, new/restored/unchanged counts, mixed provider failures, and complete Environment redaction.

- [ ] **Step 2: Run and verify RED**

```bash
go test -count=1 ./modules/cloudnode/internal/rpc -run 'TestImportSCFFunctions'
```

Expected: FAIL because import is absent.

- [ ] **Step 3: Re-read every selected function and persist only verified nodes**

For each unique reference, call `GetFunction` again and query only the fixed Timer trigger needed for trigger classification. Accumulate per-item failures without writing them. Persist all valid candidates in one batch transaction, then return `created/restored/unchanged/failed` counts and per-reference results.

Allowed Metadata keys are exactly:

```text
deployment_ready
runtime
memory_size
timeout_seconds
biz_type
import_source
imported_at
```

Do not persist remote Environment, DNS routes, assignments, Secret IDs, CLS fields, Storage targets, HMAC values, or access tokens.

- [ ] **Step 4: Run RPC and store tests**

```bash
go test -race -count=1 ./modules/cloudnode/internal/rpc ./modules/cloudnode/internal/store
```

Expected: PASS.

- [ ] **Step 5: Commit import behavior**

```bash
git add modules/cloudnode/internal/rpc/scf_sync.go modules/cloudnode/internal/rpc/scf_sync_test.go
git commit -m "feat(cloudnode): import verified scf nodes"
```

---

### Task 6: Add Typed Web API And State Model

**Files:**
- Modify: `web/src/api/cloud-node.ts`
- Modify: `web/src/api/cloud-node.test.ts`
- Create: `web/src/views/collector/cloud-node/composables/use-scf-sync.ts`
- Create: `web/src/views/collector/cloud-node/composables/use-scf-sync.test.ts`

- [ ] **Step 1: Write failing API and state tests**

Assert exact method names and bodies:

```ts
await previewSCFFunctions("account-a");
expect(callControl).toHaveBeenCalledWith("cloudnode", "PreviewSCFFunctions", { account_id: "account-a" });

await importSCFFunctions("account-a", selectedRefs);
expect(callControl).toHaveBeenCalledWith("cloudnode", "ImportSCFFunctions", {
  account_id: "account-a",
  functions: selectedRefs
});
```

State tests cover single-account default, rescan clearing stale selection, default selection of importable rows only, remove/unremove behavior, disabled confirmation while scanning/importing, and aggregate counts.

- [ ] **Step 2: Run and verify RED**

```bash
cd web
pnpm test -- src/api/cloud-node.test.ts src/views/collector/cloud-node/composables/use-scf-sync.test.ts
```

Expected: FAIL because API and composable are absent.

- [ ] **Step 3: Implement types, calls, and pure state transitions**

Keep provider response enum strings in the API layer and expose simple `canImport`, `selectedRefs`, `remove`, `restore`, and `resetForAccount` operations to the component. Do not cache preview data across Spaces.

- [ ] **Step 4: Run focused web tests**

Run the Step 2 command. Expected: PASS.

- [ ] **Step 5: Commit web data layer**

```bash
git add web/src/api/cloud-node.ts web/src/api/cloud-node.test.ts web/src/views/collector/cloud-node/composables/use-scf-sync.ts web/src/views/collector/cloud-node/composables/use-scf-sync.test.ts
git commit -m "feat(web): add scf sync data flow"
```

---

### Task 7: Build The Single-Dialog Sync Workbench

**Files:**
- Create: `web/src/views/collector/cloud-node/components/sync-scf-dialog.vue`
- Modify: `web/src/views/collector/cloud-node/cloud-node.vue`
- Modify: `web/tests/cloud-node-workflows.spec.ts`
- Create: `web/tests/cloud-node-scf-sync.spec.ts`

- [ ] **Step 1: Write failing component/browser workflow tests**

Mock cloud accounts, preview, and import APIs. Cover zero accounts with “去配置云账户”, one-account default, multiple-account required selection, scanning indicator, regional warning display, non-MooX disabled rows, removable selected rows, confirmation count, import summary, close/rescan, Space change cleanup, and parent list refresh.

- [ ] **Step 2: Run and verify RED**

```bash
cd web
pnpm test -- tests/cloud-node-workflows.spec.ts tests/cloud-node-scf-sync.spec.ts
```

Expected: FAIL because the toolbar action and dialog are absent.

- [ ] **Step 3: Implement the approved A layout**

Add a toolbar button with the existing icon library's refresh/cloud sync icon. The dialog contains account selection plus “扫描全部地域”, count tags, one table grouped/filterable by region, checkboxes for importable rows, an icon-only remove action with tooltip, regional error alerts, and a primary footer action `导入 N 个节点`.

Do not nest cards or add explanatory marketing text. Keep the table height stable, use the existing `8px`-or-smaller radius conventions, and ensure long function names ellipsize with a tooltip rather than resizing the dialog.

- [ ] **Step 4: Refresh the parent list after successful import**

On success, show `新增/恢复/已存在/失败` counts, close only after the user acknowledges the result, clear selection state, and call the existing node-list refresh. A failed import must keep rows visible for retry.

- [ ] **Step 5: Run web verification**

```bash
cd web
pnpm test
pnpm lint:eslint:check
pnpm build:prod
```

Expected: PASS without warnings or TypeScript failures.

- [ ] **Step 6: Verify responsive screenshots and interaction**

Use Playwright at `1440x900`, `1280x720`, and `390x844`. Assert the dialog, table, footer, account selector, count tags, and buttons do not overlap; confirm mocked preview/import calls occur and the rendered candidate table is nonblank.

- [ ] **Step 7: Commit the UI**

```bash
git add web/src/views/collector/cloud-node/components/sync-scf-dialog.vue web/src/views/collector/cloud-node/cloud-node.vue web/tests/cloud-node-workflows.spec.ts web/tests/cloud-node-scf-sync.spec.ts
git commit -m "feat(web): add cloud node sync dialog"
```

---

### Task 8: Add A Module-Level Sync E2E

**Files:**
- Create: `modules/cloudnode/test/scf_sync_e2e_test.go`
- Modify: `modules/cloudnode/internal/rpc/server.go` only if a production-safe dependency-injection Option is required

- [ ] **Step 1: Write the E2E using real SQLite and fake SCF inventory**

Seed one CloudAccount, return two Namespaces across two regions, include one importable Timer function, one importable Invoke function, one non-MooX function, and one region error. Call Preview, import one retained reference, call Preview again, and assert the node transitions from NEW to EXISTING while the excluded function remains absent.

- [ ] **Step 2: Run and verify RED**

```bash
go test -count=1 ./modules/cloudnode/test -run TestSCFSyncPreviewConfirmImport
```

Expected: FAIL before the fake provider injection seam is connected.

- [ ] **Step 3: Expose only a typed production Option if needed**

Prefer a narrow `SCFInventoryClient` interface and `WithSCFInventoryFactory` Option usable by tests and alternative providers. Do not export Tencent credentials or a general mutable Service hook.

- [ ] **Step 4: Run CloudNode proving set**

```bash
go test -race -count=1 ./modules/cloudnode/...
```

Expected: PASS.

- [ ] **Step 5: Commit E2E coverage**

```bash
git add modules/cloudnode/test/scf_sync_e2e_test.go modules/cloudnode/internal/rpc/server.go
git commit -m "test(cloudnode): cover scf preview and import"
```

---

### Task 9: Independent Security Review And Full Local Verification

**Files:**
- Review all files changed by Tasks 1-8 and the generated Proto diff

- [ ] **Step 1: Run focused backend and frontend verification**

```bash
go test -race -count=1 ./modules/cloudnode/...
cd web && pnpm test && pnpm lint:eslint:check && pnpm build:prod
```

- [ ] **Step 2: Run serialized workspace verification**

```bash
bash scripts/test-go-workspace.sh
make verify-pr
```

Do not run Proto generation concurrently with workspace tests.

- [ ] **Step 3: Dispatch independent `codeCR` review**

Review requirements: account/Space authorization, provider pagination, rate limiting, duplicate identities, TOCTOU revalidation, transaction behavior, partial failures, environment/secret redaction, imported deployment readiness, Collector compatibility, UI state on Space changes, and test completeness. Every finding must carry file/line evidence.

- [ ] **Step 4: Resolve all P0-P2 findings and rerun affected checks**

Do not treat a timed-out review as evidence.

---

### Task 10: Deploy And Recover The Current Production Catalog

**Files:**
- Deployment target: `ubuntu@106.53.107.122:/home/ubuntu/moox/prod`
- Browser target: `https://106.53.107.122:9527/#/collector/cloudnodes`

- [ ] **Step 1: Record and back up the empty control-plane state**

Capture service status, deployed SHA, current CloudNode/Collector counts, and timestamped copies of both SQLite databases. Record the current fresh K-line watermark before any restart.

- [ ] **Step 2: Deploy CloudNode/Admin/Web without reset**

Use repository build/deploy scripts and explicitly omit `--reset-data`. Verify local and remote binary/static-asset hashes and all changed service readiness endpoints.

- [ ] **Step 3: Restore cloud accounts through existing management**

Use the account IDs, credential secret references, App IDs, regions, and COS buckets already declared in `custom.toml`; never print SecretID/SecretKey. Verify `ListCloudAccounts` returns the intended accounts before scanning.

- [ ] **Step 4: Exercise real preview**

Select each account, scan all 17 supported regions, and compare the preview totals with direct Tencent inventory evidence. Confirm regional failures are visible, non-MooX functions cannot be selected, and no API/browser payload exposes full Environment or credentials.

- [ ] **Step 5: Confirm the retained import set**

Remove at least one disposable preview row before confirmation to prove exclusion behavior, then restore it or leave it excluded according to the actual desired fleet. Record the final selected count and regions.

- [ ] **Step 6: Import and verify local catalog**

Confirm SQLite and `GetNodeList` contain exactly the selected functions with correct account, region, Namespace, package marker, node type, trigger type, and `deployment_ready=true`. Re-run import and prove it reports unchanged rows without duplication.

- [ ] **Step 7: Complete Collector joint acceptance**

Verify the five default rules from the companion Collector plan, non-zero TaskInstances, successful Symbol refresh, Timer reconciliation, fresh SCF/CLS execution evidence, and continued fresh `binance_spot_kline_1m_view` rows. Distinguish control-plane restoration from the already-running legacy Timer writes.

- [ ] **Step 8: Browser acceptance**

At desktop and mobile widths, verify the cloud-node list is populated, the dialog remains usable, long names do not overlap, filters work, and post-import refresh needs no manual page reload.

- [ ] **Step 9: Push only after runtime proof**

Confirm the expected remote branch contains the reviewed commits and report exact local tests, `codeCR`, deployed hashes, preview/import counts, DB counts, browser screenshots, and K-line watermark.

## Acceptance Checklist

- Preview scans every entry in the complete supported-region catalog and all Namespaces with complete pagination.
- A regional provider failure does not erase successful results.
- All functions are visible; only verified current-Space MooX functions are selectable.
- Import re-reads cloud truth and does not trust browser metadata.
- No complete Environment or credential appears in response, logs, Metadata, or browser state.
- New and soft-deleted nodes are restored; rerun is idempotent.
- The approved single-dialog layout supports account choice, scan, removal, and confirmation.
- Current production CloudNode catalog is repopulated without data reset.
- Collector sees runnable imported nodes and fresh K-line writes continue.
