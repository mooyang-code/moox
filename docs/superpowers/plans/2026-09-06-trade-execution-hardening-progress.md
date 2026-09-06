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
| T01 基线与红测 | 已完成 | 基线、三类行为红测和现代全链用例均已落库；正式环境验收仍单列于T11 |
| T02 session 授权与幂等 | 已完成 | `839255b2`；独立 codeCR 和增量复核闭环，主 Agent race 复验通过 |
| T03 人工未知提交恢复 | 已完成 | `34e92f78`；durable恢复、错误身份/终态校验、deadline和Paper报价闭环；新起codeCR及主Agent复验通过 |
| T04 OKX 单笔成交费用 | 已完成 | `e2162efa`；signed cost、fillTime排序、不可变重放及真实Paper余额；新Agent/codeCR复核闭环 |
| T05 Paper 余额投影 | 已完成 | 完整历史与增量投影、原子资金校验/关闭、受控旧库迁移；新 codeCR 无剩余 P0-P2，主 Agent 全量/race 复验通过 |
| T06 故障隔离与过期 | 已完成 | 核心 `dcd9f491`；按 ID 定向唤醒随 T08 完成，独立 codeCR 与全量/相关 race/vet 闭环 |
| T07 消息结果可观测 | 已完成 | `1a941da7`；真实Runner、四包race、组件及生产构建通过；独立codeCR所有增量发现闭环 |
| T08 动态账户路由 | 已完成 | 总量与路由分离、共享真实容量、定向唤醒和旧 pin 安全切换；三个新 codeCR 无剩余 P0-P2，主 Agent 全量/race/vet 复验通过 |
| T09 普通 SubmitOrder | 已完成 | `33e0eb0b`；ControlMode、无损迁移、普通受理/恢复、proto/RPC/Web API，独立 codeCR 无剩余 P0-P2，主 Agent 全量/定向 race 复验通过 |
| T10 契约与控制台 | 已完成（源码/隔离验收） | 现代事件、Gateway 注册链、控制台、全类型 OperatorWorker 健康、实例恢复和旧自动执行路径清理已完成；正式节点 applied snapshot 仍须T11核验 |
| T11 完整验收与交付 | 执行中 | 本地全量/race/vet/协议/Web/隔离 NATS 和 Paper 已通过；正式 Trade 发布及 control-plane 接线受 SSH/凭据可达性阻塞，不能据此宣称完成 |

## 已执行验证

### T10 现代生产流水线集成

- 前一目标轮为计划文档复核，提交 `7c8e4fea`，没有发布；本轮用户恢复完整实施目标后继续编码，目标完成标准不变。
- 独立只读调查将原仓库 `c15d688b` 的 38 个 Strategy 文件与实施工作树逐一比较。8 个重叠 dirty 文件的既有行为已被主库提交吸收或修正，另补 `bootstrap/logical_account_test.go` 的 session 回归；仅移植这些已核实最终 blob，保留 scheduler/config 及其他模块复制基线。没有重新设计 DSL 或选股逻辑。
- 同时集成该提交直接依赖的 Admin Strategy source-ready ACL、Storage View 调用方权限和 Events 双侧 index provenance 校验及测试。39 + 5 个文件逐一 `git hash-object` 与 c15 提交对象比对一致；部署配置的修改仍待发布时验证，不据源码 ACL 声称正式权限已更新。
- 新 `TestHandleModernTargetUsesSessionFenceWithoutLegacyGeneration` 以旧生命周期审计值非零、当前 session 合法且消息不含旧身份字段为输入，先复现 `target event belongs to stale owner lifecycle`，再修复现代受理不传 legacy generation。初次 fixture 的 generation 为零断言失败不算业务红测。
- 新 `TestAuthorizedSessionReplacesStaleTargetAtSameOrEarlierBar` 两个分支先复现 Store SQL 错判 stale，修复为按 instance/session 独立比较 bar；同时验证旧 session 延迟消息不能覆盖已受理目标。保留 T02 的幂等认领与事务删除规则。
- 主 Agent Strategy 全量测试通过；Bootstrap/Trigger/Outbox/Store 四包 race 通过。Trade EventConsumer 和 Store 全量测试通过，新增两项定向 race 通过。Events/Admin CLI/Sysdeploy 测试及 Strategy/Events/Trade 两个改动包 vet 通过。
- 三文件外部 E2E 已改为真实 Processor 求值和持久发布，而非直接 CommitEvaluation 构造旧目标。Trade 通过 RPC handler 创建 STRATEGY Paper、认领 session、Resume，真实权益采样/WeightResolver/TargetWorker/OrderService/Paper matcher/reducer 完成 1 BTC 买入；成交 1 笔、BTC=1、USDT=50000，再次收敛无新增订单，SQLite 重开后 receipt/order/fill 保留。
- 子 Agent 执行 `env GOFLAGS=-race bash scripts/test/e2e/test-strategy-trade-event-e2e.sh` 通过；主 Agent 独立运行普通脚本通过（Strategy 0.13s、Trade 1.20s），核验实际测试 PASS 而非跳过。Broker 和两个进程均为脚本创建的 loopback 隔离实例，已退出；市场数据输入/行情为 fixture，跨进程授权是 HTTP/protojson 到真实 RPC handler 的测试桥，不覆盖生产 Gateway/tRPC。
- 新起 `review_modern_integration` 确认发布前尚有两项直接调用方缺口：Strategy 的 Trade 客户端仍默认 localhost:11200、无 Gateway target-node/鉴权配置，无法据当前代码支持独立 Trade 节点；CreateStrategyInstance 插入后 Claim 失败没有返回已创建实例，调用方不能按正常创建响应恢复。两项须在 T10 管理入口/生产接线闭环，不以本地桥接 E2E 替代。
- 审查提出的“Strategy 停用后应自动撤旧挂单”经主 Agent 按计划和最终 Submit/新目标扫描路径核验后撤回：既定合同明确停用不隐含撤单或清仓；旧待发订单仍受当前目标/session 提交检查，新目标先处理旧挂单，不能为通过审查改变停用语义。
- 主 Agent 扩大至 Trade/嵌套 tradegen、Strategy/嵌套 strategygen、Events 全量时，其余包通过，Trade/test 的 12 项人工恢复用例失败，已交实施 Agent 区分旧 fixture 的原始错误与新的 shared/account 分类并处理；不把先前局部通过当作全量通过。全类型 OperatorAction 的最终审查与复验仍在执行。旧协议删除、正式部署和生产 Paper 全链仍未完成。

