# moox-collector

独立采集控制面与 SCF 运行时模块。

## 职责边界

| 组件 | 职责 |
|------|------|
| `moox-collector` | 采集规则、任务实例、任务规划、状态上报（CollectMgr RPC） |
| `moox-collector-scf` | 腾讯云 SCF 入口，执行 CloudNode 下发的采集 JobItem |
| `modules/cloudnode` | 云账户、代码包、异步 JobItem、SCF 唤醒/直调 |
| `modules/admin` | 网关转发 `/api/admin/collectmgr/*`，不承载采集业务表 |

## 目录结构

```text
cmd/
  server/                 独立采集管理服务
  cli/                    模块 CLI（init）
  scf/                    SCF 运行时
config/                   moox-collector 服务配置
configs/                  SCF 运行时本地默认配置
schema/                   collector SQLite schema
internal/
  app/control/            独立服务启动、配置、数据库和依赖发现
  app/runtime/            SCF runtime 配置、后台服务鉴权和 URL helper
  app/runtimeboot/        SCF runtime 启动装配与定时器注册
  domain/                 采集业务领域模型
  repository/             Collector SQLite 持久化
  rpc/                    CollectMgr RPC 实现
  jobs/                   JobItem job_type 与任务 payload 定义
  executor/               采集任务即时执行编排
  planner/                规则 + dataset subjects → 任务实例
  taskrunner/             CloudNode JobItem 到采集任务的 poll/execute 适配
  serverless/             SCF runtime 事件入口
  sources/                采集器注册、交易所客户端与执行
  planner/storagesource/  从 moox-storage metadata tRPC 加载规划输入
  taskpublisher/          调用 moox-cloudnode 提交 JobItem
  reporter/               任务状态与心跳上报
  model/                  SCF 运行时事件、心跳和采集结果模型
  httpclient/             Collector 内部 HTTP 客户端与 DNS 优选
```

## 构建

```bash
make build              # moox-collector
make build-linux
make build-scf          # SCF 运行时
make package-scf        # 打包 SCF 部署 zip

# 或仓库根目录
./scripts/build.sh collector
./scripts/build.sh collector-scf
./scripts/build-collector-scf-package.sh
```

## 端口与网关

- CollectMgr HTTP：`:11402`（`config/trpc_go.yaml`）
- 管理台路径：`/api/admin/collectmgr/{Method}`（admin 网关 JWT）
- CloudNode JobItem 提交：`/api/service/cloudnode/SubmitJobItems`（Collector 控制面，经 admin 网关 HMAC 鉴权）
- SCF 唤醒：`/api/service/cloudnode/InvokeFunction`（Collector 控制面用 keepalive event 唤醒节点去 poll）
- CloudNode JobItem 运行时：`/api/service/cloudnode/PollJobItems`、`/api/service/cloudnode/ReportJobItemStatus`
- 采集任务实例状态：`/api/service/collectmgr/ReportTaskStatus`（HMAC，经网关）

## 部署关系

典型链路：

```text
admin 网关
  → moox-collector（规划任务）
  → moox-admin `/api/service/cloudnode/SubmitJobItems`（HMAC 鉴权）
  → moox-cloudnode（提交 JobItem / 唤醒 SCF）
  → moox-collector-scf（PollJobItems 后执行采集）
  → moox-storage Access（写入 K 线等）
  → 回报 cloudnode ReportJobItemStatus + collectmgr ReportTaskStatus
```

`moox-collector` 通常与 `moox-cloudnode` 同机或同发布包部署。协议定义见 `modules/collector/proto/`。

本地直接运行时数据文件默认：`./data/moox_collector.db`（以 `config/app.yaml` 为准）。
通过 `scripts/deploy-moox.sh` 发布时，配置会被改写到部署目录的 `../data/collector/moox_collector.db`。

环境变量：

- `MOOX_COLLECTOR_DB_PATH` — 覆盖 Collector SQLite 路径
- `MOOX_COLLECTOR_ADMIN_GATEWAY_URL` — 配置后从 Admin SysDeploy active 部署记录解析 CloudNode/Storage 依赖
- `MOOX_COLLECTOR_STORAGE_METADATA_TARGET` / `MOOX_COLLECTOR_STORAGE_ACCESS_TARGET` — 覆盖 storage tRPC 直连 target
- `MOOX_SERVICE_AUTH_ACCESS_KEY` / `MOOX_SERVICE_AUTH_SECRET_KEY` — Collector 调用 `/api/service/sysdeploy/*` 时使用的后台签名密钥

## SCF runtime 配置

`configs/config.yaml` 是 `moox-collector-scf` 打包进代码包的默认配置。控制面唤醒 SCF 时会在 keepalive event 中下发真实的 `service_gateway_target`、`storage_metadata_target`、`storage_access_target` 等地址；本地默认值只用于开发调试。

关键字段：

- `system.storage_metadata_target` / `system.storage_access_target` — Storage tRPC 直连地址。
- `system.service_auth` — SCF 调 `/api/service/*` 所需 HMAC 签名配置。
- `sources.market` — 运行时加载的数据源采集器配置，例如 Binance。

## 相关文档

- [docs/采集任务管理.md](../../docs/采集任务管理.md)
- [docs/云节点管理.md](../../docs/云节点管理.md)
