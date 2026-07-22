# moox-factor

因子计算模块。`moox-factor` 是独立 tRPC 服务进程，负责后续因子定义管理、事件触发计算、Python worker 执行和 Storage 结果写回。

实时 Storage 触发器只绑定 EventBus registry 预声明的 `MOOX_STORAGE/factor_calc` durable，并通过 managed bind 使用服务端的 filter、ACK、DeliverPolicy 和投递限制；这些策略不在 Factor 配置中重复声明。

`factor_calc` 的 live DeliverPolicy 当前由 EventBus registry 管理，Factor 客户端不会偷偷改成 `DeliverAll` 或从历史消息开始。需要 replay 时，必须显式创建并使用独立 durable，或使用离线的 `run-once`/补算入口；不得修改或复用 live durable 来承载历史重放。

实时 delivery 在 ACK 前先写入 Factor SQLite 的 `t_factor_event_inbox`；进程重启会 replay 未 flush 的 inbox，窗口 flush 后才删除对应记录。这个本地 pending inbox 是 live durable 之外的恢复边界，不替代 `MOOX_STORAGE/factor_calc`，也不把 replay 伪装成 live durable。

## 现状

```bash
# 仓库根目录
./scripts/build.sh factor

# 模块目录
go run ./cmd/server

# CLI 初始化、导入和单次计算
go run ./cmd/cli init --db ./data/factor/factor.db
go run ./cmd/cli import --db ./data/factor/factor.db --factors-dir ./factors --default-params 20,96
go run ./cmd/cli run-once --space crypto --dataset binance_spot_kline --subject BTC-USDT --freq 1m --bar-time 2026-07-06T09:15:00Z
```

## 目录结构

```text
cmd/server/main.go
config/app.yaml
config/trpc_go.yaml
internal/bootstrap/
internal/engine/
internal/registry/
internal/store/
internal/storageio/
pyworker/
examples/run-once/
```

## 规划方向

因子定义与结果列已可在 Storage 元数据（`Factor`、`DatasetColumn.origin_type=factor`）中登记；本模块承担：

- 因子任务调度与参数化计算
- 使用固定窗口 `EventBatcher` 将同一计算范围内的实时 Storage 事件合并为单个任务
- 结果写回 Storage Access（列级更新）
- 与 View 物化链路集成

详细存储模型见 [docs/存储概念与设计意图.md](../../docs/存储概念与设计意图.md)；模块整体落地方案见 [docs/因子计算模块设计.md](../../docs/因子计算模块设计.md)。`run-once` 自测流程见 [examples/run-once](./examples/run-once/)。

## 相关模块

- [storage](../storage/) — 因子结果持久化与查询
- [trade](../trade/) — 交易域（独立）
