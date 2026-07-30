# Factor Plan Review Remediation Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 修复 Factor 执行计划复审发现的五个 P1 和一个 P2，并用测试、真实 series-tag E2E 和独立 codeCR 证明修复完成。

**Architecture:** 保留单实例、进程内、best-effort Factor 模型。Binding 启用使用无 DAG 的集合规则和 Primary View 合同；Scheduler 与管理 RPC 共享按 `factor_id` 的进程内读写门闩，任务在锁内复核当前定义和 Binding；Archive 与 CLI 采用 fail-closed、无额外后台状态的简单修复。

**Tech Stack:** Go 1.24、tRPC-Go、SQLite、DuckDB、Python worker、Go `sync`、Testify、现有 Storage/Factor/Archive E2E 脚本。

---

## 文件结构

**Factor**

- Modify: `modules/factor/internal/registry/binding_contract.go`
  - 同批 Binding target/source 冲突检查；
  - 只接受 source 为 primary dataset 的 active View。
- Modify: `modules/factor/internal/registry/binding_contract_test.go`
  - 覆盖同批环和 secondary-only View。
- Create: `modules/factor/internal/scheduler/factor_gate.go`
  - 按 `factor_id` 管理进程内读写门闩。
- Create: `modules/factor/internal/scheduler/factor_gate_test.go`
  - 覆盖 mutation 等待在途执行。
- Modify: `modules/factor/internal/scheduler/service.go`
  - 在锁内验证任务；
  - 支持按 Factor 丢弃排队任务。
- Modify: `modules/factor/internal/scheduler/service_test.go`
  - 覆盖 stale source hash、disabled Factor 和排队任务清理。
- Modify: `modules/factor/internal/bootstrap/bootstrap.go`
  - 构造并共享 FactorGate；
  - 注入基于当前 Factor/Binding Repository 的 TaskValidator。
- Modify: `modules/factor/internal/rpc/service.go`
  - Factor/Binding mutation 使用写锁；
  - disable、definition/binding mutation 后丢弃旧排队任务。
- Modify: `modules/factor/internal/rpc/service_test.go`
  - 覆盖 disable 等待、旧任务不覆盖 Recalc。

**Archive**

- Modify: `modules/archive/internal/backfill/backfill.go`
  - `complete=false` 时在追加 Journal 前失败。
- Modify: `modules/archive/internal/backfill/backfill_test.go`
  - 覆盖 incomplete fail-closed 和无输出。
- Modify: `modules/archive/internal/writer/writer.go`
  - worker 记录首错后继续消费 job。
- Modify: `modules/archive/internal/writer/writer_test.go`
  - 覆盖 worker 数量个失败后仍有待发分区。

**CLI**

- Modify: `modules/cli/internal/command/storage_import.go`
  - 在任何 Metadata 或写入副作用前校验 `series_tag`。
- Modify: `modules/cli/internal/command/storage_import_test.go`
  - 覆盖 dry-run、真实导入、边界和无 Bind 副作用。

**文档与验收**

- Modify: `docs/superpowers/plans/2026-07-29-factor-runtime-correctness-hardening.md`
  - 只按本次真实验证证据更新 Task 7、9、14、18 和最终清单。

---

### Task 1: Factor Binding 环限制和 Primary View 合同

**Files:**
- Modify: `modules/factor/internal/registry/binding_contract.go`
- Modify: `modules/factor/internal/registry/binding_contract_test.go`
- Modify: `modules/factor/internal/registry/service.go`

- [x] **Step 1: 写同批 Binding 环的失败测试**

在 `binding_contract_test.go` 构造同一 disabled Factor 的两条 enabled Binding：

```go
bindings := []domain.FactorBinding{
    {BindingID: "a-to-b", FactorID: factor.FactorID, SpaceID: "crypto",
        SourceDataset: "a", TargetDataset: "b", Freq: "1h", Status: domain.BindingStatusEnabled},
    {BindingID: "b-to-a", FactorID: factor.FactorID, SpaceID: "crypto",
        SourceDataset: "b", TargetDataset: "a", Freq: "1h", Status: domain.BindingStatusEnabled},
}
err := validateCandidateBindingSet(bindings)
require.ErrorContains(t, err, "source dataset")
require.ErrorContains(t, err, "also targeted")
```

- [x] **Step 2: 运行环测试并确认 RED**

Run:

