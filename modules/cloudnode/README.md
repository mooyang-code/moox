# moox-cloudnode

独立云节点服务：云账户、云函数节点、云函数代码包、异步 JobItem 执行队列与 SCF 唤醒/直调能力。协议为 `trpc.moox.cloudnode.CloudNodeMgr`（定义在 `modules/cloudnode/proto`）。

## 职责

- 云账户与节点 catalog 管理。
- 函数代码包两阶段上传（`InitPackageUpload` → COS 直传 → `CompletePackageUpload`）与版本管理。
- 云节点批量创建、批量删除、批量部署到腾讯云 SCF。
- 异步 JobItem 创建、逐条 ACK、JetStream 执行队列、SQLite 查询投影、状态回报、取消和 attempt 查询。
- `InvokeFunction` / `InvokeSync` 作为 SCF 唤醒、直调和调试能力；collector 的正常执行路径使用 JobItem。
- 供 `moox-collector` planner、SCF runtime 和 `moox-cli collector function` 调用。

admin 不实现云节点业务，仅网关转发 `/api/admin/cloudnode/*`。

## 目录结构

```text
cmd/
  server/                 主服务
  cli/                    模块 CLI（init）
config/
  app.yaml                数据库、JobItem 队列参数、腾讯云 SCF 默认值
  trpc_go.yaml            CloudNodeMgr :11401
internal/
  bootstrap/              启动与 TRPC 注册
  jobqueue/               CloudNode 私有 NATS JetStream 执行队列
  projection/             SQLite JobItem 投影、attempt、心跳批处理和投影 worker
  rpc/                    CloudNodeMgr RPC 实现，按 node/account/package/job_item/invocation 拆分
  repository/             节点、账户、代码包和 invocation 持久化；旧 SQLite 队列实现仅作兜底
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
go build -o ../../bin/moox-cloudnode ./cmd/server
go build -o ../../bin/moox-cloudnode-cli ./cmd/cli

mkdir -p data log
../../bin/moox-cloudnode-cli init --db-path ./data/moox_cloudnode.db
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

本地直接运行时数据文件默认：

- SQLite 投影：`./data/moox_cloudnode.db`（以 `config/app.yaml` 为准）
- CloudNode 私有 JetStream：`../data/cloudnode/nats`

通过 `scripts/deploy-moox.sh` 发布时，SQLite 配置会被改写到部署目录的 `../data/cloudnode/moox_cloudnode.db`，JetStream store 位于部署目录的 `data/cloudnode/nats`。

CloudNode 的 SQLite 只作为管理台查询投影和控制面存储使用。服务启动时会强制把 SQLite 连接池限制为 `max_open_conns=1` / `max_idle_conns=1`，让高并发 SCF poll、心跳批处理和投影写入在进程内排队，避免同进程多连接抢 SQLite 文件写锁。

## JobItem 执行队列

CloudNode 使用两条独立的 JetStream stream，避免与 storage 的数据变更事件混用：

```text
MOOX_CLOUDNODE_EXEC       moox.cloudnode.exec.v1.>
MOOX_CLOUDNODE_PROJECTION moox.cloudnode.projection.v1.>
```

`MOOX_CLOUDNODE_EXEC` 是执行队列事实源。SCF 通过 `PollJobItems` 拉取任务，CloudNode 按 `space_id + code_package_id + job_type` 选择 durable consumer，并在 `ReportJobItemStatus` 时 ACK/NAK/TERM。

`MOOX_CLOUDNODE_PROJECTION` 承载状态投影事件。CloudNode 后台 projector 批量写 SQLite，管理台的任务列表、attempt 查询和取消状态都读 SQLite 投影。

SCF 不直接连接 NATS，也不直接写 CloudNode SQLite。

云账户、COS bucket 和云厂商密钥不写在 `config/app.yaml`，由 CloudAccount 表和相关 RPC 管理；代码包上传流程从已登记云账户读取 COS/SCF 所需配置。

环境变量：

- `MOOX_CLOUDNODE_DB_PATH` — 覆盖 CloudNode SQLite 路径

## 相关文档

- [docs/云节点管理.md](../../docs/云节点管理.md)
- [docs/云节点执行平台架构.md](../../docs/云节点执行平台架构.md)
