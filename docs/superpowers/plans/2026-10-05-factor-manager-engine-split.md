# 因子模块拆分执行计划：moox-factor-mgr + moox-factor-engine

- 日期：2026-10-05
- 设计：[`2026-10-05-factor-manager-engine-split-design.md`](../specs/2026-10-05-factor-manager-engine-split-design.md) v2（决策编号 D29–D44，下文直接引用）
- 状态：计划稿 v2（已按 2026-10-05 评审意见修订），待确认；**本会话只产出文档，不改代码**
- 取代：[`2026-10-04-factor-definition-membership-and-multipage-frontend.md`](./2026-10-04-factor-definition-membership-and-multipage-frontend.md) 的阶段 D（单体 factor 的部署与验收），改由本计划阶段 F 执行。

## 当前基线与执行约束

- 分支 `feature/mooyang`，基线提交 `f193ddcf`（成员设计与多页前端已全部实现）。开工先 `git status` / `git log -5` 核对；与本计划无关的未提交改动视为用户工作，不覆盖、不混入。
- `AGENTS.md`：不兼容历史；简体中文文档；SQL schema 排版规范；**会话结束提交并 `git push`**。
- **数据**：项目未上线，管理端 SQLite 删库重建，不备份、不写迁移（用户 2026-10-05 确认）。
- 每个 Go Task 结束：涉及包 `go build` / `go vet` / `go test -count=1` 通过；`bash scripts/check/check-gofmt.sh`、`git diff --check` 无告警；改 schema 时 `sqlite3 :memory: < modules/factor/schema/factor.sql`。阶段 A 每个 Task 结束时整个仓库相关模块必须全绿（纯重构 / 纯改名，不改行为）；阶段 B / C 的中间 Task 允许同模块其他包暂时编译失败，B5 与 C6 收口时全绿。
- 提交用 Conventional Commit，每个 Task 单独提交，只 `git add` 本 Task 的文件。
- **同批发布**：管理端、引擎、storage-access、网关种子、web 一起发布，不做滚动兼容。

## 文件责任图

| 阶段 | 文件 |
| --- | --- |
| A 共享层重构与改名 | `modules/factor/internal/catalog/artifacts.go` → `internal/artifacts/`；`internal/recalc/{service,worker}.go`、新 `executor.go`；`internal/catalog/locks.go`；`cmd/server` → `cmd/mgr` 及全仓库 `moox-factor` / `moox_factor` / `WITH_FACTOR` 引用 |
| B 管理端 | `proto/factor.proto`、`proto/factorgen/`；`schema/factor.sql`、`schema/schema_test.go`；`internal/store/recalc_jobs.go`、新 `engine_jobs.go`；新 `internal/enginehub/`；`internal/catalog/{service,reconcile}.go`；`internal/rpc/{service,convert}.go`、新 `engine_service.go`；`internal/bootstrap/{bootstrap,config,health,run_tracker}.go`；`config/app.yaml`、`config/trpc_go.yaml`；`cmd/cli/` |
| C 引擎 | 新 `cmd/engine/`、`internal/engine/`、`config/engine.yaml`、`config/trpc_go.engine.yaml`；`internal/trigger/eventconsumer/`（仅测试补充）；`scripts/check/check-module-boundaries.sh` |
| D storage-access | `modules/storage/internal/accessproxy/{proxy,principals}.go` 及测试；`modules/storage/cmd/access/main.go`；`modules/storage/config/access/principals.example.yaml` |
| E 种子 / 部署 / 前端 / 文档 | `modules/admin/internal/service/sysdeploy/{defaults,routes}.go` 及测试、`period_gateway_contract_test.go`、`config/setup/service-deployments.yaml`；`scripts/deploy/deploy-moox.sh`、新 `scripts/deploy/deploy-factor-engine.sh`、新 `deploy/launchd/com.moox.factor-engine.plist.tmpl`、新 `deploy/systemd/user/moox-factor-engine.service.tmpl`；`web/src/api/factor/types.ts`、`web/src/views/factor/{overview,task-management,recalc}/`；`modules/factor/README.md`、`docs/因子计算模块设计.md`、`docs/存储服务架构与部署.md`、新 `docs/ops/factor-engine.md` |

## 端口

| 进程 | 端口 | 用途 |
| --- | --- | --- |
| `moox-factor-mgr` | 11403 / 11404 / 11414 / 11944 / 12944 | FactorMgr tRPC / HTTP、health、admin、metrics（沿用） |
| `moox-factor-mgr` | **11405** | FactorEngine tRPC（新增，tRPC-Go 不能同端口挂两个服务） |
| `moox-factor-engine` | **11417** / 11945 / 12945 | health / admin / metrics（11415 已被 monitor、collector 占用） |

## 接口约定