```bash
cd modules/factor
go test ./internal/registry -run TestValidateCandidateBindingSetRejectsSameBatchCycle -count=1
```

Expected: FAIL，因为 `validateCandidateBindingSet` 尚不存在。

- [x] **Step 3: 实现无 DAG 的候选集合校验**

在 `binding_contract.go` 增加：

```go
func validateCandidateBindingSet(bindings []domain.FactorBinding) error {
    targets := make(map[string]string, len(bindings))
    for _, binding := range bindings {
        if binding.Status != domain.BindingStatusEnabled {
            continue
        }
        key := binding.SpaceID + "\x00" + binding.TargetDataset
        targets[key] = binding.BindingID
    }
    for _, binding := range bindings {
        if binding.Status != domain.BindingStatusEnabled {
            continue
        }
        key := binding.SpaceID + "\x00" + binding.SourceDataset
        if targetBinding, ok := targets[key]; ok {
            return fmt.Errorf(
                "source dataset %s/%s is also targeted by enabled binding %q",
                binding.SpaceID, binding.SourceDataset, targetBinding,
            )
        }
    }
    return nil
}
```

在 `ValidateEnabledBindingsForFactor` 的逐条远端校验前调用该函数。

- [x] **Step 4: 写 secondary-only View 的失败测试**

让 fake `ListViews` 只返回：

```go
&storagepb.View{
    ViewId: "joined", Status: "active", ActiveIndexId: "idx-1",
    PrimaryDatasetId: "orders",
    DatasetIds: []string{"kline"},
    ActiveColumns: []*storagepb.ViewColumn{{
        ColumnName: "kline.close", OriginId: "kline.close",
    }},
}
```

以 `SourceDataset: "kline"` 调用 `ValidateEnabledBinding`，断言：

```go
require.ErrorContains(t, err, "has no active primary view")
```

- [x] **Step 5: 运行 View 测试并确认 RED**

Run:

```bash
cd modules/factor
go test ./internal/registry -run 'TestValidateEnabledBindingRejectsSecondaryOnlyView|TestValidateCandidateBindingSetRejectsSameBatchCycle' -count=1
```

Expected: secondary-only View 测试错误地通过。

- [x] **Step 6: 强制 Primary View 合同**

在 `validateActiveViewProjection` 遍历 View 时先过滤：

```go
if view.GetPrimaryDatasetId() != binding.SourceDataset {
    continue
}
```

无匹配 View 时返回：

```go
return fmt.Errorf(
    "source dataset %s/%s has no active primary view",
    binding.SpaceID, binding.SourceDataset,
)
```

同时修正现有正例 fake，明确设置 `PrimaryDatasetId`。

- [x] **Step 7: 运行 Registry 测试**

Run:

```bash
cd modules/factor
go test ./internal/registry -count=1
```

Expected: PASS。

- [x] **Step 8: 提交**

```bash
git add modules/factor/internal/registry/binding_contract.go \
  modules/factor/internal/registry/binding_contract_test.go \
  modules/factor/internal/registry/service.go
git commit -m "fix(factor): reject binding cycles and secondary views"
```

---

### Task 2: Factor 旧任务隔离

**Files:**
- Create: `modules/factor/internal/scheduler/factor_gate.go`
- Create: `modules/factor/internal/scheduler/factor_gate_test.go`
- Modify: `modules/factor/internal/scheduler/service.go`
- Modify: `modules/factor/internal/scheduler/service_test.go`
- Modify: `modules/factor/internal/bootstrap/bootstrap.go`
- Modify: `modules/factor/internal/rpc/service.go`
- Modify: `modules/factor/internal/rpc/service_test.go`

- [x] **Step 1: 写 FactorGate 等待在途任务的失败测试**

新建 `factor_gate_test.go`：

```go
func TestFactorGateMutationWaitsForRunningTask(t *testing.T) {
    gate := NewFactorGate()
    release := gate.AcquireRun("factor-a")
    entered := make(chan struct{})
    done := make(chan struct{})
    go func() {
        gate.Mutate("factor-a", func() {
            close(entered)
        })
        close(done)
    }()
    select {
    case <-entered:
        t.Fatal("mutation entered while task still held read lock")
    case <-time.After(20 * time.Millisecond):
    }
    release()
    require.Eventually(t, func() bool {
        select {
        case <-done:
            return true
        default:
            return false
        }
    }, time.Second, time.Millisecond)
}
```

