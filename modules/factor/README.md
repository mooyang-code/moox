# moox-factor

因子管理端（`moox-factor-mgr`）与计算引擎（`moox-factor-engine`）：因子定义、计算任务、补算，以及按采集周期运行 Python 因子并写回 Storage。

设计文档：[因子](../../docs/模块/因子.md)

## 构建与测试

```bash
./scripts/build/build.sh factor-mgr
./scripts/build/build.sh factor-engine
scripts/deploy/deploy-factor-engine.sh --help
go test -count=1 ./modules/factor/...
```

## 配置

管理端 `config/app.yaml`、`config/trpc_go.yaml`；引擎 `config/engine.yaml`、`config/trpc_go.engine.yaml`；Python worker 在 `pyworker/`，因子库在 `factors/`。