```go
// modules/factor/internal/artifacts（由 catalog 移入，行为不变）
type Artifacts struct{ FactorsDir string }
func (a Artifacts) Materialize(factor domain.FactorDef) (string, error)

// modules/factor/internal/recalc（无 store 依赖，引擎使用）
type ChunkInput struct {
	Set        domain.FactorSet
	Factors    []domain.FactorDef
	Subjects   []string  // 已解析；空表示该任务无标的，直接成功
	Start, End time.Time // 已按因子集频率对齐
	Progress   time.Time // 续跑起点
}
type ProgressFunc func(ctx context.Context, progress time.Time, outcome pipeline.Outcome) (cancelled bool, err error)
type Executor struct{ /* runner, 进程内 locks, chunkPeriods, retry */ }
func (e *Executor) Run(ctx context.Context, in ChunkInput, report ProgressFunc) error

// modules/factor/internal/store（管理端）
func (s *Store) PullRecalcJob(ctx context.Context, engineID string, now time.Time, ttl time.Duration) (RecalcJob, string /*lease token*/, bool, error)
func (s *Store) ReportRecalcProgress(ctx context.Context, jobID, leaseToken string, progress int64, status, errText string, now time.Time, ttl time.Duration) (RecalcJob, error) // 令牌不符 → ErrConflict

// modules/factor/internal/enginehub（管理端）
type Hub struct{ /* store, clock, leaseTTL, startupGrace, readiness map */ }
func (h *Hub) Heartbeat(ctx context.Context, id EngineIdentity, status EngineStatus) (time.Duration, error) // 租约冲突 → ErrConflict
func (h *Hub) Snapshot(ctx context.Context, knownHash string) (hash string, notModified bool, sets []EngineSet, err error)
func (h *Hub) Engine(ctx context.Context, now time.Time) (EngineInfo, EngineStatus, bool) // GetStatus 使用，含 catalog_in_sync
func (h *Hub) SetResultReady(setID string, ready bool)                                     // 对账结果回写（D35）
func CatalogHash(sets []EngineSet) string                                                   // 规范化 JSON + sha256，不含时间戳

// modules/factor/internal/engine（引擎）
type CatalogCache struct{ /* hash, sets, syncedAt, syncErrorSince, stateFile, artifacts */ }
func (c *CatalogCache) Load(path string) (bool, error)                    // 启动时读本地 catalog.json
func (c *CatalogCache) Apply(hash string, sets []EngineSet) (changed bool, err error) // 物化源码 → 原子替换 → 落盘
func (c *CatalogCache) EnabledSetByDataset(ctx context.Context, spaceID, datasetID, freq string) (domain.FactorSet, []domain.FactorDef, bool, error) // result_ready=false → 包装 storageio.ErrInfra
func (c *CatalogCache) FilterSubjects(ctx context.Context) ([]string, error)
func NextSyncAt(now time.Time, interval, offset time.Duration) time.Time  // UTC 对齐：floor(now/interval)*interval+offset，≤now 时加 interval
```

---

## 阶段 0：文档

- [x] **Step 1：** 写设计文档 v1，并按评审意见修订为 v2。
- [x] **Step 2：** 写本计划 v1，并按评审意见修订为 v2。
- [x] **Step 3：** 在旧计划阶段 D 顶部加「已被本计划阶段 F 取代」说明。
- [ ] **Step 4：** 用户确认 v2；确认后提交 `docs(factor): design manager/engine split and plan`，并 `git push`。

---

## 阶段 A：共享层重构与改名（不改行为）

### Task A1：`Artifacts` 移到共享包

- [ ] **Step 1：** 新建 `internal/artifacts/`，把 `catalog/artifacts.go` 与 `artifacts_test.go` 原样移入（包名改为 `artifacts`），`catalog` 改为引用新包。
- [ ] **Step 2：** `cd modules/factor && go build ./... && go vet ./... && go test ./... -count=1`。
- [ ] **Step 3：** 提交 `refactor(factor): move source artifacts into a shared package`。

### Task A2：从补算 worker 拆出无 store 的 `Executor`

- [ ] **Step 1：写测试**（`recalc/executor_test.go`，假 Runner）：`TestExecutorRunsChunksAndReportsProgress`（3 块，进度回调 3 次且单调）、`TestExecutorStopsWhenProgressReportsCancelled`、`TestExecutorResumesFromProgress`、`TestExecutorEmptySubjectsSucceedsImmediately`、`TestExecutorRetriesChunkThenFails`（沿用 `WithChunkRetry` 语义）、`TestExecutorDegradedNotePassedToReport`。
- [ ] **Step 2：实现** `recalc/executor.go`：把 `Worker.Run` 中「取进度 → 切块 → `runChunkWithRetry` → 写进度」的循环搬进 `Executor.Run`，写进度改为调用 `ProgressFunc`。`Worker.Run` 改为组装 `ChunkInput`（用现有 `selection`）后调用 `Executor.Run`，`ProgressFunc` 内做原来的 `UpdateRecalcJob`。现有 `service_test.go` 全部保持通过。
- [ ] **Step 3：** 全模块验证，提交 `refactor(factor): extract a store-free recalc chunk executor`。