- [x] **Step 2: 运行 FactorGate 测试并确认 RED**

Run:

```bash
cd modules/factor
go test ./internal/scheduler -run TestFactorGateMutationWaitsForRunningTask -count=1
```

Expected: FAIL，因为 `FactorGate` 尚不存在。

- [x] **Step 3: 实现 FactorGate**

新建 `factor_gate.go`：

```go
type FactorGate struct {
    mu    sync.Mutex
    gates map[string]*sync.RWMutex
}

func NewFactorGate() *FactorGate {
    return &FactorGate{gates: make(map[string]*sync.RWMutex)}
}

func (g *FactorGate) gate(factorID string) *sync.RWMutex {
    g.mu.Lock()
    defer g.mu.Unlock()
    gate := g.gates[factorID]
    if gate == nil {
        gate = &sync.RWMutex{}
        g.gates[factorID] = gate
    }
    return gate
}

func (g *FactorGate) AcquireRun(factorID string) func() {
    gate := g.gate(factorID)
    gate.RLock()
    return gate.RUnlock
}

func (g *FactorGate) Mutate(factorID string, fn func()) {
    gate := g.gate(factorID)
    gate.Lock()
    defer gate.Unlock()
    fn()
}
```

实现使用闭包的内部 primitive；RPC 层再包装需要返回值的操作。

- [x] **Step 4: 写 stale Task 和 DropQueued 测试**

在 `service_test.go` 增加：

```go
func TestRunSkipsTaskRejectedByCurrentDefinition(t *testing.T) {
    exec := &fakeExecutor{}
    svc := NewService(Config{}, &fakeStorage{}, exec,
        WithTaskValidator(func(context.Context, Task) error {
            return ErrStaleTask
        }))
    err := svc.Run(t.Context(), validTask())
    require.ErrorIs(t, err, ErrStaleTask)
    require.Zero(t, exec.calls)
}

func TestDropQueuedFactorRemovesOnlyMatchingTasks(t *testing.T) {
    svc := NewService(Config{Workers: 1, QueueCapacity: 4}, nil, nil)
    require.NoError(t, svc.Enqueue(t.Context(), taskForFactor("old")))
    require.NoError(t, svc.Enqueue(t.Context(), taskForFactor("keep")))
    require.Equal(t, 1, svc.DropQueuedFactor("old"))
    require.Equal(t, 1, svc.Status().QueueDepth)
}
```

- [x] **Step 5: 运行 Scheduler 测试并确认 RED**

Run:

```bash
cd modules/factor
go test ./internal/scheduler -run 'TestRunSkipsTaskRejectedByCurrentDefinition|TestDropQueuedFactorRemovesOnlyMatchingTasks' -count=1
```

Expected: FAIL，因为 validator option 和 DropQueuedFactor 尚不存在。

- [x] **Step 6: 给 Scheduler 注入 Gate 和 TaskValidator**

在 `service.go` 增加：

```go
var ErrStaleTask = errors.New("factor task is stale")

type TaskValidator func(context.Context, Task) error

func WithFactorGate(gate *FactorGate) Option {
    return func(service *Service) { service.factorGate = gate }
}

func WithTaskValidator(validator TaskValidator) Option {
    return func(service *Service) { service.taskValidator = validator }
}
```

`Service` 保存 `factorGate` 和 `taskValidator`。`Run` 的最外层执行：

```go
release := func() {}
if s.factorGate != nil {
    release = s.factorGate.AcquireRun(task.Factor.FactorID)
}
defer release()
if s.taskValidator != nil {
    if err := s.taskValidator(ctx, task); err != nil {
        return err
    }
}
return s.runValidated(ctx, task)
```

把现有 `Run` 主体移动到未导出的 `runValidated`，不改变计算逻辑。

- [x] **Step 7: 实现 DropQueuedFactor**

在 scheduler 队列 mutex 内删除匹配 factor 的 pending entry，并重建各 shard key：

```go
func (s *Service) DropQueuedFactor(factorID string) int {
    s.mu.Lock()
    defer s.mu.Unlock()
    removed := 0
    for shard, queue := range s.queues {
        kept := queue[:0]
        for _, key := range queue {
            task, exists := s.pending[key]
            if exists && task.Factor.FactorID == factorID {
                delete(s.pending, key)
                removed++
                continue
            }
            kept = append(kept, key)
        }
        s.queues[shard] = kept
    }
    return removed
}
```

