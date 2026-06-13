# moox Monorepo And Quant Protocol Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 将 moox 迁移为唯一大仓，并按新概念体系完整重设计 moox 与 xData 的 PB 协议、生成代码、实现全部服务接口、保证所有模块可编译测试。

**Architecture:** 第一阶段采用 `modules/ + go.work + 多 go.mod`。控制面进入 `modules/control`，统一 CLI 进入 `modules/cli`，xData 存储面进入 `modules/storage`，采集以及原 `data-miner` 的交易所连接、调度限频、标的发现能力统一进入 `modules/collector`，因子、订单、账户先建可编译骨架。协议按 `common.proto`、`metadata.proto`、`data.proto`、`query.proto`、`adapter.proto` 和 moox 侧 `control.proto`、`collector.proto`、`node.proto`、`task.proto` 落地。

**Tech Stack:** Go 1.24、tRPC-Go、Protocol Buffers、OpenSpec、Pebble、DuckDB、Bleve、NATS、SQLite/GORM、Makefile、shell build scripts。

---

## File Structure

**Repository roots:**

- Main repo: `/Users/mooyang/Documents/go/src/github.com/mooyang-code/moox`
- Source repo: `/Users/mooyang/Documents/go/src/github.com/mooyang-code/xData-mini`
- Source repo: `/Users/mooyang/Documents/go/src/github.com/mooyang-code/data-collector`
- Capability source repo: `/Users/mooyang/Documents/go/src/github.com/mooyang-code/data-miner`
- Source repo: `/Users/mooyang/Documents/go/src/github.com/mooyang-code/factor-calculator`
- Source repo: `/Users/mooyang/Documents/go/src/github.com/mooyang-code/order-center`
- Source repo: `/Users/mooyang/Documents/go/src/github.com/mooyang-code/account-center`

**Create in moox root:**

- `/Users/mooyang/Documents/go/src/github.com/mooyang-code/moox/go.work`
- `/Users/mooyang/Documents/go/src/github.com/mooyang-code/moox/Makefile`
- `/Users/mooyang/Documents/go/src/github.com/mooyang-code/moox/.gitignore`
- `/Users/mooyang/Documents/go/src/github.com/mooyang-code/moox/build/build.sh`
- `/Users/mooyang/Documents/go/src/github.com/mooyang-code/moox/build/test.sh`
- `/Users/mooyang/Documents/go/src/github.com/mooyang-code/moox/build/release.sh`
- `/Users/mooyang/Documents/go/src/github.com/mooyang-code/moox/build/deploy.sh`
- `/Users/mooyang/Documents/go/src/github.com/mooyang-code/moox/build/acceptance.sh`
- `/Users/mooyang/Documents/go/src/github.com/mooyang-code/moox/build/package-skill.sh`
- `/Users/mooyang/Documents/go/src/github.com/mooyang-code/moox/skills/moox/SKILL.md`
- `/Users/mooyang/Documents/go/src/github.com/mooyang-code/moox/skills/moox/references/build.md`
- `/Users/mooyang/Documents/go/src/github.com/mooyang-code/moox/skills/moox/references/storage.md`
- `/Users/mooyang/Documents/go/src/github.com/mooyang-code/moox/skills/moox/references/protocol.md`
- `/Users/mooyang/Documents/go/src/github.com/mooyang-code/moox/skills/moox/references/release.md`

**Migrate into moox root:**

- `/Users/mooyang/Documents/go/src/github.com/mooyang-code/moox/server` -> `/Users/mooyang/Documents/go/src/github.com/mooyang-code/moox/modules/control`
- `/Users/mooyang/Documents/go/src/github.com/mooyang-code/moox/cli` -> `/Users/mooyang/Documents/go/src/github.com/mooyang-code/moox/modules/cli`
- `/Users/mooyang/Documents/go/src/github.com/mooyang-code/xData-mini/storage` -> `/Users/mooyang/Documents/go/src/github.com/mooyang-code/moox/modules/storage`
- `/Users/mooyang/Documents/go/src/github.com/mooyang-code/data-collector` -> `/Users/mooyang/Documents/go/src/github.com/mooyang-code/moox/modules/collector`
- `/Users/mooyang/Documents/go/src/github.com/mooyang-code/data-miner` useful exchange-source code -> `/Users/mooyang/Documents/go/src/github.com/mooyang-code/moox/modules/collector/internal/source`
- `/Users/mooyang/Documents/go/src/github.com/mooyang-code/data-miner` useful symbol-discovery code -> `/Users/mooyang/Documents/go/src/github.com/mooyang-code/moox/modules/collector/internal/discovery`
- `/Users/mooyang/Documents/go/src/github.com/mooyang-code/data-miner` useful scheduling/rate-limit code -> `/Users/mooyang/Documents/go/src/github.com/mooyang-code/moox/modules/collector/internal/scheduler`
- `/Users/mooyang/Documents/go/src/github.com/mooyang-code/xData-mini/cli` commands -> `/Users/mooyang/Documents/go/src/github.com/mooyang-code/moox/modules/cli`

**Protocol targets:**

- Storage proto: `/Users/mooyang/Documents/go/src/github.com/mooyang-code/moox/modules/storage/proto/common.proto`
- Storage proto: `/Users/mooyang/Documents/go/src/github.com/mooyang-code/moox/modules/storage/proto/metadata.proto`
- Storage proto: `/Users/mooyang/Documents/go/src/github.com/mooyang-code/moox/modules/storage/proto/data.proto`
- Storage proto: `/Users/mooyang/Documents/go/src/github.com/mooyang-code/moox/modules/storage/proto/query.proto`
- Storage proto: `/Users/mooyang/Documents/go/src/github.com/mooyang-code/moox/modules/storage/proto/adapter.proto`
- Control proto: `/Users/mooyang/Documents/go/src/github.com/mooyang-code/moox/modules/control/proto/control.proto`
- Control proto: `/Users/mooyang/Documents/go/src/github.com/mooyang-code/moox/modules/control/proto/collector.proto`
- Control proto: `/Users/mooyang/Documents/go/src/github.com/mooyang-code/moox/modules/control/proto/node.proto`
- Control proto: `/Users/mooyang/Documents/go/src/github.com/mooyang-code/moox/modules/control/proto/task.proto`

**Acceptance data and remote target:**

- Remote host: `43.132.204.177`
- Remote deploy root: `~/moox`
- Remote binary path: `~/moox/bin`
- Remote config path: `~/moox/configs`
- Remote runtime path: `~/moox/var`
- Remote control runtime path: `~/moox/var/control`
- Remote xData storage runtime path: `~/moox/var/storage`
- Remote log path: `~/moox/var/log`
- Remote acceptance data path: `~/moox/var/storage/acceptance`
- Local CSV: `/Users/mooyang/Downloads/APT-USDT.csv`
- Local CSV: `/Users/mooyang/Downloads/AR-USDT.csv`
- Acceptance workspace: `default`
- Acceptance exchange: `BINANCE`
- Acceptance dataset: `binance_spot_kline_1m`

---

## Phase 0: Execution Guards And Baseline

- [ ] **0.1 Confirm no hidden work is lost**

  Run:

  ```bash
  git -C /Users/mooyang/Documents/go/src/github.com/mooyang-code/moox status --short
  git -C /Users/mooyang/Documents/go/src/github.com/mooyang-code/xData-mini status --short
  git -C /Users/mooyang/Documents/go/src/github.com/mooyang-code/data-collector status --short
  ```

  Expected: status output is recorded in the implementation log. Existing `adapter` rename changes in `xData-mini` and planning docs in `moox` are preserved and not reverted.

- [ ] **0.2 Create a migration branch in moox**

  Run:

  ```bash
  cd /Users/mooyang/Documents/go/src/github.com/mooyang-code/moox
  git switch -c feat/modules-monorepo-quant-protocol
  ```

  Expected: the branch exists and all existing uncommitted files remain in the worktree.

