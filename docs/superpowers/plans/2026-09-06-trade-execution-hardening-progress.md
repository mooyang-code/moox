# Trade 执行与验收记录

## 完成标准

完整执行 `2026-09-06-trade-execution-hardening.md`，实施后新起 codeCR Agent 审查，完成全部修复及独立核验，再发布正式环境，用隔离模拟账户验证实际 Gateway/API、Strategy/Outbox/JetStream、Trade Worker、Paper 撮合和持久化查询链路。单测、mock 和本地 E2E 不替代正式环境验收。

不操作实盘资金，不修改现有策略目标，不删除既有数据库或交易事实；只创建本任务专用模拟账户和测试策略。发布前保护既有数据并核验真实服务与 schema。

## 基线

- 起点：`ab1d3385886595b270af17c30496d6fab6c45c64`，原分支 `feature/mooyang`。
- 执行分支：`feature/trade-execution-hardening`。
- 执行目录：`/Users/mooyang/Documents/go/src/github.com/mooyang-code/moox/.worktrees/trade-execution-hardening`。
- `.worktrees` 已验证被 Git 忽略；原工作区未切分支、未 stash、未撤销修改。
- 原工作区现有 tracked dirty diff 已复制到独立目录作为集成基线，保持未提交；这不等于纳入本次交付。涉及其文件时需重新核验原工作区最新改动，按本任务增量提交。
- 复制基线范围：`config/setup/service-deployments.yaml`，`docs/README.md`、`docs/SUMMARY.md`、选币策略计划及交易/策略架构文档，Factor go.mod/E2E，Storage server/test，Strategy 配置、bootstrap、compiler、manifest、results、trigger 相关文件，gatewayauth 实现/测试，以及 Factor runtime/E2E 脚本。

## 任务状态

| 任务 | 当前状态 | 证据/待办 |
| --- | --- | --- |
| T01 基线与红测 | 执行中 | 授权、人工 unknown、OKX费用行为红测已复现；现代目标全链E2E仍待T11集成 |
| T02 session 授权与幂等 | 已完成 | `839255b2`；独立 codeCR 和增量复核闭环，主 Agent race 复验通过 |
| T03 人工未知提交恢复 | 已完成 | `34e92f78`；durable恢复、错误身份/终态校验、deadline和Paper报价闭环；新起codeCR及主Agent复验通过 |
| T04 OKX 单笔成交费用 | 已完成 | `e2162efa`；signed cost、fillTime排序、不可变重放及真实Paper余额；新Agent/codeCR复核闭环 |
| T05 Paper 余额投影 | 已完成 | 完整历史与增量投影、原子资金校验/关闭、受控旧库迁移；新 codeCR 无剩余 P0-P2，主 Agent 全量/race 复验通过 |
| T06 故障隔离与过期 | 已完成 | 核心 `dcd9f491`；按 ID 定向唤醒随 T08 完成，独立 codeCR 与全量/相关 race/vet 闭环 |
| T07 消息结果可观测 | 已完成 | `1a941da7`；真实Runner、四包race、组件及生产构建通过；独立codeCR所有增量发现闭环 |
| T08 动态账户路由 | 已完成 | 总量与路由分离、共享真实容量、定向唤醒和旧 pin 安全切换；三个新 codeCR 无剩余 P0-P2，主 Agent 全量/race/vet 复验通过 |
| T09 普通 SubmitOrder | 未开始 | MANUAL/STRATEGY、持久化提交、完整接口/鉴权 |
| T10 契约与控制台 | 未开始 | 现代身份、删除旧执行分支、前后端与文档一致 |
| T11 完整验收与交付 | 未开始 | 全量/race/vet/协议/Web/隔离 NATS/新 Agent 审查/发布/正式 Paper E2E |

## 已执行验证

2026-09-06，在上述 worktree、复制基线后执行：

```bash
env MOOX_RUN_REAL_TRADE_DNS_E2E=0 go test -count=1 ./modules/trade/internal/application/logicalaccount ./modules/trade/internal/infra/store ./modules/trade/internal/rpc ./modules/strategy/internal/bootstrap
```

结果：四包 PASS。这仅是实施前基线，不是修复验收。

T02 正式红测：`TestModernSessionTargetCanResume` 因 `target runner does not own logical account` 失败；`TestIdempotentClaimSessionPreservesTarget`、`TestIdempotentRebindSessionPreservesTarget` 因目标被删除后 `record not found` 失败。没有用编译或环境错误充当业务红测。

