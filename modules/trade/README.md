# moox-trade

交易执行与交易事实：执行账户、组合账户、目标收敛下单、订单成交持仓、人工干预和模拟盘。

设计文档：[交易](../../docs/模块/交易.md)

## 构建与测试

```bash
./scripts/build/build.sh trade
go test -count=1 ./modules/trade/...
```

## 配置

`config/app.yaml`、`config/trpc_go.yaml`；schema 在 `schema/`。交易所接口说明见 [docs/exchange-apis.md](docs/exchange-apis.md)。

交易所密钥按 ID 经进程共享的签名网关客户端读取，caller 固定为 `trade`；`gateway_client` 配置使用本机的主机签名私钥（`secrets/host.key`，本机组件共用）。配置文件必须存在，并严格拒绝未知字段、重复键及额外 YAML 文档。工作循环退出后释放客户端和数据库。本模块无需 CGO，只有服务一个二进制（没有模块自己的 CLI，操作走 `moox-cli trade`），在本机编译或交叉编译。

TradeConsole 使用 loopback 原生 tRPC 11200，空间标识来自网关透传的原生元数据。控制台和 Strategy 使用同一规范服务名，服务目录按方法的调用名单隔离账户授权与交易操作。