- [ ] **0.3 Commit current planning artifacts before moving code**

  Run:

  ```bash
  cd /Users/mooyang/Documents/go/src/github.com/mooyang-code/moox
  git add docs/monorepo-architecture.md docs/pb-protocol-redesign.md docs/superpowers/plans/2026-06-13-monorepo-and-protocol-implementation.md openspec .codex
  git commit -m "docs: plan moox monorepo and protocol migration"
  ```

  Expected: moox has a clean checkpoint for docs and OpenSpec artifacts. If `.codex` contains local-only generated files that should not be committed, remove `.codex` from the `git add` command and record that decision in the implementation log.

- [ ] **0.4 Finalize `adapter` rename source state in xData-mini**

  Run:

  ```bash
  cd /Users/mooyang/Documents/go/src/github.com/mooyang-code/xData-mini
  git add -A storage
  git diff --cached --name-status | rg "adaptor|adapter"
  git commit -m "refactor: rename storage adaptor to adapter"
  ```

  Expected: the source repo records the already-completed `adaptor -> adapter` rename before copying into moox.

- [ ] **0.5 Capture baseline test results**

  Run:

  ```bash
  cd /Users/mooyang/Documents/go/src/github.com/mooyang-code/moox/cli && go test ./...
  cd /Users/mooyang/Documents/go/src/github.com/mooyang-code/moox/server && go test ./...
  cd /Users/mooyang/Documents/go/src/github.com/mooyang-code/xData-mini/cli && go test ./...
  cd /Users/mooyang/Documents/go/src/github.com/mooyang-code/xData-mini/storage && make test
  cd /Users/mooyang/Documents/go/src/github.com/mooyang-code/data-collector && go test ./...
  ```

  Expected: each command result is recorded. A pre-existing failure becomes a tracked baseline issue, not a migration regression.

---

## Phase 1: Monorepo Skeleton

- [ ] **1.1 Create root directories**

  Create:

  ```text
  /Users/mooyang/Documents/go/src/github.com/mooyang-code/moox/modules
  /Users/mooyang/Documents/go/src/github.com/mooyang-code/moox/build
  /Users/mooyang/Documents/go/src/github.com/mooyang-code/moox/build/make
  /Users/mooyang/Documents/go/src/github.com/mooyang-code/moox/skills/moox/references
  /Users/mooyang/Documents/go/src/github.com/mooyang-code/moox/configs
  /Users/mooyang/Documents/go/src/github.com/mooyang-code/moox/deployments
  /Users/mooyang/Documents/go/src/github.com/mooyang-code/moox/var
  ```

  Expected: root layout matches `docs/monorepo-architecture.md`.

- [ ] **1.2 Add root `.gitignore`**

  Modify:

  ```text
  /Users/mooyang/Documents/go/src/github.com/mooyang-code/moox/.gitignore
  ```

  Required entries:

  ```gitignore
  /var/
  /bin/
  /release/
  /dist/
  /coverage/
  /modules/*/bin/
  /modules/*/release/
  /modules/*/.cache/
  /modules/storage/.deps/
  /modules/storage/data/
  *.test
  cover.out
  cover.out.tmp
  ```

- [ ] **1.3 Add root `go.work`**

  Create:

  ```text
  /Users/mooyang/Documents/go/src/github.com/mooyang-code/moox/go.work
  ```

  First target content after module migration:

  ```go
  go 1.24.0

  use (
      ./modules/control
      ./modules/control/proto/gen
      ./modules/cli
      ./modules/storage
      ./modules/storage/proto/gen
      ./modules/collector
      ./modules/factor
      ./modules/order
      ./modules/account
  )
  ```

  Expected: `go work sync` succeeds after referenced module directories exist.

- [ ] **1.4 Add root Makefile as a thin dispatcher**

  Create:

  ```text
  /Users/mooyang/Documents/go/src/github.com/mooyang-code/moox/Makefile
  ```

  Required targets:

  ```makefile
  .PHONY: build test release deploy acceptance package-skill clean proto

  build:
  	./build/build.sh

  test:
  	./build/test.sh

  release:
  	./build/release.sh

  deploy:
  	./build/deploy.sh

  acceptance:
  	./build/acceptance.sh

  package-skill:
  	./build/package-skill.sh

  proto:
  	./build/build.sh proto

  clean:
  	rm -rf bin release dist coverage
  	find modules -maxdepth 2 -type d \( -name bin -o -name release \) -prune -exec rm -rf {} +
  ```

- [ ] **1.5 Commit skeleton**

  Run:

  ```bash
  cd /Users/mooyang/Documents/go/src/github.com/mooyang-code/moox
  git add .gitignore Makefile modules build skills configs deployments var go.work
  git commit -m "chore: add moox monorepo skeleton"
  ```

---

## Phase 2: Move Existing Modules Into `modules/`

- [ ] **2.1 Move moox server to `modules/control`**

  Move:

  ```text
  /Users/mooyang/Documents/go/src/github.com/mooyang-code/moox/server
  -> /Users/mooyang/Documents/go/src/github.com/mooyang-code/moox/modules/control
  ```

  Then move:

  ```text
  /Users/mooyang/Documents/go/src/github.com/mooyang-code/moox/modules/control/main.go
  -> /Users/mooyang/Documents/go/src/github.com/mooyang-code/moox/modules/control/cmd/moox-server/main.go
  ```

  Update:

  ```text
  /Users/mooyang/Documents/go/src/github.com/mooyang-code/moox/modules/control/go.mod
  /Users/mooyang/Documents/go/src/github.com/mooyang-code/moox/modules/control/Makefile
  /Users/mooyang/Documents/go/src/github.com/mooyang-code/moox/modules/control/config/*
  /Users/mooyang/Documents/go/src/github.com/mooyang-code/moox/modules/control/proto/Makefile
  ```

  Required module path:

  ```text
  github.com/mooyang-code/moox/modules/control
  ```

- [ ] **2.2 Move moox CLI to `modules/cli`**

  Move:

  ```text
  /Users/mooyang/Documents/go/src/github.com/mooyang-code/moox/cli
  -> /Users/mooyang/Documents/go/src/github.com/mooyang-code/moox/modules/cli
  ```

  Then move:

  ```text
  /Users/mooyang/Documents/go/src/github.com/mooyang-code/moox/modules/cli/main.go
  -> /Users/mooyang/Documents/go/src/github.com/mooyang-code/moox/modules/cli/cmd/moox-cli/main.go
  ```

  Required module path:

  ```text
  github.com/mooyang-code/moox/modules/cli
  ```

- [ ] **2.3 Copy xData storage to `modules/storage`**

  Copy from source repo, excluding `.git`, `.codex`, `bin`, `release`, `.cache`, `.deps`, and generated runtime data:

  ```text
  /Users/mooyang/Documents/go/src/github.com/mooyang-code/xData-mini/storage
  -> /Users/mooyang/Documents/go/src/github.com/mooyang-code/moox/modules/storage
  ```

  Then move:

  ```text
  /Users/mooyang/Documents/go/src/github.com/mooyang-code/moox/modules/storage/main.go
  -> /Users/mooyang/Documents/go/src/github.com/mooyang-code/moox/modules/storage/cmd/moox-storage/main.go
  ```

  Required module path:

  ```text
  github.com/mooyang-code/moox/modules/storage
  ```

- [ ] **2.4 Copy data-collector to `modules/collector`**

  Copy from source repo, excluding `.git`, `.claude`, `scf-build`, generated binaries, and release artifacts:

  ```text
  /Users/mooyang/Documents/go/src/github.com/mooyang-code/data-collector
  -> /Users/mooyang/Documents/go/src/github.com/mooyang-code/moox/modules/collector
  ```

  Move:

  ```text
  /Users/mooyang/Documents/go/src/github.com/mooyang-code/moox/modules/collector/cmd/serverless/main.go
  -> /Users/mooyang/Documents/go/src/github.com/mooyang-code/moox/modules/collector/cmd/moox-collector/main.go
  ```

  Required module path:

  ```text
  github.com/mooyang-code/moox/modules/collector
  ```