### T02 完成证据

- 提交 `839255b2` 只包含 Trade 授权相关两个实现文件和两个测试文件，未包含用户基线修改。
- 三个指定红测转绿；另补现代无 target/残留 runner、过期元数据、PAUSED 不触达交易依赖、旧 fence/旧身份延迟管理请求、真实切换保留 receipt。
- 主 Agent 独立执行四目标包 `go test -race -count=1` 全部 PASS；独立 codeCR 执行四包普通/race 及 `go test -count=1 ./modules/trade/...` 全部 PASS。
- codeCR 的增量审查指出“Store 测试自行复制事务编排”不能防住 Service 回归。已新增 `TestSessionServiceRollsBackWhenTargetDeleteFails`，直接调用真实 ClaimSession/RebindSession，注入 DELETE 触发器错误后验证 owner/fence/target/receipt 全回滚，再移除触发器验证同请求成功。主 Agent 定向 race 和 codeCR 增量复核均通过，P2 闭环。
- 尚未据此声称正式环境已修复；真实发布和 Paper E2E 仍属于 T11。

### T05 实施前源代码核验

- 独立 explorer 与主 Agent 核验：真实 Paper Snapshot 在 `execution/paper/adapter.go`，不是另一套 `account_state.go` Rebuild；余额当前截断 100000 Fill，并把所有活动订单预占计入 locked。
- `OrderService.Place` 又对 PENDING/SUBMITTING/SUBMIT_UNKNOWN 无条件叠加 GetUnreflectedReservation，因而 T05 必须以专项红测覆盖快照已有 locked 的情况，保证一次预占只扣一次。计划已细化为 Paper 同事务读投影和活动 reservation，Live watermark 保持原合同；不能因机械保留旧实现而重扣。
- 投影初始化需覆盖底层 CreateTradingAccount 的 Paper 路径，不只 CreatePaperSimulation；增量入口必须覆盖所有真实 InsertFill 调用，只有新增 Fill 才更新，重复/冲突不加计。
- 实施前 Fee 合同是非负成本，RealizedPnL 是有符号数。T04 已将 Fee 收敛为有符号成本；T05 延续这一合同，负 Fee 按返佣增加对应资产，不能使用 Abs。

### T05 实施与审查证据

- 新增每账户初始化元数据（含初始化时间和审计计数）及每资产精确十进制总额。底层账户创建、配置、投影同事务；`InsertFill` 只有新增才更新余额，重复/冲突不增加计数，订单/持仓归并失败会一起回滚。
- 启动时以 `(traded_at, fill_id)` keyset 完整回填；实际 100002 笔含同时间跨页数据、较早时间迟到成交通过。初始化失败与第二笔历史金额损坏均整体回滚；再次打开不重置投影、时间或 Closed 状态。Spot 买卖回转、多费用资产、负费用和 Swap PnL 的增量结果与全历史重建逐资产一致。
- 主 Agent 真实 Paper 红测：快照已锁定 50000，第二笔只需 30000 却被拒；陈旧快照又使实际现金/保证金不足的新订单被放行。现改成事务内投影与全部活动预占校验，已有预占只算一次；Live 原同步水位逻辑不变。已同步 locked 的人工 PENDING 报价上升/下降/不足资金和自身 Submit 均覆盖。
- 新发现并红测修复冻结滑点资金问题：已冻结 1% 滑点但配置改为 0%，刷新后实际需保证金 100747.5，原先只预占 99750。现按同一个冻结执行价重算预占；资金不足时价格、预占、订单状态与调用次数不变。
- 非零 Swap 持仓 leverage=0 原先被当成 margin=0，可能继续放行订单。实际 Fill 后的红测已复现；Paper 估值现在严格校验数量、入场价和杠杆，无效事实明确拒绝。快照查询计数用例证明不调用历史 ListFills/全部订单查询。
- 旧测试夹具改成事实一致：Binance httptest 使用 LIVE TESTNET 身份；Paper 的初始 BTC 来自真实历史 Order/Fill，不再只写余额快照；预占不足通过真实 Place 创建另一笔订单，不通过修改派生现金伪造。
- 新起 `review_t05_core` 确认旧库缺新索引会在回填前被严格校验拒绝。已补缺索引的真实旧表红测和受限迁移：只给完整匹配已知旧形状的 Fill 表加索引，未知列/约束/索引仍拒绝且不变更。关闭时 Fill 已提交但后续刷新失败的 P2 已改为同事务使用当前持仓派生值；另覆盖旧持仓标价 t1、账户新报价 t2 的窗口，关闭估值时间保守回到 t1，未知时间保持 0，不伪造 fresh 报价。
- 资产合同 P2 已闭环：Swap 非空结算币必须等于账户结算币，非零正负 Fee 必须提供实际 FeeAsset，不再猜费用币种。真实 InsertFill 和旧库回填的错误场景都验证 fact/余额/count 一起回滚，正常异币手续费与返佣保留。
- 最终主 Agent `env MOOX_RUN_REAL_TRADE_DNS_E2E=0 go test -count=1 ./modules/trade/...` 全部通过；`order/paper/test` 整包 race 再次全部通过，索引/初始化/回填异常/roundtrip/重启小集 race 通过，相关 vet 和 diff-check 通过。Store/schema 普通全量含 100002 笔历史通过；Store/schema 全量 race 曾通过，最后索引/时间戳/资产合同增量均另有小集 race。
- `review_t05_core` 最终独立复核上述所有发现，结论为本阶段无剩余 P0-P2；独立 projection/index/asset/roundtrip/Close 定向测试通过。主 Agent 核验源码和测试覆盖后确认 T05 完成。既有 schema inspector 不枚举 trigger，已删除新增注释中不准确的 trigger 保证；这不等于生产库已经验收。
- 下一阶段为 T06，随后 T08-T11。T01 的现代完整事件链、全部协议/UI/最终新 Agent 审查、正式部署和真实 Paper 验收均未据此宣告完成。