### T10 全类型人工操作恢复增量

- `OperatorWorker` 为每个 action 设置独立默认 30 秒尝试预算，保持串行处理。MANUAL_ORDER、FLATTEN、CANCEL_ORDER、SUBMIT_ORDER 都使用可取消逻辑账户锁；锁超时通过单独事务读取最新 durable action，只更新诊断，不把持锁者的新状态覆盖为旧快照。
- shared DB/配置错误保留并影响健康；账户边界临时错误和已记录的业务失败只影响对应 action。`errors.Join` 必须全部分支都属于可隔离类别才允许忽略，诊断写入失败仍是 shared 故障。真实 Store/OrderService/AccountSyncService 用例覆盖 A 超时后 B 推进、SUBMIT 锁竞争正常 Ready 和诊断 DB 故障 Not Ready。
- Cancel 保持不确定 action 为 RUNNING；诊断或 COMPLETED 写入失败只返回最后持久化 Action/Order 身份。RecoverCancel 先 GetOrder，查询不确定不重发；查询终态交真实 Sync/Reducer 导入成交，查询仍 OPEN 才受控撤单。网络查询、撤单和同步使用原 context，脱离取消的短 context 仅用于本地事实与诊断。
- FLATTEN 先恢复已经持久化的 child，之后才决定是否需要新行情和新单；没有当前持仓快照但仍有 SUBMIT_UNKNOWN child 时不得完成。真实红测覆盖未知 child 先查询、无重复 POST、仍 RUNNING，以及 shared sync 错误不能被吞掉。
- 扩大回归中，原始 dispatch-loss / post-ACK sync 的注入错误本来不是 typed 账户错误，故明确上抛，同时保留原 RUNNING/COMPLETED、child ID、余额、单次 POST 和后续恢复断言。账户暂未就绪和陈旧行情仍保持原人工受理 NoError/可查询 RUNNING 合同；没有为了统一健康分类把这两项改成参数失败。
- 主 Agent 最终 `env MOOX_RUN_REAL_TRADE_DNS_E2E=0 go test -count=1 ./modules/trade/... ./modules/trade/proto/tradegen ./modules/strategy/... ./modules/strategy/proto/strategygen ./packages/events/...` 全部通过。operator/order/runtime/test 四包整包 race 分别 47.653s/48.081s/12.318s/67.362s，通过。Trade/Strategy/嵌套生成包/Events 的 vet、diff-check 通过。此前失败记录保留为修复过程，不再是当前失败状态。
- 独立 codeCR 最后发现的 Cancel 响应携带旧 OPEN 状态已先红后绿：失败路径重读最新订单；读取失败仅返回已知身份并合并 DB 错误，不把陈旧状态当最新事实。主 Agent 最后 operator/rpc/test 三包普通复验通过（7.362s/3.385s/13.700s），Cancel/Lock 等定向 race 10.482s 通过，相关 vet 通过。
- `review_modern_integration` 对该 Operator/RecoverCancel 最终范围无剩余 P0/P1/P2；其指出的分层测试缺口也已补成真实 ExecutionServer -> OperatorService -> Store 的 `TestManualRPCTransientAcceptanceReturnsSuccessWithDurableAction`：not-ready/stale 两分支初次和重试均 RetInfo=SUCCESS，RUNNING/LastError 等于持久事实，0 次 POST；主 Agent 定向 race 4.339s 通过。
- 实施 Agent 自行完整普通/race 复验也通过并确认所有命令 session 退出。此处不标记整个 T10/T11 完成，不发布、不操作正式账户。现代流水线集成为 `3bbcdae7`，隔离 NATS/Paper 成交测试为 `602de472`；两者不是生产发布证明。

