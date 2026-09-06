# Trade 交易执行完善与设计收敛执行计划

> **供 Agent 执行：** REQUIRED SUB-SKILL: 使用 `superpowers:subagent-driven-development` 或 `superpowers:executing-plans`，按任务中的 `- [ ]` 逐项实施。代码审查使用 `codeCR`，等待所有审查完成后由主 Agent 独立核验。

**目标：** 完善“策略目标事件驱动异步调仓、实时下单接口、模拟账户”三条链路，先修复已确认的执行正确性问题，再统一授权、路由、接口与运行状态。

**架构：** 保留单 Trade 进程、SQLite、JetStream、账户级串行执行，以及 Live/Paper 共用的 OrderService、订单状态机、成交归并与持仓查询。Strategy 只产生 FULL 目标权重；Trade 首次受理后冻结估值和目标数量，依据实际账户事实收敛。人工接管与普通订单提交入口分开，但不复制第二套订单内核。

**技术栈：** Go、tRPC/Protobuf、SQLite/GORM、JetStream、Vue/TypeScript、Binance/OKX、Vitest/Playwright。

**状态：** 待用户确认，本轮仅核对并完善计划，不修改业务代码。下文的命令、SQL、接口草案和任务均为后续执行说明，不表示已经实施、部署或通过验收；其他分支的实现进度不据此自动勾选。

**审查基线：** `feature/mooyang`，`2bd4f508d45031e5a8fb629e5b62b88b9743810a`，2026-09-06。工作区同时存在 Strategy、Storage、文档和部署配置的其他修改；实施时重新核验，禁止覆盖或顺带提交。

**本次计划复核：** 原仓库 `feature/mooyang`，`c15d688ba3e25226c42c1fd10b5800ec21df5652`。复核开始时工作区干净；上述审查基线及其测试记录属于此前快照，不代表本轮重新运行测试。当前 Strategy 已有新的执行流水线提交，T01/T10 必须以最新生产者代码验证契约，而不是照搬旧工作区。

---

## 一、范围与决策

### 1.1 执行分期

| 阶段 | 任务 | 目的 | 开始条件 |
| --- | --- | --- | --- |
| A：正确性修复 | T01-T07 | 修复授权、幂等、不确定提交、费用、Paper 余额、故障隔离和错误可观测性 | 用户明确授权编码 |
| B：设计收敛 | T08-T10 | 分离估值与路由，补普通 SubmitOrder，统一协议及控制台 | A 验收通过，且确认下面的设计选择 |
| C：综合验收 | T11 | 验证真实生产接线、跨模块事件链和交付边界 | A、B 完成 |

允许先执行 A、独立交付，再讨论 B；不得以“计划文档已生成”为依据自动开始任何阶段。

### 1.2 阶段 B 采用的推荐设计

以下为本计划的明确推荐，不代表用户已经批准：

1. 执行授权最终统一为 `logical_account_id -> (instance_id, session_id)`；`auth_fence` 只用于控制面 CAS，不放入策略目标。旧 runner/sequence 不再成为另一套自动交易授权入口。
2. 组合账户继续支持多个同质执行账户。冻结的是组合权益、参考价和总目标数量，不冻结执行账户；参考价来源账户只作为 receipt 证据。
3. 保留 `PlaceManualOrder` 的显式接管语义：暂停组合账户、撤销策略订单、再下人工订单。新增普通 `SubmitOrder` 只允许显式 `MANUAL` 控制模式的组合账户；`STRATEGY` 模式即使暂时无人认领也不允许普通提交，不允许与自动调仓静默混跑。
4. Paper 本阶段定位为执行链验证：支持现有 MARKET、简化 LIMIT、手续费、滑点和重启恢复；不把它宣称为杠杆收益、流动性或强平风险的真实仿真。

### 1.3 不变的安全约束

- FULL 目标遗漏标的归零，空列表为零目标，`hold` 不发布新目标；缺数据、求值失败、消息失败都不能伪造成空目标。
- `valid_until` 到期只禁止基于该目标发起新交易；不自动撤销已发送订单，不自动清仓。Strategy 停用也不隐含撤单或清仓。
- PAUSED 状态仍接收和保存新目标，仍同步订单、成交和账户事实，只停止自动新下单。
- 相同 client order ID、相同用户参数回放同一订单；不同参数冲突。服务端参考价、时间、恢复次数不参与用户请求身份比较。
- 网络超时、限流和交易所 5xx 不等于明确拒单；先持久化不确定状态，再查询恢复，不盲目重发。
- 已有交易事实不得为了简化 schema 被删除或重置。无历史协议兼容要求，不等于可以清空用户的 Order、Fill、持仓、模拟账户和资金曲线。
- 正式库变更、生产开关、Testnet/实盘下单、部署和重启均不是本计划生成动作的一部分，实施阶段也必须在明确授权的环境执行。

### 1.4 本轮不做

不引入微服务拆分、分布式事务/锁、全局 exactly-once、通用工作流引擎、完整双式账本、订单簿仿真、部分成交模拟、高频延迟 SLA、多策略共享账户的虚拟持仓分账。Paper 杠杆配置、资金费和强平另立需求。Trade 到 Strategy 的可靠回报事件及其 outbox 不在本轮引入；先交付可查询状态与结构化错误记录。

### 1.5 三条能力的交付合同

| 用户需求 | 入口与执行方式 | 返回或可查询事实 | 对应任务 |
| --- | --- | --- | --- |
| 事件触发异步交易 | Strategy FULL 权重事件；Trade 持久受理后由 Worker 收敛 | receipt、目标状态、订单、成交、持仓；Broker ACK 不等于成交 | T02、T06-T08、T10-T11 |
| 实时交易接口 | MANUAL 的普通 SubmitOrder 快速受理；接管接口另行显式暂停/撤单 | action/order ID；查询真实订单状态，复用现有撤单与账户查询接口 | T03、T09-T11 |
| 模拟账户 | Paper Adapter 接入同一订单/成交内核，使用行情和简化撮合 | 余额、预占、订单、成交、持仓及重启后的同一结果 | T04-T06、T09、T11 |

这里的“实时”表示请求可及时受理和查询，不承诺同步成交或固定毫秒 SLA。第一版不新增第二套推送总线；客户端通过现有查询接口跟踪结果。

优先完成 A 阶段的正确性修复；普通 SubmitOrder 是明确的功能缺口。多账户动态路由和旧协议清理在确认设计后实施，不作为无限扩展交易平台的理由。

## 二、审查问题与任务映射

