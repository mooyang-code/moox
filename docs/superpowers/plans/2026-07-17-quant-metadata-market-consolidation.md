# Quant Metadata Market Consolidation Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 将 Binance 和 OKX 收敛到统一的 `crypto` Space，并删除被默认量化初始化与正式文档替代的旧示例文件。

**Architecture:** `metadata-quant-initial.seed.yaml` 是业务初始化唯一事实源；Space 表达市场，DataSource 表达来源，单频 Dataset ID 表达来源、产品和频率。E2E 导入 `crypto` 子空间后，通过 Metadata API 注册测试 Subject，不再依赖静态交易所演示 seed。

**Tech Stack:** YAML、Go、Cobra CLI、Node.js E2E、Bash、Storage Metadata API

---

### Task 1: 固化统一市场元数据契约

**Files:**
- Create: `modules/cli/internal/command/metadata_quant_seed_test.go`
- Modify: `modules/storage/internal/bootstrap/metadata/quant_seed_test.go`
- Modify: `modules/storage/internal/service/access/quant_seed_e2e_test.go`
- Modify: `examples/metadata-quant-initial.seed.yaml`

- [ ] **Step 1: 编写失败的默认 seed 结构测试**

新增测试，加载 `examples/metadata-quant-initial.seed.yaml` 并断言：

```go
require.Equal(t, []string{"stock_cn", "stock_hk", "stock_us", "crypto"}, metadataSeedSpaceIDs(seed))
require.ElementsMatch(t, []string{"binance", "okx"}, cryptoDataSourceIDs(seed))
require.ElementsMatch(t, []string{
    "binance_spot_kline_1h",
    "binance_perpetual_kline_1h",
    "okx_spot_kline_1h",
    "okx_perpetual_kline_1h",
}, cryptoDatasetIDs(seed))
```

- [ ] **Step 2: 运行测试并确认旧模型导致失败**

Run: `go test ./modules/cli/internal/command -run TestQuantInitialSeedUsesUnifiedCryptoMarket -count=1`

Expected: FAIL，实际 Space 仍包含 `crypto_binance` 和 `crypto_okx`。

- [ ] **Step 3: 合并 Crypto 元数据**

将两个交易所 Space 改成一个：

```yaml
spaces:
  - { space_id: crypto, name: 加密货币市场, description: 加密货币现货与永续合约行情数据, owner: quant, status: active }
```

两个 DataSource、共享 FieldGroup/Field 和四个单频 Dataset 均归属 `crypto`。Dataset 使用：

```text
binance_spot_kline_1h
binance_perpetual_kline_1h
okx_spot_kline_1h
okx_perpetual_kline_1h
```

View ID 使用 `<data_source>_<instrument_type>_<frequency>_view`，以满足 30 字符限制；DatasetColumn 和 ViewColumn 全部更新为完整 Dataset ID。

- [ ] **Step 4: 更新 Storage seed 集成断言**

将导入统计改为 `Spaces=4`、`FieldGroups=13`、`Fields=45`，并逐个读取四个新的 Crypto View。

- [ ] **Step 5: 运行元数据测试**

Run: `go test ./modules/cli/internal/command ./modules/storage/internal/bootstrap/metadata ./modules/storage/internal/service/access -count=1`

Expected: PASS。

### Task 2: 迁移 E2E 到默认量化 seed

**Files:**
- Modify: `examples/e2e/run.sh`
- Modify: `examples/e2e/verify.mjs`
- Modify: `examples/e2e/README.md`
- Test: `examples/e2e/test-run-scf-once.sh`

- [ ] **Step 1: 写入新 E2E 静态契约并确认旧脚本不满足**

静态检查必须满足：

```bash
rg -q 'metadata-quant-initial.seed.yaml' examples/e2e/run.sh
rg -q 'binance_spot_kline_1h' examples/e2e/run.sh examples/e2e/verify.mjs
! rg -q 'metadata-crypto.seed.yaml|metadata-crypto-spot-kline-1m-view.seed.yaml' examples/e2e
```

首次运行预期失败。

- [ ] **Step 2: 修改 seed 导入和采集频率**

`run.sh` 只导入平台 seed 与量化 seed 的 `crypto` 子空间：

