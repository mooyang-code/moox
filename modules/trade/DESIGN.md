# Trade 模块设计（Account + Order 合并微服务）

`trade` 模块以**单一微服务**（`moox-trade`）统一承载两条交易链路：

- **账户域（account）**：用户资金账户、余额、资金流水、交易所 API 凭证。
- **交易域（order）**：交易通道、订单、成交、持仓、账户交易操作。

> 合并理由：order 的每一步几乎都要读写 account（下单冻结余额、成交回填流水、持仓盈亏），
> 强一致需求高，放在同一进程同一本地事务最简单可靠。当前两域规模尚小，无需过早拆分为两个微服务。
> 后续若交易执行与账务的伸缩/合规诉求拉开，可再从本模块拆出账户域（schema 已按域分文件，成本低）。

风格与现有 `admin` 模块（`schema/admin.sql`、`proto/*.proto`）保持一致。

## 设计约定（与现有模块对齐）

- **表名** `t_xxx`，**列名** `c_xxx`。
- **多空间隔离**：业务表含 `c_space_id`，唯一索引一般为 `(c_space_id, c_xxx_id)`。
- **软删除**：`c_is_deleted`（'false'=有效，'true'=已删除），流水/成交等不可变表不带软删除。
- **时间**：`c_ctime` / `c_mtime`，可变表配 `update_xxx_mtime` 触发器自动刷新 `c_mtime`。
- **金额/数量**：统一用 `TEXT` 存 decimal 字符串（proto 中为 `string`），避免浮点精度丢失。
- **接口响应**：每个 Rsp 首字段为 `common.RetInfo`，`code=SUCCESS(0)` 成功；分页用 `common.Page` / `common.PageResult`。
- **鉴权**：网关 authorize filter 校验 JWT，注入 `user_id` / `space_id` 到 ctx metadata。

## 产物文件

```
modules/trade/
  go.mod
  cmd/moox-trade/main.go              # 微服务入口
  internal/service/service.go         # 服务骨架（Health）
  schema/account.sql                  # 账户域表（4 张）
  schema/order.sql                    # 交易域表（5 张）
  schema/schema.go                    # 内嵌 AccountSQL()/OrderSQL()/AllSQL()
  proto/trade_service.proto           # package trpc.moox.trade，含 9 个 service
  DESIGN.md                           # 本文档
```

---

## 一、账户域（account.sql）

| 表 | 说明 |
| --- | --- |
| `t_accounts` | 交易/资金账户，关联 `t_users.c_user_id`，可绑定交易通道 `c_channel_id` |
| `t_account_balances` | 账户按币种的余额快照（available/frozen/total），带乐观锁 `c_version` |
| `t_account_fund_flows` | 资金流水账本（只追加），余额表为其物化结果 |
| `t_account_api_keys` | 对接交易所的 API 凭证，敏感字段加密存储 |

关键设计点：

- **余额 = 流水**：`t_account_fund_flows` 是权威账本（充值/提现/划转/成交结算/手续费/资金费/调整），
  `t_account_balances` 是可重建的物化结果，扣减用 `c_version` 乐观锁防并发超扣。
- **账户 ↔ 通道 ↔ 凭证 解耦**：一个账户可持有多套 API Key（不同权限/子账户），交易通道引用账户与某一凭证。

接口（service）：

| Service | 方法 | 说明 |
| --- | --- | --- |
| `AccountSvc` | Create/Update/Delete/Get/ListAccounts | 账户 CRUD |
| `BalanceSvc` | GetBalances / SyncBalances | 查询余额、从交易所同步刷新 |
| `FundSvc` | ListFundFlows / Transfer | 资金流水查询、账户间内部划转（成对流水） |
| `ApiKeySvc` | Create/Delete/ListApiKeys | API 凭证管理（列表脱敏） |

HTTP 转发路径：`/api/trade/{account|balance|fund|apikey}/{method}`。

---

## 二、交易域（order.sql）

| 表 | 说明 |
| --- | --- |
| `t_trade_channels` | 交易通道：到交易所的下单链路，绑定账户 + API 凭证，支持实盘/模拟 |
| `t_orders` | 订单全生命周期；`c_client_order_id` 幂等键、`c_exchange_order_id` 交易所单号 |
| `t_trades` | 成交明细（fill），一笔订单对应多笔成交，不可变 |
| `t_positions` | 合约/杠杆持仓快照（数量/均价/杠杆/保证金/盈亏） |
| `t_order_operations` | 账户交易操作审计（下单/撤单/改单/全撤/查询/调杠杆）含请求、响应、耗时 |

关键设计点：

- **订单状态机** `c_status`：0待提交→1已提交→2部分成交→3完全成交 / 4已撤销 / 5部分成交后撤销 / 6拒绝 / 7过期。
- **幂等**：`(c_space_id, c_client_order_id, c_is_deleted)` 唯一，重复下单可幂等返回。
- **操作审计** `t_order_operations`：记录每次对通道的调用与结果（含 `c_latency_ms`、`c_error_code`），便于排障。

接口（service）：

| Service | 方法 | 说明 |
| --- | --- | --- |
| `ChannelSvc` | Create/Update/Delete/List/TestChannel | 交易通道管理 + 连通性测试 |
| `TradeOpSvc` | PlaceOrder/CancelOrder/CancelAllOrders/AmendOrder/SetLeverage | 账户交易操作（下单/撤单/改单/调杠杆） |
| `OrderSvc` | GetOrder / ListOrders | 订单查询（支持 only_open、时间范围） |
| `TradeQuerySvc` | ListTrades | 成交明细查询 |
| `PositionSvc` | ListPositions | 持仓查询 |

HTTP 转发路径：`/api/trade/{channel|order|trade|position}/{method}`。

> 注：proto 中成交查询 service 命名为 `TradeQuerySvc`（与消息类型 `Trade` 区分），避免歧义。

---

## 三、模块内/外关系

```
t_users (admin)
   └─ t_accounts (trade/account域)        一个用户多个账户
        ├─ t_account_balances              账户余额
        ├─ t_account_fund_flows            资金流水
        ├─ t_account_api_keys ───┐         交易所凭证
        └─ t_trade_channels (trade/order域) ◄─┘  通道绑定账户+凭证
             └─ t_orders                   下单
                  ├─ t_trades              成交明细
                  ├─ t_positions           持仓
                  └─ t_order_operations    操作审计
```

跨模块仅通过 `c_user_id` 等字符串引用，不做物理外键（与 collector 跨域引用风格一致）。
模块内账户域与交易域同库，可在同一 SQLite 事务内完成「下单→冻结→成交→结算→刷新余额」。

---

## 四、后续落地建议

1. 生成 PB：参照 `modules/admin/proto/Makefile` 增加 `modules/trade/proto/Makefile`。
2. DAO 层：API Key/Secret/Passphrase 落库前加密（参考 admin 的 SSH 凭证加解密）。
3. 启动注入 schema：在 bootstrap 中调用 `schema.AllSQL()`（或分别 `AccountSQL()`/`OrderSQL()`）建表。
4. 成交回填：交易所成交推送 → 写 `t_trades` → 更新 `t_orders` 累计成交 → 生成 `t_account_fund_flows`
   → 刷新 `t_account_balances`，全部置于同一本地事务保证强一致。
