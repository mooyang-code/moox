# 腾讯云 API 限频与采集计划协调告警执行计划

v3（2026-10-20，阶段 1～5 已编码，阶段 6 发布验收待执行；v2 已吸收一轮 Codex 审查，实现中按下文「实现中的调整」修订）。起因是 2026-10-20 23:23 的「行情采集 scf_timer 协调」严重告警。实现后以[云节点](../模块/云节点.md)、[采集](../模块/采集.md)与[监控](../模块/监控.md)为准。

## 1. 背景与线上现象

告警原文：「采集计划配置服务调用失败：请检查 CloudNode、网关连接和批处理队列」，空间 `crypto`。

control 主机日志里的时间线（北京时间）：

| 时间 | 事件 |
| --- | --- |
| 23:14:11 | CloudNode 回读定时器，十余个函数报 `RequestLimitExceeded`（每秒上限 20，实际 21～33） |
| 23:16:25 | Collector 重启（进程 3583330 → 3599226） |
| 23:17:10～13 | 再次大面积限频；一个 `node-batch` 作业在「确保定时器」时被限频，退避重试 |
| 23:17:18～23:23:10 | Collector 每分钟报 `publish lease rejected: collector publish lease is held` |
| 23:25:05 | `context deadline exceeded` |
| 23:26:10 | `collector fetch plan applied space=crypto plan=22`，自行恢复 |

这次告警由三个问题叠加而成：

1. **告警归类错误**：Collector 把「发布租约被占用」上报为 `publish_lease`，这个原因不在白名单 `assignmentFailureReasons` 里，被 `normalizeAssignmentFailureReason` 兜底成 `cloudnode`。Monitor 于是显示成「CloudNode 调用失败」。实际上这是租约在正常工作：重启前旧进程提交的批次作业还在执行，它的操作占用挡住了新进程。
2. **持续限频**：近 24 小时 CloudNode 每小时有 130～190 次 `RequestLimitExceeded`，从未停止。限频拖慢了批次作业，延长了操作占用的时间，冲突窗口也随之变长。
3. **作业还在跑也被记为失败**：拿到租约后的第一轮（23:24:10 开始），提交的批次作业在 55 秒的单轮时限内没有跑完，23:25:05 返回 `context deadline exceeded`，被记成 `cloudnode` 失败。10-19 15:44:05 那次重启后也出现过同样的现象。原因见 §2.5。

## 2. 已确认的事实

### 2.1 腾讯云限频口径