### Task A3：`Locks` 去掉文件锁

- [ ] **Step 1：** 删除 `catalog/locks.go` 中 `flock` 分支（`openFileLock`、`lockDir` 字段、`NewLocks(lockDir)` 参数）与对应测试；`bootstrap` 去掉 `WithLockDir`。保留进程内 FIFO 锁与 `LockFactorContext` 键空间隔离测试。
- [ ] **Step 2：** 全模块验证，提交 `refactor(factor): drop cross-process file locks`（D36）。

### Task A4：`moox-factor` 改名 `moox-factor-mgr`（纯改名）

- [ ] **Step 1：盘点。** `rg -n --hidden -g '!node_modules' -g '!dist' -g '!.vitepress' "moox-factor\b|moox_factor\b|moox-factor-cli|WITH_FACTOR\b|--no-factor\b|--with-factor\b|/factor/config|data/factor|logs/factor" .`，把结果按「代码 / 配置 / 脚本 / 测试 / 文档 / 历史计划」分组记录到本 Task 下；**历史计划与已归档设计文档不改**（它们描述的是当时状态）。
- [ ] **Step 2：改名。** `cmd/server` → `cmd/mgr`；二进制 `moox-factor` / `moox-factor-cli` → `moox-factor-mgr` / `moox-factor-mgr-cli`；`scripts/runtime/moox-factor-run-once.sh` 暂保留但改指向（C5 删除）；部署记录 `moox_factor` → `moox_factor_mgr`、monitor 实例 ID 前缀同步；`deploy-moox.sh` 的 `WITH_FACTOR` → `WITH_FACTOR_MGR`、`--no-factor` / `--with-factor` → `--no-factor-mgr` / `--with-factor-mgr`、包内目录 `factor/` → `factor-mgr/`、`data/factor/` → `data/factor-mgr/`、`logs/factor/` → `logs/factor-mgr/`；`build.sh` 目标名 `factor` → `factor-mgr`。`gateway_service_id=factormgr`、proto `FactorMgr`、Go 包路径 `modules/factor` 不变。
- [ ] **Step 3：验证。** `modules/factor`、`modules/admin`、`modules/cli`、`modules/monitor` 相关包全绿；`make test-script-contracts`、`make test-gateway-deploy`、`make test-greenfield-contract` 通过；`rg` 复查除历史文档外零残留。
- [ ] **Step 4：** 提交 `refactor(factor): rename moox-factor to moox-factor-mgr`。

---

## 阶段 B：协议与管理端

### Task B1：proto

- [ ] **Step 1：** 按设计 §6.1 / §6.2 修改 `proto/factor.proto`：新增 `service FactorEngine` 与消息；`GetStatusRsp.engine = 7`（`EngineInfo`，含 `catalog_hash`、`catalog_synced_at`、`catalog_in_sync`）；`RecalcJob.engine_id = 13`。
- [ ] **Step 2：** 重新生成 `proto/factorgen/`；`make proto-check` 通过。
- [ ] **Step 3：** 提交 `feat(factor): add FactorEngine protocol for the remote compute engine`。

### Task B2：schema 与 store（补算租约）

- [ ] **Step 1：schema。** `t_factor_recalc_jobs` 加 `c_engine_id`、`c_lease_token`、`c_lease_expires_at` 与索引 `idx_t_factor_recalc_jobs_pull (c_status, c_lease_expires_at)`（排版按 `AGENTS.md`）。校验 `sqlite3 :memory: < modules/factor/schema/factor.sql && echo OK`。
- [ ] **Step 2：写失败测试**（临时文件 SQLite）：`TestPullRecalcJobIsAtomic`（两个 goroutine 并发 Pull，只一个成功）、`TestPullPrefersOldestAccepted`、`TestPullReclaimsExpiredRunningLease`、`TestPullSkipsLiveLease`、`TestReportProgressRejectsWrongToken`（`ErrConflict`）、`TestReportProgressExtendsLease`、`TestReportProgressReturnsCancelled`（已取消时不改进度，返回 `cancelled`）、`TestReportProgressRejectsRegression`（进度不得回退）。
- [ ] **Step 3：实现** `store/engine_jobs.go`；`RecalcJob` 结构加三个字段；`GetRecalcJob` / `ListRecalcJobs` 带出 `engine_id`。
- [ ] **Step 4：** `go test ./internal/store/ ./schema/ -count=1`，提交 `feat(factor): lease recalc jobs to the compute engine`。

### Task B3：`enginehub`

