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
| T01 基线与红测 | 执行中 | 授权/Store/RPC/Strategy bootstrap 基线四包通过；三个授权红测已复现；人工和 OKX 红测待执行 |
| T02 session 授权与幂等 | 已完成 | `839255b2`；独立 codeCR 和增量复核闭环，主 Agent race 复验通过 |
| T03 人工未知提交恢复 | 未开始 | 包括截止时间、RPC 语义、撤单竞态和重启恢复 |
| T04 OKX 单笔成交费用 | 未开始 | 包括 WS/REST 相同成交一致性 |
| T05 Paper 余额投影 | 未开始 | 包括完整历史、原子性、旧库受控转换 |
| T06 故障隔离与过期 | 未开始 | 包括真实 Decider、账户健康和 targetGate |
| T07 消息结果可观测 | 未开始 | ActionReporter、日志、低基数指标和 UI 语义 |
| T08 动态账户路由 | 未开始 | 不重新估值、不搬同向仓位、保护已有目标 |
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
- 当前 Fee 合同是非负成本，RealizedPnL 是有符号数。T04/T05 对返佣字段必须明确处理，不得以 Abs 伪装成已经支持正确返佣，也不得在 T05 顺手扩展未定义的资金模型。

## 发布调查

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
