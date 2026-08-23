# modules

MooX 后端 Go 模块目录，由仓库根目录 `go.work` 统一管理。各模块通过 `proto/` 生成代码包通信，不直接 import 对方 `internal/` 实现。

## 模块一览

| 模块 | 二进制 | 说明 |
|------|--------|------|
| [admin](./admin/) | `moox-admin` | 中央浏览器控制面、认证、Space、部署和节点路由管理 |
| [gateway](./gateway/) | `moox-gateway` | 每台服务器的 Node Gateway，只代理本机 loopback 服务 |
| [storage](./storage/) | `moox-storage` | 统一数据存储引擎（元数据 + 事实主存 + 派生视图） |
| [collector](./collector/) | `moox-collector`、`moox-collector-scf` | 采集控制面与 SCF 运行时 |
| [cloudnode](./cloudnode/) | `moox-cloudnode` | 云账户、代码包、异步 JobItem、SCF 唤醒/直调 |
| [trade](./trade/) | `moox-trade` | 账户、订单、成交、持仓与交易所适配 |
| [monitor](./monitor/) | `moox-monitor` | 单实例服务/指标事实存储、告警和有界 Doctor Context |
| [eventbus](./eventbus/) | `moox-eventbus` | 统一 NATS JetStream broker、Stream/KV 拓扑与只读管理面 |
| [hostagent](./hostagent/) | `moox-host-agent`、`moox-host-agent-cli` | Linux amd64/arm64 主机 CPU、内存、文件系统、磁盘和网络采集 |
| [cli](./cli/) | `moox-cli` | 命令行工具（元数据/数据导入、Doctor 手工诊断、运维辅助） |
| [factor](./factor/) | `moox-factor` | 因子定义、调度和 Python worker 计算，结果写回 Storage |
| [strategy](./strategy/) | `moox-strategy` | 策略包、实时运行、回测、组合目标和绩效查询 |
| [archive](./archive/) | `moox-archive` | Storage Journal、Parquet 月分区、COS 副本和恢复 |

## 进程与网关关系

浏览器控制面访问中央 `moox-admin` 的 `:11000 /api/admin/*`；机器服务调用访问每台服务器的 `moox-gateway` 的 `:11002 /api/service/*`。Admin 根据 `t_gateway_nodes` 和 `t_service_deployments` 为各节点编译路由快照，Gateway 拉取快照后只转发到本机 loopback 服务。Admin 不直接转发业务模块，也不在静态配置中维护业务服务地址。

```text
:11000  moox-admin 中央浏览器/控制面
  ├─ /api/admin/*             → Admin 本地服务或按部署记录解析的业务服务
  └─ /api/gateway-control/*   → 节点路由快照和状态上报

:11002  每台服务器的 moox-gateway
  └─ /api/service/*           → 本机 active、gateway-enabled 的 loopback 服务

:20100-20202  moox-storage（Metadata / Access / DataView）
:11401        moox-cloudnode
:11402        moox-collector
:11200-11208  moox-trade
:11409/:11410 moox-monitor（/healthz + MonitorMgr）
:11419/:11420 moox-eventbus（/readyz + EventBusMgr）
:11430/:11431 moox-strategy（StrategyMgr + /readyz）
```

SCF 采集运行时通过 `/api/service/*`（HMAC 签名）回调后台，不经 JWT 用户鉴权。

## 构建与发布

有独立 `Makefile` 的模块会代理到仓库根脚本；没有模块级 `Makefile` 时直接使用根脚本：

```bash
# 构建全部
make build

# 构建单个模块；build.sh 每次只接收一个 target
./scripts/build.sh admin
./scripts/build.sh storage
./scripts/build.sh collector
./scripts/build.sh cloudnode
./scripts/build.sh trade
./scripts/build.sh cli
./scripts/build.sh factor
./scripts/build.sh strategy
./scripts/build.sh archive
./scripts/build.sh monitor
./scripts/build.sh eventbus
TARGET_GOOS=linux TARGET_GOARCH=amd64 ./scripts/build.sh hostagent

# 本机/远端一键发布
make deploy ARGS="--target localhost --dir /data/moox/dev"
```

`scripts/deploy-moox.sh` 默认部署 Admin、Gateway、web-host、EventBus、CloudNode、Collector、Factor、Strategy、Trade、Monitor、Storage 和 Archive；可用对应的 `--no-<module>` 关闭。`control` profile 会部署 Strategy 和 Trade；HostAgent 使用独立的 Linux rootless 部署流程。

详细架构见仓库 [`docs/架构总览.md`](../docs/架构总览.md)。