- [ ] **Step 1：写失败测试**（假时钟）：
  - 租约：`TestHeartbeatGrantsLeaseToFirstEngine`、`TestHeartbeatRejectsSecondEngineWithinTTL`、`TestHeartbeatAcceptsNewEngineAfterExpiry`、`TestStartupGraceRejectsUnknownEngine`（设计 §8）；`TestEngineOfflineAfterTTL`（保留最后状态）。
  - 快照与哈希：`TestCatalogHashStableAcrossTimestamps`、`TestCatalogHashChangesOnMemberStatus` / `OnSourceHash` / `OnSetSubjects` / `OnResultReady`、`TestSnapshotNotModifiedWhenHashMatches`、`TestSnapshotOnlyEnabledSetsAndMembersWithSource`、`TestCatalogInSyncComparesHeartbeatHash`。
  - 领取：`TestPullFiltersFactorsToEnabledSelection`、`TestPullFailsJobWhenNoSelectedFactorEnabled`（D38）、`TestPullSkipsSetsNotResultReady`。
- [ ] **Step 2：实现** `internal/enginehub/{hub.go,snapshot.go,hash.go,lease.go}`；快照组装复用 `store.ListSets` + `store.ListMembers(setID, enabled)` 并带源码。
- [ ] **Step 3：对账回写。** `catalog.Service` 增加 `WithReadyRecorder(func(setID string, ready bool))`；`reconcileSetUnlocked` 成功 / 失败时回写；启用成员流程在加列成功后回写 `true`。测试 `TestReconcileRecordsResultReady`。
- [ ] **Step 4：** 单包验证，提交 `feat(factor): add engine hub for leases, heartbeats and catalog snapshots`。

### Task B4：RPC

- [ ] **Step 1：写失败测试**（`rpc/*_test.go`）：`TestGetStatusReadsEngineHeartbeat`、`TestGetStatusEngineOfflineWhenNoHeartbeat`、`TestGetStatusReportsCatalogOutOfSync`、`TestGetFactorSetLastRunFromHeartbeat`、`TestSyncEngineCatalogNotModified`、`TestEngineHeartbeatConflictMapsToRetCode`、`TestReportRecalcProgressConvertsRFC3339`。
- [ ] **Step 2：实现** `rpc/engine_service.go` 与 `convert.go` 的消息转换；`GetStatus` / `last_run` 改读 `enginehub`；删除 `StatusAPI` / `RunSummaryAPI` 中对进程内状态的依赖。
- [ ] **Step 3：** 提交 `feat(factor): serve FactorEngine and report engine status`。

### Task B5：管理端装配瘦身

- [ ] **Step 1：** `bootstrap.go` 删除：Python 进程池、`pipeline.Runner`、`measuredRunner`、`eventconsumer`、`recalcService.Start`、`runTracker`、`startMetricsReporter`；保留 `pyexec.ValidateSource`（`sourceChecker`）。注册 `FactorEngine`：`config/trpc_go.yaml` 新增服务条目 `trpc.moox.factor.FactorEngine`（`127.0.0.1:11405`，`protocol: trpc`）。
- [ ] **Step 2：** `config.go` 删除 `eventbus` 与 `pipeline` 段、`python.workers` / `task_timeout` / `worker_path` / `factors_dir`，只留 `python.bin`；新增 `engine` 段（`lease_ttl`）。`config/app.yaml` 同步。health 依赖项改为 `sqlite` + `storage_metadata`。
- [ ] **Step 3：** `cmd/cli`（`moox-factor-mgr-cli`）：删除 `run-once`；`recalc` 改为只提交；`status` 改读管理端状态（以现有 CLI 是否直连 DB 为准，执行时核对）。
- [ ] **Step 4：收口** `cd modules/factor && go build ./... && go vet ./... && go test ./... -count=1`。提交 `refactor(factor): slim moox-factor-mgr down to the management plane`。

---

## 阶段 C：计算引擎

### Task C1：引擎配置与管理端客户端

- [ ] **Step 1：写测试**：`TestEngineConfigRejectsDatabaseSection`（`KnownFields`）、`TestEngineConfigRequiresManagerAndStorageTargets`、`TestEngineConfigRejectsOffsetNotLessThanInterval`、`TestEngineConfigEnvOverrides`、`TestManagerClientSignsRequests`（`httptest` + `gatewayauth.Verify`）、`TestManagerClientIgnoresProxyEnv`（设置 `HTTPS_PROXY` 指向一个会失败的地址，请求仍直达 `httptest` 服务）、`TestManagerClientMapsConflict`。
- [ ] **Step 2：实现** `internal/engine/config.go`（D42；默认 `catalog_sync.interval=1m`、`offset=45s`、`state_file=../data/engine/catalog.json`、`heartbeat_interval=10s`、`recalc.chunk_periods=500`）与 `managerclient.go`（`gatewayauth.NewHTTPClient` + CA 文件，`Transport.Proxy=nil`；请求路径与编码对照 collector 调 Admin Gateway 的现有 HTTP 客户端）。新增 `config/engine.yaml`、`config/trpc_go.engine.yaml`（只有 Health 服务 11417 + admin 11945 + metrics 12945）。
- [ ] **Step 3：** 提交 `feat(factor): add engine config and manager client`。

### Task C2：目录快照缓存与定时同步

