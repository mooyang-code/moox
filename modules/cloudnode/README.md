# moox-cloudnode

独立云节点服务：云账户、云函数节点、云函数代码包、异步 JobItem 队列与 SCF 唤醒/直调能力。协议为 `trpc.moox.cloudnode.CloudNodeMgr`（定义在 `modules/cloudnode/proto`）。

## 职责

- 云账户与节点 catalog 管理。
- 函数代码包两阶段上传（`InitPackageUpload` → COS 直传 → `CompletePackageUpload`）与版本管理。
- 云节点批量创建、批量删除、批量部署到腾讯云 SCF。
- 异步 JobItem 创建、逐条 ACK、轮询租约、状态回报、取消和 attempt 查询。
- `InvokeFunction` / `InvokeSync` 作为 SCF 唤醒、直调和调试能力；collector 的正常执行路径使用 JobItem。
- 供 `moox-collector` planner、SCF runtime 和 `moox-cli collector function` 调用。

admin 不实现云节点业务，仅网关转发 `/api/admin/cloudnode/*`。

## 目录结构

```text
cmd/
  moox-cloudnode/         主服务
config/
  app.yaml                数据库、JobItem 队列参数、腾讯云 SCF 默认值
  trpc_go.yaml            CloudNodeMgr :11401
internal/
  bootstrap/              启动与 TRPC 注册
  rpc/                    CloudNodeMgr RPC 实现，按 node/account/package/job_item/invocation 拆分
  repository/             QueueStore、JobItem、节点、账户、代码包和 invocation 持久化
  providers/tencent-scf/  腾讯云 SCF 客户端
  storage/                SQLite 连接
schema/                   cloudnode.sql
../../packages/cloudruntime/ 通用 SCF JobItem runtime 共享逻辑
```

## 构建与运行

```bash
# 仓库根目录
./scripts/build.sh cloudnode

# 模块目录当前没有独立 Makefile，可直接 go build
cd modules/cloudnode
go build -o ../../bin/moox-cloudnode ./cmd/moox-cloudnode

mkdir -p data log
../../bin/moox-cloudnode -conf=config/trpc_go.yaml
```

## 端口与网关

| 端口 | 服务 | 路径 |
|------|------|------|
| 11401 | CloudNodeMgr HTTP | `/api/admin/cloudnode/*`（JWT） |
| — | 同上 | `/api/service/cloudnode/*`（HMAC，SCF/采集运行时） |

## 与 collector 的关系

```text
moox-collector（CollectMgr）
  → 提交 JobItem、唤醒 collector SCF 节点、上传包元数据
moox-cloudnode（CloudNodeMgr）
  → COS + SCF API
moox-collector-scf
  → ReportHeartbeat → PollJobItems → 执行 → ReportJobItemStatus
```

本地直接运行时数据文件默认：`./data/moox_cloudnode.db`（以 `config/app.yaml` 为准）。
通过 `scripts/deploy-moox.sh` 发布时，配置会被改写到部署目录的 `../data/cloudnode/moox_cloudnode.db`。

云账户、COS bucket 和云厂商密钥不写在 `config/app.yaml`，由 CloudAccount 表和相关 RPC 管理；代码包上传流程从已登记云账户读取 COS/SCF 所需配置。

环境变量：

- `MOOX_CLOUDNODE_DB_PATH` — 覆盖 CloudNode SQLite 路径

## 相关文档

- [docs/云节点管理.md](../../docs/云节点管理.md)
- [docs/云节点执行平台架构.md](../../docs/云节点执行平台架构.md)
