# moox-cloudnode

独立云节点服务：云账户、云函数代码包、异步 work_item 队列与 SCF 同步/异步调用。协议为 `trpc.moox.cloudnode.CloudNodeMgr`（定义在 `modules/cloudnode/proto`）。

## 职责

- 云账户与节点 catalog 管理
- 函数代码包上传（COS）与版本管理
- 异步 work_item 创建、轮询、状态更新
- 向腾讯云 SCF 下发 invocation
- 供 `moox-collector` planner 与 `moox-cli collector function` 调用

admin 不实现云节点业务，仅网关转发 `/api/admin/cloudnode/*`。

## 目录结构

```text
cmd/
  moox-cloudnode/         主服务
config/
  app.yaml                数据库、COS、腾讯云凭证
  trpc_go.yaml            CloudNodeMgr :11401
internal/
  bootstrap/              启动与 TRPC 注册
  rpc/                    CloudNodeMgr RPC 实现，按 node/account/package/workitem/invocation 拆分
  repository/             异步 work_item、节点、账户、代码包和 invocation 持久化
  providers/tencent-scf/  腾讯云 SCF 客户端
  storage/                SQLite 连接
schema/                   cloudnode.sql
../../packages/cloudruntime/ 通用 SCF work_item runtime 共享逻辑
```

## 构建与运行

```bash
./scripts/build.sh cloudnode

mkdir -p data log
./bin/moox-cloudnode -conf=config/trpc_go.yaml
```

模块 Makefile（若有）代理到仓库根 `scripts/build.sh cloudnode`。

## 端口与网关

| 端口 | 服务 | 路径 |
|------|------|------|
| 11401 | CloudNodeMgr HTTP | `/api/admin/cloudnode/*`（JWT） |
| — | 同上 | `/api/service/cloudnode/*`（HMAC，SCF/采集运行时） |

## 与 collector 的关系

```text
moox-collector（CollectMgr）
  → 创建/查询 work_item、上传包元数据
moox-cloudnode（CloudNodeMgr）
  → COS + SCF API
moox-collector-scf
  → 执行 work_item，ReportHeartbeat → /api/service/cloudnode/*
```

本地直接运行时数据文件默认：`./data/moox_cloudnode.db`（以 `config/app.yaml` 为准）。
通过 `scripts/deploy-moox.sh` 发布时，配置会被改写到部署目录的 `../data/cloudnode/moox_cloudnode.db`。

环境变量：

- `MOOX_CLOUDNODE_DB_PATH` — 覆盖 CloudNode SQLite 路径

## 相关文档

- [docs/云节点管理.md](../../docs/云节点管理.md)
- [docs/云节点执行平台架构.md](../../docs/云节点执行平台架构.md)