- [ ] **Step 1：写测试**：
  - 定时器：`TestNextSyncAtAlignsToIntervalPlusOffset`（12:00:10 → 12:00:45；12:00:45 → 12:01:45；12:00:50 → 12:01:45；offset=0）。
  - 缓存：`TestCatalogApplyMaterializesSourcesAndPersists`、`TestCatalogApplyIsAtomic`（落盘失败时内存不替换）、`TestCatalogLoadFromStateFile`、`TestCatalogNotModifiedKeepsSets`、`TestCatalogSyncFailureKeepsServing`（D34：同步失败后查询仍返回旧快照，`syncErrorSince` 被设置）、`TestCatalogNotReadySetReturnsInfraError`、`TestCatalogUnknownDatasetNotFound`、`TestCatalogFilterSubjects`。
  - handler 级：`TestHandlerRetriesWhenSetNotResultReady`、`TestHandlerAcksWhenSetRemovedFromSnapshot`（真 `eventconsumer.Handler` + `CatalogCache`）。
- [ ] **Step 2：实现** `internal/engine/{catalog.go,synctimer.go}`：启动先 `Load` 本地文件，再立即同步一次；之后按 `NextSyncAt` 循环；`Apply` 顺序为物化 → 写临时文件并 rename → 替换内存；因子集列表变化时调用消费者 `SetsChanged()`。
- [ ] **Step 3：** 提交 `feat(factor): sync manager catalog snapshots on an aligned timer`。

### Task C3：心跳、租约与运行时装配

- [ ] **Step 1：写测试**：`TestEngineReadyWithLocalSnapshotWhenManagerDown`、`TestEngineNotReadyWithoutAnySnapshot`、`TestEngineStopsConsumerOnLeaseConflict`、`TestEngineKeepsRunningWhenHeartbeatUnreachable`、`TestHeartbeatCarriesCatalogHashLanesPythonAndRecentRuns`。
- [ ] **Step 2：实现** `heartbeat.go`、`runtime.go`：就绪条件 = 已有快照（同步或本地加载）+ Python 池就绪 + 消费者运行 + 未收到租约冲突；只有明确的 `ErrConflict` 才停消费者；运行记录（原 `bootstrap/run_tracker.go` 搬入）进入心跳。
- [ ] **Step 3：** 提交 `feat(factor): run the engine behind a manager lease`。

### Task C4：补算领取循环

- [ ] **Step 1：写测试**（假管理端）：`TestRecalcLoopPullsRunsAndReportsSucceeded`、`TestRecalcLoopStopsOnCancelled`、`TestRecalcLoopAbandonsOnConflict`、`TestRecalcLoopResolvesSubjectsWhenEmpty`（调用 `ListDatasetSubjects`）、`TestRecalcLoopReportsFailedWithError`、`TestRecalcLoopPausesWhileManagerDown`。
- [ ] **Step 2：实现** `internal/engine/recalc.go`：轮询 `PullRecalcJob` → 组装 `recalc.ChunkInput` → `Executor.Run`，`ProgressFunc` 调 `ReportRecalcProgress`。补算块与同一因子集的实时周期经进程内 `Locks` 串行。
- [ ] **Step 3：** 提交 `feat(factor): pull and execute recalc jobs from the manager`。

### Task C5：`cmd/engine` 与 `run-once`

- [ ] **Step 1：** `cmd/engine/main.go`：默认子命令 `serve`；`run-once --set --period [--factor] [--subject]` 用本地快照（必要时先同步一次）做单周期执行；`health` 子命令带签名访问本进程 `/readyz`（供部署脚本等待就绪）。
- [ ] **Step 2：** `scripts/build/build.sh` 增加 `factor-engine` 目标（引擎无 SQLite，`CGO_ENABLED=0`，编译时确认）；删除 `scripts/runtime/moox-factor-run-once.sh`。
- [ ] **Step 3：** 提交 `feat(factor): add moox-factor-engine binary`。

### Task C6：包边界与收口

- [ ] **Step 1：** `scripts/check/check-module-boundaries.sh`（或新增脚本并接入 `make check-boundaries`）：`go list -deps ./cmd/engine` 不得包含 `internal/store`、`internal/catalog`、`internal/rpc`、`internal/enginehub`；`go list -deps ./cmd/mgr` 不得包含 `internal/engine`、`internal/trigger/eventconsumer`、`internal/pyexec/process`。
- [ ] **Step 2：** `cd modules/factor && go build ./... && go vet ./... && go test ./... -count=1`；`make check-boundaries`。
- [ ] **Step 3：** 提交 `chore(factor): enforce manager/engine dependency boundaries`。

---

## 阶段 D：storage-access 多主体

### Task D1：主体与按主体白名单

