# moox-trade

Trade 是 MooX 的交易执行与交易事实源，负责执行账户、组合账户、订单、成交、
持仓和目标收敛。V1 面向个人量化，采用单进程、SQLite 和 JetStream，不引入分布式
事务编排、分布式锁、通用任务引擎或多策略账户拆分。

完整设计见 [DESIGN.md](DESIGN.md)。

## 核心对象

- `TradingAccount`（执行账户）：一个通过 `LiveConfig` 或 `PaperConfig` 配置的实际交易账户。
- `LogicalAccount`（组合账户）：由一个或多个同质 `TradingAccount` 组成的策略聚合账户。
- `LogicalAccountTarget`：组合账户当前唯一、可替换的完整目标，不是执行历史。
- `InstrumentTarget`：一个规范化标的及其绝对目标持仓量 `quantity`。
- `Order`、`Fill`、`Position`：订单意图、交易所成交事实和已确认持仓。
- `OperatorAction`：人工下单、撤单或逐账户清仓的幂等操作记录。

一个执行账户最多加入一个启用的组合账户。一个组合账户最多由一个
`StrategyRunner` 控制；一个 Runner 可以控制一个组合账户，而一个组合账户可以包含
多个执行账户。成员必须具有相同的 paper/live 模式、SPOT/SWAP 市场和
`settlement_asset`，但可以来自不同交易所。

## 服务

| 服务 | 默认端口 | 职责 |
| --- | --- | --- |
| `TradeConsoleService` | `11200` | 执行账户、模拟盘、组合账户、人工订单、查询和执行能力 |
| Health | `11210` | `/healthz`、`/readyz`、`/metrics` |

### DNS Resolver

启用后，Trade 通过鉴权 Gateway 暴露独立的 `TradeDNSResolverService.ResolveDomains`，供
Collector 批量解析行情域名。Resolver 只读取部署时由 `moox-cli` 从 `custom.toml` 提取的
`dns_resolver` YAML 子树，不读取完整 TOML，也不接触 SSH、云账号或交易凭据。每个候选 IPv4
会在 Trade 节点执行有界 TCP 探测并返回延迟排序；单域失败通过响应级未解析列表表达，不阻断同批
其他域名。Resolver 是辅助能力，暂时不可用不会让 Trade `/readyz` 失败。

策略目标经 `moox.trade.target.requested.v1` 事件进入 Trade，不通过公开 RPC 伪造
订单 owner。订单归属由服务端写入：

- `TARGET`：属于当前 `LogicalAccountTarget`。
- `OPERATOR`：属于一个明确的 `OperatorAction`。
- `EXTERNAL`：由账户同步发现、并非 MooX 创建。

## FULL 目标

Strategy 发布 `LogicalAccountTargetRequested`：

```text
target_id
runner_id
logical_account_id
command_sequence
targets[] InstrumentTarget
  instrument_id
  quantity
```

每条命令都是组合账户的 FULL 快照：

- `quantity` 是带符号的绝对目标持仓量，不是下单量或增量。
- 遗漏的旧标的目标为 `0`。
- 空 `targets` 表示组合账户全部目标归零。
- `hold` 不发布命令，保留上一次目标。
- 只接受更高的 `command_sequence`；低序号和重复命令不改变状态。
- PAUSED 时仍保存最新目标，但不创建订单；只有人工 Resume 才恢复收敛。

SPOT 目标不能为负数；SWAP 使用正负号表示方向。Strategy 只发送规范化
`instrument_id`，Trade 负责解析交易所原生 symbol、精度、最小量和合约张数。

## 动态择优

TargetExecutor 每次只提交一个子订单，等待账户事实更新后再计算：

1. 先撤销与当前目标冲突的自有 TARGET 订单。
2. 先关闭反向持仓。
3. 减仓优先选择可减持仓绝对值最大的账户。
4. 加仓按成员 `priority`、可用资金和交易所限制择优。
5. 当前账户无法承接剩余数量时再选择下一个合格成员。

不同执行账户上的相反方向持仓不会用净额抵消成“已完成”。无法达到交易所最小量的
剩余目标写入 `blocked_targets`，避免无限重试。

## 人工干预

`PauseLogicalAccount` 停止新的自动下单；`ResumeLogicalAccount` 使用已保存的最新 FULL
目标恢复执行。人工下单会先暂停整个组合账户并撤销活动 TARGET 订单，完成后仍保持
PAUSED。

`FlattenLogicalAccount` 是逐账户清仓：

- 同步所有成员账户。
- 撤销同步发现的全部活动订单。
- 在每个持仓所在的原执行账户提交平仓单，不按组合账户净额猜测。
- 独立记录每个账户的剩余持仓或错误。
- 最终保持 PAUSED，且不删除最新 Strategy 目标；以后 Resume 可能重新开仓。

人工下单、撤单和逐账户清仓必须提供幂等 `action_id` 和操作原因；Pause 要求原因，
Resume 会返回重新执行最新目标的提示。

## 订单与交易所

- 公共数量统一使用基础资产数量；适配器负责交易所合约换算。
- `FillPolicy` 表示限价单存续策略，支持 `GTC`、`IOC` 和 `FOK`。
- `ReducePositionOnly` 是服务端根据已确认持仓和可信执行阶段推导的内部保护字段，
  不由公共 RPC 调用者设置。
- `SUBMITTING`/`SUBMIT_UNKNOWN` 先按相同 client order ID 查询交易所，禁止盲目重发。
- 成功但非 OPEN 的下单响应立即触发账户同步；没有 Fill ID 的聚合响应不会伪造成交。
- OKX client order ID 使用 `xid` 生成，并在适配器边界校验长度和字母数字格式。
- Paper V1 使用公共行情做简化撮合：MARKET 使用最坏可成交价；LIMIT 可立即成交时按
  taker 成交，未成交的 GTC 保持 OPEN 并在行情穿价后按 maker 成交，IOC/FOK 无法全量
  立即成交时整单取消。不模拟盘口深度、排队优先级或部分成交。

Live 交易默认关闭。启用 PRODUCTION 账户和提交 live 订单需要显式
`live_trading_enabled=true`。Trade 通过服务认证的 `GetSecretValue(secret_id)` 单条读取
`SecretMaterial`，不会列出或加载无关 Secret。TESTNET 与 PRODUCTION 使用固定交易所
端点。

## 验证

```bash
go test -count=1 ./...
go test -race -count=1 ./...
go vet ./...
```
