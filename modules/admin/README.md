# moox-admin

MooX 管理入口：统一 HTTP 网关 + 认证、Space、运维等**本地基础服务**。采集规则、云节点、云账户等业务已拆到 `modules/collector` 与 `modules/cloudnode`，admin 仅做网关转发。

## 职责

| 类别 | 内容 |
|------|------|
| 网关 | JWT 鉴权、限流、CORS、`/api/admin/*` 浏览器控制路由与 `/api/gateway-control/*` 节点控制接口 |
| 本进程服务 | Auth、SpaceMgr、Dns、Ssh、SecretMgr、SysDeploy |
| 转发目标 | collectmgr、cloudnode、storage、trade 等独立进程 |
| 定时任务 | DNS 代理/探测、Admin 自身指标上报 |

## 目录结构

```text
cmd/server/               服务入口
cmd/cli/                  模块 CLI（init）
config/
  trpc_go.yaml            tRPC 服务与定时器端口
  gateway.yaml            管理网关 JWT、限流、CORS 与免鉴权配置
  app.yaml                应用级配置
internal/
  bootstrap/              启动编排、TRPC 注册
  gateway/                HTTP 网关（forward / rawhandler）
  service/                本进程业务实现（见下文「本进程服务」）
proto/                    admin/infra/ops/secret/sysdeploy proto
schema/admin.sql          admin 本地 SQLite 表
```

## 构建

```bash
# 模块目录
make build          # → ../../scripts/build.sh admin
make build-linux
make release        # 仓库级发布包

# 仓库根目录
./scripts/build.sh admin
make deploy ARGS="--target localhost --dir ~/moox/dev"
```

admin 单独部署时可排除其他服务：

```bash
make deploy SERVER=user@host   # 等价于 deploy-moox.sh --no-storage --no-web-host --no-cloudnode --no-collector
```

## 端口

| 端口 | 服务 | 网关路径示例 |
|------|------|--------------|
| 11000 | HTTP 网关 | `/api/admin/*` |
| 11100 | Auth | `/api/admin/auth/*` |
| 11101 | Dns | `/api/admin/dnsproxy/*` |
| 11106 | Ssh | `/api/admin/ssh/*`（WebSocket/SFTP 走 rawhandler） |
| 11107 | SpaceMgr | `/api/admin/space/*` |
| 11108 | SecretMgr | `/api/admin/secret/*` |
| 11109 | SysDeploy | `/api/admin/sysdeploy/*` |
| 11401 | moox-cloudnode（转发） | `/api/admin/cloudnode/*` |
| 11402 | moox-collector（转发） | `/api/admin/collectmgr/*` |
| 20200-20202 | moox-storage（转发） | `/api/admin/storage_*/*` |
| 11200-11208、11211-11212 | moox-trade（转发） | `/api/admin/trade_*/*` |
| 11001 | `trpc.moox.api.stdhttp` | 保留 HTTP service，当前不作为主网关入口 |
| 11301 / 11302 / 11305 | 定时器 | dnsproxy / dnsprobe / Admin metrics |

转发映射以 `t_service_deployments` 中的 active 部署记录为准，`config/gateway.yaml` 不再维护服务地址。

### SysDeploy 定位与限制

SysDeploy 是面向 MooX 单机部署的静态服务目录，不是类似 Polaris、Consul 或 etcd 的动态注册中心：

- 每个 `service_name` 只保存一个当前入口地址，适合一台主机上按模块拆分多个进程。
- `active` 表示配置已启用，不等价于服务健康；实际可用性由 Monitor 独立探测。
- 服务不会自行注册或续约，地址变化由部署脚本或管理员显式更新。
- 当前不支持同一服务多实例、实例心跳、自动摘除和负载均衡。
- Admin 网关和 SCF keepalive 都读取同一份 active 部署记录，避免在多个配置文件中重复维护地址。

如果未来需要同服务多实例，应单独引入服务实例模型和明确的负载均衡策略，不在当前表中用重复 `service_name` 模拟。

## 配置与数据

- 主配置：`config/trpc_go.yaml`、`config/gateway.yaml`
- SQLite：`./data/admin.db`（部署目录下为 `<deploy-dir>/data/admin.db`）
- Badger 缓存：`./data/badger`（登录盐值等）
- 日志：`./log/`

开发模式：

```bash
go run ./cmd/server -conf=config/trpc_go.yaml
```

## 本进程服务

`internal/service/` 内实现的 RPC 服务。CollectMgr、CloudNodeMgr 已迁移到独立模块，不在此目录。

| 目录 | RPC 服务 | 说明 |
|------|----------|------|
| `auth/` | `trpc.moox.infra.Auth` | 注册、登录、JWT、改密、用户信息 |
| `space/` | `trpc.moox.admin.SpaceMgr` | Space 元数据管理 |
| `dnsproxy/` | `trpc.moox.infra.Dns` | 交易所 DNS 代理与探测 |
| `ssh/` | `trpc.moox.ops.Ssh` | SSH 主机、会话、WebSocket 终端、SFTP |
| `secret/` | `trpc.moox.ops.SecretMgr` | 密钥/凭证管理 |
| `sysdeploy/` | `trpc.moox.ops.SysDeploy` | 各服务部署信息与网关解析 |
| `database/` | — | 共享 SQLite + GORM 初始化 |

各业务包通常包含 `service.go` / `impl*.go`、`dao/`、`model/`、`rpc/`（部分服务）、`config/`（auth）。注册入口：`internal/bootstrap/trpc.go` → `RegisterTRPCServices`。

主机资源监控由 `moox-host-agent`、EventBus、`moox-monitor` 和 MooX Storage 提供。Admin 只通过统一网关转发 Monitor API，不采集或保存主机指标。

**认证要点**：客户端用「盐值 + 时间戳」派生 AES 密钥加密密码后提交；用户信息存 SQLite，盐值与登录尝试等存 BadgerDB。`/api/admin/auth/Register`、`GetLoginSalt`、`Login` 等路径免 JWT（见 `gateway.yaml` 的 `no_auth_methods`）。API 形态以 `proto/infra_service.proto` 为准。

**扩展新服务**：优先作为独立模块部署，并在 `t_service_deployments` 中登记 serviceID、地址和 tRPC 服务名；只有 admin 本地基础能力才放入 `internal/service/`、`bootstrap/services.go`、`bootstrap/trpc.go` 和 `trpc_go.yaml`。

## 相关文档

- 架构总览：[docs/架构总览.md](../../docs/架构总览.md)