- [ ] **Step 1：写测试**（`accessproxy`）：`TestFactorEnginePrincipalAllowsSevenMethods`（D41 表格）、`TestFactorEnginePrincipalRejectsDeleteDatasetRows`、`TestCollectorPrincipalUnchanged`（原白名单逐项断言）、`TestUnknownKeyRejected`、`TestUpstreamCredentialsSelectedByPrincipal`（factor-engine 入站 → 上游 caller `factor`）、`TestPrincipalsFileRequires0600`。
- [ ] **Step 2：实现** `principals.go`：YAML（`principals: [{name, inbound: {key_id, caller, secret_file}, upstream: {key_id, caller, secret_file}, methods: {<service>: [...]}}]`），密钥文件沿用 `gatewayauth.CredentialsFromKeyFile` 的 0600 约束；`methodAllowed` 按主体查表；常量 `CollectorMethods`、`FactorEngineMethods`。`cmd/access/main.go`：新增 `MOOX_STORAGE_ACCESS_PRINCIPALS_FILE`，**删除**单凭据环境变量（`INBOUND_*` / `UPSTREAM_*` / `ALLOWED_CALLERS`）。示例 `config/access/principals.example.yaml`。
- [ ] **Step 3：** `cd modules/storage && go test ./internal/accessproxy/ ./cmd/access/ -count=1`；`make test-storage-boundary`。提交 `feat(storage): authorize storage-access callers per principal and admit factor engine`。

### Task D2：部署脚本下发主体文件

- [ ] **Step 1：** `deploy-moox.sh` 中 storage-access 段（约 4114–4150 行，执行时核对）改为生成 `secrets/storage-access-principals.yaml`：`collector` 主体沿用现有密钥；`factor-engine` 主体的入站密钥由 `MOOX_STORAGE_ACCESS_FACTOR_ENGINE_SECRET_FILE` 提供（缺省时生成随机密钥写入 `secrets/`，并在部署日志提示拷贝到引擎机），上游使用 `factor` 服务密钥。
- [ ] **Step 2：** 相关脚本契约断言同步。提交 `chore(deploy): provision storage-access principals`。

---

## 阶段 E：网关种子、部署、前端、文档

### Task E1：网关种子

- [ ] **Step 1：写失败测试**：`defaults_test.go` 断言 `moox_factor_mgr` 含 `trpc.moox.factor.FactorEngine` 路由（端口 11405）、4 个方法、调用方仅 `factor-engine`；`period_gateway_contract_test.go` 断言 `factor-engine` 不能调 FactorMgr 任一方法；服务密钥清单包含 `factor-engine`。
- [ ] **Step 2：实现** `defaults.go`、`config/setup/service-deployments.yaml`；描述改为「因子管理服务」。
- [ ] **Step 3：** `cd modules/admin && go test ./internal/service/sysdeploy/... ./cmd/cli/... -count=1`。提交 `feat(admin): route FactorEngine RPCs for the factor-engine caller`。

### Task E2：控制机部署包含管理端

- [ ] **Step 1：** `deploy-moox.sh`：`--profile control` 设 `WITH_FACTOR_MGR=1`；`start_factor_mgr` 去掉 EventBus 等待与 Python worker 环境变量，只保留 DB 路径、Storage Gateway、`python.bin`；新增前置检查 `python3 -c "import pandas, numpy"`（D40）；`factor/pyworker` 不再打进控制机包。
- [ ] **Step 2：** `make test-gateway-deploy`、相关脚本契约测试通过。提交 `chore(deploy): ship moox-factor-mgr with the control profile`。

### Task E3：引擎部署脚本

- [ ] **Step 1：** 新 `scripts/deploy/deploy-factor-engine.sh`：参数 `--dir`（macOS 默认 `~/Documents/moox-deploy`，Linux 默认 `~/moox-factor-engine`）、`--manager-url`（如 `https://<控制机>:11001`）、`--manager-node-id`、`--manager-ca`、`--storage-target`、`--storage-node-id`、`--eventbus-url`、`--secrets-dir`、`--skip-build`、`--no-start`。行为：编译 `moox-factor-engine`（本机架构）→ 拷贝 `pyworker/{worker.py,codec.py}` → 用 `uv`（优先）或 `python3 -m venv` 建 / 复用 `factor-engine/venv` 并安装 `runtime-requirements.txt` → 校验凭据文件均为 0600 → 渲染 `factor-engine/config/engine.yaml` → macOS 安装 `~/Library/LaunchAgents/com.moox.factor-engine.plist`（`launchctl bootstrap gui/$UID`），Linux 安装 systemd user unit；服务环境**不注入** `HTTP(S)_PROXY` → 用 `moox-factor-engine health` 等待就绪。
- [ ] **Step 2：** 新 `deploy/launchd/com.moox.factor-engine.plist.tmpl`、`deploy/systemd/user/moox-factor-engine.service.tmpl`。
- [ ] **Step 3：** 脚本契约测试（`scripts/test/contract/`）：渲染出的配置不含 `database` 段、凭据权限校验生效、`--no-start` 不触碰 launchd、模板中无代理变量。提交 `feat(deploy): add a standalone factor engine installer`。

### Task E4：前端

