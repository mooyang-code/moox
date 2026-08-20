# 按标的批量执行多个因子 Implementation Plan
> **For agentic workers:** REQUIRED SUB-SKILL: Use `superpowers:subagent-driven-development` (recommended) or `superpowers:executing-plans` to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 将同一来源 View、同一周期、同一标的下的多个因子合并为一次 Python 批量执行，减少 Python 进程调用、DataFrame 重复投影和调度开销；保留每个因子的独立结果写回、manifest、失败状态和 `FactorPeriodComputed` 语义。

**Architecture:** `ViewSourcePeriodReady` 仍按来源周期触发。TaskRunner 继续按精确读取窗口合并 View 读取，但每个 read group 产出一个“标的批次”，批次内包含多个 factor binding。Python worker 接收一个 DataFrame 和多个因子规格，返回按 binding/task 分隔的结果；Go 层逐因子校验、重试和调用现有 `WriteFactorPatch`。任何单因子失败只影响该因子，读取失败或整个 Python 批次失败才影响该标的批次中的全部因子。手动 `Run`/`RecalcFactor` 的单因子范围计算保持现有行为；实时 View-ready 和 period-scoped 批量路径使用新能力，并由配置开关支持快速回退。

**Tech Stack:** Go 1.x、现有 `taskrunner`、`engine.Executor`、Python stdio worker、pandas JSON 协议、现有 Storage manifest/writeback、Prometheus 和 CLS 结构化日志。

**Non-goals:** 不修改因子定义、输出字段命名、结果 Dataset、Storage schema、EventBus subject、marker proto 或 manifest 语义；不把不同标的合并到一个 DataFrame；不做跨标的/截面因子；不一次性把全部标的加载到内存；不在本阶段把多个因子的结果合并成一次 Storage Upsert（先复用逐因子 manifest/writeback，避免改变清理和幂等边界）。

---

## 1. 建立批量领域模型和开关

- [ ] 在 `modules/factor/internal/engine/types.go` 增加批量执行类型：
  - `BatchTask`：批次 ID、space/source/result/frequency/subject、period 时间、窗口时间、触发信息、共享 `DataFrame` 所需上下文和按稳定顺序排列的 `[]FactorTask`。
  - `BatchItemResult`：`TaskID`、`BindingID`、`FactorResult`、单项 `Err`，让响应可以部分成功。
  - `BatchResult`：批次 ID、`[]BatchItemResult`、批次级错误。
  - 保持现有 `FactorTask`/`FactorResult` 不变，确保 manifest key 和 `DeterministicTaskID` 继续按 binding+subject+period 唯一。
- [ ] 在 `modules/factor/internal/engine/executor.go` 增加可选 `BatchExecutor` 接口：
  - `ExecuteBatch(context.Context, *BatchTask, *DataFrame) (*BatchResult, error)`。
  - `Executor.Execute` 仍是单因子接口，供 `Run`、`RecalcFactor`、CLI 和批量关闭时使用。
- [ ] 在 `modules/factor/internal/taskrunner/factor_gate.go` 增加 `AcquireRuns([]string)`：去重、按 `FactorID` 字典序加读锁、逆序释放，避免同一批次多因子锁顺序反转；保留 `AcquireRun` 行为不变。
- [ ] 在 `modules/factor/internal/taskrunner/service.go` 增加 `WithBatchExecution(bool)` 和服务字段，默认启用；关闭时对 period read group 回退到当前逐因子执行路径。
- [ ] 在 `modules/factor/internal/bootstrap/config.go` 增加 `Engine.BatchEnabled bool`、默认值 `true`、`MOOX_FACTOR_ENGINE_BATCH_ENABLED` 环境覆盖和布尔值校验；在 `modules/factor/cmd/server/main.go`/`modules/factor/internal/bootstrap/bootstrap.go` 将配置传入 TaskRunner。
- [ ] 更新 `modules/factor/config/app.yaml`、配置测试和 `modules/factor/README.md`，明确 `batch_enabled` 只控制同一标的的多因子合并，不改变 `python_workers`、`view_read_workers` 上限。

**Tests:**

- `modules/factor/internal/bootstrap/config_test.go`：默认 true、YAML false、环境变量 true/false、非法值不静默启用。
- `modules/factor/internal/taskrunner/factor_gate_test.go`：多因子排序加锁、重复 FactorID 只加一次、释放后可继续获取；并发执行无死锁。

## 2. 将 period read group 改造成按标的批次

