# modules

MooX 的业务模块。每个模块是独立的 Go module（由仓库根目录 `go.work` 统一管理）和独立进程，
模块之间只通过 RPC（经节点网关）和 EventBus 事件交互，不引用对方的 `internal/` 代码。

| 模块 | 二进制 | 说明 | 设计文档 |
| --- | --- | --- | --- |
| [admin](./admin/) | `moox-admin`、`moox-admin-cli` | 控制面与管理台 API 入口 | [管理后台](../docs/模块/管理后台.md) |
| [gateway](./gateway/) | `moox-host-gateway`、`moox-host-gateway-cli` | 每台机器的节点服务网关 | [节点网关](../docs/模块/节点网关.md) |
| [eventbus](./eventbus/) | `moox-eventbus` | NATS JetStream 事件总线 | [事件总线](../docs/模块/事件总线.md) |
| [storage](./storage/) | `moox-storage-{primary,node,view,access}`、`moox-storage-cli` | 元数据、事实存储、View、外部访问代理 | [存储](../docs/模块/存储.md) |
| [collector](./collector/) | `moox-collector`、`moox-collector-scf`、`moox-collector-cli` | 采集控制面与 SCF 运行时 | [采集](../docs/模块/采集.md) |
| [cloudnode](./cloudnode/) | `moox-cloudnode`、`moox-cloudnode-cli` | 云账户、SCF 节点与代码包 | [云节点](../docs/模块/云节点.md) |
| [factor](./factor/) | `moox-factor-mgr`、`moox-factor-engine`、`moox-factor-mgr-cli` | 因子管理与计算 | [因子](../docs/模块/因子.md) |
| [strategy](./strategy/) | `moox-strategy`、`moox-strategy-cli` | 策略 DSL 与目标权重 | [策略](../docs/模块/策略.md) |
| [trade](./trade/) | `moox-trade`、`moox-trade-cli` | 交易执行与交易事实 | [交易](../docs/模块/交易.md) |
| [monitor](./monitor/) | `moox-monitor`、`moox-monitor-cli` | 健康检查与告警 | [监控](../docs/模块/监控.md) |
| [hostagent](./hostagent/) | `moox-host-agent`、`moox-host-agent-cli` | 主机资源采集 | [主机代理](../docs/模块/主机代理.md) |
| [archive](./archive/) | `moox-archive`、`moox-archive-cli` | 行变更归档为 Parquet | [归档](../docs/模块/归档.md) |
| [cli](./cli/) | `moox-cli` | 运维命令行 | [命令行工具](../docs/模块/命令行工具.md) |

构建单个目标：`./scripts/build/build.sh <target>`（例如 `admin`、`storage`、`collector-scf`、`factor-engine`）；
`make build` 构建全部。部署见[部署与运维](../docs/部署与运维.md)。