- [ ] **2.5 Merge useful data-miner capabilities into `modules/collector`**

  Copy useful packages from:

  ```text
  /Users/mooyang/Documents/go/src/github.com/mooyang-code/data-miner/internal/exchanges
  /Users/mooyang/Documents/go/src/github.com/mooyang-code/data-miner/internal/scheduler
  /Users/mooyang/Documents/go/src/github.com/mooyang-code/data-miner/internal/types
  /Users/mooyang/Documents/go/src/github.com/mooyang-code/data-miner/pkg/cryptotrader
  /Users/mooyang/Documents/go/src/github.com/mooyang-code/data-miner/pkg/utils
  ```

  Into collector internals:

  ```text
  /Users/mooyang/Documents/go/src/github.com/mooyang-code/moox/modules/collector/internal/source
  /Users/mooyang/Documents/go/src/github.com/mooyang-code/moox/modules/collector/internal/discovery
  /Users/mooyang/Documents/go/src/github.com/mooyang-code/moox/modules/collector/internal/scheduler
  ```

  Required result: no `modules/miner` or `moox-miner` module is created. Collector owns market data collection, exchange clients, symbol discovery, scheduling, rate limiting, retry, and storage writing.

- [ ] **2.6 Create factor/order/account skeletons**

  Create:

  ```text
  /Users/mooyang/Documents/go/src/github.com/mooyang-code/moox/modules/factor/go.mod
  /Users/mooyang/Documents/go/src/github.com/mooyang-code/moox/modules/factor/cmd/moox-factor/main.go
  /Users/mooyang/Documents/go/src/github.com/mooyang-code/moox/modules/order/go.mod
  /Users/mooyang/Documents/go/src/github.com/mooyang-code/moox/modules/order/cmd/moox-order/main.go
  /Users/mooyang/Documents/go/src/github.com/mooyang-code/moox/modules/account/go.mod
  /Users/mooyang/Documents/go/src/github.com/mooyang-code/moox/modules/account/cmd/moox-account/main.go
  ```

  Required behavior: each binary supports `--version` and exits successfully.

- [ ] **2.7 Rewrite import paths after moves**

  Run:

  ```bash
  cd /Users/mooyang/Documents/go/src/github.com/mooyang-code/moox
  rg -n "github.com/mooyang-code/moox/server|github.com/mooyang-code/moox/cli|github.com/mooyang-code/xData-mini/storage|github.com/mooyang-code/xData-mini/cli|github.com/mooyang-code/data-collector" modules
  ```

  Replace:

  ```text
  github.com/mooyang-code/moox/server -> github.com/mooyang-code/moox/modules/control
  github.com/mooyang-code/moox/server/proto/gen -> github.com/mooyang-code/moox/modules/control/proto/gen
  github.com/mooyang-code/moox/cli -> github.com/mooyang-code/moox/modules/cli
  github.com/mooyang-code/xData-mini/storage -> github.com/mooyang-code/moox/modules/storage
  github.com/mooyang-code/xData-mini/storage/proto/gen -> github.com/mooyang-code/moox/modules/storage/proto/gen
  github.com/mooyang-code/data-collector -> github.com/mooyang-code/moox/modules/collector
  ```

- [ ] **2.8 Verify moved modules compile before protocol rewrite**

  Run:

  ```bash
  cd /Users/mooyang/Documents/go/src/github.com/mooyang-code/moox
  go work sync
  cd modules/control && go test ./...
  cd ../cli && go test ./...
  cd ../storage && make test
  cd ../collector && go test ./...
  cd ../factor && go test ./...
  cd ../order && go test ./...
  cd ../account && go test ./...
  ```

  Expected: all module baseline tests pass or only known pre-migration baseline failures remain documented.

- [ ] **2.9 Commit module move checkpoint**

  Run:

  ```bash
  cd /Users/mooyang/Documents/go/src/github.com/mooyang-code/moox
  git add -A
  git commit -m "refactor: migrate projects into moox modules"
  ```

---

## Phase 3: Define New Storage Protocol Contract

- [ ] **3.1 Replace storage proto file set**

  In:

  ```text
  /Users/mooyang/Documents/go/src/github.com/mooyang-code/moox/modules/storage/proto
  ```

  Keep:

  ```text
  common.proto
  metadata.proto
  data.proto
  query.proto
  adapter.proto
  message.proto
  Makefile
  ```

  Remove public usage of old:

  ```text
  access.proto
  dbmanager.proto
  ```

  Required rule: table management remains internal implementation detail behind metadata/storage route operations.

- [ ] **3.2 Implement `common.proto` shared types**

  Define these messages and enums:

  ```text
  AuthInfo
  RetInfo
  Page
  PageResult
  TimeRange
  TypedValue
  ValueList
  DimensionValue
  DimensionValues
  FieldValue
  SortSpec
  FilterExpr
  WriteMode
  DataKind
  DataDomain
  FieldValueType
  ColumnOrigin
  ErrorCode
  ```

  Required `ErrorCode` values:

  ```text
  SUCCESS
  INVALID_PARAM
  NO_AUTH
  NO_PERMISSION
  INNER_ERR
  WORKSPACE_NOT_FOUND
  DATASET_NOT_FOUND
  INSTRUMENT_NOT_FOUND
  FIELD_NOT_FOUND
  FACTOR_INSTANCE_NOT_FOUND
  DATA_VIEW_NOT_READY
  DATA_VIEW_COLUMN_NOT_FOUND
  QUERY_SHAPE_UNSUPPORTED
  ROUTE_NOT_FOUND
  ROUTE_CROSS_DEVICE_UNSUPPORTED
  ENGINE_CAPABILITY_UNSUPPORTED
  DIMENSION_VALUE_INVALID
  ```

- [ ] **3.3 Implement `metadata.proto` model messages**

  Define:

  ```text
  Workspace
  MarketInfo
  Exchange
  Instrument
  InstrumentAlias
  DataSet
  DimensionDef
  Field
  FactorDef
  FactorInstance
  DataView
  DataViewColumn
  DataViewVersion
  DataViewQueryConfig
  StorageDevice
  StorageRoute
  CollectorDataSetBinding
  ```

  Required naming:

  ```text
  workspace_id
  dataset_id
  instrument_id
  internal_symbol
  external_symbol
  data_kind
  data_domain
  dimension_values
  column_origin
  snapshot_time
  ```

  Forbidden public fields:

  ```text
  project_id
  proj_id
  object_id
  partition_key
  data_view_metric
  source_type
  as_of_time_ms
  start_time_ms
  end_time_ms
  ```

- [ ] **3.4 Implement `MetadataService` RPCs**

  Add every RPC below to `metadata.proto` and implement it later:

  ```text
  CreateWorkspace
  UpdateWorkspace
  GetWorkspace
  ListWorkspaces
  CreateExchange
  UpdateExchange
  GetExchange
  ListExchanges
  UpsertInstrument
  GetInstrument
  ListInstruments
  UpsertInstrumentAlias
  ListInstrumentAliases
  CreateDataSet
  UpdateDataSet
  GetDataSet
  ListDataSets
  CreateField
  UpdateField
  GetField
  ListFields
  CreateFactorDef
  UpdateFactorDef
  GetFactorDef
  ListFactorDefs
  CreateFactorInstance
  UpdateFactorInstance
  GetFactorInstance
  ListFactorInstances
  CreateDataView
  UpdateDataView
  GetDataView
  ListDataViews
  CreateStorageDevice
  UpdateStorageDevice
  GetStorageDevice
  ListStorageDevices
  CreateStorageRoute
  UpdateStorageRoute
  GetStorageRoute
  ListStorageRoutes
  ConfigureCollectorDataSetBinding
  ListCollectorDataSetBindings
  ```

