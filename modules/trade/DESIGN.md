# Trade 通用交易执行内核

本文与 [README.md](README.md) 是 Trade 的现役事实源。`docs/superpowers/` 下的设计和执行
计划只记录决策过程，不定义当前运行契约。

## 边界与原则

Trade 负责：

- ExchangeAccount、LogicalAccount 和成员关系。
- 账户同步、readiness、订单、成交和持仓事实。
- 当前 `LogicalAccountTarget` 的持久化与收敛。
- 人工暂停、恢复、下单、撤单和逐账户清仓。

Strategy 负责不可变 Strategy、StrategyRunner、StrategyResult 和最新理论 FULL 目标；
Admin 负责 Secret；EventBus 只传输事件。Trade 不负责策略计算、多策略资金聚合、固定
账户分配权重、跨币种净值或机构级工作流。

V1 保持单进程串行协调。SQLite 事务、账户锁、逻辑账户锁、幂等 ID、交易所查询恢复和
定期同步已经覆盖个人量化所需边界，不增加分布式事务编排、分布式锁、全局
exactly-once 或通用调度框架。

```text
LogicalAccountTargetRequested / operator RPC
                    |
        LogicalAccount / TargetExecutor / OrderService
                    |
             ExchangeSession
                    |
              Binance / OKX
                    |
SQLite: account, logical account, target, operator action,
        instrument, order, fill, position, account snapshot
```

## 逻辑账户与归属

`LogicalAccount` 把多个物理账户视为一个总持仓：

```text
Strategy 1 -> N StrategyRunner
StrategyRunner 1 -> 0..1 LogicalAccount
LogicalAccount 1 -> N ExchangeAccount
ExchangeAccount 1 -> 0..1 enabled LogicalAccount
```

观察型 Runner 可以不关联逻辑账户。执行型 Runner 的 `logical_account_id` 必须和 Trade
保存的 `owner_runner_id` 相互匹配。启用期间不允许换 owner；先停用并释放旧归属，再
建立新关系。

启用成员必须同质：

- `ExecutionMode` 相同：PAPER 或 LIVE。
- `MarketType` 相同：SPOT 或 SWAP。
- `settlement_asset` 相同。
- PRODUCTION/TESTNET 环境一致。

成员可以来自不同交易所。`priority` 只是稳定的账户选择顺序，不是资金分配比例。
新增或移除成员必须在 PAUSED 状态进行。存在活动订单或非零持仓时，移除会被拒绝；
新增有敞口账户需要明确 adoption，且不会在 adoption 时立即交易。

逻辑账户的自动化状态只有 `ACTIVE` 和 `PAUSED`。readiness 由当前事实计算：

```text
ready =
  automation_state == ACTIVE
  AND 每个启用成员 session Ready
  AND 每个目标标的至少有一个可执行成员
```

成员 Not Ready 时停止新订单，但继续处理私有事件和同步。恢复 Ready 后只有仍为 ACTIVE
的账户自动继续；PAUSED 永不自行恢复。

## 数据模型

```text
t_logical_accounts
  space_id + logical_account_id
  name + owner_runner_id
  execution_mode + market_type + settlement_asset
  automation_state + pause_reason

t_logical_account_members
  space_id + logical_account_id + exchange_account_id
  priority + enabled

t_logical_account_targets
  space_id + logical_account_id
  target_id + runner_id + command_sequence
  targets_json
  status + blocked_targets_json + last_error
  accepted_at + mtime

t_operator_actions
  space_id + action_id
  logical_account_id + action_type + reason
  request_json + status + result_json + last_error
```

每个逻辑账户只有一行当前目标。`target_id` 是全局幂等与订单归属 ID；Strategy 发布时
令其等于 `result_id`，Trade 不复制 StrategyResult。执行进度通过当前目标、持仓、活动
订单和账户快照重算，不再保存第二份进度快照。

`blocked_targets_json` 记录暂时无法执行的数量和原因，例如低于交易所最小量。它不是
下一轮目标，也不参与 Strategy 输入。

## FULL 目标契约

```text
LogicalAccountTargetRequested
  target_id
  runner_id
  logical_account_id
  command_sequence
  targets[] InstrumentTarget
    instrument_id
    quantity
```

`quantity` 是规范化标的的带符号绝对目标持仓量。FULL 规则：

- 遗漏标的等于目标 `0`。
- 空列表表示所有目标归零。
- `hold` 不发事件并保留旧目标。
- 更高 sequence 原子替换旧目标。
- 低序号、重复和乱序命令不改变状态。
- PAUSED 中收到的新目标只更新存储，不创建订单。

收敛集合是当前 FULL 目标、任一成员非零持仓、任一成员活动自有订单的并集。因此旧
持仓或外部漂移不会因目标遗漏而失去处理机会。

`instrument_id` 是跨交易所规范化身份，例如 `BTC-USDT-SPOT`。Strategy 不发送原生
symbol。Trade 为候选账户解析原生 symbol、数量步长、合约换算、最小数量和最小名义
金额。无成员支持的标的是目标校验错误；临时下线则表现为 Not Ready。

## 动态收敛

每个逻辑账户由一个串行 TargetExecutor 处理。一次最多提交一个子订单，等待 Order、
Fill、Position、同步或计时器事实后重算：

