# Factor Run-Once Verification

> 以下命令是 `series_tag` 改造完成后的目标验收方式，实施前 CLI 仍使用旧参数。

Storage Gateway、source Dataset 与 Factor target Dataset 需要已经可用。Python 环境需安装
`pyworker/requirements.txt`。下面使用非 K 线 `nav/benchmark_return` 因子；Bias/CCI
只是可选模板。

```bash
cd modules/factor
go run ./cmd/cli init --db ./data/factor/factor.db
go run ./cmd/cli import \
  --db ./data/factor/factor.db \
  --factors-dir ./factors \
  --file ./factors/ExcessReturn.py \
  --factor-id excess-return \
  --input-columns nav,benchmark_return \
  --outputs excess_return,rolling_rank \
  --params-json '{"window":2}' \
  --lookback-periods 2
go run ./cmd/cli run-once \
  --config ./config/app.yaml \
  --space quant \
  --dataset portfolio_nav \
  --subject fund-a \
  --freq 1m \
  --start-time 2026-07-26T00:00:00Z \
  --end-time 2026-07-27T00:00:00Z
```

命令按当前 subject 匹配 enabled binding，并按 binding 的目标 Dataset 分组，同步完成整个
`[start_time,end_time)` 范围。范围查询不设置 `series_tag`，因此同一时间点的全部 tag
会进入 Factor；超过 2000 个不同目标时间点自动分块，且不拆开同一时间点的 tag
cohort。Python 输出用 `data_time + series_tag` 指定 target Dataset 的完整行身份。
terminal JSON 的 `elapsed_ms` 是完整读取、计算和写回耗时。

`--config` 提供 DB、Python、worker、factors、Storage Gateway、task timeout 和 retry。
显式 `--db`、`--factors-dir` 只在调试时覆盖配置。部署包从任意目录运行：

```bash
/absolute/deploy/root/bin/moox-factor-run-once \
  --space quant \
  --dataset portfolio_nav \
  --subject fund-a \
  --freq 1m \
  --start-time 2026-07-26T00:00:00Z \
  --end-time 2026-07-27T00:00:00Z \
  --factors excess-return
```

wrapper 校验部署文件，从部署根解析绝对路径，并只给子进程导出 Factor Gateway 凭证。
计算结果的 `null` 是显式清除：重跑后 Storage 中同一因子列的旧 double 不再可读。