- [x] **Step 8: 写 RPC disable/update 生命周期测试**

在 `rpc/service_test.go` 使用可控 executor：

1. 启动旧任务并阻塞在 executor；
2. 并发调用 `SetFactorStatus(disabled)`；
3. 断言 disable 在旧任务释放前没有返回；
4. 释放旧任务，断言 disable 成功；
5. 更新 source hash、enable、Recalc；
6. 断言旧 executor 不会在 Recalc 后再次写回。

另写排队任务测试，disable 后断言 `DropQueuedFactor` 被调用且旧任务未执行。

- [x] **Step 9: 运行 RPC 生命周期测试并确认 RED**

Run:

```bash
cd modules/factor
go test ./internal/rpc -run 'TestSetFactorStatusDisableWaitsForRunningTask|TestUpdatedFactorDropsQueuedOldTask' -count=1
```

Expected: disable 在旧任务仍运行时提前返回，或排队旧任务仍执行。

- [x] **Step 10: 在 bootstrap 注入当前合同 validator**

在 `bootstrap.go` 创建一个共享 `FactorGate`，传入 Scheduler 和 RPC。TaskValidator 每次
执行时：

1. 读取 `task.Factor.FactorID` 当前 Factor；
2. 要求 status 为 enabled；
3. 要求当前 `SourceHash == task.Factor.SourceHash`；
4. 从 `ListExecutable` 找到 factor、space/source/target/freq 和 subject scope 均匹配
   的 Binding；
5. 任一条件不满足返回 `scheduler.ErrStaleTask`。

Validator 不访问 Python 或 Storage。

- [x] **Step 11: 在 RPC mutation 中使用写锁**

给 RPC `Service` 增加共享 `factorGate` option。以下操作在相同 factor 的
`FactorGate.Mutate` 内执行：

- `SetFactorStatus`；
- `UpdateFactor`；
- `UpsertBinding`；
- `DeleteBinding`；
- `DeleteFactor`。

成功 disable、definition update、Binding update/delete 后调用
`scheduler.DropQueuedFactor(factorID)`。保持现有 `mutationMu -> FactorGate -> SQLite`
锁顺序。

- [x] **Step 12: 运行 Factor 测试和 race**

Run:

```bash
cd modules/factor
go test ./... -count=1
go test -race ./internal/scheduler ./internal/rpc ./internal/trigger/... -count=1
```

Expected: PASS，无 race。

- [x] **Step 13: 提交**

```bash
git add modules/factor/internal/scheduler \
  modules/factor/internal/bootstrap/bootstrap.go \
  modules/factor/internal/rpc/service.go \
  modules/factor/internal/rpc/service_test.go
git commit -m "fix(factor): fence stale runtime tasks"
```

---

### Task 3: Archive Backfill 和 writer 失败收敛

**Files:**
- Modify: `modules/archive/internal/backfill/backfill.go`
- Modify: `modules/archive/internal/backfill/backfill_test.go`
- Modify: `modules/archive/internal/writer/writer.go`
- Modify: `modules/archive/internal/writer/writer_test.go`

- [x] **Step 1: 写 incomplete Backfill 失败测试**

让 fake access 返回一行但：

```go
Complete: false,
ServedIndexedFrom: "2026-01-01T00:00:00Z",
ServedIndexedTo: "2026-01-01T01:00:00Z",
```

调用 Backfill 后断言：

```go
require.ErrorContains(t, err, "source view is incomplete")
require.Zero(t, total)
require.Empty(t, journalDirtyPartitions(t, store))
require.NoFileExists(t, expectedParquetPath)
```

- [x] **Step 2: 运行 Backfill 测试并确认 RED**

Run:

```bash
cd modules/archive
go test ./internal/backfill -run TestBackfillerRejectsIncompleteViewBeforeJournalAppend -count=1
```

Expected: 当前实现错误地成功并产生归档。

- [x] **Step 3: 实现 incomplete fail-closed**

在 RetInfo 校验后、`rowsToPatches` 前加入：

```go
if !rsp.GetComplete() {
    return total, fmt.Errorf(
        "source view is incomplete for %s/%s subject=%s freq=%s tag=%q coverage=[%s,%s)",
        plan.SpaceID, plan.DatasetID, plan.SubjectID, plan.Freq, valueOrWildcard(plan.SeriesTag),
        rsp.GetServedIndexedFrom(), rsp.GetServedIndexedTo(),
    )
}
```

