# moox-trade

交易域独立 tRPC 服务：账户、余额、资金流水、API 凭证、交易通道、订单、成交、持仓与下单编排。账户域与交易域共用同一 SQLite 文件，便于在同一事务内完成「下单 → 冻结 → 成交 → 结算 → 刷新余额」。

## 架构

```text
cmd/moox-trade/main.go          进程入口
internal/bootstrap/             配置 → DB/DAO → service → 注册 9 个 tRPC service
internal/config/                app.yaml
internal/spacecontext/          X-Space-Id 注入 + spacectx filter
internal/service/               AccountService / OrderService 编排
  order_exec.go                 下单冻结、成交回填、撤单解冻
  dao/                          GORM Store（SQLite）
  database/                     连接与 schema
internal/exchange/              交易所适配（binance / okx）
internal/rpc/                   9 个 tRPC handler
proto/                          trade_service.proto + tradegen/
schema/account.sql              账户、余额、资金流水、API 凭证表
schema/order.sql                交易通道、订单、成交、持仓、操作日志表
config/                         app.yaml + trpc_go.yaml
```

## tRPC 服务（端口 11200–11208）

| 服务 | tRPC 名 | 端口 | 网关 service 键 |
|------|---------|------|-----------------|
| 账户 | `trpc.moox.trade.AccountSvc` | 11200 | `trade_account` |
| 余额 | `trpc.moox.trade.BalanceSvc` | 11201 | `trade_balance` |
| 资金流水 | `trpc.moox.trade.FundSvc` | 11202 | `trade_fund` |
| API 凭证 | `trpc.moox.trade.ApiKeySvc` | 11203 | `trade_apikey` |
| 交易通道 | `trpc.moox.trade.ChannelSvc` | 11204 | `trade_channel` |
| 交易操作 | `trpc.moox.trade.TradeOpSvc` | 11205 | `trade_tradeop` |
| 订单 | `trpc.moox.trade.OrderSvc` | 11206 | `trade_order` |
| 成交查询 | `trpc.moox.trade.TradeQuerySvc` | 11207 | `trade_tradeq` |
| 持仓 | `trpc.moox.trade.PositionSvc` | 11208 | `trade_position` |

协议均为 HTTP，经 `moox-admin` 网关 `:11000` 以 `/api/admin/trade_*/*` 透传；转发目标来自 `t_service_deployments` active 部署记录。

全局 server filter：`validation` / `cors` / `spacectx`。

## 配置

`config/app.yaml`：

- `database.path` — SQLite 路径（默认 `./data/moox_trade.db`）
- `security.encryption_key` — API 凭证 AES-GCM 密钥
- `log.*` — 日志

环境变量：

- `MOOX_TRADE_DB_PATH` — 覆盖 Trade SQLite 路径
- `MOOX_TRADE_ENCRYPTION_KEY` — 覆盖 API 凭证加解密密钥

`config/trpc_go.yaml` — 9 个 service 监听端口。

## 构建与运行

```bash
make -C proto all                 # proto 变更后
go build -o bin/moox-trade ./cmd/moox-trade

mkdir -p data log
./bin/moox-trade -conf=config/trpc_go.yaml
```

仓库根目录：

```bash
./scripts/build.sh trade
```

`modules/trade` 当前没有独立 Makefile。

## 交易执行（OrderService）

- **下单**：计算冻结 → `AdjustFrozen` → 落库 PENDING → 适配层下单 → 回填或 REJECTED + 解冻
- **成交** `ApplyFills`：逐笔解冻、入账、手续费流水、更新订单状态
- **撤单**：适配层撤单 → 解冻剩余 → CANCELED
- **改单**：Binance 现货退化为撤单+重下；OKX 走 amend API

冻结额按 `qty - filled_qty` 重算，不单独落冻结表。

## 交易所适配

| 交易所 | 包路径 | 说明 |
|--------|--------|------|
| Binance | `internal/exchange/binance` | 现货 `/api`、U 本位 `/fapi`，HMAC-SHA256 |
| OKX | `internal/exchange/okx` | V5 API，HMAC-SHA256 + base64 |

真实 REST 端到端需有效 API 凭证；本地以签名逻辑与网关链路验证为主。私有 WebSocket 回报为后续扩展点。

## 相关文档

- [docs/架构总览.md](../../docs/架构总览.md) — 模块在整体中的位置
