# MooX

面向个人的一站式量化平台：行情采集、数据存储、因子计算、策略选股、交易执行和全链路监控，
提供 Web 管理台和 `moox-cli` 命令行两种入口。

MooX 是单用户、自托管系统。唯一的登录用户拥有全部管理能力，不建设 RBAC；
登录、24 小时会话、请求签名、nonce 防重放和密钥加密始终开启。

## 组成

| 模块 | 进程 | 职责 |
| --- | --- | --- |
| [Admin](docs/模块/管理后台.md) | `moox-admin` | 认证、空间、服务目录、密钥、SSH、初始化；管理台 API 唯一入口 |
| [Gateway](docs/模块/节点网关.md) | `moox-host-gateway` | 每台机器一个，把签名的服务请求转发到本机服务 |
| [EventBus](docs/模块/事件总线.md) | `moox-eventbus` | 内嵌 NATS JetStream，模块间的异步事件 |
| [Storage](docs/模块/存储.md) | `moox-storage-{primary,node,view,access}` | 元数据、字段级事实存储、可重建 View、外部访问代理 |
| [Collector](docs/模块/采集.md) | `moox-collector`、`moox-collector-subject`、SCF 函数 | 采集任务、周期批次、云函数调度与对账、标的同步 |
| [CloudNode](docs/模块/云节点.md) | `moox-cloudnode` | 云账户、SCF 节点、代码包与发布 |
| [Factor](docs/模块/因子.md) | `moox-factor-mgr`、`moox-factor-engine` | 因子定义与计算任务、Python 因子增量计算与补算 |
| [Strategy](docs/模块/策略.md) | `moox-strategy` | 声明式策略 DSL，按周期产出目标权重 |
| [Trade](docs/模块/交易.md) | `moox-trade` | 账户、目标收敛下单、订单成交持仓、模拟盘 |
| [Monitor](docs/模块/监控.md) | `moox-monitor` | 健康检查、指标接入、数据新鲜度、告警 |
| [HostAgent](docs/模块/主机代理.md) | `moox-host-agent` | 主机资源采集 |
| [Archive](docs/模块/归档.md) | `moox-archive` | 行变更归档为 Parquet |
| [Web](docs/模块/前端.md) | `moox-web-host` | 管理台前端 |
| [CLI](docs/模块/命令行工具.md) | `moox-cli` | 初始化、部署、数据导入、诊断 |

跨模块共享的协议、事件、鉴权和运行时能力在 `packages/`，见[共享包](docs/模块/共享包.md)。

## 文档

- [总体设计](docs/总体设计.md)：设计原则、系统全景、部署拓扑、核心数据流、事件与安全
- [部署与运维](docs/部署与运维.md)：初始化、发布、证书、凭据、数据保留与日常检查
- [模块设计](docs/README.md)：各模块的详细设计

## 快速开始

```bash
cp moox.toml.example moox.toml && chmod 600 moox.toml   # 填写主机、云账户等
make build                                              # 构建全部二进制到 bin/

./bin/moox-cli setup validate --file ./moox.toml
./bin/moox-cli setup deploy-control --file ./moox.toml
./bin/moox-cli setup apply --file ./moox.toml
./bin/moox-cli setup deploy-storage --file ./moox.toml --host storage
./bin/moox-cli setup init --file ./moox.toml --config-dir ./config/setup --storage-host storage
```

完整流程与注意事项见[部署与运维](docs/部署与运维.md)。

## 开发

```bash
make build      # 构建
make test       # Go 与前端测试
make proto      # 重新生成 Protobuf 代码
make verify     # 提交前的完整检查（CI 同款）
make release    # 生成发布包
```

仓库是 Go Workspace（`go.work`），每个模块、共享包都是独立的 Go module；模块之间只通过
RPC 和事件交互，不跨模块引用 `internal` 代码。