### T09 普通下单与控制模式阶段证据

- ControlMode 在 Domain/Store/Schema/RPC/Paper 创建贯通；默认 STRATEGY，显式 MANUAL 不允许策略 Claim/Rebind/Resume、目标受理/receipt 回放或目标提交。Claim/Rebind 的现代与旧入口均返回冲突；不是根据 owner 为空允许普通下单。
- 无损迁移增加控制模式并扩展 action CHECK，保留已有账户、成员、订单、成交、目标、receipt 和 action；12 种实际已知历史列布局、未知扩展拒绝、迟发初始化故障整事务回滚及重开均有正式用例。未触碰生产数据库。
- 普通 SubmitOrder 只做本地受理/预占，后台共用 OrderService；没有 Pause/Cancel/同步交易所 POST。绝对 deadline 首次持久化后不随恢复重算，重复请求以原 action/client ID 恢复，未知提交先查询，已过期未发单释放预占。
- codeCR 指出的生产 ErrAccountNotExecutable 分类、账户错误与 DB 的 Join、依赖等到 ctx.Done 后诊断落库、双故障/锁超时/link 失败身份丢失均已补真实行为红测和修复。最后 crash-gap 用例还断言响应 action JSON 等于数据库事实，而非内存中尚未成功保存的 link。未知 wrapper 的 nil unwrap 负例也已补齐。
- 本地 `TestOrdinaryPaperRPCWorkerFillAndReplay` 实际经过 RPC 创建 MANUAL Paper、生产 OrderService/Worker/Decider/Matcher/Reducer、查询订单成交持仓和重开 SQLite；SPOT/SWAP 均通过。`TestOrdinaryLiveMockSubmissionMarketRules` 使用实际 account.Service 和本地 fake adapter，覆盖 SPOT/SWAP 正负例、禁用初次无 action/order、无额外 POST。它不是实盘或生产网络测试。
- `control_mode_final_audit` 最终逐项核验，报告限定 T09 后端无剩余 P0/P1/P2；独立普通下单和实际 AccountService/Paper 集成测试通过。`submission_rpc_review` 复核 RPC 与 Web API 错误携带身份无剩余问题。主 Agent 已复核最新代码及定向 race，提交 `33e0eb0b` 不包含复制的无关 dirty 基线。
- 主 Agent 全 Trade、嵌套 tradegen、Gateway 和 Strategy bootstrap 普通测试全部通过；operator/order/rpc/runtime/test/tradegen 六包 race 通过。最后恢复增量的定向 race 分别 11.404s/12.606s，通过；Store ControlMode/逻辑锁定向 race 20.687s，通过。Trade/tradegen vet、schema 单独载入 SQLite、diff-check 通过。
- 之前生成阶段的未定义方法/遗漏 validator 编译错误、测试 fixture 缺少 fence/行情 catalog 等不作为行为红测证据；文档中的旧测试记录不等于当前正式环境验收。

### T10 控制台下单增量证据