| 编号 | 已确认问题或设计差异 | 代码依据 | 对应任务 |
| --- | --- | --- | --- |
| R1 | 现代 session 目标无法通过 Resume 的旧 runner 校验 | `modules/trade/internal/application/logicalaccount/service.go`，`readiness` | T02 |
| R2 | 幂等 Claim/Rebind 无条件删除当前目标 | 同文件 `ClaimSession`、`RebindSession`；`infra/store/logical_account.go` | T02 |
| R3 | 人工提交不确定，但 action 被固化为 FAILED 且未关联订单 | `modules/trade/internal/application/operator/service.go`，`PlaceManualOrder`、`failManualAction` | T03 |
| R4 | OKX WS 累计 fee 被当作单笔 fill fee | `modules/trade/internal/exchange/okx/account_events.go`，`dispatchPrivate` | T04 |
| R5 | Paper 从初始资金重建时只累计最近 100000 笔成交 | `modules/trade/internal/execution/paper/adapter.go`，`GetAccountSnapshot` | T05 |
| R6 | 单订单错误阻塞全局 Paper；普通目标过期导致全局 Not Ready | `execution/paper/matcher.go`、`runtime/paper_matcher_worker.go`、`runtime/target_worker.go` | T06 |
| R7 | Handler 业务错误没有接入 ActionReporter | `modules/trade/internal/eventconsumer/jetstream.go`、`packages/jetstream/runner.go` | T07 |
| D1 | 组合总权益计算数量，却固定在首个可用账户执行，甚至搬移原有同向仓位 | `application/target/weight_resolver.go`、`executor.go` | T08 |
| D2 | 现有下单接口实际为人工接管，不是独立普通提交 | `application/operator/service.go`、`rpc/execution.go` | T09 |
| D3 | quantity/weight、runner/instance、冻结/动态路由文档与实现并存 | `modules/trade/DESIGN.md`、`README.md`、Trade/Strategy proto 与 Web API | T10 |

