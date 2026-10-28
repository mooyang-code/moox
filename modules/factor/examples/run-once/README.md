# 单周期诊断（run-once）

`moox-factor-engine run-once` 按引擎配置读取一个因子任务的一个周期、用表达式引擎计算并写回结果，用于排查单个周期。
它复用补算流水线（读取、计算、组装、写入），并同样记入跳过账本；不消费 EventBus，也不发布实时周期完成标记。诊断进程直接读 Storage 窗口，不打开常驻引擎使用的本地 Pebble。

前提：引擎的 `config/engine.yaml` 与凭据可用；目录快照 `data/engine/catalog.json` 存在，或管理端可达（启动时会尝试同步一次）。先暂停目标任务，避免独立诊断进程与实时计算或补算同时写同一期；此命令没有常驻引擎的任务串行锁。

```bash
cd ~/moox/factor-engine
./bin/moox-factor-engine run-once \
  --config config/engine.yaml \
  --task ftask_binance_kline_1m \
  --period 2026-10-17T08:00:00Z \
  --member bias --member cci \
  --subject BTC-USDT
```

- `--member`、`--subject` 可以重复，不写时分别取任务的全部启用成员与全部标的。成员参数优先取该期已有行的 `__params`，缺失时用成员自身配置。
- 结果写入任务的结果数据集；输出里的 `null` 是显式清除，会覆盖该键已有的值。