- 创建组合和 Paper 显式选 STRATEGY/MANUAL；MANUAL 普通下单不显示接管警告，STRATEGY 入口明确为“接管并下单”。请求携带绝对提交截止时间，SPOT 不传 NET、SWAP 传 NET。
- 受理与成交分开显示。RUNNING 无 child 继续查 action；提交阶段完成仍查询 OPEN 订单到真实终态。错误中已有 action 时保留其身份并查询；传输/系统结果不确定时冻结原请求，同 ID/同参数重试。明确未受理错误才恢复编辑，不生成新 key。
- 新起 `t10_order_ui_review` 审查并补回归：明确拒绝不永久锁表单、不查询同 key 的冲突旧 action；迟到响应不串到另一个账户；Flatten 不沿用旧订单；MANUAL 不计入暂停自动执行筛选；接管后同步刷新详情和列表；接口声明允许接管 RUNNING 暂无订单。
- 审查者对旧接管路径的“CAS 返回冲突并丢 action”疑问经实际 Store 核验撤回：该更新没有所述 CAS；未为不可达假设保留额外模式分支。保留真实可达的 transport/system 不确定与明确拒绝分流。最终限定 UI 范围无剩余 P0/P1/P2，独立 44 项定向测试、类型检查和 diff-check 通过。
- Mounted Vue/Arco 用例与 Paper 表单、API 定向测试通过；主 Agent 最终 Web 全量 61 文件/234 测试通过。类型检查通过，最终生产构建通过（13.99s），存在原有 Browserslist/Sass/lottie/chunk 大小警告，不为本任务更新依赖。
- 新增 `web/tests/trade-execution.spec.ts`，拦截真实 HTTP 形状但不连接 Trade 服务。两模式、1440px/390px 共四个 Chromium 用例通过；本地截图发现固定宽度抽屉裁切，已补边界断言与响应式修复。初次 mock 路由误拦 Vite 源码 import 导致空页，修正后通过，不把该 fixture 故障作为产品缺陷。
- 最后截图另发现移动端订单 modal 的固定 520px 宽度裁切，动画中的 boundingBox 曾让断言假通过；改成检查实际 CSS 宽度后获得真实红测，再以响应式宽度修复。最终四例通过（19.5s），主 Agent 复看桌面和移动端截图确认表单及成交状态可见。生成的 mock 截图留存 `/tmp/moox-trade-ui-t09-33e0eb0b/`，不提交测试产物或虚拟 session trace。
- 尚未完成现代 Strategy producer/Outbox/EventBus 协议清理、其他 OperatorAction 的全链健康门禁、嵌入式 statik/正式发布和真实 Paper E2E。这些仍是 T10/T11 的必要待办，不能因上述本地证据宣告总目标完成。

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

### T09/T10 前置门禁增量

- 历史 receipt 迁移正式红测复现：未知列、CHECK/default/unique、额外或改形索引、trigger 原先可被 copy/drop 静默删除；直接迁移在 rename 失败时会留下 `__new` 且丢失原表。现在只接受精确已知历史形状（含实际注释与可选原索引）和当前形状，拒绝未知扩展及入站外键，迁移自身也处于事务内；Open 外层事务保留较早 target 表升级的回滚。
- 主 Agent 新增 `TestOpenLegacyPinnedCutoverRollsBackAfterPaperFailure`：从旧 logical/target schema、pinned 目标和真实 Paper Order/Fill 开始，末尾余额回填故意失败，连续两次 Open 后全库 schema/data 均与原样一致。仅修复测试故意损坏的 fee 后，升级成功、目标暂停且废止、成交和正确余额保留，再打开无二次变化。这是本地故障注入验证，不是生产迁移证明。
- T09 快速受理前置问题已红测复现：账户 mutex 占用时 Place 超过 50ms deadline 仍等待到 1s 测试上限。现复用同一账户锁注册表的 context 获取，超时返回带账户归属的 `place_lock` 错误，不创建订单、不消耗 client ID；释放锁后同请求可正常准入。锁定范围与后续风控/预占逻辑未改变。
- 独立只读调查确认普通 SubmitOrder 不能调用现有 `PlaceManualOrder` 或完整 `recoverManualChild`：前者会暂停/撤单，二者最终均会同步调用 Submit。后续应拆开“寻找/创建并关联 child”与“推进提交”，RPC 只完成前者，既有 OperatorWorker 执行后者。显式 logical ID、用户输入 deadline 必须参与请求幂等，服务端默认 deadline 只计算一次；还需补 logical-account context 锁，不能用外层 timeout 掩盖不可取消的锁等待。
- Receipt 增量核验继续发现 shared SQL normalizer 会改变字面量语义，独立 codeCR 又发现 SQLite 大小写标识符可绕过 trigger/入站 FK 检查。两类均先红后绿：receipt 专属词法比对保留 literal 和注释边界，依赖枚举按 NOCASE 比较，非规范表名拒绝；不放宽或重写其他迁移的 shared 校验。已覆盖 15 类未知结构，以及有/无原索引、有/无历史注释的四种已知形状。
- 新 `review_receipt_place_gate` 最终无剩余 P0/P1/P2，主 Agent Store 全量、receipt/组合迁移 race、Order/Operator/Target/Test 四包完整 race、Place 截止时间 race 三次及 Trade vet 通过。Place 提交为 `a4b62b62`。
- Trade 全量首轮通过，但最终重跑出现 Session Ready 与持久化 Ready 不一致的间歇失败；定向 race 50 次没有复现，已另外建立确定性 handoff 用例调查，不能用重跑通过冲淡该失败。处理记录在后续补记。T09 普通下单、T10 完整契约切换、T11 正式部署及虚拟账户真实端到端仍未完成。

### Session 启动交接回归

