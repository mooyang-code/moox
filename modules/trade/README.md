# moox-trade

MooX 的事件驱动交易内核，提供账户与通道管理、订单执行、成交结算、账本余额、仓位查询、撤单换单和目标仓位调仓。

## 目录

```text
internal/domain/          订单、执行、账本、仓位、调仓领域模型
internal/algorithm/       可替换且版本化的拆单、定价、执行算法
internal/application/     命令、消费者、对账与调仓编排
internal/infra/store/     交易存储和事务边界
internal/infra/bus/       Inbox/Outbox 与公共 JetStream 客户端
internal/infra/exchangebridge/ 交易所适配桥
internal/bootstrap/       生产装配与后台 worker
test/                     跨组件端到端测试
```

架构细节见 [DESIGN.md](./DESIGN.md)。

## 服务

账户、余额、资金、API 凭证、通道、交易操作、订单、成交、仓位服务使用端口 `11200-11208`；目标仓位调仓服务使用 `11211`。Admin 网关通过 `trade_*` deployment 转发。

## 配置

- `database.path`：Trade 数据库路径，底层实现不暴露给应用层。
- `eventbus.urls` / `eventbus.stream`：JetStream 地址与 `MOOX_TRADE` stream。
- `eventbus.execution_durable`：订单执行 consumer。
- `eventbus.rebalance_durable`：调仓请求 consumer。
- `eventbus.progress_durable`：成交后推进调仓依赖 legs 的 consumer。
- `security.encryption_key`：交易所凭证 AES-GCM 密钥。

## 构建与验证

```bash
make -C proto all
go build -o bin/moox-trade ./cmd/server
go test -count=1 ./...
go test -count=1 ./test -v
```

`test/eventbus_e2e_test.go` 会启动进程内 NATS Server，验证真实 JetStream 发布、NAK 重投、Inbox 幂等和单次交易所提交；`test/trade_e2e_test.go` 验证重启恢复、未知提交、成交结算、资金保护、撤单与调仓。