- [ ] **3.5 Implement `data.proto` request and response messages**

  Define:

  ```text
  DataRef
  Record
  RecordMutation
  TimeSeriesPoint
  FactorValuePoint
  LatestSnapshotRow
  UpsertRecordsReq/Rsp
  QueryRecordsReq/Rsp
  SetTimeSeriesReq/Rsp
  ScanTimeSeriesReq/Rsp
  SetFactorValuesReq/Rsp
  ScanFactorValuesReq/Rsp
  GetLatestSnapshotReq/Rsp
  ```

  Required `DataService` RPCs:

  ```text
  UpsertRecords
  QueryRecords
  SetTimeSeries
  ScanTimeSeries
  SetFactorValues
  ScanFactorValues
  GetLatestSnapshot
  ```

- [ ] **3.6 Implement `query.proto` query contract**

  Define:

  ```text
  QueryTime
  QueryFrameColumn
  QueryFrameRow
  QueryFrameReq/Rsp
  TextSearchReq/Rsp
  ExplainQueryReq/Rsp
  QueryPlanStep
  ```

  Required `QueryService` RPCs:

  ```text
  QueryFrame
  TextSearch
  ExplainQuery
  ```

  Required request rule: callers pass explicit `instrument_ids`; no `universe` and no caller-controlled `data_view_policy`.

- [ ] **3.7 Keep `adapter.proto` internal**

  Update `adapter.proto` so it remains the internal physical execution API. It may keep low-level row/table RPCs, but it must use the new common value types and package naming:

  ```text
  package trpc.storage.adapter
  service Adapter
  ```

  Required rule: moox CLI and ordinary external users must not import adapter messages directly.

- [ ] **3.8 Generate storage proto code**

  Run:

  ```bash
  cd /Users/mooyang/Documents/go/src/github.com/mooyang-code/moox/modules/storage
  make proto
  ```

  Expected: generated files appear under:

  ```text
  /Users/mooyang/Documents/go/src/github.com/mooyang-code/moox/modules/storage/proto/gen
  ```

  Expected: generated interfaces include `MetadataService`, `DataService`, `QueryService`, and `AdapterService`.

---

## Phase 4: Define New moox Control Protocol Contract

- [ ] **4.1 Split old `moox.proto`**

  Replace:

  ```text
  /Users/mooyang/Documents/go/src/github.com/mooyang-code/moox/modules/control/proto/moox.proto
  ```

  With:

  ```text
  /Users/mooyang/Documents/go/src/github.com/mooyang-code/moox/modules/control/proto/common.proto
  /Users/mooyang/Documents/go/src/github.com/mooyang-code/moox/modules/control/proto/control.proto
  /Users/mooyang/Documents/go/src/github.com/mooyang-code/moox/modules/control/proto/collector.proto
  /Users/mooyang/Documents/go/src/github.com/mooyang-code/moox/modules/control/proto/node.proto
  /Users/mooyang/Documents/go/src/github.com/mooyang-code/moox/modules/control/proto/task.proto
  ```

  Required package names:

  ```text
  trpc.moox.common
  trpc.moox.control
  trpc.moox.collector
  trpc.moox.node
  trpc.moox.task
  ```

- [ ] **4.2 Implement `ControlService` RPCs**

  Add and later implement:

  ```text
  CreateWorkspaceWithDefaults
  ConfigureDataSet
  ConfigureFields
  ConfigureStorageRoutes
  ConfigureCollectorBinding
  PublishMetadataChange
  ```

  Required behavior: `ControlService` calls storage `MetadataService` and does not expose storage `AdapterService`.

- [ ] **4.3 Implement collector/node/task proto surfaces**

  Define:

  ```text
  CollectorService.RegisterCollector
  CollectorService.Heartbeat
  CollectorService.AssignTask
  CollectorService.ReportTaskStatus
  NodeService.RegisterNode
  NodeService.UpdateNodeStatus
  NodeService.ListNodes
  TaskService.CreateTask
  TaskService.UpdateTask
  TaskService.GetTask
  TaskService.ListTasks
  TaskService.CancelTask
  ```

  Required rule: collector-to-dataset mapping uses `CollectorDataSetBinding`, not hard-coded dataset IDs.

- [ ] **4.4 Preserve auth or move it into account**

  Move existing `AuthAPI` implementation into one of these targets:

  ```text
  modules/control/internal/service/auth
  modules/account/internal/auth
  ```

  Required result: whichever target owns auth must compile and expose the same login/register/change-password behavior through either `control/common.proto` or account module APIs.

- [ ] **4.5 Generate control proto code**

  Run:

  ```bash
  cd /Users/mooyang/Documents/go/src/github.com/mooyang-code/moox/modules/control/proto
  make all
  ```

  Expected: generated code compiles under:

  ```text
  /Users/mooyang/Documents/go/src/github.com/mooyang-code/moox/modules/control/proto/gen
  ```

---

## Phase 5: Storage Metadata Implementation

- [ ] **5.1 Write RED tests for metadata model validation**

  Create tests:

  ```text
  /Users/mooyang/Documents/go/src/github.com/mooyang-code/moox/modules/storage/internal/services/metadata/logic/workspace_test.go
  /Users/mooyang/Documents/go/src/github.com/mooyang-code/moox/modules/storage/internal/services/metadata/logic/instrument_test.go
  /Users/mooyang/Documents/go/src/github.com/mooyang-code/moox/modules/storage/internal/services/metadata/logic/factor_test.go
  /Users/mooyang/Documents/go/src/github.com/mooyang-code/moox/modules/storage/internal/services/metadata/logic/dataview_test.go
  /Users/mooyang/Documents/go/src/github.com/mooyang-code/moox/modules/storage/internal/services/metadata/logic/route_test.go
  ```

  Required failing behaviors:

  ```text
  CreateWorkspace rejects empty name
  UpsertInstrument rejects missing exchange_id
  CreateDataSet rejects missing workspace_id
  CreateFactorInstance rejects missing factor_def_id
  CreateDataView rejects unknown source dataset
  CreateStorageRoute rejects unknown storage_device_id
  ConfigureCollectorDataSetBinding rejects unknown dataset_id
  ```

  Run each test and confirm RED before implementation.

- [ ] **5.2 Add metadata persistence models**

  Modify or create under:

  ```text
  /Users/mooyang/Documents/go/src/github.com/mooyang-code/moox/modules/storage/internal/services/metadata/dao/model
  /Users/mooyang/Documents/go/src/github.com/mooyang-code/moox/modules/storage/internal/services/metadata/dao/sqlite
  ```

  Required tables/models:

  ```text
  workspaces
  exchanges
  instruments
  instrument_aliases
  datasets
  dataset_dimensions
  fields
  factor_defs
  factor_instances
  data_views
  data_view_columns
  data_view_versions
  storage_devices
  storage_routes
  collector_dataset_bindings
  ```

- [ ] **5.3 Implement all `MetadataService` RPCs**

  Modify:

  ```text
  /Users/mooyang/Documents/go/src/github.com/mooyang-code/moox/modules/storage/internal/services/metadata/interface.go
  /Users/mooyang/Documents/go/src/github.com/mooyang-code/moox/modules/storage/internal/services/metadata/logic/imp.go
  /Users/mooyang/Documents/go/src/github.com/mooyang-code/moox/modules/storage/internal/services/metadata/logic/*.go
  ```

  Required result: every generated `MetadataService` RPC has a concrete method and returns typed errors through `common.RetInfo`.

- [ ] **5.4 Replace old project/dataset/entity naming**

  Run:

  ```bash
  cd /Users/mooyang/Documents/go/src/github.com/mooyang-code/moox/modules/storage
  rg -n "proj_id|project_id|CreateProject|UpdateProject|ListProjects|object_id|ObjectRoute|DataKey|DataAddress|Projection|DataViewMetric|partition_key|source_type"
  ```

  Required result: public protocol and service code use `workspace_id`, `instrument_id`, `DataRef`, `DataView`, `DataViewColumn`, `dimension_values`, and `column_origin`.

- [ ] **5.5 Verify metadata GREEN**

  Run:

  ```bash
  cd /Users/mooyang/Documents/go/src/github.com/mooyang-code/moox/modules/storage
  go test ./internal/services/metadata/...
  ```

  Expected: all metadata tests pass.

