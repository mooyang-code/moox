# moox-trade

交易执行与交易事实：执行账户、组合账户、目标收敛下单、订单成交持仓、人工干预和模拟盘。

设计文档：[交易](../../docs/模块/交易.md)

## 构建与测试

```bash
./scripts/build/build.sh trade
./scripts/build/build.sh trade-cli
go test -count=1 ./modules/trade/...
```

## 配置

`config/app.yaml`、`config/trpc_go.yaml`；schema 在 `schema/`。交易所接口说明见 [docs/exchange-apis.md](docs/exchange-apis.md)。