### T06 实施与阶段审查

- 已将生产撮合决策提取为 `paper.Decider`，真实 Paper parity 和账户隔离测试复用该实现，不再用测试闭包复制简化业务规则。
- 已拆除目标 consumer 与全量 worker 共用的 Gate。真实 SQLite、Executor 和事件受理测试覆盖 A 行情阻塞时 B 仍可持久化目标；每候选外部调用有截止时间，仍保留单轮串行执行。
- 已增加目标 EXPIRED 状态及 target/session CAS，发起副作用前重新检查有效期。过期不自动撤销已发送订单，也不自动丢弃本地 PENDING 或释放其 reservation；后者等待新有效目标或显式人工操作处理，不把过期解释为交易所未收单。
- 订单层外部错误带账户身份，数据库、事务和成交归并失败保持基础设施错误。真实 OrderService 测试覆盖参考价成功但第二次 Paper 报价失败、明确拒单、未知提交及限流；单账户失败不再错误地拉低全局 Worker 健康。
- Paper Session 初始化改用现有 `SyncAccount`，使事实读取与事务应用共用账户锁；账户故障恢复带 generation，旧同步不能清除新撮合故障。真实并发测试覆盖初始化快照与成交/刷新交错。
- 首轮两个独立 codeCR 指出的固定候选上限、快照覆盖、Store deadline 误分类、订单级诊断缺失、目标过期错误吞掉持久化失败以及旧 target 结构预检问题均已补实现与回归用例。
- 增量审查另发现 keyset 分页丢失首次撮合优先级：大量健康 resting GTC 会延迟新单。已通过首次撮合/resting 两阶段分页修复，513 个慢 resting 订单加高排序新单的红测转绿，首次撮合转 resting 的订单同轮不重复执行。原 100001 候选故障隔离仍通过。
- 新增真实 Submit 内部依赖跨期及 SQLite 写入 SUBMITTING 后跨期红测。最终发送前再次查授权并检查当前时间，确定未发送的订单通过版本 CAS 回到 PENDING、SubmittedAt 清零而预占保留；回退 SQL 失败不被 EXPIRED 吞掉，不会继续发送。新增领域 AbortSubmit 区别于未知提交恢复。
- 真实 AccountSync 在 adapter 获取、订单/持仓/快照/成交及关联订单查询源头包装账户错误，再执行 readiness 落库；外部失败与 readiness SQL 失败同时发生仍保留共享错误分支。真实目标撤单到 AccountSync 再到 Worker 的集成测试验证两类健康结果。
- 20ms 撮合候选超时测试曾在全包 race 负载下使健康订单的 SQLite 事务超时。测试预算调整为 2s，保留阻塞直到 ctx.Done 和健康 B 真成交的断言；不是删除失败场景或仅重跑掩盖。调整后主 Agent Paper 整包 race 通过。
- 最新主 Agent Trade 全模块普通测试通过，相关 vet 和 diff-check 通过；最终两位独立 codeCR 与阶段 race 验收结果见本节末尾。
- 最终独立复核新增的实际 timeout 问题已红测修复：已调用交易所的 Submit/Cancel/RecoverCancel 在调用预算耗尽后，使用独立 3s 上下文持久化 unknown，保留预占；裸 context cancellation/deadline 也按不确定结果处理。确定尚未 POST 的回退和 AccountSync readiness 清理同样使用有界上下文，真实 SQL 失败仍是共享故障，不能因为调用方取消就伪造数据库不可用。
- 账户缺少 adapter 时现在也清除持久化 Ready，而非只返回账户错误。Paper Matcher 使用同一账户 mutex 的 TryLock，忙账户本轮全部候选延后；18 个忙账户候选跨页只报价一次，健康 B 成交，旧故障不会被跳过行为错误清除。真实 Session 快照期间订单仍 OPEN，同步释放后才可成交。
- 预加载行情标的同时缓存 canonical/native 映射，Binance/OKX 的原生 symbol 不再每轮重复加载全量 instruments；未知标的仍传播加载错误。Matcher 每轮清理已禁用账户的活动故障诊断，关闭账户不会留下永久健康告警。
- Manager 停止测试修正调度假设：父 context 会独立唤醒 Session 和 Manager，不能在仅观察 Session canceled 后假定 Manager 已执行 stopAll。改成等待 Manager gate 关闭，但仍在释放 Session shutdown 前断言，不把异步调度窗口当行为失败；该定向 race 连续 20 次通过。
- 成功响应与 caller deadline 同时发生也有完整覆盖：订单成功后的同步和 Executor 完成状态 CAS 使用有界收尾上下文。真实 Worker 撤单返回成功但候选超时的场景仍正常完成、不会虚报全局失败；测试观察预算覆盖实际 3s 收尾，而不是仍假设原 1s 候选窗口。
- Private OPEN/终态先于 HTTP 返回时保留权威事实，晚到的成功、拒绝或 unknown 不再倒退状态；15 种组合通过。成功响应与 private ExchangeOrderID 冲突仍失败关闭并保留原事实，错误属于对应账户。
- 整包 race 揭示已有 Flatten 测试的 20ms 截止时间可能在真实 SQLite 前耗尽，导致本应先有子单的断言失败；仅把测试改为 1s 截止、10s 重试间隔、5s 观察上限，仍断言 PARTIAL 和三次同步，不改生产 Flatten。真实 Paper 隔离测试的恢复步骤也按 worker 方式重试正常 deferred scan，每一轮仍必须无错误；两项并发 E2E 重复 race 五次通过。
- 最终 `review_t06_paper_final`、`review_t06_execution_final` 分别给出限定范围无剩余 P0/P1/P2。主 Agent 独立核验源码后，最终 Trade 全模块普通测试通过；runtime/target/order/accountsync/operator/health/eventconsumer/test 八包整包 race 全部通过，Paper 整包 race 通过，迁移定向及独立 Store 整包 race 通过，相关 vet/diff-check 通过。临时编译/预期红测/并发编辑前快照及已修测试时序失败未当作通过证据。
- 本阶段没有正式部署、生产下单或更改生产开关；后续 T08-T11 与完整正式 Paper 验收仍然必须执行。