- [ ] **Step 1：** `web/src/api/factor/types.ts`：`GetStatusRsp.engine`、`RecalcJob.engine_id`；契约测试同步。
- [ ] **Step 2：** 总览页增加「计算引擎」状态卡（在线 / 离线、引擎 ID、版本、最后心跳、目录同步时间；`catalog_in_sync=false` 时显示「引擎目录未同步」）；补算 Tab 增加「执行引擎」列。
- [ ] **Step 3：** 计算任务页启用成员、启用因子集的确认框加提示（D44）：「计算引擎约 1 分钟内生效，期间的周期不会自动补算，如需补齐可手动提交补算」；对应 model / 组件测试断言文案出现。
- [ ] **Step 4：** `pnpm test:unit`、`pnpm exec vue-tsc --noEmit`、`pnpm lint:eslint:check`、`pnpm lint:prettier:check` 通过。提交 `feat(web): show factor engine status and enable latency hint`。

### Task E5：文档

- [ ] **Step 1：** `modules/factor/README.md`（两个二进制、配置、职责、端口）；`docs/因子计算模块设计.md`（拓扑与 D29–D44 摘要）；`docs/存储服务架构与部署.md`（storage-access 多主体）；新 `docs/ops/factor-engine.md`（安装、凭据清单、启停、排障：租约冲突、目录未同步、storage-access 超时、本机代理）。
- [ ] **Step 2：** 设计文档状态改为「已实施」，本计划勾选。提交 `docs(factor): document manager/engine split`。

---

## 阶段 F：部署与验收（取代旧计划阶段 D）

**前置**：
- 控制机 = `moox.toml [control_host]`，Storage 机 = `[storage_host]`；`[[other_hosts]] factor-1`（局域网 IP）作废，F1 中删除该条。
- 控制机服务 HTTPS 入口为 `:11001`（2026-10-05 实测 443 不通、11001 可达）。
- 本机 `~/.ssh/known_hosts` 没有控制机与 Storage 机的主机密钥：首次连接前把指纹给用户确认，不使用 `StrictHostKeyChecking=no`。
- 本机已准备（2026-10-05）：`~/Documents/moox-deploy/factor/venv`（Python 3.12.15、pandas 2.3.3、numpy 2.5.3）。同目录下的 `factor.sh`、`factor/config/`、`factor/pyworker/` 与空的 `bin/`、`run/`、`backup/`、`data/factor/` 是单体形态的临时产物：F2 前删除，venv 移到 `factor-engine/venv` 复用（或由 E3 脚本重建）。

### Task F1：控制机与 Storage 机发布

- [ ] **Step 1：** 停止任何主机上遗留的单体 `moox-factor`（若存在），删除其数据目录（不备份，用户已确认）；`moox.toml` 删除 `factor-1`。
- [ ] **Step 2：** 同批：① 重新种子网关（改名后的 `moox_factor_mgr`、`FactorEngine` 路由、`factor-engine` 服务密钥）→ ② `deploy-moox.sh --profile control` 组件覆盖发布 `factor-mgr`、`strategy`、`web-host` → ③ Storage 机组件覆盖发布 `storage-access`（主体文件含 `factor-engine`）。
- [ ] **Step 3：** 核对：管理端 `/readyz` 200；`GetStatus.engine.online=false`；本机直连（`--noproxy '*'`）`<Storage 机>:11004` 可达；storage-access 日志无主体加载错误。

### Task F2：本机引擎安装

- [ ] **Step 1：** 三份凭据放到 `~/Documents/moox-deploy/secrets/`（0600）：`factor-engine` Gateway 服务密钥、`factor-eventbus.yaml`（含 `ca_file`）、storage-access `factor-engine` 入站密钥；以及控制机 Caddy 根证书。
- [ ] **Step 2：** `scripts/deploy/deploy-factor-engine.sh --dir ~/Documents/moox-deploy --manager-url https://<控制机>:11001 --storage-target ip://<Storage 机>:11004 --eventbus-url tls://<控制机>:4222 ...`。
- [ ] **Step 3：** 核对：引擎 `/readyz` 200；管理端 `GetStatus.engine.online=true`、`engine_id=factor-engine@<本机 hostname>`、`catalog_in_sync=true`；`launchctl print gui/$UID/com.moox.factor-engine` 状态 running；`data/engine/catalog.json` 已生成。

### Task F3：导入与启用

- [ ] **Step 1：** 运维机执行 `moox` CLI setup（`moox.toml` 的 11 个 `[[factors.definitions]]` 与 11 个 `[[factors.members]]`）；先 `disabled`，再逐个启用。
- [ ] **Step 2：** 确认每次启用产生 `factor-enable-` 回填任务，`engine_id` 为本机引擎、最终 `succeeded`；确认启用后下一次同步（每分钟第 45 秒）内该成员进入实时计算。

### Task F4：功能验收

