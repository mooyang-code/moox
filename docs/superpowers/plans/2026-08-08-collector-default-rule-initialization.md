# Collector 默认规则初始化实施计划

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 在 Collector 数据库首次创建或重建时幂等补齐完整的内置 Binance 采集规则，并修正规则页面与当前 Proto 的字段协议，使规则能够驱动 Scheduler 重新生成任务实例。

**Architecture:** 默认规则以固定 YAML Bundle 保存，`moox-collector-cli init` 在建表后执行“只插入缺失 Rule ID”的种子导入；已有规则及用户修改永不被初始化覆盖。TaskInstance 仍由 Collector Scheduler 根据规则和 Symbol Dataset 动态生成，不引入第二套实例种子。

**Tech Stack:** Go 1.25、SQLite/GORM、YAML v3、tRPC-Go、Vue 3、TypeScript、Vitest、Shell contract tests。

---

## Scope And Safety

- 当前线上 `t_collector_task_rules`、`t_collector_task_instances` 均为 `0`；Collector DB 与 CloudNode DB 在 2026-08-08 14:09 左右同时新建，符合 `--reset-data` 或等价清理后的状态。
- 本计划不直接写 TaskInstance。Scheduler 已负责按启用 Rule、Dataset Subjects 和 frequency 调用 `UpsertMany`。
- 初始化只补缺失 Rule ID，不更新同 ID 已有行；这样用户禁用规则或修改频率后，普通部署不会把配置改回默认值。
- 默认 Bundle 不包含账号、密钥、节点或代码包信息。
- 实施必须在独立 worktree 中进行。当前 `feature/mooyang` 工作树已有与 Storage、Setup 和文档有关的未提交修改，禁止覆盖或回滚。
- Proto 公共契约只使用 `provider` 和 `market_type`；不保留 `exchange` 请求字段兼容层。

## Built-In Rule Contract

`config/setup/collector-rules.yaml` 固定包含以下五条规则：

| rule_id | data_type | provider | market_type | target Dataset | frequency |
| --- | --- | --- | --- | --- | --- |
| `builtin-binance-spot-symbols-1h` | `symbol` | `binance` | `spot` | `dataset_binance_spot_symbols` | `1h` |
| `builtin-binance-swap-symbols-1h` | `symbol` | `binance` | `swap` | `dataset_binance_swap_symbols` | `1h` |
| `builtin-binance-spot-kline-1m` | `kline` | `binance` | `spot` | `dataset_binance_spot_kline_1m` | `1m` |
| `builtin-binance-spot-kline-1h` | `kline` | `binance` | `spot` | `dataset_spot_kline_1h` | `1h` |
| `builtin-binance-swap-kline-1h` | `kline` | `binance` | `swap` | `dataset_perpetual_kline_1h` | `1h` |

K 线规则分别引用 `dataset_binance_spot_symbols` 或 `dataset_binance_swap_symbols` 作为 `symbol_dataset_id`。种子中的 `collect_params` 必须是当前扁平契约：

```yaml
rules:
  - space_id: crypto
    rule_id: builtin-binance-spot-kline-1m
    data_type: kline
    provider: binance
    market_type: spot
    enabled: true
    creator: moox-setup
    collect_params:
      provider: binance
      market_type: spot
      symbol_source: dataset
      symbol_dataset_id: dataset_binance_spot_symbols
      target_dataset_id: dataset_binance_spot_kline_1m
      frequency: 1m
```

## File Map