[API 频率限制说明](https://cloud.tencent.com/document/product/1278/109059)：默认限频的维度是 **`API + 接入地域 + 子账号`**，也就是每个接口单独计数。在此之上，还叠加了主账号限频和 IP 限频，请求要同时满足所有策略才放行。接入地域指请求解析到的云 API 服务器所在地域，与函数所在地域无关。时间窗口按请求到达服务器的时间计算。超限返回 `RequestLimitExceeded`，官方建议延迟重试。同一子账号的不同密钥共用配额，轮换密钥不会多出配额。

CloudNode 用到的接口及其默认限频（[云函数 API 概览](https://cloud.tencent.com/document/product/583/17235)）：

| 接口 | 次/秒 | CloudNode 用途 |
| --- | --- | --- |
| `ListTriggers` | **20** | 回读定时器（`GetTimerTrigger`）、确保定时器前查询、更新后轮询确认（`waitForTimerTrigger`） |
| `UpdateTrigger` | **20** | 更新定时器 |
| `ListNamespaces` / `CreateNamespace` | 20 | 确保命名空间 |
| `UpdateFunctionEventInvokeConfig` / `PutReservedConcurrencyConfig` | 20 | 异步重试配置、预留并发 |
| `GetFunction` | 100 | 回读函数配置 |
| `CreateTrigger` / `DeleteTrigger` | 100 | 创建、删除定时器 |
| `CreateFunction` / `DeleteFunction` / `UpdateFunctionConfiguration` / `UpdateFunctionCode` / `ListFunctions` | 100 | 函数增删改查 |
| `Invoke` | 100 | 同步、异步调用函数 |

所有调用都走 `scf.tencentcloudapi.com`（`-fsi` 金融区除外），从 control 主机发出，所以接入地域固定，同一云账号下同名接口共用一份配额。

### 2.2 谁在调用这些接口

- 只有 **CloudNode** 一个进程调用 SCF 管理接口（`modules/cloudnode/internal/providers/tencentscf`），它只部署在 control 上，单实例。
- Collector 引用 `packages/cloudprovider/tencent`，但只用到常量和校验函数，不发请求。SCF 函数运行时访问 Binance 和 Storage，日志上报走 CLS 的上报域名，不占 SCF 管理接口的配额。
- CLI 的运维命令会用同一云账号，但频率很低。

因此**进程内限流就等于全局限流**，既不需要中心式限流服务，也不需要把配额写进函数环境变量。

### 2.3 超限的来源

两条路径打的都是 `ListTriggers`（20 次/秒）：

- **回读**：Collector 核对时带 `refresh_readback`，CloudNode 显式回读不截断，42 个函数全部要读。每个函数并发调用 `GetFunction` 和 `ListTriggers`，并发槽是 `timerReadbackBatchSize*2 = 8`（[node.go:82](../../modules/cloudnode/internal/rpc/node.go)），`ListTriggers` 瞬时速率远超 20。
- **下发**：`EnsureTimerTrigger` 先调一次 `ListTriggers`，`UpdateTrigger` 之后再由 `waitForTimerTrigger` 每 200ms 轮询一次 `ListTriggers`，最长轮询 5 秒。单项轮询是 5 次/秒，批次执行器每次并行处理 3 项，合计 15 次/秒。

**自我维持的循环**：只要有函数读失败，就计为 `unverified`，Collector 把下次核对从 1 小时（`planVerifyInterval`）提前到 5 分钟（`planVerifyRetry`）。下一轮又是一次突发，又被限频。日志里每 6 分钟一次的 `cloud readback incomplete unverified=12～16` 就是这个循环。

限频的业务影响：采集不受影响，函数照常按定时器运行。受影响的是三方面：漂移发现会推迟；配置下发被挤占，今天有 4 次「确保定时器」被限频，都在第 2 次尝试时成功；批次作业变慢，拉长了发布租约的冲突窗口。

### 2.4 发布租约

`CollectorPublishLease` 由 Admin 签发，作用是保证同一空间同一时刻只有一个发布方修改采集函数：

- **租约**：有效期 2 分钟，需要续租。`FencingToken` 单调递增，CloudNode 提交变更时校验，已过期或被取代的发布方会被拒绝。
- **操作占用**：用来绑定一个在途的异步批次作业。每次续期都把有效期延长到「当前时间 + 6 分钟」（`operationClaimTTL`），CloudNode 每 30 秒续期一次（[publish_fence.go:28](../../modules/cloudnode/internal/rpc/publish_fence.go)）。结果不确定时，占用会保留到 TTL 到期。只要还有未结束的占用，任何人都拿不到新租约（[lease.go](../../modules/admin/internal/service/publishlease/lease.go)）。
- **冲突的持续时间没有固定上界**：它等于旧作业的剩余执行时间再加最多 6 分钟。CloudNode 批次单项的总预算是 16 分钟（3 次尝试，每次最长 5 分钟，[node_batch_runner.go:18](../../modules/cloudnode/internal/rpc/node_batch_runner.go)）。这次线上冲突持续了约 6 分钟。
- **错误类别**：Admin 用 `CONFLICT` 表示被占用或被取代，用 `NO_PERMISSION`、`INVALID_PARAM` 和内部错误表示其他情况（[service.go:25](../../modules/admin/internal/service/publishlease/service.go)）。但 Collector 的 `collectorLeaseFromResponse` 把所有非成功响应合并成一个普通错误（[scfinvoker/client.go:388](../../modules/collector/internal/scfinvoker/client.go)），错误类别在这里丢失了。
- Collector 的持有者 ID 每个进程随机生成一次，所以重启后的新进程必须等旧作业的占用结束才能拿到租约。这是预期行为，本计划不改租约语义。

### 2.5 等待作业超时被记成 `cloudnode`

- 单轮对账的时限是 55 秒（`PlanManager.timeout()`，[plan_manager.go:316](../../modules/collector/internal/marketfetch/plan_manager.go)）。
- `submit` 提交批次后调用 `waitJob` 等待结果。`waitJob` 的本意是：等到 `JobWait` 仍未结束，就返回「未结束」，把作业记入 `r.pending`，下一轮继续，**不算失败**。
- 但 `JobWait` 生产环境从未设置，默认是 90 秒，比单轮时限还长。所以总是 ctx 先到期，`waitJob` 返回 `ctx.Err()`，`submit` 再按 `r.fail("cloudnode", waitErr)` 记为失败（[plan_reconciler.go:1024-1027](../../modules/collector/internal/marketfetch/plan_reconciler.go)）。结果是作业只是慢，就被报成了「CloudNode 调用失败」。另外，`waitJob` 只在 `checkJob` 返回之后才检查上限，一次慢查询就能越过上限。
- 漂移修复 `verifyFleet` 有同样的问题：作业未结束时 `allOK=false`，最终也是 `fail("cloudnode", ...)`（[plan_reconciler.go:1393-1409](../../modules/collector/internal/marketfetch/plan_reconciler.go)）。这类作业经 `waitAux` 等待，未结束时登记在 `r.aux`（[plan_aux.go:83](../../modules/collector/internal/marketfetch/plan_aux.go)），不在 `r.pending` 里。
- 补采的创建、升级、删除（`:1634`、`:1662`、`:1688`）也调用 `waitJob`，但它们没有记录在途作业，并且用 `defer releaseBackfillLease` 在返回时立即释放租约。**所以 `waitJob` 不能全局改成提前返回**，否则补采会在作业还在跑时释放租约。这是已有缺口，本计划只保证不扩大它（§8）。
- 白名单里的 `submit_timeout`（Monitor 对它有专门文案）在生产代码里没有任何地方设置。

## 3. 目标与非目标

**目标**

1. CloudNode 发出的每类腾讯云接口，在**任意一秒窗口**内都不超过官方默认限频，`RequestLimitExceeded` 降到接近 0。
2. 偶发限频（主账号或 IP 级限频、CLI 同时操作）在 CloudNode 内部退避重试消化掉，不再变成回读失败或作业重试。
3. Collector 核对回到每小时一次，6 分钟循环消失。
4. 只有明确的「租约被占用」才单独归类并享有宽限期；Admin 不可达、无权限、响应异常照常告警。
5. 批次作业只是比单轮时限慢时，按「进行中」处理，不再记为 `cloudnode` 失败。

**非目标**

- 不做中心式限流，也不引入 Redis 之类的外部组件。
- 不在函数环境变量里下发配额，因为函数不调用这些接口（§2.2）。
- 不改发布租约和操作占用的语义与 TTL，也不改停机时的租约释放逻辑：围栏语义要求在途作业结束前不能释放。
- 不在本计划里修补采路径的租约缺口（§2.5），只保证不扩大它。另立问题跟踪。
- 暂不向腾讯云申请提高配额。如果限流后整体速度仍然不够，再考虑。

## 4. 方案总览

| 阶段 | 内容 | 模块 |
| --- | --- | --- |
| 1 | 按接口的进程内令牌桶 | CloudNode |
| 2 | `RequestLimitExceeded` 抖动退避重试 | CloudNode |
| 3 | 减少 `ListTriggers` 消耗，确认流程严格守住 5 秒预算 | CloudNode |
| 4 | 租约冲突单独归类与宽限期；作业未结束不再记为失败 | Collector、Monitor、`packages/report` |
| 5 | 文档 | docs |
| 6 | 发布与线上验收 | — |

阶段 1～3 可以单独发布 CloudNode，见效最快。阶段 4 先发布 Monitor，再发布 Collector。

## 5. 阶段 1：按接口的进程内令牌桶

**做法**

- 新增 `modules/cloudnode/internal/providers/tencentscf/ratelimit.go`，使用 `golang.org/x/time/rate`。cloudnode 的 `go.mod` 目前没有这个依赖，需要新增，版本对齐工作区里 Admin 等模块使用的 v0.15.0。
- 限流器必须是**进程级注册表**，不能挂在 `Client` 上，因为 `scfClient` 每次调用都会 `tencentscf.New(...)` 新建一个 `Client`（[node.go:1268](../../modules/cloudnode/internal/rpc/node.go)）。
- **注册表的键是 `云账号 ID + endpoint + action`**。不用 SecretID：同一子账号轮换密钥时，新旧密钥共用腾讯云的配额，而轮换窗口内新旧 `Client` 会同时存在，按 SecretID 分桶会让放行量翻倍。`tencentscf.New` 增加云账号 ID 参数，由 `scfClient(ctx, account)` 传入 `account.ID`。
- **突发容量设为 1**。`x/time/rate` 的桶初始是满的，如果突发容量等于速率，第一秒内能放行「桶里的令牌 + 当秒补充的令牌」，约等于两倍速率，仍会超限。突发容量为 1 时，请求被均匀摊开，任意一秒窗口都不超过配置速率。
- 默认速率取官方限频的 75%：20 次/秒的接口取 **15**，100 次/秒的接口取 **75**。未在表中登记的接口按 15 处理，偏保守。
- 加一个统一入口 `c.call(ctx, action string, fn func(ctx context.Context) error) error`，执行顺序是：取令牌，再执行 `fn`。provider 目录里所有 SDK 的 `XxxWithContext` 调用都改走这个入口，覆盖 §2.1 表中的全部接口。加一个单测扫描 provider 目录，禁止出现绕过 `call` 直接调用 SDK `WithContext(` 的写法。
- `call` **不额外设置超时**，只沿用调用方的 ctx。否则会破坏 `Invoke` 现有的 960 秒 HTTP 预算（[client.go:30](../../modules/cloudnode/internal/providers/tencentscf/client.go)）。
- **取令牌等待超时的错误要可识别**：`rate.Limiter.Wait` 在预计等待会超过 ctx 截止时间时，会立即返回一个普通错误（`would exceed context deadline`），此时 `ctx.Err()` 可能仍是 nil。`call` 要把它包成哨兵错误 `ErrRateLimitWaitExceedsDeadline`，`isTransientSCFProviderError`（[node_batch_runner.go:324](../../modules/cloudnode/internal/rpc/node_batch_runner.go)）把它视为瞬时错误，交给外层重试。
- 配置放在 CloudNode 已有的 `tencent_scf` 段下，可以按接口覆盖默认值：

  ```yaml
  tencent_scf:
    api_rate_limits:        # 可选；未写的接口使用内置默认值（官方限频 × 75%）
      ListTriggers: 15
      UpdateTrigger: 15
  ```

  校验规则：值必须在 `(0, 官方限频]` 范围内，接口名必须在 §2.1 的表里，否则启动失败，并给出中文错误。

- 可观测性方面，新增 `moox_cloudnode_tencent_api_wait_seconds{action}`（直方图，记录取令牌的等待时间）和 `moox_cloudnode_tencent_api_throttled_total{action}`（计数，见阶段 2）。这两个指标只在 CloudNode 本地的 `/metrics` 暴露，**不进 Monitor 白名单**。排查时直接在 control 上读取，以后需要告警时再登记。

**单测**

- 不同 action、不同云账号的桶互不影响；同一键在多个 `Client` 实例之间共享；同一云账号换 SecretID 后仍用同一个桶。
- 并发发起 42 个调用，**统计任意 1 秒滑动窗口内的请求数**，不超过配置速率。只看长期平均速率不够。
- ctx 取消时立即返回；ctx 未到期、但剩余时间不够等到下一个令牌时，返回 `ErrRateLimitWaitExceedsDeadline`，并且被 `isTransientSCFProviderError` 识别为瞬时错误。
- 配置校验：未知接口名、0 或负数、超过官方限频，都返回中文错误。

**验收**：本地用假的 SCF 客户端模拟 42 个函数的显式回读，`ListTriggers` 任意 1 秒窗口内不超过 15 次，整轮耗时约 3 秒。

## 6. 阶段 2：限频错误的抖动退避重试

**做法**

- 在 `c.call` 里识别 `*errors.TencentCloudSDKError` 且 `Code == "RequestLimitExceeded"` 的错误。这类请求在服务端被拒，没有执行，**对任何接口重试都是安全的**，包括 Create 类接口。
- 最多重试 2 次（共 3 次尝试），每次等待 `[0.5s, 2s)` 之间的随机时长（full jitter），等待期间响应 ctx 取消。每次重试都要重新取令牌。
- 每次被限频时 `throttled_total{action}` 加 1，并打一行 WARN，内容包括 action、尝试序号和 RequestId。最终仍失败时，原样返回错误。
- **与外层重试叠加后的上限**：批次执行器对瞬时错误最多尝试 3 次（间隔 500ms、1s），每次尝试的预算是 5 分钟。叠加后，同一个被持续限频的接口最多请求 9 次；内部退避每次尝试最多等 2 次 × 2 秒，3 次尝试共 6 次等待，上界 12 秒，加上外层的 1.5 秒间隔，合计不超过 13.5 秒（等待算在每次尝试的预算内，排队等令牌的时间另算），对单项 16 分钟的总预算影响可以忽略。阶段 1 生效后，这条路径预计很少触发。

**单测**：假客户端前两次返回 `RequestLimitExceeded`、第三次成功时，`call` 返回成功且计数为 2；连续 3 次限频时返回原错误；其他错误码不重试；ctx 取消时立即退出。

## 7. 阶段 3：减少 `ListTriggers` 消耗

**做法**

- `waitForTimerTrigger`（[trigger.go:186](../../modules/cloudnode/internal/providers/tencentscf/trigger.go)）改为：
  - 用 `context.WithTimeout(ctx, 5*time.Second)` 生成局部 ctx，**传给每次 `findTimerTrigger` 调用**。这样取令牌、限频重试、SDK 请求都受 5 秒约束，不会因为一次慢查询越过预算。父 ctx 剩余时间不足 5 秒时，以父 ctx 为准。
  - 第一次立即查询，之后按 500ms、1s、2s 退避。最坏情况从约 25 次 `ListTriggers` 降到 4 次左右。
  - 局部预算耗尽时，返回最后一次读到的结果，与现在的行为一致，由调用方校验。父 ctx 被取消时返回父 ctx 的错误。真正的查询错误原样返回。三种情况要区分开。
- 回读并发槽 `timerReadbackBatchSize*2` 保持不变。速率由阶段 1 的令牌桶控制，并发槽只用来限制同时在途的请求数。

**单测**：用假客户端记录调用时间戳，验证轮询间隔与次数上限；定时器在第 N 次轮询时变为一致时立即返回；单次查询一直卡住时，5 秒后返回，不会无限等待；父 ctx 取消与局部预算耗尽返回不同的结果。

## 8. 阶段 4：租约冲突单独归类，作业未结束不再记为失败

### 8.1 Collector：保留租约错误类别

`modules/collector/internal/scfinvoker/client.go`

- `collectorLeaseFromResponse` 按错误码区分：`CONFLICT` 返回哨兵 `ErrCollectorPublishLeaseHeld`（包装原消息），其他错误码、通信失败、响应校验失败保持普通错误。

`modules/collector/internal/marketfetch`

- `assignmentFailureReasons` 增加 `publish_lease_held`。**只有** `errors.Is(err, scfinvoker.ErrCollectorPublishLeaseHeld)` 时才用这个原因。Admin 不可达、无权限、响应不完整，仍然归为 `cloudnode`，照常告警。
- 获取租约的所有路径都按这个规则归类：`submit`（`:1013`），以及 `clearStale` 在 `:819` 返回、被调用方 `:707` 统一记成 `cloudnode` 的那条路径。补采路径（`:1634`、`:1662`、`:1688`）现在不调用 `fail`，保持不变。
- 删除从未使用的 `submit_timeout`，白名单与 Monitor 分支一起删。

### 8.2 Collector：作业未结束按「进行中」处理

- **不改 `waitJob` 本身**，因为补采路径依赖它现在的行为（§2.5）。新增 `waitJobWithin(ctx, jobID)`，供 `submit` 和 `waitAux`（[plan_aux.go:83](../../modules/collector/internal/marketfetch/plan_aux.go)，`verifyFleet`、清理、停用等辅助作业都经它等待）使用：
  - 等待上限是 `min(JobWait, 剩余时间 − 余量)`，余量取 5 秒与剩余时间四分之一的较小者，并用这个上限生成局部 ctx 传给 `checkJob`，避免一次慢查询越过上限。**实现中的调整**：最初写的是固定扣 5 秒，剩余时间不足 5 秒时直接视为未结束而不查询，短时限下作业永远查不到（既有的 `PlanManager` 用例因此失败），所以余量随剩余时间缩小。
  - 到达上限时作业仍未结束，就返回「未结束」，不返回错误。父 ctx 被取消时返回父 ctx 的错误。`checkJob` 本身出错时返回该错误。三种情况要区分开。
- `submit`：作业未结束时，记入 `r.pending`（现有行为），**不调用 `fail`**。
- `verifyFleet`：作业未结束的函数不算失败。`waitAux` 已经把未结束的作业登记进 `r.aux`（现有行为），后续由 `refreshAux` / `busyFunctions` 确认终态后收尾。只有作业已结束且确实失败，才调用 `fail("cloudnode", ...)`。
- **pending 指标同时覆盖 `r.pending` 和 `r.aux`**：有在途作业时，每轮收尾（`finishRound`）上报 `coordination_pending=1`，`pending_since` 取**最早在途作业的提交时间**，覆盖开放计划的 `CreatedAt`，否则计划一旦创建较久，宽限期就形同虚设。没有在途作业时保持原行为（开放计划取 `CreatedAt`）。在途作业按年龄决定是否影响对账成功：最老的在途作业未超过告警年龄（`pendingJobWarnAge`，10 分钟）时照常宣布成功；超龄说明卡住了，不再刷新成功时间，由 Monitor 的对账陈旧判定发现。最初的写法是“有在途作业就不宣布成功”，大批量升级（分批、连续十几分钟都有在途作业）会因此被误报为对账陈旧。
- **失败状态的清除规则**：`fail` 每次调用计数；一轮对账结束时，若本轮没有返回错误、且自上一轮结束以来（包括同一 tick 内对账之前的启用、升级）没有任何 `fail` 调用，就清除失败原因与 `failure_since` 并把 `coordination_healthy` 置 1，不管计划是否仍在进行中、是否还有在途作业，但不刷新最近一次成功时间。现在开放计划直接走 `advance`、不执行成功清理，会导致租约冲突解除后，旧的失败原因一直留着。

### 8.3 Collector：新增指标

- `moox_collector_market_fetch_coordination_failure_since_timestamp_seconds{space_id}`：失败原因首次出现时记录时间戳；原因变化时重置；按 §8.2 的规则清除时置 0。
- `packages/report/monitored_metrics.go` 同步登记。

### 8.4 Monitor

`modules/monitor/internal/observability/overview.go`

- 新配置 `market_health.timer_coordination_lease_grace`，默认 **10 分钟**。这是**告警策略，不是恢复时间的上界**：冲突时长取决于旧作业还要跑多久（§2.4），通常是 6 分钟左右。超过 10 分钟仍未恢复，说明作业卡住或有其他发布方，值得人工介入。
- 原因是 `publish_lease_held` 且失败持续时间不超过宽限期：状态为 `healthy`，文案为「发布租约被占用：等待在途云端作业结束，通常数分钟内自动恢复」。
- 超过宽限期：状态为 `down`，文案为「发布租约长时间被占用：请检查 CloudNode 批次作业是否卡住，以及是否有其他发布方（CLI 手工发布、补采）在运行」。
- 缺少 `failure_since` 时按超过宽限期处理。Collector 与 Monitor 按 §10 的顺序发布后，这种情况只会出现在指标丢失时。
- **pending 超过宽限期的判定（实现中的调整）**：最初计划新增「pending 超过 `pending_grace` 即 down」的分支。实现时发现 `coordination_pending` 同时用于「开放计划等待窗口结束」（最长可达一个整点周期），这样改会让正常等待的计划在 5 分钟后误报。改为不新增分支：在途作业卡住时，Collector 不刷新最近一次成功时间，Monitor 现有的对账陈旧判定（`timer_coordination_stale_after`，默认 15 分钟）会发现；首次运行尚无成功指标时仍不判定，这是已有行为，不在本计划内。

**单测**

- Collector：
  - `CONFLICT` 响应才映射到 `ErrCollectorPublishLeaseHeld`；`NO_PERMISSION`、通信失败仍归为 `cloudnode`。
  - `waitJobWithin` 在 ctx 剩余 50 秒、作业一直未结束时返回「未结束」，`submit` 不调用 `fail`，作业进入 `r.pending`，上报 pending；`checkJob` 一直卡住时在上限内返回。
  - `verifyFleet` 在作业未结束时不调用 `fail`；作业已结束且失败时仍记 `cloudnode`。
  - `pending_since` 取最早在途作业的提交时间；`r.aux` 非空时也上报 pending。
  - 开放计划进行中、本轮没有失败时，失败原因与 `failure_since` 被清除。
  - 补采路径的行为不变（回归测试）。
- Monitor：`publish_lease_held` 宽限期内为 healthy、超过宽限期为 down、缺少 `failure_since` 为 down、宽限期可配置；`cloudnode` 等其他原因不享受宽限。`modules/monitor/internal/healthview/incidents_test.go` 与前端 `web/src/views/ops/monitor/*.test.ts` 中涉及 `scf_timer` 的用例要跟着检查。

## 9. 阶段 5：文档

- [云节点](../模块/云节点.md)：腾讯云接口限流（维度、默认值、突发容量、`tencent_scf.api_rate_limits` 配置、本地指标）。
- [采集](../模块/采集.md)：协调失败原因增加 `publish_lease_held`，删除 `submit_timeout`；说明重启后短时间租约冲突属于预期；作业未结束按进行中处理。
- [监控](../模块/监控.md)：`timer_coordination_lease_grace` 及新文案。
- [部署与运维](../部署与运维.md)的故障表增加两行：「协调告警：发布租约被占用」和「CloudNode 日志出现 `RequestLimitExceeded`」，分别写明原因与处理方法。
- 在本目录 `README.md` 里更新本计划的状态。

## 10. 阶段 6：发布与线上验收

**发布顺序**（服务目录都不变）

1. 先单独发布 **CloudNode**（阶段 1～3），使用 `skills/moox/scripts/native-release.sh`，配置无需修改，默认值即可生效。
2. 观察 2 小时后，**先发布 Monitor，再发布 Collector**（阶段 4）。新指标必须登记在 `packages/report` 的白名单里，两边都内嵌了这份白名单：Collector 上报时按它过滤（[report/handler.go:279](../../packages/report/handler.go)），Monitor 入库时也按它过滤（[health_filter.go:16](../../modules/monitor/internal/metrics/health_filter.go)）。先发 Monitor，是为了避免新原因 `publish_lease_held` 先被旧 Monitor 当成普通失败。两者间隔越短越好。

**线上验收**（只读）

| 项目 | 方法 | 期望 |
| --- | --- | --- |
| 限频次数 | `grep RequestLimitExceeded cloudnode/logs/trpc.log`，按小时计数 | 从每小时 130～190 次降到接近 0 |
| 限流等待 | 在 control 上读 CloudNode 本地 `/metrics` 的 `moox_cloudnode_tencent_api_wait_seconds{action="ListTriggers"}` | 回读期间等待时间在数秒以内 |
| 核对循环 | Collector 日志 `cloud readback incomplete` | 不再每 6 分钟出现，核对恢复每小时一次 |
| 下发 | CloudNode 日志 `transient node batch provider failure` | 不再出现 `RequestLimitExceeded` 原因 |
| 重启演练 | 重启 Collector，观察协调状态 | 冲突期间显示「发布租约被占用」且为 healthy；拿到租约后的第一轮即使作业没跑完，也显示为进行中而不是失败；正常情况下不触发严重告警 |

## 11. 风险与回退

- **限流过严导致变慢**：一轮显式回读从「一瞬间」变成约 3 秒；大批量下发（例如代码包升级 42 个函数）会被 `UpdateTrigger` 的 15 次/秒限住，约 3 秒，可以接受。如果确实不够，可以通过 `api_rate_limits` 调整，上限是官方值。
- **主账号或 IP 级限频**：这两类阈值官方没有公开，靠阶段 2 的退避重试消化，并通过 `throttled_total` 观察。
- **宽限期掩盖真实卡死**：宽限期只覆盖明确的 `CONFLICT`，超过 10 分钟仍会 down；Admin 不可达、无权限等错误不享受宽限。
- **补采路径的租约缺口**：本计划不改变它，因为 `waitJob` 保持不变；它仍是已知缺口（§2.5），需要另行处理。
- **回退**：CloudNode、Collector、Monitor 都可以用 `native-release.sh rollback` 单独回退到上一个版本，没有数据迁移。

## 12. 参考

- [腾讯云 API 频率限制说明](https://cloud.tencent.com/document/product/1278/109059)
- [云函数 API 概览（各接口限频）](https://cloud.tencent.com/document/product/583/17235)
- [获取函数触发器列表 ListTriggers](https://cloud.tencent.com/document/product/583/44268)

## 13. 实现中的调整

- **`waitJobWithin` 的余量随剩余时间缩小**（§8.2）：固定扣 5 秒会让短时限下的作业永远查不到。
- **不新增 Monitor 的 pending 超期分支**（§8.4）：避免开放计划正常等待窗口时误报，卡住的在途作业由对账陈旧判定发现。
- **`fail` 计数而不是布尔标志**（§8.2）：同一 tick 内对账之前的步骤（启用、升级）失败时，不能被本轮的健康恢复清掉。
- **租约被占用的失败原因名为 `publish_lease_held`**，并删除从未使用的 `submit_timeout`（白名单与 Monitor 文案一并删）。
- **限流器键用云账号标识**：`TencentCredential` 增加 `AccountID`，由凭证解析器从云账号带出；没有账号标识时退回 `secret:<SecretID>`。
- **已知但不在本计划内**：Collector 的 `fail("package" | "store" | "storage" | "dataset" | "activity" | "preflight", …)` 同样不在白名单里，会被归为 `cloudnode`，Monitor 上仍显示「CloudNode 服务调用失败」。需要时另行把它们加入白名单并给出专属文案。
- **在途作业按年龄决定是否影响对账成功**（§8.2）：最老在途作业未超过 `pendingJobWarnAge`（10 分钟）时照常宣布成功，超龄才停止刷新成功时间。
- **Monitor 的租约宽限只在「被占用」是唯一失败原因时生效**（§8.4）：多个 Collector 实例上报不同原因时，真实故障不能被另一个实例的租约冲突盖住，文案也以真实故障为准；普通 pending 宽限同样不能盖过已超期或起始时间未知的租约失败。
- **升级等待也要收尾**（§8.2）：`EnableAll` 遇到 `errCodeUpgradePending` 时管理器会跳过本 tick 的对账，因此在 `EnableAll` 内补一次 `finishRound`，并把升级等待视为非失败。
- **令牌桶的保证**：突发容量 1 保证相邻放行的间隔不小于 1/速率，任意 1 秒窗口至多 速率+1 次（窗口两端各取一次）；默认只取官方限频的 75%，边界效应和调度抖动由 25% 余量吸收。配置值拒绝 NaN 与 Inf（YAML 的 `.nan`）。

## 14. Codex 审查记录

第一轮（计划，v1 → v2）见各节中的吸收说明。第二轮针对代码，结论与处理：

| 意见 | 处理 |
| --- | --- |
| 成功分支绕过 `failCount`，同轮日志级失败仍被清除 | **不采纳**：代码升级失败只记日志、之后同轮成功清除失败是原有行为（已删除的函数升级必然失败），改成不清除会造成常驻误报。`failCount` 只用于“无成功分支时的健康恢复” |
| 多实例聚合时租约冲突掩盖另一实例的真实故障 | 采纳：只有“被占用”是唯一原因才给宽限，文案以真实故障为准 |
| 启用前升级等待跳过 `Reconcile`，不收尾 | 采纳：`EnableAll` 内补 `finishRound`，升级等待视为非失败 |
| 令牌预约时间受限不等于实际放行满足任意 1 秒窗口 | 部分采纳：边界效应与抖动由 25% 余量吸收，写入文档；并发测试改为 250 个样本跨多个窗口 |
| `pendingGrace` 先返回，绕过租约宽限 | 采纳：已超期或起始时间未知的租约失败不再被 pending 宽限盖住 |
| 有在途作业不宣布成功，导致大批量误报对账陈旧 | 采纳：改为按最老作业的年龄判定 |
| NaN 绕过配置校验 | 采纳 |
| 真实时间 950ms 断言、复用同一 ctx 的脆弱测试 | 采纳：放宽为不依赖精确调度，并换用独立 ctx |
| 退避上界写成约 8 秒 | 采纳：改为不超过 13.5 秒 |

第三轮（对第二轮修正的复审）：

| 意见 | 处理 |
| --- | --- |
| `upgradeBeforeEnable` 读回失败被包装成升级等待却没有记失败，`EnableAll` 的收尾会把既有失败原因误清 | 采纳：读回失败路径调用 `fail("cloudnode", …)`，并补一个真正经过 `EnableAll` 的测试 |
| 多原因时文案仍可能指向租约超期 | 采纳：只有租约冲突是唯一原因才用租约文案，否则回退到通用提示 |
| 真实计时的并发测试里 106 次的窗口断言仍可能因调度暂停失败 | 采纳：窗口断言只放在假时钟测试里，并发测试只验证共桶与总体耗时 |
