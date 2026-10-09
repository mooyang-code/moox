# moox-collector

采集控制面与 SCF 运行时：采集任务、周期批次规划、Timer 领取与对账、失败重试、标的同步和 K 线重采样。

设计文档：[采集](../../docs/模块/采集.md)

## 构建与测试

```bash
./scripts/build/build.sh collector
./scripts/build/build.sh collector-scf
./scripts/build/build-collector-scf-package.sh
go test -count=1 ./modules/collector/...
```

## 配置

`config/trpc_go.yaml`、`config/app.yaml`、`config/markets/`；schema 在 `schema/collector.sql`。行情源参考见 [docs/行情接口目录.md](docs/行情接口目录.md)。