---

## Phase 6: Storage DataService Implementation

- [ ] **6.1 Write RED tests for DataService routing**

  Create:

  ```text
  /Users/mooyang/Documents/go/src/github.com/mooyang-code/moox/modules/storage/internal/services/data/logic/records_test.go
  /Users/mooyang/Documents/go/src/github.com/mooyang-code/moox/modules/storage/internal/services/data/logic/time_series_test.go
  /Users/mooyang/Documents/go/src/github.com/mooyang-code/moox/modules/storage/internal/services/data/logic/factor_values_test.go
  /Users/mooyang/Documents/go/src/github.com/mooyang-code/moox/modules/storage/internal/services/data/logic/latest_snapshot_test.go
  ```

  Required failing behaviors:

  ```text
  UpsertRecords routes TABLE/OBJECT records through storage route metadata
  QueryRecords returns records by record_key and dimension_values
  SetTimeSeries writes instrument_id + freq + timestamp data
  ScanTimeSeries returns points inside TimeRange
  SetFactorValues writes factor_instance_id values without creating global fields
  ScanFactorValues filters by factor_instance_ids
  GetLatestSnapshot returns one latest row per instrument_id
  ```

- [ ] **6.2 Create `internal/services/data`**

  Create:

  ```text
  /Users/mooyang/Documents/go/src/github.com/mooyang-code/moox/modules/storage/internal/services/data/interface.go
  /Users/mooyang/Documents/go/src/github.com/mooyang-code/moox/modules/storage/internal/services/data/logic/imp.go
  /Users/mooyang/Documents/go/src/github.com/mooyang-code/moox/modules/storage/internal/services/data/logic/records.go
  /Users/mooyang/Documents/go/src/github.com/mooyang-code/moox/modules/storage/internal/services/data/logic/time_series.go
  /Users/mooyang/Documents/go/src/github.com/mooyang-code/moox/modules/storage/internal/services/data/logic/factor_values.go
  /Users/mooyang/Documents/go/src/github.com/mooyang-code/moox/modules/storage/internal/services/data/logic/latest_snapshot.go
  /Users/mooyang/Documents/go/src/github.com/mooyang-code/moox/modules/storage/internal/services/data/logic/ref.go
  ```

  Required result: service implements generated `pb.DataService`.

- [ ] **6.3 Port old access logic into DataService**

  Move behavior from:

  ```text
  /Users/mooyang/Documents/go/src/github.com/mooyang-code/moox/modules/storage/internal/services/access/logic/data_set.go
  /Users/mooyang/Documents/go/src/github.com/mooyang-code/moox/modules/storage/internal/services/access/logic/data_get.go
  /Users/mooyang/Documents/go/src/github.com/mooyang-code/moox/modules/storage/internal/services/access/logic/data_search.go
  /Users/mooyang/Documents/go/src/github.com/mooyang-code/moox/modules/storage/internal/services/access/logic/data_delete.go
  ```

  Into:

  ```text
  /Users/mooyang/Documents/go/src/github.com/mooyang-code/moox/modules/storage/internal/services/data/logic
  ```

  Required mapping:

  ```text
  SetData -> SetTimeSeries or UpsertRecords based on DataKind
  GetData -> ScanTimeSeries or QueryRecords
  SearchData -> QueryRecords or QueryService.QueryFrame
  DeleteData -> internal delete operation behind DataService
  ```

- [ ] **6.4 Remove public object APIs**

  Replace old APIs:

  ```text
  UpsertObject
  FetchObject
  QueryObject
  DeleteObject
  ```

  With:

  ```text
  UpsertRecords
  QueryRecords
  ```

  Required rule: financial identity uses `instrument_id`; generic row identity uses `record_key`.

- [ ] **6.5 Verify DataService GREEN**

  Run:

  ```bash
  cd /Users/mooyang/Documents/go/src/github.com/mooyang-code/moox/modules/storage
  go test ./internal/services/data/...
  ```

  Expected: all DataService tests pass.

---

## Phase 7: Storage QueryService Implementation

- [ ] **7.1 Write RED tests for QueryService**

  Create:

  ```text
  /Users/mooyang/Documents/go/src/github.com/mooyang-code/moox/modules/storage/internal/services/query/logic/query_frame_test.go
  /Users/mooyang/Documents/go/src/github.com/mooyang-code/moox/modules/storage/internal/services/query/logic/text_search_test.go
  /Users/mooyang/Documents/go/src/github.com/mooyang-code/moox/modules/storage/internal/services/query/logic/explain_test.go
  ```

  Required failing behaviors:

  ```text
  QueryFrame accepts explicit instrument_ids and rejects empty dataset_id
  QueryFrame supports field columns and factor columns in one response
  QueryFrame uses snapshot_time for cross-section queries
  QueryFrame uses TimeRange for interval queries
  QueryFrame does not accept universe or data_view_policy
  TextSearch routes DOCUMENT datasets to Bleve-backed search
  ExplainQuery returns selected route and fallback plan without executing writes
  ```

- [ ] **7.2 Create `internal/services/query`**

  Create:

  ```text
  /Users/mooyang/Documents/go/src/github.com/mooyang-code/moox/modules/storage/internal/services/query/interface.go
  /Users/mooyang/Documents/go/src/github.com/mooyang-code/moox/modules/storage/internal/services/query/logic/imp.go
  /Users/mooyang/Documents/go/src/github.com/mooyang-code/moox/modules/storage/internal/services/query/logic/query_frame.go
  /Users/mooyang/Documents/go/src/github.com/mooyang-code/moox/modules/storage/internal/services/query/logic/text_search.go
  /Users/mooyang/Documents/go/src/github.com/mooyang-code/moox/modules/storage/internal/services/query/logic/explain.go
  /Users/mooyang/Documents/go/src/github.com/mooyang-code/moox/modules/storage/internal/services/query/logic/planner.go
  ```

  Required result: service implements generated `pb.QueryService`.

- [ ] **7.3 Implement DataView selection server-side**

  Modify:

  ```text
  /Users/mooyang/Documents/go/src/github.com/mooyang-code/moox/modules/storage/internal/services/query/logic/planner.go
  /Users/mooyang/Documents/go/src/github.com/mooyang-code/moox/modules/storage/internal/services/metadata/logic/dataview.go
  ```

  Required rule: `DataViewDef.query_config` controls fallback, staleness, and preferred storage. Request messages do not include `data_view_policy`.

- [ ] **7.4 Implement multi-factor column resolution**

  Required behavior:

  ```text
  DataViewColumn with COLUMN_ORIGIN_FIELD resolves field_id
  DataViewColumn with COLUMN_ORIGIN_FACTOR resolves factor_instance_id
  DataViewColumn with COLUMN_ORIGIN_EXPRESSION is rejected with QUERY_SHAPE_UNSUPPORTED in first implementation
  DataViewColumn with COLUMN_ORIGIN_SYSTEM resolves instrument_id, exchange_id, timestamp, freq, ingest_time
  ```

- [ ] **7.5 Verify QueryService GREEN**

  Run:

  ```bash
  cd /Users/mooyang/Documents/go/src/github.com/mooyang-code/moox/modules/storage
  go test ./internal/services/query/...
  ```

  Expected: all QueryService tests pass.

---

## Phase 8: Adapter, Storage Engines, And Table Naming

- [ ] **8.1 Write RED tests for new DataRef table routing**

  Create:

  ```text
  /Users/mooyang/Documents/go/src/github.com/mooyang-code/moox/modules/storage/internal/toolkit/utils/table_id_test.go
  /Users/mooyang/Documents/go/src/github.com/mooyang-code/moox/modules/storage/internal/services/adapter/logic/routing_test.go
  ```

  Required failing behaviors:

  ```text
  TIME_SERIES table IDs use workspace_id + dataset_id + instrument_id + freq
  FACTOR_VALUE table IDs include factor_instance_id and freq
  TABLE records use dataset_id + normalized dimension_values
  OBJECT records use dataset_id + instrument_id when instrument_id is present
  object_id is not part of new table naming
  ```