### T08 实施与阶段审查

- 已完成总目标数量与报价来源证据分离，删除 executor 两处 pinned 分支；同向 B 持仓直接贡献总目标，不因 A 优先级搬仓。
- OrderService 新增只读 Capacity，复用原 Validator reservation、Paper 可执行报价与账本投影、Live 同步水位；最终 Place 仍在账户锁与事务内准入。精确有理数按 base step 向下取整，不复制简化资金公式。
- 主 Agent 正式红测复现：两账户各容量 1 时原执行器仍给 A 下数量 2；MaxChildNotional 小于一个 step 时原执行器反而放行整笔。当前两项已转绿。
- 新增真实 Paper 多账户集成测试：现代权重事件经真实估值转换为总数量 4，A/B 各有 100000 USDT、参考价 50000，先 A 买 2，实际 Fill 后再 B 买 2；活动 child 阻止重复预占，最终两笔成交、每账户余额与持仓正确，重投不改变 receipt。此测试不是生产 Broker 端到端证明。
- Store.Open 整体事务执行受控切换：严格识别旧 pin JSON，暂停受影响组合、清 owner 并更新 fence、废止旧 current target；历史 receipt、OPEN order、Fill 和余额保持原样。未知字段、重复键、半 pin、触发器/FK 依赖拒绝；失败回滚包含此前 DDL，不能留下半升级状态。
- 定向 wake 使用去重队列并保留周期全扫描；独立 codeCR 发现的“定向成功清除仍存在的全量错误”已红测修复，只由成功全量扫描恢复共享健康。
- 首轮 Trade 全模块普通测试通过；目标路由及真实 Paper 拆单定向 race 通过。最终审查与修复后回归结果后续补记。
- 容量审查发现 Live 真实余额拒绝后，本地旧快照会让下一轮继续选择 A。新增真实 Store + fake Live adapter 红测复现后，在确定 REJECTED 且余额不足时锁外刷新事实；本轮仅创建 A 一单，下一轮按新余额选择 B，B 的证明状态是 OPEN，不是成交。UNKNOWN、限流、其他拒绝不进入此路径；刷新失败与原拒绝都保留。
- 升级审查发现混合 pinned/unpinned 数组不属于已知写入形状，现两种排列均拒绝并保留原库。另复现“新 Claim + 新 target 在迁移 PAUSED 状态下自动撤旧 OPEN 单”：增加迁移暂停门禁，并让后续自动 PAUSED 原因更新保留该门禁，显式 ACTIVE 恢复才解除。跨重启 Store + Executor 回归先红后绿。
- 主 Agent 补查子单 notional 上限裁剪后的最小数量：减仓仍须满足合约乘数后的 base minimum，不能裁剪到无效量再交给 OrderService；对应行为红测已转绿。
- 已补相同 mutex 注册表上的可取消获取，Capacity、SyncAccount 三层锁以及 ResolveUnknown 同步尾链不再无界等待。锁超时独立完成 readiness 清理，不再抢被占锁；SQL 清理错误保留共享分类。三种锁超时、持久化 not-ready、释放后恢复，以及真实 Submit 到 AccountSync 的持锁截止时间场景均通过。
- 最终 `review_t08_wake`、`review_t08_cutover`、`review_t08_routing` 均无剩余 P0/P1/P2。主 Agent 最终 Trade 全模块普通测试通过；AccountSync/Order/Target/Runtime/EventConsumer/Test 六包整包 race 通过；Store 整包 race 为 342.568s，之后迁移门禁和 context lock 增量另做定向 race；Trade 全模块 vet 与 diff-check 通过。
- 本阶段没有正式部署或实盘操作。T09 普通提交、T10 契约/控制台及历史 receipt 迁移安全门禁、T11 全链生产虚拟账户验收仍未完成，不能据本阶段完成标记整个目标结束。
- 本阶段按三个逻辑提交交付：`870e4ea5` 共享容量与有界刷新；`94c1831e` 动态路由与旧目标安全切换；定向唤醒及本记录随 `feat(trade): wake accepted targets by logical account` 提交。目标分支仍为 `feature/trade-execution-hardening`，不合入或发布尚未完成的 T09-T11。