- [ ] **Step 1：实时：** 等 3 个周期，确认每个因子集产出 `FactorPeriodComputed`，`GetFactorSet.last_run` 前进，`lag_seconds` 合理。
- [ ] **Step 2：故障演练：** ① 停管理端 3 分钟 → 引擎实时计算不中断、日志有同步失败告警；恢复后 `catalog_in_sync=true`。② 管理端停止期间重启引擎 → 从本地 `catalog.json` 加载并继续计算。③ `launchctl kickstart -k` 重启引擎 → 未完成补算从 `progress_time` 续跑。④ 用同一凭据、不同 `engine_id` 启动第二个引擎 → 被租约拒绝，`/readyz` 503。⑤ 断开本机网络 1 分钟 → 恢复后自动重连、JetStream 重投追平。
- [ ] **Step 3：前端：** 总览页引擎卡片与目录同步状态、启用确认框提示、补算 Tab 执行引擎列，以及旧阶段 D 的多页面验收项（总览、定义、编辑器、计算任务、计算结果、补算、侧栏高亮）。
- [ ] **Step 4：多因子集复用**（沿用旧 D1 Step 3）：同一定义加入 1m 与 1h 两个计算任务分别启用，结果互不影响。

### Task F5：性能实测

- [ ] **Step 1：** 记录 1m 因子集连续 30 个周期的分阶段耗时（`StageDurations`：读 / 算 / 写），给出 p50 / p95。
- [ ] **Step 2：** 记录一次完整回填的分块耗时；单块读取接近 storage-access 超时则调小 `recalc.chunk_periods`，并把最终取值写回 `config/engine.yaml` 默认值。

### Task F6：收尾

- [ ] **Step 1：** 本计划末尾追加「验收记录」（F1–F5 结果、实测数字、调参）。
- [ ] **Step 2：** 旧计划阶段 D 标注「已由本计划阶段 F 完成」；两份旧设计文档的状态字段指向本设计。
- [ ] **Step 3：** `git status` 确认全部提交；`git push`。

---

## 全局验收清单

- [ ] `cd modules/factor && go build ./... && go vet ./... && go test ./... -count=1`；`modules/storage`、`modules/admin`、`modules/cli`、`modules/monitor` 相关包全绿。
- [ ] `make proto-check`、`make check-boundaries`、`make test-storage-boundary`、`make test-greenfield-contract`、`make test-event-contracts`、`make test-script-contracts`、`make test-gateway-deploy` 通过；`make test-docs-architecture` 不新增失败项。
- [ ] `sqlite3 :memory: < modules/factor/schema/factor.sql`、`git diff --check`、`bash scripts/check/check-gofmt.sh` 通过。
- [ ] 引擎二进制依赖图不含 `internal/store` / `internal/catalog` / `internal/enginehub`；管理端二进制不含 `internal/engine` / `eventconsumer` / `pyexec/process`。
- [ ] `rg -n "MOOX_STORAGE_ACCESS_(INBOUND|UPSTREAM)_|MOOX_STORAGE_ACCESS_ALLOWED_CALLERS" modules scripts` 为空；除历史文档外 `rg -n "moox-factor\b|moox_factor\b" ` 为空。
- [ ] web：`pnpm test:unit`、`vue-tsc`、ESLint、Prettier、`pnpm build:prod` 全绿。
- [ ] 阶段 F 全部勾选并有验收记录。

## 风险与回滚

| 风险 | 缓解 |
| --- | --- |
| 公网读窗口延迟 | 已接受；F5 实测并调参 |
| 管理端 HTTP 调用约定与 Gateway 不一致 | C1 对照 collector 调 Admin 的现有客户端实现，`httptest` + `gatewayauth.Verify` 测签名 |
| 本机代理劫持引擎请求 | C1 禁用环境代理并有测试；E3 服务环境不注入代理变量 |
| 改名遗漏 | A4 先盘点再改，收尾 `rg` 复查 |
| 引擎与遗留单体同时消费 | F1 先停单体；D33 租约兜底 |
| 回滚 | 未上线、无数据：回退 `feature/mooyang` 到 `f193ddcf` 并重新部署单体即可 |

## 自检记录

- **覆盖：** D29 → A4 / C5 / C6；D30 → B3 / C2；D31 → B1 / B4 / B5 / E1；D32 → B3 / C2；D33 → B3 / C3 / F4；D34 → C2 / C3 / C4 / F4；D35 → B3 / C2；D36 → A3 / C4；D37 → B2 / C4；D38 → B3；D39 → B3 / B4 / C3 / E4；D40 → B5 / E2；D41 → D1 / D2；D42 → C1 / E3；D43 → E2 / E3；D44 → E4 / F3。
- **顺序：** A（纯重构 / 改名）→ B（管理端，依赖 A）→ C（引擎，依赖 A、B1）→ D（独立，可与 B / C 并行）→ E（依赖 B1、D1）→ F（依赖全部）。
- **待执行时核对：** collector 调 Admin 的 HTTP 客户端约定（C1）；现有 `factor` CLI 是否直连 DB（B5 Step 3）；Gateway 服务密钥的生成与下发入口（E1）；`deploy-moox.sh` storage-access 段行号（D2）。