- [ ] **8.2 Update adapter DAO parameter model**

  Modify:

  ```text
  /Users/mooyang/Documents/go/src/github.com/mooyang-code/moox/modules/storage/internal/services/adapter/dao/params.go
  /Users/mooyang/Documents/go/src/github.com/mooyang-code/moox/modules/storage/internal/services/adapter/dao/interface.go
  ```

  Required fields:

  ```text
  WorkspaceID
  DatasetID
  InstrumentID
  RecordKey
  FactorInstanceID
  Freq
  Timestamp
  DimensionValues
  DataKind
  DataDomain
  ```

- [ ] **8.3 Update DuckDB/Pebble/Bleve/CSV adapters**

  Modify packages:

  ```text
  internal/services/adapter/dao/duckdb
  internal/services/adapter/dao/pebble
  internal/services/adapter/dao/bleve
  internal/services/adapter/dao/csv
  ```

  Required behavior:

  ```text
  Pebble remains online time-series and latest-value path
  DuckDB remains analytical/query projection path
  Bleve remains text search path
  CSV remains cold backup/offline export path
  ```

- [ ] **8.4 Verify adapter GREEN**

  Run:

  ```bash
  cd /Users/mooyang/Documents/go/src/github.com/mooyang-code/moox/modules/storage
  make test
  ```

  Expected: adapter, DuckDB, Pebble, metadata, data, and query tests pass with CGO-aware settings.

---

## Phase 9: Control Module Implementation

- [ ] **9.1 Write RED tests for ControlService orchestration**

  Create:

  ```text
  /Users/mooyang/Documents/go/src/github.com/mooyang-code/moox/modules/control/internal/service/control/logic/workspace_test.go
  /Users/mooyang/Documents/go/src/github.com/mooyang-code/moox/modules/control/internal/service/control/logic/dataset_test.go
  /Users/mooyang/Documents/go/src/github.com/mooyang-code/moox/modules/control/internal/service/control/logic/collector_binding_test.go
  ```

  Required failing behaviors:

  ```text
  CreateWorkspaceWithDefaults calls storage MetadataService.CreateWorkspace
  ConfigureDataSet creates DataSet and declared dimensions
  ConfigureFields creates Field records
  ConfigureStorageRoutes creates StorageRoute records
  ConfigureCollectorBinding creates CollectorDataSetBinding without hard-coded dataset_id
  PublishMetadataChange emits metadata change event
  ```

- [ ] **9.2 Create ControlService implementation**

  Create:

  ```text
  /Users/mooyang/Documents/go/src/github.com/mooyang-code/moox/modules/control/internal/service/control/interface.go
  /Users/mooyang/Documents/go/src/github.com/mooyang-code/moox/modules/control/internal/service/control/logic/imp.go
  /Users/mooyang/Documents/go/src/github.com/mooyang-code/moox/modules/control/internal/service/control/logic/workspace.go
  /Users/mooyang/Documents/go/src/github.com/mooyang-code/moox/modules/control/internal/service/control/logic/dataset.go
  /Users/mooyang/Documents/go/src/github.com/mooyang-code/moox/modules/control/internal/service/control/logic/storage_route.go
  /Users/mooyang/Documents/go/src/github.com/mooyang-code/moox/modules/control/internal/service/control/logic/collector_binding.go
  /Users/mooyang/Documents/go/src/github.com/mooyang-code/moox/modules/control/internal/service/control/logic/publish.go
  ```

  Required result: service implements generated `control.ControlService`.

- [ ] **9.3 Register control services in server main**

  Modify:

  ```text
  /Users/mooyang/Documents/go/src/github.com/mooyang-code/moox/modules/control/cmd/moox-server/main.go
  /Users/mooyang/Documents/go/src/github.com/mooyang-code/moox/modules/control/config/trpc_go.yaml
  ```

  Required registrations:

  ```text
  ControlService
  CollectorService
  NodeService
  TaskService
  Auth service owner selected in Phase 4
  ```

- [ ] **9.4 Verify control GREEN**

  Run:

  ```bash
  cd /Users/mooyang/Documents/go/src/github.com/mooyang-code/moox/modules/control
  go test ./...
  ```

---

## Phase 10: CLI Implementation

- [ ] **10.1 Write RED tests for CLI command shape**

  Create:

  ```text
  /Users/mooyang/Documents/go/src/github.com/mooyang-code/moox/modules/cli/cmd/metadata_test.go
  /Users/mooyang/Documents/go/src/github.com/mooyang-code/moox/modules/cli/cmd/data_test.go
  /Users/mooyang/Documents/go/src/github.com/mooyang-code/moox/modules/cli/cmd/query_test.go
  ```

  Required failing behaviors:

  ```text
  moox-cli metadata workspace create emits JSON
  moox-cli metadata dataset create emits JSON
  moox-cli data records upsert accepts workspace_id and dataset_id
  moox-cli data timeseries scan accepts instrument_id, freq, start_time, end_time
  moox-cli data factor scan accepts factor_instance_id
  moox-cli query frame accepts instrument_ids and select_columns
  ```

- [ ] **10.2 Merge xData CLI functionality into moox CLI**

  Move useful code from:

  ```text
  /Users/mooyang/Documents/go/src/github.com/mooyang-code/xData-mini/cli/internal/csv
  /Users/mooyang/Documents/go/src/github.com/mooyang-code/xData-mini/cli/internal/importer
  /Users/mooyang/Documents/go/src/github.com/mooyang-code/xData-mini/cli/internal/scanner
  /Users/mooyang/Documents/go/src/github.com/mooyang-code/xData-mini/cli/internal/metadata
  /Users/mooyang/Documents/go/src/github.com/mooyang-code/xData-mini/cli/internal/dataclient
  ```

  Into:

  ```text
  /Users/mooyang/Documents/go/src/github.com/mooyang-code/moox/modules/cli/internal
  ```

  Required result: `xData-mini/cli` is no longer needed as a separate module.

- [ ] **10.3 Implement new CLI command tree**

  Required commands:

  ```text
  moox-cli metadata workspace create|get|list
  moox-cli metadata exchange create|get|list
  moox-cli metadata instrument upsert|get|list
  moox-cli metadata dataset create|get|list
  moox-cli metadata field create|get|list
  moox-cli metadata factor create-def|create-instance|get-instance|list-instances
  moox-cli data records upsert|query
  moox-cli data timeseries set|scan
  moox-cli data factor set|scan
  moox-cli data latest-snapshot
  moox-cli query frame
  moox-cli query text-search
  moox-cli query explain
  ```

  Required output: default JSON, with `--output json|table|yaml`.

- [ ] **10.4 Verify CLI GREEN**

  Run:

  ```bash
  cd /Users/mooyang/Documents/go/src/github.com/mooyang-code/moox/modules/cli
  go test ./...
  go run ./cmd/moox-cli --help
  ```

---

## Phase 11: Collector Absorbs Miner Capabilities

- [ ] **11.1 Write RED tests for collector dataset binding**

  Create:

  ```text
  /Users/mooyang/Documents/go/src/github.com/mooyang-code/moox/modules/collector/internal/bootstrap/binding_test.go
  /Users/mooyang/Documents/go/src/github.com/mooyang-code/moox/modules/collector/internal/reporter/storage_writer_test.go
  ```

  Required failing behaviors:

  ```text
  collector resolves dataset_id from CollectorDataSetBinding
  collector maps external_symbol to instrument_id using InstrumentAlias
  collector writes K-line data through DataService.SetTimeSeries
  collector does not hard-code dataset IDs
  ```

- [ ] **11.2 Update collector write path**

  Modify:

  ```text
  /Users/mooyang/Documents/go/src/github.com/mooyang-code/moox/modules/collector/internal/bootstrap
  /Users/mooyang/Documents/go/src/github.com/mooyang-code/moox/modules/collector/internal/collector
  /Users/mooyang/Documents/go/src/github.com/mooyang-code/moox/modules/collector/internal/reporter
  /Users/mooyang/Documents/go/src/github.com/mooyang-code/moox/modules/collector/pkg/model
  ```

  Required result: collector writes `DataRef` with `workspace_id`, `dataset_id`, `instrument_id`, `exchange_id`, `freq`, and `timestamp`.

