# MooX Factor

Factor 是面向个人量化的单实例时序因子服务。它只持久化因子定义与数据集绑定；
实时触发和计算任务都保存在进程内，不提供持久化调度、运行历史或异步进度。

## Build And Run

```bash
./scripts/build.sh factor

# 服务端启动方式保持不变
./bin/moox-factor

./bin/moox-factor-cli init --db ./data/factor/factor.db
./bin/moox-factor-cli import \
  --db ./data/factor/factor.db \
  --factors-dir ./factors \
  --default-periods 20,96

./bin/moox-factor-cli run-once \
  --space crypto \
  --dataset binance_spot_kline \
  --subject BTC-USDT \
  --freq 1m \
  --start-time 2026-07-26T00:00:00Z \
  --end-time 2026-07-27T00:00:00Z
```

## Runtime Contract

- 当前 schema 不兼容早期实验数据库；检测到旧表或旧列时会拒绝启动，请新建数据库。
- `periods` 是因子计算周期；`depends` 是源码声明的额外输入列，不表示因子 DAG。
- `lookback_bars` 是每个目标 chunk 前的输入上下文，不决定输出行数。
- 实时事件转换为 `[bar_time, bar_time + 1ns)` 的单 bar 范围。
- 手动补算使用 `[start_time, end_time)`；超过 2000 个目标 bar 时自动分 chunk。
- `run-once` 和 `RecalcFactor` 都只执行适用于当前 source、freq、subject 的 enabled binding，
  并按 binding 的 `target_dataset` 分组写回。
- Python 每个结果列必须为每个目标 bar 返回一个有限数值或 `null`。
- `null` 只跳过对应单元格写回，不影响同一行的其他因子列。

实时 Consumer 在事件加入内存 `EventBatcher` 后 ACK。ACK 后若进程退出，尚未计算的
任务可能丢失；这是个人项目为简洁性接受的边界。使用 `run-once` 或同步
`RecalcFactor` 修复缺口。补算期间没有持久化进度；失败后可以直接重跑整个范围，
已完成的字段写回会被覆盖。