- [ ] 在 `modules/factor/internal/taskrunner/read_pipeline.go` 将 `preparedTask` 拆为 `preparedBatch`：保留一个共享 `RangeChunk` 和该 group 的全部成员索引；普通非 period task 继续走原单任务通道。
- [ ] 保留 `buildPeriodReadGroups` 的精确 key 和输入列并集逻辑；明确 group 的代表任务只负责构造一次 Storage read，成员的 `Factor.InputColumns` 仍只在批次执行前投影或由 batch encoder使用。
- [ ] 调整 `runPeriodReadPipeline`：一次 read 成功后投递一个 batch，不再为每个 member 向 prepared queue 投递一项；read timeout/重试失败时把同一错误复制到所有成员的 `Result`，并正确释放 pending 计数。
- [ ] 在 `modules/factor/internal/taskrunner/service.go` 增加 `executePreparedBatch`：
  - 对每个成员执行现有 `TaskValidator`；无效成员只标记自身失败，不阻止其他有效成员进入批次。
  - 获取所有 FactorGate 读锁后调用 `BatchExecutor`；没有批量接口或开关关闭时逐成员调用现有 `runWithPeriodRead`。
  - 批量结果按 `TaskID`/`BindingID` 建索引，拒绝重复/缺失/未知成员，防止错写其他 binding。
  - 每个成功结果按现有 `filterTargetResult`、`validateFactorResult` 和 `WriteFactorPatch` 执行；写回失败只重试该成员，不重新计算整批。
  - 批次级 Python/协议错误按现有 retry policy 重试整批；重试耗尽后所有成员得到同一个终态错误。
  - 保持空目标周期的 manifest clear 行为，并让每个成员都能得到独立 terminal `Result`。
- [ ] 将 `RunAll` 的 pending/active 统计改为“批次执行槽位 + 成员终态”两层计数，避免同一批次被错误计成一个因子或重复减少；`DatasetObservation` 仍按最终因子写回逐项上报。
- [ ] 在 `modules/factor/internal/trigger/view_ready_runner.go` 保持当前 subject×binding 建模、状态聚合和 marker 逻辑，只消费 `RunAll` 返回的逐成员结果；日志额外记录 `batch_count`，不删除现有 `task_count`。

**Tests:**

- `modules/factor/internal/taskrunner/read_pipeline_test.go`：同一 subject、相同 period、两个因子只发生一次 `ReadPeriodChunk`，产生一个 prepared batch；不同 subject 仍独立读取。
- `modules/factor/internal/taskrunner/run_all_test.go`：2 个因子 1 个 subject 只调用一次 batch executor、得到 2 个结果；batch disabled 时恢复旧的 2 次单因子调用；read 失败时两个成员都终态失败。
- `modules/factor/internal/taskrunner/service_test.go`：一项写回失败只影响该因子；空结果清理每个 manifest；取消 context 不泄漏 pending/active 计数。
- `modules/factor/internal/trigger/view_ready_runner_test.go`：批次部分成功时 marker 的 binding 状态一个 complete、一个 degraded，失败 binding 清理旧输出，成功 binding 不被清空；所有成员终态后才上报 marker。

## 3. 扩展 Go/Python 批量协议

- [ ] 在 `modules/factor/internal/engine/json_codec.go` 增加 `EncodeJSONBatchRequestMeta` 和 `DecodeJSONBatchResponse`：
  - 请求包含一个共享 `df`、批次上下文和稳定排序的 `factors` 数组；每项携带 `task_id`、`binding_id` 和完整 `FactorSpec`。
  - 响应包含每项 `task_id`/`binding_id`、`ok`、结果 rows 或 `error_type`/`message`；批次级协议错误仍作为 Go error。
  - 复用现有时间、series_tag、输出列、NaN/Infinity、target window 校验；禁止 Python 返回未声明的成员或重复身份。
- [ ] 在 `modules/factor/internal/engine/executor.go` 实现 `PythonWorkerPool.ExecuteBatch`：
  - 一次 `RunAnyLoadedMany` 发送所有需要的 factor source load，去重 logical ID；一次 stdio `TYPE_RUN` 请求只携带一份 DataFrame。
  - 为 batch request 生成稳定 ID，但每个结果保留原 `FactorTask.TaskID`，不改变 writeback/manifest 幂等键。
  - 处理整个 worker 进程退出、超时、协议解码失败为批次级错误；处理单因子返回的业务失败为 `BatchItemResult.Err`。
- [ ] 在 `modules/factor/pyworker/worker.py` 增加批量分支：
  - 先解码一次 DataFrame，再逐项加载已验证模块并调用 `compute(df[input_columns], params)`。
  - 每个因子独立捕获异常并返回 item error；成功项独立校验输出列、时间范围、series_tag、重复键和排序。
  - 保持单因子旧请求格式可用，避免 `run-once`/现有 Python 协议测试被批量路径牵连。
- [ ] 在 `modules/factor/pyworker/codec.py` 提取结果编码 helper，批量响应和单项响应共用 JSON 数值/NaN/null 处理；更新协议版本说明但不改变 framing。

**Tests:**

- `modules/factor/internal/engine/json_codec_test.go`：双因子请求编码、响应按 binding/task 还原、缺项/重复项/未知项/错误项校验、目标窗口过滤。
- `modules/factor/internal/engine/executor_test.go`：fake process/pool 验证一次 batch 请求只携带一份 DataFrame 和多个 factor load；整个 worker 错误与单 item 错误分类正确。
- `modules/factor/pyworker/test_worker.py`：两个临时 factor 共享 DataFrame 返回两个结果；一个 factor 抛异常时另一个仍成功；输入列/输出列/NaN/时间范围校验与单因子一致。

## 4. 保持写回、marker 和失败语义不变

