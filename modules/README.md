# modules

MooX 后端 Go 模块目录，由仓库根目录 `go.work` 统一管理。各模块通过 `proto/` 生成代码包通信，不直接 import 对方 `internal/` 实现。

## 模块一览

| 模块 | 二进制 | 说明 |
|------|--------|------|
| [admin](./admin/) | `moox-admin` | 统一 HTTP 网关 + 认证、Space、运维等本地基础服务 |
| [storage](./storage/) | `moox-storage` | 统一数据存储引擎（元数据 + 事实主存 + 派生视图） |
| [collector](./collector/) | `moox-collector`、`moox-collector-scf` | 采集控制面与 SCF 运行时 |
| [cloudnode](./cloudnode/) | `moox-cloudnode` | 云账户、代码包、异步 JobItem、SCF 下发 |
| [trade](./trade/) | `moox-trade` | 账户、订单、成交、持仓与交易所适配 |
| [cli](./cli/) | `moox-cli` | 命令行工具（元数据导入、数据导入、运维辅助） |
| [factor](./factor/) | `moox-factor` | 因子计算（占位，待扩展） |

## 进程与网关关系

前端与 CLI 通常只访问 `moox-admin` 网关 `:11000`。网关根据 `t_service_deployments` active 部署记录将请求转发到本进程或其他独立服务；`config/gateway.yaml` 不维护服务地址。

```text
:11000  moox-admin 网关
  ├─ /api/admin/auth/*        → admin 本进程 :11100
  ├─ /api/admin/space/*       → admin 本进程 :11107
  ├─ /api/admin/collectmgr/*  → moox-collector :11402
  ├─ /api/admin/cloudnode/*   → moox-cloudnode :11401
  ├─ /api/admin/trade_*/*     → moox-trade :11200-11208
  └─ /api/admin/storage_*/*   → moox-storage :20200-20202

:20100-20202  moox-storage（Metadata / Access / DataView）
:11401        moox-cloudnode
:11402        moox-collector
:11200-11208  moox-trade
```

SCF 采集运行时通过 `/api/service/*`（HMAC 签名）回调后台，不经 JWT 用户鉴权。

## 构建与发布

模块级 `Makefile` 均代理到仓库根脚本：

```bash
# 构建全部或指定模块
make build
./scripts/build.sh admin storage collector cloudnode trade cli

# 本机/远端一键发布
make deploy ARGS="--target localhost --dir ~/moox/dev"
```

详细架构见仓库 [`docs/架构总览.md`](../docs/架构总览.md)。