- [ ] **11.3 Write RED tests for collector discovery and exchange-source output**

  Create:

  ```text
  /Users/mooyang/Documents/go/src/github.com/mooyang-code/moox/modules/collector/internal/writer/storage_writer_test.go
  /Users/mooyang/Documents/go/src/github.com/mooyang-code/moox/modules/collector/internal/source/binance/symbol_test.go
  ```

  Required failing behaviors:

  ```text
  collector source normalizes Binance symbol to internal_symbol through InstrumentAlias
  collector writes discovered tradable pairs through MetadataService.UpsertInstrument
  collector writes time-series output through DataService.SetTimeSeries
  ```

- [ ] **11.4 Update collector integration with absorbed miner packages**

  Modify:

  ```text
  /Users/mooyang/Documents/go/src/github.com/mooyang-code/moox/modules/collector/internal/app
  /Users/mooyang/Documents/go/src/github.com/mooyang-code/moox/modules/collector/internal/source
  /Users/mooyang/Documents/go/src/github.com/mooyang-code/moox/modules/collector/internal/writer
  /Users/mooyang/Documents/go/src/github.com/mooyang-code/moox/modules/collector/internal/model
  ```

- [ ] **11.5 Verify collector GREEN**

  Run:

  ```bash
  cd /Users/mooyang/Documents/go/src/github.com/mooyang-code/moox/modules/collector && go test ./...
  ```

---

## Phase 12: Factor, Order, Account Modules

- [ ] **12.1 Implement factor module minimal service**

  Create:

  ```text
  /Users/mooyang/Documents/go/src/github.com/mooyang-code/moox/modules/factor/internal/factor/definition.go
  /Users/mooyang/Documents/go/src/github.com/mooyang-code/moox/modules/factor/internal/factor/instance.go
  /Users/mooyang/Documents/go/src/github.com/mooyang-code/moox/modules/factor/internal/factor/calculator.go
  /Users/mooyang/Documents/go/src/github.com/mooyang-code/moox/modules/factor/internal/factor/calculator_test.go
  ```

  Required behavior: MA factor with windows 20, 60, and 120 writes `SetFactorValues` using distinct `factor_instance_id`.

- [ ] **12.2 Implement order module compile-safe skeleton**

  Create:

  ```text
  /Users/mooyang/Documents/go/src/github.com/mooyang-code/moox/modules/order/internal/order/model.go
  /Users/mooyang/Documents/go/src/github.com/mooyang-code/moox/modules/order/internal/order/service.go
  /Users/mooyang/Documents/go/src/github.com/mooyang-code/moox/modules/order/internal/order/service_test.go
  ```

  Required behavior: service validates `workspace_id`, `account_id`, `instrument_id`, side, price, and quantity.

- [ ] **12.3 Implement account module compile-safe skeleton**

  Create:

  ```text
  /Users/mooyang/Documents/go/src/github.com/mooyang-code/moox/modules/account/internal/account/model.go
  /Users/mooyang/Documents/go/src/github.com/mooyang-code/moox/modules/account/internal/account/service.go
  /Users/mooyang/Documents/go/src/github.com/mooyang-code/moox/modules/account/internal/account/service_test.go
  ```

  Required behavior: service validates account name, exchange, credential reference, and workspace binding.

- [ ] **12.4 Verify new modules GREEN**

  Run:

  ```bash
  cd /Users/mooyang/Documents/go/src/github.com/mooyang-code/moox/modules/factor && go test ./...
  cd /Users/mooyang/Documents/go/src/github.com/mooyang-code/moox/modules/order && go test ./...
  cd /Users/mooyang/Documents/go/src/github.com/mooyang-code/moox/modules/account && go test ./...
  ```

---

## Phase 13: Build, Release, And Skill Packaging

- [ ] **13.1 Implement `build/test.sh`**

  Required behavior:

  ```text
  modules/control -> go test ./...
  modules/cli -> go test ./...
  modules/storage -> make test
  modules/collector -> go test ./...
  modules/factor -> go test ./...
  modules/order -> go test ./...
  modules/account -> go test ./...
  ```

  Required output: print module name before each test command and stop at first failure.

- [ ] **13.2 Implement `build/build.sh`**

  Required behavior:

  ```text
  build moox-server from modules/control/cmd/moox-server
  build moox-cli from modules/cli/cmd/moox-cli
  build moox-storage from modules/storage/cmd/moox-storage with storage build settings
  build moox-collector from modules/collector/cmd/moox-collector
  build moox-factor from modules/factor/cmd/moox-factor
  build moox-order from modules/order/cmd/moox-order
  build moox-account from modules/account/cmd/moox-account
  ```

- [ ] **13.3 Implement `build/release.sh`**

  Required behavior:

  ```text
  moox-cli-linux-amd64
  moox-cli-darwin-amd64
  moox-cli-darwin-arm64
  moox-cli-windows-amd64.exe
  server binaries for requested target platform
  ```

- [ ] **13.4 Implement `build/package-skill.sh`**

  Required package content:

  ```text
  moox/SKILL.md
  moox/references/build.md
  moox/references/storage.md
  moox/references/protocol.md
  moox/references/release.md
  moox/moox-cli-linux-amd64
  moox/moox-cli-darwin-amd64
  moox/moox-cli-darwin-arm64
  moox/moox-cli-windows-amd64.exe
  ```

- [ ] **13.5 Implement `skills/moox`**

  Required skill topics:

  ```text
  when to use moox skill
  module map
  build/test commands
  storage engine roles
  protocol naming rules
  release/package procedure
  ```

- [ ] **13.6 Verify build system GREEN**

  Run:

  ```bash
  cd /Users/mooyang/Documents/go/src/github.com/mooyang-code/moox
  make test
  make build
  make package-skill
  ```

---

## Phase 14: Remote Deploy And CSV Acceptance

- [ ] **14.1 Implement `build/deploy.sh`**

  Create:

  ```text
  /Users/mooyang/Documents/go/src/github.com/mooyang-code/moox/build/deploy.sh
  ```

  Required defaults:

  ```text
  REMOTE_HOST=43.132.204.177
  REMOTE_ROOT=~/moox
  REMOTE_BIN=~/moox/bin
  REMOTE_CONFIG=~/moox/configs
  REMOTE_VAR=~/moox/var
  REMOTE_LOG=~/moox/var/log
  REMOTE_ACCEPTANCE=~/moox/var/storage/acceptance
  ```

  Required behavior:

  ```text
  resolve REMOTE_ROOT on the remote host so ~/moox means the remote user's home directory
  create remote directories with ssh
  upload moox-server, moox-cli, and moox-storage to ~/moox/bin
  upload module configs to ~/moox/configs/<module>
  upload skills package to ~/moox/release when present
  preserve previous remote release as ~/moox/backup/<timestamp>
  print exact remote paths after deployment
  ```

  Required command:

  ```bash
  cd /Users/mooyang/Documents/go/src/github.com/mooyang-code/moox
  REMOTE_HOST=43.132.204.177 REMOTE_ROOT='~/moox' ./build/deploy.sh
  ```

- [ ] **14.2 Implement remote process control**

  Add to `build/deploy.sh` or create:

  ```text
  /Users/mooyang/Documents/go/src/github.com/mooyang-code/moox/build/remote-start.sh
  /Users/mooyang/Documents/go/src/github.com/mooyang-code/moox/build/remote-stop.sh
  ```

  Required behavior:

  ```text
  stop existing moox-storage and moox-server process if running
  start ~/moox/bin/moox-storage with ~/moox/configs/storage
  start ~/moox/bin/moox-server with ~/moox/configs/control
  write storage logs under ~/moox/var/log/storage
  write control logs under ~/moox/var/log/control
  verify processes are alive after start
  ```