OKX 协议依据：[Orders channel 官方文档](https://www.okx.com/docs-v5#order-book-trading-trade-ws-order-channel)。`fee/feeCcy` 是订单累计字段，`fillFee/fillFeeCcy` 是当前成交字段；不得把两组字段混用。

### 2.1 已有验证与缺口

审查轮执行过 Trade 全量测试、核心 race 测试，以及 Strategy/events 测试，均通过；这不是下面任务的验收结果。仓库外临时测试分别复现了现代 Resume、重复 Claim 清目标、OKX 累计费用误用。临时文件不是可依赖的项目资产，T01 必须把必要用例正式纳入仓库。

当前缺少的关键跨层用例是：现代授权到实际成交、人工请求未知后重启恢复、真实字段 WS/REST 重放一致、超过十万 Fill、两模拟账户故障隔离、正常过期后的健康、生产 bootstrap 的实际撮合决策。

## 三、执行组织与命令约定

- 仓库根目录记为 `ROOT=/Users/mooyang/Documents/go/src/github.com/mooyang-code/moox`。下文路径相对 ROOT；这是文件清单，不是要求新建同名目录树。
- 未另行说明的 Go 命令从 ROOT 执行，使用具体模块路径；禁止把根目录 `go test ./...` 当作多模块验证。
- 每个任务依次执行：编写失败测试、确认失败原因、最小实现、定向测试、独立 review、提交；所有检查项起始未勾选。
- Proto 仅修改源文件并执行对应 Makefile；不得手改生成文件，不增加 `reserved` 或旧协议别名。
- Schema 任务同时更新严格结构校验和受控迁移：`CREATE TABLE IF NOT EXISTS` 不会给旧表补列或修改 CHECK。EXPIRED、SUBMIT_ORDER、ControlMode 的既有表变更必须在新结构校验前完成已知基线的一次性事务转换，保留行数、主外键和事实；未知结构仍拒绝。不得通过关闭结构校验或删除数据库让测试通过。
- 每个任务提交只包含自己的文件。执行前后检查 index；禁止 `git add .`、自动 stash、reset、清库或撤销其他人的修改。
- T02、T03、T04 可在 T01 后由不同 worker 并行；T05/T06 都涉及 Paper/Bootstrap，安排同一负责人或串行；T08/T09/T10 涉及协议与控制台，由主 Agent 集成，避免多方同时改同一 proto。
- 每个逻辑提交通过定向测试后按仓库规范提交/推送。多任务可以分批 PR，不要求把整个计划塞进一个大提交；不因推送冲突擅自强推或重写他人历史。

## 四、任务清单

### T01：固定基线与正式回归用例

**文件：**
- 修改测试：`modules/trade/internal/application/logicalaccount/service_test.go`
- 修改测试：`modules/trade/internal/application/operator/service_test.go`
- 修改测试：`modules/trade/internal/exchange/okx/account_events_test.go`
- 新增测试：`modules/trade/test/modern_target_execution_e2e_test.go`

- [ ] 记录当前 HEAD、工作区/index 文件列表、Trade/Strategy/EventBus 的实际构建命令；执行用独立工作区必须基于用户确认的当前代码，而不是丢弃未提交的现代 Strategy 改动。
- [ ] 把三个已复现问题写成正常包测试，不依赖 `/tmp`、已有服务、真实凭据或壁钟等待。测试名称固定为 `TestModernSessionTargetCanResume`、`TestIdempotentClaimSessionPreservesTarget`、`TestIdempotentRebindSessionPreservesTarget`、`TestOrdersChannelUsesPerFillFee`。
- [ ] 为人工链增加 `TestManualUnknownSubmissionRemainsRecoverable`，注入交易所 TransportUnknown，验证底层订单未知时 action 不得进入失败终态。
- [ ] 运行以下测试，确认在修复前分别因为 runner mismatch、目标消失、0.02/0.01 费用不符、action FAILED 而失败，不能接受初始化或编译失败充当红测。

```bash
go test -count=1 ./modules/trade/internal/application/logicalaccount -run 'TestModernSessionTargetCanResume|TestIdempotent.*SessionPreservesTarget'
go test -count=1 ./modules/trade/internal/application/operator -run TestManualUnknownSubmissionRemainsRecoverable
go test -count=1 ./modules/trade/internal/exchange/okx -run TestOrdersChannelUsesPerFillFee
```

- [ ] 记录红测结果后将这些用例与各自修复一起交付，避免把默认测试失败的中间提交作为阶段发布版本。

### T02：统一现代授权校验，修复 session 幂等

**文件：**
- 修改：`modules/trade/internal/infra/store/logical_account.go`
- 修改：`modules/trade/internal/application/logicalaccount/service.go`
- 修改：`modules/trade/internal/rpc/logical_account.go`
- 测试：对应 `service_test.go`、`rpc/logical_account_test.go`、`infra/store/session_target_test.go`
- 复核调用方：`modules/strategy/internal/bootstrap/logical_account.go`

- [ ] Store 的 Claim/Rebind 返回 `(fence string, changed bool, err error)`。首次建立/真实替换才返回 changed；相同身份与当前 fence 的重试返回原 fence、false；旧 fence 和旧身份仍冲突。
- [ ] Service 在同一事务内仅当 changed 时删除当前可执行目标。历史 receipt 保留；失败事务不得改变 owner、fence 或目标。
- [ ] readiness 对现代目标检查 instance/session 的完整相等关系，与 OrderService 的提交授权规则一致。过期目标不得因“持有旧目标”而伪装成可执行目标；允许组合恢复为 ACTIVE 等待下一条有效目标，不据此自动重新交易。
- [ ] 控制面重试不得因重新读取 fence 而撤销另一个新 session：完整身份匹配仍为必要条件。测试旧 release/rebind 的延迟到达。

```text
Claim/Rebind transaction:
  validate identity + expected fence
  mutate -> fence, changed
  if changed: delete current executable target
  commit
```

- [ ] 验收矩阵：相同认领不删目标、不转 fence；真实切换只清当前目标；失败 CAS 不改任何数据；现代目标 Resume 成功；旧 session 消息拒绝；PAUSED 接收目标不下单。
- [ ] 运行 `go test -count=1 ./modules/trade/internal/application/logicalaccount ./modules/trade/internal/infra/store ./modules/trade/internal/rpc ./modules/strategy/internal/bootstrap`，再运行相关包 `-race`；结果必须通过。
- [ ] 提交建议：`fix(trade): preserve targets across idempotent session claims`。

### T03：贯通人工操作与订单的不确定状态

**文件：**
- 修改：`modules/trade/internal/application/operator/service.go`、`cancel.go`
- 修改：`modules/trade/internal/runtime/operator_worker.go`
- 测试及健康语义复核：`modules/trade/internal/runtime/operator_worker_test.go`、`modules/trade/internal/health/state.go` 及对应测试
- 修改：`modules/trade/internal/rpc/execution.go`、`execution_test.go`
- 必要修改：`modules/trade/internal/application/order/service.go`、`infra/store/operator_action.go`
- 测试：上述对应测试；新增 `modules/trade/test/manual_unknown_recovery_e2e_test.go`

- [ ] `Place` 成功后，先把 child order ID 写入 action 的 `ResultJSON`，再发起 Submit。崩溃发生在这两次提交之间时，按原 client ID 找到同一订单并补关联，禁止生成新 ID。
- [ ] 复用现有 `RUNNING/COMPLETED/FAILED`，不为相同语义新增第二套订单状态。`COMPLETED` 只表示操作的提交阶段完成，不表示订单已成交。
- [ ] 按下表实现恢复分流；运行中临时错误写 last_error，但不调用通用 `failManualAction`。持久化必须有有界、可取消的操作上下文；进程停止后依靠 durable action 恢复，不能起无管理的重试 goroutine。

| child order 状态/事实 | action 行为 | 是否重新 POST |
| --- | --- | --- |
| PENDING，尚未成功发送 | RUNNING，重新核验权限/账户/风控后提交 | 允许，同一 client ID |
| SUBMITTING / SUBMIT_UNKNOWN | RUNNING，按 client ID 和成交查询 | 不允许盲重发 |
| 确认不存在且不确定窗口结束 | RUNNING，回到 PENDING 后受控提交 | 允许，同一 client ID |
| OPEN / PARTIALLY_FILLED / 已确认成交或交易所撤单 | 提交阶段 COMPLETED，返回实际订单状态 | 不允许 |
| 明确参数错误或交易所拒单 | FAILED，保留 order ID 和原因 | 不允许 |
| 临时行情/同步/账户不可用 | RUNNING，保留关联，等待恢复或显式人工取消 | 不允许绕过校验 |

- [ ] 相同 action ID 的重试返回同一个 action/order；人工响应可表达 RUNNING/UNKNOWN，不能引导客户端换 ID“再试一次”。错误或重启不得丢失已有 reservation；明确终态按现有订单事务释放。
- [ ] OperatorWorker 每个 action 的完整恢复调用使用有界、可取消 context，单 action 的行情/交易所失败或超时写入诊断后继续下一项，不把业务错误汇总成全局 Not Ready。公共存储读取/持久化失败、错误配置和 worker 退出仍影响健康，不能吞掉数据库错误。保持有界串行恢复，不为此新建工作流框架；测试 A 超时后 B 能推进、业务失败不拖低全局 readiness、公共 List/DB 失败仍不健康，并替换目前固化“任意恢复错误导致全局失败”的测试。
- [ ] 同步修改 `loadManualOrderResult` 和 RPC 错误映射：已有 durable RUNNING action 的 last_error 只用于诊断，正常读回时返回 nil application error、`RetInfo=0`，不能仅因 last_error 非空就表示“提交失败”。真实持久化/读取失败仍返回系统错误；尚未受理的校验失败、幂等冲突及明确 FAILED 保持对应错误语义。
- [ ] 关联 child order 后先恢复该订单，不能重新从暂停/撤单开始，把自己的已受理订单当作待清理挂单。若尚未关联，先按原 client ID 查找并校验完整 spec，再决定是否创建。
- [ ] 持久化绝对提交截止时间，避免依赖恢复后在任意未来时刻执行旧人工请求。A 阶段首次创建 action 的同一事务中，用 CreatedAt 加当时的默认窗口算出 `deadline_at`，写入 ResultJSON 进度；恢复只读取该绝对值，不按新配置重算。已有缺少 deadline 的 RUNNING action 一次性补值并持久化后再恢复。B 阶段允许接口显式指定 `deadline_at`；服务端生成值不加入 RequestJSON 幂等身份。默认窗口值作为执行前配置决策记录，并测试修改配置后重启仍不改变旧截止时间。
- [ ] ResultJSON 写入 order ID、错误明细或终态结果时必须保留同一 deadline，不用只含 OrderID 的新对象覆盖完整进度；同一 action 的并发 RPC/worker 更新受现有 action/组合锁保护。
- [ ] 截止时，尚未创建 child 可直接失败；已确认交易所不存在且回到 PENDING 的订单先 `DiscardPending` 释放预占，再失败。SUBMITTING/SUBMIT_UNKNOWN 继续 RUNNING，只查询、不重新 POST；查询证明已受理则完成提交阶段。不能把“超时”当成“交易所没有订单”，也不能把本地丢弃的 CANCELED 误判为提交成功。
- [ ] 顺带覆盖已确认的撤单竞态：Cancel 返回错误后重新读取订单，若私有流已推进为 FILLED/CANCELED，返回权威终态；不再对终态执行 `MarkCancelUnknown/CancelRejected`。
- [ ] 测试四个故障点：Place 后、关联后、POST 后响应丢失、后台查到 absent 后重启；分别断言已收单不重复、未收单在截止前可继续、截止后不发新单、同 ID 不变、确定终态后资金占用释放。补“交易所已 ACK，仅后置账户同步失败”的用例，action 仍应完成。
- [ ] 运行 `go test -count=1 ./modules/trade/internal/application/operator ./modules/trade/internal/application/order ./modules/trade/internal/runtime ./modules/trade/internal/rpc ./modules/trade/test`；再对前面三个包运行 `-race`。
- [ ] 提交建议：`fix(trade): recover uncertain manual order submissions`。

### T04：修正 OKX 单笔费用并验证 WS/REST 事实一致性

**文件：**
- 修改：`modules/trade/internal/exchange/okx/account_events.go`、`account_events_test.go`
- 测试：`modules/trade/internal/exchange/okx/okx_test.go`
- 新增：`modules/trade/test/okx_fill_replay_e2e_test.go`
- 复核不放宽：`modules/trade/internal/infra/store/fact.go` 的不可变 Fill 冲突检查

- [ ] WS Orders 结构显式接收 `fillFee/fillFeeCcy`，转入标准 Fill；REST fills 仍用该端点的 `fee/feeCcy`。不能采用“fillFee 为空则用累计 fee”的猜测式回退。
- [ ] 在标准 Fill 字段中校验本次成交费用完整性。收费、返佣、零费用均有明确归一规则；缺少必要字段时不得先落一笔猜测费用的不可变 Fill，再指望 REST 修正。
- [ ] 使用同时包含累计与单笔字段的真实结构 fixture：第一笔 fee=-0.01/fillFee=-0.01；第二笔 fee=-0.02/fillFee=-0.01。验证两笔费用合计 0.02，而不是 0.03。
- [ ] 覆盖 WS 先到、REST 先到、重复推送、多个部分成交、同 trade ID 内容冲突；费用币种、成交时间和手续费方向必须在两种来源间归一一致。已有错误历史 Fill 只输出诊断，不自动篡改交易事实。

```json
{
  "tradeId": "8",
  "fillSz": "2",
  "fillPx": "100",
  "fee": "-0.02",
  "feeCcy": "USDT",
  "fillFee": "-0.01",
  "fillFeeCcy": "USDT"
}
```

- [ ] 运行 `go test -count=1 ./modules/trade/internal/exchange/okx ./modules/trade/internal/application/consumer ./modules/trade/internal/application/accountsync ./modules/trade/test`；只有跨来源重放不重复记账且无 immutable replay 冲突才通过。
- [ ] 提交建议：`fix(trade): normalize OKX websocket per-fill fees`。

### T05：建立可重建的 Paper 余额投影

**文件：**
- 新增：`modules/trade/schema/paper_balance.sql`
- 新增：`modules/trade/internal/infra/store/paper_balance.go`、`paper_balance_test.go`
- 新增：`modules/trade/internal/infra/store/paper_balance_migration.go`
- 修改：`modules/trade/internal/infra/store/store.go`、`paper_simulation.go`
- 修改：`modules/trade/schema/schema.go`、`schema_test.go`
- 修改：`modules/trade/internal/application/consumer/fill.go`、`application/papersimulation/service.go`
- 修改：`modules/trade/internal/execution/paper/adapter.go`、`internal/bootstrap/bootstrap.go`
- 测试：`modules/trade/test/paper_matcher_restart_e2e_test.go`、`close_paper_simulation_e2e_test.go`

- [ ] 定义最小投影：每账户一行初始化元数据，每资产一行精确十进制总余额。不要把行情估值、可用资金、冻结资金再各存一套权威数据。新表加入 `validateExistingTradeSchema` 的 approved/reference schema，并验证第二次打开数据库仍成功。

```sql
CREATE TABLE IF NOT EXISTS t_paper_balance_states (
    c_space_id TEXT NOT NULL,
    c_trading_account_id TEXT NOT NULL,
    c_applied_fill_count INTEGER NOT NULL DEFAULT 0,
    c_initialized_at INTEGER NOT NULL,
    PRIMARY KEY (c_space_id, c_trading_account_id),
    FOREIGN KEY (c_space_id, c_trading_account_id)
        REFERENCES t_trading_accounts (c_space_id, c_trading_account_id)
);

CREATE TABLE IF NOT EXISTS t_paper_asset_balances (
    c_space_id TEXT NOT NULL,
    c_trading_account_id TEXT NOT NULL,
    c_asset TEXT NOT NULL,
    c_total TEXT NOT NULL,
    PRIMARY KEY (c_space_id, c_trading_account_id, c_asset),
    FOREIGN KEY (c_space_id, c_trading_account_id)
        REFERENCES t_paper_balance_states (c_space_id, c_trading_account_id)
);
```

- [ ] 新 Paper 创建账户、配置、初始余额投影在同一事务完成。已有 Paper 在允许撮合前，持账户锁、用单事务从初始资金和全部 Fill 重建；按稳定复合游标分页，不使用 OFFSET 作为长期游标，不以 100000 为总量上限。
- [ ] 重建中断必须整体回滚；初始化标记与结果一起提交。未初始化账户不可 Ready，不允许先截断余额运行。测试账户数据全部在临时 SQLite 中创建；正式库回填在发布阶段单独授权。
- [ ] 用稳定复合键 `(traded_at, fill_id)` 扫描事务内历史；`applied_fill_count` 只用于审计，不是按交易所时间过滤新 Fill 的水位。迟到 Fill 也必须增量入账；再次启动不覆盖已经初始化的投影。
- [ ] 每笔新 Paper Fill 的插入、订单状态、reservation 释放、持仓和余额投影在同一事务更新。只有 `InsertFill` 确认新增才修改投影；重复 Fill 不加计，冲突或任何中途错误全部回滚。禁止在已有 Tx 内调用会新建事务的 Store 方法。
- [ ] Spot：买入扣 quote、加 base；卖出相反；手续费按实际费用币种扣除。Swap：初始结算现金加已实现收益、扣费用；未实现收益和保证金仍由当前持仓/价格计算。所有金额用现有 Decimal，不用浮点数或 SQLite 浮点 SUM。
- [ ] `GetAccountSnapshot` 改读投影和活动 reservation，不再每次读取全部 Fill/全部终态订单。现有快照 watermark 与未反映 reservation 逻辑必须保留，避免预占计算两次。
- [ ] Close 只关闭执行并取消活动模拟订单，保留 Fill、余额投影与资金曲线；同事务取消并释放 reservation 后刷新最终快照，避免已关闭账户仍显示旧 locked。不得通过重建重新开启 CLOSED 账户。
- [ ] 验收：100001 笔历史、不同费币种、买卖回转、Swap 已实现收益、重复重放、事务失败、重启、关闭、重建与增量结果一致。增加查询计数断言：普通快照不调用全历史 ListFills，不用墙钟性能阈值代替结构验证。
- [ ] 运行 `go test -count=1 ./modules/trade/schema ./modules/trade/internal/infra/store ./modules/trade/internal/application/consumer ./modules/trade/internal/application/papersimulation ./modules/trade/internal/bootstrap ./modules/trade/internal/execution/paper ./modules/trade/test`，并对所有 schema 执行内存 SQLite 载入与外键检查。
- [ ] 提交建议：`fix(trade): maintain complete paper balance projections`。

### T06：隔离账户故障，拆开目标状态与 worker 健康

**文件：**
- 修改：`modules/trade/internal/execution/paper/matcher.go`、`runtime/paper_matcher_worker.go`、`runtime/session.go`
- 修改：`modules/trade/internal/application/target/executor.go`、`runtime/target_worker.go`
- 修改及测试：`modules/trade/internal/eventconsumer/jetstream.go`、`target.go` 及对应测试
- 修改：`modules/trade/internal/infra/store/target.go`、`schema/logical_account.sql`、`internal/health/state.go`
- 修改：`modules/trade/internal/bootstrap/bootstrap.go`
- 新增：`modules/trade/internal/execution/paper/decider.go`、`decider_test.go`
- 新增：`modules/trade/test/paper_account_isolation_e2e_test.go`

- [ ] 从 bootstrap 移出真实 `DecideContext` 逻辑到 `paper.Decider`，注入 Store、adapter/行情获取与 Now。Bootstrap 只组装依赖；生产和测试使用相同决策实现，不再在 parity 测试中复制另一套简化撮合规则。
- [ ] Paper Scan 按候选隔离 adapter、行情和配置错误，记录账户/订单及错误分类后继续其他账户。订单版本变化、同时撤单、账户关闭视为失效候选，不视为全局故障。
- [ ] 每个候选的外部调用设置有界 timeout；保持一轮串行即可，不为优化先引入并行撮合。不可仅用 session Ready 过滤候选，因为 Paper Ready 本身依赖 matcher，必须避免启动循环依赖。
- [ ] 区分 worker 存活/数据库可用与账户业务错误。matcher 已启动且公共持久化可用时可服务健康账户；某账户的失败只使该账户不可交易。worker 退出必须立刻不健康，不得只修成永远返回 true。
- [ ] 目标增加持久化 `EXPIRED`；到期使用当前 target ID/session 的 CAS 更新，旧扫描不能覆盖新目标。EXPIRED 不再参与正常收敛扫描；已有 Order/Fill 继续由账户同步处理，新目标仍可覆盖它。
- [ ] 撤销 consumer 与全量 worker 共用的 `targetGate`，保留现有组合账户锁。验收“A 的行情阻塞时，B 可以持久化受理目标”；不承诺单串行执行器下 B 完全不受执行排队影响。耗时外部调用有界，按 ID 唤醒优化放在此项的最后。
- [ ] 接收端复用 `RunnerConfig.IndependentBatch`，并发上限沿用有界 BatchSize，同一组合仍串行。当前默认整批串行且受理前同步估值，仅删除 targetGate 不能满足账户隔离。设置 handler 总时限及小于 AckWait 的 InProgressInterval；阻塞 A 的 Resolver 时，测试同批 B 在 A 释放前已持久化 receipt/target。后续批次仍需等待本批结束，不把批内并行描述成无排队；超时必须释放任务和锁。
- [ ] 锁顺序统一为组合账户、组合执行、执行账户；禁止在持有执行账户锁时回调获取组合锁。复核 AccountsSync/FactsObserver 的 wake 必须在释放账户锁后发生。
- [ ] 验收：A 永久失败、B 仍成交；坏候选不饿死健康候选；目标自然过期不新发单/不撤单/不清仓，实例仍健康；新目标并发到达不被过期 CAS 覆盖；真实 worker 退出可检测。
- [ ] 运行 `go test -race -count=1 ./modules/trade/internal/runtime ./modules/trade/internal/execution/paper ./modules/trade/internal/application/target ./modules/trade/internal/health ./modules/trade/internal/eventconsumer ./modules/trade/test`。
- [ ] 提交建议分为 `refactor(trade): share the production paper match decider` 与 `fix(trade): isolate account failures and expired targets`。

### T07：补齐目标受理与拒绝的最小可观测闭环

**文件：**
- 修改：`modules/trade/internal/eventconsumer/jetstream.go`、`target.go`
- 新增：`modules/trade/internal/eventconsumer/reporter.go`、`reporter_test.go`
- 修改：`modules/trade/internal/telemetry/metrics.go`
- 测试：`modules/trade/internal/eventconsumer/target_test.go`、`modules/strategy/internal/outbox/relay_test.go`
- 后续展示：`web/src/views/strategy/components/strategy-status-badge.vue`、`strategy-run-timeline.vue`

- [ ] 配置 `ActionReporter`，同时记录 handler 的业务结果与实际 ACK/NAK/TERM 动作是否成功。ErrorReporter 继续用于传输和运行错误，不把二者混为一谈。
- [ ] 结构化日志含 space、target、logical account、instance/session、decision、error_code、trace；解析失败时只记录可信 envelope/transport 信息，不输出完整 payload、签名、凭据或无限长度错误。
- [ ] 指标标签限定低基数 `decision/error_code`；target/account ID 只进日志和查询，不作为 Prometheus label。重试、旧周期被替代、过期、身份错误、永久业务拒绝分别可区分。
- [ ] Reporter 观察失败不能改变已经执行的消息动作。成功受理仍以现有 receipt 与目标原子提交为准，不能把“写了日志”当成受理成功。
- [ ] 控制台明确 `sent=Broker 已确认`，不显示为“交易成功”。当前执行状态读取 Trade 的目标/订单事实；本轮拒绝原因通过结构化日志与明确错误入口追踪，不声称已有可靠的 Trade 回报事件。
- [ ] 验收：ACK、TERM、RETRY/NAK 的业务原因均可观测；同目标重复投递不生成新订单；日志脱敏与长度上限；Reporter panic/失败不影响消息控制流。
- [ ] 运行 `go test -count=1 ./modules/trade/internal/eventconsumer ./packages/jetstream ./modules/strategy/internal/outbox`。
- [ ] 提交建议：`fix(trade): report target delivery decisions explicitly`。

### T08：分离目标估值与多账户执行路由

**前置：** T02、T05、T06 完成，用户确认保留多账户动态路由，而不是收缩为一对一账户。

**文件：**
- 修改：`modules/trade/internal/application/target/weight_resolver.go`、`executor.go` 及对应测试
- 修改：`modules/trade/internal/infra/store/target.go`、`target_receipt.go`
- 修改：`modules/trade/test/strategy_target_e2e_test.go`、`live_paper_unified_execution_e2e_test.go`

- [ ] `InstrumentTarget` 只表达规范化 instrument 和总 quantity；参考价格证据保留报价账户、原生 symbol 和时间，但它们不再构成执行账户 pin。
- [ ] 去重重投只读取原 receipt，不重新估值；执行重试和路由改变也不得改变原目标 quantity。禁止为了换账户而生成“同 target ID、不同估值”的回执。
- [ ] 收敛时先处理反向物理仓位，再按组合总量增减；同向已存在于 B 的仓位可以直接贡献目标，不因 A 优先级更高而先平 B 再开 A。
- [ ] 加仓按 priority、可用资金、最小量、步长和合约乘数选择账户；可拆到多个成员。A 容量不足或不满足最小量时继续 B；未知/未映射敞口仍 fail closed，不能作为可忽略账户跳过。
- [ ] 减仓在持仓所属账户执行，不用不同账户的相反仓位净额抵消宣告完成。一个组合一次最多一个新子订单，等待实际成交/账户事实重算，不同时创建互相基于旧快照的重复订单。
- [ ] 阶段切换不得把已有 pinned 目标静默解钉并立即交易。受控切换时暂停相关组合，保存旧 receipt 供审计，废止旧可执行目标，等待新 session/新 target；是否撤既有订单仍需显式操作。最终不保留第二套 pinned 新目标入口。

```text
equity=200, reference_price=100, weight=1 -> total_target=2
A: available=0, position=0
B: available=100, position=1
expected: buy 1 on B; no sell on B; receipt quantity remains 2
```

- [ ] 验收上述例子以及 A/B 分摊容量、相反仓位、零目标、最小量残差、同 target 重投、成员变动、市场报价短暂失败。将原“新目标必须搬仓到冻结账户”的测试替换为新合同，不删掉测试后不补等价覆盖。
- [ ] 运行 `go test -count=1 ./modules/trade/internal/application/target ./modules/trade/internal/infra/store ./modules/trade/test`。
- [ ] 提交建议：`refactor(trade): separate target valuation from execution routing`。

### T09：增加有明确归属边界的普通订单提交

**前置：** T03 完成，用户确认组合账户显式区分 `MANUAL/STRATEGY` 控制模式，普通提交只用于 MANUAL。

**文件：**
- 修改：`modules/trade/proto/trade_service.proto`、生成目录 `modules/trade/proto/tradegen/`
- 修改：`modules/trade/internal/rpc/execution.go`、`console.go`、`register.go`
- 新增：`modules/trade/internal/application/operator/submission.go`、`submission_test.go`
- 修改：`modules/trade/internal/application/operator/service.go`、`internal/domain/operator/action.go`、`schema/logical_account.sql`
- 修改：`modules/trade/internal/domain/logicalaccount/account.go`、`application/logicalaccount/service.go`、`application/papersimulation/service.go`
- 修改：`modules/trade/internal/infra/store/logical_account.go`、`store.go`、`internal/rpc/logical_account.go`、`convert.go`
- 修改及测试：`modules/trade/internal/eventconsumer/target.go`、`internal/infra/store/target.go`、`target_receipt.go`、`internal/application/target/executor.go`、`internal/application/order/service.go` 及对应测试
- 修改：`modules/trade/internal/runtime/operator_worker.go`、`infra/store/operator_action.go`
- 修改：`web/src/api/trade/index.ts`、`types.ts`、`trade.test.ts`

- [ ] 复用 OperatorAction 的持久化与恢复能力，新增 action type `SUBMIT_ORDER`；Order owner 仍由服务端设为 OPERATOR。不引入独立的订单提交数据库或后台进程。
- [ ] 新增持久化 `ControlMode=MANUAL/STRATEGY`，与 Live/Paper、ACTIVE/PAUSED 两个维度分开。已有组合明确迁移为 STRATEGY，不根据 owner 是否为空猜测；创建组合/Paper 时允许显式选择 MANUAL。同步 schema CHECK、Store 校验、RPC 转换与契约测试。
- [ ] 在 Store 严格结构校验前执行已知基线的一次性事务升级：无损增加 `c_control_mode DEFAULT 'STRATEGY'`，重建带新 action type CHECK 的 `t_operator_actions` 并保留所有行、索引和关联。测试包含历史 Order/Fill/OperatorAction 的旧库升级、再次打开，以及新 SUBMIT_ORDER 插入；不只验证空库。
- [ ] 新 `SubmitOrder` 请求使用 action ID、logical account ID、client order ID、trading account ID、instrument、方向、position_side、数量、类型、FillPolicy、限价、reason、绝对 `deadline_at`；不接受 owner、instance、session、reduce-only 等可信字段。position_side 沿用现有 ClientOrderSpec：SPOT 必须 UNSPECIFIED，SWAP 必须 NET，拒绝其他组合，不新增双向持仓模式。Paper 和 Live mock 都覆盖 SPOT/SWAP 正负例，避免新接口省略字段造成所有 SWAP 单校验失败。`PlaceManualOrder` 同步支持 deadline，省略时沿用 T03 的持久化默认截止时间。
- [ ] 组合锁内校验 MANUAL 模式、当前启用成员关系及账户可执行条件，持久化关联订单后返回受理结果。普通接口不调用 Pause，不撤其他订单；STRATEGY 账户在产生 action/order/预占副作用之前拒绝，提示使用显式人工接管接口。
- [ ] MANUAL 账户拒绝策略 Claim/Rebind、自动 Resume 和目标事件；readiness 不要求策略 owner/target。允许多个活动人工单，资金预占继续由现有执行账户锁串行保护。本轮控制模式创建后不可直接修改；后续切换功能必须另做暂停、清理活动 action/order 和身份重建，不能复用“owner 为空”绕过边界。
- [ ] 控制模式校验贯穿 Consumer 估值前、Store 原子受理与 receipt 回放前、Executor 收敛和 OrderService 实际提交前；不能只在 Claim/RPC 检查。分别测试直接调用 Store、重放旧 receipt 和恢复已存在的 PENDING 目标订单均不能绕过 MANUAL 边界。读取账户失败保留可重试的基础设施错误，只把真实模式冲突归类为永久授权拒绝。
- [ ] 新 SubmitOrder RPC 不等待交易所 POST 或最终成交，也不触发撤单；必要的行情与本地风控检查有超时。响应含 action、order ID 和实际受理状态，`RetInfo=0` 只说明成功受理。正常初次返回 RUNNING/PENDING；后台已推进时返回实际状态。ACCEPTED 仅是本文的受理描述，不增加同名 action/order 状态。
- [ ] 同时覆盖锁等待的响应边界：当前 `infra/store/store.go` 的组合锁和执行账户锁使用 `sync.Mutex.Lock`，仅设置 RPC/行情 context 不能取消排队。普通受理路径使用可取消的同一锁注册表，不能另建一套不互斥的锁；测试持锁时请求超时返回、无 action/order/预占写入，以及释放后后续请求成功。不在持锁等待期间启动遗留 goroutine。
- [ ] 上述快速响应要求不改变显式接管的顺序：PlaceManualOrder 仍须先暂停并确认策略订单已撤销，再创建 child。取消尚未确认时 action 可以 RUNNING 且没有 order ID，不承诺两种接口都能立即返回 child；未知恢复继续遵守 T03。

```text
SubmitOrder -> validate ownership + risk -> durable action/order -> RetInfo=0 + current state
OperatorWorker -> shared OrderService.Submit -> query-first recovery
PlaceManualOrder -> explicit pause/cancel takeover -> same OrderService
GetOrder / GetOperatorAction -> current facts, never infer fill from RPC success
```

- [ ] 验收：MANUAL Paper/Live mock 账户可以提交且不撤其他人工单；无人认领的 STRATEGY 账户仍被拒且状态不变；MANUAL 无法被策略认领；跨 space、禁用账户和伪造 ownership 被拒；同 action/client ID 重试稳定；同 key 异参冲突；响应前后任意崩溃不丢已受理订单。
- [ ] 生成 `make -C modules/trade/proto all`；运行 `go test -count=1 ./modules/trade/... ./modules/trade/proto/tradegen ./modules/gateway/...`。tradegen 是嵌套 Go module，必须单独包含其 validation/security 契约测试；Gateway 保持透明转发，不硬编码另一套业务规则。仅使用现有签名 Space 路由 `/api/admin/trade_console/SubmitOrder`，目标环境若收紧了 gateway_methods allowlist，部署时显式加入新方法，不另开旁路端口。
- [ ] 从 ROOT 执行 `pnpm -C web test -- src/api/trade/trade.test.ts`。
- [ ] 提交建议：`feat(trade): add durable standalone order submission`。

### T10：收敛协议、控制台与现役文档

**文件：**
- 修改：`packages/tradeeventpb/trade_events.proto` 及生成文件；`packages/events/registry.go`、`validation.go` 和相关测试
- 修改及测试：`modules/eventbus/config/app.yaml`、`modules/eventbus/internal/config/config_test.go`、`scripts/test/contract/test-docs-architecture.sh`
- 修改：`modules/trade/proto/trade_service.proto`、`internal/rpc/convert.go`、`logical_account.go`、`internal/domain/order/spec.go`
- 协同修改：`modules/strategy/internal/trigger/processor.go`、`processor_test.go`、`modern_test.go`、`internal/store/results.go`、`internal/bootstrap/logical_account.go`、`internal/rpc/service.go`、`proto/strategy.proto` 及对应生成文件/测试
- 修改 Web：`web/src/api/strategy.ts`、`strategy-types.ts`、`api/trade/types.ts`、`views/trading/logical-accounts/index.vue`、`views/trading/account-workbench/index.vue` 及对应测试
- 修改文档：`modules/trade/DESIGN.md`、`modules/trade/README.md`、`docs/交易模块架构设计.md`、`docs/交易模块功能说明.md`、`docs/运维/MooX-Trade运维.md`
- 对齐不重写：`docs/策略执行框架设计.md`、`docs/策略模块架构设计.md`

- [ ] 枚举所有 producer、consumer、RPC 和 Web 使用者，先把仍在使用的 UI/管理调用切到 instance/session，再删除旧自动执行 producer/consumer 分支。不得只删 proto 导致运行中的 Strategy 发布无人消费的消息。
- [ ] 以本次复核基线的 `trigger/processor.go:marshalTargetEvent` 作为现代生产入口，追踪求值结果持久化及 Outbox；`store/results.go:marshalLegacyTargetEvent` 是旧适配入口，不再把它误当唯一 producer。T01/T11 的现代 E2E 必须从实际 Processor 产出事件，验证 instance/session、时间窗和 FULL 目标，而非测试手工组装等价 payload。
- [ ] 最终自动交易事件只接受完整现代身份、bar_end_time、effective_at、valid_until 与 target_weight；删掉旧数量事件执行入口、legacy owner generation/command sequence 的授权 fallback。历史结果和交易事实不因此删除。
- [ ] 本任务只处理执行边界及直接调用方，不重写 Strategy DSL、因子、选股或调度实现。Strategy 同时在修改时先对齐实际契约；无法闭合直接调用链则停止该切换，不把半完成接口作为已交付版本。
- [ ] 不添加 `reserved`、别名、长期双写/双读兼容层。需要保留的历史审计字段明确为只读来源，不参与新授权或新目标生成。已有 legacy owner 必须通过受控停用/重新授权切换，不能猜测 session。
- [ ] 同步清理 EventBus 配置和架构契约脚本的旧 quantity subject/事件名。发布时核查实际 MOOX_TRADE stream、consumer filter 和发布 ACL，现代 weight subject 必须保留；停止旧 producer 后先记录旧积压的数量和处置决策，再受控更新 subject。禁止删除并重建生产 stream，也不能认为改本地 YAML 就已修改远端。旧事件负例、现代发布/消费正例和已有消息保留均纳入验收。
- [ ] 控制台保留现有订单、成交、持仓、资金曲线和账户信息；只调整身份字段、受理/未知/过期状态及新提交入口，不扩大成 UI 重设计。
- [ ] 创建账户时展示控制模式；MANUAL 显示“下单”，STRATEGY 显示“接管并下单”及暂停/撤单警告。RUNNING action 即使有 transient last_error 也继续查询，不把临时错误显示为不可恢复失败。
- [ ] 更新文档：权重外部合同、数量内部合同、动态路由、人工接管/普通提交差异、Paper 仿真边界、停止/过期/清仓语义，以及错误定位字段。

```bash
make -C packages/tradeeventpb all
make -C modules/trade/proto all
make -C modules/strategy/proto all
go test -count=1 ./packages/events/... ./packages/tradeeventpb/... ./modules/trade/... ./modules/trade/proto/tradegen ./modules/strategy/... ./modules/strategy/proto/strategygen
pnpm -C web test -- src/api/trade/trade.test.ts src/views/trading/logical-accounts/logical-account-contract.test.ts src/views/trading/account-workbench/account-workbench.test.ts
pnpm -C web exec vue-tsc --noEmit
```

- [ ] 验收必须包含从真实 Web API 形状构造现代请求；不得由测试直接写 DB owner/target 来绕过管理接口。旧协议负例应明确拒绝，正常现代目标应完成整个执行链。
- [ ] 分别提交协议/调用方与文档；每次可交付边界必须生产者和消费者匹配，不单独发布破坏性 proto 半成品。

### T11：生产接线级验收、审查与交付

**文件：**
- 新增/完善：`modules/trade/test/modern_target_execution_e2e_test.go`、`manual_unknown_recovery_e2e_test.go`、`okx_fill_replay_e2e_test.go`、`paper_account_isolation_e2e_test.go`
- 修改：`modules/strategy/test/strategy_trade_external_e2e_test.go`、`modules/trade/test/strategy_target_external_e2e_test.go`
- 修改：`scripts/test/e2e/test-strategy-trade-event-e2e.sh`、`test-strategy-trade-logical-account-e2e.sh`
- 修改/新增浏览器验收：`web/tests/strategy-console.spec.ts`、`web/tests/trade-execution.spec.ts`（新增）

- [ ] 在临时 SQLite、隔离 NATS、本地 HTTP/WS 交易所替身中执行下列验收矩阵。替身只模拟外部边界，必须复用生产 OrderService、Decider、Reducer、Consumer/Worker 组装，不能重写内部执行逻辑来证明自己正确。

| 场景 | 必须观察到的结果 |
| --- | --- |
| 现代策略到 Paper 成交 | Claim -> Result/Outbox -> NATS -> Receipt/目标 -> Resume -> Order -> Fill -> 持仓和余额 |
| 重复消息与乱序 | 同目标不重新估值/不重复下单；旧 bar/session 不覆盖新目标 |
| Claim/Rebind 重试 | 目标、receipt、fence 保持幂等；真实换 session 才废止旧执行目标 |
| FULL/hold/有效期 | 遗漏/空目标归零；hold 保留；过期不发新单、不清仓、健康不被拖低 |
| 人工/普通下单 | 两入口权限和暂停语义不同，但同一订单内核和恢复链 |
| 未知提交与重启 | 收到单只恢复；未收到单受控重试；action/order ID 不变，无幽灵预占 |
| OKX 多次成交与 REST 补偿 | 每 trade ID 一次、费用正确、不可变事实一致 |
| Paper 大历史与事务恢复 | 100001 笔完整；增量/重建一致；中途失败不半更新 |
| 多账户容量与故障 | 不无故搬同向仓；健康账户不被坏候选永久阻塞 |
| Paper 关闭与隔离 | 关闭后不撮合；历史可查；Live/Paper 不串账；跨 space 请求拒绝 |

- [ ] 执行本地后端验收：

```bash
env MOOX_RUN_REAL_TRADE_DNS_E2E=0 go test -count=1 ./modules/trade/... ./modules/trade/proto/tradegen ./modules/strategy/... ./modules/strategy/proto/strategygen ./packages/events/... ./packages/tradeeventpb/...
go test -race -count=1 ./modules/trade/internal/runtime ./modules/trade/internal/application/... ./modules/trade/internal/eventconsumer ./modules/trade/internal/execution/paper ./modules/trade/internal/infra/store
go vet ./modules/trade/... ./modules/trade/proto/tradegen ./modules/strategy/... ./modules/strategy/proto/strategygen ./packages/events/... ./packages/tradeeventpb/...
bash scripts/test/e2e/test-strategy-trade-event-e2e.sh
bash scripts/test/e2e/test-strategy-trade-logical-account-e2e.sh
make verify-pr
git diff --check
```

- [ ] 修订 E2E shell 的成功断言，必须确实跑到现代身份与目标成交用例，不能仅凭旧 Runner 用例通过或测试因 tag/环境 SKIP 判定成功。隔离服务必须清理，不连接现有生产 NATS。
- [ ] 执行前端单测、类型检查、`pnpm -C web build:prod` 和 Playwright；验证提交返回受理而非成交、未知态可追踪、过期可见、拒绝不显示成交成功。
- [ ] 使用隔离配置运行 `pnpm -C web exec playwright test tests/strategy-console.spec.ts tests/trade-execution.spec.ts`；提前确认测试服务、数据库和 API 均为本地替身，不把浏览器验收指向生产账户。
- [ ] 安排两个独立 `codeCR`：一个审查订单/资金/恢复，另一个审查目标/session/API；主 Agent 核验所有发现并闭环，不以测试通过代替资金语义审查。
- [ ] 区分记录：源码检查、定向测试、mock E2E、隔离 NATS E2E、真实 Testnet、部署验收。前三类通过不能声称真实交易所已验证。

## 五、发布与数据保护门禁

- [ ] 阶段 A 可以先独立发布；阶段 B 的破坏性契约切换必须协调 Trade、Strategy 和 Web，先停止新的策略发布/执行，再切换，不能要求长期双协议共存。
- [ ] 发布前确定实际数据库路径和备份方式；保留一致性备份、旧二进制及验证记录。只对用户确认的目标环境操作，禁止在本计划中填入凭据或默认执行生产命令。
- [ ] Paper 投影在新 schema 初始化或专用受控回填流程中完整构建；遇到无法解析的历史金额/标的映射时报告并阻止该账户交易，不默认零值，不丢弃记录继续。
- [ ] 核验 schema 升级和降级门禁：旧二进制会把新增投影表视为不兼容，不可假定直接回退 binary 就能启动。迁移是一次受控的数据转换，不保留长期双 schema 运行分支。
- [ ] 旧 pinned/current target 或 legacy 授权的切换先暂停、保留历史 receipt，再完成新身份授权并等待新目标；不得通过批量 SQL 改现有目标路由后自动开仓。
- [ ] 回滚必须考虑升级后是否产生新订单/Fill：有新交易事实时不能简单恢复旧库快照。先暂停新动作并确认账户事实，再决定回滚应用或修复向前；任何历史事实修复单独审批。
- [ ] 真正部署前重新核验该节点的服务依赖和 schema。涉及嵌入式前端时按现行流程重新生成 statik、构建并只重启授权服务；本计划生成阶段不运行这些动作。
- [ ] Testnet 下单另需确认账户、secret ID、允许标的、最大名义金额与清理方案。PRODUCTION 开关不因测试被自动打开。

## 六、完成定义

- [ ] R1-R7 全部有回归测试及修复证据，D1-D3 的最终合同与代码、协议、控制台和文档一致。
- [ ] 自动事件、普通 SubmitOrder、人工接管均进入同一 OrderService；Paper 与 Live 共用订单/成交/恢复核心。
- [ ] 没有把持久化未知态当失败终态，没有重复副作用，没有截断历史余额，没有正常业务状态导致的全局假故障。
- [ ] 本计划全部已执行项有明确测试命令和结果；未执行的真实交易所/部署项明确标识未验证。
- [ ] 最终提交只包含本计划范围的文件；独立审查完成；本地提交、远端 SHA 与目标分支经过核验。

**当前交付仅为这份执行计划，所有实施检查项保持未勾选。**
