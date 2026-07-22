# moox-factor

因子计算模块。`moox-factor` 是独立 tRPC 服务进程，负责后续因子定义管理、事件触发计算、Python worker 执行和 Storage 结果写回。

实时 Storage 触发器只读取 EventBus 的 `stream`、`consumer` 和 `fetch_max_wait`，通过 managed bind 使用服务端预声明的 `factor_calc` durable；filter、ACK 和投递限制不在 Factor 配置中重复声明。

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