### T08 实施前核验记录

- 权重受理已有 receipt 命中不重新估值；当前 `WeightResolver` 同时把报价来源写入参考价证据和 `InstrumentTarget` 的执行 pin。后续只保留前者，不能连审计证据一起删除。
- Executor 非 pinned 分支已有反向物理仓位优先平仓、总量比较和实际持仓账户减仓，但容量失败换 B 的现有测试仅用 stub 拒单。它不证明真实容量拆单：A/B 各能买 1、总差额为 2 时，现有实现仍会分别尝试下 2 并失败。T08 必须增加真实 OrderService/Paper 资金与成交后的再次收敛测试。
- 删除 Go pin 字段会让旧 JSON 被静默忽略，从而把原 pinned 目标立即变成动态路由。切换必须在 workers 启动前暂停相关组合、废止旧 current target，保留 receipt、Order 和 Fill；等待新 session/新目标，不允许靠 JSON 解码的宽松行为完成迁移。
- 原工作区分支已有后续 Strategy 和 tradeeventpb 提交及未提交修改。T10 集成前必须重新核验当前契约，不能直接用执行 worktree 中较早的复制基线覆盖生产者。
- 发布前还须对历史 `rebuildLegacyTargetReceiptTable` 做完整 known-shape/扩展列、索引、trigger、外键依赖保护及事务测试。独立审查确认它不是 T06 新改动引入，但不能因此跳过 T10/T11 的历史事实保护门禁；现有 target 表保护不代表 receipt 表也已验收。

