# 单周期诊断（run-once）

`moox-factor-engine run-once` 按引擎配置读取一个因子集的一个周期、用表达式引擎计算并写回结果，用于排查单个周期。
它与常驻引擎走同一条流水线（读取、空洞检查、计算、组装、写入），并同样记入跳过账本；不消费 EventBus。

前提：引擎的 `config/engine.yaml` 与凭据可用；目录快照 `data/engine/catalog.json` 存在，或管理端可达（启动时会尝试同步一次）。

```bash
cd ~/moox/factor-engine
./bin/moox-factor-engine run-once \
  --config config/engine.yaml \
  --set fset_binance_kline_1m \
  --period 2026-10-17T08:00:00Z \
  --factor Bias --factor Cci \
  --subject BTC-USDT
```

- `--factor`、`--subject` 可以重复，不写时分别取集合的全部启用成员与全部标的。
- 结果写入集合的结果数据集；输出里的 `null` 是显式清除，会覆盖该键已有的值。