- 调查区分了两个窗口：原脚本测试在 OnSubscribed 后发送 position，并未保证 position 在启动缓冲期到达；激活后 position 暂时置账户未就绪本来就是合法行为。另有真实实现缺陷：activate 已写 Ready=true 后，finishActivation 又重放迟到的缓冲 position，将数据库 Ready 改回 false。
- 新 `TestSessionHandlerPersistsReadyAfterLateBufferedPosition` 使用真实 Session.applyEvent、Store、AccountSync，固定“初次 drain 后、最终 gate 前”的到达顺序，原实现在 stored.Ready 断言稳定失败。现在 readiness callback 延迟到最终关闭准入并排空事件之后；回调或应用失败仍释放 gate，不遗留事件等待者。
- 原启动测试加显式 positionBuffered 屏障，保持即时 stored.Ready 断言；另外验证激活后的 position 确实临时未就绪，并由实际 Manager.SessionState + SyncAccount 恢复。运行 Session 的测试失败也会取消并等待退出后才关闭数据库。
- 修复后主 Agent Trade 全模块普通测试通过；Runtime/AccountSync/Test 三包整包 race 通过，最终测试清理增量另跑定向 race 三次通过；Trade vet/diff-check 通过。实现者 expanded session/handler race 50 次与最终 Runtime 整包 race 通过。新 `review_session_handoff_gate` 独立定向 race 20 次通过，最终无剩余 P0/P1/P2；未连接真实交易所私有流，不据此宣称网络端到端已验收。
- 本轮 Place 和 receipt 分别提交为 `a4b62b62`、`020fa35f`；Session 交接修复和本记录随 `fix(trade): persist session readiness after buffered events` 提交。没有正式部署；下一项实现仍是 T09 显式控制模式与普通 SubmitOrder，而不是把前置门禁当作 T09/T10 完成。

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

### T10 Gateway 与实例恢复增量（2026-09-06）

- 新增生产 Strategy HTTPS/HMAC Trade 客户端及专用 `trade` 配置，保持 Space 透传和最小 `trade_owner` 方法权限。客户端配置不再被本地 native Gateway 环境变量覆盖。直接 HTTP fixture 不算 Gateway 验证。
- 三进程真实 TLS Gateway/Trade HTTP/Paper Store 授权 E2E 已由主 Agent 在 `GOFLAGS=-race` 下复验：认领一次、释放一次、SubmitOrder 为零；缺失/错误 CA、错误签名/节点、跨 Space、越权方法均拒绝。该证据仅覆盖授权，不是部署或成交证明。
- CreateStrategyInstance 持久插入后的 Claim/启用/查询失败保留恢复身份；同 ID 同配置不重新 Claim，异配置不覆盖。未知 session 保留，明确拒绝后的清理错误不吞掉。Web 通用错误对象保留返回体。旧 UpdateRunner 在 RPC 和 Store CAS 两层禁止修改仍持有 session 的实例。
- 独立 `review_instance_recovery` 的 P1 旧 UpdateRunner 干扰已修复并复核；“全局唯一实例 ID 可探测占用”不构成新增字段泄露，审查者撤回该项。没有以测试成功代替跨 Space 返回字段检查。
- 独立 Gateway 审查发现实际部署仅 seed 到 Admin 本节点、setup Trade 发布未注册 ownership 路由，以及 target_node 单独配置被延迟到运行期失败。正在闭环两条注册链，必须由新 codeCR 审查后才记录其完成；手工构造的 snapshot 不再被当作部署证据。
- 本轮配置红测确认缺 URL、非法控制字符 node、仅 node 环境变量，以及 Admin 无法注册的大小写/点号 node 均被错误接受。修复后与 Admin 的小写字母/数字/连字符/下划线规则一致；Bootstrap 整包 race 通过。RPC/Store 整包 race、三包 vet、Web 全量 62 文件 236 测试和 vue-tsc 均通过。
- 新 `review_instance_response_final` 独立复核无剩余 P1/P2，另行通过 RPC/Store race、Web 全量及类型检查。该项提交 `a1e6f757`；旧 `legacyCompiled` 的进程缓存不扩展为兼容层，必须随旧 Runner API 在 T10 删除，当前中间版本不单独发布。
- 主 Agent 本轮完整 Trade/Strategy/events 测试、Gateway/Admin/CLI 注册相关全包测试和 race、完整相关 vet 均通过；Strategy/control-only 打包契约通过。运维文档已移除清库/删除 Stream 的破坏式步骤，替换为事实保留、受控升级、版本协调和正式 Paper 验收门禁。
- 主库文档提交 `16a60ea5` 的配置与真实节点注册验收门禁已同步到执行计划；保留此工作区已有执行记录，不整体覆盖成全未执行或全已执行。
- 仍需完成：最终旧协议和启动 reconciliation 清理、注册链审查及集成、全目标最终审查、生产发布、正式隔离 Paper 的真实策略到成交及重启验收。上述本地进展不勾选 T10/T11 的整体完成项。