| Path | Responsibility |
| --- | --- |
| `config/setup/collector-rules.yaml` | Stable built-in Collector Rule definitions |
| `config/setup/metadata.yaml` | Add the Binance swap Symbol Dataset and required market attributes |
| `modules/collector/internal/ruleseed/seed.go` | Parse, validate, and insert only missing default rules |
| `modules/collector/internal/ruleseed/seed_test.go` | Seed contract, validation, idempotence, and preserve-user-change tests |
| `modules/collector/cmd/cli/init_schema.go` | Add `--seed-file` and report created/unchanged counts |
| `modules/collector/cmd/cli/init_schema_test.go` | CLI init seed behavior and JSON output |
| `scripts/deploy/deploy-moox.sh` | Pass the packaged default seed to Collector init |
| `scripts/test/contract/test-deploy-moox-collector-seed.sh` | Prove bundle packaging and init wiring |
| `modules/cli/internal/command/default_setup_bundle_test.go` | Verify the complete default Metadata contract |
| `modules/cli/internal/command/metadata_quant_seed_test.go` | Verify new Dataset and frequency/market attributes |
| `web/src/views/collector/collector-rules/collector-rule-params.ts` | Canonical request/list normalization helpers |
| `web/src/views/collector/collector-rules/collector-rule-params.test.ts` | Current Proto field regression tests |
| `web/src/views/collector/collector-rules/collector-rules.vue` | Use canonical helpers and correct filters |
| `modules/collector/test/default_rule_init_e2e_test.go` | Real SQLite init, rerun, and Scheduler instance generation flow |

---

### Task 1: Add Complete Default Metadata Dependencies

**Files:**
- Modify: `config/setup/metadata.yaml`
- Modify: `modules/cli/internal/command/default_setup_bundle_test.go`
- Modify: `modules/cli/internal/command/metadata_quant_seed_test.go`

- [ ] **Step 1: Write failing default-bundle assertions**

Add assertions that the default Bundle contains `crypto/dataset_binance_swap_symbols`, that both Symbol Datasets are record Datasets with the correct `attributes.market_type`, and that the shared hourly targets expose `spot` or `swap` market attributes and frequency `1H`.

```go
assertDataset(t, seed, "crypto", "dataset_binance_swap_symbols", "binance", "record", "swap")
assertDataset(t, seed, "crypto", "dataset_spot_kline_1h", "crypto", "time_series", "spot")
assertDataset(t, seed, "crypto", "dataset_perpetual_kline_1h", "crypto", "time_series", "swap")
```

- [ ] **Step 2: Run tests and verify the missing Dataset/attributes fail**

Run:

```bash
go test -count=1 ./modules/cli/internal/command -run 'Test(DefaultSetupBundle|DefaultMetadata)'
```

Expected: FAIL because `dataset_binance_swap_symbols` and the shared target `market_type` attributes are absent.

- [ ] **Step 3: Add the Dataset and columns**

Add `dataset_binance_swap_symbols` beside `dataset_binance_spot_symbols`, with `data_source_id: binance`, `data_kind: record`, `keep_duration: '0'`, and `attributes.market_type: swap`. Add the same nine Symbol columns used by the spot Dataset: `symbol`, `external_symbol`, `base_asset`, `quote_asset`, `status`, `min_qty`, `max_qty`, `tick_size`, and `lot_size`; preserve each column's existing type/required semantics. Add `attributes.market_type: spot` to `dataset_spot_kline_1h` and `attributes.market_type: swap` to `dataset_perpetual_kline_1h`.

- [ ] **Step 4: Re-run the focused tests**

Run the Step 2 command. Expected: PASS, and the default Dataset count increases by one.

- [ ] **Step 5: Commit the metadata dependency**

```bash
git add config/setup/metadata.yaml modules/cli/internal/command/default_setup_bundle_test.go modules/cli/internal/command/metadata_quant_seed_test.go
git commit -m "feat(setup): add binance swap symbol metadata"
```

---

### Task 2: Define And Validate The Collector Rule Bundle

**Files:**
- Create: `config/setup/collector-rules.yaml`
- Create: `modules/collector/internal/ruleseed/seed.go`
- Create: `modules/collector/internal/ruleseed/seed_test.go`

- [ ] **Step 1: Write failing loader tests**

Cover all five fixed Rule IDs, duplicate `(space_id, rule_id)`, unknown YAML fields, empty identifiers, unsupported data types, mismatched top-level/provider parameters, and malformed frequency. The loader must use `yaml.Decoder.KnownFields(true)` and validate each decoded rule through `domain.ParseCollectParams(...).Validate()`.

```go
func TestLoadRuleSeedRejectsUnknownField(t *testing.T) {
    _, err := loadRuleSeed(strings.NewReader("rules:\n  - rule_id: r1\n    mystery: true\n"))
    require.ErrorContains(t, err, "field mystery not found")
}
```

