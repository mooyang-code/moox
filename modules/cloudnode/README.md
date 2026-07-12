# moox-cloudnode

独立云节点服务：云账户、云函数节点、云函数代码包、异步 JobItem 执行队列与 SCF 唤醒/直调能力。协议为 `trpc.moox.cloudnode.CloudNodeMgr`（定义在 `modules/cloudnode/proto`）。

## 职责

- 云账户与节点 catalog 管理。
- 函数代码包两阶段上传（`InitPackageUpload` → COS 直传 → `CompletePackageUpload`）与版本管理。
- 云节点批量创建、批量删除、批量部署到腾讯云 SCF。
- 异步 JobItem 创建、逐条 ACK、JetStream 执行队列、48 小时 active KV 状态、状态回报、取消和 attempt 查询。
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
  jobstate/               NATS JetStream KV active JobItem 状态
  jobhistory/             终态 JobItem 历史日库维护
  projection/             节点心跳批处理
  rpc/                    CloudNodeMgr RPC 实现，按 node/account/package/job_item/invocation 拆分
  repository/             节点、账户、代码包和 invocation 持久化
  providers/tencentscf/  腾讯云 SCF 客户端
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

- CloudNode 主 SQLite：`./data/moox_cloudnode.db`（节点、账号、代码包等控制面数据，以 `config/app.yaml` 为准）
- CloudNode 私有 JetStream：`../data/cloudnode/nats`
- JobItem 终态历史日库：`../data/cloudnode/jobs/YYYYMMDD.db`

通过 `scripts/deploy-moox.sh` 发布时，SQLite 配置会被改写到部署目录的 `../data/cloudnode/moox_cloudnode.db`，JetStream store 位于部署目录的 `data/cloudnode/nats`，JobItem 历史日库位于部署目录的 `data/cloudnode/jobs`。

CloudNode 主 SQLite 只保存控制面数据，不再保存在线 JobItem 状态。服务启动时会强制把 SQLite 连接池限制为 `max_open_conns=1` / `max_idle_conns=1`，让剩余控制面写入和心跳批处理在进程内排队，避免同进程多连接抢 SQLite 文件写锁。

## JobItem 执行队列

CloudNode 使用独立的 JetStream 执行 stream 和 KV bucket，避免与 storage 的数据变更事件混用：

```text
MOOX_CLOUDNODE_EXEC       moox.cloudnode.exec.v1.>     执行消息，负责 ACK/NAK/TERM/重投
MOOX_CLOUDNODE_JOB_ACTIVE JetStream KV bucket          active JobItem 状态，TTL 48 小时
```

`MOOX_CLOUDNODE_EXEC` 是执行队列事实源。SCF 通过 `PollJobItems` 拉取任务，CloudNode 按 `space_id + code_package_id + job_type` 选择 durable consumer，并在 `ReportJobItemStatus` 时 ACK/NAK/TERM。

`MOOX_CLOUDNODE_JOB_ACTIVE` 保存管理台可见的 active 状态、attempt 和取消指令。终态 JobItem 会 best-effort 写入 `data/cloudnode/jobs/YYYYMMDD.db`，用于本机排障和短期审计；管理台当前不查询历史日库。

tRPC 定时器 `cloudnodeJobHistorySchedule` 每天触发一次：先创建未来两天的历史日库，再删除前天、大前天的历史库。SCF 执行日志通过函数 stdout 上报到 CLS，本地不再写执行日志表。

SCF 不直接连接 NATS，也不直接写 CloudNode SQLite。

云账户、COS bucket 和云厂商密钥不写在 `config/app.yaml`，由 CloudAccount 表和相关 RPC 管理；代码包上传流程从已登记云账户读取 COS/SCF 所需配置。

环境变量：

- `MOOX_CLOUDNODE_DB_PATH` — 覆盖 CloudNode SQLite 路径

## 相关文档

- [docs/云节点管理.md](../../docs/云节点管理.md)
- [docs/云节点执行平台架构.md](../../docs/云节点执行平台架构.md)