## 最终源码增量与发布边界（2026-09-07）

- `376dfc18` 收敛了最后一轮注册与隔离测试风险：Strategy 的 Gateway native route 指向 tRPC 11430（HTTP 管理端口仍为 11433）；Strategy ACL 从旧 wildcard methods/callers 收紧为显式方法和 `admin-gateway`/`moox-cli` 调用方，并在 Admin seed 时迁移已有 wildcard 配置；Factor E2E 改用 `moox-cli` Strategy 凭据。
- `3ac59138`、`03d6142f`、`712a6505`、`d6516b85`、`68d0b5cd` 补齐 Space/事件总线资源安全边界：SQLite `CreateSpace` 为 create-only 且重复 ID 不覆盖原 metadata，使用同一规范化时间写入并返回 `created_at/updated_at`，不依赖提交后回读；catalog cache refresh 改为 after-commit best-effort，`GetSpace` 只把真实 `sql.ErrNoRows` 映射为 `SPACE_NOT_FOUND`。Factor E2E 在创建前拒绝已存在 Space，使用每次运行唯一 owner，在响应丢失或创建失败时仅清理仍归属本次运行的 Space；脚本无重启时要求预配置 allow-list，重启时合并运行进程/调用方的原有 allow-list、自动加入临时 Space，给 package-local/root 两种 View 启动命令传入 Storage secrets、`MOOX_STORAGE_EVENTBUS_URL` 和 allow-list，重启后校验 `/readyz`，失败尝试恢复原 View，且无 role credential 文件时仍保留 Factor 进程实际 EventBus URL，清理失败升级为测试失败。
- 最新本地核验：Storage metadata/catalog、Admin sysdeploy、Gateway router、gatewayproxy 普通测试及 `-race` 通过；相关 `go vet`、`git diff --check`、`bash -n scripts/test/e2e/test-factor-storage-e2e.sh` 通过。此前记录的 Trade/Strategy/Admin/CLI/Web、隔离 NATS/Paper、Gateway 授权验证仍有效；`make verify-pr` 仍受复制基线 dirty proto 与既有 Storage durable 命名规则阻断。
- 本轮改动不改变已发布 Trade 制品；Trade/CLI 制品仍来自 `89757d58`，`moox-trade` SHA-256 为 `c1ebe2368a0735451f1d3e60f5d8b2a6dd59e030b26959b3b5379ea8d26a4a83`，远端 dedicated Trade 节点已运行该制品并通过认证 `/readyz`，Paper 订单/成交及重启恢复证据保持不变。
- 最终独立 codeCR 已基于最新 `HEAD=f431610e` 完成复核，无剩余 P0/P1/P2。审查确认 Storage View 重启在调用启动命令前继承并预检凭据路径，根 `start.sh` 仅在显式 `MOOX_STORAGE_EVENTBUS_URL_OVERRIDE` 时覆盖 packaged runtime，普通启动行为不变；Trade/Strategy/Storage 关键调用链亦无新的 P0/P1/P2。该轮通过两个脚本 `bash -n`、Storage View/control profile 合同测试，以及 Trade/Strategy/Storage 共 11 个 focused package 测试；未覆盖故意不可读凭据下的真实 shell 文件系统故障注入。
- 正式 control-plane `ubuntu@106.53.107.122` 仍无法 BatchMode SSH（`Permission denied (publickey,password)`）。因此 Admin/Strategy/Gateway 注册链、应用快照和正式协调发布尚未完成；在控制面凭据恢复并完成独立核验前，T11 继续保持“执行中”，不得宣称完整正式环境交付。

尚无。必须记录最终源码 SHA、独立审查闭环、实际部署二进制 SHA/进程、正式隔离 Space/账户/策略标识、目标和订单/Fill 标识、余额/持仓断言、重启恢复和测试资源停用结果。任何一项缺失都不得标记目标完成。

## 最后一轮源码与隔离验收（2026-09-06）