- [ ] **Step 2: Verify the loader test fails**

```bash
go test -count=1 ./modules/collector/internal/ruleseed -run 'TestLoadRuleSeed'
```

Expected: FAIL because `loadRuleSeed` does not exist.

- [ ] **Step 3: Implement focused seed types and canonical validation**

Use a small internal shape and convert `CollectParams` to canonical JSON before persistence:

```go
type ruleSeed struct {
    Rules []ruleSeedItem `yaml:"rules"`
}

type ruleSeedItem struct {
    SpaceID      string         `yaml:"space_id"`
    RuleID       string         `yaml:"rule_id"`
    DataType     string         `yaml:"data_type"`
    Provider     string         `yaml:"provider"`
    MarketType   string         `yaml:"market_type"`
    Enabled      bool           `yaml:"enabled"`
    Creator      string         `yaml:"creator"`
    CollectParams map[string]any `yaml:"collect_params"`
}
```

`loadRuleSeed(io.Reader)` returns `[]domain.TaskRule`. Reject duplicate keys before opening the database. Do not accept legacy nested or `exchange` aliases.

- [ ] **Step 4: Populate the five-rule YAML and run tests**

Run:

```bash
go test -count=1 ./modules/collector/internal/ruleseed -run 'TestLoadRuleSeed|TestDefaultRuleSeed'
```

Expected: PASS with exactly five enabled rules in `crypto`.

- [ ] **Step 5: Commit the seed contract**

```bash
git add config/setup/collector-rules.yaml modules/collector/internal/ruleseed/seed.go modules/collector/internal/ruleseed/seed_test.go
git commit -m "feat(collector): define built-in binance rules"
```

---

### Task 3: Make Collector Init Missing-Only And Idempotent

**Files:**
- Modify: `modules/collector/cmd/cli/init_schema.go`
- Modify: `modules/collector/cmd/cli/init_schema_test.go`
- Modify: `modules/collector/internal/ruleseed/seed.go`
- Modify: `modules/collector/internal/ruleseed/seed_test.go`

- [ ] **Step 1: Write failing persistence tests**

Prove three behaviors against a temporary SQLite database:

1. An empty DB receives five rules.
2. Running init twice reports five unchanged rules and creates no duplicates.
3. If `builtin-binance-spot-kline-1m` is manually disabled or changed before the second run, init preserves that row exactly.

```go
first, err := seedMissingRules(ctx, store.TaskRules(), rules)
require.NoError(t, err)
assert.Equal(t, seedSummary{Created: 5, Unchanged: 0}, first)

second, err := seedMissingRules(ctx, store.TaskRules(), rules)
require.NoError(t, err)
assert.Equal(t, seedSummary{Created: 0, Unchanged: 5}, second)
```

- [ ] **Step 2: Run and verify RED**

```bash
go test -count=1 ./modules/collector/cmd/cli -run 'TestSeedMissingRules|TestRunInitCommandSeeds'
```

Expected: FAIL because init has no seed flag or persistence stage.

- [ ] **Step 3: Add `--seed-file` and structured counts**

Extend `initResult` with:

```go
RulesCreated   int `json:"rules_created"`
RulesUnchanged int `json:"rules_unchanged"`
```

After `ApplySchema`, load the supplied file and call `GetByRuleID`. Insert only on `gorm.ErrRecordNotFound`; return any other read or create error. Make the flag required only when explicitly passed by the deployment script, so standalone schema-only test invocations remain possible.

- [ ] **Step 4: Run the complete CLI package tests**

```bash
go test -count=1 ./modules/collector/cmd/cli
```

Expected: PASS, including the preserved user edit.

- [ ] **Step 5: Commit init behavior**

```bash
git add modules/collector/cmd/cli/init_schema.go modules/collector/cmd/cli/init_schema_test.go modules/collector/internal/ruleseed/seed.go modules/collector/internal/ruleseed/seed_test.go
git commit -m "feat(collector): seed missing rules during init"
```

---

### Task 4: Wire The Packaged Seed Into Deployment

