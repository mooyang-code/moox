# moox-factor

因子管理端（`moox-factor-mgr`）与计算引擎（`moox-factor-engine`）：因子定义、计算任务、补算，以及按采集周期计算表达式因子并写回 Storage。

设计文档：[因子](../../docs/模块/因子.md)

## 构建与测试

```bash
./scripts/build/build.sh factor-mgr
./scripts/build/build.sh factor-engine
go test -count=1 ./modules/factor/...
```

## 配置

管理端 `config/app.yaml`、`config/trpc_go.yaml`；引擎 `config/engine.yaml`、`config/trpc_go.engine.yaml`；因子库在 `factors/`（YAML 表达式定义），表达式语言在 `packages/factorexpr`。

FactorMgr 监听 loopback 原生 tRPC 11403，管理端的 FactorEngine 服务监听 11405；控制台与 CLI 经主机网关访问。计算引擎是普通调用方：用所在主机的密钥、以 `factor-engine` 的身份签名，从本机网关取目录后直连控制机与存储机的主机网关，访问这两个管理端服务与 Storage，并独立订阅 EventBus。