- 完成旧自动执行兼容路径收敛：Strategy outbox 仅从 `t_strategy_results` 读取并发布现代 `LogicalAccountTargetWeightRequested`；删除 `t_strategy_outbox` 双读/删除分支、旧 owner 自动 reconciliation 和旧 Rebind 授权入口。历史表/字段只保留在事实读取与审计模型中，不参与新目标授权。
- 最新未提交增量包含固定精度的跨交易所逻辑数量、Gateway/Space ACL、Strategy HTTP 11433 配置与迁移、Result outbox 限额及永久错误隔离；相关定向包测试通过。嵌入式前端已重新执行 `make -C web-host statik`。
- 验证通过：`go test -count=1 ./modules/trade/...`、`./modules/strategy/...`、`./modules/admin/...`、`./modules/cli/...`；相关 Trade/Strategy/Admin/CLI race；相关 `go vet`；`git diff --check`；`bash scripts/test/contract/test-deploy-moox-strategy.sh`；`bash scripts/test/contract/test-deploy-moox-control-profile.sh`；`env GOFLAGS=-race bash scripts/test/e2e/test-strategy-trade-event-e2e.sh`；`env GOFLAGS=-race bash scripts/test/e2e/test-strategy-trade-gateway-e2e.sh`；Web 62 文件/236 测试、`vue-tsc --noEmit`、`pnpm run build:prod`。`make verify-pr` 的 `proto-check` 因工作树存在既有未提交基线而停止；`check-module-boundaries` 仍被 Storage 配置中的历史 `durable` 命名规则阻断，均非本任务 Trade 行为回归。
- Linux/amd64 重新构建 `moox-trade`、`moox-trade-cli`、`moox-strategy`、`moox-admin`、`moox-gateway` 和 `moox-web-host` 均成功。最终源码提交为 `89757d58`，已推送 `origin/feature/trade-execution-hardening`；Trade 制品以该 SHA 写入构建信息，`moox-trade` SHA-256 为 `c1ebe2368a0735451f1d3e60f5d8b2a6dd59e030b26959b3b5379ea8d26a4a83`，`moox-trade-cli` 为 `bf0daeb118f9a053a9958e41613410073bbd79b9e22a14ab0b13009c9f34a7da`。
- 正式发布：专用 Trade 节点 `ubuntu@43.132.204.177` 缺少新部署脚本要求的 `config/components.env`，因此未伪造 component overlay；在保留二进制、配置和 SQLite/WAL 备份（`/home/ubuntu/moox/trade-move/release-backups/trade-89757d58-20260906234615`）后，原子替换 Trade/CLI 并通过现有 `start.sh trade` 重启。当前进程 PID `1308017`，认证 `/readyz` 返回 200/`ready=true`，EventBus、数据库、Paper Matcher、Target/Operator Worker 均 ready，远端二进制 hash 与本地制品一致。Trade 迁移后保留 5 个 PAPER 账户、2 笔 FILLED 订单/成交且无 LIVE 账户；未修改 live_trading_enabled。
- control host `ubuntu@106.53.107.122` 当前 BatchMode SSH 返回 `Permission denied (publickey,password)`，故无法执行 Admin/Strategy/Gateway 正式注册链、应用快照核验及协调发布。未尝试发现或重建凭据；在控制面可达前，T11 仍为阻塞状态，不能将 Trade 单组件重启宣称为完整正式环境交付。
- 隔离 Paper 真实链路已验收：`crypto` / `paper-account-230bef26ed938b41` / `paper-logical-230bef26ed938b41`，策略事件经 Strategy Processor -> Outbox -> NATS -> Trade Consumer/Receipt -> Worker/Paper Matcher；订单 `ZHIujZQJpQAc9Amh5XY-1`、Paper order `paper-order-9989b96f6709d5082121ab7f`、Fill `paper-account-...:mtdaelt2u9vsd9i6rrn1bg`，成交 0.12504 @ 79974.25，重投不重复；重启后仍 PAPER/ready、receipt hash/order/fill 保留。该资源为隔离测试资源，未连接实盘。
- `f431610e` 之后的主 Agent 核验完成，代码审查门禁已闭环；当前唯一硬阻塞仍是正式 control-plane 接线与生产节点应用快照核验。

## 最终执行与正式环境验收（2026-09-07）

