# modules

MooX 的业务模块。每个模块是独立的 Go module（由仓库根目录 `go.work` 统一管理），可以包含一个或多个进程，
模块之间只通过 RPC（经主机网关）和 EventBus 事件交互，不引用对方的 `internal/` 代码。

| 模块 | 二进制 | 说明 | 设计文档 |
| --- | --- | --- | --- |
| [admin](./admin/) | `moox-admin`、`moox-admin-cli` | 控制面与浏览器入口（HTTPS 9527、内嵌管理台页面） | [管理后台](../docs/模块/管理后台.md) |
| [hostgateway](./hostgateway/) | `moox-host-gateway`、`moox-host-gateway-cli` | 每台机器的主机网关 | [主机网关](../docs/模块/主机网关.md) |
| [eventbus](./eventbus/) | `moox-eventbus` | NATS JetStream 事件总线 | [事件总线](../docs/模块/事件总线.md) |
| [storage](./storage/) | `moox-storage-{primary,view}`、`moox-storage-cli` | 元数据、事实存储与 View | [存储](../docs/模块/存储.md) |
| [egressproxy](./egressproxy/) | `moox-egress-proxy` | 受限 HTTPS 出口与 DNS 解析 | [出口代理](../docs/模块/出口代理.md) |
| [collector](./collector/) | `moox-collector`、`moox-collector-stream`、`moox-collector-scf`、`moox-collector-cli` | 采集控制面（含云账号、代码包与云函数管理）、WS 执行端与 SCF Timer 运行时 | [采集](../docs/模块/采集.md) |
| [callbackdispatcher](./callbackdispatcher/) | `moox-callback-dispatcher` | 事件中心：按规则向用户接口发通知或执行系统动作 | [事件中心](../docs/模块/事件中心.md) |
| [factor](./factor/) | `moox-factor-mgr`、`moox-factor-engine` | 因子管理与计算 | [因子](../docs/模块/因子.md) |
| [strategy](./strategy/) | `moox-strategy`、`moox-strategy-cli` | 策略 DSL 与目标权重 | [策略](../docs/模块/策略.md) |
| [trade](./trade/) | `moox-trade` | 交易执行与交易事实 | [交易](../docs/模块/交易.md) |
| [monitor](./monitor/) | `moox-monitor` | 健康检查与告警 | [监控](../docs/模块/监控.md) |
| [hostagent](./hostagent/) | `moox-host-agent`、`moox-host-agent-cli` | 主机资源采集 | [主机代理](../docs/模块/主机代理.md) |
| [archive](./archive/) | `moox-archive-mgr`、`moox-archive-worker` | 定时拉取、SQLite 暂存与 CSV/CSV.GZ 归档 | [归档](../docs/模块/归档.md) |
| [cli](./cli/) | `moox-cli` | 运维命令行与部署 | [命令行工具](../docs/模块/命令行工具.md) |

构建单个目标：`./scripts/build/build.sh <target>`（例如 `admin`、`storage`、`collector-scf`、`factor-engine`）；
`make build` 构建脚本的 `all` 目标，SCF 包和 `archive-worker` 需另选目标；HostAgent 仅在 Linux 构建。
完整条件见[构建手册](../docs/构建手册.md)，部署见[部署与运维](../docs/部署与运维.md)。
