# Factor 执行计划审查问题修复设计

**日期：** 2026-07-30  
**状态：** 已确认，待实施

## 1. 目标与边界

本设计修复 Factor 执行计划复审发现的六个正确性问题：

1. 同批 Binding 可以形成 Dataset 环；
2. secondary View 会被误认为 Factor 可读 View；
3. Factor 定义更新后，旧排队或在途任务可能覆盖新结果；
4. Archive Backfill 会归档不完整 View；
5. Archive worker 失败后可能阻塞 producer；
6. CLI import 未在产生副作用前校验 `series_tag`。

系统仍采用个人量化场景下的简单模型：单实例 Factor、进程内调度、SQLite
Registry、允许通过 Recalc 修复实时缺口。不引入 DAG 引擎、持久化任务、事件溯源、
分布式锁或 Exactly-once。

## 2. Factor Binding 合同

### 2.1 同批环限制

启用 Factor 前，Registry 读取该 Factor 的全部 enabled Binding，先构造候选 target
集合，再逐条校验：

- `source_dataset == target_dataset` 时拒绝；
- 任一 `source_dataset` 出现在同批 target 集合中时拒绝；
- source 已标记为 `dataset_role=factor_result` 时继续拒绝。

这条规则有意禁止同一 Factor 内的 `A -> B -> C` 链和 `A -> B -> A` 环。Factor
模块不提供 DAG；需要链式计算时，应把中间结果导出到模块外或重新设计为一个 Factor。

### 2.2 Primary View 合同

Binding 的输入 View 必须同时满足：

- `status=active`；
- 存在 `active_index_id`；
- `primary_dataset_id == source_dataset`；
- active projection 覆盖全部 `input_columns`。

校验规则与 Storage Primary runtime 的 View resolver 保持一致。source 仅作为
secondary Dataset 出现在 joined View 中时，不允许启用 Binding。

## 3. Factor 任务生命周期

### 3.1 进程内执行门闩

Scheduler 为每个 `factor_id` 维护进程内读写门闩：

- realtime 和 Recalc 在执行一个 Factor 前获取读锁；
- Factor disable、定义更新和影响执行合同的 Binding 更新获取写锁；
- 写锁会等待已经开始的旧任务结束，再修改 Registry 状态；
- disable 成功后清理该 Factor 尚未开始的排队任务。

任务获取读锁后、调用 Python 前重新读取当前 Factor 与 Binding，确认：

- Factor 仍为 enabled；
- Binding 仍为 enabled 且 scope 匹配；
- 当前定义的 `source_hash` 与 Task 中冻结的值一致。

校验失败的旧任务被丢弃，不读取数据、不执行 Python、不写回 Storage。disable RPC
返回时，旧版本任务已经结束或被清理，因此后续 update、enable、Recalc 不会被旧结果
覆盖。

### 3.2 锁顺序

统一顺序为：

1. RPC mutation mutex；
2. Factor 生命周期写锁；
3. Registry/SQLite 操作。

任务侧只获取生命周期读锁，再读取 Registry。Scheduler 自身队列锁不能在等待生命周期
锁时持有，避免队列与管理 RPC 互相等待。

门闩只存在于单进程内，符合当前单实例部署合同。进程重启后内存任务本来就会丢失，
不需要恢复门闩状态。

## 4. Archive

### 4.1 Backfill 完整性

每次 Storage 范围读取在追加 Journal 前检查 `complete`。只要任一响应为
`complete=false`：

- 本次 Backfill 立即失败；
- 不把该响应中的 rows 追加到 Journal；
- 不调用本轮最终物化；
- 错误包含请求 scope 和 Storage 返回的 coverage，便于稍后重跑。

Backfill 不增加后台重试队列。View 追平后，由用户重新执行同一 Backfill 即可。

### 4.2 Writer 失败收敛

worker 的单个 `WritePartition` 失败时：

- 非阻塞记录第一个错误；
- 继续消费 `jobs`，确保 producer 总能完成发送；
- 所有 worker 退出后返回第一个错误；
- 没有 worker 错误时返回 `ctx.Err()` 或 `nil`。

失败分区继续保留 dirty 状态，后续周期或 CLI compact 可再次处理。该方案不需要额外
协调 goroutine、持久化状态或复杂取消协议。

## 5. CLI import

CLI 在打开文件、读取 Metadata、绑定 Subject 和写入数据前校验 `series_tag`：

- 必须是合法 UTF-8；
- 最长 128 字节；
- 不允许首尾空白；
- 不允许 ASCII 控制字符。

dry-run 和真实导入走同一校验入口。CLI 使用一个局部、无状态的校验函数，并通过合同
测试锁定与 Storage 相同的输入矩阵。当前不新增公共 package，避免为一个短小规则扩大
模块依赖面。

## 6. 测试设计

实施严格采用测试先行：

1. 同一 disabled Factor 的 `A -> B`、`B -> A` 在 enable 时失败；
2. source 只有 secondary View 时 Binding enable 失败；
3. 旧任务排队或运行期间 disable 会等待或丢弃任务，Recalc 结果不被旧版本覆盖；
4. Backfill 收到 `complete=false` 时不追加 Journal、不生成 Parquet；
5. worker 数量个失败分区之后仍有待处理分区时，调用及时返回首个错误；
6. CLI dry-run 和真实导入都在绑定 Subject 前拒绝非法 tag。

验证范围：

- Factor、Archive、CLI、Storage 定向单元测试；
- Factor 和 Archive 高风险包 race；
- Python worker 测试；
- Storage boundary/consistency contract；
- `scripts/test-series-tag-e2e.sh`；
- `make verify-pr`；
- 独立 `codeCR` 审查。

## 7. 完成标准

- 六个复现测试先失败、实现后通过；
- 不引入新的持久化调度、DAG 或分布式协调；
- 所有现有模块测试和真实 series-tag E2E 无回归；
- codeCR 不存在 P0-P2 未处理问题；
- 执行计划中对应 Task 7、9、14、18 和最终验收项按真实证据更新。