1. 读取所有成员的已确认持仓与活动 TARGET 订单。
2. 撤销与当前目标冲突的自有订单。
3. 先关闭方向相反的物理持仓。
4. 比较同向总量与绝对目标。
5. 减少超额持仓，或增加剩余数量。
6. 再次从事实重算。

减仓优先选择可减持仓绝对值最大的成员；加仓按成员优先级、可用资金和交易所约束动态
择优，单个账户无法承接时切换到下一个候选账户。不同账户的多空持仓不能互相净掉来
宣告完成。

TargetExecutor 只撤销同一目标拥有的 TARGET 订单。发现活动 OPERATOR 或 EXTERNAL
订单会暂停整个逻辑账户并报告冲突，不会静默接管或撤销。

## 订单内核

公共数量始终是基础资产数量。OrderService 在调用交易所前持久化订单，并按状态实现
幂等入口：

| 本地状态 | 行为 |
| --- | --- |
| `PENDING` | 提交 |
| `SUBMITTING` / `SUBMIT_UNKNOWN` | 按 client order ID 和近期成交查询 |
| OPEN、部分成交、撤单中或终态 | 返回已有订单 |
| 稳定不存在且超过不确定窗口 | 使用同一 client order ID 受控重试 |

未知状态下禁止盲目重发。幂等比较只使用调用者拥有的订单字段，不比较服务端参考价和
时间。下单响应为成功但非 OPEN 时立即同步账户；无 Fill ID 的聚合响应不能直接生成
Fill 或结算资金。

`FillPolicy` 表示限价单存续策略：`GTC`、`IOC`、`FOK`；MARKET 不携带该字段。公共
RPC 不允许设置 reduce-only。OrderService 根据确认持仓和执行阶段推导内部
`ReducePositionOnly`：SWAP 减仓、反向平仓和逐账户清仓为 true，开仓与加仓为 false。
人工订单若会穿越零点则拒绝，必须先减仓或清仓，确认归零后再反向开仓。

订单归属由服务端赋值：

```text
logical_account_id
runner_id
owner_type       TARGET | OPERATOR | EXTERNAL
owner_id         target_id 或 action_id
```

Exchange 同步导入的未知订单固定为 EXTERNAL。公共调用者不能提供 owner 或 runner。

Paper V1 使用公共行情支持 MARKET 和简化 LIMIT 撮合：可立即成交的 LIMIT 按 taker
成交，未立即成交的 GTC 等待行情穿价后按 maker 成交，IOC/FOK 无法立即全量成交时整单
取消；不模拟盘口深度、排队优先级或部分成交。Live Binance/OKX 使用交易所快照作为
余额和持仓权威。OKX client order ID 使用 `xid` 生成一次、先持久化，随后查询与受控重试
复用原值；适配器仍校验最多 32 位字母数字。

## 人工控制

人工下单、撤单和逐账户清仓需要幂等 `action_id` 和 reason。Pause 需要 reason；
Resume 在不存在运行中人工操作或外部冲突且启用成员 Ready 后恢复。

### 人工下单

`PlaceManualOrder` 在逻辑账户锁内：

1. 重新确认物理账户仍属于启用的逻辑账户。
2. 持久化 PAUSED 和暂停原因。
3. 尽力撤销所有成员的活动 TARGET 订单。
4. 强制刷新账户事实。
5. 通过同一 OrderService 创建 OPERATOR 订单。

操作结束后仍保持 PAUSED。取消订单同样记录为 `OperatorAction`。

### 逐账户清仓

`FlattenLogicalAccount`：

1. 原子保存 PAUSED 和 RUNNING action。
2. 同步所有仍属于该逻辑账户的成员，包括 disabled 成员。
3. 尽力撤销同步发现的 TARGET、OPERATOR 和 EXTERNAL 活动订单。
4. 确认撤单终态后，在持仓所在的原账户平仓。
5. 循环同步，直到全部已知持仓归零、到达有界截止时间或账户报错。
6. 按账户返回剩余持仓和错误，最终保持 PAUSED。

逐账户清仓不删除最新 `LogicalAccountTarget`。之后 Resume 可能按原目标重新开仓，RPC
与 UI 必须明确提示。相同 action ID 会继续未完成的同一操作，而不会复制子订单。

## Secret、环境与健康

普通 Admin `GetSecret` 始终返回脱敏值。Trade 通过服务认证的
`GetSecretValue(secret_id)` 单条取得独立类型 `SecretMaterial`，并校验 category、
Exchange、status、key ID、明文值和 extra config；不得先列出全部 Secret。

Live 交易总开关默认关闭。PRODUCTION 账户的创建、启用和下单在
`live_trading_enabled=false` 时失败。TESTNET 与 PRODUCTION 使用固定 REST/WS 端点；
同步和恢复不依赖开放下单开关。

readiness 包含 SQLite、启用时的 EventBus、运行 worker、Exchange session、逻辑账户和
归属配置。首次账户枚举失败按固定间隔重试；意外退出的 worker 写入 readiness 错误，
不叠加通用 supervisor。

## 持久化取舍

Trade 不维护本地双式余额投影。执行风险使用交易所权威快照、订单剩余 reservation、
不可变 Fill 和已确认 Position。Order 与 Fill 分表，因为订单是意图和状态机，而成交
可能多次、乱序、独立幂等到达。