- 最终独立 codeCR 的 P2 已闭环：目标在 Place 后或 Submit 内跨过 `valid_until` 时，Executor 现在丢弃仍为 PENDING 的目标子单；初始过期检查会分页收集后再释放全部匹配子单，避免边遍历边改变结果集导致漏清理。成功释放后目标才进入 `EXPIRED`；释放失败与过期原因以 joined error 返回，保留重试/人工处理入口。真实 Store 夹具验证 `CANCELED`、`RemainingReservedQuantity=0`，三类跨期 Paper E2E 断言同步更新。
- `scripts/deploy/deploy-moox.sh` 生成的 `healthcheck.sh` 补齐 `gateway_health_addr` 函数；此前控制面健康检查会因函数未定义误报 Gateway 失败并触发重启。Control profile 合同新增函数存在性断言。远端 `./healthcheck.sh` 已无输出错误并返回成功。
- 注册链路最终修复提交为 `25b8de15`（禁用服务清理 Gateway 字段）和 `496c2481`（compute 节点只导入 `trade_owner`，不再把 `trade_console` 的 Admin 方法导入远端，消除重复 native RPC route）；过期释放与健康检查修复为 `4df9138f`。最终工作树仅保留其他任务已有未提交文件，本轮未覆盖或提交它们。
- 正式 control-plane 部署已通过临时过滤掉本地不受支持的 `[strategy_host]` 配置段后执行：`go run ./modules/cli/cmd/moox-cli setup deploy-control --file ./moox.toml` 返回 `{"host":"control","status":"ready","reset_data":false}`。远端 `ubuntu@106.53.107.122:/data/moox/prod` 的 Caddy/Admin/Gateway/EventBus/CloudNode/Collector/Factor/Monitor/HostAgent/Archive/Web 均 running/ready；Control profile 预期不运行 Strategy，残留 PID 仅为 stale 文件，不代表进程。Admin 日志显示 control 26 项与 compute-1 1 项部署导入均 `status=ok`，未再出现 duplicate native route 或 `init_failed`。
- Dedicated Trade 节点 `ubuntu@43.132.204.177:/home/ubuntu/moox/trade-move` 仍运行 PID `1308017`，认证 `./healthcheck.sh trade` 通过；`bin/moox-trade` SHA-256 为 `c1ebe2368a0735451f1d3e60f5d8b2a6dd59e030b26959b3b5379ea8d26a4a83`，`trade/config/app.yaml` 保持 `live_trading_enabled=false`。未连接或开启 LIVE 账户。
- 正式隔离 Paper 资源通过 Trade HTTP RPC 复核：Space `crypto`、Paper account `paper-account-230bef26ed938b41`、logical account `paper-logical-230bef26ed938b41`；target `paper-target-e2e-1788698251135260751` 为 `EXPIRED`；订单 `ZHIujZQJpQAc9Amh5XY-1` 为 `FILLED`（数量 `0.12504`、均价 `79974.25`、剩余预占 `0`），Fill 数量与订单一致、手续费为 `0`；Holdings 显示 BTC `0.12504` 与 USDT `90000.01978`。这些查询未改动账户，也未使用 LIVE 凭据。
- 最终验证通过：`go test -count=1 ./modules/trade/...`（含前次全模块通过；本轮 `modules/trade/test` 重新通过）、`go test -race -count=1 ./modules/trade/internal/application/target`、Admin CLI 普通/race/vet、Trade/Control deploy contract、`bash -n`、`git diff --check`、`env GOFLAGS=-race bash scripts/test/e2e/test-strategy-trade-event-e2e.sh` 与 `test-strategy-trade-gateway-e2e.sh`。前端及 Strategy/Storage 验证沿用本记录前述通过证据；`make verify-pr` 的 proto-check 与 check-module-boundaries 仍被既有非本任务 dirty 基线/Storage durable 命名阻断。
- 仍需保留的边界：本轮 Control deploy 使用未提交本地配置的安全过滤副本，未把凭据或该副本写回仓库；远端 Trade Paper E2E 复核基于既有隔离资源和已成交事实，未进行新的实盘请求。最终独立 codeCR（基于 `HEAD=4df9138f`）若发现新 P0-P2，必须先闭环再推送分支；在此之前不标记整个目标完成。

## 最终收敛增量（2026-09-07）

- 修复目标执行器在 `Place` 成功后、最终 `Submit` 前 session/fence 或目标有效性变化时的悬挂预占：现在会先丢弃仍为 `PENDING` 的子单，再返回 `ErrTargetSession`/`ErrInvalidTarget`，并以确定性测试覆盖跨 session 变更。
- Gateway 路由归一化改为对同一 `service_id` 的方法重叠做全量两两检查，补齐 wildcard、非相邻重复方法的拒绝；保留历史上 caller allowlist 不相交的 native 路由兼容，并由 `ResolveRPCForCaller` 在认证后选择正确路由。
- Dedicated Trade 节点新增 `TradeConsoleAdminService` 原生路径别名，复用同一 Console handler；远端 `trade_console` 注册和 scoped seed 使用别名，`trade_owner` 继续使用 canonical 路径，避免新部署在 native route 上产生歧义。别名 descriptor 有单测，注册/快照/Verify 路径均已同步。
- 部署合同修复 `import_trade_owner_route` 的 shell 续行语义，避免注释截断命令；Strategy deployment contract 实际执行通过。
- 本轮 focused 测试：`packages/gatewayproxy`、Gateway router/bootstrap/controlplane、Admin cmd/internal sysdeploy、CLI setup client/command、Trade 全模块普通测试均通过；Trade target/eventconsumer/runtime/paper/rpc 与 gatewayproxy race 通过。`bash -n`、`git diff --check`、Strategy contract 通过。
- 独立 codeCR 复核上述 route pairwise、Trade alias、目标子单释放和 shell 修复未发现新增 P0/P2；旧版本 remote canonical 双路由与新 alias 的混合升级仍需按 Trade-first/控制面协调窗口执行，当前不改变已发布控制面制品。