**Files:**
- Modify: `scripts/deploy/deploy-moox.sh`
- Create: `scripts/test/contract/test-deploy-moox-collector-seed.sh`

- [ ] **Step 1: Write a failing deploy contract**

Assert that packaging copies `config/setup/collector-rules.yaml` and generated runtime init invokes:

```text
moox-collector-cli init --db-path ../data/collector/moox_collector.db --seed-file ../config/setup/collector-rules.yaml
```

Also assert that no deployment path uses `INSERT OR REPLACE` or deletes the Collector DB unless the caller explicitly supplied `--reset-data`.

- [ ] **Step 2: Run and verify RED**

```bash
bash scripts/test/contract/test-deploy-moox-collector-seed.sh
```

Expected: FAIL because the seed flag is not wired.

- [ ] **Step 3: Pass the packaged seed path from `init_collector_schema`**

Keep schema and seed in the same CLI invocation. Quote `${ROOT}` and do not resolve a source-checkout path on the remote host.

- [ ] **Step 4: Run deploy contract and syntax checks**

```bash
bash -n scripts/deploy/deploy-moox.sh
bash scripts/test/contract/test-deploy-moox-collector-seed.sh
```

Expected: PASS.

- [ ] **Step 5: Commit deployment wiring**

```bash
git add scripts/deploy/deploy-moox.sh scripts/test/contract/test-deploy-moox-collector-seed.sh
git commit -m "fix(deploy): initialize collector default rules"
```

---

### Task 5: Repair The Collector Rule Web Contract

**Files:**
- Modify: `web/src/views/collector/collector-rules/collector-rule-params.ts`
- Modify: `web/src/views/collector/collector-rules/collector-rule-params.test.ts`
- Modify: `web/src/views/collector/collector-rules/collector-rules.vue`

- [ ] **Step 1: Write failing request/list normalization tests**

Add helpers whose public TypeScript shape uses UI-friendly `exchange` only inside `CollectorRuleInput`, but emits current RPC fields:

```ts
expect(buildCollectorRuleRequest(input, "crypto", "admin")).toMatchObject({
  space_id: "crypto",
  data_type: "kline",
  provider: "binance",
  market_type: "spot"
});
expect(buildCollectorRuleRequest(input, "crypto", "admin")).not.toHaveProperty("exchange");
expect(normalizeCollectorRule({ provider: "binance" }).data_source).toBe("binance");
```

- [ ] **Step 2: Verify RED**

```bash
cd web
pnpm test -- src/views/collector/collector-rules/collector-rule-params.test.ts
```

Expected: FAIL because the helpers and `provider` mapping are absent.

- [ ] **Step 3: Implement canonical helpers and update the page**

Use `provider` for list filters, create, update, enable, and list normalization. Include `market_type` in create/update payloads. Keep `collect_params` as the already canonical object produced by `buildCollectorRuleParams`.

- [ ] **Step 4: Run frontend tests and type/build checks**

```bash
cd web
pnpm test -- src/views/collector/collector-rules/collector-rule-params.test.ts tests/page-layout-standard-contract.test.ts
pnpm build:prod
```

Expected: PASS with no TypeScript error.

- [ ] **Step 5: Commit the web protocol fix**

```bash
git add web/src/views/collector/collector-rules/collector-rule-params.ts web/src/views/collector/collector-rules/collector-rule-params.test.ts web/src/views/collector/collector-rules/collector-rules.vue
git commit -m "fix(web): align collector rules with provider contract"
```

---

### Task 6: Add A Module-Level Initialization E2E

**Files:**
- Create: `modules/collector/test/default_rule_init_e2e_test.go`

- [ ] **Step 1: Write the failing E2E**

The test must use a real temporary SQLite DB and the real seed file. It should apply schema, seed twice, load the enabled rules, provide deterministic spot/swap Symbol snapshots, run one Scheduler planning cycle, and assert that expected TaskInstances exist for `1m` and `1h`. It must not call Tencent Cloud.

- [ ] **Step 2: Run and verify RED before connecting the final orchestration seam**

```bash
go test -count=1 ./modules/collector/test -run TestDefaultRuleInitRebuildsTaskInstances
```