更新正常测试 stub，显式设置 `Complete: true`。

- [x] **Step 4: 写 writer 全 worker 失败测试**

准备 `workers+1` 个 dirty Partition，通过 fake Registry 或不可写输出让前两个
`WritePartition` 返回错误。在带 500ms timeout 的 context 中调用：

```go
started := time.Now()
err := writer.WriteDirty(ctx, 10)
require.Error(t, err)
require.Less(t, time.Since(started), 400*time.Millisecond)
```

测试还要断言所有失败分区仍为 dirty。

- [x] **Step 5: 运行 writer 测试并确认 RED**

Run:

```bash
cd modules/archive
go test ./internal/writer -run TestWriteDirtyReturnsWhenAllWorkersFailBeforeJobsExhausted -count=1 -timeout=3s
```

Expected: 当前实现等待 context deadline 或测试超时。

- [x] **Step 6: 让 worker 记录首错后继续消费**

将 worker 的错误分支改为：

```go
if _, err := w.WritePartition(ctx, key); err != nil {
    select {
    case errCh <- err:
    default:
    }
    continue
}
```

producer 的 context 分支使用带标签的 break，避免仅跳出 select：

```go
sendJobs:
for _, state := range states {
    select {
    case jobs <- state.Key:
    case <-ctx.Done():
        break sendJobs
    }
}
```

等待 worker 后优先返回 `errCh` 中的首个错误；没有 worker 错误时返回 `ctx.Err()`。

- [x] **Step 7: 运行 Archive 测试和 race**

Run:

```bash
cd modules/archive
go test ./... -count=1
go test -race ./internal/backfill ./internal/writer ./internal/bootstrap -count=1
```

Expected: PASS，无阻塞、无 race。

- [x] **Step 8: 提交**

```bash
git add modules/archive/internal/backfill modules/archive/internal/writer
git commit -m "fix(archive): fail closed on incomplete materialization"
```

---

### Task 4: CLI import series_tag 提前校验

**Files:**
- Modify: `modules/cli/internal/command/storage_import.go`
- Modify: `modules/cli/internal/command/storage_import_test.go`

- [x] **Step 1: 写非法 tag 输入矩阵**

在 `storage_import_test.go` 增加 table test：

```go
tests := []struct {
    name string
    tag  string
}{
    {name: "leading whitespace", tag: " venue:binance"},
    {name: "trailing whitespace", tag: "venue:binance "},
    {name: "control character", tag: "venue:\x01binance"},
    {name: "too long", tag: strings.Repeat("x", 129)},
    {name: "invalid utf8", tag: string([]byte{0xff})},
}
```

每个 case 同时验证 `DryRun=true` 和真实执行，断言 Metadata fake 的
`ListDatasetSubjects`、`BindDatasetSubject` 调用次数均为零。

- [x] **Step 2: 运行 CLI 测试并确认 RED**

Run:

```bash
cd modules/cli
go test ./internal/command -run TestStorageImportRejectsInvalidSeriesTagBeforeMetadataSideEffects -count=1
```

Expected: dry-run 错误地成功，真实导入已调用 Metadata。

- [x] **Step 3: 实现局部 validator**

在 `storage_import.go` 增加：

```go
const maxStorageImportSeriesTagBytes = 128

func validateStorageImportSeriesTag(tag string) error {
    if !utf8.ValidString(tag) {
        return errors.New("series_tag must be valid UTF-8")
    }
    if len(tag) > maxStorageImportSeriesTagBytes {
        return errors.New("series_tag must not exceed 128 bytes")
    }
    if strings.TrimSpace(tag) != tag {
        return errors.New("series_tag must not have leading or trailing whitespace")
    }
    for i := 0; i < len(tag); i++ {
        if tag[i] < 0x20 || tag[i] == 0x7f {
            return errors.New("series_tag must not contain ASCII control characters")
        }
    }
    return nil
}
```

在 `validateStorageImportOptions` 最后调用该函数。该函数必须在
`runStorageImport` 打开文件或调用 Metadata client 前执行。

- [x] **Step 4: 增加 Storage/CLI 合同矩阵**

在 CLI 测试中加入合法边界：

- 空 tag；
- `venue:binance`；
- 精确 128 字节 UTF-8；

