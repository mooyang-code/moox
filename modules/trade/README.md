# moox-trade

交易域独立 tRPC 服务：账户、余额、资金流水、API 凭证、交易通道、订单、成交、持仓与交易执行编排。
账户域与交易域共用同一 SQLite 文件，便于在同一事务内完成「下单 → 冻结 → 成交 → 结算 → 刷新余额」。

## 架构

```
cmd/moox-trade/main.go          进程入口（trpc.NewServer + bootstrap.Initialize）
internal/bootstrap/             启动编排：配置 → DB/DAO → service → 注册 9 个 tRPC service
internal/config/                app.yaml 配置加载
internal/spacecontext/          X-Space-Id 注入/提取 + spacectx server filter
internal/service/               业务编排（AccountService / OrderService）
  ├ order_exec.go               阶段4：下单冻结/成交回填/撤单解冻/审计
  ├ dao/                        GORM Store 实现（SQLite）
  └ database/                   SQLite 连接管理 + schema 建表
internal/exchange/              交易所适配层
  ├ httpclient/                 共用 HTTP 客户端
  ├ binance/                    Binance 现货/合约 REST（HMAC-SHA256 签名 + 标的缓存）
  ├ okx/                        OKX V5 REST（HMAC-SHA256+base64 签名）
  └ all/                        blank import 注册全部适配器
internal/rpc/                   9 个 tRPC service 的 handler + protoconv
proto/                          moox_common.proto / trade_service.proto + Makefile + tradegen/
schema/                         trade.sql（t_accounts / t_account_balances / t_fund_flows /
                                t_account_api_keys / t_trade_channels / t_orders / t_trades /
                                t_positions / t_order_operations）
config/                         app.yaml（DB/加密钥/日志） + trpc_go.yaml（9 service 端口）
```

## tRPC 服务（端口 11200-11208）

| 服务 | 路径 | 端口 |
|---|---|---|
| AccountSvc | trpc.moox.trade.AccountSvc | 11200 |
| BalanceSvc | trpc.moox.trade.BalanceSvc | 11201 |
| FundFlowSvc | trpc.moox.trade.FundFlowSvc | 11202 |
| APIKeySvc | trpc.moox.trade.APIKeySvc | 11203 |
| ChannelSvc | trpc.moox.trade.ChannelSvc | 11206 |
| TradeOpSvc | trpc.moox.trade.TradeOpSvc | 11204 |
| OrderSvc | trpc.moox.trade.OrderSvc | 11205 |
| TradeSvc | trpc.moox.trade.TradeSvc | 11207 |
| PositionSvc | trpc.moox.trade.PositionSvc | 11208 |

协议均为 HTTP（便于经 `moox-admin` 网关 :11000 以 `/api/admin/trade_*` 透传）。
全局 server filter：`validation` / `cors` / `spacectx`。

## 配置

`config/app.yaml`：
- `database.path`：SQLite 文件路径（默认 `./data/moox_trade.db`）
- `security.encryption_key`：API 凭证 AES-GCM 加密钥（任意长度，内部 SHA256 派生 32 字节）
- `log.*`：日志级别/路径/轮转

`config/trpc_go.yaml`：9 service 监听与 filter，由 trpc-go 运行时加载。

## 运行

```bash
# 生成 proto（首次或 proto 变更后）
make -C proto all

# 构建
go build -o bin/moox-trade ./cmd/moox-trade

# 启动（trpc_go.yaml 通过 -conf 指定）
mkdir -p data log
./bin/moox-trade -conf=config/trpc_go.yaml
```

或直接用脚本：

```bash
./scripts/run.sh
```

## 验证

### 单测

```bash
go test ./... -count=1
```

覆盖：
- DAO 全表 CRUD / 软删除 / 转账原子性 / API Key 加解密脱敏
- 阶段4 编排：下单冻结、适配层失败解冻+拒绝、成交回填结算、撤单解冻
- 交易所签名：Binance HMAC-SHA256（RFC 4231 向量）、signedQuery 构造、状态映射；
  OKX base64 签名、鉴权头、订单类型/instType 映射

### 网关端到端

经 `moox-admin` 网关 :11000 调用（JWT 鉴权 + `X-Space-Id` + `X-User-Id` 透传）：

```bash
# 先启动 moox-trade 与 moox-admin
python3 scripts/e2e_trade_gateway.py
```

脚本依次完成：注册 → GetLoginSalt → Login（AES-GCM 加密密码）→
`trade_account/CreateAccount` → `ListAccounts` → `ListChannels` → `ListApiKeys`。

## 交易执行链路（阶段4）

- **下单**：计算冻结币种/金额 → `AdjustFrozen` 预冻结 → 落库 PENDING + 审计 →
  适配层下单 → 回填 `exchange_order_id`/状态 + 审计。适配层失败：解冻 + REJECTED + 审计。
- **成交回填** `ApplyFills`：每笔 fill 解冻对应冻结额、计入所得资产与手续费流水、推进订单状态。
  由私有 WS 推送或定时对账调用。
- **撤单**：适配层撤单 → 解冻剩余 → CANCELED + 审计。
- **改单**：Binance 现货无原生改单，退化为撤单 + 重下；OKX 走 `/api/v5/trade/amend-order`。

冻结额不单独落库，按订单状态（`qty - filled_qty`）重算，保证冻结/解冻可对冲。

## 交易所适配器（阶段5）

- **Binance**：现货 `/api`、U本位 `/fapi`。签名 = `HMAC-SHA256(secret, query)` 十六进制，
  头 `X-MBX-APIKEY`。已实现 Ping/GetBalances/GetInstruments/PlaceOrder/CancelOrder/
  CancelAllOrders/GetOrder/ListOpenOrders/ListOrders/ListTrades/GetTradeFee/Transfer/
  SetLeverage/ListPositions；标的缓存 5 分钟 TTL。
- **OKX**：V5 API。签名 = `base64(HMAC-SHA256(secret, ts+method+path+body))`，
  头 `OK-ACCESS-KEY/SIGN/TIMESTAMP/PASSPHRASE`。已实现 Ping/GetBalances/GetInstruments/
  PlaceOrder/CancelOrder/AmendOrder/GetOrder/ListOpenOrders/ListOrders/ListTrades/
  GetTradeFee/Transfer/SetLeverage/ClosePosition/ListPositions/ListFundFlows。

> 真实交易所 REST 端到端需有效 API 凭证，本地以签名单测 + 网关链路验证为准。
> 私有 WebSocket 回报（StreamHandler）为后续扩展点。