### T09 实施前接口定位

- 下一阶段必须同步 Domain/Store/Create/Paper Create/Proto 手写 validation/返回转换，以及 Claim/Rebind/Resume/Consumer/Executor/最终 Submit 的控制模式边界，不能仅在新 SubmitOrder HTTP 入口拦截。
- `modules/trade/proto/tradegen/validation.go` 是手写契约，不是 proto 自动生成文件；生成只使用当前 Makefile，不 clean 删除该文件；嵌套 tradegen module 要单独测试。
- 新 ControlMode 迁移不能复用跳过整个 CREATE TABLE SQL 比较的 `legacyLogicalAccountShapeMatches` 宽松逻辑；必须精确认识已知旧 CHECK/列/default，并保留旧 action、order、fill。该项与 T10 已列出的 receipt exact-shape/事务门禁一起在发布前完成。

## 发布调查

### 本轮实现与验证增量

- T03 基线行为红测位于独立 `/tmp/moox-t03-red-20260906` 的 `cc0cd408`；同名真实 OrderService 用例期望 RUNNING、实际 FAILED。原实现者遗留的两个测试进程句柄已不存在，主 Agent 重新执行五包普通测试及 operator/order/runtime race 均通过，不把缺失句柄当作通过证据。
- T03 新 codeCR 发现并正在闭环：client ID 冲突永久 RUNNING、unknown 撤单递归热循环、临时提交错误被空 Order ID 误判系统失败、无效 instrument 未终结、Cancel CAS 的终态竞态。另补已有不一致成员关系下 PENDING 的 fail-closed 校验；公开成员管理原本已禁止带未终态订单的迁移，不把该用例说成正常 API 可直接制造的漏洞。
- T04 两个协议红测为第二笔累计费用 0.02 被误当单笔 0.01、REST ts 与 WS fillTime 相差 1ms 导致不可变事实不一致。已改为 WS fillFee/REST fee 并统一 fillTime，费用使用有符号成本（正收费、负返佣）；Store canonicalization 与 Reducer 同步接纳负费用，不放宽相同 trade ID 不可变比较。
- Signed fee 新 Reducer 红测先因 `incomplete normalized Fill` 拒绝返佣，修复后 consumer/store 普通及 race 通过；非零返佣仍必须有费用币种。测试涵盖重复不入账及费用符号冲突拒绝。
- T07 真实隔离 NATS 的 RunTarget 业务 TERM 指标红测转绿；Go eventconsumer/jetstream/events/outbox race 通过。原始传输错误不再进入日志；可信 UTF-8 身份保持原值，超长或控制字符处理带摘要避免碰撞。
- T07 trace 是明确标记 `trace_source=event_id` 的稳定事件级关联 ID；仅存在真正 RPC metadata 时才标记 `rpc_metadata`。不声称已经实现跨服务 span 传播。业务验证失败通过 events 的 typed PayloadValidationError 与信封损坏区分。
- Web timeline 四个状态真实组件红测转绿，连同相邻操作组件共 6 个测试通过，`vue-tsc --noEmit` 和 `pnpm build:prod` 通过。构建仍有现有 Browserslist/Sass/大包警告，未顺手更新依赖。`sent` 显示“Broker 已确认”，不是成交。尚未完成浏览器及正式环境验收。
- T07 最后一项增量发现已补：业务 payload 不合法但 envelope 已完成校验时，保留 event/space/local trace，仍不记录未经验证的 target/account/session；真实 HandleTarget 到 reporter 的两种 decode-error 形态均覆盖，独立 codeCR 最终无剩余发现。
- T04 独立 codeCR 发现 REST billId 顺序不等于实际 fillTime，以及 canonical FillID 重放绕过跨订单关联冲突。均已有效红测后修复：按 fillTime/billId 稳定排序而保留 billId 游标；所有重复成交无条件比较 OrderID/ExchangeOrderID。真实 Reducer 跨订单同 trade ID 原来返回 nil，现返回冲突，第二订单不被修改。
- T03 已补陈旧报价错误类型；只对未曾 POST 的人工 PENDING 刷新服务端报价并同事务重算预占。报价上升/下降、其他订单预占导致资金不足、unknown 重试不得改旧报价均有测试。
- 新起 `review_t03_t04_final` 审查并闭环全部发现：永久错误代码持久化与正确 RPC 映射；旧 deadline 按持久化 CreatedAt 回填、新 action 同一时钟创建；Paper 固定成交价同步刷新并保留原滑点；九种终态损坏在应用层 fail closed 且 RPC 映射 INNER_ERR。最终阶段报告无剩余 P0-P2，不代替 T11 的全目标独立审查及生产验收。
- 最新主 Agent 验证：`go test -count=1 ./modules/trade/...` 全量通过；operator/order/consumer/store/runtime/rpc/test 七包整包 `-race` 通过；operator/order/consumer/store/okx/eventconsumer/events 的 `go vet` 通过。这仍不是正式环境或真实交易所验证。
- 最后终态校验调整后，主 Agent 再执行 `env MOOX_RUN_REAL_TRADE_DNS_E2E=0 go test -count=1 ./modules/trade/...` 全量及 operator/rpc 整包 race，均通过。下一步从 T05 完整余额投影推进，不跳过 T06、T08-T11；正式环境仍未发布。