并保持错误字符串与
`modules/storage/internal/rowidentity/series_tag_test.go` 的输入矩阵一致。

- [x] **Step 5: 运行 CLI 测试**

Run:

```bash
cd modules/cli
go test ./internal/command -count=1
go test ./... -count=1
```

Expected: PASS。

- [x] **Step 6: 提交**

```bash
git add modules/cli/internal/command/storage_import.go \
  modules/cli/internal/command/storage_import_test.go
git commit -m "fix(cli): validate import series tags before side effects"
```

---

### Task 5: 集成验证和执行计划收口

**Files:**
- Modify: `docs/superpowers/plans/2026-07-29-factor-runtime-correctness-hardening.md`

- [x] **Step 1: 运行格式和定向模块测试**

Run:

```bash
gofmt -w \
  modules/factor/internal/registry \
  modules/factor/internal/scheduler \
  modules/factor/internal/bootstrap \
  modules/factor/internal/rpc \
  modules/archive/internal/backfill \
  modules/archive/internal/writer \
  modules/cli/internal/command

(cd modules/factor && go test ./... -count=1)
(cd modules/archive && go test ./... -count=1)
(cd modules/cli && go test ./... -count=1)
```

Expected: PASS。

- [x] **Step 2: 运行 race 和 Python 测试**

Run:

```bash
(cd modules/factor && go test -race ./internal/scheduler ./internal/rpc ./internal/trigger/... -count=1)
(cd modules/archive && go test -race ./internal/backfill ./internal/writer ./internal/bootstrap -count=1)
(cd modules/factor && \
  PYTHONPATH="$PWD/../../packages/pyruntime/python${PYTHONPATH:+:$PYTHONPATH}" \
  python3 -m pytest pyworker -q)
```

Expected: Go 无 race；Python `23 passed` 或更多。

- [x] **Step 3: 运行 Storage 合同和真实隔离 E2E**

Run:

```bash
./scripts/test-storage-boundary-contract.sh
./scripts/test-storage-consistency-contract.sh
./scripts/test-series-tag-e2e.sh
```

Expected: 全部 PASS；E2E 覆盖 Storage/View、Factor Python、Archive Parquet 和 Monitor。

- [x] **Step 4: 运行 workspace 验证**

Proto 生成与 workspace 测试必须串行：

```bash
make verify-pr
git diff --check
git status --short
```

Expected: `make verify-pr` PASS；仅存在本计划内预期修改。

- [x] **Step 5: 更新原执行计划**

只将本轮有证据的 Task 7、9、14、18 条目更新为已修复，并记录：

- 六个回归测试；
- 模块/race/Python/contract/E2E/verify-pr 结果；
- 实际验证 commit SHA。

Task 19 的 106、SCF、50 节点和远端 SHA 条目没有重新执行时保持未勾选。

本轮验证基于代码提交 `84d2bcc0`、`c187d0c5`、`555828a0`：

- Factor、Archive、CLI 全模块测试通过；
- Factor 与 Archive 定向 race 通过；
- Python worker `23 passed`；
- Storage boundary/consistency contract 通过；
- `test-series-tag-e2e.sh` 覆盖 Factor、Archive、Monitor 并通过；
- `make verify-pr` 与 `git diff --check` 通过。

- [x] **Step 6: 提交验证文档**

```bash
git add docs/superpowers/plans/2026-07-29-factor-runtime-correctness-hardening.md
git commit -m "docs(factor): record review remediation evidence"
```

- [x] **Step 7: 使用 codeCR 做独立审查**

审查范围从本设计提交的父提交到当前 HEAD。要求逐项复核六个原始问题、锁顺序、
测试缺口和是否引入过度设计，并提供文件/行号证据。

Expected: 无 P0-P2；如有问题，回到对应 Task 按 TDD 修复并重新运行受影响验证。

最终 codeCR 审查范围为 `5b2626bc..7e6347da`，结论为无剩余 P0-P2。非阻断测试
缺口为：未覆盖超过 1000 个 enabled Factor 的启动分页、未用真实 SQLite 删除贯穿
完整 Recalc RPC、Backfill incomplete 回归只覆盖第一页。

- [x] **Step 8: 最终状态核对**

Run:

```bash
git log --oneline --decorate -10
git status --short
git diff --check HEAD~5..HEAD
```

Expected: worktree clean，提交边界清楚，所有代码和文档修改均已提交。