- [ ] 复核并补强 `modules/factor/internal/storageio/writeback.go` 的批量调用边界：第一阶段仍逐 `FactorTask` 调 `WriteFactorPatch`，确保每个 binding 的 manifest 替换、旧行清理和 deterministic write ID 不互相覆盖；不把多个 factor 输出合并进同一 manifest。
- [ ] 在 `modules/factor/internal/trigger/view_ready_runner.go` 保持 excluded subject、failed upstream subject、空目标周期和部分失败的清理顺序；批量只改变执行边界，不改变状态聚合。
- [ ] 给 `modules/factor/internal/observability/period_metrics.go` 增加低基数批量指标，例如 `batch_running`、`batch_total`、`batch_factor_total`、`batch_elapsed_seconds`、`batch_failed_total`，标签仅 `source_view`/`frequency`/`status`，禁止 subject、period、task ID。
- [ ] 在 `modules/factor/internal/taskrunner/service.go` 和 `modules/factor/internal/trigger/view_ready_runner.go` 增加结构化日志字段：`batch_id`、`batch_count`、`batch_factor_count`、`python_execute_calls`、`write_factor_count`、`batch_elapsed_ms`；保留现有 `factor_task_start/done` 作为逐因子审计日志。
- [ ] 更新 `docs/运维/MooX指标监控.md`、`modules/factor/docs/realtime-verification.md`，给出批量调用数、因子终态数、部分失败和回退开关的排障方式。

**Tests:**

- `modules/factor/internal/observability/period_metrics_test.go`：指标注册、批次计数、低基数标签和失败计数。
- `modules/factor/internal/trigger/view_ready_runner_test.go`：确认 `FactorPeriodComputedMarker` 仍等待所有 binding 的终态，不因批量调用提前 ACK。
- `modules/factor/test/view_driven_e2e_test.go`：至少 2 个 subject × 2 个 factor，断言 batch executor 调用数为 subject 数、写回数为 subject×factor、marker 完整且无交叉输出。

## 5. 配置、文档和发布验收

- [ ] 将 `batch_enabled: true` 同步到 `modules/factor/config/app.yaml` 及正式部署模板/生成包；确认 `Load`、环境覆盖和 server bootstrap 读取的是同一字段。
- [ ] 更新 `docs/因子计算模块设计.md` 和 `docs/因子视图驱动计算设计.md`：将“同一 subject 的多个因子可以并行、每个因子一次 Python 调用”改为“同一 subject 一次批量 Python 调用，因子结果独立终态”；保留 View read worker、Python worker 总上限和有界 prepared queue 约束。
- [ ] 更新 `modules/factor/README.md`，写清：批次边界是同一 source view/frequency/period/subject；不同 subject 仍并行；手动单因子路径不受影响；关闭开关后使用旧路径。
- [ ] 增加一个 Make/脚本验收入口（沿用仓库现有 factor module test 约定），依次运行 Go 单测、Python worker 测试、factor view-driven E2E 和静态检查；不要把线上机器作为唯一验收依据。
- [ ] 在安静的本地 fixture 记录批量前后同一数据集、同一 `python_workers`/`view_read_workers` 下的：View read 次数、Python execute 次数、写回次数、周期总耗时、p50/p95、峰值 RSS；验收要求 Python execute 次数从 `subjects×factors` 降为 `subjects`，写回和 marker 计数保持正确，周期耗时不劣于旧路径，并记录实际收益而不承诺固定秒数。
- [ ] 发布前按顺序执行：
  - `cd modules/factor && go test ./... -count=1`
  - `cd modules/factor && go test -race ./... -count=1`
  - `python3 -m pytest modules/factor/pyworker -q`
  - `go vet ./modules/factor/...`（按仓库 Go workspace 实际入口调整）
  - `git diff --check`
  - factor 部署包构建、配置渲染合同和本地 view-driven E2E。
- [ ] 线上灰度验证只观察一个来源周期：检查 `factor_view_ready_done` 的 `batch_count`/`task_count`、Python execute 次数、marker 状态、因子结果最新时间和失败重试；先以 `batch_enabled=false` 作为回滚开关，不直接扩大 `python_workers` 造成内存压力。

## 6. 完成标准

- [ ] 同一 subject 的 4 个 enabled factors 只触发 1 次 Python batch，四个 binding 均写入各自结果 Dataset，manifest 不互相覆盖。
- [ ] 484 subjects × 4 factors 的周期执行表现为约 484 次 batch、1936 个逐因子终态/写回，View 读取仍约 484 次；`FactorPeriodComputed` 只在全部终态后提交。
- [ ] 一个因子计算/写回失败时，其他同标的因子成功写回并在 marker 中保持 complete；失败项清理旧输出并为 degraded，重投仍可幂等恢复。
- [ ] Python 批次级超时、进程退出、协议错误按整批重试；单因子业务异常不拖累同批其他因子。
- [ ] `batch_enabled=false` 能在不改数据 schema、不清理 manifest 的情况下回退旧执行路径。
- [ ] 本地 Go/Python/race/E2E/静态检查通过，线上日志证明批量调用数下降且因子结果时序不再因批量改造产生额外滞后。