Expected: FAIL at the missing Scheduler orchestration fixture, not because of network access.

- [ ] **Step 3: Reuse the internal rule seed package from the E2E**

Call `ruleseed.Load` and `ruleseed.SeedMissing` from the E2E; do not duplicate YAML parsing or insert SQL in the test. Add only deterministic fake Dataset/Symbol and node adapters needed by the real Scheduler.

- [ ] **Step 4: Run Collector proving set**

```bash
go test -race -count=1 ./modules/collector/...
```

Expected: PASS.

- [ ] **Step 5: Commit E2E coverage**

```bash
git add modules/collector/internal/ruleseed/seed.go modules/collector/internal/ruleseed/seed_test.go modules/collector/cmd/cli/init_schema.go modules/collector/cmd/cli/init_schema_test.go modules/collector/test/default_rule_init_e2e_test.go
git commit -m "test(collector): cover default rule initialization"
```

---

### Task 7: Independent Review And Local Verification

**Files:**
- Review all files changed by Tasks 1-6

- [ ] **Step 1: Run serialized generation/checks and focused tests**

```bash
go test -race -count=1 ./modules/collector/...
go test -count=1 ./modules/cli/internal/command
bash scripts/test/contract/test-deploy-moox-collector-seed.sh
bash -n scripts/deploy/deploy-moox.sh
cd web && pnpm test && pnpm build:prod
```

- [ ] **Step 2: Run repository verification**

```bash
bash scripts/test/contract/test-go-workspace.sh
make verify-pr
```

Run these after focused tests and never concurrently with generated-file work.

- [ ] **Step 3: Dispatch independent `codeCR` review**

Review requirements: missing-only idempotence, preservation of user edits, canonical Proto fields, Metadata/rule consistency, Scheduler ownership, deployment reset behavior, secrets, and test gaps. Findings must include file and line references.

- [ ] **Step 4: Fix P0-P2 findings and rerun the affected proving set**

Do not mark review complete while an agent is timed out or while a finding lacks a retest.

---

### Task 8: Production Recovery And Acceptance

**Files:**
- Deployment target: `ubuntu@106.53.107.122:/home/ubuntu/moox/prod`
- Browser target: `https://106.53.107.122:9527/#/collector/rules`

- [ ] **Step 1: Capture pre-deploy evidence and backups**

Record service status, current SHA, DB counts, and fresh K-line watermark. Back up `data/collector/moox_collector.db` and `data/cloudnode/moox_cloudnode.db` with timestamps.

- [ ] **Step 2: Deploy without `--reset-data`**

Build and deploy Collector, CLI, Web, and the changed default examples through repository scripts. Explicitly reject a command line containing `--reset-data`.

- [ ] **Step 3: Verify seed results in SQLite and RPC**

Expected after init:

```text
t_collector_task_rules = 5
rule IDs exactly match the built-in contract
existing same-ID rows were not overwritten
```

- [ ] **Step 4: Verify the browser rule page**

At desktop and mobile widths, confirm the five rows display provider and market correctly, create/update sends `provider` and `market_type`, and controls do not overlap.

- [ ] **Step 5: Complete joint acceptance after CloudNode sync plan**

After cloud nodes are imported, wait for Symbol refresh and Scheduler reconciliation. Prove TaskInstance count is non-zero, both spot and swap rules generate their expected frequencies, Timer assignments are active, and `view_crypto_spot_kline_1m` continues receiving fresh rows.

- [ ] **Step 6: Commit/push only after runtime proof**

Confirm local/remote branch refs and include exact test, review, deployment, browser, DB, and data-watermark evidence in the delivery summary.

## Acceptance Checklist

- Five built-in rules appear after a fresh Collector DB init.
- Re-running init is idempotent and preserves user edits.
- `dataset_binance_swap_symbols` and shared hourly targets satisfy Collector Dataset validation.
- Rule page never sends the obsolete `exchange` RPC field.
- TaskInstances are Scheduler-generated, not statically seeded.
- Deployment without reset restores rules on the current host.
- Joint CloudNode import restores runnable nodes and fresh K-line data remains visible.