- [ ] **14.3 Implement `build/acceptance.sh`**

  Create:

  ```text
  /Users/mooyang/Documents/go/src/github.com/mooyang-code/moox/build/acceptance.sh
  ```

  Required inputs:

  ```text
  APT_CSV=/Users/mooyang/Downloads/APT-USDT.csv
  AR_CSV=/Users/mooyang/Downloads/AR-USDT.csv
  REMOTE_HOST=43.132.204.177
  REMOTE_ROOT=~/moox
  WORKSPACE=default
  EXCHANGE=BINANCE
  DATASET=binance_spot_kline_1m
  FREQ=1m
  ```

  Required behavior:

  ```text
  verify both CSV files exist locally before upload
  upload CSV files to ~/moox/var/storage/acceptance
  create or upsert Workspace default
  create or upsert Exchange BINANCE
  create or upsert Instruments APT-USDT and AR-USDT with InstrumentAlias for BINANCE
  create or upsert DataSet binance_spot_kline_1m with DataKind TIME_SERIES and DataDomain MARKET_BAR
  create required K-line fields such as open, high, low, close, volume, amount when present in CSV
  import APT-USDT.csv through moox-cli data timeseries set or csv import command
  import AR-USDT.csv through moox-cli data timeseries set or csv import command
  query both instruments through moox-cli data timeseries scan
  fail if either import writes zero rows
  fail if either query returns zero rows
  print imported row counts and queried row counts
  ```

- [ ] **14.4 Add CLI import command required for acceptance**

  Ensure Phase 10 includes or update it to include:

  ```text
  moox-cli data csv import --workspace default --dataset binance_spot_kline_1m --exchange BINANCE --instrument APT-USDT --freq 1m --file ~/moox/var/storage/acceptance/APT-USDT.csv
  moox-cli data csv import --workspace default --dataset binance_spot_kline_1m --exchange BINANCE --instrument AR-USDT --freq 1m --file ~/moox/var/storage/acceptance/AR-USDT.csv
  ```

  Required behavior: the command auto-detects standard K-line CSV columns where possible and returns JSON:

  ```json
  {
    "ret_info": {"code": 0, "msg": "success"},
    "instrument_id": "APT-USDT",
    "dataset_id": "binance_spot_kline_1m",
    "written_rows": 1
  }
  ```

  The actual `written_rows` value MUST be the number of rows written from the CSV file.

- [ ] **14.5 Run remote acceptance**

  Run:

  ```bash
  cd /Users/mooyang/Documents/go/src/github.com/mooyang-code/moox
  make release
  REMOTE_HOST=43.132.204.177 REMOTE_ROOT='~/moox' make deploy
  REMOTE_HOST=43.132.204.177 REMOTE_ROOT='~/moox' APT_CSV=/Users/mooyang/Downloads/APT-USDT.csv AR_CSV=/Users/mooyang/Downloads/AR-USDT.csv make acceptance
  ```

  Expected:

  ```text
  moox-storage deployed to ~/moox/bin/moox-storage
  moox-server deployed to ~/moox/bin/moox-server
  moox-cli deployed to ~/moox/bin/moox-cli
  APT-USDT.csv uploaded to ~/moox/var/storage/acceptance/APT-USDT.csv
  AR-USDT.csv uploaded to ~/moox/var/storage/acceptance/AR-USDT.csv
  APT-USDT imported_rows > 0
  AR-USDT imported_rows > 0
  APT-USDT queried_rows > 0
  AR-USDT queried_rows > 0
  ```

---

## Phase 15: Global Protocol And Naming Cleanup

- [ ] **15.1 Remove old protocol names**

  Run:

  ```bash
  cd /Users/mooyang/Documents/go/src/github.com/mooyang-code/moox
  rg -n "proj_id|project_id|CreateProject|UpdateProject|ListProjects|object_id|ObjectRoute|DataKey|DataAddress|Projection|DataViewMetric|source_type|partition_key|GetLatestValues|as_of_time_ms|start_time_ms|end_time_ms|universe|data_view_policy" modules docs openspec skills
  ```

  Required result: only historical docs explaining removed names may contain these strings. Public proto and service code must not contain them.

- [ ] **15.2 Remove old directory names**

  Run:

  ```bash
  cd /Users/mooyang/Documents/go/src/github.com/mooyang-code/moox
  find modules -iname '*adaptor*' -o -path '*/internal/data/*' -o -path '*/cmd/moox/*'
  ```

  Required result: no old `adaptor`, `internal/data`, or `cmd/moox` paths remain.

- [ ] **15.3 Regenerate all proto code after cleanup**

  Run:

  ```bash
  cd /Users/mooyang/Documents/go/src/github.com/mooyang-code/moox/modules/storage && make proto
  cd /Users/mooyang/Documents/go/src/github.com/mooyang-code/moox/modules/control/proto && make all
  ```

- [ ] **15.4 Run full verification**

  Run:

  ```bash
  cd /Users/mooyang/Documents/go/src/github.com/mooyang-code/moox
  go work sync
  make test
  make build
  make acceptance
  openspec validate adopt-modules-monorepo --strict
  ```

  Expected: all commands pass.

- [ ] **15.5 Commit final implementation**

  Run:

  ```bash
  cd /Users/mooyang/Documents/go/src/github.com/mooyang-code/moox
  git add -A
  git commit -m "feat: migrate moox monorepo and implement quant protocol"
  ```

---

## Phase 16: Final Acceptance Checklist

- [ ] **16.1 Module layout acceptance**

  Verify:

  ```text
  modules/control
  modules/cli
  modules/storage
  modules/collector
  modules/factor
  modules/order
  modules/account
  ```

- [ ] **16.2 Protocol acceptance**

  Verify generated services exist:

  ```text
  MetadataService
  DataService
  QueryService
  AdapterService
  ControlService
  CollectorService
  NodeService
  TaskService
  ```

- [ ] **16.3 Naming acceptance**

  Verify public protocol uses:

  ```text
  Workspace
  DataSet
  DataKind
  DataDomain
  Instrument
  InstrumentAlias
  DataRef
  DataView
  DataViewColumn
  dimension_values
  snapshot_time
  GetLatestSnapshot
  ```

- [ ] **16.4 Build acceptance**

  Verify:

  ```text
  make test passes
  make build passes
  make release passes
  make deploy passes against 43.132.204.177
  make acceptance passes against 43.132.204.177
  make package-skill passes
  modules/storage make test passes
  modules/cli go test ./... passes
  modules/control go test ./... passes
  modules/collector go test ./... passes go test ./... passes
  modules/factor go test ./... passes
  modules/order go test ./... passes
  modules/account go test ./... passes
  ```

- [ ] **16.5 Remote CSV data acceptance**

  Verify:

  ```text
  /Users/mooyang/Downloads/APT-USDT.csv exists before acceptance
  /Users/mooyang/Downloads/AR-USDT.csv exists before acceptance
  remote ~/moox/var/storage/acceptance/APT-USDT.csv exists after upload
  remote ~/moox/var/storage/acceptance/AR-USDT.csv exists after upload
  APT-USDT K-line rows are written to storage
  AR-USDT K-line rows are written to storage
  QueryFrame or ScanTimeSeries can read back APT-USDT rows
  QueryFrame or ScanTimeSeries can read back AR-USDT rows
  ```

- [ ] **16.6 Documentation acceptance**

  Update:

  ```text
  /Users/mooyang/Documents/go/src/github.com/mooyang-code/moox/docs/monorepo-architecture.md
  /Users/mooyang/Documents/go/src/github.com/mooyang-code/moox/docs/pb-protocol-redesign.md
  /Users/mooyang/Documents/go/src/github.com/mooyang-code/moox/docs/quant-financial-data-concepts.md
  /Users/mooyang/Documents/go/src/github.com/mooyang-code/moox/openspec/changes/adopt-modules-monorepo/tasks.md
  ```

  Required result: docs match implemented paths, service names, and protocol names.