```bash
import_seed "platform-local.seed.yaml"
import_seed "metadata-quant-initial.seed.yaml" "crypto"
```

默认规则、Dataset 和 interval 统一为 `binance_spot_kline_1h` / `1h`。

- [ ] **Step 3: 通过 Metadata API 注册 E2E Subject**

在 `prepare` 阶段、管理请求断言前调用：

```js
await storagePost(args, token, "metadata", "RegisterDataSubject", {
  space_id: args.space,
  data_source_id: "binance",
  external_symbol: "BTCUSDT",
  subject: {
    subject_id: "BTC-USDT",
    subject_type: "crypto_pair",
    name: "Bitcoin / Tether",
    market: "crypto",
    currency: "USDT",
    timezone: "UTC",
    status: "active",
  },
  dataset_bindings: [{ dataset_id: args.dataset, subject_role: "normal", status: "active" }],
});
```

- [ ] **Step 4: 验证 E2E 脚本契约**

Run: `bash examples/e2e/test-run-scf-once.sh`

Expected: PASS。

Run: `bash -n examples/e2e/run.sh examples/e2e/run-scf-once.sh examples/e2e/test-run-scf-once.sh && node --check examples/e2e/verify.mjs`

Expected: PASS。

### Task 3: 删除旧示例并修正文档入口

**Files:**
- Delete: `examples/metadata-cn-stock.seed.yaml`
- Delete: `examples/metadata-crypto.seed.yaml`
- Delete: `examples/metadata-crypto-binance-swap-kline.seed.yaml`
- Delete: `examples/metadata-crypto-binance-swap-symbols.seed.yaml`
- Delete: `examples/metadata-crypto-spot-kline-1m-view.seed.yaml`
- Delete: `examples/data-crypto-record-symbol-profiles.json`
- Delete: `examples/trade-sync/README.md`
- Delete: `examples/e2e/cloudnode_jetstream_queue.md`
- Modify: `examples/README.md`
- Modify: `modules/cli/README.md`
- Modify: `modules/cli/internal/command/metadata_types.go`
- Modify: `docs/量化初始元数据设计.md`

- [ ] **Step 1: 删除被替代文件**

使用补丁删除八个旧文件，不删除平台、Monitor 或 E2E 可执行文件。

- [ ] **Step 2: 重写当前入口文档**

文档只展示：

```bash
moox-cli metadata import --file examples/platform-local.seed.yaml --if-not-exists
moox-cli metadata import --file examples/metadata-quant-initial.seed.yaml --if-not-exists
moox-cli metadata import --file examples/metadata-quant-initial.seed.yaml --spaces crypto --if-not-exists
```

更新 Space 和 Dataset 表，将 Crypto 描述改为统一 `crypto` Space 与四个带 `1h` 后缀的 Dataset。

- [ ] **Step 3: 扫描活动代码和文档中的旧引用**

Run:

```bash
rg -n 'crypto_binance|crypto_okx|metadata-crypto\.seed|metadata-cn-stock|metadata-crypto-binance|metadata-crypto-spot|data-crypto-record|trade-sync/README|cloudnode_jetstream_queue' examples modules/cli docs/量化初始元数据设计.md
```

Expected: 无输出；历史设计/计划文档不纳入该活动入口检查。

### Task 4: 全量验证并提交

**Files:**
- Verify: all files changed by Tasks 1-3

- [ ] **Step 1: 检查 YAML 与导入计划**

Run: `go run ./modules/cli/cmd/moox-cli metadata import --file examples/metadata-quant-initial.seed.yaml --dry-run`

Expected: JSON `status` 为 `dry_run`，命令退出码为 0。

- [ ] **Step 2: 运行仓库验证入口**

Run: `make verify`

Expected: PASS。

- [ ] **Step 3: 检查差异边界**

Run: `git diff --check && git status --short`

Expected: 无空白错误；`modules/strategy` 的并行修改不纳入本次提交。

- [ ] **Step 4: 提交并推送**

```bash
git add docs examples modules/cli modules/storage/internal/bootstrap/metadata/quant_seed_test.go modules/storage/internal/service/access/quant_seed_e2e_test.go
git commit -m "refactor: consolidate quant market metadata"
git push
```