### 既有只读发布调查

- 现行本地配置文件是原工作区的 `moox.toml`，不是旧记录中的 `custom.toml`。不复制或提交配置凭据。
- 配置确认 control/compile host 为 `106.53.107.122`，用户 `ubuntu`，control root 为 `/data/moox/prod`。远端实际有 Strategy/Web，但此目录没有 Trade binary/DB，不能向 control root 盲目发布 Trade。
- `dns_resolver.trade_node=compute-1`，对应 `43.132.204.177`。只读远端进程查询确认 Trade 实际运行在 `/home/ubuntu/moox/trade-move/bin/moox-trade -conf=config/trpc_go.yaml`；发布必须保留这一 dedicated-node 拓扑并核验 Gateway 路由。
- 直接访问该节点的 `http://127.0.0.1:11210/readyz` 返回 HTTP 401。此结果证明接口鉴权存在，不证明 readiness 失败；后续应使用正式鉴权入口查询。
- 远端配置只读确认 DB 为 `../data/trade/moox_trade.db`，`live_trading_enabled=false`。该库当前有 3 个 PAPER 账户、1 个 Order、1 个 Fill，没有查到 LIVE 账户；后续发布前必须再次核验，不能依赖此历史数量。
- 远端实际 schema 仍是 runner/command_sequence 版本：LogicalAccount 无 instance/session/fence 列，target 无现代时间字段，receipt 表不存在。T05/T09/T10 迁移验收须包含这个真实已知基线，并保留原订单/成交/账户；不能只覆盖开发库的现代 schema。
- 仓库已有 `scripts/build/build.sh trade` 和组件发布脚本，但不得默认全栈重启。
- 现役 Trade 运维文档仍包含“删除数据库/Stream”的旧 cutover 描述，与本计划数据保护约束冲突。T10 必须替换；本次不执行这些破坏式命令。
- 只读 explorer 确认：现有 CLI 没有通用 Trade RPC 调用命令；`setup deploy-service` 支持 dedicated Trade，但必须打完整且保留现行布局的服务包。激活后的 control-to-Trade 探活失败会停新服务并禁用注册，因此正式切换前须先验证路由/鉴权。
- 现有隔离 NATS/SQLite E2E 脚本不是生产验收入口，禁止把其中固定 Stream/runner 配置直接指向生产。T11 需新增显式 opt-in 的正式 API 验收入口或使用已鉴权浏览器操作专用测试资源，不直接注入数据库。

## 最终验收证据

尚无。必须记录最终源码 SHA、独立审查闭环、实际部署二进制 SHA/进程、正式隔离 Space/账户/策略标识、目标和订单/Fill 标识、余额/持仓断言、重启恢复和测试资源停用结果。任何一项缺失都不得标记目标完成。
